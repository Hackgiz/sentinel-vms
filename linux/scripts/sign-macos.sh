#!/bin/bash
# Code-signs a staged macOS Sentinel folder (dist/sentinel-<v>-macos-<arch>/)
# with the hardened runtime and, optionally, notarizes it.
#
#   scripts/sign-macos.sh [--identity ID] [--notarize] [--dry-run] <stage-dir>
#
# Identity: --identity, else $DEVELOPER_ID_APPLICATION (same variable as
# ../../build-app.sh, e.g. "Developer ID Application: Handoffgrid llc (TEAMID)"),
# else "-" (ad-hoc: local testing only, Gatekeeper will refuse it elsewhere).
#
# Notarization uses the same keychain profile as build-app.sh:
#   xcrun notarytool store-credentials <profile> --apple-id … --team-id … --password <app-specific>
#   NOTARY_PROFILE=<profile> scripts/sign-macos.sh --notarize dist/sentinel-1.0.0-macos-arm64
# It zips the folder (ditto), submits it and waits. Apple can't staple a
# ticket to a zip or a bare executable, so Gatekeeper checks it online on
# first run; a stapled .pkg needs a "Developer ID Installer" certificate
# (not set up yet). --dry-run prints the notarytool commands instead.
#
# What gets signed (inside out):
#   lib/libonnxruntime.dylib  our identity. With a Developer ID, sentinel and the
#                             dylib share a Team ID, so the hardened runtime's
#                             library validation allows the purego dlopen — no
#                             disable-library-validation entitlement needed.
#   ffmpeg, mediamtx          our identity, hardened runtime, no entitlements
#   cloudflared               kept as shipped when Cloudflare's Developer ID
#                             signature (hardened runtime, timestamped) is valid
#   sentinel                  our identity, hardened runtime; no entitlements with
#                             a Developer ID. Ad-hoc builds get
#                             packaging/macos/sentinel-adhoc.entitlements
#                             (disable-library-validation), because ad-hoc
#                             signatures have no Team ID to match.
# Sentinel needs no other entitlements: it isn't sandboxed (network.server/
# client are App Sandbox entitlements), and Go, MediaMTX, ffmpeg and ONNX
# Runtime don't JIT.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IDENTITY="${DEVELOPER_ID_APPLICATION:--}"
NOTARIZE=0
DRY=0
while [ $# -gt 1 ]; do
    case "$1" in
    --identity) IDENTITY="$2"; shift 2 ;;
    --notarize) NOTARIZE=1; shift ;;
    --dry-run) DRY=1; shift ;;
    *) break ;;
    esac
done
DIR="${1:?usage: sign-macos.sh [--identity ID] [--notarize] [--dry-run] <stage-dir>}"
[ -x "$DIR/sentinel" ] || { echo "ERROR: $DIR/sentinel not found" >&2; exit 1; }
BUNDLE_ID="com.handoffgrid.sentinel"

if [ "$IDENTITY" = "-" ]; then
    echo "==> Signing $(basename "$DIR") ad-hoc (local testing only)"
    OPTS=(--force --options runtime --timestamp=none --sign -)
    MAIN_ENT=(--entitlements "$ROOT/packaging/macos/sentinel-adhoc.entitlements")
else
    echo "==> Signing $(basename "$DIR") as \"$IDENTITY\""
    OPTS=(--force --options runtime --timestamp --sign "$IDENTITY")
    MAIN_ENT=()
fi

# codesign's description of a file (captured first: `codesign | grep -q`
# would SIGPIPE codesign and fail under pipefail).
sig_info() { codesign -dvv "$1" 2>&1 || true; }

sign() { # file identifier [extra args]
    local f="$1" id="$2"
    shift 2
    codesign "${OPTS[@]}" --identifier "$id" "$@" "$f"
    echo "    signed $(basename "$f") ($id)"
}

for lib in "$DIR"/lib/*.dylib; do
    [ -f "$lib" ] && sign "$lib" "$BUNDLE_ID.onnxruntime"
done
for tool in ffmpeg mediamtx; do
    [ -f "$DIR/$tool" ] && sign "$DIR/$tool" "$BUNDLE_ID.$tool"
done
if [ -f "$DIR/cloudflared" ]; then
    info="$(sig_info "$DIR/cloudflared")"
    if codesign --verify --strict "$DIR/cloudflared" 2>/dev/null &&
        grep -q "Authority=Developer ID Application: Cloudflare" <<<"$info" &&
        grep -q "flags=.*runtime" <<<"$info"; then
        echo "    kept Cloudflare's signature on cloudflared"
    else
        sign "$DIR/cloudflared" "$BUNDLE_ID.cloudflared"
    fi
fi
sign "$DIR/sentinel" "$BUNDLE_ID.server" ${MAIN_ENT[@]+"${MAIN_ENT[@]}"}

echo "==> Verifying"
for f in "$DIR/sentinel" "$DIR/mediamtx" "$DIR/ffmpeg" "$DIR/cloudflared" "$DIR"/lib/*.dylib; do
    [ -f "$f" ] || continue
    codesign --verify --strict --verbose=1 "$f" 2>&1 | sed 's/^/    /'
    grep -q "flags=.*runtime" <<<"$(sig_info "$f")" || { echo "ERROR: $f lacks the hardened runtime" >&2; exit 1; }
done

if [ "$NOTARIZE" = 1 ]; then
    if [ "$IDENTITY" = "-" ] && [ "$DRY" = 0 ]; then
        echo "ERROR: notarization needs a Developer ID identity (set DEVELOPER_ID_APPLICATION)" >&2
        exit 1
    fi
    PROFILE="${NOTARY_PROFILE:-}"
    [ -n "$PROFILE" ] || [ "$DRY" = 1 ] || { echo "ERROR: set NOTARY_PROFILE (xcrun notarytool store-credentials)" >&2; exit 1; }
    ZIP="$DIR.notary.zip"
    CMDS=(
        "ditto -c -k --sequesterRsrc --keepParent \"$DIR\" \"$ZIP\""
        "xcrun notarytool submit \"$ZIP\" --keychain-profile \"${PROFILE:-<NOTARY_PROFILE>}\" --wait"
        "rm -f \"$ZIP\""
    )
    if [ "$DRY" = 1 ]; then
        echo "==> Notarization (dry run; would run):"
        printf '    %s\n' "${CMDS[@]}"
    else
        echo "==> Notarizing (this waits for Apple)"
        ditto -c -k --sequesterRsrc --keepParent "$DIR" "$ZIP"
        xcrun notarytool submit "$ZIP" --keychain-profile "$PROFILE" --wait
        rm -f "$ZIP"
    fi
fi
