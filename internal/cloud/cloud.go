// Package cloud is the Anona HTTPS client: request signing (reproduced from
// libbanyackey.so), login, and the device/websocket/ICE lookups.
// See docs/PROTOCOL.md §1.
package cloud

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Region hosts (assets/config/app_config.json). This build: channel 72001001.
var Hosts = map[string]string{
	"INTL":   "https://us-api.anonasecurity.com",
	"EU":     "https://eu-api.anonasecurity.com",
	"OTHERS": "https://int-api.anonasecurity.com",
}

const (
	Channel    = 72001001
	DeviceType = 72 // app type; also the WsRequestInfo.deviceType for control
	// SECRET[72001001] from the banyackey table (full table in reference/).
	secret = "329he3wihfeibfk3209(&*^%dehsi3)*&"
)

func md5hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

// Sign implements the native s(): md5(input + secret), lowercase hex.
func Sign(input string) string { return md5hex(input + secret) }

type Client struct {
	Host string
	UUID string
	hc   *http.Client

	tmu            sync.Mutex // guards Token + creds
	token          string
	email, passwrd string // kept so the session can re-login when the token expires
}

func New(region, uuid string) *Client {
	return &Client{Host: Hosts[region], UUID: uuid, hc: &http.Client{Timeout: 25 * time.Second}}
}

// Token returns the current session token (thread-safe).
func (c *Client) Token() string {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	return c.token
}

func nowMS() int64 { return time.Now().UnixMilli() }

func (c *Client) post(path string, body map[string]any) (map[string]any, error) {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.Host+path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("UA", "okhttp/4.12.0")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("non-JSON %d: %s", resp.StatusCode, string(raw[:min(200, len(raw))]))
	}
	return out, nil
}

// signed builds a token-authenticated body: sig = md5(token+uuid+channel+ts + secret).
func (c *Client) signed(params map[string]any) map[string]any {
	ts := nowMS()
	b := map[string]any{"channel": Channel, "ts": ts, "uuid": c.UUID}
	for k, v := range params {
		b[k] = v
	}
	inp := c.UUID + fmt.Sprint(Channel)
	if tok := c.Token(); tok != "" {
		b["token"] = tok
		inp = tok + inp
	}
	b["sig"] = Sign(inp + fmt.Sprint(ts))
	return b
}

// Login signs the account-login variant, stores the token, and remembers the
// credentials so the session can re-login when the token later expires.
func (c *Client) Login(email, password string) error {
	c.tmu.Lock()
	c.email, c.passwrd = email, password
	c.tmu.Unlock()
	return c.login()
}

// login performs the actual userLoginPwd exchange using the stored credentials.
func (c *Client) login() error {
	c.tmu.Lock()
	email, password := c.email, c.passwrd
	c.tmu.Unlock()
	if email == "" {
		return fmt.Errorf("no stored credentials")
	}
	ts := nowMS()
	hashedPw := Sign(password)                                    // passWord = md5(pw+secret)
	c10 := fmt.Sprintf("%d%snull%s", DeviceType, email, hashedPw) // auth_string "%d%snull%s"
	body := map[string]any{
		"ts": ts, "channel": Channel, "deviceType": DeviceType,
		"sig": Sign(c10 + fmt.Sprint(ts)), "email": email,
		"passWord": hashedPw, "uuid": c.UUID,
	}
	out, err := c.post("/accountApi/V3/userLoginPwd", body)
	if err != nil {
		return err
	}
	rb, _ := out["resultBodyObject"].(map[string]any)
	tok, _ := rb["token"].(string)
	if tok == "" {
		return fmt.Errorf("login failed: %v", out["errorMessage"])
	}
	c.tmu.Lock()
	c.token = tok
	c.tmu.Unlock()
	return nil
}

// Relogin refreshes the session token using the stored credentials.
func (c *Client) Relogin() error { return c.login() }

// Call signs and posts a request. If the response signals an expired/invalid
// token, it re-logs-in once and retries so long-running sessions survive token
// rotation without a restart.
func (c *Client) Call(path string, params map[string]any) (map[string]any, error) {
	out, err := c.post(path, c.signed(params))
	if err != nil {
		return out, err
	}
	if isAuthExpired(out) {
		if rErr := c.Relogin(); rErr != nil {
			return out, fmt.Errorf("token expired and re-login failed: %w", rErr)
		}
		return c.post(path, c.signed(params))
	}
	return out, nil
}

// isAuthExpired reports whether a response indicates the token is no longer
// accepted (so a re-login + retry is warranted). The device cloud returns
// errorCode -1 "sig not passed" for a stale token, and other auth failures carry
// "token"/"login" in the message.
func isAuthExpired(out map[string]any) bool {
	if out == nil {
		return false
	}
	code, _ := out["errorCode"].(float64)
	msg, _ := out["errorMessage"].(string)
	m := strings.ToLower(msg)
	switch {
	case code == -1:
		return true
	case strings.Contains(m, "token"), strings.Contains(m, "not login"),
		strings.Contains(m, "sig not"), strings.Contains(m, "unauthoriz"):
		return true
	}
	return false
}

// StartRefresher proactively re-logs-in every interval until stop is closed, so
// the token is rotated well before any server-side expiry. Returns immediately;
// runs in the background.
func (c *Client) StartRefresher(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := c.Relogin(); err != nil {
					fmt.Printf("[cloud] token refresh failed: %v\n", err)
				}
			}
		}
	}()
}

type Device struct {
	DeviceID, Model, NickName, IP string
	Type, Module                  int
	Channel                       int64
}

func (c *Client) DevicesInHome(homeID string) ([]Device, error) {
	out, err := c.Call("/anona/device/api/getDeviceListByHomeId", map[string]any{"homeId": homeID})
	if err != nil {
		return nil, err
	}
	arr, _ := out["resultBodyObject"].([]any)
	var ds []Device
	for _, it := range arr {
		m, _ := it.(map[string]any)
		d := Device{DeviceID: str(m["deviceId"]), Model: str(m["model"]), NickName: str(m["deviceNickName"]), IP: str(m["ip"])}
		d.Type = int(num(m["type"]))
		d.Module = int(num(m["module"]))
		d.Channel = int64(num(m["channel"]))
		ds = append(ds, d)
	}
	return ds, nil
}

// HomeIDs returns every home id in the account (own + shared).
func (c *Client) HomeIDs() ([]string, error) {
	out, err := c.Call("/AnonaHomeApi/getAnonaHomeCountInfoList", map[string]any{})
	if err != nil {
		return nil, err
	}
	arr, _ := out["resultBodyObject"].([]any)
	var ids []string
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if id := str(m["homeId"]); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// FindDevice locates a device across all homes by id.
func (c *Client) FindDevice(deviceID string) (Device, error) {
	homes, err := c.HomeIDs()
	if err != nil {
		return Device{}, err
	}
	for _, h := range homes {
		ds, err := c.DevicesInHome(h)
		if err != nil {
			continue
		}
		for _, d := range ds {
			if d.DeviceID == deviceID {
				return d, nil
			}
		}
	}
	return Device{}, fmt.Errorf("device %s not found in account", deviceID)
}

type WSAddr struct{ Address, Token, AESKeyHex string }

func (c *Client) WebsocketAddress() (WSAddr, error) {
	out, err := c.Call("/anonaWebsocketApi/getWebsocketAddress", map[string]any{})
	if err != nil {
		return WSAddr{}, err
	}
	rb, _ := out["resultBodyObject"].(map[string]any)
	return WSAddr{Address: str(rb["websocketAddress"]), Token: str(rb["websocketToken"]), AESKeyHex: str(rb["websocketAesKey"])}, nil
}

type ICE struct {
	Stun  string
	Turns []TurnInfo
}
type TurnInfo struct{ Address, Username, Password string }

func (c *Client) IceConfig(deviceID string) (ICE, error) {
	out, err := c.Call("/anonaWebRtcApi/getIceConfigV2", map[string]any{"deviceId": deviceID})
	if err != nil {
		return ICE{}, err
	}
	rb, _ := out["resultBodyObject"].(map[string]any)
	ice := ICE{Stun: str(rb["stunAddress"])}
	if arr, ok := rb["turnInfoList"].([]any); ok {
		for _, it := range arr {
			m, _ := it.(map[string]any)
			ice.Turns = append(ice.Turns, TurnInfo{Address: str(m["turnAddress"]), Username: str(m["userName"]), Password: str(m["password"])})
		}
	}
	return ice, nil
}

func (c *Client) Online(deviceID string) (bool, error) {
	out, err := c.Call("/anona/device/api/getDeviceOnlineStatus", map[string]any{"deviceId": deviceID})
	if err != nil {
		return false, err
	}
	rb, _ := out["resultBodyObject"].(map[string]any)
	on, _ := rb["online"].(bool)
	return on, nil
}

func str(v any) string { s, _ := v.(string); return s }
func num(v any) float64 { f, _ := v.(float64); return f }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
