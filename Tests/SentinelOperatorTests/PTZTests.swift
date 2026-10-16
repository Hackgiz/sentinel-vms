import XCTest
@testable import SentinelCore

final class PTZInputTests: XCTestCase {
    func testVelocityCombinesAndCancels() {
        XCTAssertEqual(PTZInput.velocity(of: [.right]), V2.PTZMove(pan: 0.5, tilt: 0, zoom: 0))
        XCTAssertEqual(PTZInput.velocity(of: [.left, .up]), V2.PTZMove(pan: -0.5, tilt: 0.5, zoom: 0))
        XCTAssertTrue(PTZInput.velocity(of: [.left, .right, .up, .down, .zoomIn, .zoomOut]).isStill)
        XCTAssertEqual(PTZInput.velocity(of: [.zoomOut]).zoom, -0.4)
    }

    func testClampKeepsVelocitiesInRange() {
        let move = V2.PTZMove(pan: 3, tilt: -9, zoom: .nan).clamped
        XCTAssertEqual(move, V2.PTZMove(pan: 1, tilt: -1, zoom: 0))
        XCTAssertEqual(V2.PTZMove(pan: 0.2, tilt: -0.3, zoom: 1).clamped, V2.PTZMove(pan: 0.2, tilt: -0.3, zoom: 1))
    }

    func testKeymap() {
        XCTAssertEqual(PTZInput.direction(keyCode: 126, characters: nil), .up)
        XCTAssertEqual(PTZInput.direction(keyCode: 125, characters: nil), .down)
        XCTAssertEqual(PTZInput.direction(keyCode: 123, characters: nil), .left)
        XCTAssertEqual(PTZInput.direction(keyCode: 124, characters: nil), .right)
        XCTAssertEqual(PTZInput.direction(keyCode: 24, characters: "="), .zoomIn)
        XCTAssertEqual(PTZInput.direction(keyCode: 27, characters: "-"), .zoomOut)
        XCTAssertEqual(PTZInput.direction(keyCode: 69, characters: nil), .zoomIn)
        XCTAssertNil(PTZInput.direction(keyCode: 0, characters: "a"))
    }

    func testPresetNames() {
        XCTAssertEqual(PTZInput.cleanPresetName("  Gate "), "Gate")
        XCTAssertNil(PTZInput.cleanPresetName("   "))
        XCTAssertNil(PTZInput.cleanPresetName(String(repeating: "x", count: 65)))
        XCTAssertNotNil(PTZInput.cleanPresetName(String(repeating: "x", count: 64)))
    }

    func testHoldStateSendsMoveThenStop() {
        var hold = PTZHoldState()
        XCTAssertEqual(hold.press(.right), .move(V2.PTZMove(pan: 0.5, tilt: 0, zoom: 0)))
        XCTAssertNil(hold.press(.right), "key repeat does nothing")
        XCTAssertEqual(hold.press(.up), .move(V2.PTZMove(pan: 0.5, tilt: 0.5, zoom: 0)))
        XCTAssertEqual(hold.release(.right), .move(V2.PTZMove(pan: 0, tilt: 0.5, zoom: 0)))
        XCTAssertEqual(hold.release(.up), .stop)
        XCTAssertNil(hold.release(.up))
        _ = hold.press(.zoomIn)
        XCTAssertEqual(hold.releaseAll(), .stop)
        XCTAssertNil(hold.releaseAll())
    }
}

final class PTZAPITests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
    }

    func testStateMoveStopAndPresetPaths() async throws {
        MockURLProtocol.handler = { request, body in
            let path = request.url!.path
            switch (request.httpMethod!, path) {
            case ("GET", "/api/v2/cameras/c1/ptz"):
                return .json(200, #"{"supported":true,"presets":[{"token":"1","name":"Gate"}]}"#)
            case ("POST", "/api/v2/cameras/c1/ptz/move"):
                return .json(200, String(data: body ?? Data(), encoding: .utf8) ?? "{}")
            case ("POST", "/api/v2/cameras/c1/ptz/presets"):
                return .json(201, #"{"token":"2","name":"Door"}"#)
            default:
                return .json(204, "")
            }
        }
        let api = makeAPI()
        let state = try await api.ptzState(cameraID: "c1")
        XCTAssertEqual(state.presets.map(\.name), ["Gate"])
        let sent = try await api.ptzMove(cameraID: "c1", V2.PTZMove(pan: 5, tilt: -5, zoom: 0.2))
        XCTAssertEqual(sent, V2.PTZMove(pan: 1, tilt: -1, zoom: 0.2))
        try await api.ptzStop(cameraID: "c1")
        try await api.ptzGoto(cameraID: "c1", presetToken: "1")
        let saved = try await api.ptzSavePreset(cameraID: "c1", name: "Door")
        XCTAssertEqual(saved.token, "2")
        try await api.ptzDeletePreset(cameraID: "c1", presetToken: "1")
        let calls = MockURLProtocol.requests.map { "\($0.request.httpMethod!) \($0.request.url!.path)" }
        XCTAssertEqual(calls, ["GET /api/v2/cameras/c1/ptz", "POST /api/v2/cameras/c1/ptz/move",
                               "POST /api/v2/cameras/c1/ptz/stop", "POST /api/v2/cameras/c1/ptz/presets/1/goto",
                               "POST /api/v2/cameras/c1/ptz/presets", "DELETE /api/v2/cameras/c1/ptz/presets/1"])
    }

    func testProbeIsCachedOncePerCamera() async {
        let counter = Counter()
        let cache = PTZProbeCache { id in
            await counter.bump()
            if id == "plain" { return V2.PTZState(supported: false) }
            return V2.PTZState(supported: true, presets: [V2.PTZPreset(token: "1", name: "Gate")])
        }
        let first = await cache.state(for: "ptz")
        let second = await cache.state(for: "ptz")
        let plain = await cache.state(for: "plain")
        let plainAgain = await cache.state(for: "plain")
        XCTAssertEqual(first?.presets.count, 1)
        XCTAssertEqual(second, first)
        XCTAssertNil(plain)
        XCTAssertNil(plainAgain)
        let n = await counter.value
        XCTAssertEqual(n, 2)

        await cache.update(presets: [], for: "ptz")
        let updated = await cache.state(for: "ptz")
        XCTAssertEqual(updated?.presets.count, 0)
    }

    func testProbeCachesForbiddenButRetriesTransportErrors() async {
        let counter = Counter()
        let cache = PTZProbeCache { id in
            await counter.bump()
            if id == "denied" { throw SentinelAPIError.server(status: 403, code: "forbidden", message: "no") }
            throw SentinelAPIError.transport(message: "down")
        }
        _ = await cache.state(for: "denied")
        _ = await cache.state(for: "denied")
        _ = await cache.state(for: "flaky")
        _ = await cache.state(for: "flaky")
        let n = await counter.value
        XCTAssertEqual(n, 3)
        let deniedCached = await cache.isCached("denied")
        let flakyCached = await cache.isCached("flaky")
        XCTAssertTrue(deniedCached)
        XCTAssertFalse(flakyCached)
    }
}

actor Counter {
    private(set) var value = 0
    func bump() { value += 1 }
}
