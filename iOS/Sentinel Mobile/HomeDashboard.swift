// HomeDashboard.swift
// Home in regular width (WAVE19): the unfolded iPhone Duo or an iPad gets a
// dashboard in the spirit of the web Admin Overview instead of the phone's
// stacked cards stretched across the screen. Four stat tiles on top, then two
// columns: what needs attention and recent activity (55 %), camera stills and
// the Insights / People links (45 %). Capped at 1100 pt and centred.

import SwiftUI

struct HomeDashboard: View {
    @ObservedObject private var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared

    private let padding = SentinelTheme.Space.xl

    private var needsAttention: [V2.Alarm] { store.sortedAlarms.filter(\.needsAttention) }

    private var showsPeopleAndInsights: Bool {
        session.isDemoMode || session.me?.can(.viewLive) == true
    }

    var body: some View {
        GeometryReader { geo in
            let content = LayoutLogic.dashboardContentWidth(available: geo.size.width, padding: padding)
            let columns = LayoutLogic.dashboardColumns(contentWidth: content, spacing: SentinelTheme.Space.xl)
            ScrollView {
                VStack(alignment: .leading, spacing: SentinelTheme.Space.xl) {
                    HomeHeader(showsSettings: false)
                    statTiles
                    HStack(alignment: .top, spacing: SentinelTheme.Space.xl) {
                        VStack(alignment: .leading, spacing: SentinelTheme.Space.xl) {
                            attention
                            recentActivity
                        }
                        .frame(width: columns.left, alignment: .topLeading)
                        VStack(alignment: .leading, spacing: SentinelTheme.Space.xl) {
                            cameras
                            if showsPeopleAndInsights { links }
                        }
                        .frame(width: columns.right, alignment: .topLeading)
                    }
                }
                .frame(width: content, alignment: .topLeading)
                .padding(.vertical, SentinelTheme.Space.lg)
                .frame(maxWidth: .infinity)
            }
            .refreshable { await store.refreshAll() }
        }
        // Alarm cards open in Events' detail pane rather than a pushed screen.
        .environment(\.selectAlarm) { alarm in
            store.eventsSelection = .alarm(alarm.id)
            store.openAlarms()
        }
    }

    // MARK: - Stat tiles

    private var eventsToday: Int {
        store.events.filter { Calendar.current.isDateInToday($0.createdAtDate) }.count
    }

    private var statTiles: some View {
        let total = store.cameras.count
        let offline = total - store.onlineCount
        let open = store.alarms.count
        let new = store.newAlertCount
        return HStack(spacing: SentinelTheme.Space.md) {
            DashboardStatTile(title: "Cameras online", symbol: "video.fill",
                              value: "\(store.onlineCount)/\(total)",
                              note: offline > 0 ? "\(offline) offline" : (total == 0 ? "None yet" : "All online"),
                              tone: offline > 0 ? SentinelTheme.alarm : SentinelTheme.recording) {
                store.selectedTab = .live
            }
            DashboardStatTile(title: "Recording", symbol: "record.circle",
                              value: "\(store.recordingCount)",
                              note: store.recordingCount == store.onlineCount && total > 0 ? "Every online camera" : "of \(total) cameras",
                              tone: SentinelTheme.recording) {
                store.selectedTab = .live
            }
            DashboardStatTile(title: "Events today", symbol: "sparkles",
                              value: "\(eventsToday)",
                              note: "AI detections",
                              tone: SentinelTheme.accent) {
                store.eventsMode = .detections
                store.selectedTab = .events
            }
            DashboardStatTile(title: "Open alarms", symbol: "bell.badge.fill",
                              value: "\(open)",
                              note: new > 0 ? "\(new) new" : (open > 0 ? "Being handled" : "Nothing open"),
                              tone: new > 0 ? SentinelTheme.alarm : (open > 0 ? SentinelTheme.amber : SentinelTheme.recording)) {
                store.openAlarms()
            }
        }
    }

    // MARK: - Left column

    @ViewBuilder
    private var attention: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SectionHeader(title: "Needs attention", action: { store.openAlarms() }, actionLabel: "All alarms")
            if needsAttention.isEmpty {
                allClear
            } else {
                ForEach(needsAttention.prefix(LayoutLogic.dashboardAlarmLimit)) { alarm in
                    AlarmCard(alarm: alarm, dense: true)
                }
                let more = needsAttention.count - LayoutLogic.dashboardAlarmLimit
                if more > 0 {
                    Button {
                        Haptics.tap(); store.openAlarms()
                    } label: {
                        Text("\(more) more need attention")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(SentinelTheme.accent)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private var allClear: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            ZStack {
                Circle().fill(SentinelTheme.recording.opacity(0.16)).frame(width: 44, height: 44)
                Image(systemName: "checkmark.shield.fill")
                    .font(.title3.weight(.bold))
                    .foregroundStyle(SentinelTheme.recording)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text("All clear").font(.headline).foregroundStyle(.white)
                Text("No alarms need attention.").font(.subheadline).foregroundStyle(.white.opacity(0.6))
            }
            Spacer()
        }
        .padding(SentinelTheme.Space.lg)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
    }

    @ViewBuilder
    private var recentActivity: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SectionHeader(title: "Recent Activity", action: {
                store.eventsMode = .detections; store.selectedTab = .events
            }, actionLabel: "See all")
            if store.events.isEmpty {
                Text("AI detections will appear here.")
                    .font(.subheadline).foregroundStyle(.white.opacity(0.55))
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(SentinelTheme.Space.lg)
                    .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            } else {
                VStack(spacing: 0) {
                    let recent = Array(store.events.prefix(6))
                    ForEach(recent) { event in
                        Button {
                            Haptics.tap()
                            store.eventsMode = .detections
                            store.eventsSelection = .event(event)
                            store.selectedTab = .events
                        } label: {
                            ActivityRow(event: event)
                        }
                        .buttonStyle(.plain)
                        if event.id != recent.last?.id {
                            Rectangle().fill(SentinelTheme.line).frame(height: 1).padding(.leading, 96)
                        }
                    }
                }
                .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
            }
        }
    }

    // MARK: - Right column

    @ViewBuilder
    private var cameras: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SectionHeader(title: "Cameras", action: { store.selectedTab = .live }, actionLabel: "Live view")
            if store.cameras.isEmpty {
                Text(store.hasLoaded ? "No cameras yet." : "Loading cameras…")
                    .font(.subheadline).foregroundStyle(.white.opacity(0.55))
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(SentinelTheme.Space.lg)
                    .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            } else {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: SentinelTheme.Space.md),
                                         count: LayoutLogic.dashboardPreviewColumns),
                          spacing: SentinelTheme.Space.md) {
                    ForEach(store.cameras) { camera in
                        Button {
                            Haptics.tap(); store.focusLive(cameraID: camera.id)
                        } label: {
                            DashboardCameraTile(camera: camera,
                                                still: DashboardCameraTile.latestStill(for: camera.id, in: store.events,
                                                                                       demo: session.isDemoMode))
                        }
                        .buttonStyle(.plain)
                        .accessibilityHint("Opens Live focused on this camera")
                    }
                }
            }
        }
    }

    private var links: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            linkCard(symbol: "chart.bar.xaxis", title: "Insights", subtitle: "Trends and recommendations") {
                store.selectedTab = .insights
            }
            linkCard(symbol: "person.2.fill", title: "People", subtitle: "Who was recognized, and when") {
                store.selectedTab = .people
            }
        }
    }

    private func linkCard(symbol: String, title: String, subtitle: String, action: @escaping () -> Void) -> some View {
        Button {
            Haptics.tap(); action()
        } label: {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack {
                    Image(systemName: symbol)
                        .font(.title3)
                        .foregroundStyle(SentinelTheme.accent)
                    Spacer()
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(.white.opacity(0.4))
                }
                Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                Text(subtitle)
                    .font(.caption).foregroundStyle(.white.opacity(0.55))
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, minHeight: 92, alignment: .topLeading)
            .padding(SentinelTheme.Space.md)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
        }
        .buttonStyle(.plain)
    }
}

/// One stat in the dashboard's top row, after the web Admin Overview's
/// stat cards: what it counts, the number, and a toned note.
struct DashboardStatTile: View {
    let title: String
    let symbol: String
    let value: String
    let note: String
    let tone: Color
    let action: () -> Void

    var body: some View {
        Button {
            Haptics.tap(); action()
        } label: {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack(spacing: 6) {
                    Image(systemName: symbol)
                        .font(.caption.weight(.bold))
                        .foregroundStyle(tone)
                    Text(title.uppercased())
                        .font(.caption2.weight(.bold))
                        .tracking(0.5)
                        .foregroundStyle(.white.opacity(0.55))
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                }
                Text(value)
                    .font(.system(size: 30, weight: .bold, design: .rounded))
                    .foregroundStyle(.white)
                    .lineLimit(1)
                    .minimumScaleFactor(0.6)
                Text(note)
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(tone)
                    .lineLimit(1)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(tone.opacity(0.14), in: Capsule())
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(SentinelTheme.Space.lg)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(title): \(value), \(note)")
        .accessibilityAddTraits(.isButton)
    }
}

/// A detection in the dashboard's Recent Activity list: snapshot, what and
/// where, and when.
struct ActivityRow: View {
    let event: V2.Event
    @ObservedObject private var store = AppStore.shared

    var body: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            EventThumbnail(event: event)
                .frame(width: 76, height: 54)
                .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Image(systemName: SentinelTheme.kindSymbol(event.kind))
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(SentinelTheme.kindColor(event.kind))
                    Text(event.kind)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.white)
                        .lineLimit(1)
                    Spacer(minLength: 6)
                    Text(event.createdAtDate, format: .relative(presentation: .named))
                        .font(.caption2.weight(.medium))
                        .foregroundStyle(.white.opacity(0.5))
                        .lineLimit(1)
                        .fixedSize()
                }
                Text(store.cameraName(forID: event.cameraId) ?? "Camera")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.white.opacity(0.75))
                    .lineLimit(1)
                if let d = event.description, d.isEmpty == false {
                    Text(d)
                        .font(.caption)
                        .foregroundStyle(.white.opacity(0.55))
                        .lineLimit(1)
                }
            }
        }
        .padding(.horizontal, SentinelTheme.Space.md)
        .padding(.vertical, 10)
        .contentShape(Rectangle())
    }
}

/// A camera on the dashboard: its most recent event snapshot, not live video
/// (wave19-fixes). A preview grid of players decoded every stream at once and
/// showed the system player's error for streams the device can't decode
/// (H.265 at 3328×1872 in the Simulator). The still follows `store.events`,
/// so a new detection replaces it; a camera with no snapshot yet shows its
/// symbol on the dark placeholder. Tapping opens Live focused on it.
struct DashboardCameraTile: View {
    let camera: V2.Camera
    let still: V2.Event?

    /// The newest event on this camera that has a saved frame. Demo Mode has
    /// no server to fetch from, so it never has one.
    static func latestStill(for cameraID: String, in events: [V2.Event], demo: Bool) -> V2.Event? {
        guard demo == false else { return nil }
        return events
            .filter { $0.cameraId == cameraID && $0.hasThumbnail }
            .max { $0.createdAt < $1.createdAt }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Color.clear
                .aspectRatio(16/9, contentMode: .fit)
                .overlay { preview }
                .clipped()
                .overlay(alignment: .top) {
                    HStack {
                        if camera.recording && camera.isOnline { recordingPill }
                        Spacer()
                        SentinelStatusBadge(status: camera.statusLabel)
                    }
                    .padding(8)
                }

            VStack(alignment: .leading, spacing: 2) {
                Text(camera.name)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.white)
                    .lineLimit(1)
                HStack(spacing: 6) {
                    Image(systemName: "mappin.circle.fill")
                        .font(.caption2)
                    Text(camera.location.isEmpty ? camera.resolutionLabel : camera.location)
                        .font(.caption2)
                        .lineLimit(1)
                }
                .foregroundStyle(.white.opacity(0.5))
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.panel)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(SentinelTheme.line, lineWidth: 1)
        )
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder
    private var preview: some View {
        if let still {
            AuthImage(eventID: still.id, failureSymbol: "video.fill")
        } else {
            ZStack {
                LinearGradient(colors: [Color(white: 0.12), Color(white: 0.04)],
                               startPoint: .topLeading, endPoint: .bottomTrailing)
                Image(systemName: camera.isOnline ? "video.fill" : "video.slash.fill")
                    .font(.title2)
                    .foregroundStyle(.white.opacity(0.3))
            }
        }
    }

    private var recordingPill: some View {
        HStack(spacing: 4) {
            Circle()
                .fill(SentinelTheme.alarm)
                .frame(width: 6, height: 6)
            Text("REC")
                .font(.caption2.weight(.heavy))
                .foregroundStyle(.white)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(.black.opacity(0.55), in: Capsule())
    }
}
