# Contributing to Sentinel VMS

Thanks for helping. Sentinel is a free video management system built by a
working security installer, and it gets better with every camera model,
network and bug report people bring to it.

## Ways to help without writing code

- **Test it on your cameras.** Open an issue with the camera brand, model and
  firmware, and whether discovery, live view, recording and playback worked.
  The "Camera compatibility" issue template asks the right questions.
- **Report bugs.** The apps have *Help → Send Feedback*, or open an issue.
- **Try the Linux server** on hardware we don't have: x86 mini PCs, Raspberry
  Pi 4/5, NAS boxes, Docker hosts.

## Repository layout

| Folder | What it is | Language |
|---|---|---|
| `Sources/`, `Tests/` | Mac app (`HandoffGridSentinel`) and its core libraries | Swift, SwiftPM |
| `iOS/` | Sentinel Mobile, the iPhone companion app | Swift, Xcode |
| `linux/` | Headless server with a web dashboard | Go + React |
| `scripts/` | Icon and GStreamer bundling helpers for the Mac build | shell |

`MEDIA_ARCHITECTURE.md` explains the Mac video pipeline. `linux/ROADMAP.md`
tracks the Linux milestones.

## Building

**Mac app** (macOS 13+, Xcode 16+ command line tools):

```sh
swift build
swift test
./build-app.sh          # assembles a runnable .app; see the script header
```

Run the GUI through `build-app.sh`, not by opening the package in Xcode.
Release signing is optional: without `DEVELOPER_ID_APPLICATION` the script
ad-hoc signs.

**iPhone app:** open `iOS/Sentinel Mobile.xcodeproj`, pick your own team under
Signing & Capabilities, and run on a simulator or device.

**Linux server** (Go 1.27+, Node 22+):

```sh
cd linux/web && npm ci && npm run build && cd ..
go test ./...
go run ./cmd/sentinel -listen 127.0.0.1:8091 -data /tmp/sentinel-dev
```

`go run ./cmd/fakeonvif` starts a fake ONVIF camera for testing discovery.

## Rules that keep things working

- **The phone API is shared.** The iPhone app talks to both the Mac and the
  Linux server. Changes to pairing, `/api/companion/*` responses or the pairing
  QR format must stay compatible with both, and with older phone versions.
- **Don't add AGPL or GPL dependencies.** LGPL is fine only as a separate,
  dynamically loaded program (like GStreamer and FFmpeg today).
- **Never log secrets.** Camera passwords, pairing tokens, API keys and tunnel
  URLs go through the existing redaction helpers.
- **Keep the phone non-destructive.** The iPhone can view and acknowledge, but
  deleting recordings or changing users stays on the Mac or server.

## Pull requests

1. Open an issue first for anything bigger than a small fix, so we can agree
   on the approach before you spend time on it.
2. Keep each PR to one change. Include tests where the code has them.
3. CI must pass: Mac build and tests, Linux tests, dashboard build.
4. Sign off every commit (Developer Certificate of Origin):

   ```sh
   git commit -s -m "Fix ONVIF discovery on multi-homed hosts"
   ```

   The `Signed-off-by` line certifies you wrote the change or have the right
   to submit it under the Apache-2.0 license (https://developercertificate.org).

By contributing you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
