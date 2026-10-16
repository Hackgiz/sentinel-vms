import XCTest
@testable import SentinelCore

/// Stream-settings logic (range clamping, dirty tracking, only-changed PUT)
/// and its API paths.
final class OpsStreamSettingsTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() {
        super.setUp()
        MockURLProtocol.reset()
    }

    override func tearDown() {
        MockURLProtocol.reset()
        super.tearDown()
    }

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
    }

    private func stream(bitrateMax: Int = 8192) -> V2.EncoderStream {
        V2.EncoderStream(
            configToken: "tok1", profileToken: "p1", name: "Profile_1", inUse: "main", encoding: "H264",
            current: .init(width: 1920, height: 1080, fps: 25, bitrate: 4096, govLength: 50, quality: 5),
            options: .init(resolutions: [.init(width: 1280, height: 720), .init(width: 640, height: 360)],
                           fps: .init(min: 1, max: 30), bitrate: .init(min: 32, max: bitrateMax),
                           gov: .init(min: 1, max: 150), quality: .init(min: 1, max: 10)))
    }

    func testResolutionChoicesAlwaysIncludeCurrent() {
        let choices = StreamSettingsModel.resolutionChoices(stream())
        XCTAssertEqual(choices.first, .init(width: 1920, height: 1080))
        XCTAssertEqual(choices.count, 3)
        XCTAssertEqual(StreamSettingsModel.resolutionLabel(choices[1]), "1280×720")
    }

    func testClampAndBounds() {
        let r = V2.StreamRange(min: 1, max: 30)
        XCTAssertEqual(StreamSettingsModel.clamp(99, r), 30)
        XCTAssertEqual(StreamSettingsModel.clamp(-4, r), 1)
        XCTAssertEqual(StreamSettingsModel.clamp(15, r), 15)
        let free = V2.StreamRange(min: 0, max: 0)
        XCTAssertFalse(StreamSettingsModel.isBounded(free))
        XCTAssertEqual(StreamSettingsModel.clamp(100_000, free), 100_000)
        XCTAssertEqual(StreamSettingsModel.clamp(0, free), 1)
        XCTAssertTrue(StreamSettingsModel.usesSlider(V2.StreamRange(min: 1, max: 10)))
        XCTAssertFalse(StreamSettingsModel.usesSlider(V2.StreamRange(min: 32, max: 16384)))
        XCTAssertFalse(StreamSettingsModel.usesSlider(free))
        XCTAssertTrue(StreamSettingsModel.inRange(30, r))
        XCTAssertFalse(StreamSettingsModel.inRange(31, r))
        XCTAssertTrue(StreamSettingsModel.inRange(999_999, free))
        XCTAssertFalse(StreamSettingsModel.inRange(0, free))
    }

    func testDirtyTrackingSendsOnlyChangedFields() {
        let s = stream()
        var form = StreamForm(s)
        XCTAssertNil(StreamSettingsModel.change(from: s, to: form))
        XCTAssertFalse(StreamSettingsModel.isDirty(s, form))

        form.bitrate = 2048
        let change = StreamSettingsModel.change(from: s, to: form)
        XCTAssertEqual(change, V2.StreamSettingsChange(bitrate: 2048))
        XCTAssertFalse(StreamSettingsModel.reconnects(change))

        form.width = 1280; form.height = 720
        let both = StreamSettingsModel.change(from: s, to: form)
        XCTAssertEqual(both?.width, 1280)
        XCTAssertEqual(both?.height, 720)
        XCTAssertNil(both?.fps)
        XCTAssertTrue(StreamSettingsModel.reconnects(both))

        form = StreamForm(s)
        form.fps = 15
        XCTAssertTrue(StreamSettingsModel.reconnects(StreamSettingsModel.change(from: s, to: form)))
        form.fps = 25
        XCTAssertFalse(StreamSettingsModel.isDirty(s, form)) // back to the camera's value
    }

    func testProblemOnlyChecksChangedFields() {
        let s = stream()
        var form = StreamForm(s)
        form.fps = 60
        XCTAssertEqual(StreamSettingsModel.problem(s, form), "Frame rate must be a whole number from 1 to 30.")
        form.fps = 25
        form.bitrate = 999_999
        XCTAssertEqual(StreamSettingsModel.problem(s, form), "Bitrate must be a whole number from 32 to 8192.")
        // A current value outside the options (camera quirk) isn't blamed when untouched.
        var odd = s
        odd.current.quality = 99
        XCTAssertNil(StreamSettingsModel.problem(odd, StreamForm(odd)))
        let free = stream(bitrateMax: 0)
        var f2 = StreamForm(free)
        f2.bitrate = 999_999
        XCTAssertNil(StreamSettingsModel.problem(free, f2))
        f2.bitrate = 0
        XCTAssertEqual(StreamSettingsModel.problem(free, f2), "Bitrate must be a positive whole number.")
    }

    func testTitles() {
        var s = stream()
        XCTAssertEqual(StreamSettingsModel.title(s), "Main stream")
        s.inUse = "sub"
        XCTAssertEqual(StreamSettingsModel.title(s), "Sub stream")
        s.inUse = ""
        XCTAssertEqual(StreamSettingsModel.title(s), "Profile_1")
    }

    private static let settingsJSON = """
    {"streams":[{"configToken":"tok1","profileToken":"p1","name":"Profile_1","inUse":"main","encoding":"H264",
      "current":{"width":1920,"height":1080,"fps":25,"bitrate":4096,"govLength":50,"quality":5},
      "options":{"resolutions":[{"width":1280,"height":720}],"fps":{"min":1,"max":30},"bitrate":{"min":0,"max":0},
                 "gov":{"min":1,"max":150},"quality":{"min":1,"max":10}}}]}
    """

    func testFetchDecodesStreams() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/cameras/c1/stream-settings")
            return .json(200, Self.settingsJSON)
        }
        let settings = try await makeAPI().streamSettings(cameraID: "c1")
        XCTAssertEqual(settings.streams.count, 1)
        XCTAssertEqual(settings.streams[0].options.bitrate.max, 0)
        XCTAssertFalse(StreamSettingsModel.isBounded(settings.streams[0].options.bitrate))
    }

    func testApplySendsOnlyChangedFieldsAndKeepsCameraErrorVerbatim() async throws {
        let api = makeAPI()
        MockURLProtocol.handler = { _, _ in MockURLProtocol.Reply(status: 204, body: Data()) }
        try await api.applyStreamSettings(cameraID: "c1", configToken: "tok 1", change: .init(fps: 15))
        let req = try XCTUnwrap(MockURLProtocol.requests.last)
        XCTAssertEqual(req.request.httpMethod, "PUT")
        XCTAssertEqual(req.request.url?.absoluteString, "http://nvr.test:8090/api/v2/cameras/c1/stream-settings/tok%201")
        let body = try JSONSerialization.jsonObject(with: XCTUnwrap(req.body)) as? [String: Any]
        XCTAssertEqual(body?.keys.sorted(), ["fps"])

        MockURLProtocol.handler = { _, _ in
            .json(502, #"{"error":{"code":"unavailable","message":"camera said: InvalidArgVal ter:ConfigModify"}}"#)
        }
        do {
            try await api.applyStreamSettings(cameraID: "c1", configToken: "tok1", change: .init(fps: 15))
            XCTFail("expected 502")
        } catch {
            XCTAssertEqual(error.localizedDescription, "camera said: InvalidArgVal ter:ConfigModify")
        }
    }

    func testConflictReadsAsUnsupported() async {
        MockURLProtocol.handler = { _, _ in
            .json(409, #"{"error":{"code":"conflict","message":"this camera was added without ONVIF"}}"#)
        }
        do {
            _ = try await makeAPI().streamSettings(cameraID: "c1")
            XCTFail("expected 409")
        } catch {
            XCTAssertEqual((error as? SentinelAPIError)?.isConflict, true)
            XCTAssertEqual(StreamSettingsModel.unsupportedDetail(error), "this camera was added without ONVIF")
        }
    }
}
