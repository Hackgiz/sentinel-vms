import Foundation

// Wave 12 Sentinel Cloud DTOs (`WAVE12-CLOUD.md` section 4): the
// Management Server's `CloudStatus` (`GET/PUT /api/v2/cloud`,
// `POST/DELETE /api/v2/cloud/link`) and the settings body of the PUT.
// Wave 13 (`WAVE13-CLOUD-ONBOARDING.md`) adds `CloudStatus.signIn` (the
// control plane's `/v1/config`: where to sign in and what is for sale) and
// the body of the one-call link, `{"accessToken": "<supabase jwt>"}`.
//
// Unlike the rest of v2, these times are RFC 3339 strings. The decoders are
// lenient on purpose: a missing or null object falls back to its "off"
// value, an unknown enum string is kept (`OpenStringEnum`), and a time that
// is null, empty, Go's zero time or unparseable reads as nil. A time sent
// as Unix milliseconds is accepted too, in case the server follows the v2
// convention instead.

public extension V2 {
    // MARK: Enums

    /// `free | cloud | business`.
    struct CloudPlan: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }
        public static let free = CloudPlan(rawValue: "free")
        public static let cloud = CloudPlan(rawValue: "cloud")
        public static let business = CloudPlan(rawValue: "business")
    }

    /// The subscription state: `inactive | active | grace | canceled`.
    struct CloudPlanStatus: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }
        public static let inactive = CloudPlanStatus(rawValue: "inactive")
        public static let active = CloudPlanStatus(rawValue: "active")
        /// Payment past due; features stay on for 7 days after `periodEnd`.
        public static let grace = CloudPlanStatus(rawValue: "grace")
        public static let canceled = CloudPlanStatus(rawValue: "canceled")
    }

    /// One entry of `features`: `relay | push | backup`.
    struct CloudFeature: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }
        public static let relay = CloudFeature(rawValue: "relay")
        public static let push = CloudFeature(rawValue: "push")
        public static let backup = CloudFeature(rawValue: "backup")
    }

    /// The relay connection: `off | connecting | connected | retrying | inactive`.
    struct CloudRemoteState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }
        public static let off = CloudRemoteState(rawValue: "off")
        public static let connecting = CloudRemoteState(rawValue: "connecting")
        public static let connected = CloudRemoteState(rawValue: "connected")
        public static let retrying = CloudRemoteState(rawValue: "retrying")
        /// The plan isn't active (relay close code 4003).
        public static let inactive = CloudRemoteState(rawValue: "inactive")
    }

    /// How push notifications leave the server: `off | cloud | direct`
    /// (direct = the owner's own APNs key, unchanged by Sentinel Cloud).
    struct CloudPushState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }
        public static let off = CloudPushState(rawValue: "off")
        public static let cloud = CloudPushState(rawValue: "cloud")
        public static let direct = CloudPushState(rawValue: "direct")
    }

    // MARK: Nested objects

    /// `{"backupBytes": 10737418240, "backupDays": 30, "servers": 1}`
    struct CloudQuotas: Codable, Hashable, Sendable {
        public var backupBytes: Int64
        public var backupDays: Int
        public var servers: Int

        public init(backupBytes: Int64 = 0, backupDays: Int = 0, servers: Int = 0) {
            self.backupBytes = backupBytes
            self.backupDays = backupDays
            self.servers = servers
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            backupBytes = try c.decodeIfPresent(Int64.self, forKey: .backupBytes) ?? 0
            backupDays = try c.decodeIfPresent(Int.self, forKey: .backupDays) ?? 0
            servers = try c.decodeIfPresent(Int.self, forKey: .servers) ?? 0
        }
    }

    /// `{"enabled": true, "state": "connected", "url": "https://…", "error": ""}`
    struct CloudRemote: Codable, Hashable, Sendable {
        /// The owner's setting (`cloud_remote`).
        public var enabled: Bool
        public var state: CloudRemoteState
        /// The stable address, "" until the relay has one.
        public var url: String
        public var error: String

        public init(enabled: Bool = false, state: CloudRemoteState = .off, url: String = "", error: String = "") {
            self.enabled = enabled
            self.state = state
            self.url = url
            self.error = error
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
            state = try c.decodeIfPresent(CloudRemoteState.self, forKey: .state) ?? .off
            url = try c.decodeIfPresent(String.self, forKey: .url) ?? ""
            error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
        }
    }

    /// `{"state": "cloud"}`
    struct CloudPush: Codable, Hashable, Sendable {
        public var state: CloudPushState

        public init(state: CloudPushState = .off) {
            self.state = state
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            state = try c.decodeIfPresent(CloudPushState.self, forKey: .state) ?? .off
        }
    }

    /// `{"enabled": true, "bytesUsed": 123456, "objects": 12, "lastUploadAt": "…", "error": ""}`
    struct CloudBackup: Codable, Hashable, Sendable {
        /// The owner's setting (`cloud_backup`).
        public var enabled: Bool
        public var bytesUsed: Int64
        public var objects: Int
        public var lastUploadAt: Date?
        public var error: String

        private enum CodingKeys: String, CodingKey {
            case enabled, bytesUsed, objects, lastUploadAt, error
        }

        public init(enabled: Bool = false, bytesUsed: Int64 = 0, objects: Int = 0, lastUploadAt: Date? = nil, error: String = "") {
            self.enabled = enabled
            self.bytesUsed = bytesUsed
            self.objects = objects
            self.lastUploadAt = lastUploadAt
            self.error = error
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
            bytesUsed = try c.decodeIfPresent(Int64.self, forKey: .bytesUsed) ?? 0
            objects = try c.decodeIfPresent(Int.self, forKey: .objects) ?? 0
            lastUploadAt = c.decodeCloudTime(forKey: .lastUploadAt)
            error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
        }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(enabled, forKey: .enabled)
            try c.encode(bytesUsed, forKey: .bytesUsed)
            try c.encode(objects, forKey: .objects)
            try c.encodeCloudTime(lastUploadAt, forKey: .lastUploadAt)
            try c.encode(error, forKey: .error)
        }
    }

    /// The pending link, present only while a code waits for approval.
    /// `{"userCode": "ABCD-EFGH", "verifyURL": "https://sentvms.com/cloud?code=ABCD-EFGH", "expiresAt": "…"}`
    struct CloudLink: Codable, Hashable, Sendable {
        public var userCode: String
        public var verifyURL: String
        public var expiresAt: Date?

        private enum CodingKeys: String, CodingKey {
            case userCode, verifyURL, expiresAt
        }

        public init(userCode: String, verifyURL: String, expiresAt: Date?) {
            self.userCode = userCode
            self.verifyURL = verifyURL
            self.expiresAt = expiresAt
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            userCode = try c.decodeIfPresent(String.self, forKey: .userCode) ?? ""
            verifyURL = try c.decodeIfPresent(String.self, forKey: .verifyURL) ?? ""
            expiresAt = c.decodeCloudTime(forKey: .expiresAt)
        }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(userCode, forKey: .userCode)
            try c.encode(verifyURL, forKey: .verifyURL)
            try c.encodeCloudTime(expiresAt, forKey: .expiresAt)
        }
    }

    // MARK: CloudStatus

    /// `GET /api/v2/cloud` (and the answer of every cloud write).
    struct CloudStatus: Codable, Hashable, Sendable {
        public var linked: Bool
        /// The relayId; "" when not linked.
        public var serverId: String
        public var serverName: String
        public var accountEmailMasked: String
        public var plan: CloudPlan
        public var status: CloudPlanStatus
        /// Nil on the free plan.
        public var periodEnd: Date?
        @NullableArray public var features: [CloudFeature]
        public var quotas: CloudQuotas
        public var remote: CloudRemote
        public var push: CloudPush
        public var backup: CloudBackup
        /// Only while a link code is pending (an empty object reads as nil).
        public var link: CloudLink?
        /// Wave 13: where to sign in to the Sentinel Cloud account and the
        /// plans for sale; nil when the control plane is unreachable (or the
        /// server predates wave 13). Never fails the response.
        public var signIn: CloudSignIn?
        /// Wave 23: iPhone video uploads into the evidence vault are available (Sentinel Cloud).
        /// Nil on servers older than wave 23.
        public var uploadsAvailable: Bool?
        /// Wave 23 addendum 2: using an iPhone as a camera is available (Sentinel Cloud).
        /// Nil on servers older than wave 23.
        public var phoneCamerasAvailable: Bool?

        private enum CodingKeys: String, CodingKey {
            case linked, serverId, serverName, accountEmailMasked, plan, status, periodEnd, features, quotas, remote, push, backup, link, signIn
            case uploadsAvailable, phoneCamerasAvailable
        }

        public init(linked: Bool = false, serverId: String = "", serverName: String = "", accountEmailMasked: String = "",
                    plan: CloudPlan = .free, status: CloudPlanStatus = .inactive, periodEnd: Date? = nil,
                    features: [CloudFeature] = [], quotas: CloudQuotas = CloudQuotas(), remote: CloudRemote = CloudRemote(),
                    push: CloudPush = CloudPush(), backup: CloudBackup = CloudBackup(), link: CloudLink? = nil,
                    signIn: CloudSignIn? = nil) {
            self.linked = linked
            self.serverId = serverId
            self.serverName = serverName
            self.accountEmailMasked = accountEmailMasked
            self.plan = plan
            self.status = status
            self.periodEnd = periodEnd
            self.features = features
            self.quotas = quotas
            self.remote = remote
            self.push = push
            self.backup = backup
            self.link = link
            self.signIn = signIn
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            linked = try c.decodeIfPresent(Bool.self, forKey: .linked) ?? false
            serverId = try c.decodeIfPresent(String.self, forKey: .serverId) ?? ""
            serverName = try c.decodeIfPresent(String.self, forKey: .serverName) ?? ""
            accountEmailMasked = try c.decodeIfPresent(String.self, forKey: .accountEmailMasked) ?? ""
            plan = try c.decodeIfPresent(CloudPlan.self, forKey: .plan) ?? .free
            status = try c.decodeIfPresent(CloudPlanStatus.self, forKey: .status) ?? .inactive
            periodEnd = c.decodeCloudTime(forKey: .periodEnd)
            _features = try c.decode(NullableArray<CloudFeature>.self, forKey: .features)
            quotas = try c.decodeIfPresent(CloudQuotas.self, forKey: .quotas) ?? CloudQuotas()
            remote = try c.decodeIfPresent(CloudRemote.self, forKey: .remote) ?? CloudRemote()
            push = try c.decodeIfPresent(CloudPush.self, forKey: .push) ?? CloudPush()
            backup = try c.decodeIfPresent(CloudBackup.self, forKey: .backup) ?? CloudBackup()
            let pending = try c.decodeIfPresent(CloudLink.self, forKey: .link)
            link = (pending?.userCode.isEmpty ?? true) ? nil : pending
            // Tolerant: a malformed object reads as "can't sign in right now"
            // instead of failing the whole status.
            signIn = try? c.decodeIfPresent(CloudSignIn.self, forKey: .signIn)
            uploadsAvailable = try? c.decodeIfPresent(Bool.self, forKey: .uploadsAvailable)
            phoneCamerasAvailable = try? c.decodeIfPresent(Bool.self, forKey: .phoneCamerasAvailable)
        }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(linked, forKey: .linked)
            try c.encode(serverId, forKey: .serverId)
            try c.encode(serverName, forKey: .serverName)
            try c.encode(accountEmailMasked, forKey: .accountEmailMasked)
            try c.encode(plan, forKey: .plan)
            try c.encode(status, forKey: .status)
            try c.encodeCloudTime(periodEnd, forKey: .periodEnd)
            try c.encode(features, forKey: .features)
            try c.encode(quotas, forKey: .quotas)
            try c.encode(remote, forKey: .remote)
            try c.encode(push, forKey: .push)
            try c.encode(backup, forKey: .backup)
            try c.encodeIfPresent(link, forKey: .link)
            try c.encodeIfPresent(signIn, forKey: .signIn)
            try c.encodeIfPresent(uploadsAvailable, forKey: .uploadsAvailable)
            try c.encodeIfPresent(phoneCamerasAvailable, forKey: .phoneCamerasAvailable)
        }

        public func has(_ feature: CloudFeature) -> Bool { features.contains(feature) }
    }

    // MARK: Wave 13: sign-in configuration and the one-call link

    /// One plan for sale (`signIn.plans[]`):
    /// `{"id":"cloud","name":"Sentinel Cloud","month":4.99,"year":49,"servers":1,"backupGB":10,"days":30}`.
    /// Lenient: a missing or mistyped field reads as empty/0.
    struct CloudPlanOffer: Codable, Hashable, Sendable, Identifiable {
        /// `cloud | business` (what checkout's `plan` takes).
        public var id: CloudPlan
        public var name: String
        /// US dollars per month / per year.
        public var month: Double
        public var year: Double
        public var servers: Int
        public var backupGB: Int
        /// Backup retention in days.
        public var days: Int

        private enum CodingKeys: String, CodingKey {
            case id, name, month, year, servers, backupGB, days
        }

        public init(id: CloudPlan, name: String, month: Double, year: Double, servers: Int = 0, backupGB: Int = 0, days: Int = 0) {
            self.id = id
            self.name = name
            self.month = month
            self.year = year
            self.servers = servers
            self.backupGB = backupGB
            self.days = days
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = CloudPlan(rawValue: c.decodeLenientText(forKey: .id))
            name = c.decodeLenientText(forKey: .name)
            month = c.decodeLenientNumber(forKey: .month)
            year = c.decodeLenientNumber(forKey: .year)
            servers = Int(c.decodeLenientNumber(forKey: .servers).rounded())
            backupGB = Int(c.decodeLenientNumber(forKey: .backupGB).rounded())
            days = Int(c.decodeLenientNumber(forKey: .days).rounded())
        }
    }

    /// `signIn` of `GET /api/v2/cloud`: the control plane's `GET /v1/config`
    /// (public; the server caches it 1 h). The anon key is Supabase's
    /// publishable key, not a secret.
    struct CloudSignIn: Codable, Hashable, Sendable {
        public var supabaseUrl: String
        public var supabaseAnonKey: String
        public var siteBase: String
        public var checkoutURL: String
        public var portalURL: String
        public var plans: [CloudPlanOffer]

        private enum CodingKeys: String, CodingKey {
            case supabaseUrl, supabaseAnonKey, siteBase, checkoutURL, portalURL, plans
        }

        public init(supabaseUrl: String, supabaseAnonKey: String, siteBase: String = "https://sentvms.com",
                    checkoutURL: String = "", portalURL: String = "", plans: [CloudPlanOffer] = []) {
            self.supabaseUrl = supabaseUrl
            self.supabaseAnonKey = supabaseAnonKey
            self.siteBase = siteBase
            self.checkoutURL = checkoutURL
            self.portalURL = portalURL
            self.plans = plans
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            supabaseUrl = c.decodeLenientText(forKey: .supabaseUrl)
            supabaseAnonKey = c.decodeLenientText(forKey: .supabaseAnonKey)
            siteBase = c.decodeLenientText(forKey: .siteBase)
            checkoutURL = c.decodeLenientText(forKey: .checkoutURL)
            portalURL = c.decodeLenientText(forKey: .portalURL)
            let offers = (try? c.decodeIfPresent([CloudPlanOffer].self, forKey: .plans)) ?? []
            plans = offers.filter { $0.id.rawValue.isEmpty == false }
        }

        /// The plan for sale with this id, if offered.
        public func offer(_ plan: CloudPlan) -> CloudPlanOffer? { plans.first { $0.id == plan } }
    }

    /// Body of `POST /api/v2/cloud/link` in wave 13: the Sentinel Cloud
    /// account's session JWT, used once by the server and never stored.
    struct CloudLinkRequest: Codable, Hashable, Sendable {
        public var accessToken: String

        public init(accessToken: String) {
            self.accessToken = accessToken
        }
    }

    /// Body of `PUT /api/v2/cloud`: only the switches being changed
    /// (`{"remote": false}`); a nil field is left out of the JSON.
    struct CloudSettingsPatch: Codable, Hashable, Sendable {
        public var remote: Bool?
        public var backup: Bool?

        public init(remote: Bool? = nil, backup: Bool? = nil) {
            self.remote = remote
            self.backup = backup
        }
    }
}

// MARK: - Time parsing

public extension V2 {
    /// RFC 3339 times in the cloud payloads (Go's `time.Time` JSON, which may
    /// carry up to nine fractional digits).
    enum CloudTime {
        /// Nil for "", Go's zero time (`0001-01-01T00:00:00Z`) and anything
        /// unparseable.
        public static func parse(_ text: String) -> Date? {
            let trimmed = text.trimmingCharacters(in: .whitespaces)
            guard trimmed.isEmpty == false else { return nil }
            let plain = ISO8601DateFormatter()
            plain.formatOptions = [.withInternetDateTime]
            // Drop the fractional seconds (any length) and add them back by hand,
            // so nanosecond precision parses on every macOS version.
            var whole = trimmed
            var fraction: Double = 0
            if let dot = trimmed.firstIndex(of: "."), let zone = trimmed[dot...].firstIndex(where: { $0 == "Z" || $0 == "z" || $0 == "+" || $0 == "-" }) {
                let digits = trimmed[trimmed.index(after: dot)..<zone]
                guard digits.isEmpty == false, digits.allSatisfy(\.isNumber) else { return nil }
                fraction = Double("0." + digits) ?? 0
                whole = String(trimmed[..<dot] + trimmed[zone...])
            }
            guard let date = plain.date(from: whole) else { return nil }
            guard date.timeIntervalSince1970 > 0 else { return nil } // Go's zero time and other placeholders
            return date.addingTimeInterval(fraction)
        }

        public static func format(_ date: Date) -> String {
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime]
            return f.string(from: date)
        }
    }
}

extension KeyedDecodingContainer {
    /// A cloud time that never fails the response: an RFC 3339 string, or
    /// Unix milliseconds; null, missing or unreadable is nil.
    func decodeCloudTime(forKey key: Key) -> Date? {
        if let text = try? decodeIfPresent(String.self, forKey: key) { return V2.CloudTime.parse(text) }
        if let ms = try? decodeIfPresent(Int64.self, forKey: key), ms > 0 { return Date(unixMillis: ms) }
        return nil
    }
}

extension KeyedDecodingContainer {
    /// A string that never fails the response (trimmed); anything else is "".
    func decodeLenientText(forKey key: Key) -> String {
        guard let value = try? decodeIfPresent(String.self, forKey: key) else { return "" }
        return value.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// A number that never fails the response: a JSON number or a numeric
    /// string; anything else is 0.
    func decodeLenientNumber(forKey key: Key) -> Double {
        if let value = try? decodeIfPresent(Double.self, forKey: key) { return value.isFinite ? value : 0 }
        if let text = try? decodeIfPresent(String.self, forKey: key),
           let value = Double(text.trimmingCharacters(in: .whitespaces)), value.isFinite {
            return value
        }
        return 0
    }
}

extension KeyedEncodingContainer {
    mutating func encodeCloudTime(_ date: Date?, forKey key: Key) throws {
        if let date { try encode(V2.CloudTime.format(date), forKey: key) } else { try encodeNil(forKey: key) }
    }
}
