import AVFoundation
import Foundation
import SentinelCore

/// Synchronized playback of several cameras.
///
/// One master clock (`position`, Unix ms) drives every camera. Each camera
/// plays the recorded HLS playlist for the loaded window
/// (`{base}/api/v1/cameras/{id}/playback.m3u8?from=&to=`, rights `playback`),
/// fetched with a video token for the Recording Server that holds the footage
/// (`Span.recorderId`, so footage recorded before a camera moved still plays).
/// Wall time maps to playlist time through `Span.media`; a camera whose
/// player drifts more than `driftLimit` from the clock is re-seeked, and a
/// camera with no recording at the clock's time shows a gap and waits.
@MainActor
final class PlaybackController: ObservableObject {
    static let maxCameras = 9
    static let rates: [Float] = [0.25, 0.5, 1, 2, 4, 8, 16, 32]
    /// Re-seek a camera when it is this far (seconds) from the clock.
    static let driftLimit: Double = 0.8

    @Published private(set) var cameraIDs: [String] = []
    /// Start of the loaded day (local time).
    @Published private(set) var day: Date
    @Published private(set) var timelines: [String: V2.Timeline] = [:]
    @Published private(set) var loadErrors: [String: String] = [:]
    @Published private(set) var isLoading = false
    @Published var position: UnixMillis
    @Published private(set) var isPlaying = false
    @Published var rate: Float = 1 {
        didSet { lanes.values.forEach { $0.apply(rate: isPlaying ? rate : 0) } }
    }
    /// The in/out range marked on the ruler for export.
    @Published var selection = ExportSelection()
    /// How much time the ruler shows (1 minute to 24 hours), centered on the playhead.
    @Published var visibleSpan: Int64 = 3_600_000
    private var resumeAfterScrub = false
    /// Live mode: the playhead is pinned to now and no recorded video loads;
    /// the timelines still load so the bar shows the recording lanes.
    @Published private(set) var isLive = true
    /// Called when a scrub, click or backward step leaves Live, so the
    /// workspace can switch to Playback.
    var onLeaveLive: (() -> Void)?
    private var lastLiveRefresh = Date()

    private(set) var lanes: [String: PlaybackLane] = [:]
    private var api: SentinelAPI?
    private var video: VideoTokenBroker?
    private var clock: Timer?
    private var lastTick = Date()

    init() {
        let start = Calendar.current.startOfDay(for: Date())
        day = start
        position = Date().addingTimeInterval(-300).unixMillis
    }

    var windowFrom: UnixMillis { day.unixMillis }
    var windowTo: UnixMillis { Calendar.current.date(byAdding: .day, value: 1, to: day)!.unixMillis }

    func attach(api: SentinelAPI, video: VideoTokenBroker) {
        guard self.api !== api else { return }
        self.api = api
        self.video = video
        lanes.values.forEach { $0.tearDown() }
        lanes.removeAll()
        timelines.removeAll()
        if isLive == false { for id in cameraIDs { lanes[id] = PlaybackLane(cameraID: id, controller: self) } }
        Task { await reloadTimelines() }
    }

    func lane(_ cameraID: String) -> PlaybackLane? { lanes[cameraID] }

    // MARK: Cameras

    func setCameras(_ ids: [String]) {
        let wanted = Array(ids.prefix(Self.maxCameras))
        guard wanted != cameraIDs else { return }
        for (id, lane) in lanes where wanted.contains(id) == false {
            lane.tearDown()
            lanes[id] = nil
            timelines[id] = nil
        }
        cameraIDs = wanted
        for id in wanted where lanes[id] == nil && isLive == false {
            lanes[id] = PlaybackLane(cameraID: id, controller: self)
        }
        Task { await reloadTimelines() }
    }

    func toggleCamera(_ id: String) {
        if cameraIDs.contains(id) {
            setCameras(cameraIDs.filter { $0 != id })
        } else if cameraIDs.count < Self.maxCameras {
            setCameras(cameraIDs + [id])
        }
    }

    // MARK: Time

    /// Jumps to `date`, loading its day when needed.
    func go(to date: Date) {
        let target = date.unixMillis
        let newDay = Calendar.current.startOfDay(for: date)
        if newDay != day {
            day = newDay
            lanes.values.forEach { $0.unload() }
            position = target
            Task { await reloadTimelines() }
        } else {
            position = target
            syncAll(force: true)
        }
    }

    /// Where the playhead may be: no later than now.
    private func clamped(_ millis: UnixMillis) -> UnixMillis {
        max(min(millis, Date().unixMillis), 0)
    }

    // MARK: Live

    /// Leaves Live for Playback: video lanes load and the clock keeps its place.
    func leaveLive() {
        guard isLive else { return }
        isLive = false
        for id in cameraIDs where lanes[id] == nil { lanes[id] = PlaybackLane(cameraID: id, controller: self) }
        onLeaveLive?()
    }

    /// Back to Live: playback stops, lanes unload, the playhead pins to now.
    func enterLive() {
        pause()
        isLive = true
        lanes.values.forEach { $0.tearDown() }
        lanes.removeAll()
        resumeAfterScrub = false
        position = Date().unixMillis
        let today = Calendar.current.startOfDay(for: Date())
        if today != day { day = today }
        Task { await reloadTimelines() }
    }

    func seek(to millis: UnixMillis) {
        if isLive, millis < Date().unixMillis - LiveEdge.threshold { leaveLive() }
        guard isLive == false else { return }
        let target = clamped(millis)
        if target < windowFrom || target >= windowTo {
            go(to: Date(unixMillis: target))
            return
        }
        position = target
        syncAll(force: true)
    }

    /// Dragging the ruler: the playhead follows, playback holds until `endScrub`.
    func beginScrub() {
        leaveLive()
        resumeAfterScrub = isPlaying
        if isPlaying { pause() }
    }

    func scrub(to millis: UnixMillis) {
        guard isLive == false else { return }
        let target = clamped(millis)
        if target < windowFrom || target >= windowTo {
            go(to: Date(unixMillis: target))
        } else {
            position = target
            syncAll(force: false)
        }
    }

    func endScrub() {
        syncAll(force: true)
        if resumeAfterScrub { play() }
        resumeAfterScrub = false
    }

    func zoom(by factor: Double) {
        visibleSpan = TimelineRuler.zoomed(span: visibleSpan, factor: factor)
    }

    /// Whether the playhead is at the live edge (within a few seconds of now).
    var isAtLiveEdge: Bool { isLive || LiveEdge.isPinned(position: position, now: Date().unixMillis) }

    func step(seconds: Double) {
        seek(to: position + UnixMillis(seconds * 1000))
    }

    func play() {
        guard cameraIDs.isEmpty == false, isLive == false else { return }
        isPlaying = true
        lastTick = Date()
        startClock()
        syncAll(force: false)
    }

    func pause() {
        isPlaying = false
        lanes.values.forEach { $0.apply(rate: 0) }
    }

    func togglePlay() {
        isPlaying ? pause() : play()
    }

    /// `|«` / `»|` for `cameraID`: the previous/next gap edge, or the previous/next
    /// event or alarm marker when one comes before the gap.
    func skipTarget(forward: Bool, cameraID: String?) -> TimelineSkip.Target? {
        guard let cameraID, let timeline = timelines[cameraID] else { return nil }
        let spans = timeline.spans.map { TimelineGaps.Range(start: $0.start, end: $0.end) }
        let bounds = TimelineGaps.Range(start: windowFrom, end: min(windowTo, Date().unixMillis))
        let markers = timeline.events.map(\.createdAt) + timeline.alarms.map(\.createdAt)
        return TimelineSkip.target(from: position, forward: forward, spans: spans, bounds: bounds, markers: markers)
    }

    func skip(forward: Bool, cameraID: String?) {
        if let target = skipTarget(forward: forward, cameraID: cameraID) { seek(to: target.time) }
    }

    // MARK: Loading

    func reloadTimelines(resettingLanes: Bool = true) async {
        guard let api else { return }
        isLoading = true
        defer { isLoading = false }
        let from = windowFrom
        let to = windowTo
        // One camera at a time: a playback grid holds at most nine, and the
        // task-group version silently lost its results inside the app.
        for id in cameraIDs {
            do {
                let timeline = try await api.timeline(cameraID: id, from: from, to: to)
                timelines[id] = timeline
                loadErrors[id] = nil
            } catch {
                loadErrors[id] = error.localizedDescription
                SentinelLog.shared.error("playback timeline \(id) failed: \(error)", category: "playback")
            }
        }
        if resettingLanes {
            lanes.values.forEach { $0.unload() }
            syncAll(force: true)
        }
        startClock()
    }

    // MARK: Clock

    private func startClock() {
        guard clock == nil else { return }
        lastTick = Date()
        clock = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.tick() }
        }
    }

    func stop() {
        clock?.invalidate()
        clock = nil
        pause()
        lanes.values.forEach { $0.tearDown() }
    }

    private func tick() {
        let now = Date()
        let elapsed = now.timeIntervalSince(lastTick)
        lastTick = now
        if isLive {
            position = now.unixMillis
            if now >= Date(unixMillis: windowTo) { day = Calendar.current.startOfDay(for: now) }
            if isLoading == false, now.timeIntervalSince(lastLiveRefresh) > 20 {
                lastLiveRefresh = now
                Task { await reloadTimelines(resettingLanes: false) }
            }
            return
        }
        if isPlaying {
            let next = position + UnixMillis(elapsed * Double(rate) * 1000)
            let limit = min(windowTo - 1, Date().unixMillis)
            if next >= limit {
                position = limit
                pause()
            } else {
                position = next
            }
        }
        syncAll(force: false)
    }

    private func syncAll(force: Bool) {
        for lane in lanes.values {
            lane.sync(to: position, timeline: timelines[lane.cameraID], playing: isPlaying, rate: rate, force: force)
        }
    }

    // MARK: Mapping

    /// The span holding `t`, and where `t` is in that recorder's playlist (seconds).
    static func mediaTime(at t: UnixMillis, in timeline: V2.Timeline) -> (span: V2.Span, seconds: Double)? {
        timeline.mediaTime(at: t)
    }

    fileprivate func endpoint(cameraID: String, recorderID: String?, preferGateway: Bool) async throws -> VideoEndpoint {
        guard let video else { throw SentinelAPIError.signInRequired(message: "Sign in to continue.") }
        return try await video.endpoint(cameraID: cameraID, rights: [.playback], recorderID: recorderID, preferGateway: preferGateway)
    }

    fileprivate func reportDirectFailure(recorderID: String) {
        guard let video else { return }
        Task { await video.reportDirectFailure(recorderID: recorderID) }
    }
}

/// One camera in synchronized playback.
@MainActor
final class PlaybackLane: ObservableObject {
    enum State: Equatable {
        case idle
        case loading
        case playing
        case gap
        case failed(String)
    }

    let cameraID: String
    let player = AVPlayer()
    @Published private(set) var state: State = .idle
    @Published private(set) var route = ""

    private weak var controller: PlaybackController?
    private var recorderID: String??
    private var endpoint: VideoEndpoint?
    private var loadTask: Task<Void, Never>?
    private var statusObserver: NSKeyValueObservation?
    private var useGateway = false
    private var lastSeekAt = Date.distantPast
    private var retryAfter = Date.distantPast
    private var isLoading = false

    init(cameraID: String, controller: PlaybackController) {
        self.cameraID = cameraID
        self.controller = controller
        player.isMuted = true
        player.automaticallyWaitsToMinimizeStalling = true
    }

    func unload() {
        loadTask?.cancel()
        loadTask = nil
        isLoading = false
        statusObserver?.invalidate()
        statusObserver = nil
        player.replaceCurrentItem(with: nil)
        recorderID = nil
        endpoint = nil
        state = .idle
    }

    func tearDown() {
        unload()
    }

    func apply(rate: Float) {
        guard player.currentItem != nil else { return }
        if rate == 0 {
            player.pause()
        } else if state == .playing || state == .loading {
            player.rate = rate
        }
    }

    func sync(to position: UnixMillis, timeline: V2.Timeline?, playing: Bool, rate: Float, force: Bool) {
        guard let timeline else { return }
        guard let (span, seconds) = PlaybackController.mediaTime(at: position, in: timeline) else {
            if player.currentItem != nil { player.pause() }
            if state != .loading { state = .gap }
            return
        }
        // Token about to run out: load the same place with a fresh one.
        let expiring = endpoint.map { $0.expiresAt.timeIntervalSinceNow < 60 } ?? false
        if isLoading, recorderID == .some(span.recorderId) { return }
        if case .failed = state, Date() < retryAfter, recorderID == .some(span.recorderId) { return }
        if recorderID != .some(span.recorderId) || endpoint == nil || expiring {
            load(recorderID: span.recorderId, at: seconds, playing: playing, rate: rate, timeline: timeline)
            return
        }
        guard player.currentItem?.status == .readyToPlay else { return }
        if state == .gap || state == .idle { state = .playing }
        let current = player.currentTime().seconds
        let drift = abs(current - seconds)
        if (force || drift > PlaybackController.driftLimit), Date().timeIntervalSince(lastSeekAt) > (force ? 0 : 1.5) {
            lastSeekAt = Date()
            let tolerance = CMTime(seconds: force ? 0.1 : 0.4, preferredTimescale: 600)
            player.seek(to: CMTime(seconds: seconds, preferredTimescale: 600), toleranceBefore: tolerance, toleranceAfter: tolerance)
        }
        if playing {
            if player.rate != rate { player.rate = rate }
        } else if player.rate != 0 {
            player.pause()
        }
    }

    private func load(recorderID: String?, at seconds: Double, playing: Bool, rate: Float, timeline: V2.Timeline) {
        guard let controller else { return }
        loadTask?.cancel()
        self.recorderID = .some(recorderID)
        state = .loading
        isLoading = true
        let from = timeline.from
        let to = timeline.to
        let gateway = useGateway
        loadTask = Task { [weak self] in
            defer { self?.isLoading = false }
            do {
                let endpoint = try await controller.endpoint(cameraID: self?.cameraID ?? "", recorderID: recorderID, preferGateway: gateway)
                guard Task.isCancelled == false, let self else { return }
                self.endpoint = endpoint
                self.route = endpoint.isGateway ? "Gateway" : "Direct"
                let item = AVPlayerItem(url: endpoint.playback(from: from, to: to))
                self.observe(item, endpoint: endpoint)
                self.player.replaceCurrentItem(with: item)
                self.lastSeekAt = Date()
                // Resume where the clock is now (it kept running while the token loaded).
                let now = PlaybackController.mediaTime(at: controller.position, in: timeline)?.seconds ?? seconds
                await self.player.seek(to: CMTime(seconds: now, preferredTimescale: 600),
                                       toleranceBefore: CMTime(seconds: 0.1, preferredTimescale: 600),
                                       toleranceAfter: CMTime(seconds: 0.1, preferredTimescale: 600))
                guard Task.isCancelled == false else { return }
                if controller.isPlaying { self.player.rate = controller.rate } else { self.player.pause() }
            } catch {
                guard Task.isCancelled == false, let self else { return }
                let apiError = error as? SentinelAPIError
                self.state = .failed(apiError?.isForbidden == true ? "Your role can't play back this camera." : error.localizedDescription)
                self.endpoint = nil
                self.retryAfter = Date().addingTimeInterval(10)
            }
        }
    }

    private func observe(_ item: AVPlayerItem, endpoint: VideoEndpoint) {
        statusObserver?.invalidate()
        statusObserver = item.observe(\.status, options: [.new]) { [weak self] item, _ in
            let status = item.status
            let message = item.error?.localizedDescription
            DispatchQueue.main.async {
                guard let self else { return }
                switch status {
                case .readyToPlay:
                    if self.state == .loading { self.state = .playing }
                case .failed:
                    if endpoint.isGateway == false {
                        // The Recording Server's own address doesn't work from here.
                        self.controller?.reportDirectFailure(recorderID: endpoint.token.recorderId)
                        self.useGateway = true
                        self.endpoint = nil
                        self.recorderID = nil
                        self.state = .loading
                    } else {
                        self.state = .failed(message ?? "This recording couldn't be played.")
                        self.endpoint = nil
                        self.retryAfter = Date().addingTimeInterval(10)
                    }
                default:
                    break
                }
            }
        }
    }
}
