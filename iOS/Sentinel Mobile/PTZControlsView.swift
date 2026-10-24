// PTZControlsView.swift
// The PTZ button and control bar on the camera detail's Live view and in
// fullscreen (WAVE23 §C PTZ): a D-pad (press and hold = continuous move,
// release = stop), zoom −/+, and a row of presets (tap = go to). Shown only
// when the camera's `GET …/ptz` says it has PTZ and the role may control it.

import SwiftUI

/// The round "PTZ" button beside LIVE / ⤢. Renders nothing for a camera
/// without PTZ.
struct PTZToggleButton: View {
    @ObservedObject var controller: PTZController
    let cameraOnline: Bool
    @Binding var shown: Bool

    var body: some View {
        if PTZAccess.isVisible(state: controller.state, cameraOnline: cameraOnline) {
            Button {
                Haptics.tap()
                shown.toggle()
            } label: {
                Image(systemName: "arrow.up.and.down.and.arrow.left.and.right")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(shown ? Color.black : .white)
                    .padding(7)
                    .background(shown ? AnyShapeStyle(Color.white) : AnyShapeStyle(Color.black.opacity(0.45)), in: Circle())
                    .frame(width: 44, height: 44)
                    .contentShape(Rectangle())
            }
            .accessibilityLabel("PTZ")
            .accessibilityValue(shown ? "Controls shown" : "Controls hidden")
        }
    }
}

/// The control bar.
struct PTZBar: View {
    @ObservedObject var controller: PTZController
    @ObservedObject private var session = SentinelSession.shared

    var body: some View {
        VStack(spacing: 10) {
            HStack(alignment: .center, spacing: 22) {
                dPad
                VStack(spacing: 8) {
                    PTZHoldButton(direction: .zoomIn, controller: controller)
                    PTZHoldButton(direction: .zoomOut, controller: controller)
                }
            }
            if controller.presets.isEmpty == false {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(controller.presets) { preset in
                            Button {
                                controller.goto(preset)
                            } label: {
                                Label(preset.name, systemImage: "scope")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(.white)
                                    .padding(.horizontal, 12).padding(.vertical, 8)
                                    .background(Color.white.opacity(0.12), in: Capsule())
                            }
                            .accessibilityLabel(preset.name)
                            .accessibilityHint("Moves the camera to this preset.")
                        }
                    }
                    .padding(.horizontal, 2)
                }
            }
            if controller.note.isEmpty == false {
                Text(controller.note)
                    .font(.caption2).foregroundStyle(SentinelTheme.amber)
                    .multilineTextAlignment(.center)
                    .onTapGesture { controller.note = "" }
            }
            if session.isDemoMode {
                // Demo Mode sends nothing; say so, and show what would go.
                Text(controller.lastCommand.isEmpty ? "Demo camera: commands aren't sent" : "Demo: \(controller.lastCommand)")
                    .font(.caption2).foregroundStyle(.white.opacity(0.45))
                    .accessibilityIdentifier("ptzLastCommand")
                    .accessibilityLabel(controller.lastCommand)
            }
        }
        .padding(12)
        .background(Color.black.opacity(0.55), in: RoundedRectangle(cornerRadius: 14))
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Pan, tilt and zoom")
    }

    private var dPad: some View {
        VStack(spacing: 4) {
            PTZHoldButton(direction: .up, controller: controller)
            HStack(spacing: 4) {
                PTZHoldButton(direction: .left, controller: controller)
                Color.clear.frame(width: 44, height: 44)
                PTZHoldButton(direction: .right, controller: controller)
            }
            PTZHoldButton(direction: .down, controller: controller)
        }
    }
}

/// Press and hold to move; letting go (or the touch being cancelled) stops.
struct PTZHoldButton: View {
    let direction: PTZDirection
    @ObservedObject var controller: PTZController
    /// True while a finger is down. `@GestureState` also resets when the
    /// system cancels the touch, so a release is never missed.
    @GestureState private var pressing = false

    var body: some View {
        Image(systemName: direction.symbol)
            .font(.body.weight(.bold))
            .foregroundStyle(lit ? Color.black : .white)
            .frame(width: 44, height: 44)
            .background(lit ? AnyShapeStyle(Color.white) : AnyShapeStyle(Color.white.opacity(0.14)), in: Circle())
            .contentShape(Circle())
            .gesture(
                DragGesture(minimumDistance: 0)
                    .updating($pressing) { _, state, _ in state = true }
            )
            .onChange(of: pressing) { down in
                if down { controller.press(direction) } else { controller.release(direction) }
            }
            .accessibilityElement()
            .accessibilityLabel(direction.label)
            .accessibilityAddTraits(.isButton)
            .accessibilityHint("Hold to move the camera.")
            // VoiceOver can't hold: a double-tap nudges for half a second.
            .accessibilityAction {
                controller.press(direction)
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { controller.release(direction) }
            }
    }

    private var lit: Bool { pressing || controller.lit == direction }
}

/// Probes PTZ when the view appears and always stops the camera when the bar
/// closes, the view goes away or the app leaves the foreground.
struct PTZLifecycle: ViewModifier {
    let controller: PTZController
    let shown: Bool
    @Environment(\.scenePhase) private var scenePhase

    func body(content: Content) -> some View {
        content
            .task { await controller.probe() }
            .onDisappear { controller.halt() }
            .onChange(of: shown) { isShown in if isShown == false { controller.halt() } }
            .onChange(of: scenePhase) { phase in if phase != .active { controller.halt() } }
            .onReceive(NotificationCenter.default.publisher(for: UIApplication.willResignActiveNotification)) { _ in
                controller.halt()
            }
    }
}

extension View {
    func ptzLifecycle(_ controller: PTZController, shown: Bool) -> some View {
        modifier(PTZLifecycle(controller: controller, shown: shown))
    }
}
