import AppKit
import SwiftUI
import SentinelCore

/// Home (wave 20), the workspace after sign-in: the unfolded iPhone's
/// dashboard on the Mac. Page header, four stat tiles, then 55/45 columns:
/// what needs attention (acknowledged in place) and recent activity; camera
/// stills and the Insights / People cards. Capped at 1100 pt and centred.
/// The numbers come from `HomeModel` over what the Operator already holds.
struct HomeWorkspaceView<Controls: View>: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var feed: EventFeed

    let context: WorkspacePaneContext
    let onEvents: () -> Void
    @ViewBuilder let controls: () -> Controls

    @State private var busy: Set<String> = []
    @State private var error: String?

    private let padding = SentinelTheme.Space.xl
    private let gap = SentinelTheme.Space.xl

    private var connected: Bool {
        if case .connected = runtime.streamState { return true }
        return false
    }

    var body: some View {
        GeometryReader { geo in
            let content = CGFloat(HomeModel.contentWidth(available: Double(geo.size.width), padding: Double(padding)))
            let columns = HomeModel.columns(contentWidth: Double(content), spacing: Double(gap))
            ScrollView {
                VStack(alignment: .leading, spacing: gap) {
                    PageHeader(title: session.site?.name ?? "Home",
                               subtitle: HomeModel.subtitle(host: session.site?.url?.host, streamLabel: runtime.streamSummary.label),
                               dot: connected ? SentinelTheme.recording : SentinelTheme.amber) {
                        controls()
                    }
                    statTiles
                    if content >= 760 {
                        HStack(alignment: .top, spacing: gap) {
                            leftColumn.frame(width: CGFloat(columns.left), alignment: .topLeading)
                            rightColumn.frame(width: CGFloat(columns.right), alignment: .topLeading)
                        }
                    } else {
                        leftColumn
                        rightColumn
                    }
                }
                .frame(width: content, alignment: .topLeading)
                .padding(.vertical, SentinelTheme.Space.lg)
                .frame(maxWidth: .infinity)
            }
        }
        .background(SentinelTheme.background)
    }

    // MARK: Stat tiles

    private var statTiles: some View {
        let stats = HomeModel.stats(cameras: cameras.cameras, openAlarms: alarms.open, events: feed.events, now: Date())
        return HStack(spacing: SentinelTheme.Space.md) {
            ForEach(stats) { stat in
                StatTile(stat: stat) {
                    switch stat.kind {
                    case .cameras, .recording: context.openTab(.live)
                    case .events: onEvents()
                    case .alarms: context.openTab(.alarms)
                    }
                }
            }
        }
    }

    // MARK: Left column

    private var leftColumn: some View {
        VStack(alignment: .leading, spacing: gap) {
            attention
            recentActivity
        }
    }

    private var attention: some View {
        let (shown, more) = HomeModel.attention(alarms.open)
        let mayAct = session.can(.acknowledgeAlarms)
        return VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SectionTitle(title: "Needs attention", actionLabel: "All alarms") { context.openTab(.alarms) }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.alarm)
            }
            if shown.isEmpty {
                allClear
            } else {
                ForEach(shown) { alarm in
                    AlarmCard(alarm: alarm, cameraName: cameras.camera(alarm.cameraId)?.name, alwaysShowActions: true) {
                        if HomeModel.canAcknowledge(alarm, mayAct: mayAct) {
                            Button { acknowledge(alarm) } label: {
                                Label(busy.contains(alarm.id) ? "Acknowledging…" : "Acknowledge", systemImage: "checkmark.circle.fill")
                            }
                            .buttonStyle(CardPrimaryButtonStyle())
                            .disabled(busy.contains(alarm.id))
                        } else {
                            Spacer()
                        }
                        Button { context.openAlarm(alarm.id) } label: { Image(systemName: "rectangle.and.text.magnifyingglass") }
                            .buttonStyle(CardIconButtonStyle())
                            .help("Open in Alarm Manager")
                        if let id = alarm.cameraId, cameras.camera(id) != nil {
                            Button { context.openCamera(id) } label: { Image(systemName: "video.fill") }
                                .buttonStyle(CardIconButtonStyle())
                                .help("Show this camera on Live")
                        }
                    }
                    .onTapGesture { context.openAlarm(alarm.id) }
                }
                if more > 0 {
                    Button { context.openTab(.alarms) } label: {
                        Text("\(more) more need attention")
                            .font(.callout.weight(.semibold))
                            .foregroundStyle(SentinelTheme.accent)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .operatorCard(radius: SentinelTheme.Radius.sm)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private var allClear: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            Circle().fill(SentinelTheme.recording.opacity(0.16)).frame(width: 44, height: 44)
                .overlay(Image(systemName: "checkmark.shield.fill").font(.title3.weight(.bold)).foregroundStyle(SentinelTheme.recording))
            VStack(alignment: .leading, spacing: 2) {
                Text("All clear").font(.headline)
                Text("No alarms need attention.").font(.callout).foregroundStyle(.secondary)
            }
            Spacer()
        }
        .padding(SentinelTheme.Space.lg)
        .operatorCard()
    }

    private var recentActivity: some View {
        let recent = HomeModel.recentActivity(feed.events)
        return VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SectionTitle(title: "Recent Activity", actionLabel: "See all", action: onEvents)
            if recent.isEmpty {
                Text(feed.isLoading ? "Loading…" : "AI detections will appear here.")
                    .font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(SentinelTheme.Space.lg)
                    .operatorCard()
            } else {
                VStack(spacing: 0) {
                    ForEach(recent) { event in
                        Button {
                            context.openPlayback([event.cameraId], Date(unixMillis: event.createdAt).addingTimeInterval(-SearchFilter.playbackLead))
                        } label: {
                            ActivityRow(event: event, cameraName: cameras.name(of: event.cameraId))
                        }
                        .buttonStyle(.plain)
                        .help("Play the recording from just before this event")
                        if event.id != recent.last?.id {
                            Rectangle().fill(SentinelTheme.line).frame(height: 1).padding(.leading, 100)
                        }
                    }
                }
                .operatorCard()
            }
        }
    }

    // MARK: Right column

    private var rightColumn: some View {
        VStack(alignment: .leading, spacing: gap) {
            cameraGrid
            links
        }
    }

    private var cameraGrid: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SectionTitle(title: "Cameras", actionLabel: "Live view") { context.openTab(.live) }
            if cameras.cameras.isEmpty {
                Text(cameras.isLoaded ? "No cameras yet." : "Loading cameras…")
                    .font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(SentinelTheme.Space.lg)
                    .operatorCard()
            } else {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: SentinelTheme.Space.md), count: HomeModel.cameraColumns),
                          spacing: SentinelTheme.Space.md) {
                    ForEach(cameras.cameras) { camera in
                        Button { context.openCamera(camera.id) } label: {
                            CameraStillTile(camera: camera, still: HomeModel.latestStill(for: camera.id, in: feed.events))
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .help("Open \(camera.name) on Live")
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var links: some View {
        let nav = ManagementNavModel(can: session.can)
        let insights = nav.node(ManagementCatalog.Leaf.insights) != nil
        let people = nav.node(ManagementCatalog.Leaf.people) != nil
        if insights || people {
            HStack(spacing: SentinelTheme.Space.md) {
                if insights {
                    linkCard(symbol: "chart.bar.xaxis", title: "Insights", subtitle: "Trends and recommendations",
                             node: ManagementCatalog.Leaf.insights)
                }
                if people {
                    linkCard(symbol: "person.2.fill", title: "People", subtitle: "Who was recognized, and when",
                             node: ManagementCatalog.Leaf.people)
                }
            }
        }
    }

    private func linkCard(symbol: String, title: String, subtitle: String, node: String) -> some View {
        Button { context.openManagement(node) } label: {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack {
                    Image(systemName: symbol).font(.title3).foregroundStyle(SentinelTheme.accent)
                    Spacer()
                    Image(systemName: "chevron.right").font(.caption.weight(.bold)).foregroundStyle(.tertiary)
                }
                Text(title).font(.callout.weight(.semibold))
                Text(subtitle).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, minHeight: 92, alignment: .topLeading)
            .padding(SentinelTheme.Space.md)
            .operatorCard()
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help("Open \(title) in the Management window")
    }

    // MARK: Actions

    private func acknowledge(_ alarm: V2.Alarm) {
        busy.insert(alarm.id)
        error = nil
        Task {
            defer { busy.remove(alarm.id) }
            do {
                try await alarms.setState(alarm, to: .acknowledged, note: nil, api: runtime.api)
            } catch {
                self.error = "Couldn't acknowledge “\(alarm.title)”: \(error.localizedDescription)"
            }
        }
    }
}

/// A detection in Home's Recent Activity: snapshot, what and where, when.
private struct ActivityRow: View {
    let event: V2.Event
    let cameraName: String

    var body: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            EventThumb(event: event)
                .frame(width: 76, height: 48)
                .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Image(systemName: HomeModel.kindSymbol(event.kind))
                        .font(.caption.weight(.bold))
                        .foregroundStyle(SentinelTheme.kindColor(event.kind))
                    Text(event.label?.isEmpty == false ? event.label!.capitalized : event.kind)
                        .font(.callout.weight(.semibold))
                        .lineLimit(1)
                    Spacer(minLength: 6)
                    TimelineView(.periodic(from: .now, by: 30)) { context in
                        Text(HomeModel.relative(event.createdAt, now: context.date))
                            .font(.caption.weight(.medium))
                            .foregroundStyle(.secondary)
                    }
                }
                Text(cameraName).font(.caption.weight(.semibold)).foregroundStyle(.primary.opacity(0.75)).lineLimit(1)
                if let d = event.description, d.isEmpty == false {
                    Text(d).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        }
        .padding(.horizontal, SentinelTheme.Space.md)
        .padding(.vertical, 10)
        .contentShape(Rectangle())
    }
}
