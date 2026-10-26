# Protocol probes (Python)

Prototypes used to confirm the Anona cloud protocol before the Go bridge.

- `anona_sign.py` — request signing (reproduced from libbanyackey.so) + login/REST helpers.
  Self-test: `python3 anona_sign.py` (dummy login, correct vs corrupted sig).
  Live: `python3 anona_sign.py <email> <password> [INTL|EU|OTHERS]`.
- `anona_control.py` — full cloud control channel: login -> getWebsocketAddress ->
  WSS handshake -> AES-128-CBC+CRC framed packets. Read status or drive PTZ:
  `python3 anona_control.py <email> <password> [region] [deviceId]`  (GET_IPCAMERA_STATUS)
  `python3 anona_control.py <email> <password> INTL <deviceId> ptz <up|down|left|right> [click|continuous]`.
  Includes a schema-free protobuf decoder/encoder and `camera_packet()` for building any command.
- `wsmin.py` — minimal stdlib TLS WebSocket client (cert verification on).

Dependency: `pycryptodome` (for `Crypto.Cipher.AES`). Everything else is stdlib.
Protocol details: ../../docs/PROTOCOL.md.
