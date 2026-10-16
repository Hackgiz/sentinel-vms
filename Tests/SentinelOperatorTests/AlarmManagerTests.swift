import XCTest
@testable import SentinelCore

/// Alarm Manager + Incidents: filter/query building, chip counts, bulk
/// batching, triage/report flows (incl. AI off and the daily limit), incident
/// item resolution. Pure logic first, then `SentinelAPI` against `MockURLProtocol`.
final class AlarmManagerTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!
    private let now = Date(timeIntervalSince1970: 1_760_000_000)

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

    private func alarm(_ id: String, state: V2.AlarmState = .new, severity: V2.Severity = .warning, camera: String? = "Door",
                       owner: String = "", created: Int64 = 1000, events: Int = 1) -> V2.Alarm {
        let json = """
        {"id":"\(id)","cameraId":"c1","cameraName":\(camera.map { "\"\($0)\"" } ?? "null"),"kind":"Person","title":"T \(id)","detail":"",
         "severity":"\(severity.rawValue)","state":"\(state.rawValue)","owner":"\(owner)","createdAt":\(created),"updatedAt":\(created),
         "lastEventAt":\(created),"eventCount":\(events),"responseLog":[]}
        """
        return try! JSONDecoder().decode(V2.Alarm.self, from: Data(json.utf8))
    }

    private func query(_ items: [URLQueryItem]) -> [String: String] {
        Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
    }

    // MARK: Filter -> query

    func testDefaultFilterListsOpenStatesOnly() {
        let q = query(AlarmFilter.default.listQueryItems(now: now))
        XCTAssertEqual(q["states"], "New,Acknowledged,Investigating,Snoozed")
        XCTAssertNil(q["state"])
        XCTAssertNil(q["severities"])
        XCTAssertNil(q["from"])
    }

    func testNoChipsMeansEveryState() {
        let q = query(AlarmFilter(states: []).listQueryItems(now: now))
        XCTAssertEqual(q["state"], "all")
        XCTAssertNil(q["states"])
    }

    func testFullFilterQuery() {
        var f = AlarmFilter(states: [.falseAlarm, .new], severities: [.critical, .info], cameras: ["c1", "c2"], owner: "unassigned", range: .day, q: "  door ")
        let q = query(f.listQueryItems(now: now, limit: 100, before: "cur"))
        XCTAssertEqual(q["states"], "New,False Alarm")
        XCTAssertEqual(q["severities"], "Critical,Info")
        XCTAssertEqual(q["cameras"], "c1,c2")
        XCTAssertEqual(q["owner"], "unassigned")
        XCTAssertEqual(q["from"], String(now.unixMillis - 86_400_000))
        XCTAssertNil(q["to"])
        XCTAssertEqual(q["q"], "door")
        XCTAssertEqual(q["limit"], "100")
        XCTAssertEqual(q["before"], "cur")
        XCTAssertEqual(f.activeCount, 2 + 2 + 1 + 1 + 1)
        f = f.clearedKeepingStates()
        XCTAssertEqual(f.activeCount, 0)
        XCTAssertEqual(f.states, [.falseAlarm, .new])
    }

    func testCustomRangeUsesExplicitWindowAndCountsIgnoreStates() {
        let f = AlarmFilter(states: [.new], range: .custom, from: 5000, to: 9000)
        let counts = query(f.countsQueryItems(now: now))
        XCTAssertEqual(counts["from"], "5000")
        XCTAssertEqual(counts["to"], "9000")
        XCTAssertNil(counts["states"])
        XCTAssertNil(counts["state"])
    }

    func testToggles() {
        var f = AlarmFilter(states: [])
        f.toggle(state: .new); f.toggle(state: .resolved); f.toggle(state: .new)
        XCTAssertEqual(f.states, [.resolved])
        f.toggle(severity: .critical); f.toggle(camera: "c1")
        XCTAssertEqual(f.severities, [.critical])
        XCTAssertEqual(f.cameras, ["c1"])
    }

    // MARK: Chips

    func testChipCountMapping() throws {
        let counts = try JSONDecoder().decode(V2.AlarmCounts.self, from: Data(#"{"counts":{"New":4,"Acknowledged":2,"Resolved":9}}"#.utf8))
        let chips = AlarmChips.make(filter: .default, counts: counts)
        XCTAssertEqual(chips.map(\.state), AlarmFilter.allStates)
        XCTAssertEqual(chips.map(\.count), [4, 2, 0, 0, 9, 0])
        XCTAssertEqual(chips.map(\.isOn), [true, true, true, true, false, false])
        XCTAssertEqual(counts.total, 15)
    }

    // MARK: Sorting + selection + bulk

    func testSortingAndNextSort() {
        let list = [alarm("a", severity: .info, created: 3), alarm("b", severity: .critical, created: 1), alarm("c", severity: .critical, created: 2)]
        XCTAssertEqual(AlarmSorting.sorted(list, by: AlarmSort(key: .severity, ascending: true)).map(\.id), ["c", "b", "a"])
        XCTAssertEqual(AlarmSorting.sorted(list, by: AlarmSort()).map(\.id), ["a", "c", "b"])
        XCTAssertEqual(AlarmSort().toggled(to: .time), AlarmSort(key: .time, ascending: true))
        XCTAssertEqual(AlarmSort().toggled(to: .camera), AlarmSort(key: .camera, ascending: true))
        XCTAssertEqual(AlarmSort(key: .camera, ascending: true).toggled(to: .events), AlarmSort(key: .events, ascending: false))
    }

    func testBulkTargets() {
        let list = [alarm("n", state: .new), alarm("k", state: .acknowledged), alarm("s", state: .snoozed), alarm("r", state: .resolved)]
        let all = Set(list.map(\.id))
        let ack = AlarmBulk.targets(.acknowledge, alarms: list, selected: all)
        XCTAssertEqual(ack.ids, ["n", "s"])
        XCTAssertEqual(ack.state, .acknowledged)
        let close = AlarmBulk.targets(.close, alarms: list, selected: all)
        XCTAssertEqual(close.ids, ["n", "k", "s"])
        XCTAssertEqual(close.state, .resolved)
        XCTAssertEqual(AlarmBulk.targets(.close, alarms: list, selected: ["k"]).ids, ["k"])
    }

    func testShiftClickSelectsRange() {
        let order = ["a", "b", "c", "d"]
        var sel = AlarmBulk.toggleSelection([], order: order, id: "a", anchor: nil, range: false)
        sel = AlarmBulk.toggleSelection(sel, order: order, id: "c", anchor: "a", range: true)
        XCTAssertEqual(sel, ["a", "b", "c"])
        sel = AlarmBulk.toggleSelection(sel, order: order, id: "b", anchor: nil, range: false)
        XCTAssertEqual(sel, ["a", "c"])
    }

    func testBatchesSplitEvenly() {
        XCTAssertEqual(AlarmBulk.batches(["1", "2", "3", "4", "5"], size: 2), [["1", "2"], ["3", "4"], ["5"]])
        XCTAssertEqual(AlarmBulk.batches([], size: 3), [])
        XCTAssertEqual(AlarmBulk.batches(["1", "2"], size: 0), [["1"], ["2"]])
    }

    func testBulkRunCapsConcurrencyKeepsOrderAndCollectsFailures() async {
        actor Probe {
            var running = 0, peak = 0
            func enter() { running += 1; peak = max(peak, running) }
            func leave() { running -= 1 }
        }
        let probe = Probe()
        let ids = (1...10).map(String.init)
        let outcome = await AlarmBulk.run(ids: ids, batchSize: 3) { id in
            await probe.enter()
            try await Task.sleep(nanoseconds: 5_000_000)
            await probe.leave()
            if id == "4" || id == "9" { throw SentinelAPIError.server(status: 403, code: "forbidden", message: "nope") }
            return id
        }
        let peak = await probe.peak
        XCTAssertLessThanOrEqual(peak, 3)
        XCTAssertEqual(outcome.succeeded, ["1", "2", "3", "5", "6", "7", "8", "10"])
        XCTAssertEqual(outcome.failures.map(\.id), ["4", "9"])
        XCTAssertEqual(outcome.firstFailure, "nope")
        XCTAssertEqual(outcome.summary, "2 of 10 alarms could not be changed.")
    }

    // MARK: Paging + live sync

    func testPagerReplaceAppendDedupes() {
        var pager = CursorPager<V2.Alarm>()
        pager.replace(with: V2.Page(items: [alarm("a"), alarm("b")], nextCursor: "c1"))
        XCTAssertTrue(pager.hasMore)
        pager.append(V2.Page(items: [alarm("b"), alarm("c")], nextCursor: nil))
        XCTAssertEqual(pager.items.map(\.id), ["a", "b", "c"])
        XCTAssertFalse(pager.hasMore)
    }

    func testLiveSyncReplacesDropsAndRequestsReload() {
        var pager = CursorPager<V2.Alarm>()
        pager.replace(with: V2.Page(items: [alarm("a"), alarm("b")]))
        var sync = AlarmLiveSync(known: ["a", "b", "z"])
        let filter = AlarmFilter.default
        // a changes state within the chosen states -> replaced
        var r = sync.ingest([alarm("a", state: .acknowledged)], into: &pager, filter: filter)
        XCTAssertEqual(r, .init(changed: true, needsReload: false))
        XCTAssertEqual(pager.items.first?.state, .acknowledged)
        // b is resolved: leaves the open-only table
        r = sync.ingest([alarm("b", state: .resolved)], into: &pager, filter: filter)
        XCTAssertTrue(r.changed)
        XCTAssertEqual(pager.items.map(\.id), ["a"])
        // already known but not shown: ignored
        r = sync.ingest([alarm("z")], into: &pager, filter: filter)
        XCTAssertEqual(r, .init(changed: false, needsReload: false))
        // never seen: reload once
        r = sync.ingest([alarm("new")], into: &pager, filter: filter)
        XCTAssertTrue(r.needsReload)
        r = sync.ingest([alarm("new")], into: &pager, filter: filter)
        XCTAssertFalse(r.needsReload)
        // unchanged -> nothing
        r = sync.ingest([pager.items[0]], into: &pager, filter: filter)
        XCTAssertFalse(r.changed)
    }

    // MARK: API

    func testAlarmsPageAndCountsRequests() async throws {
        MockURLProtocol.handler = { req, _ in
            if req.url!.path == "/api/v2/alarms/counts" {
                return .json(200, #"{"counts":{"New":3}}"#)
            }
            return .json(200, "{\"items\":[\(Fixtures.alarm)],\"nextCursor\":\"next1\"}")
        }
        let api = makeAPI()
        let filter = AlarmFilter(states: [.new], severities: [.critical], q: "door")
        let page = try await api.alarmsPage(filter: filter, now: now, limit: 50, before: "cur0")
        XCTAssertEqual(page.nextCursor, "next1")
        let counts = try await api.alarmCounts(filter: filter, now: now)
        XCTAssertEqual(counts.count(for: .new), 3)
        let urls = MockURLProtocol.requests.map { $0.request.url!.absoluteString }
        XCTAssertTrue(urls[0].contains("states=New"))
        XCTAssertTrue(urls[0].contains("severities=Critical"))
        XCTAssertTrue(urls[0].contains("before=cur0"))
        XCTAssertTrue(urls[0].contains("limit=50"))
        XCTAssertTrue(urls[1].hasSuffix("/alarms/counts?severities=Critical&q=door") || urls[1].contains("/alarms/counts?"))
        XCTAssertFalse(urls[1].contains("states"))
    }

    func testAssignSeverityAndEventsPaths() async throws {
        MockURLProtocol.handler = { req, body in
            switch req.url!.path {
            case "/api/v2/alarms/a1/assign":
                XCTAssertEqual(String(data: body ?? Data(), encoding: .utf8), #"{"owner":"maria"}"#)
                return .json(200, Fixtures.alarm)
            case "/api/v2/alarms/a1/severity":
                XCTAssertEqual(String(data: body ?? Data(), encoding: .utf8), #"{"severity":"Info"}"#)
                return .json(200, Fixtures.alarm)
            default:
                return .json(200, "{\"items\":[\(Fixtures.event)]}")
            }
        }
        let api = makeAPI()
        _ = try await api.assignAlarm("a1", owner: "maria")
        _ = try await api.setAlarmSeverity("a1", .info)
        let events = try await api.alarmEvents("a1")
        XCTAssertEqual(events.map(\.id), ["e1"])
        XCTAssertEqual(MockURLProtocol.requests.map(\.request.httpMethod), ["POST", "POST", "GET"])
    }

    // MARK: Triage + report flows

    func testTriageAdviceIsNotAppliedAutomatically() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(200, #"{"summary":"A person at the door.","suggestedSeverity":"Critical","rationale":"After hours."}"#)
        }
        let api = makeAPI()
        let advice = try await api.triageAlarm("a1")
        XCTAssertEqual(advice.summary, "A person at the door.")
        XCTAssertEqual(advice.applicableSeverity(current: .warning), .critical)
        XCTAssertNil(advice.applicableSeverity(current: .critical))
        // The triage call is the only request: no severity POST happened.
        XCTAssertEqual(MockURLProtocol.requests.map(\.request.url!.path), ["/api/v2/alarms/a1/triage"])
    }

    func testTriageWithoutSuggestionHasNothingToApply() throws {
        let advice = try JSONDecoder().decode(V2.AlarmTriage.self, from: Data(#"{"summary":"s","suggestedSeverity":"","rationale":"r"}"#.utf8))
        XCTAssertNil(advice.suggested)
        XCTAssertNil(advice.applicableSeverity(current: .info))
    }

    func testTriageAIOffSurfacesServerMessage() async {
        MockURLProtocol.handler = { _, _ in
            .json(409, #"{"error":{"code":"conflict","message":"AI is turned off on this server."}}"#)
        }
        do {
            _ = try await makeAPI().triageAlarm("a1")
            XCTFail("expected a failure")
        } catch {
            let failure = AIFailure(error)
            XCTAssertEqual(failure, .off("AI is turned off on this server."))
            XCTAssertEqual(failure.message, "AI is turned off on this server.")
            XCTAssertTrue(failure.disablesAI)
        }
    }

    func testTriageDailyLimit429() async {
        MockURLProtocol.handler = { _, _ in
            .json(429, #"{"error":{"code":"too_many_requests","message":"Daily AI limit reached (50)."}}"#)
        }
        do {
            _ = try await makeAPI().triageAlarm("a1")
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(AIFailure(error), .limitReached("Daily AI limit reached (50)."))
        }
    }

    func testReportFlowsAndFailures() async throws {
        let incident = ###"{"id":"i1","title":"Break-in","status":"open","severity":"high","summary":"## Report","createdBy":"maria","createdAt":1,"updatedAt":2,"itemCount":3}"###
        MockURLProtocol.handler = { req, _ in
            req.url!.path == "/api/v2/incidents/i1/report" ? .json(200, incident) : .json(502, #"{"error":{"code":"internal","message":"The model call failed."}}"#)
        }
        let api = makeAPI()
        let drafted = try await api.generateIncidentReport("i1")
        XCTAssertEqual(drafted.summary, "## Report")
        XCTAssertEqual(MockURLProtocol.requests.first?.request.httpMethod, "POST")
        MockURLProtocol.handler = { _, _ in .json(429, #"{"error":{"code":"too_many_requests","message":"Daily AI limit reached."}}"#) }
        do {
            _ = try await api.generateIncidentReport("i1")
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(AIFailure(error), .limitReached("Daily AI limit reached."))
        }
        MockURLProtocol.handler = { _, _ in .json(502, #"{"error":{"code":"internal","message":"The model call failed."}}"#) }
        do {
            _ = try await api.generateIncidentReport("i1")
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(AIFailure(error), .unavailable("The model call failed."))
            XCTAssertFalse(AIFailure(error).disablesAI)
        }
    }

    func testAICapabilityFlagReadsFacetsAndFailsClosed() async {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"kinds":[],"labels":[],"threats":[],"aiEnabled":true}"#) }
        let on = await makeAPI().aiEnabled()
        XCTAssertTrue(on)
        MockURLProtocol.handler = { _, _ in .json(503, "") }
        let off = await makeAPI().aiEnabled()
        XCTAssertFalse(off)
    }

    func testOverwriteConfirmation() {
        XCTAssertFalse(IncidentRules.needsOverwriteConfirmation(savedSummary: "", draft: "  "))
        XCTAssertTrue(IncidentRules.needsOverwriteConfirmation(savedSummary: "text", draft: "text"))
        XCTAssertTrue(IncidentRules.needsOverwriteConfirmation(savedSummary: "", draft: "unsaved edit"))
        XCTAssertTrue(IncidentRules.isDirty(savedSummary: "a", draft: "b"))
    }

    // MARK: Incidents

    func testIncidentFilterQueryAndPaging() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(200, #"{"items":[{"id":"i1","title":"A","status":"open","severity":"low","summary":"","createdBy":"m","createdAt":1,"updatedAt":2,"itemCount":0}],"nextCursor":"n"}"#)
        }
        let api = makeAPI()
        let page = try await api.incidentsPage(filter: IncidentFilter(status: .investigating, q: " door "), limit: 100, before: "b0")
        XCTAssertEqual(page.nextCursor, "n")
        let url = MockURLProtocol.requests[0].request.url!.absoluteString
        XCTAssertTrue(url.contains("status=investigating"))
        XCTAssertTrue(url.contains("q=door"))
        XCTAssertTrue(url.contains("before=b0"))
        XCTAssertTrue(IncidentFilter().queryItems().isEmpty)
    }

    func testIncidentPatchOnlySendsSetFields() async throws {
        MockURLProtocol.handler = { _, body in
            XCTAssertEqual(String(data: body ?? Data(), encoding: .utf8), #"{"status":"closed"}"#)
            return .json(200, #"{"id":"i1","title":"A","status":"closed","severity":"low","summary":"","createdBy":"m","createdAt":1,"updatedAt":2,"itemCount":0}"#)
        }
        let updated = try await makeAPI().updateIncident("i1", V2.IncidentPatch(status: .closed))
        XCTAssertEqual(updated.status, .closed)
        XCTAssertEqual(MockURLProtocol.requests[0].request.httpMethod, "PATCH")
    }

    func testAddItemConflictAndRemove() async throws {
        MockURLProtocol.handler = { req, body in
            if req.httpMethod == "DELETE" { return .json(204, "") }
            XCTAssertEqual(String(data: body ?? Data(), encoding: .utf8), #"{"kind":"alarm","refId":"a1"}"#)
            return .json(409, #"{"error":{"code":"conflict","message":"already in this incident"}}"#)
        }
        let api = makeAPI()
        do {
            try await api.addIncidentItem("i1", .alarm("a1"))
            XCTFail("expected a conflict")
        } catch let error as SentinelAPIError {
            XCTAssertTrue(error.isConflict)
        }
        try await api.removeIncidentItem("i1", itemID: "it1")
    }

    func testIncidentItemResolutionStates() throws {
        let detailJSON = """
        {"incident":{"id":"i1","title":"T","status":"open","severity":"high","summary":"","createdBy":"m","createdAt":1,"updatedAt":2,"itemCount":6},
         "items":[
          {"id":"i-note","kind":"note","note":"Called guard","addedBy":"m","addedAt":50,"at":50},
          {"id":"i-alarm","kind":"alarm","refId":"a1","addedBy":"m","addedAt":60,"at":10,"alarm":\(Fixtures.alarm)},
          {"id":"i-event","kind":"event","refId":"e1","addedBy":"m","addedAt":61,"at":20,"event":\(Fixtures.event)},
          {"id":"i-restricted","kind":"alarm","addedBy":"m","addedAt":62,"at":30,"restricted":true},
          {"id":"i-missing","kind":"event","refId":"gone","addedBy":"m","addedAt":63,"at":40,"missing":true},
          {"id":"i-bare","kind":"evidence","refId":"x","addedBy":"m","addedAt":64,"at":45}
         ]}
        """
        let detail = try JSONDecoder().decode(V2.IncidentDetail.self, from: Data(detailJSON.utf8))
        let ordered = IncidentTimeline.ordered(detail.items)
        XCTAssertEqual(ordered.map(\.id), ["i-alarm", "i-event", "i-restricted", "i-missing", "i-bare", "i-note"])
        let byID = Dictionary(uniqueKeysWithValues: detail.items.map { ($0.id, $0.resolution) })
        XCTAssertEqual(byID["i-note"], .note("Called guard"))
        if case .alarm(let a)? = byID["i-alarm"] { XCTAssertEqual(a.id, "a1") } else { XCTFail("alarm") }
        if case .event(let e)? = byID["i-event"] { XCTAssertEqual(e.id, "e1") } else { XCTFail("event") }
        XCTAssertEqual(byID["i-restricted"], .restricted)
        XCTAssertEqual(byID["i-missing"], .missing)
        XCTAssertEqual(byID["i-bare"], .unresolved(kind: "evidence"))
    }

    func testRestrictedWinsOverMissingAndNullItemsDecode() throws {
        let item = V2.IncidentItem(id: "x", kind: .alarm, restricted: true, missing: true)
        XCTAssertEqual(item.resolution, .restricted)
        let empty = try JSONDecoder().decode(V2.IncidentDetail.self, from: Data(#"{"incident":{"id":"i","title":"T","status":"open","severity":"low","summary":"","createdBy":"m","createdAt":1,"updatedAt":1,"itemCount":0},"items":null}"#.utf8))
        XCTAssertTrue(empty.items.isEmpty)
    }

    func testIncidentRulesAndPicker() {
        XCTAssertEqual(IncidentRules.transitions(from: .open), [.investigating, .closed])
        XCTAssertEqual(IncidentRules.titleChange(current: "A", edited: " B "), "B")
        XCTAssertNil(IncidentRules.titleChange(current: "A", edited: "A "))
        XCTAssertNil(IncidentRules.titleChange(current: "A", edited: "  "))
        let list = [V2.Incident(id: "1", title: "a", status: .open), V2.Incident(id: "2", title: "b", status: .closed)]
        XCTAssertEqual(IncidentPicking.pickable(list).map(\.id), ["1"])
        XCTAssertEqual(V2.IncidentSeverity(alarm: .warning), .medium)
        XCTAssertEqual(IncidentPicking.defaultTitle(for: alarm("a")), "T a · Door")
    }
}
