// LiveGridView.swift
// Lists cameras pulled from the Mac and plays HLS in adaptive tiles.

import SwiftUI
import AVKit

struct LiveGridView: View {
    @ObservedObject private var store = AppStore.shared
    @State private var columns: Int = 2
    @State private var path: [SentinelCameraSummary] = []

    var body: some View {
        NavigationStack(path: $path) {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()

                VStack(spacing: 0) {
                    liveHeader
                    Group {
                        if store.cameras.isEmpty, store.lastError != nil {
                            errorState(store.lastError ?? "")
                        } else if store.cameras.isEmpty {
                            loadingState
                        } else {
                            gridContent
                        }
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            // Hide the big empty large-title bar; use a compact custom header.
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: SentinelCameraSummary.self) { camera in
                CameraDetailView(camera: camera)
            }
            // Deep-link from a tapped push notification → open that camera.
            // Consume the pending route on appear and whenever it changes or the
            // camera list arrives, so a cold-launch tap (route set before this
            // view exists, or before cameras load) still navigates reliably.
            .onAppear { consumePendingRoute() }
            .onChange(of: store.routedCameraID) { _ in consumePendingRoute() }
            .onChange(of: store.cameras.count) { _ in consumePendingRoute() }
        }
    }

    private var liveHeader: some View {
        HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
            VStack(alignment: .leading, spacing: 3) {
                Text("Live")
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                HStack(spacing: 6) {
                    Circle().fill(SentinelTheme.recording).frame(width: 7, height: 7)
                    Text("\(store.onlineCount)/\(store.cameras.count) online")
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.white.opacity(0.6))
                }
            }
            Spacer(minLength: 8)
            Menu {
                Button("1 × 1") { columns = 1 }
                Button("2 × 2") { columns = 2 }
                Button("3 × 3") { columns = 3 }
            } label: {
                Image(systemName: columns == 1 ? "square" : (columns == 2 ? "square.grid.2x2" : "square.grid.3x3"))
                    .font(.title3)
                    .foregroundStyle(.white.opacity(0.75))
                    .frame(width: 40, height: 40)
                    .background(SentinelTheme.panel, in: Circle())
                    .overlay(Circle().stroke(SentinelTheme.line, lineWidth: 1))
            }
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.top, 4)
        .padding(.bottom, SentinelTheme.Space.sm)
    }

    private func consumePendingRoute() {
        guard let id = store.routedCameraID, let camera = store.camera(id: id) else { return }
        path = [camera]
        store.routedCameraID = nil
    }

    private var gridContent: some View {
        ScrollView {
            LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 10), count: columns), spacing: 10) {
                ForEach(store.cameras) { camera in
                    NavigationLink(value: camera) { CameraTile(camera: camera) }
                        .buttonStyle(.plain)
                }
            }
            .padding(12)
        }
        .refreshable { await store.refreshAll() }
    }

    private var loadingState: some View {
        VStack(spacing: 12) {
            ProgressView().tint(SentinelTheme.accent)
            Text("Loading cameras…").font(.caption).foregroundStyle(.white.opacity(0.5))
        }
    }

    private func errorState(_ message: String) -> some View {
        VStack(spacing: 14) {
            Image(systemName: "wifi.exclamationmark")
                .font(.system(size: 44)).foregroundStyle(SentinelTheme.amber)
            Text("Couldn't reach Sentinel").font(.headline).foregroundStyle(.white)
            Text(message)
                .font(.caption).foregroundStyle(.white.opacity(0.6))
                .multilineTextAlignment(.center).padding(.horizontal, 32)
            Button("Retry") { Task { await store.refreshAll() } }
                .buttonStyle(.borderedProminent).tint(SentinelTheme.accent).padding(.top, 4)
        }
        .padding()
    }
}

struct CameraTile: View {
    let camera: SentinelCameraSummary

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ZStack(alignment: .topLeading) {
                Group {
                    if let url = SentinelSession.shared.hlsURL(for: camera) {
                        LiveHLSPlayer(url: url, isMuted: true)
                            .aspectRatio(16/9, contentMode: .fit)
                    } else {
                        placeholderTile
                            .aspectRatio(16/9, contentMode: .fit)
                    }
                }
                .clipShape(UnevenRoundedRectangle(topLeadingRadius: 10, topTrailingRadius: 10, style: .continuous))

                HStack {
                    if camera.isRecording {
                        recordingPill
                    }
                    Spacer()
                    SentinelStatusBadge(status: camera.status)
                }
                .padding(8)
            }

            VStack(alignment: .leading, spacing: 2) {
                Text(camera.name)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.white)
                    .lineLimit(1)
                HStack(spacing: 6) {
                    Image(systemName: "mappin.circle.fill")
                        .font(.caption2)
                    Text(camera.location)
                        .font(.caption2)
                        .lineLimit(1)
                }
                .foregroundStyle(.white.opacity(0.5))
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.panel)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(SentinelTheme.line, lineWidth: 1)
        )
    }

    private var recordingPill: some View {
        HStack(spacing: 4) {
            Circle()
                .fill(SentinelTheme.alarm)
                .frame(width: 6, height: 6)
            Text("REC")
                .font(.caption2.weight(.heavy))
                .foregroundStyle(.white)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(.black.opacity(0.55), in: Capsule())
    }

    private var placeholderTile: some View {
        ZStack {
            LinearGradient(
                colors: [Color(white: 0.10), Color(white: 0.03)],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            VStack(spacing: 8) {
                Image(systemName: camera.status == "offline" ? "video.slash.fill" : "video.fill")
                    .font(.title2)
                    .foregroundStyle(camera.status == "offline" ? SentinelTheme.alarm.opacity(0.7) : .white.opacity(0.35))
                Text(camera.status == "offline" ? "Offline" : "\(camera.resolution) · \(camera.fps)fps")
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(.white.opacity(0.55))
            }
        }
    }
}

struct LiveHLSPlayer: UIViewRepresentable {
    let url: URL
    let isMuted: Bool

    func makeUIView(context: Context) -> PlayerView {
        let v = PlayerView()
        v.play(url: url, isMuted: isMuted)
        return v
    }

    func updateUIView(_ uiView: PlayerView, context: Context) {}
}

final class PlayerView: UIView {
    override class var layerClass: AnyClass { AVPlayerLayer.self }
    private var playerLayer: AVPlayerLayer { layer as! AVPlayerLayer }

    func play(url: URL, isMuted: Bool) {
        let item = AVPlayerItem(url: url)
        // Start playback as soon as a small buffer is ready, instead of waiting
        // for AVPlayer's default ~3s of forward buffer. Combined with MediaMTX's
        // 3×2s segment window this keeps glass-to-glass latency in the 6-8s range.
        item.preferredForwardBufferDuration = 1.0
        if #available(iOS 16.0, *) {
            // Tell AVPlayer to stay close to live (1s behind, not the default ~5s).
            item.configuredTimeOffsetFromLive = CMTime(seconds: 1, preferredTimescale: 600)
        }

        let player = AVPlayer(playerItem: item)
        player.isMuted = isMuted
        player.automaticallyWaitsToMinimizeStalling = false
        playerLayer.player = player
        playerLayer.videoGravity = .resizeAspect
        player.play()
    }
}
