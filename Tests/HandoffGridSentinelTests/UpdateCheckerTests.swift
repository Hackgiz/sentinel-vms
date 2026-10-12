import XCTest
@testable import SentinelCore

final class UpdateCheckerTests: XCTestCase {
    func testVersionComparison() {
        XCTAssertTrue(SentinelVersion.isNewer("1.1.2", than: "1.1.1"))
        XCTAssertTrue(SentinelVersion.isNewer("1.1.10", than: "1.1.9"))
        XCTAssertTrue(SentinelVersion.isNewer("1.2", than: "1.1.9"))
        XCTAssertTrue(SentinelVersion.isNewer("1.1.1.1", than: "1.1.1"))
        XCTAssertFalse(SentinelVersion.isNewer("1.1.1", than: "1.1.1"))
        XCTAssertFalse(SentinelVersion.isNewer("1.1.0", than: "1.1.1"))
        XCTAssertFalse(SentinelVersion.isNewer("1.1.1", than: "1.1.1-beta"))
        XCTAssertFalse(SentinelVersion.isNewer("", than: "1.1.1"))
        XCTAssertFalse(SentinelVersion.isNewer("soon", than: "1.1.1"))
        XCTAssertFalse(SentinelVersion.isNewer("1.2.0", than: "dev"))
    }

    @MainActor
    func testCheckFindsUpdateAndRemembersDismissal() async throws {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "UpdateCheckerTests-\(UUID())"))
        StubFeed.body = #"{"mac":{"version":"1.1.2","notes":"Camera analytics","url":"https://sentvms.com/?get=mac"},"linux":{"version":"0.4.2"}}"#
        StubFeed.status = 200
        let checker = UpdateChecker(currentVersion: "1.1.1", session: StubFeed.session, defaults: defaults)

        await checker.checkNow()
        XCTAssertEqual(checker.latestVersion, "1.1.2")
        XCTAssertEqual(checker.notes, "Camera analytics")
        XCTAssertTrue(checker.updateAvailable)
        XCTAssertTrue(checker.shouldShowNotice)
        XCTAssertEqual(StubFeed.lastUserAgent, "SentinelMac/1.1.1")

        checker.dismissNotice()
        XCTAssertFalse(checker.shouldShowNotice)
        let reopened = UpdateChecker(currentVersion: "1.1.1", session: StubFeed.session, defaults: defaults)
        await reopened.checkNow()
        XCTAssertFalse(reopened.shouldShowNotice, "dismissal should survive a relaunch")

        // A later release shows the notice again.
        StubFeed.body = #"{"mac":{"version":"1.1.3"},"linux":{"version":"0.4.2"}}"#
        await reopened.checkNow()
        XCTAssertTrue(reopened.shouldShowNotice)
    }

    @MainActor
    func testFailedCheckKeepsWorking() async throws {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "UpdateCheckerTests-\(UUID())"))
        StubFeed.status = 503
        let checker = UpdateChecker(currentVersion: "1.1.1", session: StubFeed.session, defaults: defaults)
        await checker.checkNow()
        XCTAssertFalse(checker.updateAvailable)
        XCTAssertNotNil(checker.lastError)
        XCTAssertTrue(checker.automaticChecks, "checks default to on")
    }
}

private final class StubFeed: URLProtocol {
    nonisolated(unsafe) static var body = "{}"
    nonisolated(unsafe) static var status = 200
    nonisolated(unsafe) static var lastUserAgent: String?

    static var session: URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubFeed.self]
        return URLSession(configuration: config)
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        StubFeed.lastUserAgent = request.value(forHTTPHeaderField: "User-Agent")
        let response = HTTPURLResponse(url: request.url!, statusCode: StubFeed.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(StubFeed.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
