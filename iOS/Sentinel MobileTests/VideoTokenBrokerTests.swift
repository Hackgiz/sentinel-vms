import XCTest
@testable import Sentinel_Mobile

final class VideoTokenBrokerTests: XCTestCase {
    private let site = URL(string: "http://10.0.0.1:8090")!

    private func tokenReply(base: String, gateway: String, expiresIn: TimeInterval = 600, recorder: String = "rs1") -> Data {
        Data("""
        {"token":"v1.t","baseUrl":"\(base)","gatewayUrl":"\(gateway)","expiresAt":\(Date().addingTimeInterval(expiresIn).unixMillis),
         "rights":["live"],"cameraId":"c1","recorderId":"\(recorder)"}
        """.utf8)
    }

    private func broker() -> VideoTokenBroker {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "a", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "r", refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"), siteID: "s1")
        return VideoTokenBroker(api: SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: mockSession()))
    }

    func testDirectThenGatewayAfterFailure() async throws {
        MockURLProtocol.reset { _ in .init(body: self.tokenReply(base: "http://10.0.0.7:8090/v/v1.t", gateway: "/v/v1.t")) }
        let broker = broker()
        let direct = try await broker.endpoint(cameraID: "c1")
        XCTAssertFalse(direct.isGateway)
        XCTAssertEqual(direct.liveHLS.absoluteString, "http://10.0.0.7:8090/v/v1.t/live/c1/index.m3u8")
        await broker.reportDirectFailure(recorderID: "rs1")
        let gateway = try await broker.endpoint(cameraID: "c1")
        XCTAssertTrue(gateway.isGateway)
        XCTAssertEqual(gateway.liveHLS.absoluteString, "http://10.0.0.1:8090/v/v1.t/live/c1/index.m3u8", "a gateway path resolves against the site address")
        XCTAssertEqual(MockURLProtocol.seen.count, 1, "the token is reused")
    }

    func testPlaybackURLAndTokenRefreshBeforeExpiry() async throws {
        var issued = 0
        MockURLProtocol.reset { _ in
            issued += 1
            // The first token has only 30 s left: inside the 90 s reuse margin.
            return .init(body: self.tokenReply(base: "/v/v1.t", gateway: "/v/v1.t", expiresIn: issued == 1 ? 30 : 600))
        }
        let broker = broker()
        let first = try await broker.endpoint(cameraID: "c1", rights: [.playback], recorderID: "rs2")
        let second = try await broker.endpoint(cameraID: "c1", rights: [.playback], recorderID: "rs2")
        XCTAssertEqual(issued, 2, "an about-to-expire token is replaced")
        XCTAssertGreaterThan(second.expiresAt, first.expiresAt)
        let url = second.playback(from: 1000, to: 2000)
        XCTAssertEqual(url.path, "/v/v1.t/api/v1/cameras/c1/playback.m3u8")
        XCTAssertEqual(url.query, "from=1000&to=2000")
        let body = String(decoding: MockURLProtocol.seen[0].httpBody ?? Data(), as: UTF8.self)
        XCTAssertTrue(body.contains(#""recorderId":"rs2""#) && body.contains(#""rights":["playback"]"#), body)
    }

    func testResolve() {
        XCTAssertEqual(VideoTokenBroker.resolve("/v/abc", against: site)?.absoluteString, "http://10.0.0.1:8090/v/abc")
        XCTAssertEqual(VideoTokenBroker.resolve("https://rs.lan:8090/v/abc/", against: site)?.absoluteString, "https://rs.lan:8090/v/abc")
        XCTAssertNil(VideoTokenBroker.resolve("", against: site))
    }
}
