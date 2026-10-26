# Build the bridge binary, then assemble the runtime image on the HA base.
# The source is fetched from the published module so the add-on folder stays
# self-contained (HA builds with this folder as the Docker context). Pin
# ANONA_REF in build.yaml for reproducible builds.
ARG BUILD_FROM

FROM golang:1.23-alpine AS build
ARG ANONA_MODULE=github.com/Darkhand81/anona-ha-bridge
ARG ANONA_REF=main
RUN apk add --no-cache git
ENV CGO_ENABLED=0 GOBIN=/out
RUN go install "${ANONA_MODULE}/cmd/anona-bridge@${ANONA_REF}"

FROM ${BUILD_FROM}
# ffmpeg powers the talkback transcode (any input -> Ogg/Opus).
RUN apk add --no-cache ffmpeg
COPY --from=build /out/anona-bridge /usr/bin/anona-bridge
COPY run.sh /run.sh
RUN chmod a+x /run.sh
CMD [ "/run.sh" ]
