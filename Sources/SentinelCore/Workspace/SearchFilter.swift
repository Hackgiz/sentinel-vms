import Foundation

// The forensic search filter bar's state and how it maps to the API.
// Mirrors `linux/web/src/operator/searchModel.ts`.

public enum SearchRange: String, CaseIterable, Identifiable, Sendable {
    case hour, day, week, month, any, custom

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .hour: return "Last hour"
        case .day: return "24 hours"
        case .week: return "7 days"
        case .month: return "30 days"
        case .any: return "Any time"
        case .custom: return "Custom"
        }
    }

    /// The preset's span in milliseconds; 0 for "any" and "custom".
    public var spanMillis: Int64 {
        switch self {
        case .hour: return 3_600_000
        case .day: return 86_400_000
        case .week: return 7 * 86_400_000
        case .month: return 30 * 86_400_000
        case .any, .custom: return 0
        }
    }

    public static let presets: [SearchRange] = [.hour, .day, .week, .month, .any]
}

public struct SearchFilter: Equatable, Sendable {
    public static let pageSize = 60
    /// Playback starts a few seconds before the detection.
    public static let playbackLead: TimeInterval = 5

    public var range: SearchRange = .day
    /// Used only when `range` is `.custom` (Unix ms; 0 = open).
    public var from: UnixMillis = 0
    public var to: UnixMillis = 0
    public var cameras: [String] = []
    public var kinds: [String] = []
    public var labels: [String] = []
    public var q = ""
    /// "Elevated or high only".
    public var threatOnly = false
    /// Set by a natural-language question; shown as removable chips.
    public var threats: [String] = []
    public var minScore: Double = 0
    public var anomaly: Bool?
    /// Wave 16: plate text (normalized when sent; a prefix matches).
    public var plate = ""
    /// Wave 16: a personId.
    public var person = ""
    /// Wave 16: a trackId ("Same object").
    public var track = ""

    public init() {}

    /// The [from, to) window in Unix ms (0 = open).
    public func window(now: Date) -> (from: UnixMillis, to: UnixMillis) {
        if range == .custom { return (from, to) }
        let span = range.spanMillis
        return span > 0 ? (now.unixMillis - span, 0) : (0, 0)
    }

    private static func list(_ values: [String]) -> String? {
        values.isEmpty ? nil : values.joined(separator: ",")
    }

    /// Query items for `GET /events/search`.
    public func queryItems(now: Date, limit: Int? = nil, before: String? = nil) -> [URLQueryItem] {
        let (from, to) = window(now: now)
        let threatValues = threatOnly ? ["elevated", "high"] : threats
        let trimmed = q.trimmingCharacters(in: .whitespacesAndNewlines)
        var items: [URLQueryItem] = []
        func add(_ name: String, _ value: String?) {
            if let value { items.append(URLQueryItem(name: name, value: value)) }
        }
        add("cameras", Self.list(cameras))
        add("kinds", Self.list(kinds))
        add("labels", Self.list(labels))
        add("from", from > 0 ? String(from) : nil)
        add("to", to > 0 ? String(to) : nil)
        add("minScore", minScore > 0 ? String(minScore) : nil)
        add("threats", Self.list(threatValues))
        add("anomaly", anomaly.map { $0 ? "true" : "false" })
        add("q", trimmed.isEmpty ? nil : trimmed)
        let plateText = PlateLogic.normalize(plate)
        add("plate", plateText.isEmpty ? nil : plateText)
        add("person", person.isEmpty ? nil : person)
        add("track", track.isEmpty ? nil : track)
        add("limit", limit.map(String.init))
        add("before", before)
        return items
    }

    /// Query items for `GET /events/facets` (cameras and window only).
    public func facetQueryItems(now: Date) -> [URLQueryItem] {
        let (from, to) = window(now: now)
        var items: [URLQueryItem] = []
        if cameras.isEmpty == false { items.append(URLQueryItem(name: "cameras", value: cameras.joined(separator: ","))) }
        if from > 0 { items.append(URLQueryItem(name: "from", value: String(from))) }
        if to > 0 { items.append(URLQueryItem(name: "to", value: String(to))) }
        return items
    }

    /// The part of the filter that decides the facet counts (re-fetched only when it changes).
    public var facetKey: String {
        "\(range.rawValue)|\(from)|\(to)|\(cameras.joined(separator: ","))"
    }

    /// The filter-bar state for the filter the server understood a question as.
    public static func from(_ filter: V2.EventSearchFilter) -> SearchFilter {
        var s = SearchFilter()
        let open = (filter.from ?? 0) == 0 && (filter.to ?? 0) == 0
        let threats = filter.threats ?? []
        let elevated = threats.count == 2 && threats.contains("elevated") && threats.contains("high")
        s.range = open ? .any : .custom
        s.from = filter.from ?? 0
        s.to = filter.to ?? 0
        s.cameras = filter.cameras ?? []
        s.kinds = filter.kinds ?? []
        s.labels = filter.labels ?? []
        s.q = filter.q ?? ""
        s.threatOnly = elevated
        s.threats = elevated ? [] : threats
        s.minScore = filter.minScore ?? 0
        s.anomaly = filter.anomaly
        s.plate = filter.plate ?? ""
        s.person = filter.person ?? ""
        s.track = filter.track ?? ""
        return s
    }

    /// How many filters (besides the time range) are set.
    public var activeCount: Int {
        cameras.count + kinds.count + labels.count + (q.trimmingCharacters(in: .whitespaces).isEmpty ? 0 : 1)
            + (threatOnly ? 1 : 0) + threats.count + (minScore > 0 ? 1 : 0) + (anomaly == nil ? 0 : 1)
            + (PlateLogic.normalize(plate).isEmpty ? 0 : 1) + (person.isEmpty ? 0 : 1) + (track.isEmpty ? 0 : 1)
    }

    /// "Same object": every detection of one tracked object, at any time.
    public static func sameObject(track: String) -> SearchFilter {
        var s = SearchFilter()
        s.range = .any
        s.track = track
        return s
    }

    /// This filter with everything but the time range cleared.
    public var cleared: SearchFilter {
        var s = SearchFilter()
        s.range = range
        s.from = from
        s.to = to
        return s
    }

    public static func toggled(_ values: [String], _ value: String) -> [String] {
        values.contains(value) ? values.filter { $0 != value } : values + [value]
    }

    /// Where playback starts for an event.
    public static func playbackStart(of event: V2.Event) -> Date {
        Date(unixMillis: event.createdAt).addingTimeInterval(-playbackLead)
    }
}

/// A facet chip: a value, its count in the current window, and whether it is selected.
public struct FacetChip: Equatable, Sendable {
    public var value: String
    public var count: Int
    public var selected: Bool
}

public enum FacetChips {
    /// Facet values (most frequent first) followed by selected values the
    /// current window has none of, shown with a count of 0 so they stay
    /// visible and can be switched off.
    public static func merge(_ facets: [V2.FacetCount], selected: [String], limit: Int? = nil) -> [FacetChip] {
        var chips = facets.map { FacetChip(value: $0.value, count: $0.count, selected: selected.contains($0.value)) }
        if let limit { chips = Array(chips.prefix(limit)) }
        for value in selected where chips.contains(where: { $0.value == value }) == false {
            chips.append(FacetChip(value: value, count: 0, selected: true))
        }
        return chips
    }

    /// Events with an elevated or high threat in the window.
    public static func threatTotal(_ facets: [V2.FacetCount]) -> Int {
        facets.filter { $0.value == "elevated" || $0.value == "high" }.reduce(0) { $0 + $1.count }
    }
}

/// Why an `/events/ask` call failed, so the UI can word it and degrade.
public enum AskFailure: Equatable, Sendable {
    /// 409: AI is off or has no key. The ask field should hide.
    case aiOff(message: String)
    /// 429: the daily AI limit is reached. The ask field disables; filters still work.
    case limitReached(message: String)
    case other(message: String)

    public var message: String {
        switch self {
        case .aiOff(let m), .limitReached(let m), .other(let m): return m
        }
    }

    public init(_ error: Error) {
        let text = error.localizedDescription
        if let api = error as? SentinelAPIError {
            if api.isConflict { self = .aiOff(message: text); return }
            if api.status == 429 || api.code == "too_many_requests" { self = .limitReached(message: text); return }
        }
        self = .other(message: text)
    }
}
