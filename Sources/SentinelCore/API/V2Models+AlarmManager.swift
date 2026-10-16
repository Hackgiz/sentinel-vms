import Foundation

// DTOs for the Alarm Manager routes of `linux/api/openapi.yaml`:
// `/alarms/counts`, `/alarms/{id}/assign|severity|triage`.

public extension V2 {
    /// `GET /api/v2/alarms/counts`: every alarm state present, keyed by its raw value.
    struct AlarmCounts: Codable, Hashable, Sendable {
        public var counts: [String: Int]

        public init(counts: [String: Int] = [:]) {
            self.counts = counts
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            counts = try c.decodeIfPresent([String: Int].self, forKey: .counts) ?? [:]
        }

        private enum CodingKeys: String, CodingKey { case counts }

        public func count(for state: AlarmState) -> Int { counts[state.rawValue] ?? 0 }
        public var total: Int { counts.values.reduce(0, +) }
    }

    /// `POST /api/v2/alarms/{id}/triage`: advice only, nothing is applied.
    struct AlarmTriage: Codable, Hashable, Sendable {
        public var summary: String
        /// Critical, Warning or Info; empty when the model gave none.
        public var suggestedSeverity: String
        public var rationale: String

        public init(summary: String, suggestedSeverity: String, rationale: String) {
            self.summary = summary
            self.suggestedSeverity = suggestedSeverity
            self.rationale = rationale
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
            suggestedSeverity = try c.decodeIfPresent(String.self, forKey: .suggestedSeverity) ?? ""
            rationale = try c.decodeIfPresent(String.self, forKey: .rationale) ?? ""
        }

        private enum CodingKeys: String, CodingKey { case summary, suggestedSeverity, rationale }

        /// The suggested severity as a known value (nil when empty or unknown).
        public var suggested: Severity? { Severity(rawValue: suggestedSeverity) }

        /// The suggestion when it is a known severity that differs from `current`.
        public func applicableSeverity(current: Severity) -> Severity? {
            guard let suggested, suggested != current else { return nil }
            return suggested
        }
    }

    struct AlarmAssignment: Codable, Hashable, Sendable {
        /// A user name; empty unassigns.
        public var owner: String
        public init(owner: String) { self.owner = owner }
    }

    struct AlarmSeverityChange: Codable, Hashable, Sendable {
        public var severity: Severity
        public init(severity: Severity) { self.severity = severity }
    }

    /// Just the AI flag of `GET /api/v2/events/facets` (`aiEnabled`).
    struct AIAvailability: Decodable, Hashable, Sendable {
        public var aiEnabled: Bool

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            aiEnabled = try c.decodeIfPresent(Bool.self, forKey: .aiEnabled) ?? false
        }

        private enum CodingKeys: String, CodingKey { case aiEnabled }
    }
}
