import Foundation

/// Pure logic behind "Recover from camera" (Wave 11, stream A; the web's
/// `edgeRecovery.ts`): when the button shows, what range it fetches, the
/// recovered stretches the timeline hatches, and the job's texts. Times are
/// Unix ms.
public enum EdgeRecoveryLogic {
    public typealias Range = TimelineGaps.Range

    /// Longest range one recovery may cover (the server's MaxEdgeRecoveryMs).
    public static let maxRangeMs: Int64 = 24 * 3_600_000
    /// Holes shorter than this are keyframe slack, not gaps (the server's edgeMinGap).
    public static let minGapMs: Int64 = 10_000
    /// How often a running job is read while the timeline is on screen.
    public static let pollSeconds: UInt64 = 2

    /// The stretches of recovered footage (spans flagged `edge`), merged.
    public static func edgeRanges(_ spans: [V2.Span]) -> [Range] {
        TimelineGaps.merge(spans.filter { $0.edge == true }.map { Range(start: $0.start, end: $0.end) })
    }

    /// How much of `r` no span covers.
    public static func uncoveredMs(_ spans: [Range], _ r: Range) -> Int64 {
        var covered: Int64 = 0
        for s in TimelineGaps.merge(spans, tolerance: 0) {
            let a = max(s.start, r.start), b = min(s.end, r.end)
            if b > a { covered += b - a }
        }
        return max(0, r.end - r.start - covered)
    }

    /// Recording gaps of at least `minGapMs` in `window`, not past `now`.
    public static func gaps(_ spans: [Range], window: Range, now: Int64) -> [Range] {
        let bounds = Range(start: window.start, end: min(window.end, now))
        guard bounds.end > bounds.start else { return [] }
        return TimelineGaps.gaps(in: spans, bounds: bounds).filter { $0.end - $0.start >= minGapMs }
    }

    /// The default range the sheet offers: the marked in/out selection when
    /// part of it is unrecorded, else the visible window. Clamped to now and
    /// to 24 h (keeping the start). Nil when nothing in it is unrecorded.
    public static func target(selection: Range?, window: Range, spans: [Range], now: Int64) -> Range? {
        func clamp(_ r: Range) -> Range? {
            let end = min(r.end, now, r.start + maxRangeMs)
            guard end > r.start else { return nil }
            return Range(start: r.start, end: end)
        }
        if let sel = selection.flatMap(clamp), uncoveredMs(spans, sel) >= minGapMs { return sel }
        guard let win = clamp(window), gaps(spans, window: win, now: now).isEmpty == false else { return nil }
        return win
    }

    /// Whether the timeline bar offers "Recover from camera": the camera
    /// keeps its own recordings, the user may export, and the visible window
    /// has a gap.
    public static func offers(edgeRecording: Bool, canExport: Bool, spans: [Range], window: Range, now: Int64) -> Bool {
        edgeRecording && canExport && gaps(spans, window: window, now: now).isEmpty == false
    }

    /// Why a range can't be started, or nil.
    public static func problem(_ r: Range, now: Int64) -> String? {
        if r.end <= r.start { return "The end must be after the start." }
        if r.start >= now { return "That range is in the future." }
        if r.end - r.start > maxRangeMs { return "A recovery can cover at most 24 hours; choose a shorter range." }
        return nil
    }

    // MARK: Job

    public static func isActive(_ job: V2.EdgeRecovery?) -> Bool { job?.state == .running }

    /// "1.5 h" / "25 min" / "40 s" of footage.
    static func footage(_ s: Double) -> String {
        if s >= 3600 { return "\(trim((s / 3600 * 10).rounded() / 10)) h" }
        if s >= 60 { return "\(Int((s / 60).rounded())) min" }
        return "\(Int(s.rounded())) s"
    }

    private static func trim(_ v: Double) -> String {
        v == v.rounded() ? String(Int(v)) : String(v)
    }

    /// One line for a job's state (the web's wording).
    public static func text(_ j: V2.EdgeRecovery) -> String {
        switch j.state {
        case .running:
            return j.pieces > 0 ? "Recovering from camera… \(j.progress)%" : "Asking the camera what it recorded…"
        case .done:
            if j.gaps == 0 { return "No gaps in that range: nothing to recover." }
            if j.recovered == 0 { return "The camera had no footage for those gaps." }
            let note = (j.error ?? "").isEmpty ? "" : " \(j.error!)"
            return "Recovered \(footage(j.seconds)) from the camera (\(j.recovered) segment\(j.recovered == 1 ? "" : "s")).\(note)"
        case .canceled:
            return "Recovery canceled."
        default:
            return "Recovery failed: \((j.error ?? "").isEmpty ? "unknown error" : j.error!)"
        }
    }

    /// The notice kind a finished job posts.
    public static func noticeKind(_ j: V2.EdgeRecovery) -> NoticeKind {
        if j.state == .failed { return .error }
        return j.state == .done && j.recovered > 0 ? .success : .info
    }

    /// Whether the timelines should be re-read (footage came back).
    public static func shouldReload(_ j: V2.EdgeRecovery) -> Bool {
        (j.state == .done || j.state == .canceled) && j.recovered > 0
    }

    /// The progress counters, for the sheet.
    public static func counters(_ j: V2.EdgeRecovery) -> [(label: String, value: String)] {
        [("Gaps", "\(j.gaps)"), ("Pieces", "\(j.pieces)"), ("Recovered", "\(j.recovered)"),
         ("Skipped", "\(j.skipped)"), ("Footage", footage(j.seconds))]
    }

    /// What a failed start says: 400 / 409 / 429 in the server's (and the
    /// web's) words, the server's own message when it sent one.
    public static func startError(_ error: Error) -> String {
        guard let api = error as? SentinelAPIError, case let .server(status, _, message) = api else {
            return error.localizedDescription
        }
        let generic = message.isEmpty || message == SentinelAPIError.defaultMessage(for: status)
        func server(_ fallback: String) -> String { generic ? fallback : sentence(message) }
        switch status {
        case 400: return server("The camera can't be searched for that range. A recovery covers at most 24 hours and needs the camera's ONVIF address.")
        case 403: return "Your role can't recover footage from cameras."
        case 409: return server("The camera was on another recording server during part of that range; recover a range after it moved.")
        case 429: return server("A recovery is already running for this camera.")
        case 503: return server("The camera's recording server isn't reachable right now.")
        default: return sentence(message)
        }
    }

    /// The server's lower-case message as a sentence.
    static func sentence(_ s: String) -> String {
        guard let first = s.first else { return s }
        let body = first.uppercased() + s.dropFirst()
        return body.hasSuffix(".") ? body : body + "."
    }

    /// The button's tooltip: what will be recovered.
    public static func title(_ target: Range?) -> String {
        guard let target else { return "Mark in/out around a gap, or scroll the timeline to one, to recover it from the camera's SD card." }
        let min = max(1, Int((Double(target.end - target.start) / 60_000).rounded()))
        let size = min >= 60 ? "\(trim((Double(min) / 60 * 10).rounded() / 10)) h" : "\(min) min"
        return "Pull the camera's own SD-card footage into the gaps of this \(size) range."
    }
}
