// Command rtspprobe reads an RTSP stream (like go2rtc would) and reports the
// codec and packet/keyframe counts, to verify the bridge's output.
package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

func main() {
	url := os.Args[1]
	u, err := base.ParseURL(url)
	if err != nil { panic(err) }
	c := gortsplib.Client{}
	if err := c.Start(u.Scheme, u.Host); err != nil { panic(err) }
	defer c.Close()
	desc, _, err := c.Describe(u)
	if err != nil { fmt.Println("describe:", err); os.Exit(1) }
	for _, m := range desc.Medias {
		for _, f := range m.Formats {
			fmt.Printf("media %s format %T payloadType=%d\n", m.Type, f, f.PayloadType())
		}
	}
	if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil { panic(err) }
	var pkts, bytesTot int64
	c.OnPacketRTPAny(func(m *description.Media, f format.Format, pkt *rtp.Packet) {
		atomic.AddInt64(&pkts, 1)
		atomic.AddInt64(&bytesTot, int64(len(pkt.Payload)))
	})
	if _, err := c.Play(nil); err != nil { panic(err) }
	time.Sleep(12 * time.Second)
	fmt.Printf("received %d RTP packets, %d payload bytes in 12s\n", atomic.LoadInt64(&pkts), atomic.LoadInt64(&bytesTot))
}
