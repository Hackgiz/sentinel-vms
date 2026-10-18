import AppKit
import SwiftUI
import SentinelCore

/// Sentinel Operator for macOS: a client of a site's Management Server
/// (API v2). Recording, detection and the web Admin live in the Sentinel
/// Server, which this app can also install on the Mac it runs on.
@main
@MainActor
final class SentinelOperatorApplication: NSObject, NSApplicationDelegate {
    private static var retainedDelegate: SentinelOperatorApplication?
    private let context = OperatorContext.shared
    private let updateChecker = UpdateChecker()
    private var mainWindow: NSWindow?

    static func main() {
        CrashSentinel.install()

        // One copy at a time: surface a running one instead.
        let me = NSRunningApplication.current
        let bundleID = me.bundleIdentifier ?? "com.handoffgrid.sentinel"
        if let existing = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
            .first(where: { $0.processIdentifier != me.processIdentifier && !$0.isTerminated }) {
            existing.activate(options: [.activateAllWindows])
            return
        }

        let app = NSApplication.shared
        let delegate = SentinelOperatorApplication()
        retainedDelegate = delegate
        app.delegate = delegate
        app.setActivationPolicy(.regular)
        app.applicationIconImage = OperatorIcon.make()
        app.mainMenu = delegate.makeMainMenu()
        app.finishLaunching()
        delegate.openMainWindow()
        app.activate(ignoringOtherApps: true)
        app.run()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        SentinelLog.shared.info("Sentinel Operator launched — v\(DiagnosticsCollector.appVersion) (build \(DiagnosticsCollector.build)) on \(DiagnosticsCollector.osVersion)", category: "lifecycle")
        _ = CrashSentinel.consumeCrashFlag()
        // No daily update checks yet: the dl.sentvms.com feed describes the
        // 1.x single-machine app, not Sentinel Operator. Re-enable when the
        // feed carries an Operator release.
        ServerStatusItem.shared.installIfEnabled()
        Task {
            await context.session.restore()
            await LocalServerManager.shared.refresh()
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        openMainWindow()
        return true
    }

    func applicationWillTerminate(_ notification: Notification) {
        context.session.runtime?.stop()
    }

    private func openMainWindow() {
        if let mainWindow {
            mainWindow.makeKeyAndOrderFront(nil)
            return
        }
        let root = OperatorRootView()
            .environmentObject(context.session)
            .environmentObject(context.navigator)
            .environmentObject(context.appearance)
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1440, height: 860),
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "Sentinel Operator"
        window.titleVisibility = .hidden
        window.titlebarAppearsTransparent = true
        window.minSize = NSSize(width: 1100, height: 680)
        window.isReleasedWhenClosed = false
        window.setFrameAutosaveName("SentinelOperatorMain")
        window.contentView = NSHostingView(rootView: root)
        if window.frame.origin == .zero { window.center() }
        window.makeKeyAndOrderFront(nil)
        mainWindow = window
    }

    // MARK: Menu

    private func makeMainMenu() -> NSMenu {
        let mainMenu = NSMenu()

        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "About Sentinel Operator", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(item("Check for Updates…", #selector(checkForUpdates)))
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Sentinel Operator", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(withTitle: "Quit Sentinel Operator", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        add(appMenu, title: "Sentinel Operator", to: mainMenu)

        let fileMenu = NSMenu(title: "File")
        fileMenu.addItem(item("New Monitor Window", #selector(newMonitorWindow), key: "n"))
        fileMenu.addItem(.separator())
        fileMenu.addItem(withTitle: "Close Window", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        add(fileMenu, title: "File", to: mainMenu)

        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        add(editMenu, title: "Edit", to: mainMenu)

        let goMenu = NSMenu(title: "Go")
        for (index, title) in ["Live", "Playback", "Events", "Alarms"].enumerated() {
            let entry = item(title, #selector(goToSection(_:)), key: "\(index + 1)")
            entry.representedObject = title
            goMenu.addItem(entry)
        }
        add(goMenu, title: "Go", to: mainMenu)

        add(WallMenuController.shared.makeMenu(), title: "Wall", to: mainMenu) // Wave 10: video wall

        let windowMenu = NSMenu(title: "Window")
        let managementItem = item("Management", #selector(openManagement), key: "m")
        managementItem.keyEquivalentModifierMask = [.command, .shift]
        windowMenu.addItem(managementItem)
        windowMenu.addItem(.separator())
        windowMenu.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.addItem(withTitle: "Enter Full Screen", action: #selector(NSWindow.toggleFullScreen(_:)), keyEquivalent: "f")
            .keyEquivalentModifierMask = [.command, .control]
        add(windowMenu, title: "Window", to: mainMenu)
        NSApplication.shared.windowsMenu = windowMenu

        let helpMenu = NSMenu(title: "Help")
        helpMenu.addItem(item("Sentinel Help", #selector(openSupportSite), key: "?"))
        helpMenu.addItem(item("Privacy Policy", #selector(openPrivacyPolicy)))
        add(helpMenu, title: "Help", to: mainMenu)
        NSApplication.shared.helpMenu = helpMenu

        return mainMenu
    }

    private func item(_ title: String, _ action: Selector, key: String = "") -> NSMenuItem {
        let entry = NSMenuItem(title: title, action: action, keyEquivalent: key)
        entry.target = self
        return entry
    }

    private func add(_ menu: NSMenu, title: String, to main: NSMenu) {
        let holder = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        menu.title = title
        holder.submenu = menu
        main.addItem(holder)
    }

    @objc private func newMonitorWindow() {
        guard context.session.isSignedIn else { return }
        OperatorWindows.shared.openMonitorWindow(viewID: nil)
    }

    @objc private func openManagement() {
        ManagementWindowController.shared.open()
    }

    @objc func validateMenuItem(_ menuItem: NSMenuItem) -> Bool {
        if menuItem.action == #selector(openManagement) {
            return context.session.isSignedIn && ManagementCatalog.hasManagementAccess(context.session.can)
        }
        return true
    }

    @objc private func goToSection(_ sender: NSMenuItem) {
        guard let title = sender.representedObject as? String else { return }
        openMainWindow()
        switch title {
        case "Live": context.navigator.show(.live)
        case "Playback": context.navigator.show(.playback)
        case "Events": context.navigator.section = .events
        default: context.navigator.section = .alarms
        }
    }

    @objc private func checkForUpdates() {
        let checker = updateChecker
        Task { @MainActor in
            await checker.checkNow()
            let alert = NSAlert()
            if checker.updateAvailable, let latest = checker.latestVersion {
                alert.messageText = "Sentinel Operator \(latest) is available"
                alert.informativeText = "You have \(checker.currentVersion)." + (checker.notes.isEmpty ? "" : "\n\n\(checker.notes)")
                alert.addButton(withTitle: "Download")
                alert.addButton(withTitle: "Later")
                if alert.runModal() == .alertFirstButtonReturn { NSWorkspace.shared.open(checker.downloadURL) }
            } else if let error = checker.lastError {
                alert.messageText = "Couldn't check for updates"
                alert.informativeText = error
                alert.runModal()
            } else {
                alert.messageText = "You're up to date"
                alert.informativeText = "Sentinel Operator \(checker.currentVersion) is newer than the released Sentinel. Update checks cover Operator releases once 2.0 ships."
                alert.runModal()
            }
        }
    }

    @objc private func openSupportSite() {
        if let url = URL(string: "https://sentvms.com/support") { NSWorkspace.shared.open(url) }
    }

    @objc private func openPrivacyPolicy() {
        if let url = URL(string: "https://sentvms.com/privacy") { NSWorkspace.shared.open(url) }
    }
}

/// Signed out: the sign-in screen. Signed in: the Operator shell.
struct OperatorRootView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var navigator: OperatorNavigator
    @EnvironmentObject private var appearance: OperatorAppearance
    @ObservedObject private var importer = MacImporter.shared
    @State private var showsLocalServer = false
    @State private var showsImportOffer = false

    var body: some View {
        Group {
            switch session.phase {
            case .signedIn:
                if let runtime = session.runtime {
                    OperatorShell()
                        .operatorEnvironment(session: session, runtime: runtime, navigator: navigator, appearance: appearance)
                        .onAppear { offerImportIfNeeded() }
                }
            case .restoring:
                VStack(spacing: 12) {
                    HandoffGridMark(size: 48)
                    ProgressView()
                    Text("Connecting to \(session.site?.name ?? "the site")…").foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(SentinelTheme.background)
            case .signedOut:
                SignInView(sites: session.sites) { showsLocalServer = true }
            }
        }
        .frame(minWidth: 1000, minHeight: 640)
        .sheet(isPresented: $showsLocalServer) {
            VStack(spacing: 0) {
                LocalServerView()
                Divider()
                HStack { Spacer(); Button("Close") { showsLocalServer = false }.keyboardShortcut(.cancelAction) }.padding(12)
            }
            .frame(width: 680, height: 560)
            .environmentObject(session)
        }
        .sheet(isPresented: $showsImportOffer) {
            ImportOfferSheet {
                showsImportOffer = false
                navigator.section = .importer
            } later: {
                importer.postponed = true
                showsImportOffer = false
            }
        }
        .onChange(of: session.phase) { phase in
            if phase == .signedIn { showsLocalServer = false }
        }
        .onChange(of: session.prefill) { prefill in
            if prefill != nil { showsLocalServer = false }
        }
    }

    private func offerImportIfNeeded() {
        guard session.isAdmin, importer.shouldOffer else { return }
        showsImportOffer = true
    }
}

private struct ImportOfferSheet: View {
    let start: () -> Void
    let later: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 12) {
                Image(systemName: "square.and.arrow.down.on.square.fill").font(.largeTitle).foregroundStyle(SentinelTheme.accent)
                Text("Bring over your Sentinel 1.x setup?").font(.headline)
            }
            Text("This Mac has cameras, users, rules and evidence from the earlier version of Sentinel, which recorded by itself. Sentinel Operator now works with a Sentinel Server: import them into this site once and keep your recordings playable.")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Later", action: later)
                Button("Review Import…", action: start).buttonStyle(.borderedProminent)
            }
        }
        .padding(22)
        .frame(width: 460)
    }
}

/// The app icon, drawn (shield with a 2×2 grid), as before.
enum OperatorIcon {
    static func make() -> NSImage {
        let size = NSSize(width: 512, height: 512)
        let image = NSImage(size: size)
        image.lockFocus()
        defer { image.unlockFocus() }

        let bounds = NSRect(origin: .zero, size: size)
        NSColor(red: 0.055, green: 0.060, blue: 0.068, alpha: 1).setFill()
        NSBezierPath(roundedRect: bounds, xRadius: 112, yRadius: 112).fill()

        drawShield(in: bounds.insetBy(dx: 72, dy: 56),
                   tint: NSColor(red: 0.23, green: 0.64, blue: 0.92, alpha: 1),
                   grid: .white)
        return image
    }

    /// The shield glyph alone, app-colored, for the menu bar status item:
    /// full color while a server is up, dimmed gray while it isn't.
    static func statusGlyph(size: CGFloat = 18, dimmed: Bool) -> NSImage {
        let image = NSImage(size: NSSize(width: size, height: size), flipped: false) { bounds in
            let tint = dimmed
                ? NSColor(white: 0.5, alpha: 0.6)
                : NSColor(red: 0.23, green: 0.64, blue: 0.92, alpha: 1)
            let grid = dimmed ? NSColor(white: 0.5, alpha: 0.7) : NSColor.white
            drawShield(in: bounds.insetBy(dx: bounds.width * 0.06, dy: bounds.height * 0.03), tint: tint, grid: grid)
            return true
        }
        image.isTemplate = false
        return image
    }

    /// The shield outline and 2×2 camera grid, scaled to `rect`.
    private static func drawShield(in rect: NSRect, tint: NSColor, grid gridColor: NSColor) {
        let scale = rect.width / 368 // the 512 tile's shield rect is 368 wide
        let cr = rect.width * 0.15
        let shield = NSBezierPath()
        shield.move(to: NSPoint(x: rect.minX + cr, y: rect.maxY))
        shield.line(to: NSPoint(x: rect.maxX - cr, y: rect.maxY))
        shield.curve(to: NSPoint(x: rect.maxX, y: rect.maxY - cr),
                     controlPoint1: NSPoint(x: rect.maxX, y: rect.maxY), controlPoint2: NSPoint(x: rect.maxX, y: rect.maxY))
        shield.curve(to: NSPoint(x: rect.midX, y: rect.minY),
                     controlPoint1: NSPoint(x: rect.maxX, y: rect.midY - rect.height * 0.06),
                     controlPoint2: NSPoint(x: rect.midX + rect.width * 0.27, y: rect.minY + rect.height * 0.07))
        shield.curve(to: NSPoint(x: rect.minX, y: rect.maxY - cr),
                     controlPoint1: NSPoint(x: rect.midX - rect.width * 0.27, y: rect.minY + rect.height * 0.07),
                     controlPoint2: NSPoint(x: rect.minX, y: rect.midY - rect.height * 0.06))
        shield.curve(to: NSPoint(x: rect.minX + cr, y: rect.maxY),
                     controlPoint1: NSPoint(x: rect.minX, y: rect.maxY), controlPoint2: NSPoint(x: rect.minX, y: rect.maxY))
        shield.close()
        tint.withAlphaComponent(0.22).setFill()
        shield.fill()
        shield.lineWidth = 20 * scale
        tint.setStroke()
        shield.stroke()

        let inset = rect.width * 0.2
        let bottom = rect.height * 0.34
        let grid = NSRect(x: rect.minX + inset, y: rect.minY + bottom,
                          width: rect.width - inset * 2, height: rect.height - bottom - rect.height * 0.12)
        let gap: CGFloat = 18 * scale
        let w = (grid.width - gap) / 2
        let h = (grid.height - gap) / 2
        for row in 0..<2 {
            for col in 0..<2 {
                gridColor.withAlphaComponent(row == col ? 0.92 : 0.36).setFill()
                NSBezierPath(roundedRect: NSRect(x: grid.minX + CGFloat(col) * (w + gap), y: grid.minY + CGFloat(row) * (h + gap),
                                                 width: w, height: h), xRadius: 10 * scale, yRadius: 10 * scale).fill()
            }
        }
    }
}
