import XCTest
@testable import SentinelCore

/// Error mapping, bearer auth and refresh rotation of `SentinelAPI`.
final class SentinelAPIClientTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!
    private var store: MemoryTokenStore!

    override func setUp() {
        super.setUp()
        MockURLProtocol.reset()
        store = MemoryTokenStore()
    }

    override func tearDown() {
        MockURLProtocol.reset()
        super.tearDown()
    }

    private func makeAPI(tokens: TokenPair? = Fixtures.pair()) -> SentinelAPI {
        if let tokens { store.save(tokens, siteID: "s1") }
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Sentinel Operator (Test)")
    }

    private func authHeader(_ request: URLRequest) -> String? {
        request.value(forHTTPHeaderField: "Authorization")
    }

    // MARK: Error mapping

    func testV2ErrorShapeMapsToServerError() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(403, #"{"error":{"code":"forbidden","message":"your role (Viewer) can't do this"}}"#)
        }
        let api = makeAPI()
        do {
            _ = try await api.rules()
            XCTFail("expected an error")
        } catch let error as SentinelAPIError {
            XCTAssertEqual(error, .server(status: 403, code: "forbidden", message: "your role (Viewer) can't do this"))
            XCTAssertTrue(error.isForbidden)
            XCTAssertEqual(error.localizedDescription, "your role (Viewer) can't do this")
        }
    }

    func testOtherErrorBodiesStillReadable() {
        XCTAssertEqual(SentinelAPIError.from(status: 409, body: Data(#"{"error":"name taken"}"#.utf8)),
                       .server(status: 409, code: "conflict", message: "name taken"))
        let proxy = SentinelAPIError.from(status: 502, body: Data("<html>Bad gateway</html>".utf8))
        XCTAssertEqual(proxy.code, "internal")
        XCTAssertEqual(proxy.localizedDescription, "The server isn't reachable right now.")
        XCTAssertTrue(SentinelAPIError.from(status: 503, body: Data()).isUnavailable)
    }

    func testTransportErrorsAreFriendly() async {
        MockURLProtocol.handler = { _, _ in throw URLError(.cannotConnectToHost) }
        let api = makeAPI()
        do {
            _ = try await api.cameras()
            XCTFail("expected an error")
        } catch {
            guard case SentinelAPIError.transport(let message) = error else { return XCTFail("\(error)") }
            XCTAssertTrue(message.contains("Can't reach the server"), message)
        }
    }

    func testNotSignedInThrowsSignInRequiredWithoutCallingTheServer() async {
        let api = makeAPI(tokens: nil)
        do {
            _ = try await api.cameras()
            XCTFail("expected an error")
        } catch {
            guard case SentinelAPIError.signInRequired = error else { return XCTFail("\(error)") }
        }
        XCTAssertTrue(MockURLProtocol.requests.isEmpty)
    }

    // MARK: Auth

    func testSignInStoresTokensAndSendsBearer() async throws {
        MockURLProtocol.handler = { request, body in
            switch request.url?.path {
            case "/api/v2/auth/token":
                let json = try JSONSerialization.jsonObject(with: body ?? Data()) as? [String: Any]
                XCTAssertEqual(json?["grantType"] as? String, "password")
                XCTAssertEqual(json?["client"] as? String, "Sentinel Operator (Test)")
                XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"))
                return .json(200, Fixtures.tokenResponse(access: "fresh"))
            case "/api/v2/cameras":
                return .json(200, #"{"items":[]}"#)
            default:
                return .json(404, #"{"error":{"code":"not_found","message":"no"}}"#)
            }
        }
        let api = makeAPI(tokens: nil)
        try await api.signIn(name: "maria", password: "secret")
        XCTAssertEqual(store.load(siteID: "s1")?.accessToken, "fresh")
        _ = try await api.cameras()
        XCTAssertEqual(authHeader(MockURLProtocol.requests.last!.request), "Bearer fresh")
    }

    func testRefreshesBeforeExpiry() async throws {
        MockURLProtocol.handler = { request, body in
            if request.url?.path == "/api/v2/auth/token" {
                let json = try JSONSerialization.jsonObject(with: body ?? Data()) as? [String: Any]
                XCTAssertEqual(json?["grantType"] as? String, "refresh")
                XCTAssertEqual(json?["refreshToken"] as? String, "refresh-1")
                return .json(200, Fixtures.tokenResponse(access: "access-2", refresh: "refresh-2"))
            }
            return .json(200, #"{"items":[]}"#)
        }
        let api = makeAPI(tokens: Fixtures.pair(expiresIn: 20)) // inside the refresh margin
        _ = try await api.cameras()
        let paths = MockURLProtocol.requests.map { $0.request.url!.path }
        XCTAssertEqual(paths, ["/api/v2/auth/token", "/api/v2/cameras"])
        XCTAssertEqual(authHeader(MockURLProtocol.requests[1].request), "Bearer access-2")
        XCTAssertEqual(store.load(siteID: "s1")?.refreshToken, "refresh-2", "the rotated refresh token is persisted")
    }

    func testRetriesOnceAfter401WithARefreshedToken() async throws {
        MockURLProtocol.handler = { request, _ in
            if request.url?.path == "/api/v2/auth/token" {
                return .json(200, Fixtures.tokenResponse(access: "access-2"))
            }
            if request.value(forHTTPHeaderField: "Authorization") == "Bearer access-1" {
                return .json(401, #"{"error":{"code":"unauthorized","message":"token expired"}}"#)
            }
            return .json(200, #"{"items":[\#(Fixtures.camera)]}"#)
        }
        let api = makeAPI()
        let cameras = try await api.cameras()
        XCTAssertEqual(cameras.map(\.id), ["c1"])
        XCTAssertEqual(MockURLProtocol.requests.map { $0.request.url!.path },
                       ["/api/v2/cameras", "/api/v2/auth/token", "/api/v2/cameras"])
    }

    func testConcurrentCallsShareOneRefresh() async throws {
        MockURLProtocol.handler = { request, _ in
            if request.url?.path == "/api/v2/auth/token" {
                Thread.sleep(forTimeInterval: 0.2)
                return .json(200, Fixtures.tokenResponse(access: "access-2"))
            }
            return .json(200, #"{"items":[]}"#)
        }
        let api = makeAPI(tokens: Fixtures.pair(expiresIn: 5))
        async let a = api.cameras()
        async let b = api.rules()
        async let c = api.views()
        _ = try await (a, b, c)
        let refreshes = MockURLProtocol.requests.filter { $0.request.url?.path == "/api/v2/auth/token" }
        XCTAssertEqual(refreshes.count, 1, "a second refresh with the same token would revoke the sign-in")
    }

    func testReusedRefreshTokenEndsTheSignIn() async {
        MockURLProtocol.handler = { request, _ in
            if request.url?.path == "/api/v2/auth/token" {
                return .json(401, #"{"error":{"code":"token_reused","message":"refresh token was already used"}}"#)
            }
            return .json(401, #"{"error":{"code":"unauthorized","message":"expired"}}"#)
        }
        let api = makeAPI()
        let ended = expectation(description: "sign-in ended handler")
        await api.setSignInEndedHandler { _ in ended.fulfill() }
        do {
            _ = try await api.cameras()
            XCTFail("expected an error")
        } catch {
            guard case SentinelAPIError.signInRequired = error else { return XCTFail("\(error)") }
        }
        await fulfillment(of: [ended], timeout: 1)
        XCTAssertNil(store.load(siteID: "s1"), "tokens are forgotten")
        let signedIn = await api.isSignedIn
        XCTAssertFalse(signedIn)
    }

    func testRefreshTransportFailureKeepsTheSignIn() async {
        MockURLProtocol.handler = { _, _ in throw URLError(.timedOut) }
        let api = makeAPI(tokens: Fixtures.pair(expiresIn: 5))
        do {
            _ = try await api.cameras()
            XCTFail("expected an error")
        } catch {
            guard case SentinelAPIError.transport = error else { return XCTFail("\(error)") }
        }
        XCTAssertNotNil(store.load(siteID: "s1"), "a network blip must not sign the operator out")
    }

    func testSignOutRevokesAndForgets() async {
        MockURLProtocol.handler = { _, _ in Reply(status: 204, body: Data()) }
        let api = makeAPI()
        await api.signOut()
        let request = MockURLProtocol.requests.first
        XCTAssertEqual(request?.request.url?.path, "/api/v2/auth/revoke")
        let body = try? JSONSerialization.jsonObject(with: request?.body ?? Data()) as? [String: String]
        XCTAssertEqual(body?["refreshToken"], "refresh-1")
        XCTAssertNil(store.load(siteID: "s1"))
    }

    typealias Reply = MockURLProtocol.Reply

    // MARK: Requests

    func testQueryAndPathBuilding() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"items":[]}"#) }
        let api = makeAPI()
        _ = try await api.alarms(openOnly: false, cameraID: "c 1", cursor: nil, limit: 50)
        let url = try XCTUnwrap(MockURLProtocol.requests.last?.request.url)
        XCTAssertEqual(url.path, "/api/v2/alarms")
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        XCTAssertEqual(items.first { $0.name == "state" }?.value, "all")
        XCTAssertEqual(items.first { $0.name == "cameraId" }?.value, "c 1")
        XCTAssertNil(items.first { $0.name == "cursor" }, "nil parameters are left out")

        let start = api.oidcStartURL(redirectURI: "sentinel-operator://oidc", state: "xyz")
        XCTAssertEqual(start.path, "/api/v2/auth/oidc/start")
        XCTAssertTrue(start.absoluteString.contains("redirectUri=sentinel-operator://oidc"))
    }

    func testVideoTokenBrokerReusesAndResolves() async throws {
        let expires = Date().addingTimeInterval(600).unixMillis
        MockURLProtocol.handler = { _, _ in
            .json(200, """
            {"token":"v1.t","baseUrl":"/v/v1.t","gatewayUrl":"/v/v1.t","expiresAt":\(expires),
             "rights":["live"],"cameraId":"c1","recorderId":"local"}
            """)
        }
        let broker = VideoTokenBroker(api: makeAPI())
        let first = try await broker.endpoint(cameraID: "c1")
        _ = try await broker.endpoint(cameraID: "c1")
        XCTAssertEqual(MockURLProtocol.requests.count, 1, "the token is reused until close to expiry")
        XCTAssertEqual(first.liveHLS.absoluteString, "http://nvr.test:8090/v/v1.t/live/c1/index.m3u8")
        XCTAssertTrue(first.isGateway)
        XCTAssertEqual(first.playback(from: 1, to: 2).absoluteString,
                       "http://nvr.test:8090/v/v1.t/api/v1/cameras/c1/playback.m3u8?from=1&to=2")
    }

    func testDirectRecorderFallsBackToGateway() async {
        let api = makeAPI()
        let broker = VideoTokenBroker(api: api)
        let token = try! SentinelAPI.decode(V2.VideoToken.self, from: Data("""
        {"token":"v1.t","baseUrl":"https://rs2.lan:8090/v/v1.t/","gatewayUrl":"/v/v1.t","expiresAt":1,
         "rights":["playback"],"cameraId":"c1","recorderId":"rs2"}
        """.utf8))
        let direct = await broker.endpoint(for: token)
        XCTAssertEqual(direct.base.absoluteString, "https://rs2.lan:8090/v/v1.t")
        XCTAssertFalse(direct.isGateway)
        await broker.reportDirectFailure(recorderID: "rs2")
        let gateway = await broker.endpoint(for: token)
        XCTAssertEqual(gateway.base.absoluteString, "http://nvr.test:8090/v/v1.t")
        XCTAssertTrue(gateway.isGateway)
    }

    // MARK: Sites

    func testSiteAddressNormalization() {
        XCTAssertEqual(SavedSite.normalizedURL("192.168.1.20")?.absoluteString, "http://192.168.1.20:8090")
        XCTAssertEqual(SavedSite.normalizedURL("nvr.local:9000")?.absoluteString, "http://nvr.local:9000")
        XCTAssertEqual(SavedSite.normalizedURL(" https://sentinel.example.com/ ")?.absoluteString, "https://sentinel.example.com")
        XCTAssertEqual(SavedSite.normalizedURL("http://10.0.0.2")?.absoluteString, "http://10.0.0.2")
        XCTAssertNil(SavedSite.normalizedURL("ftp://x"))
        XCTAssertNil(SavedSite.normalizedURL(""))
    }

    func testSiteDirectoryPersists() {
        let defaults = UserDefaults(suiteName: "SentinelOperatorTests-\(UUID().uuidString)")!
        let directory = SiteDirectory(defaults: defaults)
        let site = SavedSite(name: "HQ", address: "http://10.0.0.2:8090")
        directory.upsert(site)
        directory.currentSiteID = site.id
        let reloaded = SiteDirectory(defaults: defaults)
        XCTAssertEqual(reloaded.currentSite?.name, "HQ")
        XCTAssertEqual(reloaded.site(forAddress: "10.0.0.2")?.id, site.id)
        store.save(Fixtures.pair(), siteID: site.id)
        reloaded.remove(site, tokenStore: store)
        XCTAssertNil(store.load(siteID: site.id), "removing a site forgets its tokens")
        XCTAssertNil(reloaded.currentSiteID)
    }
}
