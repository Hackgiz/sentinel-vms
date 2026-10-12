// CameraDetailView.swift
// Fullscreen-capable camera with a zoomable/PiP live player and a scrubbable
// playback timeline (recorded spans + detection and alarm markers) from the
// v2 timeline API, following the footage across Recording Servers.

import SwiftUI
import AVKit

struct CameraDetailView: View {
    let initialCamera: V2.Camera
    @ObservedObject private var store = AppStore.shared
    @State private var tab: Tab = .live
    @State private var fullscreen = false

    enum Tab: String, CaseIterable { case live, playback }

    init(camera: V2.Camera) { self.initialCamera = camera }

    /// Resolve the freshest camera from the shared store each render so the
    /// status / REC / resolution chips track live updates; fall back to the
    /// value that was pushed onto the navigation stack.
    private var camera: V2.Camera {
        store.camera(id: initialCamera.id) ?? initialCamera
    }

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            VStack(spacing: 0) {
                metadataStrip
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
        .fullScreenCover(isPresented: $fullscreen) {
            FullscreenPlayer(camera: camera)
        }
    }

    private var metadataStrip: some View {
        HStack(spacing: 10) {
            SentinelStatusBadge(status: camera.statusLabel)
            if camera.recording && camera.isOnline {
                Chip(text: "REC", systemImage: "record.circle", color: SentinelTheme.alarm)
            }
            if camera.status.viewers > 1 {
                Chip(text: "\(camera.status.viewers) watching", systemImage: "eye.fill", color: .gray)
            }
            Spacer()
            Text(camera.status.message == "Online" ? camera.resolutionLabel : camera.status.message)
                .font(.caption.weight(.medium)).foregroundStyle(.white.opacity(0.5))
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.vertical, SentinelTheme.Space.md)
        .background(SentinelTheme.chrome)
        .overlay(alignment: .bottom) { Rectangle().fill(SentinelTheme.line).frame(height: 1) }
    }

    @ViewBuilder
    private var liveContent: some View {
        VStack(spacing: 0) {
            ZStack(alignment: .topTrailing) {
                LiveStreamView(camera: camera, zoomable: true)
                    .aspectRatio(16/9, contentMode: .fit)
                    .frame(maxWidth: .infinity)
                    .background(.black)
                if camera.isOnline {
                    HStack(spacing: 6) {
                        Chip(text: "LIVE", color: SentinelTheme.alarm, filled: true)
                        Button { Haptics.tap(); fullscreen = true } label: {
                            Image(systemName: "arrow.up.left.and.arrow.down.right")
                                .font(.caption.weight(.bold)).foregroundStyle(.white)
                                .padding(7).background(.black.opacity(0.45), in: Circle())
                        }
                        .accessibilityLabel("Fullscreen")
                    }
                    .padding(SentinelTheme.Space.sm)
                }
            }
            if camera.isOnline {
                Text("Pinch to zoom · double-tap to reset · tap ⤢ for fullscreen")
                    .font(.caption2).foregroundStyle(.white.opacity(0.4))
                    .padding(.top, SentinelTheme.Space.sm)
            }
            Spacer()
        }
    }
}

// MARK: - Zoomable / PiP video

/// AVPlayerLayer-backed view with pinch + pan zoom, double-tap reset, and a
/// Picture-in-Picture controller.
struct ZoomableVideoView: UIViewRepresentable {
    let player: AVPlayer

    func makeUIView(context: Context) -> ZoomPlayerView {
        let v = ZoomPlayerView()
        v.configure(player: player)
        return v
    }
    func updateUIView(_ uiView: ZoomPlayerView, context: Context) {
        if uiView.playerLayer.player !== player { uiView.playerLayer.player = player }
    }
}

final class ZoomPlayerView: UIView, UIGestureRecognizerDelegate {
    override class var layerClass: AnyClass { AVPlayerLayer.self }
    var playerLayer: AVPlayerLayer { layer as! AVPlayerLayer }
    private var pipController: AVPictureInPictureController?
    private var scale: CGFloat = 1

    func configure(player: AVPlayer) {
        playerLayer.player = player
        playerLayer.videoGravity = .resizeAspect
        backgroundColor = .black

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

/// Simple immersive fullscreen live player presented as a cover.
struct FullscreenPlayer: View {
    let camera: V2.Camera
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            LiveStreamView(camera: camera, zoomable: true)
                .ignoresSafeArea()
            VStack {
                HStack {
                    Spacer()
                    Button { dismiss() } label: {
                        Image(systemName: "xmark")
                            .font(.headline).foregroundStyle(.white)
                            .padding(12).background(.black.opacity(0.5), in: Circle())
                    }
                    .accessibilityLabel("Close")
                }
                Spacer()
            }
            .padding()
        }
    }
}

// MARK: - Playback

/// Recorded playback for one camera: the last 24 hours from the timeline API,
/// played from the Recording Server that holds the footage at the playhead
/// (`Span.recorderId`) with a playback-right video token.
@MainActor
final class PlaybackModel: ObservableObject {
    @Published private(set) var timeline: V2.Timeline?
    /// Playhead, Unix ms.
    @Published private(set) var position: UnixMillis = 0
    @Published private(set) var isPlaying = false
    @Published private(set) var isLoading = false
    @Published private(set) var error: String?
    @Published private(set) var route = ""
    /// True when nothing was recorded at the playhead.
    @Published private(set) var inGap = false
    let player = AVPlayer()
    let cameraID: String

    private var loadedRecorder: String??
    private var endpoint: VideoEndpoint?
    private var loadTask: Task<Void, Never>?
    private var statusObserver: NSKeyValueObservation?
    private var timeObserver: Any?
    private var endObserver: NSObjectProtocol?
    private var forceGateway = false
    private var scrubbing = false

    static let windowHours = 24.0

    init(cameraID: String) {
        self.cameraID = cameraID
        player.isMuted = true
        player.automaticallyWaitsToMinimizeStalling = true
        timeObserver = player.addPeriodicTimeObserver(forInterval: CMTime(seconds: 0.5, preferredTimescale: 600), queue: .main) { [weak self] time in
            MainActor.assumeIsolated { self?.tick(time) }
        }
    }

    /// Fetches the timeline and starts playing near the newest recording.
    func load() async {
        guard let api = SentinelSession.shared.api, SentinelSession.shared.isDemoMode == false else {
            error = "Playback isn't available in Demo Mode."
            return
        }
        isLoading = true
        defer { isLoading = false }
        let to = Date().unixMillis
        let from = to - UnixMillis(Self.windowHours * 3600 * 1000)
        do {
            let tl = try await api.timeline(cameraID: cameraID, from: from, to: to)
            timeline = tl
            error = nil
            if let last = tl.spans.max(by: { $0.end < $1.end }) {
                seek(to: max(last.start, last.end - 60_000), play: true)
            } else {
                position = to
                inGap = true
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    func beginScrub() {
        scrubbing = true
    }

    /// Moves the playhead while dragging; playback resumes at `endScrub`.
    func scrub(to millis: UnixMillis) {
        position = millis
    }

    func endScrub() {
        scrubbing = false
        seek(to: position, play: true)
    }

    func togglePlay() {
        if isPlaying {
            player.pause()
            isPlaying = false
        } else if player.currentItem == nil || inGap {
            seek(to: position, play: true)
        } else {
            player.play()
            isPlaying = true
        }
    }

    func step(seconds: Double) {
        seek(to: position + UnixMillis(seconds * 1000), play: isPlaying)
    }

    /// Jumps to the next or previous detection or alarm.
    func jumpToMarker(forward: Bool) {
        guard let timeline else { return }
        let markers = (timeline.events.map(\.createdAt) + timeline.alarms.map(\.createdAt)).sorted()
        let target = forward ? markers.first { $0 > position + 2000 } : markers.last { $0 < position - 4000 }
        if let target { seek(to: target - 3000, play: true) }
    }

    func seek(to millis: UnixMillis, play: Bool) {
        guard let timeline else { return }
        var target = min(max(millis, timeline.from), timeline.to)
        var found = timeline.mediaTime(at: target)
        if found == nil {
            // In a gap: snap to the next recording, else the previous one's end.
            if let next = timeline.spans.filter({ $0.start >= target }).min(by: { $0.start < $1.start }) {
                target = next.start
            } else if let prev = timeline.spans.max(by: { $0.end < $1.end }) {
                target = max(prev.start, prev.end - 1000)
            }
            found = timeline.mediaTime(at: target)
        }
        position = target
        guard let (span, seconds) = found else {
            inGap = true
            player.pause()
            isPlaying = false
            return
        }
        inGap = false
        let expiring = endpoint.map { $0.expiresAt.timeIntervalSinceNow < 45 } ?? true
        if player.currentItem != nil, loadedRecorder == .some(span.recorderId), expiring == false {
            player.seek(to: CMTime(seconds: seconds, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero)
            if play { player.play() }
            isPlaying = play
        } else {
            load(recorder: span.recorderId, atSeconds: seconds, play: play, timeline: timeline)
        }
    }

    private func load(recorder: String?, atSeconds seconds: Double, play: Bool, timeline: V2.Timeline) {
        loadTask?.cancel()
        statusObserver?.invalidate()
        isPlaying = play
        isLoading = true
        loadTask = Task { [weak self] in
            guard let self else { return }
            defer { self.isLoading = false }
            guard let broker = SentinelSession.shared.broker else { return }
            do {
                let endpoint = try await broker.endpoint(cameraID: self.cameraID, rights: [.playback],
                                                         recorderID: recorder, preferGateway: self.forceGateway)
                guard Task.isCancelled == false else { return }
                self.endpoint = endpoint
                self.loadedRecorder = .some(recorder)
                self.route = endpoint.isGateway ? "Gateway" : "Direct"
                let item = AVPlayerItem(url: endpoint.playback(from: timeline.from, to: timeline.to))
                self.watch(item, endpoint: endpoint, retryAt: seconds, play: play, recorder: recorder, timeline: timeline)
                self.player.replaceCurrentItem(with: item)
                self.error = nil
            } catch {
                guard Task.isCancelled == false else { return }
                let forbidden = (error as? SentinelAPIError)?.isForbidden == true
                self.error = forbidden ? "Your role can't play back this camera." : error.localizedDescription
                self.isPlaying = false
            }
        }
    }

    /// Seeks once the playlist is ready; falls back to the gateway when the
    /// Recording Server can't be reached directly.
    private func watch(_ item: AVPlayerItem, endpoint: VideoEndpoint, retryAt seconds: Double, play: Bool,
                       recorder: String?, timeline: V2.Timeline) {
        statusObserver = item.observe(\.status, options: [.new]) { [weak self] item, _ in
            Task { @MainActor in
                guard let self, self.player.currentItem === item else { return }
                switch item.status {
                case .readyToPlay:
                    await self.player.seek(to: CMTime(seconds: seconds, preferredTimescale: 600),
                                           toleranceBefore: .zero, toleranceAfter: .zero)
                    if play { self.player.play() }
                case .failed:
                    if endpoint.isGateway == false {
                        await SentinelSession.shared.broker?.reportDirectFailure(recorderID: endpoint.token.recorderId)
                        self.forceGateway = true
                        self.load(recorder: recorder, atSeconds: seconds, play: play, timeline: timeline)
                    } else {
                        self.error = item.error?.localizedDescription ?? "The recording couldn't be played."
                        self.isPlaying = false
                    }
                default:
                    break
                }
            }
        }
        if let endObserver { NotificationCenter.default.removeObserver(endObserver) }
        endObserver = NotificationCenter.default.addObserver(forName: .AVPlayerItemDidPlayToEndTime, object: item, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.advancePastEnd() }
        }
    }

    /// The playlist ended: carry on with the next recording, possibly on
    /// another Recording Server.
    private func advancePastEnd() {
        guard let timeline else { return }
        if let next = timeline.spans.filter({ $0.start >= position }).min(by: { $0.start < $1.start }), next.start > position {
            seek(to: next.start, play: true)
        } else {
            isPlaying = false
        }
    }

    private func tick(_ time: CMTime) {
        guard scrubbing == false, let timeline, player.currentItem?.status == .readyToPlay,
              case .some(let recorder) = loadedRecorder, player.rate != 0 || isPlaying else { return }
        if let wall = timeline.wallTime(atMedia: time.seconds, recorderID: recorder) {
            position = wall
        }
        // Token about to run out: reload the same place with a fresh one.
        if let endpoint, endpoint.expiresAt.timeIntervalSinceNow < 40, isLoading == false {
            seek(to: position, play: isPlaying)
        }
    }

    func teardown() {
        loadTask?.cancel()
        statusObserver?.invalidate()
        statusObserver = nil
        if let timeObserver { player.removeTimeObserver(timeObserver) }
        timeObserver = nil
        if let endObserver { NotificationCenter.default.removeObserver(endObserver) }
        endObserver = nil
        player.pause()
        player.replaceCurrentItem(with: nil)
    }

    /// Stops playback when the view leaves the screen (keeps the timeline).
    func pauseAndRelease() {
        loadTask?.cancel()
        player.pause()
        isPlaying = false
    }
}

struct PlaybackTimelineView: View {
    let camera: V2.Camera
    @StateObject private var model: PlaybackModel

    init(camera: V2.Camera) {
        self.camera = camera
        _model = StateObject(wrappedValue: PlaybackModel(cameraID: camera.id))
    }

    var body: some View {
        VStack(spacing: 0) {
            ZStack {
                Color.black
                ZoomableVideoView(player: model.player)
                overlay
            }
            .aspectRatio(16/9, contentMode: .fit)
            .frame(maxWidth: .infinity)

            if let error = model.error {
                Text(error).font(.caption).foregroundStyle(SentinelTheme.alarm)
                    .padding(SentinelTheme.Space.md)
            }

            if let timeline = model.timeline {
                VStack(spacing: SentinelTheme.Space.sm) {
                    Text(Date(unixMillis: model.position), format: .dateTime.weekday(.abbreviated).hour().minute().second())
                        .font(.system(.subheadline, design: .monospaced).weight(.semibold))
                        .foregroundStyle(.white)
                    TimelineScrubber(timeline: timeline, position: model.position,
                                     onBegin: { model.beginScrub() },
                                     onChange: { model.scrub(to: $0) },
                                     onEnd: { model.endScrub() })
                        .frame(height: 96)
                    transport
                    if model.route.isEmpty == false {
                        Text("Playing \(model.route == "Direct" ? "directly from the Recording Server" : "through the Management Server")")
                            .font(.caption2).foregroundStyle(.white.opacity(0.35))
                    }
                }
                .padding(SentinelTheme.Space.md)
            } else if model.isLoading {
                ProgressView().tint(SentinelTheme.accent).padding(.top, 30)
            }
            Spacer()
        }
        .task { await model.load() }
        .onDisappear { model.pauseAndRelease() }
    }

    @ViewBuilder
    private var overlay: some View {
        if model.isLoading {
            ProgressView().tint(.white.opacity(0.6))
        } else if model.inGap, model.timeline != nil {
            VStack(spacing: 8) {
                Image(systemName: "timeline.selection").font(.largeTitle).foregroundStyle(.white.opacity(0.3))
                Text(model.timeline?.spans.isEmpty == true ? "No recordings in the last 24 hours" : "Nothing recorded here")
                    .font(.caption).foregroundStyle(.white.opacity(0.5))
            }
        }
    }

    private var transport: some View {
        HStack(spacing: SentinelTheme.Space.xl) {
            transportButton("backward.end.fill", label: "Previous event") { model.jumpToMarker(forward: false) }
            transportButton("gobackward.15", label: "Back 15 seconds") { model.step(seconds: -15) }
            Button { Haptics.tap(); model.togglePlay() } label: {
                Image(systemName: model.isPlaying ? "pause.fill" : "play.fill")
                    .font(.title2.weight(.bold)).foregroundStyle(.white)
                    .frame(width: 56, height: 56)
                    .background(SentinelTheme.accent, in: Circle())
            }
            .accessibilityLabel(model.isPlaying ? "Pause" : "Play")
            transportButton("goforward.15", label: "Forward 15 seconds") { model.step(seconds: 15) }
            transportButton("forward.end.fill", label: "Next event") { model.jumpToMarker(forward: true) }
        }
    }

    private func transportButton(_ symbol: String, label: String, action: @escaping () -> Void) -> some View {
        Button { Haptics.tap(); action() } label: {
            Image(systemName: symbol).font(.title3).foregroundStyle(.white.opacity(0.8))
                .frame(width: 40, height: 40)
        }
        .accessibilityLabel(label)
    }
}

/// Horizontal 24-hour scrubber: recorded spans as bars, detections as ticks,
/// alarms taller, and a draggable playhead.
struct TimelineScrubber: View {
    let timeline: V2.Timeline
    let position: UnixMillis
    let onBegin: () -> Void
    let onChange: (UnixMillis) -> Void
    let onEnd: () -> Void
    @State private var dragging = false

    var body: some View {
        GeometryReader { geo in
            let w = geo.size.width
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 8).fill(SentinelTheme.panel)
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(SentinelTheme.line, lineWidth: 1))

                // Gridlines every 6 hours
                ForEach(0..<5, id: \.self) { i in
                    Rectangle().fill(.white.opacity(0.08)).frame(width: 1)
                        .offset(x: CGFloat(i) / 4 * w)
                }

                ForEach(Array(timeline.spans.enumerated()), id: \.offset) { _, span in
                    let sx = x(span.start, w)
                    let ex = x(span.end, w)
                    RoundedRectangle(cornerRadius: 2)
                        .fill(SentinelTheme.accent.opacity(0.65))
                        .frame(width: max(2, ex - sx), height: 26)
                        .offset(x: sx, y: -14)
                }

                ForEach(timeline.events) { ev in
                    Rectangle().fill(SentinelTheme.kindColor(ev.kind))
                        .frame(width: 2, height: 22)
                        .offset(x: x(ev.createdAt, w), y: 16)
                }
                ForEach(timeline.alarms) { alarm in
                    Rectangle().fill(SentinelTheme.severityColor(alarm.severity.rawValue))
                        .frame(width: 3, height: 34)
                        .offset(x: x(alarm.createdAt, w), y: 10)
                }

                // Playhead
                Rectangle().fill(.white).frame(width: 2)
                    .overlay(alignment: .top) { Circle().fill(.white).frame(width: 10, height: 10).offset(y: -3) }
                    .offset(x: x(position, w) - 1)

                // Hour labels
                HStack {
                    ForEach(0..<5, id: \.self) { i in
                        Text(label(at: i)).font(.system(size: 9).monospacedDigit()).foregroundStyle(.white.opacity(0.4))
                        if i < 4 { Spacer() }
                    }
                }
                .padding(.horizontal, 4)
                .frame(maxHeight: .infinity, alignment: .bottom)
                .padding(.bottom, 3)
            }
            .contentShape(Rectangle())
            .gesture(
                DragGesture(minimumDistance: 0)
                    .onChanged { value in
                        if dragging == false { dragging = true; onBegin() }
                        onChange(time(at: value.location.x, w))
                    }
                    .onEnded { value in
                        onChange(time(at: value.location.x, w))
                        dragging = false
                        onEnd()
                    }
            )
        }
    }

    private func span() -> Double { Double(max(1, timeline.to - timeline.from)) }

    private func x(_ t: UnixMillis, _ width: CGFloat) -> CGFloat {
        CGFloat(Double(t - timeline.from) / span()) * width
    }

    private func time(at x: CGFloat, _ width: CGFloat) -> UnixMillis {
        let frac = Double(max(0, min(1, x / max(1, width))))
        return timeline.from + UnixMillis(frac * span())
    }

    private func label(at index: Int) -> String {
        let t = timeline.from + UnixMillis(Double(index) / 4 * span())
        return Date(unixMillis: t).formatted(.dateTime.hour().minute())
    }
}
