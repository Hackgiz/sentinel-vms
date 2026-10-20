import Foundation

// Appearance search DTOs (`/api/v2/events/{id}/similar`, `/api/v2/appearance`,
// `/api/v2/appearance/backfill`). Wave 11 stream C.

public extension V2 {
    /// The body of `POST /events/{id}/similar`. Absent fields take the server's defaults.
    struct SimilarEventsRequest: Codable, Hashable, Sendable {
        /// Unix ms, inclusive; nil = no lower bound.
        public var from: UnixMillis?
        /// Unix ms, exclusive; nil = no upper bound.
        public var to: UnixMillis?
        /// Limit to these cameras; nil = every visible camera.
        public var cameras: [String]?
        /// 1…100 (24 when omitted).
        public var limit: Int?
        /// Lowest cosine similarity returned, -1…1.
        public var minScore: Double?
        /// Compare against every event kind, not only the query's.
        public var anyKind: Bool?

        public init(from: UnixMillis? = nil, to: UnixMillis? = nil, cameras: [String]? = nil,
                    limit: Int? = nil, minScore: Double? = nil, anyKind: Bool? = nil) {
            self.from = from
            self.to = to
            self.cameras = cameras
            self.limit = limit
            self.minScore = minScore
            self.anyKind = anyKind
        }
    }

    struct SimilarEventHit: Codable, Hashable, Sendable {
        /// Cosine similarity, -1…1 (1 = identical appearance).
        public var score: Double
        public var event: Event

        public init(score: Double, event: Event) {
            self.score = score
            self.event = event
        }
    }

    /// The query event and its matches, best first.
    struct SimilarEvents: Codable, Hashable, Sendable {
        public var query: Event
        /// Model id (`sha256:` + 16 hex digits).
        public var model: String
        /// Candidate vectors compared.
        public var scanned: Int
        @NullableArray public var items: [SimilarEventHit]

        public init(query: Event, model: String, scanned: Int, items: [SimilarEventHit] = []) {
            self.query = query
            self.model = model
            self.scanned = scanned
            self.items = items
        }
    }

    struct AppearanceBackfillState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let running = AppearanceBackfillState(rawValue: "running")
        public static let done = AppearanceBackfillState(rawValue: "done")
        public static let failed = AppearanceBackfillState(rawValue: "failed")
        public static let canceled = AppearanceBackfillState(rawValue: "canceled")
    }

    /// The appearance index job.
    struct AppearanceBackfill: Codable, Hashable, Sendable {
        public var taskId: String
        public var state: AppearanceBackfillState
        /// Events to index.
        public var total: Int
        public var processed: Int
        public var indexed: Int
        /// Events whose snapshot was missing or whose box was too small.
        public var skipped: Int
        public var error: String?
        public var startedAt: UnixMillis
        public var finishedAt: UnixMillis?

        public init(taskId: String, state: AppearanceBackfillState, total: Int = 0, processed: Int = 0,
                    indexed: Int = 0, skipped: Int = 0, error: String? = nil, startedAt: UnixMillis = 0,
                    finishedAt: UnixMillis? = nil) {
            self.taskId = taskId
            self.state = state
            self.total = total
            self.processed = processed
            self.indexed = indexed
            self.skipped = skipped
            self.error = error
            self.startedAt = startedAt
            self.finishedAt = finishedAt
        }
    }

    /// `GET /appearance`.
    struct AppearanceStatus: Codable, Hashable, Sendable {
        public var available: Bool
        /// Why appearance search is unavailable.
        public var reason: String?
        public var model: String?
        /// Events with a vector from the current model.
        public var indexed: Int?
        /// The latest index job.
        public var backfill: AppearanceBackfill?

        public init(available: Bool, reason: String? = nil, model: String? = nil, indexed: Int? = nil,
                    backfill: AppearanceBackfill? = nil) {
            self.available = available
            self.reason = reason
            self.model = model
            self.indexed = indexed
            self.backfill = backfill
        }
    }
}
