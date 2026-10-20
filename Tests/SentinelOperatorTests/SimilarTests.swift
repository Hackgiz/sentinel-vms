import XCTest
@testable import SentinelCore

/// Appearance search ("Find similar"): request building, ordering, texts.
final class SimilarLogicTests: XCTestCase {
    private let now = Date(unixMillis: 1_760_100_000_000)
    private let day: Int64 = 86_400_000

    private func event(_ id: String = "e1", camera: String = "c1", kind: String = "Person", at: Int64 = 1_760_000_000_000,
                       thumbnail: Bool = true, box: String = "[0.1,0.2,0.4,0.9]") throws -> V2.Event {
        let json = """
        {"id":"\(id)","cameraId":"\(camera)","kind":"\(kind)","label":"person","score":0.9,"box":\(box),
         "createdAt":\(at),"hasThumbnail":\(thumbnail)}
        """
        return try SentinelAPI.jsonDecoder.decode(V2.Event.self, from: Data(json.utf8))
    }

    private func body(_ request: V2.SimilarEventsRequest) throws -> String {
        String(data: try SentinelAPI.jsonEncoder.encode(request), encoding: .utf8)!
    }

    func testDefaultRequestIsLastSevenDaysAllCamerasSameKind() throws {
        let request = SimilarLogic.request(for: try event(), options: SimilarOptions(), now: now)
        XCTAssertEqual(request.from, now.unixMillis - 7 * day)
        XCTAssertNil(request.to)
        XCTAssertNil(request.cameras)
        XCTAssertNil(request.anyKind)
        XCTAssertNil(request.minScore)
        XCTAssertEqual(request.limit, 48)
        XCTAssertEqual(try body(request), #"{"from":1759495200000,"limit":48}"#)
    }

    func testWindowsMapToFromAndTo() throws {
        let query = try event(at: 1_760_000_000_000)
        var options = SimilarOptions()
        options.window = .around
        var r = SimilarLogic.request(for: query, options: options, now: now)
        XCTAssertEqual(r.from, 1_760_000_000_000 - day)
        XCTAssertEqual(r.to, 1_760_000_000_000 + day)
        options.window = .month
        r = SimilarLogic.request(for: query, options: options, now: now)
        XCTAssertEqual(r.from, now.unixMillis - 30 * day)
        XCTAssertNil(r.to)
        options.window = .any
        r = SimilarLogic.request(for: query, options: options, now: now)
        XCTAssertNil(r.from)
        XCTAssertNil(r.to)
        // "around" never goes below zero.
        options.window = .around
        XCTAssertEqual(SimilarLogic.request(for: try event(at: 1000), options: options, now: now).from, 0)
    }

    func testCamerasAnyKindMinScoreAndLimitClamp() throws {
        var options = SimilarOptions()
        options.window = .any
        options.sameCamera = true
        options.anyKind = true
        options.minScore = 0.654
        options.limit = 500
        let r = SimilarLogic.request(for: try event(camera: "cam-9"), options: options, now: now)
        XCTAssertEqual(r.cameras, ["cam-9"])
        XCTAssertEqual(r.anyKind, true)
        XCTAssertEqual(r.minScore, 0.65)
        XCTAssertEqual(r.limit, 100)
        XCTAssertEqual(try body(r), #"{"anyKind":true,"cameras":["cam-9"],"limit":100,"minScore":0.65}"#)
        options.limit = 0
        options.minScore = -3
        let low = SimilarLogic.request(for: try event(), options: options, now: now)
        XCTAssertEqual(low.limit, 1)
        XCTAssertNil(low.minScore, "a floor of 0 or below is left to the server")
    }

    func testOrderingBestFirstDedupedWithoutQuery() throws {
        let hits = [
            V2.SimilarEventHit(score: 0.62, event: try event("a", at: 10)),
            V2.SimilarEventHit(score: 0.91, event: try event("b", at: 20)),
            V2.SimilarEventHit(score: 0.99, event: try event("q", at: 30)),
            V2.SimilarEventHit(score: 0.62, event: try event("c", at: 40)),
            V2.SimilarEventHit(score: 0.5, event: try event("b", at: 20)),
            V2.SimilarEventHit(score: .nan, event: try event("d", at: 50)),
        ]
        let ordered = SimilarLogic.ordered(hits, excluding: "q")
        XCTAssertEqual(ordered.map(\.event.id), ["b", "c", "a", "d"], "score desc, ties newest first, NaN last")
        XCTAssertEqual(ordered.first?.score, 0.91, "first occurrence of a duplicate is kept")
    }

    func testCanFindSimilar() throws {
        XCTAssertTrue(SimilarLogic.canFindSimilar(try event()))
        XCTAssertFalse(SimilarLogic.canFindSimilar(try event(thumbnail: false)))
        XCTAssertFalse(SimilarLogic.canFindSimilar(try event(kind: "Motion")))
        XCTAssertFalse(SimilarLogic.canFindSimilar(try event(kind: "audio")))
        XCTAssertFalse(SimilarLogic.canFindSimilar(try event(box: "null")))
        XCTAssertFalse(SimilarLogic.canFindSimilar(try event(box: "[0.5,0.2,0.4,0.9]")))
        XCTAssertFalse(SimilarLogic.canFindSimilar(try event(box: "[0.1,0.2,0.4]")))
    }

    func testScorePercentAndTone() {
        XCTAssertEqual(SimilarLogic.scorePercent(0.876), 88)
        XCTAssertEqual(SimilarLogic.scorePercent(-0.3), 0)
        XCTAssertEqual(SimilarLogic.scorePercent(1.4), 100)
        XCTAssertEqual(SimilarLogic.scorePercent(.nan), 0)
        XCTAssertEqual(SimilarLogic.tone(0.8), .strong)
        XCTAssertEqual(SimilarLogic.tone(0.6), .likely)
        XCTAssertEqual(SimilarLogic.tone(0.59), .weak)
    }

    func testUnavailableTextAndFailure() {
        XCTAssertNil(SimilarLogic.unavailableText(nil))
        XCTAssertNil(SimilarLogic.unavailableText(V2.AppearanceStatus(available: true)))
        let text = SimilarLogic.unavailableText(V2.AppearanceStatus(available: false, reason: "no model installed"))!
        XCTAssertTrue(text.hasPrefix("Appearance search is unavailable. No model installed. Put an OSNet"), text)
        XCTAssertTrue(SimilarLogic.unavailableText(V2.AppearanceStatus(available: false))!.contains("No appearance model is installed."))

        let down = SimilarLogic.failure(SentinelAPIError.server(status: 503, code: "unavailable",
                                                                 message: "appearance search is unavailable: ONNX Runtime missing"))
        XCTAssertTrue(down.banner)
        XCTAssertTrue(down.text.hasPrefix("Appearance search is unavailable. ONNX Runtime missing."), down.text)
        let bad = SimilarLogic.failure(SentinelAPIError.server(status: 400, code: "bad_request", message: "box too small"))
        XCTAssertFalse(bad.banner)
        XCTAssertEqual(bad.text, "box too small")
    }

    func testBackfillLabelsAndProgress() {
        var job = V2.AppearanceBackfill(taskId: "t", state: .running, total: 12_000, processed: 3_000)
        XCTAssertEqual(SimilarLogic.backfillLabel(job), "Indexing recent events: 3,000 of 12,000")
        XCTAssertEqual(SimilarLogic.backfillProgress(job), 0.25)
        job.total = 0
        XCTAssertEqual(SimilarLogic.backfillLabel(job), "Indexing recent events…")
        XCTAssertNil(SimilarLogic.backfillProgress(job))
        let done = V2.AppearanceBackfill(taskId: "t", state: .done, total: 10, processed: 10, indexed: 8, skipped: 2)
        XCTAssertEqual(SimilarLogic.backfillLabel(done), "Index built: 8 of 10 events (2 skipped)")
        XCTAssertNil(SimilarLogic.backfillProgress(done))
        XCTAssertEqual(SimilarLogic.backfillLabel(V2.AppearanceBackfill(taskId: "t", state: .canceled, total: 10, processed: 4)),
                       "Indexing canceled after 4 of 10 events")
        XCTAssertEqual(SimilarLogic.backfillLabel(V2.AppearanceBackfill(taskId: "t", state: .failed, error: "disk")), "Indexing failed: disk")
        XCTAssertEqual(SimilarLogic.backfillLabel(nil), "")
        XCTAssertEqual(SimilarLogic.summary(matches: 1, scanned: 3_400), "1 match of 3,400 indexed events")
    }
}

/// `SimilarModel` against `MockHTTP`.
@MainActor
final class SimilarModelTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    private func makeModel() throws -> SimilarModel {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
        let query = try SentinelAPI.jsonDecoder.decode(V2.Event.self, from: Data(Fixtures.event.utf8))
        return SimilarModel(query: query, backend: .init(api: api))
    }

    private func eventJSON(_ id: String) -> String {
        Fixtures.event.replacingOccurrences(of: #""id":"e1""#, with: #""id":"\#(id)""#)
    }

    func testSearchPostsRequestAndOrdersHits() async throws {
        MockURLProtocol.handler = { request, body in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url!.path, "/api/v2/events/e1/similar")
            let sent = try JSONSerialization.jsonObject(with: body ?? Data()) as! [String: Any]
            XCTAssertEqual(sent["cameras"] as? [String], ["c1"])
            XCTAssertEqual(sent["anyKind"] as? Bool, true)
            return .json(200, """
            {"query":\(Fixtures.event),"model":"sha256:0123456789abcdef","scanned":3400,
             "items":[{"score":0.7,"event":\(self.eventJSON("x"))},{"score":0.95,"event":\(self.eventJSON("y"))}]}
            """)
        }
        let model = try makeModel()
        model.options.sameCamera = true
        model.options.anyKind = true
        await model.search()
        XCTAssertEqual(model.hits?.map(\.event.id), ["y", "x"])
        XCTAssertEqual(model.scanned, 3400)
        XCTAssertEqual(model.summary, "2 matches of 3,400 indexed events")
        XCTAssertNil(model.banner)
        XCTAssertNil(model.error)
        XCTAssertFalse(model.isLoading)
    }

    func testUnavailableShowsBannerNotError() async throws {
        MockURLProtocol.handler = { request, _ in
            if request.url!.path == "/api/v2/appearance" { return .json(200, #"{"available":false,"reason":"no model installed"}"#) }
            return .json(503, #"{"error":{"code":"unavailable","message":"appearance search is unavailable: no model installed"}}"#)
        }
        let model = try makeModel()
        await model.loadStatus()
        XCTAssertEqual(model.status?.available, false)
        XCTAssertNotNil(model.banner)
        await model.search()
        XCTAssertNil(model.hits)
        XCTAssertNil(model.error)
        XCTAssertTrue(model.banner?.hasPrefix("Appearance search is unavailable. No model installed.") == true)
    }

    func testOtherErrorIsInline() async throws {
        MockURLProtocol.handler = { _, _ in .json(400, #"{"error":{"code":"bad_request","message":"The detection box is too small."}}"#) }
        let model = try makeModel()
        await model.search()
        XCTAssertEqual(model.error, "The detection box is too small.")
        XCTAssertNil(model.banner)
    }

    func testBackfillStartsThenRefreshesStatus() async throws {
        var running = false
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url!.path) {
            case ("POST", "/api/v2/appearance/backfill"):
                running = true
                return .json(201, #"{"taskId":"t1","state":"running","total":100,"processed":0,"indexed":0,"skipped":0,"startedAt":1}"#)
            case ("GET", "/api/v2/appearance"):
                return running
                    ? .json(200, #"{"available":true,"model":"m","indexed":5,"backfill":{"taskId":"t1","state":"running","total":100,"processed":40,"indexed":38,"skipped":2,"startedAt":1}}"#)
                    : .json(200, #"{"available":true,"model":"m","indexed":5}"#)
            default:
                return .json(404, #"{"error":{"code":"not_found","message":"nope"}}"#)
            }
        }
        let model = try makeModel()
        await model.loadStatus()
        XCTAssertFalse(model.isBackfillRunning)
        XCTAssertEqual(model.indexLine, "5 events indexed")
        await model.startBackfill()
        XCTAssertTrue(model.isBackfillRunning)
        XCTAssertEqual(model.indexLine, "Indexing recent events: 40 of 100")
        XCTAssertFalse(model.isBusy)
        let paths = MockURLProtocol.requests.map { "\($0.request.httpMethod ?? "") \($0.request.url!.path)" }
        XCTAssertEqual(paths, ["GET /api/v2/appearance", "POST /api/v2/appearance/backfill", "GET /api/v2/appearance"])
    }

    func testBackfillForbiddenSaysSo() async throws {
        MockURLProtocol.handler = { _, _ in .json(403, #"{"error":{"code":"forbidden","message":"Your role can't do this."}}"#) }
        let model = try makeModel()
        await model.startBackfill()
        XCTAssertEqual(model.error, "Your role can't do this.")
    }
}
