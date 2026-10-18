import XCTest
@testable import SentinelCore

/// Video wall (Wave 10, stream A): layout encode/decode, display matching,
/// tour scheduling and the alarm-monitor queue.
final class WallLayoutCodecTests: XCTestCase {
    func testDecodesServerLayoutAndRoundTrips() throws {
        let json = """
        {"id":"w1","name":"Control room","shared":true,"createdBy":"u1","createdAt":1700000000000,"updatedAt":1700000001000,"canEdit":false,
         "monitors":[
          {"slot":1,"screenHint":"Built-in Retina Display","viewId":"v1","tour":{"viewIds":["v1","v2"],"seconds":15}},
          {"slot":0,"screenHint":"DELL U2720Q","screenIndex":1,"cameraIds":["c1","c2"],"alarmMonitor":true,"fullscreen":true},
          {"slot":2,"screenHint":"LG"}
         ]}
        """
        let layout = try WallLayoutCodec.decode(Data(json.utf8))
        XCTAssertEqual(layout.name, "Control room")
        XCTAssertTrue(layout.shared)
        XCTAssertFalse(layout.canEdit)
        let ordered = WallLayoutCodec.ordered(layout)
        XCTAssertEqual(ordered.map(\.slot), [0, 1, 2])
        XCTAssertEqual(ordered[0].screenIndex, 1)
        XCTAssertTrue(ordered[0].isAlarmMonitor)
        XCTAssertTrue(ordered[0].isFullscreen)
        XCTAssertFalse(ordered[1].isAlarmMonitor)
        XCTAssertEqual(WallLayoutCodec.content(of: ordered[0]), .cameras(["c1", "c2"]))
        XCTAssertEqual(WallLayoutCodec.content(of: ordered[1]), .tour(WallTourPlan(viewIDs: ["v1", "v2"], seconds: 15), startViewID: "v1"))
        XCTAssertEqual(WallLayoutCodec.content(of: ordered[2]), .empty)

        let input = V2.WallLayoutInput(name: layout.name, shared: layout.shared, monitors: layout.monitors)
        let data = try WallLayoutCodec.encode(input)
        let back = try SentinelAPI.jsonDecoder.decode(V2.WallLayoutInput.self, from: data)
        XCTAssertEqual(back, input)
        // Unset optionals stay off the wire (the server rejects unknown/nulls only by value).
        let text = String(decoding: data, as: UTF8.self)
        XCTAssertFalse(text.contains("null"))
    }

    func testNullMonitorsDecodeEmpty() throws {
        let layout = try WallLayoutCodec.decode(Data(#"{"id":"w","name":"n","shared":false,"monitors":null,"createdBy":"u","createdAt":0,"updatedAt":0,"canEdit":true}"#.utf8))
        XCTAssertEqual(layout.monitors, [])
    }

    func testViewAndCamerasContent() {
        XCTAssertEqual(WallLayoutCodec.content(of: V2.WallMonitor(slot: 0, screenHint: "", viewId: "v9")), .view("v9"))
        XCTAssertEqual(WallLayoutCodec.content(of: V2.WallMonitor(slot: 0, screenHint: "", cameraIds: [])), .cameras([]))
        // A tour whose views were all filtered out falls back to its view.
        let filtered = V2.WallMonitor(slot: 0, screenHint: "", viewId: "v1", tour: V2.WallTour(viewIds: [], seconds: 10))
        XCTAssertEqual(WallLayoutCodec.content(of: filtered), .view("v1"))
        // A start view not in the tour is ignored.
        let tour = V2.WallMonitor(slot: 0, screenHint: "", viewId: "x", tour: V2.WallTour(viewIds: ["a", "b"], seconds: 2))
        XCTAssertEqual(WallLayoutCodec.content(of: tour), .tour(WallTourPlan(viewIDs: ["a", "b"], seconds: 5), startViewID: nil))
    }

    func testSnapshotsBecomeOrderedMonitors() {
        let snaps = [
            WallMonitorSnapshot(displayName: "LG", displayIndex: 2, cameraIDs: ["c3"]),
            WallMonitorSnapshot(displayName: "Main", displayIndex: 0, originX: 800, viewID: "v2", alarmMonitor: true),
            WallMonitorSnapshot(displayName: "Main", displayIndex: 0, originX: 0, cameraIDs: ["c1", "c1", "c2"], fullscreen: true),
            WallMonitorSnapshot(displayName: "Gone", displayIndex: nil, tour: WallTourPlan(viewIDs: ["a", "a", "b"], seconds: 99999)),
        ]
        let monitors = WallLayoutCodec.monitors(from: snaps)
        XCTAssertEqual(monitors.map(\.slot), [0, 1, 2, 3])
        XCTAssertEqual(monitors.map(\.screenHint), ["Main", "Main", "LG", "Gone"])
        XCTAssertEqual(monitors[0].cameraIds, ["c1", "c2"])
        XCTAssertEqual(monitors[0].fullscreen, true)
        XCTAssertNil(monitors[0].alarmMonitor)
        XCTAssertEqual(monitors[1].viewId, "v2")
        XCTAssertNil(monitors[1].cameraIds)
        XCTAssertEqual(monitors[1].alarmMonitor, true)
        XCTAssertEqual(monitors[2].screenIndex, 2)
        XCTAssertNil(monitors[3].screenIndex)
        XCTAssertEqual(monitors[3].tour, V2.WallTour(viewIds: ["a", "b"], seconds: 3600))
        XCTAssertEqual(monitors[3].viewId, "a")
        XCTAssertNil(monitors[3].cameraIds)
    }

    func testSnapshotsAreCappedToServerLimits() {
        let many = (0..<20).map { WallMonitorSnapshot(displayName: "D", displayIndex: 0, originX: Double($0), cameraIDs: (0..<80).map { "c\($0)" }) }
        let monitors = WallLayoutCodec.monitors(from: many)
        XCTAssertEqual(monitors.count, 16)
        XCTAssertEqual(monitors[0].cameraIds?.count, 64)
    }

    func testSavingChecks() {
        let m = [V2.WallMonitor(slot: 0, screenHint: "Main", cameraIds: [])]
        XCTAssertThrowsError(try WallLayoutSaving.input(name: "  ", shared: false, canManage: true, monitors: m)) {
            XCTAssertEqual($0 as? WallLayoutSaving.Problem, .emptyName)
        }
        XCTAssertThrowsError(try WallLayoutSaving.input(name: String(repeating: "x", count: 101), shared: false, canManage: true, monitors: m))
        XCTAssertThrowsError(try WallLayoutSaving.input(name: "Wall", shared: true, canManage: false, monitors: m)) {
            XCTAssertEqual($0 as? WallLayoutSaving.Problem, .sharedNeedsPermission)
        }
        XCTAssertThrowsError(try WallLayoutSaving.input(name: "Wall", shared: false, canManage: false, monitors: [])) {
            XCTAssertEqual($0 as? WallLayoutSaving.Problem, .noMonitors)
        }
        let input = try? WallLayoutSaving.input(name: " Night ", shared: true, canManage: true, monitors: m)
        XCTAssertEqual(input?.name, "Night")
        XCTAssertEqual(input?.shared, true)

        let shared = V2.WallLayout(id: "w", name: "n", shared: true, createdBy: "u1", canEdit: true)
        XCTAssertFalse(WallLayoutSaving.canEdit(shared, userID: "u1", canManage: false))
        XCTAssertTrue(WallLayoutSaving.canEdit(shared, userID: "u2", canManage: true))
        let mine = V2.WallLayout(id: "w", name: "n", createdBy: "u1", canEdit: false)
        XCTAssertTrue(WallLayoutSaving.canEdit(mine, userID: "u1", canManage: false))
    }
}

final class WallDisplayMatcherTests: XCTestCase {
    private let displays = [
        WallDisplay(name: "Built-in Retina Display", index: 0),
        WallDisplay(name: "DELL U2720Q", index: 1),
        WallDisplay(name: "LG UltraFine", index: 2),
    ]

    private func monitor(_ slot: Int, _ hint: String, _ index: Int? = nil) -> V2.WallMonitor {
        V2.WallMonitor(slot: slot, screenHint: hint, screenIndex: index, cameraIds: [])
    }

    func testMatchesByNameEvenWhenTheIndexMoved() {
        let placed = WallDisplayMatcher.place([monitor(0, "LG UltraFine", 1), monitor(1, "dell u2720q ", 2)], on: displays)
        XCTAssertEqual(placed, [
            WallPlacement(slot: 0, displayIndex: 2, matchedBy: .name),
            WallPlacement(slot: 1, displayIndex: 1, matchedBy: .name),
        ])
    }

    func testFallsBackToIndexThenMainDisplay() {
        let placed = WallDisplayMatcher.place([monitor(0, "Projector", 2), monitor(1, "Old TV", 7), monitor(2, "Nameless")], on: displays)
        XCTAssertEqual(placed, [
            WallPlacement(slot: 0, displayIndex: 2, matchedBy: .index),
            WallPlacement(slot: 1, displayIndex: 0, matchedBy: .fallback),
            WallPlacement(slot: 2, displayIndex: 0, matchedBy: .fallback),
        ])
    }

    func testIdenticalDisplaysUseTheSavedIndexThenSpread() {
        let twins = [WallDisplay(name: "Main", index: 0), WallDisplay(name: "DELL", index: 1), WallDisplay(name: "DELL", index: 2)]
        // Saved index decides between twins.
        XCTAssertEqual(WallDisplayMatcher.place([monitor(0, "DELL", 2)], on: twins), [WallPlacement(slot: 0, displayIndex: 2, matchedBy: .name)])
        // Without usable indices, the two monitors land on different twins.
        let spread = WallDisplayMatcher.place([monitor(0, "DELL"), monitor(1, "DELL", 9)], on: twins)
        XCTAssertEqual(Set(spread.map(\.displayIndex)), [1, 2])
        // An exact index claim wins over spreading, regardless of slot order.
        let mixed = WallDisplayMatcher.place([monitor(0, "DELL"), monitor(1, "DELL", 1)], on: twins)
        XCTAssertEqual(mixed, [
            WallPlacement(slot: 0, displayIndex: 2, matchedBy: .name),
            WallPlacement(slot: 1, displayIndex: 1, matchedBy: .name),
        ])
    }

    func testFewerDisplaysThanSaved() {
        let laptop = [WallDisplay(name: "Built-in Retina Display", index: 0)]
        let placed = WallDisplayMatcher.place([monitor(0, "DELL U2720Q", 1), monitor(1, "LG UltraFine", 2)], on: laptop)
        XCTAssertEqual(placed.map(\.displayIndex), [0, 0])
        XCTAssertEqual(placed.map(\.matchedBy), [.fallback, .fallback])
        XCTAssertEqual(WallDisplayMatcher.place([monitor(0, "x")], on: []), [])
    }
}

final class TourScheduleTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)

    private func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    func testCyclesAndWraps() {
        let tour = TourSchedule(count: 3, seconds: 10, start: t0)
        XCTAssertEqual([0, 9.99, 10, 25, 30, 61].map { tour.index(at: at($0)) }, [0, 0, 1, 2, 0, 0])
        XCTAssertEqual(tour.nextChange(after: at(0)), at(10))
        XCTAssertEqual(tour.nextChange(after: at(25)), at(30))
        XCTAssertEqual(tour.viewStart(at: at(25)), at(20))
        // Before the start: the first view.
        XCTAssertEqual(tour.index(at: at(-5)), 0)
    }

    func testStartIndex() {
        let tour = TourSchedule(count: 3, seconds: 10, start: t0, startIndex: 2)
        XCTAssertEqual(tour.index(at: t0), 2)
        XCTAssertEqual(tour.index(at: at(10)), 0)
        XCTAssertEqual(TourSchedule(count: 3, seconds: 10, start: t0, startIndex: -1).index(at: t0), 2)
    }

    func testCaptionShowsBrieflyAfterEachChange() {
        let tour = TourSchedule(count: 2, seconds: 10, start: t0)
        XCTAssertTrue(tour.captionVisible(at: at(0)))
        XCTAssertTrue(tour.captionVisible(at: at(2.9)))
        XCTAssertFalse(tour.captionVisible(at: at(3)))
        XCTAssertTrue(tour.captionVisible(at: at(11)))
        XCTAssertEqual(tour.captionEnds(after: at(1)), at(3))
        XCTAssertNil(tour.captionEnds(after: at(5)))
        XCTAssertEqual(tour.nextEvent(after: at(1)), at(3))
        XCTAssertEqual(tour.nextEvent(after: at(5)), at(10))
    }

    func testPauseFreezesAndResumeGivesAFullInterval() {
        var tour = TourSchedule(count: 3, seconds: 10, start: t0)
        tour.pause(at: at(8))
        XCTAssertTrue(tour.isPaused)
        XCTAssertEqual(tour.index(at: at(100)), 0)
        XCTAssertNil(tour.nextChange(after: at(100)))
        tour.pause(at: at(50)) // a second pause changes nothing
        tour.resume(at: at(100))
        XCTAssertFalse(tour.isPaused)
        XCTAssertEqual(tour.index(at: at(100)), 0)
        XCTAssertEqual(tour.nextChange(after: at(100)), at(110))
        XCTAssertEqual(tour.index(at: at(110)), 1)
        XCTAssertTrue(tour.captionVisible(at: at(101)))
        // Resuming on a later view keeps that view.
        tour.pause(at: at(115))
        tour.resume(at: at(200))
        XCTAssertEqual(tour.index(at: at(200)), 1)
        XCTAssertEqual(tour.index(at: at(210)), 2)
    }

    func testSkip() {
        var tour = TourSchedule(count: 3, seconds: 10, start: t0)
        tour.skip(by: 1, at: at(4))
        XCTAssertEqual(tour.index(at: at(4)), 1)
        XCTAssertEqual(tour.nextChange(after: at(4)), at(14))
        tour.skip(by: -2, at: at(5))
        XCTAssertEqual(tour.index(at: at(5)), 2)
        tour.pause(at: at(6))
        tour.skip(by: 1, at: at(7))
        XCTAssertEqual(tour.index(at: at(60)), 0)
        XCTAssertTrue(tour.isPaused)
    }

    func testSingleOrNoViews() {
        let one = TourSchedule(count: 1, seconds: 10, start: t0)
        XCTAssertEqual(one.index(at: at(1000)), 0)
        XCTAssertNil(one.nextChange(after: at(5)))
        let none = TourSchedule(count: 0, seconds: 10, start: t0)
        XCTAssertEqual(none.index(at: at(5)), 0)
        XCTAssertFalse(none.captionVisible(at: t0))
    }
}

final class AlarmMonitorQueueTests: XCTestCase {
    private func alarm(_ id: String, state: V2.AlarmState = .new, camera: String? = "c1", created: Int64 = 1000) -> V2.Alarm {
        let json = """
        {"id":"\(id)","cameraId":\(camera.map { "\"\($0)\"" } ?? "null"),"kind":"Person","title":"T \(id)","detail":"",
         "severity":"Critical","state":"\(state.rawValue)","owner":"","createdAt":\(created),"updatedAt":\(created),
         "lastEventAt":\(created),"eventCount":1,"responseLog":[]}
        """
        return try! JSONDecoder().decode(V2.Alarm.self, from: Data(json.utf8))
    }

    func testNewestFirstAndOnlyNewAlarmsOnCameras() {
        var q = AlarmMonitorQueue()
        XCTAssertTrue(q.apply(alarm("a", created: 1000), isNew: true))
        XCTAssertTrue(q.apply(alarm("b", created: 2000), isNew: true))
        XCTAssertFalse(q.apply(alarm("old", created: 500), isNew: true))
        XCTAssertEqual(q.current?.id, "b")
        XCTAssertEqual(q.queued.map(\.id), ["a", "old"])
        // Not new, no camera, or already handled: ignored.
        q.apply(alarm("u", created: 3000), isNew: false)
        q.apply(alarm("site", camera: nil, created: 3000), isNew: true)
        q.apply(alarm("empty", camera: "", created: 3000), isNew: true)
        q.apply(alarm("ack", state: .acknowledged, created: 3000), isNew: true)
        XCTAssertEqual(q.items.map(\.id), ["b", "a", "old"])
    }

    func testUpdatesKeepPlaceAndHandledAlarmsLeave() {
        var q = AlarmMonitorQueue()
        q.apply(alarm("a", created: 1000), isNew: true)
        q.apply(alarm("b", created: 2000), isNew: true)
        var updated = alarm("a", created: 1000)
        updated.title = "Renamed"
        XCTAssertFalse(q.apply(updated, isNew: false))
        XCTAssertEqual(q.items.last?.title, "Renamed")
        // Acknowledged elsewhere: gone; the next one comes up.
        XCTAssertTrue(q.apply(alarm("b", state: .acknowledged, created: 2000), isNew: false))
        XCTAssertEqual(q.current?.id, "a")
        q.apply(alarm("a", state: .resolved, created: 1000), isNew: false)
        XCTAssertTrue(q.isEmpty)
    }

    func testCapacityDropsOldest() {
        var q = AlarmMonitorQueue(capacity: 3)
        for i in 1...5 { q.apply(alarm("a\(i)", created: Int64(i)), isNew: true) }
        XCTAssertEqual(q.items.map(\.id), ["a5", "a4", "a3"])
    }

    func testDismissAndPromote() {
        var q = AlarmMonitorQueue()
        q.apply(alarm("a", created: 1), isNew: true)
        q.apply(alarm("b", created: 2), isNew: true)
        q.apply(alarm("c", created: 3), isNew: true)
        q.promote("a")
        XCTAssertEqual(q.items.map(\.id), ["a", "c", "b"])
        q.dismiss("a")
        XCTAssertEqual(q.current?.id, "c")
        // A dismissed alarm's later update doesn't bring it back.
        q.apply(alarm("a", created: 1), isNew: false)
        XCTAssertEqual(q.items.map(\.id), ["c", "b"])
        q.removeAll()
        XCTAssertNil(q.current)
    }
}

final class WallLayoutAPITests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
    }

    func testPathsAndBodies() async throws {
        let layout = #"{"id":"w1","name":"Wall","shared":false,"monitors":[{"slot":0,"screenHint":"Main","cameraIds":["c1"]}],"createdBy":"u1","createdAt":1,"updatedAt":2,"canEdit":true}"#
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod!, request.url!.path) {
            case ("GET", "/api/v2/wall-layouts"): return .json(200, #"{"items":[\#(layout)]}"#)
            case ("POST", "/api/v2/wall-layouts"): return .json(201, layout)
            case ("PATCH", "/api/v2/wall-layouts/w1"): return .json(200, layout)
            case ("GET", "/api/v2/wall-layouts/w1"): return .json(200, layout)
            default: return .json(204, "")
            }
        }
        let api = makeAPI()
        let list = try await api.wallLayouts()
        XCTAssertEqual(list.first?.monitors.first?.cameraIds, ["c1"])
        _ = try await api.createWallLayout(V2.WallLayoutInput(name: "Wall", monitors: list[0].monitors))
        _ = try await api.updateWallLayout("w1", V2.WallLayoutPatch(name: "Renamed"))
        _ = try await api.wallLayout("w1")
        try await api.deleteWallLayout("w1")
        let calls = MockURLProtocol.requests.map { "\($0.request.httpMethod!) \($0.request.url!.path)" }
        XCTAssertEqual(calls, ["GET /api/v2/wall-layouts", "POST /api/v2/wall-layouts", "PATCH /api/v2/wall-layouts/w1",
                               "GET /api/v2/wall-layouts/w1", "DELETE /api/v2/wall-layouts/w1"])
        let patch = String(decoding: MockURLProtocol.requests[2].body ?? Data(), as: UTF8.self)
        XCTAssertEqual(patch, #"{"name":"Renamed"}"#)
    }
}
