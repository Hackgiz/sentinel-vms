import XCTest
@testable import Sentinel_Mobile

@MainActor
final class SiteDirectoryTests: XCTestCase {
    private func defaults() -> UserDefaults {
        let name = "SiteDirectoryTests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        addTeardownBlock { d.removePersistentDomain(forName: name) }
        return d
    }

    func testNormalizedAddresses() {
        XCTAssertEqual(SavedSite.normalizedURL("192.168.1.20")?.absoluteString, "http://192.168.1.20:8090")
        XCTAssertEqual(SavedSite.normalizedURL("nvr.local:9000")?.absoluteString, "http://nvr.local:9000")
        XCTAssertEqual(SavedSite.normalizedURL("https://sentinel.example.com/")?.absoluteString, "https://sentinel.example.com")
        XCTAssertEqual(SavedSite.normalizedURL(" HTTP://Host:1/x/?q=1#f ")?.absoluteString, "http://Host:1/x")
        XCTAssertNil(SavedSite.normalizedURL(""))
        XCTAssertNil(SavedSite.normalizedURL("ftp://host"))
    }

    func testSitesPersistAndRemoveDropsTokens() {
        let d = defaults()
        let tokens = MemoryTokenStore()
        let dir = SiteDirectory(defaults: d)
        var a = SavedSite(name: "HQ", address: "https://hq.example.com", remoteAddress: "https://tunnel.example.com")
        a.lastUserName = "dana"
        dir.upsert(a)
        dir.upsert(SavedSite(name: "", address: "10.0.0.9"))
        dir.currentSiteID = a.id
        tokens.save(TokenPair(accessToken: "a", expiresAt: 1, refreshToken: "r", refreshExpiresAt: 2, userName: "dana"), siteID: a.id)

        let reloaded = SiteDirectory(defaults: d)
        XCTAssertEqual(reloaded.sites.count, 2)
        XCTAssertEqual(reloaded.currentSite?.remoteURL?.host, "tunnel.example.com")
        XCTAssertEqual(reloaded.currentSite?.lastUserName, "dana")
        XCTAssertEqual(reloaded.site(forAddress: "https://hq.example.com/")?.id, a.id)
        XCTAssertEqual(reloaded.sites[1].displayName, "10.0.0.9")

        reloaded.remove(a, tokenStore: tokens)
        XCTAssertNil(tokens.load(siteID: a.id))
        XCTAssertNil(reloaded.currentSiteID)
        XCTAssertEqual(SiteDirectory(defaults: d).sites.count, 1)
    }

    func testLegacyPairingBecomesASavedSite() {
        let d = defaults()
        d.set("http://192.168.1.10:8090", forKey: "sentinel.serverURL")
        d.set("https://old-tunnel.trycloudflare.com", forKey: "sentinel.remoteURL")
        d.set("Office", forKey: "sentinel.serverName")
        let dir = SiteDirectory(defaults: d)
        XCTAssertEqual(dir.sites.count, 1)
        XCTAssertEqual(dir.sites[0].name, "Office")
        XCTAssertEqual(dir.sites[0].remoteAddress, "https://old-tunnel.trycloudflare.com")
        XCTAssertEqual(dir.currentSiteID, dir.sites[0].id)
        XCTAssertNil(d.string(forKey: "sentinel.serverURL"), "legacy keys are gone")
        XCTAssertEqual(SiteDirectory(defaults: d).sites.count, 1, "migrating twice adds nothing")
    }

    func testQRPayloads() {
        let json = #"{"url":"http://192.168.1.10:8090","code":"123456","v":1,"remoteURL":"https://t.example.com"}"#
        let payload = "sentinel-vms://pair?" + json.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        let qr = SavedSite.fromQR(payload)
        XCTAssertEqual(qr?.address, "http://192.168.1.10:8090")
        XCTAssertEqual(qr?.remote, "https://t.example.com")
        XCTAssertEqual(SavedSite.fromQR("https://sentinel.example.com")?.address, "https://sentinel.example.com")
        XCTAssertNil(SavedSite.fromQR("WIFI:S:x;;"))
        let plainHTTPRemote = "sentinel-vms://pair?" + #"{"url":"http://a:1","remoteURL":"http://b:2"}"#.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        XCTAssertNil(SavedSite.fromQR(plainHTTPRemote)?.remote, "a plain-http remote is ignored")
    }
}
