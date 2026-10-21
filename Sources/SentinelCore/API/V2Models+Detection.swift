import Foundation

// Detection depth DTOs (wave 16 on the server, wave 17 on the Mac): the model
// catalog and detector settings, faces and people, plates, semantic search.
// Every enum is open so a newer server's values don't fail a whole response.

public extension V2 {
    // MARK: Models and detector

    struct ModelPurpose: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let detector = ModelPurpose(rawValue: "detector")
        public static let reid = ModelPurpose(rawValue: "reid")
        public static let audio = ModelPurpose(rawValue: "audio")
        public static let faceDetect = ModelPurpose(rawValue: "face-detect")
        public static let faceEmbed = ModelPurpose(rawValue: "face-embed")
        public static let plateDet = ModelPurpose(rawValue: "plate-det")
        public static let plateRec = ModelPurpose(rawValue: "plate-rec")
        public static let clipImage = ModelPurpose(rawValue: "clip-image")
        public static let clipText = ModelPurpose(rawValue: "clip-text")
    }

    struct ModelState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let missing = ModelState(rawValue: "missing")
        public static let downloading = ModelState(rawValue: "downloading")
        public static let installed = ModelState(rawValue: "installed")
        /// A file is present but its SHA-256 does not match.
        public static let corrupt = ModelState(rawValue: "corrupt")
    }

    /// A model download job (task kind `model-download`).
    struct ModelDownload: Codable, Hashable, Sendable {
        public var modelId: String
        public var taskId: String
        /// running, done, failed, canceled (the backfill job's states).
        public var state: AppearanceBackfillState
        /// Bytes so far.
        public var done: Int64
        /// Bytes; 0 = unknown.
        public var total: Int64
        public var error: String?
        public var startedAt: UnixMillis?
        public var finishedAt: UnixMillis?

        public init(modelId: String, taskId: String = "", state: AppearanceBackfillState, done: Int64 = 0, total: Int64 = 0,
                    error: String? = nil, startedAt: UnixMillis? = nil, finishedAt: UnixMillis? = nil) {
            self.modelId = modelId
            self.taskId = taskId
            self.state = state
            self.done = done
            self.total = total
            self.error = error
            self.startedAt = startedAt
            self.finishedAt = finishedAt
        }
    }

    /// One catalog model with its install state.
    struct Model: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var title: String
        public var purpose: ModelPurpose
        public var file: String
        public var sizeBytes: Int64
        public var license: String
        /// Where it is downloaded from (URL).
        public var source: String?
        /// One line: what it is for and what it needs.
        public var note: String?
        /// Ships with the install; cannot be deleted.
        public var bundled: Bool
        public var installed: Bool
        public var state: ModelState
        public var download: ModelDownload?

        public init(id: String, title: String, purpose: ModelPurpose, file: String = "", sizeBytes: Int64 = 0,
                    license: String = "", source: String? = nil, note: String? = nil, bundled: Bool = false,
                    installed: Bool = false, state: ModelState = .missing, download: ModelDownload? = nil) {
            self.id = id
            self.title = title
            self.purpose = purpose
            self.file = file
            self.sizeBytes = sizeBytes
            self.license = license
            self.source = source
            self.note = note
            self.bundled = bundled
            self.installed = installed
            self.state = state
            self.download = download
        }
    }

    /// `GET /models`: the catalog and where model files live, for hand installs.
    struct ModelCatalog: Codable, Hashable, Sendable {
        @NullableArray public var items: [Model]
        public var dir: String

        public init(items: [Model] = [], dir: String = "") {
            self.items = items
            self.dir = dir
        }
    }

    /// `GET`/`PUT /detector`.
    struct DetectorSettings: Codable, Hashable, Sendable {
        /// What is actually running.
        public struct Status: Codable, Hashable, Sendable {
            /// Catalog id of the model actually loaded.
            public var model: String
            /// CPU | OpenVINO | CoreML.
            public var engine: String
            public var device: String?
            /// Passes per second per camera in use.
            public var effectiveRate: Int
            /// The saved model differs from the loaded one until the detector restarts.
            public var pending: Bool
            /// Why the saved model is not running.
            public var reason: String?
            /// Mean inference time, last minute.
            public var inferMs: Double?

            public init(model: String, engine: String, device: String? = nil, effectiveRate: Int, pending: Bool = false,
                        reason: String? = nil, inferMs: Double? = nil) {
                self.model = model
                self.engine = engine
                self.device = device
                self.effectiveRate = effectiveRate
                self.pending = pending
                self.reason = reason
                self.inferMs = inferMs
            }
        }

        /// Catalog id as saved; "" = yolox-tiny.
        public var model: String
        /// 0 = auto, else 1–5.
        public var rate: Int
        public var status: Status

        public init(model: String, rate: Int, status: Status) {
            self.model = model
            self.rate = rate
            self.status = status
        }
    }

    /// The body of `PUT /detector`; absent fields keep their value.
    struct DetectorInput: Codable, Hashable, Sendable {
        public var model: String?
        public var rate: Int?

        public init(model: String? = nil, rate: Int? = nil) {
            self.model = model
            self.rate = rate
        }
    }

    // MARK: Faces and people

    /// What a match does to the event's alarm.
    struct WatchAlert: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        /// The alarm keeps its severity; the name or plate goes into its detail.
        public static let `default` = WatchAlert(rawValue: "default")
        /// The alarm is closed with a note (a known person or vehicle).
        public static let never = WatchAlert(rawValue: "never")
        /// A Critical Watchlist alarm is raised.
        public static let always = WatchAlert(rawValue: "always")
    }

    /// `GET`/`PUT /faces`.
    struct FacesStatus: Codable, Hashable, Sendable {
        public var enabled: Bool
        /// Both face models installed and loaded.
        public var available: Bool
        public var reason: String?
        public var detectModel: String?
        public var embedModel: String?
        public var people: Int
        public var unassigned: Int
        public var unassignedDays: Int

        public init(enabled: Bool = false, available: Bool = false, reason: String? = nil, detectModel: String? = nil,
                    embedModel: String? = nil, people: Int = 0, unassigned: Int = 0, unassignedDays: Int = 7) {
            self.enabled = enabled
            self.available = available
            self.reason = reason
            self.detectModel = detectModel
            self.embedModel = embedModel
            self.people = people
            self.unassigned = unassigned
            self.unassignedDays = unassignedDays
        }
    }

    /// The body of `PUT /faces`.
    struct FacesInput: Codable, Hashable, Sendable {
        public var enabled: Bool?
        /// 1–90.
        public var unassignedDays: Int?

        public init(enabled: Bool? = nil, unassignedDays: Int? = nil) {
            self.enabled = enabled
            self.unassignedDays = unassignedDays
        }
    }

    /// One stored face (its crop is `GET /faces/{id}/crop.jpg`).
    struct Face: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var eventId: String
        public var cameraId: String
        /// Unix ms.
        public var at: UnixMillis
        /// Normalized to the thumbnail.
        public var box: [Double]?
        /// Detector confidence.
        public var score: Double
        public var personId: String?
        /// Cosine similarity to the matched person, when matched.
        public var similarity: Double?

        public init(id: String, eventId: String = "", cameraId: String = "", at: UnixMillis = 0, box: [Double]? = nil,
                    score: Double = 0, personId: String? = nil, similarity: Double? = nil) {
            self.id = id
            self.eventId = eventId
            self.cameraId = cameraId
            self.at = at
            self.box = box
            self.score = score
            self.personId = personId
            self.similarity = similarity
        }
    }

    struct Person: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var notes: String?
        public var alert: WatchAlert
        @NullableArray public var faces: [Face]
        /// Unix ms of the newest matched event.
        public var lastSeenAt: UnixMillis?
        public var lastSeenCameraId: String?
        public var createdAt: UnixMillis

        public init(id: String, name: String, notes: String? = nil, alert: WatchAlert = .default, faces: [Face] = [],
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

    /// The body of `POST /people` (with `faceId`, an unassigned face to enroll)
    /// and `PATCH /people/{id}` (absent fields are kept).
    struct PersonInput: Codable, Hashable, Sendable {
        public var name: String?
        public var notes: String?
        public var alert: WatchAlert?
        /// POST only.
        public var faceId: String?

        public init(name: String? = nil, notes: String? = nil, alert: WatchAlert? = nil, faceId: String? = nil) {
            self.name = name
            self.notes = notes
            self.alert = alert
            self.faceId = faceId
        }

        public var isEmpty: Bool { name == nil && notes == nil && alert == nil && faceId == nil }
    }

    // MARK: Plates

    /// `GET`/`PUT /lpr`.
    struct LPRStatus: Codable, Hashable, Sendable {
        public var enabled: Bool
        public var available: Bool
        public var reason: String?
        public var detModel: String?
        public var recModel: String?
        /// Watchlist size.
        public var plates: Int
        /// Vehicle events with a plate in the last 24 h.
        public var readLast24h: Int?

        public init(enabled: Bool = false, available: Bool = false, reason: String? = nil, detModel: String? = nil,
                    recModel: String? = nil, plates: Int = 0, readLast24h: Int? = nil) {
            self.enabled = enabled
            self.available = available
            self.reason = reason
            self.detModel = detModel
            self.recModel = recModel
            self.plates = plates
            self.readLast24h = readLast24h
        }
    }

    /// A watchlist entry.
    struct Plate: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        /// Normalized, A–Z0–9.
        public var plate: String
        /// Who it belongs to, e.g. "Delivery van".
        public var name: String?
        public var notes: String?
        public var alert: WatchAlert
        public var lastSeenAt: UnixMillis?
        public var lastSeenCameraId: String?
        /// Newest 20 events with this plate (`GET /plates/{id}` only).
        public var sightings: [Event]?
        public var createdAt: UnixMillis

        public init(id: String, plate: String, name: String? = nil, notes: String? = nil, alert: WatchAlert = .default,
                    lastSeenAt: UnixMillis? = nil, lastSeenCameraId: String? = nil, sightings: [Event]? = nil,
                    createdAt: UnixMillis = 0) {
            self.id = id
            self.plate = plate
            self.name = name
            self.notes = notes
            self.alert = alert
            self.lastSeenAt = lastSeenAt
            self.lastSeenCameraId = lastSeenCameraId
            self.sightings = sightings
            self.createdAt = createdAt
        }
    }

    /// The body of `POST /plates` (plate required) and `PATCH /plates/{id}`.
    struct PlateInput: Codable, Hashable, Sendable {
        public var plate: String?
        public var name: String?
        public var notes: String?
        public var alert: WatchAlert?

        public init(plate: String? = nil, name: String? = nil, notes: String? = nil, alert: WatchAlert? = nil) {
            self.plate = plate
            self.name = name
            self.notes = notes
            self.alert = alert
        }

        public var isEmpty: Bool { plate == nil && name == nil && notes == nil && alert == nil }
    }

    // MARK: Semantic search

    /// `GET /semantic`.
    struct SemanticStatus: Codable, Hashable, Sendable {
        public var available: Bool
        public var reason: String?
        public var model: String?
        /// Events with a vector from the current model.
        public var indexed: Int?
        public var backfill: AppearanceBackfill?

        public init(available: Bool, reason: String? = nil, model: String? = nil, indexed: Int? = nil,
                    backfill: AppearanceBackfill? = nil) {
            self.available = available
            self.reason = reason
            self.model = model
            self.indexed = indexed
            self.backfill = backfill
        }
    }

    /// The body of `POST /events/semantic`.
    struct SemanticRequest: Codable, Hashable, Sendable {
        /// 2–200 characters.
        public var text: String
        public var cameras: [String]?
        /// Unix ms, inclusive.
        public var from: UnixMillis?
        /// Unix ms, exclusive.
        public var to: UnixMillis?
        /// 1–100 (30 when omitted).
        public var limit: Int?
        /// Cosine floor, 0–1 (0.2 when omitted).
        public var minScore: Double?

        public init(text: String, cameras: [String]? = nil, from: UnixMillis? = nil, to: UnixMillis? = nil,
                    limit: Int? = nil, minScore: Double? = nil) {
            self.text = text
            self.cameras = cameras
            self.from = from
            self.to = to
            self.limit = limit
            self.minScore = minScore
        }
    }

    struct SemanticHit: Codable, Hashable, Sendable {
        public var event: Event
        /// Cosine similarity, 0–1.
        public var score: Double

        public init(event: Event, score: Double) {
            self.event = event
            self.score = score
        }
    }

    struct SemanticResults: Codable, Hashable, Sendable {
        @NullableArray public var items: [SemanticHit]
        public var model: String

        public init(items: [SemanticHit] = [], model: String = "") {
            self.items = items
            self.model = model
        }
    }
}
