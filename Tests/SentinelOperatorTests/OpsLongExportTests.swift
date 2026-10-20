import CoreGraphics
import XCTest
@testable import SentinelCore

/// Wave 11 stream B: long exports, export options, scheduled exports,
/// share links and evidence packages (pure logic and API paths).
final class OpsLongExportTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!
    private let now: Int64 = 1_760_000_000_000
    private let hour: Int64 = 3_600_000

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

    private func body(_ index: Int? = nil) throws -> [String: Any] {
        let req = try XCTUnwrap(index.map { MockURLProtocol.requests[$0] } ?? MockURLProtocol.requests.last)
        return try XCTUnwrap(JSONSerialization.jsonObject(with: XCTUnwrap(req.body)) as? [String: Any])
    }

    private func job(_ state: V2.ExportJobState, id: String = "t1", dest: V2.ExportDest = .download, progress: Int = 0) -> V2.ExportJob {
        V2.ExportJob(taskId: id, state: state, progress: progress, cameraId: "c1", fromMs: 1, toMs: 2, dest: dest)
    }

    // MARK: Long export validation

    func testLongExportBlockerMatchesWebMessages() {
        XCTAssertEqual(LongExportRules.blocker(cameraID: nil, from: now - hour, to: now, now: now), "Pick a camera.")
        XCTAssertEqual(LongExportRules.blocker(cameraID: "", from: now - hour, to: now, now: now), "Pick a camera.")
        XCTAssertEqual(LongExportRules.blocker(cameraID: "c1", from: 0, to: now, now: now), "Pick a start and an end.")
        XCTAssertEqual(LongExportRules.blocker(cameraID: "c1", from: now, to: now, now: now), "The end must be after the start.")
        XCTAssertEqual(LongExportRules.blocker(cameraID: "c1", from: now + hour, to: now + 2 * hour, now: now), "The start is in the future.")
        XCTAssertEqual(LongExportRules.blocker(cameraID: "c1", from: now - 12 * hour - 1, to: now, now: now), "Long exports are limited to 12 hours.")
        XCTAssertNil(LongExportRules.blocker(cameraID: "c1", from: now - 12 * hour, to: now, now: now))
    }

    func testSpanText() {
        XCTAssertEqual(LongExportRules.spanText(45 * 60_000), "45 min")
        XCTAssertEqual(LongExportRules.spanText(5 * hour + 30 * 60_000), "5 h 30 min")
        XCTAssertEqual(LongExportRules.spanText(12 * hour), "12 h")
        XCTAssertEqual(LongExportRules.spanText(-5), "0 min")
    }

    func testRequestDropsTitleForDownloadsAndOmitsOffOptions() {
        let options = ExportOptions(watermark: true, regions: [.init(x0: 0, y0: 0, x1: 0.5, y1: 0.5)])
        let vault = LongExportRules.request(cameraID: "c1", from: 1, to: 2, dest: .evidence, title: "  Night  ", note: " ", options: options)
        XCTAssertEqual(vault.title, "Night")
        XCTAssertNil(vault.note)
        XCTAssertEqual(vault.watermark, true)
        XCTAssertEqual(vault.blurRegions?.count, 1)
        let download = LongExportRules.request(cameraID: "c1", from: 1, to: 2, dest: .download, title: "x", note: "y", options: .none)
        XCTAssertNil(download.title)
        XCTAssertNil(download.note)
        XCTAssertNil(download.watermark)
        XCTAssertNil(download.blurRegions)
        let long = LongExportRules.request(cameraID: "c1", from: 1, to: 2, dest: .evidence, title: String(repeating: "t", count: 300), note: "", options: .none)
        XCTAssertEqual(long.title?.count, 120)
    }

    func testJobTextAndSave() {
        XCTAssertEqual(LongExportRules.jobText(job(.running, progress: 42)), "Exporting… 42%")
        var filed = job(.done, dest: .evidence)
        XCTAssertEqual(LongExportRules.jobText(filed), "Filed as evidence.")
        filed.caseId = "EV-7"
        XCTAssertEqual(LongExportRules.jobText(filed), "Filed as evidence EV-7.")
        XCTAssertEqual(LongExportRules.jobText(job(.done)), "Ready to download.")
        XCTAssertEqual(LongExportRules.jobText(job(.canceled)), "Canceled.")
        XCTAssertEqual(LongExportRules.jobText(job(.failed)), "Failed: unknown error")
        var failed = job(.failed)
        failed.error = "no recording in the range"
        XCTAssertEqual(LongExportRules.jobText(failed), "Failed: no recording in the range")
        XCTAssertTrue(LongExportRules.canSave(job(.done)))
        XCTAssertFalse(LongExportRules.canSave(job(.done, dest: .evidence)))
        XCTAssertFalse(LongExportRules.canSave(job(.running)))
        var named = job(.done)
        XCTAssertEqual(LongExportRules.suggestedFilename(named, cameraName: "Front/Door"), "Front-Door-1.mp4")
        named.filename = "front-20261018.mp4"
        XCTAssertEqual(LongExportRules.suggestedFilename(named, cameraName: "x"), "front-20261018.mp4")
        named.filename = "../../etc/x.mp4"
        XCTAssertFalse(LongExportRules.suggestedFilename(named, cameraName: "x").contains("/"))
    }

    func testJobListUpsertsAndReportsFinished() {
        var list = ExportJobList()
        list.upsert(job(.running, id: "a"))
        list.upsert(job(.running, id: "b"))
        XCTAssertEqual(list.jobs.map(\.taskId), ["b", "a"])
        XCTAssertTrue(list.hasRunning)
        let before = list
        list.upsert(job(.done, id: "a"))
        XCTAssertEqual(list.jobs.map(\.taskId), ["b", "a"], "an update keeps its place")
        XCTAssertEqual(list.finished(since: before).map(\.taskId), ["a"])
        XCTAssertEqual(list.running.map(\.taskId), ["b"])
        list.clearFinished()
        XCTAssertEqual(list.jobs.map(\.taskId), ["b"])
        for i in 0..<30 { list.upsert(job(.done, id: "x\(i)")) }
        XCTAssertEqual(list.jobs.count, ExportJobList.limit)
        list.remove(taskID: "x29")
        XCTAssertFalse(list.jobs.contains { $0.taskId == "x29" })
    }

    // MARK: Export options

    func testRegionRectNormalizeAndValidation() {
        let r = ExportRegionRules.rect(from: CGPoint(x: 0.6, y: 0.9), to: CGPoint(x: 0.123456, y: 0.2))
        XCTAssertEqual(r, V2.ExportRegion(x0: 0.1235, y0: 0.2, x1: 0.6, y1: 0.9))
        XCTAssertNil(ExportRegionRules.problem(r))
        XCTAssertEqual(ExportRegionRules.problem(.init(x0: 0.5, y0: 0.5, x1: 0.505, y1: 0.9)), "The region is too small.")
        XCTAssertEqual(ExportRegionRules.problem(.init(x0: -0.1, y0: 0, x1: 0.5, y1: 0.5)), "The region is outside the picture.")
        XCTAssertEqual(ExportRegionRules.problem(.init(x0: 0, y0: 0, x1: .nan, y1: 0.5)), "The region is outside the picture.")
        XCTAssertNil(ExportRegionRules.problem(.init(x0: 0, y0: 0, x1: 0.01, y1: 0.01)))
        XCTAssertEqual(ExportRegionRules.normalize(CGPoint(x: 50, y: -10), in: CGSize(width: 200, height: 100)), CGPoint(x: 0.25, y: 0))
        XCTAssertEqual(ExportRegionRules.normalize(CGPoint(x: 500, y: 50), in: CGSize(width: 200, height: 100)), CGPoint(x: 1, y: 0.5))
        XCTAssertEqual(ExportRegionRules.viewRect(.init(x0: 0.25, y0: 0.5, x1: 0.75, y1: 1), in: CGSize(width: 200, height: 100)),
                       CGRect(x: 50, y: 50, width: 100, height: 50))
    }

    func testAddRemoveRegionsCapsAtEight() {
        let good = V2.ExportRegion(x0: 0, y0: 0, x1: 0.2, y1: 0.2)
        var list: [V2.ExportRegion] = []
        list = ExportRegionRules.add(.init(x0: 0.1, y0: 0.1, x1: 0.1, y1: 0.1), to: list)
        XCTAssertTrue(list.isEmpty, "a click is not a region")
        for _ in 0..<10 { list = ExportRegionRules.add(good, to: list) }
        XCTAssertEqual(list.count, 8)
        XCTAssertNil(ExportRegionRules.problem(list))
        XCTAssertEqual(ExportRegionRules.problem(list + [good]), "At most 8 regions.")
        XCTAssertEqual(ExportRegionRules.remove(at: 0, from: list).count, 7)
        XCTAssertEqual(ExportRegionRules.remove(at: 99, from: list).count, 8)
        XCTAssertEqual(ExportRegionRules.label(.init(x0: 0, y0: 0, x1: 0.25, y1: 0.1), index: 0), "Region 1: 25% × 10% of the picture")
        XCTAssertEqual(ExportRegionRules.applyTitle(count: 0), "Use No Regions")
        XCTAssertEqual(ExportRegionRules.applyTitle(count: 1), "Use 1 Region")
        XCTAssertEqual(ExportRegionRules.applyTitle(count: 3), "Use 3 Regions")
    }

    func testOptionsSummaryAndAspect() {
        XCTAssertEqual(ExportOptions.none.summary, "")
        XCTAssertNil(ExportOptions.none.note)
        XCTAssertFalse(ExportOptions.none.reencodes)
        let both = ExportOptions(watermark: true, regions: [.init(x0: 0, y0: 0, x1: 1, y1: 1), .init(x0: 0, y0: 0, x1: 1, y1: 1)])
        XCTAssertEqual(both.summary, "watermark, 2 pixelated regions")
        XCTAssertEqual(both.note, "watermark, 2 pixelated regions: the clip is re-encoded (it is no longer the camera's own video), so it takes longer.")
        XCTAssertEqual(both.regionsButtonTitle, "Pixelate Regions… (2)")
        XCTAssertEqual(ExportOptions(regions: [.init(x0: 0, y0: 0, x1: 1, y1: 1)]).summary, "1 pixelated region")
        XCTAssertEqual(ExportRegionRules.aspect(width: nil, height: nil), 16.0 / 9.0, accuracy: 1e-9)
        XCTAssertEqual(ExportRegionRules.aspect(width: 1600, height: 1200), 4.0 / 3.0, accuracy: 1e-9)
        XCTAssertEqual(ExportRegionRules.aspect(width: 0, height: 1080), 16.0 / 9.0, accuracy: 1e-9)
    }

    // MARK: Schedules

    func testClockAndWindowOvernightRule() {
        XCTAssertEqual(ScheduleRules.clockMinutes("06:30"), 390)
        XCTAssertNil(ScheduleRules.clockMinutes("6:30"))
        XCTAssertNil(ScheduleRules.clockMinutes("06:60"))
        XCTAssertNil(ScheduleRules.clockMinutes("24:00"))
        XCTAssertEqual(ScheduleRules.clockMinutes("24:00", allow24: true), 1440)
        XCTAssertNil(ScheduleRules.clockMinutes("24:01", allow24: true))
        XCTAssertNil(ScheduleRules.clockMinutes("ab:cd"))
        XCTAssertEqual(ScheduleRules.windowMinutes(start: "22:00", end: "06:00"), 480)
        XCTAssertEqual(ScheduleRules.windowMinutes(start: "08:00", end: "17:30"), 570)
        XCTAssertEqual(ScheduleRules.windowMinutes(start: "12:00", end: "24:00"), 720)
        XCTAssertNil(ScheduleRules.windowMinutes(start: "08:00", end: "08:00"))
        XCTAssertTrue(ScheduleRules.isOvernight(start: "22:00", end: "06:00"))
        XCTAssertFalse(ScheduleRules.isOvernight(start: "08:00", end: "24:00"))
        XCTAssertEqual(ScheduleRules.components("24:00")?.hour, 24)
        XCTAssertEqual(ScheduleRules.clock(hour: 7, minute: 5), "07:05")
    }

    func testScheduleBlockerMessages() {
        var d = ScheduleDraft.blank(cameraID: "c1")
        d.name = "Night"
        XCTAssertNil(ScheduleRules.blocker(d))
        var e = d; e.name = "  "
        XCTAssertEqual(ScheduleRules.blocker(e), "Give the schedule a name.")
        e = d; e.cameraID = ""
        XCTAssertEqual(ScheduleRules.blocker(e), "Pick a camera.")
        e = d; e.days = []
        XCTAssertEqual(ScheduleRules.blocker(e), "Pick at least one day.")
        e = d; e.end = "22:00"
        XCTAssertEqual(ScheduleRules.blocker(e), "Enter a start and an end time that differ.")
        e = d; e.start = "08:00"; e.end = "20:01"
        XCTAssertEqual(ScheduleRules.blocker(e), "A window can cover at most 12 hours.")
        e = d; e.start = "20:00"; e.end = "08:00"
        XCTAssertNil(ScheduleRules.blocker(e), "exactly 12 h overnight is fine")
    }

    func testDaysWindowAndLastRunText() {
        XCTAssertEqual(ScheduleRules.daysText(Array(0...6)), "Every day")
        XCTAssertEqual(ScheduleRules.daysText([5, 1, 2, 3, 4]), "Weekdays")
        XCTAssertEqual(ScheduleRules.daysText([6, 0]), "Weekends")
        XCTAssertEqual(ScheduleRules.daysText([1, 3, 5, 3]), "Mon, Wed, Fri")
        var s = V2.ExportSchedule(id: "s1", name: "N", cameraId: "c1", days: [1, 3], start: "22:00", end: "06:00")
        XCTAssertEqual(ScheduleRules.windowText(s), "Mon, Wed · 22:00 → 06:00 (next day)")
        XCTAssertEqual(ScheduleRules.lastRunText(s), "Never")
        s.lastRunAt = 5
        s.lastStatus = "ok"
        XCTAssertEqual(ScheduleRules.lastRunText(s, format: { "T\($0)" }), "OK — T5")
        s.lastStatus = "running"
        XCTAssertEqual(ScheduleRules.lastRunText(s, format: { "T\($0)" }), "Running — T5")
        s.lastStatus = "failed: nothing was recorded in the window"
        XCTAssertEqual(ScheduleRules.lastRunText(s, format: { "T\($0)" }), "Failed — nothing was recorded in the window")
        XCTAssertTrue(ScheduleRules.lastRunFailed(s))
        s.lastStatus = "failed:"
        XCTAssertEqual(ScheduleRules.lastRunText(s, format: { "T\($0)" }), "Failed — see the server log")
    }

    func testDraftToggleInputAndPatch() {
        var d = ScheduleDraft(name: " Night ", cameraID: "c1", days: [1], start: "22:00", end: "06:00", enabled: false)
        d.toggle(day: 0)
        d.toggle(day: 1)
        d.toggle(day: 9)
        XCTAssertEqual(d.days, [0])
        let input = d.input
        XCTAssertEqual(input.name, "Night")
        XCTAssertEqual(input.dest, .evidence)
        XCTAssertFalse(input.enabled)
        XCTAssertEqual(d.patch.days, [0])
        let back = ScheduleDraft(V2.ExportSchedule(id: "s", name: "A", cameraId: "c2", days: [2], start: "01:00", end: "02:00", enabled: false))
        XCTAssertEqual(back.cameraID, "c2")
        XCTAssertFalse(back.enabled)
    }

    // MARK: Share links

    func testShareBlockerAndRequest() {
        XCTAssertNil(ShareRules.blocker(preset: .oneDay, customAt: 0, maxUses: "", now: now))
        XCTAssertEqual(ShareRules.blocker(preset: .custom, customAt: 0, maxUses: "", now: now), "Pick an expiry date and time.")
        XCTAssertEqual(ShareRules.blocker(preset: .custom, customAt: now, maxUses: "", now: now), "The expiry must be in the future.")
        XCTAssertEqual(ShareRules.blocker(preset: .custom, customAt: now + 31 * 24 * hour, maxUses: "", now: now), "A link can last at most 30 days.")
        XCTAssertNil(ShareRules.blocker(preset: .custom, customAt: now + hour, maxUses: "5", now: now))
        for bad in ["-1", "1001", "2.5", "x"] {
            XCTAssertEqual(ShareRules.blocker(preset: .oneHour, customAt: 0, maxUses: bad, now: now), "Max uses must be a whole number from 0 to 1000.", bad)
        }
        let r = ShareRules.request(preset: .custom, customAt: 99, maxUses: " 3 ", note: "  for police ")
        XCTAssertEqual(r, V2.ShareLinkRequest(expiry: "custom", expiresAt: 99, maxUses: 3, note: "for police"))
        XCTAssertEqual(ShareRules.request(preset: .sevenDays, customAt: 99, maxUses: "", note: ""),
                       V2.ShareLinkRequest(expiry: "7d", expiresAt: nil, maxUses: 0, note: nil))
        XCTAssertEqual(SharePreset.allCases.map(\.label), ["1 hour", "24 hours", "7 days", "Custom…"])
    }

    func testShareUsesSortAndPackageName() {
        let a = V2.ShareLink(id: "a", evidenceId: "e", caseId: "EV-1", expiresAt: 1, maxUses: 5, uses: 3, createdAt: 10, status: .expired)
        let b = V2.ShareLink(id: "b", evidenceId: "e", caseId: "EV-1", expiresAt: 1, maxUses: 0, uses: 2, createdAt: 5, status: .active)
        let c = V2.ShareLink(id: "c", evidenceId: "e", caseId: "EV-1", expiresAt: 1, createdAt: 20, status: .revoked)
        XCTAssertEqual(ShareRules.usesText(a), "3 of 5")
        XCTAssertEqual(ShareRules.usesText(b), "2")
        XCTAssertEqual(ShareRules.sorted([a, b, c]).map(\.id), ["b", "c", "a"])
        XCTAssertTrue(ShareRules.canRevoke(b))
        XCTAssertFalse(ShareRules.canRevoke(a))
        XCTAssertEqual(EvidencePackageRules.defaultName(caseID: "EV-2026-0042"), "EV-2026-0042-package.zip")
        XCTAssertEqual(EvidencePackageRules.defaultName(caseID: "a/b"), "a-b-package.zip")
        XCTAssertEqual(EvidencePackageRules.failureText(SentinelAPIError.server(status: 409, code: "conflict", message: "")),
                       "The vault file no longer matches its recorded SHA-256, so it can't be packaged.")
        XCTAssertEqual(EvidencePackageRules.failureText(SentinelAPIError.server(status: 409, code: "conflict", message: "hash mismatch")), "hash mismatch")
    }

    func testShareURLCacheOnlyOffersActiveLinks() {
        var cache = ShareLinkURLCache()
        var link = V2.ShareLink(id: "l1", evidenceId: "e", caseId: "EV-1", expiresAt: 1)
        cache.remember(id: "l1", url: "http://x/share/t")
        cache.remember(id: "", url: "http://x/share/u")
        XCTAssertEqual(cache.url(for: link), "http://x/share/t")
        link.status = .revoked
        XCTAssertNil(cache.url(for: link))
        cache.prune(keeping: [link])
        XCTAssertEqual(cache.count, 0)
        cache.remember(id: "l2", url: "u")
        cache.forget(id: "l2")
        XCTAssertEqual(cache.count, 0)
    }

    // MARK: API

    private static let jobJSON = #"{"taskId":"t1","state":"running","progress":10,"cameraId":"c1","fromMs":1,"toMs":2,"dest":"download"}"#

    func testStartExportJobPostsOptionsAndDecodes() async throws {
        MockURLProtocol.handler = { _, _ in .json(201, Self.jobJSON) }
        let request = LongExportRules.request(cameraID: "c1", from: 1, to: 2, dest: .evidence, title: "Night", note: "",
                                              options: ExportOptions(watermark: true, regions: [.init(x0: 0.1, y0: 0.2, x1: 0.3, y1: 0.4)]))
        let job = try await makeAPI().startExportJob(request)
        XCTAssertEqual(job.state, .running)
        XCTAssertEqual(job.progress, 10)
        let req = try XCTUnwrap(MockURLProtocol.requests.last)
        XCTAssertEqual(req.request.httpMethod, "POST")
        XCTAssertEqual(req.request.url?.path, "/api/v2/export-jobs")
        let b = try body()
        XCTAssertEqual(b["dest"] as? String, "evidence")
        XCTAssertEqual(b["title"] as? String, "Night")
        XCTAssertNil(b["note"])
        XCTAssertEqual(b["watermark"] as? Bool, true)
        let regions = try XCTUnwrap(b["blurRegions"] as? [[String: Double]])
        XCTAssertEqual(regions.first?["y1"], 0.4)
    }

    func testPlainJobOmitsOptionsAndPollsCancels() async throws {
        let api = makeAPI()
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("POST", "/api/v2/export-jobs"): return .json(201, Self.jobJSON)
            case ("GET", "/api/v2/export-jobs/t1"):
                return .json(200, #"{"taskId":"t1","state":"done","progress":100,"cameraId":"c1","fromMs":1,"toMs":2,"dest":"download","filename":"f.mp4"}"#)
            case ("DELETE", "/api/v2/export-jobs/t1"):
                return .json(200, #"{"taskId":"t1","state":"canceled","progress":40,"cameraId":"c1","fromMs":1,"toMs":2,"dest":"download"}"#)
            default: return .json(404, #"{"error":{"code":"not_found","message":"no"}}"#)
            }
        }
        _ = try await api.startExportJob(.init(cameraId: "c1", fromMs: 1, toMs: 2, dest: .download))
        let b = try body(0)
        XCTAssertNil(b["watermark"])
        XCTAssertNil(b["blurRegions"])
        XCTAssertNil(b["title"])
        let polled = try await api.exportJob(taskID: "t1")
        XCTAssertEqual(polled.filename, "f.mp4")
        XCTAssertTrue(LongExportRules.canSave(polled))
        let canceled = try await api.cancelExportJob(taskID: "t1")
        XCTAssertEqual(canceled.state, .canceled)
    }

    func testOverLimitJobSurfacesServerMessage() async {
        MockURLProtocol.handler = { _, _ in .json(400, #"{"error":{"code":"bad_request","message":"a long export can cover at most 12 hours"}}"#) }
        do {
            _ = try await makeAPI().startExportJob(.init(cameraId: "c1", fromMs: 0, toMs: 99_999_999, dest: .download))
            XCTFail("expected 400")
        } catch {
            XCTAssertEqual(ExportRules.failureText(error), "a long export can cover at most 12 hours")
        }
    }

    func testEvidenceWithOptionsPostsToEvidence() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(201, #"{"id":"e1","caseId":"EV-1","cameraId":"c1","cameraName":"F","size":1,"sha256":"a","lockedAt":1,"lockedBy":"m"}"#)
        }
        let made = try await makeAPI().createEvidence(withOptions: .init(cameraId: "c1", fromMs: 1, toMs: 2, title: "T", watermark: true))
        XCTAssertEqual(made.caseId, "EV-1")
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.path, "/api/v2/evidence")
        let b = try body()
        XCTAssertEqual(b["watermark"] as? Bool, true)
        XCTAssertNil(b["blurRegions"])
    }

    func testJobAndPackageDownloadsStreamToFile() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertNotNil(request.value(forHTTPHeaderField: "Authorization"))
            switch request.url?.path ?? "" {
            case "/api/v2/export-jobs/t1/download": return MockURLProtocol.Reply(status: 200, body: Data("mp4".utf8), headers: ["Content-Type": "video/mp4"])
            case "/api/v2/evidence/e1/package": return MockURLProtocol.Reply(status: 200, body: Data("PK".utf8), headers: ["Content-Type": "application/zip"])
            default: return .json(409, #"{"error":{"code":"conflict","message":"the file no longer matches its hash"}}"#)
            }
        }
        let api = makeAPI()
        let dir = FileManager.default.temporaryDirectory
        let clip = dir.appendingPathComponent("job-\(UUID().uuidString).mp4")
        let zip = dir.appendingPathComponent("pkg-\(UUID().uuidString).zip")
        defer {
            try? FileManager.default.removeItem(at: clip)
            try? FileManager.default.removeItem(at: zip)
        }
        try await api.downloadExportJob(taskID: "t1", to: clip, session: MockURLProtocol.session())
        XCTAssertEqual(try Data(contentsOf: clip), Data("mp4".utf8))
        try await api.downloadEvidencePackage(id: "e1", to: zip, session: MockURLProtocol.session())
        XCTAssertEqual(try Data(contentsOf: zip), Data("PK".utf8))
        do {
            try await api.downloadEvidencePackage(id: "bad", to: zip, session: MockURLProtocol.session())
            XCTFail("expected 409")
        } catch {
            XCTAssertEqual(EvidencePackageRules.failureText(error), "the file no longer matches its hash")
            XCTAssertEqual(try Data(contentsOf: zip), Data("PK".utf8), "a refused download leaves the earlier file alone")
        }
    }

    func testScheduleCRUD() async throws {
        let api = makeAPI()
        let schedule = #"{"id":"s1","name":"Night","cameraId":"c1","days":[0,6],"start":"22:00","end":"06:00","dest":"evidence","enabled":true,"createdBy":"m","createdAt":1,"lastRunAt":0,"lastStatus":""}"#
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("GET", "/api/v2/export-schedules"): return .json(200, "{\"items\":[\(schedule)]}")
            case ("POST", "/api/v2/export-schedules"): return .json(201, schedule)
            case ("PATCH", "/api/v2/export-schedules/s1"): return .json(200, schedule)
            case ("DELETE", "/api/v2/export-schedules/s1"): return MockURLProtocol.Reply(status: 204, body: Data())
            default: return .json(404, #"{"error":{"code":"not_found","message":"no"}}"#)
            }
        }
        let list = try await api.exportSchedules()
        XCTAssertEqual(list.first?.days, [0, 6])
        XCTAssertEqual(ScheduleRules.daysText(list[0].days), "Weekends")
        var draft = ScheduleDraft.blank(cameraID: "c1")
        draft.name = "Night"
        _ = try await api.createExportSchedule(draft.input)
        let created = try body()
        XCTAssertEqual(created["name"] as? String, "Night")
        XCTAssertEqual(created["dest"] as? String, "evidence")
        XCTAssertEqual(created["days"] as? [Int], [0, 1, 2, 3, 4, 5, 6])
        _ = try await api.patchExportSchedule(id: "s1", .init(enabled: false))
        let patched = try body()
        XCTAssertEqual(patched.count, 1, "a toggle sends only enabled")
        XCTAssertEqual(patched["enabled"] as? Bool, false)
        try await api.deleteExportSchedule(id: "s1")
        XCTAssertEqual(MockURLProtocol.requests.last?.request.httpMethod, "DELETE")
    }

    func testShareCreateListRevoke() async throws {
        let api = makeAPI()
        let link = #"{"id":"l1","evidenceId":"e1","caseId":"EV-1","title":"T","note":"","expiresAt":9,"maxUses":0,"uses":0,"createdBy":"m","createdAt":1,"revokedAt":0,"status":"active"}"#
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("POST", "/api/v2/evidence/e1/share"): return .json(201, "{\"link\":\(link),\"token\":\"tok\",\"url\":\"http://nvr.test/share/tok\"}")
            case ("GET", "/api/v2/share-links"): return .json(200, "{\"items\":[\(link)]}")
            case ("DELETE", "/api/v2/share-links/l1"): return MockURLProtocol.Reply(status: 204, body: Data())
            default: return .json(404, #"{"error":{"code":"not_found","message":"no"}}"#)
            }
        }
        let made = try await api.shareEvidence(id: "e1", ShareRules.request(preset: .oneHour, customAt: 0, maxUses: "", note: ""))
        XCTAssertEqual(made.url, "http://nvr.test/share/tok")
        let b = try body()
        XCTAssertEqual(b["expiry"] as? String, "1h")
        XCTAssertEqual(b["maxUses"] as? Int, 0)
        XCTAssertNil(b["expiresAt"])
        let links = try await api.shareLinks()
        XCTAssertEqual(links.map(\.status), [.active])
        try await api.revokeShareLink(id: "l1")
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.path, "/api/v2/share-links/l1")
        let usedUp = try SentinelAPI.decode(V2.ShareLink.self, from: Data(#"{"id":"x","status":"used up","expiresAt":1}"#.utf8))
        XCTAssertEqual(usedUp.status, .usedUp)
    }
}
