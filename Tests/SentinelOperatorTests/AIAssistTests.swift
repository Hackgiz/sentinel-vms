import XCTest
@testable import SentinelCore

/// Wave 15: rule time profile and actions surviving a Mac edit, rule drafts
/// (incl. AI off, the daily limit and a failed model call), templates, and the
/// Insights arithmetic (range, stacking, shares, action permissions).
final class AIAssistTests: XCTestCase {
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

    private func object(_ data: Data) throws -> NSDictionary {
        try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? NSDictionary)
    }

    private let ruleWithExtras = """
    {"id":"r1","createdAt":1,"updatedAt":2,"name":"Dock","enabled":true,"cameraIds":["c1"],"kinds":["Person"],
     "minSeverity":"Warning","schedule":false,"fromMinute":0,"toMinute":0,"days":[],"delivery":"push",
     "instructions":"Call the guard.","timeProfileId":"tp1","actions":[{"type":"webhook","webhookId":"wh1"}]}
    """

    // MARK: Rule round trip (the data-loss fix)

    func testRuleKeepsTimeProfileAndActionsThroughDecodeAndEncode() throws {
        let rule = try SentinelAPI.decode(V2.Rule.self, from: Data(ruleWithExtras.utf8))
        XCTAssertEqual(rule.timeProfileId, "tp1")
        XCTAssertEqual(rule.actions, [V2.RuleAction(type: "webhook", webhookId: "wh1")])
        let encoded = try SentinelAPI.jsonEncoder.encode(rule)
        XCTAssertEqual(try object(encoded), try object(Data(ruleWithExtras.utf8)))

        // The PUT body built from it carries both.
        let input = try object(SentinelAPI.jsonEncoder.encode(V2.RuleInput(rule)))
        XCTAssertEqual(input["timeProfileId"] as? String, "tp1")
        XCTAssertEqual((input["actions"] as? [[String: String]])?.first?["webhookId"], "wh1")
    }

    func testRuleInputOmitsEmptyTimeProfileAndActions() throws {
        var input = V2.RuleInput(name: "Plain")
        XCTAssertNil(try object(SentinelAPI.jsonEncoder.encode(input))["timeProfileId"])
        input.timeProfileId = ""
        input.actions = []
        let body = try object(SentinelAPI.jsonEncoder.encode(input))
        XCTAssertNil(body["timeProfileId"])
        XCTAssertNil(body["actions"])
        XCTAssertNotNil(body["cameraIds"])
    }

    func testReplaceRuleSendsTimeProfileAndActions() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, self.ruleWithExtras) }
        let rule = try SentinelAPI.decode(V2.Rule.self, from: Data(ruleWithExtras.utf8))
        var input = V2.RuleInput(rule)
        input.enabled = false
        _ = try await makeAPI().replaceRule("r1", input)
        let sent = try object(XCTUnwrap(MockURLProtocol.requests.first?.body))
        XCTAssertEqual(MockURLProtocol.requests.first?.request.httpMethod, "PUT")
        XCTAssertEqual(sent["timeProfileId"] as? String, "tp1")
        XCTAssertEqual(sent["enabled"] as? Bool, false)
        XCTAssertNotNil(sent["actions"])
    }

    // MARK: Drafts

    func testDraftRuleParsesAndPostsText() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(200, """
            {"rule":{"name":"People at the dock","enabled":false,"cameraIds":null,"kinds":["Person"],"minSeverity":"Info",
             "schedule":true,"fromMinute":1080,"toMinute":360,"days":null,"delivery":"push","instructions":""},
             "explanation":"Alerts on people after 6pm.","warnings":["No camera named Dock."]}
            """)
        }
        let draft = try await makeAPI().draftRule("people at the dock after 6pm")
        XCTAssertEqual(draft.rule.name, "People at the dock")
        XCTAssertFalse(draft.rule.enabled)
        XCTAssertEqual(draft.rule.cameraIds, [])
        XCTAssertEqual(draft.rule.days, [])
        XCTAssertEqual(draft.explanation, "Alerts on people after 6pm.")
        XCTAssertEqual(draft.warnings, ["No camera named Dock."])
        let call = try XCTUnwrap(MockURLProtocol.requests.first)
        XCTAssertEqual(call.request.httpMethod, "POST")
        XCTAssertEqual(call.request.url?.path, "/api/v2/rules/draft")
        XCTAssertEqual(try object(XCTUnwrap(call.body)) as? [String: String], ["text": "people at the dock after 6pm"])
    }

    func testDraftRuleAIFailures() async {
        let cases: [(Int, String, AIFailure)] = [
            (409, #"{"error":{"code":"conflict","message":"AI is turned off on this server."}}"#, .off("AI is turned off on this server.")),
            (429, #"{"error":{"code":"too_many_requests","message":"Daily AI limit reached (50)."}}"#, .limitReached("Daily AI limit reached (50).")),
            (502, #"{"error":{"code":"internal","message":"The model call failed."}}"#, .unavailable("The model call failed."))
        ]
        for (status, body, expected) in cases {
            MockURLProtocol.handler = { _, _ in .json(status, body) }
            do {
                _ = try await makeAPI().draftRule("people at night")
                XCTFail("expected a failure for \(status)")
            } catch {
                XCTAssertEqual(AIFailure(error), expected)
                XCTAssertEqual(AIFailure(error).message, expected.message)
            }
        }
    }

    func testDraftTextBounds() {
        XCTAssertFalse(RuleCatalog.canDraft("ab"))
        XCTAssertFalse(RuleCatalog.canDraft("  ab  "))
        XCTAssertTrue(RuleCatalog.canDraft("abc"))
        XCTAssertTrue(RuleCatalog.canDraft(String(repeating: "x", count: 500)))
        XCTAssertFalse(RuleCatalog.canDraft(String(repeating: "x", count: 501)))
    }

    func testRuleKindsMatchServerWithoutAnimal() {
        XCTAssertEqual(RuleCatalog.kinds, ["Motion", "Person", "Vehicle", "Line crossing", "Intrusion", "Loitering", "Tamper",
                                           "Camera Offline", "Storage", "audio", "Watchlist"])
        XCTAssertFalse(RuleCatalog.kinds.contains("Animal"))
    }

    // MARK: Templates

    func testTemplatesParseAndGroupByCategory() async throws {
        let rule = #"{"name":"R","enabled":true,"cameraIds":[],"kinds":[],"minSeverity":"Info","schedule":false,"fromMinute":0,"toMinute":0,"days":[],"delivery":"dashboard","instructions":""}"#
        MockURLProtocol.handler = { _, _ in
            .json(200, """
            {"items":[
             {"id":"t1","category":"operations","title":"Ops","summary":"s","rule":\(rule)},
             {"id":"t2","category":"security","title":"Sec","summary":"s","rule":\(rule)},
             {"id":"t3","category":"facilities","title":"New","summary":"s","rule":\(rule)},
             {"id":"t4","category":"security","title":"Sec 2","summary":"s","rule":\(rule)}]}
            """)
        }
        let templates = try await makeAPI().ruleTemplates()
        XCTAssertEqual(MockURLProtocol.requests.first?.request.url?.path, "/api/v2/rule-templates")
        let groups = RuleCatalog.groupTemplates(templates)
        XCTAssertEqual(groups.map(\.id), ["security", "operations", "facilities"])
        XCTAssertEqual(groups.map(\.label), ["Security", "Operations", "Facilities"])
        XCTAssertEqual(groups[0].items.map(\.id), ["t2", "t4"])
    }

    // MARK: Insights

    func testRangeBoundsAndQuery() async throws {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        XCTAssertEqual(InsightsRange.default, .week)
        XCTAssertEqual(InsightsRange.allCases.map(\.label), ["24 h", "7 days", "30 days"])
        let day = InsightsRange.day.bounds(now: now)
        XCTAssertEqual(day.to, 1_760_000_000_000)
        XCTAssertEqual(day.to - day.from, 86_400_000)
        let month = InsightsRange.month.bounds(now: now)
        XCTAssertEqual(month.to - month.from, 30 * 86_400_000)

        MockURLProtocol.handler = { _, _ in
            .json(200, """
            {"from":1,"to":2,"totals":{"events":5,"alarms":3,"camerasWithAlarms":1,"offlineCameras":0},
             "daily":[{"day":"2026-10-14","events":5,"alarms":3,"byKind":{"Person":3}}],
             "cameras":[{"cameraId":"c1","name":"Dock","alarms":3,"topKind":"Person","alertOn":"motion"}],
             "mix":[{"kind":"Person","alarms":3,"share":1}],
             "recommendations":[{"id":"motion-only:c1","level":"suggestion","title":"t","detail":"d","cameraId":"c1",
               "action":{"type":"setAlertOn","cameraId":"c1","alertOn":"peopleAndVehicles"}}]}
            """)
        }
        let ins = try await makeAPI().insights(from: month.from, to: month.to)
        let url = try XCTUnwrap(MockURLProtocol.requests.first?.request.url)
        XCTAssertEqual(url.path, "/api/v2/insights")
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        XCTAssertEqual(items.first { $0.name == "from" }?.value, String(month.from))
        XCTAssertEqual(items.first { $0.name == "to" }?.value, String(month.to))
        XCTAssertEqual(ins.totals.alarms, 3)
        XCTAssertEqual(ins.cameras.first?.alertOn, .motion)
        XCTAssertEqual(ins.recommendations.first?.action?.alertOn, .peopleAndVehicles)
        XCTAssertFalse(ins.isEmpty)
    }

    func testInsightsToleratesNullListsAndMaps() throws {
        let ins = try SentinelAPI.decode(V2.Insights.self, from: Data("""
        {"from":1,"to":2,"totals":{"events":0,"alarms":0,"camerasWithAlarms":0,"offlineCameras":0},
         "daily":[{"day":"2026-10-14","events":0,"alarms":0,"byKind":null}],"cameras":null,"mix":null,"recommendations":null}
        """.utf8))
        XCTAssertTrue(ins.isEmpty)
        XCTAssertEqual(ins.cameras, [])
        XCTAssertNil(ins.daily[0].byKind)
    }

    func testStackDailyTopFourPlusOther() {
        let daily = [
            V2.Insights.Day(day: "2026-10-14", alarms: 12, byKind: ["Person": 5, "Vehicle": 2, "Motion": 1, "audio": 1, "Tamper": 1]),
            V2.Insights.Day(day: "2026-10-15", alarms: 4, byKind: ["Person": 1, "Loitering": 3]),
            V2.Insights.Day(day: "2026-10-16", alarms: 0, byKind: nil)
        ]
        let stack = InsightsLogic.stackDaily(daily)
        // Busiest four (Person 6, Loitering 3, Vehicle 2, then Motion/audio/Tamper tied at 1: Motion by rule order),
        // shown in rule-kind order, then Other.
        XCTAssertEqual(stack.kinds, ["Motion", "Person", "Vehicle", "Loitering", "Other"])
        let first = stack.bars[0]
        XCTAssertEqual(first.total, 12) // 2 unaccounted alarms count as Other
        XCTAssertEqual(first.segments.map(\.kind), ["Motion", "Person", "Vehicle", "Other"])
        XCTAssertEqual(first.segments.last?.count, 4) // audio 1 + Tamper 1 + 2 unaccounted
        XCTAssertEqual(stack.bars[1].segments.map(\.kind), ["Person", "Loitering"])
        XCTAssertEqual(stack.bars[2].total, 0)
        XCTAssertTrue(stack.bars[2].segments.isEmpty)

        // No Other when everything fits.
        let small = InsightsLogic.stackDaily([V2.Insights.Day(day: "d", alarms: 2, byKind: ["Person": 2])])
        XCTAssertEqual(small.kinds, ["Person"])
    }

    func testShareAndLabels() {
        XCTAssertEqual(InsightsLogic.formatShare(0), "0%")
        XCTAssertEqual(InsightsLogic.formatShare(0.004), "<1%")
        XCTAssertEqual(InsightsLogic.formatShare(0.426), "43%")
        XCTAssertEqual(InsightsLogic.formatShare(0.995), ">99%")
        XCTAssertEqual(InsightsLogic.formatShare(1), "100%")
        XCTAssertEqual(InsightsLogic.kindLabel("audio"), "Audio")
        XCTAssertEqual(InsightsLogic.kindLabel(""), "Other")
        XCTAssertEqual(InsightsLogic.dayLabel("2026-10-14", locale: Locale(identifier: "en_US")), "Wed, Oct 14")
        XCTAssertEqual(InsightsLogic.dayLabel("garbage"), "garbage")
    }

    func testActionPermissionsAndPlans() {
        XCTAssertEqual(InsightsLogic.permission(for: "setAlertOn"), .manageCameras)
        XCTAssertEqual(InsightsLogic.permission(for: "openTemplate"), .changeSettings)
        XCTAssertEqual(InsightsLogic.permission(for: "openCamera"), .manageCameras)
        XCTAssertEqual(InsightsLogic.permission(for: "openStorage"), .viewLive)
        XCTAssertNil(InsightsLogic.permission(for: "launchRocket"))

        let viewer: (V2.Permission) -> Bool = { $0 == .viewLive }
        let admin: (V2.Permission) -> Bool = { _ in true }
        func rec(_ action: V2.RecommendationAction?, camera: String? = nil) -> V2.Recommendation {
            V2.Recommendation(id: "r", cameraId: camera, action: action)
        }
        let setAlert = rec(.init(type: "setAlertOn", alertOn: .peopleAndVehicles), camera: "c1")
        XCTAssertEqual(InsightsLogic.action(for: setAlert, can: admin), .setAlertOn(cameraId: "c1", alertOn: .peopleAndVehicles))
        XCTAssertEqual(InsightsLogic.action(for: setAlert, can: admin)?.label, "Switch to people & vehicles")
        XCTAssertNil(InsightsLogic.action(for: setAlert, can: viewer))
        XCTAssertNil(InsightsLogic.action(for: rec(.init(type: "setAlertOn", cameraId: "c1")), can: admin)) // no alertOn

        XCTAssertEqual(InsightsLogic.action(for: rec(.init(type: "openTemplate", templateId: "t1")), can: admin), .openTemplate(templateId: "t1"))
        XCTAssertNil(InsightsLogic.action(for: rec(.init(type: "openTemplate", templateId: "t1")), can: viewer))
        XCTAssertNil(InsightsLogic.action(for: rec(.init(type: "openTemplate")), can: admin))

        XCTAssertEqual(InsightsLogic.action(for: rec(.init(type: "openCamera", cameraId: "c2")), can: admin), .openCamera(cameraId: "c2"))
        XCTAssertNil(InsightsLogic.action(for: rec(.init(type: "openCamera", cameraId: "c2")), can: viewer))

        XCTAssertEqual(InsightsLogic.action(for: rec(.init(type: "openStorage")), can: viewer), .openStorage)
        XCTAssertNil(InsightsLogic.action(for: rec(.init(type: "openStorage")), can: { _ in false }))
        XCTAssertNil(InsightsLogic.action(for: rec(nil), can: admin))
        XCTAssertNil(InsightsLogic.action(for: rec(.init(type: "somethingNew")), can: admin))
    }

    func testAlertChangeErrorNamesPermissionOn403() {
        XCTAssertEqual(InsightsLogic.alertChangeError(SentinelAPIError.server(status: 403, code: "forbidden", message: "x")),
                       "You need permission to change cameras.")
        XCTAssertEqual(InsightsLogic.alertChangeError(SentinelAPIError.server(status: 404, code: "not_found", message: "Not found.")),
                       "Not found.")
    }
}
