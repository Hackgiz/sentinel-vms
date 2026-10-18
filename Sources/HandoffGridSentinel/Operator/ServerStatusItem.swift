import AppKit
import Combine
import SentinelCore

/// Menu bar shield that says at a glance whether a Sentinel server is up:
/// the server this Mac hosts (LocalServerManager) and the signed-in site
/// (the event stream's state). Hidden via its own menu or Settings.
@MainActor
final class ServerStatusItem {
    static let shared = ServerStatusItem()

    static let prefKey = "menuBarServerStatus"
    static var enabledByPref: Bool {
        UserDefaults.standard.object(forKey: prefKey) == nil
            ? true
            : UserDefaults.standard.bool(forKey: prefKey)
    }

    private var item: NSStatusItem?
    private var timer: Timer?
    private var sinks: Set<AnyCancellable> = []
    private var runtimeSink: AnyCancellable?

    private var local: LocalServerManager { LocalServerManager.shared }
    private var session: OperatorSession { OperatorContext.shared.session }

    func installIfEnabled() {
        if Self.enabledByPref { install() }
    }

    func setEnabled(_ on: Bool) {
        UserDefaults.standard.set(on, forKey: Self.prefKey)
        on ? install() : remove()
    }

    private func install() {
        guard item == nil else { return }
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)

        local.$status
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.render() }
            .store(in: &sinks)
        session.objectWillChange
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in
                self?.watchRuntime()
                self?.render()
            }
            .store(in: &sinks)
        watchRuntime()

        let timer = Timer(timeInterval: 20, repeats: true) { _ in
            Task { @MainActor in await ServerStatusItem.shared.tick() }
        }
        timer.tolerance = 5
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer

        render()
        Task { await tick() }
    }

    private func remove() {
        if let item { NSStatusBar.system.removeStatusItem(item) }
        item = nil
        timer?.invalidate()
        timer = nil
        sinks.removeAll()
        runtimeSink = nil
    }

    /// Re-renders when the signed-in site's stream state changes.
    private func watchRuntime() {
        runtimeSink = session.runtime?.objectWillChange
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.render() }
    }

    private func tick() async {
        if local.isInstalled { await local.refresh() }
        render()
    }

    // MARK: State

    private enum Health {
        case up(String)      // tooltip detail
        case down(String)
        case none            // nothing to watch
    }

    private var health: Health {
        var up: [String] = []
        var down: [String] = []
        if let status = local.status {
            up.append("local server \(status.version)")
        } else if local.isInstalled {
            down.append("local server stopped")
        }
        if let site = session.site, let runtime = session.runtime {
            switch runtime.streamState {
            case .connected:
                up.append("\(site.name) connected")
            case .connecting, .idle:
                break // starting up; don't alarm
            case .waiting, .stopped:
                down.append("\(site.name) unreachable")
            }
        }
        if !down.isEmpty { return .down(down.joined(separator: ", ")) }
        if !up.isEmpty { return .up(up.joined(separator: ", ")) }
        return .none
    }

    private func render() {
        guard let button = item?.button else { return }
        let (dimmed, detail): (Bool, String)
        switch health {
        case .up(let text): (dimmed, detail) = (false, text)
        case .down(let text): (dimmed, detail) = (true, text)
        case .none: (dimmed, detail) = (true, "no server yet")
        }
        let image = OperatorIcon.statusGlyph(dimmed: dimmed)
        image.accessibilityDescription = "Sentinel server status"
        button.image = image
        button.toolTip = "Sentinel — \(detail)"
        item?.menu = makeMenu()
    }

    private func makeMenu() -> NSMenu {
        let menu = NSMenu()
        func line(_ title: String) {
            let entry = NSMenuItem(title: title, action: nil, keyEquivalent: "")
            entry.isEnabled = false
            menu.addItem(entry)
        }
        if let status = local.status {
            line("Local server: running (\(status.version))")
        } else if local.isInstalled {
            line("Local server: stopped")
        }
        if let site = session.site, let runtime = session.runtime {
            switch runtime.streamState {
            case .connected: line("\(site.name): connected")
            case .connecting, .idle: line("\(site.name): connecting…")
            case .waiting: line("\(site.name): reconnecting…")
            case .stopped: line("\(site.name): offline")
            }
        }
        if menu.items.isEmpty { line("No server yet") }
        menu.addItem(.separator())

        let open = NSMenuItem(title: "Open Sentinel Operator", action: #selector(openOperator), keyEquivalent: "")
        open.target = self
        menu.addItem(open)
        if local.isRunning, let url = URL(string: LocalServerManager.defaultAddress) {
            let dash = NSMenuItem(title: "Open Web Dashboard", action: #selector(openDashboard), keyEquivalent: "")
            dash.target = self
            dash.representedObject = url
            menu.addItem(dash)
        }
        menu.addItem(.separator())
        let hide = NSMenuItem(title: "Hide Status Icon", action: #selector(hideIcon), keyEquivalent: "")
        hide.target = self
        menu.addItem(hide)
        return menu
    }

    @objc private func openOperator() {
        NSApp.activate(ignoringOtherApps: true)
        _ = NSApp.delegate?.applicationShouldHandleReopen?(NSApp, hasVisibleWindows: false)
    }

    @objc private func openDashboard(_ sender: NSMenuItem) {
        if let url = sender.representedObject as? URL { NSWorkspace.shared.open(url) }
    }

    @objc private func hideIcon() {
        setEnabled(false)
    }
}
