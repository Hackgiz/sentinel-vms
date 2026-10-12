// LiveGridView.swift
// Lists the cameras the signed-in user may see and plays live video in tiles.

import SwiftUI
import AVKit

struct LiveGridView: View {
    @ObservedObject private var store = AppStore.shared
    @State private var columns: Int = 2
    @State private var path: [V2.Camera] = []

    var body: some View {
        NavigationStack(path: $path) {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()

                VStack(spacing: 0) {
                    liveHeader
                    Group {
                        if store.cameras.isEmpty, store.lastError != nil {
                            errorState(store.lastError ?? "")
                        } else if store.cameras.isEmpty, store.hasLoaded {
                            emptyState
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
            .navigationDestination(for: V2.Camera.self) { camera in
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

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "video.slash.fill").font(.system(size: 40)).foregroundStyle(.white.opacity(0.35))
            Text("No cameras").font(.headline).foregroundStyle(.white)
            Text("Your sign-in can't see any cameras yet. Ask an administrator to add you to a camera group.")
                .font(.caption).foregroundStyle(.white.opacity(0.55))
                .multilineTextAlignment(.center).padding(.horizontal, 40)
        }
    }

    private func errorState(_ message: String) -> some View {
        VStack(spacing: 14) {
            Image(systemName: "wifi.exclamationmark")
                .font(.system(size: 44)).foregroundStyle(SentinelTheme.amber)
            Text("Couldn't reach the Management Server").font(.headline).foregroundStyle(.white)
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
    let camera: V2.Camera

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ZStack(alignment: .topLeading) {
                LiveStreamView(camera: camera)
                    .aspectRatio(16/9, contentMode: .fit)
                    .clipShape(UnevenRoundedRectangle(topLeadingRadius: 10, topTrailingRadius: 10, style: .continuous))

                HStack {
                    if camera.recording && camera.isOnline {
                        recordingPill
                    }
                    Spacer()
                    SentinelStatusBadge(status: camera.statusLabel)
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
                    Text(camera.location.isEmpty ? camera.resolutionLabel : camera.location)
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
}
