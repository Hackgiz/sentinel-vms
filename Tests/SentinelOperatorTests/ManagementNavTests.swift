import XCTest
@testable import SentinelCore

/// Management window tree: building, permission gating, filter, deep links,
/// registry wiring and the site-name call.
final class ManagementNavTests: XCTestCase {
    private func model(_ granted: [V2.Permission]) -> ManagementNavModel {
        ManagementNavModel(can: { granted.contains($0) })
    }

    private let admin: [V2.Permission] = [.manageCameras, .manageUsers, .changeSettings, .viewAudit, .exportEvidence, .viewLive]

    override func setUp() { super.setUp(); MockURLProtocol.reset() }
    override func tearDown() { MockURLProtocol.reset(); super.tearDown() }

    func testAdminSeesWholeCatalog() {
        let m = model(admin)
        XCTAssertEqual(m.categories.map(\.id), ["basics", "servers", "devices", "client", "rules", "security", "system", "logs"])
        XCTAssertEqual(m.leaves.count, ManagementCatalog.allLeaves.count)
        XCTAssertEqual(m.leaves.count, 20)
    }

    func testSupervisorSeesMonitorTasksAndLogsOnly() {
        let m = model([.viewAudit, .viewLive])
        XCTAssertEqual(m.leaves.map(\.id), [ManagementCatalog.Leaf.systemMonitor, ManagementCatalog.Leaf.currentTasks,
                                            ManagementCatalog.Leaf.serverLogs, ManagementCatalog.Leaf.audit])
        XCTAssertEqual(m.categories.map(\.id), ["system", "logs"])
    }

    func testOperatorWithoutManagementPermissionsHasNoTree() {
        let m = model([.viewLive, .acknowledgeAlarms])
        XCTAssertTrue(m.categories.isEmpty)
        XCTAssertFalse(ManagementCatalog.hasManagementAccess { [.viewLive].contains($0) })
        XCTAssertTrue(ManagementCatalog.hasManagementAccess { $0 == .viewAudit })
    }

    func testUserManagerSeesSecurityOnly() {
        let m = model([.manageUsers])
        XCTAssertEqual(m.categories.map(\.id), ["security"])
        XCTAssertNil(m.node(ManagementCatalog.Leaf.cameras))
    }

    func testFilterNarrowsToMatchingLeavesAndExpands() {
        let m = model(admin)
        let rows = m.rows(filter: "time", collapsed: ["rules"])
        XCTAssertEqual(rows.map(\.id), ["rules", ManagementCatalog.Leaf.timeProfiles])
        XCTAssertTrue(rows[0].isExpanded)
        // Category title matches too, words are ANDed.
        XCTAssertEqual(m.filtered("security roles").flatMap(\.children).map(\.id), [ManagementCatalog.Leaf.roles])
        XCTAssertTrue(m.rows(filter: "zzz").isEmpty)
    }

    func testCollapsedCategoriesHideLeavesAndRoundTrip() {
        let m = model(admin)
        let rows = m.rows(collapsed: ["devices", "logs"])
        XCTAssertFalse(rows.contains { $0.id == ManagementCatalog.Leaf.cameras })
        XCTAssertTrue(rows.first { $0.id == "devices" }?.isExpanded == false)
        XCTAssertTrue(rows.contains { $0.id == ManagementCatalog.Leaf.roles })
        let raw = ManagementNavModel.encodeCollapsed(["logs", "devices"])
        XCTAssertEqual(raw, "devices,logs")
        XCTAssertEqual(ManagementNavModel.decodeCollapsed(raw), ["devices", "logs"])
        XCTAssertTrue(ManagementNavModel.decodeCollapsed("").isEmpty)
    }

    func testBreadcrumbs() {
        let m = model(admin)
        XCTAssertEqual(m.breadcrumb(for: ManagementCatalog.Leaf.roles, siteName: "HQ"), ["Security", "Roles"])
        XCTAssertEqual(m.breadcrumb(for: "logs", siteName: "HQ"), ["Logs"])
        XCTAssertEqual(m.breadcrumb(for: "", siteName: "HQ"), ["HQ"])
        XCTAssertEqual(m.breadcrumb(for: "nope", siteName: "HQ"), ["HQ"])
    }

    func testDeepLinksResolveOnlyToVisibleNodes() {
        let m = model(admin)
        XCTAssertEqual(m.resolve(deepLink: "#/security/roles"), "security/roles")
        XCTAssertEqual(m.resolve(deepLink: "security/roles"), "security/roles")
        XCTAssertEqual(m.resolve(deepLink: "/logs/"), "logs")
        XCTAssertEqual(m.resolve(deepLink: "bogus"), ManagementCatalog.rootID)
        XCTAssertEqual(m.resolve(deepLink: nil), ManagementCatalog.rootID)
        XCTAssertEqual(ManagementNavModel.hashPath(for: "security/roles"), "#/security/roles")
        // A Supervisor can't deep-link into Security.
        XCTAssertEqual(model([.viewAudit]).resolve(deepLink: "#/security/roles"), ManagementCatalog.rootID)
        // Round-trip every visible node through its hash.
        for node in m.categories + m.leaves {
            XCTAssertEqual(m.resolve(deepLink: ManagementNavModel.hashPath(for: node.id)), node.id)
        }
    }

    func testRegistryReportsUnregisteredLeaves() {
        var registry = ManagementPaneRegistry<String>()
        let m = model(admin)
        XCTAssertEqual(registry.unregistered(in: m).count, 20)
        registry.register(ManagementCatalog.Leaf.cameras, "cams")
        registry.register(ManagementCatalog.Leaf.roles, "roles")
        XCTAssertEqual(registry.pane(for: ManagementCatalog.Leaf.roles), "roles")
        XCTAssertTrue(registry.isRegistered(ManagementCatalog.Leaf.cameras))
        XCTAssertFalse(registry.unregistered(in: m).contains(ManagementCatalog.Leaf.cameras))
        XCTAssertEqual(registry.unregistered(in: m).count, 18)
        XCTAssertNil(registry.pane(for: "missing"))
    }

    func testLeafIDsAreUniqueAndMatchHashShape() {
        let ids = ManagementCatalog.allLeaves.map(\.id)
        XCTAssertEqual(Set(ids).count, ids.count)
        for leaf in ManagementCatalog.categories.flatMap({ c in c.children.map { (c.id, $0.id) } }) {
            XCTAssertTrue(leaf.1.hasPrefix(leaf.0 + "/"))
        }
    }

    // MARK: Site name

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: URL(string: "http://nvr.test:8090")!, siteID: "s1", tokenStore: store,
                           session: MockURLProtocol.session(), clientLabel: "Test")
    }

    func testSiteNameFromServer() async {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/site")
            return .json(200, #"{"name":"  Harbor Yard ","description":"","contact":""}"#)
        }
        let name = await makeAPI().managementSiteName()
        XCTAssertEqual(name, "Harbor Yard")
    }

    func testSiteNameFallsBackOn404AndBlank() async {
        MockURLProtocol.handler = { _, _ in .json(404, #"{"error":"not found"}"#) }
        let missing = await makeAPI().managementSiteName()
        XCTAssertEqual(missing, "Sentinel")
        MockURLProtocol.handler = { _, _ in .json(200, #"{"name":"  "}"#) }
        let blank = await makeAPI().managementSiteName()
        XCTAssertEqual(blank, "Sentinel")
    }
}
