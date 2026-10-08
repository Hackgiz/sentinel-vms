import Foundation
import SwiftUI
import SentinelCore

// MediaMTX acts as an RTSP proxy between cameras and the app.
// One camera connection → unlimited simultaneous viewers (Mac tiles, iOS HLS, GStreamer detection).
// It also handles recording natively via fmp4 segments.
public enum MediaMTXStreamState {
    case proxying(readers: Int)
    case noSignal
    case unknown

    public var label: String {
        switch self {
        case .proxying(let r): return r > 0 ? "Proxying · \(r) viewer\(r == 1 ? "" : "s")" : "Proxying"
        case .noSignal: return "No signal"
        case .unknown: return "Unknown"
        }
    }

    public var tint: Color {
        switch self {
        case .proxying: return .green
        case .noSignal: return .red
        case .unknown: return .secondary
        }
    }
}

@MainActor
public final class MediaMTXStore: ObservableObject {
    @Published public private(set) var isRunning = false
    @Published public private(set) var binaryPath: String?
    @Published public private(set) var startError: String?
    @Published public private(set) var cameraStreamStates: [UUID: MediaMTXStreamState] = [:]

    /// Invoked on the main actor after each polling cycle with the latest
    /// state for every camera in the config. Lets other stores
    /// (e.g. MediaIngestStore) treat proxying/no-signal as live-frame
    /// evidence without MediaMTXStore having to know about them.
    public var onStreamStateChange: (@MainActor (UUID, MediaMTXStreamState) -> Void)?

    public static let rtspPort = 8554
    public static let hlsPort  = 8888
    public static let apiPort  = 9997

    private var process: Process?
    private var pollTask: Task<Void, Never>?
    private let supportDir: URL
    private let configURL: URL

    // Retained inputs from the last start() so an unexpected MediaMTX exit can
    // transparently relaunch with the same configuration.
    private var lastCameras: [CameraFeed] = []
    private var lastRecordingRootURL: URL?
    private var lastCredentials: CameraCredentialStore?
    /// True only while we are deliberately tearing MediaMTX down (stop()/restart)
    /// so the termination handler can tell an intentional exit from a crash.
    private var intentionalStop = false
    /// Auto-restart bookkeeping for unexpected exits (reset once a run proves
    /// healthy via the API poll).
    private var autoRestartAttempts = 0
    private let maxAutoRestartAttempts = 5
    private var autoRestartTask: Task<Void, Never>?

    public init() {
        supportDir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        configURL = supportDir.appendingPathComponent("mediamtx.yml")
        binaryPath = Self.detectBinary(in: supportDir)
        if let saved = UserDefaults.standard.array(forKey: subDisabledKey) as? [String] {
            subDisabledCameraIDs = Set(saved.compactMap(UUID.init))
        }
    }

    /// Re-enable a camera's sub-stream (clears the auto-disabled flag) so the
    /// user can retry low-res live after fixing the camera's stream limits.
    public func reenableSubStream(for cameraID: UUID) {
        subDisabledCameraIDs.remove(cameraID)
        persistSubDisabled()
    }

    private func persistSubDisabled() {
        UserDefaults.standard.set(subDisabledCameraIDs.map(\.uuidString), forKey: subDisabledKey)
    }

    // MARK: - Live-stream routing (main + sub)

    /// Cameras that have a low-res sub-stream ingested. Live tiles, AI
    /// detection, and the iOS app all view this sub-stream so the Mac never
    /// decodes the high-res main just to show a preview — the main is reserved
    /// for full-quality recording. Populated by `writeConfig`.
    @Published public private(set) var subStreamCameraIDs: Set<UUID> = []

    /// Every camera that has a MAIN path in the generated config (i.e. is expected
    /// to be proxied). Used by `pollStreamStates` so a camera that is down — and
    /// therefore never appears in the MediaMTX API list — is still reported
    /// no-signal instead of staying `.unknown` forever.
    private var configuredCameraIDs: Set<UUID> = []

    /// Cameras whose sub-stream was AUTO-DISABLED because pulling it starved the
    /// high-res recording (budget cameras that can't serve main + sub at once).
    /// Recording always wins. Persisted so the bad pairing isn't retried every
    /// launch. Surfaced in the camera editor.
    @Published public private(set) var subDisabledCameraIDs: Set<UUID> = []
    private let subDisabledKey = "handoffgrid.subStreamAutoDisabled"

    /// Fired when the watchdog disables a camera's sub-stream so the app layer
    /// can rewrite + reload the MediaMTX config (dropping the sub path).
    public var onSubAutoDisabled: (@MainActor (UUID) -> Void)?

    /// The MediaMTX path used for *viewing* a camera: the low-res sub-stream
    /// when one exists and is allowed, otherwise the main stream. Recording
    /// always uses the main path (`<id>`), regardless of this.
    public func livePath(for cameraID: UUID) -> String {
        (subStreamCameraIDs.contains(cameraID) && subDisabledCameraIDs.contains(cameraID) == false)
            ? "\(cameraID.uuidString)-sub" : cameraID.uuidString
    }

    // MARK: - Public URLs

    public func rtspURL(for cameraID: UUID) -> String {
        "rtsp://127.0.0.1:\(Self.rtspPort)/\(livePath(for: cameraID))"
    }

    public func hlsURL(for cameraID: UUID) -> String {
        "http://127.0.0.1:\(Self.hlsPort)/\(livePath(for: cameraID))/index.m3u8"
    }

    // External URL for iOS app (uses the Mac's LAN IP, not localhost)
    public func externalHLSURL(for cameraID: UUID) -> String? {
        guard let ip = Self.localIPAddress() else { return nil }
        return "http://\(ip):\(Self.hlsPort)/\(livePath(for: cameraID))/index.m3u8"
    }

    // MARK: - Lifecycle

    public func start(cameras: [CameraFeed], recordingRootURL: URL, credentials: CameraCredentialStore) {
        guard let binaryPath else {
            startError = "MediaMTX binary not found in app support directory."
            return
        }
        // Cold start (fresh process / after a crash) vs. an in-session restart.
        // Only nuke orphaned GStreamer pipelines on a cold start — in-session we
        // track and manage our own previews and must not disrupt them.
        let coldStart = (isRunning == false && process == nil)
        stop()
        // Remember the inputs and re-arm self-heal now that the previous
        // instance (if any) is being torn down. stop() set intentionalStop =
        // true; clear it so THIS run's termination handler treats an unexpected
        // exit as a crash worth restarting.
        lastCameras = cameras
        lastRecordingRootURL = recordingRootURL
        lastCredentials = credentials
        intentionalStop = false
        autoRestartTask?.cancel()
        autoRestartTask = nil
        // Reclaim the ports before relaunching. stop() only SIGINTs our own
        // tracked process (with a 3s grace before SIGKILL), and orphans from a
        // crash/force-quit aren't tracked at all — either can still hold :8554,
        // making the fresh instance die with "address already in use" and the
        // live tiles hang forever on "Starting MediaMTX proxy". A hard kill of
        // any lingering mediamtx guarantees a clean bind.
        Self.reclaimPorts(killOrphanedGStreamer: coldStart)
        startError = nil
        writeConfig(cameras: cameras, recordingRootURL: recordingRootURL, credentials: credentials)

        let logURL = supportDir.appendingPathComponent("mediamtx.log")
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        let logFile = try? FileHandle(forWritingTo: logURL)

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: binaryPath)
        proc.arguments = [configURL.path]
        proc.standardOutput = logFile
        proc.standardError = logFile
        proc.terminationHandler = { [weak self] _ in
            try? logFile?.close()
            Task { @MainActor in
                guard let self else { return }
                self.isRunning = false
                self.process = nil
                // If MediaMTX died on its own (not via our stop()/restart), bring
                // it back. The usual cause is a transient ":8554 bind: address
                // already in use" race against a not-yet-reaped orphan; a re-run
                // after reclaiming the ports almost always succeeds. Without this,
                // the live tiles hang on "Connecting" forever.
                if self.intentionalStop == false {
                    self.scheduleAutoRestart()
                }
            }
        }

        do {
            try proc.run()
            process = proc
            isRunning = true
            startPolling()
        } catch {
            startError = error.localizedDescription
            isRunning = false
        }
    }

    public func stop() {
        intentionalStop = true
        autoRestartTask?.cancel()
        autoRestartTask = nil
        pollTask?.cancel()
        pollTask = nil
        let proc = process
        proc?.interrupt()
        if let pid = proc?.processIdentifier {
            Task.detached { [weak proc] in
                try? await Task.sleep(nanoseconds: 3_000_000_000)
                if proc?.isRunning == true {
                    kill(pid, SIGKILL)
                }
            }
        }
        process = nil
        isRunning = false
        cameraStreamStates = [:]
    }

    /// Relaunch MediaMTX after an unexpected exit, with bounded attempts and a
    /// short backoff so a momentarily-wedged port has time to free. Re-runs the
    /// full start() path (which reclaims ports first), so a self-heal also clears
    /// whatever was squatting on :8554. Gives up after `maxAutoRestartAttempts`
    /// and surfaces an error rather than spinning.
    private func scheduleAutoRestart() {
        guard let root = lastRecordingRootURL, let creds = lastCredentials else { return }
        guard autoRestartAttempts < maxAutoRestartAttempts else {
            startError = "MediaMTX exited repeatedly — port \(Self.rtspPort) may be held by another process. Live video is unavailable; quit any other copy of Sentinel and relaunch."
            return
        }
        autoRestartAttempts += 1
        let attempt = autoRestartAttempts
        autoRestartTask?.cancel()
        autoRestartTask = Task { [weak self] in
            let delay = min(5.0, 0.6 * pow(2.0, Double(attempt - 1)))
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, Task.isCancelled == false else { return }
            self.start(cameras: self.lastCameras, recordingRootURL: root, credentials: creds)
        }
    }

    /// Hard-kill anything that could wedge a fresh start: a lingering `mediamtx`
    /// holding its ports, AND orphaned Sentinel GStreamer pipelines (live
    /// previews / recorders) left behind by a crash, force-quit, or fast
    /// relaunch — those keep reading the dying proxy and stall the detector.
    /// SIGKILL is async, so we then poll until every port the new instance
    /// needs is actually free before returning, rather than guessing a sleep.
    private static func reclaimPorts(killOrphanedGStreamer: Bool) {
        // 1. Lingering MediaMTX (exact process name).
        runTool("/usr/bin/pkill", ["-9", "-x", "mediamtx"])
        // 2. Orphaned Sentinel GStreamer pipelines (cold start only). Match
        //    gst-launch processes whose command line references our app-support
        //    paths (LivePreviews / Recordings live under "HandoffGridSentinel"),
        //    so we never touch an unrelated GStreamer app or the main Sentinel
        //    process itself.
        if killOrphanedGStreamer {
            runTool("/usr/bin/pkill", ["-9", "-f", "gst-launch.*HandoffGridSentinel"])
        }
        // 3. Kill by PORT, not just by name. `pkill -x mediamtx` only catches a
        //    process literally named "mediamtx"; anything else squatting on our
        //    ports (a renamed/debug binary, a wedged orphan whose name changed,
        //    another RTSP tool) would otherwise survive and make the fresh start
        //    die with "address already in use" — exactly the bind failure that
        //    leaves every live tile stuck on "Connecting".
        let ports = [rtspPort, hlsPort, apiPort]
        for port in ports {
            for pid in listeningPIDs(onPort: port) { kill(pid, SIGKILL) }
        }
        // 4. Wait (bounded ~1.5s) for every port to actually be released by the
        //    kernel, re-killing any straggler that reappears while we wait.
        for _ in 0..<30 {
            if ports.allSatisfy({ listeningPIDs(onPort: $0).isEmpty }) { return }
            for port in ports {
                for pid in listeningPIDs(onPort: port) { kill(pid, SIGKILL) }
            }
            usleep(50_000) // 50ms
        }
    }

    /// PIDs with a LISTEN socket on `port`, via lsof. Empty if none / lsof fails.
    private static func listeningPIDs(onPort port: Int) -> [Int32] {
        let out = runTool("/usr/sbin/lsof", ["-nP", "-iTCP:\(port)", "-sTCP:LISTEN", "-t"])
        return out.split(whereSeparator: { $0 == "\n" || $0 == " " })
            .compactMap { Int32($0.trimmingCharacters(in: .whitespaces)) }
    }

    /// Runs a short-lived command-line tool synchronously, returning stdout.
    @discardableResult
    private static func runTool(_ path: String, _ arguments: [String]) -> String {
        guard FileManager.default.isExecutableFile(atPath: path) else { return "" }
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: path)
        proc.arguments = arguments
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice
        do { try proc.run() } catch { return "" }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        proc.waitUntilExit()
        return String(data: data, encoding: .utf8) ?? ""
    }

    public func streamState(for cameraID: UUID) -> MediaMTXStreamState {
        cameraStreamStates[cameraID] ?? .unknown
    }

    private func startPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            // Give MediaMTX a moment to come up before first poll
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            while Task.isCancelled == false {
                await self?.pollStreamStates()
                try? await Task.sleep(nanoseconds: 10_000_000_000)
            }
        }
    }

    private func pollStreamStates() async {
        guard let url = URL(string: "http://127.0.0.1:\(Self.apiPort)/v3/paths/list") else { return }
        guard let (data, _) = try? await URLSession.shared.data(from: url) else { return }

        struct PathItem: Decodable {
            let name: String
            let ready: Bool?
            let bytesReceived: Int?
            let readers: [AnyDecodable]?
            struct AnyDecodable: Decodable {}
        }
        struct PathList: Decodable {
            let items: [PathItem]?
        }

        guard let list = try? JSONDecoder().decode(PathList.self, from: data) else { return }
        // The API answered → MediaMTX is healthy this run; reset the crash budget
        // so a future unexpected exit gets a fresh set of restart attempts.
        autoRestartAttempts = 0
        var states: [UUID: MediaMTXStreamState] = [:]
        var byName: [String: PathItemHealth] = [:]
        for item in list.items ?? [] {
            byName[item.name] = PathItemHealth(ready: item.ready ?? false, bytesReceived: item.bytesReceived ?? 0)
            guard let id = UUID(uuidString: item.name) else { continue }
            let readerCount = item.readers?.count ?? 0
            states[id] = .proxying(readers: readerCount)
        }

        runRecordingProtectionWatchdog(byName: byName)

        // Mark every CONFIGURED camera that isn't in the API response as
        // no-signal. Driving this from the configured set (not the previous
        // poll's dict) is what lets a camera that is down at launch — and so
        // never appears in the API list — be reported offline instead of
        // staying `.unknown` forever.
        for id in configuredCameraIDs where states[id] == nil {
            states[id] = .noSignal
        }
        cameraStreamStates = states

        if let handler = onStreamStateChange {
            for (id, state) in states {
                handler(id, state)
            }
        }
    }

    // MARK: - Recording-protection watchdog

    struct PathItemHealth { let ready: Bool; let bytesReceived: Int }
    private var mainUnhealthyCount: [UUID: Int] = [:]
    private var lastMainBytes: [UUID: Int] = [:]
    /// Consecutive unhealthy polls (×10s) before we sacrifice the sub. ~30s
    /// avoids tripping on a normal cold-start reconnect.
    private let unhealthyTripCount = 3

    /// Detects the failure mode where a budget camera's low-res sub is up and
    /// healthy while its high-res MAIN (the recording source) keeps dropping —
    /// i.e. the sub is starving the recording. When that persists, disable the
    /// sub for that camera so recording recovers.
    private func runRecordingProtectionWatchdog(byName: [String: PathItemHealth]) {
        for id in subStreamCameraIDs where subDisabledCameraIDs.contains(id) == false {
            let main = byName[id.uuidString]
            let sub = byName["\(id.uuidString)-sub"]

            // Only meaningful when the sub is actually pulled (a reader is live).
            guard let sub, sub.ready else { mainUnhealthyCount[id] = 0; continue }

            let bytes = main?.bytesReceived ?? 0
            let stalled = (lastMainBytes[id].map { $0 == bytes } ?? false)
            let mainUnhealthy = (main?.ready != true) || stalled
            lastMainBytes[id] = bytes

            if mainUnhealthy {
                let n = (mainUnhealthyCount[id] ?? 0) + 1
                mainUnhealthyCount[id] = n
                if n >= unhealthyTripCount {
                    mainUnhealthyCount[id] = 0
                    subDisabledCameraIDs.insert(id)
                    persistSubDisabled()
                    let handler = onSubAutoDisabled
                    Task { @MainActor in handler?(id) }
                }
            } else {
                mainUnhealthyCount[id] = 0
            }
        }
    }

    /// The exact YAML last written to disk, so a reload whose config is
    /// byte-identical becomes a true no-op (no SIGHUP) instead of churning
    /// every camera's recording mid-segment.
    private var lastWrittenConfigYAML: String?
    private var reloadDebounceTask: Task<Void, Never>?

    /// When set (disk-pressure guard), every path is written with `record: no`
    /// so MediaMTX stops writing segments without us tearing down the streams.
    public private(set) var recordingSuspended = false

    /// Suspend or resume all MediaMTX recording (disk-pressure protection).
    public func setRecordingSuspended(
        _ suspended: Bool,
        cameras: [CameraFeed],
        recordingRootURL: URL,
        credentials: CameraCredentialStore
    ) {
        guard recordingSuspended != suspended else { return }
        recordingSuspended = suspended
        reload(cameras: cameras, recordingRootURL: recordingRootURL, credentials: credentials)
    }

    // Reload config without restarting (MediaMTX reloads on SIGHUP).
    // Debounced (~500ms) so a burst of camera-state changes collapses into one
    // reload, and a SIGHUP is sent ONLY when the generated config actually
    // changed — preventing recording hiccups from redundant reloads.
    public func reload(cameras: [CameraFeed], recordingRootURL: URL, credentials: CameraCredentialStore) {
        reloadDebounceTask?.cancel()
        reloadDebounceTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 500_000_000)
            guard let self, Task.isCancelled == false else { return }
            let changed = self.writeConfig(
                cameras: cameras,
                recordingRootURL: recordingRootURL,
                credentials: credentials
            )
            if changed, let pid = self.process?.processIdentifier {
                kill(pid, SIGHUP)
            }
        }
    }

    public var isAvailable: Bool { binaryPath != nil }

    // MARK: - Config generation

    /// Wraps a string as a YAML single-quoted scalar safe to embed as a MediaMTX
    /// `source:` value. Single quotes are escaped per the YAML spec (`'` → `''`).
    /// Returns nil only if the string contains a control/line-breaking character
    /// (newline, CR, NUL, etc.) that can't live on one config line — those are
    /// the genuine config-corruptors. Everything else (including `!`, `:`, `#`,
    /// spaces, `$`) is valid inside a quoted scalar.
    static func yamlQuotedScalar(_ s: String) -> String? {
        guard s.isEmpty == false else { return nil }
        if s.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7F }) { return nil }
        return "'" + s.replacingOccurrences(of: "'", with: "''") + "'"
    }

    @discardableResult
    public func writeConfig(cameras: [CameraFeed], recordingRootURL: URL, credentials: CameraCredentialStore) -> Bool {
        var pathBlocks: [String] = []
        var subIDs: Set<UUID> = []
        var mainIDs: Set<UUID> = []

        for camera in cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
            let sourceURL = credentials.rtspURL(for: camera)
            // Embed the URL as a YAML single-quoted scalar. This safely carries
            // passwords with `!`, `:`, `#`, spaces, etc. (unlike a GStreamer
            // pipeline arg — do NOT use isSafeForPipeline here, it rejects `!`
            // which is a perfectly valid password character for MediaMTX). Only a
            // genuine line-breaking control character would corrupt the config and
            // stop MediaMTX from starting; skip just that camera if so.
            guard let sourceScalar = Self.yamlQuotedScalar(sourceURL) else { continue }
            let cameraRecordDir = recordingRootURL
                .appendingPathComponent(camera.id.uuidString, isDirectory: true)
            try? FileManager.default.createDirectory(at: cameraRecordDir, withIntermediateDirectories: true)

            let retentionHours = (camera.retentionDays ?? 0) > 0
                ? "\((camera.retentionDays ?? 7) * 24)h"
                : "168h"

            // The MAIN (high-res) path always records the full-quality stream
            // per the camera's recording mode. MediaMTX just muxes the encoded
            // stream to disk — cheap on CPU — so recording high-res is free.
            // (The legacy dualStream mode still defers main recording to its
            // motion-triggered GStreamer pass.)
            let mainStreamRecord = camera.isRecording && camera.recordingMode != .dualStream && recordingSuspended == false

            mainIDs.insert(camera.id)
            pathBlocks.append("""
              \(camera.id.uuidString):
                source: \(sourceScalar)
                rtspTransport: tcp
                sourceOnDemand: no
                record: \(mainStreamRecord ? "yes" : "no")
                recordPath: \(recordingRootURL.path)/%path/%Y%m%d-%H%M%S-%f
                recordFormat: fmp4
                recordSegmentDuration: 15m
                recordDeleteAfter: \(retentionHours)
            """)

            // SUB (low-res) path: whenever the camera exposes a sub-stream URL we
            // ingest it for VIEWING ONLY (record: no). Live tiles, AI detection,
            // and the iOS app all read this low-res feed so the Mac never decodes
            // the high-res main just to show a thumbnail. This is the big CPU win.
            let subURL = credentials.subStreamRTSPURL(for: camera)
            if subURL.isEmpty == false && subDisabledCameraIDs.contains(camera.id) == false,
               let subScalar = Self.yamlQuotedScalar(subURL) {
                subIDs.insert(camera.id)
                // dualStream mode keeps its historical always-on low-res recording;
                // every other mode uses the sub purely for live (no disk cost).
                let subRecords = camera.recordingMode == .dualStream && recordingSuspended == false
                let subRecordDir = recordingRootURL
                    .appendingPathComponent("\(camera.id.uuidString)-sub", isDirectory: true)
                if subRecords {
                    try? FileManager.default.createDirectory(at: subRecordDir, withIntermediateDirectories: true)
                }
                // On-demand UNLESS the sub is itself being recorded (dualStream):
                // many budget cameras (e.g. Tapo) can't serve main + sub at once,
                // and recording the high-res main must never be starved. On-demand
                // means the sub is only pulled while someone is actually viewing.
                let subOnDemand = subRecords ? "no" : "yes"
                pathBlocks.append("""
                  \(camera.id.uuidString)-sub:
                    source: \(subScalar)
                    rtspTransport: tcp
                    sourceOnDemand: \(subOnDemand)
                    record: \(subRecords ? "yes" : "no")
                    recordPath: \(recordingRootURL.path)/%path/%Y%m%d-%H%M%S-%f
                    recordFormat: fmp4
                    recordSegmentDuration: 15m
                    recordDeleteAfter: \(retentionHours)
                """)
            }
        }
        subStreamCameraIDs = subIDs
        configuredCameraIDs = mainIDs

        let yaml = """
        logLevel: info
        logDestinations: [file]
        logFile: \(supportDir.appendingPathComponent("mediamtx.log").path)

        api: yes
        apiAddress: 127.0.0.1:\(Self.apiPort)

        rtsp: yes
        rtspAddress: :\(Self.rtspPort)
        # Force TCP-only transport (MediaMTX v1.11+ spelling) so we don't fight
        # over UDP 8000, which collides with various AirPlay/printer services
        # on a typical Mac.
        rtspTransports: [tcp]
        rtpAddress: :18000
        rtcpAddress: :18001
        readTimeout: 10s
        writeTimeout: 10s

        hls: yes
        hlsAddress: :\(Self.hlsPort)
        hlsAlwaysRemux: yes
        # mpegts variant tolerates Tapo cameras' variable keyframe spacing;
        # fMP4-LowLatency chokes on segment-duration drift and freezes AVPlayer.
        # hlsSegmentCount MUST be >= 3 (MediaMTX rejects fewer and serves no
        # playlist at all). Startup latency is therefore floored at ~3 ×
        # keyframe-interval; the camera's GOP is the real lever, not this count.
        hlsVariant: mpegts
        hlsSegmentCount: 3
        hlsSegmentDuration: 1s

        rtmp: no
        srt: no
        webrtc: no
        # MediaMTX v1.21+ enables a MoQ server on :8892/:8893 by default.
        moq: no

        paths:
        \(pathBlocks.isEmpty ? "  ~:" : pathBlocks.joined(separator: "\n"))
        """

        // Nothing changed since the last write: skip the disk write AND the
        // caller's SIGHUP. subStreamCameraIDs is already refreshed above, so an
        // identical config never interrupts in-progress recordings. (Still write
        // if the file somehow went missing.)
        if yaml == lastWrittenConfigYAML,
           FileManager.default.fileExists(atPath: configURL.path) {
            return false
        }

        try? FileManager.default.createDirectory(at: supportDir, withIntermediateDirectories: true)
        // The YAML embeds RTSP credentials in plaintext. Create the file with
        // 0o600 from the start (FileManager.createFile applies the supplied
        // attributes at creation time) so there's no world-readable window
        // between write and chmod. Removing any prior copy first avoids
        // inheriting looser permissions from an older config.
        try? FileManager.default.removeItem(at: configURL)
        let attrs: [FileAttributeKey: Any] = [.posixPermissions: 0o600]
        FileManager.default.createFile(
            atPath: configURL.path,
            contents: Data(yaml.utf8),
            attributes: attrs
        )
        // Belt-and-suspenders: ensure perms even if the file already existed.
        try? FileManager.default.setAttributes(attrs, ofItemAtPath: configURL.path)
        lastWrittenConfigYAML = yaml
        return true
    }

    // MARK: - Detection

    private static func detectBinary(in supportDir: URL) -> String? {
        // Check the .app bundle's Resources/ first so a shareable build can
        // ship MediaMTX alongside Sentinel VMS without making the recipient
        // install anything. Fall back to user support dir and Homebrew paths.
        var candidates: [String] = []
        if let bundleMTX = Bundle.main.url(forResource: "mediamtx", withExtension: nil) {
            candidates.append(bundleMTX.path)
        }
        candidates.append(contentsOf: [
            supportDir.appendingPathComponent("mediamtx").path,
            "/opt/homebrew/bin/mediamtx",
            "/usr/local/bin/mediamtx"
        ])
        return candidates.first { FileManager.default.fileExists(atPath: $0) }
    }

    // MARK: - Network helpers

    public static func localIPAddress() -> String? {
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0 else { return nil }
        defer { freeifaddrs(ifaddr) }
        var current = ifaddr
        while let ifa = current {
            // ifa_addr can be NULL for some interfaces (down / utun); guard it
            // before dereferencing or this crashes enumerating interfaces, which
            // would take out the external HLS URL the iOS app relies on.
            guard let addrPtr = ifa.pointee.ifa_addr else { current = ifa.pointee.ifa_next; continue }
            let family = addrPtr.pointee.sa_family
            if family == UInt8(AF_INET) {
                let name = String(cString: ifa.pointee.ifa_name)
                if name.hasPrefix("en") {
                    var addr = addrPtr.pointee
                    var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
                    getnameinfo(&addr, socklen_t(addrPtr.pointee.sa_len),
                                &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST)
                    let ip = String(cString: host)
                    if ip.hasPrefix("192.") || ip.hasPrefix("10.") || ip.hasPrefix("172.") {
                        return ip
                    }
                }
            }
            current = ifa.pointee.ifa_next
        }
        return nil
    }
}
