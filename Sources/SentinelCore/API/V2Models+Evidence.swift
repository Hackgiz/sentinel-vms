import Foundation

// Evidence cases for the Exports workspace (`/api/v2/evidence`). Kept apart
// from `V2Models.swift`, whose `V2.Evidence` is the alarm-lock shape.

public extension V2 {
    /// One case of `GET /api/v2/evidence` (`EvidenceCase`): the vault entry
    /// plus its note and exact range. Cases locked from an alarm carry
    /// `fromMs`/`toMs` of 0 and only the server's `range` label.
    struct EvidenceCase: Codable, Hashable, Identifiable, Sendable {
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
        public var note: String
        public var fromMs: UnixMillis
        public var toMs: UnixMillis

        public init(id: String, caseId: String, alarmId: String? = nil, cameraId: String, cameraName: String,
                    title: String = "", range: String = "", size: Int64, sha256: String, lockedAt: UnixMillis,
                    lockedBy: String, recorderId: String? = nil, note: String = "",
                    fromMs: UnixMillis = 0, toMs: UnixMillis = 0) {
            self.id = id
            self.caseId = caseId
            self.alarmId = alarmId
            self.cameraId = cameraId
            self.cameraName = cameraName
            self.title = title
            self.range = range
            self.size = size
            self.sha256 = sha256
            self.lockedAt = lockedAt
            self.lockedBy = lockedBy
            self.recorderId = recorderId
            self.note = note
            self.fromMs = fromMs
            self.toMs = toMs
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            caseId = try c.decode(String.self, forKey: .caseId)
            alarmId = try c.decodeIfPresent(String.self, forKey: .alarmId)
            cameraId = try c.decode(String.self, forKey: .cameraId)
            cameraName = try c.decodeIfPresent(String.self, forKey: .cameraName) ?? ""
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            range = try c.decodeIfPresent(String.self, forKey: .range) ?? ""
            size = try c.decodeIfPresent(Int64.self, forKey: .size) ?? 0
            sha256 = try c.decodeIfPresent(String.self, forKey: .sha256) ?? ""
            lockedAt = try c.decodeIfPresent(UnixMillis.self, forKey: .lockedAt) ?? 0
            lockedBy = try c.decodeIfPresent(String.self, forKey: .lockedBy) ?? ""
            recorderId = try c.decodeIfPresent(String.self, forKey: .recorderId)
            note = try c.decodeIfPresent(String.self, forKey: .note) ?? ""
            fromMs = try c.decodeIfPresent(UnixMillis.self, forKey: .fromMs) ?? 0
            toMs = try c.decodeIfPresent(UnixMillis.self, forKey: .toMs) ?? 0
        }
    }

    /// Body of `POST /api/v2/evidence`.
    struct EvidenceRequest: Codable, Hashable, Sendable {
        public var cameraId: String
        public var fromMs: UnixMillis
        public var toMs: UnixMillis
        public var title: String
        public var note: String?

        public init(cameraId: String, fromMs: UnixMillis, toMs: UnixMillis, title: String, note: String? = nil) {
            self.cameraId = cameraId
            self.fromMs = fromMs
            self.toMs = toMs
            self.title = title
            self.note = note
        }
    }

    /// Answer of `POST /api/v2/evidence/{id}/verify`.
    struct EvidenceVerification: Codable, Hashable, Sendable {
        public var ok: Bool
        public var sha256: String
        public var message: String?
    }
}
