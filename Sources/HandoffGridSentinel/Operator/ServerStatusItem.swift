import AppKit
import Combine
import SentinelCore

/// Menu bar shield that says at a glance whether a Sentinel server is up and
/// lets you start, stop and restart it: the server(s) this Mac runs under
/// launchd (LocalServiceControl) and the signed-in site (the event stream's
/// state). Blue when everything is up, orange when only part of the local
/// site is, red when it's stopped or the site is unreachable. Hidden via its
/// own menu or Settings.
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
    private var control: LocalServiceControl { LocalServiceControl.shared }
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

        control.objectWillChange
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in DispatchQueue.main.async { self?.render() } }
            .store(in: &sinks)
        session.objectWillChange
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in
                self?.watchRuntime()
                self?.render()
            }
            .store(in: &sinks)
        watchRuntime()

        let timer = Timer(timeInterval: 10, repeats: true) { _ in
            Task { @MainActor in await ServerStatusItem.shared.tick() }
        }
        timer.tolerance = 3
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
        guard control.busy == nil else { return }
        await control.refresh()
        if local.isInstalled { await local.refresh() }
        render()
    }

    // MARK: State

    private enum LocalState {
        case none                 // this Mac runs no Sentinel server
        case running(String)      // version
        case partial(Int, Int)    // loaded, total
        case starting             // loaded but not answering yet
        case stopped
    }

    private var localState: LocalState {
        let c = control
        guard c.services.isEmpty == false else { return .none }
        if c.loadedCount == 0 { return .stopped }
        if c.allLoaded == false { return .partial(c.loadedCount, c.services.count) }
        if let status = c.status { return .running(status.version) }
        return .starting
    }

    private var siteUnreachable: Bool {
        guard session.site != nil, let runtime = session.runtime else { return false }
        if case .waiting = runtime.streamState { return true }
        if case .stopped = runtime.streamState { return true }
        return false
    }

    private var siteConnected: Bool {
        guard session.site != nil, let runtime = session.runtime else { return false }
        if case .connected = runtime.streamState { return true }
        return false
    }

    private var tint: OperatorIcon.StatusTint {
        if control.busy != nil { return .partial }
        switch localState {
        case .stopped: return .stopped
        case .partial, .starting: return .partial
        case .running: return siteUnreachable ? .stopped : .running
        case .none:
            if siteUnreachable { return .stopped }
            return siteConnected ? .running : .idle
        }
    }

    private var localLine: String? {
        if let busy = control.busy {
            switch busy {
            case .starting: return "Sentinel server: starting…"
            case .stopping: return "Sentinel server: stopping…"
            case .restarting: return "Sentinel server: restarting…"
            }
        }
        let parts = control.services.count > 1 ? " · \(control.services.count) services" : ""
        switch localState {
        case .none: return nil
        case .running(let version): return "Sentinel server: running (\(version))\(parts)"
        case .partial(let up, let total): return "Sentinel server: partly running (\(up) of \(total))"
        case .starting: return "Sentinel server: starting…"
        case .stopped: return "Sentinel server: stopped"
        }
    }

    private func render() {
        guard let button = item?.button else { return }
        let image = OperatorIcon.statusGlyph(state: tint)
        image.accessibilityDescription = "Sentinel server status"
        button.image = image
        var detail: [String] = []
        if let line = localLine { detail.append(line.replacingOccurrences(of: "Sentinel server: ", with: "server ")) }
        if let site = session.site, siteConnected { detail.append("\(site.name) connected") }
        if let site = session.site, siteUnreachable { detail.append("\(site.name) unreachable") }
        button.toolTip = "Sentinel — " + (detail.isEmpty ? "no server yet" : detail.joined(separator: ", "))
        item?.menu = makeMenu()
    }

    private func makeMenu() -> NSMenu {
        let menu = NSMenu()
        func line(_ title: String) {
            let entry = NSMenuItem(title: title, action: nil, keyEquivalent: "")
            entry.isEnabled = false
            menu.addItem(entry)
        }
        func action(_ title: String, _ selector: Selector) {
            let entry = NSMenuItem(title: title, action: selector, keyEquivalent: "")
            entry.target = self
            entry.isEnabled = control.busy == nil
            menu.addItem(entry)
        }
        if let text = localLine { line(text) }
        if let site = session.site, let runtime = session.runtime {
            switch runtime.streamState {
            case .connected: line("\(site.name): connected")
            case .connecting, .idle: line("\(site.name): connecting…")
            case .waiting: line("\(site.name): reconnecting…")
            case .stopped: line("\(site.name): offline")
            }
        }
        if let error = control.lastError { line("⚠︎ \(error)") }
        if menu.items.isEmpty { line("No server yet") }

        if control.services.isEmpty == false {
            menu.addItem(.separator())
            if case .running = localState {} else { action("Start Server", #selector(startServer)) }
            if control.loadedCount > 0 {
                action("Stop Server", #selector(stopServer))
                action("Restart Server", #selector(restartServer))
            }
        }

        menu.addItem(.separator())
        let open = NSMenuItem(title: "Open Sentinel Operator", action: #selector(openOperator), keyEquivalent: "")
        open.target = self
        menu.addItem(open)
        if let url = control.dashboardURL {
            let dash = NSMenuItem(title: "Open Web Dashboard", action: #selector(openURL), keyEquivalent: "")
            dash.target = self
            dash.representedObject = url
            menu.addItem(dash)
        }
        if let log = control.logURL {
            let logs = NSMenuItem(title: "Show Server Logs", action: #selector(showLogs), keyEquivalent: "")
            logs.target = self
            logs.representedObject = log
            menu.addItem(logs)
        }
        menu.addItem(.separator())
        let hide = NSMenuItem(title: "Hide Status Icon", action: #selector(hideIcon), keyEquivalent: "")
        hide.target = self
        menu.addItem(hide)
        return menu
    }

    @objc private func startServer() { Task { await control.start(); render() } }
    @objc private func stopServer() { Task { await control.stop(); render() } }
    @objc private func restartServer() { Task { await control.restart(); render() } }

    @objc private func openOperator() {
        NSApp.activate(ignoringOtherApps: true)
        _ = NSApp.delegate?.applicationShouldHandleReopen?(NSApp, hasVisibleWindows: false)
    }

    @objc private func openURL(_ sender: NSMenuItem) {
        if let url = sender.representedObject as? URL { NSWorkspace.shared.open(url) }
    }

    @objc private func showLogs(_ sender: NSMenuItem) {
        guard let url = sender.representedObject as? URL else { return }
        if FileManager.default.fileExists(atPath: url.path) {
            NSWorkspace.shared.activateFileViewerSelecting([url])
        } else {
            NSWorkspace.shared.open(url.deletingLastPathComponent())
        }
    }

    @objc private func hideIcon() {
        setEnabled(false)
    }
}
