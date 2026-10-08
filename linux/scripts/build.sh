#!/bin/bash
# Builds release archives for Linux amd64 + arm64:
#   dist/sentinel-<version>-linux-<arch>.tar.gz  (sentinel, mediamtx, systemd unit, installer)
# and a Docker build context in dist/docker/.
#
#   VERSION=1.0.0 scripts/build.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always 2>/dev/null || echo dev)}"
MEDIAMTX_VERSION="v1.21.1"
# cloudflared (iPhone remote access). SHA-256s pinned from the release notes,
# which match GitHub's asset digests. Bump all three together.
CLOUDFLARED_VERSION="2026.10.0"
CLOUDFLARED_SHA256_amd64="d33ff2d14475178d2012c2c56beba87389ac5ded27649519f198a7d3134a99db"
CLOUDFLARED_SHA256_arm64="e6422b9d4f72d3194bc5a38676f13667c06666523217b842a877d72a80b5ac08"
# ffmpeg (motion detection + snapshots): BtbN LGPL static build, decode-only use.
# Pinned to a dated autobuild (the "latest" tag is rebuilt daily). SHA-256s are
# GitHub's asset digests. Bump all four together.
FFMPEG_RELEASE="autobuild-2026-10-07-13-07"
FFMPEG_BUILD="ffmpeg-n8.1.3-14-g330caae0c1"
FFMPEG_SHA256_amd64="8e51013f0977f0d59f2ecfb7a5c8baec7f4c6896ebd47a3877b9ca7431f2ce47"
FFMPEG_SHA256_arm64="0f4c1b60fec2076db6ebb639a2106f704007a5415ce97916c07efc59300737e2"
# Person/vehicle detection: ONNX Runtime (MIT; sha256 = GitHub asset digests)
# and YOLOX-Tiny (Apache-2.0; Megvii publishes no digest — pinned on first download).
ORT_VERSION="1.30.0"
ORT_SHA256_amd64="a5ed5a3cac51fbb2e90da632ae43d19212faaa20e76484e62bcb7c23ddb3b3fd"
ORT_SHA256_arm64="e16a27a8ed330bbc698df7330b0cf56e722f354e3bcc92118682c74ef3c3e3da"
YOLOX_URL="https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_tiny.onnx"
YOLOX_SHA256="427cc366d34e27ff7a03e2899b5e3671425c262ea2291f88bb942bc1cc70b0f7"

verify() { # file expected-sha256
    if [ "$(shasum -a 256 "$1" | cut -d' ' -f1)" != "$2" ]; then
        echo "ERROR: checksum mismatch for $1" >&2
        rm -f "$1"
        exit 1
    fi
}
DIST="$ROOT/dist"
CACHE="$ROOT/.cache"
export PATH="$HOME/.local/go/bin:$PATH"

echo "==> Dashboard"
(cd "$ROOT/web" && npm ci --silent && npm run build --silent)

rm -rf "$DIST" && mkdir -p "$DIST/docker" "$CACHE"

[ -f "$CACHE/yolox_tiny.onnx" ] || curl -fsSL -o "$CACHE/yolox_tiny.onnx" "$YOLOX_URL"
verify "$CACHE/yolox_tiny.onnx" "$YOLOX_SHA256"

echo "==> MediaMTX $MEDIAMTX_VERSION checksums"
SUMS="$CACHE/mediamtx-$MEDIAMTX_VERSION.sha256"
[ -f "$SUMS" ] || curl -fsSL -o "$SUMS" "https://github.com/bluenviron/mediamtx/releases/download/$MEDIAMTX_VERSION/checksums.sha256"

for ARCH in amd64 arm64; do
    echo "==> linux/$ARCH"
    STAGE="$DIST/sentinel-$VERSION-linux-$ARCH"
    mkdir -p "$STAGE"
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath \
        -ldflags "-s -w -X main.Version=$VERSION" -o "$STAGE/sentinel" ./cmd/sentinel)

    TARBALL="mediamtx_${MEDIAMTX_VERSION}_linux_${ARCH}.tar.gz"
    [ -f "$CACHE/$TARBALL" ] || curl -fsSL -o "$CACHE/$TARBALL" \
        "https://github.com/bluenviron/mediamtx/releases/download/$MEDIAMTX_VERSION/$TARBALL"
    # Lines are "<sha256> *<file>" (sha256sum binary mode) or "<sha256>  <file>".
    EXPECTED="$(awk -v f="$TARBALL" '{ n = $2; sub(/^\*/, "", n); if (n == f) print $1 }' "$SUMS")"
    ACTUAL="$(shasum -a 256 "$CACHE/$TARBALL" | cut -d' ' -f1)"
    if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
        echo "ERROR: checksum mismatch for $TARBALL" >&2
        exit 1
    fi
    tar -xzf "$CACHE/$TARBALL" -C "$STAGE" mediamtx

    CF="$CACHE/cloudflared-$CLOUDFLARED_VERSION-linux-$ARCH"
    [ -f "$CF" ] || curl -fsSL -o "$CF" \
        "https://github.com/cloudflare/cloudflared/releases/download/$CLOUDFLARED_VERSION/cloudflared-linux-$ARCH"
    CF_EXPECTED_VAR="CLOUDFLARED_SHA256_$ARCH"
    if [ "$(shasum -a 256 "$CF" | cut -d' ' -f1)" != "${!CF_EXPECTED_VAR}" ]; then
        echo "ERROR: checksum mismatch for cloudflared-linux-$ARCH" >&2
        rm -f "$CF"
        exit 1
    fi
    install -m 0755 "$CF" "$STAGE/cloudflared"

    case "$ARCH" in amd64) FFARCH=linux64 ;; arm64) FFARCH=linuxarm64 ;; esac
    FFTAR="$FFMPEG_BUILD-$FFARCH-lgpl-8.1.tar.xz"
    [ -f "$CACHE/$FFTAR" ] || curl -fsSL -o "$CACHE/$FFTAR" \
        "https://github.com/BtbN/FFmpeg-Builds/releases/download/$FFMPEG_RELEASE/$FFTAR"
    FF_EXPECTED_VAR="FFMPEG_SHA256_$ARCH"
    if [ "$(shasum -a 256 "$CACHE/$FFTAR" | cut -d' ' -f1)" != "${!FF_EXPECTED_VAR}" ]; then
        echo "ERROR: checksum mismatch for $FFTAR" >&2
        rm -f "$CACHE/$FFTAR"
        exit 1
    fi
    tar -xJf "$CACHE/$FFTAR" -C "$STAGE" --strip-components=2 "${FFTAR%.tar.xz}/bin/ffmpeg"
    tar -xJf "$CACHE/$FFTAR" -C "$STAGE" --strip-components=1 "${FFTAR%.tar.xz}/LICENSE.txt"
    mv "$STAGE/LICENSE.txt" "$STAGE/ffmpeg-LICENSE.txt"

    case "$ARCH" in amd64) ORTARCH=x64 ;; arm64) ORTARCH=aarch64 ;; esac
    ORTTAR="onnxruntime-linux-$ORTARCH-$ORT_VERSION.tgz"
    [ -f "$CACHE/$ORTTAR" ] || curl -fsSL -o "$CACHE/$ORTTAR" \
        "https://github.com/microsoft/onnxruntime/releases/download/v$ORT_VERSION/$ORTTAR"
    ORT_EXPECTED_VAR="ORT_SHA256_$ARCH"
    verify "$CACHE/$ORTTAR" "${!ORT_EXPECTED_VAR}"
    mkdir -p "$STAGE/lib" "$STAGE/models"
    tar -xzf "$CACHE/$ORTTAR" -C "$STAGE/lib" --strip-components=2 "${ORTTAR%.tgz}/lib/libonnxruntime.so.$ORT_VERSION"
    mv "$STAGE/lib/libonnxruntime.so.$ORT_VERSION" "$STAGE/lib/libonnxruntime.so"
    tar -xzf "$CACHE/$ORTTAR" -C "$STAGE" --strip-components=1 "${ORTTAR%.tgz}/LICENSE"
    mv "$STAGE/LICENSE" "$STAGE/onnxruntime-LICENSE.txt"
    install -m 0644 "$CACHE/yolox_tiny.onnx" "$STAGE/models/yolox_tiny.onnx"
    cat > "$STAGE/THIRD-PARTY.txt" <<NOTICE
Sentinel VMS for Linux bundles these unmodified third-party programs:

- MediaMTX $MEDIAMTX_VERSION (MIT) — https://github.com/bluenviron/mediamtx
- cloudflared $CLOUDFLARED_VERSION (Apache-2.0) — https://github.com/cloudflare/cloudflared
- ONNX Runtime $ORT_VERSION (MIT) — https://github.com/microsoft/onnxruntime — license in onnxruntime-LICENSE.txt
- YOLOX-Tiny object detection model (Apache-2.0, Megvii) — https://github.com/Megvii-BaseDetection/YOLOX
- FFmpeg $FFMPEG_BUILD, LGPL-2.1-or-later build by BtbN — license in ffmpeg-LICENSE.txt.
  Binary: https://github.com/BtbN/FFmpeg-Builds/releases/tag/$FFMPEG_RELEASE
  Source: https://github.com/FFmpeg/FFmpeg (build scripts: https://github.com/BtbN/FFmpeg-Builds)
NOTICE

    cp "$ROOT/packaging/sentinel.service" "$ROOT/packaging/install.sh" "$STAGE/"
    # --no-xattrs/COPYFILE_DISABLE: keep macOS metadata out, or GNU tar on Linux warns on extract.
    COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -czf "$STAGE.tar.gz" -C "$DIST" "$(basename "$STAGE")"

    mkdir -p "$DIST/docker/linux-$ARCH"
    cp -R "$STAGE/sentinel" "$STAGE/mediamtx" "$STAGE/cloudflared" "$STAGE/ffmpeg" "$STAGE/lib" "$STAGE/models" "$DIST/docker/linux-$ARCH/"
done
cp "$ROOT/packaging/Dockerfile" "$DIST/docker/"

echo
echo "Done:"
ls -lh "$DIST"/*.tar.gz
echo "Docker: docker buildx build --platform linux/amd64,linux/arm64 -t sentinel:$VERSION $DIST/docker"
