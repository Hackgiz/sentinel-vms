import XCTest
@testable import SentinelCore

final class PlaybackWindowTests: XCTestCase {
    private let h: Int64 = 3_600_000
    private let m: Int64 = 60_000
    private let now: Int64 = 100 * 3_600_000

    func testWindowIsPlayheadPlusMinusOneHour() {
        let w = PlaybackWindow.around(50 * h, now: now)
        XCTAssertEqual(w, PlaybackWindow(from: 49 * h, to: 51 * h))
    }

    func testWindowClampsToNowAndZero() {
        XCTAssertEqual(PlaybackWindow.around(now - 10 * m, now: now), PlaybackWindow(from: now - 10 * m - h, to: now))
        XCTAssertEqual(PlaybackWindow.around(now + 5 * h, now: now).to, now)
        XCTAssertEqual(PlaybackWindow.around(10 * m, now: now).from, 0)
    }

    func testRefetchWhenOutsideOrNearAnEnd() {
        let w = PlaybackWindow.around(50 * h, now: now)
        XCTAssertFalse(w.needsRefetch(playhead: 50 * h, now: now))
        XCTAssertFalse(w.needsRefetch(playhead: 50 * h + 49 * m, now: now))
        XCTAssertTrue(w.needsRefetch(playhead: 51 * h - 9 * m, now: now), "within 10 min of the end")
        XCTAssertTrue(w.needsRefetch(playhead: 49 * h + 9 * m, now: now), "within 10 min of the start")
        XCTAssertTrue(w.needsRefetch(playhead: 80 * h, now: now), "a seek outside")
        XCTAssertTrue(w.needsRefetch(playhead: 10 * h, now: now))
    }

    func testEndClampedToNowDoesNotRefetchEverySecond() {
        let w = PlaybackWindow.around(now - 5 * m, now: now)
        XCTAssertEqual(w.to, now)
        XCTAssertFalse(w.needsRefetch(playhead: now - 4 * m, now: now + 30_000), "the window can't grow yet")
        XCTAssertTrue(w.needsRefetch(playhead: now - 4 * m, now: now + 11 * m), "ten minutes of new footage")
    }

    func testStartOfTimeDoesNotLoop() {
        let w = PlaybackWindow.around(5 * m, now: now)
        XCTAssertEqual(w.from, 0)
        XCTAssertFalse(w.needsRefetch(playhead: 5 * m, now: now))
    }
}
