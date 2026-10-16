import AppKit
import ObjectiveC
import SwiftUI
import SentinelCore

// PTZ controls over the focused live tile: press-and-hold direction arrows
// and zoom +/-, arrow keys and +/- while the tile is focused, and a presets
// menu. Renders nothing unless the camera's `GET …/ptz` probe (cached once
// per camera for the session) says it supports PTZ.

extension SiteRuntime {
    private static var probeKey: UInt8 = 0

    /// The session's PTZ probe cache. It lives and dies with this runtime
    /// (one per sign-in), so a new sign-in probes afresh.
    var ptzProbes: PTZProbeCache {
        if let cache = objc_getAssociatedObject(self, &Self.probeKey) as? PTZProbeCache { return cache }
        let cache = PTZProbeCache(api: api)
        objc_setAssociatedObject(self, &Self.probeKey, cache, .OBJC_ASSOCIATION_RETAIN_NONATOMIC)
        return cache
    }
}

/// Sends PTZ commands for one camera, one at a time so a stop can never
/// overtake a move, and tracks which controls are held.
@MainActor
final class PTZController: ObservableObject {
    @Published private(set) var presets: [V2.PTZPreset] = []
    @Published private(set) var supported = false
    @Published private(set) var lit: PTZDirection?
    @Published var note = ""

    private let api: SentinelAPI
    private let cache: PTZProbeCache
    let cameraID: String
    private var hold = PTZHoldState()
    private var queue: Task<Void, Never> = Task {}

    init(cameraID: String, api: SentinelAPI, cache: PTZProbeCache) {
        self.cameraID = cameraID
        self.api = api
        self.cache = cache
    }

    func probe() async {
        let state = await cache.state(for: cameraID)
        supported = state != nil
        presets = state?.presets ?? []
    }

    func press(_ d: PTZDirection) { apply(hold.press(d)) }
    func release(_ d: PTZDirection) { apply(hold.release(d)) }
    func releaseAll() { apply(hold.releaseAll()) }

    private func apply(_ command: PTZCommand?) {
        guard let command else { return }
        lit = hold.last
        enqueue { [api, cameraID] in
            switch command {
            case .move(let move): try await api.ptzMove(cameraID: cameraID, move)
            case .stop: try await api.ptzStop(cameraID: cameraID)
            }
        }
    }

    func goto(_ preset: V2.PTZPreset) {
        enqueue { [api, cameraID] in try await api.ptzGoto(cameraID: cameraID, presetToken: preset.token) }
    }

    func save(name: String) async -> Bool {
        guard let clean = PTZInput.cleanPresetName(name) else {
            note = "Give the preset a name (up to 64 characters)."
            return false
        }
        do {
            let preset = try await api.ptzSavePreset(cameraID: cameraID, name: clean)
            presets.append(preset)
            await cache.update(presets: presets, for: cameraID)
            note = ""
            return true
        } catch {
            note = error.localizedDescription
            return false
        }
    }

    func delete(_ preset: V2.PTZPreset) async {
        do {
            try await api.ptzDeletePreset(cameraID: cameraID, presetToken: preset.token)
            presets.removeAll { $0.token == preset.token }
            await cache.update(presets: presets, for: cameraID)
        } catch {
            note = error.localizedDescription
        }
    }

    private func enqueue(_ work: @escaping @Sendable () async throws -> Void) {
        let previous = queue
        queue = Task { [weak self] in
            await previous.value
            do { try await work() } catch {
                await MainActor.run { self?.note = error.localizedDescription }
            }
        }
    }
}

struct PTZOverlayView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var session: OperatorSession
    let camera: V2.Camera
    /// True while this tile is the focused one: arrow keys and +/- steer it.
    var keyboard = true

    var body: some View {
        // The controller is per camera; rebuilt (and re-probed from the cache) when the tile changes.
        PTZControls(controller: PTZController(cameraID: camera.id, api: runtime.api, cache: runtime.ptzProbes),
                    mayEditPresets: session.can(.controlPtz) || session.me == nil,
                    keyboard: keyboard)
            .id(camera.id)
    }
}

private struct PTZControls: View {
    @StateObject var controller: PTZController
    let mayEditPresets: Bool
    let keyboard: Bool

    @State private var keyMonitor: Any?
    @State private var showSave = false
    @State private var presetName = ""
    @State private var pendingDelete: V2.PTZPreset?

    init(controller: @autoclosure @escaping () -> PTZController, mayEditPresets: Bool, keyboard: Bool) {
        _controller = StateObject(wrappedValue: controller())
        self.mayEditPresets = mayEditPresets
        self.keyboard = keyboard
    }

    var body: some View {
        Group {
            if controller.supported {
                panel
            } else {
                Color.clear.frame(width: 0, height: 0)
            }
        }
        .task { await controller.probe() }
        .onAppear { installKeys() }
        .onDisappear { removeKeys(); controller.releaseAll() }
        .onChange(of: keyboard) { _ in
            removeKeys()
            controller.releaseAll()
            installKeys()
        }
        .onChange(of: controller.supported) { _ in installKeys() }
    }

    private var panel: some View {
        VStack(alignment: .trailing, spacing: 6) {
            if controller.note.isEmpty == false {
                Text(controller.note)
                    .font(.caption2).foregroundStyle(.white).lineLimit(3)
                    .padding(6).frame(maxWidth: 180, alignment: .leading)
                    .background(Color.red.opacity(0.75), in: RoundedRectangle(cornerRadius: 6))
                    .onTapGesture { controller.note = "" }
            }
            HStack(alignment: .center, spacing: 8) {
                VStack(spacing: 2) {
                    holdButton(.up, "chevron.up", "Tilt up (↑)")
                    HStack(spacing: 2) {
                        holdButton(.left, "chevron.left", "Pan left (←)")
                        Color.clear.frame(width: 22, height: 22)
                        holdButton(.right, "chevron.right", "Pan right (→)")
                    }
                    holdButton(.down, "chevron.down", "Tilt down (↓)")
                }
                VStack(spacing: 2) {
                    holdButton(.zoomIn, "plus", "Zoom in (+)")
                    holdButton(.zoomOut, "minus", "Zoom out (−)")
                }
            }
            presetsMenu
        }
        .padding(8)
        .background(.black.opacity(0.55), in: RoundedRectangle(cornerRadius: 10))
        .padding(.trailing, 8)
        // Keep clicks on the controls from reaching the tile (selection, drag).
        .onTapGesture {}
    }

    private func holdButton(_ d: PTZDirection, _ symbol: String, _ help: String) -> some View {
        PTZHoldButton(symbol: symbol, lit: controller.lit == d,
                      press: { controller.press(d) }, release: { controller.release(d) })
            .help(help)
            .accessibilityLabel(help)
    }

    private var presetsMenu: some View {
        Menu {
            if controller.presets.isEmpty {
                Text("No presets yet")
            }
            ForEach(controller.presets) { preset in
                Button(preset.name) { controller.goto(preset) }
            }
            if mayEditPresets {
                Divider()
                Button("Save Current Position…") { presetName = ""; showSave = true }
                if controller.presets.isEmpty == false {
                    Menu("Delete Preset") {
                        ForEach(controller.presets) { preset in
                            Button(preset.name, role: .destructive) { pendingDelete = preset }
                        }
                    }
                }
            }
        } label: {
            Label("Presets", systemImage: "scope").font(.caption2.weight(.semibold))
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .foregroundStyle(.white)
        .popover(isPresented: $showSave, arrowEdge: .leading) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Save position as").font(.headline)
                TextField("Preset name", text: $presetName)
                    .textFieldStyle(.roundedBorder).frame(width: 200)
                    .onSubmit { savePreset() }
                HStack {
                    Spacer()
                    Button("Cancel") { showSave = false }
                    Button("Save") { savePreset() }.keyboardShortcut(.defaultAction)
                }
            }
            .padding(12)
        }
        .confirmationDialog("Delete preset \u{201C}\(pendingDelete?.name ?? "")\u{201D}?",
                            isPresented: Binding(get: { pendingDelete != nil }, set: { if $0 == false { pendingDelete = nil } }),
                            titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                if let preset = pendingDelete { Task { await controller.delete(preset) } }
                pendingDelete = nil
            }
        }
    }

    private func savePreset() {
        Task {
            if await controller.save(name: presetName) { showSave = false }
        }
    }

    // MARK: Keyboard

    private func installKeys() {
        removeKeys()
        guard keyboard, controller.supported else { return }
        let controller = controller
        keyMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .keyUp]) { event in
            guard let window = event.window, window === NSApp.keyWindow, window.attachedSheet == nil,
                  window.firstResponder is NSText == false, window.firstResponder is NSDatePicker == false,
                  event.modifierFlags.intersection([.command, .option, .control]).isEmpty,
                  let d = PTZInput.direction(keyCode: event.keyCode, characters: event.charactersIgnoringModifiers) else { return event }
            if event.type == .keyDown {
                if event.isARepeat == false { controller.press(d) }
            } else {
                controller.release(d)
            }
            return nil
        }
    }

    private func removeKeys() {
        if let keyMonitor { NSEvent.removeMonitor(keyMonitor) }
        keyMonitor = nil
    }
}

/// A button that is "down" for as long as the pointer is held on it.
private struct PTZHoldButton: View {
    let symbol: String
    let lit: Bool
    let press: () -> Void
    let release: () -> Void
    @State private var down = false

    var body: some View {
        Image(systemName: symbol)
            .font(.system(size: 10, weight: .bold))
            .foregroundStyle(.white)
            .frame(width: 22, height: 22)
            .background((lit || down) ? SentinelTheme.accent : Color.white.opacity(0.18), in: RoundedRectangle(cornerRadius: 5))
            .contentShape(Rectangle())
            .gesture(DragGesture(minimumDistance: 0)
                .onChanged { _ in
                    if down == false { down = true; press() }
                }
                .onEnded { _ in
                    down = false
                    release()
                })
    }
}
