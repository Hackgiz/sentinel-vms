import XCTest
@testable import SentinelCore

/// The API models decode the example shapes of `linux/api/openapi.yaml`.
final class V2ModelTests: XCTestCase {
    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try SentinelAPI.decode(T.self, from: Data(json.utf8))
    }

    func testTokenResponseAndMe() throws {
        let token = try decode(V2.TokenResponse.self, Fixtures.tokenResponse())
        XCTAssertEqual(token.tokenType, "Bearer")
        XCTAssertEqual(token.user.role, .operator)
        XCTAssertTrue(token.user.scope.allCameras)

        let me = try decode(V2.Me.self, """
        {"id":"u1","name":"maria","role":"Supervisor","createdAt":1,"source":"oidc",
         "scope":{"allCameras":false,"groupIds":["g1"]},
         "permissions":["acknowledgeAlarms","exportEvidence","viewLive","somethingNew"]}
        """)
        XCTAssertTrue(me.can(.exportEvidence))
        XCTAssertFalse(me.can(.manageUsers))
        XCTAssertEqual(me.permissions.last?.rawValue, "somethingNew", "unknown permissions are kept, not fatal")
        XCTAssertEqual(me.scope.groupIds, ["g1"])
    }

    func testAuthProviders() throws {
        let providers = try decode(V2.AuthProviders.self, """
        {"local":true,"ldap":false,"oidc":{"enabled":true,"displayName":"Sign in with Contoso"}}
        """)
        XCTAssertTrue(providers.oidc.enabled)
        XCTAssertEqual(providers.oidc.displayName, "Sign in with Contoso")
        let bare = try decode(V2.AuthProviders.self, #"{"local":true,"ldap":true,"oidc":{"enabled":false}}"#)
        XCTAssertNil(bare.oidc.displayName)
    }

    func testCameraListWithNullArrays() throws {
        let list = try decode(V2.Items<V2.Camera>.self, #"{"items":[\#(Fixtures.camera)]}"#)
        let camera = try XCTUnwrap(list.items.first)
        XCTAssertEqual(camera.groupIds, [], "Go's nil slice (null) decodes as []")
        XCTAssertEqual(camera.alertOn, .auto)
        XCTAssertEqual(camera.status.message, "Online")
        XCTAssertEqual(camera.resolutionLabel, "1920×1080")
        XCTAssertEqual(try decode(V2.Items<V2.Camera>.self, #"{"items":null}"#).items, [])
    }

    func testMissingRequiredFieldIsAContractError() {
        XCTAssertThrowsError(try decode(V2.Camera.self, #"{"id":"c1","name":"x"}"#)) { error in
            guard case SentinelAPIError.decoding(let message) = error else { return XCTFail("\(error)") }
            XCTAssertTrue(message.contains("missing"), message)
        }
    }

    func testVideoToken() throws {
        let token = try decode(V2.VideoToken.self, """
        {"token":"v1.abc.def","baseUrl":"https://rs2.lan:8090/v/v1.abc.def","gatewayUrl":"/v/v1.abc.def",
         "expiresAt":1760000600000,"rights":["live"],"cameraId":"c1","recorderId":"rs2"}
        """)
        XCTAssertEqual(token.rights, [.live])
        XCTAssertEqual(token.recorderId, "rs2")
    }

    func testTimelineWithSpansAcrossRecorders() throws {
        let timeline = try decode(V2.Timeline.self, """
        {"from":1000,"to":9000,"now":9500,
         "spans":[{"start":1000,"end":4000,"media":0,"recorderId":"local"},{"start":5000,"end":9000,"media":0,"recorderId":"rs2"}],
         "pending":null,"writing":true,"events":[\(Fixtures.event)],"alarms":[\(Fixtures.alarm)]}
        """)
        XCTAssertEqual(timeline.spans.count, 2)
        XCTAssertEqual(timeline.spans[1].recorderId, "rs2")
        XCTAssertEqual(timeline.pending, [])
        XCTAssertTrue(timeline.spans[0].contains(3999))
        XCTAssertFalse(timeline.spans[0].contains(4000))
        XCTAssertEqual(timeline.events.first?.box, [0.1, 0.2, 0.4, 0.9])
    }

    func testAlarmWithInstructions() throws {
        let alarm = try decode(V2.Alarm.self, Fixtures.alarm)
        XCTAssertEqual(alarm.severity, .critical)
        XCTAssertEqual(alarm.state, .new)
        XCTAssertTrue(alarm.isOpen)
        XCTAssertEqual(alarm.instructions.first?.text, "Call the guard on duty.")
        XCTAssertEqual(alarm.responseLog, [])

        let resolved = try decode(V2.Alarm.self, Fixtures.alarm.replacingOccurrences(of: #""state":"New""#, with: #""state":"False Alarm""#))
        XCTAssertEqual(resolved.state, .falseAlarm)
        XCTAssertFalse(resolved.isOpen)
    }

    func testAlarmPageAndEvidence() throws {
        let page = try decode(V2.Page<V2.Alarm>.self, #"{"items":[\#(Fixtures.alarm)],"nextCursor":"abc"}"#)
        XCTAssertEqual(page.nextCursor, "abc")
        let last = try decode(V2.Page<V2.Alarm>.self, #"{"items":[]}"#)
        XCTAssertNil(last.nextCursor)

        let evidence = try decode(V2.Evidence.self, """
        {"id":"ev1","caseId":"HG-2026-0001","alarmId":"a1","cameraId":"c1","cameraName":"Front Door",
         "title":"Person","range":"Oct 11 10:00:00 – 10:01:00","size":1234,"sha256":"ab12",
         "lockedAt":1760000000000,"lockedBy":"maria","recorderId":"local"}
        """)
        XCTAssertEqual(evidence.caseId, "HG-2026-0001")
    }

    func testEventRuleViewServer() throws {
        let event = try decode(V2.Event.self, Fixtures.event)
        XCTAssertEqual(event.threat, "elevated")

        let rule = try decode(V2.Rule.self, """
        {"id":"r1","createdAt":1,"updatedAt":2,"name":"After hours","enabled":true,"cameraIds":null,
         "kinds":["Person"],"minSeverity":"Warning","schedule":true,"fromMinute":1320,"toMinute":360,
         "days":[0,6],"delivery":"push","instructions":"Call the guard."}
        """)
        XCTAssertEqual(rule.cameraIds, [])
        XCTAssertEqual(V2.RuleInput(rule).fromMinute, 1320)

        let view = try decode(V2.View.self, """
        {"id":"v1","createdAt":1,"updatedAt":2,"name":"Lobby wall","columns":2,"rows":2,
         "tiles":[{"cameraId":"c1","x":0,"y":0,"w":2,"h":1},{"cameraId":"c2"}]}
        """)
        XCTAssertEqual(view.tiles.count, 2)
        XCTAssertNil(view.tiles[1].x)

        let server = try decode(V2.RecordingServer.self, """
        {"id":"local","name":"This Mac","local":true,"publicUrl":"","enrolledAt":0,"lastSeenAt":1,"revoked":false,
         "online":true,"storage":{"recordingsBytes":100},"version":"2.0.0","configRev":4,"mediaRunning":true,
         "cameras":3,"capabilities":{"detection":true,"objects":false,"webrtc":true}}
        """)
        XCTAssertEqual(server.storage?.recordingsBytes, 100)
    }

    func testMacImportResult() throws {
        let result = try decode(V2.MacImportResult.self, """
        {"cameras":2,"users":1,"rules":0,"evidence":1,"audit":10,"skipped":["user bob: no password"],
         "cameraIds":{"8F0C-…":"c1"}}
        """)
        XCTAssertEqual(result.cameraIds["8F0C-…"], "c1")
        XCTAssertEqual(result.skipped.count, 1)
    }

    func testRequestBodiesMatchTheContract() throws {
        func json(_ value: some Encodable) throws -> [String: Any] {
            let data = try SentinelAPI.jsonEncoder.encode(value)
            return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        }

        let password = try json(V2.TokenGrant.password(name: "maria", password: "pw", client: "Sentinel Operator (Test)"))
        XCTAssertEqual(password["grantType"] as? String, "password")
        XCTAssertEqual(password["client"] as? String, "Sentinel Operator (Test)")
        let refresh = try json(V2.TokenGrant.refresh(refreshToken: "r"))
        XCTAssertEqual(Set(refresh.keys), ["grantType", "refreshToken"])
        let oidc = try json(V2.TokenGrant.oidcCode(code: "c", client: nil))
        XCTAssertEqual(oidc["grantType"] as? String, "oidc_code")
        XCTAssertNil(oidc["client"])

        // Absent fields must stay absent (the server keeps them; unknown or null fields are 400).
        var patch = V2.CameraPatch()
        patch.name = "Back Door"
        XCTAssertEqual(Set(try json(patch).keys), ["name"])

        let state = try json(V2.AlarmStateChange(state: .falseAlarm, note: "cat"))
        XCTAssertEqual(state["state"] as? String, "False Alarm")
        XCTAssertNil(state["snoozeMinutes"])

        let tokenRequest = try json(V2.VideoTokenRequest(rights: [.playback], recorderId: "rs2"))
        XCTAssertEqual(tokenRequest["rights"] as? [String], ["playback"])
    }

    func testUnixMillis() {
        let date = Date(unixMillis: 1_760_000_000_123)
        XCTAssertEqual(date.unixMillis, 1_760_000_000_123)
    }
}
