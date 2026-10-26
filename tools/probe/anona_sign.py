#!/usr/bin/env python3
"""Anona / 70mai cloud request signing, reproduced from libbanyackey.so (app v1.7.2).

Confirmed against the live API 2026-10-25: a correct signature returns
errorCode 500200 "can Not find accountUser" for a dummy login, a corrupted one
returns errorCode -1 "sig not passed".

The native export s(JNIEnv*, jobject, long channel, jstring input) computes
    md5( input + SECRET[channel] )            (lowercase hex)
where SECRET is picked from a 120-entry table keyed by the app `channel`.
The Java layer (BanyacKeyUtils.b and warpRequestBody / account login) decides
what `input` is. See build_* helpers below.

This build (com.banyac.anona.int): channel=72001001, deviceType=72,
region hosts INTL=us-api, EU=eu-api, OTHERS=int-api .anonasecurity.com.
"""
import hashlib, json, time, urllib.request, urllib.error, uuid as _uuid, pathlib

CHANNEL = 72001001
DEVICE_TYPE = 72
# SECRET[72001001]; full table in ../../reference/banyackey_secret_table.json
SECRET = "329he3wihfeibfk3209(&*^%dehsi3)*&"
USER_NAME_KEY = "email"          # R.string.user_name for this build
AUTH_STRING = "%d%snull%s"       # R.string.auth_string: deviceType, email, hashedPw

HOSTS = {
    "INTL": "https://us-api.anonasecurity.com",
    "EU":   "https://eu-api.anonasecurity.com",
    "OTHERS": "https://int-api.anonasecurity.com",
}

def _md5(s: str) -> str:
    return hashlib.md5(s.encode()).hexdigest()

def sign(input_str: str) -> str:
    """Native s(): md5(input + secret)."""
    return _md5(input_str + SECRET)

def build_login_body(email: str, password: str, uuid: str, ts: int | None = None) -> dict:
    ts = ts or int(time.time() * 1000)
    hashed_pw = sign(password)                       # passWord field = md5(pw+secret)
    c10 = AUTH_STRING % (DEVICE_TYPE, email, hashed_pw)
    return {
        "ts": ts, "channel": CHANNEL, "deviceType": DEVICE_TYPE,
        "sig": sign(c10 + str(ts)),                  # md5(c10 + ts + secret)
        USER_NAME_KEY: email, "passWord": hashed_pw, "uuid": uuid,
    }

def build_signed_body(params: dict, uuid: str, token: str | None = None,
                      ts: int | None = None) -> dict:
    """Generic warpRequestBody signing used by device/status/webrtc APIs.
    input = [token] + uuid + channel + ts ; sig = md5(input + secret)."""
    ts = ts or int(time.time() * 1000)
    body = dict(params)
    body["channel"] = CHANNEL
    body["ts"] = ts
    body["uuid"] = uuid
    inp = uuid + str(CHANNEL)
    if token:
        body["token"] = token
        inp = token + inp
    body["sig"] = sign(inp + str(ts))
    return body

def post(host: str, path: str, body: dict, timeout: int = 20):
    req = urllib.request.Request(
        host + path, data=json.dumps(body).encode(), method="POST",
        headers={"Content-Type": "application/json",
                 "Accept-Language": "en-US", "UA": "okhttp/4.12.0"})
    try:
        r = urllib.request.urlopen(req, timeout=timeout)
        return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")

def login(host: str, email: str, password: str, uuid: str | None = None):
    uuid = uuid or str(_uuid.uuid4())
    return post(host, "/accountApi/V3/userLoginPwd",
                build_login_body(email, password, uuid))

if __name__ == "__main__":
    import sys
    if len(sys.argv) >= 3:
        region = sys.argv[3] if len(sys.argv) > 3 else "INTL"
        print(login(HOSTS[region], sys.argv[1], sys.argv[2]))
    else:
        print("usage: anona_sign.py <email> <password> [INTL|EU|OTHERS]")
        print("self-test (dummy account, correct vs corrupted sig):")
        uuid = str(_uuid.uuid4()); ts = int(time.time()*1000)
        em = "probe_%d@example.com" % ts
        good = build_login_body(em, "x", uuid, ts)
        bad = dict(good); bad["sig"] = good["sig"][:-1] + ("0" if good["sig"][-1] != "0" else "1")
        print(" good:", post(HOSTS["INTL"], "/accountApi/V3/userLoginPwd", good))
        print(" bad :", post(HOSTS["INTL"], "/accountApi/V3/userLoginPwd", bad))
