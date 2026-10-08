import Foundation
import SentinelCore

/// Moves aging recordings to the operator's archive drive and prunes the
/// archive by its own retention. A local segment is only deleted after its
/// archive copy exists with the same size and timestamps — a pulled drive or
/// failed copy just leaves the footage where it was.
public struct RecordingArchiveResult: Equatable, Sendable {
    public var moved = 0
    public var movedBytes: Int64 = 0
    public var pruned = 0
    public var failed = 0
    public var skippedReason: String?

    public var summary: String {
        if let skippedReason { return skippedReason }
        var parts = ["Archived \(moved) segment\(moved == 1 ? "" : "s") (\(ByteCountFormatter.string(fromByteCount: movedBytes, countStyle: .file)))"]
        if pruned > 0 { parts.append("removed \(pruned) expired from the archive") }
        if failed > 0 { parts.append("\(failed) failed and stayed local") }
        return parts.joined(separator: ", ") + "."
    }
}

enum RecordingArchiver {
    /// Segments touched this recently may still be open for writing.
    static let settleSeconds: TimeInterval = 300

    static func run(recordingRoot: URL, settings: RecordingArchiveSettings, now: Date = Date()) -> RecordingArchiveResult {
        var result = RecordingArchiveResult()
        guard settings.isEnabled else {
            result.skippedReason = "Archiving is off."
            return result
        }
        guard settings.isReachable, let archiveRoot = settings.archiveRootURL else {
            result.skippedReason = "Archive drive isn't connected — footage stays on this Mac until it is."
            return result
        }

        let fm = FileManager.default
        let cutoff = now.addingTimeInterval(-Double(settings.archiveAfterDays) * 86_400)
        let local = MediaIngestStore.collectAllSegments(recordingRoot: recordingRoot, archiveRoot: nil)

        for segment in local where segment.modifiedAt < cutoff && now.timeIntervalSince(segment.modifiedAt) > settleSeconds {
            let cameraDir = archiveRoot.appendingPathComponent(segment.cameraID.uuidString, isDirectory: true)
            let destination = cameraDir.appendingPathComponent(segment.fileName)
            do {
                try fm.createDirectory(at: cameraDir, withIntermediateDirectories: true)
                if fm.fileExists(atPath: destination.path) == false {
                    let partial = destination.appendingPathExtension("partial")
                    try? fm.removeItem(at: partial)
                    try fm.copyItem(at: segment.fileURL, to: partial)
                    // The timeline places segments by creation date; copies get a
                    // fresh one, so carry the originals over before publishing.
                    try fm.setAttributes([.creationDate: segment.createdAt, .modificationDate: segment.modifiedAt], ofItemAtPath: partial.path)
                    try fm.moveItem(at: partial, to: destination)
                }
                let archivedSize = (try? destination.resourceValues(forKeys: [.fileSizeKey]))?.fileSize ?? -1
                guard Int64(archivedSize) == segment.byteCount else {
                    result.failed += 1
                    continue
                }
                try fm.removeItem(at: segment.fileURL)
                result.moved += 1
                result.movedBytes += segment.byteCount
            } catch {
                result.failed += 1
            }
        }

        if settings.archiveRetentionDays > 0 {
            let expiry = now.addingTimeInterval(-Double(settings.archiveRetentionDays) * 86_400)
            let archived = MediaIngestStore.collectAllSegments(recordingRoot: archiveRoot, archiveRoot: nil)
            for segment in archived where segment.modifiedAt < expiry {
                // Only ever delete inside the archive folder we own.
                guard segment.fileURL.standardizedFileURL.path.hasPrefix(archiveRoot.standardizedFileURL.path + "/") else { continue }
                if (try? fm.removeItem(at: segment.fileURL)) != nil { result.pruned += 1 }
            }
        }
        return result
    }
}

extension MediaIngestStore {
    /// One archive pass off the main thread, then refresh the timeline.
    @discardableResult
    public func runArchivePass() async -> RecordingArchiveResult {
        let root = recordingRootURL
        let settings = RecordingArchiveSettings.current
        let result = await Task.detached(priority: .utility) {
            RecordingArchiver.run(recordingRoot: root, settings: settings)
        }.value
        if result.moved > 0 || result.pruned > 0 {
            refreshRecordingSegments()
        }
        return result
    }

    /// Hourly archive pass for the life of the app.
    public func startArchiveScheduler(onResult: @escaping @MainActor (RecordingArchiveResult) -> Void) {
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 120_000_000_000)
            while Task.isCancelled == false {
                guard let self else { return }
                if RecordingArchiveSettings.current.isEnabled {
                    let result = await self.runArchivePass()
                    onResult(result)
                }
                try? await Task.sleep(nanoseconds: 3_600_000_000_000)
            }
        }
    }
}
