# Built from a release directory (see scripts/build.sh), so the image just
# copies the already cross-compiled binaries for its architecture.
# cc (glibc + libstdc++): the bundled ffmpeg needs glibc, ONNX Runtime libstdc++.
FROM gcr.io/distroless/cc-debian12:nonroot
ARG TARGETARCH
COPY linux-${TARGETARCH}/sentinel linux-${TARGETARCH}/mediamtx linux-${TARGETARCH}/cloudflared linux-${TARGETARCH}/ffmpeg /opt/sentinel/
COPY linux-${TARGETARCH}/lib /opt/sentinel/lib
COPY linux-${TARGETARCH}/models /opt/sentinel/models
ENV SENTINEL_DATA=/data
VOLUME /data
EXPOSE 8090
ENTRYPOINT ["/opt/sentinel/sentinel", "-listen", ":8090"]
