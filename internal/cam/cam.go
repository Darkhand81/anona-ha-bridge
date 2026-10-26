// Package cam is a high-level camera controller over the control channel:
// read a parsed status, flip settings, send PTZ, and decode event pushes.
package cam

import (
	"fmt"
	"time"

	"github.com/Darkhand81/anona-ha-bridge/internal/control"
	"github.com/Darkhand81/anona-ha-bridge/internal/pb"
)

type Controller struct {
	ch       *control.Channel
	DeviceID string
}

func New(ch *control.Channel, deviceID string) *Controller {
	return &Controller{ch: ch, DeviceID: deviceID}
}

func (c *Controller) send(pkt []byte) error {
	reply, err := c.ch.Send(c.DeviceID, pkt, 12*time.Second)
	if err != nil {
		return err
	}
	if top, ok := pb.Decode(reply); ok {
		if ec := pb.Get(top, pb.FErrorCode); ec != nil && ec.V != 0 {
			return fmt.Errorf("camera error_code %d", ec.V)
		}
	}
	return nil
}

// Status is the subset of IPCameraStatus the bridge exposes.
type Status struct {
	Battery      int
	Charging     bool
	WifiRSSI     int
	HasWifiRSSI  bool
	StorageSlot   bool // model has an SD slot (memory_storage reported)
	StorageStatus int  // MemoryStorage.Status (0 NORMAL, 3 SD_NON_EXISTENT, ...)
	StorageTotal  int  // MB, valid only when a card is present
	StorageFree   int

	Privacy       bool
	NightVision   int // 0 off,1 ir,2 white,3 starlight,4 smart
	NightVisionOn bool
	IndicatorLED  bool
	WhiteLight    bool
	AudioOn       bool
	AudioVolume   int
	FlipH, FlipV  bool

	Rotation int // image_rollover_mode: 1=0°,2=90°,3=180°,4=270°

	MotionOn  bool
	PersonOn  bool
	VehicleOn bool
	PackageOn bool
	AnimalOn  bool
	MoveOn    bool
	SoundOn   bool
	BabyCryOn bool

	HasPanTilt bool
	PanTenths  int // pan in tenths of a degree
	TiltTenths int
	CalibratingPT bool

	raw []pb.Field // the IPCameraStatus fields, for read-modify-write
}

// Caps describes which entities the camera actually supports, derived from which
// fields/subfields its status reports (mirrors the app's per-model gating).
type Caps struct {
	Privacy, NightVision, IndicatorLED, WhiteLight, Audio bool
	Rotation, FlipH, FlipV                                bool
	Motion, Person, Vehicle, Package, Animal, Move        bool
	Sound, BabyCry                                        bool
	PanTilt, SDCard                                       bool
}

func has(fs []pb.Field, n int) bool { return pb.Get(fs, n) != nil }
func hasIn(fs []pb.Field, parent, child int) bool {
	if p := pb.Get(fs, parent); p != nil && p.Sub != nil {
		return pb.Get(p.Sub, child) != nil
	}
	return false
}

func capsFromRaw(raw []pb.Field) Caps {
	c := Caps{
		Privacy:      has(raw, 4),
		NightVision:  has(raw, 6),
		IndicatorLED: has(raw, 11),
		WhiteLight:   has(raw, 12),
		Audio:        has(raw, 10),
		Motion:       has(raw, 20),
		// ai_detection sub-features (field 21): only expose the ones reported
		Person:  hasIn(raw, 21, 1),
		Vehicle: hasIn(raw, 21, 3),
		Package: hasIn(raw, 21, 4),
		Animal:  hasIn(raw, 21, 5),
		Move:    hasIn(raw, 21, 6),
		// sound_detection (field 23)
		Sound:   hasIn(raw, 23, 1),
		BabyCry: hasIn(raw, 23, 2),
		// camera_display (field 5): rotation vs flip
		Rotation: hasIn(raw, 5, 5),
		FlipH:    hasIn(raw, 5, 7),
		FlipV:    hasIn(raw, 5, 8),
		PanTilt:  has(raw, 51),
	}
	// SD slot exists when the model reports memory_storage at all (even with no
	// card inserted — status then reads SD_NON_EXISTENT). The storage-size values
	// populate from the normal status poll once a card is present, so inserting a
	// card needs no restart.
	c.SDCard = has(raw, 30)
	return c
}

// Capabilities reads the status once and derives the supported entity set.
func (c *Controller) Capabilities() (Caps, error) {
	s, err := c.Status()
	if err != nil {
		return Caps{}, err
	}
	return capsFromRaw(s.raw), nil
}

func sub(fs []pb.Field, num int) []pb.Field {
	if f := pb.Get(fs, num); f != nil {
		return f.Sub
	}
	return nil
}
func val(fs []pb.Field, num int) (uint64, bool) {
	if f := pb.Get(fs, num); f != nil {
		return f.V, true
	}
	return 0, false
}
func flag(fs []pb.Field, num int) bool { v, _ := val(fs, num); return v != 0 }

// Status reads and parses IPCameraStatus.
func (c *Controller) Status() (*Status, error) {
	reply, err := c.ch.Send(c.DeviceID, pb.GetStatus(), 15*time.Second)
	if err != nil {
		return nil, err
	}
	top, ok := pb.Decode(reply)
	if !ok {
		return nil, fmt.Errorf("status: bad packet")
	}
	ipc := pb.Get(top, pb.FIPCamera)
	if ipc == nil || ipc.Sub == nil {
		return nil, fmt.Errorf("status: no ipcamera")
	}
	st := pb.Get(ipc.Sub, pb.FIPCameraStatus)
	if st == nil || st.Sub == nil {
		return nil, fmt.Errorf("status: no ip_camera_status")
	}
	s := &Status{raw: st.Sub}
	// battery: status.1 (DeviceStatus) -> .1 (Battery) -> .1 capacity, .2 charge_status
	if ds := sub(st.Sub, 1); ds != nil {
		if bat := sub(ds, 1); bat != nil {
			if v, ok := val(bat, 1); ok {
				s.Battery = int(v)
			}
			if v, ok := val(bat, 2); ok {
				s.Charging = v == 1
			}
		}
	}
	if cc := sub(st.Sub, 4); cc != nil { // camera_sleep (CameraControl)
		s.Privacy = flag(cc, 2)
	}
	if d := sub(st.Sub, 5); d != nil { // camera_display
		s.FlipH = flag(d, 7)
		s.FlipV = flag(d, 8)
		if v, ok := val(d, 5); ok {
			s.Rotation = int(v)
		}
	}
	if nv := sub(st.Sub, 6); nv != nil {
		if v, ok := val(nv, 1); ok {
			s.NightVision = int(v)
		}
		s.NightVisionOn = flag(nv, 3)
	}
	if ac := sub(st.Sub, 10); ac != nil {
		s.AudioOn = flag(ac, 1)
		if v, ok := val(ac, 2); ok {
			s.AudioVolume = int(v)
		}
	}
	if il := sub(st.Sub, 11); il != nil {
		s.IndicatorLED = flag(il, 1)
	}
	if wl := sub(st.Sub, 12); wl != nil {
		s.WhiteLight = flag(wl, 1)
	}
	if md := sub(st.Sub, 20); md != nil {
		s.MotionOn = flag(md, 1)
	}
	if ai := sub(st.Sub, 21); ai != nil {
		s.PersonOn = flag(ai, 1)
		s.VehicleOn = flag(ai, 3)
		s.PackageOn = flag(ai, 4)
		s.AnimalOn = flag(ai, 5)
	}
	if sd := sub(st.Sub, 23); sd != nil {
		s.SoundOn = flag(sd, 1)
		s.BabyCryOn = flag(sd, 2)
	}
	if ms := sub(st.Sub, 30); ms != nil {
		s.StorageSlot = true // model reports memory_storage => it has an SD slot
		if v, ok := val(ms, 2); ok {
			s.StorageStatus = int(v)
		}
		if v, ok := val(ms, 3); ok {
			s.StorageTotal = int(v)
		}
		if v, ok := val(ms, 4); ok {
			s.StorageFree = int(v)
		}
	}
	if v, ok := val(st.Sub, 46); ok {
		s.WifiRSSI = int(int32(v))
		s.HasWifiRSSI = true
	}
	if pt := sub(st.Sub, 51); pt != nil { // pan_tilt_position
		if cur := sub(pt, 1); cur != nil {
			if v, ok := val(cur, 1); ok {
				s.PanTenths = int(int32(v))
			}
			if v, ok := val(cur, 2); ok {
				s.TiltTenths = int(int32(v))
			}
			s.HasPanTilt = true
		}
		s.CalibratingPT = flag(pt, 3)
	}
	return s, nil
}

// --- direct setters ---

func (c *Controller) SetPrivacy(on bool) error      { return c.send(pb.SetPrivacy(on)) }
func (c *Controller) SetNightVision(m int, on bool) error { return c.send(pb.SetNightVision(m, on)) }
func (c *Controller) SetIndicatorLED(on bool) error  { return c.send(pb.SetIndicatorLight(on)) }
func (c *Controller) SetWhiteLight(on bool) error    { return c.send(pb.SetWhiteLight(on)) }
func (c *Controller) SetSoundLight(on bool) error    { return c.send(pb.SetSoundLight(on)) }
func (c *Controller) SetAudio(on bool, vol int) error { return c.send(pb.SetAudio(on, vol)) }
func (c *Controller) SetFlip(h, v bool) error        { return c.send(pb.SetFlip(h, v)) }
func (c *Controller) SetRotation(mode int) error     { return c.send(pb.SetRotation(mode)) }
func (c *Controller) Reboot() error                  { return c.send(pb.Reboot()) }
func (c *Controller) PTZ(direction, action int) error {
	return c.send(pb.PTZControl(direction, action))
}
func (c *Controller) Calibrate() error { return c.send(pb.ResetPanTilt()) }

// --- detection toggles (read-modify-write) ---

// status field that carries each detection submessage.
var detStatusField = map[string]int{"motion": 20, "ai": 21, "sound": 23}

func (c *Controller) setDetection(group string, fieldNum int, on bool) error {
	s, err := c.Status()
	if err != nil {
		return err
	}
	cur := sub(s.raw, detStatusField[group])
	if cur == nil {
		return fmt.Errorf("%s detection not present in status", group)
	}
	pkt, ok := pb.SetBoolInDetection(group, cur, fieldNum, on)
	if !ok {
		return fmt.Errorf("bad detection group %s", group)
	}
	return c.send(pkt)
}

func (c *Controller) SetMotion(on bool) error  { return c.setDetection("motion", 1, on) }
func (c *Controller) SetPerson(on bool) error  { return c.setDetection("ai", 1, on) }
func (c *Controller) SetVehicle(on bool) error { return c.setDetection("ai", 3, on) }
func (c *Controller) SetPackage(on bool) error { return c.setDetection("ai", 4, on) }
func (c *Controller) SetAnimal(on bool) error  { return c.setDetection("ai", 5, on) }
func (c *Controller) SetSound(on bool) error   { return c.setDetection("sound", 1, on) }
func (c *Controller) SetBabyCry(on bool) error { return c.setDetection("sound", 2, on) }

// --- events ---

// AlgTag -> short event name (smart_common.proto AlgTag).
var algName = map[uint64]string{
	1: "person", 2: "person", 3: "vehicle", 4: "package", 5: "animal",
	6: "motion", 7: "baby_cry", 9: "doorbell", 10: "package", 11: "animal", 12: "animal", 13: "animal",
}

// ParseEventTags returns the alg-tag event names in a REPORT_IPCAMERA_EVENT push.
func ParseEventTags(packet []byte) []string {
	top, ok := pb.Decode(packet)
	if !ok {
		return nil
	}
	ipc := pb.Get(top, pb.FIPCamera)
	if ipc == nil || ipc.Sub == nil {
		return nil
	}
	var events [][]pb.Field
	if e := pb.Get(ipc.Sub, 1); e != nil && e.Sub != nil { // event
		events = append(events, e.Sub)
	}
	if el := pb.Get(ipc.Sub, 2); el != nil && el.Sub != nil { // event_list
		for _, f := range el.Sub {
			if f.Num == 1 && f.Sub != nil {
				events = append(events, f.Sub)
			}
		}
	}
	var names []string
	for _, ev := range events {
		for _, f := range ev {
			if f.Num == 3 && f.Sub != nil { // property_param_list (repeated); each is a PropertyParam
				if pid, ok := val(f.Sub, 1); ok && pid == 1 { // ALG_TAG
					if v, ok := val(f.Sub, 4); ok { // value_uint32
						if n := algName[v]; n != "" {
							names = append(names, n)
						}
					}
				}
			}
		}
	}
	return names
}
