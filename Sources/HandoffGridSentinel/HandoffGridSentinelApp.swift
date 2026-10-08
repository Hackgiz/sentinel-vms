import SwiftUI
import AppKit
import Darwin
import SentinelCore
import SentinelMediaServer

@main
@MainActor
final class HandoffGridSentinelApplication: NSObject, NSApplicationDelegate {
    private static var retainedDelegate: HandoffGridSentinelApplication?
    private let dependencies = SentinelAppDependencies.shared
    private var mainWindow: NSWindow?

    /// Opts the whole process out of App Nap for its entire lifetime. Without
    /// this, macOS throttles timers and background threads once Sentinel's
    /// window is hidden/occluded — which would starve the motion monitor and
    /// recording loops. `.userInitiatedAllowingIdleSystemSleep` disables App Nap
    /// (the work is treated as high-priority) while still letting the system
    /// idle-sleep when appropriate, so it does NOT override PowerManager's
    /// conditional keep-awake assertion.
    private var backgroundActivity: NSObjectProtocol?

    static func main() {
        // Install crash capture before anything else so an early failure still
        // leaves a marker for the next launch's Diagnostics prompt.
        CrashSentinel.install()

        // Single-instance guard: if another copy of Sentinel VMS is already
        // running (e.g. macOS relaunched it at login while a different .app
        // copy is also open), surface that instance and exit instead of
        // spinning up a duplicate window + media engine.
        let me = NSRunningApplication.current
        let bundleID = me.bundleIdentifier ?? "com.handoffgrid.sentinel"
        let others = NSRunningApplication
            .runningApplications(withBundleIdentifier: bundleID)
            .filter { $0.processIdentifier != me.processIdentifier && !$0.isTerminated }
        if let existing = others.first {
            existing.activate(options: [.activateAllWindows])
            return
        }

        let app = NSApplication.shared
        let delegate = HandoffGridSentinelApplication()

        retainedDelegate = delegate
        app.delegate = delegate
        app.setActivationPolicy(.regular)
        app.applicationIconImage = delegate.makeApplicationIcon()
        app.mainMenu = delegate.makeMainMenu()
        app.finishLaunching()
        delegate.openMainWindow()
        app.activate(ignoringOtherApps: true)
        app.run()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        SentinelLog.shared.info("Sentinel VMS launched — v\(DiagnosticsCollector.appVersion) (build \(DiagnosticsCollector.build)) on \(DiagnosticsCollector.osVersion)", category: "lifecycle")
        AutoStartManager.repairIfMoved()
        if CrashSentinel.consumeCrashFlag() {
            SentinelLog.shared.warning("Previous session ended unexpectedly. Send a bug report from Health → Diagnostics if this keeps happening.", category: "crash")
        }

        backgroundActivity = ProcessInfo.processInfo.beginActivity(
            options: .userInitiatedAllowingIdleSystemSleep,
            reason: "Sentinel VMS records cameras continuously and must not be throttled by App Nap"
        )
        DispatchQueue.main.async { [weak self] in
            self?.openMainWindow()
            NSApplication.shared.activate(ignoringOtherApps: true)
        }
    }

    func applicationDidBecomeActive(_ notification: Notification) {
        if NSApplication.shared.windows.isEmpty {
            openMainWindow()
        }
        let media = dependencies.mediaIngestStore
        let needsStartup = dependencies.mediaMTXStore.isAvailable
            ? dependencies.mediaMTXStore.isRunning == false
            : media.liveStreams.isEmpty && media.activeRecordings.isEmpty
        if needsStartup {
            scheduleMediaStartup()
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        openMainWindow()
        NSApplication.shared.activate(ignoringOtherApps: true)
        return true
    }

    func applicationWillTerminate(_ notification: Notification) {
        dependencies.aiDetectionStore.stopAll()
        dependencies.mediaIngestStore.stopAll()
        dependencies.mediaEngineStore.stopExternalPreviews()
        dependencies.mediaMTXStore.stop()
        dependencies.cloudflaredTunnel.stop()
        dependencies.cameraCredentialStore.clearSessionAccess()
        if let backgroundActivity {
            ProcessInfo.processInfo.endActivity(backgroundActivity)
            self.backgroundActivity = nil
        }
    }

    // Route `sentinel://…` URLs (Shortcuts, AppleScript "open location",
    // command-clicked links) through the URLSchemeRouter so external tooling
    // can drive the app without a bespoke IPC layer.
    func application(_ application: NSApplication, open urls: [URL]) {
        URLSchemeRouter.handle(
            urls,
            commandCenter: dependencies.commandCenter,
            cameraStore: dependencies.cameraStore,
            licenseStore: dependencies.licenseStore
        )
    }

    private func openMainWindow() {
        if let mainWindow {
            mainWindow.makeKeyAndOrderFront(nil)
            mainWindow.orderFrontRegardless()
            return
        }

        let rootView = SentinelRootView()
            .sentinelEnvironment(dependencies)

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1440, height: 860),
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "Sentinel VMS"
        window.titleVisibility = .hidden
        window.titlebarAppearsTransparent = true
        window.minSize = NSSize(width: 1180, height: 720)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: rootView)
        window.center()
        window.makeKeyAndOrderFront(nil)
        window.orderFrontRegardless()

        mainWindow = window
    }

    private func scheduleMediaStartup() {
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.2) { [weak self] in
            guard let self else {
                return
            }

            Task {
                await self.startConfiguredMedia()
            }
        }
    }

    private func startConfiguredMedia() async {
        await dependencies.startConfiguredMedia()
    }

    private func makeMainMenu() -> NSMenu {
        let mainMenu = NSMenu()

        let appMenuItem = NSMenuItem()
        let appMenu = NSMenu()
        appMenu.addItem(
            withTitle: "Quit Sentinel VMS",
            action: #selector(NSApplication.terminate(_:)),
            keyEquivalent: "q"
        )
        appMenuItem.submenu = appMenu
        mainMenu.addItem(appMenuItem)

        let fileMenuItem = NSMenuItem()
        let fileMenu = NSMenu(title: "File")
        let addCameraItem = NSMenuItem(
            title: "Add Camera",
            action: #selector(addCamera),
            keyEquivalent: "n"
        )
        addCameraItem.target = self
        fileMenu.addItem(addCameraItem)

        let createIncidentItem = NSMenuItem(
            title: "Create Incident",
            action: #selector(createIncident),
            keyEquivalent: "i"
        )
        createIncidentItem.keyEquivalentModifierMask = [.command, .shift]
        createIncidentItem.target = self
        fileMenu.addItem(createIncidentItem)
        fileMenuItem.submenu = fileMenu
        mainMenu.addItem(fileMenuItem)

        // Help menu — macOS also adds its search field here automatically.
        let helpMenuItem = NSMenuItem()
        let helpMenu = NSMenu(title: "Help")
        let feedbackItem = NSMenuItem(title: "Send Feedback…", action: #selector(sendFeedback), keyEquivalent: "")
        feedbackItem.target = self
        helpMenu.addItem(feedbackItem)
        helpMenu.addItem(.separator())
        let supportItem = NSMenuItem(title: "Sentinel Help", action: #selector(openSupportSite), keyEquivalent: "?")
        supportItem.target = self
        helpMenu.addItem(supportItem)
        let privacyItem = NSMenuItem(title: "Privacy Policy", action: #selector(openPrivacyPolicy), keyEquivalent: "")
        privacyItem.target = self
        helpMenu.addItem(privacyItem)
        helpMenuItem.submenu = helpMenu
        mainMenu.addItem(helpMenuItem)
        NSApplication.shared.helpMenu = helpMenu

        return mainMenu
    }

    // MARK: - Help menu

    private var feedbackWindow: NSWindow?

    @objc private func sendFeedback() {
        if let window = feedbackWindow {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }
        let form = FeedbackForm(cameraCount: dependencies.cameraStore.cameras.count, initialKind: .idea) { [weak self] in
            self?.feedbackWindow?.close()
        }
        let window = NSWindow(contentViewController: NSHostingController(rootView: form))
        window.title = "Send Feedback"
        window.styleMask = [.titled, .closable]
        window.isReleasedWhenClosed = false
        window.center()
        NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.feedbackWindow = nil }
        }
        feedbackWindow = window
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc private func openSupportSite() {
        if let url = URL(string: "https://sentvms.com/support") { NSWorkspace.shared.open(url) }
    }

    @objc private func openPrivacyPolicy() {
        if let url = URL(string: "https://sentvms.com/privacy") { NSWorkspace.shared.open(url) }
    }

    private func makeApplicationIcon() -> NSImage {
        let size = NSSize(width: 512, height: 512)
        let image = NSImage(size: size)

        image.lockFocus()
        defer { image.unlockFocus() }

        let bounds = NSRect(origin: .zero, size: size)
        NSColor(red: 0.055, green: 0.060, blue: 0.068, alpha: 1).setFill()
        NSBezierPath(roundedRect: bounds, xRadius: 112, yRadius: 112).fill()

        let shieldRect = bounds.insetBy(dx: 72, dy: 56)
        let shield = makeIconShieldPath(in: shieldRect)

        NSColor(red: 0.23, green: 0.64, blue: 0.92, alpha: 0.22).setFill()
        shield.fill()

        shield.lineWidth = 20
        NSColor(red: 0.23, green: 0.64, blue: 0.92, alpha: 1).setStroke()
        shield.stroke()

        let gridInset = shieldRect.width * 0.2
        let gridBottom = shieldRect.height * 0.34
        let gridRect = NSRect(
            x: shieldRect.minX + gridInset,
            y: shieldRect.minY + gridBottom,
            width: shieldRect.width - gridInset * 2,
            height: shieldRect.height - gridBottom - shieldRect.height * 0.12
        )

        let gap: CGFloat = 18
        let cellW = (gridRect.width - gap) / 2
        let cellH = (gridRect.height - gap) / 2

        for row in 0..<2 {
            for col in 0..<2 {
                let isStrong = row == col
                let cellRect = NSRect(
                    x: gridRect.minX + CGFloat(col) * (cellW + gap),
                    y: gridRect.minY + CGFloat(row) * (cellH + gap),
                    width: cellW,
                    height: cellH
                )
                NSColor.white.withAlphaComponent(isStrong ? 0.92 : 0.36).setFill()
                NSBezierPath(roundedRect: cellRect, xRadius: 10, yRadius: 10).fill()
            }
        }

        return image
    }

    private func makeIconShieldPath(in rect: NSRect) -> NSBezierPath {
        let cr: CGFloat = rect.width * 0.15
        let path = NSBezierPath()

        path.move(to: NSPoint(x: rect.minX + cr, y: rect.maxY))
        path.line(to: NSPoint(x: rect.maxX - cr, y: rect.maxY))
        path.curve(
            to: NSPoint(x: rect.maxX, y: rect.maxY - cr),
            controlPoint1: NSPoint(x: rect.maxX, y: rect.maxY),
            controlPoint2: NSPoint(x: rect.maxX, y: rect.maxY)
        )
        path.curve(
            to: NSPoint(x: rect.midX, y: rect.minY),
            controlPoint1: NSPoint(x: rect.maxX, y: rect.midY - rect.height * 0.06),
            controlPoint2: NSPoint(x: rect.midX + rect.width * 0.27, y: rect.minY + rect.height * 0.07)
        )
        path.curve(
            to: NSPoint(x: rect.minX, y: rect.maxY - cr),
            controlPoint1: NSPoint(x: rect.midX - rect.width * 0.27, y: rect.minY + rect.height * 0.07),
            controlPoint2: NSPoint(x: rect.minX, y: rect.midY - rect.height * 0.06)
        )
        path.curve(
            to: NSPoint(x: rect.minX + cr, y: rect.maxY),
            controlPoint1: NSPoint(x: rect.minX, y: rect.maxY),
            controlPoint2: NSPoint(x: rect.minX, y: rect.maxY)
        )
        path.close()
        return path
    }

    @objc private func addCamera() {
        openMainWindow()
        dependencies.commandCenter.requestAddCamera()
    }

    @objc private func createIncident() {
        openMainWindow()
        dependencies.commandCenter.requestCreateIncident()
    }
}

@MainActor
final class SentinelAppDependencies: ObservableObject {
    static let shared = SentinelAppDependencies()

    let cameraStore = CameraStore()
    let licenseStore = LicenseStore()
    let mediaEngineStore = MediaEngineStore()
    let mediaIngestStore = MediaIngestStore()
    let mediaMTXStore = MediaMTXStore()
    let aiDetectionStore = AIDetectionStore()
    let onvifDiscoveryStore = ONVIFDiscoveryStore()
    let commandCenter = SentinelCommandCenter()
    let caseworkStore = CaseworkStore()
    let userDirectoryStore = UserDirectoryStore()
    let operatorSessionStore = OperatorSessionStore()
    let floorplanStore = FloorplanStore()
    let workflowStore = WorkflowStore()
    let appearanceStore = AppearanceStore()
    let cameraCredentialStore = CameraCredentialStore()
    let evidenceExporter = EvidenceExporter()
    let pairingTokenStore = PairingTokenStore()
    let httpServer = SentinelHTTPServer()
    let cloudflaredTunnel = CloudflareTunnelManager()
    let powerManager = PowerManager()
    let pushService = APNsPushService()

    /// Re-entrancy guard: startConfiguredMedia is async and fires from several
    /// onChange/.task hooks that can overlap. Without this, concurrent runs
    /// raced on MediaMTX start/stop and blacked out the live tiles.
    private var isConfiguringMedia = false

    private init() {
        // One-shot background loops that should run for the lifetime of the
        // process: frame-arrival watchdog (kills stalled GStreamer pipelines so
        // reconnect cascades fire) and disk pressure guardian (halts recording
        // before the volume fills).
        mediaIngestStore.startFrameWatchdog()
        mediaIngestStore.startDiskGuardian()

        // Push operator login/logout events into the audit log. Capture the
        // workflow store weakly to avoid retaining it through the session store
        // closure (both live for the process lifetime so this is defensive).
        operatorSessionStore.auditRecorder = { [weak workflowStore] user, action, detail in
            workflowStore?.recordAudit(area: "Operator", action: action, detail: detail, user: user)
        }
        workflowStore.operatorNameProvider = { [weak operatorSessionStore] in
            guard let operatorSessionStore else { return nil }
            return operatorSessionStore.currentOperator?.name ?? (operatorSessionStore.guestMode ? "Guest" : nil)
        }
        caseworkStore.auditRecorder = { [weak workflowStore] area, action, detail, user in
            workflowStore?.recordAudit(area: area, action: action, detail: detail, user: user)
        }
        cameraStore.auditRecorder = { [weak workflowStore] action, detail in
            workflowStore?.recordAudit(area: "Cameras", action: action, detail: detail)
        }
        mediaIngestStore.startArchiveScheduler { [weak workflowStore] result in
            guard result.moved > 0 || result.pruned > 0 || result.failed > 0 else { return }
            workflowStore?.recordAudit(area: "Storage", action: "Archive pass", detail: result.summary, user: "System")
        }

        // Bridge MediaMTX's view of each camera's source into the ingest
        // store so the Live View badge reflects whether MediaMTX is actually
        // receiving video — not just whether a recording session was
        // registered at startup. The closure runs on the main actor.
        let ingestStore = mediaIngestStore
        mediaMTXStore.onStreamStateChange = { id, state in
            switch state {
            case .proxying:
                ingestStore.recordRemoteSourceState(cameraID: id, isLive: true)
            case .noSignal:
                ingestStore.recordRemoteSourceState(
                    cameraID: id,
                    isLive: false,
                    detail: "MediaMTX is not receiving video from this camera."
                )
            case .unknown:
                break
            }
        }

        // Wire Ring-style AI descriptions: when the on-device detector sees a
        // person, it hands the frame to Claude vision via the signed-in account.
        // Captured weakly; returns nil when signed out so detection is unaffected.
        let licensing = licenseStore
        aiDetectionStore.sceneDescriber = { [weak licensing] jpeg in
            await licensing?.describeScene(jpeg: jpeg)
        }
        aiDetectionStore.describeEnabled = UserDefaults.standard.object(forKey: AIDescription.defaultsKey) as? Bool ?? true

        // Push AI alerts to paired iPhones. The describer's Ring-style line is
        // the push body; the camera name is the title. Sent only to devices
        // that registered an APNs token, and only when an Auth Key is present.
        let pushing = pushService
        let cameras = cameraStore
        let devices = pairingTokenStore
        aiDetectionStore.onSceneDescription = { [weak pushing, weak cameras, weak devices] cameraID, text in
            guard let pushing, pushing.isConfigured else { return }
            let cameraName = cameras?.cameras.first(where: { $0.id == cameraID })?.name ?? "Camera"
            let tokens = (devices?.pairedDevices ?? []).compactMap { $0.apnsPushToken }
            guard tokens.isEmpty == false else { return }
            Task { await pushing.sendAlert(title: cameraName, body: text, cameraID: cameraID, deviceTokens: tokens) }
        }

        // Recording-protection watchdog: if a camera's low-res sub starves its
        // high-res recording, MediaMTX auto-disables the sub and asks us to
        // rewrite the config (dropping the sub path) so recording recovers.
        let mtx = mediaMTXStore
        let cams = cameraStore
        let ingest = mediaIngestStore
        let creds = cameraCredentialStore
        mediaMTXStore.onSubAutoDisabled = { [weak mtx, weak cams, weak ingest, weak creds] _ in
            guard let mtx, let cams, let ingest, let creds, mtx.isRunning else { return }
            mtx.reload(cameras: cams.cameras, recordingRootURL: ingest.recordingRootURL, credentials: creds)
        }

        // Disk-pressure protection: the ingest guardian can stop its own
        // GStreamer recorders, but MediaMTX-managed recording has no Process it
        // can kill — so it asks us to suspend/resume MediaMTX recording here.
        mediaIngestStore.onDiskPressureChange = { [weak mtx, weak cams, weak ingest, weak creds] shouldHalt in
            guard let mtx, let cams, let ingest, let creds, mtx.isRunning else { return }
            mtx.setRecordingSuspended(
                shouldHalt,
                cameras: cams.cameras,
                recordingRootURL: ingest.recordingRootURL,
                credentials: creds
            )
        }

        // Companion-app HTTP API. Starts immediately so the iOS app can
        // pair before any cameras are configured.
        httpServer.start(dataSource: SentinelHTTPBridge(dependencies: self))

        // Remote access (Cloudflare tunnel) is a paid add-on ($3.99/mo): off-LAN
        // / 5G viewing only starts when the account is entitled. Paired phones
        // learn the rotating URL via the X-Sentinel-Remote-URL header whenever
        // they're on home Wi-Fi. The cached entitlement loads synchronously, so
        // a previously-subscribed user keeps remote access offline within the
        // grace window; a fresh fetch re-evaluates shortly after launch.
        if RemoteAccessPreference.autoStart, licenseStore.remoteActive, cloudflaredTunnel.isAvailable {
            cloudflaredTunnel.start()
        }
    }
}

/// Persisted on/off switch for auto-starting remote access at launch.
enum RemoteAccessPreference {
    static let defaultsKey = "handoffgrid.remoteAccess.autoStart"
    static var autoStart: Bool {
        get { UserDefaults.standard.object(forKey: defaultsKey) as? Bool ?? true }
        set { UserDefaults.standard.set(newValue, forKey: defaultsKey) }
    }
}

/// Persisted on/off switch for AI scene descriptions (Settings toggle).
enum AIDescription {
    static let defaultsKey = "handoffgrid.ai.describe"
    static var isEnabled: Bool {
        get { UserDefaults.standard.object(forKey: defaultsKey) as? Bool ?? true }
        set { UserDefaults.standard.set(newValue, forKey: defaultsKey) }
    }
}

// Bridges main-actor stores into the Sendable, non-isolated protocol the
// HTTP server uses. Each call hops to the main actor to read store state,
// serializes a snapshot, and returns it to the network worker.
final class SentinelHTTPBridge: SentinelHTTPDataSource, @unchecked Sendable {
    weak var dependencies: SentinelAppDependencies?

    init(dependencies: SentinelAppDependencies) {
        self.dependencies = dependencies
    }

    func camerasResponse() async -> Data {
        await MainActor.run {
            guard let deps = self.dependencies else { return Data("[]".utf8) }
            let payload: [[String: Any]] = deps.cameraStore.cameras.map { camera in
                var entry: [String: Any] = [
                    "id": camera.id.uuidString,
                    "name": camera.name,
                    "location": camera.location,
                    "ipAddress": camera.ipAddress,
                    "status": deps.mediaIngestStore.effectiveStatus(for: camera).rawValue,
                    "isRecording": deps.mediaIngestStore.isRecording(cameraID: camera.id),
                    "resolution": camera.resolution,
                    "fps": camera.fps,
                    "supportsPTZ": camera.supportsPTZ
                ]
                // Always return a proxy path relative to the API server so the
                // same URL works on LAN and over the Cloudflare tunnel. The iOS
                // client resolves it against whichever server URL it's currently
                // using (local or remote).
                if deps.mediaMTXStore.isRunning {
                    entry["hlsURL"] = "/hls/\(camera.id.uuidString)/index.m3u8"
                }
                return entry
            }
            return (try? JSONSerialization.data(withJSONObject: payload)) ?? Data("[]".utf8)
        }
    }

    func cameraDetailResponse(cameraID: UUID) async -> Data? {
        await MainActor.run {
            guard let deps = self.dependencies,
                  let camera = deps.cameraStore.cameras.first(where: { $0.id == cameraID }) else { return nil }
            let snapshot = deps.mediaIngestStore.healthSnapshot(for: camera)
            // Build the payload with non-nil keys only. Inserting a boxed
            // Optional.none (via `as Any`) makes JSONSerialization reject the
            // whole object, which previously 404'd this endpoint whenever a
            // camera had no error / no frame yet (i.e. the common case).
            var payload: [String: Any] = [
                "id": camera.id.uuidString,
                "name": camera.name,
                "location": camera.location,
                "status": snapshot.status.rawValue,
                "isRecording": snapshot.isRecording,
                "estimatedFPS": snapshot.estimatedFPS,
                "segmentCount": snapshot.segmentCount,
                "eventCount": snapshot.eventCount,
                "supportsPTZ": camera.supportsPTZ
            ]
            if let lastFrameAt = snapshot.lastFrameAt {
                payload["lastFrameAt"] = lastFrameAt.timeIntervalSince1970
            }
            if let lastError = snapshot.lastError {
                payload["lastError"] = lastError
            }
            if deps.mediaMTXStore.isRunning {
                payload["hlsURL"] = "/hls/\(camera.id.uuidString)/index.m3u8"
            }
            return try? JSONSerialization.data(withJSONObject: payload)
        }
    }

    func alertsResponse() async -> Data {
        await MainActor.run {
            guard let deps = self.dependencies else { return Data("[]".utf8) }
            let payload: [[String: Any]] = deps.caseworkStore.activeAlerts.map { self.alertPayload($0, deps: deps) }
            return (try? JSONSerialization.data(withJSONObject: payload)) ?? Data("[]".utf8)
        }
    }

    /// One alarm for the phone. New fields are additive so older app builds,
    /// which decode only the original keys, keep working.
    @MainActor
    private func alertPayload(_ alert: AlertEvent, deps: SentinelAppDependencies) -> [String: Any] {
        var entry: [String: Any] = [
            "id": alert.id.uuidString,
            "title": alert.title,
            "detail": alert.detail,
            "severity": alert.severity.rawValue,
            "state": alert.state.rawValue,
            "createdAt": alert.createdAt.timeIntervalSince1970,
            "lastEventAt": alert.lastEventAt.timeIntervalSince1970,
            "kind": alert.kind.rawValue,
            "owner": alert.owner,
            "eventCount": alert.eventCount,
            "source": alert.source,
            "hasClip": alert.linkedClipPath.map { FileManager.default.fileExists(atPath: $0) } ?? false,
            "instructions": deps.workflowStore.instructions(for: alert).map { ["rule": $0.rule, "text": $0.text] }
        ]
        if let cameraID = alert.cameraID { entry["cameraID"] = cameraID.uuidString }
        if let cameraName = alert.cameraName { entry["cameraName"] = cameraName }
        if let clip = deps.caseworkStore.evidenceClips.first(where: { $0.alertID == alert.id }) {
            entry["evidence"] = evidencePayload(clip)
        }
        return entry
    }

    private func evidencePayload(_ clip: EvidenceClip) -> [String: Any] {
        var entry: [String: Any] = [
            "id": clip.id.uuidString,
            "caseID": clip.caseID,
            "title": clip.title,
            "camera": clip.camera,
            "range": clip.range,
            "status": clip.status,
            "isLocked": clip.isLocked
        ]
        if let lockedAt = clip.lockedAt { entry["lockedAt"] = lockedAt.timeIntervalSince1970 }
        if let hash = clip.sha256Hash { entry["sha256"] = hash }
        if let by = clip.exportedBy { entry["lockedBy"] = by }
        if let alertID = clip.alertID { entry["alertID"] = alertID.uuidString }
        return entry
    }

    @MainActor
    private func deviceLabel(_ deviceID: UUID, deps: SentinelAppDependencies) -> String {
        let name = deps.pairingTokenStore.pairedDevices.first { $0.id == deviceID }?.name ?? "Paired device"
        return "\(name) (iPhone)"
    }

    func acknowledgeAlert(alertID: UUID, deviceID: UUID) async -> SentinelRemoteActionResult {
        await MainActor.run {
            guard let deps = self.dependencies,
                  let alert = deps.caseworkStore.alerts.first(where: { $0.id == alertID }) else {
                return .notFound("Alarm not found — it may have been cleared on the Mac.")
            }
            guard alert.isOpen else {
                return .conflict("This alarm was already \(alert.state.rawValue.lowercased()) on the Mac.")
            }
            // Only move New → Acknowledged; an alarm someone is already
            // investigating isn't downgraded. Either way, return its state.
            if alert.state == .new || alert.state == .snoozed {
                let who = self.deviceLabel(deviceID, deps: deps)
                deps.caseworkStore.setAlertState(alert, to: .acknowledged, owner: who, note: "Acknowledged from \(who)", actor: who)
                deps.workflowStore.syncNotifications(from: deps.caseworkStore.alerts)
            }
            let updated = deps.caseworkStore.alerts.first { $0.id == alertID } ?? alert
            let body = (try? JSONSerialization.data(withJSONObject: self.alertPayload(updated, deps: deps))) ?? Data("{}".utf8)
            return .ok(body)
        }
    }

    func lockEvidence(alertID: UUID, deviceID: UUID) async -> SentinelRemoteActionResult {
        await lockEvidenceOnMain(alertID: alertID, deviceID: deviceID)
    }

    @MainActor
    private func lockEvidenceOnMain(alertID: UUID, deviceID: UUID) async -> SentinelRemoteActionResult {
        guard let deps = dependencies,
              let alert = deps.caseworkStore.alerts.first(where: { $0.id == alertID }) else {
            return .notFound("Alarm not found — it may have been cleared on the Mac.")
        }
        let who = deviceLabel(deviceID, deps: deps)

        // Idempotent: a second tap (or a retry over a flaky connection) returns
        // the clip already locked for this alarm instead of making another.
        var clip = deps.caseworkStore.evidenceClips.first { $0.alertID == alertID }
        if clip == nil {
            guard let path = alert.linkedClipPath,
                  let segment = deps.mediaIngestStore.recordingSegments.first(where: { $0.fileURL.path == path }) else {
                return .conflict("No recording is linked to this alarm yet, or it has already been cleaned up.")
            }
            clip = deps.caseworkStore.createEvidencePackage(
                from: segment,
                cameraName: alert.cameraName ?? alert.source,
                operator: who,
                alertID: alertID,
                actor: who
            )
        }
        guard let clip else { return .conflict("Couldn't create the evidence package.") }

        if clip.isLocked == false {
            if let error = await deps.caseworkStore.lockClip(clip, operator: who) {
                return .conflict(error)
            }
            deps.workflowStore.recordAudit(area: "Evidence", action: "Locked evidence", detail: "\(clip.caseID) for alarm \(alert.title)", user: who)
            deps.caseworkStore.addResponseNote(to: alert, note: "Evidence \(clip.caseID) locked from \(who)")
        }
        let locked = deps.caseworkStore.evidenceClips.first { $0.id == clip.id } ?? clip
        let body = (try? JSONSerialization.data(withJSONObject: evidencePayload(locked))) ?? Data("{}".utf8)
        return .ok(body)
    }

    func eventsResponse() async -> Data {
        await MainActor.run {
            guard let deps = self.dependencies else { return Data("[]".utf8) }
            let names = Dictionary(deps.cameraStore.cameras.map { ($0.id, $0.name) },
                                   uniquingKeysWith: { a, _ in a })
            let events = deps.aiDetectionStore.recentEventsAcrossCameras(limit: 200)
            let payload: [[String: Any]] = events.map { ev in
                var entry: [String: Any] = [
                    "id": ev.id.uuidString,
                    "cameraID": ev.cameraID.uuidString,
                    "cameraName": names[ev.cameraID] ?? "Camera",
                    "kind": ev.kind.rawValue,
                    "confidence": ev.confidence,
                    "createdAt": ev.timestamp.timeIntervalSince1970,
                    "hasThumbnail": ev.framePath != nil
                ]
                if let desc = ev.sceneDescription { entry["description"] = desc }
                if let text = ev.detectedText { entry["detectedText"] = text }
                return entry
            }
            return (try? JSONSerialization.data(withJSONObject: payload)) ?? Data("[]".utf8)
        }
    }

    func eventThumbnail(eventID: UUID) async -> (Data, String)? {
        await MainActor.run {
            guard let deps = self.dependencies,
                  let event = deps.aiDetectionStore.event(withID: eventID),
                  let path = event.framePath,
                  let data = try? Data(contentsOf: URL(fileURLWithPath: path)) else { return nil }
            return (data, "image/jpeg")
        }
    }

    func segmentsResponse(cameraID: UUID, from: Date?, to: Date?) async -> Data {
        await MainActor.run {
            guard let deps = self.dependencies else { return Data("[]".utf8) }
            let segments = deps.mediaIngestStore.segments(for: cameraID)
                .filter { segment in
                    if let from, segment.modifiedAt < from { return false }
                    if let to, segment.createdAt > to { return false }
                    return true
                }
            let payload: [[String: Any]] = segments.map { segment in
                [
                    "name": segment.fileURL.lastPathComponent,
                    "createdAt": segment.createdAt.timeIntervalSince1970,
                    "modifiedAt": segment.modifiedAt.timeIntervalSince1970,
                    "sizeBytes": segment.byteCount
                ]
            }
            return (try? JSONSerialization.data(withJSONObject: payload)) ?? Data("[]".utf8)
        }
    }

    func segmentFile(cameraID: UUID, name: String) async -> (Data, String)? {
        let url: URL? = await MainActor.run { [weak dependencies] in
            dependencies?.mediaIngestStore.segments(for: cameraID)
                .first(where: { $0.fileURL.lastPathComponent == name })?.fileURL
        }
        guard let url, let data = try? Data(contentsOf: url) else { return nil }
        let mime = url.pathExtension.lowercased() == "mp4" ? "video/mp4" : "application/octet-stream"
        return (data, mime)
    }

    func snapshotFile(cameraID: UUID) async -> (Data, String)? {
        let frameDir: URL? = await MainActor.run { [weak dependencies] in
            dependencies?.mediaIngestStore.liveStream(for: cameraID)?.frameDirectoryURL
        }
        guard let frameDir else { return nil }
        let candidates = (try? FileManager.default.contentsOfDirectory(at: frameDir, includingPropertiesForKeys: [.contentModificationDateKey])) ?? []
        let latest = candidates
            .filter { $0.pathExtension.lowercased() == "jpg" || $0.pathExtension.lowercased() == "jpeg" }
            .sorted { a, b in
                let aDate = (try? a.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate) ?? .distantPast
                let bDate = (try? b.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate) ?? .distantPast
                return aDate > bDate
            }
            .first
        guard let url = latest, let data = try? Data(contentsOf: url) else { return nil }
        return (data, "image/jpeg")
    }

    func redeemPairing(code: String, deviceName: String) async -> Data? {
        await MainActor.run {
            guard let deps = self.dependencies else { return nil }
            switch deps.pairingTokenStore.redeem(pairingCode: code, deviceName: deviceName) {
            case .success(let device):
                var payload: [String: Any] = [
                    "deviceID": device.id.uuidString,
                    "token": device.token,
                    "name": device.name,
                    "serverName": Host.current().localizedName ?? "Sentinel Mac"
                ]
                if let remoteURL = deps.cloudflaredTunnel.tunnelURL {
                    payload["remoteURL"] = remoteURL
                }
                return try? JSONSerialization.data(withJSONObject: payload)
            case .failure:
                return nil
            }
        }
    }

    func authorize(token: String) async -> UUID? {
        await MainActor.run {
            guard let deps = self.dependencies,
                  let device = deps.pairingTokenStore.device(forToken: token) else { return nil }
            deps.pairingTokenStore.touch(deviceID: device.id)
            return device.id
        }
    }

    func registerDevice(deviceID: UUID, apnsToken: String) async {
        await MainActor.run {
            self.dependencies?.pairingTokenStore.touch(deviceID: deviceID, apnsToken: apnsToken)
        }
    }

    func currentRemoteURL() async -> String? {
        await MainActor.run {
            self.dependencies?.cloudflaredTunnel.tunnelURL
        }
    }

    func liveStreamPath(cameraID: UUID) async -> String {
        await MainActor.run {
            self.dependencies?.mediaMTXStore.livePath(for: cameraID) ?? cameraID.uuidString
        }
    }
}

extension SentinelAppDependencies {
    /// One-time migration: re-resolve saved ONVIF cameras whose live profile is
    /// a low-codec (MJPEG/unknown) stream onto the best available H.264/H.265
    /// profile. Cameras added before the profile-preference fix could be pinned
    /// to e.g. Hanwha `profile1` (MJPEG), which can't be muxed to HLS and leaves
    /// the tile on "PREVIEW START FAILED". Runs once, only upgrades a camera when
    /// a strictly higher-ranked codec exists, and keeps the camera's identity
    /// (recordings/credentials) intact.
    func migrateMJPEGLiveProfilesIfNeeded() async {
        let key = "handoffgrid.didMigrateMJPEGLiveProfiles.v1"
        guard UserDefaults.standard.bool(forKey: key) == false else { return }

        let candidates = cameraStore.cameras.filter {
            $0.isLocalCamera == false && ($0.onvifServiceURL?.isEmpty == false)
        }
        guard candidates.isEmpty == false else {
            UserDefaults.standard.set(true, forKey: key)
            return
        }

        for camera in candidates {
            guard let serviceURL = camera.onvifServiceURL else { continue }
            let sanitized = RTSPCredentialFormatter.sanitize(camera.rtspURL)
            let user = sanitized.username.isEmpty ? camera.username : sanitized.username
            let pass = sanitized.password.isEmpty
                ? ((try? CameraSecrets.password(for: camera.id)) ?? "")
                : sanitized.password
            let creds = ONVIFCredentials(username: user, password: pass)

            guard let details = try? await ONVIFSOAPClient.fetchCameraDetails(
                serviceURL: serviceURL,
                fallbackHost: camera.ipAddress,
                credentials: creds
            ) else { continue }

            // Rank of the profile the camera is *currently* using (matched by its
            // saved ONVIF token); treat an unmatched profile as "unknown" rank.
            let currentRank = details.profiles
                .first { $0.token == camera.onvifProfileToken }?.codecRank ?? 1
            guard let best = details.profiles.preferredForLiveView,
                  best.codecRank >= 2,            // H.264 or H.265
                  best.codecRank > currentRank    // strictly better than today's
            else { continue }

            cameraStore.repointStream(
                cameraID: camera.id,
                rtspURL: best.rtspURL,
                profileName: best.name,
                onvifProfileToken: best.token.isEmpty ? nil : best.token,
                resolution: best.resolution,
                fps: best.fps
            )
        }

        UserDefaults.standard.set(true, forKey: key)
    }

    func startConfiguredMedia() async {
        guard cameraCredentialStore.requiresSessionUnlock(for: cameraStore.cameras) == false else {
            return
        }
        // Prevent overlapping runs (multiple onChange/.task hooks can fire while
        // an earlier run is still awaiting), which would churn MediaMTX.
        guard isConfiguringMedia == false else { return }
        isConfiguringMedia = true
        defer { isConfiguringMedia = false }

        // One-time: move any ONVIF camera stuck on an unviewable MJPEG profile
        // (e.g. Hanwha profile1) onto its H.264/H.265 profile BEFORE we write the
        // MediaMTX config, so its live tile works instead of failing HLS.
        await migrateMJPEGLiveProfilesIfNeeded()

        // Await GStreamer detection so the gstreamerLaunchPath guard below can't
        // race and silently skip live previews + motion/AI detection.
        await mediaEngineStore.refreshAndWait()

        let savedDays = UserDefaults.standard.integer(forKey: "handoffgrid.recordingRetentionDays")
        let globalDays = savedDays == 0 ? 7 : savedDays
        // Per-camera retention overrides the global default; cameras without
        // their own `retentionDays` fall back to the global value. The second
        // pass mops up orphan segments from cameras that no longer exist.
        _ = mediaIngestStore.pruneRecordingsPerCamera(cameras: cameraStore.cameras, globalFallbackDays: globalDays)
        _ = mediaIngestStore.pruneRecordings(olderThanDays: globalDays)
        _ = mediaIngestStore.applyMotionRecordingPolicy(for: cameraStore.cameras)

        // Start MediaMTX first — it proxies every camera stream so the rest of the
        // app (tiles, GStreamer bridges, iOS HLS) all connect to it instead of cameras directly.
        if mediaMTXStore.isAvailable {
            if mediaMTXStore.isRunning {
                // Already up — apply any config changes via SIGHUP reload instead
                // of a stop+restart. startConfiguredMedia is re-invoked on camera
                // state changes; hard-restarting MediaMTX each time churned the
                // proxy and blacked out the live tiles.
                mediaMTXStore.reload(
                    cameras: cameraStore.cameras,
                    recordingRootURL: mediaIngestStore.recordingRootURL,
                    credentials: cameraCredentialStore
                )
            } else {
                mediaMTXStore.start(
                    cameras: cameraStore.cameras,
                    recordingRootURL: mediaIngestStore.recordingRootURL,
                    credentials: cameraCredentialStore
                )
                // Wait for MediaMTX to come up before starting dependent processes
                try? await Task.sleep(nanoseconds: 1_500_000_000)
            }
        }

        // Register MediaMTX recording sessions in the ingest store (drives UI REC badges)
        if mediaMTXStore.isRunning {
            mediaIngestStore.syncMediaMTXRecordingSessions(
                cameras: cameraStore.cameras,
                mediaMTXStore: mediaMTXStore
            )
        }

        guard mediaEngineStore.snapshot.gstreamerLaunchPath != nil else {
            syncAIDetection()
            let cameras = cameraStore.cameras
            mediaIngestStore.startConnectivityLoop { cameras }
            return
        }

        for camera in cameraStore.cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false && camera.hasEmbeddedRTSPCredentials {
            // Skip GStreamer recording when MediaMTX handles it
            if mediaMTXStore.isRunning == false {
                if camera.isRecording && mediaIngestStore.recordingSession(for: camera.id) == nil {
                    let result = await mediaIngestStore.startRecording(
                        for: camera,
                        mediaEngine: mediaEngineStore,
                        credentials: cameraCredentialStore
                    )
                    if result.didLaunch { continue }
                }
            }

            // GStreamer live bridge for AI motion detection.
            // When MediaMTX is running, bridge connects to its local re-streamed RTSP
            // so the camera's single slot stays free.
            if mediaIngestStore.liveStream(for: camera.id) == nil {
                _ = await mediaIngestStore.startLiveBridge(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore,
                    mediaMTXStore: mediaMTXStore
                )
            }
        }

        syncAIDetection()

        let cameras = cameraStore.cameras
        mediaIngestStore.startConnectivityLoop { cameras }
    }

    func syncAIDetection() {
        aiDetectionStore.sync(
            liveStreams: Array(mediaIngestStore.liveStreams.values),
            cameras: cameraStore.cameras,
            mediaIngestStore: mediaIngestStore
        )
    }
}

extension View {
    @MainActor
    func sentinelEnvironment(_ dependencies: SentinelAppDependencies) -> some View {
        environmentObject(dependencies.cameraStore)
            .environmentObject(dependencies.mediaEngineStore)
            .environmentObject(dependencies.mediaIngestStore)
            .environmentObject(dependencies.aiDetectionStore)
            .environmentObject(dependencies.onvifDiscoveryStore)
            .environmentObject(dependencies.commandCenter)
            .environmentObject(dependencies.caseworkStore)
            .environmentObject(dependencies.userDirectoryStore)
            .environmentObject(dependencies.operatorSessionStore)
            .environmentObject(dependencies.floorplanStore)
            .environmentObject(dependencies.workflowStore)
            .environmentObject(dependencies.appearanceStore)
            .environmentObject(dependencies.cameraCredentialStore)
            .environmentObject(dependencies.mediaMTXStore)
            .environmentObject(dependencies.licenseStore)
            .environmentObject(dependencies.evidenceExporter)
            .environmentObject(dependencies.pairingTokenStore)
            .environmentObject(dependencies.httpServer)
            .environmentObject(dependencies.cloudflaredTunnel)
            .environmentObject(dependencies.powerManager)
            .environmentObject(dependencies.pushService)
    }
}

@MainActor
final class AppearanceStore: ObservableObject {
    private let defaultsKey = "handoffgrid.appearance"
    @Published private(set) var appearance: AppAppearance

    init() {
        let storedAppearance = UserDefaults.standard.string(forKey: defaultsKey)
        appearance = AppAppearance(rawValue: storedAppearance ?? "") ?? .light
        applyAppearance()
    }

    var colorScheme: ColorScheme {
        appearance.colorScheme
    }

    func toggle() {
        setAppearance(appearance == .dark ? .light : .dark)
    }

    func setAppearance(_ newAppearance: AppAppearance) {
        appearance = newAppearance
        UserDefaults.standard.set(appearance.rawValue, forKey: defaultsKey)
        applyAppearance()
    }

    private func applyAppearance() {
        NSApplication.shared.appearance = NSAppearance(named: appearance.nsAppearanceName)
    }
}

enum AppAppearance: String {
    case dark
    case light

    var colorScheme: ColorScheme {
        switch self {
        case .dark: return .dark
        case .light: return .light
        }
    }

    var nsAppearanceName: NSAppearance.Name {
        switch self {
        case .dark: return .darkAqua
        case .light: return .aqua
        }
    }

    var label: String {
        switch self {
        case .dark: return "Dark"
        case .light: return "Light"
        }
    }

    var symbol: String {
        switch self {
        case .dark: return "moon.fill"
        case .light: return "sun.max.fill"
        }
    }
}

@MainActor
final class SentinelCommandCenter: ObservableObject {
    @Published private(set) var addCameraRequestID = UUID()
    @Published private(set) var createIncidentRequestID = UUID()
    @Published private(set) var navigationRequestID = UUID()
    @Published private(set) var upgradeRequestID = UUID()
    private(set) var requestedSection: SentinelSection = .home

    /// AI-workspace sub-navigation (Overview / a detector / a cloud feature).
    /// Owned here so the AI capability sidebar and `AICenterView` share one
    /// source of truth without adding `SentinelSection` cases.
    @Published var aiFocus: AICapabilityFocus = .overview

    /// One-shot hint, consumed by `SearchView` on appear: open directly on the
    /// Claude natural-language (Events) tab instead of the default recordings
    /// file search. Set via `requestAISearch()`.
    @Published var pendingAISearch = false

    func requestAddCamera() {
        addCameraRequestID = UUID()
    }

    func requestCreateIncident() {
        createIncidentRequestID = UUID()
    }

    func requestOpen(_ section: SentinelSection) {
        requestedSection = section
        navigationRequestID = UUID()
    }

    /// Opens the Search section directly on its Claude natural-language (Events)
    /// tab. Used by the AI workspace's "Open AI Search" action so it lands on the
    /// actual AI search rather than the default recordings/file search.
    func requestAISearch() {
        pendingAISearch = true
        requestOpen(.search)
    }

    func requestUpgrade() {
        upgradeRequestID = UUID()
    }
}

struct SentinelRootView: View {
    @Environment(\.scenePhase) private var scenePhase
    @EnvironmentObject private var appearanceStore: AppearanceStore
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var powerManager: PowerManager
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @State private var selectedSection: SentinelSection? = .home
    @State private var isAddingCamera = false
    @State private var isShowingCameraAccess = false
    @State private var isShowingUpgrade = false
    @StateObject private var commandPaletteStore = CommandPaletteStore()

    var body: some View {
        ZStack {
            if operatorSessionStore.isLoggedIn == false {
                OperatorLoginView()
            } else if shouldShowAccessGate {
                AppAccessLockView()
            } else {
                appShell
            }
        }
        .frame(minWidth: 1180, minHeight: 720)
        .background(SentinelTheme.background)
        .preferredColorScheme(appearanceStore.colorScheme)
        .ignoresSafeArea(.container, edges: .top)
        .sheet(isPresented: $isAddingCamera) {
            AddCameraSheet()
        }
        .sheet(isPresented: $isShowingCameraAccess) {
            CameraAccessSheet()
        }
        .sheet(isPresented: $isShowingUpgrade) {
            UpgradeSheet()
        }
        .onChange(of: commandCenter.upgradeRequestID) { _ in
            isShowingUpgrade = true
        }
        .onChange(of: cameraStore.cameras.map(\.id)) { _ in
            reloadMediaForCameraChange()
        }
        .onChange(of: commandCenter.addCameraRequestID) { _ in
            guard shouldShowAccessGate == false else {
                isShowingCameraAccess = true
                return
            }
            isAddingCamera = true
        }
        .onChange(of: commandCenter.createIncidentRequestID) { _ in
            guard shouldShowAccessGate == false else {
                isShowingCameraAccess = true
                return
            }
            caseworkStore.createIncidentFromNewestAlert()
            selectedSection = .alerts
        }
        .onChange(of: commandCenter.navigationRequestID) { _ in
            guard shouldShowAccessGate == false else {
                return
            }
            selectedSection = commandCenter.requestedSection
        }
        .onChange(of: scenePhase) { phase in
            // Deliberately do NOT tear down media on `.background`. macOS reports
            // `.background` when the window is merely minimized or occluded by
            // another app — but a VMS must keep recording + motion detection
            // running 24/7 while hidden. Tearing down here was silently stopping
            // all recording whenever the window lost focus. Media teardown now
            // happens only on willTerminate (NSApplication.willTerminateNotification).
            if phase == .active,
               shouldShowAccessGate,
               cameraCredentialStore.shouldOfferMacUnlockReset,
               cameraCredentialStore.isAuthenticating == false {
                Task {
                    _ = await cameraCredentialStore.unlockWithTouchID(for: cameraStore.cameras)
                }
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.willTerminateNotification)) { _ in
            stopMediaProcesses()
            cameraCredentialStore.clearSessionAccess()
        }
        .onAppear {
            selectedSection = cameraStore.cameras.isEmpty ? workflowStore.preferences.defaultLandingSection : .live
            powerManager.updateRecordingState(isRecording: mediaIngestStore.activeRecordings.isEmpty == false)
            Task {
                await startConfiguredMedia()
            }
        }
        .onChange(of: mediaIngestStore.activeRecordings.isEmpty) { isEmpty in
            powerManager.updateRecordingState(isRecording: isEmpty == false)
        }
        .onChange(of: configuredMediaState) { _ in
            Task {
                await startConfiguredMedia()
            }
        }
        .onChange(of: cameraCredentialStore.didChooseSessionAccess) { _ in
            Task {
                await startConfiguredMedia()
            }
        }
        .task(id: configuredMotionRecordingState) {
            await runMotionRecordingPolicyLoop()
        }
        .task(id: alertAutomationState) {
            await runAlertAutomationLoop()
        }
        .task(id: aiStreamState) {
            syncAIDetection()
        }
        .task {
            await runRecordingScheduleLoop()
        }
        .task(id: dualStreamCameraState) {
            await runDualStreamLoop()
        }
    }

    private var appShell: some View {
        HStack(spacing: 0) {
            SentinelSidebar(selection: $selectedSection)
                .frame(width: 260)

            Divider()
                .overlay(SentinelTheme.line)

            SentinelDetailView(
                section: selectedSection ?? .home,
                addCamera: { isAddingCamera = true },
                cameraAccess: { isShowingCameraAccess = true },
                createIncident: {
                    caseworkStore.createIncidentFromNewestAlert()
                    selectedSection = .alerts
                },
                exportEvidence: { selectedSection = .evidence },
                openSection: { selectedSection = $0 }
            )
        }
        .commandPalette(
            store: commandPaletteStore,
            cameras: cameraStore.cameras,
            alerts: caseworkStore.alerts,
            onSelectSection: { section in
                selectedSection = section
            },
            onSelectCamera: { _ in
                selectedSection = .live
            },
            onSelectAlert: { _ in
                selectedSection = .alerts
            }
        )
    }

    private var shouldShowAccessGate: Bool {
        cameraCredentialStore.requiresSessionUnlock(for: cameraStore.cameras)
    }

    private var configuredCameraIDs: [UUID] {
        cameraStore.cameras
            .filter { $0.isLocalCamera == false && $0.rtspURL.isEmpty == false }
            .map(\.id)
    }

    private var configuredMediaState: [String] {
        cameraStore.cameras
            .filter { $0.isLocalCamera == false && $0.rtspURL.isEmpty == false }
            .map { "\($0.id.uuidString):\($0.isRecording):\($0.recordingMode.rawValue):\($0.recordingCodec.rawValue)" }
    }

    private var configuredMotionRecordingState: [String] {
        cameraStore.cameras
            .filter { $0.isLocalCamera == false && $0.rtspURL.isEmpty == false && $0.recordingMode == .motion }
            .map { "\($0.id.uuidString):\($0.isRecording):\($0.recordingMode.rawValue)" }
    }

    private var dualStreamCameraState: [String] {
        cameraStore.cameras
            .filter { $0.isLocalCamera == false && $0.rtspURL.isEmpty == false && $0.recordingMode == .dualStream && $0.isRecording }
            .map { "\($0.id.uuidString):\($0.subStreamRTSPURL)" }
    }

    private var alertAutomationState: [String] {
        cameraStore.cameras
            .filter { $0.isLocalCamera == false && $0.rtspURL.isEmpty == false }
            .map { "\($0.id.uuidString):\($0.isRecording):\($0.recordingMode.rawValue)" }
    }

    private var aiStreamState: [String] {
        mediaIngestStore.liveStreams.values
            .sorted { $0.cameraID.uuidString < $1.cameraID.uuidString }
            .map { "\($0.cameraID.uuidString):\($0.processID)" }
    }

    private func startConfiguredMedia() async {
        guard cameraCredentialStore.requiresSessionUnlock(for: cameraStore.cameras) == false else {
            return
        }

        // Await GStreamer detection so the gstreamerLaunchPath guard below can't
        // race and silently skip live previews + motion/AI detection.
        await mediaEngineStore.refreshAndWait()

        let savedDays = UserDefaults.standard.integer(forKey: "handoffgrid.recordingRetentionDays")
        let globalDays = savedDays == 0 ? 7 : savedDays
        // Per-camera retention overrides the global default; cameras without
        // their own `retentionDays` fall back to the global value. The second
        // pass mops up orphan segments from cameras that no longer exist.
        _ = mediaIngestStore.pruneRecordingsPerCamera(cameras: cameraStore.cameras, globalFallbackDays: globalDays)
        _ = mediaIngestStore.pruneRecordings(olderThanDays: globalDays)
        _ = mediaIngestStore.applyMotionRecordingPolicy(for: cameraStore.cameras)

        // When MediaMTX is available, defer ALL stream management to
        // SentinelAppDependencies.startConfiguredMedia(), which starts MediaMTX first
        // then connects live bridges through the proxy. Starting bridges here would
        // claim camera RTSP slots before MediaMTX can proxy them.
        guard mediaMTXStore.isAvailable == false else {
            return
        }

        // GStreamer-only path (no MediaMTX installed)
        guard mediaEngineStore.snapshot.gstreamerLaunchPath != nil else {
            return
        }

        for camera in cameraStore.cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false && camera.hasEmbeddedRTSPCredentials {
            if camera.isRecording && mediaIngestStore.recordingSession(for: camera.id) == nil {
                let result = await mediaIngestStore.startRecording(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore
                )
                if result.didLaunch { continue }
            }

            if mediaIngestStore.recordingSession(for: camera.id) == nil &&
                mediaIngestStore.liveStream(for: camera.id) == nil {
                _ = await mediaIngestStore.startLiveBridge(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore
                )
            }
        }

        syncAIDetection()
    }

    private func runRecordingScheduleLoop() async {
        while Task.isCancelled == false {
            for camera in cameraStore.cameras where camera.recordingSchedule != nil && camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
                let inSchedule = camera.isInRecordingSchedule
                let isRecording = mediaIngestStore.isRecording(cameraID: camera.id)
                if inSchedule && !isRecording && camera.isRecording {
                    _ = await mediaIngestStore.startRecording(
                        for: camera, mediaEngine: mediaEngineStore, credentials: cameraCredentialStore)
                } else if !inSchedule && isRecording {
                    mediaIngestStore.stopRecording(for: camera.id)
                }
            }
            try? await Task.sleep(nanoseconds: 60_000_000_000)
        }
    }

    private func runMotionRecordingPolicyLoop() async {
        guard configuredMotionRecordingState.isEmpty == false else {
            return
        }

        while Task.isCancelled == false {
            _ = mediaIngestStore.applyMotionRecordingPolicy(for: cameraStore.cameras)
            try? await Task.sleep(nanoseconds: 10_000_000_000)
        }
    }

    private func runDualStreamLoop() async {
        let dualCameras = cameraStore.cameras.filter {
            $0.isLocalCamera == false && $0.rtspURL.isEmpty == false &&
            $0.recordingMode == .dualStream && $0.isRecording
        }
        guard dualCameras.isEmpty == false else { return }

        // How long after last motion before we stop high-res recording
        let savedCooldown = UserDefaults.standard.double(forKey: "handoffgrid.dualStreamCooldownSeconds")
        let motionCooldown: TimeInterval = savedCooldown > 0 ? savedCooldown : 30

        while Task.isCancelled == false {
            let now = Date()
            for camera in dualCameras {
                let lastMotion = mediaIngestStore.dualStreamLastMotionAt[camera.id]
                let secondsSinceMotion = lastMotion.map { now.timeIntervalSince($0) } ?? .infinity
                let isHighResRecording = mediaIngestStore.recordingSession(for: camera.id) != nil

                if secondsSinceMotion < motionCooldown && !isHighResRecording {
                    // Motion detected recently — start high-res recording
                    _ = await mediaIngestStore.startRecording(for: camera, mediaEngine: mediaEngineStore, credentials: cameraCredentialStore)
                } else if secondsSinceMotion >= motionCooldown && isHighResRecording {
                    // Cooled off — stop high-res recording
                    mediaIngestStore.stopRecording(for: camera.id)
                }
            }
            try? await Task.sleep(nanoseconds: 5_000_000_000)
        }
    }

    private func runAlertAutomationLoop() async {
        guard alertAutomationState.isEmpty == false else {
            return
        }

        try? await Task.sleep(nanoseconds: 12_000_000_000)

        while Task.isCancelled == false {
            caseworkStore.syncOperationalAlarms(cameras: cameraStore.cameras, mediaIngestStore: mediaIngestStore)
            caseworkStore.syncStorageAlarm(recordingRootURL: mediaIngestStore.recordingRootURL)
            workflowStore.syncNotifications(from: caseworkStore.alerts)
            try? await Task.sleep(nanoseconds: 8_000_000_000)
        }
    }

    private func syncAIDetection() {
        aiDetectionStore.sync(
            liveStreams: Array(mediaIngestStore.liveStreams.values),
            cameras: cameraStore.cameras,
            mediaIngestStore: mediaIngestStore
        )
    }

    private func stopMediaProcesses() {
        aiDetectionStore.stopAll()
        mediaIngestStore.stopAll()
        mediaEngineStore.stopExternalPreviews()
    }

    /// When the camera list changes (e.g. a camera was just added), MediaMTX
    /// must be told about the new set of paths so it creates the RTSP source +
    /// HLS muxer the live tiles and recorder depend on. Without this, a camera
    /// added while the app is running stays black until the next relaunch.
    private func reloadMediaForCameraChange() {
        guard mediaMTXStore.isAvailable, mediaMTXStore.isRunning else { return }
        mediaMTXStore.reload(
            cameras: cameraStore.cameras,
            recordingRootURL: mediaIngestStore.recordingRootURL,
            credentials: cameraCredentialStore
        )
        // Give MediaMTX a moment to load the new path before wiring up the
        // recording sessions that drive the REC badges.
        Task {
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            mediaIngestStore.syncMediaMTXRecordingSessions(
                cameras: cameraStore.cameras,
                mediaMTXStore: mediaMTXStore
            )
        }
    }

}

struct OperatorLoginView: View {
    @EnvironmentObject private var userDirectoryStore: UserDirectoryStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var licenseStore: LicenseStore
    @State private var selectedUser: UserAccount?
    @State private var pin = ""
    @State private var confirmPin = ""
    @State private var pinError: String?
    @FocusState private var pinFocused: Bool

    // First-run admin creation
    @State private var adminName = ""
    @State private var adminPassword = ""
    @State private var adminConfirm = ""
    @State private var createError: String?
    @FocusState private var createFocus: CreateField?
    private enum CreateField { case name, password, confirm }

    // After admin is created, hold the session until the license step completes.
    @State private var pendingAdmin: UserAccount?
    @State private var pendingPassword = ""
    // Once a cloud account exists, show the plan picker before finishing setup.
    @State private var showPlanStep = false

    private var isFirstRun: Bool { userDirectoryStore.users.isEmpty }

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            if let admin = pendingAdmin {
                if showPlanStep {
                    PlanOnboardingStepView {
                        finishSetup(admin: admin, password: pendingPassword)
                    }
                } else {
                    LicenseSetupStepView(admin: admin, adminPassword: pendingPassword) { didCreateAccount in
                        if didCreateAccount && licenseStore.isSignedIn {
                            showPlanStep = true
                        } else {
                            finishSetup(admin: admin, password: pendingPassword)
                        }
                    }
                }
            } else if isFirstRun {
                createAdminContent
            } else {
                signInContent
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - First-run: create the initial administrator

    private var createAdminContent: some View {
        VStack(spacing: 24) {
            HandoffGridMark(size: 52)

            VStack(spacing: 6) {
                Text("Create Administrator Account")
                    .font(.title2.weight(.semibold))
                Text("Set up the first administrator to secure this Sentinel VMS install. You can add more users and roles later.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: 380)

            VStack(spacing: 12) {
                TextField("Administrator name", text: $adminName)
                    .textFieldStyle(.roundedBorder)
                    .focused($createFocus, equals: .name)
                SecureField("Password (min 8 characters)", text: $adminPassword)
                    .textFieldStyle(.roundedBorder)
                    .focused($createFocus, equals: .password)
                SecureField("Confirm password", text: $adminConfirm)
                    .textFieldStyle(.roundedBorder)
                    .focused($createFocus, equals: .confirm)
                    .onSubmit(createAdministrator)

                if let createError {
                    Text(createError)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.red)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }

                Button(action: createAdministrator) {
                    Label("Create Account", systemImage: "checkmark.shield.fill")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .keyboardShortcut(.return)
            }
            .frame(width: 320)
        }
        .padding(40)
        .onAppear { createFocus = .name }
    }

    private func createAdministrator() {
        createError = nil
        let trimmed = adminName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.isEmpty == false else {
            createError = "Enter an administrator name."
            return
        }
        guard adminPassword.count >= 8 else {
            createError = "Password must be at least 8 characters."
            return
        }
        guard adminPassword == adminConfirm else {
            createError = "Passwords don't match."
            return
        }
        guard let admin = userDirectoryStore.createAdministrator(name: trimmed, password: adminPassword) else {
            createError = "Could not create the account."
            return
        }
        // Don't log in yet — show the license setup step first.
        pendingAdmin = admin
        pendingPassword = adminPassword
        workflowStore.recordAudit(area: "Access", action: "Administrator account created", detail: "\(admin.name) (Admin)")
    }

    private func finishSetup(admin: UserAccount, password: String) {
        _ = operatorSessionStore.login(admin, pin: password)
        userDirectoryStore.markSeen(admin)
        pendingAdmin = nil
        pendingPassword = ""
        showPlanStep = false
    }

    // MARK: - Returning users: sign in


    private var signInContent: some View {
        VStack(spacing: 28) {
            HandoffGridMark(size: 52)

            VStack(spacing: 6) {
                Text("Who's on shift?")
                    .font(.title2.weight(.semibold))
                Text("Select your operator profile and sign in to begin.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }

            if let user = selectedUser {
                VStack(spacing: 16) {
                    HStack(spacing: 12) {
                        Image(systemName: "person.crop.circle.fill")
                            .font(.title)
                            .foregroundStyle(SentinelTheme.accent)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(user.name).font(.headline)
                            Text(user.role).font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Button("Change") { selectedUser = nil; pin = "" }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                    }
                    .padding(14)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))

                    if user.hasPin {
                        VStack(spacing: 10) {
                            SecureField("Enter password", text: $pin)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 220)
                                .focused($pinFocused)
                                .onSubmit { attemptLogin(user) }

                            if let error = operatorSessionStore.loginError {
                                Text(error)
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(.red)
                            }
                        }
                    } else {
                        // Mandatory passwords: an account with no password must set
                        // one before its first sign-in. Covers existing accounts and
                        // freshly invited users.
                        VStack(spacing: 10) {
                            Text("Set a password to continue")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(SentinelTheme.amber)
                            SecureField("New password (min 8 characters)", text: $pin)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 220)
                                .focused($pinFocused)
                            SecureField("Confirm password", text: $confirmPin)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 220)
                                .onSubmit { attemptLogin(user) }

                            if let pinError {
                                Text(pinError)
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(.red)
                            }
                        }
                    }

                    Button {
                        attemptLogin(user)
                    } label: {
                        Label(user.hasPin ? "Sign In" : "Set Password & Sign In",
                              systemImage: user.hasPin ? "lock.open.fill" : "key.fill")
                            .frame(width: 220)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .keyboardShortcut(.return)
                }
                .frame(maxWidth: 400)
            } else {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 160, maximum: 200))], spacing: 12) {
                    ForEach(userDirectoryStore.users) { user in
                        Button {
                            selectedUser = user
                            pin = ""
                            confirmPin = ""
                            pinError = nil
                            pinFocused = true
                        } label: {
                            VStack(spacing: 10) {
                                Image(systemName: "person.crop.circle.fill")
                                    .font(.largeTitle)
                                    .foregroundStyle(SentinelTheme.accent)
                                Text(user.name)
                                    .font(.callout.weight(.semibold))
                                    .lineLimit(1)
                                Text(user.role)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                                Label(user.hasPin ? "Password required" : "Set password",
                                      systemImage: user.hasPin ? "lock.fill" : "lock.badge.clock")
                                    .font(.caption2)
                                    .foregroundStyle(user.hasPin ? .secondary : SentinelTheme.amber)
                            }
                            .padding(14)
                            .frame(maxWidth: .infinity)
                            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
                            .overlay {
                                RoundedRectangle(cornerRadius: 10)
                                    .stroke(SentinelTheme.line, lineWidth: 1)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }
                .frame(maxWidth: 500)
            }
        }
        .padding(40)
    }

    private func attemptLogin(_ user: UserAccount) {
        pinError = nil
        var account = user

        // Mandatory passwords: no account may sign in without one. If this account
        // has none yet, force the operator to set one now, then continue.
        if account.hasPin == false {
            guard pin.count >= 8 else {
                pinError = "Password must be at least 8 characters."
                return
            }
            guard pin == confirmPin else {
                pinError = "Passwords don't match."
                return
            }
            userDirectoryStore.setPin(pin, for: account.id)
            account.passwordHash = OperatorSessionStore.pinHash(for: pin)
            workflowStore.recordAudit(area: "Access", action: "Password set", detail: "\(account.name) (\(account.role))")
        }

        guard operatorSessionStore.login(account, pin: pin) else { return }
        userDirectoryStore.markSeen(account)
        workflowStore.recordAudit(area: "Access", action: "Operator signed in", detail: "\(account.name) (\(account.role))")
    }
}

// MARK: - First-run license setup step (step 2 of initial setup wizard)

struct LicenseSetupStepView: View {
    let admin: UserAccount
    let adminPassword: String
    /// Called with `true` when a cloud account was created/signed in (so the
    /// plan picker should follow), or `false` when the user skipped.
    let onComplete: (Bool) -> Void

    @EnvironmentObject private var licenseStore: LicenseStore
    @State private var email = ""
    @State private var password = ""
    @FocusState private var focused: Field?
    private enum Field { case email, password }

    var body: some View {
        VStack(spacing: 28) {
            HandoffGridMark(size: 52)

            VStack(spacing: 6) {
                Text("Create Your Sentinel Account")
                    .font(.title2.weight(.semibold))
                Text("One free camera is included. Create a cloud account now so you can add more cameras later without interrupting your setup.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: 400)

            VStack(spacing: 12) {
                TextField("Email address", text: $email)
                    .textFieldStyle(.roundedBorder)
                    .textContentType(.username)
                    .disableAutocorrection(true)
                    .focused($focused, equals: .email)

                SecureField("Password (min 8 characters)", text: $password)
                    .textFieldStyle(.roundedBorder)
                    .textContentType(.password)
                    .focused($focused, equals: .password)
                    .onSubmit(signUp)

                if let error = licenseStore.lastError {
                    Text(error)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.red)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }

                Button(action: signUp) {
                    HStack {
                        if licenseStore.isBusy { ProgressView().controlSize(.small) }
                        Label("Create Account & Continue", systemImage: "checkmark.circle.fill")
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(licenseStore.isBusy || email.isEmpty || password.count < 8)
                .keyboardShortcut(.return)

                Button("Skip for now") { onComplete(false) }
                    .buttonStyle(.plain)
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            .frame(width: 320)

            Text("Sentinel VMS is free — unlimited cameras, recording, and remote access.")
                .font(.caption2)
                .foregroundStyle(.tertiary)
        }
        .padding(40)
        .onAppear { focused = .email }
    }

    private func signUp() {
        guard !email.isEmpty, password.count >= 8 else { return }
        Task {
            await licenseStore.signUp(email: email, password: password)
            // If sign-up succeeded (no error) or the account already exists, continue.
            if licenseStore.lastError == nil || licenseStore.isSignedIn {
                onComplete(true)
            }
            // If there's an error (e.g. weak password, already registered), stay on
            // the screen so the user can correct it or choose to skip.
        }
    }
}

/// First-run plan picker. Shown after the cloud account is created so new
/// users see what's free vs. paid up front and can start checkout immediately
/// or continue on the free tier. Checkout opens Stripe in the browser; the
/// entitlement syncs automatically once payment completes, so we don't block
/// finishing setup on it.
struct PlanOnboardingStepView: View {
    let onComplete: () -> Void

    @Environment(\.openURL) private var openURL

    var body: some View {
        VStack(spacing: 24) {
            HandoffGridMark(size: 48)

            VStack(spacing: 6) {
                Text("Sentinel is Free")
                    .font(.title2.weight(.semibold))
                Text("Unlimited cameras, recording, live view, and remote access — no subscriptions, no per-camera charges. Set up your cameras and you're done.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: 440)
            }

            VStack(spacing: 14) {
                // Everything free.
                planCard(
                    icon: "checkmark.seal.fill",
                    title: "Unlimited Cameras — Free",
                    detail: "Every camera gets live view, recording, playback, on-device detection, and remote access from anywhere. No limits, no subscriptions.",
                    badge: "FREE"
                )

                // Optional donation.
                planCard(
                    icon: "heart.fill",
                    title: "Support Sentinel",
                    detail: "Sentinel is free and always will be. If it's useful to you, an optional donation funds new features — and your suggestions and feedback are just as welcome."
                ) {
                    if DonationConfig.isConfigured {
                        Button {
                            openURL(DonationConfig.url)
                        } label: {
                            Label("Donate", systemImage: "heart").frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                    } else {
                        Text("Donation link coming soon.")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }

                // AI features: bring your own Anthropic key (no subscription).
                planCard(
                    icon: "sparkles",
                    title: "AI Features — Bring Your Own Key",
                    detail: "Optional. Add your own Anthropic API key in the AI tab to enable Claude scene descriptions, daily digests, and event search. You pay Anthropic directly — typically pennies per alert."
                ) {
                    Text("Set up later in the AI tab → “Set Up AI”.")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .frame(maxWidth: 460)

            Button(action: onComplete) {
                Label("Get Started", systemImage: "arrow.right.circle.fill")
                    .frame(maxWidth: 460)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)

            Text("No payment, ever. Everything runs on your Mac.")
                .font(.caption2)
                .foregroundStyle(.tertiary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 440)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    @ViewBuilder
    private func planCard<Content: View>(
        icon: String,
        title: String,
        detail: String,
        badge: String? = nil,
        @ViewBuilder content: () -> Content = { EmptyView() }
    ) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 10) {
                Image(systemName: icon)
                    .font(.system(size: 18, weight: .medium))
                    .foregroundStyle(SentinelTheme.accent)
                    .frame(width: 24)
                Text(title)
                    .font(.callout.weight(.semibold))
                Spacer()
                if let badge {
                    Text(badge)
                        .font(.caption2.weight(.bold))
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(Color.green.opacity(0.16), in: Capsule())
                        .foregroundStyle(.green)
                }
            }
            Text(detail)
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            content()
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
        .overlay { RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1) }
    }
}

struct AppAccessLockView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore

    var body: some View {
        VStack(spacing: 20) {
            HandoffGridMark(size: 62)

            VStack(spacing: 6) {
                Text("Unlock Sentinel VMS")
                    .font(.title2.weight(.semibold))

                Text("Use Touch ID to open the app and enable live view, recording, playback, alerts, exports, and administration.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 460)
                    .fixedSize(horizontal: false, vertical: true)
            }

            VStack(spacing: 10) {
                Button {
                    Task {
                        _ = await cameraCredentialStore.unlockWithTouchID(for: cameraStore.cameras)
                    }
                } label: {
                    Label(
                        cameraCredentialStore.lastAccessError == nil ? (cameraCredentialStore.isAuthenticating ? "Waiting for Touch ID" : "Unlock with Touch ID") : "Retry Touch ID",
                        systemImage: "touchid"
                    )
                    .frame(width: 260)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(cameraCredentialStore.isAuthenticating)
            }

            Text(cameraCredentialStore.lastAccessMessage)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)

            if let error = cameraCredentialStore.lastAccessError {
                VStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(SentinelTheme.amber)

                    Text(error)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.red)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: 460)
                }
                .padding(12)
                .background(.red.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
            }

            if cameraCredentialStore.lastAccessError != nil {
                VStack(spacing: 8) {
                    Button {
                        cameraCredentialStore.sleepDisplayForMacUnlock()
                    } label: {
                        Label("Lock Mac to Reset Touch ID", systemImage: "lock.fill")
                            .frame(width: 260)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.large)

                    Text("After you unlock macOS, Sentinel VMS will retry Touch ID automatically.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: 460)
                }
            }
        }
        .padding(34)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(SentinelTheme.background)
    }
}

struct CameraAccessSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 12) {
                HandoffGridMark(size: 34)

                VStack(alignment: .leading, spacing: 2) {
                    Text("Camera Access")
                        .font(.headline)

                    Text(cameraCredentialStore.hasSessionAccess ? "Session unlocked" : "Touch ID preferred")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            Text("Touch ID unlocks HandoffGrid. Cameras with unattended credentials can start live view and recording without a macOS Keychain password prompt.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            Button {
                Task {
                    if await cameraCredentialStore.unlockWithTouchID(for: cameraStore.cameras) {
                        dismiss()
                    }
                }
            } label: {
                Label(
                    cameraCredentialStore.lastAccessError == nil ? (cameraCredentialStore.isAuthenticating ? "Waiting for Touch ID" : "Unlock with Touch ID") : "Retry Touch ID",
                    systemImage: "touchid"
                )
                .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(cameraCredentialStore.isAuthenticating)

            Text(cameraCredentialStore.lastAccessMessage)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)

            if let error = cameraCredentialStore.lastAccessError {
                Text(error)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if cameraCredentialStore.lastAccessError != nil {
                Button {
                    cameraCredentialStore.sleepDisplayForMacUnlock()
                } label: {
                    Label("Lock Mac to Reset Touch ID", systemImage: "lock.fill")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
            }

            HStack {
                Button("Done") {
                    dismiss()
                }
                .disabled(cameraCredentialStore.isAuthenticating)

                Spacer()
            }
        }
        .padding(22)
        .frame(width: 460)
        .background(SentinelTheme.background)
    }
}

struct SentinelDetailView: View {
    let section: SentinelSection
    let addCamera: () -> Void
    let cameraAccess: () -> Void
    let createIncident: () -> Void
    let exportEvidence: () -> Void
    let openSection: (SentinelSection) -> Void

    var body: some View {
        VStack(spacing: 0) {
            TopCommandBar(
                section: section,
                addCamera: addCamera,
                cameraAccess: cameraAccess,
                createIncident: createIncident,
                exportEvidence: exportEvidence
            )

            Divider()
                .overlay(SentinelTheme.line)

            switch section {
            case .home:
                OperatorHomeView(openSection: openSection)
            case .live:
                LiveMonitorView()
            case .personalViews:
                PersonalViewsView()
            case .search:
                SearchView()
            case .alerts:
                AlertsView()
            case .maps:
                MapsView()
            case .evidence:
                EvidenceView()
            case .handoff:
                ShiftHandoffView()
            case .notifications:
                NotificationCenterView()
            case .ai:
                AICenterView()
            case .cameras:
                CamerasView(addCamera: addCamera)
            case .storage:
                StorageView()
            case .users:
                UsersView()
            case .audit:
                AuditLogView()
            case .settings:
                SettingsPreferencesView()
            case .plan:
                PlanBillingView()
            case .health:
                HealthView()
            }
        }
        .background(SentinelTheme.background)
    }
}

struct SentinelSidebar: View {
    @Binding var selection: SentinelSection?
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    // Remember the last section visited inside each workspace, so tapping a
    // rail icon returns you to where you left off rather than always the first.
    @State private var lastSectionByWorkspace: [SentinelWorkspace: SentinelSection] = [:]

    private var activeWorkspace: SentinelWorkspace {
        SentinelWorkspace.workspace(containing: selection ?? .live)
    }

    var body: some View {
        HStack(spacing: 0) {
            workspaceRail
            workspacePanel
        }
        .background(SentinelTheme.chrome)
        .overlay(alignment: .trailing) {
            Rectangle()
                .fill(SentinelTheme.line)
                .frame(width: 1)
        }
        .onChange(of: selection) { newValue in
            guard let newValue else { return }
            lastSectionByWorkspace[SentinelWorkspace.workspace(containing: newValue)] = newValue
        }
    }

    // Far-left vertical rail of workspace icons (Genetec-style).
    private var workspaceRail: some View {
        VStack(spacing: 6) {
            // Clear the traffic-light buttons in the transparent titlebar.
            Color.clear.frame(height: 40)

            ForEach(SentinelWorkspace.allCases) { workspace in
                WorkspaceRailButton(
                    workspace: workspace,
                    isActive: workspace == activeWorkspace,
                    badge: railBadge(for: workspace)
                ) { selectWorkspace(workspace) }
            }

            Spacer()

            HandoffGridMark(size: 26)
                .padding(.bottom, 14)
                .opacity(0.85)
        }
        .frame(width: 64)
        .frame(maxHeight: .infinity)
        .background(SentinelTheme.background)
        .overlay(alignment: .trailing) {
            Rectangle().fill(SentinelTheme.line).frame(width: 1)
        }
    }

    // Contextual sub-list showing only the active workspace's sections.
    private var workspacePanel: some View {
        VStack(spacing: 0) {
            Color.clear.frame(height: 40)

            Text(activeWorkspace.title)
                .font(.title3.weight(.semibold))
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 14)
                .padding(.bottom, 10)

            if activeWorkspace == .ai {
                // The AI workspace owns a single SentinelSection (.ai); instead of
                // a one-row list it renders a richer capability rail (Overview +
                // detectors + cloud features) driven by commandCenter.aiFocus.
                AICapabilitySidebar()
            } else {
                ScrollView(.vertical, showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(activeWorkspace.sections) { item in
                            SidebarRow(
                                item: item,
                                isSelected: selection == item,
                                badge: sidebarBadge(for: item)
                            ) { selection = item }
                        }
                    }
                    .padding(.horizontal, 8)
                    .padding(.bottom, 12)
                }
            }

            SystemMiniStatus()
        }
        .frame(width: 196)
    }

    private func selectWorkspace(_ workspace: SentinelWorkspace) {
        if let remembered = lastSectionByWorkspace[workspace],
           workspace.sections.contains(remembered) {
            selection = remembered
        } else {
            selection = workspace.defaultSection
        }
        // Entering AI from the rail always lands on the Overview dashboard.
        if workspace == .ai {
            commandCenter.aiFocus = .overview
        }
    }

    private func railBadge(for workspace: SentinelWorkspace) -> Int {
        workspace.sections.reduce(0) { $0 + sidebarBadge(for: $1) }
    }

    private func sidebarBadge(for section: SentinelSection) -> Int {
        switch section {
        case .alerts: return caseworkStore.alerts.filter { $0.alertState == .new }.count
        case .notifications: return workflowStore.unreadNotificationCount
        default: return 0
        }
    }
}

private struct WorkspaceRailButton: View {
    let workspace: SentinelWorkspace
    let isActive: Bool
    let badge: Int
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(spacing: 3) {
                ZStack(alignment: .topTrailing) {
                    Image(systemName: workspace.symbol)
                        .font(.system(size: 17, weight: .medium))
                        .frame(width: 44, height: 30)
                        .foregroundStyle(isActive ? SentinelTheme.accent : .secondary)
                        .background(
                            isActive ? SentinelTheme.accent.opacity(0.15) : .clear,
                            in: RoundedRectangle(cornerRadius: 9)
                        )

                    if badge > 0 {
                        Circle()
                            .fill(.red)
                            .frame(width: 8, height: 8)
                            .offset(x: 2, y: -2)
                    }
                }

                Text(workspace.railTitle)
                    .font(.system(size: 9, weight: .medium))
                    .foregroundStyle(isActive ? SentinelTheme.accent : .secondary)
                    .lineLimit(1)
                    .minimumScaleFactor(0.75)
            }
            .frame(width: 60)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(workspace.title)
    }
}

private struct SidebarRow: View {
    let item: SentinelSection
    let isSelected: Bool
    let badge: Int
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Image(systemName: item.symbol)
                    .frame(width: 18, alignment: .center)
                    .foregroundStyle(isSelected ? SentinelTheme.accent : .primary)

                Text(item.rawValue)
                    .font(.callout)
                    .foregroundStyle(isSelected ? SentinelTheme.accent : .primary)
                    .lineLimit(1)

                Spacer(minLength: 4)

                if badge > 0 {
                    Text("\(badge)")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(.white)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(.red, in: Capsule())
                        .minimumScaleFactor(0.8)
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.15) : .clear,
                in: RoundedRectangle(cornerRadius: 7)
            )
            .contentShape(RoundedRectangle(cornerRadius: 7))
        }
        .buttonStyle(.plain)
    }
}

struct BrandHeader: View {
    var body: some View {
        HStack {
            HandoffGridLogo(markSize: 44)
            Spacer()
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .background(SentinelTheme.panel.opacity(0.72))
    }
}

struct SystemMiniStatus: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var workflowStore: WorkflowStore

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Divider().overlay(SentinelTheme.line)

            if let op = operatorSessionStore.currentOperator {
                HStack(spacing: 8) {
                    Image(systemName: "person.crop.circle.fill")
                        .font(.caption)
                        .foregroundStyle(SentinelTheme.accent)

                    VStack(alignment: .leading, spacing: 1) {
                        Text(op.name)
                            .font(.caption.weight(.semibold))
                            .lineLimit(1)
                        Text(op.role)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }

                    Spacer()

                    Button {
                        workflowStore.recordAudit(area: "Access", action: "Operator signed out", detail: op.name)
                        operatorSessionStore.logout()
                    } label: {
                        Image(systemName: "rectangle.portrait.and.arrow.right")
                            .font(.caption)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .help("Sign out \(op.name)")
                }
                .padding(.horizontal, 16)

                Divider().overlay(SentinelTheme.line)
            }

            HStack {
                Text("Sentinel Server")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Text(serverStatusLabel)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(serverStatusColor)
            }
            .padding(.horizontal, 16)

            HStack {
                Text("\(onlineCount)/\(cameraStore.cameras.count) online")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Spacer()
                if mediaIngestStore.activeRecordings.isEmpty == false {
                    Text("\(mediaIngestStore.activeRecordings.count) recording")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.red)
                } else {
                    Text("\(mediaIngestStore.recordingSegments.count) segments")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal, 16)
            .padding(.bottom, 16)
        }
        .padding(.top, 10)
    }

    private var onlineCount: Int {
        cameraStore.cameras.filter { mediaIngestStore.effectiveStatus(for: $0) != .offline }.count
    }

    private var hasOfflineCameras: Bool {
        cameraStore.cameras.contains { mediaIngestStore.effectiveStatus(for: $0) == .offline }
    }

    private var serverStatusLabel: String {
        if hasOfflineCameras { return "Degraded" }
        if mediaMTXStore.isAvailable && mediaMTXStore.isRunning == false { return "Starting" }
        return "Healthy"
    }

    private var serverStatusColor: Color {
        if hasOfflineCameras { return .orange }
        if mediaMTXStore.isAvailable && mediaMTXStore.isRunning == false { return SentinelTheme.amber }
        return .green
    }
}

struct TopCommandBar: View {
    @EnvironmentObject private var appearanceStore: AppearanceStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    let section: SentinelSection
    let addCamera: () -> Void
    let cameraAccess: () -> Void
    let createIncident: () -> Void
    let exportEvidence: () -> Void

    /// Locks the whole app: signs the operator out and seals the vault, so the
    /// login screen returns and nobody can see the cameras without signing back
    /// in. Recording and detection keep running in the background.
    private func lockApp() {
        operatorSessionStore.logout()
    }

    /// The lock button reads "Lock" when signed in; tapping it locks the app.
    private var lockLabel: some View {
        Label("Lock", systemImage: "lock.fill")
    }

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(section.rawValue)
                    .font(.title3.weight(.semibold))

                Text(sectionSubtitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Spacer(minLength: 12)

            ViewThatFits(in: .horizontal) {
                HStack(spacing: 10) {
                    serverStatusBadge
                    commandButtons
                }

                HStack(spacing: 8) {
                    Image(systemName: "checkmark.seal.fill")
                        .foregroundStyle(.green)
                        .help("Server Healthy")

                    Button(action: lockApp) { lockLabel }
                        .labelStyle(.iconOnly)
                        .help("Lock Sentinel — sign-in required to return")

                    themePicker

                    Menu {
                        Button(action: createIncident) {
                            Label("Incident", systemImage: "exclamationmark.triangle.fill")
                        }
                        .disabled(canCreateIncident == false)
                        .help(incidentHelpText)

                        Button(action: exportEvidence) {
                            Label("Export", systemImage: "square.and.arrow.up")
                        }

                        Button(action: addCamera) {
                            Label("Add Camera", systemImage: "plus.circle.fill")
                        }
                    } label: {
                        Label("Actions", systemImage: "ellipsis.circle")
                    }
                    .menuStyle(.button)
                }
                .buttonStyle(.bordered)
            }
        }
        .padding(.horizontal, 18)
        .padding(.top, 16)
        .padding(.bottom, 8)
        .background(SentinelTheme.chrome)
        .overlay(alignment: .bottom) {
            Rectangle()
                .fill(SentinelTheme.line)
                .frame(height: 1)
        }
    }

    private var serverStatusBadge: some View {
        Label("Server Healthy", systemImage: "checkmark.seal.fill")
            .font(.caption.weight(.semibold))
            .foregroundStyle(.green)
            .lineLimit(1)
            .padding(.horizontal, 9)
            .padding(.vertical, 5)
            .background(.green.opacity(0.12), in: RoundedRectangle(cornerRadius: 4))
    }

    private var commandButtons: some View {
        HStack(spacing: 6) {
            Button(action: lockApp) { lockLabel }
                .help("Lock Sentinel — sign-in required to return")

            themePicker

            Button(action: createIncident) {
                Label("Incident", systemImage: "exclamationmark.triangle.fill")
            }
            .disabled(canCreateIncident == false)
            .help(incidentHelpText)

            Button(action: exportEvidence) {
                Label("Export", systemImage: "square.and.arrow.up")
            }
            // "Add Camera" lives in Administration → Cameras (limit-aware, shows
            // pricing/available slots); no duplicate in the global header.
        }
        .buttonStyle(.bordered)
    }

    private var canCreateIncident: Bool {
        caseworkStore.activeAlerts.isEmpty == false
    }

    private var incidentHelpText: String {
        canCreateIncident ? "Create an incident from the newest active alert" : "No active alert is available for incident creation"
    }

    private var themePicker: some View {
        Picker(
            "Theme",
            selection: Binding(
                get: { appearanceStore.appearance },
                set: { appearanceStore.setAppearance($0) }
            )
        ) {
            Label("Light", systemImage: "sun.max.fill")
                .tag(AppAppearance.light)

            Label("Dark", systemImage: "moon.fill")
                .tag(AppAppearance.dark)
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        .frame(width: 110)
        .help("Switch light and dark mode")
    }

    private var sectionSubtitle: String {
        switch section {
        case .live:
            return "Live, playback, recording, and timeline"
        case .storage:
            return "Retention, archive path, and active recording state"
        case .health:
            return "Media engine and camera connectivity"
        case .cameras:
            return "Camera inventory and stream setup"
        case .plan:
            return "Camera licenses, AI Alerts, and billing"
        default:
            return "Live operations console"
        }
    }
}
