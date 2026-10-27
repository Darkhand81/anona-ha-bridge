# Anona Pano Bridge

Bridges 70mai **Anona Pano** cameras (HM1010 and family) into Home Assistant
through the vendor cloud:

- **Live video** as an RTSP stream per camera (H.265/H.264 + Opus audio), ready
  for [go2rtc](https://github.com/AlexxIT/go2rtc) / the Home Assistant camera.
- **Controls & status over MQTT discovery** — PTZ buttons, privacy / LED /
  spotlight / audio / detection switches, night-vision & rotation selects,
  Wi-Fi / SD sensors, and motion / person / animal / vehicle / package / baby-cry
  event sensors.
- **Two-way talkback** — POST audio to the add-on to play it on the camera.

It supervises every camera: reconnects with backoff if a stream drops and
refreshes the cloud token so it keeps running unattended.

## Installation

1. In Home Assistant go to **Settings → Apps → Install Apps** (older versions:
   **Settings → Add-ons → Add-on Store**), open the ⋮ menu → **Repositories**,
   and add this repository's URL.
2. Install **Anona Pano Bridge** from the list.
3. Fill in the options (below) and start it. Open the **Log** tab to watch each
   camera come online — it also prints each camera's stream name (needed for
   video below).

> The add-on installs a prebuilt multi-arch image from GHCR, so it starts
> quickly (no on-device compile).

## Options

| Option | Required | Notes |
|---|---|---|
| `email` / `password` | yes | Your 70mai / Anona account login. |
| `region` | yes | `INTL`, `EU`, or `OTHERS` — match the app's region. |
| `devices` | no | List of `deviceId`s to bridge. Empty = **every** camera in the account. |
| `mqtt_url` | no | e.g. `tcp://core-mosquitto:1883`. Leave blank to auto-use the Mosquitto add-on. |
| `mqtt_user` / `mqtt_password` | no | Only when `mqtt_url` is set manually. |
| `has_battery` | no | Force battery entities (mains models hide them; HM1010 has none). |
| `rtsp_user` / `rtsp_password` | no | Set both to require a login on the video stream. Blank = open on your LAN. |
| `token_refresh` | no | How often to re-login, e.g. `6h`. |
| `rtsp_listen` / `http_listen` | no | Bind addresses; defaults `:8554` / `:8080`. |

Finding a `deviceId`: start the add-on with `devices` empty and read the log —
each discovered camera is printed with its id, model, and RTSP path.

## Using the video in Home Assistant

The add-on serves each camera as an RTSP stream on the host. To display it you
need the camera's **RTSP URL**.

### 1. Find your RTSP URL

```
rtsp://<HA_IP>:8554/<stream-name>
```

- **`<HA_IP>`** — your Home Assistant machine's IP (**Settings → System →
  Network**, or your router). Another add-on on the same host can use `127.0.0.1`.
- **`<stream-name>`** — from this add-on's **Log**. Each camera prints:
  ```
  [living_room_cam] live; rtsp path /living_room_cam
  ```
  The part after the slash is the stream name (derived from the camera's name).

Example: `rtsp://192.168.1.50:8554/living_room_cam`. If you set `rtsp_user` /
`rtsp_password`, use `rtsp://user:pass@192.168.1.50:8554/living_room_cam`.

Tip: paste the URL into VLC (**Media → Open Network Stream**) to confirm it plays.

### 2. Quickest display — Generic Camera

1. **Settings → Devices & Services → Add Integration → Generic Camera.**
2. Put your RTSP URL in **Stream Source URL**, leave the still-image field blank,
   and finish. Confirm the preview.
3. You get a `camera.` entity — add a **Picture** or **Camera** card pointing at
   it. Repeat per camera.

### 3. Lower latency — go2rtc (optional)

Install the **go2rtc** add-on and add one entry per camera (same stream name),
then add the camera via the **WebRTC Camera** integration:

```yaml
# go2rtc configuration
streams:
  living_room_cam: rtsp://127.0.0.1:8554/living_room_cam
```

## Talkback

POST any ffmpeg-decodable audio to the add-on; it transcodes to Opus and plays
it on the camera speaker:

```
POST http://<HA-host-ip>:8080/talk/<camera-path>
```

From Home Assistant, a `rest_command` posting a generated TTS file works well.
If only one camera is live you can POST to `/talk` without the path.

## Notes

- All control and media go through the vendor cloud (the device chain is locked
  down — see the project docs); the add-on needs internet access.
- MQTT entities appear even with no video client connected.
- The account credentials are stored by Home Assistant in the add-on options.
