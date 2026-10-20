import Foundation

// Wave 11 (stream B): long exports, export options, scheduled exports and
// share links (`/api/v2/export-jobs`, `/api/v2/export-schedules`,
// `/api/v2/evidence/{id}/share`, `/api/v2/share-links`). Kept apart from
// `V2Models+Evidence.swift`, which this wave does not edit.

public extension V2 {
    /// A normalized rectangle of the video frame (0–1, origin top left),
    /// pixelated in an exported clip (`ExportRegion`).
    struct ExportRegion: Codable, Hashable, Sendable {
        public var x0: Double
        public var y0: Double
        public var x1: Double
        public var y1: Double

        public init(x0: Double, y0: Double, x1: Double, y1: Double) {
            self.x0 = x0
            self.y0 = y0
            self.x1 = x1
            self.y1 = y1
        }
    }

    struct ExportDest: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        /// The file is kept on the recording server for 30 minutes after the job is done.
        public static let download = ExportDest(rawValue: "download")
        /// The file goes into the evidence vault as a case.
        public static let evidence = ExportDest(rawValue: "evidence")
    }

    struct ExportJobState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let running = ExportJobState(rawValue: "running")
        public static let done = ExportJobState(rawValue: "done")
        public static let failed = ExportJobState(rawValue: "failed")
        public static let canceled = ExportJobState(rawValue: "canceled")
    }

    /// A long (chunked, up to 12 h) export job (`ExportJob`).
    struct ExportJob: Codable, Hashable, Identifiable, Sendable {
        public var taskId: String
        public var state: ExportJobState
        public var progress: Int
        public var error: String?
        public var cameraId: String
        public var fromMs: UnixMillis
        public var toMs: UnixMillis
        public var dest: ExportDest
        public var downloadUrl: String?
        public var filename: String?
        public var evidenceId: String?
        public var caseId: String?

        public var id: String { taskId }

        public init(taskId: String, state: ExportJobState, progress: Int = 0, error: String? = nil, cameraId: String,
                    fromMs: UnixMillis, toMs: UnixMillis, dest: ExportDest, downloadUrl: String? = nil,
                    filename: String? = nil, evidenceId: String? = nil, caseId: String? = nil) {
            self.taskId = taskId
            self.state = state
            self.progress = progress
            self.error = error
            self.cameraId = cameraId
            self.fromMs = fromMs
            self.toMs = toMs
            self.dest = dest
            self.downloadUrl = downloadUrl
            self.filename = filename
            self.evidenceId = evidenceId
            self.caseId = caseId
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            taskId = try c.decode(String.self, forKey: .taskId)
            state = try c.decode(ExportJobState.self, forKey: .state)
            progress = try c.decodeIfPresent(Int.self, forKey: .progress) ?? 0
            error = try c.decodeIfPresent(String.self, forKey: .error)
            cameraId = try c.decodeIfPresent(String.self, forKey: .cameraId) ?? ""
            fromMs = try c.decodeIfPresent(UnixMillis.self, forKey: .fromMs) ?? 0
            toMs = try c.decodeIfPresent(UnixMillis.self, forKey: .toMs) ?? 0
            dest = try c.decodeIfPresent(ExportDest.self, forKey: .dest) ?? .download
            downloadUrl = try c.decodeIfPresent(String.self, forKey: .downloadUrl)
            filename = try c.decodeIfPresent(String.self, forKey: .filename)
            evidenceId = try c.decodeIfPresent(String.self, forKey: .evidenceId)
            caseId = try c.decodeIfPresent(String.self, forKey: .caseId)
        }
    }

    /// Body of `POST /api/v2/export-jobs`. Options are omitted when off.
    struct ExportJobRequest: Codable, Hashable, Sendable {
        public var cameraId: String
        public var fromMs: UnixMillis
        public var toMs: UnixMillis
        public var dest: ExportDest
        public var title: String?
        public var note: String?
        public var watermark: Bool?
        public var blurRegions: [ExportRegion]?

        public init(cameraId: String, fromMs: UnixMillis, toMs: UnixMillis, dest: ExportDest, title: String? = nil,
                    note: String? = nil, watermark: Bool? = nil, blurRegions: [ExportRegion]? = nil) {
            self.cameraId = cameraId
            self.fromMs = fromMs
            self.toMs = toMs
            self.dest = dest
            self.title = title
            self.note = note
            self.watermark = watermark
            self.blurRegions = blurRegions
        }
    }

    /// Body of `POST /api/v2/evidence` with the export options
    /// (`V2.EvidenceRequest` predates them and is left as it is).
    struct EvidenceOptionsRequest: Codable, Hashable, Sendable {
        public var cameraId: String
        public var fromMs: UnixMillis
        public var toMs: UnixMillis
        public var title: String
        public var note: String?
        public var watermark: Bool?
        public var blurRegions: [ExportRegion]?

        public init(cameraId: String, fromMs: UnixMillis, toMs: UnixMillis, title: String, note: String? = nil,
                    watermark: Bool? = nil, blurRegions: [ExportRegion]? = nil) {
            self.cameraId = cameraId
            self.fromMs = fromMs
            self.toMs = toMs
            self.title = title
            self.note = note
            self.watermark = watermark
            self.blurRegions = blurRegions
        }
    }

    /// A scheduled export (`ExportSchedule`): when a window ends the server
    /// files it into the evidence vault as a long export.
    struct ExportSchedule: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var cameraId: String
        /// Days a window starts on, 0 = Sunday … 6 = Saturday.
        public var days: [Int]
        /// "HH:MM", server local time.
        public var start: String
        /// "HH:MM" or "24:00"; an end at or before the start runs overnight.
        public var end: String
        public var dest: ExportDest
        public var enabled: Bool
        public var createdBy: String
        public var createdAt: UnixMillis
        /// End of the last window handled; 0 = never.
        public var lastRunAt: UnixMillis
        /// "running", "ok" or "failed: …"; empty before the first run.
        public var lastStatus: String

        public init(id: String, name: String, cameraId: String, days: [Int], start: String, end: String,
                    dest: ExportDest = .evidence, enabled: Bool = true, createdBy: String = "", createdAt: UnixMillis = 0,
                    lastRunAt: UnixMillis = 0, lastStatus: String = "") {
            self.id = id
            self.name = name
            self.cameraId = cameraId
            self.days = days
            self.start = start
            self.end = end
            self.dest = dest
            self.enabled = enabled
            self.createdBy = createdBy
            self.createdAt = createdAt
            self.lastRunAt = lastRunAt
            self.lastStatus = lastStatus
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            cameraId = try c.decodeIfPresent(String.self, forKey: .cameraId) ?? ""
            days = try c.decodeIfPresent([Int].self, forKey: .days) ?? []
            start = try c.decodeIfPresent(String.self, forKey: .start) ?? ""
            end = try c.decodeIfPresent(String.self, forKey: .end) ?? ""
            dest = try c.decodeIfPresent(ExportDest.self, forKey: .dest) ?? .evidence
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
            createdBy = try c.decodeIfPresent(String.self, forKey: .createdBy) ?? ""
            createdAt = try c.decodeIfPresent(UnixMillis.self, forKey: .createdAt) ?? 0
            lastRunAt = try c.decodeIfPresent(UnixMillis.self, forKey: .lastRunAt) ?? 0
            lastStatus = try c.decodeIfPresent(String.self, forKey: .lastStatus) ?? ""
        }
    }

    /// Body of `POST /api/v2/export-schedules`.
    struct ExportScheduleInput: Codable, Hashable, Sendable {
        public var name: String
        public var cameraId: String
        public var days: [Int]
        public var start: String
        public var end: String
        public var dest: ExportDest
        public var enabled: Bool

        public init(name: String, cameraId: String, days: [Int], start: String, end: String,
                    dest: ExportDest = .evidence, enabled: Bool = true) {
            self.name = name
            self.cameraId = cameraId
            self.days = days
            self.start = start
            self.end = end
            self.dest = dest
            self.enabled = enabled
        }
    }

    /// Body of `PATCH /api/v2/export-schedules/{id}`; nil fields keep their value.
    struct ExportSchedulePatch: Codable, Hashable, Sendable {
        public var name: String?
        public var cameraId: String?
        public var days: [Int]?
        public var start: String?
        public var end: String?
        public var dest: ExportDest?
        public var enabled: Bool?

        public init(name: String? = nil, cameraId: String? = nil, days: [Int]? = nil, start: String? = nil,
                    end: String? = nil, dest: ExportDest? = nil, enabled: Bool? = nil) {
            self.name = name
            self.cameraId = cameraId
            self.days = days
            self.start = start
            self.end = end
            self.dest = dest
            self.enabled = enabled
        }
    }

    struct ShareLinkStatus: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let active = ShareLinkStatus(rawValue: "active")
        public static let expired = ShareLinkStatus(rawValue: "expired")
        public static let revoked = ShareLinkStatus(rawValue: "revoked")
        public static let usedUp = ShareLinkStatus(rawValue: "used up")
    }

    /// An expiring link to an evidence case (`ShareLink`); never the token.
    struct ShareLink: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var evidenceId: String
        public var caseId: String
        public var title: String
        public var note: String
        public var expiresAt: UnixMillis
        /// 0 = unlimited.
        public var maxUses: Int
        public var uses: Int
        public var createdBy: String
        public var createdAt: UnixMillis
        /// 0 = not revoked.
        public var revokedAt: UnixMillis
        public var status: ShareLinkStatus

        public init(id: String, evidenceId: String, caseId: String, title: String = "", note: String = "",
                    expiresAt: UnixMillis, maxUses: Int = 0, uses: Int = 0, createdBy: String = "",
                    createdAt: UnixMillis = 0, revokedAt: UnixMillis = 0, status: ShareLinkStatus = .active) {
            self.id = id
            self.evidenceId = evidenceId
            self.caseId = caseId
            self.title = title
            self.note = note
            self.expiresAt = expiresAt
            self.maxUses = maxUses
            self.uses = uses
            self.createdBy = createdBy
            self.createdAt = createdAt
            self.revokedAt = revokedAt
            self.status = status
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            evidenceId = try c.decodeIfPresent(String.self, forKey: .evidenceId) ?? ""
            caseId = try c.decodeIfPresent(String.self, forKey: .caseId) ?? ""
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            note = try c.decodeIfPresent(String.self, forKey: .note) ?? ""
            expiresAt = try c.decodeIfPresent(UnixMillis.self, forKey: .expiresAt) ?? 0
            maxUses = try c.decodeIfPresent(Int.self, forKey: .maxUses) ?? 0
            uses = try c.decodeIfPresent(Int.self, forKey: .uses) ?? 0
            createdBy = try c.decodeIfPresent(String.self, forKey: .createdBy) ?? ""
            createdAt = try c.decodeIfPresent(UnixMillis.self, forKey: .createdAt) ?? 0
            revokedAt = try c.decodeIfPresent(UnixMillis.self, forKey: .revokedAt) ?? 0
            status = try c.decodeIfPresent(ShareLinkStatus.self, forKey: .status) ?? .active
        }
    }

    /// Body of `POST /api/v2/evidence/{id}/share`.
    struct ShareLinkRequest: Codable, Hashable, Sendable {
        /// "1h", "24h", "7d" or "custom".
        public var expiry: String
        /// Unix ms, with `expiry` "custom" only (at most 30 days ahead).
        public var expiresAt: UnixMillis?
        /// 0 = unlimited within the expiry.
        public var maxUses: Int
        public var note: String?

        public init(expiry: String, expiresAt: UnixMillis? = nil, maxUses: Int = 0, note: String? = nil) {
            self.expiry = expiry
            self.expiresAt = expiresAt
            self.maxUses = maxUses
            self.note = note
        }
    }

    /// Answer of `POST /api/v2/evidence/{id}/share`: the link, shown once.
    struct ShareLinkCreated: Codable, Hashable, Sendable {
        public var link: ShareLink
        public var token: String
        public var url: String

        public init(link: ShareLink, token: String, url: String) {
            self.link = link
            self.token = token
            self.url = url
        }
    }
}
