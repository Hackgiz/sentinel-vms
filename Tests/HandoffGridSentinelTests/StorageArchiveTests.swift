import XCTest
@testable import SentinelCore
@testable import SentinelMediaServer

final class StorageForecastTests: XCTestCase {
    private let gb: Int64 = 1_000_000_000
    private let now = Date()

    /// One camera recording 10 GB/day for the last 2 days.
    private func segments(camera: UUID, gbPerDay: Int64) -> [RecordingSegment] {
        (0..<48).map { hour in
            let start = now.addingTimeInterval(-Double(48 - hour) * 3600)
            return RecordingSegment(id: "\(camera)-\(hour)", cameraID: camera, fileURL: URL(fileURLWithPath: "/tmp/\(hour).mp4"),
                                    createdAt: start, modifiedAt: start.addingTimeInterval(3600), byteCount: gbPerDay * gb / 24)
        }
    }

    func testLevelsOffWhenRetentionFits() {
        let cam = UUID()
        // 20 GB on disk, 7-day retention → steady state 70 GB; 50 GB more needed, 200 GB free.
        let f = StorageForecast.compute(segments: segments(camera: cam, gbPerDay: 10), retentionDays: [cam: 7],
                                        freeBytes: 200 * gb, now: now)
        guard case .levelsOff(let atBytes, let inDays) = f.outcome else { return XCTFail("\(f.outcome)") }
        XCTAssertEqual(Double(atBytes) / Double(gb), 70, accuracy: 1)
        XCTAssertEqual(inDays, 5, accuracy: 0.2)
    }

    func testFillsWhenRetentionDoesNotFit() {
        let cam = UUID()
        // Needs 50 GB more but only 25 GB above the 5 GB floor → fills in ~2.5 days.
        let f = StorageForecast.compute(segments: segments(camera: cam, gbPerDay: 10), retentionDays: [cam: 7],
                                        freeBytes: 30 * gb, now: now)
        guard case .fills(let days) = f.outcome else { return XCTFail("\(f.outcome)") }
        XCTAssertEqual(days, 2.5, accuracy: 0.2)
    }

    func testArchivingShortensEffectiveRetention() {
        let cam = UUID()
        let f = StorageForecast.compute(segments: segments(camera: cam, gbPerDay: 10), retentionDays: [cam: 30],
                                        freeBytes: 30 * gb, archiveAfterDays: 2, now: now)
        guard case .levelsOff = f.outcome else { return XCTFail("archive after 2d should keep ~20 GB local: \(f.outcome)") }
    }

    func testNoDataIsReportedNotGuessed() {
        let f = StorageForecast.compute(segments: [], retentionDays: [:], freeBytes: 100 * gb, now: now)
        XCTAssertEqual(f.outcome, .insufficientData)
    }
}

final class RecordingArchiverTests: XCTestCase {
    private var temp: URL!

    override func setUpWithError() throws {
        temp = FileManager.default.temporaryDirectory.appendingPathComponent("archiver-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: temp, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: temp)
    }

    private func makeSegment(root: URL, camera: UUID, name: String, ageDays: Double) throws -> URL {
        let dir = root.appendingPathComponent(camera.uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent(name)
        try Data(repeating: 7, count: 4096).write(to: url)
        let date = Date().addingTimeInterval(-ageDays * 86_400)
        try FileManager.default.setAttributes([.creationDate: date, .modificationDate: date], ofItemAtPath: url.path)
        return url
    }

    func testMovesOnlyOldSegmentsAndKeepsTimestamps() throws {
        let recordings = temp.appendingPathComponent("Recordings")
        let drive = temp.appendingPathComponent("Drive")
        try FileManager.default.createDirectory(at: drive, withIntermediateDirectories: true)
        let cam = UUID()
        let old = try makeSegment(root: recordings, camera: cam, name: "old.mp4", ageDays: 5)
        let fresh = try makeSegment(root: recordings, camera: cam, name: "fresh.mp4", ageDays: 0.5)
        let oldCreated = try old.resourceValues(forKeys: [.creationDateKey]).creationDate!

        let settings = RecordingArchiveSettings(isEnabled: true, folderPath: drive.path, archiveAfterDays: 3, archiveRetentionDays: 0)
        let result = RecordingArchiver.run(recordingRoot: recordings, settings: settings)

        XCTAssertEqual(result.moved, 1)
        XCTAssertEqual(result.failed, 0)
        XCTAssertFalse(FileManager.default.fileExists(atPath: old.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: fresh.path))
        let archived = drive.appendingPathComponent("Sentinel Archive/\(cam.uuidString)/old.mp4")
        let archivedCreated = try archived.resourceValues(forKeys: [.creationDateKey]).creationDate!
        XCTAssertEqual(archivedCreated.timeIntervalSince1970, oldCreated.timeIntervalSince1970, accuracy: 1)
    }

    func testMissingDriveLeavesFootageInPlace() throws {
        let recordings = temp.appendingPathComponent("Recordings")
        let old = try makeSegment(root: recordings, camera: UUID(), name: "old.mp4", ageDays: 5)
        let settings = RecordingArchiveSettings(isEnabled: true, folderPath: temp.appendingPathComponent("Unplugged").path,
                                                archiveAfterDays: 1, archiveRetentionDays: 0)
        let result = RecordingArchiver.run(recordingRoot: recordings, settings: settings)
        XCTAssertNotNil(result.skippedReason)
        XCTAssertTrue(FileManager.default.fileExists(atPath: old.path))
    }

    func testArchiveRetentionPrunesOnlyExpired() throws {
        let drive = temp.appendingPathComponent("Drive")
        let archiveRoot = drive.appendingPathComponent("Sentinel Archive")
        let cam = UUID()
        let expired = try makeSegment(root: archiveRoot, camera: cam, name: "a.mp4", ageDays: 40)
        let kept = try makeSegment(root: archiveRoot, camera: cam, name: "b.mp4", ageDays: 10)
        let settings = RecordingArchiveSettings(isEnabled: true, folderPath: drive.path, archiveAfterDays: 3, archiveRetentionDays: 30)
        let result = RecordingArchiver.run(recordingRoot: temp.appendingPathComponent("Recordings"), settings: settings)
        XCTAssertEqual(result.pruned, 1)
        XCTAssertFalse(FileManager.default.fileExists(atPath: expired.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: kept.path))
    }
}
