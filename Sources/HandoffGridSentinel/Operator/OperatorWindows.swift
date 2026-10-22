import AppKit
import SwiftUI
import SentinelCore

/// The objects every Sentinel Operator window shares: the session (site,
/// sign-in, live state), cross-view navigation and the appearance choice.
@MainActor
final class OperatorContext {
    static let shared = OperatorContext()

    #if DEBUG
    let session = DebugLaunch.current != nil ? DebugLaunch.makeSession() : OperatorSession()
    #else
    let session = OperatorSession()
    #endif
    let navigator = OperatorNavigator()
    let appearance = OperatorAppearance()
}

/// Extra windows: monitor windows (a live wall for another display, showing
/// all cameras or a saved view) and pop-out windows (one camera each).
/// Monitor windows are remembered per site and reopened on the same display
/// after the next sign-in. Wave 10 makes them a wall: layouts saved to the
/// server and recalled onto matching displays (`OperatorWindows+Wall.swift`),
/// tours and alarm monitors (`MonitorWindowState`, `WallMonitorViews.swift`).
@MainActor
final class OperatorWindows: NSObject, NSWindowDelegate {
    static let shared = OperatorWindows()

    private struct MonitorRecord: Codable, Hashable {
        var id: String
        var viewID: String?
        var screenName: String?
        var frame: String?
        // Wave 10: display-position fallback, alarm monitor and tour.
        var screenIndex: Int?
        var alarmMonitor: Bool?
        var tour: WallTourPlan?
    }

    private var cameraWindows: [String: NSWindow] = [:]
    private(set) var monitorWindows: [String: NSWindow] = [:]
    private(set) var monitorStates: [String: MonitorWindowState] = [:]
    private var restoring = false

    var context: OperatorContext { .shared }

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

    /// Opens a live wall window, on `screen` when given. `content` overrides
    /// `viewID` (a recalled wall layout); `tour` starts cycling views.
    @discardableResult
    func openMonitorWindow(viewID: String?, screen: NSScreen? = nil, id: String = UUID().uuidString, frame: NSRect? = nil,
                           content: WallMonitorContent? = nil, alarmMonitor: Bool = false, tour: WallTourPlan? = nil) -> NSWindow? {
        guard let runtime = context.session.runtime else { return nil }
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
        let state = MonitorWindowState(id: id, alarmMonitor: alarmMonitor)
        if let content {
            state.pendingContent = content
        } else if let viewID {
            state.pendingContent = .view(viewID)
        }
        monitorStates[id] = state
        if let tour, content == nil { state.startTour(tour) }
        window.contentView = NSHostingView(rootView:
            MonitorWindowContent(windowID: "monitor-\(id)", state: state)
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
        return window
    }

    /// Reopens the monitor windows that were open when this site was last used.
    func restoreMonitors() {
        guard let key = monitorsKey,
              let data = UserDefaults.standard.data(forKey: key),
              let records = try? JSONDecoder().decode([MonitorRecord].self, from: data) else { return }
        restoring = true
        defer { restoring = false; saveMonitors() }
        let screens = NSScreen.screens
        let pending = records.filter { monitorWindows[$0.id] == nil }
        // Same display by name, else by position (WallDisplayMatcher).
        let hints = pending.enumerated().map { slot, record in
            V2.WallMonitor(slot: slot, screenHint: record.screenName ?? "", screenIndex: record.screenIndex)
        }
        let placements = WallDisplayMatcher.place(hints, on: Self.displays(screens))
        for (slot, record) in pending.enumerated() {
            let placement = placements.first { $0.slot == slot }
            let screen = placement.flatMap { screens.indices.contains($0.displayIndex) ? screens[$0.displayIndex] : nil }
            // The saved frame only still fits the display it was saved on.
            let frame = placement?.matchedBy == .name ? record.frame.map(NSRectFromString) : nil
            openMonitorWindow(viewID: nil, screen: screen, id: record.id, frame: frame,
                              alarmMonitor: record.alarmMonitor ?? false, tour: record.tour)
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
        monitorStates.values.forEach { $0.halt() }
        monitorStates.removeAll()
        restoring = false
    }

    /// Closes and forgets the monitor windows only (before recalling a layout).
    func closeMonitors() {
        restoring = true
        monitorWindows.values.forEach { $0.close() }
        monitorWindows.removeAll()
        monitorStates.values.forEach { $0.halt() }
        monitorStates.removeAll()
        restoring = false
        saveMonitors()
    }

    var monitorCount: Int { monitorWindows.count }

    /// A monitor's alarm flag or tour changed: remember it.
    func monitorStateChanged() {
        saveMonitors()
    }

    static func displays(_ screens: [NSScreen] = NSScreen.screens) -> [WallDisplay] {
        screens.enumerated().map { WallDisplay(name: $0.element.localizedName, index: $0.offset) }
    }

    private var monitorsKey: String? {
        context.session.site.map { "sentinel.operator.monitors.\($0.id)" }
    }

    private func saveMonitors() {
        guard restoring == false, let key = monitorsKey else { return }
        let screens = NSScreen.screens
        let records = monitorWindows.map { id, window in
            MonitorRecord(id: id, viewID: nil, screenName: window.screen?.localizedName,
                          frame: NSStringFromRect(window.frame),
                          screenIndex: window.screen.flatMap { screen in screens.firstIndex { $0 == screen } },
                          alarmMonitor: monitorStates[id]?.alarmMonitor == true ? true : nil,
                          tour: monitorStates[id]?.tourPlan)
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
        let closing = monitorWindows.filter { $0.value === window }.map(\.key)
        monitorWindows = monitorWindows.filter { $0.value !== window }
        for id in closing { monitorStates.removeValue(forKey: id)?.halt() }
        if monitorWindows.count != before { saveMonitors() }
    }

    func windowDidMove(_ notification: Notification) {
        saveMonitors()
    }

    func windowDidEndLiveResize(_ notification: Notification) {
        saveMonitors()
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
