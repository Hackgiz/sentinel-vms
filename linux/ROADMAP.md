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
| M1+ | Low-latency live view: WebRTC (WHEP) with automatic HLS fallback | done 2026-10-10: MediaMTX WHEP on loopback behind an authenticated, same-origin, rate-limited proxy (`/webrtc/<camera>/whep`); media on UDP+TCP 18189 (`-webrtc-port`/`SENTINEL_WEBRTC_PORT`, 0 = off), no STUN; extra ICE addresses via System page or `SENTINEL_WEBRTC_HOSTS`; the browser's own address is added to the answer when it's an IP (Docker bridge works when the port is published 1:1). Live page: Auto/WebRTC/HLS per browser; Auto falls back to HLS on refusal (e.g. H.265 without browser support), no decoded frame within 4 s, or repeated disconnects. Verified with headless Chrome against MediaMTX 1.21.1 on macOS (WebRTC over UDP, ICE-TCP only, codec-refusal fallback). Not yet tried: a real H.265 camera, Docker, Linux host, Firefox/Safari. |
| M2 | Playback & export: timeline, segment playback, precise clip export (ffmpeg), snapshots | done 2026-10-10 — **Playback** page: day timeline per camera (recorded spans, gaps, event ticks, alarm flags, 24h/6h/1h/10m zoom, click/drag scrub), play/pause, ±10 s, previous/next event, 1–8× speed, date picker, 2–4 cameras in sync. Served by Sentinel, not MediaMTX's playback server: each segment's fMP4 fragments are indexed (cached, extended while a segment is still recording) and a day becomes an HLS VOD playlist of keyframe-to-keyframe pieces, re-packed on request from the original files (MediaMTX's ~1 s parts don't start on keyframes, which made hls.js loop after seeks). Clip export (≤30 min, stream copy via ffmpeg; gaps closed up) as a download or into the evidence vault (SHA-256, read-only), JPEG snapshot at the playhead; Operator+ only, all audited. Checked against real MediaMTX 1.21.1 recordings (index, clip, snapshot with ffmpeg 6) and in headless Chrome (play, scrub, gap skip, 2-camera sync, event jumps, export/evidence/snapshot). Not yet on Linux/a real camera. |
| M3 | Camera management: ONVIF discovery/profiles, main/sub streams, PTZ | in progress — network scan done 2026-10-07 (WS-Discovery on every LAN interface, opt-in subnet sweep, add-by-IP, ONVIF sign-in → stream picker for record/live). PTZ pending. Dev tool: `cmd/fakeonvif`. |
| M4 | iPhone compatibility: Mac companion API (pair QR, HLS proxy, events, alarm actions), cloudflared tunnel, APNs push | mostly done 2026-10-07 (v0.2.0): Mac-compatible phone API + QR pairing (hashed tokens, 5-min one-shot codes, 5-try lockout), live HLS proxy, recordings w/ Range, device registration, revoke; bundled cloudflared quick tunnel whose origin serves ONLY the phone API. Verified with the iOS app's own SentinelAPI code + the real app in Simulator (pairs, lists cameras, shows video). Open: in-app live tiles freeze after the first frames in the Simulator (Safari plays the same stream fine) — confirm on a real iPhone. Alarms/events/push arrive with M5. |
| M5 | Detection & alarms: motion + person/vehicle (ONNX), alarm queue, rules engine (cameras/hours/instructions) | mostly done — v0.3.0 motion/alarms/events/evidence; v0.4.0 (2026-10-07) person/vehicle/animal detection: YOLOX-Tiny (Apache-2.0) on bundled ONNX Runtime 1.30 loaded via purego (no cgo), run only on frames with motion (≤1/s/camera), parked-vehicle filter, per-camera alarm mode (auto / any motion / people & vehicles / people only), snapshot = analyzed frame with the detection boxed. 2026-10-10: alarm rules engine (Mac NotificationRule parity + days of week, minute precision, overnight windows in server time; camera/kind/min-severity filters; instructions on dashboard alarms and in the phone API's `instructions` [{rule,text}]; Admin-only editor, audited) and iPhone push (`internal/push`: bounded async queue with retries, dead-token cleanup; Mac payload shape; relay client + direct-APNs with your own .p8; quiet hours; per-camera 60 s coalescing), plus a low-disk "Storage" alarm. Pending for push: deploy the relay (`sentvms-dl-worker/src/push-relay.js`, proposal) and confirm on a real iPhone. |
| M6 | AI: Claude scene descriptions, NL search, digest (bring-your-own key) | done 2026-10-07 (v0.4.0): official Anthropic Go SDK; Mac prompts/models (claude-haiku-4-5 descriptions with threat level; claude-sonnet-5-5 search + digest at low effort with the server-side refusal fallback); key validated + sealed; daily description cap; descriptions on events/alarms/phone. |
| M7 | Evidence & compliance: evidence vault + integrity, hash-chained audit log, supervisor approvals, export packages | |
| M8 | Ops: layouts, floorplans + alarm overlay, storage forecast, archive, health/diagnostics | |
| M9 | Desktop wrapper, .deb + multi-arch Docker, licensing | Docker in progress 2026-10-10: distroless image fixed for runtime (/data owned by nonroot for named volumes, licenses shipped, `-healthcheck` HEALTHCHECK, embedded tzdata for `TZ`), `packaging/docker-compose.yml` (host networking for ONVIF multicast + correct pairing address, `SENTINEL_LAN_URL` for bridge mode, commented /dev/dri + Coral/Hailo + WebRTC port), GHCR multi-arch workflow on `v*` tags. Image not yet built or run (no Docker on the dev Mac). .deb, desktop wrapper, licensing pending. |

## Integrations

- **MQTT + Home Assistant** — done 2026-10-10: paho MQTT client (EPL-2.0/EDL-1.0)
  publishing availability (LWT), per-camera motion/person/vehicle/online
  binary sensors with a 30 s off-timeout, event + alarm JSON streams, last
  event + snapshot image; Home Assistant MQTT discovery; opt-in, audited
  recording/detection switches; System-page settings with sealed password and
  connection test. Bounded drop-oldest queue, reconnect with backoff. Tested
  against an in-process broker (mochi-mqtt, test-only). Not yet tried against
  a real Home Assistant install.

## Hardware acceleration (2026-10-11)

Goal: more cameras per small box (Intel N100, Raspberry Pi 5).

| Piece | Status |
|---|---|
| VAAPI decode for detection (Intel/AMD) | done — startup probe (`ffmpeg -hwaccels`, render node, one DRM-only device open), `-init_hw_device vaapi=va:<node>,connection_type=drm -hwaccel vaapi -hwaccel_device va` with frames downloaded to system memory so the existing filter graph is unchanged; per-camera fallback to CPU for 1 h on device error / no frame in 20 s / GPU rejects stream. Bundled BtbN ffmpeg has VAAPI on amd64+arm64 (needs host libva + VA driver). `SENTINEL_FFMPEG` / `SENTINEL_HWACCEL_DEVICE` overrides. |
| Quick Sync decode | done as second choice (only when VAAPI fails to open; amd64 build only; needs codec from MediaMTX → `h264_qsv`/`hevc_qsv` with a QSV device derived from the DRM VAAPI device) |
| OpenVINO detection (Intel iGPU/NPU) | done — `SENTINEL_ORT_LIB` → OpenVINO-enabled ONNX Runtime (API ≥ 17); provider appended + warm-up inference, else CPU with the reason shown. Engine switch applies on restart. |
| System page card + `GET/PUT /api/v1/hwaccel` (Auto/Off, audited, ChangeSettings) | done |
| systemd: `/dev/dri` reachable (PrivateDevices off, `DevicePolicy=closed`, `DeviceAllow=char-drm`/`char-accel`, `~@raw-io`), `sentinel` in `render`/`video` | done (`systemd-analyze security`: 3.7, same as before) |
| Verified on real GPU hardware | **no** — everything above is tested with fakes, real ffmpeg 8.1 failure paths (no GPU) and the macOS ONNX Runtime; first N100 run still needed |

TODO:
- **Raspberry Pi 5 HEVC decode.** No H.264 hardware decoder exists; the HEVC
  block needs the V4L2 request API, only in Raspberry Pi OS's ffmpeg fork
  (`-hwaccel drm`, SAND frame formats). Would need: `SENTINEL_FFMPEG` pointing
  at it, a `drm` method for H.265 streams only, and a check that downloaded
  frames are really NV12 (garbled frames would cause false motion). Not done
  because it can't be verified here.
- **Coral Edge TPU (USB / M.2).** Needs an `*_edgetpu.tflite` detector (e.g.
  SSD MobileNet v2 or YOLOv8n compiled with `edgetpu_compiler`), TensorFlow
  Lite's C library + `libedgetpu` loaded via purego, and its own pre/post-
  processing. Plugs in via `detect.Backend` / `NewObjectDetectorWithBackend`.
  Google's runtime is effectively unmaintained (2022 libedgetpu; the gasket
  PCIe driver breaks on new kernels), so weigh before building. Presence is
  already detected (`internal/hwinfo`) and shown as "not supported yet".
- **Hailo-8/8L (Pi AI Kit / AI HAT+).** Needs a `.hef` model (Hailo Model Zoo
  YOLO, compiled with Hailo's x86-only Dataflow Compiler) and HailoRT's C API
  via purego; same `Backend` hook. Presence detected (`/dev/hailo*`).
- Docker image: add a variant with libva + `intel-media-va-driver` so GPU decode
  works in containers (distroless has none).
- Optional: live (no-restart) detection engine switch; runtime fallback when
  OpenVINO inference starts failing after startup.

## Mac → Linux mapping notes

| Mac | Linux |
|---|---|
| SwiftUI / AppKit | Web dashboard (React) |
| Apple Vision (person detection) | ONNX Runtime + YOLO (M5) |
| Network.framework HTTP server | net/http |
| Keychain / SecretVault | encrypted secrets file keyed by an install key (0600) |
| launchd LaunchAgent | systemd unit |
| GStreamer previews | MediaMTX WebRTC (WHEP) / HLS + ffmpeg for frames/exports |

## Known issues (2.0.0)

- The Linux Operator AppImage launcher needs `libz.so` (the dev symlink), which
  stock Ubuntu lacks — the app inside runs fine once extracted. Before offering
  Operator-for-Linux downloads: build the AppImage with a runtime that does not
  need it (or ship .deb/tar.gz built on Linux as the primary format). The .deb
  cannot be cross-built on macOS (fpm needs GNU tar); build it on Linux.
