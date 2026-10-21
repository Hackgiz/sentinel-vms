import XCTest
@testable import Sentinel_Mobile

/// Insights on the phone (WAVE15-CLIENT-PARITY.md, iPhone): decoding, the
/// range query, the daily bars, share formatting, which actions a user gets,
/// and the API calls. No network: `MockURLProtocol` answers everything.
final class InsightsTests: XCTestCase {
    private let site = URL(string: "http://10.0.0.1:8090")!

    private func api() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "sat", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "srt", refreshExpiresAt: Date().addingTimeInterval(86400).unixMillis, userName: "op"),
                   siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: mockSession())
    }

    private let sample = """
    {"from":1000,"to":2000,
     "totals":{"events":12,"alarms":9,"camerasWithAlarms":2,"offlineCameras":1},
     "daily":[{"day":"2026-10-14","events":5,"alarms":4,"byKind":{"Motion":3,"Person":1}},
              {"day":"2026-10-15","events":7,"alarms":5,"byKind":null}],
     "cameras":[{"cameraId":"cam-7","name":"Gate","alarms":7,"topKind":"Motion","alertOn":"motion"}],
     "mix":[{"kind":"Motion","alarms":3,"share":0.75},{"kind":"Person","alarms":1,"share":0.25}],
     "recommendations":[{"id":"motion-only:cam-7","level":"attention","title":"Gate alarms on any motion",
       "detail":"Most were motion.","cameraId":"cam-7",
       "action":{"type":"setAlertOn","cameraId":"cam-7","alertOn":"peopleAndVehicles"}}]}
    """

    private func day(_ d: String, alarms: Int, _ byKind: [String: Int], events: Int = 0) -> V2.Insights.Day {
        V2.Insights.Day(day: d, events: events, alarms: alarms, byKind: byKind)
    }

    private func rec(_ type: V2.RecommendationAction.ActionType?, cameraID: String? = "cam-1",
                     alertOn: V2.AlertOn? = .peopleAndVehicles, templateID: String? = "t1") -> V2.Recommendation {
        V2.Recommendation(id: "r", level: .suggestion, title: "T", detail: "D", cameraId: cameraID,
                          action: type.map { V2.RecommendationAction(type: $0, cameraId: nil, alertOn: alertOn, templateId: templateID) })
    }

    // MARK: - Decoding

    func testDecodesInsightsWithNullMapsAndUnknownValues() throws {
        let ins = try SentinelAPI.decode(V2.Insights.self, from: Data(sample.utf8))
        XCTAssertEqual(ins.totals.alarms, 9)
        XCTAssertEqual(ins.daily.count, 2)
        XCTAssertEqual(ins.daily[1].byKind, [:], "Go's nil map decodes as empty")
        XCTAssertEqual(ins.cameras.first?.alertOn, .motion)
        XCTAssertEqual(ins.recommendations.first?.level, .attention)
        XCTAssertEqual(ins.recommendations.first?.action?.type, .setAlertOn)

        let odd = try SentinelAPI.decode(V2.Recommendation.self, from: Data(#"{"id":"x","level":"urgent","title":"t","detail":"d","action":{"type":"openMoon"}}"#.utf8))
        XCTAssertEqual(odd.level.rawValue, "urgent")
        XCTAssertNil(InsightsMath.action(for: odd, can: { _ in true }), "an unknown action shows nothing")

        let empty = try SentinelAPI.decode(V2.Insights.self, from: Data(#"{"from":1,"to":2,"totals":{"events":0,"alarms":0,"camerasWithAlarms":0,"offlineCameras":0},"daily":null,"cameras":null,"mix":null,"recommendations":null}"#.utf8))
        XCTAssertTrue(InsightsMath.isEmpty(empty))
    }

    // MARK: - Range

    func testRangeQueryEndsNowAndDefaultsToSevenDays() {
        let now: UnixMillis = 1_760_000_000_000
        XCTAssertEqual(InsightsMath.Range.default, .days7)
        XCTAssertEqual(InsightsMath.RangeQuery(.day1, now: now), .init(from: now - 86_400_000, to: now))
        XCTAssertEqual(InsightsMath.RangeQuery(.days7, now: now).from, now - 7 * 86_400_000)
        XCTAssertEqual(InsightsMath.RangeQuery(.days30, now: now).from, now - 30 * 86_400_000)
        XCTAssertEqual(InsightsMath.Range.allCases.map(\.label), ["24 h", "7 days", "30 days"])
    }

    // MARK: - Daily bars

    func testStackKeepsTopFourKindsAndSumsTheRestAsOther() {
        let daily = [
            day("2026-10-14", alarms: 19, ["Motion": 6, "Person": 5, "Vehicle": 4, "Tamper": 3, "audio": 1]),
            day("2026-10-15", alarms: 2, ["Loitering": 2])
        ]
        let s = InsightsMath.stackDaily(daily)
        // Top four by total, in rule-kind order; audio and Loitering become Other.
        XCTAssertEqual(s.kinds, ["Motion", "Person", "Vehicle", "Tamper", "Other"])
        XCTAssertEqual(s.bars[0].segments.map(\.kind), ["Motion", "Person", "Vehicle", "Tamper", "Other"])
        XCTAssertEqual(s.bars[0].segments.last?.count, 1)
        XCTAssertEqual(s.bars[0].total, 19)
        XCTAssertEqual(s.bars[1].segments, [.init(kind: "Other", count: 2)])
        XCTAssertEqual(s.peak, 19)

        // A tie goes to the kind that comes first among the rule kinds.
        let tie = InsightsMath.stackDaily([day("2026-10-14", alarms: 2, ["Tamper": 1, "Loitering": 1])], top: 1)
        XCTAssertEqual(tie.kinds, ["Loitering", "Other"])
    }

    func testStackCountsUnaccountedAlarmsAsOtherAndSkipsZeroes() {
        let s = InsightsMath.stackDaily([
            day("2026-10-14", alarms: 4, ["Motion": 3, "Person": 0]),
            day("2026-10-15", alarms: 0, [:])
        ])
        XCTAssertEqual(s.kinds, ["Motion", "Other"])
        XCTAssertEqual(s.bars[0].segments, [.init(kind: "Motion", count: 3), .init(kind: "Other", count: 1)])
        XCTAssertEqual(s.bars[0].total, 4, "a bar is never shorter than the day's alarms")
        XCTAssertEqual(s.bars[1].segments, [])
        XCTAssertEqual(s.bars[1].total, 0)

        let none = InsightsMath.stackDaily([day("2026-10-14", alarms: 0, [:])])
        XCTAssertEqual(none.kinds, [])
        XCTAssertEqual(none.peak, 0)
    }

    // MARK: - Formatting

    func testShareFormatting() {
        XCTAssertEqual(InsightsMath.formatShare(0), "0%")
        XCTAssertEqual(InsightsMath.formatShare(-1), "0%")
        XCTAssertEqual(InsightsMath.formatShare(.nan), "0%")
        XCTAssertEqual(InsightsMath.formatShare(0.004), "<1%")
        XCTAssertEqual(InsightsMath.formatShare(0.25), "25%")
        XCTAssertEqual(InsightsMath.formatShare(0.336), "34%")
        XCTAssertEqual(InsightsMath.formatShare(0.995), ">99%")
        XCTAssertEqual(InsightsMath.formatShare(1), "100%")
    }

    func testLabels() {
        XCTAssertEqual(InsightsMath.kindLabel("audio"), "Audio")
        XCTAssertEqual(InsightsMath.kindLabel(""), "Other")
        XCTAssertEqual(InsightsMath.alertOnLabel(.peopleAndVehicles), "People & vehicles")
        XCTAssertEqual(InsightsMath.dayLabel("2026-10-14", locale: Locale(identifier: "en_US")), "Wed, Oct 14")
        XCTAssertEqual(InsightsMath.dayLabel("soon"), "soon")
    }

    // MARK: - Actions

    func testActionPermissions() {
        XCTAssertEqual(InsightsMath.permission(for: .setAlertOn), .manageCameras)
        XCTAssertEqual(InsightsMath.permission(for: .openTemplate), .changeSettings)
        XCTAssertEqual(InsightsMath.permission(for: .openCamera), .manageCameras)
        XCTAssertEqual(InsightsMath.permission(for: .openStorage), .viewLive)
        XCTAssertNil(InsightsMath.permission(for: .init(rawValue: "openMoon")))
    }

    func testSwitchButtonOnlyWithManageCameras() {
        let r = rec(.setAlertOn)
        XCTAssertEqual(InsightsMath.action(for: r, can: { $0 == .manageCameras }),
                       .setAlertOn(label: "Switch to people & vehicles", cameraID: "cam-1", alertOn: .peopleAndVehicles),
                       "the camera id falls back to the recommendation's")
        XCTAssertNil(InsightsMath.action(for: r, can: { $0 == .viewLive }))
        XCTAssertNil(InsightsMath.action(for: rec(.setAlertOn, cameraID: nil), can: { _ in true }))
        XCTAssertNil(InsightsMath.action(for: rec(.setAlertOn, alertOn: nil), can: { _ in true }))
        XCTAssertNil(InsightsMath.action(for: rec(nil), can: { _ in true }))
    }

    func testOtherActionsAreTextOnly() {
        for type in [V2.RecommendationAction.ActionType.openTemplate, .openCamera, .openStorage] {
            guard case .hint = InsightsMath.action(for: rec(type), can: { _ in true }) else {
                return XCTFail("\(type.rawValue) should be a hint")
            }
        }
        XCTAssertNil(InsightsMath.action(for: rec(.openTemplate), can: { $0 == .viewLive }))
        XCTAssertNil(InsightsMath.action(for: rec(.openTemplate, templateID: nil), can: { _ in true }))
        XCTAssertNotNil(InsightsMath.action(for: rec(.openStorage), can: { $0 == .viewLive }))
    }

    // MARK: - API

    func testInsightsRequestSendsTheRangeInMillis() async throws {
        MockURLProtocol.reset { _ in .init(body: Data(self.sample.utf8)) }
        let ins = try await api().insights(InsightsMath.RangeQuery(from: 1_000, to: 2_000))
        XCTAssertEqual(ins.recommendations.count, 1)
        let req = MockURLProtocol.seen.last!
        XCTAssertEqual(req.httpMethod, "GET")
        XCTAssertEqual(req.url?.path, "/api/v2/insights")
        let items = URLComponents(url: req.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
        XCTAssertEqual(items.first { $0.name == "from" }?.value, "1000")
        XCTAssertEqual(items.first { $0.name == "to" }?.value, "2000")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer sat")
    }

    func testSetAlertOnPatchesOnlyTheAlertSetting() async throws {
        MockURLProtocol.reset { _ in .init(body: Data("{}".utf8)) }
        try await api().setAlertOn(cameraID: "cam 7", .peopleAndVehicles)
        let req = MockURLProtocol.seen.last!
        XCTAssertEqual(req.httpMethod, "PATCH")
        XCTAssertEqual(req.url?.absoluteString, "http://10.0.0.1:8090/api/v2/cameras/cam%207")
        XCTAssertEqual(String(decoding: req.httpBody ?? Data(), as: UTF8.self), #"{"alertOn":"peopleAndVehicles"}"#)
    }

    func testErrorsShowTheServersMessage() async {
        for status in [403, 409, 429, 502] {
            let message = "server says \(status)"
            MockURLProtocol.reset { _ in
                .init(status: status, body: Data(#"{"error":{"code":"x","message":"\#(message)"}}"#.utf8))
            }
            do {
                _ = try await api().insights(from: 1, to: 2)
                XCTFail("HTTP \(status) should throw")
            } catch {
                XCTAssertEqual((error as? SentinelAPIError)?.status, status)
                XCTAssertEqual(InsightsMath.errorMessage(error), message)
            }
        }
    }
}
