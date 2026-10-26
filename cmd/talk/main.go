// Command talk sends an audio clip to the camera speaker (talkback).
// It transcodes the input to Ogg/Opus with ffmpeg, then streams it.
//
//	talk <email> <password> <deviceId> <audiofile> [region]
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/google/uuid"

	"github.com/Darkhand81/anona-ha-bridge/internal/cloud"
	"github.com/Darkhand81/anona-ha-bridge/internal/control"
	"github.com/Darkhand81/anona-ha-bridge/internal/media"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Println("usage: talk <email> <password> <deviceId> <audiofile> [region]")
		os.Exit(2)
	}
	email, password, deviceID, audio := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	region := "INTL"
	if len(os.Args) > 5 {
		region = os.Args[5]
	}

	// Transcode to 48 kHz mono Ogg/Opus.
	ogg := audio + ".talk.ogg"
	cmd := exec.Command("ffmpeg", "-nostdin", "-y", "-i", audio,
		"-ar", "48000", "-ac", "1", "-c:a", "libopus", "-b:a", "24k",
		"-page_duration", "20000", "-f", "ogg", ogg)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("ffmpeg transcode failed: %v\n%s\n", err, out)
		os.Exit(1)
	}
	defer os.Remove(ogg)

	clientID := uuid.NewString()
	cl := cloud.New(region, clientID)
	fatal(cl.Login(email, password), "login")
	ice, err := cl.IceConfig(deviceID)
	fatal(err, "ice")
	wa, err := cl.WebsocketAddress()
	fatal(err, "wsaddr")
	ch, err := control.Dial(control.WSInfo(wa))
	fatal(err, "control")
	defer ch.Close()

	sess, err := media.Start(cl, ch, deviceID, clientID, ice, media.NullSink{})
	fatal(err, "session")
	defer sess.Close()
	fmt.Println("session up; waiting for connection before talking")
	time.Sleep(6 * time.Second)

	f, err := os.Open(ogg)
	fatal(err, "open ogg")
	defer f.Close()
	fmt.Println("talking...")
	if err := sess.Talk(f); err != nil {
		fmt.Println("talk error:", err)
		os.Exit(1)
	}
	fmt.Println("talkback finished OK")
}

func fatal(err error, what string) {
	if err != nil {
		fmt.Printf("FATAL %s: %v\n", what, err)
		os.Exit(1)
	}
}
