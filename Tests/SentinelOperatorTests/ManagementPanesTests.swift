import XCTest
@testable import SentinelCore

/// Wave 6 management panes: roles matrix, weekly grid, webhooks, tasks,
/// logs, thresholds, report save, and the API paths behind them.
final class ManagementPanesTests: XCTestCase {
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

    private func bodyJSON(_ data: Data?) -> [String: Any] {
        (data.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
    }

    // MARK: Roles

    private let catalog = [
        V2.PermissionInfo(id: "viewLive", label: "View live", group: "Video"),
        V2.PermissionInfo(id: "viewPlayback", label: "Playback", group: "Video"),
        V2.PermissionInfo(id: "manageUsers", label: "Manage users", group: "Security")
    ]

    func testRoleDirtyTrackingAndSaveInput() {
        let role = V2.RoleRecord(id: "r1", name: "Guard", description: "Night", permissions: ["viewLive"], userCount: 2)
        var m = RoleEditorModel(role: role)
        XCTAssertFalse(m.isDirty)
        XCTAssertFalse(m.canSave)
        m.set("manageUsers", true)
        XCTAssertTrue(m.isDirty)
        XCTAssertTrue(m.canSave)
        m.set("manageUsers", false)
        XCTAssertFalse(m.isDirty, "toggling back clears the dirty flag")
        m.set("viewPlayback", true)
        m.set("manageUsers", true)
        XCTAssertEqual(m.input(catalog: catalog).permissions, ["viewLive", "viewPlayback", "manageUsers"])
    }

    func testBuiltinRolesAreLocked() {
        let builtin = V2.RoleRecord(id: "admin", name: "Admin", builtin: true, permissions: ["viewLive"])
        var m = RoleEditorModel(role: builtin)
        XCTAssertTrue(m.isLocked)
        m.set("manageUsers", true)
        m.toggleGroup(["manageUsers"])
        m.name = "Renamed"
        XCTAssertFalse(m.has("manageUsers"), "locked roles ignore permission edits")
        XCTAssertFalse(m.canSave)
        XCTAssertNotNil(m.blocker)
        XCTAssertEqual(RoleRules.deleteBlocker(builtin), "Built-in roles can't be deleted.")
    }

    func testNewRoleValidationAndGroupToggle() {
        var m = RoleEditorModel(role: nil)
        XCTAssertEqual(m.blocker, "Give the role a name.")
        m.name = "  Auditor "
        XCTAssertEqual(m.blocker, "Grant at least one permission.")
        m.toggleGroup(["viewLive", "viewPlayback"])
        XCTAssertTrue(m.has("viewLive") && m.has("viewPlayback"))
        XCTAssertTrue(m.canSave)
        XCTAssertEqual(m.input(catalog: catalog).name, "Auditor")
        m.toggleGroup(["viewLive", "viewPlayback"])
        XCTAssertFalse(m.has("viewLive"))
        let groups = RoleEditorModel.groups(catalog)
        XCTAssertEqual(groups.map(\.name), ["Video", "Security"])
        XCTAssertEqual(groups[0].items.count, 2)
    }

    func testDeleteBlockedWhileUsersHoldRole() {
        let role = V2.RoleRecord(id: "r1", name: "Guard", userCount: 3)
        XCTAssertEqual(RoleRules.deleteBlocker(role), "3 users still hold this role. Move them to another role first.")
        XCTAssertNil(RoleRules.deleteBlocker(V2.RoleRecord(id: "r2", name: "Spare")))
    }

    func testRoleAPIDecodesAndSurfacesTheDelete409() async throws {
        MockURLProtocol.handler = { req, body in
            switch (req.httpMethod ?? "", req.url?.path ?? "") {
            case ("GET", "/api/v2/roles"):
                return .json(200, #"{"items":[{"id":"admin","name":"Admin","builtin":true,"permissions":["viewLive"],"userCount":1},{"id":"r1","name":"Guard","description":"","builtin":false,"permissions":null,"userCount":2}]}"#)
            case ("GET", "/api/v2/permissions"):
                return .json(200, #"{"items":[{"id":"viewLive","label":"View live","group":"Video","description":"Watch cameras"}]}"#)
            case ("PUT", "/api/v2/roles/r1"):
                return .json(200, #"{"id":"r1","name":"Guard 2","permissions":["viewLive"],"userCount":2}"#)
            case ("DELETE", "/api/v2/roles/r1"):
                return .json(409, #"{"error":{"code":"conflict","message":"2 users hold this role."}}"#)
            default: return .json(404, "{}")
            }
        }
        let api = makeAPI()
        let roles = try await api.roles()
        XCTAssertEqual(roles.map(\.id), ["admin", "r1"])
        XCTAssertTrue(roles[0].builtin)
        XCTAssertEqual(roles[1].permissions, [])
        let cat = try await api.permissionCatalog()
        XCTAssertEqual(cat.first?.group, "Video")
        _ = try await api.updateRole("r1", V2.RoleInput(name: "Guard 2", description: "", permissions: ["viewLive"]))
        let put = MockURLProtocol.requests.last
        XCTAssertEqual(bodyJSON(put?.body)["name"] as? String, "Guard 2")
        do {
            try await api.deleteRole("r1")
            XCTFail("expected 409")
        } catch let SentinelAPIError.server(status, _, message) {
            XCTAssertEqual(status, 409)
            XCTAssertEqual(message, "2 users hold this role.")
        }
    }

    // MARK: Weekly grid

    func testGridRoundTripsSimpleWindows() {
        let windows = [V2.TimeWindow(days: [1, 2, 3, 4, 5], start: "09:00", end: "17:30")]
        let grid = WeeklyGridModel(windows: windows)
        XCTAssertTrue(grid[1, 18])
        XCTAssertTrue(grid[1, 34])
        XCTAssertFalse(grid[1, 35], "17:30 is exclusive")
        XCTAssertFalse(grid[0, 18])
        XCTAssertEqual(grid.hoursPerWeek, 8.5 * 5)
        XCTAssertEqual(grid.windows(), windows)
        XCTAssertFalse(grid.widenedOnLoad)
    }

    func testGridLoadsPastMidnightWindowsAcrossTheWeekBoundary() {
        // Saturday 22:00 -> Sunday 06:00.
        let grid = WeeklyGridModel(windows: [V2.TimeWindow(days: [6], start: "22:00", end: "06:00")])
        XCTAssertTrue(grid[6, 44])
        XCTAssertTrue(grid[6, 47])
        XCTAssertTrue(grid[0, 0])
        XCTAssertTrue(grid[0, 11])
        XCTAssertFalse(grid[0, 12])
        XCTAssertEqual(grid.hoursPerWeek, 8)
    }

    func testGridSavesPastMidnightSpansAsOneWindow() {
        var grid = WeeklyGridModel()
        // Friday 22:00 -> Saturday 02:00, drawn as two cell runs.
        grid.paint(days: 5...5, slots: 44...47, on: true)
        grid.paint(days: 6...6, slots: 0...3, on: true)
        XCTAssertEqual(grid.windows(), [V2.TimeWindow(days: [5], start: "22:00", end: "02:00")])
        // Round trip.
        XCTAssertEqual(WeeklyGridModel(windows: grid.windows()).cells, grid.cells)
    }

    func testGridWrapAcrossSundayAndEndAtMidnight() {
        var grid = WeeklyGridModel()
        grid.paint(days: 6...6, slots: 44...47, on: true)
        grid.paint(days: 0...0, slots: 0...1, on: true)
        XCTAssertEqual(grid.windows(), [V2.TimeWindow(days: [6], start: "22:00", end: "01:00")])

        var lone = WeeklyGridModel()
        lone.paint(days: 2...2, slots: 40...47, on: true)
        XCTAssertEqual(lone.windows(), [V2.TimeWindow(days: [2], start: "20:00", end: "00:00")])
        XCTAssertEqual(WeeklyGridModel(windows: lone.windows()).cells, lone.cells)
    }

    func testGridKeepsAmbiguousNextDayRunSeparate() {
        // Tail starts 20:00, next day's lead runs to 22:00: merging would read as same-day.
        var grid = WeeklyGridModel()
        grid.paint(days: 1...1, slots: 40...47, on: true)
        grid.paint(days: 2...2, slots: 0...43, on: true)
        let w = grid.windows()
        XCTAssertEqual(w.count, 2)
        XCTAssertEqual(WeeklyGridModel(windows: w).cells, grid.cells)
    }

    func testFullDayAndSharedSpansGroupDays() {
        var grid = WeeklyGridModel()
        grid.paint(days: 0...0, slots: 0...47, on: true)
        XCTAssertEqual(grid.windows(), [V2.TimeWindow(days: [0], start: "00:00", end: "23:59")])
        XCTAssertEqual(WeeklyGridModel(windows: grid.windows()).cells, grid.cells)

        var two = WeeklyGridModel()
        two.setRange(days: [1, 3], startSlot: 16, endSlot: 20)
        XCTAssertEqual(two.windows(), [V2.TimeWindow(days: [1, 3], start: "08:00", end: "10:00")])
    }

    func testGridWidensUnalignedMinutesAndIgnoresGarbage() {
        let grid = WeeklyGridModel(windows: [V2.TimeWindow(days: [1], start: "09:10", end: "09:40"),
                                             V2.TimeWindow(days: [9], start: "bad", end: "10:00")])
        XCTAssertTrue(grid.widenedOnLoad)
        XCTAssertTrue(grid[1, 18])
        XCTAssertTrue(grid[1, 19])
        XCTAssertFalse(grid[1, 20])
        XCTAssertEqual(WeeklyGridModel.minutes("24:00"), 1440)
        XCTAssertNil(WeeklyGridModel.minutes("25:00"))
        XCTAssertEqual(TimeProfileRules.blocker(name: "x", grid: WeeklyGridModel()), "Mark at least one time slot.")
        XCTAssertNil(TimeProfileRules.blocker(name: "x", grid: grid))
    }

    func testTimeProfileAPIAcceptsWeeklyTopLevelOrInBody() async throws {
        MockURLProtocol.handler = { req, _ in
            if req.httpMethod == "GET" {
                return .json(200, #"{"items":[{"id":"t1","name":"Office","weekly":[{"days":[1],"start":"09:00","end":"17:00"}]},{"id":"t2","name":"Nights","body":{"weekly":[{"days":[6],"start":"22:00","end":"06:00"}]}}]}"#)
            }
            return .json(200, #"{"id":"t1","name":"Office","weekly":[]}"#)
        }
        let api = makeAPI()
        let profiles = try await api.timeProfiles()
        XCTAssertEqual(profiles[0].weekly.count, 1)
        XCTAssertEqual(profiles[1].weekly.first?.end, "06:00")
        _ = try await api.createTimeProfile(V2.TimeProfileInput(name: "Office", weekly: profiles[0].weekly))
        let sent = bodyJSON(MockURLProtocol.requests.last?.body)
        XCTAssertEqual((sent["weekly"] as? [[String: Any]])?.count, 1)
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.path, "/api/v2/time-profiles")
    }

    // MARK: Webhooks

    func testWebhookSecretIsWriteOnly() throws {
        let hook = V2.Webhook(id: "w1", name: "Slack", url: "https://hooks.example.com/x", events: ["alarm.new"], hasSecret: true)
        var draft = WebhookDraft(hook)
        XCTAssertTrue(draft.secretStatus.hasPrefix("A secret is set"))
        XCTAssertEqual(draft.newSecret, "", "the saved secret never enters the draft")
        XCTAssertNil(draft.input().secret, "untouched secret is omitted so the server keeps it")
        let untouched = try JSONSerialization.jsonObject(with: JSONEncoder().encode(draft.input())) as? [String: Any]
        XCTAssertNil(untouched?["secret"])

        draft.newSecret = "s3cret"
        XCTAssertEqual(draft.input().secret, "s3cret")
        XCTAssertEqual(draft.secretStatus, "The saved secret will be replaced.")
        draft.newSecret = ""
        draft.clearSecret = true
        XCTAssertEqual(draft.input().secret, "")
        XCTAssertEqual(draft.secretStatus, "The saved secret will be removed.")
        XCTAssertEqual(WebhookDraft().secretStatus, "No secret set. Deliveries are unsigned.")
    }

    func testWebhookDraftValidationAndEvents() {
        var d = WebhookDraft()
        XCTAssertEqual(d.blocker, "Give the webhook a name.")
        d.name = "Hook"
        XCTAssertEqual(d.blocker, "Enter a full http:// or https:// address.")
        d.url = "ftp://x.example.com"
        XCTAssertNotNil(d.blocker)
        d.url = " https://x.example.com/in "
        XCTAssertNil(d.blocker)
        d.eventsText = "alarm.new, motion  alarm.new\nperson"
        XCTAssertEqual(d.events, ["alarm.new", "motion", "person"])
        XCTAssertEqual(d.input().url, "https://x.example.com/in")
    }

    func testWebhookStatusDotAndTestSummaries() {
        let now = Date(unixMillis: 1_760_000_100_000)
        XCTAssertEqual(WebhookStatusDot.of(V2.Webhook(id: "a", name: "", url: "")), .never)
        XCTAssertEqual(WebhookStatusDot.of(V2.Webhook(id: "a", name: "", url: "", lastStatus: 200, lastFiredAt: 1)), .ok)
        XCTAssertEqual(WebhookStatusDot.of(V2.Webhook(id: "a", name: "", url: "", lastStatus: 500, lastFiredAt: 1)), .failing)
        XCTAssertEqual(WebhookStatusDot.of(V2.Webhook(id: "a", name: "", url: "", lastStatus: 0, lastFiredAt: 1)), .failing)
        XCTAssertEqual(WebhookStatusDot.of(V2.Webhook(id: "a", name: "", url: "", enabled: false, lastStatus: 200, lastFiredAt: 1)), .disabled)
        XCTAssertTrue(WebhookStatusDot.caption(V2.Webhook(id: "a", name: "", url: "", lastStatus: 502, lastFiredAt: 1_760_000_000_000), now: now).hasPrefix("HTTP 502"))
        XCTAssertEqual(WebhookStatusDot.testSummary(V2.WebhookTestResult(ok: true, status: 204)), "Delivered (HTTP 204).")
        XCTAssertEqual(WebhookStatusDot.testSummary(V2.WebhookTestResult(ok: false, status: 0, error: "timeout")), "Failed: timeout")
    }

    func testWebhookAPIPathsAndPresenceDecoding() async throws {
        MockURLProtocol.handler = { req, _ in
            let path = req.url?.path ?? ""
            if path.hasSuffix("/test") { return .json(200, #"{"ok":false,"status":500,"error":"upstream"}"#) }
            return .json(200, #"{"items":[{"id":"w1","name":"Slack","url":"https://x","events":null,"enabled":true,"hasSecret":true,"lastStatus":200,"lastFiredAt":1760000000000}]}"#)
        }
        let api = makeAPI()
        let hooks = try await api.webhooks()
        XCTAssertEqual(hooks.first?.hasSecret, true)
        XCTAssertEqual(hooks.first?.events, [])
        let result = try await api.testWebhook("w1")
        XCTAssertFalse(result.ok)
        XCTAssertEqual(result.error, "upstream")
        let last = MockURLProtocol.requests.last
        XCTAssertEqual(last?.request.httpMethod, "POST")
        XCTAssertEqual(last?.request.url?.path, "/api/v2/webhooks/w1/test")
    }

    // MARK: Tasks

    func testTaskBoardOrdersFiltersAndFormats() {
        let now = Date(unixMillis: 1_760_001_000_000)
        func t(_ id: String, _ state: V2.TaskState, started: Int64, finished: Int64? = nil, progress: Double? = nil, error: String? = nil) -> V2.SystemTask {
            V2.SystemTask(id: id, kind: "scan", title: id, state: state, progress: progress, started: started, finished: finished, error: error)
        }
        let old = t("old", .done, started: 1_760_000_000_000, finished: 1_760_000_050_000)   // ~16 min before now
        let recent = t("recent", .done, started: 1_760_000_600_000, finished: 1_760_000_700_000)
        let failed = t("failed", .failed, started: 1_760_000_650_000, finished: 1_760_000_800_000, error: "no route")
        let run1 = t("run1", .running, started: 1_760_000_900_000, progress: 42)
        let run2 = t("run2", .running, started: 1_760_000_950_000)
        let rows = TaskBoard.rows([old, recent, failed, run1, run2], now: now)
        XCTAssertEqual(rows.map(\.id), ["run2", "run1", "failed", "recent"])
        XCTAssertEqual(TaskBoard.progressText(run1), "42%")
        XCTAssertEqual(TaskBoard.progressText(run2), "Working…")
        XCTAssertNil(TaskBoard.fraction(run2))
        XCTAssertEqual(TaskBoard.fraction(run1), 0.42)
        XCTAssertEqual(TaskBoard.progressText(failed), "no route")
        XCTAssertEqual(TaskBoard.progressText(recent), "Done")
        XCTAssertEqual(TaskBoard.duration(recent, now: now), "1m 40s")
    }

    func testTasksAPIDecodesIndeterminateProgress() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(200, #"{"items":[{"id":"k1","kind":"discovery","title":"Scan","state":"running","progress":null,"started":1760000000000}]}"#)
        }
        let tasks = try await makeAPI().systemTasks()
        XCTAssertEqual(tasks.first?.state, .running)
        XCTAssertNil(tasks.first?.progress)
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.path, "/api/v2/system/tasks")
    }

    // MARK: Logs

    func testLogTailBoundsSortsAndFiltersByLevel() {
        var tail = LogTail()
        var lines = (0..<(LogTail.capacity + 50)).map { V2.LogLine(ts: Int64($0), level: "info", message: "m\($0)") }
        lines.shuffle()
        tail.replace(with: lines)
        XCTAssertEqual(tail.lines.count, LogTail.capacity)
        XCTAssertEqual(tail.lines.first?.message, "m50", "oldest lines are dropped")
        XCTAssertEqual(tail.lines.last?.message, "m\(LogTail.capacity + 49)")

        tail.replace(with: [V2.LogLine(ts: 1, level: "DEBUG", message: "d"), V2.LogLine(ts: 2, level: "info", message: "i"),
                            V2.LogLine(ts: 3, level: "WARN", message: "w"), V2.LogLine(ts: 4, level: "error", message: "e")])
        tail.minimumLevel = .warn
        XCTAssertEqual(tail.visible.map(\.message), ["w", "e"])
        tail.minimumLevel = .debug
        XCTAssertEqual(tail.visible.count, 4)
        XCTAssertTrue(tail.text({ "\($0)" }).contains("ERROR e"))
    }

    func testLogsAPIQuery() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"items":[{"ts":5,"level":"warn","message":"disk"}]}"#) }
        let lines = try await makeAPI().serverLogs(server: "rec 2", level: "warn", limit: 300)
        XCTAssertEqual(lines.first?.message, "disk")
        let items = URLComponents(url: MockURLProtocol.requests.last!.request.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
        XCTAssertEqual(items.first { $0.name == "server" }?.value, "rec 2")
        XCTAssertEqual(items.first { $0.name == "level" }?.value, "warn")
        XCTAssertEqual(items.first { $0.name == "limit" }?.value, "300")
    }

    // MARK: Thresholds

    func testThresholdClampingKeepsAmberAtOrBelowRed() {
        var e = ThresholdEditor()
        XCTAssertFalse(e.isDirty)
        e.setAmber(120, .cpu)
        XCTAssertEqual(e.values.cpu, V2.ThresholdPair(amber: 100, red: 100), "amber pulls red up, both capped at 100")
        e.setRed(50, .cpu)
        XCTAssertEqual(e.values.cpu, V2.ThresholdPair(amber: 50, red: 50))
        e.setAmber(-5, .mem)
        XCTAssertEqual(e.values.mem.amber, 1)
        e.setRed(.nan, .disk)
        XCTAssertTrue(e.values.disk.red >= e.values.disk.amber)
        XCTAssertTrue(e.isDirty)
        e.resetToDefaults()
        XCTAssertFalse(e.isDirty)
        let loaded = ThresholdEditor(V2.MonitorThresholds(cpu: V2.ThresholdPair(amber: 99, red: 60), mem: V2.MonitorThresholds.defaults.mem, disk: V2.MonitorThresholds.defaults.disk))
        XCTAssertEqual(loaded.values.cpu, V2.ThresholdPair(amber: 60, red: 60), "a bad server value is clamped on load")
    }

    func testThresholdsAPIRoundTrip() async throws {
        MockURLProtocol.handler = { req, _ in
            req.httpMethod == "GET"
                ? .json(200, #"{"cpu":{"amber":70,"red":90},"mem":{"amber":80,"red":95},"disk":{"amber":85.0,"red":97}}"#)
                : .json(200, #"{"cpu":{"amber":60,"red":90},"mem":{"amber":80,"red":95},"disk":{"amber":85,"red":97}}"#)
        }
        let api = makeAPI()
        var editor = ThresholdEditor(try await api.monitorThresholds())
        XCTAssertEqual(editor.values.disk.amber, 85)
        editor.setAmber(60, .cpu)
        let saved = try await api.updateMonitorThresholds(editor.values)
        XCTAssertEqual(saved.cpu.amber, 60)
        let sent = bodyJSON(MockURLProtocol.requests.last?.body)
        XCTAssertEqual((sent["cpu"] as? [String: Any])?["amber"] as? Double, 60)
        XCTAssertEqual(MockURLProtocol.requests.last?.request.httpMethod, "PUT")
    }

    // MARK: Site, report, notifications

    func testSiteInfoRoundTrip() async throws {
        MockURLProtocol.handler = { _, _ in .json(200, #"{"name":"HQ","description":"Main","contact":"ops@example.com"}"#) }
        let api = makeAPI()
        var info = try await api.siteInfo()
        XCTAssertEqual(info.name, "HQ")
        info.contact = "it@example.com"
        _ = try await api.updateSiteInfo(info)
        let last = MockURLProtocol.requests.last
        XCTAssertEqual(last?.request.httpMethod, "PUT")
        XCTAssertEqual(last?.request.url?.path, "/api/v2/site")
        XCTAssertEqual(bodyJSON(last?.body)["contact"] as? String, "it@example.com")
    }

    func testReportFileNamesPreviewAndSaveFlow() async throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(secondsFromGMT: 0)!
        let date = Date(timeIntervalSince1970: 1_760_000_000)
        XCTAssertEqual(ConfigReportModel.suggestedFileName(site: "Main Office!", format: .json, date: date, calendar: cal), "main-office-config-2025-10-09.json")
        XCTAssertEqual(ConfigReportModel.suggestedFileName(site: "", format: .html, date: date, calendar: cal), "sentinel-config-2025-10-09.html")
        XCTAssertTrue(ConfigReportModel.preview(Data(#"{"b":1,"a":{"x":2}}"#.utf8)).contains("\"a\" : {"))
        XCTAssertEqual(ConfigReportModel.preview(Data("<html>hi</html>".utf8)), "<html>hi</html>")

        MockURLProtocol.handler = { req, _ in
            let html = req.url?.query?.contains("format=html") == true
            return MockURLProtocol.Reply(status: 200, body: Data((html ? "<html>report</html>" : #"{"servers":[]}"#).utf8),
                                         headers: ["Content-Type": html ? "text/html" : "application/json"])
        }
        let api = makeAPI()
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("report-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let jsonURL = dir.appendingPathComponent("r.json")
        let written = try await ConfigReportModel.save(api: api, format: .json, to: jsonURL)
        XCTAssertEqual(written, 14)
        XCTAssertEqual(try String(contentsOf: jsonURL), #"{"servers":[]}"#)
        let htmlURL = dir.appendingPathComponent("r.html")
        _ = try await ConfigReportModel.save(api: api, format: .html, to: htmlURL)
        XCTAssertEqual(try String(contentsOf: htmlURL), "<html>report</html>")
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.path, "/api/v2/system/config-report")
    }

    func testNotificationsUseV1EndpointsAndHidePasswords() async throws {
        MockURLProtocol.handler = { req, _ in
            switch (req.httpMethod ?? "", req.url?.path ?? "") {
            case ("GET", "/api/v1/mqtt"), ("PUT", "/api/v1/mqtt"):
                return .json(200, #"{"enabled":true,"host":"broker.local","port":1883,"tls":false,"username":"u","topicPrefix":"sentinel","discoveryPrefix":"homeassistant","allowCommands":false,"hasPassword":true,"status":{"state":"connected","dropped":0}}"#)
            case ("POST", "/api/v1/mqtt/test"):
                return .json(200, #"{"ok":false,"error":"refused"}"#)
            case ("GET", "/api/v1/push"):
                return .json(200, #"{"mode":"relay","relayURL":"https://r","relayRegistered":true,"ready":true,"quietEnabled":true,"quietFrom":1320,"quietTo":420,"devices":2,"pushDevices":1,"sent":4,"failed":0,"dropped":0}"#)
            case ("POST", "/api/v1/push/test"):
                return .json(200, #"{"sent":1,"failed":[]}"#)
            default: return .json(404, "{}")
            }
        }
        let api = makeAPI()
        let mqtt = try await api.mqttSettings()
        XCTAssertTrue(mqtt.hasPassword)
        XCTAssertEqual(NotificationRules.mqttStateText(mqtt.status), "Connected")
        _ = try await api.updateMQTT(V2.MQTTUpdate(host: "b"))
        let put = bodyJSON(MockURLProtocol.requests.last?.body)
        XCTAssertNil(put["password"], "an untouched password is not sent")
        XCTAssertEqual(put["host"] as? String, "b")
        let test = try await api.testMQTT()
        XCTAssertEqual(test.error, "refused")
        let push = try await api.pushStatus()
        XCTAssertEqual(NotificationRules.clock(push.quietFrom), "22:00")
        XCTAssertEqual(NotificationRules.clock(push.quietTo), "07:00")
        let sent = try await api.testPush()
        XCTAssertEqual(NotificationRules.pushTestSummary(sent), "Test sent to 1 device.")
        XCTAssertNotNil(NotificationRules.mqttBlocker(host: " ", port: 1883, enabled: true))
        XCTAssertNil(NotificationRules.mqttBlocker(host: " ", port: 0, enabled: false))
        XCTAssertNotNil(NotificationRules.mqttBlocker(host: "h", port: 70000, enabled: true))
    }
}
