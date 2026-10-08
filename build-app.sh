#!/usr/bin/env bash
# Build a shareable Sentinel VMS.app bundle.
#
#   ./build-app.sh
#
# Output: dist/Sentinel VMS.app  + dist/Sentinel VMS.zip
#
# DISTRIBUTION MODES
# ------------------
# The script picks one of two signing modes based on the environment:
#
#   1. Developer ID + notarization  (for shipping outside the Mac App Store)
#      Triggered when DEVELOPER_ID_APPLICATION is set in the environment
#      (e.g. "Developer ID Application: Your Name (TEAMID)").
#      If NOTARY_PROFILE is also set, the script will submit to Apple's
#      notary service and staple the ticket. NOTARY_PROFILE is the name
#      of a keychain profile created via:
#          xcrun notarytool store-credentials <profile> \
#              --apple-id <id> --team-id <TEAMID> --password <app-specific-pw>
#
#   2. Ad-hoc  (preview builds for friends — Gatekeeper requires right-click)
#      Used when DEVELOPER_ID_APPLICATION is unset. The bundle still runs
#      but Apple will warn on first launch.
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$PWD"
DIST="$ROOT/dist"
APP="$DIST/Sentinel VMS.app"
APP_NAME="Sentinel VMS"
BUNDLE_ID="com.handoffgrid.sentinel"
ENTITLEMENTS="$ROOT/Sources/HandoffGridSentinel/HandoffGridSentinel.entitlements"
PRIVACY_MANIFEST="$ROOT/Sources/HandoffGridSentinel/PrivacyInfo.xcprivacy"

DEV_ID="${DEVELOPER_ID_APPLICATION:-}"
NOTARY="${NOTARY_PROFILE:-}"

if [ -n "$DEV_ID" ]; then
    echo "==> Signing mode: Developer ID (\"$DEV_ID\")"
    if [ -n "$NOTARY" ]; then
        echo "    Notarization: enabled (profile: $NOTARY)"
    else
        echo "    Notarization: SKIPPED (set NOTARY_PROFILE to enable)"
    fi
else
    echo "==> Signing mode: ad-hoc (preview only; not for App Store / public release)"
fi

echo "==> Cleaning previous build…"
rm -rf "$DIST"
mkdir -p "$DIST"

echo "==> Building release binary (this can take a couple of minutes)…"
# Ask SwiftPM where it put the binary rather than guessing: the universal-build
# output folder moved between Xcode releases (.build/apple/Products → .build/out/
# Products), and guessing the old path silently shipped a months-old binary.
if swift build -c release --arch arm64 --arch x86_64; then
    BIN_DIR="$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)"
else
    echo "    universal build failed — falling back to this Mac's architecture only"
    swift build -c release
    BIN_DIR="$(swift build -c release --show-bin-path)"
fi
BIN="$BIN_DIR/HandoffGridSentinel"
if [ ! -x "$BIN" ]; then
    echo "ERROR: release binary not found at $BIN"
    exit 1
fi
# Belt and braces: never bundle a binary older than the sources it should contain.
NEWEST_SOURCE="$(find "$ROOT/Sources" -name '*.swift' -newer "$BIN" -print -quit)"
if [ -n "$NEWEST_SOURCE" ]; then
    echo "ERROR: $BIN is older than $NEWEST_SOURCE — refusing to bundle a stale build"
    exit 1
fi
echo "    using $BIN ($(lipo -archs "$BIN"))"

echo "==> Assembling $APP_NAME.app bundle…"
mkdir -p "$APP/Contents/MacOS"
mkdir -p "$APP/Contents/Resources"

cp "$BIN" "$APP/Contents/MacOS/HandoffGridSentinel"
cp "$ROOT/Sources/HandoffGridSentinel/Info.plist" "$APP/Contents/Info.plist"
cp "$PRIVACY_MANIFEST" "$APP/Contents/Resources/PrivacyInfo.xcprivacy"

echo "==> Rendering app icon (.icns)…"
ICONSET="$DIST/AppIcon.iconset"
rm -rf "$ICONSET"
mkdir -p "$ICONSET"
swift "$ROOT/scripts/make-icon.swift" "$ICONSET"
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
rm -rf "$ICONSET"

# Bundle MediaMTX so the recipient doesn't need to install it separately.
MEDIAMTX_SRC="$HOME/Library/Application Support/HandoffGridSentinel/mediamtx"
if [ -x "$MEDIAMTX_SRC" ]; then
    cp "$MEDIAMTX_SRC" "$APP/Contents/Resources/mediamtx"
    chmod +x "$APP/Contents/Resources/mediamtx"
    echo "    bundled MediaMTX from $MEDIAMTX_SRC"
else
    echo "    WARNING: MediaMTX not found at $MEDIAMTX_SRC"
    echo "             friends will need: brew install mediamtx"
fi

# Bundle a trimmed, self-contained GStreamer runtime so users don't need to
# install GStreamer separately. Set BUNDLE_GSTREAMER=0 to skip (smaller build
# that relies on a system GStreamer install at runtime).
if [ "${BUNDLE_GSTREAMER:-1}" != "0" ]; then
    echo "==> Staging bundled GStreamer runtime…"
    "$ROOT/scripts/stage-gstreamer.sh" "$APP"
else
    echo "==> Skipping GStreamer bundling (BUNDLE_GSTREAMER=0)"
fi

# Write a simple PkgInfo so Launch Services treats it as a real app.
printf 'APPL????' > "$APP/Contents/PkgInfo"

# -----------------------------------------------------------------------------
# Codesigning
# -----------------------------------------------------------------------------
# For Developer ID + notarization we need:
#   - Every embedded executable / dylib signed first (inside-out)
#   - The outer .app signed last, with --options runtime (Hardened Runtime)
#     and our entitlements
# -----------------------------------------------------------------------------

if [ -n "$DEV_ID" ]; then
    SIGN_IDENTITY="$DEV_ID"
    SIGN_OPTS=(--force --options runtime --timestamp)
else
    SIGN_IDENTITY="-"
    SIGN_OPTS=(--force)
fi

echo "==> Codesigning embedded executables…"
# Sign any nested Mach-O binaries (mediamtx, dylibs we may add later).
while IFS= read -r -d '' f; do
    # Skip the main executable; we sign that with the app.
    if [ "$f" = "$APP/Contents/MacOS/HandoffGridSentinel" ]; then
        continue
    fi
    file "$f" | grep -qE "Mach-O|dynamically linked shared library" || continue
    echo "    signing $(basename "$f")"
    codesign "${SIGN_OPTS[@]}" --sign "$SIGN_IDENTITY" \
        ${DEV_ID:+--entitlements "$ENTITLEMENTS"} \
        "$f"
done < <(find "$APP/Contents" -type f -print0)

echo "==> Codesigning app bundle…"
codesign "${SIGN_OPTS[@]}" --sign "$SIGN_IDENTITY" \
    ${DEV_ID:+--entitlements "$ENTITLEMENTS"} \
    --identifier "$BUNDLE_ID" \
    "$APP"

echo "==> Verifying signature…"
codesign --verify --deep --strict --verbose=2 "$APP" || {
    echo "ERROR: signature verification failed"
    exit 1
}
if [ -n "$DEV_ID" ]; then
    spctl --assess --type execute --verbose=2 "$APP" || \
        echo "    (spctl warning above is expected before notarization)"
fi

# -----------------------------------------------------------------------------
# Notarization
# -----------------------------------------------------------------------------
if [ -n "$DEV_ID" ] && [ -n "$NOTARY" ]; then
    echo "==> Submitting to Apple notary service…"
    NOTARY_ZIP="$DIST/_notary.zip"
    ditto -c -k --sequesterRsrc --keepParent "$APP" "$NOTARY_ZIP"
    xcrun notarytool submit "$NOTARY_ZIP" \
        --keychain-profile "$NOTARY" \
        --wait
    rm -f "$NOTARY_ZIP"

    echo "==> Stapling notarization ticket…"
    xcrun stapler staple "$APP"
    xcrun stapler validate "$APP"
fi

echo "==> Zipping for distribution…"
cd "$DIST"
ditto -c -k --sequesterRsrc --keepParent "$APP_NAME.app" "$APP_NAME.zip"
cd "$ROOT"

# Write a README the recipient sees if they expand the zip into a folder.
if [ -n "$DEV_ID" ] && [ -n "$NOTARY" ]; then
    cat > "$DIST/README.txt" <<'EOF'
Sentinel VMS

INSTALL
  1. Unzip "Sentinel VMS.zip"
  2. Drag "Sentinel VMS.app" into /Applications
  3. Double-click to launch.

REQUIREMENTS
  - macOS 13 (Ventura) or newer, Apple Silicon or Intel
  - At least one ONVIF / RTSP camera on the same Wi-Fi

Everything else (GStreamer media engine, MediaMTX) is bundled inside the
app — there is nothing else to install.
EOF
else
    cat > "$DIST/README.txt" <<'EOF'
Sentinel VMS — preview build

INSTALL
  1. Unzip "Sentinel VMS.zip"
  2. Drag "Sentinel VMS.app" into /Applications

FIRST RUN (one-time, preview builds only)
  Because this build isn't notarized through Apple, the first launch
  needs an explicit override:

  - Right-click (or Control-click) "Sentinel VMS.app" in Finder
  - Choose "Open" → confirm "Open" in the dialog
  - From then on, double-clicking works normally.

  If the dialog won't let you open it, go to:
    System Settings → Privacy & Security → scroll down → "Open Anyway"

REQUIREMENTS
  - macOS 13 (Ventura) or newer, Apple Silicon
  - At least one ONVIF / RTSP camera on the same Wi-Fi
  (GStreamer + MediaMTX are bundled — nothing else to install.)
EOF
fi

SIZE=$(du -sh "$APP" | awk '{print $1}')
ZIP_SIZE=$(du -sh "$DIST/$APP_NAME.zip" | awk '{print $1}')

echo ""
echo "Done."
echo ""
echo "    App:    $APP   ($SIZE)"
echo "    Zip:    $DIST/$APP_NAME.zip   ($ZIP_SIZE)"
echo "    Notes:  $DIST/README.txt"
echo ""
if [ -n "$DEV_ID" ] && [ -n "$NOTARY" ]; then
    echo "    Status: signed with Developer ID + notarized + stapled. Ready to ship."
elif [ -n "$DEV_ID" ]; then
    echo "    Status: signed with Developer ID (NOT notarized). Set NOTARY_PROFILE to ship."
else
    echo "    Status: ad-hoc signed. Preview only; recipients must right-click → Open."
fi
