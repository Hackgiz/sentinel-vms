import Foundation

// DTOs for the `/api/v2/incidents` family of `linux/api/openapi.yaml`.

public extension V2 {
    struct IncidentStatus: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let open = IncidentStatus(rawValue: "open")
        public static let investigating = IncidentStatus(rawValue: "investigating")
        public static let closed = IncidentStatus(rawValue: "closed")
        public static let all: [IncidentStatus] = [.open, .investigating, .closed]

        public var label: String { rawValue.prefix(1).uppercased() + rawValue.dropFirst() }
    }

    struct IncidentSeverity: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let low = IncidentSeverity(rawValue: "low")
        public static let medium = IncidentSeverity(rawValue: "medium")
        public static let high = IncidentSeverity(rawValue: "high")
        public static let critical = IncidentSeverity(rawValue: "critical")
        public static let all: [IncidentSeverity] = [.low, .medium, .high, .critical]

        public var label: String { rawValue.prefix(1).uppercased() + rawValue.dropFirst() }

        /// The incident severity an alarm's severity starts a new incident at.
        public init(alarm: V2.Severity) {
            switch alarm {
            case .critical: self = .critical
            case .warning: self = .medium
            case .info: self = .low
            }
        }
    }

    struct Incident: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var title: String
        public var status: IncidentStatus
        public var severity: IncidentSeverity
        /// Markdown; the drafted report lands here.
        public var summary: String
        public var createdBy: String
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis
        public var itemCount: Int

        public init(id: String, title: String, status: IncidentStatus = .open, severity: IncidentSeverity = .medium,
                    summary: String = "", createdBy: String = "", createdAt: UnixMillis = 0,
                    updatedAt: UnixMillis = 0, itemCount: Int = 0) {
            self.id = id
            self.title = title
            self.status = status
            self.severity = severity
            self.summary = summary
            self.createdBy = createdBy
            self.createdAt = createdAt
            self.updatedAt = updatedAt
            self.itemCount = itemCount
        }
    }

    struct IncidentItemKind: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let alarm = IncidentItemKind(rawValue: "alarm")
        public static let event = IncidentItemKind(rawValue: "event")
        public static let evidence = IncidentItemKind(rawValue: "evidence")
        public static let note = IncidentItemKind(rawValue: "note")
    }

    struct IncidentItem: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var kind: IncidentItemKind
        /// Empty for notes and restricted items.
        public var refId: String?
        public var note: String?
        public var addedBy: String
        public var addedAt: UnixMillis
        /// When the underlying thing happened; the chronology sorts on it.
        public var at: UnixMillis
        /// The object lies outside the caller's camera scope.
        public var restricted: Bool?
        /// The object no longer exists.
        public var missing: Bool?
        public var alarm: Alarm?
        public var event: Event?
        public var evidence: Evidence?

        public init(id: String, kind: IncidentItemKind, refId: String? = nil, note: String? = nil, addedBy: String = "",
                    addedAt: UnixMillis = 0, at: UnixMillis = 0, restricted: Bool? = nil, missing: Bool? = nil,
                    alarm: Alarm? = nil, event: Event? = nil, evidence: Evidence? = nil) {
            self.id = id
            self.kind = kind
            self.refId = refId
            self.note = note
            self.addedBy = addedBy
            self.addedAt = addedAt
            self.at = at
            self.restricted = restricted
            self.missing = missing
            self.alarm = alarm
            self.event = event
            self.evidence = evidence
        }
    }

    /// `GET /api/v2/incidents/{id}`.
    struct IncidentDetail: Codable, Hashable, Sendable {
        public var incident: Incident
        @NullableArray public var items: [IncidentItem]
    }

    struct NewIncident: Codable, Hashable, Sendable {
        public var title: String
        public var severity: IncidentSeverity?
        public init(title: String, severity: IncidentSeverity? = nil) {
            self.title = title
            self.severity = severity
        }
    }

    /// `PATCH /api/v2/incidents/{id}`: only the fields set are sent.
    struct IncidentPatch: Codable, Hashable, Sendable {
        public var title: String?
        public var status: IncidentStatus?
        public var severity: IncidentSeverity?
        public var summary: String?

        public init(title: String? = nil, status: IncidentStatus? = nil, severity: IncidentSeverity? = nil, summary: String? = nil) {
            self.title = title
            self.status = status
            self.severity = severity
            self.summary = summary
        }
    }

    /// `POST /api/v2/incidents/{id}/items`.
    struct IncidentItemInput: Codable, Hashable, Sendable {
        public var kind: IncidentItemKind
        public var refId: String?
        public var note: String?

        public init(kind: IncidentItemKind, refId: String? = nil, note: String? = nil) {
            self.kind = kind
            self.refId = refId
            self.note = note
        }

        public static func alarm(_ id: String) -> IncidentItemInput { .init(kind: .alarm, refId: id) }
        public static func event(_ id: String) -> IncidentItemInput { .init(kind: .event, refId: id) }
        public static func evidence(_ id: String) -> IncidentItemInput { .init(kind: .evidence, refId: id) }
        public static func note(_ text: String) -> IncidentItemInput { .init(kind: .note, note: text) }
    }
}
