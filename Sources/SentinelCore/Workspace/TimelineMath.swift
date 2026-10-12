import Foundation

/// Pure math behind the Operator playback timeline bar: the visible window
/// (always centered on the playhead), zoom limits, ruler ticks, recording
/// gaps and the in/out export selection. No UI types, so it is unit-tested.
public enum TimelineRuler {
    public static let minSpan: Int64 = 60_000            // 1 minute
    public static let maxSpan: Int64 = 86_400_000        // 24 hours

    public static func clampSpan(_ span: Int64) -> Int64 {
        min(max(span, minSpan), maxSpan)
    }

    /// The span after zooming by `factor` (> 1 zooms in, < 1 zooms out).
    public static func zoomed(span: Int64, factor: Double) -> Int64 {
        guard factor.isFinite, factor > 0 else { return clampSpan(span) }
        return clampSpan(Int64((Double(span) / factor).rounded()))
    }

    /// Visible range for a playhead at `center`.
    public static func visibleRange(center: Int64, span: Int64) -> (from: Int64, to: Int64) {
        let span = clampSpan(span)
        let from = center - span / 2
        return (from, from + span)
    }

    /// Time at horizontal position `x` of a ruler `width` points wide.
    public static func time(atX x: Double, width: Double, center: Int64, span: Int64) -> Int64 {
        guard width > 0 else { return center }
        let range = visibleRange(center: center, span: span)
        return range.from + Int64((x / width * Double(clampSpan(span))).rounded())
    }

    /// Horizontal position of `t` on a ruler `width` points wide.
    public static func x(for t: Int64, width: Double, center: Int64, span: Int64) -> Double {
        let range = visibleRange(center: center, span: span)
        return Double(t - range.from) / Double(clampSpan(span)) * width
    }

    /// The new playhead time after dragging the ruler by `dx` points
    /// (dragging right moves back in time).
    public static func panned(center: Int64, dx: Double, width: Double, span: Int64) -> Int64 {
        guard width > 0 else { return center }
        return center - Int64((dx / width * Double(clampSpan(span))).rounded())
    }

    public struct Tick: Equatable {
        public enum Level: Int { case minor = 0, major = 1, day = 2 }
        public var time: Int64
        public var level: Level
        /// Empty for minor ticks.
        public var label: String
    }

    /// Candidate tick steps, from one second to one day.
    public static let steps: [Int64] = [
        1_000, 2_000, 5_000, 10_000, 15_000, 30_000,
        60_000, 120_000, 300_000, 600_000, 900_000, 1_800_000,
        3_600_000, 7_200_000, 10_800_000, 21_600_000, 43_200_000, 86_400_000
    ]

    /// The smallest step giving at most `targetCount` labelled ticks across `span`.
    public static func majorStep(span: Int64, targetCount: Int) -> Int64 {
        let wanted = Double(max(span, 1)) / Double(max(targetCount, 1))
        return steps.first { Double($0) >= wanted } ?? steps[steps.count - 1]
    }

    /// Ticks for `[from, to]`, aligned to local time (`utcOffsetSeconds` for
    /// that time zone). Majors carry a label (HH:mm:ss under a minute of
    /// step, HH:mm otherwise, a date at local midnight); minors subdivide.
    public static func ticks(from: Int64, to: Int64, targetCount: Int = 8, utcOffsetSeconds: Int = 0) -> [Tick] {
        guard to > from else { return [] }
        let major = majorStep(span: to - from, targetCount: targetCount)
        let minor = minorStep(for: major)
        let offset = Int64(utcOffsetSeconds) * 1000
        var result: [Tick] = []
        var t = ((from + offset) / minor) * minor - offset
        if t < from { t += minor }
        while t <= to {
            let local = t + offset
            if local % 86_400_000 == 0 {
                result.append(Tick(time: t, level: .day, label: dateLabel(local)))
            } else if local % major == 0 {
                result.append(Tick(time: t, level: .major, label: timeLabel(local, seconds: major < 60_000)))
            } else {
                result.append(Tick(time: t, level: .minor, label: ""))
            }
            t += minor
        }
        return result
    }

    static func minorStep(for major: Int64) -> Int64 {
        switch major {
        case 1_000: return 1_000
        case 2_000: return 1_000
        case 5_000: return 1_000
        case 10_000: return 5_000
        case 15_000: return 5_000
        case 30_000: return 10_000
        case 60_000: return 10_000
        case 120_000: return 30_000
        case 300_000: return 60_000
        case 600_000: return 120_000
        case 900_000: return 300_000
        case 1_800_000: return 300_000
        case 3_600_000: return 600_000
        case 7_200_000: return 1_800_000
        case 10_800_000: return 3_600_000
        case 21_600_000: return 3_600_000
        case 43_200_000: return 3_600_000
        default: return 10_800_000
        }
    }

    static func timeLabel(_ local: Int64, seconds: Bool) -> String {
        let day = ((local % 86_400_000) + 86_400_000) % 86_400_000
        let h = day / 3_600_000, m = (day / 60_000) % 60, s = (day / 1000) % 60
        let hm = String(format: "%02d:%02d", Int(h), Int(m))
        return seconds ? hm + String(format: ":%02d", Int(s)) : hm
    }

    static func dateLabel(_ local: Int64) -> String {
        let days = Int(floor(Double(local) / 86_400_000))
        // Civil date from days since 1970-01-01 (proleptic Gregorian).
        let z = days + 719_468
        let era = (z >= 0 ? z : z - 146_096) / 146_097
        let doe = z - era * 146_097
        let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365
        let doy = doe - (365 * yoe + yoe / 4 - yoe / 100)
        let mp = (5 * doy + 2) / 153
        let d = doy - (153 * mp + 2) / 5 + 1
        let m = mp < 10 ? mp + 3 : mp - 9
        let y = yoe + era * 400 + (m <= 2 ? 1 : 0)
        return String(format: "%04d-%02d-%02d", y, m, d)
    }
}

/// Recording spans, merged, and the gaps between them.
public enum TimelineGaps {
    public struct Range: Equatable, Hashable {
        public var start: Int64
        public var end: Int64
        public init(start: Int64, end: Int64) {
            self.start = start
            self.end = end
        }
    }

    /// Sorted, with spans that overlap or touch (within `tolerance` ms) joined.
    public static func merge(_ spans: [Range], tolerance: Int64 = 1000) -> [Range] {
        var merged: [Range] = []
        for span in spans.filter({ $0.end > $0.start }).sorted(by: { $0.start < $1.start }) {
            if var last = merged.last, span.start <= last.end + tolerance {
                last.end = max(last.end, span.end)
                merged[merged.count - 1] = last
            } else {
                merged.append(span)
            }
        }
        return merged
    }

    /// The gaps inside `bounds`: before the first span, between spans and
    /// after the last. With no spans the whole of `bounds` is one gap.
    public static func gaps(in spans: [Range], bounds: Range, tolerance: Int64 = 1000) -> [Range] {
        guard bounds.end > bounds.start else { return [] }
        var result: [Range] = []
        var cursor = bounds.start
        for span in merge(spans, tolerance: tolerance) {
            if span.end <= bounds.start { continue }
            if span.start >= bounds.end { break }
            if span.start - cursor > tolerance { result.append(Range(start: cursor, end: span.start)) }
            cursor = max(cursor, span.end)
        }
        if bounds.end - cursor > tolerance { result.append(Range(start: cursor, end: bounds.end)) }
        return result
    }

    /// "Next gap »|": the start of the first recording after `t` — skips
    /// forward over the gap that follows the current footage, landing on
    /// video (matches the Linux Operator).
    public static func nextGap(after t: Int64, spans: [Range], bounds: Range) -> Int64? {
        merge(spans).first { $0.start > t }?.start
    }

    /// "|« Prev gap": just before the end of the footage preceding `t` —
    /// skips back over the gap onto the previous recording's tail, landing
    /// on video (matches the Linux Operator).
    public static func previousGap(before t: Int64, spans: [Range], bounds: Range, lead: Int64 = 1000) -> Int64? {
        var best: Int64?
        for s in merge(spans) where s.end <= t - lead && !(t >= s.start && t < s.end) {
            best = max(best ?? Int64.min, s.end - lead)
        }
        return best
    }
}

/// The in/out range marked on the ruler for export. `[` sets the in point,
/// `]` the out point; the two never cross.
public struct ExportSelection: Equatable {
    public private(set) var markIn: Int64?
    public private(set) var markOut: Int64?

    public init(markIn: Int64? = nil, markOut: Int64? = nil) {
        self.markIn = markIn
        self.markOut = markOut
        normalize(keepingIn: true)
    }

    /// Both points set and in < out.
    public var range: (from: Int64, to: Int64)? {
        guard let markIn, let markOut, markOut > markIn else { return nil }
        return (markIn, markOut)
    }

    public var isEmpty: Bool { markIn == nil && markOut == nil }

    public mutating func setIn(_ t: Int64) {
        markIn = t
        normalize(keepingIn: true)
    }

    public mutating func setOut(_ t: Int64) {
        markOut = t
        normalize(keepingIn: false)
    }

    public mutating func clear() {
        markIn = nil
        markOut = nil
    }

    /// The point just set wins; the other is dropped if it would cross.
    private mutating func normalize(keepingIn: Bool) {
        guard let i = markIn, let o = markOut, o <= i else { return }
        if keepingIn { markOut = nil } else { markIn = nil }
    }
}
