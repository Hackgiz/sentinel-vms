import XCTest
@testable import SentinelCore

/// Wave 5 stream A: the tab model, the view tree, the notice bus and the
/// save-target rules behind the Operator shell.
final class TabModelTests: XCTestCase {
    func testFixedTabOrderLabelsAndKeys() {
        XCTAssertEqual(FixedTab.order.map(\.label), ["Home", "Live", "Map", "Search", "Exports", "Alarm Manager", "Incidents", "System Monitor", "Reports"])
        XCTAssertEqual(FixedTab.order.map(\.key), ["0", "1", "6", "5", "7", "3", "8", "9", "r"])
        XCTAssertEqual(FixedTab.order.map(\.railLabel), ["Home", "Live", "Map", "Search", "Exports", "Alarms", "Incidents", "Monitor", "Reports"])
        XCTAssertEqual(FixedTab.reports.keyLabel, "⌘R")
        XCTAssertEqual(FixedTab.reports.rawValue, "reports", "the Linux Operator's workspace id")
        XCTAssertEqual(Set(FixedTab.allCases), Set(FixedTab.order))
    }

    func testOpenActivateAndReopenViewTabs() {
        var s = TabsState.initial
        s.openView("a")
        s.openView("b")
        XCTAssertEqual(s.views, ["a", "b"])
        XCTAssertEqual(s.active, "view:b")
        s.activate("alarms")
        s.openView("a")
        XCTAssertEqual(s.views, ["a", "b"], "reopening switches instead of duplicating")
        XCTAssertEqual(s.activeViewID, "a")
        s.activate(FixedTab.map.rawValue)
        XCTAssertNil(s.activeViewID)
        XCTAssertEqual(s.activeFixed, .map)
    }

    func testCloseHandsOverToTheLeftNeighbourThenLive() {
        var s = TabsState(active: "view:b", views: ["a", "b", "c"])
        s.closeView("b")
        XCTAssertEqual(s, TabsState(active: "view:a", views: ["a", "c"]))
        s.closeView("a")
        XCTAssertEqual(s, TabsState(active: "view:c", views: ["c"]), "closing the first falls to the next one")
        s.closeView("c")
        XCTAssertEqual(s, TabsState(active: "live", views: []))

        var other = TabsState(active: "search", views: ["a", "b"])
        other.closeView("a")
        XCTAssertEqual(other.active, "search", "closing an inactive tab leaves the active one")
        other.closeView("zzz")
        XCTAssertEqual(other.views, ["b"])
    }

    func testReconcileDropsDeletedViewsAndStaleActive() {
        var s = TabsState(active: "view:gone", views: ["a", "gone"])
        s.reconcile { $0 == "a" }
        XCTAssertEqual(s, TabsState(active: "live", views: ["a"]))
        var t = TabsState(active: "view:a", views: ["a"])
        t.reconcile { _ in true }
        XCTAssertEqual(t.active, "view:a")
        var u = TabsState(active: "bogus", views: [])
        u.reconcile { _ in true }
        XCTAssertEqual(u.active, "live")
    }

    func testPersistsPerSiteInUserDefaults() throws {
        let suite = "sentinel.test.tabs.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        TabsState(active: "view:v2", views: ["v1", "v2"]).save(siteID: "siteA", defaults: defaults)
        TabsState(active: "map", views: []).save(siteID: "siteB", defaults: defaults)
        XCTAssertEqual(TabsState.load(siteID: "siteA", defaults: defaults), TabsState(active: "view:v2", views: ["v1", "v2"]))
        XCTAssertEqual(TabsState.load(siteID: "siteB", defaults: defaults).active, "map")
        XCTAssertEqual(TabsState.load(siteID: "never", defaults: defaults), .initial)
    }

    func testParseIsLenient() {
        XCTAssertEqual(TabsState.parse(nil), .initial)
        XCTAssertEqual(TabsState.parse(Data("not json".utf8)), .initial)
        let messy = Data(#"{"active":"","views":["a","","a","b"]}"#.utf8)
        XCTAssertEqual(TabsState.parse(messy), TabsState(active: "live", views: ["a", "b"]))
    }

    func testSignInLandsOnHomeKeepingViewTabs() {
        let s = TabsState(active: "view:b", views: ["a", "b"]).afterSignIn()
        XCTAssertEqual(s, TabsState(active: "home", views: ["a", "b"]))
        XCTAssertEqual(s.activeFixed, .home)
    }

    func testBadgeCapsAtNinePlus() {
        XCTAssertEqual(WorkspaceTabs.badgeText(0), "")
        XCTAssertEqual(WorkspaceTabs.badgeText(-3), "")
        XCTAssertEqual(WorkspaceTabs.badgeText(1), "1")
        XCTAssertEqual(WorkspaceTabs.badgeText(9), "9")
        XCTAssertEqual(WorkspaceTabs.badgeText(10), "9+")
        XCTAssertEqual(WorkspaceTabs.badgeText(250), "9+")
    }

    func testShortcutsMirrorTheWeb() {
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "1"), TabShortcut(tab: "live", mode: .live))
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "2"), TabShortcut(tab: "live", mode: .playback))
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "3")?.tab, "alarms")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "4"), TabShortcut(tab: "live", events: true))
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "5")?.tab, "search")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "6")?.tab, "map")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "7")?.tab, "exports")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "8")?.tab, "incidents")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "9")?.tab, "monitor")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "0")?.tab, "home", "wave 20: ⌘0 is Home")
        XCTAssertNil(WorkspaceTabs.shortcut(for: "0", available: [.live]))
        XCTAssertNil(WorkspaceTabs.shortcut(for: "a"))
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "r")?.tab, "reports", "parity 3.4.2: ⌘R is Reports")
        XCTAssertEqual(WorkspaceTabs.shortcut(for: "R")?.tab, "reports")
        XCTAssertNil(WorkspaceTabs.shortcut(for: "r", available: [.live]))
        XCTAssertNil(WorkspaceTabs.shortcut(for: "12"))
        XCTAssertNil(WorkspaceTabs.shortcut(for: "6", available: [.live]), "unavailable tabs have no binding")
    }

    func testCameraSidebarOnlyInLiveAndSavedViewTabs() {
        XCTAssertTrue(WorkspaceTabs.hasCameraSidebar(TabsState(active: "live"), onWorkspace: true))
        XCTAssertTrue(WorkspaceTabs.hasCameraSidebar(TabsState(active: "view:a", views: ["a"]), onWorkspace: true))
        for tab in FixedTab.allCases where tab != .live {
            XCTAssertFalse(WorkspaceTabs.hasCameraSidebar(TabsState(active: tab.rawValue), onWorkspace: true), tab.rawValue)
        }
        for pane in RailPane.allCases {
            XCTAssertFalse(WorkspaceTabs.hasCameraSidebar(TabsState(active: pane.tabID), onWorkspace: true), pane.rawValue)
        }
        XCTAssertFalse(WorkspaceTabs.hasCameraSidebar(TabsState(active: "live"), onWorkspace: false), "Events and other pages are full width")
        XCTAssertFalse(WorkspaceTabs.showsCameraSidebar(TabsState(active: "live"), onWorkspace: true, userVisible: false), "the toggle hides it in Live")
        XCTAssertTrue(WorkspaceTabs.showsCameraSidebar(TabsState(active: "live"), onWorkspace: true, userVisible: true))
        XCTAssertFalse(WorkspaceTabs.showsCameraSidebar(TabsState(active: "map"), onWorkspace: true, userVisible: true))
    }

    func testRailPanesMapToManagementLeavesAndTabIDs() {
        XCTAssertEqual(RailPane.railOrder.map(\.title), ["Insights", "Rules", "People", "Plates"])
        XCTAssertEqual(RailPane.insights.leaf, ManagementCatalog.Leaf.insights)
        XCTAssertEqual(RailPane.rules.leaf, ManagementCatalog.Leaf.rules)
        XCTAssertEqual(RailPane.people.leaf, ManagementCatalog.Leaf.people)
        XCTAssertEqual(RailPane.plates.leaf, ManagementCatalog.Leaf.plates)
        XCTAssertNil(RailPane.admin.leaf, "Admin shows the whole tree")
        for pane in RailPane.allCases {
            XCTAssertEqual(RailPane.of(tab: pane.tabID), pane)
            XCTAssertNil(FixedTab(rawValue: pane.tabID))
        }
        XCTAssertNil(RailPane.of(tab: "insights"))
        XCTAssertNil(RailPane.of(tab: "pane:nope"))
        XCTAssertNil(RailPane.of(tab: "view:insights"))
    }

    func testManagementNodesOpenInTheirRailPaneElseAdmin() {
        XCTAssertEqual(RailPane.destination(forNode: ManagementCatalog.Leaf.insights).pane, .insights)
        XCTAssertNil(RailPane.destination(forNode: ManagementCatalog.Leaf.insights).node)
        XCTAssertEqual(RailPane.destination(forNode: ManagementCatalog.Leaf.rules).pane, .rules)
        let cams = RailPane.destination(forNode: ManagementCatalog.Leaf.cameras)
        XCTAssertEqual(cams.pane, .admin)
        XCTAssertEqual(cams.node, ManagementCatalog.Leaf.cameras)
    }

    func testPaneTabsActivateSurviveReconcileAndPersist() throws {
        var s = TabsState(active: "home", views: ["a"])
        s.activate(RailPane.people.tabID)
        XCTAssertEqual(s.activePane, .people)
        XCTAssertNil(s.activeFixed)
        XCTAssertNil(s.activeViewID)
        s.reconcile { _ in true }
        XCTAssertEqual(s.activePane, .people, "a pane tab is not stale")
        let data = try JSONEncoder().encode(s)
        XCTAssertEqual(TabsState.parse(data).activePane, .people)
        var junk = TabsState(active: "pane:gone")
        junk.reconcile { _ in true }
        XCTAssertEqual(junk.active, "live")
        // Switching away and back keeps the views open.
        s.activate("map")
        s.activate(RailPane.people.tabID)
        XCTAssertEqual(s, TabsState(active: "pane:people", views: ["a"]))
    }

    func testClockTicksOnTheMinute() {
        XCTAssertEqual(WorkspaceTabs.msToNextMinute(nowMs: 0), 60_000)
        XCTAssertEqual(WorkspaceTabs.msToNextMinute(nowMs: 59_000), 1_000)
        XCTAssertEqual(WorkspaceTabs.msToNextMinute(nowMs: 120_001), 59_999)
    }
}

final class ViewTreeTests: XCTestCase {
    private func v(_ id: String, _ name: String, shared: Bool = false, group: String? = nil, order: Int? = nil) -> V2.SavedView {
        V2.SavedView(id: id, name: name, shared: shared ? true : nil, groupId: group, sortOrder: order)
    }

    private let groups = [
        V2.ViewGroup(id: "g-sites", parentId: "", name: "Sites", sortOrder: 1),
        V2.ViewGroup(id: "g-hq", parentId: "g-sites", name: "HQ", sortOrder: 0),
        V2.ViewGroup(id: "g-ops", parentId: "", name: "Ops", sortOrder: 0),
        V2.ViewGroup(id: "g-orphan", parentId: "deleted", name: "Orphan", sortOrder: 2),
    ]

    func testFoldersNestByParentIdFoldersFirstInOrder() {
        let shared = [v("s1", "Lobby", shared: true, group: "g-hq"), v("s2", "Top", shared: true), v("s3", "Gate", shared: true, group: "g-sites")]
        let nodes = ViewTree.sharedNodes(groups: groups, shared: shared)
        XCTAssertEqual(nodes.map(\.id), ["f:g-ops", "f:g-sites", "f:g-orphan", "v:s2"], "folders by sortOrder then name; orphan folder is top level; views after")
        guard case .folder(_, _, let children, let count) = nodes[1] else { return XCTFail("folder expected") }
        XCTAssertEqual(children.map(\.id), ["f:g-hq", "v:s3"])
        XCTAssertEqual(count, 2, "viewCount counts nested views")
    }

    func testViewWithMissingFolderFallsToTopLevel() {
        let nodes = ViewTree.sharedNodes(groups: [], shared: [v("s1", "Lost", shared: true, group: "nope")])
        XCTAssertEqual(nodes.map(\.id), ["v:s1"])
    }

    func testCyclicFoldersDoNotHangAndAreDropped() {
        let loop = [V2.ViewGroup(id: "a", parentId: "b", name: "A"), V2.ViewGroup(id: "b", parentId: "a", name: "B")]
        XCTAssertEqual(ViewTree.sharedNodes(groups: loop, shared: []).count, 0)
    }

    func testRootsAndLockGlyph() {
        let priv = [v("p2", "beta"), v("p1", "Alpha")]
        let shared = [v("s1", "Lobby", shared: true, group: "g-hq")]
        let manager = ViewTree.build(views: priv, shared: shared, groups: groups, query: "", canManage: true)
        XCTAssertEqual(manager.map(\.label), ["Private", "Shared"])
        XCTAssertEqual(manager[0].nodes.map(\.id), ["v:p1", "v:p2"], "private views sort by name, case-insensitively")
        XCTAssertEqual(manager.map(\.readOnly), [false, false])
        let viewer = ViewTree.build(views: priv, shared: shared, groups: groups, query: "", canManage: false)
        XCTAssertEqual(viewer.map(\.readOnly), [false, true], "no manageViews: Shared root is locked")
        XCTAssertEqual(viewer[1].viewCount, 1)
    }

    func testSearchKeepsMatchingViewsAndTheirFolders() {
        let shared = [v("s1", "Lobby", shared: true, group: "g-hq"), v("s2", "Gate", shared: true, group: "g-hq")]
        let roots = ViewTree.build(views: [v("p1", "Lobby cam")], shared: shared, groups: groups, query: " lob ", canManage: false)
        XCTAssertEqual(roots.map(\.id), [.private, .shared])
        guard case .folder(let id, _, let sites, let count) = roots[1].nodes[0] else { return XCTFail("folder expected") }
        XCTAssertEqual(id, "g-sites")
        XCTAssertEqual(count, 1)
        guard case .folder(_, _, let hq, _) = sites[0] else { return XCTFail("nested folder expected") }
        XCTAssertEqual(hq.map(\.id), ["v:s1"], "non-matching Gate is pruned")
    }

    func testFolderNameMatchKeepsEverythingInsideAndEmptyRootsDrop() {
        let shared = [v("s1", "Lobby", shared: true, group: "g-hq"), v("s2", "Gate", shared: true, group: "g-hq")]
        let roots = ViewTree.build(views: [v("p1", "Mine")], shared: shared, groups: groups, query: "hq", canManage: true)
        XCTAssertEqual(roots.map(\.id), [.shared], "Private has no match and drops away while searching")
        XCTAssertEqual(roots[0].viewCount, 2)
        XCTAssertTrue(ViewTree.build(views: [], shared: [], groups: groups, query: "zzz", canManage: true).isEmpty)
        XCTAssertEqual(ViewTree.build(views: [], shared: [], groups: [], query: "", canManage: true).count, 2, "both roots always show without a search")
    }

    func testFolderOptionsAndDescendants() {
        let options = ViewTree.folderOptions(groups: groups)
        XCTAssertEqual(options.map(\.label), ["Ops", "Sites", "Sites / HQ", "Orphan"])
        XCTAssertEqual(ViewTree.descendants(of: "g-sites", groups: groups), ["g-sites", "g-hq"])
    }

    func testFindPrefersPrivateThenShared() {
        let p = v("x", "private")
        let s = v("y", "shared", shared: true)
        XCTAssertEqual(ViewTree.find("x", views: [p], shared: [s])?.name, "private")
        XCTAssertEqual(ViewTree.find("y", views: [p], shared: [s])?.name, "shared")
        XCTAssertNil(ViewTree.find("z", views: [p], shared: [s]))
    }
}

@MainActor
final class NoticeBusTests: XCTestCase {
    private var clock = Date(timeIntervalSince1970: 1_000_000)

    private func makeCenter() -> NoticeCenter {
        NoticeCenter(now: { [unowned self] in self.clock }, schedulesExpiry: false)
    }

    func testKeyedNoticeReplacesItsPredecessor() {
        let center = makeCenter()
        center.post(NoticeInput(kind: .warn, text: "Recording server A is offline.", key: "recorder:a"))
        center.post(NoticeInput(kind: .info, text: "other"))
        clock += 1
        center.post(NoticeInput(kind: .success, text: "Recording server A is back online.", key: "recorder:a"))
        XCTAssertEqual(center.notices.map(\.text), ["other", "Recording server A is back online."])
    }

    func testCapsAtFourDroppingTheOldest() {
        let center = makeCenter()
        for n in 1...6 { center.post(.warn, "line \(n)") }
        XCTAssertEqual(center.notices.map(\.text), ["line 3", "line 4", "line 5", "line 6"])
        XCTAssertEqual(NoticeList.maxNotices, 4)
    }

    func testSameNoticeWithinThirtySecondsIsDropped() {
        let center = makeCenter()
        XCTAssertTrue(center.post(NoticeInput(kind: .warn, text: "Camera X went offline", key: "k")))
        clock += 29
        XCTAssertFalse(center.post(NoticeInput(kind: .warn, text: "Camera X went offline", key: "k")))
        XCTAssertEqual(center.notices.count, 1)
        clock += 2
        XCTAssertTrue(center.post(NoticeInput(kind: .warn, text: "Camera X went offline", key: "k")), "after 30 s it is news again")
        XCTAssertTrue(center.post(NoticeInput(kind: .warn, text: "Camera X went offline", key: "other")), "a different key is a different notice")
        XCTAssertFalse(center.post(nil))
    }

    func testInfoAndSuccessFadeAfterEightSecondsButWarningsStay() {
        let center = makeCenter()
        center.post(.info, "info")
        center.post(.success, "success")
        center.post(.warn, "warn")
        center.post(.error, "error")
        clock += 7.9
        center.expire()
        XCTAssertEqual(center.notices.count, 4)
        clock += 0.2
        center.expire()
        XCTAssertEqual(center.notices.map(\.text), ["warn", "error"])
        XCTAssertEqual(NoticeList.autoDismissAfter, 8)
    }

    func testDismissAndClear() {
        let center = makeCenter()
        center.post(.warn, "a")
        center.post(.warn, "b")
        center.dismiss(id: center.notices[0].id)
        XCTAssertEqual(center.notices.map(\.text), ["b"])
        center.clear()
        XCTAssertTrue(center.notices.isEmpty)
        XCTAssertTrue(center.post(.warn, "a"), "clear also forgets the dedupe memory")
    }

    func testStreamTransitionRules() {
        XCTAssertNil(NoticeRules.recorder(name: "NVR", wasOnline: nil, online: false, id: "r1"), "first report is not a transition")
        XCTAssertNil(NoticeRules.recorder(name: "NVR", wasOnline: true, online: true, id: "r1"))
        let down = NoticeRules.recorder(name: "NVR", wasOnline: true, online: false, id: "r1")
        XCTAssertEqual(down?.kind, .warn)
        XCTAssertEqual(down?.key, "recorder:r1")
        XCTAssertEqual(down?.text, "Recording server NVR is offline. Its cameras are unavailable until it reconnects.")
        let up = NoticeRules.recorder(name: "NVR", wasOnline: false, online: true, id: "r1")
        XCTAssertEqual(up?.kind, .success)
        XCTAssertEqual(up?.text, "Recording server NVR is back online.")
        XCTAssertEqual(up?.key, down?.key, "back replaces offline")

        XCTAssertEqual(NoticeRules.camera(name: "Door", wasOnline: true, online: false, id: "c1")?.text, "Camera Door went offline")
        XCTAssertEqual(NoticeRules.camera(name: "Door", wasOnline: false, online: true, id: "c1")?.kind, .info)
        XCTAssertNil(NoticeRules.camera(name: "Door", wasOnline: nil, online: true, id: "c1"))
        XCTAssertNil(NoticeRules.camera(name: "Door", wasOnline: true, online: true, id: "c1"))
    }

    func testSetupHintIsKeyedInfoWithTheWebCopy() {
        XCTAssertEqual(NoticeRules.setupHint.kind, .info)
        XCTAssertEqual(NoticeRules.setupHint.key, "setup-hint")
        XCTAssertTrue(NoticeRules.setupHint.text.hasPrefix("Setup mode edits saved views."))
        let center = makeCenter()
        center.post(NoticeRules.setupHint)
        clock += 1
        XCTAssertFalse(center.post(NoticeRules.setupHint), "pressing again within 30 s doesn't stack")
    }
}

final class SaveTargetTests: XCTestCase {
    private let folders: Set<String> = ["g1", "g2"]

    func testNameIsTrimmedAndRequired() throws {
        XCTAssertEqual(try ViewSaveTarget(name: "  Lobby  ").validated(canManage: false, folderIDs: folders).name, "Lobby")
        XCTAssertThrowsError(try ViewSaveTarget(name: "   ").validated(canManage: true, folderIDs: folders)) {
            XCTAssertEqual($0 as? ViewSaveTarget.Problem, .emptyName)
        }
        XCTAssertThrowsError(try ViewSaveTarget(name: String(repeating: "x", count: 101)).validated(canManage: true, folderIDs: folders)) {
            XCTAssertEqual($0 as? ViewSaveTarget.Problem, .nameTooLong)
        }
        XCTAssertNoThrow(try ViewSaveTarget(name: String(repeating: "x", count: 100)).validated(canManage: false, folderIDs: folders))
    }

    func testSharedNeedsManageViewsAndAKnownFolder() throws {
        XCTAssertThrowsError(try ViewSaveTarget(name: "A", shared: true).validated(canManage: false, folderIDs: folders)) {
            XCTAssertEqual($0 as? ViewSaveTarget.Problem, .sharedNeedsPermission)
        }
        XCTAssertThrowsError(try ViewSaveTarget(name: "A", shared: true, groupId: "ghost").validated(canManage: true, folderIDs: folders)) {
            XCTAssertEqual($0 as? ViewSaveTarget.Problem, .unknownFolder)
        }
        XCTAssertEqual(try ViewSaveTarget(name: "A", shared: true, groupId: "g2").validated(canManage: true, folderIDs: folders),
                       ViewSaveTarget(name: "A", shared: true, groupId: "g2"))
        XCTAssertEqual(try ViewSaveTarget(name: "A", shared: true).validated(canManage: true, folderIDs: folders).groupId, "", "top level is fine")
    }

    func testPrivateTargetDropsAFolder() throws {
        let t = try ViewSaveTarget(name: "A", shared: false, groupId: "g1").validated(canManage: true, folderIDs: folders)
        XCTAssertFalse(t.shared)
        XCTAssertEqual(t.groupId, "")
    }

    func testUpdateKeepsWhereASharedViewLives() {
        var grid = WorkspaceGrid(columns: 2, rows: 1)
        grid.place("c1", at: 0)
        let shared = V2.SavedView(id: "s", name: "Lobby", shared: true, groupId: "g1", sortOrder: 3)
        let input = ViewInputs.update(shared, grid: grid)
        XCTAssertEqual(input.shared, true)
        XCTAssertEqual(input.groupId, "g1")
        XCTAssertEqual(input.sortOrder, 3)
        XCTAssertEqual(input.tiles.map(\.cameraId), ["c1"])
        let priv = ViewInputs.update(V2.SavedView(id: "p", name: "Mine"), name: "Renamed")
        XCTAssertEqual(priv.name, "Renamed")
        XCTAssertNil(priv.shared)
        XCTAssertNil(priv.groupId)
    }

    func testNewAndMoveBuildTheRightBodies() throws {
        var grid = WorkspaceGrid(columns: 1, rows: 1)
        grid.place("c1", at: 0)
        let shared = ViewInputs.new(grid: grid, target: ViewSaveTarget(name: "N", shared: true, groupId: "g1"))
        XCTAssertEqual(shared.shared, true)
        XCTAssertEqual(shared.groupId, "g1")
        let priv = ViewInputs.new(grid: grid, target: ViewSaveTarget(name: "N"))
        XCTAssertNil(priv.shared)
        let moved = ViewInputs.move(V2.SavedView(id: "s", name: "S", shared: true, groupId: "g1"), toGroup: "")
        XCTAssertEqual(moved.groupId, "", "moving to the top level sends an explicit empty folder")
        let json = String(decoding: try JSONEncoder().encode(moved), as: UTF8.self)
        XCTAssertTrue(json.contains(#""groupId":"""#), json)
    }

    func testCanEdit() {
        let shared = V2.SavedView(id: "s", name: "S", shared: true)
        let priv = V2.SavedView(id: "p", name: "P")
        XCTAssertFalse(ViewInputs.canEdit(shared, canManage: false))
        XCTAssertTrue(ViewInputs.canEdit(shared, canManage: true))
        XCTAssertTrue(ViewInputs.canEdit(priv, canManage: false))
    }

    func testDirtyTrackingThroughGridEquality() {
        let view = V2.SavedView(id: "v", name: "V", columns: 2, rows: 1,
                                tiles: [V2.ViewTile(cameraId: "c1", x: 0, y: 0), V2.ViewTile(cameraId: "c2", x: 1, y: 0)])
        var grid = WorkspaceGrid(view: view.asView)
        XCTAssertEqual(grid, WorkspaceGrid(view: view.asView))
        grid.place("c1", at: 1)   // swap
        XCTAssertNotEqual(grid, WorkspaceGrid(view: view.asView))
        grid.place("c1", at: 0)   // swap back
        XCTAssertEqual(grid, WorkspaceGrid(view: view.asView), "undoing an edit is clean again")
    }
}

final class ViewApiTests: XCTestCase {
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
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Sentinel Operator (Test)")
    }

    func testManageViewsPermissionDecodes() throws {
        let me = try SentinelAPI.decode(V2.Me.self, from: Data("""
        {"id":"u1","name":"maria","role":"Supervisor","createdAt":1,"source":"local",
         "scope":{"allCameras":true,"groupIds":[]},"permissions":["viewLive","manageViews"]}
        """.utf8))
        XCTAssertTrue(me.can(.manageViews))
        let viewer = try SentinelAPI.decode(V2.Me.self, from: Data("""
        {"id":"u2","name":"v","role":"Viewer","createdAt":1,"source":"local","scope":{"allCameras":true,"groupIds":[]},"permissions":["viewLive"]}
        """.utf8))
        XCTAssertFalse(viewer.can(.manageViews))
    }

    func testSharedTreeDecodesFoldersAndSharedViews() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/view-groups")
            return .json(200, """
            {"groups":[{"id":"g1","parentId":"","name":"Sites","sortOrder":1},{"id":"g2","name":"HQ","sortOrder":0,"parentId":"g1"}],
             "views":[{"id":"v1","name":"Lobby","columns":2,"rows":1,"createdAt":1,"updatedAt":2,
                       "tiles":[{"cameraId":"c1","x":0,"y":0,"w":1,"h":1}],"shared":true,"groupId":"g2","sortOrder":4},
                      {"id":"v2","name":"Bare","columns":1,"rows":1,"createdAt":1,"updatedAt":2,"tiles":null,"shared":true}]}
            """)
        }
        let tree = try await makeAPI().sharedViewTree()
        XCTAssertEqual(tree.groups.map(\.parentId), ["", "g1"])
        XCTAssertEqual(tree.views.first?.groupId, "g2")
        XCTAssertEqual(tree.views.first?.sortOrder, 4)
        XCTAssertEqual(tree.views.last?.tiles, [])
        XCTAssertTrue(tree.views.allSatisfy(\.isShared))
    }

    func testPrivateViewsDecodeWithoutSharedFields() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(200, #"{"items":[{"id":"p1","name":"Mine","columns":3,"rows":3,"createdAt":1,"updatedAt":1,"tiles":[]}]}"#)
        }
        let views = try await makeAPI().savedViews()
        XCTAssertEqual(views.first?.isShared, false)
        XCTAssertEqual(views.first?.asView.name, "Mine")
    }

    func testCreateSharedViewSendsFolderAndSharedFlag() async throws {
        MockURLProtocol.handler = { _, _ in
            .json(201, #"{"id":"n1","name":"Wall","columns":1,"rows":1,"createdAt":1,"updatedAt":1,"tiles":[],"shared":true,"groupId":"g1"}"#)
        }
        let input = V2.SavedViewInput(name: "Wall", columns: 1, rows: 1, tiles: [], shared: true, groupId: "g1")
        let saved = try await makeAPI().createSavedView(input)
        XCTAssertEqual(saved.groupId, "g1")
        let sent = try XCTUnwrap(MockURLProtocol.requests.last)
        XCTAssertEqual(sent.request.httpMethod, "POST")
        XCTAssertEqual(sent.request.url?.path, "/api/v2/views")
        let body = try XCTUnwrap(sent.body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] })
        XCTAssertEqual(body["shared"] as? Bool, true)
        XCTAssertEqual(body["groupId"] as? String, "g1")
        XCTAssertNil(body["sortOrder"])
    }

    func testReplaceSharedViewWithoutPermissionSurfacesForbidden() async {
        MockURLProtocol.handler = { _, _ in
            .json(403, #"{"error":{"code":"forbidden","message":"changing shared views needs ManageViews"}}"#)
        }
        do {
            _ = try await makeAPI().replaceSavedView("v1", V2.SavedViewInput(name: "A", columns: 1, rows: 1, tiles: [], shared: true))
            XCTFail("expected 403")
        } catch let error as SentinelAPIError {
            XCTAssertTrue(error.isForbidden)
            XCTAssertEqual(error.localizedDescription, "changing shared views needs ManageViews")
        } catch { XCTFail("\(error)") }
    }

    func testFolderCrud() async throws {
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("POST", "/api/v2/view-groups"): return .json(201, #"{"id":"g9","parentId":"g1","name":"New","sortOrder":0}"#)
            case ("PUT", "/api/v2/view-groups/g9"): return .json(200, #"{"id":"g9","parentId":"","name":"Renamed","sortOrder":0}"#)
            case ("DELETE", "/api/v2/view-groups/g9"): return MockURLProtocol.Reply(status: 204, body: Data())
            default: return .json(404, #"{"error":{"code":"not_found","message":"no"}}"#)
            }
        }
        let api = makeAPI()
        let made = try await api.createViewGroup(V2.ViewGroupInput(name: "New", parentId: "g1"))
        XCTAssertEqual(made.parentId, "g1")
        let renamed = try await api.updateViewGroup("g9", V2.ViewGroupInput(name: "Renamed", parentId: ""))
        XCTAssertEqual(renamed.name, "Renamed")
        try await api.deleteViewGroup("g9")
        XCTAssertEqual(MockURLProtocol.requests.map { $0.request.httpMethod ?? "" }, ["POST", "PUT", "DELETE"])
    }

    func testDeletingANonEmptyFolderIsAConflict() async {
        MockURLProtocol.handler = { _, _ in
            .json(409, #"{"error":{"code":"conflict","message":"the folder still holds views"}}"#)
        }
        do {
            try await makeAPI().deleteViewGroup("g1")
            XCTFail("expected 409")
        } catch let error as SentinelAPIError {
            XCTAssertEqual(error.localizedDescription, "the folder still holds views")
        } catch { XCTFail("\(error)") }
    }
}

/// Wave 20 mac3: the collapsible views-and-cameras panel and the camera
/// tree without revoked recording servers.
final class CameraPanelTests: XCTestCase {
    func testCollapsedWidthIsAThinStrip() {
        XCTAssertEqual(CameraPanel.collapsedWidth, 28)
        XCTAssertEqual(CameraPanel.width(collapsed: true, expandedWidth: 300), 28)
        XCTAssertEqual(CameraPanel.width(collapsed: false, expandedWidth: 300), 300)
        XCTAssertEqual(CameraPanel.label, "Views & Cameras")
    }

    func testCollapsedStatePersistsPerSiteAndWindow() throws {
        let suite = "sentinel.test.cameraPanel.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }

        XCTAssertEqual(CameraPanel.storageKey(siteID: "s1"), "sentinel.cameraPanel.collapsed.s1.main")
        XCTAssertEqual(CameraPanel.storageKey(siteID: "s1", windowID: "monitor-2"), "sentinel.cameraPanel.collapsed.s1.monitor-2")
        XCTAssertNil(CameraPanel.loadCollapsed(siteID: "s1", defaults: defaults), "no choice yet")

        CameraPanel.saveCollapsed(true, siteID: "s1", defaults: defaults)
        XCTAssertEqual(CameraPanel.loadCollapsed(siteID: "s1", defaults: defaults), true)
        XCTAssertNil(CameraPanel.loadCollapsed(siteID: "s2", defaults: defaults), "other sites keep their own")
        XCTAssertNil(CameraPanel.loadCollapsed(siteID: "s1", windowID: "monitor-2", defaults: defaults))

        CameraPanel.saveCollapsed(false, siteID: "s1", defaults: defaults)
        XCTAssertEqual(CameraPanel.loadCollapsed(siteID: "s1", defaults: defaults), false)
    }

    private func server(_ id: String, revoked: Bool, online: Bool = true) throws -> V2.RecordingServer {
        let json = """
        {"id":"\(id)","name":"RS \(id)","local":false,"publicUrl":"","enrolledAt":0,"lastSeenAt":1,"revoked":\(revoked),
         "online":\(online),"version":"3.3.0","configRev":1,"mediaRunning":true,"cameras":0,
         "capabilities":{"detection":true,"objects":false,"webrtc":true}}
        """
        return try JSONDecoder().decode(V2.RecordingServer.self, from: Data(json.utf8))
    }

    func testRevokedServersLeaveTheCameraTree() throws {
        let servers = [try server("local", revoked: false), try server("1", revoked: true, online: false),
                       try server("2", revoked: true, online: false), try server("3", revoked: false)]
        let items = [CameraTree.Item(id: "c1", name: "Door", serverID: "local", online: true),
                     CameraTree.Item(id: "c2", name: "Yard", serverID: "3", online: true)]
        let parents = CameraTree.serverParents(servers, items: items)
        XCTAssertEqual(parents.map(\.id), ["local", "3"], "revoked RS 1 / RS 2 are hidden")

        let tree = CameraTree.build(items: items, parents: parents, grouping: .server, query: "")
        XCTAssertEqual(tree.map(\.id), ["local", "3"])
        XCTAssertEqual(tree.map(\.totalCount), [1, 1])
    }

    func testRevokedServerStillHoldingCamerasKeepsItsName() throws {
        let servers = [try server("local", revoked: false), try server("old", revoked: true, online: false)]
        let items = [CameraTree.Item(id: "c1", name: "Gate", serverID: "old", online: false)]
        let parents = CameraTree.serverParents(servers, items: items)
        XCTAssertEqual(parents.map(\.id), ["local", "old"])
        XCTAssertEqual(parents.last?.name, "RS old", "its cameras don't fall under a bare id")
    }

    func testServerParentsWithoutRevocationAreUnchanged() throws {
        let servers = [try server("a", revoked: false, online: false), try server("b", revoked: false)]
        XCTAssertEqual(CameraTree.serverParents(servers, items: []),
                       [CameraTree.Parent(id: "a", name: "RS a", online: false), CameraTree.Parent(id: "b", name: "RS b", online: true)])
    }
}
