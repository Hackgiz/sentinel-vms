import XCTest
@testable import SentinelCore

final class SearchFilterTests: XCTestCase {
    private let now = Date(unixMillis: 1_760_000_000_000)

    private func query(_ items: [URLQueryItem]) -> [String: String] {
        Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
    }

    func testDefaultIsLast24HoursOnly() {
        let q = query(SearchFilter().queryItems(now: now))
        XCTAssertEqual(q, ["from": "1759913600000"])
    }

    func testEveryFilterMapsToItsParameter() {
        var f = SearchFilter()
        f.range = .week
        f.cameras = ["c1", "c2"]
        f.kinds = ["Person", "Vehicle"]
        f.labels = ["jacket"]
        f.q = "  red  "
        f.minScore = 0.7
        f.threatOnly = true
        f.anomaly = true
        let q = query(f.queryItems(now: now, limit: 60, before: "cur"))
        XCTAssertEqual(q["cameras"], "c1,c2")
        XCTAssertEqual(q["kinds"], "Person,Vehicle")
        XCTAssertEqual(q["labels"], "jacket")
        XCTAssertEqual(q["from"], "1759395200000")
        XCTAssertNil(q["to"])
        XCTAssertEqual(q["minScore"], "0.7")
        XCTAssertEqual(q["threats"], "elevated,high")
        XCTAssertEqual(q["anomaly"], "true")
        XCTAssertEqual(q["q"], "red")
        XCTAssertEqual(q["limit"], "60")
        XCTAssertEqual(q["before"], "cur")
    }

    func testAnyTimeAndCustomWindows() {
        var f = SearchFilter()
        f.range = .any
        XCTAssertNil(query(f.queryItems(now: now))["from"])
        f.range = .custom
        f.from = 1000
        f.to = 2000
        let q = query(f.queryItems(now: now))
        XCTAssertEqual(q["from"], "1000")
        XCTAssertEqual(q["to"], "2000")
    }

    func testThreatsListUsedWhenNotThreatOnly() {
        var f = SearchFilter()
        f.threats = ["high"]
        XCTAssertEqual(query(f.queryItems(now: now))["threats"], "high")
    }

    func testFacetQueryHasOnlyCamerasAndWindow() {
        var f = SearchFilter()
        f.cameras = ["c1"]
        f.kinds = ["Person"]
        f.q = "x"
        let q = query(f.facetQueryItems(now: now))
        XCTAssertEqual(Set(q.keys), ["cameras", "from"])
        XCTAssertEqual(f.facetKey, SearchFilter().with { $0.cameras = ["c1"] }.facetKey)
        XCTAssertNotEqual(f.facetKey, SearchFilter().facetKey)
    }

    func testActiveCountAndClear() {
        var f = SearchFilter()
        f.cameras = ["c1"]; f.kinds = ["Person"]; f.q = "a"; f.threatOnly = true; f.minScore = 0.5; f.anomaly = false
        XCTAssertEqual(f.activeCount, 6)
        let cleared = f.cleared
        XCTAssertEqual(cleared.activeCount, 0)
        XCTAssertEqual(cleared.range, f.range)
    }

    func testPlaybackStartLeadsByFiveSeconds() throws {
        let event = try SentinelAPI.jsonDecoder.decode(V2.Event.self, from: Data(Fixtures.event.utf8))
        XCTAssertEqual(SearchFilter.playbackStart(of: event).unixMillis, event.createdAt - 5000)
    }

    func testFacetChipMergeKeepsSelectedWithZeroCount() {
        let facets = [V2.FacetCount(value: "Person", count: 12), V2.FacetCount(value: "Vehicle", count: 3)]
        let chips = FacetChips.merge(facets, selected: ["Vehicle", "Animal"])
        XCTAssertEqual(chips.map(\.value), ["Person", "Vehicle", "Animal"])
        XCTAssertEqual(chips.map(\.count), [12, 3, 0])
        XCTAssertEqual(chips.map(\.selected), [false, true, true])
        XCTAssertEqual(FacetChips.merge(facets + [V2.FacetCount(value: "Dog", count: 1)], selected: [], limit: 2).count, 2)
        XCTAssertEqual(FacetChips.threatTotal([V2.FacetCount(value: "high", count: 2), V2.FacetCount(value: "low", count: 9),
                                               V2.FacetCount(value: "elevated", count: 5)]), 7)
    }

    func testAskFilterMapsIntoFilterBar() {
        let answer = V2.EventSearchFilter(cameras: ["c1"], kinds: ["Person"], labels: ["jacket"], from: 1000, to: 2000,
                                          minScore: 0.6, threats: ["elevated", "high"], anomaly: nil, q: "red")
        let f = SearchFilter.from(answer)
        XCTAssertEqual(f.range, .custom)
        XCTAssertEqual(f.from, 1000)
        XCTAssertEqual(f.to, 2000)
        XCTAssertEqual(f.cameras, ["c1"])
        XCTAssertEqual(f.q, "red")
        XCTAssertTrue(f.threatOnly)
        XCTAssertTrue(f.threats.isEmpty)
        XCTAssertEqual(f.minScore, 0.6)

        let open = SearchFilter.from(V2.EventSearchFilter(threats: ["high"]))
        XCTAssertEqual(open.range, .any)
        XCTAssertFalse(open.threatOnly)
        XCTAssertEqual(open.threats, ["high"])
    }

    func testAskFailureClassification() {
        XCTAssertEqual(AskFailure(SentinelAPIError.server(status: 409, code: "conflict", message: "AI is off")), .aiOff(message: "AI is off"))
        XCTAssertEqual(AskFailure(SentinelAPIError.server(status: 429, code: "too_many_requests", message: "limit")), .limitReached(message: "limit"))
        XCTAssertEqual(AskFailure(SentinelAPIError.transport(message: "down")), .other(message: "down"))
    }
}

private extension SearchFilter {
    func with(_ change: (inout SearchFilter) -> Void) -> SearchFilter {
        var copy = self
        change(&copy)
        return copy
    }
}

/// `SearchModel` against `MockHTTP`: paging, facets and the ask flow.
@MainActor
final class SearchModelTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    private func makeModel() -> SearchModel {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
        return SearchModel(backend: .init(api: api, pageSize: 2))
    }

    private func eventJSON(_ id: String, at: Int) -> String {
        Fixtures.event.replacingOccurrences(of: #""id":"e1""#, with: #""id":"\#(id)""#)
            .replacingOccurrences(of: #""createdAt":1760000000000"#, with: #""createdAt":\#(1_760_000_000_000 + at)"#)
    }

    func testCursorPagingAppendsAndStops() async {
        MockURLProtocol.handler = { request, _ in
            let items = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)!.queryItems ?? []
            let before = items.first { $0.name == "before" }?.value
            switch before {
            case nil: return .json(200, "{\"items\":[\(self.eventJSON("e3", at: 3)),\(self.eventJSON("e2", at: 2))],\"nextCursor\":\"c1\"}")
            case "c1": return .json(200, "{\"items\":[\(self.eventJSON("e2", at: 2)),\(self.eventJSON("e1", at: 1))]}")
            default: return .json(400, #"{"error":{"code":"bad_request","message":"bad cursor"}}"#)
            }
        }
        let model = makeModel()
        await model.search()
        XCTAssertEqual(model.events.map(\.id), ["e3", "e2"])
        XCTAssertTrue(model.hasMore)
        await model.loadMore()
        XCTAssertEqual(model.events.map(\.id), ["e3", "e2", "e1"], "overlap is de-duplicated")
        XCTAssertFalse(model.hasMore)
        await model.loadMore()
        XCTAssertEqual(MockURLProtocol.requests.count, 2, "no request once the cursor is spent")
        let second = MockURLProtocol.requests[1].request.url!.query ?? ""
        XCTAssertTrue(second.contains("before=c1"))
        XCTAssertTrue(second.contains("limit=2"))
    }

    func testUnchangedFilterDoesNotSearchAgain() async {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"items":[]}"#) }
        let model = makeModel()
        await model.search()
        await model.search()
        XCTAssertEqual(MockURLProtocol.requests.count, 1)
        await model.search(force: true)
        XCTAssertEqual(MockURLProtocol.requests.count, 2)
        XCTAssertTrue(model.hasSearched)
        XCTAssertTrue(model.events.isEmpty)
    }

    func testFacetsFillChipsAndEnableAsk() async {
        MockURLProtocol.handler = { _, _ in
            .json(200, #"{"kinds":[{"value":"Person","count":4}],"labels":null,"threats":[{"value":"high","count":1}],"aiEnabled":true}"#)
        }
        let model = makeModel()
        model.filter.kinds = ["Animal"]
        await model.loadFacets()
        XCTAssertEqual(model.kindChips.map(\.value), ["Person", "Animal"])
        XCTAssertEqual(model.threatTotal, 1)
        XCTAssertTrue(model.askAvailable)
        await model.loadFacets()
        XCTAssertEqual(MockURLProtocol.requests.count, 1, "same cameras and window: cached")
        model.filter.cameras = ["c1"]
        await model.loadFacets()
        XCTAssertEqual(MockURLProtocol.requests.count, 2)
    }

    func testAskFillsFilterBarAndShowsFirstPage() async {
        MockURLProtocol.handler = { request, body in
            if request.url!.path.hasSuffix("/facets") { return .json(200, #"{"kinds":[],"labels":[],"threats":[],"aiEnabled":true}"#) }
            XCTAssertEqual(request.url!.path, "/api/v2/events/ask")
            XCTAssertEqual(String(data: body ?? Data(), encoding: .utf8), #"{"query":"person at the door"}"#)
            return .json(200, "{\"filter\":{\"cameras\":[\"c1\"],\"kinds\":[\"Person\"],\"from\":1000,\"q\":\"door\"},\"items\":[\(self.eventJSON("e1", at: 0))],\"nextCursor\":\"n1\"}")
        }
        let model = makeModel()
        await model.loadFacets()
        model.question = "person at the door"
        await model.ask()
        XCTAssertEqual(model.understood, "person at the door")
        XCTAssertEqual(model.filter.cameras, ["c1"])
        XCTAssertEqual(model.filter.kinds, ["Person"])
        XCTAssertEqual(model.filter.range, .custom)
        XCTAssertEqual(model.filter.q, "door")
        XCTAssertEqual(model.events.map(\.id), ["e1"])
        XCTAssertTrue(model.hasMore)
        // The view's change-triggered search must not replace the answer.
        await model.search()
        XCTAssertEqual(MockURLProtocol.requests.filter { $0.request.url!.path.hasSuffix("/search") }.count, 0)
    }

    func testAskAIOffHidesAndLimitDisables() async {
        var status = 409
        MockURLProtocol.handler = { request, _ in
            if request.url!.path.hasSuffix("/facets") { return .json(200, #"{"kinds":[],"labels":[],"threats":[],"aiEnabled":true}"#) }
            return status == 409
                ? .json(409, #"{"error":{"code":"conflict","message":"AI is turned off."}}"#)
                : .json(429, #"{"error":{"code":"too_many_requests","message":"Daily AI limit reached."}}"#)
        }
        let off = makeModel()
        await off.loadFacets()
        XCTAssertTrue(off.askAvailable)
        off.question = "anything"
        await off.ask()
        XCTAssertEqual(off.askFailure, .aiOff(message: "AI is turned off."))
        XCTAssertFalse(off.askAvailable)

        status = 429
        let limited = makeModel()
        await limited.loadFacets()
        limited.question = "anything"
        await limited.ask()
        XCTAssertTrue(limited.askAvailable, "the field stays, disabled")
        XCTAssertEqual(limited.askLimitMessage, "Daily AI limit reached.")
        let before = MockURLProtocol.requests.count
        await limited.ask()
        XCTAssertEqual(MockURLProtocol.requests.count, before, "no further ask while limited")
        XCTAssertTrue(limited.events.isEmpty)
    }
}
