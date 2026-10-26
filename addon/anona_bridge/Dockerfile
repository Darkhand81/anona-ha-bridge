# Runtime image for the add-on. The bridge binary is cross-compiled in CI (see
# .github/workflows/build-addon.yaml) into bin/anona-bridge.<arch> before the
# Home Assistant builder invokes this Dockerfile, so there is no Go toolchain or
# source fetch here — the image is pinned to exactly the released code.
ARG BUILD_FROM
FROM ${BUILD_FROM}

# BUILD_ARCH is supplied by the Home Assistant builder (aarch64/amd64/armv7).
ARG BUILD_ARCH

# ffmpeg powers the talkback transcode (any input -> Ogg/Opus).
RUN apk add --no-cache ffmpeg

COPY bin/anona-bridge.${BUILD_ARCH} /usr/bin/anona-bridge
COPY run.sh /run.sh
RUN chmod a+x /usr/bin/anona-bridge /run.sh

CMD [ "/run.sh" ]
