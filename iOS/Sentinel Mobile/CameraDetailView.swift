// CameraDetailView.swift
// Fullscreen-capable camera with a zoomable/PiP live player and a scrubbable
// playback timeline (recorded segments + AI event markers).

import SwiftUI
import AVKit

struct CameraDetailView: View {
    let initialCamera: SentinelCameraSummary
    @ObservedObject private var store = AppStore.shared
    @State private var tab: Tab = .live
    @State private var fullscreen = false
    @State private var detail: SentinelCameraDetail?

    enum Tab: String, CaseIterable { case live, playback }

    init(camera: SentinelCameraSummary) { self.initialCamera = camera }

    /// Resolve the freshest camera from the shared store each render so the
    /// status / REC / resolution chips track background refreshes; fall back to
    /// the value that was pushed onto the navigation stack.
    private var camera: SentinelCameraSummary {
        store.camera(id: initialCamera.id) ?? initialCamera
    }

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            VStack(spacing: 0) {
                metadataStrip
                healthStrip
                Picker("", selection: $tab) {
                    ForEach(Tab.allCases, id: \.self) { Text($0.rawValue.capitalized).tag($0) }
                }
                .pickerStyle(.segmented)
                .padding(SentinelTheme.Space.md)

                switch tab {
                case .live:     liveContent
                case .playback: PlaybackTimelineView(camera: camera)
                }
            }
        }
        .navigationTitle(camera.name)
        .navigationBarTitleDisplayMode(.inline)
        // Home hides its nav bar; force this pushed view's bar visible so the
        // back button always shows (guards the iOS 16 toolbar-hidden quirk).
        .toolbar(.visible, for: .navigationBar)
        .task(id: camera.id) {
            detail = try? await SentinelSession.shared.cameraDetail(cameraID: camera.id)
        }
        .fullScreenCover(isPresented: $fullscreen) {
            FullscreenPlayer(url: SentinelSession.shared.hlsURL(for: camera), isLive: true)
        }
    }

    @ViewBuilder
    private var healthStrip: some View {
        if let d = detail {
            HStack(spacing: 14) {
                healthItem("bolt.fill", String(format: "%.0f fps", d.estimatedFPS))
                healthItem("film.stack", "\(d.segmentCount) clip\(d.segmentCount == 1 ? "" : "s")")
                healthItem("sparkles", "\(d.eventCount) event\(d.eventCount == 1 ? "" : "s")")
                Spacer()
                if let err = d.lastError, err.isEmpty == false {
                    Label(err, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption2).foregroundStyle(SentinelTheme.amber).lineLimit(1)
                }
            }
            .padding(.horizontal, SentinelTheme.Space.lg)
            .padding(.vertical, SentinelTheme.Space.sm)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(SentinelTheme.panel)
            .overlay(alignment: .bottom) { Rectangle().fill(SentinelTheme.line).frame(height: 1) }
        }
    }

    private func healthItem(_ icon: String, _ text: String) -> some View {
        HStack(spacing: 4) {
            Image(systemName: icon).font(.caption2)
            Text(text).font(.caption2.weight(.medium))
        }
        .foregroundStyle(.white.opacity(0.6))
    }

    private var metadataStrip: some View {
        HStack(spacing: 10) {
            SentinelStatusBadge(status: camera.status)
            if camera.isRecording {
                Chip(text: "REC", systemImage: "record.circle", color: SentinelTheme.alarm)
            }
            Spacer()
            Text("\(camera.resolution) · \(camera.fps)fps")
                .font(.caption.weight(.medium)).foregroundStyle(.white.opacity(0.5))
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.vertical, SentinelTheme.Space.md)
        .background(SentinelTheme.chrome)
        .overlay(alignment: .bottom) { Rectangle().fill(SentinelTheme.line).frame(height: 1) }
    }

    @ViewBuilder
    private var liveContent: some View {
        if let url = SentinelSession.shared.hlsURL(for: camera) {
            VStack(spacing: 0) {
                ZStack(alignment: .topTrailing) {
                    ZoomableVideoView(url: url, isLive: true, isMuted: true)
                        .aspectRatio(16/9, contentMode: .fit)
                        .frame(maxWidth: .infinity)
                        .background(.black)
                    HStack(spacing: 6) {
                        Chip(text: "LIVE", color: SentinelTheme.alarm, filled: true)
                        Button { Haptics.tap(); fullscreen = true } label: {
                            Image(systemName: "arrow.up.left.and.arrow.down.right")
                                .font(.caption.weight(.bold)).foregroundStyle(.white)
                                .padding(7).background(.black.opacity(0.45), in: Circle())
                        }
                    }
                    .padding(SentinelTheme.Space.sm)
                }
                Text("Pinch to zoom · double-tap to reset · tap ⤢ for fullscreen")
                    .font(.caption2).foregroundStyle(.white.opacity(0.4))
                    .padding(.top, SentinelTheme.Space.sm)
                Spacer()
            }
        } else {
            noStream
        }
    }

    private var noStream: some View {
        VStack(spacing: 12) {
            Image(systemName: "video.slash.fill").font(.system(size: 48)).foregroundStyle(.white.opacity(0.35))
            Text("No live stream").font(.headline).foregroundStyle(.white)
            Text("This camera is reachable but the HLS feed isn't available right now.")
                .font(.caption).foregroundStyle(.white.opacity(0.55))
                .multilineTextAlignment(.center).padding(.horizontal, 30)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

// MARK: - Zoomable / PiP video

/// AVPlayerLayer-backed view with pinch + pan zoom, double-tap reset, and a
/// Picture-in-Picture controller. Keeps glass-to-glass latency low for live.
struct ZoomableVideoView: UIViewRepresentable {
    let url: URL
    var isLive: Bool
    var isMuted: Bool = false

    func makeUIView(context: Context) -> ZoomPlayerView {
        let v = ZoomPlayerView()
        v.configure(url: url, isLive: isLive, isMuted: isMuted)
        return v
    }
    func updateUIView(_ uiView: ZoomPlayerView, context: Context) {}
}

final class ZoomPlayerView: UIView, UIGestureRecognizerDelegate {
    override class var layerClass: AnyClass { AVPlayerLayer.self }
    private var playerLayer: AVPlayerLayer { layer as! AVPlayerLayer }
    private var pipController: AVPictureInPictureController?
    private var scale: CGFloat = 1

    func configure(url: URL, isLive: Bool, isMuted: Bool) {
        let item = AVPlayerItem(url: url)
        item.preferredForwardBufferDuration = 1.0
        if isLive, #available(iOS 16.0, *) {
            item.configuredTimeOffsetFromLive = CMTime(seconds: 1, preferredTimescale: 600)
        }
        let player = AVPlayer(playerItem: item)
        player.isMuted = isMuted
        player.automaticallyWaitsToMinimizeStalling = !isLive
        playerLayer.player = player
        playerLayer.videoGravity = .resizeAspect
        player.play()

        // NOTE: deliberately do NOT take an AVAudioSession (.playback) here.
        // Every player in this app is muted, so claiming the audio session only
        // ducks the user's music and pairs with a background-audio mode we don't
        // ship (App Store guideline 2.5.4). Inline PiP can still be offered while
        // the app is foreground; it just won't continue in the background.
        if AVPictureInPictureController.isPictureInPictureSupported() {
            pipController = AVPictureInPictureController(playerLayer: playerLayer)
            pipController?.canStartPictureInPictureAutomaticallyFromInline = true
        }

        let pinch = UIPinchGestureRecognizer(target: self, action: #selector(onPinch(_:)))
        let pan = UIPanGestureRecognizer(target: self, action: #selector(onPan(_:)))
        let doubleTap = UITapGestureRecognizer(target: self, action: #selector(onDoubleTap))
        doubleTap.numberOfTapsRequired = 2
        pan.delegate = self
        [pinch, pan, doubleTap].forEach { addGestureRecognizer($0) }
        isUserInteractionEnabled = true
    }

    @objc private func onPinch(_ g: UIPinchGestureRecognizer) {
        if g.state == .changed {
            scale = min(max(scale * g.scale, 1), 5)
            playerLayer.setAffineTransform(CGAffineTransform(scaleX: scale, y: scale))
            g.scale = 1
        }
    }
    @objc private func onPan(_ g: UIPanGestureRecognizer) {
        guard scale > 1 else { return }
        let t = g.translation(in: self)
        playerLayer.setAffineTransform(playerLayer.affineTransform().translatedBy(x: t.x / scale, y: t.y / scale))
        g.setTranslation(.zero, in: self)
    }
    @objc private func onDoubleTap() {
        scale = 1
        UIView.animate(withDuration: 0.2) { self.playerLayer.setAffineTransform(.identity) }
    }
    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool { true }
}

/// Simple immersive fullscreen player presented as a cover.
struct FullscreenPlayer: View {
    let url: URL?
    var isLive: Bool
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            if let url {
                ZoomableVideoView(url: url, isLive: isLive, isMuted: true)
                    .ignoresSafeArea()
            }
            VStack {
                HStack {
                    Spacer()
                    Button { dismiss() } label: {
                        Image(systemName: "xmark")
                            .font(.headline).foregroundStyle(.white)
                            .padding(12).background(.black.opacity(0.5), in: Circle())
                    }
                }
                Spacer()
            }
            .padding()
        }
    }
}

// MARK: - Playback timeline

@MainActor
final class PlaybackViewModel: ObservableObject {
    @Published var segments: [SentinelSegment] = []
    @Published var error: String?
    let cameraID: UUID
    init(cameraID: UUID) { self.cameraID = cameraID }

    func load() async {
        do {
            let to = Date()
            let from = to.addingTimeInterval(-24 * 3600)
            segments = try await SentinelSession.shared.segments(cameraID: cameraID, from: from, to: to)
                .sorted { $0.createdAt < $1.createdAt }
        } catch { self.error = error.localizedDescription }
    }
}

struct PlaybackTimelineView: View {
    let camera: SentinelCameraSummary
    @StateObject private var model: PlaybackViewModel
    @ObservedObject private var store = AppStore.shared
    @State private var selected: SentinelSegment?

    init(camera: SentinelCameraSummary) {
        self.camera = camera
        _model = StateObject(wrappedValue: PlaybackViewModel(cameraID: camera.id))
    }

    private var events: [SentinelEvent] { store.events.filter { $0.cameraID == camera.id } }

    var body: some View {
        VStack(spacing: 0) {
            // Player area
            ZStack {
                Color.black
                if let selected, let url = SentinelSession.shared.segmentURL(cameraID: camera.id, name: selected.name) {
                    ZoomableVideoView(url: url, isLive: false)
                } else {
                    VStack(spacing: 8) {
                        Image(systemName: "timeline.selection").font(.largeTitle).foregroundStyle(.white.opacity(0.3))
                        Text(model.segments.isEmpty ? "No recordings in the last 24h" : "Scrub the timeline to play")
                            .font(.caption).foregroundStyle(.white.opacity(0.5))
                    }
                }
            }
            .aspectRatio(16/9, contentMode: .fit)
            .frame(maxWidth: .infinity)

            if let error = model.error {
                Text(error).font(.caption).foregroundStyle(SentinelTheme.alarm).padding(SentinelTheme.Space.md)
            }

            TimelineScrubber(segments: model.segments, events: events, selected: $selected)
                .frame(height: 96)
                .padding(SentinelTheme.Space.md)

            Spacer()
        }
        .task { await model.load() }
    }
}

/// Horizontal 24-hour scrubber: recorded segments as bars, AI events as ticks,
/// a draggable playhead that selects (and plays) the segment under it.
struct TimelineScrubber: View {
    let segments: [SentinelSegment]
    let events: [SentinelEvent]
    @Binding var selected: SentinelSegment?

    private let windowHours: Double = 24

    var body: some View {
        GeometryReader { geo in
            let w = geo.size.width
            let start = Date().addingTimeInterval(-windowHours * 3600)

            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 8).fill(SentinelTheme.panel)
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(SentinelTheme.line, lineWidth: 1))

                // Hour gridlines (every 6h)
                ForEach([0, 6, 12, 18, 24], id: \.self) { h in
                    Rectangle().fill(.white.opacity(0.08)).frame(width: 1)
                        .offset(x: CGFloat(Double(h) / windowHours) * w)
                }

                // Recorded segment bars
                ForEach(segments) { seg in
                    let sx = xPos(for: seg.createdAtDate, start: start, width: w)
                    let ex = xPos(for: seg.modifiedAtDate, start: start, width: w)
                    RoundedRectangle(cornerRadius: 2)
                        .fill(SentinelTheme.accent.opacity(selected?.id == seg.id ? 0.95 : 0.6))
                        .frame(width: max(2, ex - sx), height: 26)
                        .offset(x: sx, y: 0)
                }

                // Event ticks
                ForEach(events) { ev in
                    Rectangle().fill(SentinelTheme.kindColor(ev.kind))
                        .frame(width: 2, height: 40)
                        .offset(x: xPos(for: ev.createdAtDate, start: start, width: w), y: 22)
                }
            }
            .contentShape(Rectangle())
            .gesture(
                DragGesture(minimumDistance: 0).onEnded { value in
                    let frac = max(0, min(1, value.location.x / w))
                    let target = start.addingTimeInterval(frac * windowHours * 3600)
                    if let seg = segmentContaining(target) ?? nearestSegment(to: target) {
                        Haptics.tap(); selected = seg
                    }
                }
            )
        }
    }

    private func xPos(for date: Date, start: Date, width: CGFloat) -> CGFloat {
        CGFloat(date.timeIntervalSince(start) / (windowHours * 3600)) * width
    }

    private func segmentContaining(_ date: Date) -> SentinelSegment? {
        segments.first { $0.createdAtDate <= date && date <= $0.modifiedAtDate }
    }
    private func nearestSegment(to date: Date) -> SentinelSegment? {
        segments.min { abs($0.createdAt - date.timeIntervalSince1970) < abs($1.createdAt - date.timeIntervalSince1970) }
    }
}
