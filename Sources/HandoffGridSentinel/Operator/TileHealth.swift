import SwiftUI
import SentinelCore

/// Small glyph next to a tile's REC badge: the kind of the camera's latest
/// detection (person, vehicle, animal, motion, other). It pulses and fades
/// over ~5 s, driven by `ActivityTracker` (fed from the event stream, no polling).
struct TileActivityGlyph: View {
    @ObservedObject var tracker: ActivityTracker
    let cameraID: String

    @State private var shown: ActivityTracker.Entry?
    @State private var opacity = 0.0
    @State private var pulsing = false

    var body: some View {
        Group {
            if let shown {
                Image(systemName: shown.kind.symbol)
                    .font(.system(size: 9, weight: .bold))
                    .foregroundStyle(.white)
                    .padding(5)
                    .background(SentinelTheme.accent.opacity(0.85), in: Circle())
                    .scaleEffect(pulsing ? 1.2 : 0.95)
                    .opacity(opacity)
                    .help("\(shown.kind.label) detected")
                    .accessibilityLabel("\(shown.kind.label) detected")
            }
        }
        .task(id: tracker.latest[cameraID]) {
            guard let entry = tracker.active(cameraID) else {
                shown = nil
                return
            }
            let left = ActivityTracker.fadeSeconds - Date().timeIntervalSince(entry.at)
            shown = entry
            pulsing = false
            withTransaction(Transaction(animation: nil)) { opacity = 1 }
            withAnimation(.easeInOut(duration: 0.6).repeatForever(autoreverses: true)) { pulsing = true }
            withAnimation(.linear(duration: max(left, 0.1))) { opacity = 0 }
            try? await Task.sleep(nanoseconds: UInt64(max(left, 0.1) * 1_000_000_000))
            guard Task.isCancelled == false else { return }
            withTransaction(Transaction(animation: nil)) { pulsing = false }
            shown = nil
        }
    }
}

/// Amber border and "reconnecting…" caption while the camera is offline.
struct TileReconnectingOverlay: View {
    let isOffline: Bool

    var body: some View {
        if isOffline {
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                .stroke(SentinelTheme.amber, lineWidth: 2)
                .overlay(alignment: .top) {
                    Text("reconnecting…")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundStyle(.black)
                        .padding(.horizontal, 7).padding(.vertical, 2)
                        .background(SentinelTheme.amber, in: Capsule())
                        .padding(.top, 36)
                }
                .allowsHitTesting(false)
        }
    }
}
