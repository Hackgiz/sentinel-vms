#!/usr/bin/env bash
# Stage a trimmed, self-contained GStreamer runtime inside the app bundle so
# users don't have to install GStreamer separately.
#
#   scripts/stage-gstreamer.sh "/path/to/Sentinel VMS.app"
#
# The official macOS GStreamer.framework is ~3.8 GB because it ships static
# archives, headers, and ~257 plugins. We only need the runtime libraries plus
# the handful of plugins the app's pipelines actually use, which brings the
# bundled payload down to ~300 MB.
#
# The framework is built with @rpath / @loader_path install names, so the tree
# is fully relocatable: copying it anywhere and preserving the bin/lib/libexec
# structure is enough — no install_name_tool rewriting required. build-app.sh
# signs every nested Mach-O afterwards.
set -euo pipefail

APP="${1:?usage: stage-gstreamer.sh <app-bundle>}"
SRC="${GSTREAMER_ROOT:-/Library/Frameworks/GStreamer.framework/Versions/1.0}"
DEST="$APP/Contents/Resources/gstreamer"

if [ ! -d "$SRC" ]; then
    echo "    WARNING: GStreamer not found at $SRC"
    echo "             app will fall back to a system GStreamer install if present."
    exit 0
fi

# Plugins required by the app's pipelines (live preview + tile/recording ingest)
# plus the decoders/depayloaders decodebin auto-plugs for common ONVIF/RTSP
# cameras (H.264, H.265, MJPEG, MP4/MKV/MPEG-TS containers, AAC audio).
PLUGINS=(
    libgstcoreelements        # queue, typefind, filesink, capsfilter, identity, fakesink
    libgsttypefindfunctions   # media type detection
    libgstplayback            # uridecodebin, decodebin, playbin
    libgstautodetect          # autovideosink / autoaudiosink
    libgstvideoconvertscale   # videoconvert, videoscale
    libgstvideorate           # videorate
    libgstvideofilter         # base video filters
    libgstvideoparsersbad     # h264parse, h265parse
    libgstapplemedia          # VideoToolbox hardware decode (vtdec)
    libgstlibav               # software avdec_h264 / avdec_h265 fallback
    libgstjpeg                # jpegenc / jpegdec
    libgstpng                 # pngenc / pngdec
    libgstosxvideo            # native macOS video sink
    libgstopengl              # glimagesink (autovideosink target)
    libgstrtp                 # RTP (de)payloaders
    libgstrtsp                # rtspsrc
    libgstrtpmanager          # rtpbin / rtpjitterbuffer
    libgstudp                 # default RTSP/UDP transport
    libgsttcp                 # RTSP-over-TCP transport
    libgstdtls                # secured RTSP
    libgstsrtp                # secured RTP
    libgstsoup                # souphttpsrc (HTTP/HLS sources)
    libgstmultifile           # multifilesink (JPEG frame dump)
    libgstisomp4              # qtdemux / MP4
    libgstmatroska            # matroskademux / MKV
    libgstmpegtsdemux         # tsdemux / MPEG-TS
    libgstaudioconvert
    libgstaudioresample
    libgstaudioparsers        # aacparse etc.
    libgstapp                 # appsrc / appsink
    libgstcoretracers
)

echo "    staging GStreamer runtime from $SRC"
rm -rf "$DEST"
mkdir -p "$DEST/bin" "$DEST/lib/gstreamer-1.0" "$DEST/libexec/gstreamer-1.0"

# Command-line tools the app spawns.
for tool in gst-launch-1.0 gst-inspect-1.0; do
    cp "$SRC/bin/$tool" "$DEST/bin/$tool"
done

# All top-level runtime dylibs (glib, gobject, ffmpeg, codecs, …). Copying the
# whole set guarantees every plugin's @rpath dependency closure is satisfied.
# Use -a so the many `libfoo.dylib -> libfoo.N.dylib` version symlinks stay
# symlinks instead of being dereferenced into full duplicate copies (which
# would roughly double the payload).
cp -a "$SRC"/lib/*.dylib "$DEST/lib/"

# Curated plugin set.
missing=()
for p in "${PLUGINS[@]}"; do
    f="$SRC/lib/gstreamer-1.0/$p.dylib"
    if [ -f "$f" ]; then
        cp "$f" "$DEST/lib/gstreamer-1.0/"
    else
        missing+=("$p")
    fi
done
if [ "${#missing[@]}" -gt 0 ]; then
    echo "    NOTE: plugins not found in this GStreamer build (skipped): ${missing[*]}"
fi

# Out-of-process plugin scanner (gstreamer execs this; its rpath resolves to ../../lib).
if [ -f "$SRC/libexec/gstreamer-1.0/gst-plugin-scanner" ]; then
    cp "$SRC/libexec/gstreamer-1.0/gst-plugin-scanner" "$DEST/libexec/gstreamer-1.0/"
fi

# GIO modules (TLS for https sources) if present.
if [ -d "$SRC/lib/gio/modules" ]; then
    mkdir -p "$DEST/lib/gio/modules"
    cp "$SRC"/lib/gio/modules/*.so "$DEST/lib/gio/modules/" 2>/dev/null || true
fi

SIZE=$(du -sh "$DEST" | awk '{print $1}')
PLUGIN_COUNT=$(ls "$DEST/lib/gstreamer-1.0/"*.dylib 2>/dev/null | wc -l | tr -d ' ')
echo "    bundled GStreamer: $SIZE  ($PLUGIN_COUNT plugins)"
