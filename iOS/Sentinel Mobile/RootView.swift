// RootView.swift
// Top-level navigation. Shows the site list until a Management Server is
// chosen, the sign-in screen until the user is signed in, then the main tab
// bar: Home · Live · Events · Settings.

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
    ///   --tab live|events|alarms|settings, --route <cameraId>
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
        default: break
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

    var body: some View {
        VStack(spacing: 0) {
            if session.isDemoMode && !store.suppressDemoBanner { demoBanner }

            TabView(selection: $store.selectedTab) {
                HomeView()
                    .tabItem { Label("Home", systemImage: "house.fill") }
                    .tag(AppTab.home)

                LiveGridView()
                    .tabItem { Label("Live", systemImage: "video.fill") }
                    .tag(AppTab.live)

                EventsView()
                    .tabItem { Label("Events", systemImage: "sparkles.rectangle.stack.fill") }
                    .badge(store.newAlertCount)
                    .tag(AppTab.events)

                SettingsView()
                    .tabItem { Label("Settings", systemImage: "gearshape.fill") }
                    .tag(AppTab.settings)
            }
            .tint(SentinelTheme.accent)
        }
        .background(SentinelTheme.background.ignoresSafeArea())
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
