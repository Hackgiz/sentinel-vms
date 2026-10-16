import Foundation

// Wave 6 management DTOs (`WAVE6-ADMIN.md`): site info, roles + permission
// catalog, time profiles, webhooks, monitor thresholds, tasks, server logs,
// plus the v1 push / MQTT settings the Notifications pane regroups.
//
// Where the contract leaves a field's exact spelling open the decoders are
// lenient (missing keys fall back to empty values) so a small server-side
// difference doesn't blank a pane.

public extension V2 {
    // MARK: Site information (`GET/PUT /api/v2/site`)

    struct SiteInfo: Codable, Hashable, Sendable {
        public var name: String
        public var description: String
        public var contact: String

        public init(name: String = "", description: String = "", contact: String = "") {
            self.name = name
            self.description = description
            self.contact = contact
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            description = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
            contact = try c.decodeIfPresent(String.self, forKey: .contact) ?? ""
        }
    }

    // MARK: Roles (`/api/v2/roles`, `/api/v2/permissions`)

    /// A role row. Named `RoleRecord` because `V2.Role` is the open enum of
    /// the four built-in names on users.
    struct RoleRecord: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var description: String
        public var builtin: Bool
        /// Stable permission ids.
        public var permissions: [String]
        public var userCount: Int

        public init(id: String, name: String, description: String = "", builtin: Bool = false,
                    permissions: [String] = [], userCount: Int = 0) {
            self.id = id
            self.name = name
            self.description = description
            self.builtin = builtin
            self.permissions = permissions
            self.userCount = userCount
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
            description = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
            builtin = try c.decodeIfPresent(Bool.self, forKey: .builtin) ?? false
            permissions = try c.decodeIfPresent([String].self, forKey: .permissions) ?? []
            userCount = try c.decodeIfPresent(Int.self, forKey: .userCount) ?? 0
        }
    }

    /// Body of `POST /api/v2/roles` and `PUT /api/v2/roles/{id}`.
    struct RoleInput: Codable, Hashable, Sendable {
        public var name: String
        public var description: String
        public var permissions: [String]

        public init(name: String, description: String, permissions: [String]) {
            self.name = name
            self.description = description
            self.permissions = permissions
        }
    }

    /// One entry of the permission catalog (`GET /api/v2/permissions`).
    struct PermissionInfo: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var label: String
        public var group: String
        public var description: String

        public init(id: String, label: String, group: String = "", description: String = "") {
            self.id = id
            self.label = label
            self.group = group
            self.description = description
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            label = try c.decodeIfPresent(String.self, forKey: .label) ?? id
            group = try c.decodeIfPresent(String.self, forKey: .group) ?? ""
            description = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
        }
    }

    // MARK: Time profiles (`/api/v2/time-profiles`)

    /// One weekly window: days 0 (Sunday) … 6, "HH:MM" start/end in the
    /// server's local time. An end at or before the start runs past midnight
    /// into the next day.
    struct TimeWindow: Codable, Hashable, Sendable {
        public var days: [Int]
        public var start: String
        public var end: String

        public init(days: [Int], start: String, end: String) {
            self.days = days
            self.start = start
            self.end = end
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            days = try c.decodeIfPresent([Int].self, forKey: .days) ?? []
            start = try c.decodeIfPresent(String.self, forKey: .start) ?? "00:00"
            end = try c.decodeIfPresent(String.self, forKey: .end) ?? "00:00"
        }
    }

    struct TimeProfile: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var weekly: [TimeWindow]

        public init(id: String, name: String, weekly: [TimeWindow] = []) {
            self.id = id
            self.name = name
            self.weekly = weekly
        }

        private struct Body: Codable { var weekly: [TimeWindow]? }

        private enum CodingKeys: String, CodingKey { case id, name, weekly, body }

        /// Accepts `weekly` at the top level or inside a `body` object.
        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
            if let direct = try c.decodeIfPresent([TimeWindow].self, forKey: .weekly) {
                weekly = direct
            } else {
                weekly = try c.decodeIfPresent(Body.self, forKey: .body)?.weekly ?? []
            }
        }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(id, forKey: .id)
            try c.encode(name, forKey: .name)
            try c.encode(weekly, forKey: .weekly)
        }
    }

    struct TimeProfileInput: Codable, Hashable, Sendable {
        public var name: String
        public var weekly: [TimeWindow]

        public init(name: String, weekly: [TimeWindow]) {
            self.name = name
            self.weekly = weekly
        }
    }

    // MARK: Webhooks (`/api/v2/webhooks`)

    /// A webhook as the server reports it. The secret is write-only: the
    /// server says only whether one is set.
    struct Webhook: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var url: String
        public var events: [String]
        public var enabled: Bool
        public var hasSecret: Bool
        /// HTTP status of the last delivery (0 = never fired / network error).
        public var lastStatus: Int
        public var lastFiredAt: UnixMillis

        public init(id: String, name: String, url: String, events: [String] = [], enabled: Bool = true,
                    hasSecret: Bool = false, lastStatus: Int = 0, lastFiredAt: UnixMillis = 0) {
            self.id = id
            self.name = name
            self.url = url
            self.events = events
            self.enabled = enabled
            self.hasSecret = hasSecret
            self.lastStatus = lastStatus
            self.lastFiredAt = lastFiredAt
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            url = try c.decodeIfPresent(String.self, forKey: .url) ?? ""
            events = try c.decodeIfPresent([String].self, forKey: .events) ?? []
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
            hasSecret = try c.decodeIfPresent(Bool.self, forKey: .hasSecret) ?? false
            lastStatus = try c.decodeIfPresent(Int.self, forKey: .lastStatus) ?? 0
            lastFiredAt = try c.decodeIfPresent(UnixMillis.self, forKey: .lastFiredAt) ?? 0
        }
    }

    /// Body of `POST`/`PUT /api/v2/webhooks`. `secret == nil` keeps the saved
    /// one; `""` clears it.
    struct WebhookInput: Codable, Hashable, Sendable {
        public var name: String
        public var url: String
        public var events: [String]
        public var enabled: Bool
        public var secret: String?

        public init(name: String, url: String, events: [String], enabled: Bool, secret: String? = nil) {
            self.name = name
            self.url = url
            self.events = events
            self.enabled = enabled
            self.secret = secret
        }

        private enum CodingKeys: String, CodingKey { case name, url, events, enabled, secret }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(name, forKey: .name)
            try c.encode(url, forKey: .url)
            try c.encode(events, forKey: .events)
            try c.encode(enabled, forKey: .enabled)
            try c.encodeIfPresent(secret, forKey: .secret)
        }
    }

    /// Answer of `POST /api/v2/webhooks/{id}/test`.
    struct WebhookTestResult: Codable, Hashable, Sendable {
        public var ok: Bool
        public var status: Int
        public var error: String?

        public init(ok: Bool, status: Int = 0, error: String? = nil) {
            self.ok = ok
            self.status = status
            self.error = error
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            status = try c.decodeIfPresent(Int.self, forKey: .status) ?? 0
            error = try c.decodeIfPresent(String.self, forKey: .error)
            ok = try c.decodeIfPresent(Bool.self, forKey: .ok) ?? ((200..<300).contains(status) && error == nil)
        }
    }

    // MARK: Monitor thresholds (`/api/v2/system/thresholds`)

    struct ThresholdPair: Codable, Hashable, Sendable {
        public var amber: Double
        public var red: Double

        public init(amber: Double, red: Double) {
            self.amber = amber
            self.red = red
        }
    }

    struct MonitorThresholds: Codable, Hashable, Sendable {
        public var cpu: ThresholdPair
        public var mem: ThresholdPair
        public var disk: ThresholdPair

        public static let defaults = MonitorThresholds(
            cpu: ThresholdPair(amber: 80, red: 95),
            mem: ThresholdPair(amber: 80, red: 95),
            disk: ThresholdPair(amber: 80, red: 95))

        public init(cpu: ThresholdPair, mem: ThresholdPair, disk: ThresholdPair) {
            self.cpu = cpu
            self.mem = mem
            self.disk = disk
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            cpu = try c.decodeIfPresent(ThresholdPair.self, forKey: .cpu) ?? Self.defaults.cpu
            mem = try c.decodeIfPresent(ThresholdPair.self, forKey: .mem) ?? Self.defaults.mem
            disk = try c.decodeIfPresent(ThresholdPair.self, forKey: .disk) ?? Self.defaults.disk
        }
    }

    // MARK: Current tasks (`GET /api/v2/system/tasks`)

    struct TaskState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }
        public static let running = TaskState(rawValue: "running")
        public static let done = TaskState(rawValue: "done")
        public static let failed = TaskState(rawValue: "failed")
    }

    struct SystemTask: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var kind: String
        public var title: String
        public var state: TaskState
        /// 0-100, nil = indeterminate.
        public var progress: Double?
        public var started: UnixMillis
        public var finished: UnixMillis?
        public var error: String?

        public init(id: String, kind: String = "", title: String, state: TaskState, progress: Double? = nil,
                    started: UnixMillis, finished: UnixMillis? = nil, error: String? = nil) {
            self.id = id
            self.kind = kind
            self.title = title
            self.state = state
            self.progress = progress
            self.started = started
            self.finished = finished
            self.error = error
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? kind
            state = try c.decodeIfPresent(TaskState.self, forKey: .state) ?? .running
            progress = try c.decodeIfPresent(Double.self, forKey: .progress)
            started = try c.decodeIfPresent(UnixMillis.self, forKey: .started) ?? 0
            finished = try c.decodeIfPresent(UnixMillis.self, forKey: .finished)
            error = try c.decodeIfPresent(String.self, forKey: .error)
        }
    }

    // MARK: Server logs (`GET /api/v2/system/logs`)

    struct LogLine: Codable, Hashable, Sendable {
        public var ts: UnixMillis
        public var level: String
        public var message: String
        public var server: String

        public init(ts: UnixMillis, level: String, message: String, server: String = "") {
            self.ts = ts
            self.level = level
            self.message = message
            self.server = server
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            ts = try c.decodeIfPresent(UnixMillis.self, forKey: .ts) ?? 0
            level = try c.decodeIfPresent(String.self, forKey: .level) ?? "info"
            message = try c.decodeIfPresent(String.self, forKey: .message) ?? ""
            server = try c.decodeIfPresent(String.self, forKey: .server) ?? ""
        }
    }

    // MARK: Push + MQTT (v1: `/api/v1/push`, `/api/v1/mqtt`)

    struct PushStatus: Codable, Hashable, Sendable {
        public var mode: String
        public var relayURL: String
        public var relayRegistered: Bool
        public var apnsKeyID: String
        public var apnsTeamID: String
        public var apnsTopic: String
        public var apnsHasKey: Bool
        public var ready: Bool
        public var quietEnabled: Bool
        /// Minutes after midnight.
        public var quietFrom: Int
        public var quietTo: Int
        public var devices: Int
        public var pushDevices: Int
        public var sent: Int
        public var failed: Int
        public var dropped: Int
        public var lastSentAt: Int64
        public var lastError: String

        public init(mode: String = "", relayURL: String = "", ready: Bool = false) {
            self.mode = mode
            self.relayURL = relayURL
            relayRegistered = false
            apnsKeyID = ""
            apnsTeamID = ""
            apnsTopic = ""
            apnsHasKey = false
            self.ready = ready
            quietEnabled = false
            quietFrom = 22 * 60
            quietTo = 7 * 60
            devices = 0
            pushDevices = 0
            sent = 0
            failed = 0
            dropped = 0
            lastSentAt = 0
            lastError = ""
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            func s(_ k: CodingKeys) throws -> String { try c.decodeIfPresent(String.self, forKey: k) ?? "" }
            func b(_ k: CodingKeys) throws -> Bool { try c.decodeIfPresent(Bool.self, forKey: k) ?? false }
            func i(_ k: CodingKeys) throws -> Int { try c.decodeIfPresent(Int.self, forKey: k) ?? 0 }
            mode = try s(.mode)
            relayURL = try s(.relayURL)
            relayRegistered = try b(.relayRegistered)
            apnsKeyID = try s(.apnsKeyID)
            apnsTeamID = try s(.apnsTeamID)
            apnsTopic = try s(.apnsTopic)
            apnsHasKey = try b(.apnsHasKey)
            ready = try b(.ready)
            quietEnabled = try b(.quietEnabled)
            quietFrom = try c.decodeIfPresent(Int.self, forKey: .quietFrom) ?? 22 * 60
            quietTo = try c.decodeIfPresent(Int.self, forKey: .quietTo) ?? 7 * 60
            devices = try i(.devices)
            pushDevices = try i(.pushDevices)
            sent = try i(.sent)
            failed = try i(.failed)
            dropped = try i(.dropped)
            lastSentAt = try c.decodeIfPresent(Int64.self, forKey: .lastSentAt) ?? 0
            lastError = try s(.lastError)
        }
    }

    /// Partial body of `PUT /api/v1/push`; nil fields keep the saved value.
    struct PushUpdate: Codable, Hashable, Sendable {
        public var mode: String?
        public var relayURL: String?
        public var apnsKey: String?
        public var apnsKeyID: String?
        public var apnsTeamID: String?
        public var apnsTopic: String?
        public var quietEnabled: Bool?
        public var quietFrom: Int?
        public var quietTo: Int?

        public init(mode: String? = nil, relayURL: String? = nil, apnsKey: String? = nil, apnsKeyID: String? = nil,
                    apnsTeamID: String? = nil, apnsTopic: String? = nil, quietEnabled: Bool? = nil,
                    quietFrom: Int? = nil, quietTo: Int? = nil) {
            self.mode = mode
            self.relayURL = relayURL
            self.apnsKey = apnsKey
            self.apnsKeyID = apnsKeyID
            self.apnsTeamID = apnsTeamID
            self.apnsTopic = apnsTopic
            self.quietEnabled = quietEnabled
            self.quietFrom = quietFrom
            self.quietTo = quietTo
        }
    }

    struct PushTestResult: Codable, Hashable, Sendable {
        public var sent: Int
        public var failed: [String]

        public init(sent: Int = 0, failed: [String] = []) {
            self.sent = sent
            self.failed = failed
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            sent = try c.decodeIfPresent(Int.self, forKey: .sent) ?? 0
            failed = try c.decodeIfPresent([String].self, forKey: .failed) ?? []
        }
    }

    struct MQTTStatus: Codable, Hashable, Sendable {
        public var state: String
        public var error: String
        public var connectedAt: Int64
        public var dropped: Int

        public init(state: String = "disabled") {
            self.state = state
            error = ""
            connectedAt = 0
            dropped = 0
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            state = try c.decodeIfPresent(String.self, forKey: .state) ?? "disabled"
            error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
            connectedAt = try c.decodeIfPresent(Int64.self, forKey: .connectedAt) ?? 0
            dropped = try c.decodeIfPresent(Int.self, forKey: .dropped) ?? 0
        }
    }

    /// `GET /api/v1/mqtt`. The password is write-only (`hasPassword`).
    struct MQTTSettings: Codable, Hashable, Sendable {
        public var enabled: Bool
        public var host: String
        public var port: Int
        public var tls: Bool
        public var username: String
        public var topicPrefix: String
        public var discoveryPrefix: String
        public var allowCommands: Bool
        public var hasPassword: Bool
        public var status: MQTTStatus

        public init() {
            enabled = false
            host = ""
            port = 1883
            tls = false
            username = ""
            topicPrefix = "sentinel"
            discoveryPrefix = "homeassistant"
            allowCommands = false
            hasPassword = false
            status = MQTTStatus()
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
            host = try c.decodeIfPresent(String.self, forKey: .host) ?? ""
            port = try c.decodeIfPresent(Int.self, forKey: .port) ?? 1883
            tls = try c.decodeIfPresent(Bool.self, forKey: .tls) ?? false
            username = try c.decodeIfPresent(String.self, forKey: .username) ?? ""
            topicPrefix = try c.decodeIfPresent(String.self, forKey: .topicPrefix) ?? "sentinel"
            discoveryPrefix = try c.decodeIfPresent(String.self, forKey: .discoveryPrefix) ?? "homeassistant"
            allowCommands = try c.decodeIfPresent(Bool.self, forKey: .allowCommands) ?? false
            hasPassword = try c.decodeIfPresent(Bool.self, forKey: .hasPassword) ?? false
            status = try c.decodeIfPresent(MQTTStatus.self, forKey: .status) ?? MQTTStatus()
        }
    }

    /// Body of `PUT /api/v1/mqtt` and `POST /api/v1/mqtt/test`; nil keeps the
    /// saved value, `password == ""` clears it.
    struct MQTTUpdate: Codable, Hashable, Sendable {
        public var enabled: Bool?
        public var host: String?
        public var port: Int?
        public var tls: Bool?
        public var username: String?
        public var password: String?
        public var topicPrefix: String?
        public var discoveryPrefix: String?
        public var allowCommands: Bool?

        public init(enabled: Bool? = nil, host: String? = nil, port: Int? = nil, tls: Bool? = nil,
                    username: String? = nil, password: String? = nil, topicPrefix: String? = nil,
                    discoveryPrefix: String? = nil, allowCommands: Bool? = nil) {
            self.enabled = enabled
            self.host = host
            self.port = port
            self.tls = tls
            self.username = username
            self.password = password
            self.topicPrefix = topicPrefix
            self.discoveryPrefix = discoveryPrefix
            self.allowCommands = allowCommands
        }
    }

    struct MQTTTestResult: Codable, Hashable, Sendable {
        public var ok: Bool
        public var error: String?
    }
}
