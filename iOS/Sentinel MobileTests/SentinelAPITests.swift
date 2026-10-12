import XCTest
@testable import Sentinel_Mobile

/// Canned HTTP answers for URLSession.
final class MockURLProtocol: URLProtocol {
    struct Reply { var status = 200; var body = Data(); var error: URLError? }
    nonisolated(unsafe) static var handler: ((URLRequest) -> Reply)?
    nonisolated(unsafe) static var requests: [URLRequest] = []
    private static let lock = NSLock()

    static func reset(_ handler: @escaping (URLRequest) -> Reply) {
        lock.lock(); defer { lock.unlock() }
        self.handler = handler
        requests = []
    }
    static var seen: [URLRequest] { lock.lock(); defer { lock.unlock() }; return requests }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lock.lock()
        var request = self.request
        if request.httpBody == nil, let stream = request.httpBodyStream {
            stream.open()
            var data = Data()
            let buffer = UnsafeMutablePointer<UInt8>.allocate(capacity: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(buffer, maxLength: 4096)
                if n <= 0 { break }
                data.append(buffer, count: n)
            }
            buffer.deallocate()
            request.httpBody = data
        }
        Self.requests.append(request)
        let reply = Self.handler?(request) ?? Reply(status: 500)
        Self.lock.unlock()
        if let error = reply.error {
            client?.urlProtocol(self, didFailWithError: error)
            return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply.body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

func mockSession() -> URLSession {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [MockURLProtocol.self]
    return URLSession(configuration: config)
}

func tokenJSON(access: String, refresh: String, expiresIn: TimeInterval = 900, refreshExpiresIn: TimeInterval = 86400 * 30) -> Data {
    let now = Date().unixMillis
    return Data("""
    {"tokenType":"Bearer","accessToken":"\(access)","expiresAt":\(now + Int64(expiresIn * 1000)),
     "refreshToken":"\(refresh)","refreshExpiresAt":\(now + Int64(refreshExpiresIn * 1000)),
     "user":{"id":"u1","name":"op","role":"Operator","createdAt":1,"source":"local","scope":{"allCameras":true,"groupIds":[]}}}
    """.utf8)
}

final class SentinelAPITests: XCTestCase {
    private let site = URL(string: "http://10.0.0.1:8090")!

    private func api(store: TokenStoring = MemoryTokenStore(), secondary: URL? = nil) -> SentinelAPI {
        SentinelAPI(addresses: SiteAddresses(primary: site, secondary: secondary), siteID: "s1",
                    tokenStore: store, session: mockSession())
    }

    func testPasswordSignInStoresTokensAndSendsBearer() async throws {
        let store = MemoryTokenStore()
        MockURLProtocol.reset { req in
            if req.url!.path == "/api/v2/auth/token" { return .init(body: tokenJSON(access: "sat_1", refresh: "srt_1")) }
            return .init(body: Data(#"{"items":[]}"#.utf8))
        }
        let api = api(store: store)
        try await api.signIn(name: "op", password: "pw")
        XCTAssertEqual(store.load(siteID: "s1")?.refreshToken, "srt_1")
        XCTAssertEqual(store.load(siteID: "s1")?.userName, "op")
        _ = try await api.cameras()
        let last = MockURLProtocol.seen.last!
        XCTAssertEqual(last.value(forHTTPHeaderField: "Authorization"), "Bearer sat_1")
        let signInBody = String(decoding: MockURLProtocol.seen[0].httpBody ?? Data(), as: UTF8.self)
        XCTAssertTrue(signInBody.contains(#""grantType":"password""#), signInBody)
    }

    func testExpiringAccessTokenIsRefreshedOnceForConcurrentCalls() async throws {
        let store = MemoryTokenStore()
        // Access token with 10 s left: inside the 60 s refresh margin.
        store.save(TokenPair(accessToken: "old", expiresAt: Date().addingTimeInterval(10).unixMillis,
                             refreshToken: "srt_old", refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"),
                   siteID: "s1")
        MockURLProtocol.reset { req in
            if req.url!.path == "/api/v2/auth/token" { return .init(body: tokenJSON(access: "sat_new", refresh: "srt_new")) }
            return .init(body: Data(#"{"items":[]}"#.utf8))
        }
        let api = api(store: store)
        async let a = api.cameras()
        async let b = api.cameras()
        _ = try await (a, b)
        let refreshes = MockURLProtocol.seen.filter { $0.url!.path == "/api/v2/auth/token" }
        XCTAssertEqual(refreshes.count, 1, "single-flight: a refresh token must never be presented twice")
        let body = String(decoding: refreshes[0].httpBody ?? Data(), as: UTF8.self)
        XCTAssertTrue(body.contains(#""refreshToken":"srt_old""#) && body.contains(#""grantType":"refresh""#), body)
        XCTAssertEqual(store.load(siteID: "s1")?.refreshToken, "srt_new", "rotated refresh token is saved")
        let cameraCalls = MockURLProtocol.seen.filter { $0.url!.path == "/api/v2/cameras" }
        XCTAssertTrue(cameraCalls.allSatisfy { $0.value(forHTTPHeaderField: "Authorization") == "Bearer sat_new" })
    }

    func test401RefreshesAndRetriesOnce() async throws {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "revoked", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "srt_1", refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"),
                   siteID: "s1")
        MockURLProtocol.reset { req in
            if req.url!.path == "/api/v2/auth/token" { return .init(body: tokenJSON(access: "fresh", refresh: "srt_2")) }
            if req.value(forHTTPHeaderField: "Authorization") == "Bearer revoked" {
                return .init(status: 401, body: Data(#"{"error":{"code":"unauthorized","message":"expired"}}"#.utf8))
            }
            return .init(body: Data(#"{"items":[]}"#.utf8))
        }
        let cams = try await api(store: store).cameras()
        XCTAssertTrue(cams.isEmpty)
        XCTAssertEqual(MockURLProtocol.seen.filter { $0.url!.path == "/api/v2/cameras" }.count, 2)
    }

    func testRejectedRefreshEndsTheSignIn() async throws {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "old", expiresAt: Date().addingTimeInterval(5).unixMillis,
                             refreshToken: "srt_1", refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"),
                   siteID: "s1")
        MockURLProtocol.reset { _ in
            .init(status: 401, body: Data(#"{"error":{"code":"token_reused","message":"x"}}"#.utf8))
        }
        let api = api(store: store)
        let ended = expectation(description: "sign-in ended")
        await api.setSignInEndedHandler { message in
            XCTAssertTrue(message.contains("somewhere else"), message)
            ended.fulfill()
        }
        do {
            _ = try await api.cameras()
            XCTFail("expected signInRequired")
        } catch SentinelAPIError.signInRequired { }
        await fulfillment(of: [ended], timeout: 2)
        XCTAssertNil(store.load(siteID: "s1"), "tokens are dropped")
        let signedIn = await api.isSignedIn
        XCTAssertFalse(signedIn)
    }

    func testFallsBackToSecondaryAddressWhenPrimaryIsUnreachable() async throws {
        let remote = URL(string: "https://tunnel.example.com")!
        MockURLProtocol.reset { req in
            if req.url!.host == "10.0.0.1" { return .init(error: URLError(.cannotConnectToHost)) }
            return .init(body: Data(#"{"local":true,"ldap":false,"oidc":{"enabled":false}}"#.utf8))
        }
        let api = api(secondary: remote)
        _ = try await api.providers()
        XCTAssertEqual(api.baseURL, remote, "the address that answered becomes the active one")
        XCTAssertTrue(api.addresses.isUsingSecondary)
        XCTAssertEqual(MockURLProtocol.seen.map { $0.url!.host }, ["10.0.0.1", "tunnel.example.com"])
        // Next call goes straight to the remote address.
        _ = try await api.providers()
        XCTAssertEqual(MockURLProtocol.seen.last!.url!.host, "tunnel.example.com")
        XCTAssertEqual(MockURLProtocol.seen.count, 3)
    }

    func testServerErrorsDoNotTriggerFallback() async throws {
        MockURLProtocol.reset { _ in .init(status: 503, body: Data(#"{"error":{"code":"unavailable","message":"busy"}}"#.utf8)) }
        let api = api(secondary: URL(string: "https://tunnel.example.com")!)
        do {
            _ = try await api.providers()
            XCTFail("expected an error")
        } catch let error as SentinelAPIError {
            XCTAssertTrue(error.isUnavailable)
        }
        XCTAssertEqual(MockURLProtocol.seen.count, 1)
    }

    func testOldServerIsReportedAsTooOld() async throws {
        MockURLProtocol.reset { _ in .init(status: 200, body: Data("<html>dashboard</html>".utf8)) }
        let probe = SentinelAPI(baseURL: site, siteID: "p", tokenStore: MemoryTokenStore(), session: mockSession())
        do {
            _ = try await SentinelSession.checkServer(probe)
            XCTFail("expected too old")
        } catch let error as SentinelAPIError {
            XCTAssertEqual(error.code, "server_too_old")
            XCTAssertTrue(error.localizedDescription.contains("too old"))
        }
        MockURLProtocol.reset { _ in .init(status: 404, body: Data(#"{"error":"not found"}"#.utf8)) }
        do {
            _ = try await SentinelSession.checkServer(probe)
            XCTFail("expected too old")
        } catch let error as SentinelAPIError {
            XCTAssertEqual(error.code, "server_too_old")
        }
    }

    func testPushRegistrationRequests() async throws {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "a", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "r", refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"), siteID: "s1")
        MockURLProtocol.reset { _ in .init(status: 204) }
        let api = api(store: store)
        try await api.registerPush(apnsToken: "abcd", deviceName: "My iPhone")
        try await api.unregisterPush(apnsToken: "abcd")
        let put = MockURLProtocol.seen[0], del = MockURLProtocol.seen[1]
        XCTAssertEqual(put.httpMethod, "PUT")
        XCTAssertEqual(put.url!.path, "/api/v2/push/registration")
        XCTAssertEqual(String(decoding: put.httpBody ?? Data(), as: UTF8.self), #"{"apnsToken":"abcd","deviceName":"My iPhone"}"#)
        XCTAssertEqual(del.httpMethod, "DELETE")
        XCTAssertEqual(del.url!.query, "apnsToken=abcd")
    }
}
