# Anona protocol notes (confirmed on the wire)

Byte-level findings as they are verified against the real service and camera.
Design rationale is in [DESIGN.md](DESIGN.md).

## 1. HTTPS request signing — CONFIRMED 2026-10-25

Reproduced from `libbanyackey.so` (arm64, app v1.7.2) and verified against the live API.

**Algorithm.** The native export `s(JNIEnv*, jobject, long channel, jstring input)` returns

```
sig = md5( input + SECRET[channel] )          # lowercase hex, 32 chars
```

`SECRET[channel]` is chosen from a 120-entry table: an int32 key array at vaddr
`0x14fb0` and a parallel pointer array of secret strings at `0x47728` (filled by
R_AARCH64_RELATIVE relocations). The code linearly scans the key array for
`channel` and uses the secret at the same index. Full table:
[`reference/banyackey_secret_table.json`](../reference/banyackey_secret_table.json).

**This build** (`com.banyac.anona.int`): `channel = 72001001`, `deviceType = 72`
(both from string resources `app_channel` / `app_type`), so

```
SECRET = "329he3wihfeibfk3209(&*^%dehsi3)*&"
```

**What `input` is.** The Java layer builds it, then appends `ts`:

- Generic API (`warpRequestBody`, device/status/webrtc calls):
  `input = [token] + uuid + str(channel) + str(ts)` — token only when authenticated.
- Account login (`userLoginPwd`): a different shape (below).

`uuid` is any stable per-client id (the bridge picks its own). `ts` is epoch ms
(the app subtracts a server-clock delta; 0 delta works).

**Login request.** `POST /accountApi/V3/userLoginPwd`, JSON body:

```
passWord = md5(password + SECRET)                         # password is pre-hashed
c10      = "72" + email + "null" + passWord               # auth_string "%d%snull%s"
sig      = md5(c10 + str(ts) + SECRET)
body     = { ts, channel:72001001, deviceType:72, sig, email, passWord, uuid }
```

Headers: `Content-Type: application/json`, `Accept-Language`, `UA`.

**Region hosts** (`assets/config/app_config.json`): INTL → `https://us-api.anonasecurity.com`,
EU → `https://eu-api.anonasecurity.com`, OTHERS → `https://int-api.anonasecurity.com`,
RUSSIA/PREVIEW → `pp-openapi.70mai.com.cn`. (The old `anonaapp.70mai.com.cn` is the
China build and returns 405 for this app.)

**Verification.** Dummy login on all three main hosts:
correct sig → `{"error":true,"errorCode":500200,"errorMessage":"can Not find accountUser"}`;
corrupted sig → `{"error":true,"errorCode":-1,"errorMessage":"sig not passed"}`.
Script: [`tools/probe/anona_sign.py`](../tools/probe/anona_sign.py) (run with no args for the self-test;
`anona_sign.py <email> <password> [INTL|EU|OTHERS]` to actually log in).

Note `errorCode 500200` = sig valid, account absent. So the signing algorithm is
proven without any real credentials.

## 1a. Cloud bootstrap chain — CONFIRMED 2026-10-25

Logged in with a real account (region INTL, `us-api.anonasecurity.com`) and walked
the whole pre-media chain. All calls use the generic token-signed envelope
(`anona_sign.build_signed_body`). Confirmed working:

| Call | Result |
|---|---|
| `POST /accountApi/V3/userLoginPwd` | returns `token`, `userID`, `channel` |
| `POST /AnonaHomeApi/getAnonaHomeNameList` | homes (own + shared); body `{}` |
| `POST /anona/device/api/getDeviceListByHomeId` `{homeId}` | device records: `deviceId, type, module, channel, model, ip, deviceNickName, lastOnlineTs` |
| `POST /anona/device/api/getDeviceOnlineStatus` `{deviceId}` | `{online, lastAliveTs}` |
| `POST /anonaWebsocketApi/getWebsocketAddress` | body `{}` (per-user, **not** per-device); returns `websocketAddress` (`wss://websocketforappv2.anonasecurity.com:10006/websocket`), `websocketToken`, `websocketAesKey` |
| `POST /anonaWebRtcApi/getIceConfigV2` `{deviceId}` | `stunAddress`, `turnInfoList[{turnAddress,userName,password}]`, ipv6 variants; TURN user = `<expiry>:<deviceId>` |

Notes:
- `getWebsocketAddress` rejects a `deviceId` field (`BaseTokenDTO` has none) — send `{}`.
- `getAnonaDeviceStatus` needs a param other than `deviceId` (errorCode 502440); live
  status comes over the control channel anyway, so it is not needed.
- The test cameras are two **HM1010** Pano units (the model this bridge targets),
  shared into the account from another user's home.

## 1b. Control-channel crypto — CONFIRMED 2026-10-25

`websocketAesKey` is a 32-char hex string. `th.a.g()` hex-decodes it to **16 bytes**,
so the control channel is **AES-128-CBC / PKCS5**, with a **fixed IV** of
`00 01 02 03 04 05 06 07 08 09 0a 0b 0c 0d 0e 0f` (`th/c.java`). Frame = 
`hex( AES128CBC(key, IV, json) ‖ CRC32(ciphertext) )`; CRC32 is the standard
reflected table in `th/b.java`. This resolves the key-length and IV unknowns from
DESIGN.md §8.2.

## 1c. Control channel round-trip — CONFIRMED 2026-10-25

Full `GET_IPCAMERA_STATUS` round-trip against camera `651f50f69d134b6ab061c0e02ed258dd`
(192.168.1.213, HM1010). Client: [`tools/probe/anona_control.py`](../tools/probe/anona_control.py)
(+ `wsmin.py`, a stdlib TLS WebSocket client; needs `pycryptodome`).

1. `getWebsocketAddress` → `wss://websocketforappv2.anonasecurity.com:10006/websocket`,
   `websocketToken`, `websocketAesKey` (16 bytes hex).
2. Open WSS (normal cert verification). First frame is **plaintext JSON**:
   `{"handshakeToken": <websocketToken>, "operateId": 10000000, "ts": <ms>}`.
   Reply `{"ackCode":200,"isAck":true,"operateId":"10000000"}` = authed.
3. After auth every frame (both ways) is `hex( AES128CBC(key,IV,PKCS7(json)) + CRC32_LE(ct) )`.
4. Request JSON `WsRequestInfo`:
   `{ts, deviceId, deviceType:72, operateId:<uuid>, target:2, content:<hex Packet>}`.
   - **`deviceType` is the app type `72`** (R.string.app_type), NOT the camera's type (96).
     With 96 the server acks `10000` and the device never sees it; with 72 it acks `200`.
   - `target:2` routes to the device. `operateId` is any unique string (UUID works).
   - `content` for status = `Packet{type=IPCamera(50), id=4}` = bytes `08 32 10 04`.
5. Server sends an **ack** frame (`ackCode:200` delivered / `10000` not delivered / `20001` failed),
   then the device's **reply** frame: `ackCode:null, source:2, deviceId:<id>`, same `operateId`,
   `content` = hex `Packet` whose `ipcamera`(52).`ip_camera_status`(3) is the `IPCameraStatus`.
6. Keep-alive: WS ping every ~60 s.

Live decode confirmed sane values: battery 66%, privacy off, night vision IR on, audio on
(high), indicator LED on, motion detection on with a detection area, alarm voice "PLEASE_LEAVE".
This closes DESIGN.md §8.2. The same channel carries every control command — swap the `content`.

### Write control (PTZ) — CONFIRMED 2026-10-25

`CONTROL_PAN_TILT` (id 107) moves the camera. Packet =
`Packet{type=50, id=107, ipcamera{ pan_tilt_control(80){ direction(1), action_type(2) } }}`;
`direction` 1=up 2=down 3=left 4=right 5=stop, `action_type` 1=click (one step) 2=continuous
(move until a `stop` is sent). Reply packet = `08 32 10 6b 88 03 00` =
`Packet{id=107, error_code(49)=0 (NO_ERROR)}`. Verified: 12 steps (left/right/up/down) on the
192.168.1.213 camera each returned NO_ERROR and the unit moved. Run:
`anona_control.py <email> <pw> INTL <deviceId> ptz <up|down|left|right> [click|continuous]`.

PTZ step buttons (and continuous+stop) are the control surface. The dedicated
`GET_PAN_TILT_CURRENT_POSITION` (id 109) returns a stub on this model, but the
absolute position **is** reported in `IPCameraStatus.pan_tilt_position` (field 51):
`current_position{horizontal, vertical}` in tenths of a degree (e.g. 1799 = 179.9°),
plus `motor_calibration_pending`. The bridge exposes these as pan/tilt sensors.
(Corrected 2026-10-25: an earlier note here wrongly said position was unavailable.)
Other setters (privacy id 19, night vision 23, indicator LED 33, reboot 112,
snapshot 115) follow the same pattern: build the packet with
`camera_packet(id, ipcamera_body)` and read `error_code`.

## 1d. WebRTC live media — CONFIRMED 2026-10-25

Full live session negotiated and video pulled back out as RTSP, in Go
([`cmd/anona-bridge`](../cmd/anona-bridge), [`internal/media`](../internal/media)).

1. `getIceConfigV2` → STUN + two TURN servers (per-device TURN creds).
2. pion PeerConnection (MaxBundle, RTCP-mux require), one recvonly video + one
   recvonly audio transceiver; MediaEngine offers H.264, H.265 and Opus.
3. Create offer, **strip `a=extmap:` lines** (regex `^a=extmap:.*\r?\n`),
   **zlib-deflate** the SDP (stdlib `compress/zlib` = Java `Deflater` default),
   send as `WEB_RTC_START` (id 96) `webrtc_start_request.sdp_offer_compressed`
   with `stun_address`, `turn_info_list`, `p2p_live_info{live:true}`.
4. Reply (same operateId) = `Packet.ipcamera.webrtc_start_response(71)`
   `{error_code(1), sdp_answer_compressed(2)}`; inflate (2) → answer SDP →
   `SetRemoteDescription`.
5. Trickle ICE both ways: local candidates → `WEB_RTC_CANDIDATE` (id 97); the
   camera's candidates arrive as control-channel **pushes** (Packet id 97,
   `webrtc_candidate(72){candidate(3), sdp_mid(4), sdp_m_line_index(5)}`).
6. On `connected`, send `P2P_LIVE_STREAM_START` (id 100) over the control channel;
   media then flows on the WebRTC tracks. The camera also opens data channels
   `proto` / `data` / `mass` (not needed for the live view).

Observed: the HM1010 negotiates **H.265** video (payload type 98) and **Opus**
audio (48 kHz, pt 111). The bridge republishes both at `rtsp://host:8554/cam`;
`ffmpeg` records an HEVC+Opus stream and a reader pulls ~1200 video packets /
1.37 MB in 12 s. This closes DESIGN.md §8.4.

### Audio + talkback — CONFIRMED 2026-10-25

- **Listen:** the audio m-line is negotiated **sendrecv**; the camera's Opus is
  received via `OnTrack` and republished as the RTSP audio media. Verified: a 6 s
  capture decodes to 48 kHz stereo Opus with a real (quiet-room) signal.
- **Talk:** the offer carries a local Opus track (`KvsAudioTrack` in
  `KvsLocalMediaStream`). To talk, send `P2P_VOICE_START` (id 102,
  `p2p_voice_start_request(120){client_type,client_id}`) over the control channel,
  then write Opus to the track; stop with `P2P_VOICE_STOP` (id 103,
  `p2p_voice_stop(122)`). The reply's `p2p_voice_start_response(121).error_code(1)`
  is 0 on success (8 = `BUSY_IN_VOICE_TALK` if another talker holds it). Verified:
  `P2P_VOICE_START` accepted (NO_ERROR) and Opus streamed to the camera, both via
  the `talk` command and the bridge's `POST /talk` HTTP endpoint, concurrently with
  the live RTSP stream. **Audible output confirmed on the camera speaker.**

  Gotcha: each RTP audio payload must be one Opus packet (one 20 ms frame).
  Transcode with `ffmpeg … -c:a libopus -page_duration 20000 -f ogg` so each Ogg
  page carries a single packet; without it ffmpeg packs ~1 s per page and the
  camera gates the voice session fine but decodes nothing (silent speaker).

## 2. LAN surface of the camera — CHECKED 2026-10-25

Camera at `192.168.1.213` answers ICMP but exposes **no** open TCP on the common
set scanned (22, 23, 80, 443, 554, 1883, 1935, 5000, 8000, 8080, 8443, 8554,
8888, 8899, 9000, 34567, 37777, 49152). No RTSP/ONVIF/HTTP locally. This matches
the design's assumption that control and media are cloud/WebRTC only; there is no
local control path to use. (A full 1-65535 sweep has not been run.)

## 3. Still to confirm

Next: log in with the real account, `getDeviceListByHomeId` to read the camera's
`DeviceTypeInfo` (deviceId, certs, aesKey, channel), then `getWebsocketAddress` +
the AES/CRC control-channel handshake and a `GET_IPCAMERA_STATUS`. See DESIGN.md §8.

## 3. Status quirks

- **Battery is boilerplate on mains models.** `IPCameraStatus.battery` (a
  `DeviceStatus`) is always present, but on the HM1010 (mains pan/tilt) it reads a
  static 66% with `charge_status=NOT_CHARGING` and `charge_config.mode=SOLAR` —
  none of it real (confirmed by the owner; value unchanged across polls). The app
  gates battery UI by a per-model feature list, which the cloud does not expose to
  shared users. The bridge therefore hides battery unless the model is known to
  have one (`ha.ModelHasBattery`) or `ANONA_HAS_BATTERY=1` is set.
- **SD storage** is gated by the SD *slot* (the model reporting `memory_storage`),
  not by a card being inserted. With no card the bridge still exposes an **SD Card**
  status sensor (reads "No card"/`SD_NON_EXISTENT`) and leaves Storage Free/Total
  unavailable; inserting a card is picked up by the 30 s status poll and fills them
  in automatically, no restart needed.

## 4. Status-field audit (2026-10-25)

Audited every IPCameraStatus field against the live HM1010. Key points:

- **error_code is not a capability signal.** The firmware returns NO_ERROR for
  SET of features it doesn't physically have (white light, flip, siren all
  accepted). So the bridge gates entities by **which fields/subfields the status
  actually reports** (`cam.capsFromRaw`), mirroring the app's per-model gating.
- **Boilerplate / absent on this model (hidden):** battery + charge_config
  (static 66%, mode SOLAR); SD size while no card is inserted; `flip_horizontal/vertical`
  (absent — the model uses `image_rollover_mode` rotation instead); AI
  `vehicle`/`package` detection (absent — `ai_detection` reports only person,
  animal, move).
- **Real but previously missed (now exposed):** `pan_tilt_position` (field 51) and
  `image_rollover_mode` rotation (0/90/180/270).
- **Confirmed real:** privacy, night vision, indicator LED, white light (color
  night-vision lamp), audio (+volume), motion/person/animal/move detection, sound
  + baby-cry detection, Wi-Fi RSSI (changes between polls).

Capability-gated discovery means another model that reports vehicle/package,
flip, a battery, or an SD card will automatically get those entities.
