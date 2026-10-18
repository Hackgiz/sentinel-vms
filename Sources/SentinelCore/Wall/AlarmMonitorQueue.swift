import Foundation

/// The alarms an alarm monitor shows: the newest full-tile, the rest in a
/// small queue beside it. Fed by the live event stream (`alarm.new` /
/// `alarm.updated`), never by polling.
///
/// - Only a *new* alarm on a camera joins (`isNew`, state New, has a camera).
/// - Newest first by creation time; past `capacity` the oldest drop off.
/// - An update keeps an alarm in place; once it leaves New (acknowledged,
///   closed — here or anywhere) it leaves the monitor.
/// - Dismissing removes it here only; the alarm itself stays as it is.
public struct AlarmMonitorQueue: Equatable, Sendable {
    public static let defaultCapacity = 8

    public let capacity: Int
    public private(set) var items: [V2.Alarm] = []

    public init(capacity: Int = AlarmMonitorQueue.defaultCapacity) {
        self.capacity = max(capacity, 1)
    }

    /// The alarm shown full-tile.
    public var current: V2.Alarm? { items.first }
    /// The ones waiting behind it, newest first.
    public var queued: [V2.Alarm] { Array(items.dropFirst()) }
    public var isEmpty: Bool { items.isEmpty }

    /// Applies a stream message; returns whether the shown alarm changed.
    @discardableResult
    public mutating func apply(_ alarm: V2.Alarm, isNew: Bool) -> Bool {
        let before = current?.id
        if let index = items.firstIndex(where: { $0.id == alarm.id }) {
            if Self.belongs(alarm) {
                items[index] = alarm
            } else {
                items.remove(at: index)
            }
        } else if isNew, Self.belongs(alarm) {
            items.append(alarm)
            items.sort { ($0.createdAt, $0.id) > ($1.createdAt, $1.id) }
            if items.count > capacity { items.removeLast(items.count - capacity) }
        }
        return current?.id != before
    }

    /// Takes an alarm off this monitor without changing it.
    public mutating func dismiss(_ id: String) {
        items.removeAll { $0.id == id }
    }

    /// Brings a queued alarm to the full tile (it keeps its place otherwise).
    public mutating func promote(_ id: String) {
        guard let index = items.firstIndex(where: { $0.id == id }), index > 0 else { return }
        items.insert(items.remove(at: index), at: 0)
    }

    public mutating func removeAll() {
        items.removeAll()
    }

    static func belongs(_ alarm: V2.Alarm) -> Bool {
        guard let camera = alarm.cameraId, camera.isEmpty == false else { return false }
        return alarm.isOpen && alarm.state == .new
    }
}
