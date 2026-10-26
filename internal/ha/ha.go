// Package ha exposes a camera to Home Assistant over MQTT discovery: settings as
// switches/selects, PTZ and reboot as buttons, status as sensors, and detection
// events as binary sensors. State is one retained JSON topic; entities read it
// with value_template. Commands re-poll status so HA reflects the camera, not an
// optimistic guess.
package ha

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Darkhand81/anona-ha-bridge/internal/cam"
)

type Bridge struct {
	cli        mqtt.Client
	ctl        *cam.Controller
	caps       cam.Caps
	node       string
	devName    string
	model      string
	hasBattery bool
	stateTopic string
	availTopic string
	cmdBase    string
	evtBase    string
	poll       time.Duration
	stop       chan struct{}
}

var rotModes = []string{"0", "90", "180", "270"}

func rotToStr(m int) string {
	if m >= 1 && m <= 4 {
		return rotModes[m-1]
	}
	return "0"
}
func rotToInt(s string) int {
	for i, r := range rotModes {
		if r == s {
			return i + 1
		}
	}
	return 1
}

var nvModes = []string{"off", "ir", "white_light", "starlight", "smart"}

func nvToStr(m int) string {
	if m >= 0 && m < len(nvModes) {
		return nvModes[m]
	}
	return "off"
}
func nvToInt(s string) int {
	for i, n := range nvModes {
		if n == s {
			return i
		}
	}
	return 0
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// ModelHasBattery reports whether a model is battery-powered. The camera's
// IPCameraStatus always carries a DeviceStatus block, but on mains models it is
// firmware boilerplate (e.g. HM1010 reports a static 66% and charge mode SOLAR),
// so battery is exposed only for known battery models (or when forced).
func ModelHasBattery(model string) bool {
	switch strings.ToUpper(model) {
	case "HM1010": // mains indoor pan/tilt
		return false
	}
	// Default: don't invent a battery. Battery models can be added here, or
	// forced on with ANONA_HAS_BATTERY=1.
	return false
}

func New(broker, user, pass, clientID string, ctl *cam.Controller, devName, model string, hasBattery bool, caps cam.Caps) *Bridge {
	node := "anona_" + sanitize(ctl.DeviceID)
	b := &Bridge{
		ctl: ctl, caps: caps, node: node, devName: devName, model: model, hasBattery: hasBattery,
		stateTopic: "anona/" + node + "/state",
		availTopic: "anona/" + node + "/availability",
		cmdBase:    "anona/" + node + "/cmd",
		evtBase:    "anona/" + node + "/event",
		poll:       30 * time.Second,
		stop:       make(chan struct{}),
	}
	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(clientID).
		SetUsername(user).SetPassword(pass).SetAutoReconnect(true).
		SetWill(b.availTopic, "offline", 1, true).
		SetOnConnectHandler(b.onConnect)
	b.cli = mqtt.NewClient(opts)
	return b
}

func (b *Bridge) Start() error {
	if t := b.cli.Connect(); t.Wait() && t.Error() != nil {
		return t.Error()
	}
	go b.pollLoop()
	return nil
}

// Stop ends the poll loop, marks the device offline, and disconnects the MQTT
// client. Used when a camera worker tears down before reconnecting so each
// restart does not leak a goroutine or broker connection. Idempotent.
func (b *Bridge) Stop() {
	select {
	case <-b.stop:
		return // already stopped
	default:
		close(b.stop)
	}
	if b.cli.IsConnected() {
		b.cli.Publish(b.availTopic, 1, true, "offline").Wait()
		b.cli.Disconnect(250)
	}
}

func (b *Bridge) onConnect(c mqtt.Client) {
	b.publishDiscovery()
	c.Publish(b.availTopic, 1, true, "online")
	c.Subscribe(b.cmdBase+"/#", 1, b.onCommand)
	// prime state
	if s, err := b.ctl.Status(); err == nil {
		b.publishState(s)
	}
}

func (b *Bridge) device() map[string]any {
	return map[string]any{
		"identifiers":  []string{b.node},
		"name":         b.devName,
		"model":        b.model,
		"manufacturer": "Anona / 70mai",
	}
}

func (b *Bridge) pub(component, obj string, cfg map[string]any) {
	cfg["availability_topic"] = b.availTopic
	cfg["device"] = b.device()
	cfg["unique_id"] = b.node + "_" + obj
	topic := fmt.Sprintf("homeassistant/%s/%s/%s/config", component, b.node, obj)
	buf, _ := json.Marshal(cfg)
	b.cli.Publish(topic, 1, true, buf)
}

func (b *Bridge) sw(obj, name, jsonKey string) {
	b.pub("switch", obj, map[string]any{
		"name":          name,
		"state_topic":   b.stateTopic,
		"value_template": fmt.Sprintf("{{ value_json.%s }}", jsonKey),
		"command_topic": b.cmdBase + "/" + obj,
		"payload_on":    "ON", "payload_off": "OFF",
		"state_on": "ON", "state_off": "OFF",
	})
}

func (b *Bridge) button(obj, name string) {
	b.pub("button", obj, map[string]any{
		"name": name, "command_topic": b.cmdBase + "/" + obj, "payload_press": "PRESS",
	})
}

func (b *Bridge) sensor(obj, name, jsonKey, unit, devClass string) {
	cfg := map[string]any{
		"name": name, "state_topic": b.stateTopic,
		"value_template": fmt.Sprintf("{{ value_json.%s }}", jsonKey),
	}
	if unit != "" {
		cfg["unit_of_measurement"] = unit
	}
	if devClass != "" {
		cfg["device_class"] = devClass
	}
	b.pub("sensor", obj, cfg)
}

func (b *Bridge) binary(obj, name, devClass string) {
	b.pub("binary_sensor", obj, map[string]any{
		"name": name, "state_topic": b.evtBase + "/" + obj,
		"payload_on": "ON", "payload_off": "OFF",
		"off_delay": 30, "device_class": devClass,
	})
}

func (b *Bridge) publishDiscovery() {
	c := b.caps
	// buttons
	if c.PanTilt {
		b.button("ptz_up", "PTZ Up")
		b.button("ptz_down", "PTZ Down")
		b.button("ptz_left", "PTZ Left")
		b.button("ptz_right", "PTZ Right")
		b.button("ptz_stop", "PTZ Stop")
		b.button("ptz_calibrate", "PTZ Calibrate")
	}
	b.button("reboot", "Reboot")
	// switches (gated by reported capability)
	if c.Privacy {
		b.sw("privacy", "Privacy Mode", "privacy")
	}
	if c.IndicatorLED {
		b.sw("indicator_led", "Indicator LED", "indicator_led")
	}
	if c.WhiteLight {
		b.sw("white_light", "Spotlight", "white_light")
	}
	if c.Audio {
		b.sw("audio", "Audio", "audio")
	}
	if c.FlipH {
		b.sw("flip_h", "Flip Horizontal", "flip_h")
	}
	if c.FlipV {
		b.sw("flip_v", "Flip Vertical", "flip_v")
	}
	if c.Motion {
		b.sw("motion", "Motion Detection", "motion")
	}
	if c.Person {
		b.sw("person", "Person Detection", "person")
	}
	if c.Vehicle {
		b.sw("vehicle", "Vehicle Detection", "vehicle")
	}
	if c.Package {
		b.sw("package", "Package Detection", "package")
	}
	if c.Animal {
		b.sw("animal", "Animal Detection", "animal")
	}
	if c.Sound {
		b.sw("sound", "Sound Detection", "sound")
	}
	if c.BabyCry {
		b.sw("baby_cry", "Baby Cry Detection", "baby_cry")
	}
	// selects
	if c.NightVision {
		b.pub("select", "night_vision", map[string]any{
			"name": "Night Vision", "state_topic": b.stateTopic,
			"value_template": "{{ value_json.night_vision }}",
			"command_topic":  b.cmdBase + "/night_vision", "options": nvModes,
		})
	}
	if c.Rotation {
		b.pub("select", "rotation", map[string]any{
			"name": "Image Rotation", "state_topic": b.stateTopic,
			"value_template": "{{ value_json.rotation }}",
			"command_topic":  b.cmdBase + "/rotation", "options": rotModes,
			"unit_of_measurement": "°",
		})
	}
	// sensors
	if b.hasBattery {
		b.sensor("battery", "Battery", "battery", "%", "battery")
	}
	b.sensor("wifi_rssi", "Wi-Fi RSSI", "wifi_rssi", "dBm", "signal_strength")
	if c.SDCard {
		b.sensor("sd_status", "SD Card", "sd_status", "", "")
		b.sensor("storage_free", "Storage Free", "storage_free", "MB", "data_size")
		b.sensor("storage_total", "Storage Total", "storage_total", "MB", "data_size")
	}
	if c.PanTilt {
		b.sensor("pan", "Pan", "pan", "°", "")
		b.sensor("tilt", "Tilt", "tilt", "°", "")
	}
	// event binary sensors (only for supported detections)
	if c.Motion {
		b.binary("motion", "Motion", "motion")
	}
	if c.Person {
		b.binary("person", "Person", "occupancy")
	}
	if c.Animal {
		b.binary("animal", "Animal", "occupancy")
	}
	if c.Vehicle {
		b.binary("vehicle", "Vehicle", "occupancy")
	}
	if c.Package {
		b.binary("package", "Package", "occupancy")
	}
	if c.BabyCry {
		b.binary("baby_cry", "Baby Cry", "sound")
	}
}

// MemoryStorage.Status -> readable text.
var sdStatus = map[int]string{
	0: "Normal", 1: "Formatting", 2: "Error", 3: "No card", 4: "Ejected",
	5: "Low capacity", 6: "Low class level", 7: "Incompatible format",
	8: "Slow write speed", 9: "High capacity", 10: "Too many fragments",
	11: "Non-owner files", 12: "Non-system files", 13: "Clearing",
}

func sdStatusName(s int) string {
	if n, ok := sdStatus[s]; ok {
		return n
	}
	return fmt.Sprintf("status %d", s)
}

func onoff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func (b *Bridge) publishState(s *cam.Status) {
	m := map[string]any{
		"privacy": onoff(s.Privacy), "night_vision": nvToStr(s.NightVision),
		"indicator_led": onoff(s.IndicatorLED), "white_light": onoff(s.WhiteLight),
		"audio": onoff(s.AudioOn), "flip_h": onoff(s.FlipH), "flip_v": onoff(s.FlipV),
		"rotation": rotToStr(s.Rotation),
		"motion":   onoff(s.MotionOn), "person": onoff(s.PersonOn), "vehicle": onoff(s.VehicleOn),
		"package": onoff(s.PackageOn), "animal": onoff(s.AnimalOn),
		"sound": onoff(s.SoundOn), "baby_cry": onoff(s.BabyCryOn),
	}
	if s.HasPanTilt {
		m["pan"] = float64(s.PanTenths) / 10.0
		m["tilt"] = float64(s.TiltTenths) / 10.0
	}
	if b.hasBattery {
		m["battery"] = s.Battery
		m["charging"] = s.Charging
	}
	if s.HasWifiRSSI {
		m["wifi_rssi"] = s.WifiRSSI
	}
	if b.caps.SDCard {
		m["sd_status"] = sdStatusName(s.StorageStatus)
		if s.StorageTotal > 0 { // size valid only when a card is present
			m["storage_total"] = s.StorageTotal
			m["storage_free"] = s.StorageFree
		}
	}
	buf, _ := json.Marshal(m)
	b.cli.Publish(b.stateTopic, 1, true, buf)
}

func (b *Bridge) pollLoop() {
	t := time.NewTicker(b.poll)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			if s, err := b.ctl.Status(); err == nil {
				b.publishState(s)
			}
		}
	}
}

func (b *Bridge) onCommand(_ mqtt.Client, msg mqtt.Message) {
	obj := strings.TrimPrefix(msg.Topic(), b.cmdBase+"/")
	payload := string(msg.Payload())
	on := payload == "ON"
	var err error
	switch obj {
	case "ptz_up":
		err = b.ctl.PTZ(1, 1)
	case "ptz_down":
		err = b.ctl.PTZ(2, 1)
	case "ptz_left":
		err = b.ctl.PTZ(3, 1)
	case "ptz_right":
		err = b.ctl.PTZ(4, 1)
	case "ptz_stop":
		err = b.ctl.PTZ(5, 2)
	case "ptz_calibrate":
		err = b.ctl.Calibrate()
	case "reboot":
		err = b.ctl.Reboot()
	case "privacy":
		err = b.ctl.SetPrivacy(on)
	case "indicator_led":
		err = b.ctl.SetIndicatorLED(on)
	case "white_light":
		err = b.ctl.SetWhiteLight(on)
	case "audio":
		err = b.ctl.SetAudio(on, 0)
	case "flip_h":
		v := false
		if s, e := b.ctl.Status(); e == nil {
			v = s.FlipV
		}
		err = b.ctl.SetFlip(on, v)
	case "flip_v":
		h := false
		if s, e := b.ctl.Status(); e == nil {
			h = s.FlipH
		}
		err = b.ctl.SetFlip(h, on)
	case "motion":
		err = b.ctl.SetMotion(on)
	case "person":
		err = b.ctl.SetPerson(on)
	case "vehicle":
		err = b.ctl.SetVehicle(on)
	case "package":
		err = b.ctl.SetPackage(on)
	case "animal":
		err = b.ctl.SetAnimal(on)
	case "sound":
		err = b.ctl.SetSound(on)
	case "baby_cry":
		err = b.ctl.SetBabyCry(on)
	case "night_vision":
		m := nvToInt(payload)
		err = b.ctl.SetNightVision(m, m != 0)
	case "rotation":
		err = b.ctl.SetRotation(rotToInt(payload))
	default:
		return
	}
	if err != nil {
		fmt.Printf("[ha] command %s=%q error: %v\n", obj, payload, err)
	}
	// reflect real state
	if s, e := b.ctl.Status(); e == nil {
		b.publishState(s)
	}
}

// OnEvent publishes event-driven binary sensors (auto-off via off_delay).
func (b *Bridge) OnEvent(names []string) {
	for _, n := range names {
		obj := n
		if n == "person" {
			obj = "person"
		}
		b.cli.Publish(b.evtBase+"/"+obj, 1, false, "ON")
	}
}
