// HomeView.swift
// Glanceable dashboard: overall status, quick stats, recent AI events, and
// fast access to cameras. Compact width is the phone page; regular width
// shows HomeDashboard (WAVE19).

import SwiftUI

struct HomeView: View {
    @ObservedObject var store = AppStore.shared
    @ObservedObject var session = SentinelSession.shared
    @Environment(\.horizontalSizeClass) private var sizeClass

    private var width: LayoutWidth { LayoutWidth(sizeClass) }

    var body: some View {
        NavigationStack {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()
                if width == .regular {
                    // WAVE19: a dashboard, not the phone page stretched wide.
                    HomeDashboard()
                } else {
                    ScrollView {
                        VStack(spacing: SentinelTheme.Space.lg) {
                            HomeHeader(showsSettings: true)
                            statusHero
                            if let top = store.sortedAlarms.first(where: \.needsAttention) {
                                AlarmCard(alarm: top, compact: true)
                            }
                            statsRow
                            insightsLink
                            recentActivity
                            camerasSection
                        }
                        .padding(SentinelTheme.Space.lg)
                    }
                    .refreshable { await store.refreshAll() }
                }
            }
            // Hide the (empty) large-title bar that left a big dead gap at the
            // top; we show a compact custom header pinned under the status bar
            // instead. Pushed screens (CameraDetailView) keep their own bar.
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: V2.Camera.self) { CameraDetailView(camera: $0) }
            .navigationDestination(for: V2.Alarm.self) { AlarmDetailView(alarmID: $0.id) }
            // People and Insights, pushed in compact width. The push lives in
            // the store so folding the Duo with one open keeps it open
            // (unfolded, it becomes the sidebar's selection).
            .navigationDestination(isPresented: Binding(get: { store.homePush != nil },
                                                        set: { if $0 == false { store.homePush = nil } })) {
                switch store.homePush {
                case .people: PeopleView()
                case .insights: InsightsView()
                default: EmptyView()
                }
            }
        }
    }

    // MARK: - Status hero

    private var statusHero: some View {
        let alerts = store.newAlertCount
        let allClear = alerts == 0
        let color = allClear ? SentinelTheme.recording : SentinelTheme.alarm
        return HStack(spacing: SentinelTheme.Space.md) {
            ZStack {
                Circle().fill(color.opacity(0.16)).frame(width: 54, height: 54)
                Image(systemName: allClear ? "checkmark.shield.fill" : "exclamationmark.shield.fill")
                    .font(.title2.weight(.bold))
                    .foregroundStyle(color)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(allClear ? "All clear" : "\(alerts) new alarm\(alerts == 1 ? "" : "s")")
                    .font(.title3.weight(.bold))
                    .foregroundStyle(.white)
                Text(allClear ? "No alarms need attention" : "Tap to review alarms")
                    .font(.subheadline)
                    .foregroundStyle(.white.opacity(0.6))
            }
            Spacer()
        }
        .padding(SentinelTheme.Space.lg)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(color.opacity(0.3), lineWidth: 1))
        .contentShape(Rectangle())
        .onTapGesture { if !allClear { Haptics.tap(); store.openAlarms() } }
    }

    // MARK: - Stats

    private var statsRow: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            stat(value: "\(store.onlineCount)/\(store.cameras.count)", label: "Online", color: SentinelTheme.recording, icon: "video.fill")
            stat(value: "\(store.recordingCount)", label: "Recording", color: SentinelTheme.alarm, icon: "record.circle")
            stat(value: "\(store.events.count)", label: "Events", color: SentinelTheme.accent, icon: "sparkles")
        }
    }

    private func stat(value: String, label: String, color: Color, icon: String) -> some View {
        VStack(spacing: 4) {
            Image(systemName: icon).font(.callout).foregroundStyle(color)
            Text(value).font(.title3.weight(.bold)).foregroundStyle(.white)
            Text(label).font(.caption2).foregroundStyle(.white.opacity(0.55))
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, SentinelTheme.Space.md)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
    }

    // MARK: - Insights

    /// Shown once /api/v2/me has loaded: `session.can` says yes while it's unknown.
    /// People (WAVE17) sits next to it under the same gate.
    @ViewBuilder
    private var insightsLink: some View {
        if session.me?.can(.viewLive) == true {
            Button { Haptics.tap(); store.homePush = .insights } label: {
                homeLinkLabel(symbol: "chart.bar.xaxis", title: "Insights", subtitle: "Trends and recommendations")
            }
            .buttonStyle(.plain)
            Button { Haptics.tap(); store.homePush = .people } label: {
                homeLinkLabel(symbol: "person.2.fill", title: "People", subtitle: "Recognized people and when they were seen")
            }
            .buttonStyle(.plain)
        }
    }

    private func homeLinkLabel(symbol: String, title: String, subtitle: String) -> some View {
        HStack(spacing: SentinelTheme.Space.md) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(SentinelTheme.accent)
                .frame(width: 36)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                Text(subtitle).font(.caption).foregroundStyle(.white.opacity(0.55))
            }
            Spacer()
            Image(systemName: "chevron.right").font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.4))
        }
        .padding(SentinelTheme.Space.md)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
    }

    // MARK: - Recent activity

    @ViewBuilder
    private var recentActivity: some View {
        if store.events.isEmpty == false {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                SectionHeader(title: "Recent Activity", action: { store.eventsMode = .detections; store.selectedTab = .events }, actionLabel: "See all")
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: SentinelTheme.Space.md) {
                        ForEach(store.events.prefix(8)) { event in
                            Button {
                                Haptics.tap(); store.eventsMode = .detections; store.selectedTab = .events
                            } label: {
                                EventThumbCard(event: event, size: LayoutLogic.stripCardSize(for: width))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
        }
    }

    // MARK: - Cameras

    @ViewBuilder
    private var camerasSection: some View {
        if store.cameras.isEmpty == false {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                SectionHeader(title: "Cameras", action: { store.selectedTab = .live }, actionLabel: "Live view")
                // Adaptive: two across on a phone, three or more unfolded,
                // instead of two tiles stretched across a 7.6-inch screen.
                LazyVGrid(columns: [GridItem(.adaptive(minimum: LayoutLogic.tileMinimum(for: width)),
                                             spacing: SentinelTheme.Space.md)],
                          spacing: SentinelTheme.Space.md) {
                    ForEach(store.cameras) { camera in
                        NavigationLink(value: camera) { CameraTile(camera: camera) }
                            .buttonStyle(.plain)
                    }
                }
            }
        }
    }
}

/// Home's title: the site, how it's reached, and (compact) a Settings button.
struct HomeHeader: View {
    var showsSettings: Bool
    @ObservedObject private var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared

    var body: some View {
        HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
            VStack(alignment: .leading, spacing: 3) {
                Text(displayServerName)
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                    .lineLimit(1)
                HStack(spacing: 6) {
                    Circle().fill(connectionColor).frame(width: 7, height: 7)
                    Text(connectionLabel)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.white.opacity(0.6))
                    if session.isViaSentinelCloud { CloudPill() } // WAVE12(D): relay host *-cloud.sentvms.com on the secondary address
                }
            }
            Spacer(minLength: 8)
            if showsSettings {
                Button {
                    Haptics.tap(); store.selectedTab = .settings
                } label: {
                    Image(systemName: "gearshape.fill")
                        .font(.title3)
                        .foregroundStyle(.white.opacity(0.75))
                        .frame(width: 40, height: 40)
                        .background(SentinelTheme.panel, in: Circle())
                        .overlay(Circle().stroke(SentinelTheme.line, lineWidth: 1))
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.top, 4)
    }

    private var displayServerName: String {
        if let name = session.serverName, name.isEmpty == false { return name }
        return "Sentinel"
    }

    private var connectionLabel: String {
        if session.isDemoMode { return "Demo mode · sample data" }
        let route = session.isUsingRemote ? "Remote address" : "Local network"
        return store.streamState.isConnected ? "\(route) · live" : route
    }

    private var connectionColor: Color {
        if session.isDemoMode { return SentinelTheme.recording }
        return session.isUsingRemote ? SentinelTheme.amber : SentinelTheme.recording
    }
}

/// Compact event card used in the Home "Recent Activity" strip.
struct EventThumbCard: View {
    let event: V2.Event
    /// Sized like a Home camera tile for the current width.
    var size = LayoutLogic.stripCardSize(for: .compact)
    @ObservedObject private var store = AppStore.shared

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ZStack(alignment: .topLeading) {
                EventThumbnail(event: event)
                    .frame(width: size.width, height: size.height)
                    .clipShape(UnevenRoundedRectangle(topLeadingRadius: SentinelTheme.Radius.sm, topTrailingRadius: SentinelTheme.Radius.sm, style: .continuous))
                Chip(text: event.kind.uppercased(), systemImage: SentinelTheme.kindSymbol(event.kind),
                     color: SentinelTheme.kindColor(event.kind), filled: true)
                    .padding(6)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(store.cameraName(forID: event.cameraId) ?? "Camera").font(.caption.weight(.semibold)).foregroundStyle(.white).lineLimit(1)
                Text(event.createdAtDate, format: .relative(presentation: .named))
                    .font(.caption2).foregroundStyle(.white.opacity(0.5))
            }
            .padding(SentinelTheme.Space.sm)
            .frame(width: size.width, alignment: .leading)
            .background(SentinelTheme.chrome)
        }
        .frame(width: size.width)
        .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
    }
}
