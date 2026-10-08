import Foundation
#if canImport(os)
import os
#endif

// MARK: - Sentinel activity log
//
// Sentinel is free and still under active development, so when an operator hits a
// bug we need a trail to look at. `SentinelLog` is a lightweight, app-wide logger
// that does three things:
//
//   1. Keeps an in-memory ring buffer (the most recent `maxEntries`) that the
//      Diagnostics panel renders live.
//   2. Appends every entry to a rotating file at
//      `~/Library/Application Support/HandoffGridSentinel/Logs/sentinel.log`
//      (rolls to `sentinel.1.log` past `maxFileBytes`), so a report can include
//      context from before the panel was opened — even across a crash.
//   3. Redacts obvious secrets (RTSP passwords, API keys, bearer tokens) on the
//      way in, so neither the on-disk log nor an uploaded bug report leaks
//      credentials.
//
// It is safe to call from any thread/actor: writes are funneled through a private
// serial queue and the `@Published` buffer is updated on the main queue.
public final class SentinelLog: ObservableObject {

    public static let shared = SentinelLog()

    public enum Level: String, Codable, CaseIterable, Sendable {
        case debug, info, warning, error

        public var rank: Int {
            switch self {
            case .debug: return 0
            case .info: return 1
            case .warning: return 2
            case .error: return 3
            }
        }

        public var symbol: String {
            switch self {
            case .debug: return "ant"
            case .info: return "info.circle"
            case .warning: return "exclamationmark.triangle"
            case .error: return "xmark.octagon"
            }
        }
    }

    public struct Entry: Identifiable, Codable, Sendable {
        public let id: UUID
        public let date: Date
        public let level: Level
        public let category: String
        public let message: String

        public init(id: UUID = UUID(), date: Date, level: Level, category: String, message: String) {
            self.id = id
            self.date = date
            self.level = level
            self.category = category
            self.message = message
        }
    }

    /// Newest-last list of recent entries for live display.
    @Published public private(set) var entries: [Entry] = []

    /// Set false to capture info/debug too; default keeps the buffer focused.
    /// The on-disk file always records everything at `minimumLevel` or above.
    public var minimumLevel: Level = .info

    private let maxEntries = 1000
    private let maxFileBytes = 2 * 1024 * 1024  // 2 MB before rotation
    private let queue = DispatchQueue(label: "com.handoffgrid.sentinel.log", qos: .utility)
    private let fileURL: URL
    private let rotatedURL: URL
    private let isoFormatter: ISO8601DateFormatter

    #if canImport(os)
    private let osLog = os.Logger(subsystem: "com.handoffgrid.sentinel", category: "app")
    #endif

    private init() {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
            .appendingPathComponent("Logs", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        self.fileURL = dir.appendingPathComponent("sentinel.log")
        self.rotatedURL = dir.appendingPathComponent("sentinel.1.log")

        let fmt = ISO8601DateFormatter()
        fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        self.isoFormatter = fmt
    }

    public var logFileURL: URL { fileURL }
    public var logsDirectory: URL { fileURL.deletingLastPathComponent() }

    // MARK: Logging entry points

    public func debug(_ message: String, category: String = "general")   { log(.debug, message, category) }
    public func info(_ message: String, category: String = "general")     { log(.info, message, category) }
    public func warning(_ message: String, category: String = "general")  { log(.warning, message, category) }
    public func error(_ message: String, category: String = "general")    { log(.error, message, category) }

    /// Convenience for the many `catch` sites: logs a thrown error with context.
    public func error(_ error: Error, context: String, category: String = "general") {
        log(.error, "\(context): \(error.localizedDescription)", category)
    }

    public func log(_ level: Level, _ message: String, _ category: String = "general") {
        guard level.rank >= minimumLevel.rank else { return }
        let entry = Entry(date: Date(), level: level, category: category, message: Self.redact(message))

        #if canImport(os)
        switch level {
        case .debug:   osLog.debug("[\(category, privacy: .public)] \(entry.message, privacy: .public)")
        case .info:    osLog.info("[\(category, privacy: .public)] \(entry.message, privacy: .public)")
        case .warning: osLog.warning("[\(category, privacy: .public)] \(entry.message, privacy: .public)")
        case .error:   osLog.error("[\(category, privacy: .public)] \(entry.message, privacy: .public)")
        }
        #endif

        queue.async { [weak self] in self?.appendToFile(entry) }

        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.entries.append(entry)
            if self.entries.count > self.maxEntries {
                self.entries.removeFirst(self.entries.count - self.maxEntries)
            }
        }
    }

    // MARK: File handling (serial queue only)

    private func appendToFile(_ entry: Entry) {
        let line = "\(isoFormatter.string(from: entry.date)) [\(entry.level.rawValue.uppercased())] [\(entry.category)] \(entry.message)\n"
        guard let data = line.data(using: .utf8) else { return }

        rotateIfNeeded()

        if let handle = try? FileHandle(forWritingTo: fileURL) {
            defer { try? handle.close() }
            _ = try? handle.seekToEnd()
            try? handle.write(contentsOf: data)
        } else {
            try? data.write(to: fileURL, options: .atomic)
        }
    }

    private func rotateIfNeeded() {
        let attrs = try? FileManager.default.attributesOfItem(atPath: fileURL.path)
        let size = (attrs?[.size] as? NSNumber)?.int64Value ?? 0
        guard size >= maxFileBytes else { return }
        try? FileManager.default.removeItem(at: rotatedURL)
        try? FileManager.default.moveItem(at: fileURL, to: rotatedURL)
    }

    /// Returns the tail of the on-disk log (rotated + current), newest content
    /// last, capped to `maxBytes` so a bug report stays small.
    public func recentLogText(maxBytes: Int = 256 * 1024) -> String {
        queue.sync {
            var combined = ""
            if let rotated = try? String(contentsOf: rotatedURL, encoding: .utf8) { combined += rotated }
            if let current = try? String(contentsOf: fileURL, encoding: .utf8) { combined += current }
            if combined.utf8.count <= maxBytes { return combined }
            let tail = String(combined.suffix(maxBytes))
            // Drop a partial first line so the report starts cleanly.
            if let nl = tail.firstIndex(of: "\n") {
                return String(tail[tail.index(after: nl)...])
            }
            return tail
        }
    }

    // MARK: Secret redaction

    private static let redactors: [(NSRegularExpression, String)] = {
        let patterns: [(String, String)] = [
            // rtsp://user:password@host  ->  rtsp://user:***@host
            ("(rtsps?://[^:/@\\s]+:)[^@\\s]+(@)", "$1***$2"),
            // Anthropic / generic api keys
            ("sk-ant-[A-Za-z0-9_\\-]+", "sk-ant-***"),
            ("(api[_-]?key\\\"?\\s*[:=]\\s*\\\"?)[A-Za-z0-9_\\-]{8,}", "$1***"),
            // Bearer tokens
            ("(?i)(bearer\\s+)[A-Za-z0-9._\\-]{8,}", "$1***"),
            // password=... / "password":"..."
            ("(?i)(password\\\"?\\s*[:=]\\s*\\\"?)[^\\s\\\"&]+", "$1***"),
        ]
        return patterns.compactMap { pattern, template in
            (try? NSRegularExpression(pattern: pattern)).map { ($0, template) }
        }
    }()

    public static func redact(_ text: String) -> String {
        var result = text
        for (regex, template) in redactors {
            let range = NSRange(result.startIndex..., in: result)
            result = regex.stringByReplacingMatches(in: result, range: range, withTemplate: template)
        }
        return result
    }
}
