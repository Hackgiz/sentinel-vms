# Sentinel VMS for Linux — roadmap

Goal: feature parity with the Mac app (`../Sources`), as a headless Go server
with a web dashboard, plus a thin desktop wrapper later. The iPhone app pairs
to it exactly as it does to the Mac — the companion API is kept identical.

## Architecture

- **One Go binary** serves the web dashboard, the web API (`/api/v1/*`, cookie
  sessions) and the iPhone companion API (Mac-compatible paths, bearer tokens)
  on one port (default 8090).
- **MediaMTX** does camera ingest, recording (fMP4 segments, same layout as the
  Mac: `<data>/recordings/<cameraUUID>/…`) and HLS. It listens on 127.0.0.1
  only; browsers and phones get video through the Go server's authenticated
  proxy. Config generation carries over the Mac's MediaMTX ≥1.21 lessons
  (`%path` in recordPath, `rtspTransport(s)`, `moq: no`).
- **SQLite** (pure-Go driver, no cgo) for cameras, users, sessions, alarms,
  evidence, audit.
- **Web UI**: React + TypeScript (Vite), built into the binary with `go:embed`.

## Milestones

| # | Milestone | Status |
|---|---|---|
| M1 | Foundation: users/roles + login, cameras (RTSP), live view, 24/7 recording + retention, MediaMTX supervisor, systemd + Docker | done — verified on macOS, and on Ubuntu 26.04 arm64 (Lima VM, 2026-10-07): systemd install, setup/login, RTSP camera, live HLS via proxy, 24/7 recording + range playback, audit verify, restart recovery. Docker image not yet run. |
| M2 | Playback & export: timeline, segment playback, precise clip export (ffmpeg), snapshots | |
| M3 | Camera management: ONVIF discovery/profiles, main/sub streams, PTZ | in progress — network scan done 2026-10-07 (WS-Discovery on every LAN interface, opt-in subnet sweep, add-by-IP, ONVIF sign-in → stream picker for record/live). PTZ pending. Dev tool: `cmd/fakeonvif`. |
| M4 | iPhone compatibility: Mac companion API (pair QR, HLS proxy, events, alarm actions), cloudflared tunnel, APNs push | mostly done 2026-10-07 (v0.2.0): Mac-compatible phone API + QR pairing (hashed tokens, 5-min one-shot codes, 5-try lockout), live HLS proxy, recordings w/ Range, device registration, revoke; bundled cloudflared quick tunnel whose origin serves ONLY the phone API. Verified with the iOS app's own SentinelAPI code + the real app in Simulator (pairs, lists cameras, shows video). Open: in-app live tiles freeze after the first frames in the Simulator (Safari plays the same stream fine) — confirm on a real iPhone. Alarms/events/push arrive with M5. |
| M5 | Detection & alarms: motion + person/vehicle (ONNX), alarm queue, rules engine (cameras/hours/instructions) | mostly done — v0.3.0 motion/alarms/events/evidence; v0.4.0 (2026-10-07) person/vehicle/animal detection: YOLOX-Tiny (Apache-2.0) on bundled ONNX Runtime 1.30 loaded via purego (no cgo), run only on frames with motion (≤1/s/camera), parked-vehicle filter, per-camera alarm mode (auto / any motion / people & vehicles / people only), snapshot = analyzed frame with the detection boxed. Pending: rules engine (hours/instructions), push. |
| M6 | AI: Claude scene descriptions, NL search, digest (bring-your-own key) | done 2026-10-07 (v0.4.0): official Anthropic Go SDK; Mac prompts/models (claude-haiku-4-5 descriptions with threat level; claude-sonnet-5-5 search + digest at low effort with the server-side refusal fallback); key validated + sealed; daily description cap; descriptions on events/alarms/phone. |
| M7 | Evidence & compliance: evidence vault + integrity, hash-chained audit log, supervisor approvals, export packages | |
| M8 | Ops: layouts, floorplans + alarm overlay, storage forecast, archive, health/diagnostics | |
| M9 | Desktop wrapper, .deb + multi-arch Docker, licensing | |

## Mac → Linux mapping notes

| Mac | Linux |
|---|---|
| SwiftUI / AppKit | Web dashboard (React) |
| Apple Vision (person detection) | ONNX Runtime + YOLO (M5) |
| Network.framework HTTP server | net/http |
| Keychain / SecretVault | encrypted secrets file keyed by an install key (0600) |
| launchd LaunchAgent | systemd unit |
| GStreamer previews | MediaMTX HLS + ffmpeg for frames/exports |
