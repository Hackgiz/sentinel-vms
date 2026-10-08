import Accelerate
import Foundation
import Darwin
import CoreGraphics
import ImageIO
import SentinelCore

@MainActor
public final class MediaIngestStore: ObservableObject, MediaIngestProviding {
    @Published public private(set) var liveStreams: [UUID: LocalLiveStream] = [:]
    @Published public private(set) var activeRecordings: [UUID: RecordingSessionStatus] = [:]
    @Published public private(set) var recordingSegments: [RecordingSegment] = []
    @Published public private(set) var motionEvents: [UUID: [MotionEvent]] = [:]
    @Published public private(set) var lastLiveFrameAt: [UUID: Date] = [:]
    @Published public private(set) var liveFrameRates: [UUID: Double] = [:]
    @Published public private(set) var streamErrors: [UUID: String] = [:]
    @Published public private(set) var lastMessage: String?

    public private(set) var dualStreamLastMotionAt: [UUID: Date] = [:]

    private var liveProcesses: [UUID: Process] = [:]
    private var recordingProcesses: [UUID: Process] = [:]
    private var motionMonitorTasks: [UUID: Task<Void, Never>] = [:]
    private var startingRecordingIDs: Set<UUID> = []
    private var lastDetectionEventAt: [String: Date] = [:]
    private var liveFrameHistory: [UUID: [Date]] = [:]
    private var userStoppedCameraIDs: Set<UUID> = []
    private var reconnectTasks: [UUID: Task<Void, Never>] = [:]
    private var reconnectAttempts: [UUID: Int] = [:]
    private let reconnectDelays: [UInt64] = [8_000_000_000, 30_000_000_000, 120_000_000_000]

    private let motionPreBufferSeconds: TimeInterval = 8
    private let motionPostBufferSeconds: TimeInterval = 20
    private let motionSegmentSettleSeconds: TimeInterval = 12

    public let recordingRootURL: URL
    private let liveRootURL: URL
    private let motionEventsURL: URL

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        let temporaryDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)

        self.recordingRootURL = supportDirectory.appendingPathComponent("Recordings", isDirectory: true)
        self.liveRootURL = temporaryDirectory.appendingPathComponent("LivePreviews", isDirectory: true)
        self.motionEventsURL = supportDirectory.appendingPathComponent("motion-events.json")

        try? createDirectoryIfNeeded(recordingRootURL)
        try? createDirectoryIfNeeded(liveRootURL)
        loadMotionEvents()
        Task {
            let rootURL = self.recordingRootURL
            let archiveURL = RecordingArchiveSettings.current.activeArchiveRoot
            let segments = await Task.detached(priority: .userInitiated) {
                MediaIngestStore.collectAllSegments(recordingRoot: rootURL, archiveRoot: archiveURL)
            }.value
            self.recordingSegments = segments
        }
    }

    public var storageSummary: RecordingStorageSummary {
        RecordingStorageSummary(
            segmentCount: recordingSegments.filter { $0.isArchived == false }.count,
            activeRecordingCount: activeRecordings.count,
            totalBytes: recordingSegments.filter { $0.isArchived == false }.reduce(0) { $0 + $1.byteCount },
            recordingRootURL: recordingRootURL
        )
    }

    public func liveStream(for cameraID: UUID) -> LocalLiveStream? {
        liveStreams[cameraID]
    }

    public func effectiveStatus(for camera: CameraFeed) -> CameraStatus {
        // Local AVFoundation cameras are always available when the device is.
        if camera.isLocalCamera { return .online }

        // Explicit reconnect/no-frames errors are the strongest negative
        // signal — show the operator that the tile won't have video.
        if streamErrors[camera.id] != nil { return .offline }

        // Frame arrived within the freshness window — actual proof of life.
        if hasFreshLiveFrame(for: camera.id) { return .online }

        // GStreamer live-preview pipeline registered without error: frames
        // are imminent, treat as online so the badge doesn't flicker during
        // pipeline startup.
        if liveStreams[camera.id] != nil { return .online }

        // No live evidence either way. `camera.status` is captured at
        // discovery time and never updated, so trusting it would leave a
        // stale "Online" badge on a camera that hasn't streamed yet.
        return .offline
    }

    /// Push live-source state in from a proxy/recorder that doesn't itself
    /// emit tile-preview frames into this store (currently: MediaMTX). This
    /// lets `effectiveStatus` and `hasFreshLiveFrame` reflect what the proxy
    /// can actually see from the camera.
    public func recordRemoteSourceState(cameraID: UUID, isLive: Bool, detail: String? = nil) {
        if isLive {
            markLiveFrame(cameraID: cameraID)
        } else {
            if streamErrors[cameraID] == nil {
                streamErrors[cameraID] = detail ?? "No video from camera."
            }
            lastLiveFrameAt[cameraID] = nil
        }
    }

    public func recordingSession(for cameraID: UUID) -> RecordingSessionStatus? {
        activeRecordings[cameraID]
    }

    public func isRecording(cameraID: UUID) -> Bool {
        activeRecordings[cameraID] != nil
    }

    public func segments(for cameraID: UUID) -> [RecordingSegment] {
        recordingSegments
            .filter { $0.cameraID == cameraID }
            .sorted { $0.createdAt < $1.createdAt }
    }

    public func motionEvents(for cameraID: UUID) -> [MotionEvent] {
        (motionEvents[cameraID] ?? [])
            .sorted { $0.timestamp < $1.timestamp }
    }

    public func motionEvents(for segment: RecordingSegment) -> [MotionEvent] {
        let start = min(segment.createdAt, segment.modifiedAt)
        let end = max(segment.createdAt, segment.modifiedAt)

        return motionEvents(for: segment.cameraID).filter { event in
            event.timestamp >= start && event.timestamp <= end
        }
    }

    public func markLiveFrame(cameraID: UUID, timestamp: Date = Date()) {
        lastLiveFrameAt[cameraID] = timestamp
        streamErrors[cameraID] = nil
        // A real frame means the bridge is genuinely healthy → reset the reconnect
        // backoff so the next blip retries fast. A persistently-down camera never
        // reaches here, so its backoff correctly ramps to the 2m cap.
        if reconnectAttempts[cameraID, default: 0] != 0 { reconnectAttempts[cameraID] = 0 }

        let cutoff = timestamp.addingTimeInterval(-10)
        var history = liveFrameHistory[cameraID] ?? []
        history.append(timestamp)
        history.removeAll { $0 < cutoff }
        if history.count > 120 { history.removeFirst(history.count - 120) }
        liveFrameHistory[cameraID] = history

        if let first = history.first, let last = history.last {
            let duration = max(last.timeIntervalSince(first), 0.1)
            liveFrameRates[cameraID] = Double(max(history.count - 1, 0)) / duration
        }
    }

    public func healthSnapshot(for camera: CameraFeed) -> CameraHealthSnapshot {
        CameraHealthSnapshot(
            status: effectiveStatus(for: camera),
            isLive: liveStreams[camera.id] != nil,
            isRecording: activeRecordings[camera.id] != nil,
            lastFrameAt: lastLiveFrameAt[camera.id],
            estimatedFPS: liveFrameRates[camera.id] ?? 0,
            segmentCount: segments(for: camera.id).count,
            eventCount: motionEvents(for: camera.id).count,
            lastError: streamErrors[camera.id]
        )
    }

    public func registerMotion(cameraID: UUID, intensity: Double, threshold: Double = 0.09, timestamp: Date = Date()) {
        registerDetection(cameraID: cameraID, intensity: intensity, kind: .motion, threshold: threshold, timestamp: timestamp)
    }

    public func registerPersonDetection(cameraID: UUID, confidence: Double = 1, timestamp: Date = Date()) {
        registerDetection(cameraID: cameraID, intensity: confidence, kind: .person, threshold: 0.01, timestamp: timestamp)
    }

    @discardableResult
    public func pruneRecordings(olderThanDays days: Int) -> Int {
        guard days > 0 else {
            return 0
        }

        refreshRecordingSegments()
        let cutoff = Date().addingTimeInterval(-Double(days) * 86_400)
        let expiredSegments = recordingSegments.filter { segment in
            segment.modifiedAt < cutoff && activeRecordings[segment.cameraID] == nil
        }

        var removed = 0
        for segment in expiredSegments {
            guard isSafeRecordingFile(segment.fileURL) else { continue }
            do {
                try FileManager.default.removeItem(at: segment.fileURL)
                removed += 1
            } catch {
                lastMessage = "Could not delete \(segment.fileName): \(error.localizedDescription)"
            }
        }

        refreshRecordingSegments()
        if removed > 0 {
            lastMessage = "Retention removed \(removed) old recording segment\(removed == 1 ? "" : "s")."
        }
        return removed
    }

    @discardableResult
    public func applyMotionRecordingPolicy(for cameras: [CameraFeed]) -> Int {
        let motionCameraIDs = Set(cameras
            .filter { $0.isLocalCamera == false && $0.recordingMode == .motion }
            .map(\.id))

        guard motionCameraIDs.isEmpty == false else {
            return 0
        }

        refreshRecordingSegments()

        let now = Date()
        let expiredSegments = recordingSegments.filter { segment in
            guard motionCameraIDs.contains(segment.cameraID) else {
                return false
            }

            let age = now.timeIntervalSince(segment.modifiedAt)
            guard age >= motionPostBufferSeconds + motionSegmentSettleSeconds else {
                return false
            }

            return hasDetectionNear(segment) == false
        }

        var removed = 0
        for segment in expiredSegments {
            guard isSafeRecordingFile(segment.fileURL) else { continue }
            do {
                try FileManager.default.removeItem(at: segment.fileURL)
                removed += 1
            } catch {
                lastMessage = "Could not remove idle motion clip \(segment.fileName): \(error.localizedDescription)"
            }
        }

        if removed > 0 {
            refreshRecordingSegments()
            lastMessage = "Motion recording removed \(removed) idle clip\(removed == 1 ? "" : "s")."
        }

        return removed
    }

    public func deleteSegment(_ segment: RecordingSegment) {
        guard isSafeRecordingFile(segment.fileURL) else {
            lastMessage = "Refused to delete \(segment.fileName): path is outside the recordings directory."
            return
        }
        do {
            try FileManager.default.removeItem(at: segment.fileURL)
            refreshRecordingSegments()
            lastMessage = "Deleted \(segment.fileName)."
        } catch {
            lastMessage = "Could not delete \(segment.fileName): \(error.localizedDescription)"
        }
    }

    // Guard against TOCTOU symlink swaps and path traversal: only delete files
    // that resolve to a regular file inside `recordingRootURL`.
    private func isSafeRecordingFile(_ url: URL) -> Bool {
        let root = recordingRootURL.standardizedFileURL.resolvingSymlinksInPath().path
        let target = url.standardizedFileURL.resolvingSymlinksInPath().path
        guard target.hasPrefix(root + "/") else { return false }
        let values = try? url.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey])
        if values?.isSymbolicLink == true { return false }
        return values?.isRegularFile == true
    }

    private func hasFreshLiveFrame(for cameraID: UUID) -> Bool {
        guard let timestamp = lastLiveFrameAt[cameraID] else {
            return false
        }

        // 30s window: MediaMTX polls source state every 10s and we want one
        // failed poll cycle of margin before flipping the tile to offline.
        return Date().timeIntervalSince(timestamp) < 30
    }

    private func registerDetection(
        cameraID: UUID,
        intensity: Double,
        kind: VideoDetectionKind,
        threshold: Double,
        timestamp: Date
    ) {
        guard intensity >= threshold else {
            return
        }

        let eventKey = "\(cameraID.uuidString)-\(kind.rawValue)"
        if let lastTimestamp = lastDetectionEventAt[eventKey],
           timestamp.timeIntervalSince(lastTimestamp) < 2.5 {
            return
        }

        lastDetectionEventAt[eventKey] = timestamp
        dualStreamLastMotionAt[cameraID] = timestamp
        var cameraEvents = motionEvents[cameraID] ?? []
        cameraEvents.append(MotionEvent(cameraID: cameraID, timestamp: timestamp, intensity: intensity, kind: kind))

        if cameraEvents.count > 500 {
            cameraEvents.removeFirst(cameraEvents.count - 500)
        }

        motionEvents[cameraID] = cameraEvents
        lastMessage = kind == .person ? "Person detected." : "Motion detected."
        saveMotionEvents()
    }

    public func syncMediaMTXRecordingSessions(cameras: [CameraFeed], mediaMTXStore: MediaMTXStore) {
        for camera in cameras where camera.isLocalCamera == false && camera.isRecording {
            guard activeRecordings[camera.id] == nil else { continue }
            let cameraDir = recordingRootURL.appendingPathComponent(camera.id.uuidString, isDirectory: true)
            activeRecordings[camera.id] = RecordingSessionStatus(
                cameraID: camera.id,
                cameraName: camera.name,
                startedAt: Date(),
                directoryURL: cameraDir,
                logURL: cameraDir.appendingPathComponent("mediamtx.log"),
                processID: -1  // sentinel: managed by MediaMTX, not a local Process
            )
        }
    }

    public func startLiveBridge(
        for camera: CameraFeed,
        mediaEngine: MediaEngineStore,
        credentials: CameraCredentialStore,
        mediaMTXStore: MediaMTXStore? = nil
    ) async -> MediaPipelineLaunchResult {
        mediaEngine.refresh()

        guard let launchPath = mediaEngine.snapshot.gstreamerLaunchPath else {
            return .failure(
                "GStreamer Missing",
                detail: "gst-launch-1.0 was not found. Install GStreamer before starting in-app live preview."
            )
        }

        guard camera.rtspURL.isEmpty == false else {
            return .failure("No RTSP URL", detail: "This camera does not have an RTSP URL configured.")
        }

        if let stream = liveStreams[camera.id] {
            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Live Already Running",
                detail: "\(camera.name) is already publishing a local tile preview.",
                commandPreview: stream.frameDirectoryURL.path
            )
        }

        let streamDirectory = liveRootURL.appendingPathComponent(camera.id.uuidString, isDirectory: true)
        do {
            try resetDirectory(streamDirectory)
        } catch {
            return .failure("Live Directory Failed", detail: error.localizedDescription)
        }

        let playlistURL = streamDirectory.appendingPathComponent("live.m3u8")
        let framePattern = streamDirectory.appendingPathComponent("frame-%06d.jpg").path
        // When MediaMTX is running, GStreamer connects to its re-streamed RTSP (not the camera).
        // This means the camera's single RTSP slot stays with MediaMTX.
        let rtspURL: String
        if let mtx = mediaMTXStore, mtx.isRunning {
            rtspURL = mtx.rtspURL(for: camera.id)
        } else {
            rtspURL = credentials.rtspURL(for: camera)
        }
        guard RTSPCredentialFormatter.isSafeForPipeline(rtspURL) else {
            return .failure(
                "Invalid RTSP URL",
                detail: "The RTSP URL for \(camera.name) contains disallowed characters and was rejected before launching GStreamer."
            )
        }
        let logURL = streamDirectory.appendingPathComponent("gstreamer-live.log")
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        let logFile = try? FileHandle(forWritingTo: logURL)

        let process = Process()
        process.executableURL = URL(fileURLWithPath: launchPath)
        process.arguments = [
            "-e",
            "rtspsrc",
            "location=\(rtspURL)",
            "protocols=tcp",
            "latency=200",
            "retry=3",
            "!",
            "decodebin",
            "!",
            "videoconvert",
            "!",
            "videorate",
            "!",
            "video/x-raw,framerate=3/1",
            "!",
            "jpegenc",
            "quality=80",
            "!",
            "multifilesink",
            "location=\(framePattern)",
            "max-files=8",
            "sync=false"
        ]
        process.environment = MediaEngineProcessEnvironment.gstreamerEnvironment(launchPath: launchPath)
        process.standardOutput = logFile
        process.standardError = logFile

        process.terminationHandler = { [weak self, weak mediaEngine, weak credentials] _ in
            try? logFile?.close()
            Task { @MainActor in
                guard let self else { return }
                guard self.liveStreams[camera.id]?.processID == process.processIdentifier else { return }
                self.stopBackgroundMotionMonitor(for: camera.id)
                self.liveProcesses[camera.id] = nil
                self.liveStreams[camera.id] = nil
                self.lastLiveFrameAt[camera.id] = nil
                self.liveFrameRates[camera.id] = nil
                self.liveFrameHistory[camera.id] = nil
                self.streamErrors[camera.id] = "Tile preview stopped. Check the stream log if this was unexpected."
                self.lastMessage = "Tile preview stopped for \(camera.name). Log: \(logURL.path)"
                if self.userStoppedCameraIDs.contains(camera.id) == false,
                   let mediaEngine, let credentials {
                    self.scheduleReconnect(camera: camera, mediaEngine: mediaEngine, credentials: credentials, isRecording: false)
                }
            }
        }

        do {
            try process.run()
            // NOTE: do NOT reset reconnectAttempts here. Resetting on every launch
            // meant a camera that is DOWN (process starts but never produces a
            // frame, so the watchdog kills it ~20s later) relaunched every ~28s
            // forever instead of backing off. The backoff is reset only when a
            // real frame arrives (see markLiveFrame).
            userStoppedCameraIDs.remove(camera.id)
            liveProcesses[camera.id] = process
            // If MediaMTX is running, hand its HLS URL to the AI detector so
            // it can decode frames via VideoToolbox/AVPlayer instead of
            // polling the JPEG snapshot dump.
            let detectorHLSURL: URL? = {
                guard let mtx = mediaMTXStore, mtx.isRunning else { return nil }
                return URL(string: mtx.hlsURL(for: camera.id))
            }()

            let stream = LocalLiveStream(
                cameraID: camera.id,
                cameraName: camera.name,
                playlistURL: playlistURL,
                frameDirectoryURL: streamDirectory,
                logURL: logURL,
                startedAt: Date(),
                processID: process.processIdentifier,
                hlsURL: detectorHLSURL
            )
            liveStreams[camera.id] = stream
            startBackgroundMotionMonitor(for: stream)
            lastMessage = "Started tile preview for \(camera.name)."
            streamErrors[camera.id] = nil

            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Tile Preview Started",
                detail: "Sentinel is writing live preview frames for \(camera.name).",
                commandPreview: "\(commandPreview(launchPath: launchPath, arguments: process.arguments))\nLog: \(logURL.path)"
            )
        } catch {
            streamErrors[camera.id] = error.localizedDescription
            return .failure("Live Start Failed", detail: error.localizedDescription)
        }
    }

    public func stopLiveBridge(for cameraID: UUID) {
        userStoppedCameraIDs.insert(cameraID)
        reconnectTasks[cameraID]?.cancel()
        reconnectTasks[cameraID] = nil
        reconnectAttempts[cameraID] = 0

        guard let process = liveProcesses[cameraID] else {
            if activeRecordings[cameraID] == nil {
                stopBackgroundMotionMonitor(for: cameraID)
                liveStreams[cameraID] = nil
                lastLiveFrameAt[cameraID] = nil
                liveFrameRates[cameraID] = nil
                liveFrameHistory[cameraID] = nil
            }
            return
        }

        let pid = process.processIdentifier
        kill(pid, SIGINT)
        // GStreamer occasionally ignores SIGINT during plugin teardown.
        // Schedule a SIGKILL fallback so we never leak a zombie.
        Task.detached { [weak process] in
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            if process?.isRunning == true {
                kill(pid, SIGKILL)
            }
        }
        stopBackgroundMotionMonitor(for: cameraID)
        liveProcesses[cameraID] = nil
        liveStreams[cameraID] = nil
        lastLiveFrameAt[cameraID] = nil
        liveFrameRates[cameraID] = nil
        liveFrameHistory[cameraID] = nil
        lastMessage = "Stopped in-app live preview."
    }

    public func stopAllLiveBridges() {
        for cameraID in Array(liveProcesses.keys) {
            stopLiveBridge(for: cameraID)
        }
    }

    @discardableResult
    public func startRecording(
        for camera: CameraFeed,
        mediaEngine: MediaEngineStore,
        credentials: CameraCredentialStore
    ) async -> MediaPipelineLaunchResult {
        mediaEngine.refresh()

        guard let launchPath = mediaEngine.snapshot.gstreamerLaunchPath else {
            return .failure(
                "GStreamer Missing",
                detail: "gst-launch-1.0 was not found. Install GStreamer before recording RTSP streams."
            )
        }

        guard camera.rtspURL.isEmpty == false else {
            return .failure("No RTSP URL", detail: "This camera does not have an RTSP URL configured.")
        }

        if let status = activeRecordings[camera.id] {
            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Recording Already Running",
                detail: "\(camera.name) has been recording since \(RecordingFormatters.timeFormatter.string(from: status.startedAt)).",
                commandPreview: status.directoryURL.path
            )
        }

        if startingRecordingIDs.contains(camera.id) {
            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Recording Starting",
                detail: "\(camera.name) is already starting a recording pipeline.",
                commandPreview: ""
            )
        }

        startingRecordingIDs.insert(camera.id)
        defer {
            startingRecordingIDs.remove(camera.id)
        }

        // Most IP cameras only allow one concurrent RTSP session. Stop the live bridge
        // first so the camera's stream slot is free before recording connects.
        if liveStreams[camera.id] != nil {
            stopLiveBridge(for: camera.id)
            try? await Task.sleep(nanoseconds: 800_000_000)
        }

        let cameraDirectory = recordingRootURL.appendingPathComponent(camera.id.uuidString, isDirectory: true)
        do {
            try createDirectoryIfNeeded(cameraDirectory)
        } catch {
            return .failure("Recording Directory Failed", detail: error.localizedDescription)
        }

        let timestamp = RecordingFormatters.fileFormatter.string(from: Date())
        let outputPattern = cameraDirectory.appendingPathComponent("\(timestamp)-%05d.mp4").path
        let rtspURL = credentials.rtspURL(for: camera)
        guard RTSPCredentialFormatter.isSafeForPipeline(rtspURL) else {
            return .failure(
                "Invalid RTSP URL",
                detail: "The RTSP URL for \(camera.name) contains disallowed characters and was rejected before launching GStreamer."
            )
        }
        let logURL = cameraDirectory.appendingPathComponent("\(timestamp)-recording.log")
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        let logFile = try? FileHandle(forWritingTo: logURL)


        // PASSTHROUGH recording: parse (do NOT decode/re-encode) the camera's
        // native H.264/H.265 elementary stream and mux it straight to MP4.
        //
        // The old pipeline decoded then re-encoded every frame in realtime
        // (decodebin ! videoconvert ! vtenc_h264). On a multi-camera wall that
        // pegged the CPU, which starved the motion monitor and the main actor —
        // tripping the 12s frame-stall watchdog into a SIGKILL → reconnect loop
        // that read as "recording isn't stable." Re-encoding also threw away the
        // camera's chosen quality and ignored recordingCodec entirely.
        //
        // parsebin auto-detects the codec, so one pipeline records both H.264 and
        // H.265 cameras at full source quality with near-zero CPU — the same
        // cheap remux MediaMTX already does for the main path. (Pure-MJPEG RTSP
        // cameras, which are essentially nonexistent among ONVIF/RTSP IP cams,
        // are the only sources this doesn't cover.)
        let process = Process()
        process.executableURL = URL(fileURLWithPath: launchPath)
        process.arguments = [
            "-e",
            "rtspsrc",
            "location=\(rtspURL)",
            "protocols=tcp",
            "latency=400",
            "retry=5",
            "!",
            "parsebin",
            "!",
            "splitmuxsink",
            "muxer-factory=mp4mux",
            "max-size-time=300000000000",
            "location=\(outputPattern)"
        ]
        process.environment = MediaEngineProcessEnvironment.gstreamerEnvironment(launchPath: launchPath)
        process.standardOutput = logFile
        process.standardError = logFile

        process.terminationHandler = { [weak self, weak mediaEngine, weak credentials] _ in
            try? logFile?.close()
            Task { @MainActor in
                guard let self else { return }
                guard self.activeRecordings[camera.id]?.processID == process.processIdentifier else { return }
                self.recordingProcesses[camera.id] = nil
                self.activeRecordings[camera.id] = nil
                self.streamErrors[camera.id] = "Recording process ended."
                self.refreshRecordingSegments()
                self.lastMessage = "Recording stopped for \(camera.name). Log: \(logURL.path)"
                if self.userStoppedCameraIDs.contains(camera.id) == false,
                   let mediaEngine, let credentials {
                    self.scheduleReconnect(camera: camera, mediaEngine: mediaEngine, credentials: credentials, isRecording: true)
                } else if self.userStoppedCameraIDs.contains(camera.id),
                          let mediaEngine, let credentials,
                          self.liveStreams[camera.id] == nil {
                    // User stopped recording — restore live bridge after a short delay
                    try? await Task.sleep(nanoseconds: 600_000_000)
                    _ = await self.startLiveBridge(for: camera, mediaEngine: mediaEngine, credentials: credentials)
                }
            }
        }

        do {
            try process.run()
            reconnectAttempts[camera.id] = 0
            userStoppedCameraIDs.remove(camera.id)
            recordingProcesses[camera.id] = process
            activeRecordings[camera.id] = RecordingSessionStatus(
                cameraID: camera.id,
                cameraName: camera.name,
                startedAt: Date(),
                directoryURL: cameraDirectory,
                logURL: logURL,
                processID: process.processIdentifier
            )
            lastMessage = "Started local recording for \(camera.name)."
            streamErrors[camera.id] = nil

            try? await Task.sleep(nanoseconds: 1_000_000_000)

            if process.isRunning == false {
                recordingProcesses[camera.id] = nil
                activeRecordings[camera.id] = nil
                refreshRecordingSegments()
                try? logFile?.close()
                let detail = "GStreamer exited before recording could begin. \(recordingLogExcerpt(from: logURL))"
                streamErrors[camera.id] = detail
                return .failure("Recording Stopped", detail: detail)
            }

            // Live bridge was stopped before recording to free the camera's RTSP slot.
            // Recording tiles use CameraSignalSurface or AVPlayer for display.

            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Recording Started",
                detail: recordingLaunchDetail(for: camera),
                commandPreview: "\(commandPreview(launchPath: launchPath, arguments: process.arguments))\nLog: \(logURL.path)"
            )
        } catch {
            try? logFile?.close()
            streamErrors[camera.id] = error.localizedDescription
            return .failure("Recording Failed", detail: error.localizedDescription)
        }
    }


    public func stopRecording(for cameraID: UUID) {
        userStoppedCameraIDs.insert(cameraID)
        reconnectTasks[cameraID]?.cancel()
        reconnectTasks[cameraID] = nil
        reconnectAttempts[cameraID] = 0

        // MediaMTX-managed session (processID == -1): just clear the UI status.
        // MediaMTX continues recording; user must toggle the camera's isRecording flag
        // and call mediaMTXStore.reload() to actually stop MediaMTX recording.
        if activeRecordings[cameraID]?.processID == -1 {
            activeRecordings[cameraID] = nil
            refreshRecordingSegments()
            lastMessage = "MediaMTX recording stopped for this session."
            return
        }

        guard let process = recordingProcesses[cameraID] else {
            activeRecordings[cameraID] = nil
            refreshRecordingSegments()
            return
        }

        let pid = process.processIdentifier
        kill(pid, SIGINT)
        Task.detached { [weak process] in
            try? await Task.sleep(nanoseconds: 3_000_000_000)
            if process?.isRunning == true {
                kill(pid, SIGKILL)
            }
        }
        recordingProcesses[cameraID] = nil
        activeRecordings[cameraID] = nil
        refreshRecordingSegments()
        lastMessage = "Stopped local recording."
    }

    public func startConnectivityLoop(cameraProvider: @escaping @Sendable () -> [CameraFeed]) {
        Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 60_000_000_000)
                let cameras = cameraProvider()
                for camera in cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
                    guard let self else { return }
                    if self.liveStream(for: camera.id) != nil { continue }
                    if self.recordingSession(for: camera.id) != nil { continue }
                    let result = await RTSPStreamProbe.check(camera: camera)
                    if result.state == .failed {
                        await MainActor.run { [weak self] in
                            if self?.streamErrors[camera.id] == nil {
                                self?.streamErrors[camera.id] = "Periodic check: \(result.title) — \(result.detail)"
                            }
                        }
                    }
                }
            }
        }
    }

    // Frame-arrival watchdog: an RTSP pipeline can stay technically "alive"
    // (the gst-launch process is still running) while the camera has silently
    // stopped delivering frames — a power blip, a NAT mapping expiry, a router
    // reboot. The existing reconnect logic only fires when `terminationHandler`
    // runs. This loop kills any live pipeline that hasn't produced a frame in
    // `stallTimeoutSeconds`, which triggers the reconnect cascade.
    public func startFrameWatchdog() {
        Task { [weak self] in
            let stallTimeoutSeconds: TimeInterval = 12
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self else { return }
                await MainActor.run {
                    let now = Date()
                    // Grace period after a pipeline starts: a freshly-launched
                    // GStreamer process needs a few seconds to negotiate RTSP
                    // and emit the first JPEG. Don't kill it during that window.
                    let startupGraceSeconds: TimeInterval = 20
                    for (cameraID, stream) in self.liveStreams {
                        guard let process = self.liveProcesses[cameraID], process.isRunning else { continue }
                        guard self.userStoppedCameraIDs.contains(cameraID) == false else { continue }
                        let runningFor = now.timeIntervalSince(stream.startedAt)
                        guard runningFor >= startupGraceSeconds else { continue }
                        guard let lastFrame = self.lastLiveFrameAt[cameraID] else {
                            // No frames ever received past the grace window — kill it.
                            self.streamErrors[cameraID] = "No frames received. Reconnecting…"
                            kill(process.processIdentifier, SIGKILL)
                            continue
                        }
                        let age = now.timeIntervalSince(lastFrame)
                        if age > stallTimeoutSeconds {
                            // Kill the stalled process; terminationHandler will
                            // schedule a reconnect via existing backoff logic.
                            let ageSeconds = max(0, min(age, 86_400))
                            self.streamErrors[cameraID] = "Stream stalled (\(Int(ageSeconds))s without a frame). Reconnecting…"
                            kill(process.processIdentifier, SIGKILL)
                        }
                    }
                }
            }
        }
    }

    // Per-camera retention enforcement. Each camera carries its own
    // `retentionDays` (Optional<Int> in CameraFeed). This replaces the single
    // global UserDefaults retention with a per-camera rule. Cameras without a
    // retention preference fall back to the global default.
    @discardableResult
    public func pruneRecordingsPerCamera(cameras: [CameraFeed], globalFallbackDays: Int) -> Int {
        refreshRecordingSegments()
        let now = Date()
        var byCamera: [UUID: Int] = [:]
        for camera in cameras {
            let days = camera.retentionDays ?? globalFallbackDays
            if days > 0 { byCamera[camera.id] = days }
        }

        var removed = 0
        for segment in recordingSegments {
            guard let days = byCamera[segment.cameraID] else { continue }
            guard activeRecordings[segment.cameraID] == nil else { continue }
            let cutoff = now.addingTimeInterval(-Double(days) * 86_400)
            guard segment.modifiedAt < cutoff else { continue }
            guard isSafeRecordingFile(segment.fileURL) else { continue }
            do {
                try FileManager.default.removeItem(at: segment.fileURL)
                removed += 1
            } catch {
                lastMessage = "Retention: could not delete \(segment.fileName): \(error.localizedDescription)"
            }
        }

        if removed > 0 {
            refreshRecordingSegments()
            lastMessage = "Per-camera retention removed \(removed) segment\(removed == 1 ? "" : "s")."
        }
        return removed
    }

    // Disk pressure guardrail. A recording engine that fills the disk is worse
    // than one that stopped: it can corrupt the entire volume. This returns
    // available bytes on the recording volume, and `shouldHaltRecording` lets
    // callers decide when to suspend. Threshold defaults to 5 GB.
    public var availableRecordingBytes: Int64 {
        let values = try? recordingRootURL.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
        return values?.volumeAvailableCapacityForImportantUsage ?? 0
    }

    public func shouldHaltRecording(minFreeBytes: Int64 = 5_000_000_000) -> Bool {
        availableRecordingBytes > 0 && availableRecordingBytes < minFreeBytes
    }

    /// Disk-pressure state, so we halt once and resume once (with hysteresis)
    /// rather than re-halting every poll.
    private var diskHalted = false

    /// Invoked when disk pressure crosses the halt/resume threshold so the app
    /// can suspend/resume MediaMTX-managed recording (which has no Process here,
    /// so the guardian can't stop it directly). `true` = suspend, `false` = resume.
    public var onDiskPressureChange: (@MainActor (_ shouldHalt: Bool) -> Void)?

    public func startDiskGuardian(minFreeBytes: Int64 = 5_000_000_000) {
        Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 30_000_000_000)
                guard let self else { return }
                await MainActor.run {
                    let low = self.shouldHaltRecording(minFreeBytes: minFreeBytes)
                    // Resume only once we're comfortably back above the floor so a
                    // recording doesn't flap on/off right at the threshold.
                    let recovered = self.availableRecordingBytes > minFreeBytes + minFreeBytes / 2

                    if low, self.diskHalted == false {
                        self.diskHalted = true
                        // Count BOTH GStreamer recorders and MediaMTX-managed
                        // sessions (processID -1, present in activeRecordings).
                        let gstIDs = Array(self.recordingProcesses.keys)
                        let totalActive = self.activeRecordings.count
                        guard totalActive > 0 || gstIDs.isEmpty == false else { return }
                        let freeGB = Double(self.availableRecordingBytes) / 1_000_000_000
                        self.lastMessage = String(
                            format: "Disk low (%.1f GB free). Halting %d active recording(s) to protect the volume.",
                            freeGB, max(totalActive, gstIDs.count)
                        )
                        // Stop GStreamer recorders directly...
                        for cameraID in gstIDs {
                            self.stopRecording(for: cameraID)
                        }
                        // ...and ask the app to suspend MediaMTX recording too.
                        self.onDiskPressureChange?(true)
                    } else if self.diskHalted, recovered {
                        self.diskHalted = false
                        let freeGB = Double(self.availableRecordingBytes) / 1_000_000_000
                        self.lastMessage = String(
                            format: "Disk recovered (%.1f GB free). Resuming recording.",
                            freeGB
                        )
                        self.onDiskPressureChange?(false)
                    }
                }
            }
        }
    }

    public func stopAll() {
        for cameraID in Array(reconnectTasks.keys) {
            reconnectTasks[cameraID]?.cancel()
        }
        reconnectTasks.removeAll()

        for cameraID in Array(liveProcesses.keys) {
            stopLiveBridge(for: cameraID)
        }

        for cameraID in Array(recordingProcesses.keys) {
            stopRecording(for: cameraID)
        }

        for cameraID in Array(motionMonitorTasks.keys) {
            stopBackgroundMotionMonitor(for: cameraID)
        }
    }

    private func scheduleReconnect(camera: CameraFeed, mediaEngine: MediaEngineStore, credentials: CameraCredentialStore, isRecording: Bool) {
        let attempt = reconnectAttempts[camera.id, default: 0]
        reconnectAttempts[camera.id] = attempt + 1
        // Use the backoff schedule, then keep retrying forever at the slowest
        // cadence. A surveillance bridge must NEVER permanently give up — a
        // transient port conflict, camera reboot, or network blip should always
        // self-heal once it clears, without the user relaunching the app.
        let delay = attempt < reconnectDelays.count
            ? reconnectDelays[attempt]
            : (reconnectDelays.last ?? 120_000_000_000)
        let delayLabel = delay < 15_000_000_000 ? "8s" : delay < 45_000_000_000 ? "30s" : "2m"

        reconnectTasks[camera.id]?.cancel()
        reconnectTasks[camera.id] = Task { [weak self, weak mediaEngine, weak credentials] in
            try? await Task.sleep(nanoseconds: delay)
            guard Task.isCancelled == false,
                  let self, let mediaEngine, let credentials else { return }
            guard self.userStoppedCameraIDs.contains(camera.id) == false else { return }

            if isRecording {
                _ = await self.startRecording(for: camera, mediaEngine: mediaEngine, credentials: credentials)
            } else {
                _ = await self.startLiveBridge(for: camera, mediaEngine: mediaEngine, credentials: credentials)
            }
        }

        let attemptLabel = attempt < reconnectDelays.count
            ? "attempt \(attempt + 1)/\(reconnectDelays.count)"
            : "retrying every \(delayLabel)"
        streamErrors[camera.id] = "Stream disconnected. Reconnecting in \(delayLabel)… (\(attemptLabel))"
        lastMessage = "Reconnecting \(camera.name) in \(delayLabel)…"
    }

    public func refreshRecordingSegments() {
        try? createDirectoryIfNeeded(recordingRootURL)
        let rootURL = recordingRootURL
        let archiveURL = RecordingArchiveSettings.current.activeArchiveRoot
        Task {
            let segments = await Task.detached(priority: .utility) {
                MediaIngestStore.collectAllSegments(recordingRoot: rootURL, archiveRoot: archiveURL)
            }.value
            self.recordingSegments = segments
        }
    }

    /// Local recordings plus, when an archive drive is mounted, archived ones —
    /// so footage moved off the Mac stays on the timeline and plays.
    nonisolated static func collectAllSegments(recordingRoot: URL, archiveRoot: URL?) -> [RecordingSegment] {
        var segments = collectRecordingSegments(from: recordingRoot, isArchived: false)
        if let archiveRoot {
            // A segment present in both (mid-move) shows once, from the local copy.
            let localNames = Set(segments.map { "\($0.cameraID)/\($0.fileName)" })
            segments += collectRecordingSegments(from: archiveRoot, isArchived: true)
                .filter { localNames.contains("\($0.cameraID)/\($0.fileName)") == false }
        }
        return segments.sorted { $0.createdAt > $1.createdAt }
    }

    nonisolated private static func collectRecordingSegments(from recordingRootURL: URL, isArchived: Bool) -> [RecordingSegment] {
        let fileManager = FileManager.default
        guard let enumerator = fileManager.enumerator(
            at: recordingRootURL,
            includingPropertiesForKeys: [.creationDateKey, .contentModificationDateKey, .fileSizeKey, .isRegularFileKey],
            options: [.skipsHiddenFiles]
        ) else {
            return []
        }

        var segments: [RecordingSegment] = []

        for case let fileURL as URL in enumerator {
            guard ["mp4", "mov"].contains(fileURL.pathExtension.lowercased()) else {
                continue
            }

            let cameraIDString = fileURL.deletingLastPathComponent().lastPathComponent
            guard let cameraID = UUID(uuidString: cameraIDString) else {
                continue
            }

            do {
                let values = try fileURL.resourceValues(forKeys: [.creationDateKey, .contentModificationDateKey, .fileSizeKey, .isRegularFileKey])
                guard values.isRegularFile == true else {
                    continue
                }

                let createdAt = values.creationDate ?? values.contentModificationDate ?? Date.distantPast
                let modifiedAt = values.contentModificationDate ?? createdAt
                let byteCount = Int64(values.fileSize ?? 0)
                guard byteCount > 0 else {
                    continue
                }

                segments.append(
                    RecordingSegment(
                        id: fileURL.path,
                        cameraID: cameraID,
                        fileURL: fileURL,
                        createdAt: createdAt,
                        modifiedAt: modifiedAt,
                        byteCount: byteCount,
                        isArchived: isArchived
                    )
                )
            } catch {
                continue
            }
        }

        return segments.sorted { $0.createdAt > $1.createdAt }
    }

    private func loadMotionEvents() {
        do {
            guard FileManager.default.fileExists(atPath: motionEventsURL.path) else {
                return
            }

            let data = try Data(contentsOf: motionEventsURL)
            motionEvents = try JSONDecoder().decode([UUID: [MotionEvent]].self, from: data)
        } catch {
            motionEvents = [:]
        }
    }

    private func saveMotionEvents() {
        do {
            try createDirectoryIfNeeded(motionEventsURL.deletingLastPathComponent())
            let data = try JSONEncoder.prettySentinel.encode(motionEvents)
            try data.write(to: motionEventsURL, options: .atomic)
        } catch {
            lastMessage = "Motion events could not be saved: \(error.localizedDescription)"
        }
    }

    private func commandPreview(launchPath: String, arguments: [String]?) -> String {
        ([launchPath] + (arguments ?? []).map(RTSPCredentialFormatter.redacted)).joined(separator: " ")
    }

    private func recordingLaunchDetail(for camera: CameraFeed) -> String {
        switch camera.recordingMode {
        case .continuous:
            return "Sentinel is recording \(camera.name) at original quality (MP4) and keeping the live tile active."
        case .motion:
            return "Sentinel is recording \(camera.name) at original quality (MP4) and retaining clips around motion."
        case .dualStream:
            return "Sentinel is recording low-res continuously and high-res on motion for \(camera.name)."
        }
    }

    private func hasDetectionNear(_ segment: RecordingSegment) -> Bool {
        let start = min(segment.createdAt, segment.modifiedAt).addingTimeInterval(-motionPreBufferSeconds)
        let end = max(segment.createdAt, segment.modifiedAt).addingTimeInterval(motionPostBufferSeconds)

        return motionEvents(for: segment.cameraID).contains { event in
            event.timestamp >= start && event.timestamp <= end
        }
    }

    private func startBackgroundMotionMonitor(for stream: LocalLiveStream) {
        stopBackgroundMotionMonitor(for: stream.cameraID)

        // Run the frame reading + signature math OFF the main actor. Previously
        // this was `Task(priority: .utility)` inside this @MainActor method,
        // which inherited main-actor isolation and got starved (never scheduled)
        // whenever the main actor was busy → motion silently stopped. Detached,
        // it runs on a background thread and only hops to main for the quick
        // state mutations (markLiveFrame / registerMotion).
        let cameraID = stream.cameraID
        let frameDir = stream.frameDirectoryURL
        sentinelDebugLog("MON-START \(cameraID.uuidString.prefix(8))")
        motionMonitorTasks[cameraID] = Task.detached(priority: .utility) { [weak self] in
            var lastProcessedFrameURL: URL?
            var previousSignature: BackgroundMotionFrameSignature?
            var iter = 0

            while Task.isCancelled == false {
                iter += 1
                if iter % 10 == 0 { sentinelDebugLog("MON-ALIVE \(cameraID.uuidString.prefix(8)) iter=\(iter)") }
                if let latestFrameURL = Self.latestFrameURL(in: frameDir),
                   latestFrameURL != lastProcessedFrameURL,
                   let cgImage = Self.loadCGImage(from: latestFrameURL) {
                    lastProcessedFrameURL = latestFrameURL

                    let timestamp = Date()
                    let signature = BackgroundMotionFrameSignature(cgImage: cgImage)
                    if iter % 10 == 0, let signature, let previousSignature {
                        sentinelDebugLog("MON-INT \(cameraID.uuidString.prefix(8)) intensity=\(String(format: "%.4f", signature.motionIntensity(comparedTo: previousSignature)))")
                    }

                    // Fire-and-forget the main-actor state updates. Do NOT await
                    // them: awaiting blocks this loop until the main actor is
                    // free, and under UI load the low-priority hop can stall for
                    // many seconds → motion silently stops. Enqueuing instead
                    // lets the loop keep reading frames at a steady cadence.
                    Task { @MainActor [weak self] in self?.markLiveFrame(cameraID: cameraID, timestamp: timestamp) }

                    if let signature, let previousSignature {
                        let sensitivity = UserDefaults.standard.double(forKey: "handoffgrid.motionSensitivity")
                        let intensity = signature.motionIntensity(comparedTo: previousSignature)
                        // Threshold is now "fraction of the frame that must change"
                        // (see motionIntensity). 0.04 ≈ a person walking through.
                        let threshold = sensitivity == 0 ? 0.04 : sensitivity
                        if intensity >= threshold {
                            Task { @MainActor [weak self] in
                                self?.registerMotion(cameraID: cameraID, intensity: intensity, threshold: threshold, timestamp: timestamp)
                            }
                        }
                    }

                    if let signature {
                        previousSignature = signature
                    }
                }

                try? await Task.sleep(nanoseconds: 500_000_000)
            }
        }
    }

    private func stopBackgroundMotionMonitor(for cameraID: UUID) {
        if motionMonitorTasks[cameraID] != nil { sentinelDebugLog("MON-STOP \(cameraID.uuidString.prefix(8))") }
        motionMonitorTasks[cameraID]?.cancel()
        motionMonitorTasks[cameraID] = nil
    }

    private nonisolated static func latestFrameURL(in directoryURL: URL) -> URL? {
        guard let files = try? FileManager.default.contentsOfDirectory(
            at: directoryURL,
            includingPropertiesForKeys: [.contentModificationDateKey, .fileSizeKey]
        ) else {
            return nil
        }

        return files
            .filter { $0.pathExtension.lowercased() == "jpg" }
            .compactMap { url -> (URL, Date)? in
                let values = try? url.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
                guard let modified = values?.contentModificationDate,
                      (values?.fileSize ?? 0) > 0 else {
                    return nil
                }

                return (url, modified)
            }
            .max { $0.1 < $1.1 }?
            .0
    }

    private nonisolated static func loadCGImage(from url: URL) -> CGImage? {
        guard let source = CGImageSourceCreateWithURL(url as CFURL, nil) else {
            return nil
        }

        return CGImageSourceCreateImageAtIndex(source, 0, nil)
    }

    private func recordingLogExcerpt(from logURL: URL) -> String {
        guard let data = try? Data(contentsOf: logURL), data.isEmpty == false else {
            return "No diagnostic output was written yet. Log: \(logURL.path)"
        }

        let output = String(decoding: data, as: UTF8.self)
        let lines = output
            .split(separator: "\n")
            .suffix(3)
            .joined(separator: " ")

        return "\(lines) Log: \(logURL.path)"
    }

    private func resetDirectory(_ url: URL) throws {
        let fileManager = FileManager.default
        try? fileManager.removeItem(at: url)
        try createDirectoryIfNeeded(url)
    }

}

/// Serializes all debug-log writes. The motion monitor runs one detached task
/// PER CAMERA, so multiple background threads call this concurrently. The old
/// implementation opened a shared FileHandle and used the deprecated
/// `write(_:)`, which raises an *Objective-C* exception on a bad/closed file
/// descriptor — uncatchable by `try?` — so a write race would SIGABRT the whole
/// app (orphaning the MediaMTX/GStreamer children). A serial queue + the
/// throwing `write(contentsOf:)`/`seekToEnd()` APIs make it crash-proof.
private let sentinelDebugLogQueue = DispatchQueue(label: "com.handoffgrid.sentinel.debuglog")

func sentinelDebugLog(_ msg: String) {
    let line = "\(Date()) \(msg)\n"
    sentinelDebugLogQueue.async {
        guard let data = line.data(using: .utf8) else { return }
        let url = URL(fileURLWithPath: "/tmp/sentinel-motion-debug.log")
        if let handle = try? FileHandle(forWritingTo: url) {
            defer { try? handle.close() }
            _ = try? handle.seekToEnd()
            try? handle.write(contentsOf: data)
        } else {
            try? data.write(to: url)
        }
    }
}

private struct BackgroundMotionFrameSignature {
    public let samples: [UInt8]

    init?(cgImage: CGImage) {
        // 32x18 (576 cells) instead of 16x9 (144). Higher resolution means a
        // moving subject occupies enough cells for the "fraction of frame that
        // changed" metric (see motionIntensity) to land in a usable range.
        let sampleWidth = 32
        let sampleHeight = 18
        var pixels = [UInt8](repeating: 0, count: sampleWidth * sampleHeight * 4)
        let colorSpace = CGColorSpaceCreateDeviceRGB()

        guard let context = CGContext(
            data: &pixels,
            width: sampleWidth,
            height: sampleHeight,
            bitsPerComponent: 8,
            bytesPerRow: sampleWidth * 4,
            space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ) else {
            return nil
        }

        context.interpolationQuality = .low
        context.draw(cgImage, in: CGRect(x: 0, y: 0, width: sampleWidth, height: sampleHeight))

        // Extract R, G, B channels and average to luminance using vDSP
        let pixelCount = sampleWidth * sampleHeight
        var floatPixels = [Float](repeating: 0, count: pixels.count)
        vDSP_vfltu8(pixels, 1, &floatPixels, 1, vDSP_Length(pixels.count))

        var r = [Float](repeating: 0, count: pixelCount)
        var g = [Float](repeating: 0, count: pixelCount)
        var b = [Float](repeating: 0, count: pixelCount)
        for i in 0..<pixelCount {
            r[i] = floatPixels[i * 4]
            g[i] = floatPixels[i * 4 + 1]
            b[i] = floatPixels[i * 4 + 2]
        }
        // Weighted luminance: 0.299R + 0.587G + 0.114B
        var luma = [Float](repeating: 0, count: pixelCount)
        var wr: Float = 0.299, wg: Float = 0.587, wb: Float = 0.114
        vDSP_vsma(r, 1, &wr, luma, 1, &luma, 1, vDSP_Length(pixelCount))
        vDSP_vsma(g, 1, &wg, luma, 1, &luma, 1, vDSP_Length(pixelCount))
        vDSP_vsma(b, 1, &wb, luma, 1, &luma, 1, vDSP_Length(pixelCount))
        var uint8Luma = [UInt8](repeating: 0, count: pixelCount)
        vDSP_vfixu8(luma, 1, &uint8Luma, 1, vDSP_Length(pixelCount))
        samples = uint8Luma
    }

    public func motionIntensity(comparedTo previous: BackgroundMotionFrameSignature) -> Double {
        guard samples.count == previous.samples.count, samples.isEmpty == false else {
            return 0
        }

        // Fraction of cells whose luma changed by more than a per-cell delta.
        //
        // The old metric was mean(|Δluma|) / 255 across ALL cells, which for a
        // person occupying a handful of cells produced ~0.004 — an order of
        // magnitude below the 0.03–0.20 threshold range, so the motion track was
        // permanently empty. "What fraction of the frame moved" scales the way a
        // human expects: a person walking through ≈ 0.03–0.10, a near/large
        // subject higher, sensor noise on a static scene ≈ 0. The per-cell delta
        // (out of 255) rejects JPEG/compression noise and slow lighting drift.
        let perCellDelta = 18
        var changedCells = 0
        for i in 0..<samples.count {
            if abs(Int(samples[i]) - Int(previous.samples[i])) > perCellDelta {
                changedCells += 1
            }
        }

        return Double(changedCells) / Double(samples.count)
    }
}

private func createDirectoryIfNeeded(_ url: URL) throws {
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
}