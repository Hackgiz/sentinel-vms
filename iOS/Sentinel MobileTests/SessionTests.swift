import XCTest
@testable import Sentinel_Mobile

@MainActor
final class SessionTests: XCTestCase {
    func testOIDCCallbackParsing() throws {
        let ok = URL(string: "sentinel-operator://oidc?code=abc123&state=S1")!
        XCTAssertEqual(try SentinelSession.oidcCode(from: ok, expectedState: "S1"), "abc123")
        XCTAssertThrowsError(try SentinelSession.oidcCode(from: ok, expectedState: "other"), "a foreign state is refused")
        let denied = URL(string: "sentinel-operator://oidc?error=access_denied&error_description=not%20mapped&state=S1")!
        XCTAssertThrowsError(try SentinelSession.oidcCode(from: denied, expectedState: "S1")) { error in
            XCTAssertTrue(error.localizedDescription.contains("not mapped"), error.localizedDescription)
        }
    }

    func testStartResumesTheLastSiteSignedOutWithoutTokens() async throws {
        let suite = "SessionTests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let directory = SiteDirectory(defaults: defaults)
        let site = SavedSite(name: "HQ", address: "http://10.0.0.1:8090")
        directory.upsert(site)
        directory.currentSiteID = site.id
        let session = SentinelSession(directory: directory, tokenStore: MemoryTokenStore(), defaults: defaults)
        session.start()
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertEqual(session.phase, .signedOut(siteID: site.id))
    }

    func testStartResumesSignedInWhenTokensAreSaved() async throws {
        let suite = "SessionTests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let directory = SiteDirectory(defaults: defaults)
        let site = SavedSite(name: "HQ", address: "http://10.0.0.1:8090")
        directory.upsert(site)
        directory.currentSiteID = site.id
        let tokens = MemoryTokenStore()
        tokens.save(TokenPair(accessToken: "a", expiresAt: Date().addingTimeInterval(800).unixMillis,
                              refreshToken: "r", refreshExpiresAt: Date().addingTimeInterval(86400).unixMillis, userName: "dana"), siteID: site.id)
        let session = SentinelSession(directory: directory, tokenStore: tokens, defaults: defaults)
        session.start()
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertEqual(session.phase, .signedIn(siteID: site.id))
        XCTAssertTrue(session.can(.acknowledgeAlarms), "unknown permissions are not hidden")
    }

    func testDemoModeDataFlow() async throws {
        let session = SentinelSession.shared
        session.enterDemoMode()
        defer { session.leaveDemo() }
        let store = AppStore.shared
        await store.refreshAll()
        XCTAssertEqual(store.cameras.count, 6)
        let alarm = try XCTUnwrap(store.sortedAlarms.first)
        XCTAssertTrue(alarm.needsAttention)
        await store.acknowledge(alarm)
        XCTAssertEqual(store.alarms.first { $0.id == alarm.id }?.state, .acknowledged)
        await store.setState(alarm, to: .resolved, note: "false positive")
        XCTAssertNil(store.alarms.first { $0.id == alarm.id }, "resolved alarms leave the open queue")
    }
}
