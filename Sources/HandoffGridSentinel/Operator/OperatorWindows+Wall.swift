import AppKit
import SentinelCore

/// The video wall over the monitor windows (Wave 10, stream A): read the
/// open monitors into a layout, recall a layout onto the displays that are
/// connected now, and take every monitor full screen.
extension OperatorWindows {
    /// The open monitor windows as they are now.
    func wallSnapshots() -> [WallMonitorSnapshot] {
        let screens = NSScreen.screens
        return monitorWindows.compactMap { id, window in
            guard let state = monitorStates[id] else { return nil }
            let screen = window.screen
            let displayIndex = screen.flatMap { s in screens.firstIndex { $0 == s } }
            let top = (screen?.frame.maxY ?? 0) - window.frame.maxY
            let left = window.frame.minX - (screen?.frame.minX ?? 0)
            let workspace = state.workspace
            return WallMonitorSnapshot(
                displayName: screen?.localizedName ?? "",
                displayIndex: displayIndex,
                originX: Double(left), originY: Double(top),
                viewID: state.tourPlan == nil ? workspace?.activeViewID : state.tourViewID,
                cameraIDs: workspace?.cameraIDs ?? [],
                tour: state.tourPlan,
                alarmMonitor: state.alarmMonitor,
                fullscreen: window.styleMask.contains(.fullScreen)
            )
        }
    }

    /// The monitors to store for the windows open now.
    func currentWallMonitors() -> [V2.WallMonitor] {
        WallLayoutCodec.monitors(from: wallSnapshots())
    }

    /// Replaces the monitor windows with `layout`'s, each on the display with
    /// its saved name, else at its saved position, else the main display.
    /// Monitors sharing a display split it side by side; flagged ones go
    /// full screen one after another (macOS animates each into its own Space).
    func recall(_ layout: V2.WallLayout) {
        guard context.session.runtime != nil else { return }
        closeMonitors()
        let screens = NSScreen.screens
        let monitors = WallLayoutCodec.ordered(layout)
        let placements = WallDisplayMatcher.place(monitors, on: Self.displays(screens))
        var perDisplay: [Int: [Int]] = [:]
        for placement in placements { perDisplay[placement.displayIndex, default: []].append(placement.slot) }

        var toFullscreen: [NSWindow] = []
        for monitor in monitors {
            guard let placement = placements.first(where: { $0.slot == monitor.slot }),
                  screens.indices.contains(placement.displayIndex) else { continue }
            let screen = screens[placement.displayIndex]
            let siblings = perDisplay[placement.displayIndex] ?? [monitor.slot]
            let frame = Self.frame(on: screen, part: siblings.firstIndex(of: monitor.slot) ?? 0, of: siblings.count)
            let window = openMonitorWindow(viewID: nil, screen: screen, frame: frame,
                                           content: WallLayoutCodec.content(of: monitor),
                                           alarmMonitor: monitor.isAlarmMonitor)
            if monitor.isFullscreen, let window { toFullscreen.append(window) }
        }
        enterFullScreen(toFullscreen)
    }

    /// "Fullscreen All Monitors": every monitor window full screen on its
    /// display; when all already are, they all leave full screen.
    func toggleFullscreenAllMonitors() {
        let windows = monitorWindows.values.sorted { $0.frame.minX < $1.frame.minX }
        let allFull = windows.isEmpty == false && windows.allSatisfy { $0.styleMask.contains(.fullScreen) }
        if allFull {
            stagger(windows) { $0.toggleFullScreen(nil) }
        } else {
            enterFullScreen(windows.filter { $0.styleMask.contains(.fullScreen) == false })
        }
    }

    var allMonitorsFullscreen: Bool {
        monitorWindows.isEmpty == false && monitorWindows.values.allSatisfy { $0.styleMask.contains(.fullScreen) }
    }

    private func enterFullScreen(_ windows: [NSWindow]) {
        stagger(windows) { window in
            guard window.styleMask.contains(.fullScreen) == false else { return }
            window.toggleFullScreen(nil)
        }
    }

    /// One window at a time: several full-screen transitions started in the
    /// same run-loop turn can be dropped by AppKit.
    private func stagger(_ windows: [NSWindow], _ action: @escaping (NSWindow) -> Void) {
        for (i, window) in windows.enumerated() {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.2 + Double(i) * 0.8) { action(window) }
        }
    }

    /// Part `part` of `count` side-by-side columns of the display's visible area.
    static func frame(on screen: NSScreen, part: Int, of count: Int) -> NSRect {
        let visible = screen.visibleFrame
        guard count > 1 else { return visible.insetBy(dx: 24, dy: 24) }
        let width = visible.width / CGFloat(count)
        return NSRect(x: visible.minX + CGFloat(part) * width, y: visible.minY, width: width, height: visible.height)
            .insetBy(dx: 8, dy: 8)
    }
}

/// Wall commands shared by the Wall menu and the toolbar picker.
@MainActor
enum WallActions {
    private static var context: OperatorContext { .shared }

    /// "Save Current Monitors as Layout…": asks for a name (and sharing,
    /// for roles that may share), then stores the open monitors.
    static func saveCurrentMonitors() {
        let session = context.session
        guard let runtime = session.runtime else { return }
        let monitors = OperatorWindows.shared.currentWallMonitors()
        let canManage = session.can(.manageViews)
        let alert = NSAlert()
        alert.messageText = "Save Wall Layout"
        alert.informativeText = monitors.isEmpty
            ? "Open monitor windows first (File ▸ New Monitor Window), arrange them on your displays, then save."
            : "Saves the \(monitors.count) open monitor window\(monitors.count == 1 ? "" : "s"): their displays, what they show, tours and alarm monitors."
        let name = NSTextField(frame: NSRect(x: 0, y: 28, width: 280, height: 24))
        name.placeholderString = "Layout name"
        let share = NSButton(checkboxWithTitle: "Shared with everyone on this site", target: nil, action: nil)
        share.frame = NSRect(x: 0, y: 0, width: 280, height: 20)
        share.isEnabled = canManage
        if canManage == false { share.toolTip = "Your role can't share wall layouts." }
        let accessory = NSView(frame: NSRect(x: 0, y: 0, width: 280, height: 54))
        accessory.addSubview(name)
        accessory.addSubview(share)
        alert.accessoryView = accessory
        alert.addButton(withTitle: "Save")
        alert.addButton(withTitle: "Cancel")
        alert.window.initialFirstResponder = name
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        do {
            let input = try WallLayoutSaving.input(name: name.stringValue, shared: share.state == .on,
                                                   canManage: canManage, monitors: monitors)
            Task {
                do {
                    try await runtime.wall.create(input, api: runtime.api)
                } catch {
                    show("Couldn't save the wall layout", error.localizedDescription)
                }
            }
        } catch {
            show("Couldn't save the wall layout", error.localizedDescription)
        }
    }

    /// "Update with Current Monitors".
    static func updateWithCurrentMonitors(_ layout: V2.WallLayout) {
        guard let runtime = context.session.runtime else { return }
        let monitors = OperatorWindows.shared.currentWallMonitors()
        guard monitors.isEmpty == false else {
            show("No monitor windows are open", WallLayoutSaving.Problem.noMonitors.localizedDescription)
            return
        }
        Task {
            do {
                try await runtime.wall.update(layout.id, V2.WallLayoutPatch(monitors: monitors), api: runtime.api)
            } catch {
                show("Couldn't update “\(layout.name)”", error.localizedDescription)
            }
        }
    }

    static func recall(_ layout: V2.WallLayout) {
        OperatorWindows.shared.recall(layout)
    }

    static func delete(_ layout: V2.WallLayout) {
        guard let runtime = context.session.runtime else { return }
        let alert = NSAlert()
        alert.messageText = "Delete “\(layout.name)”?"
        alert.informativeText = layout.shared
            ? "This shared layout disappears for everyone. Open monitor windows stay as they are."
            : "Open monitor windows stay as they are."
        alert.addButton(withTitle: "Delete").hasDestructiveAction = true
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        Task {
            do {
                try await runtime.wall.delete(layout, api: runtime.api)
            } catch {
                show("Couldn't delete “\(layout.name)”", error.localizedDescription)
            }
        }
    }

    static func refresh() {
        guard let runtime = context.session.runtime else { return }
        Task { await runtime.wall.reload(api: runtime.api) }
    }

    static func canEdit(_ layout: V2.WallLayout) -> Bool {
        let session = context.session
        return WallLayoutSaving.canEdit(layout, userID: session.me?.id, canManage: session.can(.manageViews))
    }

    private static func show(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.runModal()
    }
}
