// Package media runs the WebRTC live session against an Anona camera: build an
// offer, strip extmap, zlib-compress it, send WEB_RTC_START over the control
// channel, apply the compressed answer, trickle candidates, and surface the
// camera's RTP tracks. See docs/DESIGN.md §2.4.
package media

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"

	"github.com/Darkhand81/anona-ha-bridge/internal/cloud"
	"github.com/Darkhand81/anona-ha-bridge/internal/control"
	"github.com/Darkhand81/anona-ha-bridge/internal/pb"
)

var extmapRE = regexp.MustCompile(`(?m)^a=extmap:.*\r?\n`)

func deflate(s string) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func inflate(b []byte) (string, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	return string(out), err
}

// TrackSink receives RTP for one media track.
type TrackSink interface {
	OnVideo(codec webrtc.RTPCodecParameters, pkt *rtp.Packet)
	OnAudio(codec webrtc.RTPCodecParameters, pkt *rtp.Packet)
}

type Session struct {
	ch         *control.Channel
	deviceID   string
	clientID   string
	pc         *webrtc.PeerConnection
	sink       TrackSink
	localAudio *webrtc.TrackLocalStaticSample
	talking    atomic.Bool
}

func newAPI() (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	// Offer H.264 and H.265 for video, Opus for audio — the camera answers with one.
	for _, c := range []struct {
		mime string
		pt   webrtc.PayloadType
		fmtp string
	}{
		{webrtc.MimeTypeH264, 102, "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"},
		{webrtc.MimeTypeH265, 98, ""},
	} {
		if err := m.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: c.mime, ClockRate: 90000, SDPFmtpLine: c.fmtp},
			PayloadType:        c.pt,
		}, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m)), nil
}

func iceServers(ice cloud.ICE) []webrtc.ICEServer {
	var s []webrtc.ICEServer
	if ice.Stun != "" {
		s = append(s, webrtc.ICEServer{URLs: []string{ice.Stun}})
	}
	for _, t := range ice.Turns {
		s = append(s, webrtc.ICEServer{URLs: []string{t.Address}, Username: t.Username, Credential: t.Password})
	}
	return s
}

// Start negotiates the live session and begins delivering RTP to sink.
func Start(cl *cloud.Client, ch *control.Channel, deviceID, clientID string, ice cloud.ICE, sink TrackSink) (*Session, error) {
	api, err := newAPI()
	if err != nil {
		return nil, err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers:   iceServers(ice),
		BundlePolicy: webrtc.BundlePolicyMaxBundle,
		RTCPMuxPolicy: webrtc.RTCPMuxPolicyRequire,
	})
	if err != nil {
		return nil, err
	}
	s := &Session{ch: ch, deviceID: deviceID, clientID: clientID, pc: pc, sink: sink}

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return nil, err
	}
	// Audio is sendrecv: we receive the camera mic and can send talkback.
	localAudio, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"KvsAudioTrack", "KvsLocalMediaStream")
	if err != nil {
		return nil, err
	}
	if _, err := pc.AddTransceiverFromTrack(localAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv}); err != nil {
		return nil, err
	}
	s.localAudio = localAudio

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		ci := c.ToJSON()
		mid := ""
		if ci.SDPMid != nil {
			mid = *ci.SDPMid
		}
		idx := "0"
		if ci.SDPMLineIndex != nil {
			idx = fmt.Sprint(*ci.SDPMLineIndex)
		}
		pkt := pb.WebRtcCandidate(s.clientID, ci.Candidate, mid, idx)
		if _, err := ch.Send(deviceID, pkt, 8*time.Second); err != nil {
			// candidates are best-effort; log-worthy but not fatal
		}
	})

	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		codec := tr.Codec()
		isVideo := strings.HasPrefix(strings.ToLower(codec.RTPCodecCapability.MimeType), "video")
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if isVideo {
				s.sink.OnVideo(codec, pkt)
			} else {
				s.sink.OnAudio(codec, pkt)
			}
		}
	})

	connected := make(chan struct{}, 1)
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		fmt.Println("[webrtc] state:", st)
		if st == webrtc.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})

	// Build offer, strip extmap, compress, send WEB_RTC_START.
	offer, err := pc.CreateOffer(&webrtc.OfferOptions{})
	if err != nil {
		return nil, err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, err
	}
	stripped := extmapRE.ReplaceAllString(pc.LocalDescription().SDP, "")
	var turns []pb.TurnServer
	for _, t := range ice.Turns {
		turns = append(turns, pb.TurnServer{Address: t.Address, Username: t.Username, Password: t.Password})
	}
	startPkt := pb.WebRtcStart(clientID, deflate(stripped), ice.Stun, turns, true)
	reply, err := ch.Send(deviceID, startPkt, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("WEB_RTC_START: %w", err)
	}
	answerSDP, err := parseAnswer(reply)
	if err != nil {
		return nil, err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}); err != nil {
		return nil, fmt.Errorf("set answer: %w", err)
	}

	ch.OnPush(s.onPush)

	// Tell the camera to start the live stream once negotiated.
	go func() {
		select {
		case <-connected:
		case <-time.After(15 * time.Second):
			fmt.Println("[webrtc] not connected before livestream start")
		}
		if _, err := ch.Send(deviceID, pb.P2PLiveStreamStart(clientID), 15*time.Second); err != nil {
			fmt.Println("[webrtc] P2P_LIVE_STREAM_START:", err)
		} else {
			fmt.Println("[webrtc] live stream started")
		}
	}()

	return s, nil
}

// parseAnswer extracts and inflates webrtc_start_response.sdp_answer_compressed.
func parseAnswer(packet []byte) (string, error) {
	top, ok := pb.Decode(packet)
	if !ok {
		return "", fmt.Errorf("answer: bad packet")
	}
	if ec := pb.Get(top, pb.FErrorCode); ec != nil && ec.V != 0 {
		return "", fmt.Errorf("answer: error_code %d", ec.V)
	}
	ipc := pb.Get(top, pb.FIPCamera)
	if ipc == nil || ipc.Sub == nil {
		return "", fmt.Errorf("answer: no ipcamera")
	}
	resp := pb.Get(ipc.Sub, pb.FWebRtcStartResp)
	if resp == nil || resp.Sub == nil {
		return "", fmt.Errorf("answer: no webrtc_start_response")
	}
	if ec := pb.Get(resp.Sub, 1); ec != nil && ec.V != 0 {
		return "", fmt.Errorf("answer: response error_code %d", ec.V)
	}
	comp := pb.Get(resp.Sub, 2) // sdp_answer_compressed
	if comp == nil {
		return "", fmt.Errorf("answer: no sdp_answer_compressed")
	}
	return inflate(comp.B)
}

func (s *Session) onPush(push control.Push) {
	if push.DeviceID != "" && push.DeviceID != s.deviceID {
		return
	}
	top, ok := pb.Decode(push.Packet)
	if !ok {
		return
	}
	ipc := pb.Get(top, pb.FIPCamera)
	if ipc == nil || ipc.Sub == nil {
		return
	}
	cand := pb.Get(ipc.Sub, 72) // webrtc_candidate
	if cand == nil || cand.Sub == nil {
		return
	}
	candidate := string(getBytes(cand.Sub, 3))
	mid := string(getBytes(cand.Sub, 4))
	if candidate == "" {
		return
	}
	_ = s.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidate, SDPMid: strPtr(mid)})
}

func getBytes(fs []pb.Field, num int) []byte {
	if f := pb.Get(fs, num); f != nil {
		return f.B
	}
	return nil
}
func strPtr(s string) *string { return &s }

// Talk streams Ogg/Opus audio (48 kHz) to the camera speaker: it gates with
// P2P_VOICE_START, writes Opus pages paced by their granule position, then
// P2P_VOICE_STOP. The input must be an Ogg container with an Opus stream
// (e.g. `ffmpeg -i in.wav -c:a libopus -f ogg out.ogg`).
func (s *Session) Talk(oggOpus io.Reader) error {
	if !s.talking.CompareAndSwap(false, true) {
		return fmt.Errorf("talkback already active")
	}
	defer s.talking.Store(false)

	reply, err := s.ch.Send(s.deviceID, pb.P2PVoiceStart(s.clientID), 15*time.Second)
	if err != nil {
		return fmt.Errorf("P2P_VOICE_START: %w", err)
	}
	if ec := voiceStartError(reply); ec != 0 {
		return fmt.Errorf("P2P_VOICE_START error_code %d (8=BUSY_IN_VOICE_TALK)", ec)
	}
	fmt.Println("[talk] P2P_VOICE_START accepted (NO_ERROR)")
	defer func() { _, _ = s.ch.Send(s.deviceID, pb.P2PVoiceStop(s.clientID), 10*time.Second) }()

	ogg, _, err := oggreader.NewWith(oggOpus)
	if err != nil {
		return fmt.Errorf("ogg: %w", err)
	}
	var lastGranule uint64
	var sent int
	for {
		page, hdr, err := ogg.ParseNextPage()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ogg page: %w", err)
		}
		samples := hdr.GranulePosition - lastGranule
		lastGranule = hdr.GranulePosition
		dur := time.Duration(float64(samples) / 48000.0 * float64(time.Second))
		if dur <= 0 {
			dur = 20 * time.Millisecond
		}
		if err := s.localAudio.WriteSample(media.Sample{Data: page, Duration: dur}); err != nil {
			return fmt.Errorf("write sample: %w", err)
		}
		sent++
		time.Sleep(dur)
	}
	fmt.Printf("[talk] sent %d opus pages\n", sent)
	return nil
}

func (s *Session) Close() error { return s.pc.Close() }

// voiceStartError returns the error_code from a P2P_VOICE_START reply
// (top-level error_code, or ipcamera.p2p_voice_start_response(121).error_code(1)).
func voiceStartError(packet []byte) uint64 {
	top, ok := pb.Decode(packet)
	if !ok {
		return 0
	}
	if ec := pb.Get(top, pb.FErrorCode); ec != nil {
		return ec.V
	}
	if ipc := pb.Get(top, pb.FIPCamera); ipc != nil && ipc.Sub != nil {
		if resp := pb.Get(ipc.Sub, 121); resp != nil && resp.Sub != nil {
			if ec := pb.Get(resp.Sub, 1); ec != nil {
				return ec.V
			}
		}
	}
	return 0
}

// NullSink discards received media (for talk-only sessions).
type NullSink struct{}

func (NullSink) OnVideo(webrtc.RTPCodecParameters, *rtp.Packet) {}
func (NullSink) OnAudio(webrtc.RTPCodecParameters, *rtp.Packet) {}
