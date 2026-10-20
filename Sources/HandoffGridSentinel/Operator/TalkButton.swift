import AppKit
import SwiftUI
import SentinelCore

// Hold-to-talk on the selected live tile: two-way audio through the
// camera's speaker (talk-down). Shown only for cameras whose payload says
// `talkback` and for users with controlPtz, like the web's TalkButton.
// Pressing (mouse, or holding T while the tile is selected) opens the
// microphone and the talkback socket; releasing mutes. The session stays
// open between presses until the server's 30 s idle limit ends it, so a
// quick back-and-forth doesn't renegotiate with the camera every time.

/// One camera's talk-down: the session, the microphone and what the
/// button shows.
@MainActor
final class TalkController: ObservableObject {
    @Published private(set) var phase: TalkPhase = .idle
    @Published private(set) var held = false
    @Published private(set) var level: Double = 0
    @Published var note = ""

    let cameraID: String
    private let api: SentinelAPI
    private var session: TalkSession?
    private var mic: MicCapture?
    /// Bumped per session so callbacks from a dropped session are ignored.
    private var generation = 0

    init(cameraID: String, api: SentinelAPI) {
        self.cameraID = cameraID
        self.api = api
    }

    func press() {
        guard held == false else { return }
        held = true
        note = ""
        if let session, session.phase.isEnded == false {
            session.setTalking(true)
            phase = session.phase
            return
        }
        begin()
    }

    func release() {
        guard held else { return }
        held = false
        level = 0
        session?.setTalking(false)
        if let session { phase = session.phase }
    }

    /// Hang up and release the microphone (tile gone, camera changed).
    func teardown() {
        let session = self.session
        self.session = nil
        generation += 1
        mic?.stop()
        mic = nil
        session?.close()
        held = false
        level = 0
        phase = .idle
    }

    private func begin() {
        generation += 1
        let gen = generation
        let session = TalkSession(api: api, cameraID: cameraID) { [weak self] _ in
            Task { @MainActor in self?.sync(gen) }
        }
        self.session = session
        session.setTalking(true)
        phase = .connecting
        Task { [weak self] in
            do {
                try await MicCapture.authorize()
                guard let self, self.session === session, session.phase.isEnded == false else { return }
                let mic = MicCapture { [session, weak self] frame, level in
                    session.send(frame: frame)
                    Task { @MainActor in self?.meter(level) }
                }
                try mic.start()
                self.mic = mic
                session.open()
            } catch {
                guard let self, self.session === session else { return }
                self.teardown()
                self.note = error.localizedDescription
            }
        }
    }

    /// Mirrors the session's phase; on its end drops it, releases the
    /// microphone and shows why (if there's anything to say).
    private func sync(_ gen: Int) {
        guard gen == generation, let session else { return }
        let current = session.phase
        if case .ended(_, let message) = current {
            self.session = nil
            generation += 1
            mic?.stop()
            mic = nil
            level = 0
            phase = .idle
            if let message { note = message }
            return
        }
        phase = current
    }

    private func meter(_ value: Double) {
        guard held, phase == .talking else {
            if level != 0 { level = 0 }
            return
        }
        // Fast attack, slow release, like a VU meter.
        let next = value > level ? value : level * 0.85 + value * 0.15
        if abs(next - level) > 0.01 { level = next }
    }

    var label: String {
        guard held else { return "Hold to talk" }
        switch phase {
        case .talking, .ready: return "Talking…"
        default: return "Connecting…"
        }
    }
}

struct TalkButton: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var session: OperatorSession
    let camera: V2.Camera
    /// True while this tile is the selected one: holding T talks.
    var keyboard = true

    var body: some View {
        if camera.talkback == true, session.can(.controlPtz) {
            // Per camera; rebuilt (and hung up) when the tile shows another camera.
            TalkControls(controller: TalkController(cameraID: camera.id, api: runtime.api),
                         cameraName: camera.name, keyboard: keyboard)
                .id(camera.id)
        }
    }
}

private struct TalkControls: View {
    @StateObject var controller: TalkController
    let cameraName: String
    let keyboard: Bool

    @State private var keyMonitor: Any?
    @State private var resignObserver: NSObjectProtocol?
    @State private var pointerDown = false

    init(controller: @autoclosure @escaping () -> TalkController, cameraName: String, keyboard: Bool) {
        _controller = StateObject(wrappedValue: controller())
        self.cameraName = cameraName
        self.keyboard = keyboard
    }

    var body: some View {
        VStack(spacing: 6) {
            if controller.note.isEmpty == false {
                Text(controller.note)
                    .font(.caption2).foregroundStyle(.white).lineLimit(4)
                    .multilineTextAlignment(.center)
                    .padding(6).frame(maxWidth: 240)
                    .background(Color.red.opacity(0.75), in: RoundedRectangle(cornerRadius: 6))
                    .onTapGesture { controller.note = "" }
                    .help("Click to dismiss")
            }
            button
        }
        .padding(.bottom, 34) // above the tile's footer pills
        .onAppear { installKeys() }
        .onDisappear { removeKeys(); controller.teardown() }
        .onChange(of: keyboard) { _ in
            removeKeys()
            controller.release()
            installKeys()
        }
    }

    private var live: Bool { controller.held && controller.phase == .talking }

    private var button: some View {
        HStack(spacing: 6) {
            Image(systemName: live ? "mic.fill" : "mic")
                .font(.system(size: 11, weight: .bold))
            Text(controller.label)
                .font(.caption2.weight(.semibold))
            if live {
                LevelMeter(level: controller.level)
            }
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 10).padding(.vertical, 6)
        .background(background, in: Capsule())
        .overlay(Capsule().stroke(Color.white.opacity(0.25), lineWidth: 1))
        .contentShape(Capsule())
        .gesture(DragGesture(minimumDistance: 0)
            .onChanged { _ in
                if pointerDown == false { pointerDown = true; controller.press() }
            }
            .onEnded { _ in
                pointerDown = false
                controller.release()
            })
        .help("Hold to talk through \(cameraName)'s speaker (or hold T)")
        .accessibilityLabel("Hold to talk through \(cameraName)")
        .accessibilityAddTraits(controller.held ? [.isButton, .isSelected] : .isButton)
    }

    private var background: Color {
        if live { return Color.red.opacity(0.85) }
        if controller.held { return SentinelTheme.amber.opacity(0.85) }
        return Color.black.opacity(0.6)
    }

    // MARK: Keyboard (hold T)

    private func installKeys() {
        removeKeys()
        let controller = controller
        if keyboard {
            keyMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .keyUp]) { event in
                guard let window = event.window, window === NSApp.keyWindow, window.attachedSheet == nil,
                      window.firstResponder is NSText == false, window.firstResponder is NSDatePicker == false,
                      event.modifierFlags.intersection([.command, .option, .control]).isEmpty,
                      TalkKeys.isTalkKey(event.charactersIgnoringModifiers) else { return event }
                if event.type == .keyDown {
                    if event.isARepeat == false { controller.press() }
                } else {
                    controller.release()
                }
                return nil
            }
        }
        // Switching apps mid-press would leave the button held (no key/mouse up comes).
        resignObserver = NotificationCenter.default.addObserver(forName: NSApplication.didResignActiveNotification,
                                                                object: nil, queue: .main) { _ in
            Task { @MainActor in controller.release() }
        }
    }

    private func removeKeys() {
        if let keyMonitor { NSEvent.removeMonitor(keyMonitor) }
        keyMonitor = nil
        if let resignObserver { NotificationCenter.default.removeObserver(resignObserver) }
        resignObserver = nil
    }
}

private enum TalkKeys {
    static func isTalkKey(_ characters: String?) -> Bool {
        characters == "t" || characters == "T"
    }
}

/// Five bars that light with the microphone level.
private struct LevelMeter: View {
    let level: Double

    var body: some View {
        HStack(alignment: .bottom, spacing: 2) {
            ForEach(0..<5, id: \.self) { i in
                let lit = level >= Double(i + 1) / 6
                RoundedRectangle(cornerRadius: 1)
                    .fill(lit ? Color.white : Color.white.opacity(0.3))
                    .frame(width: 3, height: CGFloat(4 + i * 2))
            }
        }
        .frame(height: 12)
        .accessibilityHidden(true)
    }
}
