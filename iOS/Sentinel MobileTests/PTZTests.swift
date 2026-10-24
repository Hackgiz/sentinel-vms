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

    func testSaveAndDeletePresetShapes() async throws {
        MockURLProtocol.reset { req in
            if req.httpMethod == "POST" { return .init(status: 201, body: Data(#"{"token":"t9","name":"Door"}"#.utf8)) }
            return .init(status: 204)
        }
        let api = signedInAPI()
        let made = try await api.ptzSavePreset(cameraID: "cam 1", name: "Door")
        XCTAssertEqual(made, V2.PTZPreset(token: "t9", name: "Door"))
        try await api.ptzDeletePreset(cameraID: "cam 1", presetToken: "t/9")
        let seen = MockURLProtocol.seen
        XCTAssertEqual(seen.map(\.httpMethod), ["POST", "DELETE"])
        XCTAssertEqual(seen.map { $0.url!.absoluteString }, [
            "http://10.0.0.1:8090/api/v2/cameras/cam%201/ptz/presets",
            "http://10.0.0.1:8090/api/v2/cameras/cam%201/ptz/presets/t%2F9"
        ])
        let body = try JSONSerialization.jsonObject(with: seen[0].httpBody ?? Data()) as? [String: String]
        XCTAssertEqual(body, ["name": "Door"])
        XCTAssertNil(seen[1].httpBody)
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
        return V2.PTZState(supported: supported, presets: presets)
    }

    var presets = [V2.PTZPreset(token: "p1", name: "Gate")]
    var saved: [String] = []
    var deleted: [String] = []

    func savePreset(name: String, cameraID: String) async throws {
        if fail { throw SentinelAPIError.transport(message: "down") }
        saved.append(name)
        presets.append(V2.PTZPreset(token: "p\(presets.count + 1)", name: name))
    }

    func deletePreset(token: String, cameraID: String) async throws {
        if fail { throw SentinelAPIError.transport(message: "down") }
        deleted.append(token)
        presets.removeAll { $0.token == token }
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

    func testSavePresetTrimsNamesRefreshesTheListAndRejectsEmpty() async {
        let client = RecordingPTZClient()
        let c = PTZController(cameraID: "c1", client: client)
        await c.probe()
        let empty = await c.savePreset(named: "   ")
        XCTAssertFalse(empty)
        XCTAssertTrue(client.saved.isEmpty)
        let ok = await c.savePreset(named: "  Door  ")
        XCTAssertTrue(ok)
        XCTAssertEqual(client.saved, ["Door"])
        XCTAssertEqual(c.presets.map(\.name), ["Gate", "Door"], "list refreshed after saving")
    }

    func testDeletePresetRefreshesTheList() async {
        let client = RecordingPTZClient()
        let c = PTZController(cameraID: "c1", client: client)
        await c.probe()
        let ok = await c.deletePreset(c.presets[0])
        XCTAssertTrue(ok)
        XCTAssertEqual(client.deleted, ["p1"])
        XCTAssertTrue(c.presets.isEmpty)
    }

    func testPresetFailureKeepsListAndSetsNote() async {
        let client = RecordingPTZClient()
        let c = PTZController(cameraID: "c1", client: client)
        await c.probe()
        client.fail = true
        let ok = await c.savePreset(named: "X")
        XCTAssertFalse(ok)
        XCTAssertFalse(c.note.isEmpty)
        XCTAssertEqual(c.presets.map(\.name), ["Gate"])
    }

    func testPresetEditingNeedsThePTZRight() {
        XCTAssertTrue(PTZAccess.canEditPresets(isDemo: true, permissions: []))
        XCTAssertTrue(PTZAccess.canEditPresets(isDemo: false, permissions: [.controlPtz]))
        XCTAssertFalse(PTZAccess.canEditPresets(isDemo: false, permissions: [.viewLive]))
        XCTAssertEqual(PTZPresetName.clean(String(repeating: "a", count: 80))?.count, 64)
    }

    func testDemoPresetEditsAreNoOps() async throws {
        let c = PTZController(cameraID: DemoPTZClient.cameraID, client: DemoPTZClient())
        await c.probe()
        let before = c.presets
        let saved = await c.savePreset(named: "New")
        XCTAssertTrue(saved)
        let deleted = await c.deletePreset(before[0])
        XCTAssertTrue(deleted)
        XCTAssertEqual(c.presets, before)
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

// MARK: - Wave 24: playback window, backoff, load gate, token

final class PlaybackWindowTests: XCTestCase {
    private let hour: UnixMillis = 3_600_000
    private let now: UnixMillis = 100 * 3_600_000

    func testWindowIsPlayheadPlusMinusOneHourClampedToNow() {
        let w = PlaybackWindow.around(50 * hour, now: now)
        XCTAssertEqual(w, PlaybackWindow(from: 49 * hour, to: 51 * hour))
        let live = PlaybackWindow.around(now - 10 * 60_000, now: now)
        XCTAssertEqual(live.to, now, "never past now")
        XCTAssertEqual(live.from, now - 10 * 60_000 - hour)
        XCTAssertTrue(live.contains(now - 10 * 60_000))
    }

    func testRefetchWithinTenMinutesOfAnEndOrOutside() {
        let w = PlaybackWindow.around(50 * hour, now: now)   // 49h...51h
        XCTAssertFalse(w.needsRefetch(playhead: 50 * hour, now: now))
        XCTAssertTrue(w.needsRefetch(playhead: 51 * hour - 5 * 60_000, now: now), "near the end")
        XCTAssertTrue(w.needsRefetch(playhead: 49 * hour + 5 * 60_000, now: now), "near the start")
        XCTAssertFalse(w.needsRefetch(playhead: 51 * hour - 11 * 60_000, now: now))
        XCTAssertTrue(w.needsRefetch(playhead: 60 * hour, now: now), "a seek outside the window")
        XCTAssertTrue(w.needsRefetch(playhead: 10 * hour, now: now))
    }

    func testWindowClampedToNowDoesNotRefetchInALoop() {
        let playhead = now - 5 * 60_000
        let w = PlaybackWindow.around(playhead, now: now)   // ends at now
        XCTAssertFalse(w.needsRefetch(playhead: playhead, now: now), "end is already live: nothing further to fetch")
        // A minute of wall time later there is something more to fetch.
        XCTAssertTrue(w.needsRefetch(playhead: playhead + 2 * 60_000, now: now + 3 * 60_000))
    }
}

final class PlaybackReliabilityTests: XCTestCase {
    func testBackoffDoublesAndCapsAtThirtySeconds() {
        var b = PlaybackBackoff()
        let t0 = Date(timeIntervalSince1970: 1000)
        XCTAssertTrue(b.canAttempt(now: t0))
        var delays: [TimeInterval] = []
        for _ in 0..<8 {
            b.recordFailure(now: t0)
            delays.append(b.delay)
        }
        XCTAssertEqual(delays, [1, 2, 4, 8, 16, 30, 30, 30])
        XCTAssertFalse(b.canAttempt(now: t0.addingTimeInterval(29)), "no retry storm while backing off")
        XCTAssertTrue(b.canAttempt(now: t0.addingTimeInterval(30)))
        b.reset()
        XCTAssertTrue(b.canAttempt(now: t0))
        XCTAssertEqual(b.failures, 0)
    }

    func testOnlyTheLatestLoadMayClearLoading() {
        var gate = PlaybackLoadGate()
        let first = gate.begin()
        let second = gate.begin()   // supersedes (and cancels) the first
        XCTAssertFalse(gate.isLatest(first), "a cancelled load must not clear isLoading")
        XCTAssertTrue(gate.isLatest(second))
        let third = gate.begin()
        XCTAssertFalse(gate.isLatest(second))
        XCTAssertTrue(gate.isLatest(third))
    }

    func testTogglePlayWithAnExpiredTokenReloadsInsteadOfResuming() {
        let now = Date(timeIntervalSince1970: 5000)
        func act(playing: Bool = false, item: Bool = true, gap: Bool = false, expiry: Date?) -> PlaybackToggleAction {
            .decide(isPlaying: playing, hasItem: item, inGap: gap, tokenExpiry: expiry, now: now)
        }
        XCTAssertEqual(act(expiry: now.addingTimeInterval(-10)), .reload, "expired: refresh the token and continue")
        XCTAssertEqual(act(expiry: now.addingTimeInterval(30)), .reload, "about to expire")
        XCTAssertEqual(act(expiry: now.addingTimeInterval(300)), .resume)
        XCTAssertEqual(act(playing: true, expiry: now.addingTimeInterval(-10)), .pause)
        XCTAssertEqual(act(item: false, expiry: nil), .reload)
        XCTAssertEqual(act(gap: true, expiry: now.addingTimeInterval(300)), .reload)
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
