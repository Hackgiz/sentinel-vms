import Foundation

// Detection zones, privacy masks and line-crossing tripwires
// (`/api/v2/cameras/{id}/zones`). Wave 11 stream E.

public extension V2 {
    /// detect: motion/object detection only counts inside; mask: blacked out of
    /// every snapshot, thumbnail and export; line: a two-point tripwire.
    enum ZoneKind: String, Codable, CaseIterable, Hashable, Sendable {
        case detect, mask, line
    }

    /// Which way crossings of a line count, relative to p0 → p1.
    enum LineDirection: String, Codable, CaseIterable, Hashable, Sendable {
        case ltr, rtl, both
    }

    /// The alarm a crossing raises; `info` logs the event only.
    enum LineSeverity: String, Codable, CaseIterable, Hashable, Sendable {
        case critical = "Critical"
        case warning = "Warning"
        case info = "Info"
    }

    /// Line metadata. Unknown or damaged values decode as nil, which reads as
    /// the server's defaults (`both`, `Critical`) through `ZoneLines`.
    struct ZoneLineMeta: Codable, Hashable, Sendable {
        public var direction: LineDirection?
        public var severity: LineSeverity?

        public init(direction: LineDirection? = nil, severity: LineSeverity? = nil) {
            self.direction = direction
            self.severity = severity
        }

        private enum CodingKeys: String, CodingKey { case direction, severity }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            direction = (try? c.decodeIfPresent(String.self, forKey: .direction)).flatMap { $0.flatMap(LineDirection.init(rawValue:)) }
            severity = (try? c.decodeIfPresent(String.self, forKey: .severity)).flatMap { $0.flatMap(LineSeverity.init(rawValue:)) }
        }
    }

    struct CameraZone: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var kind: ZoneKind
        /// For a line, the rule name carried on its Line crossing events.
        public var name: String
        /// A polygon (detect, mask; 3–20 vertices) or the two ends of a line, [x, y] 0–1.
        public var points: [[Double]]
        public var meta: ZoneLineMeta?
        public var enabled: Bool
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis

        public init(id: String, kind: ZoneKind, name: String, points: [[Double]], meta: ZoneLineMeta? = nil,
                    enabled: Bool = true, createdAt: UnixMillis = 0, updatedAt: UnixMillis = 0) {
            self.id = id
            self.kind = kind
            self.name = name
            self.points = points
            self.meta = meta
            self.enabled = enabled
            self.createdAt = createdAt
            self.updatedAt = updatedAt
        }

        private enum CodingKeys: String, CodingKey { case id, kind, name, points, meta, enabled, createdAt, updatedAt }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            kind = try c.decode(ZoneKind.self, forKey: .kind)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            points = try c.decodeIfPresent([[Double]].self, forKey: .points) ?? []
            // Damaged meta (not an object) reads as defaults, as the server does.
            meta = (try? c.decodeIfPresent(ZoneLineMeta.self, forKey: .meta)) ?? nil
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
            createdAt = try c.decodeIfPresent(UnixMillis.self, forKey: .createdAt) ?? 0
            updatedAt = try c.decodeIfPresent(UnixMillis.self, forKey: .updatedAt) ?? 0
        }
    }

    /// One zone of a PUT. An existing `id` keeps the zone; omit it for a new one.
    struct CameraZoneInput: Codable, Hashable, Sendable {
        public var id: String?
        public var kind: ZoneKind
        public var name: String
        public var points: [[Double]]
        /// Lines only.
        public var meta: ZoneLineMeta?
        public var enabled: Bool

        public init(id: String? = nil, kind: ZoneKind, name: String, points: [[Double]],
                    meta: ZoneLineMeta? = nil, enabled: Bool = true) {
            self.id = id
            self.kind = kind
            self.name = name
            self.points = points
            self.meta = meta
            self.enabled = enabled
        }
    }

    /// `PUT …/zones` body: the camera's full zone set, replaced atomically.
    struct CameraZonesPut: Codable, Hashable, Sendable {
        public var zones: [CameraZoneInput]

        public init(zones: [CameraZoneInput]) {
            self.zones = zones
        }
    }
}
