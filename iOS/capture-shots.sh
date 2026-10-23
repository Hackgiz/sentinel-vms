#!/bin/bash
# Capture App Store screenshots for Sentinel VMS from demo mode.
# Boots iPhone 17 Pro Max (6.9") + iPad Pro 13" (M5), launches the Debug app
# with --demo --tab <tab>, and screenshots each tab.
#
# iPhone Duo: `./capture-shots.sh duo folded|unfolded`. simctl can't set the
# Duo's posture, so set it first with the posture control in DeviceHub
# and keep the Simulator window open (headless Duo captures come back black).
# It shoots the booted Duo as-is, without erasing it, into duo/.
set -e

APP="$(cd "$(dirname "$0")" && pwd)/build-sim/Build/Products/Debug-iphonesimulator/Sentinel Mobile.app"
BID="com.handoffgrid.sentinel.mobile"
OUT="$HOME/Desktop/sentinel-screenshots"
IPHONE="4369FB57-0F1A-4088-949B-608BE6B45B73"   # iPhone 17 Pro Max
IPAD="E74A009F-EF10-4325-B72D-D1CD0E909EF6"      # iPad Pro 13-inch (M5)

shoot() {
  local udid="$1" label="$2" subdir="$3"
  mkdir -p "$OUT/$subdir"
  # Wipe the device (clears keychain) so any real Mac pairing left over from
  # prior testing doesn't suppress demo mode (the --demo guard is !isPaired).
  xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
  xcrun simctl erase "$udid" >/dev/null 2>&1 || true
  xcrun simctl boot "$udid" >/dev/null 2>&1 || true
  xcrun simctl bootstatus "$udid" -b >/dev/null 2>&1
  xcrun simctl install "$udid" "$APP"
  for tab in home live events settings; do
    xcrun simctl terminate "$udid" "$BID" >/dev/null 2>&1 || true
    xcrun simctl launch --terminate-running-process "$udid" "$BID" --demo --tab "$tab" >/dev/null 2>&1
    sleep 11
    xcrun simctl io "$udid" screenshot "$OUT/$subdir/${label}-${tab}.png" >/dev/null 2>&1
    echo "captured $subdir/${label}-${tab}.png"
  done
}

if [ "$1" = "duo" ]; then
  posture="${2:?usage: capture-shots.sh duo folded|unfolded}"
  DUO=$(xcrun simctl list devices booted | awk -F'[()]' '/iPhone Duo/{print $2; exit}')
  [ -n "$DUO" ] || { echo "boot the iPhone Duo in Simulator first"; exit 1; }
  mkdir -p "$OUT/duo"
  xcrun simctl install "$DUO" "$APP"
  for tab in home live events; do
    xcrun simctl launch --terminate-running-process "$DUO" "$BID" --demo --tab "$tab" >/dev/null 2>&1
    sleep 11
    xcrun simctl io "$DUO" screenshot "$OUT/duo/duo-${posture}-${tab}.png" >/dev/null 2>&1
    echo "captured duo/duo-${posture}-${tab}.png"
  done
  exit 0
fi

xcrun simctl boot "$IPHONE" 2>/dev/null || true
xcrun simctl boot "$IPAD" 2>/dev/null || true

shoot "$IPHONE" "iphone69" "6.9-inch"
shoot "$IPAD" "ipad13" "13-inch"

echo "=== sizes ==="
for f in "$OUT"/6.9-inch/*.png "$OUT"/13-inch/*.png; do
  sips -g pixelWidth -g pixelHeight "$f" 2>/dev/null | awk 'NR>1{printf "%s ", $2} END{print ""}' | sed "s|^|$(basename "$f"): |"
done
echo "DONE -> $OUT"
