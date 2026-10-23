import SwiftUI
import SentinelCore

/// What a workspace pane can ask the shell to do. The coordinator hands
/// these to the real views when wiring them in.
struct WorkspacePaneContext {
    /// Shows a camera in the Live tab (e.g. a click on a map pin).
    var openCamera: (String) -> Void
    /// Opens Playback on the Live tab at a time (e.g. a search result).
    var openPlayback: (_ cameraIDs: [String], _ at: Date) -> Void
    /// Switches to another fixed tab (e.g. Incidents -> Alarm Manager).
    var openTab: (FixedTab) -> Void
    /// Opens the Alarm Manager tab (and lets it focus `alarmID` itself).
    var openAlarm: (String) -> Void
    /// The alarm the Alarm Manager should select, cleared by the pane.
    var alarmFocus: Binding<String?>
    /// A camera + range handed over from Playback's "Lock as Evidence…".
    var exportPrefill: ExportPrefill?
    /// Opens a Management node in the main window: its rail pane (Insights,
    /// People, …) or the Admin workspace with that node selected.
    var openManagement: (String) -> Void = { _ in }
}

/// The registry behind the tab bar's fixed workspaces other than Live.
///
/// Until a stream's real view is registered the tab shows a placeholder.
/// To wire a stream in at merge time, register it once at launch (see
/// `WorkspacePaneRegistry.installBuiltInPanes()`), for example:
///
///     register(.map) { ctx in AnyView(MapWorkspaceView(onOpenCamera: ctx.openCamera)) }
///
/// The builder runs whenever the tab is shown; panes keep their own state
/// in `@StateObject`s (the shell keeps no instance alive while another tab
/// is selected).
@MainActor
final class WorkspacePaneRegistry {
    typealias Builder = (WorkspacePaneContext) -> AnyView

    static let shared: WorkspacePaneRegistry = {
        let registry = WorkspacePaneRegistry()
        registry.installBuiltInPanes()
        return registry
    }()

    private var builders: [FixedTab: Builder] = [:]

    func register(_ tab: FixedTab, _ builder: @escaping Builder) {
        builders[tab] = builder
    }

    func isRegistered(_ tab: FixedTab) -> Bool { builders[tab] != nil }

    /// The pane for `tab`: the registered view, or the placeholder.
    func pane(for tab: FixedTab, context: WorkspacePaneContext) -> AnyView {
        if let builder = builders[tab] { return builder(context) }
        return AnyView(PlaceholderPane(tab: tab))
    }

    /// The coordinator's wiring point: one line per stream view, added at merge.
    private func installBuiltInPanes() {
        register(.map) { ctx in AnyView(MapWorkspaceView(onOpenCamera: ctx.openCamera)) }
        register(.search) { ctx in AnyView(SearchWorkspaceView(onPlayback: { id, at in ctx.openPlayback([id], at) })) }
        register(.exports) { ctx in
            AnyView(ExportsWorkspaceView(prefill: ctx.exportPrefill, onNotice: { message, isError in
                _ = NoticeCenter.shared.post(isError ? .error : .success, message)
            }))
        }
        register(.alarms) { ctx in AnyView(AlarmManagerWorkspaceView(focusAlarmID: ctx.alarmFocus, onPlayback: { id, at in ctx.openPlayback([id], at) })) }
        register(.incidents) { ctx in AnyView(IncidentsWorkspaceView(onPlayback: { id, at in ctx.openPlayback([id], at) }, onOpenAlarm: ctx.openAlarm)) }
        register(.monitor) { _ in AnyView(SystemMonitorWorkspaceView()) }
        register(.reports) { _ in AnyView(ReportsWorkspaceView()) }
    }
}

/// Stands in for a workspace whose stream hasn't been merged yet.
struct PlaceholderPane: View {
    let tab: FixedTab

    private var stream: String {
        switch tab {
        case .map, .search: return "stream B"
        case .alarms, .incidents: return "stream C"
        case .exports, .monitor: return "stream D"
        case .reports: return "parity 3.4.2"
        case .home, .live: return "the shell"
        }
    }

    private var symbol: String {
        switch tab {
        case .home: return "house"
        case .live: return "rectangle.grid.2x2"
        case .map: return "map"
        case .search: return "magnifyingglass"
        case .exports: return "square.and.arrow.up"
        case .alarms: return "exclamationmark.octagon"
        case .incidents: return "folder.badge.questionmark"
        case .monitor: return "waveform.path.ecg"
        case .reports: return "chart.bar.doc.horizontal"
        }
    }

    var body: some View {
        VStack(spacing: 10) {
            Image(systemName: symbol).font(.system(size: 34)).foregroundStyle(.secondary)
            Text(tab.label).font(.title3.weight(.semibold))
            Text("\(tab.label) arrives with \(stream).")
                .font(.callout).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(SentinelTheme.background)
    }
}
