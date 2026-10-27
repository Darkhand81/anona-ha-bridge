package media

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/auth"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const rtspRealm = "anona-bridge"

// Server republishes one or more cameras' WebRTC RTP as RTSP for readers such as
// go2rtc. A single listener serves many cameras, each on its own path; the
// handler routes DESCRIBE/SETUP by path to the matching stream. Register a
// camera with Publisher(path), which returns a TrackSink for that path.
type Server struct {
	srv     *gortsplib.Server
	mu      sync.Mutex
	streams map[string]*rtspStream

	// Optional credentials. When user is empty the server is open (default).
	user, pass, nonce string
}

// NewRTSPServer starts the RTSP listener. If user is non-empty, readers must
// authenticate (basic or digest) with user/pass; otherwise the server is open.
func NewRTSPServer(listen, user, pass string) (*Server, error) {
	s := &Server{streams: map[string]*rtspStream{}, user: user, pass: pass}
	if user != "" {
		n, err := auth.GenerateNonce()
		if err != nil {
			return nil, err
		}
		s.nonce = n
	}
	s.srv = &gortsplib.Server{Handler: s, RTSPAddress: listen}
	if err := s.srv.Start(); err != nil {
		return nil, err
	}
	authNote := "open (no auth)"
	if user != "" {
		authNote = "auth required"
	}
	fmt.Printf("[rtsp] listening on rtsp://%s/<path> (%s)\n", listen, authNote)
	return s, nil
}

// authFail returns a 401 response when credentials are required and the request
// fails validation, or nil when the request is allowed (including when no
// credentials are configured).
func (s *Server) authFail(req *base.Request) *base.Response {
	if s.user == "" {
		return nil
	}
	if err := auth.Validate(req, s.user, s.pass, nil, rtspRealm, s.nonce); err != nil {
		return &base.Response{
			StatusCode: base.StatusUnauthorized,
			Header: base.Header{
				"WWW-Authenticate": auth.GenerateWWWAuthenticate(nil, rtspRealm, s.nonce),
			},
		}
	}
	return nil
}

func normPath(p string) string { return strings.Trim(p, "/") }

// Publisher registers (or returns the existing) stream at path and hands back a
// TrackSink to feed it. go2rtc then reads rtsp://<host>:<listen>/<path>.
func (s *Server) Publisher(path string) *rtspStream {
	path = normPath(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.streams[path]; ok {
		return st
	}
	st := &rtspStream{path: path, srv: s.srv}
	s.streams[path] = st
	fmt.Printf("[rtsp] publisher path=/%s\n", path)
	return st
}

func (s *Server) lookup(path string) *rtspStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[normPath(path)]
}

func (s *Server) Close() {
	s.mu.Lock()
	for _, st := range s.streams {
		st.close()
	}
	s.mu.Unlock()
	s.srv.Close()
}

// --- gortsplib.ServerHandler (routes by path) ---

func (s *Server) OnConnOpen(*gortsplib.ServerHandlerOnConnOpenCtx)       {}
func (s *Server) OnSessionOpen(*gortsplib.ServerHandlerOnSessionOpenCtx) {}

func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if resp := s.authFail(ctx.Request); resp != nil {
		return resp, nil, nil
	}
	st := s.lookup(ctx.Path)
	if st == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return st.describe()
}

func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if resp := s.authFail(ctx.Request); resp != nil {
		return resp, nil, nil
	}
	st := s.lookup(ctx.Path)
	if st == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return st.setup()
}

func (s *Server) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// rtspStream holds the published media for a single camera path. It implements
// media.TrackSink: the stream is created once the negotiated codecs are known
// (both video and audio, or after a short grace with whatever is present).
type rtspStream struct {
	path string
	srv  *gortsplib.Server

	mu           sync.Mutex
	stream       *gortsplib.ServerStream
	vmedia       *description.Media
	amedia       *description.Media
	vpt, apt     uint8
	vcodec       *webrtc.RTPCodecParameters
	acodec       *webrtc.RTPCodecParameters
	built        bool
	timerStarted bool
}

func (r *rtspStream) describe() (*base.Response, *gortsplib.ServerStream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, r.stream, nil
}

func (r *rtspStream) setup() (*base.Response, *gortsplib.ServerStream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, r.stream, nil
}

// note records a codec and triggers (or completes) stream creation. Lock held.
func (r *rtspStream) note(codec webrtc.RTPCodecParameters, isVideo bool) {
	if isVideo && r.vcodec == nil {
		c := codec
		r.vcodec = &c
		r.vpt = uint8(codec.PayloadType)
	}
	if !isVideo && r.acodec == nil {
		c := codec
		r.acodec = &c
		r.apt = uint8(codec.PayloadType)
	}
	if r.built {
		return
	}
	if !r.timerStarted {
		r.timerStarted = true
		go func() {
			time.Sleep(2500 * time.Millisecond)
			r.mu.Lock()
			r.build()
			r.mu.Unlock()
		}()
	}
	if r.vcodec != nil && r.acodec != nil {
		r.build()
	}
}

// build creates the RTSP stream from whatever codecs are known. Lock held.
func (r *rtspStream) build() {
	if r.built || r.vcodec == nil {
		return
	}
	mime := strings.ToLower(r.vcodec.RTPCodecCapability.MimeType)
	var vf format.Format
	switch {
	case strings.Contains(mime, "h265") || strings.Contains(mime, "hevc"):
		vf = &format.H265{PayloadTyp: r.vpt}
	case strings.Contains(mime, "h264"):
		vf = &format.H264{PayloadTyp: r.vpt, PacketizationMode: 1}
	default:
		return
	}
	r.vmedia = &description.Media{Type: description.MediaTypeVideo, Formats: []format.Format{vf}}
	medias := []*description.Media{r.vmedia}
	if r.acodec != nil {
		ch := r.acodec.RTPCodecCapability.Channels
		if ch == 0 {
			ch = 2
		}
		af := &format.Opus{PayloadTyp: r.apt, ChannelCount: int(ch)}
		r.amedia = &description.Media{Type: description.MediaTypeAudio, Formats: []format.Format{af}}
		medias = append(medias, r.amedia)
	}
	r.stream = gortsplib.NewServerStream(r.srv, &description.Session{Medias: medias})
	r.built = true
	audio := "none"
	if r.amedia != nil {
		audio = fmt.Sprintf("opus pt=%d", r.apt)
	}
	fmt.Printf("[rtsp] /%s stream ready: video %s pt=%d, audio %s\n", r.path, r.vcodec.RTPCodecCapability.MimeType, r.vpt, audio)
}

func (r *rtspStream) OnVideo(codec webrtc.RTPCodecParameters, pkt *rtp.Packet) {
	r.mu.Lock()
	r.note(codec, true)
	stream, medi, pt, ok := r.stream, r.vmedia, r.vpt, r.built && r.vmedia != nil
	r.mu.Unlock()
	if !ok {
		return
	}
	p := *pkt
	p.PayloadType = pt
	_ = stream.WritePacketRTP(medi, &p)
}

func (r *rtspStream) OnAudio(codec webrtc.RTPCodecParameters, pkt *rtp.Packet) {
	r.mu.Lock()
	r.note(codec, false)
	stream, medi, pt, ok := r.stream, r.amedia, r.apt, r.built && r.amedia != nil
	r.mu.Unlock()
	if !ok {
		return
	}
	p := *pkt
	p.PayloadType = pt
	_ = stream.WritePacketRTP(medi, &p)
}

func (r *rtspStream) close() {
	r.mu.Lock()
	if r.stream != nil {
		r.stream.Close()
		r.stream = nil
		r.built = false
	}
	r.mu.Unlock()
}
