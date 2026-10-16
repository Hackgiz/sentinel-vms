import Foundation

// System Monitor feed (`GET /api/v2/system/health`).

public extension V2 {
    struct CameraHealth: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var online: Bool
        public var recording: Bool
        /// Age of the newest recording file, measured on the recorder.
        public var lastSegmentAgeSec: Int64?
        /// Ingest rate; nil when unknown.
        public var kbps: Double?
        public init(id: String, name: String, online: Bool, recording: Bool, lastSegmentAgeSec: Int64? = nil, kbps: Double? = nil) {
            self.id = id; self.name = name; self.online = online; self.recording = recording
            self.lastSegmentAgeSec = lastSegmentAgeSec; self.kbps = kbps
        }
    }

    struct MemoryUsage: Codable, Hashable, Sendable {
        public var used: Int64
        public var total: Int64
        public init(used: Int64, total: Int64) { self.used = used; self.total = total }
    }

    struct DiskUsage: Codable, Hashable, Sendable {
        public var used: Int64
        public var free: Int64
        public var total: Int64
        public init(used: Int64, free: Int64, total: Int64) { self.used = used; self.free = free; self.total = total }
    }

    /// One recording server. An offline one still appears, with null metrics
    /// and its last-known cameras.
    struct ServerHealth: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var online: Bool
        public var latencyMs: Int64?
        /// 1-minute load as a percent of the cores (0…100); nil when unknown.
        public var cpu: Double?
        public var mem: MemoryUsage?
        public var disk: DiskUsage?
        public var dbBytes: Int64?
        public var uptimeSec: Int64?
        public var version: String
        @NullableArray public var cameras: [CameraHealth]

        public init(id: String, name: String, online: Bool, latencyMs: Int64? = nil, cpu: Double? = nil,
                    mem: MemoryUsage? = nil, disk: DiskUsage? = nil, dbBytes: Int64? = nil,
                    uptimeSec: Int64? = nil, version: String = "", cameras: [CameraHealth] = []) {
            self.id = id; self.name = name; self.online = online; self.latencyMs = latencyMs
            self.cpu = cpu; self.mem = mem; self.disk = disk; self.dbBytes = dbBytes
            self.uptimeSec = uptimeSec; self.version = version; self.cameras = cameras
        }
    }

    struct SystemHealth: Codable, Hashable, Sendable {
        @NullableArray public var servers: [ServerHealth]
        public init(servers: [ServerHealth]) { self.servers = servers }
    }
}
