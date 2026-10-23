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

public enum V2 {}

/// Unix time in milliseconds, as used throughout the API.
public typealias UnixMillis = Int64

public extension Date {
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
public struct NullableArray<Element: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
    public var wrappedValue: [Element]

    public init(wrappedValue: [Element]) {
        self.wrappedValue = wrappedValue
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        wrappedValue = container.decodeNil() ? [] : try container.decode([Element].self)
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(wrappedValue)
    }
}

public extension KeyedDecodingContainer {
    func decode<T>(_ type: NullableArray<T>.Type, forKey key: Key) throws -> NullableArray<T> {
        try decodeIfPresent(type, forKey: key) ?? NullableArray(wrappedValue: [])
    }
}

/// A string enum that keeps unknown values instead of failing the whole
/// response (the server can add an alarm kind or a permission before this
/// client knows it).
public protocol OpenStringEnum: RawRepresentable, Codable, Hashable, Sendable where RawValue == String {
    init(rawValue: String)
}

public extension OpenStringEnum {
    init(from decoder: Decoder) throws {
        self.init(rawValue: try decoder.singleValueContainer().decode(String.self))
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }
}

// MARK: - Errors and lists

public extension V2 {
    /// `{"error": {"code": "...", "message": "..."}}`
    struct ErrorEnvelope: Codable, Hashable, Sendable {
        public struct Body: Codable, Hashable, Sendable {
            public var code: String
            public var message: String
        }
        public var error: Body
    }

    /// `{"items": [...]}`
    struct Items<Item: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
        @NullableArray public var items: [Item]

        public init(items: [Item]) {
            self.items = items
        }
    }

    /// `{"items": [...], "nextCursor": "..."}` — `nextCursor` absent on the last page.
    struct Page<Item: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
        @NullableArray public var items: [Item]
        public var nextCursor: String?

        public init(items: [Item], nextCursor: String? = nil) {
            self.items = items
            self.nextCursor = nextCursor
        }
    }
}

// MARK: - Auth

public extension V2 {
    struct Role: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let admin = Role(rawValue: "Admin")
        public static let supervisor = Role(rawValue: "Supervisor")
        public static let `operator` = Role(rawValue: "Operator")
        public static let viewer = Role(rawValue: "Viewer")
        public static let all: [Role] = [.admin, .supervisor, .operator, .viewer]
    }

    struct Permission: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let manageCameras = Permission(rawValue: "manageCameras")
        public static let manageUsers = Permission(rawValue: "manageUsers")
        public static let changeSettings = Permission(rawValue: "changeSettings")
        public static let viewAudit = Permission(rawValue: "viewAudit")
        public static let acknowledgeAlarms = Permission(rawValue: "acknowledgeAlarms")
        public static let exportEvidence = Permission(rawValue: "exportEvidence")
        public static let viewLive = Permission(rawValue: "viewLive")
    }

    struct UserScope: Codable, Hashable, Sendable {
        public var allCameras: Bool
        @NullableArray public var groupIds: [String]

        public init(allCameras: Bool, groupIds: [String] = []) {
            self.allCameras = allCameras
            self.groupIds = groupIds
        }
    }

    struct User: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var role: Role
        public var createdAt: UnixMillis
        /// `local`, `ldap` or `oidc`.
        public var source: String
        public var scope: UserScope
    }

    /// `Me` = `User` + `permissions`.
    struct Me: Codable, Hashable, Sendable {
        public var id: String
        public var name: String
        public var role: Role
        public var createdAt: UnixMillis
        public var source: String
        public var scope: UserScope
        @NullableArray public var permissions: [Permission]

        public func can(_ permission: Permission) -> Bool {
            permissions.contains(permission)
        }

        public var user: User {
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

        public func encode(to encoder: Encoder) throws {
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
        public var tokenType: String
        public var accessToken: String
        public var expiresAt: UnixMillis
        public var refreshToken: String
        public var refreshExpiresAt: UnixMillis
        public var user: User
    }

    struct AuthProviders: Codable, Hashable, Sendable {
        public struct OIDC: Codable, Hashable, Sendable {
            public var enabled: Bool
            public var displayName: String?
        }
        public var local: Bool
        public var ldap: Bool
        public var oidc: OIDC
    }
}

// MARK: - Video

public extension V2 {
    struct VideoRight: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let live = VideoRight(rawValue: "live")
        public static let playback = VideoRight(rawValue: "playback")
        public static let export = VideoRight(rawValue: "export")
    }

    struct VideoTokenRequest: Codable, Hashable, Sendable {
        public var rights: [VideoRight]
        public var recorderId: String?

        public init(rights: [VideoRight], recorderId: String? = nil) {
            self.rights = rights
            self.recorderId = recorderId
        }
    }

    struct VideoToken: Codable, Hashable, Sendable {
        public var token: String
        /// `{recorderUrl}/v/{token}` — may be a path (`/v/…`) when the recording
        /// server's public URL isn't known; resolve against the site address.
        public var baseUrl: String
        /// `/v/{token}` on the management server (works wherever the API does).
        public var gatewayUrl: String
        public var expiresAt: UnixMillis
        @NullableArray public var rights: [VideoRight]
        public var cameraId: String
        public var recorderId: String
    }
}

// MARK: - Cameras

public extension V2 {
    struct AlertOn: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let auto = AlertOn(rawValue: "auto")
        public static let motion = AlertOn(rawValue: "motion")
        public static let peopleAndVehicles = AlertOn(rawValue: "peopleAndVehicles")
        public static let people = AlertOn(rawValue: "people")
    }

    /// How a camera records (wave 22). `default` follows the server setting.
    struct RecordingMode: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let `default` = RecordingMode(rawValue: "default")
        public static let continuous = RecordingMode(rawValue: "continuous")
        public static let events = RecordingMode(rawValue: "events")
        public static let dual = RecordingMode(rawValue: "dual")
        public static let off = RecordingMode(rawValue: "off")
    }

    /// What opens a keep window in events/dual mode (wave 22). `default` follows the server setting.
    struct RecordTrigger: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let `default` = RecordTrigger(rawValue: "default")
        public static let motion = RecordTrigger(rawValue: "motion")
        public static let objects = RecordTrigger(rawValue: "objects")
    }

    struct CameraStatus: Codable, Hashable, Sendable {
        public var online: Bool
        public var viewers: Int
        public var width: Int?
        public var height: Int?
        public var analytics: String?
        /// "Online | No signal | Media engine starting | Recording server offline"
        public var message: String

        public init(online: Bool, viewers: Int = 0, width: Int? = nil, height: Int? = nil, analytics: String? = nil, message: String) {
            self.online = online
            self.viewers = viewers
            self.width = width
            self.height = height
            self.analytics = analytics
            self.message = message
        }
    }

    struct Camera: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var location: String
        public var rtspUrl: String
        public var subRtspUrl: String
        public var username: String
        public var hasPassword: Bool
        public var recording: Bool
        public var retentionDays: Int
        public var fps: Int
        public var motionLevel: Int
        public var alertOn: AlertOn
        public var onvifUrl: String
        public var cameraAnalytics: Bool
        public var sortOrder: Int
        public var createdAt: UnixMillis
        public var recorderId: String
        @NullableArray public var groupIds: [String]
        public var status: CameraStatus
        // Wave 11 (waves 7–9 parity). Optional: older servers omit them.
        /// Enabled privacy-mask polygons ([x, y] vertices, 0–1 of the full frame) to black out over video.
        public var masks: [[[Double]]]? // WAVE11(A): polygons of [x, y] (the server sends [][][2]float64); [[Double]] failed to decode any camera with a mask
        /// The camera offers ONVIF Profile G: its SD card can fill recording gaps (edge recovery).
        public var edgeRecording: Bool?
        /// The camera offers an ONVIF audio backchannel (G.711 µ-law) for talk-down.
        public var talkback: Bool?
        public var audioDetect: Bool?
        /// dB over the rolling noise floor that counts as a loud noise (6–60).
        public var audioThresholdDb: Int?
        /// Seconds a person track stays before a Loitering event; 0 or absent = off (wave 16).
        public var loiterSeconds: Int?
        // Wave 22 recording modes. Optional: older servers omit them (then `recording` is the whole story).
        public var recordingMode: RecordingMode?
        /// Read-only: what the recorder actually does (never `default`).
        public var effectiveRecordingMode: RecordingMode?
        public var recordTrigger: RecordTrigger?
        /// 0 = server default.
        public var preRollSeconds: Int?
        /// 0 = server default.
        public var postRollSeconds: Int?
        /// Low-res (dual) retention; 0 = server default.
        public var lowResRetentionDays: Int?

        public var resolutionLabel: String {
            if let w = status.width, let h = status.height, w > 0, h > 0 { return "\(w)×\(h)" }
            return status.online ? "Live" : "—"
        }
    }

    struct CameraInput: Codable, Hashable, Sendable {
        public var name: String
        public var location: String?
        public var rtspUrl: String
        public var subRtspUrl: String?
        public var username: String?
        public var password: String?
        public var recording: Bool?
        public var retentionDays: Int?
        public var motionLevel: Int?
        public var alertOn: AlertOn?
        public var onvifUrl: String?
        public var cameraAnalytics: Bool?
        public var recorderId: String?
        public var groupIds: [String]?
        /// 0–3600 seconds, 0 = off (wave 16).
        public var loiterSeconds: Int?
        // Wave 22 (absent = server default).
        public var recordingMode: RecordingMode?
        public var recordTrigger: RecordTrigger?
        public var preRollSeconds: Int?
        public var postRollSeconds: Int?
        public var lowResRetentionDays: Int?

        public init(name: String, location: String? = nil, rtspUrl: String, subRtspUrl: String? = nil,
                    username: String? = nil, password: String? = nil, recording: Bool? = nil,
                    retentionDays: Int? = nil, motionLevel: Int? = nil, alertOn: AlertOn? = nil,
                    onvifUrl: String? = nil, cameraAnalytics: Bool? = nil, recorderId: String? = nil,
                    groupIds: [String]? = nil, loiterSeconds: Int? = nil,
                    recordingMode: RecordingMode? = nil, recordTrigger: RecordTrigger? = nil,
                    preRollSeconds: Int? = nil, postRollSeconds: Int? = nil, lowResRetentionDays: Int? = nil) {
            self.name = name
            self.location = location
            self.rtspUrl = rtspUrl
            self.subRtspUrl = subRtspUrl
            self.username = username
            self.password = password
            self.recording = recording
            self.retentionDays = retentionDays
            self.motionLevel = motionLevel
            self.alertOn = alertOn
            self.onvifUrl = onvifUrl
            self.cameraAnalytics = cameraAnalytics
            self.recorderId = recorderId
            self.groupIds = groupIds
            self.loiterSeconds = loiterSeconds
            self.recordingMode = recordingMode
            self.recordTrigger = recordTrigger
            self.preRollSeconds = preRollSeconds
            self.postRollSeconds = postRollSeconds
            self.lowResRetentionDays = lowResRetentionDays
        }
    }

    /// Absent fields are kept by the server (JSONEncoder omits nil optionals).
    struct CameraPatch: Codable, Hashable, Sendable {
        public var name: String?
        public var location: String?
        public var rtspUrl: String?
        public var subRtspUrl: String?
        public var username: String?
        public var password: String?
        public var recording: Bool?
        public var retentionDays: Int?
        public var motionLevel: Int?
        public var alertOn: AlertOn?
        public var onvifUrl: String?
        public var cameraAnalytics: Bool?
        public var sortOrder: Int?
        public var recorderId: String?
        public var groupIds: [String]?
        public var audioDetect: Bool? // wave 11
        public var audioThresholdDb: Int? // wave 11, 6–60
        public var loiterSeconds: Int? // wave 16, 0–3600, 0 = off
        // Wave 22. The server answers 400 for `dual` on a camera without a sub-stream.
        public var recordingMode: RecordingMode?
        public var recordTrigger: RecordTrigger?
        public var preRollSeconds: Int? // 0 = default, else 0–30
        public var postRollSeconds: Int? // 0 = default, else 5–120
        public var lowResRetentionDays: Int? // 0 = default, max 365

        public init() {}
    }
}

// MARK: - Recordings

public extension V2 {
    struct SpanQuality: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let full = SpanQuality(rawValue: "full")
        public static let low = SpanQuality(rawValue: "low")
    }

    /// `GET/PATCH /api/v2/settings/recording` (wave 22): the server-wide
    /// recording defaults. Fields the server leaves out take the documented defaults.
    struct RecordingSettings: Codable, Hashable, Sendable {
        public var defaultMode: RecordingMode
        public var defaultTrigger: RecordTrigger
        public var defaultPreRollSeconds: Int
        public var defaultPostRollSeconds: Int
        public var defaultRetentionDays: Int
        public var defaultLowResRetentionDays: Int
        /// 0 = off, else 5–50: delete the oldest video when free space drops under this.
        public var minFreePercent: Int

        public init(defaultMode: RecordingMode = .continuous, defaultTrigger: RecordTrigger = .motion,
                    defaultPreRollSeconds: Int = 5, defaultPostRollSeconds: Int = 15,
                    defaultRetentionDays: Int = 7, defaultLowResRetentionDays: Int = 14, minFreePercent: Int = 0) {
            self.defaultMode = defaultMode
            self.defaultTrigger = defaultTrigger
            self.defaultPreRollSeconds = defaultPreRollSeconds
            self.defaultPostRollSeconds = defaultPostRollSeconds
            self.defaultRetentionDays = defaultRetentionDays
            self.defaultLowResRetentionDays = defaultLowResRetentionDays
            self.minFreePercent = minFreePercent
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            let d = RecordingSettings()
            defaultMode = try c.decodeIfPresent(RecordingMode.self, forKey: .defaultMode) ?? d.defaultMode
            defaultTrigger = try c.decodeIfPresent(RecordTrigger.self, forKey: .defaultTrigger) ?? d.defaultTrigger
            defaultPreRollSeconds = try c.decodeIfPresent(Int.self, forKey: .defaultPreRollSeconds) ?? d.defaultPreRollSeconds
            defaultPostRollSeconds = try c.decodeIfPresent(Int.self, forKey: .defaultPostRollSeconds) ?? d.defaultPostRollSeconds
            defaultRetentionDays = try c.decodeIfPresent(Int.self, forKey: .defaultRetentionDays) ?? d.defaultRetentionDays
            defaultLowResRetentionDays = try c.decodeIfPresent(Int.self, forKey: .defaultLowResRetentionDays) ?? d.defaultLowResRetentionDays
            minFreePercent = try c.decodeIfPresent(Int.self, forKey: .minFreePercent) ?? d.minFreePercent
        }
    }

    /// `PATCH /api/v2/settings/recording`: absent fields are kept.
    struct RecordingSettingsPatch: Codable, Hashable, Sendable {
        public var defaultMode: RecordingMode?
        public var defaultTrigger: RecordTrigger?
        public var defaultPreRollSeconds: Int?
        public var defaultPostRollSeconds: Int?
        public var defaultRetentionDays: Int?
        public var defaultLowResRetentionDays: Int?
        public var minFreePercent: Int?

        public init() {}

        public var isEmpty: Bool { self == RecordingSettingsPatch() }
    }

    struct Span: Codable, Hashable, Sendable {
        public var start: UnixMillis
        public var end: UnixMillis
        /// Seconds into the playback playlist for the same window (of the same recorderId).
        public var media: Double?
        /// Recording server that has this footage.
        public var recorderId: String?
        /// Footage recovered from the camera's SD card (edge recovery); absent = recorded live. Wave 11.
        public var edge: Bool?
        /// "full" | "low" (wave 22: the dual mode's 24/7 sub-stream); absent = full.
        public var quality: SpanQuality?

        public init(start: UnixMillis, end: UnixMillis, media: Double? = nil, recorderId: String? = nil, edge: Bool? = nil,
                    quality: SpanQuality? = nil) {
            self.start = start
            self.end = end
            self.media = media
            self.recorderId = recorderId
            self.edge = edge
            self.quality = quality
        }

        /// Low-res footage (drawn lighter on the timeline).
        public var isLowRes: Bool { quality == .low }

        public func contains(_ t: UnixMillis) -> Bool { t >= start && t < end }
    }

    struct Timeline: Codable, Hashable, Sendable {
        public var from: UnixMillis
        public var to: UnixMillis
        public var now: UnixMillis
        @NullableArray public var spans: [Span]
        @NullableArray public var pending: [Span]
        public var writing: Bool
        @NullableArray public var events: [Event]
        @NullableArray public var alarms: [Alarm]
    }

}

public extension V2.Timeline {
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

public extension V2 {
    struct Segment: Codable, Hashable, Sendable {
        public var name: String
        public var start: UnixMillis
        public var end: UnixMillis
        public var size: Int64
        public var writing: Bool
        public var recorderId: String?
    }
}

// MARK: - Alarms and events

public extension V2 {
    /// Same raw values as the app's `AlertState` (New, Acknowledged, …, False Alarm).
    typealias AlarmState = AlertState
    /// Critical, Warning, Info.
    typealias Severity = AlertSeverity

    struct Instruction: Codable, Hashable, Sendable {
        public var rule: String
        public var text: String
    }

    struct Alarm: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var cameraId: String?
        public var cameraName: String?
        public var kind: String
        public var title: String
        public var detail: String
        public var severity: Severity
        public var state: AlarmState
        public var owner: String
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis
        public var lastEventAt: UnixMillis
        public var eventCount: Int
        public var eventId: String?
        @NullableArray public var instructions: [Instruction]
        @NullableArray public var responseLog: [String]

        public var isOpen: Bool { state.isOpen }
    }

    struct AlarmStateChange: Codable, Hashable, Sendable {
        public var state: AlarmState
        public var note: String?
        public var snoozeMinutes: Int?

        public init(state: AlarmState, note: String? = nil, snoozeMinutes: Int? = nil) {
            self.state = state
            self.note = note
            self.snoozeMinutes = snoozeMinutes
        }
    }

    struct Evidence: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var caseId: String
        public var alarmId: String?
        public var cameraId: String
        public var cameraName: String
        public var title: String
        public var range: String
        public var size: Int64
        public var sha256: String
        public var lockedAt: UnixMillis
        public var lockedBy: String
        public var recorderId: String?
    }

    struct Event: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var cameraId: String
        public var kind: String
        public var label: String?
        public var score: Double
        /// Normalized [x1, y1, x2, y2].
        public var box: [Double]?
        public var createdAt: UnixMillis
        public var hasThumbnail: Bool
        public var alarmId: String?
        public var description: String?
        /// none, low, elevated, high
        public var threat: String?
        public var anomaly: Bool?
        public var reason: String?
        public var tags: [String]?
        // Wave 16; older servers omit them.
        /// The object track this detection belongs to; events of one track share it.
        public var trackId: String?
        /// The matched person (faces).
        public var personId: String?
        public var personName: String?
        /// The plate read on a vehicle event, normalized A–Z0–9.
        public var plate: String?
    }
}

// MARK: - Rules

public extension V2 {
    /// Something a rule does when it fires, besides its alarm behavior.
    struct RuleAction: Codable, Hashable, Sendable {
        /// "webhook".
        public var type: String
        public var webhookId: String

        public init(type: String = "webhook", webhookId: String) {
            self.type = type
            self.webhookId = webhookId
        }
    }

    struct RuleInput: Codable, Hashable, Sendable {
        public var name: String
        public var enabled: Bool
        /// Empty = every camera.
        public var cameraIds: [String]
        /// Empty = any alarm.
        public var kinds: [String]
        /// Info, Warning or Critical.
        public var minSeverity: String
        public var schedule: Bool
        public var fromMinute: Int
        public var toMinute: Int
        /// 0 = Sunday … 6 = Saturday.
        public var days: [Int]
        public var delivery: String
        public var instructions: String
        /// Outside this time profile the rule doesn't fire. A PUT rebuilds the
        /// rule from the body, so these two must round-trip or editing clears them.
        public var timeProfileId: String?
        public var actions: [RuleAction]?

        public init(name: String, enabled: Bool = true, cameraIds: [String] = [], kinds: [String] = [],
                    minSeverity: String = "Info", schedule: Bool = false, fromMinute: Int = 0,
                    toMinute: Int = 0, days: [Int] = [], delivery: String = "", instructions: String = "",
                    timeProfileId: String? = nil, actions: [RuleAction]? = nil) {
            self.name = name
            self.enabled = enabled
            self.cameraIds = cameraIds
            self.kinds = kinds
            self.minSeverity = minSeverity
            self.schedule = schedule
            self.fromMinute = fromMinute
            self.toMinute = toMinute
            self.days = days
            self.delivery = delivery
            self.instructions = instructions
            self.timeProfileId = timeProfileId
            self.actions = actions
        }

        public init(_ rule: Rule) {
            self.init(name: rule.name, enabled: rule.enabled, cameraIds: rule.cameraIds, kinds: rule.kinds,
                      minSeverity: rule.minSeverity, schedule: rule.schedule, fromMinute: rule.fromMinute,
                      toMinute: rule.toMinute, days: rule.days, delivery: rule.delivery,
                      instructions: rule.instructions, timeProfileId: rule.timeProfileId, actions: rule.actions)
        }

        private enum CodingKeys: String, CodingKey {
            case name, enabled, cameraIds, kinds, minSeverity, schedule, fromMinute, toMinute, days, delivery, instructions
            case timeProfileId, actions
        }

        // Drafts and templates come from Go structs, whose nil slices arrive as null.
        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = try c.decode(String.self, forKey: .name)
            enabled = try c.decode(Bool.self, forKey: .enabled)
            cameraIds = try c.decodeIfPresent([String].self, forKey: .cameraIds) ?? []
            kinds = try c.decodeIfPresent([String].self, forKey: .kinds) ?? []
            minSeverity = try c.decode(String.self, forKey: .minSeverity)
            schedule = try c.decode(Bool.self, forKey: .schedule)
            fromMinute = try c.decode(Int.self, forKey: .fromMinute)
            toMinute = try c.decode(Int.self, forKey: .toMinute)
            days = try c.decodeIfPresent([Int].self, forKey: .days) ?? []
            delivery = try c.decode(String.self, forKey: .delivery)
            instructions = try c.decode(String.self, forKey: .instructions)
            timeProfileId = try c.decodeIfPresent(String.self, forKey: .timeProfileId)
            actions = try c.decodeIfPresent([RuleAction].self, forKey: .actions)
        }

        /// The optional fields are left out when empty, as the server writes them.
        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(name, forKey: .name)
            try c.encode(enabled, forKey: .enabled)
            try c.encode(cameraIds, forKey: .cameraIds)
            try c.encode(kinds, forKey: .kinds)
            try c.encode(minSeverity, forKey: .minSeverity)
            try c.encode(schedule, forKey: .schedule)
            try c.encode(fromMinute, forKey: .fromMinute)
            try c.encode(toMinute, forKey: .toMinute)
            try c.encode(days, forKey: .days)
            try c.encode(delivery, forKey: .delivery)
            try c.encode(instructions, forKey: .instructions)
            if let timeProfileId, timeProfileId.isEmpty == false { try c.encode(timeProfileId, forKey: .timeProfileId) }
            if let actions, actions.isEmpty == false { try c.encode(actions, forKey: .actions) }
        }
    }

    /// `Rule` = `RuleInput` + id and times.
    struct Rule: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis
        public var name: String
        public var enabled: Bool
        @NullableArray public var cameraIds: [String]
        @NullableArray public var kinds: [String]
        public var minSeverity: String
        public var schedule: Bool
        public var fromMinute: Int
        public var toMinute: Int
        @NullableArray public var days: [Int]
        public var delivery: String
        public var instructions: String
        public var timeProfileId: String?
        public var actions: [RuleAction]?
    }
}

// MARK: - Users, groups, servers, audit

public extension V2 {
    struct NewUser: Codable, Hashable, Sendable {
        public var name: String
        public var role: Role
        public var password: String
        public var scope: UserScope?

        public init(name: String, role: Role, password: String, scope: UserScope? = nil) {
            self.name = name
            self.role = role
            self.password = password
            self.scope = scope
        }
    }

    struct UserPatch: Codable, Hashable, Sendable {
        public var role: Role?
        public var password: String?
        public var scope: UserScope?

        public init(role: Role? = nil, password: String? = nil, scope: UserScope? = nil) {
            self.role = role
            self.password = password
            self.scope = scope
        }
    }

    struct CameraGroup: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        @NullableArray public var cameraIds: [String]
        public var createdAt: UnixMillis
    }

    struct RecordingServer: Codable, Hashable, Identifiable, Sendable {
        public struct Storage: Codable, Hashable, Sendable {
            public var diskFreeBytes: Int64?
            public var diskTotalBytes: Int64?
            public var recordingsBytes: Int64
        }
        public struct Capabilities: Codable, Hashable, Sendable {
            public var detection: Bool
            public var objects: Bool
            public var webrtc: Bool
        }
        public var id: String
        public var name: String
        public var local: Bool
        public var publicUrl: String
        public var enrolledAt: UnixMillis
        public var lastSeenAt: UnixMillis
        public var revoked: Bool
        public var online: Bool
        public var storage: Storage?
        public var version: String
        public var configRev: Int64
        public var mediaRunning: Bool
        public var mediaError: String?
        public var cameras: Int
        public var capabilities: Capabilities

        private enum CodingKeys: String, CodingKey {
            case id, name, local, publicUrl, enrolledAt, lastSeenAt, revoked, online, storage, version
            case configRev, mediaRunning, mediaError, cameras, capabilities
        }

        /// The registry fields (`local`, `publicUrl`, `enrolledAt`,
        /// `lastSeenAt`, `revoked`) came with phase 2; a server that predates
        /// them still lists its one built-in Recording Server.
        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            name = try c.decode(String.self, forKey: .name)
            local = try c.decodeIfPresent(Bool.self, forKey: .local) ?? (id == "local")
            publicUrl = try c.decodeIfPresent(String.self, forKey: .publicUrl) ?? ""
            enrolledAt = try c.decodeIfPresent(UnixMillis.self, forKey: .enrolledAt) ?? 0
            lastSeenAt = try c.decodeIfPresent(UnixMillis.self, forKey: .lastSeenAt) ?? 0
            revoked = try c.decodeIfPresent(Bool.self, forKey: .revoked) ?? false
            online = try c.decode(Bool.self, forKey: .online)
            storage = try c.decodeIfPresent(Storage.self, forKey: .storage)
            version = try c.decode(String.self, forKey: .version)
            configRev = try c.decode(Int64.self, forKey: .configRev)
            mediaRunning = try c.decode(Bool.self, forKey: .mediaRunning)
            mediaError = try c.decodeIfPresent(String.self, forKey: .mediaError)
            cameras = try c.decode(Int.self, forKey: .cameras)
            capabilities = try c.decode(Capabilities.self, forKey: .capabilities)
        }
    }

    struct AuditEntry: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var at: UnixMillis
        public var user: String
        public var area: String
        public var action: String
        public var detail: String
        public var serverId: String
    }
}

public extension V2 {
    struct AuditVerify: Codable, Hashable, Sendable {
        public struct ServerChain: Codable, Hashable, Sendable {
            public var serverId: String
            public var ok: Bool
            public var entries: Int
        }
        public var ok: Bool
        public var entries: Int
        public var brokenAt: String?
        @NullableArray public var servers: [ServerChain]
    }
}

// MARK: - Saved views

public extension V2 {
    struct ViewTile: Codable, Hashable, Sendable {
        public var cameraId: String
        public var x: Int?
        public var y: Int?
        public var w: Int?
        public var h: Int?

        public init(cameraId: String, x: Int? = nil, y: Int? = nil, w: Int? = nil, h: Int? = nil) {
            self.cameraId = cameraId
            self.x = x
            self.y = y
            self.w = w
            self.h = h
        }
    }

    struct ViewInput: Codable, Hashable, Sendable {
        public var name: String
        public var columns: Int
        public var rows: Int
        public var tiles: [ViewTile]

        public init(name: String, columns: Int, rows: Int, tiles: [ViewTile]) {
            self.name = name
            self.columns = columns
            self.rows = rows
            self.tiles = tiles
        }
    }

    /// `View` = `ViewInput` + id and times.
    struct View: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis
        public var name: String
        public var columns: Int
        public var rows: Int
        @NullableArray public var tiles: [ViewTile]

        public var input: ViewInput {
            ViewInput(name: name, columns: columns, rows: rows, tiles: tiles)
        }
    }
}

// MARK: - Mac import

public extension V2 {
    struct MacImport: Codable, Hashable, Sendable {
        public struct Camera: Codable, Hashable, Sendable {
            public var sourceId: String
            public var name: String
            public var location: String?
            public var rtspUrl: String
            public var subRtspUrl: String?
            public var username: String?
            public var password: String?
            public var recording: Bool?
            public var retentionDays: Int?
            public var motionLevel: Int?
            public var onvifUrl: String?
            public var recordingsPath: String?
            /// continuous | events | dual (wave 22); nil = the server default
            /// when recording, off when not.
            public var recordingMode: String?

            public init(sourceId: String, name: String, location: String? = nil, rtspUrl: String,
                        subRtspUrl: String? = nil, username: String? = nil, password: String? = nil,
                        recording: Bool? = nil, retentionDays: Int? = nil, motionLevel: Int? = nil,
                        onvifUrl: String? = nil, recordingsPath: String? = nil, recordingMode: String? = nil) {
                self.recordingMode = recordingMode
                self.sourceId = sourceId
                self.name = name
                self.location = location
                self.rtspUrl = rtspUrl
                self.subRtspUrl = subRtspUrl
                self.username = username
                self.password = password
                self.recording = recording
                self.retentionDays = retentionDays
                self.motionLevel = motionLevel
                self.onvifUrl = onvifUrl
                self.recordingsPath = recordingsPath
            }
        }

        public struct User: Codable, Hashable, Sendable {
            public var name: String
            public var role: Role
            /// bcrypt only; the Mac app's hashes aren't bcrypt, so this stays nil.
            public var passwordHash: String?

            public init(name: String, role: Role, passwordHash: String? = nil) {
                self.name = name
                self.role = role
                self.passwordHash = passwordHash
            }
        }

        public struct Evidence: Codable, Hashable, Sendable {
            public var caseId: String
            public var cameraSourceId: String?
            public var cameraName: String
            public var title: String
            public var range: String
            public var size: Int64
            public var sha256: String
            public var lockedAt: UnixMillis
            public var lockedBy: String

            public init(caseId: String, cameraSourceId: String? = nil, cameraName: String, title: String,
                        range: String, size: Int64, sha256: String, lockedAt: UnixMillis, lockedBy: String) {
                self.caseId = caseId
                self.cameraSourceId = cameraSourceId
                self.cameraName = cameraName
                self.title = title
                self.range = range
                self.size = size
                self.sha256 = sha256
                self.lockedAt = lockedAt
                self.lockedBy = lockedBy
            }
        }

        public struct AuditEntry: Codable, Hashable, Sendable {
            public var at: UnixMillis
            public var user: String
            public var area: String
            public var action: String
            public var detail: String

            public init(at: UnixMillis, user: String, area: String, action: String, detail: String) {
                self.at = at
                self.user = user
                self.area = area
                self.action = action
                self.detail = detail
            }
        }

        public var sourceVersion: String
        public var cameras: [Camera]
        public var users: [User]
        public var rules: [RuleInput]
        public var evidence: [Evidence]
        public var audit: [AuditEntry]

        public init(sourceVersion: String, cameras: [Camera] = [], users: [User] = [], rules: [RuleInput] = [],
                    evidence: [Evidence] = [], audit: [AuditEntry] = []) {
            self.sourceVersion = sourceVersion
            self.cameras = cameras
            self.users = users
            self.rules = rules
            self.evidence = evidence
            self.audit = audit
        }
    }

    struct MacImportResult: Codable, Hashable, Sendable {
        public var cameras: Int
        public var users: Int
        public var rules: Int
        public var evidence: Int
        public var audit: Int
        @NullableArray public var skipped: [String]
        /// Mac sourceId → new camera id.
        public var cameraIds: [String: String]

        public init(cameras: Int, users: Int, rules: Int, evidence: Int, audit: Int, skipped: [String], cameraIds: [String: String]) {
            self.cameras = cameras
            self.users = users
            self.rules = rules
            self.evidence = evidence
            self.audit = audit
            self.skipped = skipped
            self.cameraIds = cameraIds
        }
    }
}
