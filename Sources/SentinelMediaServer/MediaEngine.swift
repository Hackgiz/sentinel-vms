import Foundation
import SwiftUI
import SentinelCore

public enum MediaEngineReadiness: String {
    case ready = "Ready"
    case partial = "Partial"
    case missing = "Missing"

    public var tint: Color {
        switch self {
        case .ready: return .green
        case .partial: return .orange
        case .missing: return .red
        }
    }
}

public struct MediaEngineSnapshot {
    public let readiness: MediaEngineReadiness
    public let gstreamerLaunchPath: String?
    public let gstreamerInspectPath: String?
    public let frameworkPath: String?
    public let vlcAppPath: String?
    public let notes: [String]

    public var canProbePipelines: Bool {
        gstreamerLaunchPath != nil && gstreamerInspectPath != nil
    }

    public static let unavailable = MediaEngineSnapshot(
        readiness: .missing,
        gstreamerLaunchPath: nil,
        gstreamerInspectPath: nil,
        frameworkPath: nil,
        vlcAppPath: nil,
        notes: [
            "GStreamer runtime not found.",
            "Install the macOS runtime and development packages before live RTSP preview."
        ]
    )
}

public struct MediaPipelineLaunchResult {
    public let didLaunch: Bool
    public let title: String
    public let detail: String
    public let commandPreview: String

    public init(didLaunch: Bool, title: String, detail: String, commandPreview: String) {
        self.didLaunch = didLaunch
        self.title = title
        self.detail = detail
        self.commandPreview = commandPreview
    }

    public static func failure(_ title: String, detail: String) -> MediaPipelineLaunchResult {
        MediaPipelineLaunchResult(
            didLaunch: false,
            title: title,
            detail: detail,
            commandPreview: ""
        )
    }
}

@MainActor
public final class MediaEngineStore: ObservableObject {
    @Published public private(set) var snapshot = MediaEngineSnapshot.unavailable
    @Published public private(set) var activePreviewProcessIDs: [Int32] = []
    private var activePreviewProcesses: [Int32: Process] = [:]

    public init() {
        Task { self.refresh() }
    }

    public func refresh() {
        Task {
            let detected = await Task.detached(priority: .userInitiated) {
                MediaEngineDetector.detect()
            }.value
            self.snapshot = detected
        }
    }

    /// Detect synchronously (awaitable) and publish the result. Use this before
    /// any code that gates on `snapshot.gstreamerLaunchPath` — the fire-and-
    /// forget `refresh()` updates the snapshot asynchronously, so a guard run
    /// right after it can race and wrongly see GStreamer as missing (which
    /// silently disables live previews + motion/AI detection for the session).
    @discardableResult
    public func refreshAndWait() async -> MediaEngineSnapshot {
        let detected = await Task.detached(priority: .userInitiated) {
            MediaEngineDetector.detect()
        }.value
        self.snapshot = detected
        return detected
    }

    public func launchExternalPreview(for camera: CameraFeed, credentials: CameraCredentialStore) -> MediaPipelineLaunchResult {
        refresh()

        guard let launchPath = snapshot.gstreamerLaunchPath else {
            return .failure(
                "GStreamer Missing",
                detail: "gst-launch-1.0 was not found. Install the GStreamer runtime and development packages."
            )
        }

        guard camera.rtspURL.isEmpty == false else {
            return .failure(
                "No RTSP URL",
                detail: "This camera does not have an RTSP URL configured."
            )
        }

        let rtspURL = credentials.rtspURL(for: camera)
        guard RTSPCredentialFormatter.isSafeForPipeline(rtspURL) else {
            return .failure(
                "Invalid RTSP URL",
                detail: "The RTSP URL contains characters that are not allowed in a streaming pipeline."
            )
        }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: launchPath)
        process.arguments = [
            "-q",
            "uridecodebin",
            "uri=\(rtspURL)",
            "!",
            "videoconvert",
            "!",
            "autovideosink"
        ]
        process.environment = MediaEngineProcessEnvironment.gstreamerEnvironment(launchPath: launchPath)
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        process.terminationHandler = { [weak self] terminatedProcess in
            Task { @MainActor in
                self?.activePreviewProcesses[terminatedProcess.processIdentifier] = nil
                self?.activePreviewProcessIDs.removeAll { $0 == terminatedProcess.processIdentifier }
            }
        }

        do {
            try process.run()
            activePreviewProcesses[process.processIdentifier] = process
            activePreviewProcessIDs.append(process.processIdentifier)

            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Preview Launched",
                detail: "GStreamer opened an external live preview for \(camera.name).",
                commandPreview: commandPreview(launchPath: launchPath, arguments: process.arguments)
            )
        } catch {
            return .failure(
                "Preview Failed",
                detail: error.localizedDescription
            )
        }
    }

    public func launchVLCPreview(for camera: CameraFeed, credentials: CameraCredentialStore) -> MediaPipelineLaunchResult {
        refresh()

        guard snapshot.vlcAppPath != nil else {
            return .failure(
                "VLC Missing",
                detail: "VLC was not found in /Applications."
            )
        }

        guard camera.rtspURL.isEmpty == false else {
            return .failure(
                "No RTSP URL",
                detail: "This camera does not have an RTSP URL configured."
            )
        }

        let vlcURL = credentials.rtspURL(for: camera)
        guard RTSPCredentialFormatter.isSafeForPipeline(vlcURL) else {
            return .failure(
                "Invalid RTSP URL",
                detail: "The RTSP URL contains characters that are not allowed."
            )
        }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        process.arguments = [
            "-a",
            "VLC",
            vlcURL
        ]

        do {
            try process.run()
            return MediaPipelineLaunchResult(
                didLaunch: true,
                title: "VLC Preview Opened",
                detail: "VLC is opening the RTSP stream for \(camera.name).",
                commandPreview: commandPreview(launchPath: "/usr/bin/open", arguments: process.arguments)
            )
        } catch {
            return .failure(
                "VLC Preview Failed",
                detail: error.localizedDescription
            )
        }
    }

    public func stopExternalPreviews() {
        let snapshot = activePreviewProcesses
        for (processID, process) in snapshot {
            if process.isRunning {
                kill(processID, SIGTERM)
            }
        }

        activePreviewProcesses.removeAll()
        activePreviewProcessIDs.removeAll()

        // SIGTERM may be ignored by a stuck child (blocked I/O, plugin hang).
        // Escalate to SIGKILL after a short grace period so quitting the app
        // doesn't leave orphaned gst-launch processes behind.
        Task.detached {
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            for (processID, process) in snapshot where process.isRunning {
                kill(processID, SIGKILL)
            }
        }
    }

    private func commandPreview(launchPath: String, arguments: [String]?) -> String {
        ([launchPath] + (arguments ?? []).map(RTSPCredentialFormatter.redacted)).joined(separator: " ")
    }
}

public enum MediaEngineProcessEnvironment {
    /// Builds the environment for a spawned GStreamer process by deriving every
    /// path from `launchPath` itself. A GStreamer install — whether the system
    /// framework at `/Library/Frameworks/GStreamer.framework/Versions/1.0` or
    /// the trimmed copy bundled inside the app at
    /// `…/Sentinel VMS.app/Contents/Resources/gstreamer` — always has the shape
    /// `<root>/bin/gst-launch-1.0`, so the root is `launchPath/../..`. This lets
    /// the bundled runtime work without hard-coding the system location.
    public static func gstreamerEnvironment(launchPath: String? = nil) -> [String: String] {
        var environment = ProcessInfo.processInfo.environment
        guard let launchPath else { return environment }

        let fileManager = FileManager.default
        let binDir = URL(fileURLWithPath: launchPath).deletingLastPathComponent()   // <root>/bin
        let root = binDir.deletingLastPathComponent()                               // <root>
        let libDir = root.appendingPathComponent("lib")
        let pluginDir = libDir.appendingPathComponent("gstreamer-1.0")

        environment["PATH"] = [binDir.path, environment["PATH"]].compactMap { $0 }.joined(separator: ":")

        guard fileManager.fileExists(atPath: libDir.path) else { return environment }

        environment["DYLD_LIBRARY_PATH"] = [libDir.path, environment["DYLD_LIBRARY_PATH"]].compactMap { $0 }.joined(separator: ":")
        environment["GST_PLUGIN_PATH"] = pluginDir.path
        environment["GST_PLUGIN_SYSTEM_PATH_1_0"] = pluginDir.path

        // Point GStreamer at the out-of-process plugin scanner that ships with
        // this install (otherwise it may fail to locate one when bundled).
        let scanner = root.appendingPathComponent("libexec/gstreamer-1.0/gst-plugin-scanner")
        if fileManager.fileExists(atPath: scanner.path) {
            environment["GST_PLUGIN_SCANNER_1_0"] = scanner.path
        }

        // GIO TLS modules (https sources) if present in this install.
        let gioModules = libDir.appendingPathComponent("gio/modules")
        if fileManager.fileExists(atPath: gioModules.path) {
            environment["GIO_MODULE_DIR"] = gioModules.path
        }

        // The app bundle is read-only and code-signed, so the plugin registry
        // cache must live in a writable location instead of next to the binary.
        if let support = fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first {
            let supportDir = support.appendingPathComponent("HandoffGridSentinel", isDirectory: true)
            try? fileManager.createDirectory(at: supportDir, withIntermediateDirectories: true)
            environment["GST_REGISTRY_1_0"] = supportDir.appendingPathComponent("gst-registry.bin").path
        }

        return environment
    }
}

public enum MediaEngineDetector {
    /// GStreamer runtime bundled inside the app at
    /// `…/Sentinel VMS.app/Contents/Resources/gstreamer`, or nil if this build
    /// wasn't bundled (e.g. a plain `swift run` from the package).
    private static var bundledRoot: URL? {
        Bundle.main.resourceURL?.appendingPathComponent("gstreamer", isDirectory: true)
    }

    public static func detect() -> MediaEngineSnapshot {
        // Prefer the bundled runtime; fall back to a system install.
        let bundledBin = bundledRoot?.appendingPathComponent("bin")

        let launchPath = firstExistingPath([
            bundledBin?.appendingPathComponent("gst-launch-1.0").path,
            "/Library/Frameworks/GStreamer.framework/Versions/1.0/bin/gst-launch-1.0",
            "/opt/homebrew/bin/gst-launch-1.0",
            "/usr/local/bin/gst-launch-1.0"
        ])

        let inspectPath = firstExistingPath([
            bundledBin?.appendingPathComponent("gst-inspect-1.0").path,
            "/Library/Frameworks/GStreamer.framework/Versions/1.0/bin/gst-inspect-1.0",
            "/opt/homebrew/bin/gst-inspect-1.0",
            "/usr/local/bin/gst-inspect-1.0"
        ])

        let frameworkPath = firstExistingPath([
            bundledRoot?.appendingPathComponent("lib/libgstreamer-1.0.0.dylib").path,
            "/Library/Frameworks/GStreamer.framework",
            "/opt/homebrew/lib/libgstreamer-1.0.dylib",
            "/usr/local/lib/libgstreamer-1.0.dylib"
        ])

        let vlcAppPath = firstExistingPath([
            "/Applications/VLC.app",
            "\(NSHomeDirectory())/Applications/VLC.app"
        ])

        let readiness: MediaEngineReadiness
        if launchPath != nil && inspectPath != nil && frameworkPath != nil {
            readiness = .ready
        } else if launchPath != nil || inspectPath != nil || frameworkPath != nil {
            readiness = .partial
        } else {
            readiness = .missing
        }

        return MediaEngineSnapshot(
            readiness: readiness,
            gstreamerLaunchPath: launchPath,
            gstreamerInspectPath: inspectPath,
            frameworkPath: frameworkPath,
            vlcAppPath: vlcAppPath,
            notes: notes(for: readiness)
        )
    }

    private static func firstExistingPath(_ paths: [String?]) -> String? {
        paths.compactMap { $0 }.first { FileManager.default.fileExists(atPath: $0) }
    }

    private static func notes(for readiness: MediaEngineReadiness) -> [String] {
        switch readiness {
        case .ready:
            return [
                "GStreamer is available for RTSP ingest experiments.",
                "Next step: bridge a pipeline into a SwiftUI preview surface."
            ]
        case .partial:
            return [
                "Only part of the GStreamer install was found.",
                "Install both runtime and development packages for app embedding."
            ]
        case .missing:
            return [
                "GStreamer was not found on this Mac.",
                "Use the official macOS runtime and development packages for the cleanest Xcode path."
            ]
        }
    }
}

public enum SentinelStreamingArchitecture {
    public static let ingestEngine = "GStreamer"
    public static let appleDecode = "VideoToolbox"
    public static let appleClientPlayback = "AVPlayer + HLS"
    public static let lowLatencyFuture = "WebRTC"
}
