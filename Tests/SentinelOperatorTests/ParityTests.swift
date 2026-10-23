import AppKit
import SwiftUI
import XCTest
@testable import SentinelCore

/// Parity 3.4.2 (PARITY-342.md): the detection-kind palette and symbols, the
/// alarm colour, the playback pre-rolls, and the Reports arithmetic and API.
final class ParityTests: XCTestCase {
    /// "#rrggbb" of a colour, in sRGB.
    private func hex(_ color: Color) -> String {
        guard let c = NSColor(color).usingColorSpace(.sRGB) else { return "?" }
        func byte(_ v: CGFloat) -> Int { Int((v * 255).rounded()) }
        return String(format: "#%02x%02x%02x", byte(c.redComponent), byte(c.greenComponent), byte(c.blueComponent))
    }

    // MARK: §1 colours

    func testKindColoursMatchTheContractTable() {
        let table: [String: String] = [
            "Person": "#2dd4bf", "Vehicle": "#3399ff", "Face": "#ff8c1a", "License Plate": "#fab819",
            "Animal": "#12d478", "Package": "#c8955a", "Loitering": "#cc4dd9",
            "Line crossing": "#f2559a", "Intrusion": "#f2559a", "audio": "#8b7cf6", "Sound": "#8b7cf6",
            "Watchlist": "#ff3b30", "Motion": "#94a3b8", "Tamper": "#94a3b8", "": "#94a3b8"
        ]
        for (kind, want) in table {
            XCTAssertEqual(hex(SentinelTheme.kindColor(kind)), want, kind)
        }
    }

    func testAlarmColourIsWatchlistRedElseSeverity() {
        XCTAssertEqual(hex(SentinelTheme.alarmColor(kind: "Watchlist", severity: .info)), "#ff3b30")
        XCTAssertEqual(hex(SentinelTheme.alarmColor(kind: "Person", severity: .critical)), hex(SentinelTheme.alarm))
        XCTAssertEqual(hex(SentinelTheme.alarmColor(kind: "Person", severity: .warning)), hex(SentinelTheme.amber))
        XCTAssertEqual(hex(SentinelTheme.alarmColor(kind: "Person", severity: .info)), hex(SentinelTheme.accent))
    }

    // MARK: §2 symbols

    func testEveryContractKindHasItsSymbol() {
        let table: [String: String] = [
            "Person": "figure.walk", "Vehicle": "car.fill", "Face": "person.fill.viewfinder",
            "License Plate": "doc.text.magnifyingglass", "Animal": "pawprint.fill", "Package": "shippingbox.fill",
            "Loitering": "clock.badge.exclamationmark", "Line crossing": "arrow.left.and.right",
            "Intrusion": "figure.walk.departure", "audio": "waveform", "Sound": "waveform",
            "Watchlist": "flag.fill", "Motion": "figure.walk.motion", "Something new": "sparkles"
        ]
        for (kind, want) in table {
            XCTAssertEqual(HomeModel.kindSymbol(kind), want, kind)
        }
        for symbol in Set(table.values) {
            XCTAssertNotNil(NSImage(systemSymbolName: symbol, accessibilityDescription: nil), "\(symbol) exists on this macOS")
        }
        XCTAssertNotNil(NSImage(systemSymbolName: FixedTab.reports.symbol, accessibilityDescription: nil))
    }

    // MARK: §3 pre-roll

    func testPreRollsAreFiveSecondsForEventsAndTenForAlarms() throws {
        XCTAssertEqual(PlaybackJump.eventPreRoll, 5)
        XCTAssertEqual(PlaybackJump.alarmPreRoll, 10)
        XCTAssertEqual(SearchFilter.playbackLead, PlaybackJump.eventPreRoll, "search uses the same lead")

        let t: UnixMillis = 1_760_000_000_000
        XCTAssertEqual(PlaybackJump.eventStart(t).unixMillis, t - 5_000)
        XCTAssertEqual(PlaybackJump.alarmStart(t).unixMillis, t - 10_000)

        let alarm = try SentinelAPI.decode(V2.Alarm.self, from: Data("""
        {"id":"a1","cameraId":"c1","kind":"Person","title":"t","detail":"","severity":"Critical","state":"New",
         "owner":"","createdAt":\(t),"updatedAt":\(t),"lastEventAt":\(t),"eventCount":1,"instructions":null,"responseLog":null}
        """.utf8))
        XCTAssertEqual(PlaybackJump.start(of: alarm).unixMillis, t - 10_000)
        let event = try SentinelAPI.decode(V2.Event.self, from: Data("""
        {"id":"e1","cameraId":"c1","kind":"Person","score":0.9,"createdAt":\(t),"hasThumbnail":false}
        """.utf8))
        XCTAssertEqual(PlaybackJump.start(of: event).unixMillis, t - 5_000)
        XCTAssertEqual(SearchFilter.playbackStart(of: event), PlaybackJump.start(of: event))
    }

    // MARK: Reports

    private let hour: UnixMillis = 3_600_000

    private var base: V2.Activity {
        V2.Activity(
            from: 10 * hour, to: 13 * hour, totals: V2.ActivityTotals(events: 9, alarms: 4),
            events: [
                V2.ActivityEvent(cameraId: "c1", kind: "Person", hour: 10 * hour, count: 3),
                V2.ActivityEvent(cameraId: "c1", kind: "Vehicle", hour: 11 * hour, count: 1),
                V2.ActivityEvent(cameraId: "c2", kind: "Motion", hour: 11 * hour, count: 5)
            ],
            alarms: [
                V2.ActivityAlarm(cameraId: "c1", kind: "Person", severity: "Critical", count: 1),
                V2.ActivityAlarm(cameraId: "c2", kind: "Motion", severity: "Info", count: 2),
                V2.ActivityAlarm(cameraId: "c2", kind: "Motion", severity: "Warning", count: 1)
            ])
    }

    func testCameraRowsSumNameAndPickTheCommonestKind() {
        XCTAssertEqual(ReportModel.cameraRows(base, names: ["c1": "Front door"]), [
            .init(cameraId: "c2", name: "c2", events: 5, alarms: 3, topKind: "Motion"),
            .init(cameraId: "c1", name: "Front door", events: 4, alarms: 1, topKind: "Person")
        ])
        var quiet = base
        quiet.events = []
        quiet.alarms = []
        XCTAssertEqual(ReportModel.cameraRows(quiet, names: [:]), [])

        var alarmsOnly = base
        alarmsOnly.events = []
        let rows = ReportModel.cameraRows(alarmsOnly, names: [:])
        XCTAssertEqual(rows.map(\.topKind), [nil, nil], "no detections, no top kind")
        XCTAssertEqual(ReportModel.activeCameras(rows), 2)
    }

    func testSeverityTotalsOrderAndAdd() {
        var other = base
        other.alarms.append(V2.ActivityAlarm(cameraId: "c1", kind: "x", severity: "Notice", count: 9))
        XCTAssertEqual(ReportModel.severityTotals(other).map(\.severity), ["Critical", "Warning", "Info", "Notice"])
        XCTAssertEqual(ReportModel.severityTotals(base).first { $0.severity == "Info" }?.count, 2)
    }

    func testHourlySeriesFillsTheWholeRange() {
        XCTAssertEqual(ReportModel.series(base, bucket: .hour).map(\.count), [3, 6, 0])
        var late = base
        late.events = [V2.ActivityEvent(cameraId: "c1", kind: "Person", hour: 12 * hour, count: 2)]
        XCTAssertEqual(ReportModel.series(late, bucket: .hour).map(\.count), [0, 0, 2])
        XCTAssertEqual(ReportModel.series(late, bucket: .hour).map(\.at), [10 * hour, 11 * hour, 12 * hour])
    }

    func testDailySeriesUsesLocalDaysAcrossDaylightSaving() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = try XCTUnwrap(TimeZone(identifier: "America/New_York"))
        // 2026-11-01 is the US fall-back day (25 hours long).
        let start = try XCTUnwrap(cal.date(from: DateComponents(year: 2026, month: 10, day: 31, hour: 12)))
        let end = try XCTUnwrap(cal.date(from: DateComponents(year: 2026, month: 11, day: 2, hour: 12)))
        let nov1Late = try XCTUnwrap(cal.date(from: DateComponents(year: 2026, month: 11, day: 1, hour: 23)))
        let a = V2.Activity(from: start.unixMillis, to: end.unixMillis, totals: V2.ActivityTotals(events: 4, alarms: 0),
                            events: [V2.ActivityEvent(cameraId: "c1", kind: "Person", hour: nov1Late.unixMillis, count: 4)],
                            alarms: [])
        let bars = ReportModel.series(a, bucket: .day, calendar: cal)
        XCTAssertEqual(bars.map(\.count), [0, 4, 0])
        XCTAssertEqual(bars.map { cal.component(.day, from: Date(unixMillis: $0.at)) }, [31, 1, 2])
        XCTAssertTrue(bars.allSatisfy { cal.component(.hour, from: Date(unixMillis: $0.at)) == 0 }, "every bar starts at midnight")
    }

    func testRangesAndKindLabel() {
        XCTAssertEqual(ReportRange.allCases.map(\.rawValue), ["24h", "7d", "31d"], "the web's ids")
        XCTAssertEqual(ReportRange.default, .day)
        XCTAssertEqual(ReportRange.day.bucket, .hour)
        XCTAssertEqual(ReportRange.week.bucket, .day)
        let now = Date(unixMillis: 1_760_000_000_000)
        let month = ReportRange.month.bounds(now: now)
        XCTAssertEqual(month.to, now.unixMillis)
        XCTAssertEqual(month.to - month.from, 31 * 86_400_000, "the server's 31-day cap")
        XCTAssertEqual(ReportModel.kindLabel(""), "Other")
        XCTAssertEqual(ReportModel.kindLabel("Person"), "Person")
    }

    func testActivityReportCallsTheEndpointAndToleratesNullLists() async throws {
        MockURLProtocol.reset()
        defer { MockURLProtocol.reset() }
        MockURLProtocol.handler = { _, _ in
            .json(200, """
            {"from":1,"to":2,"totals":{"events":3,"alarms":0},
             "events":[{"cameraId":"c1","kind":"Person","hour":0,"count":3}],"alarms":null}
            """)
        }
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://nvr.test:8090")!, siteID: "s1", tokenStore: store,
                              session: MockURLProtocol.session(), clientLabel: "Test")
        let report = try await api.activityReport(from: 100, to: 200)
        let url = try XCTUnwrap(MockURLProtocol.requests.first?.request.url)
        XCTAssertEqual(url.path, "/api/v2/reports/activity")
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        XCTAssertEqual(items.first { $0.name == "from" }?.value, "100")
        XCTAssertEqual(items.first { $0.name == "to" }?.value, "200")
        XCTAssertEqual(report.totals.events, 3)
        XCTAssertEqual(report.events.first?.kind, "Person")
        XCTAssertEqual(report.alarms, [])
    }
}
