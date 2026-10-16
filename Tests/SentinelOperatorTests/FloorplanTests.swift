import XCTest
@testable import SentinelCore

final class FloorplanGeometryTests: XCTestCase {
    func testFittedRectLetterboxes() {
        // A 2:1 plan in a square container leaves bars above and below.
        let rect = FloorplanGeometry.fittedRect(imageSize: CGSize(width: 2000, height: 1000), in: CGSize(width: 400, height: 400))
        XCTAssertEqual(rect, CGRect(x: 0, y: 100, width: 400, height: 200))
        XCTAssertEqual(FloorplanGeometry.fittedRect(imageSize: .zero, in: CGSize(width: 10, height: 10)), .zero)
    }

    func testNormalizeClampsAndReportsInside() {
        let rect = CGRect(x: 0, y: 100, width: 400, height: 200)
        let mid = FloorplanGeometry.normalize(CGPoint(x: 100, y: 150), in: rect)
        XCTAssertEqual(mid.x, 0.25, accuracy: 1e-9)
        XCTAssertEqual(mid.y, 0.25, accuracy: 1e-9)
        XCTAssertTrue(mid.inside)
        let outside = FloorplanGeometry.normalize(CGPoint(x: -50, y: 500), in: rect)
        XCTAssertEqual(outside.x, 0)
        XCTAssertEqual(outside.y, 1)
        XCTAssertFalse(outside.inside)
    }

    func testPinPositionRoundTripsWithNormalize() {
        let rect = CGRect(x: 20, y: 10, width: 300, height: 150)
        let pin = V2.FloorplanPin(cameraId: "c1", x: 0.4, y: 0.8)
        let point = FloorplanGeometry.position(of: pin, in: rect)
        XCTAssertEqual(point.x, 140, accuracy: 1e-9)
        XCTAssertEqual(point.y, 130, accuracy: 1e-9)
        let back = FloorplanGeometry.normalize(point, in: rect)
        XCTAssertEqual(back.x, 0.4, accuracy: 1e-9)
        XCTAssertEqual(back.y, 0.8, accuracy: 1e-9)
        // Out-of-range pins are clamped onto the image.
        let wild = FloorplanGeometry.position(of: V2.FloorplanPin(cameraId: "c", x: 3, y: -1), in: rect)
        XCTAssertEqual(wild, CGPoint(x: 320, y: 10))
    }

    func testHitTestPicksNearestWithinRadius() {
        let rect = CGRect(x: 0, y: 0, width: 100, height: 100)
        let pins = [V2.FloorplanPin(cameraId: "a", x: 0.5, y: 0.5), V2.FloorplanPin(cameraId: "b", x: 0.55, y: 0.5)]
        XCTAssertEqual(FloorplanGeometry.hitTest(CGPoint(x: 49, y: 50), pins: pins, in: rect, radius: 14)?.cameraId, "a")
        XCTAssertEqual(FloorplanGeometry.hitTest(CGPoint(x: 56, y: 50), pins: pins, in: rect, radius: 14)?.cameraId, "b")
        XCTAssertNil(FloorplanGeometry.hitTest(CGPoint(x: 90, y: 90), pins: pins, in: rect, radius: 14))
    }
}

final class FloorplanPinStateTests: XCTestCase {
    private func camera(online: Bool) -> V2.Camera {
        var json = Fixtures.camera
        if online == false { json = json.replacingOccurrences(of: #""online":true"#, with: #""online":false"#) }
        return try! SentinelAPI.jsonDecoder.decode(V2.Camera.self, from: Data(json.utf8))
    }

    private func alarm(_ state: String, at: Int64 = 1_760_000_000_000, camera: String = "c1") -> V2.Alarm {
        let json = Fixtures.alarm.replacingOccurrences(of: #""state":"New""#, with: #""state":"\#(state)""#)
            .replacingOccurrences(of: #""createdAt":1760000000000"#, with: #""createdAt":\#(at)"#)
            .replacingOccurrences(of: #""cameraId":"c1""#, with: #""cameraId":"\#(camera)""#)
        return try! SentinelAPI.jsonDecoder.decode(V2.Alarm.self, from: Data(json.utf8))
    }

    func testAlarmBeatsOfflineBeatsNormal() {
        XCTAssertEqual(FloorplanPins.state(camera: camera(online: true), cameraID: "c1", alarms: []), .normal)
        XCTAssertEqual(FloorplanPins.state(camera: camera(online: false), cameraID: "c1", alarms: []), .offline)
        XCTAssertEqual(FloorplanPins.state(camera: nil, cameraID: "c1", alarms: []), .offline)
        XCTAssertEqual(FloorplanPins.state(camera: camera(online: false), cameraID: "c1", alarms: [alarm("New")]), .alarmed)
    }

    func testSnoozedAndOtherCamerasDoNotPulse() {
        XCTAssertEqual(FloorplanPins.state(camera: camera(online: true), cameraID: "c1", alarms: [alarm("Snoozed")]), .normal)
        XCTAssertEqual(FloorplanPins.state(camera: camera(online: true), cameraID: "c1", alarms: [alarm("New", camera: "c2")]), .normal)
        XCTAssertEqual(FloorplanPins.alarm(on: "c1", in: [alarm("Acknowledged", at: 5), alarm("Investigating", at: 9)])?.createdAt, 9)
    }
}

final class FloorplanAPITests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!
    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
    }

    func testListDecodesPinsAndSorts() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(200, #"{"items":[{"id":"p2","name":"Upstairs","sortOrder":1,"width":800,"height":600,"mime":"image/png","updatedAt":5,"pins":null},{"id":"p1","name":"Ground","sortOrder":0,"width":800,"height":600,"mime":"image/png","updatedAt":4,"pins":[{"cameraId":"c1","x":0.5,"y":0.25,"heading":90}]}]}"#)
        }
        let plans = try await makeAPI().floorplans()
        XCTAssertEqual(plans.map(\.id), ["p1", "p2"])
        XCTAssertEqual(plans[0].pins, [V2.FloorplanPin(cameraId: "c1", x: 0.5, y: 0.25, heading: 90)])
        XCTAssertTrue(plans[1].pins.isEmpty)
    }

    func testImageIsRevalidatedWithEtag() async throws {
        MockURLProtocol.handler = { request, _ in
            if request.value(forHTTPHeaderField: "If-None-Match") == "\"v1\"" {
                return .init(status: 304, body: Data(), headers: [:])
            }
            return .init(status: 200, body: Data([1, 2, 3]), headers: ["ETag": "\"v1\"", "Content-Type": "image/png"])
        }
        let api = makeAPI()
        let session = MockURLProtocol.session()
        let cache = FloorplanImageCache(api: api, session: session)
        let first = try await cache.image(for: "p1")
        XCTAssertEqual(first, Data([1, 2, 3]))
        let second = try await cache.image(for: "p1")
        XCTAssertEqual(second, Data([1, 2, 3]))
        let requests = MockURLProtocol.requests
        XCTAssertNil(requests[0].request.value(forHTTPHeaderField: "If-None-Match"))
        XCTAssertEqual(requests[1].request.value(forHTTPHeaderField: "If-None-Match"), "\"v1\"")
        XCTAssertEqual(requests[1].request.value(forHTTPHeaderField: "Authorization"), "Bearer access-1")
    }

    func testChangedImageReplacesCacheAndNotFoundDrops() async throws {
        let calls = Counter()
        let cache = FloorplanImageCache { _, etag in
            let n = await calls.next()
            switch n {
            case 1: return FloorplanImageResponse(data: Data([1]), etag: "a")
            case 2: XCTAssertEqual(etag, "a"); return FloorplanImageResponse(data: Data([2]), etag: "b")
            default: throw SentinelAPIError.server(status: 404, code: "not_found", message: "gone")
            }
        }
        _ = try await cache.image(for: "p")
        let updated = try await cache.image(for: "p")
        XCTAssertEqual(updated, Data([2]))
        do { _ = try await cache.image(for: "p"); XCTFail("expected 404") } catch {}
        let entry = await cache.cachedEntry("p")
        XCTAssertNil(entry)
    }

    func testTransportErrorFallsBackToCachedCopy() async throws {
        let calls = Counter()
        let cache = FloorplanImageCache { _, _ in
            if await calls.next() == 1 { return FloorplanImageResponse(data: Data([9]), etag: "a") }
            throw SentinelAPIError.transport(message: "down")
        }
        _ = try await cache.image(for: "p")
        let offline = try await cache.image(for: "p")
        XCTAssertEqual(offline, Data([9]))
    }
}

extension Counter {
    func next() -> Int { bump(); return value }
}
