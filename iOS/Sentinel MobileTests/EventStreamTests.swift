import XCTest
@testable import Sentinel_Mobile

final class EventStreamTests: XCTestCase {
    func testStreamURL() {
        XCTAssertEqual(SentinelEventStream.streamURL(baseURL: URL(string: "https://s.example.com/")!, topics: ["alarm", "event"])?.absoluteString,
                       "wss://s.example.com/api/v2/events/stream?topics=alarm,event")
        XCTAssertEqual(SentinelEventStream.streamURL(baseURL: URL(string: "http://10.0.0.1:8090")!, topics: [])?.absoluteString,
                       "ws://10.0.0.1:8090/api/v2/events/stream")
    }

    func testBackoffGrowsAndCaps() {
        let delays = (0..<10).map { SentinelEventStream.backoff(attempt: $0) }
        XCTAssertTrue((0.8...1.2).contains(delays[0]))
        XCTAssertTrue((1.6...2.4).contains(delays[1]))
        XCTAssertTrue((3.2...4.8).contains(delays[2]))
        for d in delays[6...] { XCTAssertTrue((24...36).contains(d), "capped at 30 s ±20 %: \(d)") }
    }

    @MainActor
    func testStoreAppliesStreamMessagesAndResyncs() async throws {
        let store = AppStore()
        let alarm = try JSONDecoder().decode(V2.Alarm.self, from: Data("""
        {"id":"a1","cameraId":"c1","kind":"Person","title":"T","detail":"","severity":"Critical","state":"New","owner":"Unassigned",
         "createdAt":1,"updatedAt":1,"lastEventAt":1,"eventCount":1,"instructions":null,"responseLog":null}
        """.utf8))
        store.apply(.alarmNew(alarm))
        XCTAssertEqual(store.alarms.map(\.id), ["a1"])
        XCTAssertEqual(store.newAlertCount, 1)

        var acked = alarm
        acked.state = .acknowledged
        store.apply(.alarmUpdated(acked))
        XCTAssertEqual(store.alarms.first?.state, .acknowledged)
        XCTAssertEqual(store.newAlertCount, 0)

        var resolved = alarm
        resolved.state = .resolved
        store.apply(.alarmUpdated(resolved))
        XCTAssertTrue(store.alarms.isEmpty, "a closed alarm leaves the open queue")

        let event = try JSONDecoder().decode(V2.Event.self, from: Data(#"{"id":"e1","cameraId":"c1","kind":"Person","score":0.9,"createdAt":5,"hasThumbnail":true}"#.utf8))
        store.apply(.eventNew(event))
        store.apply(.eventNew(event))
        XCTAssertEqual(store.events.count, 1, "the same event twice is one row")

        store.cameras = [DemoData.cameras[0]]
        let id = DemoData.cameras[0].id
        store.apply(.cameraStatus(.init(cameraId: id, online: false)))
        XCTAssertFalse(store.cameras[0].status.online)
        XCTAssertEqual(store.cameras[0].status.message, "No signal")
    }
}
