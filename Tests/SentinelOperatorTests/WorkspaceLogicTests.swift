import XCTest
@testable import SentinelCore

/// The pure logic behind the workspace layout: ruler ticks and zoom, gap
/// navigation, the in/out export selection, the tile grid and the camera tree.
final class TimelineMathTests: XCTestCase {
    func testSpanIsClampedToOneMinuteAndTwentyFourHours() {
        XCTAssertEqual(TimelineRuler.clampSpan(1), 60_000)
        XCTAssertEqual(TimelineRuler.clampSpan(10 * 86_400_000), 86_400_000)
        XCTAssertEqual(TimelineRuler.zoomed(span: 3_600_000, factor: 2), 1_800_000)
        XCTAssertEqual(TimelineRuler.zoomed(span: 90_000, factor: 10), 60_000)
        XCTAssertEqual(TimelineRuler.zoomed(span: 43_200_000, factor: 0.01), 86_400_000)
        XCTAssertEqual(TimelineRuler.zoomed(span: 600_000, factor: .nan), 600_000)
    }

    func testPlayheadStaysCentered() {
        let range = TimelineRuler.visibleRange(center: 1_000_000, span: 600_000)
        XCTAssertEqual(range.from, 700_000)
        XCTAssertEqual(range.to, 1_300_000)
        XCTAssertEqual(TimelineRuler.x(for: 1_000_000, width: 800, center: 1_000_000, span: 600_000), 400, accuracy: 0.001)
        XCTAssertEqual(TimelineRuler.time(atX: 0, width: 800, center: 1_000_000, span: 600_000), 700_000)
    }

    func testDraggingRightMovesBackInTime() {
        let t = TimelineRuler.panned(center: 1_000_000, dx: 80, width: 800, span: 600_000)
        XCTAssertEqual(t, 940_000)
        XCTAssertEqual(TimelineRuler.panned(center: 1_000_000, dx: -80, width: 800, span: 600_000), 1_060_000)
    }

    func testMajorStepAdaptsFromSecondsToHours() {
        XCTAssertEqual(TimelineRuler.majorStep(span: 60_000, targetCount: 8), 10_000)
        XCTAssertEqual(TimelineRuler.majorStep(span: 3_600_000, targetCount: 8), 600_000)
        XCTAssertEqual(TimelineRuler.majorStep(span: 86_400_000, targetCount: 8), 10_800_000)
    }

    func testTicksAlignToLocalTimeAndLabel() {
        // 10:00:00 to 10:01:00 UTC, no offset: majors every 10 s with seconds.
        let base: Int64 = 10 * 3_600_000
        let ticks = TimelineRuler.ticks(from: base, to: base + 60_000)
        let majors = ticks.filter { $0.level != .minor }
        XCTAssertEqual(majors.first?.label, "10:00:00")
        XCTAssertEqual(majors.map(\.time), stride(from: base, through: base + 60_000, by: 10_000).map { $0 })
        XCTAssertTrue(ticks.contains { $0.level == .minor })

        // Hour ticks over a day, in UTC+2: local midnight is a date tick.
        let day: Int64 = 20_000 * 86_400_000
        let offset = 7200
        let from = day - 2 * 3_600_000 - 7200 * 1000 + 7200 * 1000   // 22:00 UTC the day before = 00:00 local
        let dayTicks = TimelineRuler.ticks(from: from, to: from + 86_400_000, utcOffsetSeconds: offset)
        let dates = dayTicks.filter { $0.level == .day }
        XCTAssertEqual(dates.first?.time, from)
        XCTAssertEqual(dates.first?.label.count, 10)
        XCTAssertEqual(dayTicks.first { $0.level == .major }?.label.hasSuffix(":00"), true)
    }

    func testDateLabelIsCivilDate() {
        XCTAssertEqual(TimelineRuler.dateLabel(0), "1970-01-01")
        XCTAssertEqual(TimelineRuler.dateLabel(951_782_400_000), "2000-02-29")
        XCTAssertEqual(TimelineRuler.dateLabel(-86_400_000), "1969-12-31")
    }

    func testTicksStayInRange() {
        let ticks = TimelineRuler.ticks(from: 1234, to: 61_234)
        XCTAssertTrue(ticks.allSatisfy { $0.time >= 1234 && $0.time <= 61_234 })
        XCTAssertTrue(TimelineRuler.ticks(from: 5, to: 5).isEmpty)
    }

    // MARK: Gaps

    private let bounds = TimelineGaps.Range(start: 0, end: 100_000)
    private func spans() -> [TimelineGaps.Range] {
        [.init(start: 10_000, end: 30_000), .init(start: 30_500, end: 50_000), .init(start: 70_000, end: 90_000)]
    }

    func testMergeJoinsTouchingAndOverlappingSpans() {
        let merged = TimelineGaps.merge(spans() + [.init(start: 45_000, end: 52_000)])
        XCTAssertEqual(merged, [.init(start: 10_000, end: 52_000), .init(start: 70_000, end: 90_000)])
        XCTAssertEqual(TimelineGaps.merge([.init(start: 5, end: 5)]), [])
    }

    func testGapsIncludeEdgesAndSkipTinySeams() {
        XCTAssertEqual(TimelineGaps.gaps(in: spans(), bounds: bounds), [
            .init(start: 0, end: 10_000), .init(start: 50_000, end: 70_000), .init(start: 90_000, end: 100_000)
        ])
        XCTAssertEqual(TimelineGaps.gaps(in: [], bounds: bounds), [bounds])
    }

    func testNextAndPreviousGapLandOnFootage() {
        // Both jumps land on video, matching the Linux Operator: forward to
        // the start of the next recording, back to just before the end of
        // the previous one.
        XCTAssertEqual(TimelineGaps.nextGap(after: 5_000, spans: spans(), bounds: bounds), 10_000)
        XCTAssertEqual(TimelineGaps.nextGap(after: 20_000, spans: spans(), bounds: bounds), 70_000)
        XCTAssertEqual(TimelineGaps.nextGap(after: 50_000, spans: spans(), bounds: bounds), 70_000)
        XCTAssertNil(TimelineGaps.nextGap(after: 95_000, spans: spans(), bounds: bounds))
        XCTAssertEqual(TimelineGaps.previousGap(before: 80_000, spans: spans(), bounds: bounds), 49_000)
        XCTAssertEqual(TimelineGaps.previousGap(before: 65_000, spans: spans(), bounds: bounds), 49_000)
        XCTAssertNil(TimelineGaps.previousGap(before: 50_000, spans: spans(), bounds: bounds))
        XCTAssertNil(TimelineGaps.previousGap(before: 0, spans: spans(), bounds: bounds))
    }

    // MARK: Selection

    func testSelectionNeedsInBeforeOut() {
        var s = ExportSelection()
        XCTAssertNil(s.range)
        s.setIn(1000)
        XCTAssertNil(s.range)
        s.setOut(5000)
        XCTAssertEqual(s.range?.from, 1000)
        XCTAssertEqual(s.range?.to, 5000)
    }

    func testCrossingPointDropsTheOtherOne() {
        var s = ExportSelection(markIn: 1000, markOut: 5000)
        s.setIn(6000)          // in past out: out is dropped
        XCTAssertEqual(s.markIn, 6000)
        XCTAssertNil(s.markOut)
        s.setOut(2000)         // out before in: in is dropped
        XCTAssertEqual(s.markOut, 2000)
        XCTAssertNil(s.markIn)
        s.clear()
        XCTAssertTrue(s.isEmpty)
        XCTAssertNil(ExportSelection(markIn: 9, markOut: 3).markOut)
    }
}

final class WorkspaceGridTests: XCTestCase {
    func testPlaceMovesAndSwaps() {
        var g = WorkspaceGrid(columns: 2, rows: 2)
        g.place("a", at: 0)
        g.place("b", at: 1)
        g.place("a", at: 1)                       // swap
        XCTAssertEqual(g.slots, ["b", "a", nil, nil])
        g.place("a", at: 3)                       // move into free tile
        XCTAssertEqual(g.slots, ["b", nil, nil, "a"])
        XCTAssertEqual(g.cameraIDs, ["b", "a"])
    }

    func testDoubleClickGoesToFirstFreeTile() {
        var g = WorkspaceGrid(columns: 2, rows: 1)
        XCTAssertEqual(g.placeInFirstFree("a"), 0)
        XCTAssertEqual(g.placeInFirstFree("b"), 1)
        XCTAssertEqual(g.placeInFirstFree("a"), 0, "already on the grid")
        XCTAssertNil(g.placeInFirstFree("c"), "no free tile")
    }

    func testDropSeveralFillsFollowingFreeTiles() {
        var g = WorkspaceGrid(columns: 2, rows: 2)
        g.place("x", at: 0)
        g.place(["a", "b", "c"], startingAt: 2)
        XCTAssertEqual(g.slots, ["x", "b", "a", "c"])
    }

    func testFillOpensAsNearSquareGrid() {
        var g = WorkspaceGrid()
        g.fill(with: ["1", "2", "3", "4", "5", "2"])
        XCTAssertEqual(g.columns, 3)
        XCTAssertEqual(g.rows, 2)
        XCTAssertEqual(g.slots, ["1", "2", "3", "4", "5", nil])
        XCTAssertTrue(WorkspaceGrid.dimensions(forCount: 1) == (1, 1))
        XCTAssertTrue(WorkspaceGrid.dimensions(forCount: 4) == (2, 2))
        XCTAssertTrue(WorkspaceGrid.dimensions(forCount: 10) == (4, 3))
    }

    func testResizeKeepsPositionsAndRehomesOverflow() {
        var g = WorkspaceGrid(columns: 3, rows: 3)
        g.place("a", at: 0)
        g.place("b", at: 4)
        g.place("c", at: 8)
        g.resize(columns: 2, rows: 2)
        XCTAssertEqual(g.slots[0], "a")
        XCTAssertEqual(g.slots[3], "b")
        XCTAssertTrue(g.cameraIDs.contains("c"), "overflow camera moves to a free tile")
    }

    func testViewRoundTrip() {
        var g = WorkspaceGrid(columns: 2, rows: 2)
        g.place("a", at: 1)
        g.place("b", at: 2)
        let input = g.viewInput(name: "Lobby")
        XCTAssertEqual(input.tiles.map(\.cameraId), ["a", "b"])
        let view = V2.View(id: "v", createdAt: 0, updatedAt: 0, name: input.name, columns: input.columns, rows: input.rows, tiles: input.tiles)
        XCTAssertEqual(WorkspaceGrid(view: view), g)
    }

    func testPruneDropsUnavailableCameras() {
        var g = WorkspaceGrid(columns: 2, rows: 1)
        g.place("a", at: 0)
        g.place("b", at: 1)
        g.prune { $0 == "a" }
        XCTAssertEqual(g.slots, ["a", nil])
    }
}

final class CameraTreeTests: XCTestCase {
    private let items = [
        CameraTree.Item(id: "1", name: "Front door", location: "Lobby", serverID: "s1", groupIDs: ["g1"], online: true),
        CameraTree.Item(id: "2", name: "Dock", location: "Yard", serverID: "s2", groupIDs: [], online: false),
        CameraTree.Item(id: "3", name: "Back door", serverID: "s1", groupIDs: ["g1", "g2"], online: true)
    ]
    private let servers = [CameraTree.Parent(id: "s1", name: "Main", online: true), CameraTree.Parent(id: "s2", name: "Yard server", online: false)]

    func testGroupsByServer() {
        let tree = CameraTree.build(items: items, parents: servers, grouping: .server, query: "")
        XCTAssertEqual(tree.map(\.parent.name), ["Main", "Yard server"])
        XCTAssertEqual(tree[0].cameras.map(\.id), ["1", "3"])
        XCTAssertEqual(tree[1].totalCount, 1)
    }

    func testGroupsByGroupWithUngrouped() {
        let groups = [CameraTree.Parent(id: "g1", name: "Entrances", online: true), CameraTree.Parent(id: "g2", name: "Rear", online: true)]
        let tree = CameraTree.build(items: items, parents: groups, grouping: .group, query: "")
        XCTAssertEqual(tree.map(\.parent.name), ["Entrances", "Rear", "Ungrouped"])
        XCTAssertEqual(tree[0].cameras.map(\.id), ["1", "3"], "a camera shows in each of its groups")
        XCTAssertEqual(tree[2].cameras.map(\.id), ["2"])
    }

    func testSearchFiltersCamerasAndHidesEmptyParents() {
        let byName = CameraTree.build(items: items, parents: servers, grouping: .server, query: " DOOR ")
        XCTAssertEqual(byName.count, 1)
        XCTAssertEqual(byName[0].cameras.map(\.id), ["1", "3"])
        XCTAssertEqual(byName[0].totalCount, 2)
        let byLocation = CameraTree.build(items: items, parents: servers, grouping: .server, query: "yard")
        XCTAssertEqual(byLocation.map(\.parent.id), ["s2"], "matches the camera's location and the server name")
        let byServer = CameraTree.build(items: items, parents: servers, grouping: .server, query: "main")
        XCTAssertEqual(byServer[0].cameras.count, 2, "a matching server shows all its cameras")
    }

    func testUnknownServerStillListsItsCameras() {
        let tree = CameraTree.build(items: items, parents: [], grouping: .server, query: "")
        XCTAssertEqual(Set(tree.map(\.parent.id)), ["s1", "s2"])
    }
}
