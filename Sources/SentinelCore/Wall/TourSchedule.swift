import Foundation

/// The clock of a monitor's tour: which of `count` views is up at a given
/// time, when the next change is due, and whether the view's name caption
/// is still showing. Pure time math; the window drives it with real dates.
///
/// - Each view stays up `seconds`, then the next comes, wrapping around.
/// - Pausing (pointer over the window, a click) freezes the current view;
///   resuming gives it a fresh full interval, so an operator who just looked
///   at something isn't cut off a moment later.
/// - The caption shows for `captionDuration` after each view comes up.
public struct TourSchedule: Equatable, Sendable {
    public static let captionDuration: TimeInterval = 3

    public let count: Int
    public let seconds: TimeInterval
    /// When view 0 came up for the first time (shifted by pauses and skips).
    public private(set) var anchor: Date
    public private(set) var pausedAt: Date?

    public init(count: Int, seconds: TimeInterval, start: Date, startIndex: Int = 0) {
        self.count = max(count, 0)
        self.seconds = max(seconds, 1)
        let first = self.count > 0 ? ((startIndex % self.count) + self.count) % self.count : 0
        self.anchor = start.addingTimeInterval(-Double(first) * self.seconds)
    }

    public var isPaused: Bool { pausedAt != nil }

    /// Completed intervals since the anchor at `date` (frozen while paused).
    private func step(at date: Date) -> Int {
        let effective = pausedAt.map { min($0, date) } ?? date
        let elapsed = max(effective.timeIntervalSince(anchor), 0)
        return Int((elapsed / seconds).rounded(.down))
    }

    /// The view up at `date` (0 when there are none).
    public func index(at date: Date) -> Int {
        guard count > 0 else { return 0 }
        return step(at: date) % count
    }

    /// When the view up at `date` came up.
    public func viewStart(at date: Date) -> Date {
        anchor.addingTimeInterval(Double(step(at: date)) * seconds)
    }

    /// The next time the view changes after `date`; nil while paused or
    /// with fewer than two views.
    public func nextChange(after date: Date) -> Date? {
        guard count > 1, pausedAt == nil else { return nil }
        return viewStart(at: date).addingTimeInterval(seconds)
    }

    /// Whether the view's name caption is showing at `date`.
    public func captionVisible(at date: Date) -> Bool {
        guard count > 0 else { return false }
        return date.timeIntervalSince(viewStart(at: date)) < Self.captionDuration
    }

    /// When the caption at `date` goes away; nil when it isn't showing.
    public func captionEnds(after date: Date) -> Date? {
        guard captionVisible(at: date) else { return nil }
        return viewStart(at: date).addingTimeInterval(Self.captionDuration)
    }

    /// The next moment the window needs to redraw after `date`: the caption
    /// hiding or the view changing, whichever comes first.
    public func nextEvent(after date: Date) -> Date? {
        [captionEnds(after: date), nextChange(after: date)].compactMap { $0 }.min()
    }

    public mutating func pause(at date: Date) {
        guard pausedAt == nil else { return }
        pausedAt = date
    }

    /// Resumes on the same view with a fresh full interval (and caption).
    public mutating func resume(at date: Date) {
        guard let paused = pausedAt else { return }
        let current = step(at: paused)
        pausedAt = nil
        anchor = date.addingTimeInterval(-Double(current) * seconds)
    }

    /// Moves `by` views (negative = back), the new view getting a full
    /// interval. Keeps a pause in place.
    public mutating func skip(by offset: Int, at date: Date) {
        guard count > 0 else { return }
        let target = ((index(at: date) + offset) % count + count) % count
        anchor = date.addingTimeInterval(-Double(target) * seconds)
        if pausedAt != nil { pausedAt = date }
    }
}
