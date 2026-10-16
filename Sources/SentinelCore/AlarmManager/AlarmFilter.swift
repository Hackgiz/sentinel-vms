import Foundation

// The Alarm Manager's filter bar, chips, sorting, selection and bulk actions as
// plain value types, so they can be tested without a view. Mirrors the web
// operator's `alarmModel.ts`.

/// The filter bar of the Alarm Manager. Builds the query strings of
/// `GET /api/v2/alarms` and `GET /api/v2/alarms/counts`.
public struct AlarmFilter: Equatable, Sendable {
    public enum Range: String, CaseIterable, Identifiable, Sendable {
        case any, hour, day, week, custom

        public var id: String { rawValue }

        public var label: String {
            switch self {
            case .any: return "Any time"
            case .hour: return "Last hour"
            case .day: return "24 hours"
            case .week: return "7 days"
            case .custom: return "Custom…"
            }
        }

        /// Window length for the presets; 0 = open (any/custom).
        public var milliseconds: Int64 {
            switch self {
            case .any, .custom: return 0
            case .hour: return 3_600_000
            case .day: return 86_400_000
            case .week: return 7 * 86_400_000
            }
        }
    }

    public static let allStates: [V2.AlarmState] = [.new, .acknowledged, .investigating, .snoozed, .resolved, .falseAlarm]
    public static let openStates: [V2.AlarmState] = [.new, .acknowledged, .investigating, .snoozed]
    public static let severities: [V2.Severity] = [.critical, .warning, .info]
    public static let unassignedOwner = "unassigned"

    /// Chips pressed. Empty = every state.
    public var states: [V2.AlarmState]
    public var severities: [V2.Severity]
    public var cameras: [String]
    /// "" = anyone, "unassigned", or a user name.
    public var owner: String
    public var range: Range
    /// Used only when `range == .custom` (Unix ms; 0 = open).
    public var from: UnixMillis
    public var to: UnixMillis
    public var q: String

    public init(states: [V2.AlarmState] = AlarmFilter.openStates, severities: [V2.Severity] = [], cameras: [String] = [],
                owner: String = "", range: Range = .any, from: UnixMillis = 0, to: UnixMillis = 0, q: String = "") {
        self.states = states
        self.severities = severities
        self.cameras = cameras
        self.owner = owner
        self.range = range
        self.from = from
        self.to = to
        self.q = q
    }

    public static let `default` = AlarmFilter()

    /// The [from, to) window in Unix ms (0 = open).
    public func window(now: Date) -> (from: UnixMillis, to: UnixMillis) {
        if range == .custom { return (from, to) }
        let length = range.milliseconds
        return length > 0 ? (now.unixMillis - length, 0) : (0, 0)
    }

    private static func list(_ values: [String]) -> String? {
        values.isEmpty ? nil : values.joined(separator: ",")
    }

    /// Everything but the state chips: the filters the per-state counts honor.
    public func countsQueryItems(now: Date) -> [URLQueryItem] {
        let w = window(now: now)
        let trimmed = q.trimmingCharacters(in: .whitespacesAndNewlines)
        return [
            URLQueryItem(name: "severities", value: Self.list(severities.map(\.rawValue))),
            URLQueryItem(name: "cameras", value: Self.list(cameras)),
            URLQueryItem(name: "owner", value: owner.isEmpty ? nil : owner),
            URLQueryItem(name: "from", value: w.from > 0 ? String(w.from) : nil),
            URLQueryItem(name: "to", value: w.to > 0 ? String(w.to) : nil),
            URLQueryItem(name: "q", value: trimmed.isEmpty ? nil : trimmed)
        ].filter { $0.value != nil }
    }

    /// Query for `GET /api/v2/alarms`. No chip pressed = every state (`state=all`).
    public func listQueryItems(now: Date, limit: Int? = nil, before: String? = nil) -> [URLQueryItem] {
        var items = countsQueryItems(now: now)
        if states.isEmpty {
            items.append(URLQueryItem(name: "state", value: "all"))
        } else {
            let ordered = Self.allStates.filter(states.contains)
            items.append(URLQueryItem(name: "states", value: ordered.map(\.rawValue).joined(separator: ",")))
        }
        if let limit { items.append(URLQueryItem(name: "limit", value: String(limit))) }
        if let before { items.append(URLQueryItem(name: "before", value: before)) }
        return items
    }

    /// How many filters besides the state chips are set.
    public var activeCount: Int {
        severities.count + cameras.count + (owner.isEmpty ? 0 : 1) + (range == .any ? 0 : 1)
            + (q.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? 0 : 1)
    }

    /// Same filters with the state chips kept ("Clear filters").
    public func clearedKeepingStates() -> AlarmFilter {
        AlarmFilter(states: states)
    }

    public mutating func toggle(state: V2.AlarmState) {
        if let index = states.firstIndex(of: state) { states.remove(at: index) } else { states.append(state) }
    }

    public mutating func toggle(severity: V2.Severity) {
        if let index = severities.firstIndex(of: severity) { severities.remove(at: index) } else { severities.append(severity) }
    }

    public mutating func toggle(camera id: String) {
        if let index = cameras.firstIndex(of: id) { cameras.remove(at: index) } else { cameras.append(id) }
    }

    /// Whether an alarm still belongs in a list loaded with this filter's
    /// state chips (live updates drop alarms that moved out of the chosen states).
    public func includesState(of alarm: V2.Alarm) -> Bool {
        states.isEmpty || states.contains(alarm.state)
    }
}

// MARK: - State chips

/// One state chip: the state, its live count and whether it is pressed.
public struct AlarmChip: Equatable, Identifiable, Sendable {
    public var state: V2.AlarmState
    public var count: Int
    public var isOn: Bool
    public var id: String { state.rawValue }
}

public enum AlarmChips {
    /// The six chips in canonical order with counts from `/alarms/counts`
    /// (a state the server omitted counts 0).
    public static func make(filter: AlarmFilter, counts: V2.AlarmCounts) -> [AlarmChip] {
        AlarmFilter.allStates.map { AlarmChip(state: $0, count: counts.count(for: $0), isOn: filter.states.contains($0)) }
    }
}

// MARK: - Sorting

public enum AlarmSortKey: String, CaseIterable, Sendable {
    case severity, state, camera, owner, time, events
}

public struct AlarmSort: Equatable, Sendable {
    public var key: AlarmSortKey
    public var ascending: Bool

    public init(key: AlarmSortKey = .time, ascending: Bool = false) {
        self.key = key
        self.ascending = ascending
    }

    /// Clicking a header: the same key flips direction; a new key starts
    /// descending for time/events and ascending otherwise.
    public func toggled(to newKey: AlarmSortKey) -> AlarmSort {
        if newKey == key { return AlarmSort(key: key, ascending: !ascending) }
        return AlarmSort(key: newKey, ascending: !(newKey == .time || newKey == .events))
    }
}

public extension V2.Alarm {
    /// Critical 0 … Info 2, for sorting.
    var severityRank: Int { severity.rank }

    var stateRank: Int { AlarmFilter.allStates.firstIndex(of: state) ?? AlarmFilter.allStates.count }

    var cameraSortName: String { cameraName ?? "" }
}

public enum AlarmSorting {
    /// Sorted copy; ties fall back to newest first, then id, so the order is stable.
    public static func sorted(_ alarms: [V2.Alarm], by sort: AlarmSort) -> [V2.Alarm] {
        func compare(_ a: V2.Alarm, _ b: V2.Alarm) -> Int {
            switch sort.key {
            case .severity: return a.severityRank - b.severityRank
            case .state: return a.stateRank - b.stateRank
            case .camera: return order(a.cameraSortName.localizedCaseInsensitiveCompare(b.cameraSortName))
            case .owner: return order(a.owner.localizedCaseInsensitiveCompare(b.owner))
            case .events: return a.eventCount - b.eventCount
            case .time: return a.createdAt == b.createdAt ? 0 : (a.createdAt < b.createdAt ? -1 : 1)
            }
        }
        func order(_ r: ComparisonResult) -> Int { r == .orderedAscending ? -1 : (r == .orderedDescending ? 1 : 0) }
        let sign = sort.ascending ? 1 : -1
        return alarms.sorted { a, b in
            let c = compare(a, b)
            if c != 0 { return sign * c < 0 }
            if a.createdAt != b.createdAt { return a.createdAt > b.createdAt }
            return a.id < b.id
        }
    }
}

// MARK: - Bulk actions

public enum AlarmBulk {
    public enum Action: Sendable { case acknowledge, close }

    /// The selected alarms a bulk action applies to, and the state it sets.
    /// Acknowledge only touches New/Snoozed; Close any open alarm.
    public static func targets(_ action: Action, alarms: [V2.Alarm], selected: Set<String>) -> (ids: [String], state: V2.AlarmState) {
        let eligible: (V2.Alarm) -> Bool = action == .acknowledge
            ? { $0.state == .new || $0.state == .snoozed }
            : { $0.isOpen }
        return (alarms.filter { selected.contains($0.id) && eligible($0) }.map(\.id),
                action == .acknowledge ? .acknowledged : .resolved)
    }

    /// Toggle one id, or (shift-click) the range between the anchor and it, in table order.
    public static func toggleSelection(_ selected: Set<String>, order: [String], id: String, anchor: String?, range: Bool) -> Set<String> {
        var next = selected
        if range, let anchor, anchor != id,
           let a = order.firstIndex(of: anchor), let b = order.firstIndex(of: id) {
            let on = !selected.contains(id)
            for x in order[min(a, b)...max(a, b)] {
                if on { next.insert(x) } else { next.remove(x) }
            }
            return next
        }
        if next.contains(id) { next.remove(id) } else { next.insert(id) }
        return next
    }

    /// Splits ids into batches of `size` (at least 1) for a bulk call.
    public static func batches(_ ids: [String], size: Int) -> [[String]] {
        let n = max(size, 1)
        return stride(from: 0, to: ids.count, by: n).map { Array(ids[$0..<min($0 + n, ids.count)]) }
    }

    /// The outcome of a bulk run: updated objects (in request order) and failures.
    public struct Outcome<Value: Sendable>: Sendable {
        public var succeeded: [Value] = []
        public var failures: [(id: String, message: String)] = []
        public var attempted = 0

        public var failedCount: Int { failures.count }
        public var firstFailure: String? { failures.first?.message }

        /// "2 of 5 alarms could not be changed." or nil when all went through.
        public var summary: String? {
            failures.isEmpty ? nil : "\(failures.count) of \(attempted) alarm\(attempted == 1 ? "" : "s") could not be changed."
        }
    }

    /// Runs `operation` per id, `batchSize` at a time (the server isn't flooded
    /// by a select-all), preserving request order in the outcome. Failures
    /// never stop the run.
    public static func run<Value: Sendable>(ids: [String], batchSize: Int = 8,
                                            _ operation: @escaping @Sendable (String) async throws -> Value) async -> Outcome<Value> {
        var outcome = Outcome<Value>()
        outcome.attempted = ids.count
        for batch in batches(ids, size: batchSize) {
            let results = await withTaskGroup(of: (Int, Result<Value, Error>).self) { group -> [(Int, Result<Value, Error>)] in
                for (index, id) in batch.enumerated() {
                    group.addTask {
                        do { return (index, .success(try await operation(id))) } catch { return (index, .failure(error)) }
                    }
                }
                var all: [(Int, Result<Value, Error>)] = []
                for await r in group { all.append(r) }
                return all.sorted { $0.0 < $1.0 }
            }
            for (index, result) in results {
                switch result {
                case .success(let value): outcome.succeeded.append(value)
                case .failure(let error): outcome.failures.append((batch[index], error.localizedDescription))
                }
            }
        }
        return outcome
    }
}

// MARK: - Incident picker

public enum IncidentPicking {
    /// Incidents an alarm can still be added to: not closed.
    public static func pickable(_ incidents: [V2.Incident]) -> [V2.Incident] {
        incidents.filter { $0.status != .closed }
    }

    /// Default title when starting a new incident from an alarm.
    public static func defaultTitle(for alarm: V2.Alarm) -> String {
        alarm.title + ((alarm.cameraName ?? "").isEmpty ? "" : " · \(alarm.cameraName!)")
    }
}

// MARK: - Paging

/// A list loaded by `before`/`nextCursor` keyset paging: first page replaces,
/// later pages append without duplicates, live updates upsert in place.
public struct CursorPager<Item: Codable & Hashable & Sendable & Identifiable>: Sendable where Item.ID == String {
    public private(set) var items: [Item] = []
    public private(set) var nextCursor: String?
    public private(set) var isLoaded = false

    public init() {}

    public var hasMore: Bool { nextCursor != nil }

    public mutating func replace(with page: V2.Page<Item>) {
        items = page.items
        nextCursor = page.nextCursor
        isLoaded = true
    }

    public mutating func append(_ page: V2.Page<Item>) {
        let known = Set(items.map(\.id))
        items += page.items.filter { !known.contains($0.id) }
        nextCursor = page.nextCursor
        isLoaded = true
    }

    /// Replaces an item already shown; returns whether it was there.
    @discardableResult
    public mutating func replaceExisting(_ item: Item) -> Bool {
        guard let index = items.firstIndex(where: { $0.id == item.id }) else { return false }
        items[index] = item
        return true
    }

    public mutating func insertAtFront(_ item: Item) {
        items.removeAll { $0.id == item.id }
        items.insert(item, at: 0)
    }

    public mutating func remove(id: String) {
        items.removeAll { $0.id == id }
    }

    public mutating func reset() {
        self = CursorPager()
    }
}

// MARK: - Live updates

/// Applies alarms seen on the live event stream to a loaded table without
/// polling: alarms already shown are replaced (or dropped when they left the
/// chosen states); an alarm the table has never seen asks for a first-page reload.
public struct AlarmLiveSync: Sendable {
    private var known: Set<String>

    public init(known: Set<String> = []) {
        self.known = known
    }

    public struct Result: Equatable, Sendable {
        public var changed = false
        public var needsReload = false
    }

    public mutating func markKnown(_ ids: some Sequence<String>) {
        known.formUnion(ids)
    }

    public mutating func ingest(_ updates: [V2.Alarm], into pager: inout CursorPager<V2.Alarm>, filter: AlarmFilter) -> Result {
        var result = Result()
        let shown = Dictionary(pager.items.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        for alarm in updates {
            if let current = shown[alarm.id] {
                guard current != alarm else { continue }
                if filter.includesState(of: alarm) {
                    pager.replaceExisting(alarm)
                } else {
                    pager.remove(id: alarm.id)
                }
                result.changed = true
            } else if known.insert(alarm.id).inserted {
                result.needsReload = true
                result.changed = true
            }
        }
        return result
    }
}
