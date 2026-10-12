import XCTest
@testable import SentinelCore

/// Runs the client against a real Sentinel Server. Skipped unless
/// `SENTINEL_TEST_SERVER` (e.g. `http://127.0.0.1:18090`) names a server whose
/// Admin is `SENTINEL_TEST_USER` / `SENTINEL_TEST_PASSWORD`
/// (default admin / password123). Creates and removes its own camera, rule,
/// user and view.
final class LiveServerTests: XCTestCase {
    private var api: SentinelAPI!

    override func setUp() async throws {
        guard let address = ProcessInfo.processInfo.environment["SENTINEL_TEST_SERVER"], let url = URL(string: address) else {
            throw XCTSkip("Set SENTINEL_TEST_SERVER to run against a real server.")
        }
        api = SentinelAPI(baseURL: url, siteID: "live-\(UUID().uuidString)", tokenStore: MemoryTokenStore(), clientLabel: "Sentinel Operator (tests)")
        let env = ProcessInfo.processInfo.environment
        try await api.signIn(name: env["SENTINEL_TEST_USER"] ?? "admin", password: env["SENTINEL_TEST_PASSWORD"] ?? "password123")
    }

    override func tearDown() async throws {
        await api?.signOut()
    }

    func testEndToEnd() async throws {
        let providers = try await api.providers()
        XCTAssertTrue(providers.local)

        let me = try await api.me()
        XCTAssertEqual(me.role, .admin)
        XCTAssertTrue(me.can(.manageCameras))

        // Refresh rotation: force one, then keep working.
        _ = try await api.accessToken(forceRefresh: true)
        _ = try await api.me()

        let servers = try await api.recordingServers()
        XCTAssertFalse(servers.isEmpty)
        _ = try await api.cameraGroups()

        // Camera lifecycle; credentials typed into the URL move out of it.
        // With several recording servers the create must name one.
        var newCamera = V2.CameraInput(name: "Operator test \(UUID().uuidString.prefix(6))",
                                       rtspUrl: "rtsp://u:p@192.0.2.10:554/stream1", recording: false)
        newCamera.recorderId = (servers.first(where: { $0.online }) ?? servers[0]).id
        let camera = try await api.createCamera(newCamera)
        XCTAssertFalse(camera.rtspUrl.contains("p@"))
        XCTAssertTrue(camera.hasPassword)
        var patch = V2.CameraPatch()
        patch.location = "Test bench"
        let changed = try await api.updateCamera(camera.id, patch)
        XCTAssertEqual(changed.location, "Test bench")
        let all = try await api.cameras()
        XCTAssertTrue(all.contains { $0.id == camera.id })

        let token = try await api.videoToken(cameraID: camera.id, rights: [.live, .playback])
        XCTAssertEqual(token.cameraId, camera.id)
        XCTAssertTrue(token.gatewayUrl.contains("/v/"))
        let broker = VideoTokenBroker(api: api)
        let endpoint = try await broker.endpoint(cameraID: camera.id)
        XCTAssertTrue(endpoint.liveHLS.absoluteString.hasSuffix("/live/\(camera.id)/index.m3u8"))

        let now = Date().unixMillis
        let timeline = try await api.timeline(cameraID: camera.id, from: now - 3_600_000, to: now)
        XCTAssertEqual(timeline.from, now - 3_600_000)
        _ = try? await api.recordings(cameraID: camera.id)

        _ = try await api.alarms(openOnly: false, limit: 10)
        _ = try await api.events(limit: 10)

        // Rules
        let rule = try await api.createRule(V2.RuleInput(name: "Operator test rule", cameraIds: [camera.id], kinds: ["Person"],
                                                         minSeverity: "Warning", instructions: "Check the door."))
        var input = V2.RuleInput(rule)
        input.enabled = false
        let replaced = try await api.replaceRule(rule.id, input)
        XCTAssertFalse(replaced.enabled)
        try await api.deleteRule(rule.id)

        // Views (a server from before saved views answers 404)
        do {
            let view = try await api.createView(V2.ViewInput(name: "Operator test view", columns: 2, rows: 1,
                                                             tiles: [V2.ViewTile(cameraId: camera.id, x: 0, y: 0, w: 1, h: 1)]))
            XCTAssertEqual(view.tiles.first?.cameraId, camera.id)
            let views = try await api.views()
            XCTAssertTrue(views.contains { $0.id == view.id })
            try await api.deleteView(view.id)
        } catch let error as SentinelAPIError where error.isNotFound {
            print("note: this server has no /api/v2/views yet")
        }

        // Users
        let user = try await api.createUser(V2.NewUser(name: "optest\(Int.random(in: 1000...9999))", role: .viewer, password: "password123",
                                                       scope: V2.UserScope(allCameras: true)))
        let promoted = try await api.updateUser(user.id, V2.UserPatch(role: .operator))
        XCTAssertEqual(promoted.role, .operator)
        try await api.deleteUser(user.id)

        // Audit
        let audit = try await api.audit(limit: 20)
        XCTAssertFalse(audit.items.isEmpty)
        _ = try await api.verifyAudit()

        // Event stream: hello first.
        let stream = SentinelEventStream(api: api)
        var hello: V2.StreamMessage.Hello?
        for await update in stream.start() {
            if case .message(.hello(let message), _) = update { hello = message; break }
            if case .state(.stopped(let reason)) = update { XCTFail(reason); break }
        }
        stream.stop()
        XCTAssertEqual(hello?.user.id, me.id)

        try await api.deleteCamera(camera.id)
        do {
            _ = try await api.camera(camera.id)
            XCTFail("deleted camera still answers")
        } catch let error as SentinelAPIError {
            XCTAssertTrue(error.isNotFound, "\(error)")
        }
    }
}
