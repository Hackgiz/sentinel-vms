#!/bin/bash
# Capture App Store screenshots for Sentinel VMS from demo mode.
# Boots iPhone 17 Pro Max (6.9") + iPad Pro 13" (M5), launches the Debug app
# with --demo --tab <tab>, and screenshots each tab.
set -e

APP="$(cd "$(dirname "$0")" && pwd)/build-sim/Build/Products/Debug-iphonesimulator/Sentinel Mobile.app"
BID="com.handoffgrid.sentinel.mobile"
OUT="$HOME/Desktop/sentinel-screenshots"
IPHONE="02531E17-9FA0-4A12-A13B-F512ED56F66C"   # iPhone 17 Pro Max
IPAD="50929589-F16E-4283-86A4-913D28F60567"      # iPad Pro 13-inch (M5)

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

xcrun simctl boot "$IPHONE" 2>/dev/null || true
xcrun simctl boot "$IPAD" 2>/dev/null || true

shoot "$IPHONE" "iphone69" "6.9-inch"
shoot "$IPAD" "ipad13" "13-inch"

echo "=== sizes ==="
for f in "$OUT"/6.9-inch/*.png "$OUT"/13-inch/*.png; do
  sips -g pixelWidth -g pixelHeight "$f" 2>/dev/null | awk 'NR>1{printf "%s ", $2} END{print ""}' | sed "s|^|$(basename "$f"): |"
done
echo "DONE -> $OUT"
