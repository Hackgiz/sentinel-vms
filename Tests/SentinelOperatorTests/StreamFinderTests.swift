import XCTest
@testable import SentinelCore

/// Wave 22 addendum on the Mac: discover/resize decoding and calls, and the
/// Find streams picker's logic.
final class StreamFinderTests: XCTestCase {
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

    private func object(_ data: Data?) throws -> NSDictionary {
        try XCTUnwrap(JSONSerialization.jsonObject(with: XCTUnwrap(data)) as? NSDictionary)
    }

    private let fullJSON = #"""
    {"streams":[
      {"url":"rtsp://10.0.0.5:554/Streaming/Channels/101","label":"3840×2160 · H.265 · 20 fps","width":3840,"height":2160,"fps":20,"codec":"H265","source":"onvif","role":"main","encoderToken":"enc0"},
      {"url":"rtsp://10.0.0.5:554/Streaming/Channels/102","label":"1280×720 · H.264 · 15 fps","width":1280,"height":720,"fps":15,"codec":"H264","source":"onvif","role":"sub","encoderToken":"enc1"},
      {"url":"rtsp://10.0.0.5:554/Streaming/Channels/103","label":"640×360 · H.264 · 12.5 fps","width":640,"height":360,"fps":12.5,"codec":"H264","source":"guess","role":""}
    ],
    "recommendedSub":"rtsp://10.0.0.5:554/Streaming/Channels/103",
    "tooSmallSub":null}
    """#

    private let main = "rtsp://10.0.0.5:554/Streaming/Channels/101"

    private func discovery(_ json: String) throws -> V2.StreamDiscovery {
        try SentinelAPI.decode(V2.StreamDiscovery.self, from: Data(json.utf8))
    }

    // MARK: Decoding

    func testFullDiscoveryDecodes() throws {
        let d = try discovery(fullJSON)
        XCTAssertEqual(d.streams.count, 3)
        XCTAssertEqual(d.streams[0].codec, "H265")
        XCTAssertEqual(d.streams[0].role, "main")
        XCTAssertEqual(d.streams[0].encoderToken, "enc0")
        XCTAssertEqual(d.streams[2].fps, 12.5)
        XCTAssertEqual(d.streams[2].source, "guess")
        XCTAssertNil(d.streams[2].encoderToken)
        XCTAssertEqual(d.recommendedSub, "rtsp://10.0.0.5:554/Streaming/Channels/103")
        XCTAssertNil(d.tooSmallSub)
    }

    func testMinimalDiscoveryDecodes() throws {
        let d = try discovery(#"{"streams":[{"url":"rtsp://cam/stream2"}]}"#)
        XCTAssertEqual(d.streams, [V2.DiscoveredStream(url: "rtsp://cam/stream2", source: "")])
        XCTAssertNil(d.recommendedSub)
        XCTAssertNil(d.tooSmallSub)
        XCTAssertEqual(try discovery(#"{"streams":null,"recommendedSub":""}"#), V2.StreamDiscovery(streams: []))
        XCTAssertEqual(try discovery("{}").streams, [])
    }

    func testTooSmallSubDecodes() throws {
        let d = try discovery(#"""
        {"streams":[{"url":"rtsp://cam/1","width":1920,"height":1080,"codec":"H264","role":"main"},
                    {"url":"rtsp://cam/2","width":352,"height":240,"codec":"H264","role":"sub","encoderToken":"e2"}],
         "recommendedSub":null,
         "tooSmallSub":{"url":"rtsp://cam/2","encoderToken":"e2","width":352,"height":240}}
        """#)
        XCTAssertEqual(d.tooSmallSub, V2.TooSmallSub(url: "rtsp://cam/2", encoderToken: "e2", width: 352, height: 240))
    }

    // MARK: Calls

    func testDiscoverSendsSavedLoginByDefault() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/api/v2/cameras/cam%201/streams/discover".removingPercentEncoding)
            return .json(200, self.fullJSON)
        }
        let d = try await makeAPI().discoverStreams(cameraID: "cam 1")
        XCTAssertEqual(d.streams.count, 3)
        let body = try object(MockURLProtocol.requests.last?.body)
        XCTAssertEqual(body.count, 0, "no credentials = use the camera's saved login")
    }

    func testDiscoverSendsTypedLogin() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"streams":[]}"#) }
        _ = try await makeAPI().discoverStreams(cameraID: "c1", username: "admin", password: "pw")
        let body = try object(MockURLProtocol.requests.last?.body)
        XCTAssertEqual(body["username"] as? String, "admin")
        XCTAssertEqual(body["password"] as? String, "pw")
    }

    func testDiscover404IsOlderServer() async {
        MockURLProtocol.handler = { _, _ in .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#) }
        do {
            _ = try await makeAPI().discoverStreams(cameraID: "c1")
            XCTFail("expected a 404")
        } catch {
            XCTAssertTrue(StreamFinderLogic.isOlderServer(error))
            XCTAssertEqual(StreamFinderLogic.errorText(error), StreamFinderLogic.olderServerNote)
        }
        // A plain-text 404 (an old server's mux) too.
        MockURLProtocol.handler = { _, _ in MockURLProtocol.Reply(status: 404, body: Data("404 page not found".utf8)) }
        do {
            _ = try await makeAPI().discoverStreams(cameraID: "c1")
            XCTFail("expected a 404")
        } catch {
            XCTAssertTrue(StreamFinderLogic.isOlderServer(error))
        }
    }

    func testDiscover422AuthFailed() async {
        MockURLProtocol.handler = { _, _ in .json(422, #"{"error":"onvif: unauthorized","authFailed":true}"#) }
        do {
            _ = try await makeAPI().discoverStreams(cameraID: "c1")
            XCTFail("expected a 422")
        } catch {
            let api = error as? SentinelAPIError
            XCTAssertEqual(api?.status, 422)
            XCTAssertTrue(api?.isAuthFailed == true)
            XCTAssertEqual(api?.errorDescription, "onvif: unauthorized")
            XCTAssertFalse(StreamFinderLogic.isOlderServer(error))
            XCTAssertEqual(StreamFinderLogic.errorText(error), "The camera rejected the username or password. Check them above and try again.")
        }
    }

    func testDiscover422Unreachable() async {
        MockURLProtocol.handler = { _, _ in .json(422, #"{"error":"the camera didn't answer at 10.0.0.5","authFailed":false}"#) }
        do {
            _ = try await makeAPI().discoverStreams(cameraID: "c1")
            XCTFail("expected a 422")
        } catch {
            let api = error as? SentinelAPIError
            XCTAssertFalse(api?.isAuthFailed == true)
            XCTAssertEqual(StreamFinderLogic.errorText(error), "the camera didn't answer at 10.0.0.5")
        }
        // The v2 envelope with authFailed beside it also counts.
        MockURLProtocol.handler = { _, _ in
            .json(422, #"{"error":{"code":"unavailable","message":"login rejected"},"authFailed":true}"#)
        }
        do {
            _ = try await makeAPI().discoverStreams(cameraID: "c1")
            XCTFail("expected a 422")
        } catch {
            XCTAssertTrue((error as? SentinelAPIError)?.isAuthFailed == true)
            XCTAssertEqual((error as? SentinelAPIError)?.errorDescription, "login rejected")
        }
    }

    func testResizeSendsBodyAndDecodesStream() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/cameras/c1/streams/resize")
            return .json(200, #"{"url":"rtsp://cam/2","label":"1280×720 · H.264 · 15 fps","width":1280,"height":720,"fps":15,"codec":"H264","source":"onvif","role":"sub","encoderToken":"e2"}"#)
        }
        let s = try await makeAPI().resizeStream(cameraID: "c1", encoderToken: "e2", width: 1280, height: 720)
        XCTAssertEqual(s.width, 1280)
        XCTAssertEqual(s.encoderToken, "e2")
        let body = try object(MockURLProtocol.requests.last?.body)
        XCTAssertEqual(body["encoderToken"] as? String, "e2")
        XCTAssertEqual(body["width"] as? Int, 1280)
        XCTAssertEqual(body["height"] as? Int, 720)
        XCTAssertNil(body["fps"], "fps is left out unless given")
    }

    // MARK: Logic

    func testRowsLabelsBadgesAndRecommended() throws {
        let rows = StreamFinderLogic.rows(try discovery(fullJSON), mainURL: main, currentSub: "rtsp://10.0.0.5:554/Streaming/Channels/102")
        XCTAssertEqual(rows.map(\.title), ["3840×2160 · H.265 · 20 fps", "1280×720 · H.264 · 15 fps", "640×360 · H.264 · 12.5 fps"])
        XCTAssertEqual(rows.map(\.badge), [nil, nil, "Guessed"])
        XCTAssertEqual(rows.map(\.recommended), [false, false, true])
        XCTAssertEqual(rows.map(\.roleNote), ["Main stream now", "Second stream now", nil])
        XCTAssertEqual(rows.map(\.selected), [false, true, false])
    }

    func testTitleFallsBackToNumbers() {
        XCTAssertEqual(StreamFinderLogic.title(.init(url: "rtsp://x", width: 640, height: 480, fps: 10, codec: "H264")), "640×480 · H.264 · 10 fps")
        XCTAssertEqual(StreamFinderLogic.title(.init(url: "rtsp://x", codec: "MJPEG")), "MJPEG")
        XCTAssertEqual(StreamFinderLogic.title(.init(url: "rtsp://x")), "rtsp://x")
    }

    func testPickRecommendedLocally() {
        let streams: [V2.DiscoveredStream] = [
            .init(url: "rtsp://c/main", width: 1920, height: 1080, codec: "H264", role: "main"),
            .init(url: "rtsp://c/tiny", width: 352, height: 240, codec: "H264"),
            .init(url: "rtsp://c/mjpeg", width: 640, height: 360, codec: "MJPEG"),
            .init(url: "rtsp://c/hd", width: 1280, height: 720, codec: "H264"),
            .init(url: "rtsp://c/vga", width: 640, height: 480, codec: "H265"),
        ]
        XCTAssertEqual(StreamFinderLogic.pickRecommended(streams, mainURL: "rtsp://c/main")?.url, "rtsp://c/vga")
        // The main URL is never the recommendation even if its role is blank.
        XCTAssertNil(StreamFinderLogic.pickRecommended([.init(url: "rtsp://c/main", width: 1280, height: 720, codec: "H264")], mainURL: "rtsp://c/main"))
        // Server's pick wins when it's in the list; else the local rule.
        let d = V2.StreamDiscovery(streams: streams, recommendedSub: "rtsp://c/hd")
        XCTAssertEqual(StreamFinderLogic.recommendedURL(d, mainURL: "rtsp://c/main"), "rtsp://c/hd")
        let stale = V2.StreamDiscovery(streams: streams, recommendedSub: "rtsp://gone")
        XCTAssertEqual(StreamFinderLogic.recommended(stale, mainURL: "rtsp://c/main")?.url, "rtsp://c/vga")
    }

    func testBanners() throws {
        let small = V2.TooSmallSub(url: "rtsp://cam/2", encoderToken: "e2", width: 352, height: 240)
        let d = V2.StreamDiscovery(streams: [.init(url: "rtsp://cam/1", width: 1920, height: 1080, codec: "H264", role: "main"),
                                             .init(url: "rtsp://cam/2", width: 352, height: 240, codec: "H264", encoderToken: "e2")],
                                   tooSmallSub: small)
        XCTAssertEqual(StreamFinderLogic.banner(d, mainURL: "rtsp://cam/1", currentSub: ""),
                       .tooSmall(text: "This camera's second stream is only 352×240.", button: "Make it 1280×720", sub: small))
        XCTAssertFalse(StreamFinderLogic.canUseRecommended(d, mainURL: "rtsp://cam/1", currentSub: ""))

        // A too-small sub without an encoder token can't be resized: no banner offer.
        var noToken = d
        noToken.tooSmallSub?.encoderToken = ""
        XCTAssertNil(StreamFinderLogic.banner(noToken, mainURL: "rtsp://cam/1", currentSub: ""))

        let onlyMain = V2.StreamDiscovery(streams: [.init(url: "rtsp://cam/1", role: "main")])
        guard case .noneFound = StreamFinderLogic.banner(onlyMain, mainURL: "rtsp://cam/1", currentSub: "") else {
            return XCTFail("expected noneFound")
        }
        guard case .noneFound = StreamFinderLogic.banner(V2.StreamDiscovery(streams: []), mainURL: "rtsp://cam/1", currentSub: "") else {
            return XCTFail("expected noneFound for an empty list")
        }

        let full = try discovery(fullJSON)
        XCTAssertNil(StreamFinderLogic.banner(full, mainURL: main, currentSub: ""))
        XCTAssertTrue(StreamFinderLogic.canUseRecommended(full, mainURL: main, currentSub: ""))
        let rec = "rtsp://10.0.0.5:554/Streaming/Channels/103"
        guard case .alreadyUsing = StreamFinderLogic.banner(full, mainURL: main, currentSub: rec) else {
            return XCTFail("expected alreadyUsing")
        }
        XCTAssertFalse(StreamFinderLogic.canUseRecommended(full, mainURL: main, currentSub: rec))
    }

    func testAvailabilityAndCredentials() {
        XCTAssertTrue(StreamFinderLogic.canFind(isSaved: true, serverSupportsDiscover: true))
        XCTAssertFalse(StreamFinderLogic.canFind(isSaved: false, serverSupportsDiscover: true), "new cameras: save first")
        XCTAssertFalse(StreamFinderLogic.canFind(isSaved: true, serverSupportsDiscover: false), "older server: hidden")

        XCTAssertTrue(StreamFinderLogic.credentials(username: "admin", typedPassword: nil) == (nil, nil))
        XCTAssertTrue(StreamFinderLogic.credentials(username: "admin", typedPassword: "") == (nil, nil))
        XCTAssertTrue(StreamFinderLogic.credentials(username: " admin ", typedPassword: "pw") == ("admin", "pw"))
        XCTAssertTrue(StreamFinderLogic.credentials(username: "", typedPassword: "pw") == (nil, "pw"))
    }

    func testFieldWordingAndDualHint() {
        XCTAssertEqual(StreamFinderLogic.fieldLabel, "Second stream (low-res)")
        XCTAssertEqual(StreamFinderLogic.fieldHelp, "Used for live view, detection and low-res recording.")
        let dual = RecordingModeLogic.choices(hasSubStream: false, serverDefault: nil).first { $0.mode == .dual }
        XCTAssertEqual(dual?.enabled, false)
        XCTAssertEqual(dual?.detail, StreamFinderLogic.dualHint)
    }
}
