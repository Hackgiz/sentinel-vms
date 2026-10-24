// PhoneCameraView.swift
// WAVE23 (stream I): Settings → "Use this iPhone as a camera". Thin UI over
// PhoneCameraController, which ties PhoneCameraCapture (device only) to
// PhoneStreamUplink (PhoneCameraLogic.swift).

import AVFoundation
import SwiftUI
import UIKit

/// Remembers per site that the server answered 404 to a wave-23 feature,
/// so the entry point is hidden on older servers.
enum Wave23Support {
    enum Feature: String { case phoneCamera, uploads }

    static func isUnsupported(_ feature: Feature, siteID: String?, defaults: UserDefaults = .standard) -> Bool {
        guard let siteID else { return false }
        return defaults.bool(forKey: key(feature, siteID))
    }

    static func markUnsupported(_ feature: Feature, siteID: String?, _ value: Bool = true,
                                defaults: UserDefaults = .standard) {
        guard let siteID else { return }
        defaults.set(value, forKey: key(feature, siteID))
    }

    private static func key(_ feature: Feature, _ siteID: String) -> String {
        "sentinel.mobile.wave23.\(feature.rawValue).unsupported.\(siteID)"
    }
}

@MainActor
final class PhoneCameraController: ObservableObject {
    @Published var settings = PhoneCameraSettings.load()
    @Published private(set) var phase: PhoneCameraPhase = .idle
    @Published private(set) var credentials: PhoneCameraCredentials?
    @Published private(set) var kbps = 0
    @Published private(set) var notice: String?
    @Published private(set) var serverLive: Bool?
    @Published private(set) var serverRecording: String?
    @Published private(set) var batteryWarning: String?
    @Published private(set) var thermalWarning: String?
    @Published private(set) var micDenied = false
    /// The key was rejected / camera removed: offer to set up again.
    @Published private(set) var needsNewKey = false
    /// "Using your iPhone as a camera is part of Sentinel Cloud."
    @Published var showCloudSheet = false

    let capture = PhoneCameraCapture()
    private let store: PhoneCameraCredentialStoring
    private var uplink: PhoneStreamUplink?
    private var meter = UplinkRateMeter()
    private var statusTask: Task<Void, Never>?
    private var tickTask: Task<Void, Never>?
    private var observers: [NSObjectProtocol] = []
    private var noticeClearAt: Date?

    init(store: PhoneCameraCredentialStoring = KeychainPhoneCameraStore()) {
        self.store = store
        if let id = SentinelSession.shared.currentSite?.id { credentials = store.load(siteID: id) }
        if let credentials { settings.name = credentials.name }
    }

    private var session: SentinelSession { SentinelSession.shared }
    private var siteID: String? { session.currentSite?.id }

    var canCreate: Bool { session.isSignedIn && session.can(.manageCameras) }

    // MARK: Start / stop

    func start() async {
        guard phase.isActive == false else { return }
        notice = nil
        needsNewKey = false
        settings.save()
        phase = phase.next(.start)
        guard let api = session.api, let siteID else {
            phase = phase.next(.fatal("Choose and sign in to a site first."))
            return
        }
        // Sentinel Cloud only (addendum 2). Unknown counts as yes; the 402 is the backstop.
        if session.isSignedIn, await CloudFeatureAvailability.check(.phoneCamera, api: api) == .cloudRequired {
            phase = .idle
            showCloudSheet = true
            return
        }
        guard PhoneCameraCapture.isAvailable else {
            phase = phase.next(.fatal("This device has no camera Sentinel can use (the Simulator has none)."))
            return
        }
        // 1. The phone camera on the server (created once, then reused).
        if credentials == nil {
            guard canCreate else {
                phase = phase.next(.fatal("Only an admin (Manage Cameras) can add this iPhone as a camera."))
                return
            }
            let name = settings.name.trimmingCharacters(in: .whitespacesAndNewlines)
            do {
                let created = try await api.createPhoneCamera(name: name.isEmpty ? "iPhone camera" : name)
                let creds = PhoneCameraCredentials(cameraId: created.camera.id, pushKey: created.pushKey, name: created.camera.name)
                store.save(creds, siteID: siteID)
                credentials = creds
            } catch let error as SentinelAPIError where error.isCloudRequired {
                phase = .idle
                showCloudSheet = true
                return
            } catch let error as SentinelAPIError where error.isNotFound {
                Wave23Support.markUnsupported(.phoneCamera, siteID: siteID)
                phase = phase.next(.fatal("This server doesn't support iPhone cameras yet. Update Sentinel on the server, then try again."))
                return
            } catch {
                phase = phase.next(.fatal(error.localizedDescription))
                return
            }
        }
        guard let credentials else { return }

        // 2. Permissions.
        let (camera, mic) = await PhoneCameraCapture.requestAccess(microphone: settings.microphone)
        guard camera else {
            phase = phase.next(.fatal("Sentinel can't use the camera. Allow it in iOS Settings › Sentinel › Camera."))
            return
        }
        micDenied = settings.microphone && mic == false

        // 3. Capture → uplink.
        let uplink = PhoneStreamUplink(
            transport: HTTPPushTransport(api: api, cameraID: credentials.cameraId, pushKey: credentials.pushKey),
            onEvent: { [weak self] event in Task { @MainActor in self?.handle(event) } })
        self.uplink = uplink
        capture.onInit = { data, format in
            let request = V2.PushSessionRequest(width: format.width, height: format.height, fps: format.fps,
                                                audio: format.audio ? "aac" : nil, device: UIDevice.current.name)
            Task { await uplink.setInit(data, format: request) }
        }
        capture.onSegment = { segment in Task { await uplink.enqueue(segment) } }
        capture.onError = { [weak self] message in Task { @MainActor in self?.showNotice(message) } }
        do {
            try capture.configure(lens: settings.lens, quality: settings.quality,
                                  microphone: settings.microphone && mic, portrait: Self.isPortrait)
        } catch {
            phase = phase.next(.fatal((error as? PushError).map(Self.describe) ?? error.localizedDescription))
            self.uplink = nil
            return
        }
        await uplink.start()
        capture.start()
        phase = phase.next(.captureStarted)
        UIApplication.shared.isIdleTimerDisabled = true
        observe()
        startStatusPolling(cameraID: credentials.cameraId)
    }

    func stop() async {
        capture.stop()
        await uplink?.stop()
        uplink = nil
        statusTask?.cancel(); statusTask = nil
        tickTask?.cancel(); tickTask = nil
        observers.forEach(NotificationCenter.default.removeObserver)
        observers = []
        UIApplication.shared.isIdleTimerDisabled = false
        kbps = 0
        serverLive = nil
        serverRecording = nil
        if case .failed = phase { return }
        phase = phase.next(.stop)
    }

    /// Forgets this phone's camera on this site (after the key was revoked
    /// or the camera removed). The next Start creates a new one.
    func forgetCamera() {
        if let siteID { store.delete(siteID: siteID) }
        credentials = nil
        needsNewKey = false
        phase = .idle
    }

    /// Asks the server for a new push key for the same camera (admins).
    func rotateKey() async {
        guard let api = session.api, let siteID, var creds = credentials else { return }
        do {
            creds.pushKey = try await api.rotatePushKey(cameraID: creds.cameraId).pushKey
            store.save(creds, siteID: siteID)
            credentials = creds
            needsNewKey = false
            phase = .idle
            await start()
        } catch let error as SentinelAPIError where error.isNotFound {
            forgetCamera()
            showNotice("That camera was removed on the server. Start again to add this iPhone as a new camera.")
        } catch {
            showNotice(error.localizedDescription)
        }
    }

    // MARK: Background and interruptions

    func scenePhaseChanged(_ scene: ScenePhase) {
        switch scene {
        case .background:
            guard phase.isActive else { return }
            // iOS stops the camera in the background: end this session cleanly.
            capture.stop()
            if let uplink { Task { await uplink.stop() } }
            phase = phase.next(.backgrounded)
        case .active:
            guard case .paused(.background) = phase, let uplink else { return }
            Task {
                await uplink.start()
                capture.restartWriter()
                capture.start()
                phase = phase.next(.captureStarted)
            }
        default:
            break
        }
    }

    private func observe() {
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: .AVCaptureSessionWasInterrupted, object: capture.session, queue: .main) { [weak self] _ in
            Task { @MainActor in
                guard let self, case .paused(.background) = self.phase else {
                    self?.phase = self?.phase.next(.interrupted) ?? .idle
                    return
                }
            }
        })
        observers.append(center.addObserver(forName: .AVCaptureSessionInterruptionEnded, object: capture.session, queue: .main) { [weak self] _ in
            Task { @MainActor in
                guard let self, case .paused(.interrupted) = self.phase else { return }
                // A new writer → new init segment → new push session.
                self.capture.restartWriter()
                self.phase = self.phase.next(.resumed)
            }
        })
        observers.append(center.addObserver(forName: .AVCaptureSessionRuntimeError, object: capture.session, queue: .main) { [weak self] _ in
            Task { @MainActor in
                guard let self, self.phase.isActive else { return }
                self.capture.restartWriter()
                self.capture.start()
            }
        })
        UIDevice.current.isBatteryMonitoringEnabled = true
        observers.append(center.addObserver(forName: UIDevice.batteryLevelDidChangeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.updateDeviceWarnings() }
        })
        observers.append(center.addObserver(forName: UIDevice.batteryStateDidChangeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.updateDeviceWarnings() }
        })
        observers.append(center.addObserver(forName: ProcessInfo.thermalStateDidChangeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.updateDeviceWarnings() }
        })
        updateDeviceWarnings()
        tickTask = Task { [weak self] in
            while Task.isCancelled == false {
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                guard let self else { return }
                self.kbps = self.meter.kilobitsPerSecond(now: Date())
                if let at = self.noticeClearAt, Date() >= at { self.notice = nil; self.noticeClearAt = nil }
            }
        }
    }

    private func updateDeviceWarnings() {
        let device = UIDevice.current
        batteryWarning = PhoneCameraWarnings.battery(level: device.batteryLevel,
                                                     charging: device.batteryState == .charging || device.batteryState == .full)
        thermalWarning = PhoneCameraWarnings.thermal(ProcessInfo.processInfo.thermalState)
    }

    // MARK: Uplink events

    private func handle(_ event: UplinkEvent) {
        switch event {
        case .sessionStarted:
            break
        case .sent(_, let bytes):
            meter.record(bytes: bytes, at: Date())
            phase = phase.next(.segmentSent)
        case .skipped:
            showNotice("Connection too slow, skipping ahead")
        case .trouble(let message):
            phase = phase.next(.trouble(message))
        case .fatal(.cloudRequired):
            // The subscription ended: the uplink already ended the session;
            // stop capture and explain.
            Task {
                await stop()
                showCloudSheet = true
            }
        case .fatal(let error):
            needsNewKey = true
            let message = Self.describe(error)
            Task { await stop() }
            phase = phase.next(.fatal(message))
        }
    }

    private func showNotice(_ text: String) {
        notice = text
        noticeClearAt = Date().addingTimeInterval(5)
    }

    static func describe(_ error: PushError) -> String {
        switch error {
        case .keyRejected: return "The server no longer accepts this iPhone's camera key (it was rotated or revoked)."
        case .cameraGone: return "This iPhone's camera was removed on the server."
        case .cloudRequired: return CloudRequiredSheet.Feature.phoneCamera.message
        case .sessionGone, .tokenRejected, .outOfOrder: return "The stream was interrupted."
        case .transient(let m), .refused(let m): return m
        }
    }

    // MARK: LIVE / REC from the server

    private func startStatusPolling(cameraID: String) {
        statusTask?.cancel()
        statusTask = Task { [weak self] in
            while Task.isCancelled == false {
                if let api = SentinelSession.shared.api, SentinelSession.shared.isSignedIn,
                   let camera = try? await api.camera(cameraID) {
                    self?.serverLive = camera.status.online
                    self?.serverRecording = PhoneCameraWarnings.recordingLabel(camera)
                }
                try? await Task.sleep(nanoseconds: 10_000_000_000)
            }
        }
    }

    private static var isPortrait: Bool {
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        return scene?.interfaceOrientation.isLandscape != true
    }
}

/// Screen texts derived from device and server state (unit tested).
enum PhoneCameraWarnings {
    static func battery(level: Float, charging: Bool) -> String? {
        guard charging == false, level >= 0 else { return nil }
        if level <= 0.1 { return "Battery at \(Int(level * 100))%. Plug in or streaming stops soon." }
        if level <= 0.2 { return "Battery at \(Int(level * 100))%. Plug in to keep streaming." }
        return nil
    }

    static func thermal(_ state: ProcessInfo.ThermalState) -> String? {
        switch state {
        case .serious: return "This iPhone is hot. Lower the quality or move it out of the sun."
        case .critical: return "This iPhone is too hot. iOS may stop the camera; lower the quality now."
        default: return nil
        }
    }

    /// "REC" (24/7), "REC on events", or nil (live only / unknown).
    static func recordingLabel(_ camera: V2.Camera) -> String? {
        switch camera.effectiveMode {
        case "continuous", "dual": return "REC"
        case "events": return "REC on events"
        default: return nil
        }
    }
}

// MARK: - Screen

struct PhoneCameraView: View {
    @StateObject private var controller = PhoneCameraController()
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationStack {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()
                if controller.phase.isActive {
                    streamingView
                } else {
                    setupForm
                }
            }
            .navigationTitle("iPhone as a camera")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { Task { await controller.stop(); dismiss() } }
                }
            }
        }
        .onChange(of: scenePhase) { controller.scenePhaseChanged($0) }
        .sheet(isPresented: $controller.showCloudSheet) { CloudRequiredSheet(feature: .phoneCamera) }
        .onDisappear { Task { await controller.stop() } }
    }

    // MARK: Setup

    private var setupForm: some View {
        Form {
            Section {
                if let creds = controller.credentials {
                    LabeledContent("Camera", value: creds.name)
                } else {
                    TextField("Camera name", text: $controller.settings.name)
                        .textInputAutocapitalization(.words)
                }
                Picker("Lens", selection: $controller.settings.lens) {
                    ForEach(PhoneCameraLens.allCases) { Text($0.label).tag($0) }
                }
                Picker("Quality", selection: $controller.settings.quality) {
                    ForEach(PhoneCameraQuality.allCases) { Text($0.label).tag($0) }
                }
                Toggle("Microphone", isOn: $controller.settings.microphone)
            } footer: {
                Text("Sentinel treats this iPhone like any camera: live view, recording, detection and alarms. Keep Sentinel open on this screen while it streams; iOS pauses the camera when the app is in the background or the screen locks.")
            }

            if case .failed(let message) = controller.phase {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle.fill").foregroundStyle(SentinelTheme.alarm)
                    if controller.needsNewKey {
                        if controller.canCreate, controller.credentials != nil {
                            Button("Get a new key for this camera") { Task { await controller.rotateKey() } }
                        }
                        Button("Set up as a new camera", role: .destructive) { controller.forgetCamera() }
                    }
                }
            }
            if let notice = controller.notice {
                Section { Text(notice).foregroundStyle(.secondary) }
            }

            Section {
                Button {
                    Haptics.tap()
                    Task { await controller.start() }
                } label: {
                    Label(controller.credentials == nil ? "Add camera and start streaming" : "Start streaming",
                          systemImage: "video.fill")
                        .frame(maxWidth: .infinity)
                }
                .disabled(controller.credentials == nil && controller.canCreate == false)
            } footer: {
                if controller.credentials == nil && controller.canCreate == false {
                    Text("An admin (Manage Cameras) adds this iPhone as a camera the first time.")
                } else if PhoneCameraCapture.isAvailable == false {
                    Text("No camera on this device.")
                }
            }
        }
        .scrollContentBackground(.hidden)
    }

    // MARK: Streaming

    private var streamingView: some View {
        ZStack {
            CameraPreview(session: controller.capture.session)
                .ignoresSafeArea(edges: .bottom)
            VStack(spacing: 8) {
                HStack(spacing: 8) {
                    stateBadge
                    if let rec = controller.serverRecording {
                        badge(rec, color: SentinelTheme.alarm, systemImage: "record.circle")
                    }
                    Spacer()
                    badge("\(controller.kbps) kb/s", color: .black.opacity(0.6), systemImage: "arrow.up")
                }
                if let warning = controller.thermalWarning { banner(warning, systemImage: "thermometer.sun.fill") }
                if let warning = controller.batteryWarning { banner(warning, systemImage: "battery.25") }
                if controller.micDenied { banner("Microphone not allowed: streaming video only.", systemImage: "mic.slash.fill") }
                if let notice = controller.notice { banner(notice, systemImage: "hare.fill") }
                if case .paused(let reason) = controller.phase {
                    banner(reason == .background
                           ? "Paused: iOS stops the camera while Sentinel is in the background. Streaming resumes when you come back."
                           : "Paused: another app or a call is using the camera. Streaming resumes when it's free.",
                           systemImage: "pause.circle.fill")
                }
                if case .reconnecting(let message) = controller.phase {
                    banner("Reconnecting… \(message)", systemImage: "wifi.exclamationmark")
                }
                Spacer()
                Text("Keep Sentinel open on this screen. iOS pauses the camera in the background.")
                    .font(.caption).foregroundStyle(.white.opacity(0.8))
                    .multilineTextAlignment(.center)
                    .padding(8)
                    .background(.black.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
                Button(role: .destructive) {
                    Haptics.tap()
                    Task { await controller.stop() }
                } label: {
                    Label("Stop streaming", systemImage: "stop.fill")
                        .font(.headline)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                        .background(SentinelTheme.alarm, in: RoundedRectangle(cornerRadius: 12))
                        .foregroundStyle(.white)
                }
            }
            .padding(14)
        }
    }

    private var stateBadge: some View {
        let (text, color): (String, Color) = {
            switch controller.phase {
            case .streaming: return controller.serverLive == false ? ("SENDING", SentinelTheme.amber) : ("LIVE", SentinelTheme.recording)
            case .connecting, .settingUp: return ("CONNECTING", SentinelTheme.amber)
            case .reconnecting: return ("RECONNECTING", SentinelTheme.amber)
            case .paused: return ("PAUSED", .gray)
            default: return ("OFF", .gray)
            }
        }()
        return badge(text, color: color, systemImage: "dot.radiowaves.left.and.right")
    }

    private func badge(_ text: String, color: Color, systemImage: String) -> some View {
        Label(text, systemImage: systemImage)
            .font(.caption.weight(.bold))
            .padding(.horizontal, 8).padding(.vertical, 4)
            .background(color, in: Capsule())
            .foregroundStyle(.white)
    }

    private func banner(_ text: String, systemImage: String) -> some View {
        Label(text, systemImage: systemImage)
            .font(.caption.weight(.semibold))
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(8)
            .background(.black.opacity(0.65), in: RoundedRectangle(cornerRadius: 8))
            .foregroundStyle(.white)
    }
}

/// The capture session's live preview.
struct CameraPreview: UIViewRepresentable {
    let session: AVCaptureSession

    final class PreviewView: UIView {
        override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }
        var previewLayer: AVCaptureVideoPreviewLayer { layer as! AVCaptureVideoPreviewLayer }
    }

    func makeUIView(context: Context) -> PreviewView {
        let view = PreviewView()
        view.backgroundColor = .black
        view.previewLayer.session = session
        view.previewLayer.videoGravity = .resizeAspect
        return view
    }

    func updateUIView(_ uiView: PreviewView, context: Context) {}
}
