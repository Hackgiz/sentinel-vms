// CameraDetailView.swift
// Fullscreen-capable camera with a zoomable/PiP live player and a scrubbable
// playback timeline (recorded spans + detection and alarm markers) from the
// v2 timeline API, following the footage across Recording Servers.

import SwiftUI
import AVKit

struct CameraDetailView: View {
    let initialCamera: V2.Camera
    @ObservedObject private var store = AppStore.shared
    @State private var tab: Tab
    /// Where Playback should open (a detection's time); cleared once used so
    /// switching tabs doesn't jump back to it.
    @State private var startAt: UnixMillis?
    /// PTZ for this camera (WAVE23): probed on appear, bar toggled by the PTZ button.
    @StateObject private var ptz: PTZController
    @State private var showPTZ = false

    enum Tab: String, CaseIterable { case live, playback }

    /// With `startAt`, opens on Playback at that moment instead of Live. Pass
    /// `PlaybackModel.eventStart(_:)` / `alarmStart(_:)` so the pre-roll is in view.
    init(camera: V2.Camera, startAt: UnixMillis? = nil) {
        self.initialCamera = camera
        _tab = State(initialValue: startAt == nil ? .live : .playback)
        _startAt = State(initialValue: startAt)
        _ptz = StateObject(wrappedValue: PTZController(cameraID: camera.id))
    }

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
                case .playback: PlaybackTimelineView(camera: camera, startAt: $startAt)
                }
            }
        }
        .navigationTitle(camera.name)
        .navigationBarTitleDisplayMode(.inline)
        // Home hides its nav bar; force this pushed view's bar visible so the
        // back button always shows (guards the iOS 16 toolbar-hidden quirk).
        .toolbar(.visible, for: .navigationBar)
        .ptzLifecycle(ptz, shown: showPTZ && tab == .live)
        // Fullscreen is presented by MainTabView (`store.fullscreenCamera`):
        // a cover owned here would be torn down with this view when a
        // landscape turn switches a wide phone to the sidebar shell.
    }

    private var metadataStrip: some View {
        HStack(spacing: 10) {
            SentinelStatusBadge(status: camera.statusLabel)
            if camera.recording && camera.isOnline {
                Chip(text: "REC", systemImage: "record.circle", color: SentinelTheme.alarm)
            }
            if let mode = camera.recordingModeLabel { // wave 22: what this camera records
                Chip(text: mode, systemImage: camera.effectiveMode == "off" ? "video.slash" : "externaldrive", color: .gray)
                    .accessibilityLabel("Recording: \(mode)")
            }
            if let roll = camera.rollSummary { // wave 24 D1: null = server default
                Chip(text: roll, systemImage: "clock.arrow.circlepath", color: .gray)
                    .accessibilityLabel("Pre and post roll: \(roll)")
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
                    HStack(spacing: 2) {
                        Chip(text: "LIVE", color: SentinelTheme.alarm, filled: true)
                            .allowsHitTesting(false)
                        PTZToggleButton(controller: ptz, cameraOnline: camera.isOnline, shown: $showPTZ)
                        Button { Haptics.tap(); store.fullscreenCamera = FullscreenCamera(id: camera.id) } label: {
                            Image(systemName: "arrow.up.left.and.arrow.down.right")
                                .font(.caption.weight(.bold)).foregroundStyle(.white)
                                .padding(7).background(.black.opacity(0.45), in: Circle())
                                // A full 44 pt target: the 26 pt circle alone was easy to miss.
                                .frame(width: 44, height: 44)
                                .contentShape(Rectangle())
                        }
                        .accessibilityLabel("Fullscreen")
                    }
                    .padding(.trailing, 2)
                }
            }
            if showPTZ, PTZAccess.isVisible(state: ptz.state, cameraOnline: camera.isOnline) {
                PTZBar(controller: ptz)
                    .padding(.horizontal, SentinelTheme.Space.md)
                    .padding(.top, SentinelTheme.Space.sm)
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

/// The pinch-zoom arithmetic of the live player, kept free of views so it can
/// be tested: the scale stays within 1…`maxScale`, and the picture is never
/// panned past its own edges.
struct VideoZoom: Equatable {
    static let maxScale: CGFloat = 5

    private(set) var scale: CGFloat = 1
    /// Translation of the picture's centre, in the view's points.
    private(set) var offset: CGPoint = .zero

    var isZoomed: Bool { scale > 1.001 }

    /// "1.0×", "2.5×": the video view's accessibility value.
    var label: String { String(format: "%.1f×", scale) }

    mutating func pinch(by factor: CGFloat, in size: CGSize) {
        guard factor.isFinite, factor > 0 else { return }
        scale = min(max(scale * factor, 1), Self.maxScale)
        clamp(in: size)
    }

    mutating func pan(by delta: CGPoint, in size: CGSize) {
        guard isZoomed else { return }
        offset = CGPoint(x: offset.x + delta.x, y: offset.y + delta.y)
        clamp(in: size)
    }

    mutating func reset() { self = VideoZoom() }

    /// Keeps the zoomed picture covering the view (after a pan, a pinch out
    /// or a new size such as a rotation).
    mutating func clamp(in size: CGSize) {
        let maxX = size.width * (scale - 1) / 2
        let maxY = size.height * (scale - 1) / 2
        offset = CGPoint(x: min(max(offset.x, -maxX), maxX), y: min(max(offset.y, -maxY), maxY))
    }

    var transform: CGAffineTransform {
        CGAffineTransform(translationX: offset.x, y: offset.y).scaledBy(x: scale, y: scale)
    }
}

/// Player with pinch + pan zoom, double-tap reset, and a Picture-in-Picture
/// controller. Anything SwiftUI stacks above it (LiveStreamView's spinner,
/// error and demo placeholder) must be `.allowsHitTesting(false)`: SwiftUI
/// content on top takes the touches and the recognizers never fire.
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

/// A clipping container that SwiftUI lays out; the zoom transform goes on the
/// inner player view only, so SwiftUI's layout never fights it and the zoomed
/// picture never spills over the rest of the screen.
final class ZoomPlayerView: UIView, UIGestureRecognizerDelegate {
    private let content = PlayerView()
    var playerLayer: AVPlayerLayer { content.playerLayer }
    private var pipController: AVPictureInPictureController?
    private(set) var zoom = VideoZoom()

    func configure(player: AVPlayer) {
        playerLayer.player = player
        backgroundColor = .black
        clipsToBounds = true
        content.isUserInteractionEnabled = false
        addSubview(content)

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
        pan.maximumNumberOfTouches = 2
        let doubleTap = UITapGestureRecognizer(target: self, action: #selector(onDoubleTap))
        doubleTap.numberOfTapsRequired = 2
        [pinch, pan, doubleTap].forEach {
            $0.delegate = self
            addGestureRecognizer($0)
        }
        isUserInteractionEnabled = true
        isAccessibilityElement = true
        accessibilityIdentifier = "liveVideo"
        accessibilityLabel = "Live video"
        accessibilityHint = "Pinch to zoom, double-tap to reset."
        accessibilityTraits = .allowsDirectInteraction
        apply()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        // Lay out untransformed, then re-apply the zoom for the new size.
        content.transform = .identity
        content.frame = bounds
        zoom.clamp(in: bounds.size)
        apply()
    }

    private func apply() {
        content.transform = zoom.transform
        accessibilityValue = zoom.label
    }

    @objc private func onPinch(_ g: UIPinchGestureRecognizer) {
        guard g.state == .began || g.state == .changed else { return }
        zoom.pinch(by: g.scale, in: bounds.size)
        g.scale = 1
        apply()
    }
    @objc private func onPan(_ g: UIPanGestureRecognizer) {
        guard zoom.isZoomed else { return }
        zoom.pan(by: g.translation(in: self), in: bounds.size)
        g.setTranslation(.zero, in: self)
        apply()
    }
    @objc private func onDoubleTap() {
        zoom.reset()
        UIView.animate(withDuration: 0.2) { self.apply() }
    }

    /// Unzoomed, one-finger pans belong to whatever is around the player
    /// (the navigation back-swipe); zoomed, they move the picture.
    override func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        if g is UIPanGestureRecognizer { return zoom.isZoomed }
        return true
    }
    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        // Pinch and pan together on this view.
        other.view === self
    }
}

/// Immersive fullscreen live player presented as a cover from MainTabView.
/// On an iPhone it turns the interface to landscape so a 16:9 stream fills
/// the screen (in portrait the cover would show the same letterboxed band as
/// the inline player), and turns back when closed if the phone is still held
/// upright. iPads keep their orientation.
struct FullscreenPlayer: View {
    let cameraID: String
    @ObservedObject private var store = AppStore.shared
    @Environment(\.dismiss) private var dismiss
    @StateObject private var ptz: PTZController
    @State private var showPTZ = false

    init(cameraID: String) {
        self.cameraID = cameraID
        _ptz = StateObject(wrappedValue: PTZController(cameraID: cameraID))
    }

    private var camera: V2.Camera? { store.camera(id: cameraID) }

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            if let camera {
                LiveStreamView(camera: camera, zoomable: true)
                    .ignoresSafeArea()
            }
            VStack {
                HStack {
                    if let camera {
                        Text(camera.name)
                            .font(.subheadline.weight(.semibold)).foregroundStyle(.white.opacity(0.85))
                            .padding(.horizontal, 12).padding(.vertical, 8)
                            .background(.black.opacity(0.5), in: Capsule())
                    }
                    Spacer()
                    if let camera {
                        PTZToggleButton(controller: ptz, cameraOnline: camera.isOnline, shown: $showPTZ)
                    }
                    Button { dismiss() } label: {
                        Image(systemName: "xmark")
                            .font(.headline).foregroundStyle(.white)
                            .padding(12).background(.black.opacity(0.5), in: Circle())
                    }
                    .accessibilityLabel("Close")
                }
                Spacer()
                if showPTZ, PTZAccess.isVisible(state: ptz.state, cameraOnline: camera?.isOnline == true) {
                    PTZBar(controller: ptz)
                        .frame(maxWidth: 420)
                }
            }
            .padding()
        }
        .ptzLifecycle(ptz, shown: showPTZ)
        .statusBarHidden(true)
        .persistentSystemOverlays(.hidden)
        .onAppear {
            if LayoutLogic.fullscreenTurnsLandscape(phone: UIDevice.current.userInterfaceIdiom == .phone) {
                InterfaceOrientation.request(.landscape)
            }
        }
        .onDisappear {
            let device = UIDevice.current.orientation
            if LayoutLogic.fullscreenRestoresPortrait(phone: UIDevice.current.userInterfaceIdiom == .phone,
                                                      deviceIsLandscape: device.isLandscape) {
                InterfaceOrientation.request(.portrait)
            }
        }
    }
}

/// Asks the window scene for an interface orientation (iOS 16+). The app
/// allows every orientation on iPhone, so the request is honoured; the next
/// physical turn takes over again afterwards.
enum InterfaceOrientation {
    @MainActor
    static func request(_ mask: UIInterfaceOrientationMask) {
        for scene in UIApplication.shared.connectedScenes.compactMap({ $0 as? UIWindowScene }) {
            scene.requestGeometryUpdate(.iOS(interfaceOrientations: mask)) { _ in }
            scene.keyWindow?.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
        }
    }
}

// MARK: - Playback window logic (wave 24)

/// D2: the playlist window Playback asks for, playhead ± 1 h clamped to now,
/// instead of the whole 24 h. Pure so it can be unit-tested.
struct PlaybackWindow: Equatable, Sendable {
    var from: UnixMillis
    var to: UnixMillis

    nonisolated static let halfSpan: UnixMillis = 3_600_000
    /// Refetch when the playhead is this close to an end of the window.
    nonisolated static let edgeMargin: UnixMillis = 600_000
    /// A refetch must gain at least this much, so a window already clamped to
    /// "now" doesn't refetch in a loop.
    nonisolated static let minGain: UnixMillis = 60_000

    /// Playhead ± 1 h, clamped to `now` (the playhead itself is always inside).
    nonisolated static func around(_ playhead: UnixMillis, now: UnixMillis) -> PlaybackWindow {
        let to = max(min(playhead + halfSpan, now), playhead)
        return PlaybackWindow(from: playhead - halfSpan, to: to)
    }

    nonisolated func contains(_ t: UnixMillis) -> Bool { t >= from && t <= to }

    /// True when the playhead is outside the window, or within 10 min of an
    /// end and a fresh window would reach meaningfully further.
    nonisolated func needsRefetch(playhead: UnixMillis, now: UnixMillis) -> Bool {
        if contains(playhead) == false { return true }
        let fresh = Self.around(playhead, now: now)
        if playhead > to - Self.edgeMargin, fresh.to >= to + Self.minGain { return true }
        if playhead < from + Self.edgeMargin, fresh.from <= from - Self.minGain { return true }
        return false
    }
}

/// Tracks the newest load so a cancelled/superseded one can't clear
/// `isLoading` for the load that replaced it.
struct PlaybackLoadGate: Sendable {
    private var latest = 0

    /// Starts a load; returns its id.
    nonisolated mutating func begin() -> Int {
        latest += 1
        return latest
    }

    /// True when `id` is still the newest load (only then may it clear isLoading).
    nonisolated func isLatest(_ id: Int) -> Bool { id == latest }
}

/// Exponential backoff for automatic reloads from the playback tick: 1, 2, 4 …
/// capped at 30 s; a success resets it.
struct PlaybackBackoff: Sendable {
    private(set) var failures = 0
    private var retryAt: Date?

    nonisolated static let cap: TimeInterval = 30

    nonisolated var delay: TimeInterval {
        failures == 0 ? 0 : min(Self.cap, pow(2, Double(min(failures - 1, 10))))
    }

    nonisolated mutating func recordFailure(now: Date = Date()) {
        failures += 1
        retryAt = now.addingTimeInterval(delay)
    }

    nonisolated mutating func reset() {
        failures = 0
        retryAt = nil
    }

    nonisolated func canAttempt(now: Date = Date()) -> Bool {
        guard let retryAt else { return true }
        return now >= retryAt
    }
}

/// What the play button does, given the state of the video token.
enum PlaybackToggleAction: Equatable, Sendable {
    case pause, resume, reload

    /// An expired (or nearly expired) token reloads through the broker, which
    /// hands out a fresh one, rather than resuming an item that will fail.
    nonisolated static func decide(isPlaying: Bool, hasItem: Bool, inGap: Bool, tokenExpiry: Date?, now: Date = Date()) -> PlaybackToggleAction {
        if isPlaying { return .pause }
        if hasItem == false || inGap { return .reload }
        if let tokenExpiry, tokenExpiry.timeIntervalSince(now) < 45 { return .reload }
        return .resume
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
    /// Set until the pool first activates the model, and again after it stops it.
    private var needsLoad = true
    /// A requested start (see `start(at:)`) the next load seeks to instead of
    /// the newest recording.
    private var pendingStart: UnixMillis?
    /// D2: the ±1 h timeline whose `media` offsets the loaded playlist uses
    /// (`timeline` stays the 24 h lane for drawing, markers and gap snapping).
    private var playTimeline: V2.Timeline?
    private var window: PlaybackWindow?
    private var loadGate = PlaybackLoadGate()
    private var backoff = PlaybackBackoff()

    static let windowHours = 24.0

    /// How far before a detection Playback opens, so its lead-up is in view.
    nonisolated static let eventPreRoll: UnixMillis = 5_000
    /// How far before an alarm Playback opens: alarms get more lead-up.
    nonisolated static let alarmPreRoll: UnixMillis = 10_000

    /// Where Playback opens for a detection at `createdAt`.
    nonisolated static func eventStart(_ createdAt: UnixMillis) -> UnixMillis { createdAt - eventPreRoll }
    /// Where Playback opens for an alarm raised at `createdAt`.
    nonisolated static func alarmStart(_ createdAt: UnixMillis) -> UnixMillis { createdAt - alarmPreRoll }

    /// The next (or previous) marker's start from `position`, each with its
    /// own pre-roll. Compares starts, not marker times, so a forward jump
    /// doesn't land on the marker it just played.
    nonisolated static func markerTarget(events: [UnixMillis], alarms: [UnixMillis], position: UnixMillis, forward: Bool) -> UnixMillis? {
        let starts = (events.map(eventStart) + alarms.map(alarmStart)).sorted()
        return forward ? starts.first { $0 > position + 2000 } : starts.last { $0 < position - 4000 }
    }

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
        let gateID = loadGate.begin()
        isLoading = true
        // A seek below starts a newer load; only the latest may clear the flag.
        defer { if loadGate.isLatest(gateID) { isLoading = false } }
        let to = Date().unixMillis
        var from = to - UnixMillis(Self.windowHours * 3600 * 1000)
        // Reach back far enough to cover an older requested start.
        if let start = pendingStart { from = min(from, start - 30 * 60_000) }
        do {
            let tl = try await api.timeline(cameraID: cameraID, from: from, to: to)
            timeline = tl
            playTimeline = nil // a fresh lane: the next seek fetches its ±1 h window
            window = nil
            loadedRecorder = nil
            error = nil
            if let start = pendingStart {
                pendingStart = nil
                seek(to: start, play: true)
            } else if let last = tl.spans.max(by: { $0.end < $1.end }) {
                seek(to: max(last.start, last.end - 60_000), play: true)
            } else {
                position = to
                inGap = true
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// Plays from `millis` (an event or alarm start, pre-roll included),
    /// loading the timeline first when it isn't loaded or doesn't reach that
    /// far back.
    func start(at millis: UnixMillis) {
        pendingStart = millis
        // Not active yet, or a load is in flight: that load picks it up.
        guard needsLoad == false, isLoading == false, let timeline else { return }
        if millis >= timeline.from {
            pendingStart = nil
            seek(to: millis, play: true)
        } else {
            Task { await load() }
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
        } else {
            switch PlaybackToggleAction.decide(isPlaying: isPlaying, hasItem: player.currentItem != nil, inGap: inGap,
                                               tokenExpiry: endpoint?.expiresAt) {
            case .pause:
                player.pause()
                isPlaying = false
            case .reload:
                // Expired token: the reload asks the broker for a fresh one.
                backoff.reset()
                seek(to: position, play: true)
            case .resume:
                player.play()
                isPlaying = true
            }
        }
    }

    func step(seconds: Double) {
        seek(to: position + UnixMillis(seconds * 1000), play: isPlaying)
    }

    /// Jumps to the next or previous detection or alarm.
    func jumpToMarker(forward: Bool) {
        guard let timeline else { return }
        let target = Self.markerTarget(events: timeline.events.map(\.createdAt), alarms: timeline.alarms.map(\.createdAt),
                                       position: position, forward: forward)
        if let target { seek(to: target, play: true) }
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
        guard found != nil else {
            inGap = true
            player.pause()
            isPlaying = false
            return
        }
        inGap = false
        // D2: media offsets must come from the window the playlist covers.
        guard let window, window.contains(target), let playTimeline else {
            loadWindow(around: target, play: play)
            return
        }
        guard let (span, seconds) = playTimeline.mediaTime(at: target) else {
            inGap = true
            player.pause()
            isPlaying = false
            return
        }
        let expiring = endpoint.map { $0.expiresAt.timeIntervalSinceNow < 45 } ?? true
        if player.currentItem != nil, loadedRecorder == .some(span.recorderId), expiring == false {
            player.seek(to: CMTime(seconds: seconds, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero)
            if play { player.play() }
            isPlaying = play
        } else {
            load(recorder: span.recorderId, atSeconds: seconds, play: play, timeline: playTimeline, window: window)
        }
    }

    /// Fetches the ±1 h timeline around `target`, then seeks into it (which
    /// reloads the playlist for that window).
    private func loadWindow(around target: UnixMillis, play: Bool) {
        guard let api = SentinelSession.shared.api, SentinelSession.shared.isDemoMode == false else { return }
        loadTask?.cancel()
        statusObserver?.invalidate()
        isPlaying = play
        let gateID = loadGate.begin()
        isLoading = true
        let cameraID = cameraID
        let next = PlaybackWindow.around(target, now: max(Date().unixMillis, target))
        loadTask = Task { [weak self] in
            guard let self else { return }
            defer { if self.loadGate.isLatest(gateID) { self.isLoading = false } }
            do {
                let tl = try await api.timeline(cameraID: cameraID, from: next.from, to: next.to)
                guard Task.isCancelled == false else { return }
                self.playTimeline = tl
                self.window = next
                self.loadedRecorder = nil // the loaded item covers the old window
                self.backoff.reset()
                self.seek(to: target, play: play)
            } catch {
                guard Task.isCancelled == false else { return }
                self.backoff.recordFailure()
                self.error = error.localizedDescription
                self.isPlaying = false
            }
        }
    }

    private func load(recorder: String?, atSeconds seconds: Double, play: Bool, timeline: V2.Timeline, window: PlaybackWindow) {
        loadTask?.cancel()
        statusObserver?.invalidate()
        isPlaying = play
        let gateID = loadGate.begin()
        isLoading = true
        loadTask = Task { [weak self] in
            guard let self else { return }
            // A cancelled load must not clear the flag for the one that replaced it.
            defer { if self.loadGate.isLatest(gateID) { self.isLoading = false } }
            guard let broker = SentinelSession.shared.broker else { return }
            do {
                let endpoint = try await broker.endpoint(cameraID: self.cameraID, rights: [.playback],
                                                         recorderID: recorder, preferGateway: self.forceGateway)
                guard Task.isCancelled == false else { return }
                self.endpoint = endpoint
                self.loadedRecorder = .some(recorder)
                self.route = endpoint.isGateway ? "Gateway" : "Direct"
                let item = AVPlayerItem(url: endpoint.playback(from: window.from, to: window.to))
                self.watch(item, endpoint: endpoint, retryAt: seconds, play: play, recorder: recorder, timeline: timeline, window: window)
                self.player.replaceCurrentItem(with: item)
                self.error = nil
                self.backoff.reset()
            } catch {
                guard Task.isCancelled == false else { return }
                self.backoff.recordFailure()
                let forbidden = (error as? SentinelAPIError)?.isForbidden == true
                self.error = forbidden ? "Your role can't play back this camera." : error.localizedDescription
                self.isPlaying = false
            }
        }
    }

    /// Seeks once the playlist is ready; falls back to the gateway when the
    /// Recording Server can't be reached directly.
    private func watch(_ item: AVPlayerItem, endpoint: VideoEndpoint, retryAt seconds: Double, play: Bool,
                       recorder: String?, timeline: V2.Timeline, window: PlaybackWindow) {
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
                        self.load(recorder: recorder, atSeconds: seconds, play: play, timeline: timeline, window: window)
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
        // The playlist only covers the ±1 h window: at its end, fetch the next.
        if let window, position >= window.to - 2000, window.to < Date().unixMillis - PlaybackWindow.minGain {
            loadWindow(around: position, play: true)
            return
        }
        if let next = timeline.spans.filter({ $0.start >= position }).min(by: { $0.start < $1.start }), next.start > position {
            seek(to: next.start, play: true)
        } else {
            isPlaying = false
        }
    }

    private func tick(_ time: CMTime) {
        guard scrubbing == false, let playTimeline, player.currentItem?.status == .readyToPlay,
              case .some(let recorder) = loadedRecorder, player.rate != 0 || isPlaying else { return }
        if let wall = playTimeline.wallTime(atMedia: time.seconds, recorderID: recorder) {
            position = wall
        }
        // Failed loads back off (1 s doubling to 30 s) instead of retrying every tick.
        guard isLoading == false, backoff.canAttempt() else { return }
        // Playhead near either end of the ±1 h window: fetch the next window.
        if let window, window.needsRefetch(playhead: position, now: Date().unixMillis) {
            loadWindow(around: position, play: isPlaying)
            return
        }
        // Token about to run out: reload the same place with a fresh one.
        if let endpoint, endpoint.expiresAt.timeIntervalSinceNow < 40 {
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

extension PlaybackModel: PooledPlayer {
    /// First hold after a stop: fetch a fresh 24 hours and start near the
    /// newest recording. A hold within the pool's grace (a fold) never gets
    /// here twice, so the playhead stays put.
    func activate() {
        guard needsLoad else { return }
        needsLoad = false
        Task { await load() }
    }

    func deactivate() {
        pauseAndRelease()
        needsLoad = true
    }
}

struct PlaybackTimelineView: View {
    let camera: V2.Camera
    /// Owned by `VideoPools.playback`, not this view: a fold rebuilds the
    /// screen and the playhead, item and token must carry on.
    @ObservedObject private var model: PlaybackModel
    @Binding private var startAt: UnixMillis?

    init(camera: V2.Camera, startAt: Binding<UnixMillis?> = .constant(nil)) {
        self.camera = camera
        _startAt = startAt
        _model = ObservedObject(wrappedValue: VideoPools.playback.model(for: camera.id))
    }

    var body: some View {
        GeometryReader { geo in
            // Wide and short (landscape unfolded, iPad landscape): video on
            // the left at full height, scrubber and transport beside it.
            if LayoutLogic.controlsBesideVideo(width: geo.size.width, height: geo.size.height) {
                HStack(alignment: .top, spacing: 0) {
                    video
                    ScrollView { controls }
                        .frame(width: LayoutLogic.sideControlsWidth)
                }
            } else {
                VStack(spacing: 0) {
                    video
                    controls
                    Spacer()
                }
            }
        }
        .onAppear {
            // Before retain, so a fresh load starts there rather than at the newest recording.
            if let startAt {
                model.start(at: startAt)
                self.startAt = nil
            }
            VideoPools.playback.retain(camera.id)
        }
        .onDisappear { VideoPools.playback.release(camera.id) }
    }

    private var video: some View {
        ZStack {
            Color.black
            ZoomableVideoView(player: model.player)
            overlay
        }
        .aspectRatio(16/9, contentMode: .fit)
        .frame(maxWidth: .infinity)
    }

    @ViewBuilder
    private var controls: some View {
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
                if timeline.spans.contains(where: \.isLowRes) { LowResLegend() } // wave 22
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

/// Wave 22: explains the lighter bars (the sub-stream recorded 24/7 in the
/// "Low-res 24/7 + full-res on events" mode).
struct LowResLegend: View {
    var body: some View {
        HStack(spacing: 6) {
            RoundedRectangle(cornerRadius: 2).fill(SentinelTheme.accent.opacity(0.65)).frame(width: 14, height: 8)
            Text("Full-res").font(.caption2).foregroundStyle(.white.opacity(0.5))
            RoundedRectangle(cornerRadius: 2).fill(SentinelTheme.accent.opacity(0.28)).frame(width: 14, height: 8)
                .padding(.leading, 6)
            Text("Low-res").font(.caption2).foregroundStyle(.white.opacity(0.5))
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Lighter bars are low-res footage")
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
                    // Wave 22: low-res footage is the same colour, lighter.
                    RoundedRectangle(cornerRadius: 2)
                        .fill(SentinelTheme.accent.opacity(span.isLowRes ? 0.28 : 0.65))
                        .frame(width: max(2, ex - sx), height: 26)
                        .offset(x: sx, y: -14)
                }

                ForEach(timeline.events) { ev in
                    Rectangle().fill(SentinelTheme.kindColor(ev.kind))
                        .frame(width: 2, height: 22)
                        .offset(x: x(ev.createdAt, w), y: 16)
                }
                ForEach(timeline.alarms) { alarm in
                    Rectangle().fill(SentinelTheme.alarmColor(alarm))
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
