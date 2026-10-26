// Command verify exercises the Go cloud + control foundation against a live
// camera: login, open the control channel, read IPCameraStatus, nudge PTZ.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/Darkhand81/anona-ha-bridge/internal/cloud"
	"github.com/Darkhand81/anona-ha-bridge/internal/control"
	"github.com/Darkhand81/anona-ha-bridge/internal/pb"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Println("usage: verify <email> <password> <deviceId> [region]")
		os.Exit(2)
	}
	email, password, deviceID := os.Args[1], os.Args[2], os.Args[3]
	region := "INTL"
	if len(os.Args) > 4 {
		region = os.Args[4]
	}
	cl := cloud.New(region, uuid.NewString())
	must(cl.Login(email, password), "login")
	fmt.Println("login OK, token acquired")

	wa, err := cl.WebsocketAddress()
	must(err, "websocketAddress")
	ch, err := control.Dial(control.WSInfo(wa))
	must(err, "control dial")
	defer ch.Close()
	fmt.Println("control channel handshake OK")

	// GET_IPCAMERA_STATUS
	reply, err := ch.Send(deviceID, pb.GetStatus(), 20*time.Second)
	must(err, "get status")
	top, _ := pb.Decode(reply)
	ipc := pb.Get(top, pb.FIPCamera)
	if ipc != nil && ipc.Sub != nil {
		st := pb.Get(ipc.Sub, pb.FIPCameraStatus)
		if st != nil && st.Sub != nil {
			if bat := pb.Get(st.Sub, 1); bat != nil && bat.Sub != nil {
				if b := pb.Get(bat.Sub, 1); b != nil && b.Sub != nil {
					if cap := pb.Get(b.Sub, 1); cap != nil {
						fmt.Printf("status OK: battery %d%%\n", cap.V)
					}
				}
			}
			if nv := pb.Get(st.Sub, 6); nv != nil {
				fmt.Println("  night_vision present")
			}
		}
	}

	// PTZ nudge: right click, then back left.
	rc, err := ptz(ch, deviceID, pb.DirRight)
	fmt.Printf("PTZ right -> err=%v error_code=%d\n", err, rc)
	time.Sleep(time.Second)
	rc, err = ptz(ch, deviceID, pb.DirLeft)
	fmt.Printf("PTZ left  -> err=%v error_code=%d\n", err, rc)
}

func ptz(ch *control.Channel, deviceID string, dir int) (uint64, error) {
	reply, err := ch.Send(deviceID, pb.PTZControl(dir, pb.ActClick), 15*time.Second)
	if err != nil {
		return 0, err
	}
	top, _ := pb.Decode(reply)
	if ec := pb.Get(top, pb.FErrorCode); ec != nil {
		return ec.V, nil
	}
	return 0, nil // error_code absent = NO_ERROR
}

func must(err error, what string) {
	if err != nil {
		fmt.Printf("FATAL %s: %v\n", what, err)
		os.Exit(1)
	}
}
