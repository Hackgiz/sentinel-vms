#!/bin/bash
# Removes the Sentinel VMS LaunchDaemon and program files.
#
#   sudo /usr/local/sentinel/uninstall.sh           # keeps data and recordings
#   sudo /usr/local/sentinel/uninstall.sh --purge   # also deletes data, logs and the _sentinel account
set -euo pipefail

LABEL="com.handoffgrid.sentinel.server"
PLIST="/Library/LaunchDaemons/$LABEL.plist"
PREFIX="/usr/local/sentinel"
LOGDIR="/Library/Logs/Sentinel"
SVC_USER="_sentinel"
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo $0 $*" >&2
    exit 1
fi

# The data directory may have been moved with install.sh --data.
DATA="/Library/Application Support/Sentinel"
if [ -f "$PLIST" ]; then
    d="$(plutil -extract ProgramArguments.2 raw -o - "$PLIST" 2>/dev/null || true)"
    [ -n "$d" ] && DATA="$d"
fi

if launchctl print "system/$LABEL" >/dev/null 2>&1; then
    echo "==> Stopping the service"
    launchctl bootout "system/$LABEL" 2>/dev/null || true
fi
rm -f "$PLIST" "/etc/newsyslog.d/$LABEL.conf"
FW=/usr/libexec/ApplicationFirewall/socketfilterfw
for f in "$PREFIX/sentinel" "$PREFIX/mediamtx"; do
    [ -x "$FW" ] && "$FW" --remove "$f" >/dev/null 2>&1 || true
done
rm -rf "$PREFIX"
echo "==> Removed the service and $PREFIX"

if [ "$PURGE" = 1 ]; then
    # Only delete what is recognizably a Sentinel data directory.
    if [ -f "$DATA/sentinel.db" ] || [ "$DATA" = "/Library/Application Support/Sentinel" ]; then
        rm -rf "$DATA"
    else
        echo "Not deleting $DATA: it doesn't look like a Sentinel data directory." >&2
    fi
    rm -rf "$LOGDIR"
    dscl . -delete "/Users/$SVC_USER" 2>/dev/null || true
    dscl . -delete "/Groups/$SVC_USER" 2>/dev/null || true
    echo "==> Deleted $DATA, $LOGDIR and the $SVC_USER account"
else
    echo "Kept data and recordings in $DATA (sudo $0 --purge deletes them)."
fi
