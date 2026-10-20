import Foundation

// Edge recording recovery (`/api/v2/cameras/{cameraId}/edge-recovery`,
// `/api/v2/edge-recovery/{taskId}`): fill recording gaps from the camera's
// own SD card (ONVIF Profile G). Wave 11, stream A.

public extension V2 {
    struct EdgeRecoveryState: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let running = EdgeRecoveryState(rawValue: "running")
        public static let done = EdgeRecoveryState(rawValue: "done")
        public static let failed = EdgeRecoveryState(rawValue: "failed")
        public static let canceled = EdgeRecoveryState(rawValue: "canceled")
    }

    struct EdgeRecovery: Codable, Hashable, Sendable {
        /// The matching entry in the tasks list (kind export).
        public var taskId: String
        public var state: EdgeRecoveryState
        /// 0–100.
        public var progress: Int
        /// Why it failed; on a done job, a note that some pieces failed.
        public var error: String?
        public var cameraId: String
        public var fromMs: UnixMillis
        public var toMs: UnixMillis
        /// Holes found in the local recordings.
        public var gaps: Int
        /// Footage pieces to fetch (known once the camera answered).
        public var pieces: Int
        /// Segments written.
        public var recovered: Int
        /// Pieces that brought nothing.
        public var skipped: Int
        /// Seconds of footage recovered.
        public var seconds: Double

        public init(taskId: String, state: EdgeRecoveryState, progress: Int = 0, error: String? = nil, cameraId: String,
                    fromMs: UnixMillis, toMs: UnixMillis, gaps: Int = 0, pieces: Int = 0, recovered: Int = 0,
                    skipped: Int = 0, seconds: Double = 0) {
            self.taskId = taskId
            self.state = state
            self.progress = progress
            self.error = error
            self.cameraId = cameraId
            self.fromMs = fromMs
            self.toMs = toMs
            self.gaps = gaps
            self.pieces = pieces
            self.recovered = recovered
            self.skipped = skipped
            self.seconds = seconds
        }
    }

    struct EdgeRecoveryRequest: Codable, Hashable, Sendable {
        public var fromMs: UnixMillis
        public var toMs: UnixMillis

        public init(fromMs: UnixMillis, toMs: UnixMillis) {
            self.fromMs = fromMs
            self.toMs = toMs
        }
    }
}
