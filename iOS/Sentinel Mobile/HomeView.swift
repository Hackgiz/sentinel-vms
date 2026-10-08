// HomeView.swift
// Glanceable dashboard: overall status, quick stats, recent AI events, and
// fast access to cameras.

import SwiftUI

struct HomeView: View {
    @ObservedObject var store = AppStore.shared
    @ObservedObject var session = SentinelSession.shared

    var body: some View {
        NavigationStack {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()
                ScrollView {
                    VStack(spacing: SentinelTheme.Space.lg) {
                        homeHeader
                        statusHero
                        if let top = store.sortedAlarms.first(where: \.needsAttention) {
                            AlarmCard(alert: top, compact: true)
                        }
                        statsRow
                        recentActivity
                        camerasSection
                    }
                    .padding(SentinelTheme.Space.lg)
                }
                .refreshable { await store.refreshAll() }
            }
            // Hide the (empty) large-title bar that left a big dead gap at the
            // top; we show a compact custom header pinned under the status bar
            // instead. Pushed screens (CameraDetailView) keep their own bar.
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: SentinelCameraSummary.self) { CameraDetailView(camera: $0) }
        }
    }

    // MARK: - Header

    private var homeHeader: some View {
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
                }
            }
            Spacer(minLength: 8)
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
        .padding(.top, 4)
    }

    private var displayServerName: String {
        if session.isDemoMode { return "Demo" }
        if let name = session.serverName, name.isEmpty == false { return name }
        return "Sentinel"
    }

    private var connectionLabel: String {
        if session.isDemoMode { return "Demo mode · sample data" }
        return session.isUsingRemote ? "Remote · Cloudflare" : "Local network"
    }

    private var connectionColor: Color {
        if session.isDemoMode { return SentinelTheme.recording }
        return session.isUsingRemote ? SentinelTheme.amber : SentinelTheme.recording
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
                                EventThumbCard(event: event)
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
                LazyVGrid(columns: [GridItem(.flexible(), spacing: SentinelTheme.Space.md),
                                    GridItem(.flexible(), spacing: SentinelTheme.Space.md)],
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

/// Compact event card used in the Home "Recent Activity" strip.
struct EventThumbCard: View {
    let event: SentinelEvent

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ZStack(alignment: .topLeading) {
                EventThumbnail(event: event)
                    .frame(width: 150, height: 90)
                    .clipShape(UnevenRoundedRectangle(topLeadingRadius: SentinelTheme.Radius.sm, topTrailingRadius: SentinelTheme.Radius.sm, style: .continuous))
                Chip(text: event.kind.uppercased(), systemImage: SentinelTheme.kindSymbol(event.kind),
                     color: SentinelTheme.kindColor(event.kind), filled: true)
                    .padding(6)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(event.cameraName).font(.caption.weight(.semibold)).foregroundStyle(.white).lineLimit(1)
                Text(event.createdAtDate, format: .relative(presentation: .named))
                    .font(.caption2).foregroundStyle(.white.opacity(0.5))
            }
            .padding(SentinelTheme.Space.sm)
            .frame(width: 150, alignment: .leading)
            .background(SentinelTheme.chrome)
        }
        .frame(width: 150)
        .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
    }
}
