import AppKit
import AVFoundation
import SwiftUI
import SentinelCore

/// What a live tile shows over its video.
@MainActor
final class LivePlayerStatus: ObservableObject {
    enum State: Equatable {
        case connecting
        case playing
        case failed(String)
    }

    @Published var state: State = .connecting
    /// "Direct" (recording server) or "Gateway" (through the management server).
    @Published var route: String = ""
}

/// Live video of one camera: LL-HLS from `{baseUrl}/live/{id}/index.m3u8`
/// played by AVPlayer (VideoToolbox decode). WebRTC would need a third-party
/// stack on macOS; HLS needs none and the gateway sends segments with a
/// Content-Length, which AVPlayer requires.
///
/// The video token is part of the URL and lasts ≤ 10 minutes, so shortly
/// before it expires a second player is prepared on a fresh token and swapped
/// in once it plays (no black frame). If the recording server's direct
/// address fails, the broker switches that server to the gateway.
struct LiveVideoView: NSViewRepresentable {
    let cameraID: String
    let broker: VideoTokenBroker
    var gravity: AVLayerVideoGravity = .resizeAspect
    let status: LivePlayerStatus

    func makeNSView(context: Context) -> LiveHLSPlayerView {
        let view = LiveHLSPlayerView()
        view.playerLayer.videoGravity = gravity
        view.start(cameraID: cameraID, broker: broker, status: status)
        return view
    }

    func updateNSView(_ view: LiveHLSPlayerView, context: Context) {
        view.playerLayer.videoGravity = gravity
        if view.cameraID != cameraID {
            view.start(cameraID: cameraID, broker: broker, status: status)
        }
    }

    static func dismantleNSView(_ view: LiveHLSPlayerView, coordinator: ()) {
        view.stop()
    }
}

@MainActor
final class LiveHLSPlayerView: NSView {
    let playerLayer = AVPlayerLayer()
    private(set) var cameraID: String?

    private var broker: VideoTokenBroker?
    private weak var status: LivePlayerStatus?
    private var player: AVPlayer?
    private var endpoint: VideoEndpoint?
    private var standby: (player: AVPlayer, endpoint: VideoEndpoint, observer: NSKeyValueObservation)?
    private var itemObserver: NSKeyValueObservation?
    private var notificationTokens: [NSObjectProtocol] = []
    private var connectTask: Task<Void, Never>?
    private var refreshTask: Task<Void, Never>?
    private var watchdog: Timer?
    private var lastProgress: (time: CMTime, at: Date) = (.zero, Date())
    private var failures = 0

    override func makeBackingLayer() -> CALayer { playerLayer }

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        playerLayer.backgroundColor = NSColor.black.cgColor
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    func start(cameraID: String, broker: VideoTokenBroker, status: LivePlayerStatus) {
        stop()
        self.cameraID = cameraID
        self.broker = broker
        self.status = status
        failures = 0
        connect()
        watchdog = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.checkProgress() }
        }
    }

    func stop() {
        connectTask?.cancel()
        refreshTask?.cancel()
        watchdog?.invalidate()
        watchdog = nil
        dropObservers()
        standby?.observer.invalidate()
        standby?.player.pause()
        standby = nil
        player?.pause()
        playerLayer.player = nil
        player = nil
        endpoint = nil
        cameraID = nil
    }

    private func connect(after delay: TimeInterval = 0) {
        guard let cameraID, let broker else { return }
        connectTask?.cancel()
        if delay > 0 || status?.state != .playing {
            status?.state = .connecting
        }
        connectTask = Task { [weak self] in
            if delay > 0 { try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) }
            guard Task.isCancelled == false else { return }
            do {
                let endpoint = try await broker.endpoint(cameraID: cameraID)
                guard Task.isCancelled == false else { return }
                self?.play(endpoint)
            } catch {
                guard Task.isCancelled == false, let self else { return }
                let apiError = error as? SentinelAPIError
                if apiError?.isForbidden == true {
                    self.status?.state = .failed("Your role can't view this camera.")
                    return
                }
                self.status?.state = .failed(error.localizedDescription)
                self.failures += 1
                self.connect(after: self.backoff)
            }
        }
    }

    private var backoff: TimeInterval { min(15, pow(2, Double(min(failures, 4)))) }

    private func makePlayer(_ endpoint: VideoEndpoint) -> AVPlayer {
        let item = AVPlayerItem(url: endpoint.liveHLS)
        item.preferredForwardBufferDuration = 1
        item.configuredTimeOffsetFromLive = CMTime(seconds: 1, preferredTimescale: 600)
        let player = AVPlayer(playerItem: item)
        player.isMuted = true
        player.automaticallyWaitsToMinimizeStalling = false
        return player
    }

    private func play(_ endpoint: VideoEndpoint) {
        dropObservers()
        player?.pause()
        let player = makePlayer(endpoint)
        self.player = player
        self.endpoint = endpoint
        playerLayer.player = player
        status?.route = endpoint.isGateway ? "Gateway" : "Direct"
        lastProgress = (.zero, Date())
        observe(player)
        player.play()
        scheduleRefresh(for: endpoint)
    }

    private func observe(_ player: AVPlayer) {
        guard let item = player.currentItem else { return }
        itemObserver = item.observe(\.status, options: [.new]) { [weak self] item, _ in
            let failed = item.status == .failed
            let message = item.error?.localizedDescription
            DispatchQueue.main.async {
                guard let self else { return }
                if failed { self.handleFailure(message) } else if item.status == .readyToPlay { self.status?.state = .playing }
            }
        }
        let center = NotificationCenter.default
        notificationTokens = [
            center.addObserver(forName: .AVPlayerItemFailedToPlayToEndTime, object: item, queue: .main) { [weak self] note in
                let error = note.userInfo?[AVPlayerItemFailedToPlayToEndTimeErrorKey] as? Error
                MainActor.assumeIsolated { self?.handleFailure(error?.localizedDescription) }
            }
        ]
    }

    private func dropObservers() {
        itemObserver?.invalidate()
        itemObserver = nil
        notificationTokens.forEach(NotificationCenter.default.removeObserver)
        notificationTokens = []
    }

    private func checkProgress() {
        guard let player else { return }
        let now = player.currentTime()
        if CMTimeCompare(now, lastProgress.time) != 0 {
            if now.seconds > 0 { failures = 0 }
            lastProgress = (now, Date())
            if status?.state != .playing, player.currentItem?.status == .readyToPlay { status?.state = .playing }
            return
        }
        // Not advancing: a stall while playing, or a start that never happens.
        let limit: TimeInterval = status?.state == .playing ? 10 : 20
        if Date().timeIntervalSince(lastProgress.at) > limit, player.timeControlStatus != .paused {
            handleFailure(status?.state == .playing ? "The live stream stalled." : "The live stream didn't start.")
        }
    }

    private func handleFailure(_ message: String?) {
        failures += 1
        guard let endpoint else {
            connect(after: backoff)
            return
        }
        dropObservers()
        player?.pause()
        let broker = self.broker
        if endpoint.isGateway == false {
            // The recording server's own address didn't work from here: go through the management server.
            Task { await broker?.reportDirectFailure(recorderID: endpoint.token.recorderId) }
        }
        if failures >= 2 {
            Task { await broker?.invalidate(cameraID: endpoint.token.cameraId) }
        }
        status?.state = .failed(message ?? "The live stream stopped.")
        lastProgress = (.zero, Date())
        connect(after: backoff)
    }

    // MARK: Token refresh

    private func scheduleRefresh(for endpoint: VideoEndpoint) {
        refreshTask?.cancel()
        // The broker hands out a new token once fewer than 90 s are left.
        let lead = VideoTokenBroker.reuseMargin - 15
        let wait = max(5, endpoint.expiresAt.timeIntervalSinceNow - lead)
        refreshTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000))
            guard Task.isCancelled == false else { return }
            await self?.prepareStandby()
        }
    }

    private func prepareStandby() async {
        guard let cameraID, let broker else { return }
        guard let next = try? await broker.endpoint(cameraID: cameraID) else {
            // Couldn't get a token now; try again shortly while the current one still works.
            refreshTask = Task { [weak self] in
                try? await Task.sleep(nanoseconds: 10_000_000_000)
                await self?.prepareStandby()
            }
            return
        }
        let standbyPlayer = makePlayer(next)
        let observer = standbyPlayer.observe(\.timeControlStatus, options: [.new]) { [weak self] player, _ in
            guard player.timeControlStatus == .playing else { return }
            DispatchQueue.main.async { self?.promoteStandby() }
        }
        standby?.observer.invalidate()
        standby = (standbyPlayer, next, observer)
        standbyPlayer.play()
        // If the standby never starts, fall back to a plain reconnect before the old token runs out.
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 20_000_000_000)
            guard let self, let standby = self.standby, standby.player === standbyPlayer else { return }
            standby.observer.invalidate()
            self.standby = nil
            self.play(next)
        }
    }

    private func promoteStandby() {
        guard let standby else { return }
        standby.observer.invalidate()
        self.standby = nil
        dropObservers()
        player?.pause()
        player = standby.player
        endpoint = standby.endpoint
        playerLayer.player = standby.player
        status?.route = standby.endpoint.isGateway ? "Gateway" : "Direct"
        lastProgress = (.zero, Date())
        observe(standby.player)
        scheduleRefresh(for: standby.endpoint)
    }
}
