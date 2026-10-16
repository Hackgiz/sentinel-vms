import AppKit
import SwiftUI
import SentinelCore

/// The objects every Sentinel Operator window shares: the session (site,
/// sign-in, live state), cross-view navigation and the appearance choice.
@MainActor
final class OperatorContext {
    static let shared = OperatorContext()

    let session = OperatorSession()
    let navigator = OperatorNavigator()
    let appearance = OperatorAppearance()
}

/// Extra windows: monitor windows (a live wall for another display, showing
/// all cameras or a saved view) and pop-out windows (one camera each).
/// Monitor windows are remembered per site and reopened on the same display
/// after the next sign-in.
@MainActor
final class OperatorWindows: NSObject, NSWindowDelegate {
    static let shared = OperatorWindows()

    private struct MonitorRecord: Codable, Hashable {
        var id: String
        var viewID: String?
        var screenName: String?
        var frame: String?
    }

    private var cameraWindows: [String: NSWindow] = [:]
    private var monitorWindows: [String: NSWindow] = [:]
    private var restoring = false

    private var context: OperatorContext { .shared }

    // MARK: Pop-out (one camera)

    func openCamera(_ cameraID: String, name: String) {
        if let existing = cameraWindows[cameraID] {
            existing.makeKeyAndOrderFront(nil)
            return
        }
        guard let runtime = context.session.runtime else { return }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 800, height: 450),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = name
        window.contentAspectRatio = NSSize(width: 16, height: 9)
        window.minSize = NSSize(width: 320, height: 180)
        window.isReleasedWhenClosed = false
        window.collectionBehavior.insert(.fullScreenPrimary)
        window.delegate = self
        window.contentView = NSHostingView(rootView:
            CameraPopOutContent(cameraID: cameraID)
                .operatorEnvironment(session: context.session, runtime: runtime,
                                     navigator: context.navigator, appearance: context.appearance)
        )
        window.center()
        window.makeKeyAndOrderFront(nil)
        cameraWindows[cameraID] = window
    }

    // MARK: Monitor windows (live wall)

    /// Opens a live wall window, on `screen` (full screen) when given.
    func openMonitorWindow(viewID: String?, screen: NSScreen? = nil, id: String = UUID().uuidString, frame: NSRect? = nil) {
        guard let runtime = context.session.runtime else { return }
        let window = NSWindow(
            contentRect: frame ?? NSRect(x: 0, y: 0, width: 1280, height: 760),
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "Sentinel Operator — Monitor"
        window.titlebarAppearsTransparent = true
        window.minSize = NSSize(width: 480, height: 300)
        window.isReleasedWhenClosed = false
        window.collectionBehavior.insert(.fullScreenPrimary)
        window.delegate = self
        window.identifier = NSUserInterfaceItemIdentifier("monitor." + id)
        if let viewID {
            UserDefaults.standard.set(viewID, forKey: "sentinel.live.monitor-\(id).view")
        }
        window.contentView = NSHostingView(rootView:
            MonitorWindowContent(windowID: "monitor-\(id)")
                .operatorEnvironment(session: context.session, runtime: runtime,
                                     navigator: context.navigator, appearance: context.appearance)
        )
        if let screen {
            let visible = screen.visibleFrame
            window.setFrame(frame ?? visible.insetBy(dx: 40, dy: 40), display: true)
        } else if frame == nil {
            window.center()
        }
        window.makeKeyAndOrderFront(nil)
        monitorWindows[id] = window
        saveMonitors()
    }

    /// Reopens the monitor windows that were open when this site was last used.
    func restoreMonitors() {
        guard let key = monitorsKey,
              let data = UserDefaults.standard.data(forKey: key),
              let records = try? JSONDecoder().decode([MonitorRecord].self, from: data) else { return }
        restoring = true
        defer { restoring = false; saveMonitors() }
        for record in records where monitorWindows[record.id] == nil {
            let screen = NSScreen.screens.first { $0.localizedName == record.screenName }
            let frame = record.frame.map(NSRectFromString)
            openMonitorWindow(viewID: nil, screen: screen, id: record.id, frame: frame)
        }
    }

    /// Closes every extra window (sign-out, site switch). Monitor windows stay
    /// remembered for the next sign-in to the same site.
    func closeAll() {
        saveMonitors()
        restoring = true
        ManagementWindowController.shared.close()
        (Array(cameraWindows.values) + Array(monitorWindows.values)).forEach { $0.close() }
        cameraWindows.removeAll()
        monitorWindows.removeAll()
        restoring = false
    }

    var monitorCount: Int { monitorWindows.count }

    private var monitorsKey: String? {
        context.session.site.map { "sentinel.operator.monitors.\($0.id)" }
    }

    private func saveMonitors() {
        guard restoring == false, let key = monitorsKey else { return }
        let records = monitorWindows.map { id, window in
            MonitorRecord(id: id, viewID: nil, screenName: window.screen?.localizedName,
                          frame: NSStringFromRect(window.frame))
        }
        if let data = try? JSONEncoder().encode(records) {
            UserDefaults.standard.set(data, forKey: key)
        }
    }

    // MARK: NSWindowDelegate

    func windowWillClose(_ notification: Notification) {
        guard let window = notification.object as? NSWindow else { return }
        cameraWindows = cameraWindows.filter { $0.value !== window }
        let before = monitorWindows.count
        monitorWindows = monitorWindows.filter { $0.value !== window }
        if monitorWindows.count != before { saveMonitors() }
    }

    func windowDidMove(_ notification: Notification) {
        saveMonitors()
    }

    func windowDidEndLiveResize(_ notification: Notification) {
        saveMonitors()
    }
}

/// A monitor window: a live grid with its own layout, remembered per window.
private struct MonitorWindowContent: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var views: SavedViewsStore
    @StateObject private var workspace: WorkspaceModel
    let windowID: String

    init(windowID: String) {
        self.windowID = windowID
        _workspace = StateObject(wrappedValue: WorkspaceModel(windowID: windowID))
    }

    var body: some View {
        WorkspaceGridView(workspace: workspace)
            .padding(.top, 28)
            .background(SentinelTheme.background)
            .onAppear {
                if let site = session.site { workspace.bind(siteID: site.id) }
                let key = "sentinel.live.\(windowID).view"
                if let viewID = UserDefaults.standard.string(forKey: key), let view = views.view(viewID) {
                    workspace.openView(view)
                    UserDefaults.standard.removeObject(forKey: key)
                }
                workspace.seedIfNeeded(with: cameras.cameras.map(\.id), limit: 16)
            }
            .onChange(of: cameras.cameras.map(\.id)) { ids in
                workspace.seedIfNeeded(with: ids, limit: 16)
            }
    }
}

/// A pop-out window: one camera, with "keep on top".
private struct CameraPopOutContent: View {
    @EnvironmentObject private var cameras: CameraDirectory
    @State private var keepOnTop = false
    let cameraID: String

    var body: some View {
        Group {
            if let camera = cameras.camera(cameraID) {
                LiveTile(camera: camera, isSelected: false, fill: false, showsControls: false)
                    .overlay(alignment: .bottomTrailing) {
                        Button {
                            keepOnTop.toggle()
                            NSApp.keyWindow?.level = keepOnTop ? .floating : .normal
                        } label: {
                            Image(systemName: keepOnTop ? "pin.fill" : "pin")
                                .font(.system(size: 11, weight: .semibold))
                                .foregroundStyle(keepOnTop ? SentinelTheme.accent : .white)
                                .padding(7)
                                .background(.black.opacity(0.6), in: Circle())
                        }
                        .buttonStyle(.plain)
                        .padding(10)
                        .help(keepOnTop ? "Stop keeping this window on top" : "Keep this window on top")
                    }
            } else {
                Text("This camera isn't available any more.")
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color.black)
    }
}
