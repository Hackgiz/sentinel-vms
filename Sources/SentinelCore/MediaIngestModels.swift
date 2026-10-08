import Foundation

public struct LocalLiveStream: Identifiable, Hashable, Sendable {
    public let cameraID: UUID
    public let cameraName: String
    public let playlistURL: URL
    public let frameDirectoryURL: URL
    public let logURL: URL
    public let startedAt: Date
    public let processID: Int32
    /// MediaMTX HLS URL when the proxy is running. When present, the AI
    /// detector decodes frames directly from this stream via VideoToolbox
    /// instead of polling the JPEG dump in `frameDirectoryURL`.
    public let hlsURL: URL?

    public var id: UUID { cameraID }

    public init(cameraID: UUID, cameraName: String, playlistURL: URL, frameDirectoryURL: URL, logURL: URL, startedAt: Date, processID: Int32, hlsURL: URL? = nil) {
        self.cameraID = cameraID
        self.cameraName = cameraName
        self.playlistURL = playlistURL
        self.frameDirectoryURL = frameDirectoryURL
        self.logURL = logURL
        self.startedAt = startedAt
        self.processID = processID
        self.hlsURL = hlsURL
    }
}

public struct RecordingSessionStatus: Identifiable, Hashable, Sendable {
    public let cameraID: UUID
    public let cameraName: String
    public let startedAt: Date
    public let directoryURL: URL
    public let logURL: URL
    public let processID: Int32

    public var id: UUID { cameraID }

    public init(cameraID: UUID, cameraName: String, startedAt: Date, directoryURL: URL, logURL: URL, processID: Int32) {
        self.cameraID = cameraID
        self.cameraName = cameraName
        self.startedAt = startedAt
        self.directoryURL = directoryURL
        self.logURL = logURL
        self.processID = processID
    }
}

public struct RecordingSegment: Identifiable, Hashable, Sendable {
    public let id: String
    public let cameraID: UUID
    public let fileURL: URL
    public let createdAt: Date
    public let modifiedAt: Date
    public let byteCount: Int64
    /// Lives in the external recording archive rather than the local Recordings
    /// folder (plays normally; local retention never touches it).
    public let isArchived: Bool

    public init(id: String, cameraID: UUID, fileURL: URL, createdAt: Date, modifiedAt: Date, byteCount: Int64, isArchived: Bool = false) {
        self.isArchived = isArchived
        self.id = id
        self.cameraID = cameraID
        self.fileURL = fileURL
        self.createdAt = createdAt
        self.modifiedAt = modifiedAt
        self.byteCount = byteCount
    }

    public var fileName: String {
        fileURL.lastPathComponent
    }

    public var timeLabel: String {
        RecordingFormatters.timeFormatter.string(from: createdAt)
    }

    public var dateLabel: String {
        RecordingFormatters.dateFormatter.string(from: createdAt)
    }

    public var sizeLabel: String {
        ByteCountFormatter.string(fromByteCount: byteCount, countStyle: .file)
    }
}

public enum VideoDetectionKind: String, Codable, Hashable, Sendable {
    case motion
    case person

    public var label: String {
        switch self {
        case .motion: return "Motion"
        case .person: return "Person"
        }
    }
}

public struct MotionEvent: Identifiable, Codable, Hashable, Sendable {
    public let id: UUID
    public let cameraID: UUID
    public let timestamp: Date
    public let intensity: Double
    public let kind: VideoDetectionKind

    public init(
        id: UUID = UUID(),
        cameraID: UUID,
        timestamp: Date = Date(),
        intensity: Double,
        kind: VideoDetectionKind = .motion
    ) {
        self.id = id
        self.cameraID = cameraID
        self.timestamp = timestamp
        self.intensity = intensity
        self.kind = kind
    }
}

public struct RecordingStorageSummary: Sendable {
    public let segmentCount: Int
    public let activeRecordingCount: Int
    public let totalBytes: Int64
    public let recordingRootURL: URL

    public init(segmentCount: Int, activeRecordingCount: Int, totalBytes: Int64, recordingRootURL: URL) {
        self.segmentCount = segmentCount
        self.activeRecordingCount = activeRecordingCount
        self.totalBytes = totalBytes
        self.recordingRootURL = recordingRootURL
    }

    public var totalSizeLabel: String {
        ByteCountFormatter.string(fromByteCount: totalBytes, countStyle: .file)
    }
}

public struct CameraHealthSnapshot: Hashable, Sendable {
    public let status: CameraStatus
    public let isLive: Bool
    public let isRecording: Bool
    public let lastFrameAt: Date?
    public let estimatedFPS: Double
    public let segmentCount: Int
    public let eventCount: Int
    public let lastError: String?

    public init(status: CameraStatus, isLive: Bool, isRecording: Bool, lastFrameAt: Date?, estimatedFPS: Double, segmentCount: Int, eventCount: Int, lastError: String?) {
        self.status = status
        self.isLive = isLive
        self.isRecording = isRecording
        self.lastFrameAt = lastFrameAt
        self.estimatedFPS = estimatedFPS
        self.segmentCount = segmentCount
        self.eventCount = eventCount
        self.lastError = lastError
    }

    public var frameAgeLabel: String {
        guard let lastFrameAt else {
            return "No frames yet"
        }

        let age = max(0, Int(Date().timeIntervalSince(lastFrameAt)))
        if age < 2 {
            return "Now"
        }

        if age < 60 {
            return "\(age)s ago"
        }

        return "\(age / 60)m ago"
    }

    public var fpsLabel: String {
        estimatedFPS > 0 ? String(format: "%.1f FPS", estimatedFPS) : "Pending"
    }

    public var activityLabel: String {
        if isRecording {
            return "Recording"
        }

        if isLive {
            return "Live preview"
        }

        return "Idle"
    }
}

public enum RecordingFormatters {
    public static let timeFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .none
        formatter.timeStyle = .medium
        return formatter
    }()

    public static let dateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .medium
        formatter.timeStyle = .none
        return formatter
    }()

    public static let fileFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd-HHmmss"
        return formatter
    }()
}

@MainActor
public protocol MediaIngestProviding: AnyObject {
    var streamErrors: [UUID: String] { get }
    var lastLiveFrameAt: [UUID: Date] { get }
    func effectiveStatus(for camera: CameraFeed) -> CameraStatus
    func recordingSession(for cameraID: UUID) -> RecordingSessionStatus?
    func segments(for cameraID: UUID) -> [RecordingSegment]
    func motionEvents(for cameraID: UUID) -> [MotionEvent]
    func registerMotion(cameraID: UUID, intensity: Double, threshold: Double, timestamp: Date)
    func registerPersonDetection(cameraID: UUID, confidence: Double, timestamp: Date)
}

@MainActor
public extension MediaIngestProviding {
    func registerMotion(cameraID: UUID, intensity: Double, timestamp: Date = Date()) {
        registerMotion(cameraID: cameraID, intensity: intensity, threshold: 0.09, timestamp: timestamp)
    }
}
