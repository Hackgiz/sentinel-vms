import XCTest
@testable import Sentinel_Mobile

/// Sentinel Cloud on the phone (WAVE12-CLOUD.md, stream D): decoding, the
/// remote-address rule, backup token renewal, the "via Sentinel Cloud" host
/// rule and backup URLs. No network: `MockURLProtocol` answers everything.
@MainActor
final class CloudTests: XCTestCase {
    private let lan = URL(string: "http://10.0.0.1:8090")!
    private let relay = URL(string: "https://k7f3q9x2ab-cloud.sentvms.com")!
    private let scb = "scb_k7f3q9x2ab_" + String(repeating: "A", count: 43)

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try SentinelAPI.decode(T.self, from: Data(json.utf8))
    }

    private func iso(_ text: String) -> Date {
        ISO8601DateFormatter().date(from: text)!
    }

    private func signedInStore(siteID: String = "s1") -> MemoryTokenStore {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "sat", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "srt", refreshExpiresAt: Date().addingTimeInterval(86400).unixMillis, userName: "op"),
                   siteID: siteID)
        return store
    }

    private func api(secondary: URL? = nil) -> SentinelAPI {
        SentinelAPI(addresses: SiteAddresses(primary: lan, secondary: secondary), siteID: "s1",
                    tokenStore: signedInStore(), session: mockSession())
    }

    private func grant(expiresIn: TimeInterval = 30 * 86400, base: String = "https://cloud.sentvms.com/v1/backup",
                       token: String? = nil) -> V2.CloudBackupAccess {
        V2.CloudBackupAccess(token: token ?? scb, expiresAt: Date().addingTimeInterval(expiresIn),
                             serverId: "k7f3q9x2ab", baseURL: base)
    }

    private func backupAccessJSON(expiresIn: TimeInterval) -> Data {
        Data("""
        {"token":"\(scb)","expiresAt":\(Date().addingTimeInterval(expiresIn).unixMillis),
         "serverId":"k7f3q9x2ab","baseURL":"https://cloud.sentvms.com/v1/backup"}
        """.utf8)
    }

    // MARK: - Decoding

    func testCloudRemoteDecoding() throws {
        let cloud = try decode(V2.CloudRemote.self, #"{"remoteURL":"https://k7f3q9x2ab-cloud.sentvms.com","via":"cloud"}"#)
        XCTAssertEqual(cloud.remoteURL, "https://k7f3q9x2ab-cloud.sentvms.com")
        XCTAssertTrue(cloud.isCloud)

        let none = try decode(V2.CloudRemote.self, #"{"remoteURL":null,"via":null}"#)
        XCTAssertNil(none.remoteURL)
        XCTAssertFalse(none.isCloud)

        let tunnel = try decode(V2.CloudRemote.self, #"{"remoteUrl":"https://quiet-river.trycloudflare.com","via":"tunnel"}"#)
        XCTAssertEqual(tunnel.remoteURL, "https://quiet-river.trycloudflare.com", "the v2 `Url` spelling is accepted too")
        XCTAssertFalse(tunnel.isCloud)

        XCTAssertNil(try decode(V2.CloudRemote.self, "{}").remoteURL)
    }

    func testBackupAccessDecodingAcceptsEveryTimeFormat() throws {
        let when = iso("2026-11-18T20:00:00Z")
        let forms = [
            #""2026-11-18T20:00:00Z""#,
            #""2026-11-18T20:00:00.000Z""#,
            String(when.unixMillis),
            String(Int(when.timeIntervalSince1970))
        ]
        for form in forms {
            let access = try decode(V2.CloudBackupAccess.self, """
            {"token":"\(scb)","expiresAt":\(form),"serverId":"k7f3q9x2ab","baseURL":"https://cloud.sentvms.com/v1/backup"}
            """)
            XCTAssertEqual(access.expiresAt.timeIntervalSince1970, when.timeIntervalSince1970, accuracy: 0.001, form)
            XCTAssertEqual(access.token, scb)
            XCTAssertEqual(access.serverId, "k7f3q9x2ab")
            XCTAssertEqual(access.baseURL, "https://cloud.sentvms.com/v1/backup")
        }

        let other = try decode(V2.CloudBackupAccess.self, #"{"token":"t","expiresAt":"2026-11-18T20:00:00Z","serverID":"x","baseUrl":"https://b.example/v1/backup"}"#)
        XCTAssertEqual(other.baseURL, "https://b.example/v1/backup")
        XCTAssertEqual(other.serverId, "x")

        XCTAssertThrowsError(try decode(V2.CloudBackupAccess.self, #"{"token":"t","expiresAt":1}"#), "no baseURL, no backup")

        // What the Keychain keeps reads back the same.
        let stored = try JSONDecoder().decode(V2.CloudBackupAccess.self, from: JSONEncoder().encode(other))
        XCTAssertEqual(stored, other)
    }

    func testBackupListingDecodingAndGrouping() throws {
        let listing = try decode(Cloud.BackupListing.self, """
        {"objects":[
          {"key":"alarms/a2/clip.mp4","size":1048576,"uploadedAt":"2026-10-19T20:05:00Z","contentType":"video/mp4","meta":{}},
          {"key":"alarms/a2/snapshot.jpg","size":20480,"uploadedAt":"2026-10-19T20:01:00Z","contentType":"image/jpeg",
           "meta":{"alarmId":"a2","cameraName":"Front Door","title":"Person detected","createdAt":\(iso("2026-10-19T20:00:00Z").unixMillis),"severity":"Warning"}},
          {"key":"alarms/a2/alarm.json","size":900,"uploadedAt":"2026-10-19T20:01:00Z","contentType":"application/json"},
          {"key":"events/e9/thumb.jpg","size":100,"uploadedAt":"2026-10-19T19:00:00Z"},
          {"key":"alarms/a1/snapshot.jpg","size":100,"uploadedAt":"2026-10-18T10:00:00Z","meta":null},
          {"size":5}
        ],"next":"alarms/a1/snapshot.jpg"}
        """)
        XCTAssertEqual(listing.objects.count, 5, "an object without a key is skipped, not fatal")
        XCTAssertEqual(listing.next, "alarms/a1/snapshot.jpg")
        XCTAssertEqual(listing.objects[0].size, 1_048_576)
        XCTAssertEqual(listing.objects[0].uploadedAt, iso("2026-10-19T20:05:00Z"))

        var entries = Cloud.BackupAlarmEntry.merge(listing.objects, into: [])
        XCTAssertEqual(entries.map(\.id), ["a2", "a1"], "alarms only, newest first; event thumbnails are left out")
        let a2 = entries[0]
        XCTAssertEqual(a2.snapshotKey, "alarms/a2/snapshot.jpg")
        XCTAssertEqual(a2.clipKey, "alarms/a2/clip.mp4")
        XCTAssertEqual(a2.alarmJSONKey, "alarms/a2/alarm.json")
        XCTAssertEqual(a2.clipSize, 1_048_576)
        XCTAssertEqual(a2.uploadedAt, iso("2026-10-19T20:05:00Z"), "the latest upload of the group")
        XCTAssertEqual(a2.summary.cameraName, "Front Door")
        XCTAssertEqual(a2.date, iso("2026-10-19T20:00:00Z"), "the alarm's own time beats the upload time")
        XCTAssertTrue(a2.summary.isDescriptive)
        XCTAssertNil(entries[1].clipKey)
        XCTAssertFalse(entries[1].summary.isDescriptive)

        // The next page adds a1's late clip and an older alarm.
        let page2 = try decode(Cloud.BackupListing.self, """
        {"objects":[{"key":"alarms/a1/clip.mp4","size":10,"uploadedAt":"2026-10-18T10:02:00Z","meta":"{\\"cameraName\\":\\"Yard\\"}"},
                    {"key":"alarms/a0/snapshot.jpg","size":10,"uploadedAt":1792000000000}]}
        """)
        XCTAssertNil(page2.next)
        entries = Cloud.BackupAlarmEntry.merge(page2.objects, into: entries)
        XCTAssertEqual(entries.map(\.id), ["a2", "a1", "a0"])
        XCTAssertEqual(entries[1].clipKey, "alarms/a1/clip.mp4")
        XCTAssertEqual(entries[1].summary.cameraName, "Yard", "meta sent as the header's JSON text is read too")

        XCTAssertTrue(try decode(Cloud.BackupListing.self, #"{"objects":null}"#).objects.isEmpty)
        XCTAssertNil(Cloud.BackupKey("alarms/a1"))
        XCTAssertEqual(Cloud.BackupKey("events/e1/thumb.jpg")?.eventID, "e1")
    }

    func testAlarmJSONIsV2AlarmPlusKeys() throws {
        let alarm = """
        "id":"a1","cameraId":"c1","cameraName":"Front Door","kind":"Person","title":"Person detected",
        "detail":"At the door","severity":"Warning","state":"New","owner":"Unassigned",
        "createdAt":1792440000000,"updatedAt":1792440000000,"lastEventAt":1792440060000,"eventCount":2,
        "eventId":"e1","instructions":[],"responseLog":null
        """
        let full = try decode(Cloud.BackupAlarmRecord.self,
                              "{\(alarm),\"snapshotKey\":\"alarms/a1/snapshot.jpg\",\"clipKey\":\"alarms/a1/clip.mp4\"}")
        XCTAssertEqual(full.alarm?.id, "a1", "the file decodes as the server's V2.Alarm")
        XCTAssertEqual(full.alarm?.severity, .warning)
        XCTAssertEqual(full.summary.title, "Person detected")
        XCTAssertEqual(full.summary.cameraName, "Front Door")
        XCTAssertEqual(full.summary.createdAt, Date(unixMillis: 1_792_440_000_000))
        XCTAssertEqual(full.summary.eventCount, 2)
        XCTAssertEqual(full.summary.snapshotKey, "alarms/a1/snapshot.jpg")
        XCTAssertEqual(full.summary.clipKey, "alarms/a1/clip.mp4")

        let nested = try decode(Cloud.BackupAlarmRecord.self,
                                "{\"alarm\":{\(alarm)},\"keys\":{\"snapshot\":\"s.jpg\",\"clip\":\"c.mp4\"}}")
        XCTAssertEqual(nested.alarm?.id, "a1", "the alarm may be nested under `alarm`")
        XCTAssertEqual(nested.summary.snapshotKey, "s.jpg")
        XCTAssertEqual(nested.summary.clipKey, "c.mp4")

        // Not a full V2.Alarm: still shown with what's there.
        let partial = try decode(Cloud.BackupAlarmRecord.self,
                                 #"{"id":"a3","title":"Motion","cameraName":"Yard","createdAt":"2026-10-19T08:00:00Z","clipKey":"alarms/a3/clip.mp4"}"#)
        XCTAssertNil(partial.alarm)
        XCTAssertEqual(partial.summary.alarmID, "a3")
        XCTAssertEqual(partial.summary.createdAt, iso("2026-10-19T08:00:00Z"))

        let entry = Cloud.BackupAlarmEntry(id: "a3", snapshotKey: "alarms/a3/snapshot.jpg").applying(partial)
        XCTAssertEqual(entry.clipKey, "alarms/a3/clip.mp4", "keys named in alarm.json are used")
        XCTAssertEqual(entry.snapshotKey, "alarms/a3/snapshot.jpg", "a key the file doesn't name stays")
        XCTAssertEqual(entry.displayTitle, "Motion")
        XCTAssertEqual(entry.summary.cameraName, "Yard")
    }

    // MARK: - Remote address

    func testRemoteAddressSaveRule() {
        let cloudURL = "https://k7f3q9x2ab-cloud.sentvms.com"
        let cloud = V2.CloudRemote(remoteURL: cloudURL, via: "cloud")
        let tunnel = V2.CloudRemote(remoteURL: "https://new-name.trycloudflare.com", via: "tunnel")
        let null = V2.CloudRemote(remoteURL: nil, via: nil)
        func site(_ remote: String?, _ source: RemoteAddressSource?) -> SavedSite {
            SavedSite(id: "s1", name: "HQ", address: "http://10.0.0.1:8090", remoteAddress: remote, remoteAddressSource: source)
        }
        func save(_ site: SavedSite, _ reported: V2.CloudRemote) -> String? {
            CloudSync.remoteAddressToSave(for: site, reported: reported)
        }
        let old = "https://old-name.trycloudflare.com"

        // No remote address yet: whatever the server reports.
        XCTAssertEqual(save(site(nil, nil), cloud), cloudURL)
        XCTAssertEqual(save(site(nil, nil), tunnel), "https://new-name.trycloudflare.com")
        XCTAssertEqual(save(site("", .manual), cloud), cloudURL, "an empty address counts as none")

        // Saved from the server before: follows the server, cloud or tunnel.
        XCTAssertEqual(save(site(old, .server), tunnel), "https://new-name.trycloudflare.com")
        XCTAssertEqual(save(site(old, .server), cloud), cloudURL)

        // From a QR code: only a Sentinel Cloud address replaces it.
        XCTAssertEqual(save(site(old, .qr), cloud), cloudURL, "a stable cloud host beats a scanned quick tunnel")
        XCTAssertNil(save(site(old, .qr), tunnel), "a tunnel address doesn't replace a scanned one")

        // Typed by hand: never replaced.
        XCTAssertNil(save(site("https://cams.example.com", .manual), cloud))
        XCTAssertNil(save(site("https://cams.example.com", .manual), tunnel))
        XCTAssertNil(save(site("https://cams.example.com", nil), cloud), "saved before sources were tracked: counts as typed")

        // Never cleared, never saved twice, https only.
        XCTAssertNil(save(site(old, .qr), null), "null leaves a QR-scanned remote address alone")
        XCTAssertNil(save(site(old, .server), null))
        XCTAssertNil(save(site(nil, nil), null))
        XCTAssertNil(save(site("https://K7F3Q9X2AB-cloud.sentvms.com/", .server), cloud), "the same address (case, trailing slash)")
        XCTAssertNil(save(site(nil, nil), V2.CloudRemote(remoteURL: "", via: "cloud")))
        XCTAssertNil(save(site(nil, nil), V2.CloudRemote(remoteURL: "http://k7f3q9x2ab-cloud.sentvms.com", via: "cloud")),
                     "only https, as from a QR code")
        XCTAssertNil(save(site(nil, nil), V2.CloudRemote(remoteURL: "not an address", via: "cloud")))
        XCTAssertEqual(save(site(nil, nil), V2.CloudRemote(remoteURL: cloudURL + "/?x=1", via: "cloud")), cloudURL, "saved normalized")
    }

    func testRemoteAddressSourceIsSavedAndMigrated() throws {
        // A site saved by an earlier version has no source: a remote address it has counts as typed.
        let legacy = try JSONDecoder().decode(SavedSite.self, from: Data(
            #"{"id":"s1","name":"HQ","address":"http://10.0.0.1:8090","remoteAddress":"https://cams.example.com"}"#.utf8))
        XCTAssertNil(legacy.remoteAddressSource)
        XCTAssertEqual(legacy.remoteSource, .manual)
        let noRemote = try JSONDecoder().decode(SavedSite.self, from: Data(#"{"id":"s2","name":"","address":"10.0.0.2"}"#.utf8))
        XCTAssertNil(noRemote.remoteSource, "no address, no source")

        var site = SavedSite(name: "HQ", address: "http://10.0.0.1:8090", remoteAddress: relay.absoluteString, remoteAddressSource: .server)
        let reread = try JSONDecoder().decode(SavedSite.self, from: JSONEncoder().encode(site))
        XCTAssertEqual(reread.remoteSource, .server)
        site.remoteAddress = nil
        XCTAssertNil(site.remoteSource, "a cleared address has no source")

        let name = "CloudTests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: name)!
        defer { defaults.removePersistentDomain(forName: name) }
        let directory = SiteDirectory(defaults: defaults)
        directory.upsert(SavedSite(id: "q", name: "", address: "10.0.0.3", remoteAddress: "https://t.example.com", remoteAddressSource: .qr))
        XCTAssertEqual(SiteDirectory(defaults: defaults).site(id: "q")?.remoteSource, .qr, "persisted")
    }

    func testRemoteAddressFromTheServer() async throws {
        let api = api()
        let site = SavedSite(id: "s1", name: "HQ", address: lan.absoluteString,
                             remoteAddress: "https://scanned.trycloudflare.com", remoteAddressSource: .qr)

        MockURLProtocol.reset { _ in .init(body: Data(#"{"remoteURL":"https://k7f3q9x2ab-cloud.sentvms.com","via":"cloud"}"#.utf8)) }
        let updated = await CloudSync.updatedSite(site, api: api)
        XCTAssertEqual(updated?.remoteAddress, "https://k7f3q9x2ab-cloud.sentvms.com")
        XCTAssertEqual(updated?.remoteAddressSource, .server, "marked as from the server")
        XCTAssertEqual(updated?.id, site.id)
        let request = try XCTUnwrap(MockURLProtocol.seen.last)
        XCTAssertEqual(request.url?.path, "/api/v2/cloud/remote")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer sat")

        var typed = site
        typed.remoteAddressSource = .manual
        let keepTyped = await CloudSync.updatedSite(typed, api: api)
        XCTAssertNil(keepTyped, "a typed address stays even against Sentinel Cloud")

        MockURLProtocol.reset { _ in .init(body: Data(#"{"remoteURL":null,"via":null}"#.utf8)) }
        let keep = await CloudSync.updatedSite(site, api: api)
        XCTAssertNil(keep, "null: the scanned remote stays")

        MockURLProtocol.reset { _ in .init(status: 404, body: Data(#"{"error":{"code":"not_found","message":"no route"}}"#.utf8)) }
        let tooOld = await CloudSync.updatedSite(site, api: api)
        XCTAssertNil(tooOld, "a server from before Sentinel Cloud changes nothing")
    }

    func testAdoptedRemoteAddressBecomesTheSecondaryAddress() async throws {
        let suite = "CloudTests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let directory = SiteDirectory(defaults: defaults)
        let site = SavedSite(name: "HQ", address: lan.absoluteString, remoteAddress: "https://scanned.trycloudflare.com")
        directory.upsert(site)
        // No tokens: the session stays signed out and never calls the server.
        let session = SentinelSession(directory: directory, tokenStore: MemoryTokenStore(), defaults: defaults)
        session.select(site)
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertEqual(session.phase, .signedOut(siteID: site.id))
        XCTAssertEqual(session.api?.addresses.secondary?.host, "scanned.trycloudflare.com")

        var updated = site
        updated.remoteAddress = relay.absoluteString
        let before = session.api
        await session.adoptRemoteAddress(updated)
        XCTAssertFalse(session.api === before, "a fresh client replaces the old one")
        XCTAssertEqual(directory.site(id: site.id)?.remoteAddress, relay.absoluteString, "saved")
        XCTAssertEqual(session.api?.addresses.secondary, relay, "used from now on")
        XCTAssertEqual(session.phase, .signedOut(siteID: site.id))
        XCTAssertFalse(session.isViaSentinelCloud, "on the primary address until it stops answering")
    }

    /// The hand-over waits for a refresh already running on the old client
    /// (here a forced one, as the event stream does after its socket was
    /// closed for an expired token), so the fresh client reads the rotated
    /// refresh token from the store and never presents a used one.
    func testHandOverWaitsForARefreshInFlight() async throws {
        let store = signedInStore()   // access token good for 800 s: no refresh needed on its own
        let started = expectation(description: "refresh started")
        let tokens = tokenJSON(access: "sat_new", refresh: "srt_new")
        MockURLProtocol.reset { req in
            guard req.url!.path == "/api/v2/auth/token" else { return .init(body: Data(#"{"items":[]}"#.utf8)) }
            started.fulfill()
            Thread.sleep(forTimeInterval: 0.3)
            return .init(body: tokens)
        }
        let old = SentinelAPI(addresses: SiteAddresses(primary: lan, secondary: nil), siteID: "s1",
                              tokenStore: store, session: mockSession())
        let forced = Task { try await old.accessToken(forceRefresh: true) }
        await fulfillment(of: [started], timeout: 2)
        XCTAssertEqual(store.load(siteID: "s1")?.refreshToken, "srt", "still in flight")

        await CloudSync.settleTokens(of: old)
        XCTAssertEqual(store.load(siteID: "s1")?.refreshToken, "srt_new", "the refresh landed before the hand-over goes on")
        _ = try await forced.value

        // The fresh client starts from the store: its next refresh presents the rotated token.
        MockURLProtocol.reset { _ in .init(body: tokens) }
        let fresh = SentinelAPI(addresses: SiteAddresses(primary: lan, secondary: relay), siteID: "s1",
                                tokenStore: store, session: mockSession())
        _ = try await fresh.accessToken(forceRefresh: true)
        let refresh = try XCTUnwrap(MockURLProtocol.seen.first { $0.url!.path == "/api/v2/auth/token" })
        let body = String(decoding: refresh.httpBody ?? Data(), as: UTF8.self)
        XCTAssertTrue(body.contains(#""refreshToken":"srt_new""#), body)
    }

    // MARK: - Backup token

    func testBackupTokenRenewalRule() {
        let now = Date()
        func grant(_ left: TimeInterval, token: String = "scb_x") -> V2.CloudBackupAccess {
            V2.CloudBackupAccess(token: token, expiresAt: now.addingTimeInterval(left), serverId: "x", baseURL: "https://b")
        }
        XCTAssertTrue(BackupAccess.needsRenewal(nil, now: now), "no token: fetch one")
        XCTAssertFalse(BackupAccess.needsRenewal(grant(30 * 86400), now: now))
        XCTAssertFalse(BackupAccess.needsRenewal(grant(7 * 86400 + 60), now: now))
        XCTAssertTrue(BackupAccess.needsRenewal(grant(7 * 86400 - 60), now: now), "under 7 days left: renew")
        XCTAssertTrue(BackupAccess.needsRenewal(grant(-1), now: now))
        XCTAssertTrue(BackupAccess.isUsable(grant(60), now: now), "a token about to be renewed still opens the backup")
        XCTAssertFalse(BackupAccess.isUsable(grant(-1), now: now))
        XCTAssertFalse(BackupAccess.isUsable(grant(86400, token: ""), now: now))
        XCTAssertFalse(BackupAccess.isUsable(nil, now: now))
    }

    func testBackupTokenIsFetchedStoredRenewedAndDropped() async throws {
        let store = MemoryBackupTokenStore()
        let access = BackupAccess(store: store)
        let api = api()

        let fresh = backupAccessJSON(expiresIn: 30 * 86400)
        MockURLProtocol.reset { _ in .init(body: fresh) }
        await access.refreshIfNeeded(api: api)
        XCTAssertEqual(store.load(siteID: "s1")?.token, scb, "kept in the token store")
        XCTAssertEqual(access.grant(forSite: "s1")?.token, scb)
        XCTAssertNil(access.grant(forSite: "other"))
        XCTAssertEqual(MockURLProtocol.seen.map { $0.url!.path }, ["/api/v2/cloud/backup-access"])
        XCTAssertEqual(MockURLProtocol.seen[0].value(forHTTPHeaderField: "Authorization"), "Bearer sat")

        await access.refreshIfNeeded(api: api)
        XCTAssertEqual(MockURLProtocol.seen.count, 1, "a fresh token isn't fetched again")

        // A saved token with 3 days left is loaded offline, then renewed.
        store.save(grant(expiresIn: 3 * 86400, token: "scb_old"), siteID: "s1")
        let relaunched = BackupAccess(store: store)
        relaunched.activate(siteID: "s1")
        XCTAssertEqual(relaunched.grant(forSite: "s1")?.token, "scb_old")
        MockURLProtocol.reset { _ in .init(body: fresh) }
        await relaunched.refreshIfNeeded(api: api)
        XCTAssertEqual(MockURLProtocol.seen.count, 1)
        XCTAssertEqual(store.load(siteID: "s1")?.token, scb)

        // The server can't be reached: the saved token stays.
        store.save(grant(expiresIn: 3 * 86400, token: "scb_old"), siteID: "s1")
        let offline = BackupAccess(store: store)
        MockURLProtocol.reset { _ in .init(error: URLError(.cannotConnectToHost)) }
        await offline.refreshIfNeeded(api: api)
        XCTAssertEqual(offline.grant(forSite: "s1")?.token, "scb_old")
        XCTAssertEqual(store.load(siteID: "s1")?.token, "scb_old")

        // Backup turned off or the server unlinked (404): dropped.
        MockURLProtocol.reset { _ in .init(status: 404, body: Data(#"{"error":{"code":"not_found","message":"backup is off"}}"#.utf8)) }
        await offline.refreshIfNeeded(api: api)
        XCTAssertNil(offline.grant(forSite: "s1"))
        XCTAssertNil(store.load(siteID: "s1"))

        // An expired token isn't kept.
        store.save(grant(expiresIn: -60), siteID: "s1")
        let expired = BackupAccess(store: store)
        expired.activate(siteID: "s1")
        XCTAssertNil(expired.grant(forSite: "s1"))
        XCTAssertNil(store.load(siteID: "s1"))

        // Sign-out.
        store.save(grant(), siteID: "s1")
        expired.activate(siteID: "s1")
        XCTAssertNotNil(expired.grant(forSite: "s1"))
        expired.clear(siteID: "s1")
        XCTAssertNil(expired.grant(forSite: "s1"))
        XCTAssertNil(store.load(siteID: "s1"))
    }

    // MARK: - "via Sentinel Cloud"

    func testRelayPillHostRule() {
        XCTAssertTrue(Cloud.isRelayHost(relay))
        XCTAssertTrue(Cloud.isRelayHost(URL(string: "https://K7F3Q9X2AB-CLOUD.SENTVMS.COM")))
        XCTAssertFalse(Cloud.isRelayHost(URL(string: "https://cloud.sentvms.com")), "the control plane isn't a relay")
        XCTAssertFalse(Cloud.isRelayHost(URL(string: "https://-cloud.sentvms.com")))
        XCTAssertFalse(Cloud.isRelayHost(URL(string: "https://x-cloud.sentvms.com.example.net")))
        XCTAssertFalse(Cloud.isRelayHost(URL(string: "https://quiet-river.trycloudflare.com")))
        XCTAssertFalse(Cloud.isRelayHost(nil))

        XCTAssertTrue(Cloud.isViaRelay(isUsingSecondary: true, secondary: relay))
        XCTAssertFalse(Cloud.isViaRelay(isUsingSecondary: false, secondary: relay), "on the local network: no pill")
        XCTAssertFalse(Cloud.isViaRelay(isUsingSecondary: true, secondary: URL(string: "https://quiet-river.trycloudflare.com")))

        let addresses = SiteAddresses(primary: lan, secondary: relay)
        XCTAssertFalse(Cloud.isViaRelay(isUsingSecondary: addresses.isUsingSecondary, secondary: addresses.secondary))
        addresses.markWorking(relay)
        XCTAssertTrue(Cloud.isViaRelay(isUsingSecondary: addresses.isUsingSecondary, secondary: addresses.secondary))
    }

    func testCloudStatusHeadline() {
        func resolve(viaCloud: Bool = false, usingRemote: Bool = false, remote: V2.CloudRemote? = nil,
                     known: Bool = true, failed: Bool = false, backup: Bool = false, siteRemote: URL? = nil) -> CloudConnectionStatus {
            CloudConnectionStatus.resolve(viaCloud: viaCloud, usingRemote: usingRemote, remote: remote, remoteKnown: known,
                                          failed: failed, hasBackup: backup, siteRemote: siteRemote)
        }
        let cloud = V2.CloudRemote(remoteURL: relay.absoluteString, via: "cloud")
        XCTAssertEqual(resolve(viaCloud: true, usingRemote: true), .viaCloud)
        XCTAssertEqual(CloudConnectionStatus.viaCloud.title, "Connected via Sentinel Cloud")
        XCTAssertEqual(resolve(remote: cloud), .localNetwork)
        XCTAssertEqual(CloudConnectionStatus.localNetwork.title, "Local network")
        XCTAssertEqual(resolve(usingRemote: true, remote: cloud), .remoteAddress)
        XCTAssertEqual(resolve(remote: V2.CloudRemote(remoteURL: "https://q.trycloudflare.com", via: "tunnel")), .notLinked)
        XCTAssertEqual(resolve(remote: nil), .notLinked, "a server too old to answer isn't linked")
        XCTAssertEqual(CloudConnectionStatus.notLinked.title, "Not linked — set up in Sentinel Admin on your server")
        XCTAssertEqual(resolve(known: false), .checking)
        XCTAssertEqual(resolve(known: false, failed: true), .unreachable)
        XCTAssertEqual(resolve(known: false, failed: true, backup: true), .localNetwork, "a saved backup token means linked")
        XCTAssertEqual(resolve(known: false, siteRemote: relay), .localNetwork)

        // App Store rule: the status screen never talks money.
        let titles = [CloudConnectionStatus.viaCloud, .localNetwork, .remoteAddress, .notLinked, .checking, .unreachable].map(\.title)
        for title in titles {
            XCTAssertFalse(title.contains("$") || title.lowercased().contains("subscri") || title.lowercased().contains("buy"), title)
        }
    }

    // MARK: - Backup URLs and requests

    func testBackupURLBuilding() throws {
        let client = BackupClient(grant: grant(base: "https://cloud.sentvms.com/v1/backup/"))
        let first = try XCTUnwrap(client.listURL())
        XCTAssertEqual(first.absoluteString, "https://cloud.sentvms.com/v1/backup/objects?limit=200")
        let next = try XCTUnwrap(client.listURL(after: "alarms/a1/snapshot.jpg"))
        let items = URLComponents(url: next, resolvingAgainstBaseURL: false)?.queryItems ?? []
        XCTAssertEqual(next.path, "/v1/backup/objects")
        XCTAssertEqual(items.first { $0.name == "after" }?.value, "alarms/a1/snapshot.jpg")
        XCTAssertEqual(items.first { $0.name == "limit" }?.value, "200")

        XCTAssertEqual(client.objectURL(key: "alarms/a1/clip.mp4")?.absoluteString,
                       "https://cloud.sentvms.com/v1/backup/objects/alarms/a1/clip.mp4")
        XCTAssertEqual(client.playbackURL(key: "alarms/a1/clip.mp4")?.absoluteString,
                       "https://cloud.sentvms.com/v1/backup/objects/alarms/a1/clip.mp4?token=\(scb)",
                       "AVPlayer can't set headers: the token rides in the query")
        XCTAssertEqual(client.objectURL(key: "alarms/a b?/clip.mp4")?.absoluteString,
                       "https://cloud.sentvms.com/v1/backup/objects/alarms/a%20b%3F/clip.mp4", "segments escaped, slashes kept")

        let odd = BackupClient(grant: grant(token: "scb_a+b/c="))
        XCTAssertEqual(odd.playbackURL(key: "k").flatMap { URLComponents(url: $0, resolvingAgainstBaseURL: false)?.percentEncodedQuery },
                       "token=scb_a%2Bb%2Fc%3D")

        let bad = BackupClient(grant: grant(base: "ftp://cloud.sentvms.com/v1/backup"))
        XCTAssertNil(bad.listURL())
        XCTAssertNil(bad.objectURL(key: "k"))
        XCTAssertNil(bad.playbackURL(key: "k"))
    }

    func testBackupClientSendsTheTokenAndExplainsExpiry() async throws {
        let client = BackupClient(grant: grant(), session: mockSession())
        MockURLProtocol.reset { req in
            if req.url!.path == "/v1/backup/objects" {
                return .init(body: Data(#"{"objects":[{"key":"alarms/a1/snapshot.jpg","size":3,"uploadedAt":"2026-10-19T20:00:00Z"}],"next":null}"#.utf8))
            }
            return .init(body: Data("jpg".utf8))
        }
        let listing = try await client.list()
        XCTAssertEqual(listing.objects.map(\.key), ["alarms/a1/snapshot.jpg"])
        XCTAssertNil(listing.next)
        let data = try await client.data(key: "alarms/a1/snapshot.jpg")
        XCTAssertEqual(data, Data("jpg".utf8))
        XCTAssertEqual(MockURLProtocol.seen.map { $0.url!.path }, ["/v1/backup/objects", "/v1/backup/objects/alarms/a1/snapshot.jpg"])
        XCTAssertTrue(MockURLProtocol.seen.allSatisfy { $0.value(forHTTPHeaderField: "Authorization") == "Bearer \(scb)" })
        XCTAssertTrue(MockURLProtocol.seen.allSatisfy { $0.url?.query?.contains("token=") != true }, "no token in URLs that can carry a header")

        MockURLProtocol.reset { _ in .init(status: 401, body: Data(#"{"error":"backup token expired"}"#.utf8)) }
        do {
            _ = try await client.list()
            XCTFail("expected an error")
        } catch {
            XCTAssertTrue(BackupClient.message(for: error).contains("expired"), BackupClient.message(for: error))
        }

        MockURLProtocol.reset { _ in .init(error: URLError(.notConnectedToInternet)) }
        do {
            _ = try await client.list()
            XCTFail("expected an error")
        } catch {
            XCTAssertEqual(BackupClient.message(for: error), "This iPhone is offline.")
        }
    }

    // MARK: - Live view through the relay

    /// Live view through the relay is HLS from the gateway (`/v/<token>`):
    /// the broker resolves it against whichever address answered, so on the
    /// secondary address it's the relay.
    func testVideoGatewayResolvesAgainstTheRelay() async throws {
        let addresses = SiteAddresses(primary: lan, secondary: relay)
        let api = SentinelAPI(addresses: addresses, siteID: "s1", tokenStore: signedInStore(), session: mockSession())
        let broker = VideoTokenBroker(api: api)
        func reply(base: String) -> Data {
            Data("""
            {"token":"v1.t","baseUrl":"\(base)","gatewayUrl":"/v/v1.t","expiresAt":\(Date().addingTimeInterval(600).unixMillis),
             "rights":["live"],"cameraId":"c1","recorderId":"rs1"}
            """.utf8)
        }
        // On cellular: the LAN address doesn't answer, the relay does.
        let gatewayOnly = reply(base: "/v/v1.t")
        MockURLProtocol.reset { req in
            if req.url!.host == "10.0.0.1" { return .init(error: URLError(.cannotConnectToHost)) }
            return .init(body: gatewayOnly)
        }
        let endpoint = try await broker.endpoint(cameraID: "c1")
        XCTAssertTrue(addresses.isUsingSecondary)
        XCTAssertTrue(endpoint.isGateway)
        XCTAssertEqual(endpoint.liveHLS.absoluteString, "https://k7f3q9x2ab-cloud.sentvms.com/v/v1.t/live/c1/index.m3u8")

        // A Recording Server with its own public address: off the LAN the
        // gateway on the relay is used straight away, no direct attempt first.
        await broker.invalidateAll()
        let withPublicURL = reply(base: "http://10.0.0.7:8090/v/v1.t")
        MockURLProtocol.reset { req in
            if req.url!.host == "10.0.0.1" { return .init(error: URLError(.cannotConnectToHost)) }
            return .init(body: withPublicURL)
        }
        let viaRelay = try await broker.endpoint(cameraID: "c1")
        XCTAssertTrue(viaRelay.isGateway)
        XCTAssertEqual(viaRelay.liveHLS.absoluteString, "https://k7f3q9x2ab-cloud.sentvms.com/v/v1.t/live/c1/index.m3u8")
    }

    /// On the local network nothing changes: the Recording Server's own
    /// address first, the gateway only after it failed.
    func testVideoOnTheLANStillTriesTheRecordingServerFirst() async throws {
        let addresses = SiteAddresses(primary: lan, secondary: relay)
        let api = SentinelAPI(addresses: addresses, siteID: "s1", tokenStore: signedInStore(), session: mockSession())
        let broker = VideoTokenBroker(api: api)
        let reply = Data("""
        {"token":"v1.t","baseUrl":"http://10.0.0.7:8090/v/v1.t","gatewayUrl":"/v/v1.t","expiresAt":\(Date().addingTimeInterval(600).unixMillis),
         "rights":["live"],"cameraId":"c1","recorderId":"rs1"}
        """.utf8)
        MockURLProtocol.reset { _ in .init(body: reply) }
        let direct = try await broker.endpoint(cameraID: "c1")
        XCTAssertFalse(addresses.isUsingSecondary)
        XCTAssertFalse(direct.isGateway)
        XCTAssertEqual(direct.liveHLS.absoluteString, "http://10.0.0.7:8090/v/v1.t/live/c1/index.m3u8")
        await broker.reportDirectFailure(recorderID: "rs1")
        let gateway = try await broker.endpoint(cameraID: "c1")
        XCTAssertTrue(gateway.isGateway)
        XCTAssertEqual(gateway.base.absoluteString, "http://10.0.0.1:8090/v/v1.t")
    }

    // MARK: - Offline affordance

    func testOfflineBackupOfferRules() {
        func probe(grant: Bool = true, signedIn: Bool = true, connected: Bool = false, idle: Bool = false, failed: Bool = false) -> Bool {
            OfflineBackupOffer.shouldProbe(hasGrant: grant, signedIn: signedIn, streamConnected: connected, streamIdle: idle, loadFailed: failed)
        }
        XCTAssertTrue(probe(), "stream down and a backup token: check the site")
        XCTAssertFalse(probe(grant: false), "nothing to offer without a token")
        XCTAssertFalse(probe(signedIn: false))
        XCTAssertFalse(probe(connected: true))
        XCTAssertFalse(probe(idle: true), "an idle stream (in the background) proves nothing")
        XCTAssertTrue(probe(connected: true, failed: true))

        XCTAssertTrue(OfflineBackupOffer.meansUnreachable(SentinelAPIError.transport(message: "x")))
        XCTAssertTrue(OfflineBackupOffer.meansUnreachable(SentinelAPIError.from(status: 503, body: Data(#"{"error":"server not connected"}"#.utf8))),
                      "the relay answering for a server that isn't connected")
        XCTAssertFalse(OfflineBackupOffer.meansUnreachable(SentinelAPIError.from(status: 404, body: Data())))
        XCTAssertFalse(OfflineBackupOffer.meansUnreachable(SentinelAPIError.signInRequired(message: "x")))
        XCTAssertFalse(OfflineBackupOffer.meansUnreachable(CancellationError()))
    }
}
