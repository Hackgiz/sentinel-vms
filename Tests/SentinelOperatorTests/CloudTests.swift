import XCTest
@testable import SentinelCore

/// Wave 12 Sentinel Cloud: `CloudStatus` decoding (WAVE12-CLOUD.md section 4),
/// `CloudLogic` phases, countdown, rows and formatting, and the API calls.
final class CloudTests: XCTestCase {
    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    /// Section 4's sample verbatim (comments removed), "…" placeholders included.
    static let contractSample = """
    {
      "linked": true,
      "serverId": "k7f3q9x2ab",
      "serverName": "Office Mac",
      "accountEmailMasked": "e***@gmail.com",
      "plan": "cloud", "status": "active", "periodEnd": "2026-11-19T00:00:00Z",
      "features": ["relay","push","backup"],
      "quotas": {"backupBytes": 10737418240, "backupDays": 30, "servers": 1},
      "remote": {"enabled": true, "state": "connected", "url": "https://k7f3q9x2ab-cloud.sentvms.com", "error": ""},
      "push":   {"state": "cloud"},
      "backup": {"enabled": true, "bytesUsed": 123456, "objects": 12, "lastUploadAt": "…", "error": ""},
      "link":   {"userCode": "ABCD-EFGH", "verifyURL": "https://sentvms.com/cloud?code=ABCD-EFGH", "expiresAt": "…"}
    }
    """

    static let linkedSample = """
    {"linked":true,"serverId":"k7f3q9x2ab","serverName":"Office Mac","accountEmailMasked":"e***@gmail.com",
     "plan":"cloud","status":"active","periodEnd":"2026-11-19T00:00:00Z","features":["relay","push","backup"],
     "quotas":{"backupBytes":10737418240,"backupDays":30,"servers":1},
     "remote":{"enabled":true,"state":"connected","url":"https://k7f3q9x2ab-cloud.sentvms.com","error":""},
     "push":{"state":"cloud"},
     "backup":{"enabled":true,"bytesUsed":123456,"objects":12,"lastUploadAt":"2026-10-19T19:55:00.123456789Z","error":""}}
    """

    private func decode(_ json: String) throws -> V2.CloudStatus {
        try SentinelAPI.decode(V2.CloudStatus.self, from: Data(json.utf8))
    }

    private func date(_ text: String) -> Date { V2.CloudTime.parse(text)! }

    private func isoDay(_ d: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withFullDate]
        return f.string(from: d)
    }

    // MARK: Decoding

    func testDecodesTheContractSample() throws {
        let s = try decode(Self.contractSample)
        XCTAssertTrue(s.linked)
        XCTAssertEqual(s.serverId, "k7f3q9x2ab")
        XCTAssertEqual(s.serverName, "Office Mac")
        XCTAssertEqual(s.accountEmailMasked, "e***@gmail.com")
        XCTAssertEqual(s.plan, .cloud)
        XCTAssertEqual(s.status, .active)
        XCTAssertEqual(s.periodEnd, date("2026-11-19T00:00:00Z"))
        XCTAssertEqual(s.periodEnd?.timeIntervalSince1970, 1_795_046_400)
        XCTAssertEqual(s.features, [.relay, .push, .backup])
        XCTAssertTrue(s.has(.backup))
        XCTAssertEqual(s.quotas, V2.CloudQuotas(backupBytes: 10_737_418_240, backupDays: 30, servers: 1))
        XCTAssertEqual(s.remote, V2.CloudRemote(enabled: true, state: .connected, url: "https://k7f3q9x2ab-cloud.sentvms.com", error: ""))
        XCTAssertEqual(s.push.state, .cloud)
        XCTAssertTrue(s.backup.enabled)
        XCTAssertEqual(s.backup.bytesUsed, 123_456)
        XCTAssertEqual(s.backup.objects, 12)
        XCTAssertNil(s.backup.lastUploadAt, "an unparseable time reads as nil instead of failing the response")
        XCTAssertEqual(s.link?.userCode, "ABCD-EFGH")
        XCTAssertEqual(s.link?.verifyURL, "https://sentvms.com/cloud?code=ABCD-EFGH")
        XCTAssertNil(s.link?.expiresAt)
    }

    func testDecodesAnUnlinkedServerWithNullsAndMissingKeys() throws {
        let s = try decode("""
        {"linked":false,"serverId":"","periodEnd":null,"features":null,"quotas":null,
         "remote":{"enabled":true,"state":"off","url":"","error":""},"push":{"state":"direct"},"link":null}
        """)
        XCTAssertFalse(s.linked)
        XCTAssertEqual(s.serverId, "")
        XCTAssertEqual(s.plan, .free)
        XCTAssertEqual(s.status, .inactive)
        XCTAssertNil(s.periodEnd)
        XCTAssertEqual(s.features, [])
        XCTAssertEqual(s.quotas, V2.CloudQuotas())
        XCTAssertEqual(s.remote.state, .off)
        XCTAssertEqual(s.push.state, .direct)
        XCTAssertEqual(s.backup, V2.CloudBackup())
        XCTAssertNil(s.link)
        // The barest answer still decodes.
        XCTAssertEqual(try decode("{}"), V2.CloudStatus())
    }

    func testUnknownEnumStringsAreKept() throws {
        let s = try decode("""
        {"linked":true,"plan":"enterprise","status":"paused","features":["relay","teleport"],
         "remote":{"enabled":true,"state":"warp"},"push":{"state":"pigeon"}}
        """)
        XCTAssertEqual(s.plan.rawValue, "enterprise")
        XCTAssertEqual(s.status.rawValue, "paused")
        XCTAssertEqual(s.features.map(\.rawValue), ["relay", "teleport"])
        XCTAssertEqual(s.remote.state.rawValue, "warp")
        XCTAssertEqual(CloudLogic.planLabel(s.plan), "Enterprise")
        XCTAssertEqual(CloudLogic.statusLabel(s.status), CloudLabel("Paused", .neutral))
        XCTAssertEqual(CloudLogic.remoteLabel(s.remote), CloudLabel("Warp", .neutral))
        XCTAssertEqual(CloudLogic.pushLabel(s.push.state), CloudLabel("Pigeon", .neutral))
    }

    func testTimesAcceptFractionsMillisAndRejectPlaceholders() throws {
        let s = try decode(Self.linkedSample)
        let upload = try XCTUnwrap(s.backup.lastUploadAt)
        XCTAssertEqual(upload.timeIntervalSince1970, date("2026-10-19T19:55:00Z").timeIntervalSince1970 + 0.123456789, accuracy: 0.000_001)
        XCTAssertEqual(V2.CloudTime.parse("2026-10-19T20:00:00.5+02:00")?.timeIntervalSince1970,
                       date("2026-10-19T18:00:00Z").timeIntervalSince1970 + 0.5)
        XCTAssertNil(V2.CloudTime.parse("0001-01-01T00:00:00Z"), "Go's zero time")
        XCTAssertNil(V2.CloudTime.parse(""))
        XCTAssertNil(V2.CloudTime.parse("2026-10-19T20:00:00.Z"))
        XCTAssertNil(V2.CloudTime.parse("yesterday"))
        // Unix milliseconds, should the server follow the v2 convention.
        let ms = try decode(#"{"periodEnd":1795046400000}"#)
        XCTAssertEqual(ms.periodEnd, date("2026-11-19T00:00:00Z"))
        // An empty link object (Go struct without a pointer) is no link.
        let empty = try decode(#"{"link":{"userCode":"","verifyURL":"","expiresAt":"0001-01-01T00:00:00Z"}}"#)
        XCTAssertNil(empty.link)
    }

    func testStatusRoundTripsAndPatchOmitsUnsetSwitches() throws {
        let s = try decode(Self.linkedSample)
        let again = try decode(String(decoding: try SentinelAPI.jsonEncoder.encode(s), as: UTF8.self))
        XCTAssertEqual(again.periodEnd, s.periodEnd)
        XCTAssertEqual(again.remote, s.remote)
        XCTAssertEqual(again.features, s.features)
        let remoteOff = String(decoding: try SentinelAPI.jsonEncoder.encode(V2.CloudSettingsPatch(remote: false)), as: UTF8.self)
        XCTAssertEqual(remoteOff, #"{"remote":false}"#)
        let both = String(decoding: try SentinelAPI.jsonEncoder.encode(V2.CloudSettingsPatch(remote: true, backup: false)), as: UTF8.self)
        XCTAssertEqual(both, #"{"backup":false,"remote":true}"#)
    }

    // MARK: Phase and countdown

    func testPhaseFollowsLinkStateAndExpiry() {
        let now = date("2026-10-19T20:00:00Z")
        let link = V2.CloudLink(userCode: "ABCD-EFGH", verifyURL: "https://sentvms.com/cloud?code=ABCD-EFGH",
                                expiresAt: now.addingTimeInterval(581))
        XCTAssertEqual(CloudLogic.phase(V2.CloudStatus(), now: now, awaitingLink: false), .notLinked)
        XCTAssertEqual(CloudLogic.phase(V2.CloudStatus(link: link), now: now, awaitingLink: false), .linking(link))
        XCTAssertTrue(CloudLogic.shouldPoll(.linking(link)))
        // The code ran out locally, or the server dropped it without linking.
        XCTAssertEqual(CloudLogic.phase(V2.CloudStatus(link: link), now: now.addingTimeInterval(600), awaitingLink: true), .expired)
        XCTAssertEqual(CloudLogic.phase(V2.CloudStatus(), now: now, awaitingLink: true), .expired)
        XCTAssertFalse(CloudLogic.shouldPoll(.expired))
        // Linked wins over a stale code.
        XCTAssertEqual(CloudLogic.phase(V2.CloudStatus(linked: true, link: link), now: now, awaitingLink: true), .linked)
        XCTAssertFalse(CloudLogic.shouldPoll(.linked))
        XCTAssertFalse(CloudLogic.shouldPoll(.notLinked))
        // An unknown expiry is the server's call.
        let open = V2.CloudLink(userCode: "ABCD-EFGH", verifyURL: "", expiresAt: nil)
        XCTAssertEqual(CloudLogic.phase(V2.CloudStatus(link: open), now: now, awaitingLink: true), .linking(open))
    }

    func testLinkCountdown() {
        let now = date("2026-10-19T20:00:00Z")
        let expires = now.addingTimeInterval(600)
        XCTAssertEqual(CloudLogic.remainingSeconds(until: expires, now: now), 600)
        XCTAssertEqual(CloudLogic.countdown(until: expires, now: now), "10:00")
        XCTAssertEqual(CloudLogic.countdown(until: expires, now: now.addingTimeInterval(19)), "9:41")
        XCTAssertEqual(CloudLogic.countdown(until: expires, now: now.addingTimeInterval(599.4)), "0:01", "rounds up")
        XCTAssertEqual(CloudLogic.countdown(until: expires, now: now.addingTimeInterval(600)), "0:00")
        XCTAssertEqual(CloudLogic.remainingSeconds(until: expires, now: now.addingTimeInterval(900)), 0)
        XCTAssertNil(CloudLogic.remainingSeconds(until: nil, now: now))
        XCTAssertEqual(CloudLogic.countdown(until: nil, now: now), "")
        let link = V2.CloudLink(userCode: "ABCD-EFGH", verifyURL: "", expiresAt: expires)
        XCTAssertEqual(CloudLogic.countdownText(link, now: now.addingTimeInterval(19)), "Code expires in 9:41")
        XCTAssertEqual(CloudLogic.countdownText(link, now: expires), "Code expired")
        XCTAssertTrue(CloudLogic.isExpired(link, now: expires))
        XCTAssertFalse(CloudLogic.isExpired(link, now: now))
        XCTAssertEqual(CloudLogic.countdownText(V2.CloudLink(userCode: "X", verifyURL: "", expiresAt: nil), now: now), "Waiting for approval")
    }

    func testVerifyURLOnlyOpensWebAddresses() {
        let good = V2.CloudLink(userCode: "ABCD-EFGH", verifyURL: "https://sentvms.com/cloud?code=ABCD-EFGH", expiresAt: nil)
        XCTAssertEqual(CloudLogic.verifyURL(good).absoluteString, "https://sentvms.com/cloud?code=ABCD-EFGH")
        for bad in ["", "file:///etc/passwd", "sentinel://link", "not a url"] {
            let link = V2.CloudLink(userCode: "ABCD-EFGH", verifyURL: bad, expiresAt: nil)
            XCTAssertEqual(CloudLogic.verifyURL(link).absoluteString, "https://sentvms.com/cloud?code=ABCD-EFGH", bad)
        }
    }

    // MARK: Formatting

    func testBytesAndQuota() {
        XCTAssertEqual(CloudLogic.bytes(0), "0 B")
        XCTAssertEqual(CloudLogic.bytes(-5), "0 B")
        XCTAssertEqual(CloudLogic.bytes(1023), "1023 B")
        XCTAssertEqual(CloudLogic.bytes(1024), "1 KB")
        XCTAssertEqual(CloudLogic.bytes(1536), "1.5 KB")
        XCTAssertEqual(CloudLogic.bytes(123_456), "121 KB")
        XCTAssertEqual(CloudLogic.bytes(1_288_490_189), "1.2 GB")
        XCTAssertEqual(CloudLogic.bytes(10_737_418_240), "10 GB")
        XCTAssertEqual(CloudLogic.bytes(107_374_182_400), "100 GB")
        XCTAssertEqual(CloudLogic.usageText(used: 1_288_490_189, quota: 10_737_418_240, objects: 12), "1.2 GB of 10 GB · 12 files")
        XCTAssertEqual(CloudLogic.usageText(used: 1024, quota: 0, objects: 1), "1 KB used · 1 file")
        XCTAssertEqual(CloudLogic.usageFraction(used: 5_368_709_120, quota: 10_737_418_240), 0.5)
        XCTAssertEqual(CloudLogic.usageFraction(used: 20, quota: 10), 1)
        XCTAssertEqual(CloudLogic.usageFraction(used: 20, quota: 0), 0)
        XCTAssertEqual(CloudLogic.usageTone(used: 1, quota: 10), .good)
        XCTAssertEqual(CloudLogic.usageTone(used: 9, quota: 10), .warning)
        XCTAssertEqual(CloudLogic.usageTone(used: 10, quota: 10), .bad)
        XCTAssertEqual(CloudLogic.usageTone(used: 10, quota: 0), .neutral)
        XCTAssertEqual(CloudLogic.retentionText(days: 30), "30 days")
        XCTAssertEqual(CloudLogic.retentionText(days: 1), "1 day")
        XCTAssertNil(CloudLogic.retentionText(days: 0))
    }

    func testLastUploadText() {
        let now = date("2026-10-19T20:00:00Z")
        XCTAssertEqual(CloudLogic.lastUploadText(nil, now: now), "Nothing uploaded yet")
        XCTAssertEqual(CloudLogic.lastUploadText(now.addingTimeInterval(-20), now: now), "Just now")
        XCTAssertEqual(CloudLogic.lastUploadText(now.addingTimeInterval(-300), now: now), "5 min ago")
        XCTAssertEqual(CloudLogic.lastUploadText(now.addingTimeInterval(-3 * 3600 - 5), now: now), "3 h ago")
        XCTAssertEqual(CloudLogic.lastUploadText(now.addingTimeInterval(-3 * 86_400), now: now, format: isoDay), "2026-10-16")
    }

    func testStateLabels() {
        XCTAssertEqual(CloudLogic.planLabel(.free), "Free")
        XCTAssertEqual(CloudLogic.planLabel(.cloud), "Sentinel Cloud")
        XCTAssertEqual(CloudLogic.planLabel(.business), "Sentinel Cloud Business")
        XCTAssertEqual(CloudLogic.statusLabel(.active), CloudLabel("Active", .good))
        XCTAssertEqual(CloudLogic.statusLabel(.grace), CloudLabel("Payment past due", .warning))
        XCTAssertEqual(CloudLogic.statusLabel(.canceled).tone, .bad)
        XCTAssertEqual(CloudLogic.statusLabel(.inactive).tone, .bad)
        XCTAssertEqual(CloudLogic.remoteLabel(V2.CloudRemote(enabled: true, state: .connected)), CloudLabel("Connected", .good))
        XCTAssertEqual(CloudLogic.remoteLabel(V2.CloudRemote(enabled: true, state: .connecting)).tone, .working)
        XCTAssertEqual(CloudLogic.remoteLabel(V2.CloudRemote(enabled: true, state: .retrying)).tone, .warning)
        XCTAssertEqual(CloudLogic.remoteLabel(V2.CloudRemote(enabled: true, state: .inactive)).tone, .bad)
        // Switched off by the owner reads "Off" whatever the state says.
        XCTAssertEqual(CloudLogic.remoteLabel(V2.CloudRemote(enabled: false, state: .connected)), CloudLabel("Off", .neutral))
        XCTAssertEqual(CloudLogic.pushLabel(.cloud), CloudLabel("Through Sentinel Cloud", .good))
        XCTAssertEqual(CloudLogic.pushLabel(.direct).tone, .good)
        XCTAssertEqual(CloudLogic.pushLabel(.off), CloudLabel("Off", .neutral))
    }

    func testPeriodTextAndFeatureNotes() throws {
        var s = try decode(Self.linkedSample)
        XCTAssertEqual(CloudLogic.periodText(s, format: isoDay), "Current period ends 2026-11-19")
        s.status = .grace
        XCTAssertEqual(CloudLogic.periodText(s, format: isoDay), "Features stay on until 2026-11-26; update your payment to keep them")
        s.status = .canceled
        XCTAssertEqual(CloudLogic.periodText(s, format: isoDay), "Ended 2026-11-19")
        s.periodEnd = nil
        XCTAssertNil(CloudLogic.periodText(s))

        s = try decode(Self.linkedSample)
        XCTAssertNil(CloudLogic.featureNote(s, .relay))
        s.features = [.push]
        XCTAssertEqual(CloudLogic.featureNote(s, .relay), "Not included in your plan.")
        s.status = .canceled
        s.features = []
        XCTAssertEqual(CloudLogic.featureNote(s, .backup), "Paused until your Sentinel Cloud subscription is active again.")
        s.linked = false
        XCTAssertNil(CloudLogic.featureNote(s, .backup))
    }

    // MARK: Rows

    func testRowsForALinkedServer() throws {
        let s = try decode(Self.linkedSample)
        let now = date("2026-10-19T20:00:00Z")
        let rows = CloudLogic.rows(s, now: now, format: isoDay)
        XCTAssertEqual(rows.map(\.id), ["plan", "status", "period", "account", "server",
                                        "remoteState", "remoteAddress", "push",
                                        "backupUsage", "backupRetention", "backupLastUpload"])
        func row(_ id: String) -> CloudRow? { rows.first { $0.id == id } }
        XCTAssertEqual(row("plan")?.value, "Sentinel Cloud")
        XCTAssertEqual(row("status")?.tone, .good)
        XCTAssertEqual(row("period")?.value, "Current period ends 2026-11-19")
        XCTAssertEqual(row("account")?.value, "e***@gmail.com")
        XCTAssertEqual(row("server")?.value, "Office Mac (k7f3q9x2ab)")
        XCTAssertEqual(row("remoteState")?.value, "Connected")
        XCTAssertEqual(row("remoteAddress")?.value, "https://k7f3q9x2ab-cloud.sentvms.com")
        XCTAssertEqual(row("remoteAddress")?.copyValue, "https://k7f3q9x2ab-cloud.sentvms.com")
        XCTAssertEqual(row("push")?.value, "Through Sentinel Cloud")
        XCTAssertEqual(row("backupUsage")?.value, "121 KB of 10 GB · 12 files")
        XCTAssertEqual(row("backupUsage")?.tone, .good)
        XCTAssertEqual(row("backupRetention")?.value, "30 days")
        XCTAssertEqual(row("backupLastUpload")?.value, "4 min ago")
        XCTAssertEqual(Set(rows.filter { $0.section == .subscription }.map(\.id)), ["plan", "status", "period", "account", "server"])
        XCTAssertEqual(rows.filter { $0.copyValue != nil }.map(\.id), ["remoteAddress"])
    }

    func testRowsShowProblemsAndAMissingAddress() throws {
        var s = try decode(Self.linkedSample)
        s.remote = V2.CloudRemote(enabled: true, state: .retrying, url: "", error: "relay refused the connection")
        s.backup.error = "Backup is full"
        s.backup.bytesUsed = s.quotas.backupBytes
        s.accountEmailMasked = ""
        s.serverName = ""
        let rows = CloudLogic.rows(s, now: Date())
        func row(_ id: String) -> CloudRow? { rows.first { $0.id == id } }
        XCTAssertNil(row("account"))
        XCTAssertEqual(row("server")?.value, "k7f3q9x2ab")
        XCTAssertEqual(row("remoteState")?.tone, .warning)
        XCTAssertEqual(row("remoteAddress")?.value, "Not assigned yet")
        XCTAssertNil(row("remoteAddress")?.copyValue)
        XCTAssertEqual(row("remoteError")?.value, "relay refused the connection")
        XCTAssertEqual(row("remoteError")?.tone, .bad)
        XCTAssertEqual(row("backupError")?.value, "Backup is full")
        XCTAssertEqual(row("backupUsage")?.tone, .bad)
    }

    func testUserFacingCopyKeepsTheContractWords() throws {
        let s = try decode(Self.linkedSample)
        var texts = [CloudLogic.intro, CloudLogic.linkSteps, CloudLogic.unlinkWarning] + CloudLogic.benefits
        texts += CloudLogic.rows(s, now: Date()).flatMap { [$0.label, $0.value] }
        for text in texts {
            XCTAssertFalse(text.lowercased().contains("tunnel"), text)
        }
        XCTAssertEqual(CloudLogic.manageSubscriptionURL.absoluteString, "https://sentvms.com/cloud")
        XCTAssertEqual(CloudLogic.linkPollSeconds, 5)
        XCTAssertEqual(CloudLogic.statusRefreshSeconds, 60)
        XCTAssertTrue(CloudLogic.errorText(SentinelAPIError.server(status: 404, code: "not_found", message: "Not found."))
            .contains("doesn't offer Sentinel Cloud"))
        XCTAssertEqual(CloudLogic.errorText(SentinelAPIError.server(status: 502, code: "internal", message: "Sentinel Cloud is unreachable")),
                       "Sentinel Cloud is unreachable")
    }

    func testCloudLeafIsGatedOnChangeSettings() {
        XCTAssertEqual(ManagementCatalog.Leaf.cloud, "basics/cloud")
        let admin = ManagementNavModel(can: { [.changeSettings, .viewAudit].contains($0) })
        XCTAssertEqual(admin.node(ManagementCatalog.Leaf.cloud)?.title, "Sentinel Cloud")
        XCTAssertEqual(admin.breadcrumb(for: ManagementCatalog.Leaf.cloud, siteName: "HQ"), ["Basics", "Sentinel Cloud"])
        XCTAssertEqual(admin.resolve(deepLink: "#/basics/cloud"), ManagementCatalog.Leaf.cloud)
        let supervisor = ManagementNavModel(can: { $0 == .viewAudit })
        XCTAssertNil(supervisor.node(ManagementCatalog.Leaf.cloud))
        XCTAssertEqual(supervisor.resolve(deepLink: "#/basics/cloud"), ManagementCatalog.rootID)
    }

    // MARK: API

    func testCloudCallsUseTheSection4Routes() async throws {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://nvr.test:8090")!, siteID: "s1", tokenStore: store,
                              session: MockURLProtocol.session(), clientLabel: "Test")
        let pending = """
        {"linked":false,"serverId":"","link":{"userCode":"ABCD-EFGH","verifyURL":"https://sentvms.com/cloud?code=ABCD-EFGH",
         "expiresAt":"2026-10-19T20:10:00Z"}}
        """
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer access-1")
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("GET", "/api/v2/cloud"), ("PUT", "/api/v2/cloud"): return .json(200, Self.linkedSample)
            case ("POST", "/api/v2/cloud/link"): return .json(200, pending)
            case ("DELETE", "/api/v2/cloud/link"): return .json(200, #"{"linked":false,"serverId":""}"#)
            default: return .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#)
            }
        }

        let status = try await api.cloudStatus()
        XCTAssertTrue(status.linked)
        XCTAssertEqual(status.remote.url, "https://k7f3q9x2ab-cloud.sentvms.com")

        let linking = try await api.startCloudLink()
        XCTAssertEqual(linking.link?.userCode, "ABCD-EFGH")
        XCTAssertEqual(linking.link?.expiresAt, date("2026-10-19T20:10:00Z"))

        _ = try await api.updateCloud(V2.CloudSettingsPatch(backup: false))

        let unlinked = try await api.unlinkCloud()
        XCTAssertFalse(unlinked.linked)

        let log = MockURLProtocol.requests
        XCTAssertEqual(log.map { "\($0.request.httpMethod ?? "") \($0.request.url?.path ?? "")" },
                       ["GET /api/v2/cloud", "POST /api/v2/cloud/link", "PUT /api/v2/cloud", "DELETE /api/v2/cloud/link"])
        XCTAssertEqual(log[2].request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(log[2].body.map { String(decoding: $0, as: UTF8.self) }, #"{"backup":false}"#)
        XCTAssertNil(log[1].body.flatMap { $0.isEmpty ? nil : $0 }, "the link POST has no body")
    }
}

// MARK: - Wave 13: one-click Sentinel Cloud (WAVE13-CLOUD-ONBOARDING.md, stream D)

/// `CloudStatus.signIn`, `CloudAccount` (Supabase and billing requests,
/// answers and errors as one sentence, link errors, status lines, plans),
/// the `CloudCard` state machine, `CloudAccountClient` and the two new
/// server calls. Every request goes to the stub protocol; `.test` hosts only.
extension CloudTests {
    static let signInJSON = """
    {"supabaseUrl":"https://project.supabase.test","supabaseAnonKey":"sb_publishable_test",
     "siteBase":"https://sentvms.com",
     "checkoutURL":"https://project.supabase.test/functions/v1/sentinel-create-checkout",
     "portalURL":"https://project.supabase.test/functions/v1/sentinel-portal",
     "plans":[{"id":"cloud","name":"Sentinel Cloud","month":4.99,"year":49,"servers":1,"backupGB":10,"days":30},
              {"id":"business","name":"Sentinel Cloud Business","month":19.99,"year":199,"servers":5,"backupGB":100,"days":90}]}
    """

    static var config: V2.CloudSignIn {
        try! SentinelAPI.decode(V2.CloudSignIn.self, from: Data(signInJSON.utf8))
    }

    static var unlinkedSample: String { #"{"linked":false,"serverId":"","signIn":\#(signInJSON)}"# }

    private func body(_ request: URLRequest) -> String {
        request.httpBody.map { String(decoding: $0, as: UTF8.self) } ?? ""
    }

    private func accountError(_ call: () throws -> Any) -> CloudAccountError? {
        do {
            _ = try call()
            return nil
        } catch {
            return error as? CloudAccountError
        }
    }

    // MARK: signIn decoding

    func testDecodesTheSignInConfig() throws {
        let s = try decode(Self.unlinkedSample)
        let signIn = try XCTUnwrap(s.signIn)
        XCTAssertEqual(signIn.supabaseUrl, "https://project.supabase.test")
        XCTAssertEqual(signIn.supabaseAnonKey, "sb_publishable_test")
        XCTAssertEqual(signIn.siteBase, "https://sentvms.com")
        XCTAssertEqual(signIn.checkoutURL, "https://project.supabase.test/functions/v1/sentinel-create-checkout")
        XCTAssertEqual(signIn.portalURL, "https://project.supabase.test/functions/v1/sentinel-portal")
        XCTAssertEqual(signIn.plans, [
            V2.CloudPlanOffer(id: .cloud, name: "Sentinel Cloud", month: 4.99, year: 49, servers: 1, backupGB: 10, days: 30),
            V2.CloudPlanOffer(id: .business, name: "Sentinel Cloud Business", month: 19.99, year: 199, servers: 5, backupGB: 100, days: 90)
        ])
        XCTAssertEqual(signIn.offer(.business)?.year, 199)
        XCTAssertNil(signIn.offer(.free))
        XCTAssertTrue(CloudAccount.canSignIn(signIn))
        // Round trip keeps it.
        let again = try decode(String(decoding: try SentinelAPI.jsonEncoder.encode(s), as: UTF8.self))
        XCTAssertEqual(again.signIn, signIn)
    }

    func testSignInIsTolerant() throws {
        // Unreachable control plane, an older server, junk: never fails the status.
        XCTAssertNil(try decode(#"{"linked":true,"signIn":null}"#).signIn)
        XCTAssertNil(try decode(#"{"linked":true}"#).signIn)
        let junk = try decode(#"{"linked":true,"plan":"cloud","signIn":"soon"}"#)
        XCTAssertNil(junk.signIn)
        XCTAssertEqual(junk.plan, .cloud)
        // Partial objects keep what they have; bad plan entries are dropped.
        let partial = try decode("""
        {"signIn":{"supabaseUrl":" https://p.supabase.test ","supabaseAnonKey":7,
          "plans":[{"id":"cloud","month":"4.99","year":49,"backupGB":10.4},{"name":"no id"}]}}
        """)
        let signIn = try XCTUnwrap(partial.signIn)
        XCTAssertEqual(signIn.supabaseUrl, "https://p.supabase.test")
        XCTAssertEqual(signIn.supabaseAnonKey, "")
        XCTAssertFalse(CloudAccount.canSignIn(signIn), "no key, no sign-in")
        XCTAssertEqual(signIn.plans, [V2.CloudPlanOffer(id: .cloud, name: "", month: 4.99, year: 49, servers: 0, backupGB: 10, days: 0)])
        let plansNotAList = try decode(#"{"signIn":{"supabaseUrl":"https://p.supabase.test","supabaseAnonKey":"k","plans":{}}}"#)
        XCTAssertEqual(plansNotAList.signIn?.plans, [])
        XCTAssertTrue(CloudAccount.canSignIn(plansNotAList.signIn))
    }

    // MARK: Supabase requests

    func testSignInAndSignUpRequests() throws {
        let signIn = try CloudAccount.authRequest(Self.config, email: "  ej@example.com \n", password: "pa ss/\"word", createAccount: false)
        XCTAssertEqual(signIn.url?.absoluteString, "https://project.supabase.test/auth/v1/token?grant_type=password")
        XCTAssertEqual(signIn.httpMethod, "POST")
        XCTAssertEqual(signIn.value(forHTTPHeaderField: "apikey"), "sb_publishable_test")
        XCTAssertEqual(signIn.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertNil(signIn.value(forHTTPHeaderField: "Authorization"), "the publishable key is not a bearer token")
        XCTAssertEqual(body(signIn), #"{"email":"ej@example.com","password":"pa ss/\"word"}"#)

        var trailing = Self.config
        trailing.supabaseUrl = "https://project.supabase.test/"
        let signUp = try CloudAccount.authRequest(trailing, email: "ej@example.com", password: "pw", createAccount: true)
        XCTAssertEqual(signUp.url?.absoluteString, "https://project.supabase.test/auth/v1/signup?redirect_to=https%3A%2F%2Fsentvms%2Ecom%2Fcloud")
        XCTAssertEqual(body(signUp), #"{"email":"ej@example.com","password":"pw"}"#)
        XCTAssertEqual(signUp.value(forHTTPHeaderField: "apikey"), "sb_publishable_test")
    }

    func testNothingIsSentToAnAddressThatIsNotHTTPS() {
        for address in ["http://project.supabase.test", "", "ftp://project.supabase.test", "project.supabase.test", "https://"] {
            var config = Self.config
            config.supabaseUrl = address
            XCTAssertFalse(CloudAccount.canSignIn(config), address)
            let error = accountError { try CloudAccount.authRequest(config, email: "a@b.c", password: "pw", createAccount: false) }
            XCTAssertEqual(error?.kind, .insecure, address)
        }
        var noKey = Self.config
        noKey.supabaseAnonKey = ""
        XCTAssertEqual(accountError { try CloudAccount.authRequest(noKey, email: "a@b.c", password: "pw", createAccount: true) }?.kind, .insecure)
        var badCheckout = Self.config
        badCheckout.checkoutURL = "http://project.supabase.test/functions/v1/sentinel-create-checkout"
        XCTAssertEqual(accountError { try CloudAccount.checkoutRequest(badCheckout, accessToken: "jwt", plan: .cloud, interval: .month) }?.kind, .insecure)
        var badPortal = Self.config
        badPortal.portalURL = ""
        XCTAssertEqual(accountError { try CloudAccount.portalRequest(badPortal, accessToken: "jwt") }?.kind, .insecure)
    }

    func testAuthAnswers() throws {
        let session = Data(#"{"access_token":"jwt-1","token_type":"bearer","expires_in":3600,"refresh_token":"r","user":{"id":"u"}}"#.utf8)
        XCTAssertEqual(try CloudAccount.authOutcome(status: 200, body: session, createAccount: false), .session(accessToken: "jwt-1"))
        // Sign-up on an auto-confirming project answers a session too.
        XCTAssertEqual(try CloudAccount.authOutcome(status: 200, body: session, createAccount: true), .session(accessToken: "jwt-1"))
        let wrapped = Data(#"{"user":{"id":"u"},"session":{"access_token":"jwt-2"}}"#.utf8)
        XCTAssertEqual(try CloudAccount.authOutcome(status: 200, body: wrapped, createAccount: true), .session(accessToken: "jwt-2"))
        // Needs confirming: {user, session: null}, or GoTrue's bare user.
        let pending = Data(#"{"user":{"id":"u","email":"ej@example.com"},"session":null}"#.utf8)
        XCTAssertEqual(try CloudAccount.authOutcome(status: 200, body: pending, createAccount: true), .confirmEmail)
        let bare = Data(#"{"id":"u","email":"ej@example.com","confirmation_sent_at":"2026-10-19T20:00:00Z"}"#.utf8)
        XCTAssertEqual(try CloudAccount.authOutcome(status: 200, body: bare, createAccount: true), .confirmEmail)
        // A sign-in without a session is a failure, not a confirmation.
        XCTAssertEqual(accountError { try CloudAccount.authOutcome(status: 200, body: bare, createAccount: false) }?.kind, .failed)
    }

    func testAuthErrorsAreOneSentence() {
        func text(_ status: Int, _ json: String) -> String { CloudAccount.authErrorText(status: status, body: Data(json.utf8)) }
        XCTAssertEqual(text(400, #"{"code":"invalid_credentials","error_code":"invalid_credentials","msg":"Invalid login credentials"}"#),
                       "The email or password is wrong.")
        XCTAssertEqual(text(400, #"{"error":"invalid_grant","error_description":"Invalid login credentials"}"#),
                       "The email or password is wrong.")
        XCTAssertEqual(text(400, #"{"code":"email_not_confirmed","msg":"Email not confirmed"}"#),
                       "Confirm your email first (open the link Sentinel Cloud sent you), then sign in.")
        XCTAssertEqual(text(422, #"{"code":"user_already_exists","msg":"User already registered"}"#),
                       "There is already an account with this email; switch off Create account and sign in.")
        XCTAssertEqual(text(422, #"{"code":"weak_password","msg":"Password should be at least 6 characters.","weak_password":{"reasons":["length"]}}"#),
                       "Password should be at least 6 characters.")
        XCTAssertEqual(text(429, #"{"code":"over_request_rate_limit","msg":"Request rate limit reached"}"#),
                       "Too many attempts; wait a minute and try again.")
        XCTAssertEqual(text(400, #"{"message":"signups not allowed\n for   this instance. Ask the owner."}"#),
                       "Signups not allowed for this instance.")
        XCTAssertEqual(text(503, "<html>Bad gateway</html>"), "Sentinel Cloud's sign-in isn't answering right now; try again in a minute.")
        XCTAssertEqual(text(418, "{}"), "Sentinel Cloud's sign-in failed (HTTP 418).")
        // Thrown as one error with the right kind.
        let rejected = accountError { try CloudAccount.authOutcome(status: 400, body: Data(#"{"msg":"Invalid login credentials"}"#.utf8), createAccount: false) }
        XCTAssertEqual(rejected, CloudAccountError(.rejected, "The email or password is wrong."))
        XCTAssertEqual(accountError { try CloudAccount.authOutcome(status: 500, body: Data(), createAccount: false) }?.kind, .failed)
    }

    func testSentence() {
        XCTAssertEqual(CloudAccount.sentence("invalid session — sign in again"), "Invalid session — sign in again.")
        XCTAssertEqual(CloudAccount.sentence("  First one.  Second One. "), "First one.")
        XCTAssertEqual(CloudAccount.sentence("Done!"), "Done!")
        XCTAssertEqual(CloudAccount.sentence("version 1.2 is out"), "Version 1.2 is out.")
        XCTAssertEqual(CloudAccount.sentence(" \n "), "")
    }

    // MARK: Billing requests

    func testCheckoutAndPortalRequests() throws {
        let checkout = try CloudAccount.checkoutRequest(Self.config, accessToken: "jwt-1", plan: .business, interval: .year)
        XCTAssertEqual(checkout.url?.absoluteString, "https://project.supabase.test/functions/v1/sentinel-create-checkout")
        XCTAssertEqual(checkout.httpMethod, "POST")
        XCTAssertEqual(checkout.value(forHTTPHeaderField: "Authorization"), "Bearer jwt-1")
        XCTAssertEqual(checkout.value(forHTTPHeaderField: "apikey"), "sb_publishable_test")
        XCTAssertEqual(checkout.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(body(checkout),
                       #"{"cancel_url":"https://sentvms.com/cloud","interval":"year","plan":"business","success_url":"https://sentvms.com/cloud?checkout=success"}"#)
        XCTAssertEqual(CloudAccount.checkoutSuccessURL, "https://sentvms.com/cloud?checkout=success")

        let portal = try CloudAccount.portalRequest(Self.config, accessToken: "jwt-1")
        XCTAssertEqual(portal.url?.absoluteString, "https://project.supabase.test/functions/v1/sentinel-portal")
        XCTAssertEqual(portal.value(forHTTPHeaderField: "Authorization"), "Bearer jwt-1")
        XCTAssertEqual(portal.value(forHTTPHeaderField: "apikey"), "sb_publishable_test")
        XCTAssertEqual(body(portal), #"{"return_url":"https://sentvms.com/cloud"}"#)
    }

    func testBillingAnswersOnlyOpenHTTPSPages() throws {
        func url(_ status: Int, _ json: String) throws -> URL { try CloudAccount.redirectURL(status: status, body: Data(json.utf8)) }
        XCTAssertEqual(try url(200, #"{"url":"https://checkout.stripe.com/c/pay/cs_test_1"}"#).absoluteString,
                       "https://checkout.stripe.com/c/pay/cs_test_1")
        for bad in [#"{"url":"http://checkout.stripe.test/x"}"#, #"{"url":"file:///etc/passwd"}"#, #"{"url":"javascript:alert(1)"}"#, "{}", "[]"] {
            XCTAssertEqual(accountError { try url(200, bad) }?.kind, .failed, bad)
        }
        XCTAssertEqual(accountError { try url(401, #"{"error":"Invalid session — sign in again"}"#) },
                       CloudAccountError(.sessionExpired, "Your Sentinel Cloud sign-in expired; enter your password again."))
        XCTAssertEqual(accountError { try url(409, #"{"error":"You already subscribe to Sentinel Cloud. Use Manage subscription to change your plan.","manage":true}"#) },
                       CloudAccountError(.failed, "You already subscribe to Sentinel Cloud."))
        XCTAssertEqual(accountError { try url(404, #"{"error":"No subscription found for this account"}"#) }?.message,
                       "No subscription found for this account.")
        XCTAssertEqual(accountError { try url(502, "") }?.message, "Sentinel Cloud's billing isn't answering right now; try again in a minute.")
    }

    // MARK: Server link errors

    func testLinkErrorsMapToOneSentence() {
        func server(_ status: Int, _ code: String, _ message: String = "x") -> SentinelAPIError {
            .server(status: status, code: code, message: message)
        }
        XCTAssertEqual(CloudAccount.linkFailure(server(401, "cloud_sign_in")), .signIn)
        XCTAssertEqual(CloudAccount.linkFailure(server(402, "cloud_server_limit")), .serverLimit)
        XCTAssertEqual(CloudAccount.linkFailure(server(409, "cloud_already_linked")), .alreadyLinked)
        XCTAssertEqual(CloudAccount.linkFailure(server(502, "cloud_unreachable")), .unreachable)
        // A body that isn't the v2 shape still maps by status.
        XCTAssertEqual(CloudAccount.linkFailure(server(401, "unauthorized")), .signIn)
        XCTAssertEqual(CloudAccount.linkFailure(server(402, "internal")), .serverLimit)
        XCTAssertEqual(CloudAccount.linkFailure(server(409, "conflict")), .alreadyLinked)
        XCTAssertEqual(CloudAccount.linkFailure(server(502, "internal")), .unreachable)
        XCTAssertEqual(CloudAccount.linkFailure(server(500, "internal")), .other)
        XCTAssertEqual(CloudAccount.linkFailure(SentinelAPIError.transport(message: "offline")), .other)

        XCTAssertEqual(CloudAccount.linkErrorText(server(401, "cloud_sign_in")),
                       "Sentinel Cloud didn't accept the sign-in; enter your password again.")
        XCTAssertEqual(CloudAccount.linkErrorText(server(402, "cloud_server_limit")),
                       "Your Sentinel Cloud plan has no room for another server; upgrade it, or unlink a server, on sentvms.com.")
        XCTAssertEqual(CloudAccount.linkErrorText(server(409, "cloud_already_linked")), "This server is already linked to Sentinel Cloud.")
        XCTAssertEqual(CloudAccount.linkErrorText(server(502, "cloud_unreachable")),
                       "This server can't reach Sentinel Cloud right now; try again in a minute.")
        XCTAssertEqual(CloudAccount.linkErrorText(server(500, "internal", "the database is locked")), "The database is locked.")
        XCTAssertTrue(CloudAccount.linkErrorText(server(404, "not_found")).contains("doesn't offer Sentinel Cloud"))
        XCTAssertEqual(CloudAccount.errorText(CloudAccountError(.network, "This Mac is offline; connect to the internet and try again.")),
                       "This Mac is offline; connect to the internet and try again.")
    }

    // MARK: Linked card

    func testStatusLines() throws {
        var s = try decode(Self.linkedSample)
        XCTAssertEqual(CloudAccount.statusLines(s), [
            CloudStatusLine(id: "remote", label: "Remote address", value: "connected · https://k7f3q9x2ab-cloud.sentvms.com",
                            tone: .good, copyValue: "https://k7f3q9x2ab-cloud.sentvms.com"),
            CloudStatusLine(id: "push", label: "Push notifications", value: "through Sentinel Cloud", tone: .good),
            CloudStatusLine(id: "backup", label: "Offsite backup", value: "on · 121 KB of 10 GB", tone: .good)
        ])
        s.remote.state = .connecting
        XCTAssertEqual(CloudAccount.remoteLine(s).value, "connecting…")
        XCTAssertNil(CloudAccount.remoteLine(s).copyValue)
        s.remote.state = .retrying
        XCTAssertEqual(CloudAccount.remoteLine(s).value, "connecting…")
        s.remote.state = .inactive
        XCTAssertEqual(CloudAccount.remoteLine(s).value, "off")
        s.remote = V2.CloudRemote(enabled: false, state: .connected, url: "https://k7f3q9x2ab-cloud.sentvms.com")
        XCTAssertEqual(CloudAccount.remoteLine(s).value, "off", "switched off by the owner")
        s.push.state = .direct
        XCTAssertEqual(CloudAccount.pushLine(s).value, "your own APNs key")
        s.push.state = .off
        XCTAssertEqual(CloudAccount.pushLine(s).value, "off")
        s.backup.bytesUsed = s.quotas.backupBytes
        XCTAssertEqual(CloudAccount.backupLine(s).tone, .bad)
        s.features = [.relay, .push]
        XCTAssertEqual(CloudAccount.backupLine(s).value, "off", "not in the plan")
        s.features = [.backup]
        s.backup.enabled = false
        XCTAssertEqual(CloudAccount.backupLine(s).value, "off")
        s.backup.enabled = true
        s.quotas.backupBytes = 0
        s.backup.bytesUsed = 1_288_490_189
        XCTAssertEqual(CloudAccount.backupLine(s).value, "on · 1.2 GB used")
    }

    func testButtonsFollowThePlan() throws {
        var s = try decode(Self.linkedSample)
        XCTAssertEqual(CloudAccount.offer(for: s), .manage)
        XCTAssertEqual(CloudAccount.planSummary(s), "Sentinel Cloud · Active")
        s.status = .grace
        XCTAssertEqual(CloudAccount.offer(for: s), .manage)
        XCTAssertEqual(CloudAccount.planSummary(s), "Sentinel Cloud · Payment past due")
        s.status = .canceled
        XCTAssertEqual(CloudAccount.offer(for: s), .subscribeAgain)
        s.status = .inactive
        XCTAssertEqual(CloudAccount.offer(for: s), .subscribe)
        s.plan = .free
        s.status = .active
        XCTAssertEqual(CloudAccount.offer(for: s), .subscribe, "free is never managed")
        XCTAssertEqual(CloudAccount.planSummary(s), "Free")
    }

    func testPlansForSale() {
        let plans = Self.config.plans
        XCTAssertEqual(CloudAccount.price(4.99), "$4.99")
        XCTAssertEqual(CloudAccount.price(49), "$49")
        XCTAssertEqual(CloudAccount.price(19.99), "$19.99")
        XCTAssertEqual(CloudAccount.price(199.0), "$199")
        XCTAssertEqual(plans.map(CloudAccount.shortName), ["Sentinel Cloud", "Business"])
        XCTAssertEqual(plans.map(CloudAccount.offerLabel), ["Sentinel Cloud  $4.99/mo · $49/yr", "Business  $19.99/mo · $199/yr"])
        XCTAssertEqual(plans.map(CloudAccount.offerDetail), ["1 server · 10 GB backup · 30 days", "5 servers · 100 GB backup · 90 days"])
        XCTAssertEqual(CloudAccount.shortName(V2.CloudPlanOffer(id: .business, name: "", month: 0, year: 0)), "Sentinel Cloud Business")
        XCTAssertEqual(CloudAccount.subscribeTitle(plans[0], interval: .month, again: false), "Subscribe · $4.99/month")
        XCTAssertEqual(CloudAccount.subscribeTitle(plans[1], interval: .year, again: true), "Subscribe Again · $199/year")
        XCTAssertEqual(CloudAccount.subscribeTitle(nil, interval: .year, again: false), "Subscribe")
        XCTAssertEqual(CloudBillingInterval.allCases.map(\.label), ["Monthly", "Yearly"])
    }

    // MARK: Card state machine

    func testCardTurnsOnWithOneSignIn() throws {
        var card = CloudCard()
        XCTAssertEqual(card.phase, .loading)
        XCTAssertFalse(card.showsForm)

        card.loaded(try decode(Self.unlinkedSample))
        XCTAssertEqual(card.phase, .notLinked)
        XCTAssertTrue(card.canSignIn)
        XCTAssertTrue(card.showsForm)
        XCTAssertNil(card.offer)
        XCTAssertEqual(card.submitTitle, "Turn On Sentinel Cloud")
        XCTAssertFalse(card.canSubmit(password: "pw"), "no email yet")
        card.email = " ej@example.com "
        XCTAssertFalse(card.canSubmit(password: ""))
        XCTAssertTrue(card.canSubmit(password: "pw"))

        card.startSignIn()
        XCTAssertEqual(card.phase, .linking(.signingIn))
        XCTAssertEqual(card.linkingText, "Signing in to Sentinel Cloud…")
        XCTAssertFalse(card.showsForm)
        // A background status read doesn't interrupt the sign-in.
        card.loaded(try decode(Self.unlinkedSample))
        XCTAssertEqual(card.phase, .linking(.signingIn))

        card.signedIn()
        XCTAssertEqual(card.phase, .linking(.linking))
        XCTAssertEqual(card.linkingText, "Linking this server…")

        var linked = try decode(Self.linkedSample)
        linked.plan = .free
        linked.status = .inactive
        card.linked(linked)
        XCTAssertEqual(card.phase, .linked)
        XCTAssertEqual(card.offer, .subscribe)
        XCTAssertFalse(card.showsForm)
        XCTAssertNil(card.error)
    }

    func testCardSignUpThatNeedsConfirmingAndFailures() throws {
        var card = CloudCard()
        card.loaded(try decode(Self.unlinkedSample))
        card.email = "new@example.com"
        card.createAccount = true
        card.startSignIn()
        XCTAssertEqual(card.linkingText, "Creating your account…")
        card.needsEmailConfirmation()
        XCTAssertEqual(card.phase, .notLinked)
        XCTAssertTrue(card.awaitingConfirmation)
        XCTAssertFalse(card.createAccount, "the next press signs in")
        XCTAssertEqual(card.note, "Check your email, then sign in.")
        XCTAssertEqual(card.submitTitle, "I Confirmed It, Sign Me In")

        card.startSignIn()
        XCTAssertNil(card.note)
        card.signInFailed("Confirm your email first (open the link Sentinel Cloud sent you), then sign in.")
        XCTAssertEqual(card.phase, .notLinked)
        XCTAssertEqual(card.error, "Confirm your email first (open the link Sentinel Cloud sent you), then sign in.")
        XCTAssertTrue(card.awaitingConfirmation)

        card.startSignIn()
        XCTAssertNil(card.error)
        card.signedIn()
        XCTAssertFalse(card.awaitingConfirmation)
        card.linkFailed(.serverLimit, message: "Your Sentinel Cloud plan has no room for another server; upgrade it, or unlink a server, on sentvms.com.")
        XCTAssertEqual(card.phase, .notLinked)
        XCTAssertTrue(card.serverLimitReached)
        XCTAssertNotNil(card.error)
        card.startSignIn()
        XCTAssertFalse(card.serverLimitReached)

        // No sign-in config (control plane unreachable): the form can't submit.
        var offline = CloudCard()
        offline.loaded(V2.CloudStatus())
        offline.email = "ej@example.com"
        XCTAssertEqual(offline.phase, .notLinked)
        XCTAssertFalse(offline.canSignIn)
        XCTAssertFalse(offline.canSubmit(password: "pw"))
    }

    func testCardSubscribePollsUntilThePlanIsActive() throws {
        let now = date("2026-10-19T20:00:00Z")
        var free = try decode(Self.linkedSample)
        free.plan = .free
        free.status = .inactive
        free.signIn = Self.config
        var card = CloudCard()
        card.loaded(free, now: now)
        XCTAssertEqual(card.offer, .subscribe)

        // No session in memory (the pane was reopened): ask inline.
        card.needsAccount(for: .checkout(.cloud, .year))
        XCTAssertEqual(card.pendingAction, .checkout(.cloud, .year))
        XCTAssertTrue(card.showsForm)
        XCTAssertEqual(card.submitTitle, "Sign In and Continue")
        card.email = "ej@example.com"
        XCTAssertTrue(card.canSubmit(password: "pw"))
        card.startSignIn()
        XCTAssertEqual(card.phase, .linked, "the card stays linked while signing in again")
        XCTAssertTrue(card.busy)
        XCTAssertFalse(card.canSubmit(password: "pw"))
        card.signedIn()
        XCTAssertEqual(card.phase, .linked)

        card.openedCheckout(now: now)
        XCTAssertNil(card.pendingAction)
        XCTAssertFalse(card.busy)
        XCTAssertEqual(card.paymentWaitUntil, now.addingTimeInterval(180))
        XCTAssertTrue(card.isWaitingForPayment(now: now.addingTimeInterval(170)))
        XCTAssertFalse(card.isWaitingForPayment(now: now.addingTimeInterval(180)))
        XCTAssertEqual(card.note, "Finish paying in your browser; this card updates by itself.")

        card.loaded(free, now: now.addingTimeInterval(30))
        XCTAssertNotNil(card.paymentWaitUntil, "still free: keep polling")
        var paid = free
        paid.plan = .cloud
        paid.status = .active
        card.loaded(paid, now: now.addingTimeInterval(40))
        XCTAssertNil(card.paymentWaitUntil)
        XCTAssertEqual(card.offer, .manage)
        XCTAssertEqual(card.note, "Your Sentinel Cloud plan is active.")

        // Three minutes without a payment: stop and say how to check.
        var late = CloudCard()
        late.loaded(free, now: now)
        late.openedCheckout(now: now)
        late.loaded(free, now: now.addingTimeInterval(181))
        XCTAssertNil(late.paymentWaitUntil)
        XCTAssertEqual(late.note, "If you finished paying, press Refresh in a minute to see your plan.")
        var gaveUp = CloudCard()
        gaveUp.loaded(free, now: now)
        gaveUp.openedCheckout(now: now)
        gaveUp.paymentWaitTimedOut()
        XCTAssertNil(gaveUp.paymentWaitUntil)
    }

    func testCardManageExpiredSessionAndTurnOff() throws {
        var card = CloudCard()
        card.loaded(try decode(Self.linkedSample))
        XCTAssertEqual(card.offer, .manage)

        card.beginAction()
        XCTAssertTrue(card.busy)
        card.actionFailed("Your Sentinel Cloud sign-in expired; enter your password again.", sessionExpired: true, retry: .portal)
        XCTAssertEqual(card.pendingAction, .portal)
        XCTAssertEqual(card.error, "Your Sentinel Cloud sign-in expired; enter your password again.")
        XCTAssertTrue(card.showsForm)
        card.cancelAccountPrompt()
        XCTAssertNil(card.pendingAction)
        XCTAssertNil(card.error)

        card.beginAction()
        card.actionFailed("No subscription found for this account.")
        XCTAssertNil(card.pendingAction)
        XCTAssertEqual(card.error, "No subscription found for this account.")
        card.beginAction()
        XCTAssertNil(card.error)
        card.openedPortal()
        XCTAssertFalse(card.busy)
        XCTAssertEqual(card.note, "Sentinel Cloud's billing page opened in your browser.")

        var backupOff = try decode(Self.linkedSample)
        backupOff.backup.enabled = false
        card.beginAction()
        card.actionSucceeded(backupOff)
        XCTAssertFalse(card.busy)
        XCTAssertEqual(card.status?.backup.enabled, false)
        XCTAssertEqual(card.phase, .linked)

        card.beginAction()
        card.unlinked(V2.CloudStatus(signIn: Self.config))
        XCTAssertEqual(card.phase, .notLinked)
        XCTAssertFalse(card.busy)
        XCTAssertNil(card.offer)
        XCTAssertTrue(card.showsForm)

        // Unlinked elsewhere: the next read drops back to the form.
        var other = CloudCard()
        other.loaded(try decode(Self.linkedSample))
        other.needsAccount(for: .portal)
        other.loaded(V2.CloudStatus())
        XCTAssertEqual(other.phase, .notLinked)
        XCTAssertNil(other.pendingAction)
        // needsAccount only applies to a linked card.
        other.needsAccount(for: .portal)
        XCTAssertNil(other.pendingAction)
    }

    func testCardCopyKeepsTheContractWords() throws {
        var texts = [CloudAccount.linkErrorText(SentinelAPIError.server(status: 401, code: "cloud_sign_in", message: "")),
                     CloudAccount.linkErrorText(SentinelAPIError.server(status: 402, code: "cloud_server_limit", message: "")),
                     CloudAccount.linkErrorText(SentinelAPIError.server(status: 409, code: "cloud_already_linked", message: "")),
                     CloudAccount.linkErrorText(SentinelAPIError.server(status: 502, code: "cloud_unreachable", message: "")),
                     CloudAccount.insecureAddress.message]
        texts += CloudAccount.statusLines(try decode(Self.linkedSample)).flatMap { [$0.label, $0.value] }
        var card = CloudCard()
        card.loaded(try decode(Self.unlinkedSample))
        texts.append(card.submitTitle)
        card.startSignIn()
        texts.append(card.linkingText)
        card.needsEmailConfirmation()
        texts += [card.submitTitle, card.note ?? ""]
        for text in texts {
            XCTAssertFalse(text.lowercased().contains("tunnel"), text)
            XCTAssertFalse(text.isEmpty)
        }
        XCTAssertEqual(CloudAccount.paymentPollSeconds, 10)
        XCTAssertEqual(CloudAccount.paymentPollWindow, 180)
    }

    // MARK: Client over the stub protocol

    func testClientSignsInAndOpensBillingOverHTTPS() async throws {
        MockURLProtocol.handler = { request, body in
            XCTAssertEqual(request.url?.scheme, "https")
            XCTAssertEqual(request.url?.host, "project.supabase.test")
            XCTAssertEqual(request.value(forHTTPHeaderField: "apikey"), "sb_publishable_test")
            switch request.url?.path ?? "" {
            case "/auth/v1/token":
                XCTAssertEqual(request.url?.query, "grant_type=password")
                let json = try JSONSerialization.jsonObject(with: body ?? Data()) as? [String: String]
                if json?["password"] == "right" { return .json(200, #"{"access_token":"jwt-1","user":{"id":"u"}}"#) }
                return .json(400, #"{"error":"invalid_grant","error_description":"Invalid login credentials"}"#)
            case "/auth/v1/signup":
                return .json(200, #"{"id":"u","email":"new@example.com"}"#)
            case "/functions/v1/sentinel-create-checkout":
                XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer jwt-1")
                return .json(200, #"{"url":"https://checkout.stripe.com/c/pay/cs_test_1"}"#)
            case "/functions/v1/sentinel-portal":
                return request.value(forHTTPHeaderField: "Authorization") == "Bearer jwt-1"
                    ? .json(200, #"{"url":"https://billing.stripe.com/p/session/test_1"}"#)
                    : .json(401, #"{"error":"Invalid session — sign in again"}"#)
            default:
                return .json(404, "{}")
            }
        }
        let client = CloudAccountClient(session: MockURLProtocol.session())
        let config = Self.config

        let outcome = try await client.signIn(config, email: "ej@example.com", password: "right", createAccount: false)
        XCTAssertEqual(outcome, .session(accessToken: "jwt-1"))
        do {
            _ = try await client.signIn(config, email: "ej@example.com", password: "wrong", createAccount: false)
            XCTFail("expected a rejection")
        } catch {
            XCTAssertEqual(error as? CloudAccountError, CloudAccountError(.rejected, "The email or password is wrong."))
            XCTAssertEqual(CloudAccount.errorText(error), "The email or password is wrong.")
        }
        let created = try await client.signIn(config, email: "new@example.com", password: "pw", createAccount: true)
        XCTAssertEqual(created, .confirmEmail)

        let checkout = try await client.checkoutURL(config, accessToken: "jwt-1", plan: .cloud, interval: .month)
        XCTAssertEqual(checkout.absoluteString, "https://checkout.stripe.com/c/pay/cs_test_1")
        let portal = try await client.portalURL(config, accessToken: "jwt-1")
        XCTAssertEqual(portal.absoluteString, "https://billing.stripe.com/p/session/test_1")
        do {
            _ = try await client.portalURL(config, accessToken: "stale")
            XCTFail("expected an expired session")
        } catch {
            XCTAssertEqual((error as? CloudAccountError)?.kind, .sessionExpired)
        }
        XCTAssertEqual(MockURLProtocol.requests.map { $0.request.url?.path ?? "" },
                       ["/auth/v1/token", "/auth/v1/token", "/auth/v1/signup", "/functions/v1/sentinel-create-checkout",
                        "/functions/v1/sentinel-portal", "/functions/v1/sentinel-portal"])
        XCTAssertEqual(MockURLProtocol.requests[3].body.map { String(decoding: $0, as: UTF8.self) },
                       #"{"cancel_url":"https://sentvms.com/cloud","interval":"month","plan":"cloud","success_url":"https://sentvms.com/cloud?checkout=success"}"#)
    }

    func testClientSendsNothingInsecureAndExplainsNetworkFailures() async {
        MockURLProtocol.handler = { _, _ in throw URLError(.notConnectedToInternet) }
        let client = CloudAccountClient(session: MockURLProtocol.session())
        var insecure = Self.config
        insecure.supabaseUrl = "http://project.supabase.test"
        do {
            _ = try await client.signIn(insecure, email: "ej@example.com", password: "pw", createAccount: false)
            XCTFail("expected a refusal")
        } catch {
            XCTAssertEqual((error as? CloudAccountError)?.kind, .insecure)
        }
        XCTAssertTrue(MockURLProtocol.requests.isEmpty, "the password never left over plain http")
        do {
            _ = try await client.signIn(Self.config, email: "ej@example.com", password: "pw", createAccount: false)
            XCTFail("expected a network error")
        } catch {
            XCTAssertEqual(error as? CloudAccountError, CloudAccountError(.network, "This Mac is offline; connect to the internet and try again."))
        }
    }

    // MARK: Server calls

    func testLinkAndRefreshUseTheWave13Routes() async throws {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://nvr.test:8090")!, siteID: "s1", tokenStore: store,
                              session: MockURLProtocol.session(), clientLabel: "Test")
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.host, "nvr.test")
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("POST", "/api/v2/cloud/link"), ("POST", "/api/v2/cloud/refresh"): return .json(200, Self.linkedSample)
            case ("GET", "/api/v2/cloud"): return .json(200, Self.unlinkedSample)
            default: return .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#)
            }
        }
        let before = try await api.cloudStatus()
        XCTAssertFalse(before.linked)
        XCTAssertEqual(before.signIn?.plans.count, 2)
        let linked = try await api.linkCloud(accessToken: "jwt-1")
        XCTAssertTrue(linked.linked)
        let refreshed = try await api.refreshCloud()
        XCTAssertEqual(refreshed.plan, .cloud)

        let log = MockURLProtocol.requests
        XCTAssertEqual(log.map { "\($0.request.httpMethod ?? "") \($0.request.url?.path ?? "")" },
                       ["GET /api/v2/cloud", "POST /api/v2/cloud/link", "POST /api/v2/cloud/refresh"])
        XCTAssertEqual(log[1].request.value(forHTTPHeaderField: "Authorization"), "Bearer access-1", "the server call carries the Sentinel token")
        XCTAssertEqual(log[1].request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(log[1].body.map { String(decoding: $0, as: UTF8.self) }, #"{"accessToken":"jwt-1"}"#)
        XCTAssertNil(log[2].body.flatMap { $0.isEmpty ? nil : $0 }, "refresh has no body")
    }

    func testLinkErrorsComeThroughTheClient() async throws {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://nvr.test:8090")!, siteID: "s1", tokenStore: store,
                              session: MockURLProtocol.session(), clientLabel: "Test")
        MockURLProtocol.handler = { _, _ in
            .json(402, #"{"error":{"code":"cloud_server_limit","message":"plan allows 1 server","upgradeURL":"https://sentvms.com/cloud"}}"#)
        }
        do {
            _ = try await api.linkCloud(accessToken: "jwt-1")
            XCTFail("expected an error")
        } catch {
            XCTAssertEqual(CloudAccount.linkFailure(error), .serverLimit)
        }

        // 401 cloud_sign_in is about the account token in the body, not our
        // session: SentinelAPI must NOT refresh and resend it (that would leave
        // a second rejected link attempt on the control plane).
        MockURLProtocol.reset()
        MockURLProtocol.handler = { request, _ in
            if request.url?.path == "/api/v2/auth/token" { return .json(200, Fixtures.tokenResponse(access: "access-2")) }
            return .json(401, #"{"error":{"code":"cloud_sign_in","message":"Sentinel Cloud rejected the sign-in"}}"#)
        }
        do {
            _ = try await api.linkCloud(accessToken: "jwt-1")
            XCTFail("expected an error")
        } catch {
            XCTAssertEqual(CloudAccount.linkFailure(error), .signIn)
            XCTAssertEqual(CloudAccount.linkErrorText(error), "Sentinel Cloud didn't accept the sign-in; enter your password again.")
        }
        XCTAssertEqual(MockURLProtocol.requests.map { $0.request.url?.path ?? "" },
                       ["/api/v2/cloud/link"])
    }
}
