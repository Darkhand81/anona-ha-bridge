// Command anona-bridge runs live WebRTC sessions against one or more Anona
// cameras and republishes each as RTSP for go2rtc / Home Assistant, with
// optional MQTT discovery and talkback. It supervises the cameras (reconnect
// with backoff, token refresh) until interrupted.
//
// Two ways to invoke it:
//
//	# positional, single camera (legacy; served at rtsp://host:8554/cam):
//	anona-bridge <email> <password> <deviceId> [region] [rtspListen]
//
//	# env-driven, every camera in the account (used by the HA add-on):
//	ANONA_EMAIL=... ANONA_PASSWORD=... [ANONA_REGION=INTL] \
//	ANONA_MQTT_URL=tcp://homeassistant:1883 anona-bridge
//
// Env (also honored in positional mode): ANONA_REGION, ANONA_DEVICES (comma-
// separated deviceId allowlist; empty = all), ANONA_RTSP_LISTEN, ANONA_HTTP_LISTEN,
// ANONA_MQTT_URL/USER/PASS, ANONA_HAS_BATTERY, ANONA_TOKEN_REFRESH (e.g. 6h).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Darkhand81/anona-ha-bridge/internal/supervisor"
)

func main() {
	// ANONA_DEBUG=1 turns on pion's verbose ICE/WebRTC logging (to stderr) by
	// setting PION_LOG_DEBUG, which pion's default logger reads at startup. Set
	// it before anything creates the WebRTC API.
	if os.Getenv("ANONA_DEBUG") == "1" && os.Getenv("PION_LOG_DEBUG") == "" {
		os.Setenv("PION_LOG_DEBUG", "all")
	}

	opt := optionsFromEnv()

	var email, password string
	switch {
	case len(os.Args) >= 4:
		// legacy positional single-camera form
		email, password = os.Args[1], os.Args[2]
		opt.Devices = []string{os.Args[3]}
		opt.ForcePath = "cam"
		if len(os.Args) > 4 {
			opt.Region = os.Args[4]
		}
		if len(os.Args) > 5 {
			opt.RTSPListen = os.Args[5]
		}
	case os.Getenv("ANONA_EMAIL") != "":
		email = os.Getenv("ANONA_EMAIL")
		password = os.Getenv("ANONA_PASSWORD")
	default:
		fmt.Println("usage: anona-bridge <email> <password> <deviceId> [region] [rtspListen]")
		fmt.Println("   or: set ANONA_EMAIL / ANONA_PASSWORD (+ optional ANONA_* env) and run with no args")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := supervisor.Run(ctx, email, password, opt); err != nil {
		fmt.Printf("FATAL: %v\n", err)
		os.Exit(1)
	}
}

func optionsFromEnv() supervisor.Options {
	opt := supervisor.Options{
		Region:     envOr("ANONA_REGION", "INTL"),
		RTSPListen: envOr("ANONA_RTSP_LISTEN", ":8554"),
		RTSPUser:   os.Getenv("ANONA_RTSP_USER"),
		RTSPPass:   os.Getenv("ANONA_RTSP_PASS"),
		HTTPListen: envOr("ANONA_HTTP_LISTEN", ":8080"),
		MQTTURL:    os.Getenv("ANONA_MQTT_URL"),
		MQTTUser:   os.Getenv("ANONA_MQTT_USER"),
		MQTTPass:   os.Getenv("ANONA_MQTT_PASS"),
		HasBattery: os.Getenv("ANONA_HAS_BATTERY") == "1",
	}
	if d := strings.TrimSpace(os.Getenv("ANONA_DEVICES")); d != "" {
		opt.Devices = strings.Split(d, ",")
	}
	if r := os.Getenv("ANONA_TOKEN_REFRESH"); r != "" {
		if dur, err := time.ParseDuration(r); err == nil {
			opt.RefreshEvery = dur
		}
	}
	return opt
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
