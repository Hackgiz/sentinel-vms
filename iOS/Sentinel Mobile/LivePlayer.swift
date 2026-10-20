// LivePlayer.swift
// Live video for one camera: a video token from the Management Server, then
// LL-HLS at `{base}/live/{id}/index.m3u8` straight from the Recording Server
// (`baseUrl`), falling back to the Management Server's gateway (`gatewayUrl`)
// when the direct address doesn't work (e.g. on cellular). The token is
// renewed before it expires.

import SwiftUI
import AVKit

@MainActor
final class LivePlayerModel: ObservableObject {
    enum Phase: Equatable {
        case idle
        case loading
        case playing
        case failed(String)
    }

    @Published private(set) var phase: Phase = .idle
    /// "Direct" or "Gateway": how the video is reaching the phone.
    @Published private(set) var route = ""
    let player = AVPlayer()
    let cameraID: String

    private var task: Task<Void, Never>?
    private var cancelWait: (() -> Void)?
    private var statusObserver: NSKeyValueObservation?
    private var forceGateway = false

    /// Wait before trying again after a failure on the gateway.
    static let retryDelay: UInt64 = 5_000_000_000
    /// Replace the item this long before its token expires.
    static let renewMargin: TimeInterval = 80

    init(cameraID: String, muted: Bool = true) {
        self.cameraID = cameraID
        player.isMuted = muted
        // Leave automaticallyWaitsToMinimizeStalling on (the default). Off,
        // the player never rebuffers once the buffer drains: through Sentinel
        // Cloud's relay it froze on one frame, polling the playlist without
        // asking for another segment (the LAN never drained it). Playback
        // (CameraDetailView) already keeps it on.
    }

    deinit {
        task?.cancel()
    }

    func start() {
        guard task == nil else { return }
        phase = .loading
        task = Task { [weak self] in await self?.run() }
    }

    func stop() {
        task?.cancel()
        task = nil
        cancelWait?()
        cancelWait = nil
        statusObserver?.invalidate()
        statusObserver = nil
        player.pause()
        player.replaceCurrentItem(with: nil)
        phase = .idle
    }

    fileprivate enum Outcome: Sendable {
        case failed(String)
        case expiring
        case cancelled
    }

    private func run() async {
        while Task.isCancelled == false {
            guard let broker = SentinelSession.shared.broker else {
                phase = .failed("Not signed in.")
                return
            }
            let endpoint: VideoEndpoint
            do {
                endpoint = try await broker.endpoint(cameraID: cameraID, rights: [.live], preferGateway: forceGateway)
            } catch {
                guard Task.isCancelled == false else { return }
                if let api = error as? SentinelAPIError, api.isForbidden || api.isNotFound {
                    phase = .failed("You can't view this camera.")
                    return
                }
                phase = .failed(error.localizedDescription)
                try? await Task.sleep(nanoseconds: Self.retryDelay)
                continue
            }
            guard Task.isCancelled == false else { return }

            route = endpoint.isGateway ? "Gateway" : "Direct"
            let item = AVPlayerItem(url: endpoint.liveHLS)
            // Start as soon as a little is buffered and stay close to live.
            // Segments are 2 s, so one second from live was never reachable
            // ("Cannot get that close to live"); on the LAN the player coped,
            // but through Sentinel Cloud's relay it skipped segments chasing
            // the edge and froze on one frame. Direct: one segment back.
            // Gateway/relay: three segments back (the HLS minimum) with a
            // deeper buffer, trading a few seconds of delay for motion.
            let remote = endpoint.isGateway
            item.preferredForwardBufferDuration = remote ? 3.0 : 1.0
            item.configuredTimeOffsetFromLive = CMTime(seconds: remote ? 6 : 2, preferredTimescale: 600)
            player.replaceCurrentItem(with: item)
            player.play()

            let renewAt = max(5, endpoint.expiresAt.timeIntervalSinceNow - Self.renewMargin)
            switch await wait(for: item, renewAfter: renewAt) {
            case .cancelled:
                return
            case .expiring:
                continue
            case .failed(let message):
                if endpoint.isGateway == false {
                    // The recording server's own address didn't work from here.
                    await broker.reportDirectFailure(recorderID: endpoint.token.recorderId)
                    forceGateway = true
                    continue
                }
                phase = .failed(message)
                try? await Task.sleep(nanoseconds: Self.retryDelay)
            }
        }
    }

    private func wait(for item: AVPlayerItem, renewAfter: TimeInterval) async -> Outcome {
        let watch = ItemWatch()
        cancelWait = { watch.finish(.cancelled) }
        watch.observer = item.observe(\.status, options: [.new]) { [weak self] item, _ in
            switch item.status {
            case .readyToPlay:
                Task { @MainActor in self?.phase = .playing }
            case .failed:
                watch.finish(.failed(item.error?.localizedDescription ?? "The video couldn't be played."))
            default:
                break
            }
        }
        watch.timer = Task {
            try? await Task.sleep(nanoseconds: UInt64(renewAfter * 1_000_000_000))
            if Task.isCancelled == false { watch.finish(.expiring) }
        }
        let outcome = await watch.outcome()
        watch.stop()
        cancelWait = nil
        return outcome
    }
}

/// Resolves once, with whichever happens first: the item fails, its token is
/// about to expire, or the player is stopped.
private final class ItemWatch: @unchecked Sendable {
    private let lock = NSLock()
    private var result: LivePlayerModel.Outcome?
    private var continuation: CheckedContinuation<LivePlayerModel.Outcome, Never>?
    var observer: NSKeyValueObservation?
    var timer: Task<Void, Never>?

    func finish(_ outcome: LivePlayerModel.Outcome) {
        lock.lock()
        guard result == nil else { lock.unlock(); return }
        result = outcome
        let waiting = continuation
        continuation = nil
        lock.unlock()
        waiting?.resume(returning: outcome)
    }

    func outcome() async -> LivePlayerModel.Outcome {
        await withCheckedContinuation { (continuation: CheckedContinuation<LivePlayerModel.Outcome, Never>) in
            lock.lock()
            if let result {
                lock.unlock()
                continuation.resume(returning: result)
            } else {
                self.continuation = continuation
                lock.unlock()
            }
        }
    }

    func stop() {
        observer?.invalidate()
        observer = nil
        timer?.cancel()
        timer = nil
    }
}

/// A bare `AVPlayerLayer` for a tile.
struct PlayerLayerView: UIViewRepresentable {
    let player: AVPlayer

    func makeUIView(context: Context) -> PlayerView {
        let v = PlayerView()
        v.playerLayer.player = player
        return v
    }

    func updateUIView(_ uiView: PlayerView, context: Context) {
        if uiView.playerLayer.player !== player { uiView.playerLayer.player = player }
    }
}

final class PlayerView: UIView {
    override class var layerClass: AnyClass { AVPlayerLayer.self }
    var playerLayer: AVPlayerLayer { layer as! AVPlayerLayer }

    override init(frame: CGRect) {
        super.init(frame: frame)
        playerLayer.videoGravity = .resizeAspect
        backgroundColor = .black
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }
}

/// Live video for a camera: the player plus loading / error states. Starts
/// when it appears and stops when it leaves the screen.
struct LiveStreamView: View {
    let camera: V2.Camera
    var zoomable = false
    @StateObject private var model: LivePlayerModel

    init(camera: V2.Camera, zoomable: Bool = false) {
        self.camera = camera
        self.zoomable = zoomable
        _model = StateObject(wrappedValue: LivePlayerModel(cameraID: camera.id))
    }

    var body: some View {
        ZStack {
            Color.black
            if camera.status.online {
                if zoomable {
                    ZoomableVideoView(player: model.player)
                } else {
                    PlayerLayerView(player: model.player)
                }
                overlay
            } else {
                offline
            }
        }
        .onAppear { if camera.status.online { model.start() } }
        .onDisappear { model.stop() }
        .onChange(of: camera.status.online) { online in
            if online { model.start() } else { model.stop() }
        }
    }

    @ViewBuilder
    private var overlay: some View {
        switch model.phase {
        case .idle, .loading:
            ProgressView().tint(.white.opacity(0.6))
        case .failed(let message):
            VStack(spacing: 6) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(SentinelTheme.amber)
                Text(message)
                    .font(.caption2).foregroundStyle(.white.opacity(0.7))
                    .multilineTextAlignment(.center).lineLimit(3)
                    .padding(.horizontal, 8)
            }
        case .playing:
            EmptyView()
        }
    }

    private var offline: some View {
        ZStack {
            LinearGradient(colors: [Color(white: 0.10), Color(white: 0.03)], startPoint: .topLeading, endPoint: .bottomTrailing)
            VStack(spacing: 8) {
                Image(systemName: "video.slash.fill").font(.title2).foregroundStyle(SentinelTheme.alarm.opacity(0.7))
                Text(camera.status.message.isEmpty ? "Offline" : camera.status.message)
                    .font(.caption2.weight(.medium)).foregroundStyle(.white.opacity(0.55))
            }
        }
    }
}
