# Anona Pano Bridge

Bridge **70mai Anona Pano** cameras (HM1010 and family) into Home Assistant.
The camera has no local API — it is brokered entirely through the Anona/70mai
cloud — so this bridge speaks that cloud protocol (reverse-engineered from the
Android app) and re-exposes each camera locally as a standard RTSP stream plus
MQTT entities.

> Interoperability / home-automation research for cameras **you own**. It talks
> to the vendor cloud with your own account; it does not bypass or circumvent it.

## Features

- **Live video over RTSP** — one stream per camera (H.265/H.264 video + Opus
  audio), ready for [go2rtc](https://github.com/AlexxIT/go2rtc) and the Home
  Assistant camera.
- **Controls & status via MQTT discovery** — PTZ (up/down/left/right/stop,
  calibrate), privacy / indicator LED / spotlight / audio / detection switches,
  night-vision and rotation selects, Wi-Fi / SD-card / pan-tilt-position sensors,
  and motion / person / animal / vehicle / package / baby-cry event sensors.
  Entities are gated per-model by the camera's reported capabilities.
- **Two-way talkback** — POST any audio to the bridge and it plays on the camera
  speaker.
- **Multi-camera supervision** — discovers every camera in the account, runs
  them all on one shared cloud connection, reconnects with backoff, and refreshes
  the session token, so it runs unattended.

## How it works

```
account login (signed HTTPS REST)
      │
      ▼
control channel  ── one per-account WebSocket, AES-128-CBC + CRC32 framed,
      │             multiplexed across all cameras by deviceId
      ├── status / PTZ / settings  ──▶  MQTT discovery entities
      └── WebRTC signalling
              │
              ▼
        WebRTC P2P (H.265 + Opus)  ──▶  republished as RTSP  ──▶  go2rtc / HA
                                                                  talkback ◀──
```

The control channel is **per-account** (not per-camera), so a single connection
carries every camera; the media is peer-to-peer WebRTC, republished locally as
RTSP. See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) for the wire details and
[`docs/DESIGN.md`](docs/DESIGN.md) for the architecture.

## Install

### As a Home Assistant add-on (recommended)

This repository is a Home Assistant add-on repository. In Home Assistant go to
**Settings → Add-ons → Add-on store → ⋮ → Repositories**, add this repo's URL,
then install **Anona Pano Bridge**. Configure your account `email`/`password` and
`region`, and start it. Full guide: [`addon/anona_bridge/DOCS.md`](addon/anona_bridge/DOCS.md).

> The add-on installs **prebuilt multi-arch images** from GHCR, published by the
> release workflow ([`.github/workflows/build-addon.yaml`](.github/workflows/build-addon.yaml)).
> To ship a build, bump `version` in
> [`addon/anona_bridge/config.yaml`](addon/anona_bridge/config.yaml) and publish a
> GitHub Release. See [`docs/RUNNING.md`](docs/RUNNING.md) for the release steps.

### As a standalone binary

```bash
go build ./cmd/anona-bridge

# single camera (served at rtsp://host:8554/cam):
anona-bridge <email> <password> <deviceId> [region] [rtspListen]

# every camera in the account (env-driven):
ANONA_EMAIL=you@example.com ANONA_PASSWORD='…' ANONA_REGION=INTL \
ANONA_MQTT_URL=tcp://homeassistant.local:1883 anona-bridge
```

`region` is `INTL`, `EU`, or `OTHERS`. Run/build details, the full environment
variable list, and go2rtc / talkback wiring are in
[`docs/RUNNING.md`](docs/RUNNING.md).

## Status

Verified live against an HM1010: signing/login, the control channel, WebRTC→RTSP
video **and** audio, two-way talkback, the MQTT entity layer, and multi-camera
discovery + whole-account reconnect. Requires the vendor cloud and internet
access — the device chain is locked down (signed + encrypted firmware,
cryptographic server-cert validation), so local-only operation is not feasible;
see [`docs/LOCAL-BYPASS.md`](docs/LOCAL-BYPASS.md) and
[`docs/FIRMWARE.md`](docs/FIRMWARE.md).

## Layout

```
cmd/anona-bridge    the bridge (supervisor entrypoint: env- or arg-driven)
cmd/verify          foundation check (login, status, PTZ) — no media
cmd/talk            send an audio clip to the camera speaker (talkback)
cmd/rtspprobe       RTSP reader for verifying the output
internal/cloud      HTTPS client + request signing + token refresh
internal/control    per-account WebSocket control channel (AES-CBC + CRC32)
internal/pb         hand-built protobuf wire codec + packet builders
internal/media      pion WebRTC session + multi-path gortsplib RTSP server
internal/cam        high-level camera controller (status, setters, events)
internal/ha         Home Assistant MQTT discovery
internal/supervisor multi-camera supervision (discovery, reconnect, refresh)
addon/anona_bridge  Home Assistant add-on packaging
docs/               protocol, design, running, firmware & bypass analysis
```

## License

No license is granted yet. Reverse-engineered protocol details are provided for
interoperability with hardware the user owns.
