// Package control is the Anona cloud control channel: a WebSocket carrying
// AES-128-CBC + CRC32 framed JSON, each wrapping a hex protobuf Packet.
// See docs/PROTOCOL.md §1b/§1c.
package control

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Darkhand81/anona-ha-bridge/internal/cloud"
)

var iv = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

const AppDeviceType = cloud.DeviceType // 72
const TargetDevice = 2

func pkcs7(b []byte) []byte {
	n := 16 - len(b)%16
	for i := 0; i < n; i++ {
		b = append(b, byte(n))
	}
	return b
}
func unpkcs7(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	n := int(b[len(b)-1])
	if n <= 0 || n > len(b) {
		return b
	}
	return b[:len(b)-n]
}

func frame(key, plain []byte) string {
	padded := pkcs7(append([]byte{}, plain...))
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(mustAES(key), iv).CryptBlocks(ct, padded)
	var crc [4]byte
	c := crc32.ChecksumIEEE(ct)
	crc[0] = byte(c)
	crc[1] = byte(c >> 8)
	crc[2] = byte(c >> 16)
	crc[3] = byte(c >> 24)
	return hex.EncodeToString(append(ct, crc[:]...))
}

func unframe(key []byte, hexs string) ([]byte, error) {
	raw, err := hex.DecodeString(hexs)
	if err != nil || len(raw) < 20 {
		return nil, fmt.Errorf("bad frame")
	}
	ct := raw[:len(raw)-4]
	if crc32.ChecksumIEEE(ct) != uint32(raw[len(raw)-4])|uint32(raw[len(raw)-3])<<8|uint32(raw[len(raw)-2])<<16|uint32(raw[len(raw)-1])<<24 {
		return nil, fmt.Errorf("crc mismatch")
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(mustAES(key), iv).CryptBlocks(pt, ct)
	return unpkcs7(pt), nil
}

func mustAES(key []byte) cipher.Block { b, _ := aes.NewCipher(key); return b }

// Reply is a decoded control-channel response.
type Reply struct {
	OperateID string `json:"operateId"`
	AckCode   *int   `json:"ackCode"`
	Source    *int   `json:"source"`
	DeviceID  string `json:"deviceId"`
	Content   string `json:"content"`
}

// Push is a device-initiated frame (e.g. WEB_RTC_CANDIDATE, status report).
type Push struct {
	DeviceID string
	Packet   []byte // decoded hex content (protobuf Packet)
}

type Channel struct {
	ws     *websocket.Conn
	key    []byte
	mu     sync.Mutex
	wmu    sync.Mutex
	wait   map[string]chan Reply
	hmu    sync.Mutex
	pushCB []func(Push)
	closed bool
	done   chan struct{}
}

// Done is closed when the channel drops (read error, keepalive write error, or
// an explicit Close). Supervisors select on it to trigger a reconnect.
func (c *Channel) Done() <-chan struct{} { return c.done }

// OnPush registers a handler for device-initiated frames (candidates, events,
// status reports). Handlers are called concurrently per push.
func (c *Channel) OnPush(fn func(Push)) {
	c.hmu.Lock()
	c.pushCB = append(c.pushCB, fn)
	c.hmu.Unlock()
}

func (c *Channel) dispatch(p Push) {
	c.hmu.Lock()
	hs := append([]func(Push){}, c.pushCB...)
	c.hmu.Unlock()
	for _, h := range hs {
		go h(p)
	}
}

// Dial opens the control channel and completes the plaintext handshake.
func Dial(addr WSInfo) (*Channel, error) {
	key, err := hex.DecodeString(addr.AESKeyHex)
	if err != nil || len(key) != 16 {
		return nil, fmt.Errorf("bad aes key")
	}
	ws, _, err := websocket.DefaultDialer.Dial(addr.Address, nil)
	if err != nil {
		return nil, err
	}
	c := &Channel{ws: ws, key: key, wait: map[string]chan Reply{}, done: make(chan struct{})}
	// plaintext handshake
	hs, _ := json.Marshal(map[string]any{"handshakeToken": addr.Token, "operateId": 10000000, "ts": time.Now().UnixMilli()})
	if err := ws.WriteMessage(websocket.TextMessage, hs); err != nil {
		return nil, err
	}
	_, msg, err := ws.ReadMessage()
	if err != nil {
		return nil, err
	}
	var auth struct {
		AckCode   int `json:"ackCode"`
	}
	json.Unmarshal(msg, &auth)
	if auth.AckCode != 200 {
		return nil, fmt.Errorf("handshake rejected: %s", string(msg))
	}
	go c.readLoop()
	go c.keepAlive()
	return c, nil
}

// WSInfo mirrors cloud.WSAddr without importing a cycle.
type WSInfo struct{ Address, Token, AESKeyHex string }

func (c *Channel) readLoop() {
	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			c.failAll()
			return
		}
		pt, err := unframe(c.key, string(msg))
		if err != nil {
			continue
		}
		var r Reply
		if json.Unmarshal(pt, &r) != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.wait[r.OperateID]
		c.mu.Unlock()
		if ok {
			ch <- r
			continue
		}
		// device-initiated push with content
		if r.Content != "" {
			if b, err := hex.DecodeString(r.Content); err == nil {
				c.dispatch(Push{DeviceID: r.DeviceID, Packet: b})
			}
		}
	}
}

func (c *Channel) keepAlive() {
	t := time.NewTicker(50 * time.Second)
	defer t.Stop()
	for range t.C {
		c.wmu.Lock()
		err := c.ws.WriteMessage(websocket.PingMessage, nil)
		c.wmu.Unlock()
		if err != nil {
			c.failAll()
			return
		}
	}
}

// failAll marks the channel closed, releases every pending waiter, and closes
// Done. Idempotent: safe to call from the read loop and the keepalive at once.
func (c *Channel) failAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.done)
	for id, ch := range c.wait {
		close(ch)
		delete(c.wait, id)
	}
}

// Send a packet to a device and wait for its reply packet (ackCode==nil frame).
// Returns an error on ackCode 10000 (undelivered) / 20001 (failed) or timeout.
func (c *Channel) Send(deviceID string, packet []byte, timeout time.Duration) ([]byte, error) {
	opid := uuid.NewString()
	ch := make(chan Reply, 4)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("channel closed")
	}
	c.wait[opid] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.wait, opid); c.mu.Unlock() }()

	body, _ := json.Marshal(map[string]any{
		"ts": time.Now().UnixMilli(), "deviceId": deviceID, "deviceType": AppDeviceType,
		"operateId": opid, "target": TargetDevice, "content": hex.EncodeToString(packet),
	})
	c.wmu.Lock()
	err := c.ws.WriteMessage(websocket.TextMessage, []byte(frame(c.key, body)))
	c.wmu.Unlock()
	if err != nil {
		return nil, err
	}
	deadline := time.After(timeout)
	for {
		select {
		case r, ok := <-ch:
			if !ok {
				return nil, fmt.Errorf("channel closed")
			}
			if r.AckCode != nil {
				switch *r.AckCode {
				case 10000:
					return nil, fmt.Errorf("not delivered (ackCode 10000; wrong deviceType?)")
				case 20001:
					return nil, fmt.Errorf("device failed (ackCode 20001)")
				}
				// 200 = delivered to device; the content reply follows
				continue
			}
			if r.Content != "" {
				return hex.DecodeString(r.Content)
			}
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for %s", opid)
		}
	}
}

func (c *Channel) Close() error { c.failAll(); return c.ws.Close() }
