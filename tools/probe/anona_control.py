#!/usr/bin/env python3
"""Anona cloud control-channel client — verified end to end 2026-10-25.

Flow: login -> getWebsocketAddress -> open WSS -> plaintext auth handshake
(operateId 10000000) -> send an AES-128-CBC+CRC32-framed WsRequestInfo carrying a
hex-encoded `Packet` protobuf -> read the device's reply frame and decode it.

Confirmed against camera 651f50f69d134b6ab061c0e02ed258dd (192.168.1.213, HM1010):
GET_IPCAMERA_STATUS returns a full IPCameraStatus.

Framing (see docs/PROTOCOL.md):
  frame_text = hex( AES128CBC(key, IV=00..0f, PKCS7(json)) + CRC32_LE(ciphertext) )
  key = bytes.fromhex(websocketAesKey)        # 16 bytes
Request JSON (WsRequestInfo):
  {ts, deviceId, deviceType:72, operateId:<uuid>, target:2, content:hex(packet)}
  deviceType is the APP type (72), NOT the camera's type. target=2 routes to device.
  Server ack: ackCode 200 = delivered to device; 10000 = not delivered (bad route);
  the device's reply is a later frame with ackCode=null, source=2, matching operateId.

Requires: pycryptodome (Crypto.Cipher.AES). wsmin.py sits beside this file.
Usage: anona_control.py <email> <password> [region] [deviceId]
"""
import sys, os, json, time, uuid as U, binascii, zlib
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import anona_sign as A
from wsmin import WS
from Crypto.Cipher import AES

IV = bytes(range(16))

def _frame(key, js):
    b = js.encode(); b += bytes([16 - len(b) % 16]) * (16 - len(b) % 16)
    ct = AES.new(key, AES.MODE_CBC, IV).encrypt(b)
    return binascii.hexlify(ct + zlib.crc32(ct).to_bytes(4, "little")).decode()

def _unframe(key, hexs):
    raw = binascii.unhexlify(hexs); ct = raw[:-4]
    if zlib.crc32(ct).to_bytes(4, "little") != raw[-4:]:
        raise ValueError("CRC mismatch")
    p = AES.new(key, AES.MODE_CBC, IV).decrypt(ct)
    return p[:-p[-1]]

# --- minimal protobuf wire decoder (no schema needed) ---
def _var(b, o):
    r = s = 0
    while True:
        x = b[o]; o += 1; r |= (x & 0x7f) << s; s += 7
        if not x & 0x80: break
    return r, o

def pb_decode(b):
    o = 0; out = []
    try:
        while o < len(b):
            tag, o = _var(b, o); fn = tag >> 3; wt = tag & 7
            if wt == 0: v, o = _var(b, o); out.append((fn, 0, v))
            elif wt == 2:
                ln, o = _var(b, o); ch = b[o:o+ln]
                if o + ln > len(b): return None
                o += ln
                sub = pb_decode(ch)
                # keep a nested message only if it re-encodes to the same bytes,
                # otherwise it is really a string/bytes field
                out.append((fn, 2, sub if (sub is not None and pb_encode(sub) == ch) else ch))
            elif wt == 5: out.append((fn, 5, int.from_bytes(b[o:o+4], "little"))); o += 4
            elif wt == 1: out.append((fn, 1, int.from_bytes(b[o:o+8], "little"))); o += 8
            else: return None
    except (IndexError, ValueError):
        return None
    return out

def _enc_var(n):
    o = bytearray()
    while True:
        x = n & 0x7f; n >>= 7; o.append(x | (0x80 if n else 0))
        if not n: break
    return bytes(o)

def pb_encode(node):
    """Re-encode a pb_decode() tree (used both to validate nesting and to build packets)."""
    out = bytearray()
    for fn, wt, v in node:
        if wt == 0: out += _enc_var((fn << 3) | 0) + _enc_var(v)
        elif wt == 5: out += _enc_var((fn << 3) | 5) + int(v).to_bytes(4, "little")
        elif wt == 1: out += _enc_var((fn << 3) | 1) + int(v).to_bytes(8, "little")
        elif wt == 2:
            body = pb_encode(v) if isinstance(v, list) else v
            out += _enc_var((fn << 3) | 2) + _enc_var(len(body)) + body
    return bytes(out)

def field_var(fn, val): return _enc_var((fn << 3) | 0) + _enc_var(val)
def field_msg(fn, body): return _enc_var((fn << 3) | 2) + _enc_var(len(body)) + body

def camera_packet(pid, ipcamera_body=b""):
    """Packet{type=IPCamera(50), id=pid, [ipcamera=...]}."""
    p = field_var(1, 50) + field_var(2, pid)
    return p + (field_msg(52, ipcamera_body) if ipcamera_body else b"")

PTZ_DIR = {"up": 1, "down": 2, "left": 3, "right": 4, "stop": 5}
def ptz_control_packet(direction, action="click"):
    """CONTROL_PAN_TILT (id 107): IPCamera.pan_tilt_control(80){direction(1), action_type(2)}."""
    ptc = field_var(1, PTZ_DIR[direction]) + field_var(2, 1 if action == "click" else 2)
    return camera_packet(107, field_msg(80, ptc))
def ptz_position_query_packet():
    """GET_PAN_TILT_CURRENT_POSITION (id 109)."""
    return camera_packet(109)

IPCAM_STATUS = {1:"battery",2:"long_endurance",4:"camera_sleep/privacy",5:"camera_display",
    6:"night_vision",10:"audio_config",11:"indicator_light",12:"white_light",20:"motion_detection",
    21:"ai_detection",22:"detection_alarm",23:"sound_detection",30:"memory_storage",46:"wifi_rssi",
    47:"network_connection",51:"pan_tilt_position",255:"timestamp"}

def pb_show(node, ind=0, lab=None):
    for fn, wt, v in node:
        name = (lab or {}).get(fn, "")
        if wt == 2 and isinstance(v, list):
            print("  "*ind + f"#{fn} {name} {{"); pb_show(v, ind+1); print("  "*ind + "}")
        elif wt == 2:
            t = v.decode() if v and all(32 <= c < 127 for c in v) else v.hex()
            print("  "*ind + f"#{fn} {name} = {t!r}")
        else:
            print("  "*ind + f"#{fn} {name} = {v}")

# GET_IPCAMERA_STATUS = Packet{type=IPCamera(50), id=4}
PKT_GET_STATUS = bytes([0x08, 0x32, 0x10, 0x04])
APP_DEVICE_TYPE = 72
TARGET_DEVICE = 2

class ControlChannel:
    def __init__(self, host, token, uuid):
        self.host, self.token, self.uuid = host, token, uuid
        wa = json.loads(A.post(host, "/anonaWebsocketApi/getWebsocketAddress",
                               A.build_signed_body({}, uuid, token=token))[1])["resultBodyObject"]
        self.key = bytes.fromhex(wa["websocketAesKey"])
        self.ws = WS(wa["websocketAddress"], timeout=25)
        self.ws.send(json.dumps({"handshakeToken": wa["websocketToken"],
                                 "operateId": 10000000, "ts": int(time.time()*1000)}))
        auth = json.loads(self.ws.recv())
        if auth.get("ackCode") != 200:
            raise RuntimeError("handshake rejected: %r" % auth)

    def request(self, device_id, packet_bytes, wait=25):
        oid = str(U.uuid4())
        self.ws.send(_frame(self.key, json.dumps({
            "ts": int(time.time()*1000), "deviceId": device_id,
            "deviceType": APP_DEVICE_TYPE, "operateId": oid,
            "target": TARGET_DEVICE, "content": binascii.hexlify(packet_bytes).decode()})))
        t0 = time.time()
        while time.time() - t0 < wait:
            js = json.loads(_unframe(self.key, self.ws.recv()).decode())
            if js.get("operateId") == oid and js.get("ackCode") == 10000:
                raise RuntimeError("not delivered to device (ackCode 10000)")
            if js.get("operateId") == oid and js.get("content"):
                self.last_content_hex = js["content"]
                return pb_decode(binascii.unhexlify(js["content"]))
        raise TimeoutError("no device reply")

    def close(self): self.ws.close()

def get_status(cc, device_id):
    pkt = cc.request(device_id, PKT_GET_STATUS)
    ipc = next(v for fn, wt, v in pkt if fn == 52)          # Packet.ipcamera
    return next(v for fn, wt, v in ipc if fn == 3)          # IPCamera.ip_camera_status

def ptz(cc, device_id, direction, action="click"):
    """Send CONTROL_PAN_TILT. Returns error_code (0 = NO_ERROR)."""
    pkt = cc.request(device_id, ptz_control_packet(direction, action))
    ec = [v for fn, wt, v in (pkt or []) if fn == 49]
    return ec[0] if ec else 0

if __name__ == "__main__":
    email, password = sys.argv[1], sys.argv[2]
    region = sys.argv[3] if len(sys.argv) > 3 else "INTL"
    did = sys.argv[4] if len(sys.argv) > 4 else "651f50f69d134b6ab061c0e02ed258dd"
    cmd = sys.argv[5] if len(sys.argv) > 5 else "status"
    uuid = str(U.uuid4())
    tok = json.loads(A.login(A.HOSTS[region], email, password, uuid)[1])["resultBodyObject"]["token"]
    cc = ControlChannel(A.HOSTS[region], tok, uuid)
    print("handshake OK")
    if cmd == "ptz":
        direction = sys.argv[6] if len(sys.argv) > 6 else "left"
        action = sys.argv[7] if len(sys.argv) > 7 else "click"
        rc = ptz(cc, did, direction, action)
        print(f"PTZ {direction} {action} -> error_code={rc} ({'NO_ERROR' if rc == 0 else 'ERROR'})")
        if action == "continuous":
            time.sleep(1.0); print("stop ->", "error_code=%d" % ptz(cc, did, "stop", "continuous"))
    else:
        print("GET_IPCAMERA_STATUS from", did)
        pb_show(get_status(cc, did), 0, IPCAM_STATUS)
    cc.close()
