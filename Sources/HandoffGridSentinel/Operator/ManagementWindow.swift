import AppKit
import SwiftUI
import SentinelCore

// The native Management window (wave 6 stream E): a site-navigation tree on
// the left, the selected pane under a breadcrumb on the right. Tree logic
// lives in `SentinelCore/Management/ManagementNavModel.swift`.

// MARK: - Pane registry (stream F plugs in here)

/// What a management pane can ask the window to do.
struct ManagementPaneContext {
    /// Selects another node (e.g. "security/roles").
    var open: (String) -> Void
}

/// id -> view factory for the Management window's leaves.
///
/// WIRING (coordinator, at merge of stream F): add one line per F pane in
/// `installStreamFPanes()` below, keyed by `ManagementCatalog.Leaf.*`, e.g.
///
///     register(ManagementCatalog.Leaf.roles) { _ in AnyView(RolesPane()) }
///
/// Leaves without a registered pane render `ManagementStubPane`. The
/// existing flat admin views are registered by `installAdminLeaves()`.
@MainActor
final class ManagementPanes {
    typealias Builder = (ManagementPaneContext) -> AnyView

    static let shared: ManagementPanes = {
        let panes = ManagementPanes()
        panes.installAdminLeaves()
        panes.installStreamFPanes()
        return panes
    }()

    private var registry = ManagementPaneRegistry<Builder>()

    func register(_ id: String, _ builder: @escaping Builder) { registry.register(id, builder) }
    func isRegistered(_ id: String) -> Bool { registry.isRegistered(id) }

    func pane(for node: ManagementNode, context: ManagementPaneContext) -> AnyView {
        if let builder = registry.pane(for: node.id) { return builder(context) }
        return AnyView(ManagementStubPane(node: node))
    }

    /// Existing views from AdminViews.swift / the System Monitor, rendered unchanged.
    private func installAdminLeaves() {
        register(ManagementCatalog.Leaf.overview) { _ in AnyView(ManagementOverviewPane()) }
        register(ManagementCatalog.Leaf.recordingServers) { _ in AnyView(RecordingServersView()) }
        register(ManagementCatalog.Leaf.cameras) { _ in AnyView(CamerasAdminView()) }
        register(ManagementCatalog.Leaf.rules) { _ in AnyView(RulesView()) }
        register(ManagementCatalog.Leaf.users) { _ in AnyView(UsersAdminView()) }
        register(ManagementCatalog.Leaf.systemMonitor) { _ in AnyView(SystemMonitorWorkspaceView()) }
        register(ManagementCatalog.Leaf.audit) { _ in AnyView(AuditLogV2View()) }
        register(ManagementCatalog.Leaf.zones) { _ in AnyView(ZonesPaneHost()) } // WAVE11(E): Zones & Privacy
        register(ManagementCatalog.Leaf.cloud) { _ in AnyView(CloudPaneHost()) } // WAVE12(E): Sentinel Cloud
        register(ManagementCatalog.Leaf.insights) { context in AnyView(InsightsPane(context: context)) } // wave 15
        register(ManagementCatalog.Leaf.people) { _ in AnyView(PeoplePane()) } // wave 17
        register(ManagementCatalog.Leaf.plates) { _ in AnyView(PlatesPane()) } // wave 17
        register(ManagementCatalog.Leaf.detection) { _ in AnyView(DetectionPane()) } // wave 17
    }

    /// Stream F's panes, registered from `ManagementPaneCatalog.panes`.
    private func installStreamFPanes() {
        let leaves: [String: String] = [
            "site-info": ManagementCatalog.Leaf.siteInfo,
            "roles": ManagementCatalog.Leaf.roles,
            "time-profiles": ManagementCatalog.Leaf.timeProfiles,
            "webhooks": ManagementCatalog.Leaf.webhooks,
            "notifications": ManagementCatalog.Leaf.notifications,
            "tasks": ManagementCatalog.Leaf.currentTasks,
            "evidence": ManagementCatalog.Leaf.evidenceLocks,
            "config-report": ManagementCatalog.Leaf.configReport,
            "logs": ManagementCatalog.Leaf.serverLogs,
            "thresholds": ManagementCatalog.Leaf.monitorThresholds,
        ]
        for pane in ManagementPaneCatalog.panes {
            guard let leaf = leaves[pane.id] else { continue }
            register(leaf) { _ in AnyView(ManagementAPIPane(make: pane.factory)) }
        }
    }
}

/// Hands the site's `SentinelAPI` from the environment to a stream-F pane.
private struct ManagementAPIPane: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let make: (SentinelAPI) -> AnyView

    var body: some View { make(runtime.api) }
}

/// A leaf whose native pane isn't built (yet): points at Sentinel Admin.
struct ManagementStubPane: View {
    let node: ManagementNode

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Label(node.title, systemImage: node.symbol).font(.title3.weight(.semibold))
            OpenInAdminCard(title: "Manage \(node.title) in Sentinel Admin",
                            detail: "This page is available in the web Sentinel Admin; a native version is coming.")
            Spacer()
        }
        .padding(18)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// Basics > Overview: site counts from the live directory.
private struct ManagementOverviewPane: View {
    @EnvironmentObject private var cameras: CameraDirectory

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            statRow("Cameras", "\(cameras.cameras.count) (\(cameras.onlineCount) online)")
            statRow("Recording Servers", "\(cameras.recorders.count)")
            Spacer()
        }
        .padding(18)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func statRow(_ title: String, _ value: String) -> some View {
        HStack {
            Text(title).foregroundStyle(.secondary)
            Spacer()
            Text(value).font(.callout.weight(.semibold))
        }
        .padding(14)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }
}

// MARK: - Selection & window controller

/// The window's selection, shared so menu entries can deep-link into it.
@MainActor
final class ManagementSelection: ObservableObject {
    @Published var nodeID = ManagementCatalog.rootID
}

@MainActor
final class ManagementWindowController: NSObject, NSWindowDelegate {
    static let shared = ManagementWindowController()

    let selection = ManagementSelection()
    private var window: NSWindow?

    /// Opens (or raises) the window, selecting `node` (a deep link such as
    /// "security/roles"; nil keeps the current selection).
    func open(node: String? = nil) {
        let context = OperatorContext.shared
        guard let runtime = context.session.runtime,
              ManagementCatalog.hasManagementAccess(context.session.can) else { return }
        if let node { selection.nodeID = node }
        if let window {
            window.makeKeyAndOrderFront(nil)
            return
        }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1120, height: 720),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Sentinel Management"
        window.minSize = NSSize(width: 820, height: 520)
        window.isReleasedWhenClosed = false
        window.setFrameAutosaveName("SentinelManagement")
        window.delegate = self
        window.contentView = NSHostingView(rootView:
            ManagementWindowView(selection: selection)
                .operatorEnvironment(session: context.session, runtime: runtime,
                                     navigator: context.navigator, appearance: context.appearance)
        )
        if window.frame.origin == .zero { window.center() }
        window.makeKeyAndOrderFront(nil)
        self.window = window
    }

    func close() {
        window?.close()
        window = nil
    }

    func windowWillClose(_ notification: Notification) {
        window = nil
    }
}

// MARK: - Views

struct ManagementWindowView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @ObservedObject var selection: ManagementSelection

    @AppStorage("sentinel.management.collapsed") private var collapsedRaw = ""
    @State private var filter = ""
    @State private var siteName = ManagementCatalog.fallbackSiteName

    private var model: ManagementNavModel { ManagementNavModel(can: session.can) }
    private var current: String { model.resolve(deepLink: selection.nodeID) }

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 200, ideal: 240, max: 340)
        } detail: {
            detail
        }
        .task(id: session.site?.id) { siteName = await runtime.api.managementSiteName() }
    }

    // MARK: Sidebar

    private var collapsed: Set<String> { ManagementNavModel.decodeCollapsed(collapsedRaw) }

    private var sidebar: some View {
        VStack(spacing: 0) {
            TextField("Filter", text: $filter)
                .textFieldStyle(.roundedBorder)
                .padding(8)
            List {
                Button { selection.nodeID = ManagementCatalog.rootID } label: {
                    Label(siteName, systemImage: "shield.lefthalf.filled").font(.callout.weight(.semibold))
                }
                .buttonStyle(.plain)
                .listRowBackground(current == ManagementCatalog.rootID ? SentinelTheme.accent.opacity(0.22) : Color.clear)

                ForEach(model.rows(filter: filter, collapsed: collapsed)) { row in
                    rowView(row)
                }
            }
            .listStyle(.sidebar)
        }
    }

    @ViewBuilder
    private func rowView(_ row: ManagementRow) -> some View {
        let selected = current == row.node.id
        HStack(spacing: 4) {
            if row.node.isLeaf {
                Label(row.node.title, systemImage: row.node.symbol)
                    .padding(.leading, 14)
            } else {
                Button { toggle(row.node.id) } label: {
                    Image(systemName: row.isExpanded ? "chevron.down" : "chevron.right")
                        .font(.caption2.weight(.bold)).frame(width: 12)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("\(row.isExpanded ? "Collapse" : "Expand") \(row.node.title)")
                Text(row.node.title).font(.callout.weight(.semibold))
            }
            Spacer(minLength: 0)
        }
        .contentShape(Rectangle())
        .onTapGesture { selection.nodeID = row.node.id }
        .listRowBackground(selected ? SentinelTheme.accent.opacity(0.22) : Color.clear)
    }

    private func toggle(_ id: String) {
        var set = collapsed
        if set.contains(id) { set.remove(id) } else { set.insert(id) }
        collapsedRaw = ManagementNavModel.encodeCollapsed(set)
    }

    // MARK: Detail

    private var detail: some View {
        VStack(spacing: 0) {
            let crumbs = model.breadcrumb(for: current, siteName: siteName)
            HStack(spacing: 6) {
                ForEach(Array(crumbs.enumerated()), id: \.offset) { index, crumb in
                    if index > 0 { Image(systemName: "chevron.right").font(.caption2).foregroundStyle(.secondary) }
                    Text(crumb)
                        .font(index == crumbs.count - 1 ? .headline : .callout)
                        .foregroundStyle(index == crumbs.count - 1 ? Color.primary : Color.secondary)
                }
                Spacer()
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            Divider().overlay(SentinelTheme.line)
            content
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .background(SentinelTheme.background)
    }

    @ViewBuilder
    private var content: some View {
        if let node = model.node(current), node.isLeaf {
            ManagementPanes.shared.pane(for: node, context: ManagementPaneContext(open: { selection.nodeID = $0 }))
                .id(node.id)
        } else {
            ManagementWelcomePane(siteName: siteName,
                                  entries: model.node(current)?.children ?? model.categories,
                                  isCategory: current != ManagementCatalog.rootID,
                                  open: { selection.nodeID = $0 })
        }
    }
}

/// The root (and category) pane: product mark, a paragraph, and links.
struct ManagementWelcomePane: View {
    let siteName: String
    let entries: [ManagementNode]
    let isCategory: Bool
    let open: (String) -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if isCategory == false {
                    HandoffGridMark(size: 44)
                    Text(siteName).font(.title.weight(.semibold))
                    Text("Manage this site's servers, cameras, rules, people and system health. Pick a page from the tree, or jump in below.")
                        .foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                ForEach(entries) { entry in
                    Button { open(entry.id) } label: {
                        Label(entry.title, systemImage: entry.symbol)
                    }
                    .buttonStyle(.link)
                }
            }
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
    }
}
