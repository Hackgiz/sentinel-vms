import Foundation

/// Where and when recordings get moved off the Mac's disk. Backed by
/// UserDefaults so the archiver (SentinelMediaServer) and Storage UI share it.
public struct RecordingArchiveSettings: Equatable {
    public var isEnabled: Bool
    /// Folder the operator picked (usually on an external drive). Archived
    /// segments go under `<folder>/Sentinel Archive/<cameraID>/`.
    public var folderPath: String?
    /// Move segments once they're this many days old.
    public var archiveAfterDays: Int
    /// Delete archived segments after this many days; 0 = keep forever.
    public var archiveRetentionDays: Int

    private static let defaults = UserDefaults.standard
    private enum Key {
        static let enabled = "handoffgrid.archive.enabled"
        static let folder = "handoffgrid.archive.folderPath"
        static let after = "handoffgrid.archive.afterDays"
        static let retention = "handoffgrid.archive.retentionDays"
    }

    public static var current: RecordingArchiveSettings {
        get {
            RecordingArchiveSettings(
                isEnabled: defaults.bool(forKey: Key.enabled),
                folderPath: defaults.string(forKey: Key.folder),
                archiveAfterDays: max(1, defaults.object(forKey: Key.after) as? Int ?? 3),
                archiveRetentionDays: max(0, defaults.integer(forKey: Key.retention))
            )
        }
        set {
            defaults.set(newValue.isEnabled, forKey: Key.enabled)
            defaults.set(newValue.folderPath, forKey: Key.folder)
            defaults.set(newValue.archiveAfterDays, forKey: Key.after)
            defaults.set(newValue.archiveRetentionDays, forKey: Key.retention)
        }
    }

    public var archiveRootURL: URL? {
        guard let folderPath, folderPath.isEmpty == false else { return nil }
        return URL(fileURLWithPath: folderPath, isDirectory: true)
            .appendingPathComponent("Sentinel Archive", isDirectory: true)
    }

    /// The drive is plugged in and the folder exists.
    public var isReachable: Bool {
        guard let folderPath else { return false }
        var isDir: ObjCBool = false
        return FileManager.default.fileExists(atPath: folderPath, isDirectory: &isDir) && isDir.boolValue
    }

    /// Scan roots for the timeline: the archive only when enabled and mounted.
    public var activeArchiveRoot: URL? {
        isEnabled && isReachable ? archiveRootURL : nil
    }
}

/// "At this rate, when does the recording disk fill?" Retention makes usage
/// level off: once a camera has `retentionDays` of footage, cleanup deletes as
/// fast as it records. So the question is whether the footage still to come
/// before each camera levels off fits in the free space above the halt floor.
public struct StorageForecast: Equatable {
    public enum Outcome: Equatable {
        /// Not enough recent footage to measure a rate yet.
        case insufficientData
        /// Every camera levels off within the free space.
        case levelsOff(atBytes: Int64, inDays: Double)
        /// Recording halts (free space hits the floor) in this many days.
        case fills(inDays: Double)
    }

    public struct CameraRate: Equatable {
        public let cameraID: UUID
        public let bytesPerDay: Double
        public let currentBytes: Int64
        public let retentionDays: Int

        public var steadyStateBytes: Double { bytesPerDay * Double(retentionDays) }
        public var remainingGrowthBytes: Double { max(0, steadyStateBytes - Double(currentBytes)) }
    }

    public let outcome: Outcome
    public let totalBytesPerDay: Double
    public let cameras: [CameraRate]

    /// - Parameters:
    ///   - segments: LOCAL recording segments only (archived ones don't use this disk).
    ///   - retentionDays: per-camera retention (MediaMTX defaults to 7 days).
    ///   - freeBytes: available space on the recording volume.
    ///   - floorBytes: free space at which the disk guardian halts recording.
    ///   - archiveAfterDays: when archiving is on, footage leaves this disk after
    ///     this many days — the effective local retention is the shorter of the two.
    public static func compute(
        segments: [RecordingSegment],
        retentionDays: [UUID: Int],
        freeBytes: Int64,
        floorBytes: Int64 = 5_000_000_000,
        archiveAfterDays: Int? = nil,
        now: Date = Date(),
        window: TimeInterval = 48 * 3600
    ) -> StorageForecast {
        let byCamera = Dictionary(grouping: segments, by: \.cameraID)
        var rates: [CameraRate] = []
        for (cameraID, cameraSegments) in byCamera {
            let recent = cameraSegments.filter { now.timeIntervalSince($0.modifiedAt) <= window }
            guard let oldestRecent = recent.map(\.createdAt).min() else { continue }
            // Measure over the span actually covered (a camera added 6 hours ago
            // has 6 hours of data, not 48), but at least an hour to avoid noise.
            let span = max(3600, min(window, now.timeIntervalSince(oldestRecent)))
            let bytes = recent.reduce(Int64(0)) { $0 + $1.byteCount }
            var retention = retentionDays[cameraID] ?? 7
            if let archiveAfterDays { retention = min(retention, archiveAfterDays) }
            rates.append(CameraRate(
                cameraID: cameraID,
                bytesPerDay: Double(bytes) / span * 86_400,
                currentBytes: cameraSegments.reduce(0) { $0 + $1.byteCount },
                retentionDays: max(1, retention)
            ))
        }

        let totalRate = rates.reduce(0) { $0 + $1.bytesPerDay }
        guard totalRate > 0 else {
            return StorageForecast(outcome: .insufficientData, totalBytesPerDay: 0, cameras: rates)
        }

        let headroom = Double(max(0, freeBytes - floorBytes))
        let growth = rates.reduce(0) { $0 + $1.remainingGrowthBytes }
        let outcome: Outcome
        if growth <= headroom {
            let current = rates.reduce(0) { $0 + Double($1.currentBytes) }
            let days = rates.map { $0.bytesPerDay > 0 ? $0.remainingGrowthBytes / $0.bytesPerDay : 0 }.max() ?? 0
            outcome = .levelsOff(atBytes: Int64(current + growth), inDays: days)
        } else {
            // Simulate day by day: each camera grows at its rate until it levels off.
            var remaining = headroom
            var day = 0.0
            var growing = rates.filter { $0.remainingGrowthBytes > 0 }.map { ($0.bytesPerDay, $0.remainingGrowthBytes) }
            while remaining > 0, growing.isEmpty == false {
                let dailyGrowth = growing.reduce(0) { $0 + min($1.0, $1.1) }
                if dailyGrowth >= remaining {
                    day += remaining / dailyGrowth
                    remaining = 0
                    break
                }
                remaining -= dailyGrowth
                day += 1
                growing = growing.map { ($0.0, $0.1 - min($0.0, $0.1)) }.filter { $0.1 > 0 }
            }
            outcome = .fills(inDays: day)
        }
        return StorageForecast(outcome: outcome, totalBytesPerDay: totalRate, cameras: rates)
    }
}
