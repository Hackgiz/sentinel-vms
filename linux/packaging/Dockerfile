# syntax=docker/dockerfile:1
# Built from the Docker context that scripts/build.sh writes to dist/docker/,
# so the image just copies the already cross-compiled binaries for its arch:
#   docker buildx build --platform linux/amd64,linux/arm64 -t sentinel dist/docker
#
# Base: distroless cc (glibc + libstdc++, ca-certificates, tzdata, no shell).
#  - sentinel loads ONNX Runtime with purego, which makes it a dynamically
#    linked glibc binary; ONNX Runtime itself needs libstdc++.
#  - ffmpeg (BtbN static build), mediamtx and cloudflared are self-contained.
# Runs as distroless "nonroot" (uid/gid 65532).
FROM gcr.io/distroless/cc-debian12:nonroot
ARG TARGETARCH
ARG VERSION=dev

LABEL org.opencontainers.image.title="Sentinel VMS" \
      org.opencontainers.image.description="Self-hosted video management server: recording, live view, detection, iPhone app" \
      org.opencontainers.image.url="https://sentvms.com" \
      org.opencontainers.image.source="https://github.com/Hackgiz/sentinel-vms" \
      org.opencontainers.image.version="${VERSION}"

# The binaries look for their helpers next to themselves:
#   /opt/sentinel/{mediamtx,ffmpeg,cloudflared}, lib/libonnxruntime.so, models/yolox_tiny.onnx
COPY linux-${TARGETARCH}/sentinel linux-${TARGETARCH}/mediamtx linux-${TARGETARCH}/cloudflared linux-${TARGETARCH}/ffmpeg /opt/sentinel/
COPY linux-${TARGETARCH}/lib /opt/sentinel/lib
COPY linux-${TARGETARCH}/models /opt/sentinel/models
COPY linux-${TARGETARCH}/licenses /opt/sentinel/licenses

# /data must exist in the image, owned by nonroot: a new named volume copies
# this directory's owner and mode, so the server can write to it. (Distroless
# has no shell to RUN mkdir/chown, so the build context carries an empty
# rootfs/data directory.) Bind mounts keep the host directory's owner; see
# docker-compose.yml.
COPY --chown=65532:65532 rootfs/ /

ENV SENTINEL_DATA=/data
# Tells the dashboard to show Docker upgrade steps instead of install.sh.
ENV SENTINEL_DOCKER=1
VOLUME /data
EXPOSE 8090 18189/udp 18189/tcp
USER 65532:65532

# The binary probes itself (GET /api/v1/status on localhost); there is no curl.
# If you change -listen, pass the same -listen to the healthcheck.
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD ["/opt/sentinel/sentinel", "-healthcheck", "-listen", ":8090"]

ENTRYPOINT ["/opt/sentinel/sentinel", "-listen", ":8090"]
