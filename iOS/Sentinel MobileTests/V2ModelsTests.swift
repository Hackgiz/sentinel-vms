import XCTest
@testable import Sentinel_Mobile

/// Decoding of the examples in linux/api/openapi.yaml.
final class V2ModelsTests: XCTestCase {
    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    func testTokenResponse() throws {
        let r = try decode(V2.TokenResponse.self, """
        {"tokenType":"Bearer","accessToken":"sat_x","expiresAt":1791771869892,"refreshToken":"srt_y","refreshExpiresAt":1794362969892,
         "user":{"id":"u1","name":"admin","role":"Admin","createdAt":1791765456000,"source":"local","scope":{"allCameras":true,"groupIds":null}}}
        """)
        XCTAssertEqual(r.user.role, .admin)
        XCTAssertTrue(r.user.scope.groupIds.isEmpty, "Go's null slice decodes as empty")
        let pair = TokenPair(r)
        XCTAssertEqual(pair.userName, "admin")
        XCTAssertFalse(pair.refreshExpired(now: Date(unixMillis: 1791771869892)))
    }

    func testVideoTokenWithGateway() throws {
        let t = try decode(V2.VideoToken.self, """
        {"token":"v1.a.b","baseUrl":"http://10.0.0.5:8090/v/v1.a.b","gatewayUrl":"/v/v1.a.b","expiresAt":1791771869892,
         "rights":["live"],"cameraId":"c1","recorderId":"rs2"}
        """)
        XCTAssertEqual(t.rights, [.live])
        XCTAssertEqual(t.recorderId, "rs2")
    }

    func testCameraDecodesOptionalStatusFields() throws {
        let c = try decode(V2.Camera.self, """
        {"id":"c1","name":"Door","location":"","rtspUrl":"rtsp://x","subRtspUrl":"","username":"","hasPassword":false,"recording":true,
         "retentionDays":0,"fps":25,"motionLevel":2,"alertOn":"future-mode","onvifUrl":"","cameraAnalytics":true,"sortOrder":0,
         "createdAt":1,"recorderId":"local","groupIds":null,"status":{"online":true,"viewers":1,"message":"Online"}}
        """)
        XCTAssertEqual(c.alertOn.rawValue, "future-mode", "an enum value this app doesn't know must not break the list")
        XCTAssertEqual(c.resolutionLabel, "Live")
        XCTAssertTrue(c.groupIds.isEmpty)
    }

    func testAlarmAndPage() throws {
        let page = try decode(V2.Page<V2.Alarm>.self, """
        {"items":[{"id":"a1","cameraId":"c1","cameraName":"Door","kind":"Person","title":"Person","detail":"","severity":"Warning",
          "state":"New","owner":"Unassigned","createdAt":1,"updatedAt":2,"lastEventAt":3,"eventCount":2,"eventId":"e1",
          "instructions":[{"rule":"After hours","text":"Call the manager"}],"responseLog":null}],"nextCursor":"abc"}
        """)
        XCTAssertEqual(page.nextCursor, "abc")
        let a = page.items[0]
        XCTAssertTrue(a.needsAttention)
        XCTAssertTrue(a.canAcknowledge)
        XCTAssertNil(a.assignee)
        XCTAssertEqual(a.instructions.first?.text, "Call the manager")
        XCTAssertTrue(a.responseLog.isEmpty)
        XCTAssertTrue(V2.AlarmState.snoozed.isOpen)
        XCTAssertFalse(V2.AlarmState.falseAlarm.isOpen)
        XCTAssertFalse(V2.AlarmState(rawValue: "Resolved").isOpen)
    }

    func testAlarmStateChangeEncoding() throws {
        let data = try SentinelAPI.jsonEncoder.encode(V2.AlarmStateChange(state: .snoozed, note: "later", snoozeMinutes: 30))
        XCTAssertEqual(String(decoding: data, as: UTF8.self), #"{"note":"later","snoozeMinutes":30,"state":"Snoozed"}"#)
    }

    func testTimelineMediaMapping() throws {
        let t = try decode(V2.Timeline.self, """
        {"from":0,"to":100000,"now":100000,"spans":[
           {"start":1000,"end":11000,"media":0,"recorderId":"rs1"},
           {"start":20000,"end":30000,"media":10,"recorderId":"rs1"},
           {"start":40000,"end":50000,"media":0,"recorderId":"rs2"}],
         "pending":null,"writing":true,"events":null,"alarms":null}
        """)
        let m = try XCTUnwrap(t.mediaTime(at: 25000))
        XCTAssertEqual(m.seconds, 15, accuracy: 0.001)
        XCTAssertEqual(m.span.recorderId, "rs1")
        XCTAssertNil(t.mediaTime(at: 15000), "gap")
        XCTAssertEqual(try XCTUnwrap(t.mediaTime(at: 41000)).span.recorderId, "rs2")
        XCTAssertEqual(t.wallTime(atMedia: 15, recorderID: "rs1"), 25000)
        XCTAssertEqual(t.wallTime(atMedia: 1, recorderID: "rs2"), 41000)
        XCTAssertNil(t.wallTime(atMedia: 25, recorderID: "rs1"))
    }

    func testStreamMessages() throws {
        func parse(_ json: String) throws -> V2.StreamMessage { try V2.StreamMessage.decode(Data(json.utf8)).message }
        guard case .cameraStatus(let s) = try parse(#"{"type":"camera.status","at":5,"data":{"cameraId":"c1","online":false}}"#) else { return XCTFail() }
        XCTAssertEqual(s.cameraId, "c1")
        XCTAssertFalse(s.online)
        guard case .lagged(let l) = try parse(#"{"type":"lagged","at":5,"data":{"dropped":12}}"#) else { return XCTFail() }
        XCTAssertEqual(l.dropped, 12)
        guard case .configChanged(let c) = try parse(#"{"type":"config.changed","at":5,"data":{"cameraIds":null}}"#) else { return XCTFail() }
        XCTAssertTrue(c.cameraIds.isEmpty)
        guard case .unknown(let type) = try parse(#"{"type":"future.thing","at":5,"data":{}}"#) else { return XCTFail() }
        XCTAssertEqual(type, "future.thing")
    }

    func testErrorEnvelopeMapping() {
        let body = Data(#"{"error":{"code":"token_reused","message":"used twice"}}"#.utf8)
        let e = SentinelAPIError.from(status: 401, body: body)
        XCTAssertEqual(e.code, "token_reused")
        XCTAssertEqual(e.errorDescription, "used twice")
        XCTAssertEqual(SentinelAPIError.from(status: 503, body: Data("<html>".utf8)).code, "unavailable")
    }
}
