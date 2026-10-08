# Sentinel Media Architecture

This project should be built as a Mac-first VMS with an iOS-ready client path.

## Recommendation

Use this split:

```text
RTSP / ONVIF Cameras
    -> macOS Sentinel Media Engine
       -> GStreamer ingest
       -> VideoToolbox hardware decode/encode where possible
       -> local segmented recording
       -> HLS for Apple clients
       -> WebRTC later for low-latency remote viewing
    -> macOS SwiftUI app
    -> future iOS app using AVPlayer for HLS
```

## Why

AVFoundation and AVPlayer are excellent for Apple-native playback, especially HLS and local files, but they are not the right foundation for broad RTSP camera ingest. GStreamer is a better fit for the ingest side because it is designed around live media pipelines, RTSP/RTP, plugins, and app embedding on macOS and iOS.

The iOS app should not connect directly to every RTSP camera. The Mac app/server should own camera credentials, ingest, recording, health, indexing, and export. The iOS app should talk to Sentinel and play Apple-friendly streams.

## ONVIF And RTSP

ONVIF and RTSP are both needed, but they solve different jobs:

```text
ONVIF
  -> discover cameras
  -> authenticate to camera services
  -> read device information
  -> list stream profiles
  -> get RTSP stream URIs
  -> PTZ controls
  -> camera events and health

RTSP
  -> carry live video/audio from the selected stream profile
  -> feed preview, recording, timeline, search, and export
```

Sentinel should prefer this setup flow:

```text
Discover ONVIF camera
    -> choose camera
    -> choose profile
    -> Sentinel stores camera metadata and secure credentials
    -> Sentinel uses the profile RTSP URL for GStreamer ingest
```

Manual RTSP entry remains important for quick setup, lab testing, non-ONVIF cameras, and edge cases.

## Current ONVIF Step

Sentinel now includes a real WS-Discovery multicast probe to:

```text
239.255.255.250:3702
```

This discovers ONVIF device service URLs on the local network. Sentinel now also includes a SOAP profile lookup path with optional WS-Security UsernameToken digest credentials:

```text
GetDeviceInformation
GetProfiles
GetStreamUri
```

The app also calls `GetCapabilities` internally to locate the media service URL before calling media profile APIs. Real discoveries still begin with a pending profile placeholder, then the operator can fetch actual profiles and add the selected RTSP URI. Demo results remain available in the UI for workflow testing.

The remaining ONVIF layers are:

```text
PTZ capability discovery
Event capability discovery
Camera time synchronization handling
Digest retry/fallback behavior for cameras with nonstandard auth
```

## macOS Engine Layers

1. Camera Registry
   - ONVIF device service URL
   - RTSP URL
   - unattended RTSP credentials in the private camera config when auto-recording is required
   - stream profile
   - recording policy

2. Ingest Supervisor
   - owns one pipeline per active camera
   - reconnects failed cameras
   - reports health and bitrate
   - exposes preview frames to the UI

3. Recording Service
   - writes timestamped segments
   - tracks gaps and retention
   - protects evidence-locked clips

4. Playback Index
   - maps cameras and time ranges to segments
   - stores motion/events/bookmarks
   - feeds timeline/search/evidence views

5. Client Stream Service
   - HLS first for Apple compatibility
   - WebRTC later for low-latency live viewing

## GStreamer Install Target

For the cleanest Xcode integration, prefer the official GStreamer macOS runtime and development packages. Sentinel currently checks these locations:

```text
/Library/Frameworks/GStreamer.framework
/Library/Frameworks/GStreamer.framework/Versions/1.0/bin/gst-launch-1.0
/Library/Frameworks/GStreamer.framework/Versions/1.0/bin/gst-inspect-1.0
```

Homebrew paths are also detected for development experiments:

```text
/opt/homebrew/bin/gst-launch-1.0
/opt/homebrew/bin/gst-inspect-1.0
/opt/homebrew/lib/libgstreamer-1.0.dylib
```

## Implemented Prototype Path

Sentinel now has the first end-to-end RTSP milestone wired through the app:

```text
Manual RTSP camera
    -> Inspector connection test
    -> ONVIF profile fetch from discovered cameras
    -> Start In-App Live
       -> GStreamer RTSP ingest
       -> rolling local HLS playlist
       -> SwiftUI tile playback with AVKit
    -> Start Local Recording
       -> GStreamer RTSP ingest
       -> timestamped MP4 splitmux segments
       -> Storage index
       -> Playback timeline
```

The process-based GStreamer prototype now uses decode/re-encode pipelines for broader video compatibility:

```text
uridecodebin uri=<rtsp-url> caps=video/x-raw
    ! videoconvert
    ! x264enc
    ! h264parse
    ! mpegtsmux
    ! hlssink playlist-location=<live.m3u8> location=<segment-%05d.ts>

uridecodebin uri=<rtsp-url> caps=video/x-raw
    ! videoconvert
    ! x264enc
    ! h264parse
    ! splitmuxsink muxer=mp4mux max-size-time=60000000000 location=<timestamp-%05d.mp4>
```

This makes the prototype more tolerant of H.264, H.265, MJPEG, and mixed audio/video RTSP streams, at the cost of CPU from re-encoding. Unattended recording uses RTSP credentials that the local GStreamer process can read, so the app redacts them in UI text and keeps the camera config private to the current macOS user.

Recordings are indexed from:

```text
~/Library/Application Support/HandoffGridSentinel/Recordings
```

## Next Implementation Path

1. Install GStreamer runtime and development packages on macOS.
2. Expand pipelines beyond H.264 to H.265 and audio-aware stream selection.
3. Add SOAP support for ONVIF `GetProfiles` and `GetStreamUri`.
4. Add retention pruning and evidence-lock protection.
5. Replace process-based ingest with a native Swift/Objective-C GStreamer bridge when frame-level control is needed.
6. Build the iOS app against Sentinel HTTP APIs and AVPlayer.

## Current Preview Step

Sentinel supports both an in-app HLS preview bridge and an external GStreamer preview launch. The external validation command still uses:

```text
gst-launch-1.0 -v uridecodebin uri=<rtsp-url> ! videoconvert ! autovideosink
```

The external window is a validation step. The in-app bridge proves the same RTSP path can feed SwiftUI through an Apple-native playback surface. A future native bridge should replace the process pipeline when Sentinel needs direct frame access, overlays, motion analysis, and tighter error recovery.
