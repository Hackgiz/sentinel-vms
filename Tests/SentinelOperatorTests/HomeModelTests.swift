import XCTest
@testable import SentinelCore

/// Wave 20: the Home workspace's numbers, cards and layout.
final class HomeModelTests: XCTestCase {
    private let now = Date(unixMillis: 1_760_000_000_000)
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func camera(_ id: String, online: Bool = true, recording: Bool = true) -> V2.Camera {
        var json = Fixtures.camera.replacingOccurrences(of: #""id":"c1""#, with: #""id":"\#(id)""#)
        if online == false { json = json.replacingOccurrences(of: #""online":true"#, with: #""online":false"#) }
        if recording == false { json = json.replacingOccurrences(of: #""recording":true"#, with: #""recording":false"#) }
        return try! SentinelAPI.jsonDecoder.decode(V2.Camera.self, from: Data(json.utf8))
    }

    private func alarm(_ id: String, state: String = "New", severity: String = "Warning", last: Int64 = 1_760_000_000_000) -> V2.Alarm {
        let json = Fixtures.alarm.replacingOccurrences(of: #""id":"a1""#, with: #""id":"\#(id)""#)
            .replacingOccurrences(of: #""state":"New""#, with: #""state":"\#(state)""#)
            .replacingOccurrences(of: #""severity":"Critical""#, with: #""severity":"\#(severity)""#)
            .replacingOccurrences(of: #""lastEventAt":1760000001000"#, with: #""lastEventAt":\#(last)"#)
        return try! SentinelAPI.jsonDecoder.decode(V2.Alarm.self, from: Data(json.utf8))
    }

    private func event(_ id: String, camera: String = "c1", at: Int64, thumbnail: Bool = true) -> V2.Event {
        let json = Fixtures.event.replacingOccurrences(of: #""id":"e1""#, with: #""id":"\#(id)""#)
            .replacingOccurrences(of: #""cameraId":"c1""#, with: #""cameraId":"\#(camera)""#)
            .replacingOccurrences(of: #""createdAt":1760000000000"#, with: #""createdAt":\#(at)"#)
            .replacingOccurrences(of: #""hasThumbnail":true"#, with: #""hasThumbnail":\#(thumbnail)"#)
        return try! SentinelAPI.jsonDecoder.decode(V2.Event.self, from: Data(json.utf8))
    }

    func testStatTilesCountWhatTheOperatorHolds() {
        let cameras = [camera("a"), camera("b"), camera("c", online: false), camera("d", recording: false)]
        let midnight = utc.startOfDay(for: now).unixMillis
        let events = [event("e1", at: midnight + 1000), event("e2", at: midnight - 1000), event("e3", at: now.unixMillis)]
        let alarms = [alarm("a1"), alarm("a2", state: "Acknowledged"), alarm("a3")]
        let stats = HomeModel.stats(cameras: cameras, openAlarms: alarms, events: events, now: now, calendar: utc)
        XCTAssertEqual(stats.map(\.kind), [.cameras, .recording, .events, .alarms])
        XCTAssertEqual(stats.map(\.value), ["3/4", "2", "2", "3"])
        XCTAssertEqual(stats.map(\.note), ["1 offline", "of 4 cameras", "AI detections", "2 new"])
        XCTAssertEqual(stats.map(\.tone), [.bad, .good, .info, .bad])
    }

    func testStatNotesWhenAllIsWell() {
        let stats = HomeModel.stats(cameras: [camera("a"), camera("b")], openAlarms: [alarm("a1", state: "Acknowledged")],
                                    events: [], now: now, calendar: utc)
        XCTAssertEqual(stats.map(\.note), ["All online", "Every online camera", "AI detections", "Being handled"])
        XCTAssertEqual(stats[3].tone, .warning)
        let empty = HomeModel.stats(cameras: [], openAlarms: [], events: [], now: now, calendar: utc)
        XCTAssertEqual(empty.map(\.value), ["0/0", "0", "0", "0"])
        XCTAssertEqual(empty[0].note, "None yet")
        XCTAssertEqual(empty[3].note, "Nothing open")
        XCTAssertEqual(empty[3].tone, .good)
    }

    func testNeedsAttentionIsNewAlarmsWorstFirstCappedAtThree() {
        let open = [
            alarm("w-old", last: 100), alarm("ack", state: "Acknowledged", severity: "Critical"),
            alarm("w-new", last: 300), alarm("crit", severity: "Critical", last: 50), alarm("info", severity: "Info", last: 999),
        ]
        XCTAssertEqual(HomeModel.needsAttention(open).map(\.id), ["crit", "w-new", "w-old", "info"])
        let (shown, more) = HomeModel.attention(open)
        XCTAssertEqual(shown.map(\.id), ["crit", "w-new", "w-old"])
        XCTAssertEqual(more, 1)
        XCTAssertEqual(HomeModel.attention([]).more, 0)
    }

    func testAcknowledgeNeedsTheRightStateAndPermission() {
        XCTAssertTrue(HomeModel.canAcknowledge(alarm("a"), mayAct: true))
        XCTAssertTrue(HomeModel.canAcknowledge(alarm("a", state: "Snoozed"), mayAct: true))
        XCTAssertFalse(HomeModel.canAcknowledge(alarm("a", state: "Investigating"), mayAct: true))
        XCTAssertFalse(HomeModel.canAcknowledge(alarm("a"), mayAct: false))
    }

    func testRecentActivityAndStills() {
        var events: [V2.Event] = []
        for i in 0..<9 {
            let camera = i % 2 == 0 ? "c1" : "c2"
            events.append(event("e\(i)", camera: camera, at: Int64(1000 + i), thumbnail: i != 8))
        }
        XCTAssertEqual(HomeModel.recentActivity(events).map(\.id), ["e8", "e7", "e6", "e5", "e4", "e3"])
        XCTAssertEqual(HomeModel.latestStill(for: "c1", in: events)?.id, "e6", "the newest event with a frame")
        XCTAssertEqual(HomeModel.latestStill(for: "c2", in: events)?.id, "e7")
        XCTAssertNil(HomeModel.latestStill(for: "c9", in: events))
    }

    func testSubtitleSaysWhereAndHow() {
        XCTAssertEqual(HomeModel.subtitle(host: "192.168.2.10", streamLabel: "Live"), "Local network · live")
        XCTAssertEqual(HomeModel.subtitle(host: "127.0.0.1", streamLabel: "Reconnecting"), "Local network · reconnecting")
        XCTAssertEqual(HomeModel.subtitle(host: "vms.example.com", streamLabel: "Live"), "vms.example.com · live")
        XCTAssertEqual(HomeModel.subtitle(host: nil, streamLabel: "Connecting"), "Remote · connecting")
        XCTAssertTrue(HomeModel.isLocal(host: "nvr.local"))
        XCTAssertTrue(HomeModel.isLocal(host: "172.20.0.4"))
        XCTAssertFalse(HomeModel.isLocal(host: "172.32.0.4"))
        XCTAssertFalse(HomeModel.isLocal(host: "8.8.8.8"))
    }

    func testLayoutCapsAndSplitsFiftyFiveFortyFive() {
        XCTAssertEqual(HomeModel.contentWidth(available: 2000, padding: 24), 1100)
        XCTAssertEqual(HomeModel.contentWidth(available: 900, padding: 24), 852)
        XCTAssertEqual(HomeModel.contentWidth(available: 20, padding: 24), 0)
        let cols = HomeModel.columns(contentWidth: 1100, spacing: 24)
        XCTAssertEqual(cols.left, 591)
        XCTAssertEqual(cols.right, 485)
    }

    func testRelativeTimesAndSymbols() {
        let ms = now.unixMillis
        XCTAssertEqual(HomeModel.relative(ms - 10_000, now: now), "just now")
        XCTAssertEqual(HomeModel.relative(ms - 600_000, now: now), "10 min ago")
        XCTAssertEqual(HomeModel.relative(ms - 7_200_000, now: now), "2 h ago")
        XCTAssertEqual(HomeModel.relative(ms - 90_000_000, now: now), "yesterday")
        XCTAssertEqual(HomeModel.relative(ms - 3 * 86_400_000, now: now), "3 days ago")
        XCTAssertEqual(HomeModel.alarmSymbol(kind: "Camera Offline", severity: .critical), "video.slash.fill")
        XCTAssertEqual(HomeModel.alarmSymbol(kind: "Tamper", severity: .critical), "exclamationmark.octagon.fill")
        XCTAssertEqual(HomeModel.kindSymbol("License Plate"), "doc.text.magnifyingglass")
    }
}

/// Wave 20: the Alarm Manager's card list keeps the table's selection.
final class AlarmCardSelectionTests: XCTestCase {
    private let ids = ["a", "b", "c", "d"]

    func testClickCommandAndShift() {
        XCTAssertEqual(AlarmCardSelection.click("b", ids: ids, selection: ["a", "c"], anchor: "a", command: false, shift: false), ["b"])
        XCTAssertEqual(AlarmCardSelection.click("b", ids: ids, selection: ["a"], anchor: "a", command: true, shift: false), ["a", "b"])
        XCTAssertEqual(AlarmCardSelection.click("a", ids: ids, selection: ["a", "b"], anchor: "a", command: true, shift: false), ["b"])
        XCTAssertEqual(AlarmCardSelection.click("d", ids: ids, selection: ["b"], anchor: "b", command: false, shift: true), ["b", "c", "d"])
        XCTAssertEqual(AlarmCardSelection.click("a", ids: ids, selection: ["c"], anchor: "c", command: false, shift: true), ["a", "b", "c"])
        XCTAssertEqual(AlarmCardSelection.click("c", ids: ids, selection: [], anchor: nil, command: false, shift: true), ["c"],
                       "no anchor: a plain selection")
    }

    func testArrowsStepAndClamp() {
        XCTAssertEqual(AlarmCardSelection.step(ids: ids, selection: [], anchor: nil, down: true), "a")
        XCTAssertEqual(AlarmCardSelection.step(ids: ids, selection: [], anchor: nil, down: false), "d")
        XCTAssertEqual(AlarmCardSelection.step(ids: ids, selection: ["b"], anchor: "b", down: true), "c")
        XCTAssertEqual(AlarmCardSelection.step(ids: ids, selection: ["d"], anchor: "d", down: true), "d")
        XCTAssertEqual(AlarmCardSelection.step(ids: ids, selection: ["a"], anchor: "a", down: false), "a")
        XCTAssertEqual(AlarmCardSelection.step(ids: ids, selection: ["c"], anchor: "gone", down: false), "b")
        XCTAssertNil(AlarmCardSelection.step(ids: [], selection: [], anchor: nil, down: true))
    }
}
