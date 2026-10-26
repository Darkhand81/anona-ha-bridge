# Cloud-bypass feasibility (redirect test, 2026-10-25)

Theoretical investigation: can the camera run without the vendor cloud? Tested by
redirecting the camera's traffic (IPFire DNAT, scoped to the camera's IP) through
a TLS MITM on a LAN box and observing what it does. Camera: HM1010, 192.168.1.213.

## Verdict

**Revised 2026-10-25 after the local-responder test: a stock-camera cloud bypass is
NOT feasible.** The camera validates the server's certificate cryptographically for
authenticated operations, and we can't forge a cert that chains to its pinned root.

- **TLS handshake is permissive, but the app layer is not.** The camera completes a
  TLS handshake with any cert (self-signed included) and does the *unauthenticated*
  `getTs` call. But it then validates the peer cert at the application layer and will
  not send the *authenticated* `activeDevice` unless the cert chains to a baked-in
  Anona root. Tested three server certs against the camera:
  - self-signed → camera loops `getTs`, never activates.
  - a fake CA with the **exact** Anona issuer name (`O=Anona, CN=com.anona.device.hm2001`)
    but our own key → still loops `getTs` (so the check is **not** name-based).
  - only the **real** vendor cert (direct to cloud) → camera activates in ~30 s.
  So the check is a real cryptographic chain validation. Without the vendor's CA
  private key (server-side only; not in the camera), we cannot impersonate the
  backend for anything past `getTs`. **Control bypass blocked.**
- **Earlier "no verification" was wrong.** It was based only on the TLS handshake
  succeeding with our cert (the probe). That tests the transport, not the app-layer
  check that actually gates the authenticated flow.
- **Onboarding bypass: also gated** (bind-cert CA signature), as before.
- **Media** is peer-to-peer (WebRTC), but you still need the cloud (or the vendor's
  key) to reach the control/signaling that sets it up.

Net: the Home Assistant bridge must go through the vendor cloud. Local-only operation
would require the vendor's device-CA private key, a firmware flaw in the cert check,
or vendor-provided local support.

## What the camera's cloud link looks like

- Connects **only to port 443**, to two device backends (both AWS us-west-2):
  - `t76-us-api.70mai.com.cn` (seen as 44.224.127.197)
  - `us-anona-device.anonasecurity.com` (seen as 52.38.70.121)
  These differ from the app's `us-api.anonasecurity.com`.
- The backend's server cert is issued by a **private Anona CA**
  (`O=Anona, CN=com.anona.device.hm2001`), not a public CA. The camera accepts our
  self-signed cert anyway, so it does not actually enforce that CA.
- The **device protocol is plain HTTPS REST** (not the app's AES-framed WebSocket).
  Captured first call, in cleartext:

  ```
  POST /baseServiceApi/V2/getTs HTTP/1.1
  Host: t76-us-api.70mai.com.cn
  Accept: */*
  Content-Type: text/plain
  Content-Length: 0

  -> 200 {"resultBodyObject":<epoch_ms>,"error":false,"errorCode":0}
  ```

  Next in the sequence is `POST /anona/device/api/activeDevice` (activation). Auth
  rides in an `mdBanya_token` header (the Banyac token family already reversed).

## Caveats learned

- A naive HTTP proxy breaks the camera's activation: converting the response from
  `Transfer-Encoding: chunked` to `Content-Length`, or rewriting headers, is enough
  that the camera completes `getTs` (200) but never advances to `activeDevice` — it
  loops `getTs` across both backends every ~60 s. Capturing the full activation and
  binding needs a **byte-faithful transparent TLS proxy** (e.g. mitmproxy, or raw
  byte-pump that preserves the exact HTTP bytes), not an `http.client` re-serializer.
- The camera and account are healthy: within 30 s of removing the redirect it
  reconnected to the real cloud and control was fully restored.

## Note on the boot/connection issue

The camera's reconnect alternates `getTs` between the two AWS Oregon backends on
443. Intermittent trouble reaching those IPs (DNS for `t76-us-api.70mai.com.cn` /
`us-anona-device.anonasecurity.com`, firewall, or latency to AWS us-west-2) would
show up exactly as slow or failed boots/reconnects. Worth watching those two hosts
on the router if the issue recurs.

## How the test was run

IPFire (the camera's gateway), scoped to the camera's IP only:
```
# redirect the camera's cloud ports to the LAN box (192.168.1.214)
iptables -t nat -A PREROUTING  -i green0 -s 192.168.1.213 -p tcp --dport 443   -j DNAT --to 192.168.1.214:8443
iptables -t nat -A PREROUTING  -i green0 -s 192.168.1.213 -p tcp --dport 8883  -j DNAT --to 192.168.1.214:8883
iptables -t nat -A PREROUTING  -i green0 -s 192.168.1.213 -p tcp --dport 10006 -j DNAT --to 192.168.1.214:10006
# fix same-subnet return path so replies go back through the router (un-NAT)
iptables -t nat -A POSTROUTING -o green0 -s 192.168.1.213 -d 192.168.1.214 -j MASQUERADE
```
Box side: a TLS probe (verdict: cert not verified) and a forwarding MITM pinning the
real backends' leaf certs. Remove all four rules to restore the camera.
