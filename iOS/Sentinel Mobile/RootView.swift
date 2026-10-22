// RootView.swift
// Top-level navigation. Shows the site list until a Management Server is
// chosen, the sign-in screen until the user is signed in, then the main
// navigation: a tab bar (Home · Live · Events · Settings) in compact width, a
// sidebar (adding People and Insights) in regular width (WAVE19).

import SwiftUI

struct RootView: View {
    @ObservedObject var session = SentinelSession.shared

    var body: some View {
        Group {
            switch session.phase {
            case .noSite:
                WelcomeView()
            case .signedOut:
                SignInView()
            case .signedIn, .demo:
                MainTabView()
            }
        }
        // Widgets and the alarm Live Activity open sentinelmobile:// links.
        .onOpenURL { url in
            // Kept until the tabs show (a cold launch is still signing in).
            if let link = SentinelDeepLink(url: url) { AppStore.shared.open(link) }
        }
        .onAppear {
            session.start()
            #if DEBUG
            applyLaunchArguments()
            #endif
        }
    }

    #if DEBUG
    /// Dev-only launch arguments for the Simulator (compiled out of Release):
    ///   --demo                         jump into Demo Mode
    ///   --site-url <url> --user <name> --password <pw>   add the site and sign in
    ///   --tab live|events|alarms|people|insights|settings, --route <cameraId>
    ///   --select-alarm <alarmId>, --focus <cameraId>   (regular-width panes)
    private func applyLaunchArguments() {
        let args = CommandLine.arguments
        func value(after flag: String) -> String? {
            guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
            return args[i + 1]
        }
        if let url = value(after: "--site-url"), session.isSignedIn == false {
            let user = value(after: "--user"), password = value(after: "--password")
            let remote = value(after: "--remote-url")
            Task {
                do {
                    let site = try await session.addSite(name: value(after: "--site-name") ?? "", address: url, remoteAddress: remote)
                    session.select(site)
                    try await Task.sleep(nanoseconds: 300_000_000)
                    if let user, let password { try await session.signIn(name: user, password: password) }
                } catch {
                    print("launch sign-in failed: \(error.localizedDescription)")
                }
            }
        }
        if args.contains("--demo"), session.isSignedIn == false {
            session.enterDemoMode()
            AppStore.shared.suppressDemoBanner = true   // clean chrome for screenshots
        }
        switch value(after: "--tab") {
        case "live": AppStore.shared.selectedTab = .live
        case "events": AppStore.shared.selectedTab = .events
        case "alarms": AppStore.shared.openAlarms()
        case "settings": AppStore.shared.selectedTab = .settings
        case "people": AppStore.shared.selectedTab = .people
        case "insights": AppStore.shared.selectedTab = .insights
        default: break
        }
        // Screenshots of the regular-width panes (WAVE19).
        if let id = value(after: "--select-alarm") {
            AppStore.shared.eventsSelection = .alarm(id)
            AppStore.shared.openAlarms()
        }
        if let id = value(after: "--focus") {
            AppStore.shared.focusLive(cameraID: id)
        }
        if let id = value(after: "--route") {
            AppStore.shared.route(toCameraID: id)
        }
    }
    #endif
}

struct MainTabView: View {
    @ObservedObject var session = SentinelSession.shared
    @ObservedObject var store = AppStore.shared
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.horizontalSizeClass) private var sizeClass

    @State private var tabBarGeometry: TabBarGeometry?

    private var width: LayoutWidth { LayoutWidth(sizeClass) }

    /// Trailing room scrolling content leaves for a side-mounted tab bar.
    private var sideTabBarInset: CGFloat {
        guard let g = tabBarGeometry else { return 0 }
        return LayoutLogic.trailingTabBarInset(tabBar: g.frame, containerWidth: g.windowWidth,
                                               safeTrailing: g.safeTrailing, model: DeviceModel.identifier, width: width)
    }

    var body: some View {
        VStack(spacing: 0) {
            if session.isDemoMode && !store.suppressDemoBanner { demoBanner }

            if LayoutLogic.usesSidebar(for: width) {
                SidebarShell()
            } else {
                tabBar
            }
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        // A fold or unfold swaps the chrome; keep the user on the same screen.
        .onAppear { adapt(to: width) }
        .onChange(of: width) { adapt(to: $0) }
        .onChange(of: store.selectedTab) { _ in adapt(to: width) }
        .sheet(isPresented: Binding(get: { store.routedAlarmID != nil },
                                    set: { if $0 == false { store.routedAlarmID = nil } })) {
            if let id = store.routedAlarmID {
                NavigationStack {
                    AlarmDetailView(alarmID: id)
                        .navigationDestination(for: V2.Camera.self) { CameraDetailView(camera: $0) }
                        .toolbar {
                            ToolbarItem(placement: .cancellationAction) {
                                Button("Done") { store.routedAlarmID = nil }
                            }
                        }
                }
                .preferredColorScheme(.dark)
            }
        }
        .onAppear { store.startAuto() }
        .onDisappear { store.stopAuto() }
        // Another site / sign-in: reload and reconnect the live stream.
        .onChange(of: session.phase) { _ in store.startAuto() }
        // The stream can't survive the background; reconnect on return.
        .onChange(of: scenePhase) { phase in
            if phase == .active { store.startAuto() } else if phase == .background { store.stopAuto() }
        }
    }

    /// Compact width (phone, folded Duo): the system tab bar, as before.
    private var tabBar: some View {
        TabView(selection: $store.selectedTab) {
            HomeView()
                .modifier(TrailingClearance(inset: sideTabBarInset))
                .tabItem { Label("Home", systemImage: "house.fill") }
                .tag(AppTab.home)

            LiveGridView()
                .modifier(TrailingClearance(inset: sideTabBarInset))
                .tabItem { Label("Live", systemImage: "video.fill") }
                .tag(AppTab.live)

            EventsView()
                .modifier(TrailingClearance(inset: sideTabBarInset))
                .tabItem { Label("Events", systemImage: "sparkles.rectangle.stack.fill") }
                .badge(store.newAlertCount)
                .tag(AppTab.events)

            SettingsView()
                .modifier(TrailingClearance(inset: sideTabBarInset))
                .tabItem { Label("Settings", systemImage: "gearshape.fill") }
                .tag(AppTab.settings)
        }
        .tint(SentinelTheme.accent)
        // The Duo's outer screen stands the bar up on the trailing edge;
        // keep scrolling content clear of it (wave19-fixes).
        .background(TabBarFrameProbe { tabBarGeometry = $0 })
    }

    /// Moves People and Insights between the sidebar and Home's stack.
    private func adapt(to width: LayoutWidth) {
        let next = LayoutLogic.navigationAfterWidthChange(tab: store.selectedTab, homePush: store.homePush, to: width)
        if next.tab != store.selectedTab { store.selectedTab = next.tab }
        if next.homePush != store.homePush { store.homePush = next.homePush }
    }

    private var demoBanner: some View {
        HStack(spacing: 8) {
            Image(systemName: "play.rectangle.fill")
                .foregroundStyle(SentinelTheme.recording)
            Text("Demo Mode")
                .font(.caption.weight(.bold))
                .foregroundStyle(.white)
            Text("· sample data")
                .font(.caption)
                .foregroundStyle(.white.opacity(0.6))
            Spacer()
            Button("Exit") { session.leaveDemo() }
                .font(.caption.weight(.semibold))
                .foregroundStyle(SentinelTheme.accent)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(SentinelTheme.chrome)
        .overlay(alignment: .bottom) {
            Rectangle().fill(SentinelTheme.recording.opacity(0.45)).frame(height: 1)
        }
    }
}

// MARK: - Side-mounted tab bar (wave19-fixes)

/// What the window says about the compact TabView's bar: its frame (nil
/// when there is none to read), the window width and its trailing safe area.
struct TabBarGeometry: Equatable {
    var frame: CGRect?
    var windowWidth: CGFloat
    var safeTrailing: CGFloat
}

/// Reads where UIKit put the compact TabView's bar. On the iPhone Duo's
/// outer screen iOS 27 draws it as a vertical capsule at the bottom right,
/// and SwiftUI offers no placement or inset for that, so the frame is read
/// from the hosting window (`LayoutLogic.trailingTabBarInset`).
struct TabBarFrameProbe: UIViewRepresentable {
    let onChange: (TabBarGeometry) -> Void

    func makeUIView(context: Context) -> ProbeView {
        let v = ProbeView()
        v.onChange = onChange
        v.isUserInteractionEnabled = false
        return v
    }

    func updateUIView(_ uiView: ProbeView, context: Context) {
        uiView.onChange = onChange
        uiView.report()
    }

    final class ProbeView: UIView {
        var onChange: ((TabBarGeometry) -> Void)?
        private var last: TabBarGeometry?

        override func didMoveToWindow() { super.didMoveToWindow(); report() }
        override func layoutSubviews() { super.layoutSubviews(); report() }
        override func safeAreaInsetsDidChange() { super.safeAreaInsetsDidChange(); report() }

        func report() {
            DispatchQueue.main.async { [weak self] in self?.measure() }
        }

        private func measure() {
            guard let window else { return }
            var frame: CGRect?
            if let bar = Self.tabBar(in: window.rootViewController), bar.isHidden == false, bar.alpha > 0.01 {
                frame = bar.convert(bar.bounds, to: window)
            }
            let value = TabBarGeometry(frame: frame, windowWidth: window.bounds.width,
                                       safeTrailing: window.safeAreaInsets.right)
            guard value != last else { return }
            last = value
            #if DEBUG
            if CommandLine.arguments.contains("--dump-tabbar") {
                NSLog("%@", "TABBAR frame=\(String(describing: frame)) window=\(window.bounds) safe=\(window.safeAreaInsets)" as NSString)
            }
            #endif
            onChange?(value)
        }

        private static func tabBar(in vc: UIViewController?) -> UITabBar? {
            guard let vc else { return nil }
            if let tbc = vc as? UITabBarController { return tbc.tabBar }
            for child in vc.children { if let bar = tabBar(in: child) { return bar } }
            return nil
        }
    }
}

enum DeviceModel {
    /// "iPhone19,4" and the like; the simulator reports the model it runs.
    static let identifier: String = {
        if let sim = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"] { return sim }
        var info = utsname()
        uname(&info)
        return withUnsafeBytes(of: &info.machine) { raw in
            String(decoding: raw.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
    }()
}

/// A trailing safe-area inset (iOS 16 has no `safeAreaPadding`): content
/// lays out and scrolls inside it, backgrounds that ignore the safe area
/// still reach the edge.
struct TrailingClearance: ViewModifier {
    let inset: CGFloat

    func body(content: Content) -> some View {
        content.safeAreaInset(edge: .trailing, spacing: 0) {
            Color.clear.frame(width: inset).allowsHitTesting(false)
        }
    }
}
