import Foundation

/// The slice of recorded video a Playback session downloads as a playlist (wave 24, D2).
///
/// A whole-day playlist is 6-9 MB per camera, so playback asks for the playhead
/// plus and minus one hour, clamped to now. The ruler keeps its 24 h timeline
/// for drawing, but the media offsets used to play come from this window.
public struct PlaybackWindow: Equatable, Sendable {
    /// Milliseconds either side of the playhead (1 h).
    public static let halfSpan: Int64 = 3_600_000
    /// Fetch the next window once the playhead is this close to an end (10 min).
    public static let edgeMargin: Int64 = 600_000
    /// An end only counts as "moved" when the next window gains at least this much.
    public static let minGrowth: Int64 = 600_000

    public let from: UnixMillis
    public let to: UnixMillis

    public init(from: UnixMillis, to: UnixMillis) {
        self.from = from
        self.to = to
    }

    /// Playhead - 1 h ... playhead + 1 h, never later than `now` and never before 0.
    public static func around(_ playhead: UnixMillis, now: UnixMillis) -> PlaybackWindow {
        let head = min(max(playhead, 0), now)
        let to = min(head + halfSpan, now)
        return PlaybackWindow(from: max(head - halfSpan, 0), to: max(to, head))
    }

    /// True when `t` is inside the window.
    public func contains(_ t: UnixMillis) -> Bool { t >= from && t <= to }

    /// True when the playhead needs another window: it is outside this one,
    /// or within 10 minutes of an end that the next window would move out
    /// (an end clamped to "now" only moves when time has passed).
    public func needsRefetch(playhead: UnixMillis, now: UnixMillis) -> Bool {
        let head = min(max(playhead, 0), now)
        if contains(head) == false { return true }
        let next = Self.around(head, now: now)
        if head - from < Self.edgeMargin, next.from <= from - Self.minGrowth { return true }
        if to - head < Self.edgeMargin, next.to >= to + Self.minGrowth { return true }
        return false
    }
}
