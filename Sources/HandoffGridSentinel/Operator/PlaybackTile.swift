import AppKit
import AVFoundation
import SwiftUI
import SentinelCore

struct PlaybackTile: View {
    @ObservedObject var lane: PlaybackLane
    let camera: V2.Camera?
    let error: String?
    var isSelected = false

    var body: some View {
        ZStack {
            Color.black
            OperatorPlayerView(player: lane.player)
            switch lane.state {
            case .loading:
                ProgressView().controlSize(.small).tint(.white)
            case .gap:
                label("No recording at this time", symbol: "film.stack")
            case .failed(let message):
                label(message, symbol: "exclamationmark.triangle.fill")
            case .idle, .playing:
                EmptyView()
            }
            if let error {
                label(error, symbol: "exclamationmark.triangle.fill")
            }
        }
        .overlay(alignment: .topLeading) {
            Text(camera?.name ?? "Camera")
                .font(.system(size: 11, weight: .semibold))
                .foregroundStyle(.white)
                .padding(.horizontal, 8).padding(.vertical, 4)
                .background(.black.opacity(0.55), in: RoundedRectangle(cornerRadius: 4))
                .padding(8)
        }
        .overlay(alignment: .bottomTrailing) {
            if lane.route.isEmpty == false {
                MetricPill(text: lane.route.uppercased()).padding(8)
            }
        }
        .overlay {
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                .stroke(isSelected ? SentinelTheme.accent : Color.white.opacity(0.08), lineWidth: isSelected ? 2 : 1)
        }
        .clipShape(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .contentShape(Rectangle())
    }

    private func label(_ text: String, symbol: String) -> some View {
        VStack(spacing: 6) {
            Image(systemName: symbol).foregroundStyle(SentinelTheme.amber)
            Text(text).font(.caption2.weight(.semibold)).foregroundStyle(.white).multilineTextAlignment(.center).lineLimit(3)
        }
        .padding(10)
        .background(.black.opacity(0.7), in: RoundedRectangle(cornerRadius: 6))
    }
}

/// An `AVPlayerLayer` for a SwiftUI view.
struct OperatorPlayerView: NSViewRepresentable {
    let player: AVPlayer

    func makeNSView(context: Context) -> OperatorPlayerNSView {
        let view = OperatorPlayerNSView()
        view.playerLayer.player = player
        return view
    }

    func updateNSView(_ view: OperatorPlayerNSView, context: Context) {
        if view.playerLayer.player !== player { view.playerLayer.player = player }
    }
}

final class OperatorPlayerNSView: NSView {
    let playerLayer = AVPlayerLayer()

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        playerLayer.videoGravity = .resizeAspect
        playerLayer.backgroundColor = NSColor.black.cgColor
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override func makeBackingLayer() -> CALayer { playerLayer }
}
