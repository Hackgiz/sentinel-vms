import AppKit
import SwiftUI
import SentinelCore

/// Pages of Sentinel Operator. The workspace (sidebar, camera grid and, in
/// Playback, the timeline bar) is home; Events, Alarms and the Administration
/// pages open in its place and a button returns to it.
enum OperatorSection: String, CaseIterable, Identifiable, Hashable {
    case workspace = "Workspace"
    case events = "Events"
    case alarms = "Alarm Queue"
    case cameras = "Cameras"
    case rules = "Alarm Rules"
    case users = "Users"
    case servers = "Recording Servers"
    case audit = "Audit Log"
    case thisMac = "Server on this Mac"
    case importer = "Import from Sentinel 1.x"
    case settings = "Settings"

    var id: String { rawValue }

    var symbol: String {
        switch self {
        case .workspace: return "rectangle.grid.2x2.fill"
        case .events: return "bolt.horizontal.fill"
        case .alarms: return "exclamationmark.octagon.fill"
        case .cameras: return "camera.fill"
        case .rules: return "checklist"
        case .users: return "person.2.fill"
        case .servers: return "server.rack"
        case .audit: return "list.bullet.rectangle.portrait.fill"
        case .thisMac: return "desktopcomputer"
        case .importer: return "square.and.arrow.down.on.square.fill"
        case .settings: return "gearshape.fill"
        }
    }

    var subtitle: String {
        switch self {
        case .workspace: return "Live and recorded video, synchronized across cameras"
        case .events: return "Detections and analytics"
        case .alarms: return "Respond, investigate and lock evidence"
        case .cameras: return "Cameras on this site"
        case .rules: return "What raises an alarm and what to do"
        case .users: return "Who can sign in"
        case .servers: return "Recording servers and their health"
        case .audit: return "Who did what"
        case .thisMac: return "Host a Sentinel Server on this Mac"
        case .importer: return "Bring cameras, users and evidence from the old app"
        case .settings: return "Appearance and sites"
        }
    }

    /// The Administration menu.
    static let admin: [OperatorSection] = [.cameras, .rules, .users, .servers, .audit, .thisMac, .importer, .settings]
}

/// Cross-view navigation requests (e.g. "Play from this alarm").
@MainActor
final class OperatorNavigator: ObservableObject {
    struct PlaybackRequest: Equatable {
        var cameraIDs: [String]
        var at: Date
    }

    @Published var section: OperatorSection = .workspace
    /// Live or Playback; flips the whole workspace.
    @Published var mode: WorkspaceMode = .live
    @Published var playbackRequest: PlaybackRequest?
    @Published var selectedAlarmID: String?

    func openPlayback(cameraIDs: [String], at: Date) {
        playbackRequest = PlaybackRequest(cameraIDs: cameraIDs, at: at)
        section = .workspace
        mode = .playback
    }

    func openAlarm(_ id: String) {
        selectedAlarmID = id
        section = .alarms
    }

    func show(_ mode: WorkspaceMode) {
        section = .workspace
        self.mode = mode
    }
}

/// The signed-in window: sidebar (Views and Cameras), toolbar with the
/// Live / Playback switch, the camera grid, and the timeline bar in Playback.
struct OperatorShell: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator
    @StateObject private var workspace = WorkspaceModel()
    @StateObject private var controller = PlaybackController()
    @State private var isExporting = false
    @State private var hostWindow: NSWindow?
    @State private var keyMonitor: Any?
    @State private var hasOpenedPlayback = false

    var body: some View {
        HStack(spacing: 0) {
            if workspace.sidebarVisible {
                VStack(spacing: 0) {
                    Color.clear.frame(height: 34)
                    WorkspaceSidebar(workspace: workspace)
                    sidebarFooter
                }
                .frame(width: workspace.sidebarWidth)
                .background(SentinelTheme.chrome)
                SidebarResizer(width: $workspace.sidebarWidth)
            }
            VStack(spacing: 0) {
                WorkspaceToolbar(workspace: workspace, section: navigator.section)
                Divider().overlay(SentinelTheme.line)
                if let attention = alarms.attention {
                    CriticalAlarmBanner(alarm: attention)
                }
                content
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                if navigator.section == .workspace, navigator.mode == .playback {
                    PlaybackTimelineBar(controller: controller, selectedCameraID: workspace.selectedCameraID,
                                        onLive: { navigator.show(.live) }, onExport: beginExport)
                }
            }
            .background(SentinelTheme.background)
        }
        .background(WindowReader(window: $hostWindow))
        .sheet(isPresented: $isExporting) {
            PlaybackExportSheet(controller: controller)
        }
        .onAppear {
            if let site = session.site { workspace.bind(siteID: site.id) }
            controller.attach(api: runtime.api, video: runtime.video)
            workspace.seedIfNeeded(with: cameras.cameras.map(\.id))
            installKeys()
            consumeRequest()
            applyMode()
        }
        .onDisappear {
            removeKeys()
            controller.stop()
        }
        .onChange(of: cameras.cameras.map(\.id)) { ids in
            workspace.seedIfNeeded(with: ids)
            if cameras.isLoaded, ids.isEmpty == false { workspace.prune(available: Set(ids)) }
        }
        .onChange(of: navigator.mode) { _ in applyMode() }
        .onChange(of: navigator.section) { section in
            if section != .workspace { controller.pause() }
        }
        .onChange(of: workspace.cameraIDs) { _ in applyMode() }
        .onChange(of: navigator.playbackRequest) { _ in consumeRequest() }
    }

    @ViewBuilder
    private var content: some View {
        switch navigator.section {
        case .workspace:
            WorkspaceGridView(workspace: workspace, controller: controller, mode: navigator.mode)
        case .events: page(.events) { EventsView() }
        case .alarms: page(.alarms) { AlarmQueueView() }
        case .cameras: page(.cameras) { CamerasAdminView() }
        case .rules: page(.rules) { RulesView() }
        case .users: page(.users) { UsersAdminView() }
        case .servers: page(.servers) { RecordingServersView() }
        case .audit: page(.audit) { AuditLogV2View() }
        case .thisMac: page(.thisMac) { LocalServerView() }
        case .importer: page(.importer) { MacImportView() }
        case .settings: page(.settings) { OperatorSettingsView() }
        }
    }

    /// A page that takes the grid's place, with a way back.
    private func page<Content: View>(_ section: OperatorSection, @ViewBuilder _ body: () -> Content) -> some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Button { navigator.section = .workspace } label: { Label("Workspace", systemImage: "chevron.left") }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .help("Back to the camera grid")
                VStack(alignment: .leading, spacing: 1) {
                    Text(section.rawValue).font(.headline)
                    Text(section.subtitle).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            Divider().overlay(SentinelTheme.line)
            body()
        }
    }

    private var sidebarFooter: some View {
        VStack(spacing: 0) {
            Divider().overlay(SentinelTheme.line)
            HStack(spacing: 8) {
                Button { navigator.section = .events } label: { Label("Events", systemImage: OperatorSection.events.symbol) }
                Button { navigator.openAlarm(navigator.selectedAlarmID ?? alarms.open.first?.id ?? "") } label: {
                    Label(alarms.open.isEmpty ? "Alarms" : "Alarms \(alarms.open.count)", systemImage: OperatorSection.alarms.symbol)
                }
                Spacer()
                Menu {
                    AdministrationMenuItems()
                } label: {
                    Image(systemName: "gearshape")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .help("Administration")
            }
            .controlSize(.small)
            .padding(.horizontal, 12)
            .padding(.top, 8)
            SiteMiniStatus()
        }
    }

    // MARK: Mode

    /// Playback follows the cameras on the grid; Live hands the players back.
    private func applyMode() {
        if navigator.mode == .playback, navigator.section == .workspace {
            if hasOpenedPlayback == false {
                hasOpenedPlayback = true
                if navigator.playbackRequest == nil { controller.go(to: Date().addingTimeInterval(-60)) }
            }
            controller.setCameras(workspace.cameraIDs)
        } else if navigator.mode == .live {
            controller.pause()
            controller.setCameras([])
        }
    }

    private func consumeRequest() {
        guard let request = navigator.playbackRequest else { return }
        navigator.playbackRequest = nil
        hasOpenedPlayback = true
        request.cameraIDs.forEach { workspace.reveal($0) }
        workspace.selectedCameraID = request.cameraIDs.first
        controller.setCameras(workspace.cameraIDs)
        controller.visibleSpan = 3_600_000
        controller.go(to: request.at)
        controller.play()
    }

    private func beginExport() {
        controller.pause()
        isExporting = true
    }

    // MARK: Keyboard

    private func installKeys() {
        removeKeys()
        keyMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
            handleKey(event) ? nil : event
        }
    }

    private func removeKeys() {
        if let keyMonitor { NSEvent.removeMonitor(keyMonitor) }
        keyMonitor = nil
    }

    /// space play/pause, arrows step 1 s (shift: 10 s), [ ] mark in/out, L back to Live.
    private func handleKey(_ event: NSEvent) -> Bool {
        guard let window = hostWindow, event.window === window, window.attachedSheet == nil,
              navigator.section == .workspace, navigator.mode == .playback,
              event.modifierFlags.intersection([.command, .option, .control]).isEmpty,
              window.firstResponder is NSText == false, window.firstResponder is NSDatePicker == false else { return false }
        let shift = event.modifierFlags.contains(.shift)
        switch event.keyCode {
        case 49: controller.togglePlay()
        case 123: controller.step(seconds: shift ? -10 : -1)
        case 124: controller.step(seconds: shift ? 10 : 1)
        default:
            switch event.charactersIgnoringModifiers {
            case "[": controller.selection.setIn(controller.position)
            case "]": controller.selection.setOut(controller.position)
            case "l", "L": navigator.show(.live)
            default: return false
            }
        }
        return true
    }
}

/// The Administration entries, as menu items (they set the navigator's section).
struct AdministrationMenuItems: View {
    @EnvironmentObject private var navigator: OperatorNavigator

    var body: some View {
        ForEach(OperatorSection.admin) { section in
            Button { navigator.section = section } label: { Label(section.rawValue, systemImage: section.symbol) }
        }
    }
}

/// The drag handle between the sidebar and the workspace.
private struct SidebarResizer: View {
    @Binding var width: CGFloat
    @State private var startWidth: CGFloat?

    var body: some View {
        Rectangle().fill(SentinelTheme.line).frame(width: 1)
            .overlay {
                Color.clear
                    .frame(width: 9)
                    .contentShape(Rectangle())
                    .overlay(CursorRegion(cursor: .resizeLeftRight))
                    .gesture(DragGesture(minimumDistance: 1, coordinateSpace: .global)
                        .onChanged { value in
                            let start = startWidth ?? width
                            startWidth = start
                            let range = WorkspaceModel.sidebarWidthRange
                            width = min(max(start + value.translation.width, range.lowerBound), range.upperBound)
                        }
                        .onEnded { _ in startWidth = nil })
                    .onTapGesture(count: 2) { width = WorkspaceModel.defaultSidebarWidth }
            }
    }
}

/// A cursor over a region, without taking mouse events.
struct CursorRegion: NSViewRepresentable {
    let cursor: NSCursor

    func makeNSView(context: Context) -> CursorNSView { CursorNSView(cursor: cursor) }
    func updateNSView(_ view: CursorNSView, context: Context) {}
}

final class CursorNSView: NSView {
    private let cursor: NSCursor

    init(cursor: NSCursor) {
        self.cursor = cursor
        super.init(frame: .zero)
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    override func resetCursorRects() {
        addCursorRect(bounds, cursor: cursor)
    }
}

/// Hands back the NSWindow a SwiftUI view lives in.
private struct WindowReader: NSViewRepresentable {
    @Binding var window: NSWindow?

    func makeNSView(context: Context) -> NSView {
        let view = NSView()
        DispatchQueue.main.async { window = view.window }
        return view
    }

    func updateNSView(_ view: NSView, context: Context) {
        if window !== view.window { DispatchQueue.main.async { window = view.window } }
    }
}

/// The toolbar above the grid: sidebar toggle, the Live / Playback switch
/// (top-left), the layout picker, then Events, Alarms and the more menu.
struct WorkspaceToolbar: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var appearance: OperatorAppearance
    @EnvironmentObject private var navigator: OperatorNavigator
    @ObservedObject var workspace: WorkspaceModel
    let section: OperatorSection

    private static let layouts: [(columns: Int, rows: Int)] = [(1, 1), (2, 1), (2, 2), (3, 2), (3, 3), (4, 3), (4, 4), (5, 4), (6, 5)]

    var body: some View {
        HStack(spacing: 10) {
            if workspace.sidebarVisible == false { Color.clear.frame(width: 72, height: 1) }
            Button { withAnimation(.easeInOut(duration: 0.15)) { workspace.sidebarVisible.toggle() } } label: {
                Image(systemName: "sidebar.left")
            }
            .keyboardShortcut("s", modifiers: [.command, .control])
            .help(workspace.sidebarVisible ? "Hide the sidebar" : "Show the sidebar")

            Picker("Mode", selection: Binding(get: { navigator.mode }, set: { navigator.show($0) })) {
                ForEach(WorkspaceMode.allCases) { mode in Text(mode.rawValue).tag(mode) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(width: 160)
            .help("Live video, or recorded video on a timeline")

            Menu {
                ForEach(Array(Self.layouts.enumerated()), id: \.offset) { _, layout in
                    Button {
                        workspace.setLayout(columns: layout.columns, rows: layout.rows)
                    } label: {
                        let selected = workspace.grid.columns == layout.columns && workspace.grid.rows == layout.rows
                        Text("\(layout.columns) × \(layout.rows)" + (selected ? "  ✓" : ""))
                    }
                }
                Divider()
                Button("Clear Grid") { workspace.clearGrid() }
            } label: {
                Label("\(workspace.grid.columns) × \(workspace.grid.rows)", systemImage: "square.grid.2x2")
            }
            .menuStyle(.button)
            .fixedSize()
            .help("Grid layout")

            if workspace.focusedCameraID != nil {
                Button { workspace.focusedCameraID = nil } label: {
                    Label("Back to Grid", systemImage: "arrow.down.right.and.arrow.up.left")
                }
            }

            Spacer(minLength: 12)

            connectionBadge

            Button { toggle(.events) } label: { Label("Events", systemImage: OperatorSection.events.symbol) }
                .tint(section == .events ? SentinelTheme.accent : nil)
                .help("Detections and analytics")
            Button { toggle(.alarms) } label: {
                Label(alarms.open.isEmpty ? "Alarms" : "Alarms (\(alarms.open.count))", systemImage: OperatorSection.alarms.symbol)
            }
            .tint(section == .alarms ? SentinelTheme.accent : (alarms.newCount > 0 ? .red : nil))
            .help("Respond, investigate and lock evidence")

            moreMenu
        }
        .buttonStyle(.bordered)
        .controlSize(.small)
        .padding(.horizontal, 14)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .background(SentinelTheme.chrome)
    }

    private func toggle(_ target: OperatorSection) {
        navigator.section = navigator.section == target ? .workspace : target
    }

    private var moreMenu: some View {
        Menu {
            if let site = session.site {
                Text("\(site.name) — \(site.address)")
            }
            Button("Open Sentinel Admin") { if let url = session.site?.url { NSWorkspace.shared.open(url) } }
            Menu("Administration") { AdministrationMenuItems() }
            Divider()
            Toggle("Fill Tiles (crop video)", isOn: $workspace.fillTiles)
            Menu("Monitor Window") {
                Button("New Window") { OperatorWindows.shared.openMonitorWindow(viewID: workspace.activeViewID) }
                ForEach(Array(NSScreen.screens.enumerated()), id: \.offset) { _, screen in
                    Button("On \(screen.localizedName)") {
                        OperatorWindows.shared.openMonitorWindow(viewID: workspace.activeViewID, screen: screen)
                    }
                }
            }
            Divider()
            ForEach(session.sites.sites.filter { $0.id != session.site?.id }) { other in
                Button("Switch to \(other.name)") {
                    session.switchSite()
                    Task { await session.connect(to: other) }
                }
            }
            Button("Add Another Site…") { session.switchSite() }
            Divider()
            Picker("Theme", selection: Binding(get: { appearance.appearance }, set: { appearance.set($0) })) {
                Label("Light", systemImage: "sun.max.fill").tag(OperatorAppearance.Mode.light)
                Label("Dark", systemImage: "moon.fill").tag(OperatorAppearance.Mode.dark)
            }
            Divider()
            Button("Sign Out") { Task { await session.signOut() } }
        } label: {
            Label(session.site?.name ?? "Site", systemImage: "ellipsis.circle")
        }
        .menuStyle(.button)
        .fixedSize()
    }

    private var connectionBadge: some View {
        let summary = runtime.streamSummary
        let color = Color(nsColor: summary.color)
        return Label(summary.label == "Live" ? "Connected" : summary.label, systemImage: summary.label == "Live" ? "checkmark.seal.fill" : "bolt.horizontal.circle")
            .font(.caption.weight(.semibold))
            .foregroundStyle(color)
            .lineLimit(1)
            .padding(.horizontal, 9)
            .padding(.vertical, 5)
            .background(color.opacity(0.12), in: RoundedRectangle(cornerRadius: 4))
            .help(helpText)
    }

    private var helpText: String {
        switch runtime.streamState {
        case .connected(let version): return "Live notifications from the Management Server (version \(version))"
        case .waiting(let retry, let reason): return "\(reason) Retrying in \(Int(retry.rounded())) s."
        case .stopped(let reason): return reason
        default: return "Connecting to the Management Server…"
        }
    }
}

/// Signed-in user, site and connection state at the bottom of the sidebar.
struct SiteMiniStatus: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Divider().overlay(SentinelTheme.line)
            if let me = session.me {
                HStack(spacing: 8) {
                    Image(systemName: "person.crop.circle.fill").font(.caption).foregroundStyle(SentinelTheme.accent)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(me.name).font(.caption.weight(.semibold)).lineLimit(1)
                        Text(me.role.rawValue).font(.caption2).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button {
                        Task { await session.signOut() }
                    } label: {
                        Image(systemName: "rectangle.portrait.and.arrow.right").font(.caption)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .help("Sign out of \(session.site?.name ?? "this site")")
                }
                .padding(.horizontal, 16)
                Divider().overlay(SentinelTheme.line)
            }
            HStack {
                Text(session.site?.name ?? "Site").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                Spacer()
                Text(runtime.streamSummary.label)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Color(nsColor: runtime.streamSummary.color))
            }
            .padding(.horizontal, 16)
            HStack {
                Text("\(cameras.onlineCount)/\(cameras.cameras.count) cameras online")
                    .font(.caption2).foregroundStyle(.secondary)
                Spacer()
            }
            .padding(.horizontal, 16)
            .padding(.bottom, 16)
        }
        .padding(.top, 10)
    }
}

/// Red strip across the top when a new critical alarm arrives.
struct CriticalAlarmBanner: View {
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var navigator: OperatorNavigator
    let alarm: V2.Alarm

    var body: some View {
        HStack(spacing: 10) {
            PulsingDot()
            VStack(alignment: .leading, spacing: 1) {
                Text(alarm.title).font(.callout.weight(.semibold))
                Text([alarm.cameraName, alarm.detail].compactMap { $0 }.filter { $0.isEmpty == false }.joined(separator: " · "))
                    .font(.caption).lineLimit(1)
            }
            Spacer()
            Button("Respond") {
                navigator.openAlarm(alarm.id)
                alarms.attention = nil
            }
            .buttonStyle(.borderedProminent)
            .tint(.red)
            Button {
                alarms.attention = nil
            } label: {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .help("Hide (the alarm stays in the queue)")
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 18)
        .padding(.vertical, 10)
        .background(SentinelTheme.alarm.opacity(0.92))
    }
}

private struct PulsingDot: View {
    @State private var on = false
    var body: some View {
        Circle().fill(.white).frame(width: 10, height: 10)
            .opacity(on ? 1 : 0.3)
            .onAppear { withAnimation(.easeInOut(duration: 0.7).repeatForever()) { on = true } }
    }
}

/// Light/dark choice, applied to the whole app.
@MainActor
final class OperatorAppearance: ObservableObject {
    enum Mode: String { case light, dark }
    private let key = "handoffgrid.appearance"
    @Published private(set) var appearance: Mode

    init() {
        appearance = Mode(rawValue: UserDefaults.standard.string(forKey: key) ?? "") ?? .light
        apply()
    }

    var colorScheme: ColorScheme { appearance == .dark ? .dark : .light }

    func set(_ mode: Mode) {
        appearance = mode
        UserDefaults.standard.set(mode.rawValue, forKey: key)
        apply()
    }

    private func apply() {
        NSApplication.shared.appearance = NSAppearance(named: appearance == .dark ? .darkAqua : .aqua)
    }
}

/// Sections that are Admin work but not worth a native page: send to Sentinel Admin.
struct OpenInAdminCard: View {
    @EnvironmentObject private var session: OperatorSession
    let title: String
    let detail: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "safari").font(.title2).foregroundStyle(SentinelTheme.accent)
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.callout.weight(.semibold))
                Text(detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
            Button("Open Sentinel Admin") {
                if let url = session.site?.url { NSWorkspace.shared.open(url) }
            }
        }
        .padding(14)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }
}

/// "You can't see this" for sections the signed-in role may not use.
struct PermissionNote: View {
    let text: String
    var body: some View {
        VStack(spacing: 10) {
            Image(systemName: "lock.fill").font(.title).foregroundStyle(.secondary)
            Text(text).font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(40)
    }
}

extension View {
    /// Everything an Operator window needs from the signed-in session.
    @MainActor
    func operatorEnvironment(session: OperatorSession, runtime: SiteRuntime, navigator: OperatorNavigator, appearance: OperatorAppearance) -> some View {
        environmentObject(session)
            .environmentObject(runtime)
            .environmentObject(runtime.cameras)
            .environmentObject(runtime.alarms)
            .environmentObject(runtime.events)
            .environmentObject(runtime.views)
            .environmentObject(navigator)
            .environmentObject(appearance)
    }
}

enum OperatorFormat {
    static let time: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .none
        f.timeStyle = .medium
        return f
    }()

    static let shortTime: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .none
        f.timeStyle = .short
        return f
    }()

    static let dateTime: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .medium
        f.timeStyle = .medium
        return f
    }()

    static func relative(_ millis: UnixMillis) -> String {
        let date = Date(unixMillis: millis)
        let interval = Date().timeIntervalSince(date)
        if interval < 60 { return "just now" }
        if interval < 3600 { return "\(Int(interval / 60)) min ago" }
        if Calendar.current.isDateInToday(date) { return time.string(from: date) }
        return dateTime.string(from: date)
    }

    static func bytes(_ count: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: count, countStyle: .file)
    }
}
