import Foundation

/// Keeps overlapping loads honest: only the latest load may publish its result
/// or clear the loading flag (wave 24). A cancelled or superseded load that
/// finishes late changes nothing.
public struct LatestLoad: Equatable, Sendable {
    private var serial = 0
    private var active = 0

    public init() {}

    /// True while the latest load has not finished.
    public var isLoading: Bool { active == serial && serial != 0 && finished == false }
    private var finished = false

    /// Starts a load and returns its ticket; older tickets stop being latest.
    public mutating func begin() -> Int {
        serial += 1
        active = serial
        finished = false
        return serial
    }

    public func isLatest(_ ticket: Int) -> Bool { ticket == serial }

    /// Ends `ticket`. Returns true (and clears `isLoading`) only when it is the latest.
    @discardableResult
    public mutating func finish(_ ticket: Int) -> Bool {
        guard ticket == serial else { return false }
        finished = true
        return true
    }
}
