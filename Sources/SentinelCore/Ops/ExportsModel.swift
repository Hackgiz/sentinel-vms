import Foundation

// Pure logic for the Exports workspace (port of web `exportsModel.ts`), so
// validation, batching, labels and the SHA copy state are testable.

/// A camera and range handed from Playback to the Exports workspace
/// ("Lock as Evidence…"). The coordinator passes it to
/// `ExportsWorkspaceView(prefill:)`.
public struct ExportPrefill: Hashable, Sendable {
    public var cameraID: String
    public var from: UnixMillis
    public var to: UnixMillis

    public init(cameraID: String, from: UnixMillis, to: UnixMillis) {
        self.cameraID = cameraID
        self.from = from
        self.to = to
    }
}

public enum ExportRules {
    /// Longest range: the server's own clip limit (30 minutes).
    public static let maxRangeMs: Int64 = 30 * 60_000
    public static let titleLimit = 120
    public static let noteLimit = 500
    public static let defaultTitle = "Clip Export"

    /// Why the form can't be submitted, or nil when it can.
    public static func blocker(cameraIDs: [String], from: UnixMillis, to: UnixMillis, now: UnixMillis = Date().unixMillis) -> String? {
        if cameraIDs.isEmpty { return "Pick at least one camera." }
        if from <= 0 || to <= 0 { return "Pick a start and an end." }
        if to <= from { return "The end must be after the start." }
        if from > now { return "The start is in the future." }
        if to - from > maxRangeMs { return "Exports are limited to \(maxRangeMs / 60_000) minutes." }
        return nil
    }

    /// Cameras the operator may pick: only those the session can see, in
    /// the order given, without duplicates.
    public static func limit(_ ids: [String], toVisible visible: [String]) -> [String] {
        let allowed = Set(visible)
        var seen = Set<String>()
        return ids.filter { allowed.contains($0) && seen.insert($0).inserted }
    }

    /// Shared label for a multi-camera selection ("" for a single camera).
    public static func batchLabel(count: Int, at date: Date, calendar: Calendar = .current) -> String {
        guard count >= 2 else { return "" }
        let c = calendar.dateComponents([.year, .month, .day, .hour, .minute], from: date)
        return String(format: "Batch %04d-%02d-%02d %02d:%02d (%d cameras)",
                      c.year ?? 0, c.month ?? 0, c.day ?? 0, c.hour ?? 0, c.minute ?? 0, count)
    }

    /// The note stored on each case: the batch label, then the operator's words.
    public static func composeNote(label: String, note: String) -> String {
        [label, note.trimmingCharacters(in: .whitespacesAndNewlines)].filter { $0.isEmpty == false }.joined(separator: " · ")
    }

    public static func title(_ text: String) -> String {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return String((trimmed.isEmpty ? defaultTitle : trimmed).prefix(titleLimit))
    }

    /// What the server needs to say when it refuses: its own message
    /// (e.g. the over-30-minute 400) verbatim, otherwise the error's text.
    public static func failureText(_ error: Error) -> String {
        if let api = error as? SentinelAPIError, case .server(_, _, let message) = api, message.isEmpty == false {
            return message
        }
        return error.localizedDescription
    }

    public static func summary(made: Int, failed: Int) -> String {
        var parts: [String] = []
        if made > 0 { parts.append("Locked \(made) clip\(made == 1 ? "" : "s") as evidence.") }
        if failed > 0 { parts.append("\(failed) export\(failed == 1 ? "" : "s") failed.") }
        return parts.joined(separator: " ")
    }
}

/// Progress of a sequential batch: "Locking Front Door (2 of 3)…".
public struct ExportProgress: Equatable, Sendable {
    public var index: Int
    public var total: Int
    public var camera: String

    public var text: String { "Locking \(camera) (\(index) of \(total))…" }
}

public struct ExportBatchOutcome: Equatable, Sendable {
    public var made: Int
    public var failures: [String]
    public var summary: String { ExportRules.summary(made: made, failed: failures.count) }
}

public enum ExportBatch {
    /// Runs `create` for each camera one after another (the server cuts one
    /// clip per request), reporting progress first. A failure is recorded
    /// and the batch carries on with the next camera.
    public static func run(cameraIDs: [String], name: (String) -> String,
                           progress: (ExportProgress) -> Void,
                           create: (String) async throws -> Void) async -> ExportBatchOutcome {
        var made = 0
        var failures: [String] = []
        for (offset, id) in cameraIDs.enumerated() {
            if Task.isCancelled { break }
            progress(ExportProgress(index: offset + 1, total: cameraIDs.count, camera: name(id)))
            do {
                try await create(id)
                made += 1
            } catch {
                failures.append("\(name(id)): \(ExportRules.failureText(error))")
            }
        }
        return ExportBatchOutcome(made: made, failures: failures)
    }
}

/// Display helpers for the case table.
public enum EvidenceDisplay {
    /// 1024-based, like the web client ("12.3 MB").
    public static func bytes(_ n: Int64) -> String {
        if n < 1024 { return "\(n) B" }
        let units = ["KB", "MB", "GB", "TB"]
        var v = Double(n) / 1024
        var i = 0
        while v >= 1024, i < units.count - 1 { v /= 1024; i += 1 }
        return v >= 100 ? "\(Int(v.rounded())) \(units[i])" : String(format: "%.1f %@", v, units[i])
    }

    /// First 16 hex digits and an ellipsis; the full hash is the tooltip/copy value.
    public static func shortSHA(_ sha: String) -> String {
        sha.count > 16 ? String(sha.prefix(16)) + "…" : sha
    }

    /// Exact range when recorded, otherwise the server's label (alarm locks).
    public static func rangeText(_ c: V2.EvidenceCase, formatter: (UnixMillis) -> String = defaultFormat,
                                 timeFormatter: (UnixMillis) -> String = defaultTime) -> String {
        c.fromMs > 0 && c.toMs > 0 ? "\(formatter(c.fromMs)) – \(timeFormatter(c.toMs))" : c.range
    }

    public static func defaultFormat(_ ms: UnixMillis) -> String {
        Date(unixMillis: ms).formatted(date: .abbreviated, time: .standard)
    }

    public static func defaultTime(_ ms: UnixMillis) -> String {
        Date(unixMillis: ms).formatted(date: .omitted, time: .standard)
    }
}

/// Which case's SHA was just copied ("Copied" for a moment), and what goes
/// on the pasteboard (always the full hash).
public struct SHACopyModel: Equatable, Sendable {
    public private(set) var copiedID: String?

    public init() {}

    public func text(for c: V2.EvidenceCase) -> String { c.sha256 }

    public func label(for id: String) -> String { copiedID == id ? "Copied" : "Copy" }

    public mutating func markCopied(_ id: String) { copiedID = id }

    /// Clears the flag unless a newer copy replaced it.
    public mutating func reset(_ id: String) { if copiedID == id { copiedID = nil } }
}

/// Inline verification result per case.
public enum VerifyState: Equatable, Sendable {
    case running
    case ok
    case failed(String)

    public static func from(_ result: V2.EvidenceVerification) -> VerifyState {
        result.ok ? .ok : .failed(result.message ?? "The file does not match its recorded SHA-256, or is missing.")
    }
}
