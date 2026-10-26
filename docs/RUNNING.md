# Running the bridge

Go bridge: a live WebRTC session per camera, republished as RTSP for go2rtc /
Home Assistant, plus MQTT discovery and talkback. Status: video + audio +
talkback and the MQTT entity layer are proven end to end; **multi-camera
supervision (reconnect + token refresh) and a Home Assistant add-on wrapper are
now implemented** (`internal/supervisor`, `addon/anona_bridge/`).

## Build

```bash
go build ./cmd/anona-bridge
```

Go 1.23+. Dependencies: pion/webrtc v4, bluenviron/gortsplib v4, gorilla/websocket.

## Run

Two ways to invoke it.

**Single camera (positional, legacy — served at `/cam`):**

```bash
anona-bridge <email> <password> <deviceId> [region] [rtspListen]
# e.g.
anona-bridge you@example.com 'password' 651f50f69d134b6ab061c0e02ed258dd INTL :8554
```

**Every camera in the account (env-driven — used by the add-on):**

```bash
ANONA_EMAIL=you@example.com ANONA_PASSWORD='password' ANONA_REGION=INTL \
ANONA_MQTT_URL=tcp://homeassistant.local:1883 \
anona-bridge          # no positional args
```

`region` is INTL / EU / OTHERS (see docs/PROTOCOL.md §1a). The bridge logs in,
discovers the account's cameras (or just the ones in `ANONA_DEVICES`), and runs
one supervised **worker per camera**: each opens its own control channel,
negotiates WebRTC, and serves live **video + audio**. A worker that drops is
restarted with capped backoff, and the shared cloud token is re-logged-in
periodically, so it runs unattended.

Each camera gets its own RTSP path, a slug derived from its nickname (printed at
startup), e.g. `rtsp://<host>:8554/living_room`. The positional single-camera
form keeps the legacy path `rtsp://<host>:8554/cam`. An HTTP server (`:8080` by
default) serves health and talkback.

### Environment variables

| Var | Default | Meaning |
|---|---|---|
| `ANONA_EMAIL` / `ANONA_PASSWORD` | — | account login (env-driven mode) |
| `ANONA_REGION` | `INTL` | INTL / EU / OTHERS |
| `ANONA_DEVICES` | all | comma-separated `deviceId` allowlist |
| `ANONA_RTSP_LISTEN` | `:8554` | RTSP bind address |
| `ANONA_HTTP_LISTEN` | `:8080` | health + talkback bind address |
| `ANONA_MQTT_URL` / `_USER` / `_PASS` | — | MQTT broker (blank = no MQTT) |
| `ANONA_HAS_BATTERY` | — | `1` forces battery entities |
| `ANONA_TOKEN_REFRESH` | `6h` | proactive re-login interval |

### Talkback

Send audio to the camera speaker by POSTing any ffmpeg-decodable audio (WAV, MP3,
Opus, a TTS clip) to the bridge; it transcodes to Opus and plays it:

```bash
# name the camera by its path; /talk alone works when only one camera is live
curl -X POST --data-binary @hello.wav http://127.0.0.1:8080/talk/living_room
```

Home Assistant can drive this with a `rest_command` posting a generated TTS file.
A standalone tester is also available:

```bash
go run ./cmd/talk <email> <password> <deviceId> <audiofile> [region]
```

Find a camera's `deviceId` with the Python probe
([`tools/probe/anona_sign.py`](../tools/probe/anona_sign.py)) or by calling
`getDeviceListByHomeId`.

## go2rtc / Home Assistant

```yaml
# go2rtc.yaml — one entry per camera; use the path printed at startup
streams:
  living_room: rtsp://127.0.0.1:8554/living_room   # or /cam in single-camera mode
```

Home Assistant then gets a camera entity via the go2rtc integration (WebRTC/HLS,
snapshots).

## MQTT entities (Home Assistant discovery)

Set the broker in the environment and the bridge auto-discovers a full set of
entities (works even with no video viewer connected):

```bash
export ANONA_MQTT_URL=tcp://homeassistant.local:1883
export ANONA_MQTT_USER=...      # optional
export ANONA_MQTT_PASS=...      # optional
export ANONA_HAS_BATTERY=1      # only for genuine battery models (mains models hide battery)
anona-bridge <email> <password> <deviceId> INTL
```

Published under `homeassistant/...` (one device per camera, named from the app):

- **Buttons:** PTZ up / down / left / right / stop, calibrate, reboot.
- **Switches:** privacy, indicator LED, spotlight, audio, flip H, flip V, and the
  detection toggles — motion, person, vehicle, package, animal, sound, baby cry.
- **Select:** night vision (off / ir / white_light / starlight / smart).
- **Sensors:** battery, Wi-Fi RSSI, storage free / total.
- **Binary sensors (event-driven, auto-off):** motion, person, animal, vehicle,
  package, baby cry — driven by the camera's `REPORT_IPCAMERA_EVENT` pushes.

State is a single retained JSON topic polled every 30 s and refreshed right after
each command (no optimistic state). Commands that need the whole message
(detection toggles) read current status, flip one field, and resend.

### Talkback from Home Assistant

```yaml
# configuration.yaml
rest_command:
  camera_say:
    url: http://anona-bridge:8080/talk/living_room
    method: POST
    content_type: audio/wav
    payload: "{{ audio_bytes }}"   # or post a TTS-generated file
```

## Verify without a player

```bash
go run ./cmd/rtspprobe rtsp://127.0.0.1:8554/cam
# prints the negotiated codec and the RTP packet/byte counts over 12s
```

Confirmed 2026-10-25 against camera 192.168.1.213 (HM1010): WebRTC connected,
**H.265** video negotiated, ~1200 RTP packets / ~1.37 MB in 12 s pulled back out
over RTSP.

## Home Assistant add-on

`addon/anona_bridge/` packages the bridge as an installable HA add-on. The repo
root is also an add-on *repository* (`repository.yaml`), so in Home Assistant you
add this repo's URL under **Settings → Add-ons → Add-on store → ⋮ →
Repositories**, then install **Anona Pano Bridge**.

- Options (`config.yaml`): `email`, `password`, `region`, optional `devices`
  allowlist, MQTT settings, `has_battery`, `token_refresh`. MQTT auto-discovers
  the Mosquitto add-on when `mqtt_url` is blank (`services: mqtt:want`).
- Home Assistant pulls **prebuilt multi-arch images** from GHCR
  (`config.yaml` `image:`), so there is no on-device build. The images are
  produced by the release workflow (below); the `Dockerfile` just drops the
  CI-compiled binary onto the HA base with `ffmpeg`.
- Runs with `host_network: true` so WebRTC host ICE candidates are the LAN IP
  (matching the proven direct-P2P setup) and RTSP `:8554` / talkback `:8080` are
  reachable on the host. `run.sh` maps the options to the `ANONA_*` env above.

### Releasing a new add-on build

Images are built and pushed by
[`.github/workflows/build-addon.yaml`](../.github/workflows/build-addon.yaml):

1. Bump `version` in `addon/anona_bridge/config.yaml`.
2. Commit and push to `main`.
3. Publish a **GitHub Release** (tag it to match, e.g. `v0.1.1`), or run the
   workflow manually (**Actions → Build add-on images → Run workflow**).
4. CI cross-compiles the binary per arch (aarch64/amd64/armv7) and uses the
   Home Assistant builder to push `ghcr.io/darkhand81/anona-ha-bridge/<arch>-anona_bridge:<version>`
   using the built-in `GITHUB_TOKEN` (no extra secrets).

Home Assistant then offers the new `version` and pulls the image. For HA to pull
without credentials, make the GHCR packages **public** (GitHub → the package →
Package settings → Change visibility) once; otherwise configure registry auth in
Home Assistant.

See [`addon/anona_bridge/DOCS.md`](../addon/anona_bridge/DOCS.md) for the
end-user guide.

## Layout

```
cmd/anona-bridge   the bridge (supervisor entrypoint: env- or arg-driven)
internal/supervisor multi-camera supervision: discovery, per-camera workers,
                   reconnect/backoff, token refresh, talkback HTTP routing
cmd/verify         foundation check (login, status, PTZ) — no media
cmd/talk           send an audio clip to the camera speaker (talkback)
cmd/rtspprobe      RTSP reader for verifying the output
internal/cloud     HTTPS client + request signing
internal/control   WebSocket control channel (AES-128-CBC + CRC32 framing)
internal/pb        hand-built protobuf wire codec + packet builders
internal/media     pion WebRTC session + gortsplib RTSP server
internal/cam       high-level camera controller (status, setters, events)
internal/ha        Home Assistant MQTT discovery
```
