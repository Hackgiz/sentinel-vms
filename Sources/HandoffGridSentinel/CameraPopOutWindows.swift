import SwiftUI
import AppKit
import SentinelCore

/// Pop-out camera windows: one free-floating, resizable window per camera so an
/// operator can park a feed on a second display while the main grid keeps
/// running. Re-opening a camera that already has a window just brings it front.
@MainActor
final class CameraPopOutWindows: NSObject, NSWindowDelegate {
    static let shared = CameraPopOutWindows()

    private var windows: [UUID: NSWindow] = [:]

    func open(_ camera: CameraFeed) {
        if let existing = windows[camera.id] {
            existing.makeKeyAndOrderFront(nil)
            return
        }

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 800, height: 450),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = camera.name
        window.contentAspectRatio = NSSize(width: 16, height: 9)
        window.minSize = NSSize(width: 320, height: 180)
        window.isReleasedWhenClosed = false
        window.collectionBehavior.insert(.fullScreenPrimary)
        window.delegate = self
        window.contentView = NSHostingView(
            rootView: CameraPopOutView(cameraID: camera.id)
                .sentinelEnvironment(SentinelAppDependencies.shared)
        )
        window.center()
        window.makeKeyAndOrderFront(nil)
        windows[camera.id] = window

        SentinelAppDependencies.shared.workflowStore.recordAudit(
            area: "Live View", action: "Popped out camera", detail: camera.name
        )
    }

    func closeAll() {
        windows.values.forEach { $0.close() }
    }

    func windowWillClose(_ notification: Notification) {
        guard let window = notification.object as? NSWindow else { return }
        windows = windows.filter { $0.value !== window }
    }
}

/// Content of a pop-out window. Owns its own playback controller so scrubbing
/// in the main grid doesn't yank the pop-out off live (and vice versa).
private struct CameraPopOutView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @StateObject private var playback = MonitoringPlaybackController()
    @State private var keepOnTop = false
    let cameraID: UUID

    var body: some View {
        Group {
            if let camera = cameraStore.cameras.first(where: { $0.id == cameraID }) {
                CameraTile(camera: camera, isSelected: true, contentMode: .fit)
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
                EmptyStateLine(text: "This camera was removed.")
            }
        }
        .environmentObject(playback)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color.black)
    }
}
