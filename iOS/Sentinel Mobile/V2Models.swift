import Foundation

// Codable models for Sentinel API v2 (`linux/api/openapi.yaml`, the contract).
//
// Conventions from the spec: camelCase keys, identifiers end in `Id`, times are
// Unix milliseconds (`Int64`), lists are `{"items": [...]}` with an optional
// `nextCursor`. Every type lives in the `V2` namespace so it can't collide
// with the app's own view models (`CameraStatus`, `CameraGroup`, …).
//
// Decoding is strict about required fields (a missing one is a contract
// break worth surfacing) but lenient where the Go server may legitimately
// differ: an array may arrive as `null` (Go's nil slice) and an enum may grow
// a value this client doesn't know yet.

enum V2 {}

/// Unix time in milliseconds, as used throughout the API.
typealias UnixMillis = Int64

extension Date {
    init(unixMillis: UnixMillis) {
        self.init(timeIntervalSince1970: TimeInterval(unixMillis) / 1000)
    }

    var unixMillis: UnixMillis {
        UnixMillis((timeIntervalSince1970 * 1000).rounded())
    }
}

// MARK: - Decoding helpers

/// An array that decodes JSON `null` or a missing key as `[]`. Go encodes a
/// nil slice as `null`; the spec says these are arrays, so treat both alike.
@propertyWrapper
struct NullableArray<Element: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
    var wrappedValue: [Element]

    init(wrappedValue: [Element]) {
        self.wrappedValue = wrappedValue
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        wrappedValue = container.decodeNil() ? [] : try container.decode([Element].self)
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(wrappedValue)
    }
}

extension KeyedDecodingContainer {
    func decode<T>(_ type: NullableArray<T>.Type, forKey key: Key) throws -> NullableArray<T> {
        try decodeIfPresent(type, forKey: key) ?? NullableArray(wrappedValue: [])
    }
}

/// A string enum that keeps unknown values instead of failing the whole
/// response (the server can add an alarm kind or a permission before this
/// client knows it).
protocol OpenStringEnum: RawRepresentable, Codable, Hashable, Sendable where RawValue == String {
    init(rawValue: String)
}

extension OpenStringEnum {
    init(from decoder: Decoder) throws {
        self.init(rawValue: try decoder.singleValueContainer().decode(String.self))
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }
}

// MARK: - Errors and lists

extension V2 {
    /// `{"error": {"code": "...", "message": "..."}}`
    struct ErrorEnvelope: Codable, Hashable, Sendable {
        struct Body: Codable, Hashable, Sendable {
            var code: String
            var message: String
        }
        var error: Body
    }

    /// `{"items": [...]}`
    struct Items<Item: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
        @NullableArray var items: [Item]

        init(items: [Item]) {
            self.items = items
        }
    }

    /// `{"items": [...], "nextCursor": "..."}` — `nextCursor` absent on the last page.
    struct Page<Item: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
        @NullableArray var items: [Item]
        var nextCursor: String?

        init(items: [Item], nextCursor: String? = nil) {
            self.items = items
            self.nextCursor = nextCursor
        }
    }
}

// MARK: - Auth

extension V2 {
    struct Role: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let admin = Role(rawValue: "Admin")
        static let supervisor = Role(rawValue: "Supervisor")
        static let `operator` = Role(rawValue: "Operator")
        static let viewer = Role(rawValue: "Viewer")
        static let all: [Role] = [.admin, .supervisor, .operator, .viewer]
    }

    struct Permission: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let manageCameras = Permission(rawValue: "manageCameras")
        static let manageUsers = Permission(rawValue: "manageUsers")
        static let changeSettings = Permission(rawValue: "changeSettings")
        static let viewAudit = Permission(rawValue: "viewAudit")
        static let acknowledgeAlarms = Permission(rawValue: "acknowledgeAlarms")
        static let exportEvidence = Permission(rawValue: "exportEvidence")
        static let viewLive = Permission(rawValue: "viewLive")
    }

    struct UserScope: Codable, Hashable, Sendable {
        var allCameras: Bool
        @NullableArray var groupIds: [String]

        init(allCameras: Bool, groupIds: [String] = []) {
            self.allCameras = allCameras
            self.groupIds = groupIds
        }
    }

    struct User: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var name: String
        var role: Role
        var createdAt: UnixMillis
        /// `local`, `ldap` or `oidc`.
        var source: String
        var scope: UserScope
    }

    /// `Me` = `User` + `permissions`.
    struct Me: Codable, Hashable, Sendable {
        var id: String
        var name: String
        var role: Role
        var createdAt: UnixMillis
        var source: String
        var scope: UserScope
        @NullableArray var permissions: [Permission]

        func can(_ permission: Permission) -> Bool {
            permissions.contains(permission)
        }

        var user: User {
            User(id: id, name: name, role: role, createdAt: createdAt, source: source, scope: scope)
        }
    }

    /// The three grants of `POST /api/v2/auth/token`, discriminated by `grantType`.
    enum TokenGrant: Encodable, Sendable {
        case password(name: String, password: String, client: String?)
        case refresh(refreshToken: String)
        case oidcCode(code: String, client: String?)

        private enum CodingKeys: String, CodingKey {
            case grantType, name, password, client, refreshToken, code
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            switch self {
            case let .password(name, password, client):
                try c.encode("password", forKey: .grantType)
                try c.encode(name, forKey: .name)
                try c.encode(password, forKey: .password)
                try c.encodeIfPresent(client, forKey: .client)
            case let .refresh(refreshToken):
                try c.encode("refresh", forKey: .grantType)
                try c.encode(refreshToken, forKey: .refreshToken)
            case let .oidcCode(code, client):
                try c.encode("oidc_code", forKey: .grantType)
                try c.encode(code, forKey: .code)
                try c.encodeIfPresent(client, forKey: .client)
            }
        }
    }

    struct TokenResponse: Codable, Hashable, Sendable {
        var tokenType: String
        var accessToken: String
        var expiresAt: UnixMillis
        var refreshToken: String
        var refreshExpiresAt: UnixMillis
        var user: User
    }

    struct AuthProviders: Codable, Hashable, Sendable {
        struct OIDC: Codable, Hashable, Sendable {
            var enabled: Bool
            var displayName: String?
        }
        var local: Bool
        var ldap: Bool
        var oidc: OIDC
    }
}

// MARK: - Video

extension V2 {
    struct VideoRight: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let live = VideoRight(rawValue: "live")
        static let playback = VideoRight(rawValue: "playback")
        static let export = VideoRight(rawValue: "export")
    }

    struct VideoTokenRequest: Codable, Hashable, Sendable {
        var rights: [VideoRight]
        var recorderId: String?

        init(rights: [VideoRight], recorderId: String? = nil) {
            self.rights = rights
            self.recorderId = recorderId
        }
    }

    struct VideoToken: Codable, Hashable, Sendable {
        var token: String
        /// `{recorderUrl}/v/{token}` — may be a path (`/v/…`) when the recording
        /// server's URL isn't known; resolve against the site address.
        var baseUrl: String
        /// `/v/{token}` on the management server (works wherever the API does).
        var gatewayUrl: String
        var expiresAt: UnixMillis
        @NullableArray var rights: [VideoRight]
        var cameraId: String
        var recorderId: String
    }
}

// MARK: - Cameras

extension V2 {
    struct AlertOn: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let auto = AlertOn(rawValue: "auto")
        static let motion = AlertOn(rawValue: "motion")
        static let peopleAndVehicles = AlertOn(rawValue: "peopleAndVehicles")
        static let people = AlertOn(rawValue: "people")
    }

    struct CameraStatus: Codable, Hashable, Sendable {
        var online: Bool
        var viewers: Int
        var width: Int?
        var height: Int?
        var analytics: String?
        /// "Online | No signal | Media engine starting | Recording server offline"
        var message: String

        init(online: Bool, viewers: Int = 0, width: Int? = nil, height: Int? = nil, analytics: String? = nil, message: String) {
            self.online = online
            self.viewers = viewers
            self.width = width
            self.height = height
            self.analytics = analytics
            self.message = message
        }
    }

    struct Camera: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var name: String
        var location: String
        var rtspUrl: String
        var subRtspUrl: String
        var username: String
        var hasPassword: Bool
        var recording: Bool
        var retentionDays: Int
        var fps: Int
        var motionLevel: Int
        var alertOn: AlertOn
        var onvifUrl: String
        var cameraAnalytics: Bool
        var sortOrder: Int
        var createdAt: UnixMillis
        var recorderId: String
        @NullableArray var groupIds: [String]
        var status: CameraStatus
        // Wave 22 recording modes. Optional: older servers omit them.
        /// "default" | "continuous" | "events" | "dual" | "off".
        var recordingMode: String?
        /// Read-only: what the recorder actually does.
        var effectiveRecordingMode: String?
        /// Wave 23: "rtsp" | "phone"; older servers omit it (= rtsp).
        var source: String?

        var isPhoneCamera: Bool { source == "phone" }

        var resolutionLabel: String {
            if let w = status.width, let h = status.height, w > 0, h > 0 { return "\(w)×\(h)" }
            return status.online ? "Live" : "—"
        }

        /// What the camera records: the server's effective mode, else its own
        /// concrete mode, else the old `recording` flag ("continuous" / "off").
        var effectiveMode: String {
            if let e = effectiveRecordingMode, e.isEmpty == false, e != "default" { return e }
            if let m = recordingMode, m.isEmpty == false, m != "default" { return m }
            return recording ? "continuous" : "off"
        }

        /// The short mode label shown in camera info: "24/7", "Events",
        /// "24/7 low + events" or "Live only"; nil on servers without recording modes.
        var recordingModeLabel: String? {
            guard recordingMode != nil || effectiveRecordingMode != nil else { return nil }
            return Self.modeLabel(effectiveMode)
        }

        static func modeLabel(_ mode: String) -> String {
            switch mode {
            case "continuous": return "24/7"
            case "events": return "Events"
            case "dual": return "24/7 low + events"
            case "off": return "Live only"
            default: return mode.capitalized
            }
        }
    }
}

// MARK: - Recordings

extension V2 {
    struct Span: Codable, Hashable, Sendable {
        var start: UnixMillis
        var end: UnixMillis
        /// Seconds into the playback playlist for the same window (of the same recorderId).
        var media: Double?
        /// Recording server that has this footage.
        var recorderId: String?
        /// "full" | "low" (wave 22: the dual mode's 24/7 sub-stream); absent = full.
        var quality: String?

        init(start: UnixMillis, end: UnixMillis, media: Double? = nil, recorderId: String? = nil, quality: String? = nil) {
            self.start = start
            self.end = end
            self.media = media
            self.recorderId = recorderId
            self.quality = quality
        }

        /// Low-res footage, drawn lighter on the timeline.
        var isLowRes: Bool { quality == "low" }

        func contains(_ t: UnixMillis) -> Bool { t >= start && t < end }
    }

    struct Timeline: Codable, Hashable, Sendable {
        var from: UnixMillis
        var to: UnixMillis
        var now: UnixMillis
        @NullableArray var spans: [Span]
        @NullableArray var pending: [Span]
        var writing: Bool
        @NullableArray var events: [Event]
        @NullableArray var alarms: [Alarm]
    }

}

extension V2.Timeline {
    /// The recorded span holding wall time `t`, and where `t` is in the
    /// playback playlist of the same window and Recording Server (seconds).
    func mediaTime(at t: UnixMillis) -> (span: V2.Span, seconds: Double)? {
        guard let span = spans.first(where: { $0.contains(t) }) else { return nil }
        return (span, mediaStart(of: span) + Double(t - span.start) / 1000)
    }

    /// Wall time of second `seconds` in `recorderID`'s playback playlist.
    func wallTime(atMedia seconds: Double, recorderID: String?) -> UnixMillis? {
        for span in spans where span.recorderId == recorderID {
            let start = mediaStart(of: span)
            let length = Double(span.end - span.start) / 1000
            if seconds >= start, seconds < start + length {
                return span.start + UnixMillis(((seconds - start) * 1000).rounded())
            }
        }
        return nil
    }

    /// `Span.media`, or — when the server leaves it out — the spans of the
    /// same Recording Server laid end to end in time order.
    func mediaStart(of span: V2.Span) -> Double {
        if let media = span.media { return media }
        return spans
            .filter { $0.recorderId == span.recorderId && $0.start < span.start }
            .reduce(0) { $0 + Double($1.end - $1.start) / 1000 }
    }
}

extension V2 {
    struct Segment: Codable, Hashable, Sendable {
        var name: String
        var start: UnixMillis
        var end: UnixMillis
        var size: Int64
        var writing: Bool
        var recorderId: String?
    }
}
// MARK: - Alarms and events

extension V2 {

    struct Instruction: Codable, Hashable, Sendable {
        var rule: String
        var text: String
    }

    struct Alarm: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var cameraId: String?
        var cameraName: String?
        var kind: String
        var title: String
        var detail: String
        var severity: Severity
        var state: AlarmState
        var owner: String
        var createdAt: UnixMillis
        var updatedAt: UnixMillis
        var lastEventAt: UnixMillis
        var eventCount: Int
        var eventId: String?
        @NullableArray var instructions: [Instruction]
        @NullableArray var responseLog: [String]

        var isOpen: Bool { state.isOpen }
    }

    struct AlarmStateChange: Codable, Hashable, Sendable {
        var state: AlarmState
        var note: String?
        var snoozeMinutes: Int?

        init(state: AlarmState, note: String? = nil, snoozeMinutes: Int? = nil) {
            self.state = state
            self.note = note
            self.snoozeMinutes = snoozeMinutes
        }
    }

    struct Evidence: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var caseId: String
        var alarmId: String?
        var cameraId: String
        var cameraName: String
        var title: String
        var range: String
        var size: Int64
        var sha256: String
        var lockedAt: UnixMillis
        var lockedBy: String
        var recorderId: String?
    }

    struct Event: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var cameraId: String
        var kind: String
        var label: String?
        var score: Double
        /// Normalized [x1, y1, x2, y2].
        var box: [Double]?
        var createdAt: UnixMillis
        var hasThumbnail: Bool
        var alarmId: String?
        var description: String?
        /// none, low, elevated, high
        var threat: String?
        var anomaly: Bool?
        var reason: String?
        var tags: [String]?
        // Wave 16 detection depth; absent on older servers and on events nothing matched.
        /// The object track this detection belongs to; events of one track share it.
        var trackId: String?
        /// The matched person (face recognition).
        var personId: String?
        var personName: String?
        /// Plate read on a vehicle event, normalized A–Z0–9.
        var plate: String?
    }
}

// MARK: - Alarm vocabulary and push

extension V2 {
    /// New, Acknowledged, Investigating, Snoozed, Resolved, False Alarm. Open
    /// so a state this app doesn't know yet still decodes.
    struct AlarmState: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let new = AlarmState(rawValue: "New")
        static let acknowledged = AlarmState(rawValue: "Acknowledged")
        static let investigating = AlarmState(rawValue: "Investigating")
        static let snoozed = AlarmState(rawValue: "Snoozed")
        static let resolved = AlarmState(rawValue: "Resolved")
        static let falseAlarm = AlarmState(rawValue: "False Alarm")

        var isOpen: Bool { self != .resolved && self != .falseAlarm }
    }

    /// Critical, Warning, Info.
    struct Severity: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let critical = Severity(rawValue: "Critical")
        static let warning = Severity(rawValue: "Warning")
        static let info = Severity(rawValue: "Info")
    }

    /// `PUT /api/v2/push/registration`.
    struct PushRegistration: Codable, Hashable, Sendable {
        var apnsToken: String
        var deviceName: String?
    }

    /// `PUT /api/v2/push/activity-token`: the followed alarm's Live Activity push token.
    struct ActivityTokenRegistration: Codable, Hashable, Sendable {
        var apnsToken: String
        var alarmId: String
        var activityToken: String
    }
}

// MARK: - Insights

extension V2 {
    /// `GET /api/v2/insights`: trends and recommendations for a range.
    struct Insights: Codable, Hashable, Sendable {
        struct Totals: Codable, Hashable, Sendable {
            var events: Int
            var alarms: Int
            var camerasWithAlarms: Int
            /// Cameras with at least one Camera Offline alarm in the range.
            var offlineCameras: Int
        }

        struct Day: Codable, Hashable, Sendable {
            /// The management server's local date, YYYY-MM-DD.
            var day: String
            var events: Int
            var alarms: Int
            var byKind: [String: Int]

            init(day: String, events: Int, alarms: Int, byKind: [String: Int]) {
                self.day = day
                self.events = events
                self.alarms = alarms
                self.byKind = byKind
            }

            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                day = try c.decode(String.self, forKey: .day)
                events = try c.decode(Int.self, forKey: .events)
                alarms = try c.decode(Int.self, forKey: .alarms)
                // Go encodes an empty map as `null`.
                byKind = try c.decodeIfPresent([String: Int].self, forKey: .byKind) ?? [:]
            }
        }

        struct Camera: Codable, Hashable, Sendable {
            var cameraId: String
            var name: String
            var alarms: Int
            var topKind: String?
            var alertOn: AlertOn
        }

        struct KindShare: Codable, Hashable, Sendable {
            var kind: String
            var alarms: Int
            /// 0...1 of the range's alarms.
            var share: Double
        }

        var from: UnixMillis
        var to: UnixMillis
        var totals: Totals
        /// Every local day in the range, oldest first, zero-filled.
        @NullableArray var daily: [Day]
        /// Up to 10 cameras, most alarms first.
        @NullableArray var cameras: [Camera]
        /// Alarm kinds, most alarms first.
        @NullableArray var mix: [KindShare]
        /// At most 6, most urgent first.
        @NullableArray var recommendations: [Recommendation]
    }

    struct Recommendation: Codable, Hashable, Identifiable, Sendable {
        struct Level: OpenStringEnum {
            let rawValue: String
            init(rawValue: String) { self.rawValue = rawValue }

            static let attention = Level(rawValue: "attention")
            static let suggestion = Level(rawValue: "suggestion")
            static let info = Level(rawValue: "info")
        }

        /// Stable per rule and camera, e.g. `motion-only:cam-7`.
        var id: String
        var level: Level
        var title: String
        var detail: String
        var cameraId: String?
        var action: RecommendationAction?
    }

    struct RecommendationAction: Codable, Hashable, Sendable {
        struct ActionType: OpenStringEnum {
            let rawValue: String
            init(rawValue: String) { self.rawValue = rawValue }

            static let setAlertOn = ActionType(rawValue: "setAlertOn")
            static let openTemplate = ActionType(rawValue: "openTemplate")
            static let openCamera = ActionType(rawValue: "openCamera")
            static let openStorage = ActionType(rawValue: "openStorage")
        }

        var type: ActionType
        /// For setAlertOn and openCamera.
        var cameraId: String?
        var alertOn: AlertOn?
        /// For openTemplate.
        var templateId: String?
    }

    /// `PATCH /api/v2/cameras/{cameraId}` with only the alert setting; absent fields are kept.
    struct CameraAlertOnPatch: Codable, Hashable, Sendable {
        var alertOn: AlertOn
    }
}
