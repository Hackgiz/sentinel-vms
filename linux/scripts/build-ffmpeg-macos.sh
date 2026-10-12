#!/bin/bash
# Builds an LGPL ffmpeg for macOS from the official FFmpeg source release.
#
#   scripts/build-ffmpeg-macos.sh <arm64|amd64> <output-dir>
#
# Writes <output-dir>/ffmpeg and <output-dir>/ffmpeg-LICENSE.txt. Results are
# cached in $SENTINEL_CACHE (default linux/.cache), so a release build only
# compiles once per FFmpeg version and architecture (about 3–6 minutes).
#
# Why build it ourselves: there is no reputable *LGPL* static ffmpeg for macOS
# to download (evermeet.cx, osxexperts and martin-riedl.de ship GPL builds
# with x264/x265; BtbN's builds, used for Linux, have no macOS target).
# Building from the release tarball gives us:
#   - LGPL-2.1-or-later only: no --enable-gpl / --enable-nonfree, no external
#     codec libraries. Only Apple system frameworks/libraries are linked
#     (VideoToolbox, CoreMedia, CoreVideo, AudioToolbox, libz, libbz2),
#     so the binary is self-contained on any Mac.
#   - VideoToolbox hardware decode (`-hwaccel videotoolbox`) for detection.
#   - --disable-autodetect, so nothing from Homebrew/MacPorts on the build
#     machine leaks in and the result is the same everywhere.
#
# Intel (amd64) cross-builds on Apple Silicon. Hand-written x86 assembly
# needs nasm; without it the build uses --disable-x86asm (C fallbacks; the
# heavy H.264/HEVC decode still goes to VideoToolbox).
set -euo pipefail

ARCH="${1:?usage: build-ffmpeg-macos.sh <arm64|amd64> <output-dir>}"
OUT="${2:?usage: build-ffmpeg-macos.sh <arm64|amd64> <output-dir>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CACHE="${SENTINEL_CACHE:-$ROOT/.cache}"

# FFmpeg release (same 8.1 line as the BtbN Linux builds). SHA-256 of the
# ffmpeg.org tarball (also GPG-signed: ffmpeg-$FFMPEG_VERSION.tar.xz.asc).
FFMPEG_VERSION="8.1.3"
FFMPEG_SHA256="7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3"
MACOS_MIN="12.0" # Go 1.25+ binaries need macOS 12 too

case "$ARCH" in
arm64) CLANG_ARCH=arm64 FF_ARCH=aarch64 ;;
amd64) CLANG_ARCH=x86_64 FF_ARCH=x86_64 ;;
*) echo "unknown arch $ARCH" >&2; exit 2 ;;
esac

# Bump CONFIG_REV when the configure flags below change.
CONFIG_REV=1
BUILT="$CACHE/ffmpeg-$FFMPEG_VERSION-macos-$ARCH-lgpl-r$CONFIG_REV"
mkdir -p "$CACHE" "$OUT"

if [ ! -x "$BUILT/ffmpeg" ]; then
    TARBALL="$CACHE/ffmpeg-$FFMPEG_VERSION.tar.xz"
    [ -f "$TARBALL" ] || curl -fsSL -o "$TARBALL" "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz"
    if [ "$(shasum -a 256 "$TARBALL" | cut -d' ' -f1)" != "$FFMPEG_SHA256" ]; then
        echo "ERROR: checksum mismatch for $TARBALL" >&2
        rm -f "$TARBALL"
        exit 1
    fi
    SRC="$(mktemp -d "${TMPDIR:-/tmp}/sentinel-ffmpeg.XXXXXX")"
    trap 'rm -rf "$SRC"' EXIT
    tar -xJf "$TARBALL" -C "$SRC"
    cd "$SRC/ffmpeg-$FFMPEG_VERSION"

    EXTRA=()
    if [ "$(uname -m)" != "$CLANG_ARCH" ]; then
        EXTRA+=(--enable-cross-compile)
    fi
    if [ "$FF_ARCH" = x86_64 ] && ! command -v nasm >/dev/null; then
        echo "    (nasm not found: building amd64 ffmpeg without x86 assembly)"
        EXTRA+=(--disable-x86asm)
    fi
    SDK="$(xcrun --sdk macosx --show-sdk-path)"
    echo "==> Configuring FFmpeg $FFMPEG_VERSION (macOS $ARCH, LGPL)"
    FLAGS=(
        --arch="$FF_ARCH" --target-os=darwin
        --cc="clang -arch $CLANG_ARCH"
        --extra-cflags="-mmacosx-version-min=$MACOS_MIN"
        --extra-ldflags="-mmacosx-version-min=$MACOS_MIN"
        --disable-autodetect
        --enable-videotoolbox --enable-audiotoolbox
        --enable-zlib --enable-bzlib --enable-pthreads
        --enable-static --disable-shared
        --disable-doc --disable-debug --disable-ffplay --disable-ffprobe
        ${EXTRA[@]+"${EXTRA[@]}"}
    )
    ./configure --sysroot="$SDK" "${FLAGS[@]}" >"$SRC/configure.log" || { tail -30 "$SRC/configure.log"; cat ffbuild/config.log | tail -40; exit 1; }
    grep -q "^License: LGPL" "$SRC/configure.log" || { echo "ERROR: configure did not produce an LGPL build" >&2; grep '^License' "$SRC/configure.log" >&2; exit 1; }
    echo "==> Compiling FFmpeg (macOS $ARCH)"
    make -j"$(sysctl -n hw.ncpu)" ffmpeg >"$SRC/make.log" 2>&1 || { tail -40 "$SRC/make.log"; exit 1; }
    strip -x ffmpeg
    mkdir -p "$BUILT"
    install -m 0755 ffmpeg "$BUILT/ffmpeg"
    cp COPYING.LGPLv2.1 "$BUILT/ffmpeg-LICENSE.txt"
    {
        echo
        echo "----"
        echo "FFmpeg $FFMPEG_VERSION, built unmodified from"
        echo "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz (sha256 $FFMPEG_SHA256)"
        echo "by Sentinel's scripts/build-ffmpeg-macos.sh with: ./configure ${FLAGS[*]}"
    } >>"$BUILT/ffmpeg-LICENSE.txt"
fi

install -m 0755 "$BUILT/ffmpeg" "$OUT/ffmpeg"
cp "$BUILT/ffmpeg-LICENSE.txt" "$OUT/ffmpeg-LICENSE.txt"
echo "    ffmpeg $FFMPEG_VERSION (macOS $ARCH, LGPL) -> $OUT/ffmpeg"
