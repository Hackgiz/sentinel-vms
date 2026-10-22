import XCTest
import SwiftUI
@testable import Sentinel_Mobile

/// The iPhone Duo (WAVE18-DUO.md): width-aware layout decisions, and video
/// that survives a fold. No server: the players never get a token here.
final class LayoutTests: XCTestCase {
    // MARK: - LayoutLogic

    func testWidthFromSizeClass() {
        XCTAssertEqual(LayoutWidth(.regular), .regular)
        XCTAssertEqual(LayoutWidth(.compact), .compact)
        XCTAssertEqual(LayoutWidth(nil), .compact, "unknown shows the phone layout")
    }

    func testLiveColumnDefaults() {
        XCTAssertEqual(LayoutLogic.columns(for: .compact), 2)
        XCTAssertEqual(LayoutLogic.columns(for: .regular), 3)
    }

    func testFourByFourOnlyWhenRegular() {
        XCTAssertEqual(LayoutLogic.columnChoices(for: .compact), [1, 2, 3])
        XCTAssertEqual(LayoutLogic.columnChoices(for: .regular), [1, 2, 3, 4])
    }

    func testRememberedColumnsFallBackWhenNotOffered() {
        XCTAssertEqual(LayoutLogic.liveColumns(stored: 0, for: .compact), 2, "never chosen")
        XCTAssertEqual(LayoutLogic.liveColumns(stored: 0, for: .regular), 3, "never chosen")
        XCTAssertEqual(LayoutLogic.liveColumns(stored: 1, for: .compact), 1)
        XCTAssertEqual(LayoutLogic.liveColumns(stored: 4, for: .regular), 4)
        XCTAssertEqual(LayoutLogic.liveColumns(stored: 4, for: .compact), 2, "4×4 isn't offered folded")
        XCTAssertEqual(LayoutLogic.liveColumns(stored: 9, for: .regular), 3)
    }

    func testEachWidthRemembersItsOwnChoice() {
        XCTAssertNotEqual(LayoutLogic.columnsStorageKey(for: .compact), LayoutLogic.columnsStorageKey(for: .regular))
        XCTAssertEqual(LayoutLogic.columnsStorageKey(for: .regular), "live.columns.regular")
    }

    func testGridMenuSymbolsAndLabels() {
        XCTAssertEqual(LayoutLogic.gridSymbol(columns: 1), "square")
        XCTAssertEqual(LayoutLogic.gridSymbol(columns: 2), "square.grid.2x2")
        XCTAssertEqual(LayoutLogic.gridSymbol(columns: 3), "square.grid.3x3")
        XCTAssertEqual(LayoutLogic.gridSymbol(columns: 4), "square.grid.4x3.fill")
        XCTAssertEqual(LayoutLogic.gridLabel(columns: 4), "4 × 4")
    }

    func testSplitOnlyWhenRegular() {
        XCTAssertTrue(LayoutLogic.usesSplit(for: .regular))
        XCTAssertFalse(LayoutLogic.usesSplit(for: .compact))
    }

    func testTileSizes() {
        XCTAssertEqual(LayoutLogic.tileMinimum(for: .compact), 150)
        XCTAssertEqual(LayoutLogic.tileMinimum(for: .regular), 220)
        XCTAssertEqual(LayoutLogic.stripCardSize(for: .compact), CGSize(width: 150, height: 90))
        XCTAssertEqual(LayoutLogic.stripCardSize(for: .regular), CGSize(width: 220, height: 132))
    }

    func testControlsBesideVideoOnlyWideAndShort() {
        XCTAssertTrue(LayoutLogic.controlsBesideVideo(width: 1000, height: 600), "unfolded landscape")
        XCTAssertFalse(LayoutLogic.controlsBesideVideo(width: 760, height: 900), "unfolded portrait")
        XCTAssertFalse(LayoutLogic.controlsBesideVideo(width: 680, height: 300), "phone landscape is too narrow")
        XCTAssertFalse(LayoutLogic.controlsBesideVideo(width: 400, height: 800), "phone portrait")
    }

    // MARK: - PlayerPool

    @MainActor
    private final class FakePlayer: PooledPlayer {
        let key: String
        var activations = 0
        var deactivations = 0
        init(key: String) { self.key = key }
        func activate() { activations += 1 }
        func deactivate() { deactivations += 1 }
    }

    @MainActor
    func testPoolKeepsOneModelPerCamera() {
        let pool = PlayerPool<FakePlayer>(grace: 60) { FakePlayer(key: $0) }
        let a = pool.model(for: "cam-1")
        XCTAssertTrue(a === pool.model(for: "cam-1"))
        XCTAssertFalse(a === pool.model(for: "cam-2"))
    }

    @MainActor
    func testReleaseThenRetainWithinGraceKeepsPlaying() async throws {
        let pool = PlayerPool<FakePlayer>(grace: 0.1) { FakePlayer(key: $0) }
        let model = pool.model(for: "cam-1")
        pool.retain("cam-1")
        pool.release("cam-1")            // the old layout's view goes…
        XCTAssertTrue(pool.isStopPending("cam-1"))
        pool.retain("cam-1")             // …and the new one appears
        XCTAssertFalse(pool.isStopPending("cam-1"))
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(model.deactivations, 0)
        XCTAssertEqual(pool.holdCount("cam-1"), 1)
        XCTAssertTrue(model === pool.model(for: "cam-1"))
    }

    @MainActor
    func testStopsAfterGraceWhenNobodyHolds() async throws {
        let pool = PlayerPool<FakePlayer>(grace: 0.05) { FakePlayer(key: $0) }
        let model = pool.model(for: "cam-1")
        pool.retain("cam-1")
        pool.retain("cam-1")             // tile and detail both showing it
        pool.release("cam-1")
        XCTAssertFalse(pool.isStopPending("cam-1"), "still held once")
        pool.release("cam-1")
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(model.deactivations, 1)
        XCTAssertFalse(pool.isStopPending("cam-1"))
        // The model stays cached, so a view still holding it is never stale.
        XCTAssertTrue(model === pool.model(for: "cam-1"))
        pool.release("cam-1")            // unbalanced release is ignored
        XCTAssertEqual(pool.holdCount("cam-1"), 0)
    }

    // MARK: - Fold with a hosted view

    /// Rebuilds its whole tree when the size class flips, the worst case for
    /// a view-owned player: the LiveStreamView before and after are
    /// different views.
    private struct FoldHost: View {
        let camera: V2.Camera
        let sizeClass: UserInterfaceSizeClass

        var body: some View {
            Group {
                if sizeClass == .regular {
                    HStack { LiveStreamView(camera: camera).frame(width: 320, height: 180) }
                } else {
                    VStack { LiveStreamView(camera: camera).frame(width: 160, height: 90) }
                }
            }
            .environment(\.horizontalSizeClass, sizeClass)
        }
    }

    @MainActor
    func testLivePlayerSurvivesSizeClassChange() async throws {
        var camera = try XCTUnwrap(DemoData.cameras.first { $0.status.online })
        camera.id = "fold-test-\(UUID().uuidString)"

        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 400, height: 800))
        let host = UIHostingController(rootView: FoldHost(camera: camera, sizeClass: .compact))
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        try await Task.sleep(nanoseconds: 300_000_000)

        let model = VideoPools.live.model(for: camera.id)
        let player = model.player
        XCTAssertEqual(VideoPools.live.holdCount(camera.id), 1, "the compact view holds the camera")

        host.rootView = FoldHost(camera: camera, sizeClass: .regular)   // unfold
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertTrue(VideoPools.live.model(for: camera.id) === model)
        XCTAssertTrue(VideoPools.live.model(for: camera.id).player === player, "same AVPlayer after unfolding")
        XCTAssertEqual(VideoPools.live.holdCount(camera.id), 1, "the regular view took over the hold")
        XCTAssertFalse(VideoPools.live.isStopPending(camera.id))

        host.rootView = FoldHost(camera: camera, sizeClass: .compact)   // fold again
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertTrue(VideoPools.live.model(for: camera.id).player === player, "same AVPlayer after folding")
        XCTAssertEqual(VideoPools.live.holdCount(camera.id), 1)
    }

    @MainActor
    func testPlaybackModelIsPooledPerCamera() {
        let a = VideoPools.playback.model(for: "fold-playback")
        XCTAssertTrue(a === VideoPools.playback.model(for: "fold-playback"))
        XCTAssertTrue(a.player === VideoPools.playback.model(for: "fold-playback").player)
    }

    // MARK: - WAVE19: designed for the unfolded Duo

    func testSidebarOnlyWhenRegular() {
        XCTAssertTrue(LayoutLogic.usesSidebar(for: .regular))
        XCTAssertFalse(LayoutLogic.usesSidebar(for: .compact), "compact keeps the TabView")
        XCTAssertEqual(LayoutLogic.sidebarWidth, 84)
    }

    func testSidebarAndTabBarSections() {
        XCTAssertEqual(LayoutLogic.sidebarTabs(showsPeopleAndInsights: true), [.home, .live, .events, .people, .insights])
        XCTAssertEqual(LayoutLogic.sidebarTabs(showsPeopleAndInsights: false), [.home, .live, .events])
        XCTAssertEqual(LayoutLogic.tabBarTabs, [.home, .live, .events, .settings])
        XCTAssertFalse(LayoutLogic.tabBarTabs.contains(.people), "People is pushed from Home when folded")
    }

    func testFoldingMovesPeopleAndInsightsOntoHome() {
        var r = LayoutLogic.navigationAfterWidthChange(tab: .people, homePush: nil, to: .compact)
        XCTAssertEqual(r.tab, .home); XCTAssertEqual(r.homePush, .people)
        r = LayoutLogic.navigationAfterWidthChange(tab: .insights, homePush: nil, to: .compact)
        XCTAssertEqual(r.tab, .home); XCTAssertEqual(r.homePush, .insights)
        r = LayoutLogic.navigationAfterWidthChange(tab: .events, homePush: nil, to: .compact)
        XCTAssertEqual(r.tab, .events); XCTAssertNil(r.homePush)
        r = LayoutLogic.navigationAfterWidthChange(tab: .live, homePush: .people, to: .compact)
        XCTAssertEqual(r.tab, .live); XCTAssertEqual(r.homePush, .people, "Home's stack survives a tab switch")
    }

    func testUnfoldingSelectsThePushedScreen() {
        var r = LayoutLogic.navigationAfterWidthChange(tab: .home, homePush: .insights, to: .regular)
        XCTAssertEqual(r.tab, .insights); XCTAssertNil(r.homePush)
        r = LayoutLogic.navigationAfterWidthChange(tab: .live, homePush: .people, to: .regular)
        XCTAssertEqual(r.tab, .live); XCTAssertNil(r.homePush)
        r = LayoutLogic.navigationAfterWidthChange(tab: .settings, homePush: nil, to: .regular)
        XCTAssertEqual(r.tab, .settings)
    }

    func testDashboardIsCappedAndSplit55_45() {
        XCTAssertEqual(LayoutLogic.dashboardContentWidth(available: 1600, padding: 24), 1100, "landscape caps")
        XCTAssertEqual(LayoutLogic.dashboardContentWidth(available: 800, padding: 24), 752)
        XCTAssertEqual(LayoutLogic.dashboardContentWidth(available: 20, padding: 24), 0)
        let cols = LayoutLogic.dashboardColumns(contentWidth: 1100, spacing: 24)
        XCTAssertEqual(cols.left + cols.right, 1076)
        XCTAssertEqual(cols.left, 591)
        XCTAssertGreaterThan(cols.left, cols.right)
        XCTAssertEqual(LayoutLogic.dashboardAlarmLimit, 3)
        XCTAssertEqual(LayoutLogic.dashboardPreviewColumns, 2)
    }

    func testListColumnStaysBetween400And440() {
        XCTAssertEqual(LayoutLogic.listColumnWidth(available: 700), 400)
        XCTAssertEqual(LayoutLogic.listColumnWidth(available: 1050), 420)
        XCTAssertEqual(LayoutLogic.listColumnWidth(available: 2000), 440)
    }

    func testAlarmActionsOnlyOnSelectedCardWhenRegular() {
        XCTAssertTrue(LayoutLogic.alarmActionsVisible(width: .compact, inList: true, selected: false))
        XCTAssertTrue(LayoutLogic.alarmActionsVisible(width: .regular, inList: false, selected: false), "Home always shows Acknowledge")
        XCTAssertFalse(LayoutLogic.alarmActionsVisible(width: .regular, inList: true, selected: false))
        XCTAssertTrue(LayoutLogic.alarmActionsVisible(width: .regular, inList: true, selected: true))
    }

    func testFocusLayoutOnlyWhenRegular() {
        XCTAssertEqual(LayoutLogic.liveLayout(stored: "focus", for: .regular), .focus)
        XCTAssertEqual(LayoutLogic.liveLayout(stored: "focus", for: .compact), .grid, "folded is always the grid")
        XCTAssertEqual(LayoutLogic.liveLayout(stored: nil, for: .regular), .grid)
        XCTAssertEqual(LayoutLogic.liveLayout(stored: "bogus", for: .regular), .grid)
    }

    func testFocusArrangement() {
        let ids = ["a", "b", "c"]
        var r = LayoutLogic.focusArrangement(cameraIDs: ids, focused: "b")
        XCTAssertEqual(r.main, "b"); XCTAssertEqual(r.others, ["a", "c"])
        r = LayoutLogic.focusArrangement(cameraIDs: ids, focused: nil)
        XCTAssertEqual(r.main, "a"); XCTAssertEqual(r.others, ["b", "c"])
        r = LayoutLogic.focusArrangement(cameraIDs: ids, focused: "gone")
        XCTAssertEqual(r.main, "a", "a removed camera falls back to the first")
        r = LayoutLogic.focusArrangement(cameraIDs: [], focused: "a")
        XCTAssertNil(r.main); XCTAssertEqual(r.others, [])
        XCTAssertEqual(LayoutLogic.focusColumnWidth(available: 600), 220)
        XCTAssertEqual(LayoutLogic.focusColumnWidth(available: 1000), 250)
        XCTAssertEqual(LayoutLogic.focusColumnWidth(available: 2000), 320)
    }

    /// Focus swaps move streams between the big slot and the column; the
    /// pool hands back the same AVPlayers, so nothing reconnects.
    @MainActor
    func testFocusSwapKeepsPlayers() async throws {
        let store = AppStore.shared
        let saved = (store.cameras, store.focusedCameraID)
        defer { store.cameras = saved.0; store.focusedCameraID = saved.1 }
        let suffix = UUID().uuidString
        let cams: [V2.Camera] = DemoData.cameras.filter(\.isOnline).prefix(3).map {
            var c = $0; c.id = "\($0.id)-swap-\(suffix)"; return c
        }
        store.cameras = cams
        store.focusedCameraID = cams[0].id

        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1100, height: 800))
        let host = UIHostingController(rootView: LiveFocusLayout(cameras: cams))
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        try await Task.sleep(nanoseconds: 300_000_000)

        let players = cams.map { VideoPools.live.model(for: $0.id).player }
        for cam in cams { XCTAssertEqual(VideoPools.live.holdCount(cam.id), 1, "\(cam.name) shown once") }

        store.focusedCameraID = cams[2].id      // tap a small tile
        try await Task.sleep(nanoseconds: 300_000_000)
        for (cam, player) in zip(cams, players) {
            XCTAssertTrue(VideoPools.live.model(for: cam.id).player === player, "same AVPlayer after the swap")
            XCTAssertEqual(VideoPools.live.holdCount(cam.id), 1)
            XCTAssertFalse(VideoPools.live.isStopPending(cam.id))
        }
    }

    /// The real shell: unfolding swaps the TabView for the sidebar and
    /// rebuilds every screen. The tab, the open alarm, the focused camera
    /// and the AVPlayers all come through.
    private struct ShellHost: View {
        let sizeClass: UserInterfaceSizeClass
        var body: some View {
            MainTabView().environment(\.horizontalSizeClass, sizeClass)
        }
    }

    @MainActor
    func testFoldKeepsSelectionsAndPlayers() async throws {
        let store = AppStore.shared
        let suffix = UUID().uuidString
        let cams: [V2.Camera] = DemoData.cameras.filter(\.isOnline).prefix(3).map {
            var c = $0; c.id = "\($0.id)-fold-\(suffix)"; return c
        }
        let alarmID = "fold-alarm-\(suffix)"
        UserDefaults.standard.set(LayoutLogic.LiveLayout.focus.rawValue, forKey: LayoutLogic.liveLayoutStorageKey)
        defer {
            UserDefaults.standard.removeObject(forKey: LayoutLogic.liveLayoutStorageKey)
            store.selectedTab = .home
            store.eventsSelection = nil
            store.focusedCameraID = nil
            store.cameras = []
        }
        store.cameras = cams
        store.selectedTab = .live
        store.focusedCameraID = cams[1].id
        store.eventsSelection = .alarm(alarmID)

        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1100, height: 800))
        let host = UIHostingController(rootView: ShellHost(sizeClass: .regular))
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        try await Task.sleep(nanoseconds: 500_000_000)
        store.cameras = cams   // the shell's startAuto may have reloaded
        try await Task.sleep(nanoseconds: 300_000_000)

        let players = cams.map { VideoPools.live.model(for: $0.id).player }
        XCTAssertGreaterThan(VideoPools.live.holdCount(cams[1].id), 0, "the focused camera is showing")

        window.frame = CGRect(x: 0, y: 0, width: 400, height: 800)
        host.rootView = ShellHost(sizeClass: .compact)     // fold
        try await Task.sleep(nanoseconds: 600_000_000)
        XCTAssertEqual(store.selectedTab, .live)
        XCTAssertEqual(store.focusedCameraID, cams[1].id)
        XCTAssertEqual(store.eventsSelection, .alarm(alarmID))
        // (The system TabView doesn't render its tabs in a window without a
        // scene, so the folded grid's holds aren't checked here; the fold
        // test above covers a view that is rebuilt across the size change.)

        window.frame = CGRect(x: 0, y: 0, width: 1100, height: 800)
        host.rootView = ShellHost(sizeClass: .regular)     // unfold
        try await Task.sleep(nanoseconds: 600_000_000)
        XCTAssertEqual(store.selectedTab, .live)
        XCTAssertEqual(store.focusedCameraID, cams[1].id)
        XCTAssertEqual(store.eventsSelection, .alarm(alarmID))
        for (cam, player) in zip(cams, players) {
            XCTAssertTrue(VideoPools.live.model(for: cam.id).player === player, "same AVPlayer across the fold")
        }
        XCTAssertGreaterThan(VideoPools.live.holdCount(cams[1].id), 0, "Focus shows the same camera again")
    }

    // MARK: - wave19-fixes: dashboard stills and the side-mounted tab bar

    private func event(_ id: String, camera: String, at ms: Int64, thumb: Bool = true) -> V2.Event {
        V2.Event(id: id, cameraId: camera, kind: "Person", label: "person", score: 0.9, box: nil,
                 createdAt: ms, hasThumbnail: thumb, alarmId: nil, description: nil,
                 threat: nil, anomaly: nil, reason: nil, tags: nil)
    }

    func testDashboardStillIsNewestSnapshotOfThatCamera() {
        let events = [
            event("old", camera: "cam-a", at: 1_000),
            event("other", camera: "cam-b", at: 9_000),
            event("new-no-frame", camera: "cam-a", at: 8_000, thumb: false),
            event("new", camera: "cam-a", at: 5_000),
        ]
        XCTAssertEqual(DashboardCameraTile.latestStill(for: "cam-a", in: events, demo: false)?.id, "new",
                       "newest with a frame, whatever the list order")
        XCTAssertEqual(DashboardCameraTile.latestStill(for: "cam-b", in: events, demo: false)?.id, "other")
        XCTAssertNil(DashboardCameraTile.latestStill(for: "cam-c", in: events, demo: false), "no event yet: placeholder")
        XCTAssertNil(DashboardCameraTile.latestStill(for: "cam-a", in: events, demo: true), "Demo Mode has nothing to fetch")
    }

    func testDashboardStillFollowsNewEvents() {
        var events = [event("first", camera: "cam-a", at: 1_000)]
        XCTAssertEqual(DashboardCameraTile.latestStill(for: "cam-a", in: events, demo: false)?.id, "first")
        events.insert(event("second", camera: "cam-a", at: 2_000), at: 0)
        XCTAssertEqual(DashboardCameraTile.latestStill(for: "cam-a", in: events, demo: false)?.id, "second")
    }

    func testSideMountedTabBarDetection() {
        // Measured: iPhone 17 Pro bottom bar, Duo outer-screen capsule (iOS 27.1 Simulator).
        XCTAssertFalse(LayoutLogic.isSideMounted(tabBar: CGRect(x: 0, y: 791, width: 402, height: 83)))
        XCTAssertTrue(LayoutLogic.isSideMounted(tabBar: CGRect(x: 397, y: 0, width: 69, height: 678)))
        XCTAssertFalse(LayoutLogic.isSideMounted(tabBar: .zero))
    }

    func testSideTabBarInsetOnlyWhenTheBarComesClose() {
        // The Duo as measured: window 466 wide, 84 pt trailing safe area,
        // bar from x 397. Content ends at 382, 15 pt short of the bar.
        let duoBar = CGRect(x: 397, y: 0, width: 69, height: 678)
        XCTAssertEqual(LayoutLogic.sideTabBarInset(tabBar: duoBar, containerWidth: 466, safeTrailing: 84, width: .compact), 1,
                       "topped up to the 16 pt gap")
        XCTAssertEqual(LayoutLogic.sideTabBarInset(tabBar: duoBar, containerWidth: 466, safeTrailing: 84, width: .compact, gap: 12), 0)
        // No system safe area: content would run under the bar.
        XCTAssertEqual(LayoutLogic.sideTabBarInset(tabBar: duoBar, containerWidth: 466, safeTrailing: 0, width: .compact), 85)
        // Regular width uses the sidebar; a bottom bar never insets.
        XCTAssertEqual(LayoutLogic.sideTabBarInset(tabBar: duoBar, containerWidth: 466, safeTrailing: 0, width: .regular), 0)
        XCTAssertEqual(LayoutLogic.sideTabBarInset(tabBar: CGRect(x: 0, y: 791, width: 402, height: 83),
                                                   containerWidth: 402, safeTrailing: 0, width: .compact), 0)
    }

    func testSideTabBarFallbackOnlyOnTheFoldedDuo() {
        XCTAssertEqual(LayoutLogic.fallbackSideTabBarInset(model: "iPhone19,4", width: .compact, safeTrailing: 0), 84)
        XCTAssertEqual(LayoutLogic.fallbackSideTabBarInset(model: "iPhone19,4", width: .compact, safeTrailing: 84), 0,
                       "the system safe area already clears it")
        XCTAssertEqual(LayoutLogic.fallbackSideTabBarInset(model: "iPhone19,4", width: .regular, safeTrailing: 0), 0)
        XCTAssertEqual(LayoutLogic.fallbackSideTabBarInset(model: "iPhone18,1", width: .compact, safeTrailing: 0), 0)
        // A readable frame wins over the fallback; none falls back.
        XCTAssertEqual(LayoutLogic.trailingTabBarInset(tabBar: nil, containerWidth: 466, safeTrailing: 0,
                                                       model: "iPhone19,4", width: .compact), 84)
        XCTAssertEqual(LayoutLogic.trailingTabBarInset(tabBar: CGRect(x: 0, y: 791, width: 402, height: 83), containerWidth: 402,
                                                       safeTrailing: 0, model: "iPhone19,4", width: .compact), 0)
    }
}
