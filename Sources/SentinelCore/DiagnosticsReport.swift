import Foundation
#if canImport(Darwin)
import Darwin
#endif

// MARK: - Diagnostics & bug reporting
//
// Assembles a *redacted* snapshot of the running app — version, OS, hardware,
// coarse camera count, and the tail of the activity log — that an operator can
// send when they hit a bug. There are no secrets in here by construction: the
// log is redacted at write time (see `SentinelLog`) and we deliberately collect
// only counts, never camera URLs, credentials, or API keys.
//
// Delivery is "upload, then fall back to email": `BugReportSubmitter` POSTs the
// report to the Sentinel gateway Worker; if that fails (offline, server down)
// the caller opens a prefilled mail draft instead, so a report is never lost.

public struct DiagnosticsReport: Codable, Sendable {
    public var appVersion: String
    public var build: String
    public var osVersion: String
    public var model: String
    public var cameraCount: Int
    public var crashedLastLaunch: Bool
    public var userMessage: String
    public var contactEmail: String
    public var logTail: String
    public var generatedAt: Date
    /// "bug" | "idea" | "question" — what kind of feedback this is.
    public var kind: String = "bug"
    /// Which app sent it ("mac" here; the Linux server sends "linux").
    public var platform: String = "mac"

    /// What goes on the wire / into the email body. Keep it human-readable.
    public func summaryText() -> String {
        """
        Sentinel VMS \(kind == "bug" ? "Bug Report" : kind == "idea" ? "Feature Idea" : "Question")
        =======================
        App:        \(appVersion) (build \(build))
        macOS:      \(osVersion)
        Mac model:  \(model)
        Cameras:    \(cameraCount)
        Crash on previous launch: \(crashedLastLaunch ? "YES" : "no")
        Generated:  \(ISO8601DateFormatter().string(from: generatedAt))

        What happened / steps to reproduce:
        \(userMessage.isEmpty ? "(none provided)" : userMessage)

        Contact: \(contactEmail.isEmpty ? "(none provided)" : contactEmail)

        ---- recent activity log (redacted) ----
        \(logTail)
        """
    }
}

public enum DiagnosticsCollector {

    public static var appVersion: String {
        Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "dev"
    }

    public static var build: String {
        Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? "0"
    }

    public static var osVersion: String {
        ProcessInfo.processInfo.operatingSystemVersionString
    }

    /// Hardware model identifier, e.g. "Mac14,9".
    public static var hardwareModel: String {
        #if canImport(Darwin)
        var size = 0
        sysctlbyname("hw.model", nil, &size, nil, 0)
        guard size > 0 else { return "unknown" }
        var buffer = [CChar](repeating: 0, count: size)
        sysctlbyname("hw.model", &buffer, &size, nil, 0)
        return String(cString: buffer)
        #else
        return "unknown"
        #endif
    }

    public static func build(cameraCount: Int,
                             userMessage: String,
                             contactEmail: String,
                             crashedLastLaunch: Bool,
                             kind: String = "bug") -> DiagnosticsReport {
        var report = DiagnosticsReport(
            appVersion: appVersion,
            build: build,
            osVersion: osVersion,
            model: hardwareModel,
            cameraCount: cameraCount,
            crashedLastLaunch: crashedLastLaunch,
            userMessage: userMessage,
            contactEmail: contactEmail,
            logTail: SentinelLog.shared.recentLogText(),
            generatedAt: Date()
        )
        report.kind = kind
        return report
    }
}

// MARK: - Upload

public struct BugReportSubmitter {

    /// The Sentinel download/gateway Worker also accepts bug reports at POST /report.
    public static let endpoint = URL(string: "https://dl.sentvms.com/report")!

    public enum SubmitError: LocalizedError {
        case http(Int)
        case transport(String)

        public var errorDescription: String? {
            switch self {
            case .http(let code): return "Report server returned \(code)."
            case .transport(let m): return m
            }
        }
    }

    let session: URLSession
    public init(session: URLSession = .shared) { self.session = session }

    /// Uploads the report. Returns on 2xx, throws otherwise so the caller can
    /// fall back to an email draft.
    public func submit(_ report: DiagnosticsReport) async throws {
        var request = URLRequest(url: Self.endpoint)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "content-type")
        request.timeoutInterval = 20
        request.httpBody = try JSONEncoder().encode(report)

        let (_, response): (Data, URLResponse)
        do {
            (_, response) = try await session.data(for: request)
        } catch {
            throw SubmitError.transport(error.localizedDescription)
        }
        let code = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200...299).contains(code) else { throw SubmitError.http(code) }
    }
}

// MARK: - Crash capture
//
// A best-effort "did we die last time?" signal. On an uncaught Objective-C
// exception or a fatal POSIX signal we drop a marker file; the next launch reads
// it (then clears it) so the Diagnostics panel can prompt the user to send a
// report. Signal handlers must stay async-signal-safe, so we only do a single
// low-level write of a fixed string — no Swift allocation, no logging.

public enum CrashSentinel {

    private static var markerURL: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
            .appendingPathComponent("Logs", isDirectory: true)
            .appendingPathComponent("crash.marker")
    }

    /// Call once at launch, BEFORE checking `consumeCrashFlag()`.
    public static func install() {
        NSSetUncaughtExceptionHandler { exception in
            SentinelLog.shared.error("Uncaught exception: \(exception.name.rawValue) — \(exception.reason ?? "")", category: "crash")
            CrashSentinel.writeMarker()
        }
        for sig in [SIGILL, SIGABRT, SIGFPE, SIGSEGV, SIGBUS, SIGTRAP] {
            signal(sig) { _ in
                CrashSentinel.writeMarkerSignalSafe()
                signal(SIGABRT, SIG_DFL)
                abort()
            }
        }
    }

    /// Returns true exactly once if the previous run ended in a crash, clearing
    /// the marker so the next launch reads false.
    public static func consumeCrashFlag() -> Bool {
        let url = markerURL
        guard FileManager.default.fileExists(atPath: url.path) else { return false }
        try? FileManager.default.removeItem(at: url)
        SentinelLog.shared.warning("Recovered from a crash on the previous launch.", category: "crash")
        return true
    }

    static func writeMarker() {
        try? Data("crash".utf8).write(to: markerURL, options: .atomic)
    }

    /// Async-signal-safe variant: open + write + close via POSIX only.
    static func writeMarkerSignalSafe() {
        markerURL.withUnsafeFileSystemRepresentation { path in
            guard let path else { return }
            let fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0o644)
            guard fd >= 0 else { return }
            let bytes: [UInt8] = Array("crash".utf8)
            _ = bytes.withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
            close(fd)
        }
    }
}
