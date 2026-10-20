import CoreGraphics
import XCTest
@testable import SentinelCore

/// Wave 11 stream A: bookmarks, privacy-mask geometry and edge recovery.
final class PlaybackWave11Tests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!
    private let m: Int64 = 60_000

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

    private func json(_ data: Data?) -> [String: Any] {
        guard let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return obj
    }

    private func bm(_ id: String, at: Int64, dur: Int64 = 0, created: Int64 = 0, camera: String = "c1", title: String = "") -> V2.Bookmark {
        V2.Bookmark(id: id, cameraId: camera, atMs: at, durationMs: dur, title: title, createdAt: created)
    }

    static let bookmarkJSON = """
    {"id":"b1","cameraId":"c1","atMs":1760000000000,"durationMs":0,"title":"Van","note":"","color":"teal",
     "createdBy":"u1","createdAt":1760000001000,"canEdit":true}
    """

    // MARK: Bookmark API

    func testBookmarksListSendsWindowAndDecodes() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"items":[\#(Self.bookmarkJSON)]}"#) }
        let list = try await makeAPI().bookmarks(cameraID: "c 1", from: 10, to: 20)
        XCTAssertEqual(list.count, 1)
        XCTAssertEqual(list[0].color, .teal)
        XCTAssertTrue(list[0].canEdit)
        let url = try XCTUnwrap(MockURLProtocol.requests.first?.request.url)
        XCTAssertEqual(url.path, "/api/v2/cameras/c 1/bookmarks")
        XCTAssertTrue(url.query?.contains("from=10") == true && url.query?.contains("to=20") == true, url.absoluteString)
    }

    func testBookmarksListWithoutBoundsSendsNoQuery() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"items":null}"#) }
        let list = try await makeAPI().bookmarks(cameraID: "c1")
        XCTAssertTrue(list.isEmpty)
        XCTAssertNil(MockURLProtocol.requests.first?.request.url?.query)
    }

    func testAddPatchDeleteBookmark() async throws {
        MockURLProtocol.handler = { request, _ in
            request.httpMethod == "DELETE" ? .json(204, "") : .json(request.httpMethod == "POST" ? 201 : 200, Self.bookmarkJSON)
        }
        let api = makeAPI()
        _ = try await api.addBookmark(cameraID: "c1", V2.BookmarkInput(atMs: 5, durationMs: 0, title: "Van", color: "teal"))
        _ = try await api.patchBookmark(id: "b1", V2.BookmarkPatch(title: "Truck"))
        try await api.deleteBookmark(id: "b1")
        let reqs = MockURLProtocol.requests
        XCTAssertEqual(reqs.map { $0.request.httpMethod }, ["POST", "PATCH", "DELETE"])
        XCTAssertEqual(reqs[0].request.url?.path, "/api/v2/cameras/c1/bookmarks")
        XCTAssertEqual(json(reqs[0].body)["atMs"] as? Int, 5)
        XCTAssertEqual(json(reqs[0].body)["color"] as? String, "teal")
        XCTAssertEqual(reqs[1].request.url?.path, "/api/v2/bookmarks/b1")
        XCTAssertEqual(json(reqs[1].body).keys.sorted(), ["title"])
        XCTAssertEqual(reqs[2].request.url?.path, "/api/v2/bookmarks/b1")
    }

    func testBookmarkForbiddenSurfacesServerMessage() async {
        MockURLProtocol.handler = { _, _ in
            .json(403, #"{"error":{"code":"forbidden","message":"only the person who added a bookmark, or a manager, can change it"}}"#)
        }
        do {
            try await makeAPI().deleteBookmark(id: "b1")
            XCTFail("expected an error")
        } catch {
            XCTAssertTrue((error as? SentinelAPIError)?.isForbidden == true)
        }
    }

    func testBookmarkColorTokens() {
        XCTAssertEqual(V2.BookmarkColor(rawValue: "").resolved, .blue)
        XCTAssertEqual(V2.BookmarkColor(rawValue: "chartreuse").hex, 0x3B82F6)
        XCTAssertEqual(V2.BookmarkColor.red.hex, 0xEF4444)
        XCTAssertEqual(V2.BookmarkColor.all.count, 9)
    }

    // MARK: Bookmark logic

    func testSortUpsertRemoveAndWindow() {
        let list = BookmarkLogic.sorted([bm("c", at: 20), bm("b", at: 10, created: 2), bm("a", at: 10, created: 1)])
        XCTAssertEqual(list.map(\.id), ["a", "b", "c"])
        let moved = BookmarkLogic.upsert(list, bm("c", at: 5))
        XCTAssertEqual(moved.map(\.id), ["c", "a", "b"])
        XCTAssertEqual(BookmarkLogic.remove(moved, id: "a").map(\.id), ["c", "b"])
        let ranges = [bm("p", at: 50), bm("r", at: 10, dur: 30), bm("late", at: 200)]
        XCTAssertEqual(BookmarkLogic.inWindow(ranges, start: 35, end: 60).map(\.id), ["p", "r"])
        XCTAssertEqual(BookmarkLogic.inWindow(ranges, start: 41, end: 49).map(\.id), [])
    }

    func testLabelsAndDurations() {
        XCTAssertEqual(BookmarkLogic.label(bm("a", at: 1, title: "  ")), "Bookmark")
        XCTAssertEqual(BookmarkLogic.label(bm("a", at: 1, title: " Van ")), "Van")
        XCTAssertEqual(BookmarkLogic.durationText(0), "")
        XCTAssertEqual(BookmarkLogic.durationText(80_000), "1 min 20 s")
        XCTAssertEqual(BookmarkLogic.durationText(3_600_000), "1 h")
        XCTAssertEqual(BookmarkLogic.durationText(400), "0 s")
    }

    func testNewTimeUsesSelectionOnlyWhenAskedAndValid() {
        XCTAssertTrue(BookmarkLogic.newTime(at: 100, selection: (10, 50), useSelection: true) == (10, 40))
        XCTAssertTrue(BookmarkLogic.newTime(at: 100, selection: (10, 50), useSelection: false) == (100, 0))
        XCTAssertTrue(BookmarkLogic.newTime(at: 100, selection: nil, useSelection: true) == (100, 0))
        XCTAssertTrue(BookmarkLogic.newTime(at: 1, selection: (0, 2 * BookmarkLogic.maxDurationMs), useSelection: true)
            == (1, BookmarkLogic.maxDurationMs))
    }

    func testExportRangePadsPointsAndClampsToNow() {
        XCTAssertTrue(BookmarkLogic.exportRange(bm("p", at: 100_000), now: 1_000_000) == (70_000, 130_000))
        XCTAssertTrue(BookmarkLogic.exportRange(bm("p", at: 100_000), now: 110_000) == (70_000, 110_000))
        XCTAssertTrue(BookmarkLogic.exportRange(bm("r", at: 100_000, dur: 5000), now: 1_000_000) == (100_000, 105_000))
        XCTAssertTrue(BookmarkLogic.exportRange(bm("p", at: 10_000), now: 1_000_000) == (0, 40_000))
    }

    func testGlyphsAndHitTesting() {
        let list = [bm("p", at: 50), bm("r", at: 0, dur: 200), bm("out", at: 500)]
        let glyphs = BookmarkLogic.glyphs(list, from: 0, to: 100, width: 1000)
        XCTAssertEqual(glyphs.map(\.id), ["p", "r"])
        XCTAssertEqual(glyphs[0].x, 500)
        XCTAssertEqual(glyphs[1].x, 0)
        XCTAssertEqual(glyphs[1].x1, 1000) // clipped to the window
        XCTAssertEqual(BookmarkLogic.hit(glyphs, x: 503)?.id, "p") // the flag wins over the bar under it
        XCTAssertEqual(BookmarkLogic.hit(glyphs, x: 800)?.id, "r")
        XCTAssertNil(BookmarkLogic.hit(BookmarkLogic.glyphs([bm("p", at: 50)], from: 0, to: 100, width: 1000), x: 520))
        XCTAssertTrue(BookmarkLogic.glyphs(list, from: 10, to: 10, width: 100).isEmpty)
    }

    func testDraftBuildsPayloadsAndValidates() {
        var draft = BookmarkLogic.Draft(cameraID: "c1", at: 1000, selection: (500, 900))
        XCTAssertTrue(draft.useSelection)
        draft.title = "  Van  "
        draft.color = .green
        XCTAssertEqual(draft.input, V2.BookmarkInput(atMs: 500, durationMs: 400, title: "Van", note: "", color: "green"))
        draft.useSelection = false
        XCTAssertEqual(draft.input.durationMs, 0)
        XCTAssertEqual(draft.input.atMs, 1000)
        XCTAssertNil(draft.problem)
        draft.title = String(repeating: "x", count: 121)
        XCTAssertNotNil(draft.problem)
        XCTAssertFalse(BookmarkLogic.Draft(cameraID: "c1", at: 1, selection: (5, 5)).useSelection)
        XCTAssertNotNil(BookmarkLogic.Draft(cameraID: "", at: 1, selection: nil).problem)

        var edit = BookmarkLogic.Draft(editing: V2.Bookmark(id: "b1", cameraId: "c1", atMs: 10, durationMs: 0, title: "A", note: "n", color: V2.BookmarkColor(rawValue: "")))
        XCTAssertTrue(edit.isEditing)
        XCTAssertEqual(edit.color, .blue)
        edit.note = " moved "
        XCTAssertEqual(edit.patch, V2.BookmarkPatch(title: "A", note: "moved", color: "blue"))
    }

    func testBookmarkBook() {
        var book = BookmarkBook()
        book.replace(["c1": [bm("b", at: 20), bm("a", at: 10)], "c2": [bm("x", at: 15, camera: "c2")]], from: 0, to: 100)
        XCTAssertEqual(book.bookmarks(for: "c1").map(\.id), ["a", "b"])
        XCTAssertEqual(book.all.map(\.id), ["a", "x", "b"])
        XCTAssertTrue(book.bookmarks(for: nil).isEmpty)
        book.upsert(bm("x", at: 5, camera: "c1"))
        XCTAssertEqual(book.bookmarks(for: "c1").map(\.id), ["x", "a", "b"])
        XCTAssertTrue(book.bookmarks(for: "c2").isEmpty)
        book.remove(id: "a")
        XCTAssertEqual(book.all.map(\.id), ["x", "b"])
        XCTAssertEqual(book.bookmark("b")?.atMs, 20)
    }

    // MARK: Mask geometry

    func testFrameSizeFallsBackTo1080p() {
        XCTAssertEqual(MaskGeometry.frameSize(width: 2560, height: 1440), CGSize(width: 2560, height: 1440))
        XCTAssertEqual(MaskGeometry.frameSize(width: nil, height: 1440), CGSize(width: 1920, height: 1080))
        XCTAssertEqual(MaskGeometry.frameSize(width: 0, height: 0), CGSize(width: 1920, height: 1080))
    }

    func testVideoRectLetterboxesAndFills() {
        let frame = CGSize(width: 1920, height: 1080)
        // A 4:3 tile letterboxes a 16:9 stream top and bottom.
        XCTAssertEqual(MaskGeometry.videoRect(container: CGSize(width: 400, height: 300), frame: frame, gravity: .fit),
                       CGRect(x: 0, y: 37.5, width: 400, height: 225))
        // A wide tile pillarboxes.
        XCTAssertEqual(MaskGeometry.videoRect(container: CGSize(width: 1000, height: 270), frame: frame, gravity: .fit),
                       CGRect(x: 260, y: 0, width: 480, height: 270))
        // Fill overflows on the long side, centered.
        let fill = MaskGeometry.videoRect(container: CGSize(width: 400, height: 300), frame: frame, gravity: .fill)
        XCTAssertEqual(fill.height, 300, accuracy: 0.001)
        XCTAssertEqual(fill.width, 533.333, accuracy: 0.001)
        XCTAssertEqual(fill.minX, -66.666, accuracy: 0.001)
        XCTAssertEqual(MaskGeometry.videoRect(container: .zero, frame: frame, gravity: .fit), .zero)
    }

    func testPolygonsArePlacedInTheVideoRect() {
        let masks: [[[Double]]] = [[[0, 0], [1, 0], [1, 1], [0, 1]], [[0.5, 0.5], [0.6]], [[-1, 0.5], [2, 0.5], [0.5, 1.5]]]
        let polys = MaskGeometry.polygons(masks)
        XCTAssertEqual(polys.count, 2) // the two-vertex one is dropped
        XCTAssertEqual(polys[1], [CGPoint(x: 0, y: 0.5), CGPoint(x: 1, y: 0.5), CGPoint(x: 0.5, y: 1)]) // clamped
        let placed = MaskGeometry.layout(masks: masks, container: CGSize(width: 400, height: 300),
                                         frame: CGSize(width: 1920, height: 1080), gravity: .fit)
        XCTAssertEqual(placed[0], [CGPoint(x: 0, y: 37.5), CGPoint(x: 400, y: 37.5), CGPoint(x: 400, y: 262.5), CGPoint(x: 0, y: 262.5)])
        XCTAssertTrue(MaskGeometry.polygons(nil).isEmpty)
    }

    func testCameraWithMasksDecodes() throws {
        let text = Fixtures.camera.replacingOccurrences(of: #""groupIds":null,"#,
                                                        with: #""groupIds":null,"masks":[[[0.1,0.2],[0.3,0.2],[0.3,0.4]]],"edgeRecording":true,"#)
        let camera = try SentinelAPI.decode(V2.Camera.self, from: Data(text.utf8))
        XCTAssertEqual(camera.masks?.first?.count, 3)
        XCTAssertEqual(camera.edgeRecording, true)
        XCTAssertEqual(MaskGeometry.polygons(camera.masks).count, 1)
    }

    // MARK: Edge recovery

    private var spans: [V2.Span] {
        [V2.Span(start: 0, end: 10 * m), V2.Span(start: 10 * m, end: 15 * m, edge: true), V2.Span(start: 15 * m, end: 20 * m, edge: true),
         V2.Span(start: 30 * m, end: 40 * m), V2.Span(start: 40 * m + 5000, end: 50 * m, edge: false)]
    }

    private var ranges: [TimelineGaps.Range] { spans.map { TimelineGaps.Range(start: $0.start, end: $0.end) } }
    private func r(_ a: Int64, _ b: Int64) -> TimelineGaps.Range { TimelineGaps.Range(start: a, end: b) }

    func testEdgeRangesAndUncovered() {
        XCTAssertEqual(EdgeRecoveryLogic.edgeRanges(spans), [r(10 * m, 20 * m)])
        XCTAssertEqual(EdgeRecoveryLogic.edgeRanges([]), [])
        XCTAssertEqual(EdgeRecoveryLogic.uncoveredMs(ranges, r(0, 20 * m)), 0)
        XCTAssertEqual(EdgeRecoveryLogic.uncoveredMs(ranges, r(15 * m, 35 * m)), 10 * m)
        XCTAssertEqual(EdgeRecoveryLogic.uncoveredMs([], r(0, m)), m)
    }

    func testGapsIgnoreKeyframeSlackAndTheFuture() {
        XCTAssertEqual(EdgeRecoveryLogic.gaps(ranges, window: r(0, 60 * m), now: 55 * m), [r(20 * m, 30 * m), r(50 * m, 55 * m)])
        XCTAssertEqual(EdgeRecoveryLogic.gaps(ranges, window: r(35 * m, 50 * m), now: 99 * m), []) // 5 s hole only
        XCTAssertEqual(EdgeRecoveryLogic.gaps(ranges, window: r(60 * m, 70 * m), now: 50 * m), [])
    }

    func testTargetPrefersSelectionThenWindowClamped() {
        let now: Int64 = 1_000_000_000_000
        XCTAssertEqual(EdgeRecoveryLogic.target(selection: r(15 * m, 35 * m), window: r(0, 60 * m), spans: ranges, now: now), r(15 * m, 35 * m))
        // A fully recorded selection falls back to the visible window.
        XCTAssertEqual(EdgeRecoveryLogic.target(selection: r(0, 10 * m), window: r(0, 60 * m), spans: ranges, now: now), r(0, 60 * m))
        // Clamped to now.
        XCTAssertEqual(EdgeRecoveryLogic.target(selection: nil, window: r(0, 60 * m), spans: ranges, now: 25 * m), r(0, 25 * m))
        // And to 24 h.
        XCTAssertEqual(EdgeRecoveryLogic.target(selection: r(0, 30 * 60 * m), window: r(0, m), spans: [], now: now), r(0, EdgeRecoveryLogic.maxRangeMs))
        // Nothing unrecorded: nothing to offer.
        XCTAssertNil(EdgeRecoveryLogic.target(selection: nil, window: r(0, 10 * m), spans: ranges, now: now))
    }

    func testOffersOnlyWithEdgeRecordingPermissionAndAGap() {
        XCTAssertTrue(EdgeRecoveryLogic.offers(edgeRecording: true, canExport: true, spans: ranges, window: r(0, 60 * m), now: 99 * m))
        XCTAssertFalse(EdgeRecoveryLogic.offers(edgeRecording: false, canExport: true, spans: ranges, window: r(0, 60 * m), now: 99 * m))
        XCTAssertFalse(EdgeRecoveryLogic.offers(edgeRecording: true, canExport: false, spans: ranges, window: r(0, 60 * m), now: 99 * m))
        XCTAssertFalse(EdgeRecoveryLogic.offers(edgeRecording: true, canExport: true, spans: ranges, window: r(0, 10 * m), now: 99 * m))
    }

    func testRangeProblems() {
        XCTAssertNil(EdgeRecoveryLogic.problem(r(0, m), now: 2 * m))
        XCTAssertNotNil(EdgeRecoveryLogic.problem(r(m, m), now: 2 * m))
        XCTAssertNotNil(EdgeRecoveryLogic.problem(r(3 * m, 4 * m), now: 2 * m))
        XCTAssertNotNil(EdgeRecoveryLogic.problem(r(0, EdgeRecoveryLogic.maxRangeMs + 1), now: .max))
    }

    func testJobTexts() {
        func job(_ state: V2.EdgeRecoveryState = .running, progress: Int = 0, error: String? = nil, gaps: Int = 1,
                 pieces: Int = 0, recovered: Int = 0, seconds: Double = 0) -> V2.EdgeRecovery {
            V2.EdgeRecovery(taskId: "t1", state: state, progress: progress, error: error, cameraId: "c", fromMs: 0, toMs: 1,
                            gaps: gaps, pieces: pieces, recovered: recovered, seconds: seconds)
        }
        XCTAssertTrue(EdgeRecoveryLogic.isActive(job()))
        XCTAssertFalse(EdgeRecoveryLogic.isActive(job(.done)))
        XCTAssertFalse(EdgeRecoveryLogic.isActive(nil))
        XCTAssertEqual(EdgeRecoveryLogic.text(job()), "Asking the camera what it recorded…")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(progress: 50, pieces: 4)), "Recovering from camera… 50%")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(.done, gaps: 0)), "No gaps in that range: nothing to recover.")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(.done, pieces: 2)), "The camera had no footage for those gaps.")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(.done, pieces: 2, recovered: 2, seconds: 1500)), "Recovered 25 min from the camera (2 segments).")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(.done, recovered: 1, seconds: 5400)), "Recovered 1.5 h from the camera (1 segment).")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(.failed, error: "camera unreachable")), "Recovery failed: camera unreachable")
        XCTAssertEqual(EdgeRecoveryLogic.text(job(.canceled)), "Recovery canceled.")
        XCTAssertEqual(EdgeRecoveryLogic.noticeKind(job(.failed)), .error)
        XCTAssertEqual(EdgeRecoveryLogic.noticeKind(job(.done, recovered: 1)), .success)
        XCTAssertEqual(EdgeRecoveryLogic.noticeKind(job(.done)), .info)
        XCTAssertTrue(EdgeRecoveryLogic.shouldReload(job(.canceled, recovered: 1)))
        XCTAssertFalse(EdgeRecoveryLogic.shouldReload(job(.done)))
        XCTAssertEqual(EdgeRecoveryLogic.counters(job(.done, recovered: 2, seconds: 30)).map(\.value), ["1", "0", "2", "0", "30 s"])
    }

    func testTitles() {
        XCTAssertTrue(EdgeRecoveryLogic.title(nil).contains("gap"))
        XCTAssertTrue(EdgeRecoveryLogic.title(r(0, 10 * m)).contains("10 min range"))
        XCTAssertTrue(EdgeRecoveryLogic.title(r(0, 90 * m)).contains("1.5 h range"))
    }

    func testStartErrorsUseTheServersWords() {
        let tooLong = SentinelAPIError.server(status: 400, code: "bad_request", message: "a recovery can cover at most 24 hours; choose a shorter range")
        XCTAssertEqual(EdgeRecoveryLogic.startError(tooLong), "A recovery can cover at most 24 hours; choose a shorter range.")
        let moved = SentinelAPIError.from(status: 409, body: Data())
        XCTAssertTrue(EdgeRecoveryLogic.startError(moved).contains("another recording server"))
        let busy = SentinelAPIError.from(status: 429, body: Data())
        XCTAssertEqual(EdgeRecoveryLogic.startError(busy), "A recovery is already running for this camera.")
        let busyServer = SentinelAPIError.server(status: 429, code: "too_many_requests", message: "a recovery is already running for this camera")
        XCTAssertEqual(EdgeRecoveryLogic.startError(busyServer), "A recovery is already running for this camera.")
        XCTAssertTrue(EdgeRecoveryLogic.startError(SentinelAPIError.from(status: 403, body: Data())).contains("role"))
    }

    func testEdgeRecoveryAPI() async throws {
        let body = #"{"taskId":"t 1","state":"running","progress":0,"cameraId":"c1","fromMs":1,"toMs":2,"gaps":3,"pieces":0,"recovered":0,"skipped":0,"seconds":0}"#
        MockURLProtocol.handler = { request, _ in .json(request.httpMethod == "POST" ? 201 : 200, body) }
        let api = makeAPI()
        let started = try await api.startEdgeRecovery(cameraID: "c1", from: 1, to: 2)
        XCTAssertEqual(started.gaps, 3)
        XCTAssertEqual(started.state, .running)
        _ = try await api.edgeRecovery(taskID: started.taskId)
        _ = try await api.cancelEdgeRecovery(taskID: started.taskId)
        let reqs = MockURLProtocol.requests
        XCTAssertEqual(reqs.map { $0.request.httpMethod }, ["POST", "GET", "DELETE"])
        XCTAssertEqual(reqs[0].request.url?.path, "/api/v2/cameras/c1/edge-recovery")
        XCTAssertEqual(json(reqs[0].body)["fromMs"] as? Int, 1)
        XCTAssertEqual(json(reqs[0].body)["toMs"] as? Int, 2)
        XCTAssertEqual(reqs[1].request.url?.path, "/api/v2/edge-recovery/t 1")
    }

    func testSpanEdgeFlagDecodes() throws {
        let tl = try SentinelAPI.decode(V2.Span.self, from: Data(#"{"start":1,"end":2,"edge":true}"#.utf8))
        XCTAssertEqual(tl.edge, true)
    }
}
