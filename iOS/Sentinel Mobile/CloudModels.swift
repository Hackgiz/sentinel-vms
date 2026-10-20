// CloudModels.swift
// Sentinel Cloud as the phone sees it (WAVE12-CLOUD.md sections 1 and 4):
// the Management Server's `GET /api/v2/cloud/remote` and
// `GET /api/v2/cloud/backup-access`, the offsite backup listing
// `GET /v1/backup/objects`, and the `alarm.json` stored next to each backed-up
// alarm's snapshot and clip.
//
// Decoding here is deliberately tolerant. The server half of these shapes is
// written in parallel with this app, and the backup is read when the server
// may be gone for good, so a renamed field or a time sent as text instead of
// milliseconds must not hide someone's footage.

import Foundation

// MARK: - Management Server (API v2, section 4)

extension V2 {
    /// `GET /api/v2/cloud/remote`: where the server can be reached from
    /// outside its network. `remoteURL` is null when it can't be.
    struct CloudRemote: Codable, Hashable, Sendable {
        var remoteURL: String?
        /// `cloud` (the Sentinel Cloud relay), `tunnel`, or null.
        var via: String?

        init(remoteURL: String?, via: String?) {
            self.remoteURL = remoteURL
            self.via = via
        }

        var isCloud: Bool { via?.lowercased() == "cloud" }

        private enum CodingKeys: String, CodingKey { case remoteURL, remoteUrl, via }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            remoteURL = Cloud.firstString(c, .remoteURL, .remoteUrl)
            via = try? c.decodeIfPresent(String.self, forKey: .via)
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(remoteURL, forKey: .remoteURL)
            try c.encode(via, forKey: .via)
        }
    }

    /// `GET /api/v2/cloud/backup-access`: a read-only `scb_` token for this
    /// server's offsite backup and where the backup lives. 404 when backup is
    /// off or the server isn't linked.
    struct CloudBackupAccess: Codable, Hashable, Sendable {
        var token: String
        var expiresAt: Date
        var serverId: String
        /// `https://cloud.sentvms.com/v1/backup`
        var baseURL: String

        init(token: String, expiresAt: Date, serverId: String, baseURL: String) {
            self.token = token
            self.expiresAt = expiresAt
            self.serverId = serverId
            self.baseURL = baseURL
        }

        private enum CodingKeys: String, CodingKey { case token, expiresAt, serverId, serverID, baseURL, baseUrl }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            token = try c.decode(String.self, forKey: .token)
            guard let base = Cloud.firstString(c, .baseURL, .baseUrl) else {
                throw DecodingError.keyNotFound(CodingKeys.baseURL, .init(codingPath: c.codingPath, debugDescription: "no baseURL"))
            }
            baseURL = base
            serverId = Cloud.firstString(c, .serverId, .serverID) ?? ""
            // A token without a readable expiry is treated as already due for
            // renewal rather than refused.
            expiresAt = (try? c.decodeIfPresent(Cloud.FlexibleDate.self, forKey: .expiresAt))?.date ?? Date(timeIntervalSince1970: 0)
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(token, forKey: .token)
            try c.encode(expiresAt.unixMillis, forKey: .expiresAt)
            try c.encode(serverId, forKey: .serverId)
            try c.encode(baseURL, forKey: .baseURL)
        }
    }
}

// MARK: - Sentinel Cloud (section 1)

/// Types that come from Sentinel Cloud itself rather than the Management Server.
enum Cloud {
    /// Relay hostnames are `<relayId>-cloud.sentvms.com`.
    static let relayHostSuffix = "-cloud.sentvms.com"

    /// True for a Sentinel Cloud relay address.
    static func isRelayHost(_ url: URL?) -> Bool {
        guard let host = url?.host?.lowercased() else { return false }
        return host.hasSuffix(relayHostSuffix) && host.count > relayHostSuffix.count
    }

    /// "via Sentinel Cloud": the phone is on the site's secondary address and
    /// that address is a Sentinel Cloud relay.
    static func isViaRelay(isUsingSecondary: Bool, secondary: URL?) -> Bool {
        isUsingSecondary && isRelayHost(secondary)
    }

    /// The first of several spellings of a string field that is present and not empty.
    fileprivate static func firstString<K: CodingKey>(_ c: KeyedDecodingContainer<K>, _ keys: K...) -> String? {
        for key in keys {
            if let value = try? c.decodeIfPresent(String.self, forKey: key), value.isEmpty == false { return value }
        }
        return nil
    }

    // MARK: Times

    /// A time sent as Unix milliseconds (the v2 convention), Unix seconds, or
    /// RFC 3339 text (the Cloud's convention).
    struct FlexibleDate: Codable, Hashable, Sendable {
        var date: Date

        init(_ date: Date) { self.date = date }

        init(from decoder: Decoder) throws {
            let c = try decoder.singleValueContainer()
            if let number = try? c.decode(Double.self), let date = FlexibleDate.date(fromNumber: number) {
                self.date = date
            } else if let text = try? c.decode(String.self), let date = FlexibleDate.date(fromText: text) {
                self.date = date
            } else {
                throw DecodingError.dataCorruptedError(in: c, debugDescription: "not a time")
            }
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.singleValueContainer()
            try c.encode(date.unixMillis)
        }

        /// Milliseconds when the number is too big to be seconds (anything
        /// after 1973 in milliseconds is more than 1e11).
        static func date(fromNumber number: Double) -> Date? {
            guard number.isFinite, number > 0 else { return nil }
            return number > 1e11 ? Date(timeIntervalSince1970: number / 1000) : Date(timeIntervalSince1970: number)
        }

        static func date(fromText text: String) -> Date? {
            let trimmed = text.trimmingCharacters(in: .whitespaces)
            if let number = Double(trimmed) { return date(fromNumber: number) }
            let plain = ISO8601DateFormatter()
            if let date = plain.date(from: trimmed) { return date }
            let fractional = ISO8601DateFormatter()
            fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            return fractional.date(from: trimmed)
        }
    }

    // MARK: Free-form JSON

    /// Any JSON value: the listing's `meta` (whatever the server put in
    /// `X-Sentinel-Meta`) and `alarm.json` are read through this.
    enum JSONValue: Codable, Hashable, Sendable {
        case string(String)
        case number(Double)
        case bool(Bool)
        case object([String: JSONValue])
        case array([JSONValue])
        case null

        init(from decoder: Decoder) throws {
            let c = try decoder.singleValueContainer()
            if c.decodeNil() { self = .null }
            else if let b = try? c.decode(Bool.self) { self = .bool(b) }
            else if let n = try? c.decode(Double.self) { self = .number(n) }
            else if let s = try? c.decode(String.self) { self = .string(s) }
            else if let a = try? c.decode([JSONValue].self) { self = .array(a) }
            else { self = .object(try c.decode([String: JSONValue].self)) }
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.singleValueContainer()
            switch self {
            case .string(let s): try c.encode(s)
            case .number(let n): try c.encode(n)
            case .bool(let b): try c.encode(b)
            case .object(let o): try c.encode(o)
            case .array(let a): try c.encode(a)
            case .null: try c.encodeNil()
            }
        }

        var stringValue: String? {
            switch self {
            case .string(let s): return s.isEmpty ? nil : s
            case .number(let n): return n.rounded() == n && abs(n) < 9e15 ? String(Int64(n)) : String(n)
            default: return nil
            }
        }

        var intValue: Int? {
            switch self {
            case .number(let n): return Int(exactly: n.rounded())
            case .string(let s): return Int(s)
            default: return nil
            }
        }

        var dateValue: Date? {
            switch self {
            case .number(let n): return FlexibleDate.date(fromNumber: n)
            case .string(let s): return FlexibleDate.date(fromText: s)
            default: return nil
            }
        }

        var objectValue: [String: JSONValue]? {
            if case .object(let o) = self { return o }
            return nil
        }
    }

    // MARK: Backup listing

    /// `GET /v1/backup/objects` → `{objects:[…], next}`, newest first.
    struct BackupListing: Codable, Hashable, Sendable {
        var objects: [BackupObject]
        /// Pass as `after` for the next page; absent on the last one.
        var next: String?

        init(objects: [BackupObject], next: String? = nil) {
            self.objects = objects
            self.next = next
        }

        private enum CodingKeys: String, CodingKey { case objects, items, next, nextCursor }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            let list = (try? c.decodeIfPresent([Lossy<BackupObject>].self, forKey: .objects))
                ?? (try? c.decodeIfPresent([Lossy<BackupObject>].self, forKey: .items))
            objects = (list ?? []).compactMap(\.value)
            next = Cloud.firstString(c, .next, .nextCursor)
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(objects, forKey: .objects)
            try c.encodeIfPresent(next, forKey: .next)
        }
    }

    /// One stored object. `key` is relative to the server:
    /// `alarms/<alarmId>/snapshot.jpg`, `…/clip.mp4`, `…/alarm.json`,
    /// `events/<eventId>/thumb.jpg`.
    struct BackupObject: Codable, Hashable, Sendable {
        var key: String
        var size: Int64
        var uploadedAt: Date?
        var contentType: String?
        var meta: [String: JSONValue]

        init(key: String, size: Int64 = 0, uploadedAt: Date? = nil, contentType: String? = nil, meta: [String: JSONValue] = [:]) {
            self.key = key
            self.size = size
            self.uploadedAt = uploadedAt
            self.contentType = contentType
            self.meta = meta
        }

        private enum CodingKeys: String, CodingKey { case key, size, uploadedAt, contentType, meta }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            key = try c.decode(String.self, forKey: .key)
            size = (try? c.decodeIfPresent(Int64.self, forKey: .size)) ?? 0
            uploadedAt = (try? c.decodeIfPresent(FlexibleDate.self, forKey: .uploadedAt))?.date
            contentType = try? c.decodeIfPresent(String.self, forKey: .contentType)
            // `meta` may also arrive as the JSON text of the upload header.
            if let object = try? c.decodeIfPresent([String: JSONValue].self, forKey: .meta) {
                meta = object
            } else if let text = try? c.decodeIfPresent(String.self, forKey: .meta),
                      let object = try? JSONDecoder().decode([String: JSONValue].self, from: Data(text.utf8)) {
                meta = object
            } else {
                meta = [:]
            }
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(key, forKey: .key)
            try c.encode(size, forKey: .size)
            try c.encodeIfPresent(uploadedAt.map(FlexibleDate.init), forKey: .uploadedAt)
            try c.encodeIfPresent(contentType, forKey: .contentType)
            try c.encode(meta, forKey: .meta)
        }

        /// What the key says it is.
        var parsedKey: BackupKey? { BackupKey(key) }
    }

    /// The fixed key layout of section 1.
    struct BackupKey: Hashable, Sendable {
        enum Kind: Hashable, Sendable { case snapshot, clip, alarmJSON, eventThumb, other(String) }
        var alarmID: String?
        var eventID: String?
        var kind: Kind

        init?(_ key: String) {
            let parts = key.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
            guard parts.count == 3 else { return nil }
            switch parts[0] {
            case "alarms":
                alarmID = parts[1]
                eventID = nil
                switch parts[2] {
                case "snapshot.jpg": kind = .snapshot
                case "clip.mp4": kind = .clip
                case "alarm.json": kind = .alarmJSON
                default: kind = .other(parts[2])
                }
            case "events":
                alarmID = nil
                eventID = parts[1]
                kind = parts[2] == "thumb.jpg" ? .eventThumb : .other(parts[2])
            default:
                return nil
            }
        }

        static func snapshot(alarmID: String) -> String { "alarms/\(alarmID)/snapshot.jpg" }
        static func clip(alarmID: String) -> String { "alarms/\(alarmID)/clip.mp4" }
        static func alarmJSON(alarmID: String) -> String { "alarms/\(alarmID)/alarm.json" }
    }

    // MARK: alarm.json

    /// What the phone shows for a backed-up alarm, read from `alarm.json` or
    /// from the listing's `meta`. Every field is optional: whatever is there
    /// is shown.
    struct BackupAlarmSummary: Hashable, Sendable {
        var alarmID: String?
        var cameraID: String?
        var cameraName: String?
        var kind: String?
        var title: String?
        var detail: String?
        var severity: String?
        var state: String?
        var createdAt: Date?
        var lastEventAt: Date?
        var eventCount: Int?
        var snapshotKey: String?
        var clipKey: String?

        init() {}

        /// Reads the `V2.Alarm` field names plus `snapshotKey`/`clipKey`. Also
        /// accepts the alarm nested under `alarm` and the keys nested under
        /// `keys` (`snapshot`/`clip`), so either reading of the contract works.
        init(json: [String: JSONValue]) {
            var fields = json
            if let nested = json["alarm"]?.objectValue {
                fields = nested.merging(json.filter { $0.key != "alarm" }) { inner, _ in inner }
            }
            func string(_ names: String...) -> String? {
                for name in names { if let value = fields[name]?.stringValue { return value } }
                return nil
            }
            alarmID = string("id", "alarmId", "alarmID")
            cameraID = string("cameraId", "cameraID")
            cameraName = string("cameraName", "camera")
            kind = string("kind")
            title = string("title")
            detail = string("detail")
            severity = string("severity")
            state = string("state")
            createdAt = fields["createdAt"]?.dateValue
            lastEventAt = fields["lastEventAt"]?.dateValue
            eventCount = fields["eventCount"]?.intValue
            snapshotKey = string("snapshotKey")
            clipKey = string("clipKey")
            if let keys = fields["keys"]?.objectValue {
                snapshotKey = snapshotKey ?? keys["snapshot"]?.stringValue ?? keys["snapshotKey"]?.stringValue
                clipKey = clipKey ?? keys["clip"]?.stringValue ?? keys["clipKey"]?.stringValue
            }
        }

        init(alarm: V2.Alarm) {
            alarmID = alarm.id
            cameraID = alarm.cameraId
            cameraName = alarm.cameraName
            kind = alarm.kind
            title = alarm.title
            detail = alarm.detail
            severity = alarm.severity.rawValue
            state = alarm.state.rawValue
            createdAt = Date(unixMillis: alarm.createdAt)
            lastEventAt = Date(unixMillis: alarm.lastEventAt)
            eventCount = alarm.eventCount
        }

        /// Fills the gaps in `self` from `other`.
        func filling(from other: BackupAlarmSummary) -> BackupAlarmSummary {
            var s = self
            s.alarmID = s.alarmID ?? other.alarmID
            s.cameraID = s.cameraID ?? other.cameraID
            s.cameraName = s.cameraName ?? other.cameraName
            s.kind = s.kind ?? other.kind
            s.title = s.title ?? other.title
            s.detail = s.detail ?? other.detail
            s.severity = s.severity ?? other.severity
            s.state = s.state ?? other.state
            s.createdAt = s.createdAt ?? other.createdAt
            s.lastEventAt = s.lastEventAt ?? other.lastEventAt
            s.eventCount = s.eventCount ?? other.eventCount
            s.snapshotKey = s.snapshotKey ?? other.snapshotKey
            s.clipKey = s.clipKey ?? other.clipKey
            return s
        }

        /// Enough to show a row without fetching `alarm.json`.
        var isDescriptive: Bool { (title != nil || kind != nil) && (createdAt != nil || lastEventAt != nil) }
    }

    /// `alarms/<id>/alarm.json`: the server's `V2.Alarm` plus the keys of the
    /// snapshot and clip stored with it.
    struct BackupAlarmRecord: Decodable, Hashable, Sendable {
        /// The alarm, when the file matches `V2.Alarm` exactly.
        var alarm: V2.Alarm?
        var summary: BackupAlarmSummary

        init(alarm: V2.Alarm?, summary: BackupAlarmSummary) {
            self.alarm = alarm
            self.summary = summary
        }

        init(from decoder: Decoder) throws {
            let json = try decoder.singleValueContainer().decode([String: JSONValue].self)
            var summary = BackupAlarmSummary(json: json)
            let strict = try? V2.Alarm(from: decoder)
            let nested: V2.Alarm? = {
                guard strict == nil, let inner = json["alarm"],
                      let data = try? JSONEncoder().encode(inner) else { return nil }
                return try? JSONDecoder().decode(V2.Alarm.self, from: data)
            }()
            alarm = strict ?? nested
            if let alarm { summary = summary.filling(from: BackupAlarmSummary(alarm: alarm)) }
            self.summary = summary
        }
    }

    /// One backed-up alarm in the browser: its objects grouped by alarm id.
    struct BackupAlarmEntry: Hashable, Identifiable, Sendable {
        var id: String
        var snapshotKey: String?
        var clipKey: String?
        var alarmJSONKey: String?
        /// Latest upload among its objects.
        var uploadedAt: Date?
        var clipSize: Int64?
        var summary: BackupAlarmSummary

        init(id: String, snapshotKey: String? = nil, clipKey: String? = nil, alarmJSONKey: String? = nil,
             uploadedAt: Date? = nil, clipSize: Int64? = nil, summary: BackupAlarmSummary = BackupAlarmSummary()) {
            self.id = id
            self.snapshotKey = snapshotKey
            self.clipKey = clipKey
            self.alarmJSONKey = alarmJSONKey
            self.uploadedAt = uploadedAt
            self.clipSize = clipSize
            self.summary = summary
        }

        /// When it happened, as best known.
        var date: Date? { summary.createdAt ?? summary.lastEventAt ?? uploadedAt }

        var displayTitle: String { summary.title ?? summary.kind ?? "Alarm" }

        /// Folds `objects` (any page) into `entries`, newest first. Objects
        /// that aren't part of a backed-up alarm (event thumbnails) are left out.
        static func merge(_ objects: [BackupObject], into entries: [BackupAlarmEntry]) -> [BackupAlarmEntry] {
            var byID = Dictionary(entries.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
            var order = entries.map(\.id)
            for object in objects {
                guard let parsed = object.parsedKey, let alarmID = parsed.alarmID else { continue }
                var entry = byID[alarmID] ?? BackupAlarmEntry(id: alarmID)
                if byID[alarmID] == nil { order.append(alarmID) }
                switch parsed.kind {
                case .snapshot: entry.snapshotKey = object.key
                case .clip:
                    entry.clipKey = object.key
                    entry.clipSize = object.size
                case .alarmJSON: entry.alarmJSONKey = object.key
                default: break
                }
                if let at = object.uploadedAt, entry.uploadedAt.map({ at > $0 }) ?? true { entry.uploadedAt = at }
                if object.meta.isEmpty == false {
                    entry.summary = entry.summary.filling(from: BackupAlarmSummary(json: object.meta))
                }
                byID[alarmID] = entry
            }
            let merged = order.compactMap { byID[$0] }
            // Stable newest-first: undated entries keep their listing order at the end.
            return merged.enumerated().sorted { a, b in
                switch (a.element.date, b.element.date) {
                case let (x?, y?) where x != y: return x > y
                case (.some, nil): return true
                case (nil, .some): return false
                default: return a.offset < b.offset
                }
            }.map(\.element)
        }

        /// Takes the details from a fetched `alarm.json`. Keys it names win
        /// over the ones guessed from the listing.
        func applying(_ record: BackupAlarmRecord) -> BackupAlarmEntry {
            var entry = self
            entry.summary = record.summary.filling(from: summary)
            if let key = record.summary.snapshotKey { entry.snapshotKey = key }
            if let key = record.summary.clipKey { entry.clipKey = key }
            return entry
        }
    }

    /// Decodes an element or skips it, so one odd object doesn't empty the list.
    fileprivate struct Lossy<Value: Decodable>: Decodable {
        var value: Value?
        init(from decoder: Decoder) throws { value = try? Value(from: decoder) }
    }
}
