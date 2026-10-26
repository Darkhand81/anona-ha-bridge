# Anona Pano → Home Assistant bridge: design

Status: draft v2, written from static analysis of the Android app `com.banyac.anona.int` v1.7.2
(decompiled with jadx). Nothing here has been exercised against a real camera yet; section 8 lists
what has to be confirmed first, in the order it should be confirmed. File references are to the
jadx output; the protobuf schemas recovered from the app are in [`proto/`](../proto).

## 1. Goals

| Capability | Exposed in Home Assistant as |
|---|---|
| Live video + audio | RTSP `rtsp://<bridge>:8554/<camera>` consumed by go2rtc, which gives HA a camera entity with WebRTC/HLS/MSE and snapshots |
| PTZ: step and continuous up/down/left/right, stop, absolute position, calibrate | MQTT-discovered `button` entities, `number` entities for pan/tilt position, and a `ptz` command topic for automations |
| Talkback (audio to the camera speaker) | WebRTC two-way audio in the HA camera card via go2rtc backchannel, plus an HTTP endpoint on the bridge for TTS/media playback |
| Privacy mode, night vision mode, indicator LED, flip/rotate, reboot | `switch`, `select`, `button` |
| Motion / person / pet / vehicle / package / baby-cry events | `binary_sensor` and `event` entities |
| Battery, Wi‑Fi RSSI, storage state, PTZ position | `sensor` |

Non-goals for v1: cloud recordings, SD-card playback/timeline, onboarding and Wi‑Fi provisioning
(do that in the app), sharing/members, doorbell models.

## 2. How the app talks to the camera (findings)

The Pano is cloud-brokered for control and peer-to-peer (WebRTC) for media. There is no TUTK,
PPPP or Agora SDK in the app; media uses Google WebRTC (`libjingle_peerconnection_so.so`) and
nothing else. Camera control never goes over the LAN in normal operation.

```
                HTTPS (signed JSON)                  WSS (AES-CBC + CRC32 framed JSON,
 ┌───────┐ ───────────────────────────────►  ┌──────────────┐  protobuf hex inside)
 │  App  │   login, homes, devices, ICE,     │  Anona cloud │ ◄─────────────────────────► camera
 │       │   WS address + token + AES key    │ anonaapp.    │   control packets both ways,
 │       │ ◄──────────────────────────────── │ 70mai.com.cn │   status/event pushes
 └───┬───┘                                   └──────────────┘
     │  WebRTC (STUN/TURN from the cloud ICE config)
     │  audio+video tracks  ◄────────────────────────────────────────────────────────── camera
     │  data channels "proto", "data", "mass" (camera-created)   ◄──────────────────── camera
     │  local audio track "KvsAudioTrack" (talkback)  ───────────────────────────────► camera
```

### 2.1 HTTPS API (`https://anonaapp.70mai.com.cn`)

Retrofit interfaces: `com/banya/anona/ipc/data/IpcService.java`, `com/banya/anona/data/b.java`,
plus the account module. Every call is `POST` with a JSON body built by
`com.banya.base.ktx.data.a.warpRequestBody` (`com/banya/base/ktx/data/a.java`):

| Body field | Value |
|---|---|
| request params | endpoint specific, e.g. `deviceId`, `homeId` |
| `channel` | app channel id (constant per app build) |
| `uuid` | app install id |
| `ts` | `now_ms - serverDelta` (the app corrects for clock skew) |
| `token` | user token from login (when the endpoint needs auth) |
| `sig` | `BanyacKeyUtils.sign(channel, "<ts>", <token><uuid><channel>)` — **native code**, `libbanyackey.so` |

Extra headers: `Accept-Language`, `UA`. Responses are `{"error":bool,"errorCode":int,"resultBodyObject":...}`.

The request signature is the only part of the HTTP layer that is not plain JSON. It is computed in
a native library (`com/banyac/key/BanyacKeyUtils.java` → `System.loadLibrary("banyackey")`). "Banyac"
is 70mai's company and the same signing scheme is used by the 70mai dash-cam app, which has been
reverse-engineered publicly (see Alu's write-up linked in section 9), so the algorithm is likely
recoverable by reading that lib or by running it under `qemu-user`. This is step 1 of the plan.

Endpoints the bridge needs:

| Purpose | Path |
|---|---|
| Login (username + password) | `/accountApi/V3/userLoginPwd` |
| Homes | `/AnonaHomeApi/getAnonaHomeNameList` |
| Devices in a home (returns `DeviceTypeInfo`: `deviceId`, `model`, `deviceCert`, `userCert`, `aesKey`, `channel`, `port`, …) | `/anona/device/api/getDeviceListByHomeId` |
| Online state | `/anona/device/api/getDeviceOnlineStatus` |
| Last reported status (cloud copy of `IPCameraStatus`) | `/anona/device/status/api/getAnonaDeviceStatus` |
| **Control channel address, token and AES key** | `/anonaWebsocketApi/getWebsocketAddress` → `{websocketAddress, websocketToken, websocketAesKey}` |
| **STUN/TURN for WebRTC** | `/anonaWebRtcApi/getIceConfigV2` → `WebrtcInfo{stunAddress, turnInfoList[{turnAddress,userName,password}], ipv6StunAddress, ipv6TurnInfoList}` |
| Event history | `/anona/event/api/retrieveEventPagesV2`, `/anona/event/api/getEventInfoV2` |

### 2.2 Cloud control channel (WebSocket)

Implemented in `com/banya/anona/data/f.java` with the Java-WebSocket library (`jp.b`). Models in
`com/banya/anona/data/model/Ws*.java`. Sequence:

1. `GET getWebsocketAddress` → `wss://…`, `websocketToken`, `websocketAesKey` (hex string).
2. Open the socket. First message is **plaintext JSON**: `{"operateId":10000000,"handshakeToken":<websocketToken>,"ts":<ms>}`.
   The reply is JSON with `operateId == 10000000` on success.
3. After auth every text frame in both directions is `hex( AES-128/256-CBC-PKCS5(key=hex2bytes(websocketAesKey), iv=FIXED_IV, json) ‖ CRC32(ciphertext) )`.
   `FIXED_IV` is a constant in `th/c.java`; CRC32 is the standard table in `th/b.java`.
4. Request JSON (`WsRequestInfo`): `{"operateId":"<unique id>","deviceId":"…","deviceType":<int>,"target":2,"ts":<ms>,"content":"<hex of Packet protobuf>"}`.
   `target=2` is what the app passes for IP cameras (the `2` in `callWebSocket(2, …)`); the device type comes from `DeviceTypeInfo`.
5. Response JSON (`WsResponseInfo`): `{"operateId","deviceId","deviceType","source","ts","content":"<hex Packet>","ackCode"}`.
   The app matches on `operateId`; `ackCode == 20001` is treated as a failed delivery (most likely "device offline"). Unsolicited frames with no matching `operateId` are device pushes (status reports, events) and are fanned out as `WebSocketDataReceiverEvent(did, hexData, source)`.
6. Keep-alive: a WebSocket ping every 60 s. Default command timeout is 15 s; WebRTC start uses longer.

### 2.3 Packet protocol

`smart.proto` `Packet{type, id, payload}`. For the camera `type = IPCamera (50)`, `id` is the
`IPCamera.IPCameraID` number and the payload is the matching `IPCamera` oneof field. Replies carry
the same `id` and either the payload or `error_code` (`smart_common.proto` `ErrorCode`; note
`BUSY_IN_VOICE_TALK`, `BUSY_IN_MOTOR_RUNNING`). All builders are in
`com/banya/anona/ipc/data/PacketKt.java`. Every "client" style message sets `client_type = 0` and
`client_id = <app install id>`; the bridge should use its own stable id.

Commands the bridge uses:

| id | Command | Payload |
|---|---|---|
| 4 | `GET_IPCAMERA_STATUS` | empty → `IPCameraStatus` (battery, privacy, night vision, LED, detection config, storage, Wi‑Fi RSSI, `pan_tilt_position`) |
| 3 / 8 | `REPORT_IPCAMERA_STATUS[S]` | pushed by the camera on change |
| 1 / 2 | `REPORT_IPCAMERA_EVENT[S]` | `Event{event_id, property_param_list}`; `PropertyID.ALG_TAG` holds the `AlgTag` (people, vehicle, package, animal, motion, baby cry…) |
| 11 | `WAKE_UP_DEVICE` | `WakeupDevice.Request{wakeup_duration}` (battery models) |
| 19 | `SET_CAMERA_CONTROL` | `CameraControl{sleep_enable:false, privacy_mode_on}` |
| 21 | `SET_CAMERA_DISPLAY` | flip/rotate/HDR/watermarks |
| 23 | `SET_NIGHT_VISION` | `NightVision{mode, night_vision_on}` |
| 33 | `SET_INDICATOR_LIGHT` | `IndicatorLight{on}` |
| 96 | `WEB_RTC_START` | `WebRtcStart.Request{client_type, client_id, sdp_offer_compressed, stun_address, turn_info_list, p2p_live_info{live, resolutionChoice}}` → `Response{error_code, sdp_answer_compressed}` |
| 97 | `WEB_RTC_CANDIDATE` | `{client_type, client_id, candidate, sdp_mid, sdp_m_line_index}` (trickle; both directions) |
| 98 | `WEB_RTC_DISCONNECT` | `{client_type, client_id}` |
| 99 | `WEB_RTC_WAKE_UP` | wake a sleeping camera before `WEB_RTC_START` |
| 100 / 101 | `P2P_LIVE_STREAM_START` / `STOP` | `{client_type, client_id, resolutionChoice}` → `{error_code, video_codec, audio_codec}` |
| 102 / 103 | `P2P_VOICE_START` / `STOP` | `{client_type, client_id}` — talkback gate |
| 106 | `RESET_PAN_TILT` | empty (recalibrate) |
| 107 | `CONTROL_PAN_TILT` | `PanTiltControl{direction: UP/DOWN/LEFT/RIGHT/STOP, action_type: CLICK/CONTINUOUS}` |
| 108 | `CHANGE_PAN_TILT_POSITION` | `PanTiltPosition{target_position{horizontal, vertical}}`, units are **tenths of a degree** (app multiplies by 10) |
| 109 | `GET_PAN_TILT_CURRENT_POSITION` | → `PanTiltPosition{current_position, motor_calibration_pending}` |
| 112 | `REBOOT_CAMERA` | `RebootCamera{enable:true}` |
| 115 / 116 | `SNAPSHOT_REQUEST` / `RESPONSE` | `{image_id, upload_uri, content_type}` — the camera uploads a JPEG to the given URL; the bridge can serve its own upload URL |
| 7 | `KEEP_P2P_CONNECTION` | `{duration_seconds, signaling_channel_name}` — legacy KVS keepalive, see 2.6 |

### 2.4 WebRTC media session

Session manager: `com/banya/anona/ipc/aws/p0.java` (the `aws` name and `Kvs*` labels are left over
from the earlier Kinesis design, see 2.6). The app is the **offerer**; the camera answers.

1. `getIceConfigV2` → ICE servers (STUN + TURN with credentials).
2. `PeerConnection` config: Unified Plan, `bundlePolicy=MAXBUNDLE`, `rtcpMuxPolicy=REQUIRE`,
   `keyType=ECDSA`, `tcpCandidatePolicy=ENABLED`, `continualGatheringPolicy=GATHER_CONTINUALLY`.
   Field trials enable `WebRTC-Video-H26xPacketBuffer`, i.e. **H.265 over RTP** is expected.
3. Local media: one audio track `KvsAudioTrack` in stream `KvsLocalMediaStream`, created
   `setEnabled(false)` (it is the talkback microphone). Offer constraints `OfferToReceiveAudio=true`,
   `OfferToReceiveVideo=true`; dual-lens models add two `RECV_ONLY` video transceivers.
4. The offer SDP has `a=extmap:` lines stripped (regex `^a=extmap:.*\r?\n`), is **zlib-deflated**
   (`java.util.zip.Deflater`, default settings; `com/banya/anona/ipc/utils/UtilsKt.java`) and sent as
   `sdp_offer_compressed` in `WEB_RTC_START` over the cloud channel. The answer comes back
   zlib-deflated in `sdp_answer_compressed`. Local ICE candidates are buffered and sent with
   `WEB_RTC_CANDIDATE`; remote candidates arrive as `WEB_RTC_CANDIDATE` pushes. `use_trickle_ice`
   is negotiable; the fallback is to wait for gathering to finish and send a complete offer.
5. The camera adds: a primary `MediaStream` with the video and audio tracks, optionally a second
   stream with id `anona_webrtc_secondary_stream` (second lens), and three data channels it
   creates itself: `proto`, `data`, `mass`. The app also creates one called `Data channel` that is
   effectively unused.
   - `proto`: binary frames, each a bare `IPCameraP2P` protobuf (`ipcamera_p2p.proto`): live
     start/stop, voice start/stop, playback control, file list. Replies carry the same `id`.
   - `mass`: bulk data (`MassData{data_id, data_size, data_buffer, frame_info}`) for downloads and
     SD playback; a secondary stream is prefixed with the magic `EF BE AD DE`.
   - `data`: not used by the live path.
6. Once `proto` is open the app sends `P2P_LIVE_STREAM_START` through the cloud channel
   (`IpcRepository.liveStreamStart`), and media starts flowing on the WebRTC tracks. Video is
   H.264 or H.265, audio is Opus (the `P2P_CODEC`/`CODEC` enums allow AAC/G.711 too; the response
   tells you which).
7. Teardown: `P2P_LIVE_STREAM_STOP` + `WEB_RTC_DISCONNECT`, then close the PeerConnection.

### 2.5 Talkback

Enable the local audio track and send `P2P_VOICE_START` (cloud channel; `callStartPacket`). Audio
goes out on the already-negotiated `sendrecv` audio m-line as Opus. `P2P_VOICE_STOP` ends it. The
camera refuses a second talker with `BUSY_IN_VOICE_TALK`. An alternative `START_VOICE` exists on
the `proto` data channel (`startVoicePacket`) for older firmware.

### 2.6 Legacy and side paths worth knowing about

- **AWS Kinesis Video Streams signaling** (`P2PConnect.Request/Response` with `ak/sk/token`,
  `signaling_channel_arn`, `wss_endpoint`; `WebSocketClient` + `RtcEvent` with `SDP_OFFER`,
  `SDP_ANSWER`, `ICE_CANDIDATE` JSON). Still compiled in, but the live path above does not use it.
  Ignore unless a camera firmware answers `WEB_RTC_START` with `NOT_SUPPORT`.
- **Local TCP socket** (`com/banya/anona/ipc/data/IpcSocket.java`): connects to `DeviceTypeInfo.ip:port`
  and writes `hex( WifiBindHead(1,0,2,len) ‖ Packet )`, reads a hex reply and strips a 5-byte head.
  Used during Wi‑Fi binding. If the camera keeps this port open after binding, it would give a
  **LAN-only control path** with no cloud dependency; worth a port scan and one `GET_IPCAMERA_STATUS`
  attempt (step 3 in section 8). Not relied upon in the design.
- **Firebase push** (`ui/service/FirebaseService.java`) carries event notifications to phones; the
  bridge gets the same events from the WebSocket pushes, so FCM is not needed.

## 3. Bridge architecture

One daemon ("anona-bridge"), written in **Go**, plus go2rtc and an MQTT broker, which most HA installs
already have.

```
                       ┌──────────────────────────────── anona-bridge (Go) ────────────────────────────────┐
  Anona cloud  ◄─HTTPS─┤ cloud client: login, device list, ICE, WS address; token refresh                   │
  Anona cloud  ◄─WSS───┤ control channel: AES/CRC framing, operateId matching, push fan-out, keep-alive     │
  camera       ◄─RTC───┤ one WebRTC peer per camera (pion): tracks in, talkback track out, proto channel    │
                       │ RTSP server (gortsplib): RTP passthrough of H.264/H.265 + Opus, no transcoding     ├─RTSP─► go2rtc ─► HA camera entity
                       │ MQTT client: HA discovery, state topics, command topics (PTZ, switches, selects)   ├─MQTT─► HA MQTT integration
                       │ HTTP: /api/<cam>/snapshot.jpg, POST /api/<cam>/talk (audio file/stream), /healthz  ├─HTTP─► HA media/TTS
                       └────────────────────────────────────────────────────────────────────────────────────┘
```

Why Go: pion gives full control of the SDP (needed for the extmap stripping, H.265 and the custom
signaling), RTP can be forwarded without decoding (aiortc in Python decodes every frame and has no
H.265), and the whole thing ships as one static binary / HA add-on. The command-path parts (HTTPS,
WebSocket, protobuf) are protocol work that is quick to prototype in Python first; see section 6.

### 3.1 Components

**Cloud client.** Logs in once, caches the token on disk, lists homes and devices, filters to Pano
models, refreshes `getWebsocketAddress` and `getIceConfigV2` when they are rejected or expired.
Implements the request signature once the algorithm is confirmed.

**Control channel.** One WebSocket for all cameras. API: `Call(deviceId, Packet) (Packet, error)`
with timeout, and a `Pushes` channel. On any push with `type=IPCamera`, decode and route:
status → MQTT state, event → MQTT event + binary_sensor pulse, `WEB_RTC_CANDIDATE` → the right
peer. Reconnect with backoff; re-auth on open.

**Media session (per camera).** State machine `idle → waking → offering → connected → stopping`.
Starts on first RTSP client (go2rtc pulls on demand), stops after N seconds idle unless
"always on" is configured. Repackages RTP: video and audio tracks are forwarded to gortsplib's
server as `H264`/`H265` and `Opus` formats with the original timestamps. Data-channel `proto` is
kept open for voice start and for cameras that need `START_LIVE_STREAM` there.

**RTSP server.** One path per camera, `rtsp://bridge:8554/<slug>`. Accepts a **backchannel** audio
track (the ONVIF `a=sendonly` convention that go2rtc uses for two-way audio) and feeds it to the
camera's talkback track after sending `P2P_VOICE_START`. If that turns out to be awkward in
gortsplib, phase 4 falls back to the HTTP talk endpoint only.

**MQTT.** Home Assistant discovery under `homeassistant/…`, availability topic, one device per
camera (model, firmware, serial from `DeviceInfo`). Command topics are JSON; the bridge acks by
publishing the new state after the camera confirms (no optimistic state).

**HTTP.** Snapshot: either `SNAPSHOT_REQUEST` with the bridge's own upload URL (preferred, full
resolution, works while idle) or a frame grabbed from the running stream via go2rtc. Talk: accepts
WAV/PCM/MP3, transcodes to Opus with an embedded encoder, runs the voice session.

### 3.2 Home Assistant entity model (per camera)

| Entity | Source / command |
|---|---|
| `camera.<name>` | go2rtc stream `rtsp://bridge/<slug>` (configured in go2rtc, not MQTT) |
| `button.<name>_ptz_{up,down,left,right}` | `CONTROL_PAN_TILT{dir, CLICK}` |
| `button.<name>_ptz_stop`, `button.<name>_ptz_calibrate` | `CONTROL_PAN_TILT{STOP}`, `RESET_PAN_TILT` |
| `number.<name>_pan`, `number.<name>_tilt` (degrees) | `CHANGE_PAN_TILT_POSITION`; state from `pan_tilt_position.current_position / 10` |
| `anona/<slug>/ptz/set` command topic | `{"direction":"left","action":"continuous"}` for joystick-style automations |
| `switch.<name>_privacy_mode` | `SET_CAMERA_CONTROL{privacy_mode_on}`; state `camera_sleep.privacy_mode_on` |
| `select.<name>_night_vision` (off / ir / white_light / starlight / smart) | `SET_NIGHT_VISION`; state `night_vision.mode` |
| `switch.<name>_indicator_led` | `SET_INDICATOR_LIGHT` |
| `switch.<name>_flip_vertical`, `switch.<name>_flip_horizontal` | `SET_CAMERA_DISPLAY` |
| `button.<name>_reboot` | `REBOOT_CAMERA` |
| `binary_sensor.<name>_motion` (auto-off after 10 s), `_person`, `_pet`, `_vehicle`, `_package`, `_baby_cry` | `REPORT_IPCAMERA_EVENT` `ALG_TAG` |
| `event.<name>_detection` | same, with the raw tag as event type |
| `sensor.<name>_battery`, `_wifi_rssi`, `_storage_free`, `_pan`, `_tilt` | `IPCameraStatus` |
| `binary_sensor.<name>_online` / availability | `getDeviceOnlineStatus` + WS `ackCode 20001` |

### 3.3 Configuration

```yaml
# anona-bridge.yaml
anona:
  username: ...
  password: ...          # or token file
  region_host: anonaapp.70mai.com.cn
mqtt: { url: mqtt://homeassistant:1883, username: ..., password: ... }
rtsp: { listen: ":8554" }
http: { listen: ":8080" }
cameras:                 # optional; default = every Pano in the account
  - device_id: "..."
    slug: living_room
    always_on: false
    idle_timeout: 60s
```

go2rtc side: `streams: { living_room: rtsp://anona-bridge:8554/living_room }`.

## 4. Key flows

**Start live view.** go2rtc connects to RTSP → bridge `WEB_RTC_WAKE_UP` (if battery model) →
`getIceConfigV2` (cached) → new pion PeerConnection with recvonly video ×1–2, sendrecv audio →
create offer → strip `a=extmap` → deflate → `WEB_RTC_START` → inflate answer → set remote →
exchange `WEB_RTC_CANDIDATE` → wait for `proto` channel open → `P2P_LIVE_STREAM_START` →
on first RTP packet, announce RTSP tracks and start forwarding.

**PTZ step.** MQTT command → `CONTROL_PAN_TILT` → on `NO_ERROR` schedule
`GET_PAN_TILT_CURRENT_POSITION` after 1 s and publish the position (motor takes time; also poll while
`motor_calibration_pending`). `BUSY_IN_MOTOR_RUNNING` → retry once after 500 ms, then surface the error.

**Talkback.** RTSP backchannel opens (or HTTP talk begins) → ensure live session → `P2P_VOICE_START`
→ enable local audio track → forward Opus RTP → on end `P2P_VOICE_STOP` and disable the track.

**Events.** WS push `REPORT_IPCAMERA_EVENT` → publish `event` + set binary_sensor on → timer off.
Also poll `GET_IPCAMERA_STATUS` every 5 min as a safety net and on reconnect.

## 5. Repository layout

```
anona-ha-bridge/
  proto/                 recovered .proto files (done)
  docs/DESIGN.md         this file
  docs/PROTOCOL.md       byte-level notes as they are confirmed on the wire
  tools/dump_protos.py   proto extractor (move from docs/, drop the hard-coded sys.path)
  tools/probe/           Python prototypes for steps 1–5 of section 8 (httpx, websockets, protobuf, pycryptodome)
  cmd/anona-bridge/      Go main
  internal/cloud/        HTTPS client + signer
  internal/control/      WebSocket channel, framing, Packet helpers (generated Go protobuf in internal/pb)
  internal/media/        pion session, RTSP server, talkback
  internal/ha/           MQTT discovery and entities
  addon/                 HA add-on manifest + Dockerfile
```

## 6. Implementation phases

1. **Protocol probe (Python). DONE 2026-10-25.** Reproduce the request signature; login; list devices; fetch WS
   address; perform the WS handshake; send `GET_IPCAMERA_STATUS` and print the decoded status.
   Then `CONTROL_PAN_TILT` — the first visible proof. Exit criterion: the camera moves on command.
2. **Media (Go). DONE 2026-10-25 (video; audio/talkback pending).** Cloud + control channel ported to Go; pion session; log codecs and packet
   rates; dump to a file via ffmpeg to confirm decodable video. Then gortsplib server and go2rtc.
3. **Home Assistant. DONE 2026-10-25 (entities; add-on packaging pending).** MQTT discovery: PTZ/calibrate/reboot buttons, privacy/LED/spotlight/audio/flip + detection toggles (switches), night vision (select), battery/RSSI/storage (sensors), motion/person/animal/vehicle/package/baby-cry (event binary_sensors). go2rtc for the camera. Verified live against the broker.
4. **Talkback. DONE 2026-10-25.** HTTP `/talk` endpoint (ffmpeg->Opus) + `cmd/talk`; camera audio republished over RTSP. TTS from HA via rest_command. RTSP backchannel not needed.
5. **Events and snapshots.** Push handling, binary sensors, snapshot upload URL.
6. **Hardening.** Token refresh, WS reconnect, sleep/wake handling for battery models, metrics.

## 7. Risks

- **Signature.** Everything depends on reproducing `libbanyackey.so`'s `sign()`. If the algorithm
  is not recoverable, fallback is to run the arm64 lib under `qemu-aarch64` in the add-on container
  (ugly but workable) or to proxy signing through a rooted phone during development.
- **Cloud dependency.** No LAN control path is confirmed; if 70mai's service is down the bridge is
  down. The local bind socket (2.6) is the only lead for a LAN path.
- **Terms of service / account lockout.** A second "device" on the account (`uuid`) is normal for
  the app, but an always-connected WebSocket may be rate-limited. Keep one socket, one login.
- **Firmware variance.** Older firmware may need `START_LIVE_STREAM` on the `proto` channel instead
  of `P2P_LIVE_STREAM_START`, or the KVS path. The session code should try the new path first and
  fall back on `NOT_SUPPORT`.
- **H.265 in HA.** go2rtc handles H.265 over WebRTC in Chromium-based browsers only; offer
  `resolutionChoice` for 720p/H.264 as a config option if the camera supports it.

## 8. To confirm, in order

1. ~~`sig` algorithm~~ — **DONE 2026-10-25.** `md5(input + secret)`, secret from a 120-entry
   table in `libbanyackey.so` keyed by channel; this build = channel 72001001, secret
   `329he3wihfeibfk3209(&*^%dehsi3)*&`. Login, home list, device list, online status,
   `getWebsocketAddress` and `getIceConfigV2` all verified against the live INTL API. See
   [PROTOCOL.md](PROTOCOL.md) §1. Signer: [tools/probe/anona_sign.py](../tools/probe/anona_sign.py).
2. ~~WS framing~~ — **DONE 2026-10-25.** AES-128-CBC/PKCS5, key = hex-decode of `websocketAesKey`
   (16 bytes), fixed IV `00..0f`, trailing CRC32-LE. Handshake `operateId 10000000` (ackCode 200),
   request `deviceType:72` (app type) + `target:2`, `GET_IPCAMERA_STATUS` round-trip decoded live.
   See PROTOCOL.md §1b/§1c. Client: [tools/probe/anona_control.py](../tools/probe/anona_control.py).
3. Whether the camera listens on the LAN after binding (`nmap`, then one framed `GET_IPCAMERA_STATUS`
   over `IpcSocket` format).
4. ~~WebRTC~~ — **DONE 2026-10-25.** Trickle ICE works; the HM1010 offers **H.265** (pt 98);
   media starts after `P2P_LIVE_STREAM_START`; no `WEB_RTC_WAKE_UP` was needed for this (mains)
   camera. Live video negotiated and republished as RTSP end to end. See PROTOCOL.md §1d.
5. PTZ position range and sign convention (`horizontal`/`vertical` in tenths of a degree, assumed
   centred at 0).
6. Event push shape on this firmware (`REPORT_IPCAMERA_EVENT` vs `_EVENTS`, property ids).
7. Snapshot upload: whether `upload_uri` must be HTTPS and whether any host is accepted.
8. Session limits: how many concurrent viewers the camera accepts (`P2P_BUSY`), idle timeout.

## 9. Sources

- Decompiled app (jadx) at `/tmp/claude-1000/-home-paul/a9dda853-227c-41c0-b045-d99554c7ee6b/scratchpad/src/sources`
  (temporary; copy anything you need to keep). Key files: `com/banya/anona/data/f.java` (control
  WebSocket), `com/banya/anona/ipc/aws/p0.java` (WebRTC session), `com/banya/anona/ipc/data/PacketKt.java`
  (packet builders), `com/banya/anona/ipc/data/IpcService.java` (REST), `com/banya/base/ktx/data/a.java`
  (request signing), `th/c.java`, `th/b.java` (AES, CRC).
- 70mai Anona Pano HM1010 product page: https://70mai.my/products/anona-pano-indoor-cctv-hm1010
- Reverse engineering the 70mai Android app (same vendor, same signing family): https://alu.dog/posts/reverse-engineering-the-70mai-android-app/
- go2rtc: https://github.com/AlexxIT/go2rtc — RTSP source, WebRTC output, two-way audio backchannel
- pion/webrtc: https://github.com/pion/webrtc — gortsplib: https://github.com/bluenviron/gortsplib
