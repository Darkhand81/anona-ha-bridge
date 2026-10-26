# Obtaining camera firmware (via the app's OTA API)

Yes — the firmware is downloadable through the app's over-the-air update check, with
a normal signed request and no special access. Verified 2026-10-25 for the HM1010.

## How

Call the version-check endpoint with just the device's model identifiers (the same
signed envelope the bridge uses, `cloud.Client` / `tools/probe/anona_sign.py`):

```
POST /versionApi/V3/checkNewRomFromApp
body (+ signed envelope): {"deviceType":96, "deviceModule":96001, "deviceChannel":96001001}
```

(Only those three fields; the DTO `VersionApiV3CheckNewRomFromUserReqDTO` rejects
others such as `romVersion`/`version`.) Response:

```json
{"resultBodyObject":{
  "newVersion": true,
  "version": "1.6.244",
  "subVersion": "a",
  "fileUrl": "https://usresource.70mai.com/96001001/OTA_1208_FULL_70mai_hm1010_1.6.244_20260715_full.zip",
  "fileMd5": "a8872427e0a836784953b532d788120d",
  "fileSize": 28926197
}}
```

`fileUrl` is a **public** CDN link (`usresource.70mai.com`, no auth) — just download
it. (EU/other regions likely use a different resource host.)

## What the image is (HM1010, 1.6.244)

`fw.zip` (29 MB, md5 matches) contains:
- `update.bin` (27.6 MB) — the firmware, **unencrypted**. A composite image:
  U-Boot `uImage` kernel (Linux 4.4.94), a **JFFS2** root filesystem (~offset 2.6 MB),
  and more (bzip2 blob further in). SoC is **Ingenic (MIPS, T20/T31 class)**.
- `build_tools.zip` → `updater_binary` (MIPS ELF installer), `Verse-script.us`,
  `loadScript.us`. The install script shows the flash layout and an A/B scheme:
  `/uboot_params`, `/kernel`, `/rootfs`, `/system` (~74% of the image — the app
  partition), `/SwapAB`, `/swap_AB` (+ `swap_AB.sh`).

## Unpack attempt — all payloads ENCRYPTED (2026-10-25)

Installed binwalk + jefferson + mtd-utils and dug in. Findings:

- **Platform:** OpenHarmony (OHOS) build `HM1010-MP-Build-SI`, Ingenic MIPS,
  kernel Linux-4.4.94 (LZMA uImage at 0x78f). The updater (`build_tools/updater_binary`,
  MIPS ELF) is OHOS's, bundling OpenSSL + lz4/gzip/zlib/bzip2 and doing package
  signature verification.
- **Partition table** (in the `uboot_params` header at offset 0, "70Mai" magic):
  `version_list, board_list, uboot_params, kernel, rootfs (~5.6 MB), system (~25 MB),
  SwapAB, swap_AB`. `update.bin` is these raw partition images concatenated and
  `raw_image_write`-n by the updater.
- **The rootfs/system images are encrypted.** Their data is near-maximal entropy
  (rootfs 7.73, system 7.99) with **no** filesystem magic (no squashfs/jffs2/
  cramfs/ubifs/ext4) and **no** compression header; no standard compressor
  (zlib/gzip/deflate/lzma/xz/bz2/lz4) decodes them; and there are **zero** plaintext
  userland strings in the whole image (no busybox, `/bin/sh`, libc, `anona`, `getTs`,
  `BEGIN CERTIFICATE`). Only the partition-table header and the kernel uImage header
  are readable. jefferson/binwalk extract nothing.

## Image format + is the decrypt key recoverable? (reversed 2026-10-25)

Worked out the full image format and traced the decrypt path. **The key is NOT
recoverable from any downloadable artifact.**

**Image format** — `update.bin` is a 70Mai composite:
- A **plaintext** partition-table header at offset 0 (`01 00 88 00 … "70Mai"`),
  listing `version_list, board_list, uboot_params, kernel, rootfs, system, SwapAB,
  swap_AB` with each partition's stored size + flash size.
- The partition payloads, concatenated. **All payloads are encrypted** — not just
  rootfs/system. The `kernel` payload's uImage header advertises `comp=LZMA` and the
  body opens with a copied LZMA-alone header (`5d 00 00 00 02 ff…`), but decoding it
  yields 45 KB of garbage (repeating `<p>%&%`, entropy 5.7) then a stream error; a
  real Linux-4.4.94 kernel would decompress to several MB of MIPS code + readable
  strings. So the LZMA header is a leftover/decoy and the body is ciphertext
  (uniform ~7.95 entropy), exactly like rootfs (`0e cf 74 93…`) and system
  (`8f d2 45 d8…`). Only the small headers are cleartext.

**The updater never decrypts.** `build_tools/Verse-script.us` contains only
`raw_image_write` (×6), `show_progress`, one `System()` — **no decrypt/AES verb**. It
writes every payload to flash **verbatim**. (The `updater_binary` does link full
OpenSSL — `AES_*`, `EVP_*`, `CMS_*` — but that is for *package-signature*
verification; the flash path never invokes it. Confirmed from the script, so no MIPS
disassembly was needed.)

**Therefore the decrypt happens at boot, with a key that is not in anything we can
download:**
- The OTA is **model-generic and fetched unauthenticated** by model id, so whatever
  decrypts these payloads is **shared across all HM1010 units** and lives **on-device**
  (u-boot / Ingenic boot ROM / eFuse), applied before/at boot — it is deliberately
  *not* shipped in the package.
- The updater holds no image key (it does raw writes).
- The `uboot_params` partition in the OTA is config, not the u-boot binary; the
  bootloader that would hold/derive the key is not updated by this OTA.

**So the pinned Anona CA and the server-cert-validation code CANNOT be recovered from
the OTA download alone, and neither can the image decrypt key.** Getting the rootfs
would require a **hardware flash/UART/JTAG dump from a live camera** (which yields the
decrypted on-flash filesystem, or the u-boot binary that holds the key) — not static
analysis of the download. If the on-device key is eFuse/OTP-derived it is per-SoC and
not software-recoverable at all.

This is consistent with the LOCAL-BYPASS.md verdict: the device chain is locked down
(signed + fully-encrypted OTA, cryptographic server-cert validation). The firmware
route to find a cert-check weakness is gated by the same encryption.

Downloaded copy (this session, scratchpad): `…/scratchpad/firmware/`
(`fw.zip`, `update.bin`, `bt/` = updater + scripts).
