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

1. In Home Assistant go to **Settings → Add-ons → Add-on store**, open the
   ⋮ menu → **Repositories**, and add this repository's URL.
2. Install **Anona Pano Bridge** from the store.
3. Fill in the options (below) and start it.

> The image is built on-device from the published source the first time you
> install it, so the first start takes a few minutes.

## Options

| Option | Required | Notes |
|---|---|---|
| `email` / `password` | yes | Your 70mai / Anona account login. |
| `region` | yes | `INTL`, `EU`, or `OTHERS` — match the app's region. |
| `devices` | no | List of `deviceId`s to bridge. Empty = **every** camera in the account. |
| `mqtt_url` | no | e.g. `tcp://core-mosquitto:1883`. Leave blank to auto-use the Mosquitto add-on. |
| `mqtt_user` / `mqtt_password` | no | Only when `mqtt_url` is set manually. |
| `has_battery` | no | Force battery entities (mains models hide them; HM1010 has none). |
| `token_refresh` | no | How often to re-login, e.g. `6h`. |
| `rtsp_listen` / `http_listen` | no | Bind addresses; defaults `:8554` / `:8080`. |

Finding a `deviceId`: start the add-on with `devices` empty and read the log —
each discovered camera is printed with its id, model, and RTSP path.

## Using the video in Home Assistant

The add-on serves RTSP on the host. Point go2rtc at it (one entry per camera —
the path is printed in the log, derived from the camera's name):

```yaml
# go2rtc configuration
streams:
  living_room: rtsp://<HA-host-ip>:8554/living_room
```

Then add the camera via the go2rtc / WebRTC integration.

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
