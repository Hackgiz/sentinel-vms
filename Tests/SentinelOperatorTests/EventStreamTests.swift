import XCTest
@testable import SentinelCore

/// Decoding of `StreamMessage`s from `GET /api/v2/events/stream`.
final class EventStreamTests: XCTestCase {
    private func decode(_ json: String) throws -> V2.StreamMessage {
        try V2.StreamMessage.decode(Data(json.utf8)).message
    }

    func testHello() throws {
        let (message, at) = try V2.StreamMessage.decode(Data("""
        {"type":"hello","at":1760000000000,"data":{"user":\(Fixtures.user),"topics":["alarm","event","camera","config"],"serverVersion":"2.0.0"}}
        """.utf8))
        XCTAssertEqual(at, 1_760_000_000_000)
        guard case .hello(let hello) = message else { return XCTFail("\(message)") }
        XCTAssertEqual(hello.serverVersion, "2.0.0")
        XCTAssertEqual(hello.topics.count, 4)
        XCTAssertEqual(hello.user.name, "maria")
    }

    func testAlarmAndEventMessages() throws {
        guard case .alarmNew(let alarm) = try decode(#"{"type":"alarm.new","at":1,"data":\#(Fixtures.alarm)}"#) else {
            return XCTFail()
        }
        XCTAssertEqual(alarm.id, "a1")
        guard case .alarmUpdated = try decode(#"{"type":"alarm.updated","at":1,"data":\#(Fixtures.alarm)}"#) else {
            return XCTFail()
        }
        guard case .eventNew(let event) = try decode(#"{"type":"event.new","at":1,"data":\#(Fixtures.event)}"#) else {
            return XCTFail()
        }
        XCTAssertEqual(event.kind, "Person")
        guard case .eventUpdated = try decode(#"{"type":"event.updated","at":1,"data":\#(Fixtures.event)}"#) else {
            return XCTFail()
        }
    }

    func testCameraAndConfigMessages() throws {
        XCTAssertEqual(try decode(#"{"type":"camera.status","at":1,"data":{"cameraId":"c1","online":false}}"#),
                       .cameraStatus(.init(cameraId: "c1", online: false)))
        XCTAssertEqual(try decode(#"{"type":"camera.activity","at":1,"data":{"cameraId":"c1","kind":"motion"}}"#),
                       .cameraActivity(.init(cameraId: "c1", kind: "motion")))
        XCTAssertEqual(try decode(#"{"type":"config.changed","at":1,"data":{"cameraIds":["c1","c2"]}}"#),
                       .configChanged(.init(cameraIds: ["c1", "c2"])))
        XCTAssertEqual(try decode(#"{"type":"config.changed","at":1,"data":{"cameraIds":null}}"#),
                       .configChanged(.init(cameraIds: [])))
        XCTAssertEqual(try decode(#"{"type":"recorder.status","at":1,"data":{"recorderId":"rs2","online":true}}"#),
                       .recorderStatus(.init(recorderId: "rs2", online: true)))
    }

    func testLaggedAndUnknown() throws {
        XCTAssertEqual(try decode(#"{"type":"lagged","at":1,"data":{"dropped":42}}"#), .lagged(.init(dropped: 42)))
        XCTAssertEqual(try decode(#"{"type":"something.new","at":1,"data":{"x":1}}"#), .unknown(type: "something.new"))
        XCTAssertEqual(try decode(#"{"type":"something.new","at":1}"#), .unknown(type: "something.new"))
    }

    func testMalformedKnownMessageThrows() {
        XCTAssertThrowsError(try decode(#"{"type":"alarm.new","at":1,"data":{"id":"a1"}}"#))
        XCTAssertThrowsError(try decode(#"{"at":1}"#))
    }

    func testStreamURLAndBackoff() {
        let url = SentinelEventStream.streamURL(baseURL: URL(string: "https://nvr.example.com/")!, topics: ["alarm", "camera"])
        XCTAssertEqual(url?.absoluteString, "wss://nvr.example.com/api/v2/events/stream?topics=alarm,camera")
        let plain = SentinelEventStream.streamURL(baseURL: URL(string: "http://10.0.0.2:8090")!, topics: [])
        XCTAssertEqual(plain?.absoluteString, "ws://10.0.0.2:8090/api/v2/events/stream")

        XCTAssertLessThanOrEqual(SentinelEventStream.backoff(attempt: 0), 1.2)
        XCTAssertGreaterThanOrEqual(SentinelEventStream.backoff(attempt: 0), 0.8)
        XCTAssertLessThanOrEqual(SentinelEventStream.backoff(attempt: 20), 36, "capped at 30 s ± jitter")
    }
}
