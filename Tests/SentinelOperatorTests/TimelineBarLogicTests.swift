import XCTest
@testable import SentinelCore

final class TimelineBarLogicTests: XCTestCase {
    private let spans = [TimelineGaps.Range(start: 0, end: 40_000), TimelineGaps.Range(start: 70_000, end: 90_000)]
    private let bounds = TimelineGaps.Range(start: 0, end: 100_000)

    func testSkipPrefersAnEventBeforeTheNextGap() {
        let target = TimelineSkip.target(from: 5_000, forward: true, spans: spans, bounds: bounds, markers: [30_000])
        XCTAssertEqual(target, TimelineSkip.Target(time: 27_000, isEvent: true))
    }

    func testSkipFallsBackToTheGapEdge() {
        let target = TimelineSkip.target(from: 5_000, forward: true, spans: spans, bounds: bounds, markers: [])
        XCTAssertEqual(target, TimelineSkip.Target(time: 70_000, isEvent: false))
        // Marker beyond the gap target: the gap comes first.
        let far = TimelineSkip.target(from: 5_000, forward: true, spans: spans, bounds: bounds, markers: [95_000])
        XCTAssertEqual(far?.isEvent, false)
    }

    func testSkipBackwardMirrorsForward() {
        let event = TimelineSkip.target(from: 80_000, forward: false, spans: spans, bounds: bounds, markers: [60_000])
        XCTAssertEqual(event, TimelineSkip.Target(time: 57_000, isEvent: true))
        let gap = TimelineSkip.target(from: 80_000, forward: false, spans: spans, bounds: bounds, markers: [10_000])
        XCTAssertEqual(gap, TimelineSkip.Target(time: 39_000, isEvent: false))
        XCTAssertNil(TimelineSkip.target(from: 100, forward: false, spans: spans, bounds: bounds, markers: []))
    }

    func testMarkerStyleSplitsMotionFromObjects() {
        XCTAssertEqual(TimelineSkip.style(kind: "motion", label: nil), .motion)
        XCTAssertEqual(TimelineSkip.style(kind: "detection", label: "Person"), .object)
        XCTAssertEqual(TimelineSkip.style(kind: "vehicle", label: nil), .object)
        XCTAssertEqual(TimelineSkip.style(kind: "object", label: "car"), .object)
    }

    func testLiveEdgePinningAndReadout() {
        XCTAssertTrue(LiveEdge.isPinned(position: 99_000, now: 100_000))
        XCTAssertFalse(LiveEdge.isPinned(position: 90_000, now: 100_000))
        XCTAssertEqual(LiveEdge.readout(live: true, time: "12:00:00"), "LIVE")
        XCTAssertEqual(LiveEdge.readout(live: false, time: "12:00:00"), "12:00:00")
    }
}
