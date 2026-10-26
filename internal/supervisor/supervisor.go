// Package supervisor runs the bridge for one or more cameras under a single
// cloud session. It discovers the account's cameras, starts one worker per
// camera (each with its own control channel, WebRTC->RTSP stream, MQTT device
// and event pushes), restarts any worker that drops with capped backoff, and
// keeps the shared cloud token refreshed. See docs/RUNNING.md.
package supervisor

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Darkhand81/anona-ha-bridge/internal/cam"
	"github.com/Darkhand81/anona-ha-bridge/internal/cloud"
	"github.com/Darkhand81/anona-ha-bridge/internal/control"
	"github.com/Darkhand81/anona-ha-bridge/internal/ha"
	"github.com/Darkhand81/anona-ha-bridge/internal/media"
)

// Options configures a supervisor run.
type Options struct {
	Region       string        // INTL / EU / OTHERS
	Devices      []string      // deviceId allowlist; empty = every camera in the account
	RTSPListen   string        // e.g. ":8554"
	HTTPListen   string        // e.g. ":8080" (talkback + health); "" disables
	MQTTURL      string        // e.g. tcp://homeassistant:1883; "" disables MQTT
	MQTTUser     string        // optional
	MQTTPass     string        // optional
	HasBattery   bool          // force battery entities (mains models hide them)
	RefreshEvery time.Duration // proactive token re-login interval (0 => 6h)
	MaxBackoff   time.Duration // worker restart backoff cap (0 => 60s)
	ForcePath    string        // when exactly one camera is targeted, use this rtsp path (legacy single-cam "/cam")
}

// Bridge is a running supervisor: the cloud session plus the shared RTSP server
// and a registry of live media sessions (for talkback routing by path).
type Bridge struct {
	cl   *cloud.Client
	opt  Options
	rtsp *media.Server

	mu       sync.RWMutex
	sessions map[string]*media.Session // rtsp path -> live session
}

// cameraTarget is one camera to supervise.
type cameraTarget struct {
	dev  cloud.Device
	path string // rtsp/mqtt path slug (unique within the run)
	name string
}

// Run logs in, discovers the cameras, and supervises them until ctx is
// cancelled. It blocks until then.
func Run(ctx context.Context, email, password string, opt Options) error {
	if opt.RTSPListen == "" {
		opt.RTSPListen = ":8554"
	}
	if opt.MaxBackoff == 0 {
		opt.MaxBackoff = 60 * time.Second
	}

	cl := cloud.New(opt.Region, uuid.NewString())
	if err := cl.Login(email, password); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Println("[sup] login OK")
	cl.StartRefresher(opt.RefreshEvery, ctx.Done())

	targets, err := discover(cl, opt.Devices)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	if len(targets) == 0 {
		return fmt.Errorf("no cameras found for this account (check region/allowlist)")
	}
	if opt.ForcePath != "" && len(targets) == 1 {
		targets[0].path = strings.Trim(opt.ForcePath, "/")
	}
	for _, t := range targets {
		fmt.Printf("[sup] camera %q model=%s id=%s -> rtsp path /%s\n", t.name, t.dev.Model, t.dev.DeviceID, t.path)
	}

	rtsp, err := media.NewRTSPServer(opt.RTSPListen)
	if err != nil {
		return fmt.Errorf("rtsp: %w", err)
	}
	defer rtsp.Close()

	b := &Bridge{cl: cl, opt: opt, rtsp: rtsp, sessions: map[string]*media.Session{}}
	if opt.HTTPListen != "" {
		go b.serveHTTP(opt.HTTPListen)
	}

	b.runAll(ctx, targets)
	fmt.Println("[sup] shutting down")
	return nil
}

// discover lists every camera across the account's homes, filtered by allowlist,
// and assigns each a unique path slug.
func discover(cl *cloud.Client, allow []string) ([]cameraTarget, error) {
	homes, err := cl.HomeIDs()
	if err != nil {
		return nil, err
	}
	allowSet := map[string]bool{}
	for _, d := range allow {
		if d = strings.TrimSpace(d); d != "" {
			allowSet[d] = true
		}
	}
	used := map[string]bool{}
	var out []cameraTarget
	seen := map[string]bool{}
	for _, h := range homes {
		devs, err := cl.DevicesInHome(h)
		if err != nil {
			fmt.Printf("[sup] warn: list home %s: %v\n", h, err)
			continue
		}
		for _, d := range devs {
			if seen[d.DeviceID] {
				continue
			}
			seen[d.DeviceID] = true
			if len(allowSet) > 0 && !allowSet[d.DeviceID] {
				continue
			}
			name := d.NickName
			if name == "" {
				name = "Anona " + d.DeviceID[:min(8, len(d.DeviceID))]
			}
			out = append(out, cameraTarget{dev: d, name: name, path: uniquePath(name, d.DeviceID, used)})
		}
	}
	return out, nil
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// uniquePath derives a stable, collision-free rtsp/mqtt slug from the nickname,
// falling back to the deviceId.
func uniquePath(name, deviceID string, used map[string]bool) string {
	slug := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if slug == "" {
		slug = strings.ToLower(deviceID)
	}
	cand := slug
	if used[cand] { // disambiguate with a short id suffix
		cand = slug + "_" + strings.ToLower(deviceID[:min(6, len(deviceID))])
	}
	for used[cand] {
		cand += "x"
	}
	used[cand] = true
	return cand
}

// runAll keeps one shared control channel up for the whole account and runs
// every camera's media session on it, reconnecting the channel with capped
// backoff whenever it drops. The control channel is per-account (per-user), so a
// single channel must multiplex all cameras by deviceId; opening one channel per
// camera makes their handshakes evict each other (observed as session flapping).
func (b *Bridge) runAll(ctx context.Context, targets []cameraTarget) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		b.cycle(ctx, targets)
		if ctx.Err() != nil {
			return
		}
		// A channel that stayed up a while resets the backoff.
		if time.Since(start) > 2*time.Minute {
			backoff = time.Second
		}
		fmt.Printf("[sup] control channel down; reconnecting in %s\n", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > b.opt.MaxBackoff {
			backoff = b.opt.MaxBackoff
		}
	}
}

// cycle dials the shared control channel, brings up every camera on it, and
// blocks until the channel drops or ctx is cancelled, then tears everything
// down. A camera that fails to come up is skipped for this cycle (the others
// still run); it is retried on the next reconnect.
func (b *Bridge) cycle(ctx context.Context, targets []cameraTarget) {
	wa, err := b.cl.WebsocketAddress()
	if err != nil {
		fmt.Printf("[sup] websocket address: %v\n", err)
		return
	}
	ch, err := control.Dial(control.WSInfo(wa))
	if err != nil {
		fmt.Printf("[sup] control dial: %v\n", err)
		return
	}
	defer ch.Close()
	fmt.Println("[sup] control channel up")

	var (
		sessions []*media.Session
		paths    []string
		hubs     []*ha.Bridge
	)
	for _, t := range targets {
		id := t.dev.DeviceID
		if on, _ := b.cl.Online(id); !on {
			fmt.Printf("[%s] warning: camera reports offline; trying anyway\n", t.path)
		}
		ice, err := b.cl.IceConfig(id)
		if err != nil {
			fmt.Printf("[%s] ice config: %v (skipping this cycle)\n", t.path, err)
			continue
		}
		clientID := uuid.NewString()
		if b.opt.MQTTURL != "" {
			if hub, err := b.startMQTT(ch, t, clientID); err != nil {
				fmt.Printf("[%s] warning: MQTT start failed: %v\n", t.path, err)
			} else {
				hubs = append(hubs, hub)
			}
		}
		sink := b.rtsp.Publisher(t.path)
		sess, err := media.Start(b.cl, ch, id, clientID, ice, sink)
		if err != nil {
			fmt.Printf("[%s] media start: %v (skipping this cycle)\n", t.path, err)
			continue
		}
		b.setSession(t.path, sess)
		sessions = append(sessions, sess)
		paths = append(paths, t.path)
		fmt.Printf("[%s] live; rtsp path /%s\n", t.path, t.path)
	}
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
		for _, p := range paths {
			b.setSession(p, nil)
		}
		for _, h := range hubs {
			h.Stop()
		}
	}()
	if len(sessions) == 0 {
		fmt.Println("[sup] no cameras came up this cycle")
		return
	}
	fmt.Printf("[sup] %d/%d cameras live\n", len(sessions), len(targets))
	select {
	case <-ctx.Done():
	case <-ch.Done():
	}
}

func (b *Bridge) startMQTT(ch *control.Channel, t cameraTarget, clientID string) (*ha.Bridge, error) {
	ctl := cam.New(ch, t.dev.DeviceID)
	model := t.dev.Model
	if model == "" {
		model = "HM1010"
	}
	caps, err := ctl.Capabilities()
	if err != nil {
		fmt.Printf("[%s] warning: capability probe failed: %v\n", t.path, err)
	}
	hasBattery := ha.ModelHasBattery(model) || b.opt.HasBattery
	hub := ha.New(b.opt.MQTTURL, b.opt.MQTTUser, b.opt.MQTTPass,
		"anona-"+t.path+"-"+clientID[:8], ctl, t.name, model, hasBattery, caps)
	if err := hub.Start(); err != nil {
		return nil, err
	}
	fmt.Printf("[%s] MQTT discovery published\n", t.path)
	ch.OnPush(func(p control.Push) {
		if p.DeviceID != "" && p.DeviceID != t.dev.DeviceID {
			return
		}
		if tags := cam.ParseEventTags(p.Packet); len(tags) > 0 {
			hub.OnEvent(tags)
		}
	})
	return hub, nil
}

func (b *Bridge) setSession(path string, s *media.Session) {
	b.mu.Lock()
	if s == nil {
		delete(b.sessions, path)
	} else {
		b.sessions[path] = s
	}
	b.mu.Unlock()
}

func (b *Bridge) session(path string) *media.Session {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sessions[path]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
