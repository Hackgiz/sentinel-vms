import Foundation

// Pure logic for the long (server-side, ≤ 12 h) export panel (port of web
// `longExportModel.ts`): range validation, labels and the running-jobs list.

public enum LongExportRules {
    /// Longest long export, as the server's playback.MaxLongExport.
    public static let maxRangeMs: Int64 = 12 * 3_600_000
    /// Progress poll while a job runs and the panel is on screen.
    public static let pollSeconds: UInt64 = 2
    public static let titleLimit = 120
    public static let noteLimit = 500

    /// Why a long export can't be started, or nil when it can.
    public static func blocker(cameraID: String?, from: UnixMillis, to: UnixMillis, now: UnixMillis = Date().unixMillis) -> String? {
        guard let cameraID, cameraID.isEmpty == false else { return "Pick a camera." }
        if from <= 0 || to <= 0 { return "Pick a start and an end." }
        if to <= from { return "The end must be after the start." }
        if from > now { return "The start is in the future." }
        if to - from > maxRangeMs { return "Long exports are limited to \(maxRangeMs / 3_600_000) hours." }
        return nil
    }

    /// "5 h 30 min", "45 min": how much footage the range spans.
    public static func spanText(_ ms: Int64) -> String {
        let minutes = max(0, Int((Double(ms) / 60_000).rounded()))
        let h = minutes / 60
        let m = minutes % 60
        if h == 0 { return "\(m) min" }
        return m == 0 ? "\(h) h" : "\(h) h \(m) min"
    }

    /// The POST body. Title/note only travel to an evidence destination;
    /// empty strings are dropped and both are clipped to the server's limits.
    public static func request(cameraID: String, from: UnixMillis, to: UnixMillis, dest: V2.ExportDest,
                               title: String, note: String, options: ExportOptions) -> V2.ExportJobRequest {
        func clip(_ s: String, _ limit: Int) -> String? {
            let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
            return t.isEmpty ? nil : String(t.prefix(limit))
        }
        let toVault = dest == .evidence
        return V2.ExportJobRequest(
            cameraId: cameraID, fromMs: from, toMs: to, dest: dest,
            title: toVault ? clip(title, titleLimit) : nil,
            note: toVault ? clip(note, noteLimit) : nil,
            watermark: options.watermarkField, blurRegions: options.regionsField)
    }

    /// Whether a job is still being worked on (and so should be polled).
    public static func isActive(_ job: V2.ExportJob?) -> Bool { job?.state == .running }

    /// One line for a job's state.
    public static func jobText(_ job: V2.ExportJob) -> String {
        switch job.state {
        case .running:
            return "Exporting… \(job.progress)%"
        case .done:
            if job.dest == .evidence {
                return "Filed as evidence\(job.caseId.map { $0.isEmpty ? "" : " \($0)" } ?? "")."
            }
            return "Ready to download."
        case .canceled:
            return "Canceled."
        default:
            return "Failed: \(job.error.flatMap { $0.isEmpty ? nil : $0 } ?? "unknown error")"
        }
    }

    /// Whether the job has a file to save (done, download destination).
    public static func canSave(_ job: V2.ExportJob) -> Bool { job.state == .done && job.dest == .download }

    /// The Save panel's suggested name: the server's filename, else one from the camera and start.
    public static func suggestedFilename(_ job: V2.ExportJob, cameraName: String) -> String {
        if let name = job.filename?.trimmingCharacters(in: .whitespacesAndNewlines), name.isEmpty == false {
            return FileNames.safe(name)
        }
        let base = FileNames.safe(cameraName.isEmpty ? "export" : cameraName)
        return "\(base)-\(job.fromMs).mp4"
    }
}

/// The long-export jobs this Operator started in this session, newest first.
public struct ExportJobList: Equatable, Sendable {
    public private(set) var jobs: [V2.ExportJob] = []
    /// Most jobs kept (the server keeps a finished job for an hour anyway).
    public static let limit = 20

    public init(jobs: [V2.ExportJob] = []) {
        self.jobs = Array(jobs.prefix(Self.limit))
    }

    /// Adds a job or replaces the one with the same task id (keeping its place).
    public mutating func upsert(_ job: V2.ExportJob) {
        if let index = jobs.firstIndex(where: { $0.taskId == job.taskId }) {
            jobs[index] = job
        } else {
            jobs.insert(job, at: 0)
            if jobs.count > Self.limit { jobs.removeLast(jobs.count - Self.limit) }
        }
    }

    public mutating func remove(taskID: String) {
        jobs.removeAll { $0.taskId == taskID }
    }

    /// Drops finished jobs (the "Clear finished" button).
    public mutating func clearFinished() {
        jobs.removeAll { LongExportRules.isActive($0) == false }
    }

    public var running: [V2.ExportJob] { jobs.filter(LongExportRules.isActive) }
    public var hasRunning: Bool { jobs.contains(where: LongExportRules.isActive) }

    /// Jobs that were running in `old` and are not any more in `self`: what to announce.
    public func finished(since old: ExportJobList) -> [V2.ExportJob] {
        let wasRunning = Set(old.running.map(\.taskId))
        return jobs.filter { wasRunning.contains($0.taskId) && LongExportRules.isActive($0) == false }
    }
}

/// Save-panel names.
public enum FileNames {
    /// Replaces path separators and colons, so a server or case name can't
    /// point the Save panel at another folder.
    public static func safe(_ name: String) -> String {
        let cleaned = name.map { "/:\\".contains($0) ? "-" : $0 }
        let s = String(cleaned).trimmingCharacters(in: .whitespacesAndNewlines)
        return s.isEmpty ? "export" : s
    }
}
