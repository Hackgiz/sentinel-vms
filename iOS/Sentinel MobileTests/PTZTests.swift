// PTZTests.swift
// WAVE23 §C: the iPhone PTZ logic (velocity, hold/release/halt, visibility),
// the API calls, the controller's command order, and the live player's zoom.

import XCTest
@testable import Sentinel_Mobile

final class PTZInputTests: XCTestCase {
    func testVelocityFromDirections() {
        XCTAssertEqual(PTZInput.velocity(of: []), V2.PTZMove(pan: 0, tilt: 0, zoom: 0))
        XCTAssertEqual(PTZInput.velocity(of: [.right]), V2.PTZMove(pan: 0.5, tilt: 0, zoom: 0))
        XCTAssertEqual(PTZInput.velocity(of: [.left]), V2.PTZMove(pan: -0.5, tilt: 0, zoom: 0))
        XCTAssertEqual(PTZInput.velocity(of: [.up]), V2.PTZMove(pan: 0, tilt: 0.5, zoom: 0))
        XCTAssertEqual(PTZInput.velocity(of: [.down]), V2.PTZMove(pan: 0, tilt: -0.5, zoom: 0))
        XCTAssertEqual(PTZInput.velocity(of: [.zoomIn]), V2.PTZMove(pan: 0, tilt: 0, zoom: 0.4))
        XCTAssertEqual(PTZInput.velocity(of: [.zoomOut]), V2.PTZMove(pan: 0, tilt: 0, zoom: -0.4))
        XCTAssertEqual(PTZInput.velocity(of: [.up, .right]), V2.PTZMove(pan: 0.5, tilt: 0.5, zoom: 0))
        XCTAssertTrue(PTZInput.velocity(of: [.left, .right]).isStill, "opposites cancel")
    }

    func testClamp() {
        let m = V2.PTZMove(pan: 3, tilt: -2, zoom: .nan).clamped
        XCTAssertEqual(m, V2.PTZMove(pan: 1, tilt: -1, zoom: 0))
    }

    func testHoldPressReleaseStop() {
        var h = PTZHoldState()
        XCTAssertEqual(h.press(.up), .move(V2.PTZMove(pan: 0, tilt: 0.5, zoom: 0)))
        XCTAssertNil(h.press(.up), "a second press of a held control sends nothing")
        XCTAssertEqual(h.press(.right), .move(V2.PTZMove(pan: 0.5, tilt: 0.5, zoom: 0)))
        XCTAssertEqual(h.last, .right)
        XCTAssertEqual(h.release(.up), .move(V2.PTZMove(pan: 0.5, tilt: 0, zoom: 0)))
        XCTAssertNil(h.release(.down), "releasing an unheld control sends nothing")
        XCTAssertEqual(h.release(.right), .stop)
        XCTAssertNil(h.halt(), "nothing held, nothing moving: no extra stop")
    }

    func testHaltAlwaysStopsWhileHeldOrMoving() {
        var h = PTZHoldState()
        _ = h.press(.zoomIn)
        XCTAssertEqual(h.halt(), .stop)
        XCTAssertTrue(h.held.isEmpty)
        XCTAssertNil(h.halt())
        // Opposites held: still (stop sent), yet the halt stops again because they are held.
        _ = h.press(.left)
        XCTAssertEqual(h.press(.right), .stop)
        XCTAssertEqual(h.halt(), .stop)
    }

    func testCommandSummary() {
        XCTAssertEqual(PTZCommand.move(V2.PTZMove(pan: 0.5, tilt: 0, zoom: -0.4)).summary, "move 0.5 0 -0.4")
        XCTAssertEqual(PTZCommand.stop.summary, "stop")
        XCTAssertEqual(PTZCommand.goto("p1").summary, "goto p1")
    }

    func testVisibilityRules() {
        XCTAssertTrue(PTZAccess.shouldProbe(isDemo: true, permissions: []))
        XCTAssertTrue(PTZAccess.shouldProbe(isDemo: false, permissions: nil), "unknown role: the probe decides")
        XCTAssertTrue(PTZAccess.shouldProbe(isDemo: false, permissions: [.viewLive, .controlPtz]))
        XCTAssertFalse(PTZAccess.shouldProbe(isDemo: false, permissions: [.viewLive]))

        let yes = V2.PTZState(supported: true, presets: [])
        XCTAssertTrue(PTZAccess.isVisible(state: yes, cameraOnline: true))
        XCTAssertFalse(PTZAccess.isVisible(state: yes, cameraOnline: false))
        XCTAssertFalse(PTZAccess.isVisible(state: V2.PTZState(supported: false), cameraOnline: true))
        XCTAssertFalse(PTZAccess.isVisible(state: nil, cameraOnline: true), "not probed yet")
    }

    func testStateDecodesNullPresets() throws {
        let s = try SentinelAPI.decode(V2.PTZState.self, from: Data(#"{"supported":false,"presets":null}"#.utf8))
        XCTAssertFalse(s.supported)
        XCTAssertTrue(s.presets.isEmpty)
        let t = try SentinelAPI.decode(V2.PTZState.self,
                                       from: Data(#"{"supported":true,"presets":[{"token":"1","name":"Gate"}]}"#.utf8))
        XCTAssertEqual(t.presets, [V2.PTZPreset(token: "1", name: "Gate")])
    }
}

final class PTZAPITests: XCTestCase {
    private func signedInAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "sat", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "srt", refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"),
                   siteID: "s1")
        return SentinelAPI(addresses: SiteAddresses(primary: URL(string: "http://10.0.0.1:8090")!, secondary: nil),
                           siteID: "s1", tokenStore: store, session: mockSession())
    }

    func testCallsUseTheDocumentedPathsAndBodies() async throws {
        MockURLProtocol.reset { req in
            if req.url!.path.hasSuffix("/ptz") { return .init(body: Data(#"{"supported":true,"presets":[]}"#.utf8)) }
            if req.url!.path.hasSuffix("/move") { return .init(body: Data(#"{"pan":1,"tilt":0,"zoom":0}"#.utf8)) }
            return .init(status: 204)
        }
        let api = signedInAPI()
        let state = try await api.ptzState(cameraID: "cam 1")
        XCTAssertTrue(state.supported)
        try await api.ptzMove(cameraID: "cam 1", V2.PTZMove(pan: 2, tilt: 0, zoom: 0))
        try await api.ptzStop(cameraID: "cam 1")
        try await api.ptzGoto(cameraID: "cam 1", presetToken: "p/1")

        let seen = MockURLProtocol.seen
        XCTAssertEqual(seen.map(\.httpMethod), ["GET", "POST", "POST", "POST"])
        XCTAssertEqual(seen.map { $0.url!.absoluteString }, [
            "http://10.0.0.1:8090/api/v2/cameras/cam%201/ptz",
            "http://10.0.0.1:8090/api/v2/cameras/cam%201/ptz/move",
            "http://10.0.0.1:8090/api/v2/cameras/cam%201/ptz/stop",
            "http://10.0.0.1:8090/api/v2/cameras/cam%201/ptz/presets/p%2F1/goto"
        ])
        let body = try JSONSerialization.jsonObject(with: seen[1].httpBody ?? Data()) as? [String: Double]
        XCTAssertEqual(body, ["pan": 1, "tilt": 0, "zoom": 0], "velocities are clamped before sending")
        XCTAssertTrue(seen.allSatisfy { $0.value(forHTTPHeaderField: "Authorization") == "Bearer sat" })
    }
}

/// Records what the controller sends.
final class RecordingPTZClient: PTZClient, @unchecked Sendable {
    private let lock = NSLock()
    private var _sent: [PTZCommand] = []
    var supported = true
    var fail = false
    var sent: [PTZCommand] { lock.lock(); defer { lock.unlock() }; return _sent }

    func state(cameraID: String) async throws -> V2.PTZState {
        if fail { throw SentinelAPIError.transport(message: "down") }
        return V2.PTZState(supported: supported, presets: [V2.PTZPreset(token: "p1", name: "Gate")])
    }

    func send(_ command: PTZCommand, cameraID: String) async throws {
        // Later commands must not overtake earlier ones even if these are slow.
        if case .move = command { try await Task.sleep(nanoseconds: 30_000_000) }
        lock.lock(); _sent.append(command); lock.unlock()
    }
}

@MainActor
final class PTZControllerTests: XCTestCase {
    func testPressReleaseSendsMoveThenStopInOrder() async {
        let client = RecordingPTZClient()
        let c = PTZController(cameraID: "c1", client: client)
        await c.probe()
        XCTAssertEqual(c.state?.supported, true)
        XCTAssertEqual(c.presets.map(\.name), ["Gate"])
        c.press(.left)
        c.release(.left)
        c.goto(c.presets[0])
        await c.drain()
        XCTAssertEqual(client.sent, [.move(V2.PTZMove(pan: -0.5, tilt: 0, zoom: 0)), .stop, .goto("p1")])
        XCTAssertEqual(c.lastCommand, "goto p1")
    }

    func testHaltStopsAHeldMove() async {
        let client = RecordingPTZClient()
        let c = PTZController(cameraID: "c1", client: client)
        c.press(.zoomIn)
        c.halt()   // view gone / app backgrounded mid-hold
        c.halt()
        await c.drain()
        XCTAssertEqual(client.sent, [.move(V2.PTZMove(pan: 0, tilt: 0, zoom: 0.4)), .stop])
    }

    func testFailedProbeMeansNoPTZ() async {
        let client = RecordingPTZClient()
        client.fail = true
        let c = PTZController(cameraID: "c1", client: client)
        await c.probe()
        XCTAssertEqual(c.state?.supported, false)
        XCTAssertFalse(PTZAccess.isVisible(state: c.state, cameraOnline: true))
    }

    func testDemoClientHasOnePTZCamera() async throws {
        let demo = DemoPTZClient()
        let yes = try await demo.state(cameraID: DemoPTZClient.cameraID)
        XCTAssertTrue(yes.supported)
        XCTAssertFalse(yes.presets.isEmpty)
        let no = try await demo.state(cameraID: "demo-cam-1")
        XCTAssertFalse(no.supported)
    }
}

final class VideoZoomTests: XCTestCase {
    private let size = CGSize(width: 400, height: 225)

    func testPinchClampsScale() {
        var z = VideoZoom()
        XCTAssertEqual(z.label, "1.0×")
        z.pinch(by: 2, in: size)
        XCTAssertEqual(z.scale, 2)
        XCTAssertEqual(z.label, "2.0×")
        z.pinch(by: 10, in: size)
        XCTAssertEqual(z.scale, VideoZoom.maxScale)
        z.pinch(by: 0.01, in: size)
        XCTAssertEqual(z.scale, 1)
        z.pinch(by: .nan, in: size)
        XCTAssertEqual(z.scale, 1)
    }

    func testPanOnlyWhenZoomedAndNeverPastTheEdges() {
        var z = VideoZoom()
        z.pan(by: CGPoint(x: 50, y: 50), in: size)
        XCTAssertEqual(z.offset, .zero, "no pan at 1×")
        z.pinch(by: 2, in: size)
        z.pan(by: CGPoint(x: 1000, y: -1000), in: size)
        XCTAssertEqual(z.offset, CGPoint(x: 200, y: -112.5))
        // Zooming back out pulls the picture back over the view.
        z.pinch(by: 0.5, in: size)
        XCTAssertEqual(z.offset, .zero)
    }

    func testResetAndTransform() {
        var z = VideoZoom()
        z.pinch(by: 3, in: size)
        z.pan(by: CGPoint(x: 10, y: 0), in: size)
        XCTAssertEqual(z.transform.a, 3)
        XCTAssertEqual(z.transform.tx, 10)
        z.reset()
        XCTAssertEqual(z, VideoZoom())
        XCTAssertTrue(z.transform.isIdentity)
    }
}
