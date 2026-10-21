#!/bin/bash
# Installs (or upgrades) Sentinel VMS as a macOS LaunchDaemon. Run from the
# unzipped release:
#
#   sudo ./install.sh [--listen :8090] [--data DIR] [--env NAME=value ...]
#
# - Program:  /usr/local/sentinel (sentinel, mediamtx, ffmpeg, cloudflared, ONNX Runtime, model)
# - Data:     /Library/Application Support/Sentinel (or --data), owned by _sentinel
# - Service:  /Library/LaunchDaemons/com.handoffgrid.sentinel.server.plist,
#             runs as the hidden _sentinel user, starts at boot
# - Logs:     /Library/Logs/Sentinel/sentinel.log (rotated by newsyslog)
#
# Upgrades keep the existing service settings (address, data directory,
# environment) unless --listen, --data or --env are given; then the service
# definition is rewritten with exactly those. Data is never touched.
set -euo pipefail

LABEL="com.handoffgrid.sentinel.server"
PLIST="/Library/LaunchDaemons/$LABEL.plist"
PREFIX="/usr/local/sentinel"
DATA="/Library/Application Support/Sentinel"
LOGDIR="/Library/Logs/Sentinel"
SVC_USER="_sentinel"
LISTEN=":8090"
ENV_ARGS=()
REWRITE=0

while [ $# -gt 0 ]; do
    case "$1" in
    --listen) LISTEN="$2"; REWRITE=1; shift 2 ;;
    --data) DATA="$2"; REWRITE=1; shift 2 ;;
    --env) ENV_ARGS+=(-env "$2"); REWRITE=1; shift 2 ;;
    -h | --help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown option $1 (see --help)" >&2; exit 2 ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo ./install.sh" >&2
    exit 1
fi
here="$(cd "$(dirname "$0")" && pwd)"
case "$DATA" in /*) ;; *) echo "--data must be an absolute path" >&2; exit 2 ;; esac

# Refuse binaries whose signature is broken (e.g. a damaged download), then
# clear the browser's quarantine flag so launchd can start them.
for f in sentinel mediamtx ffmpeg cloudflared lib/libonnxruntime.dylib; do
    [ -f "$here/$f" ] || continue
    if ! codesign --verify --strict "$here/$f" 2>/dev/null; then
        echo "ERROR: $f has no valid code signature; download the release again." >&2
        exit 1
    fi
done
xattr -dr com.apple.quarantine "$here" 2>/dev/null || true

# Hidden role account, like the system's own _www or _postgres.
if ! dscl . -read "/Users/$SVC_USER" >/dev/null 2>&1; then
    used="$( (dscl . -list /Users UniqueID; dscl . -list /Groups PrimaryGroupID) | awk '{print $2}' | sort -n | uniq)"
    id=""
    for candidate in $(seq 300 499); do
        if ! printf '%s\n' "$used" | grep -qx "$candidate"; then id="$candidate"; break; fi
    done
    [ -n "$id" ] || { echo "ERROR: no free system UID/GID between 300 and 499" >&2; exit 1; }
    echo "==> Creating service account $SVC_USER (uid/gid $id)"
    if ! dscl . -read "/Groups/$SVC_USER" >/dev/null 2>&1; then
        dscl . -create "/Groups/$SVC_USER"
        dscl . -create "/Groups/$SVC_USER" PrimaryGroupID "$id"
        dscl . -create "/Groups/$SVC_USER" RealName "Sentinel VMS"
        dscl . -create "/Groups/$SVC_USER" Password "*"
    fi
    gid="$(dscl . -read "/Groups/$SVC_USER" PrimaryGroupID | awk '{print $2}')"
    dscl . -create "/Users/$SVC_USER"
    dscl . -create "/Users/$SVC_USER" UniqueID "$id"
    dscl . -create "/Users/$SVC_USER" PrimaryGroupID "$gid"
    dscl . -create "/Users/$SVC_USER" UserShell /usr/bin/false
    dscl . -create "/Users/$SVC_USER" NFSHomeDirectory /var/empty
    dscl . -create "/Users/$SVC_USER" RealName "Sentinel VMS"
    dscl . -create "/Users/$SVC_USER" Password "*"
    dscl . -create "/Users/$SVC_USER" IsHidden 1
fi

# Stop the running service before replacing its files.
if launchctl print "system/$LABEL" >/dev/null 2>&1; then
    echo "==> Stopping the running service"
    launchctl bootout "system/$LABEL" 2>/dev/null || true
    for _ in $(seq 1 40); do launchctl print "system/$LABEL" >/dev/null 2>&1 || break; sleep 0.5; done
fi

echo "==> Installing to $PREFIX"
install -d -m 0755 "$PREFIX" "$PREFIX/lib" "$PREFIX/models"
install -m 0755 "$here/sentinel" "$PREFIX/sentinel"
install -m 0755 "$here/mediamtx" "$PREFIX/mediamtx"
[ -f "$here/ffmpeg" ] && install -m 0755 "$here/ffmpeg" "$PREFIX/ffmpeg"
[ -f "$here/cloudflared" ] && install -m 0755 "$here/cloudflared" "$PREFIX/cloudflared"
if [ -f "$here/lib/libonnxruntime.dylib" ] && [ -f "$here/models/yolox_tiny.onnx" ]; then
    install -m 0755 "$here/lib/libonnxruntime.dylib" "$PREFIX/lib/libonnxruntime.dylib"
    for m in "$here"/models/*; do install -m 0644 "$m" "$PREFIX/models/$(basename "$m")"; done
fi
for f in THIRD-PARTY.txt ffmpeg-LICENSE.txt onnxruntime-LICENSE.txt uninstall.sh; do
    [ -f "$here/$f" ] && install -m 0644 "$here/$f" "$PREFIX/$f"
done
chmod 0755 "$PREFIX/uninstall.sh" 2>/dev/null || true
chown -R root:wheel "$PREFIX"

install -d -m 0750 -o "$SVC_USER" -g "$SVC_USER" "$DATA"
install -d -m 0750 -o "$SVC_USER" -g "$SVC_USER" "$LOGDIR"
cat >/etc/newsyslog.d/$LABEL.conf <<EOF
# Sentinel VMS server log: keep 7 x 10 MB, compressed.
# logfilename                      [owner:group]        mode count size(KB) when flags
$LOGDIR/sentinel.log  $SVC_USER:$SVC_USER  640  7     10240    *    JN
EOF

if [ ! -f "$PLIST" ] || [ "$REWRITE" = 1 ]; then
    echo "==> Writing $PLIST"
    "$PREFIX/sentinel" service plist -program "$PREFIX/sentinel" -data "$DATA" -listen "$LISTEN" \
        ${ENV_ARGS[@]+"${ENV_ARGS[@]}"} >"$PLIST.tmp"
    plutil -lint "$PLIST.tmp" >/dev/null
    mv "$PLIST.tmp" "$PLIST"
    chown root:wheel "$PLIST"
    chmod 0644 "$PLIST"
else
    echo "==> Keeping the existing service settings ($PLIST)"
fi

# The application firewall (if on) would otherwise block incoming connections
# to the dashboard and WebRTC media.
FW=/usr/libexec/ApplicationFirewall/socketfilterfw
if [ -x "$FW" ] && "$FW" --getglobalstate 2>/dev/null | grep -qi "enabled"; then
    for f in "$PREFIX/sentinel" "$PREFIX/mediamtx"; do
        "$FW" --add "$f" >/dev/null 2>&1 || true
        "$FW" --unblockapp "$f" >/dev/null 2>&1 || true
    done
    echo "==> Allowed sentinel and mediamtx through the macOS firewall"
fi

echo "==> Starting the service"
launchctl bootstrap system "$PLIST"
launchctl enable "system/$LABEL"

port="${LISTEN##*:}"
ip="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)"
echo
echo "Sentinel VMS is running at http://${ip:-localhost}:${port:-8090}"
echo "First install? Open it to create the administrator account."
echo "Status:  sudo launchctl print system/$LABEL | head -20"
echo "Logs:    tail -f $LOGDIR/sentinel.log"
echo "Remove:  sudo $PREFIX/uninstall.sh   (keeps recordings; --purge deletes data and the $SVC_USER account)"
