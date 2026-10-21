// V2Models+Detection.swift
// Wave 16 detection models the phone reads (WAVE17-CLIENT-PARITY.md,
// iPhone): the people gallery and its faces, and semantic search
// ("Describe it"). Shapes follow `linux/api/openapi.yaml`. Plates, models
// and detector settings aren't on the phone, so they aren't here.

import Foundation

extension V2 {
    /// What a match does to the event's alarm: `default` names them in the
    /// alarm, `never` closes it (a known person), `always` raises a Critical
    /// Watchlist alarm.
    struct WatchAlert: OpenStringEnum {
        let rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        static let `default` = WatchAlert(rawValue: "default")
        static let never = WatchAlert(rawValue: "never")
        static let always = WatchAlert(rawValue: "always")
    }

    /// A stored face; its crop is `GET /api/v2/faces/{id}/crop.jpg`.
    struct Face: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var eventId: String
        var cameraId: String
        var at: UnixMillis
        /// Normalized to the thumbnail.
        var box: [Double]?
        /// Detector confidence.
        var score: Double
        var personId: String?
        /// Cosine similarity to the matched person, when matched.
        var similarity: Double?
    }

    /// `GET /api/v2/people` items and `GET /api/v2/people/{id}`.
    struct Person: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var name: String
        var notes: String?
        var alert: WatchAlert
        @NullableArray var faces: [Face]
        var lastSeenAt: UnixMillis?
        var lastSeenCameraId: String?
        var createdAt: UnixMillis

        init(id: String, name: String, notes: String? = nil, alert: WatchAlert = .default, faces: [Face] = [],
             lastSeenAt: UnixMillis? = nil, lastSeenCameraId: String? = nil, createdAt: UnixMillis = 0) {
            self.id = id
            self.name = name
            self.notes = notes
            self.alert = alert
            self.faces = faces
            self.lastSeenAt = lastSeenAt
            self.lastSeenCameraId = lastSeenCameraId
            self.createdAt = createdAt
        }
    }

    /// `GET /api/v2/faces`: whether face recognition is on and can run.
    struct FacesStatus: Codable, Hashable, Sendable {
        var enabled: Bool
        var available: Bool
        var reason: String?
        var detectModel: String?
        var embedModel: String?
        var people: Int
        var unassigned: Int
        var unassignedDays: Int
    }

    /// The semantic index job, as `GET /api/v2/semantic` reports it.
    struct SemanticBackfill: Codable, Hashable, Sendable {
        var taskId: String
        /// running, done, failed, canceled
        var state: String
        var total: Int
        var processed: Int
        var indexed: Int
        var skipped: Int
        var error: String?
        var startedAt: UnixMillis
        var finishedAt: UnixMillis?
    }

    /// `GET /api/v2/semantic`.
    struct SemanticStatus: Codable, Hashable, Sendable {
        var available: Bool
        /// Why it's unavailable (which model is missing, …).
        var reason: String?
        var model: String?
        /// Events with a vector from the current model.
        var indexed: Int?
        var backfill: SemanticBackfill?

        init(available: Bool, reason: String? = nil, model: String? = nil, indexed: Int? = nil,
             backfill: SemanticBackfill? = nil) {
            self.available = available
            self.reason = reason
            self.model = model
            self.indexed = indexed
            self.backfill = backfill
        }
    }

    /// `POST /api/v2/events/semantic` body.
    struct SemanticSearch: Codable, Hashable, Sendable {
        var text: String
        var cameras: [String]?
        var from: UnixMillis?
        var to: UnixMillis?
        var limit: Int?
        var minScore: Double?
    }

    /// One match: the event and its cosine similarity, 0...1.
    struct SemanticHit: Codable, Hashable, Identifiable, Sendable {
        var event: Event
        var score: Double

        var id: String { event.id }
    }

    /// `POST /api/v2/events/semantic` answer, best first.
    struct SemanticResults: Codable, Hashable, Sendable {
        @NullableArray var items: [SemanticHit]
        var model: String
    }
}
