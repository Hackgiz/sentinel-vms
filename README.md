# Sentinel VMS

A free video management system (VMS) for Mac, Linux and iPhone. Record IP
cameras around the clock, review footage on a timeline, and get alerts on
your phone.

Sentinel was built by a security installer who spent years putting in VMS
systems and couldn't find a good, affordable option for the Mac. It is a side
project given to the community. Official builds are at
**[sentvms.com](https://sentvms.com)**.

## What it does

- **Finds cameras** on your network with ONVIF discovery, or add any RTSP URL.
- **Records your way**, per camera or server-wide: 24/7, events only (with
  pre- and post-roll), or low-res 24/7 + full-res on events. Standard MP4
  segments on any local disk, kept for as long as you choose, and the oldest
  video can make room when the disk fills.
- **Finds each camera's second stream** in one click (ONVIF, or the usual
  addresses tried for you) for lighter live view, detection and low-res recording.
- **Playback timeline** with scrubbing and precise clip export.
- **Motion, person and vehicle detection**, all on-device.
- **Alarms** grouped into incidents, with an evidence vault and audit log.
- **iPhone app** (Sentinel Mobile): live view, playback, PTZ, events and alerts
  at home or away, through an encrypted Cloudflare tunnel. No port forwarding.
  With the optional Sentinel Cloud service, a spare iPhone can also be a camera,
  and videos can be sent from the phone straight into the evidence vault.
- **Optional AI descriptions** of events using your own Anthropic API key.
  Nothing leaves your network unless you turn this on.

## Pieces

| Folder | What it is |
|---|---|
| [`Sources/`](Sources) | Mac app, SwiftUI, video through GStreamer and MediaMTX |
| [`iOS/`](iOS) | Sentinel Mobile, the iPhone companion app |
| [`linux/`](linux) | Headless Linux server (Go) with a React web dashboard; also runs in Docker |

The iPhone app pairs with either the Mac or the Linux server by scanning a QR
code.

## Quick start for developers

```sh
# Mac app
swift build && swift test
./build-app.sh

# Linux server
cd linux/web && npm ci && npm run build && cd ..
go test ./...
go run ./cmd/sentinel -listen 127.0.0.1:8091 -data /tmp/sentinel-dev
```

The iPhone app opens in Xcode from `iOS/Sentinel Mobile.xcodeproj`; choose
your own signing team. Push notifications need your own APNs key and Team ID
(set `SentinelAPNsTeamID` in the Mac app's Info.plist).

See [CONTRIBUTING.md](CONTRIBUTING.md) for details and the project's ground rules.

## Help wanted

- Camera compatibility reports: brand, model, firmware, what worked.
- Linux testing on mini PCs, Raspberry Pi 4/5, NAS boxes and Docker hosts.
- PTZ camera control, alarm schedules, and more languages.

Look for issues labeled `good first issue`.

## Supporting the project

Sentinel is free. If it saves you money on a job, you can
[support the project](https://donate.stripe.com/6oUfZj2Rg74u2pU6CT2ZO00).
It helps pay for the Apple developer account, hosting and test cameras.

## License

[Apache License 2.0](LICENSE). The names "Sentinel VMS" and "Sentinel
Mobile" and the logos are trademarks of Handoffgrid LLC and are not covered
by the license; see [NOTICE](NOTICE). If you ship a modified version, please
give it a different name.

Security issues: see [SECURITY.md](SECURITY.md).
