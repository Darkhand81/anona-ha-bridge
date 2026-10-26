// Package pb provides just enough protobuf wire handling for the Anona Packet
// protocol, hand-built so the bridge needs no .proto codegen. Field numbers come
// from proto/smart*.proto (recovered from the app).
package pb

import "encoding/binary"

// ---- encoding ----

func uvarint(n uint64) []byte {
	var b []byte
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if n == 0 {
			return b
		}
	}
}

// FieldVar appends a varint (wiretype 0) field.
func FieldVar(fn int, v uint64) []byte { return append(uvarint(uint64(fn)<<3|0), uvarint(v)...) }

// FieldMsg appends a length-delimited (wiretype 2) field.
func FieldMsg(fn int, body []byte) []byte {
	out := uvarint(uint64(fn)<<3 | 2)
	out = append(out, uvarint(uint64(len(body)))...)
	return append(out, body...)
}

// Packet type/ids from smart.proto and smart_ipcamera.proto.
const (
	TypeIPCamera = 50

	IDReportStatus      = 3
	IDGetStatus         = 4
	IDWakeupDevice      = 11
	IDSetCameraControl  = 19 // privacy
	IDSetNightVision    = 23
	IDSetIndicatorLight = 33
	IDWebRtcStart       = 96
	IDWebRtcCandidate   = 97
	IDWebRtcDisconnect  = 98
	IDWebRtcWakeup      = 99
	IDLiveStreamStart   = 100
	IDLiveStreamStop    = 101
	IDVoiceStart        = 102
	IDVoiceStop         = 103
	IDControlPanTilt    = 107
	IDGetPanTiltPos     = 109
	IDReboot            = 112

	// IPCamera oneof field numbers (payload inside Packet.ipcamera, field 52)
	fIPCamera          = 52
	fErrorCode         = 49
	fPanTiltControl    = 80
	fWebRtcStartReq    = 70
	fWebRtcStartResp   = 71
	fWebRtcCandidate   = 72
	fWebRtcDisconnect  = 73
	fP2PLiveStartReq   = 75
	fP2PLiveStop       = 77
	fP2PVoiceStartReq  = 120
	fP2PVoiceStop      = 122
	fIPCameraStatus    = 3
	fPanTiltPosition   = 81
)

// CameraPacket builds Packet{type=IPCamera, id, [ipcamera=body]}.
func CameraPacket(id int, ipcameraBody []byte) []byte {
	p := FieldVar(1, TypeIPCamera)
	p = append(p, FieldVar(2, uint64(id))...)
	if len(ipcameraBody) > 0 {
		p = append(p, FieldMsg(fIPCamera, ipcameraBody)...)
	}
	return p
}

// GetStatus = Packet{type=IPCamera, id=4}.
func GetStatus() []byte { return CameraPacket(IDGetStatus, nil) }

// PTZ directions / actions.
const (
	DirUp = 1 + iota
	DirDown
	DirLeft
	DirRight
	DirStop
)
const (
	ActClick      = 1
	ActContinuous = 2
)

// PTZControl = Packet{id=107, ipcamera{pan_tilt_control(80){direction(1),action_type(2)}}}.
func PTZControl(direction, action int) []byte {
	ptc := append(FieldVar(1, uint64(direction)), FieldVar(2, uint64(action))...)
	return CameraPacket(IDControlPanTilt, FieldMsg(fPanTiltControl, ptc))
}

// WebRtcStart = Packet{id=96, ipcamera{webrtc_start_request(70){...}}}.
// Mirrors PacketKt.webrtcStartPacket for the live path.
type TurnServer struct{ Address, Username, Password string }

func WebRtcStart(clientID string, sdpOfferCompressed []byte, stun string, turns []TurnServer, live bool) []byte {
	// WebRtcStart.Request fields: client_type(1), client_id(2), sdp_offer_compressed(3),
	// stun_address(4), turn_info_list(5), p2p_live_info(6).
	req := FieldVar(1, 0) // client_type = 0
	req = append(req, FieldMsg(2, []byte(clientID))...)
	req = append(req, FieldMsg(3, sdpOfferCompressed)...)
	req = append(req, FieldMsg(4, []byte(stun))...)
	for _, t := range turns {
		// TurnInfo.List{ list(1) repeated TurnInfo{turn_address(1),user_name(2),pass_word(3)} }
		ti := FieldMsg(1, []byte(t.Address))
		ti = append(ti, FieldMsg(2, []byte(t.Username))...)
		ti = append(ti, FieldMsg(3, []byte(t.Password))...)
		req = append(req, FieldMsg(5, FieldMsg(1, ti))...)
	}
	// P2PLiveInfo{ live(1) }
	liveB := 0
	if live {
		liveB = 1
	}
	req = append(req, FieldMsg(6, FieldVar(1, uint64(liveB)))...)
	return CameraPacket(IDWebRtcStart, FieldMsg(fWebRtcStartReq, req))
}

// WebRtcCandidate = Packet{id=97, ipcamera{webrtc_candidate(72){client_type,client_id,candidate,sdp_mid,sdp_m_line_index}}}.
func WebRtcCandidate(clientID, candidate, sdpMid, sdpMLineIndex string) []byte {
	c := FieldVar(1, 0)
	c = append(c, FieldMsg(2, []byte(clientID))...)
	c = append(c, FieldMsg(3, []byte(candidate))...)
	c = append(c, FieldMsg(4, []byte(sdpMid))...)
	c = append(c, FieldMsg(5, []byte(sdpMLineIndex))...)
	return CameraPacket(IDWebRtcCandidate, FieldMsg(fWebRtcCandidate, c))
}

// P2PLiveStreamStart = Packet{id=100, ipcamera{p2p_livestream_start_request(75){client_type,client_id}}}.
func P2PLiveStreamStart(clientID string) []byte {
	r := FieldVar(1, 0)
	r = append(r, FieldMsg(2, []byte(clientID))...)
	return CameraPacket(IDLiveStreamStart, FieldMsg(fP2PLiveStartReq, r))
}

// P2PVoiceStart = Packet{id=102, ipcamera{p2p_voice_start_request(120){client_type,client_id}}} — talkback gate.
func P2PVoiceStart(clientID string) []byte {
	r := FieldVar(1, 0)
	r = append(r, FieldMsg(2, []byte(clientID))...)
	return CameraPacket(IDVoiceStart, FieldMsg(fP2PVoiceStartReq, r))
}

// P2PVoiceStop = Packet{id=103, ipcamera{p2p_voice_stop(122){client_type,client_id}}}.
func P2PVoiceStop(clientID string) []byte {
	r := FieldVar(1, 0)
	r = append(r, FieldMsg(2, []byte(clientID))...)
	return CameraPacket(IDVoiceStop, FieldMsg(fP2PVoiceStop, r))
}

// IPCamera payload field numbers (inside Packet.ipcamera).
const (
	fCameraControl  = 21 // CameraControl (privacy/sleep)
	fCameraDisplay  = 22
	fNightVision    = 23
	fAudioConfig    = 27
	fIndicatorLight = 28
	fWhiteLight     = 29
	fSoundLight     = 30
	fRebootCamera   = 31
	fMotionDetect   = 40
	fAiDetect       = 41
	fDetectAlarm    = 42
	fSoundDetect    = 43
)

// SET command ids (cameraPacket id argument).
const (
	idSetCameraControl = 19
	idSetCameraDisplay = 21
	idSetNightVision   = 23
	idSetAudioConfig   = 31
	idSetIndicator     = 33
	idSetWhiteLight    = 35
	idSetSoundLight    = 38
	idResetPanTilt     = 106
	idSetMotionDetect  = 49
	idSetAiDetect      = 51
	idSetDetectAlarm   = 53
	idSetSoundDetect   = 55
	idReboot           = 112
)

func b2i(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// --- direct setters (no read-modify-write needed) ---

// SetPrivacy: CameraControl{sleep_enable=false, privacy_mode_on=on}.
func SetPrivacy(on bool) []byte {
	cc := append(FieldVar(1, 0), FieldVar(2, b2i(on))...)
	return CameraPacket(idSetCameraControl, FieldMsg(fCameraControl, cc))
}

// SetNightVision: NightVision{mode, night_vision_on=on}. mode 0..4.
func SetNightVision(mode int, on bool) []byte {
	nv := append(FieldVar(1, uint64(mode)), FieldVar(3, b2i(on))...)
	return CameraPacket(idSetNightVision, FieldMsg(fNightVision, nv))
}

func SetIndicatorLight(on bool) []byte {
	return CameraPacket(idSetIndicator, FieldMsg(fIndicatorLight, FieldVar(1, b2i(on))))
}
func SetWhiteLight(on bool) []byte {
	return CameraPacket(idSetWhiteLight, FieldMsg(fWhiteLight, FieldVar(1, b2i(on))))
}
func SetSoundLight(on bool) []byte {
	return CameraPacket(idSetSoundLight, FieldMsg(fSoundLight, FieldVar(1, b2i(on))))
}

// SetAudio: AudioConfig{on, [volume]}. volume 1..3 (LOW/MIDDLE/HIGH); 0 omits it.
func SetAudio(on bool, volume int) []byte {
	ac := FieldVar(1, b2i(on))
	if volume > 0 {
		ac = append(ac, FieldVar(2, uint64(volume))...)
	}
	return CameraPacket(idSetAudioConfig, FieldMsg(fAudioConfig, ac))
}

// SetFlip: CameraDisplay{flip_horizontal(7), flip_vertical(8)}.
func SetFlip(horizontal, vertical bool) []byte {
	d := append(FieldVar(7, b2i(horizontal)), FieldVar(8, b2i(vertical))...)
	return CameraPacket(idSetCameraDisplay, FieldMsg(fCameraDisplay, d))
}

// SetRotation: CameraDisplay{image_rollover_mode(5)}. mode 1=0°,2=90°,3=180°,4=270°.
func SetRotation(mode int) []byte {
	return CameraPacket(idSetCameraDisplay, FieldMsg(fCameraDisplay, FieldVar(5, uint64(mode))))
}

func Reboot() []byte {
	return CameraPacket(idReboot, FieldMsg(fRebootCamera, FieldVar(1, 1)))
}
func ResetPanTilt() []byte { return CameraPacket(idResetPanTilt, nil) }

// --- read-modify-write setters (detection) ---

// Detection payload ids, used to resend the whole submessage with one bool flipped.
var detectSet = map[string][2]int{
	// name: {setPacketID, ipcameraField}
	"motion": {idSetMotionDetect, fMotionDetect},
	"ai":     {idSetAiDetect, fAiDetect},
	"alarm":  {idSetDetectAlarm, fDetectAlarm},
	"sound":  {idSetSoundDetect, fSoundDetect},
}

// SetBoolInDetection resends an existing detection submessage (decoded Fields
// from the current status) with one scalar field set to on. group is one of
// motion/ai/alarm/sound.
func SetBoolInDetection(group string, current []Field, fieldNum int, on bool) ([]byte, bool) {
	ids, ok := detectSet[group]
	if !ok {
		return nil, false
	}
	sub := setScalar(current, fieldNum, b2i(on))
	return CameraPacket(ids[0], FieldMsg(ids[1], Encode(sub))), true
}

// setScalar returns a copy of fs with the varint field fieldNum set to v
// (replacing it if present, else appending).
func setScalar(fs []Field, fieldNum int, v uint64) []Field {
	out := make([]Field, 0, len(fs)+1)
	found := false
	for _, f := range fs {
		if f.Num == fieldNum && f.Typ == 0 {
			out = append(out, Field{Num: fieldNum, Typ: 0, V: v})
			found = true
		} else {
			out = append(out, f)
		}
	}
	if !found {
		out = append(out, Field{Num: fieldNum, Typ: 0, V: v})
	}
	return out
}

// Encode serializes a field tree (exported wrapper around encode).
func Encode(fs []Field) []byte { return encode(fs) }

// ---- decoding (schema-free) ----

type Field struct {
	Num int
	Typ int // 0 varint, 2 bytes/msg, 5 i32, 1 i64
	V   uint64
	B   []byte    // for wiretype 2 (raw)
	Sub []Field   // for wiretype 2 when it parses as a message
}

func readUvarint(b []byte, o int) (uint64, int, bool) {
	var r uint64
	var s uint
	for {
		if o >= len(b) {
			return 0, o, false
		}
		c := b[o]
		o++
		r |= uint64(c&0x7f) << s
		if c&0x80 == 0 {
			return r, o, true
		}
		s += 7
		if s > 63 {
			return 0, o, false
		}
	}
}

// Decode parses bytes as a protobuf message; ok=false if not well-formed.
func Decode(b []byte) ([]Field, bool) {
	o := 0
	var out []Field
	for o < len(b) {
		tag, no, ok := readUvarint(b, o)
		if !ok {
			return nil, false
		}
		o = no
		fn := int(tag >> 3)
		wt := int(tag & 7)
		switch wt {
		case 0:
			v, no, ok := readUvarint(b, o)
			if !ok {
				return nil, false
			}
			o = no
			out = append(out, Field{Num: fn, Typ: 0, V: v})
		case 2:
			ln, no, ok := readUvarint(b, o)
			if !ok || no+int(ln) > len(b) {
				return nil, false
			}
			o = no
			chunk := b[o : o+int(ln)]
			o += int(ln)
			f := Field{Num: fn, Typ: 2, B: chunk}
			if sub, ok := Decode(chunk); ok && reencodeEq(sub, chunk) {
				f.Sub = sub
			}
			out = append(out, f)
		case 5:
			if o+4 > len(b) {
				return nil, false
			}
			out = append(out, Field{Num: fn, Typ: 5, V: uint64(binary.LittleEndian.Uint32(b[o:]))})
			o += 4
		case 1:
			if o+8 > len(b) {
				return nil, false
			}
			out = append(out, Field{Num: fn, Typ: 1, V: binary.LittleEndian.Uint64(b[o:])})
			o += 8
		default:
			return nil, false
		}
	}
	return out, true
}

func encode(fs []Field) []byte {
	var out []byte
	for _, f := range fs {
		switch f.Typ {
		case 0:
			out = append(out, FieldVar(f.Num, f.V)...)
		case 2:
			body := f.B
			if f.Sub != nil {
				body = encode(f.Sub)
			}
			out = append(out, FieldMsg(f.Num, body)...)
		case 5:
			out = append(out, uvarint(uint64(f.Num)<<3|5)...)
			var tmp [4]byte
			binary.LittleEndian.PutUint32(tmp[:], uint32(f.V))
			out = append(out, tmp[:]...)
		case 1:
			out = append(out, uvarint(uint64(f.Num)<<3|1)...)
			var tmp [8]byte
			binary.LittleEndian.PutUint64(tmp[:], f.V)
			out = append(out, tmp[:]...)
		}
	}
	return out
}

func reencodeEq(fs []Field, orig []byte) bool {
	e := encode(fs)
	if len(e) != len(orig) {
		return false
	}
	for i := range e {
		if e[i] != orig[i] {
			return false
		}
	}
	return true
}

// Get returns the first field with the given number, or nil.
func Get(fs []Field, num int) *Field {
	for i := range fs {
		if fs[i].Num == num {
			return &fs[i]
		}
	}
	return nil
}

// Field-number accessors used by the media layer.
const (
	FIPCamera        = fIPCamera
	FErrorCode       = fErrorCode
	FWebRtcStartResp = fWebRtcStartResp
	FIPCameraStatus  = fIPCameraStatus
)
