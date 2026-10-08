// RootView.swift
// Top-level navigation. Shows pairing scanner until paired, then the main
// tab bar: Home · Live · Events · Settings.

import SwiftUI

struct RootView: View {
    @ObservedObject var session = SentinelSession.shared

    var body: some View {
        Group {
            if session.isPaired {
                MainTabView()
            } else {
                PairingScannerView()
            }
        }
        // Dev-only: `--demo` launch arg jumps straight into Demo Mode (and
        // optional `--tab live|events|settings` selects a tab) so the redesigned
        // screens can be screenshotted without a paired Mac. Compiled out of
        // Release builds.
        .onAppear {
            #if DEBUG
            let args = CommandLine.arguments
            // `--pair-url <url> --pair-code <code>` pairs like "Enter Server Info
            // Manually", for testing against a server (e.g. Linux) in the Simulator.
            if let u = args.firstIndex(of: "--pair-url"), u + 1 < args.count,
               let c = args.firstIndex(of: "--pair-code"), c + 1 < args.count, !session.isPaired {
                Task { try? await session.pairManually(serverURL: args[u + 1], code: args[c + 1], deviceName: "Simulator") }
            }
            if args.contains("--demo"), !session.isPaired {
                session.enterDemoMode()
                AppStore.shared.suppressDemoBanner = true   // clean chrome for screenshots
            }
            if let i = args.firstIndex(of: "--tab"), i + 1 < args.count {
                switch args[i + 1] {
                case "live": AppStore.shared.selectedTab = .live
                case "events": AppStore.shared.selectedTab = .events
                case "alarms": AppStore.shared.openAlarms()
                case "settings": AppStore.shared.selectedTab = .settings
                default: AppStore.shared.selectedTab = .home
                }
            }
            if let i = args.firstIndex(of: "--route"), i + 1 < args.count,
               let id = UUID(uuidString: args[i + 1]) {
                AppStore.shared.route(toCameraID: id)
            }
            #endif
        }
    }
}

struct MainTabView: View {
    @ObservedObject var session = SentinelSession.shared
    @ObservedObject var store = AppStore.shared

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
        .onAppear { store.startAuto() }
        .onDisappear { store.stopAuto() }
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
            Button("Exit") { session.unpair() }
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
