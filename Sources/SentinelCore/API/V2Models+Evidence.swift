import Foundation

// Evidence cases for the Exports workspace (`/api/v2/evidence`). Kept apart
// from `V2Models.swift`, whose `V2.Evidence` is the alarm-lock shape.

public extension V2 {
    /// Where an evidence item came from (wave 23). Absent = cut from recordings.
    struct EvidenceSource: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        /// A video sent from the iPhone (`POST /api/v2/evidence/uploads`).
        public static let upload = EvidenceSource(rawValue: "upload")
    }

    /// One case of `GET /api/v2/evidence` (`EvidenceCase`): the vault entry
    /// plus its note and exact range. Cases locked from an alarm carry
    /// `fromMs`/`toMs` of 0 and only the server's `range` label.
    struct EvidenceCase: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var caseId: String
        public var alarmId: String?
        /// Empty for an upload that isn't tied to a camera.
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
        // Wave 23 uploads. Optional: older servers omit them.
        /// `upload` for a video sent from the iPhone; nil for footage cut from recordings.
        public var source: EvidenceSource?
        /// Who sent the video.
        public var uploadedBy: String?
        /// The phone it came from ("EJ's iPhone").
        public var device: String?
        /// When the video was recorded (Unix ms), when the phone knew it.
        public var recordedAt: UnixMillis?
        /// The original file name ("IMG_0042.MOV"), when the server sends it.
        public var fileName: String?
        /// `video/mp4` or `video/quicktime`, when the server sends it.
        public var contentType: String?

        public var isUpload: Bool { source == .upload }

        public init(id: String, caseId: String, alarmId: String? = nil, cameraId: String, cameraName: String,
                    title: String = "", range: String = "", size: Int64, sha256: String, lockedAt: UnixMillis,
                    lockedBy: String, recorderId: String? = nil, note: String = "",
                    fromMs: UnixMillis = 0, toMs: UnixMillis = 0,
                    source: EvidenceSource? = nil, uploadedBy: String? = nil, device: String? = nil,
                    recordedAt: UnixMillis? = nil, fileName: String? = nil, contentType: String? = nil) {
            self.source = source
            self.uploadedBy = uploadedBy
            self.device = device
            self.recordedAt = recordedAt
            self.fileName = fileName
            self.contentType = contentType
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
            cameraId = try c.decodeIfPresent(String.self, forKey: .cameraId) ?? ""
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
            source = try c.decodeIfPresent(EvidenceSource.self, forKey: .source)
            uploadedBy = try c.decodeIfPresent(String.self, forKey: .uploadedBy)
            device = try c.decodeIfPresent(String.self, forKey: .device)
            recordedAt = try c.decodeIfPresent(UnixMillis.self, forKey: .recordedAt)
            fileName = try c.decodeIfPresent(String.self, forKey: .fileName)
            contentType = try c.decodeIfPresent(String.self, forKey: .contentType)
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
