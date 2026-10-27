# Anona Pano Bridge

Use your **70mai Anona Pano** camera in Home Assistant — live video, pan/tilt/zoom,
motion and AI alerts, and talk-to-the-camera audio — without the phone app.

These cameras don't have a built-in local stream (no RTSP/ONVIF of their own), so
this add-on connects to them the same way the official app does, using **your own
Anona account**, and brings everything into Home Assistant as normal entities.

## What you get in Home Assistant

- 📹 **Live video** — a camera stream you can view, snapshot, and record (via
  [go2rtc](https://github.com/AlexxIT/go2rtc)/WebRTC, so it's low-latency).
- 🔊 **Two-way audio** — hear the camera and talk back through its speaker.
- 🕹️ **Pan / tilt / zoom** — buttons to move the camera and recenter it.
- 🎛️ **Controls** — switches for privacy mode, status LED, spotlight, siren/alarm
  audio, and each detection type (motion, person, vehicle, package, animal,
  sound, baby cry); selects for night vision and image rotation.
- 🚨 **Alerts** — motion / person / animal / vehicle / package / baby-cry sensors
  that fire from the camera's own detection, ready for automations.
- 📈 **Status** — Wi-Fi signal, SD-card state, and current pan/tilt position.
- 🎥 **Multiple cameras** — every camera on your account is set up automatically.

Only the controls your specific camera model actually supports are created, so you
won't get switches that do nothing.

## Before you start

You'll need:

1. **Home Assistant OS / Supervised** (so you can install add-ons).
2. The **Mosquitto broker** add-on (Settings → Add-ons → Add-on store →
   *Mosquitto broker*). This add-on uses it automatically for the entities.
3. The **go2rtc** add-on *or* the built-in WebRTC Camera integration, to view the
   video (see [Watching the video](#watching-the-video)).
4. Your **Anona / 70mai account** email and password, and your region
   (**INTL**, **EU**, or **OTHERS** — whichever you chose when signing up).
5. An **internet connection** — the cameras are cloud-connected, so the bridge
   needs to reach the Anona cloud with your account (see
   [Good to know](#good-to-know)).

## Installing

1. In Home Assistant, go to **Settings → Add-ons → Add-on store**.
2. Click the **⋮** menu (top right) → **Repositories**.
3. Paste this repository's URL and click **Add**:
   ```
   https://github.com/Darkhand81/anona-ha-bridge
   ```
4. Close the dialog, find **Anona Pano Bridge** in the store, and click
   **Install**.
5. Open the **Configuration** tab, enter your account **email**, **password**, and
   **region**, and **Save**.
6. **Start** the add-on. Open the **Log** tab — within a minute or so you should
   see each camera discovered and come online.

Your cameras then appear automatically in **Settings → Devices & Services → MQTT**
as devices with all their controls and sensors.

### Options

| Option | Needed? | What it does |
|---|---|---|
| **email** / **password** | Yes | Your Anona / 70mai account login. |
| **region** | Yes | `INTL`, `EU`, or `OTHERS` — match the app. |
| **devices** | No | List specific camera IDs to bridge. Leave empty for **all** cameras. |
| **has_battery** | No | Turn on only for battery models (most are mains-powered). |
| **rtsp_user** / **rtsp_password** | No | Set both to password-protect the video stream. Leave blank and it's open on your LAN. |
| **mqtt_url** / **mqtt_user** / **mqtt_password** | No | Only if you use an external MQTT broker instead of the Mosquitto add-on. |

(A couple of advanced options — `token_refresh`, `rtsp_listen`, `http_listen` —
have sensible defaults you normally won't touch.)

## Watching the video

The add-on publishes each camera as an RTSP stream on your Home Assistant host.
The easiest way to view it is the **go2rtc** add-on: add one line per camera,
using the stream name shown in the add-on's log (it's based on the camera's name).

```yaml
# go2rtc configuration
streams:
  living_room: rtsp://YOUR_HA_IP:8554/living_room
```

Then add the camera through the **WebRTC Camera** / go2rtc integration. (If you
only have one camera, its stream name is `cam`.)

If you set **rtsp_user** / **rtsp_password**, include them in the URL:
`rtsp://user:pass@YOUR_HA_IP:8554/living_room`.

## Talking through the camera

The add-on accepts audio and plays it on the camera's speaker. The simplest setup
is a `rest_command` that posts a Text-to-Speech clip:

```yaml
# configuration.yaml
rest_command:
  living_room_say:
    url: http://YOUR_HA_IP:8080/talk/living_room
    method: POST
    content_type: audio/wav
    payload: "{{ audio }}"
```

Call it from an automation or script with any WAV/MP3/TTS audio.

## Good to know

- **It uses the Anona cloud.** 70mai cameras can't be controlled purely locally —
  there's no on-device API, and the firmware only trusts the vendor's servers. So
  the bridge signs in with your account and talks to the cloud, exactly like the
  app does. Video itself is sent directly from the camera to Home Assistant
  (peer-to-peer) whenever possible.
- **It needs internet.** If the Anona cloud is unreachable, cameras go offline —
  same as in the app.
- **Your login stays in Home Assistant.** Credentials are stored in the add-on's
  options and only sent to the Anona servers to sign in.
- **This is an unofficial, community project** — not affiliated with 70mai or
  Anona. Use it with cameras you own.

## Troubleshooting

- **No entities appear** → make sure the **Mosquitto broker** add-on is installed
  and running; check the add-on log for an MQTT connection message.
- **Camera shows offline in the log** → double-check the **region**, and confirm
  the camera works in the Anona app (the account must have access to it).
- **Video won't play** → confirm the go2rtc stream URL uses your HA IP and the
  exact stream name from the log; the stream appears a few seconds after start.
- **Talkback is silent** → make sure you're posting to the right camera path and
  the camera isn't muted/in privacy mode.

Still stuck? Open an issue with the relevant lines from the add-on log.

---

## For developers

The bridge is written in Go. It signs in to the Anona/70mai cloud (request signing
reverse-engineered from the Android app), opens one per-account WebSocket control
channel multiplexed across all cameras, and negotiates a WebRTC session per camera
that it republishes locally as RTSP; controls and status are exposed over MQTT
discovery.

- **[docs/RUNNING.md](docs/RUNNING.md)** — build and run the binary directly, full
  environment-variable list, and the add-on release flow.
- **[docs/DESIGN.md](docs/DESIGN.md)** — architecture.
- **[docs/PROTOCOL.md](docs/PROTOCOL.md)** — the cloud/control/media wire format.
- **[docs/FIRMWARE.md](docs/FIRMWARE.md)** / **[docs/LOCAL-BYPASS.md](docs/LOCAL-BYPASS.md)**
  — why local-only operation isn't possible.

```
cmd/anona-bridge     entrypoint (env- or arg-driven, single- or multi-camera)
internal/cloud       HTTPS client + request signing + token refresh
internal/control     per-account WebSocket control channel (AES-CBC + CRC32)
internal/pb          protobuf wire codec + packet builders
internal/media       WebRTC session (pion) + multi-path RTSP server (gortsplib)
internal/cam         camera controller (status, setters, events)
internal/ha          Home Assistant MQTT discovery
internal/supervisor  multi-camera supervision (discovery, reconnect, refresh)
addon/anona_bridge   Home Assistant add-on packaging
```

Quick standalone run:

```bash
go build ./cmd/anona-bridge
ANONA_EMAIL=you@example.com ANONA_PASSWORD='…' ANONA_REGION=INTL \
ANONA_MQTT_URL=tcp://homeassistant.local:1883 ./anona-bridge
```

### License

No license is granted yet. The reverse-engineered protocol details are provided
for interoperability with hardware you own.
