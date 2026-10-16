import Foundation

// Forensic search DTOs (`/api/v2/events/search|facets|ask`). Wave 5 stream B.

public extension V2 {
    /// An event search as the filter bar holds it (times in Unix ms).
    struct EventSearchFilter: Codable, Hashable, Sendable {
        public var cameras: [String]?
        public var kinds: [String]?
        public var labels: [String]?
        public var from: UnixMillis?
        public var to: UnixMillis?
        public var minScore: Double?
        public var threats: [String]?
        public var anomaly: Bool?
        public var q: String?

        public init(cameras: [String]? = nil, kinds: [String]? = nil, labels: [String]? = nil,
                    from: UnixMillis? = nil, to: UnixMillis? = nil, minScore: Double? = nil,
                    threats: [String]? = nil, anomaly: Bool? = nil, q: String? = nil) {
            self.cameras = cameras
            self.kinds = kinds
            self.labels = labels
            self.from = from
            self.to = to
            self.minScore = minScore
            self.threats = threats
            self.anomaly = anomaly
            self.q = q
        }
    }

    struct FacetCount: Codable, Hashable, Sendable {
        public var value: String
        public var count: Int

        public init(value: String, count: Int) {
            self.value = value
            self.count = count
        }
    }

    /// Counts by kind, label and threat, most frequent first.
    struct EventFacets: Codable, Hashable, Sendable {
        @NullableArray public var kinds: [FacetCount]
        @NullableArray public var labels: [FacetCount]
        @NullableArray public var threats: [FacetCount]
        /// Whether `/events/ask` is available (AI on, key saved).
        public var aiEnabled: Bool

        public init(kinds: [FacetCount] = [], labels: [FacetCount] = [], threats: [FacetCount] = [], aiEnabled: Bool = false) {
            self.kinds = kinds
            self.labels = labels
            self.threats = threats
            self.aiEnabled = aiEnabled
        }
    }

    /// `POST /events/ask`: the filter the question was understood as, and its first page.
    struct EventAnswer: Codable, Hashable, Sendable {
        public var filter: EventSearchFilter
        @NullableArray public var items: [Event]
        public var nextCursor: String?

        public init(filter: EventSearchFilter, items: [Event] = [], nextCursor: String? = nil) {
            self.filter = filter
            self.items = items
            self.nextCursor = nextCursor
        }
    }
}
