// LiveGridView.swift
// Lists the cameras the signed-in user may see and plays live video in tiles.

import SwiftUI
import AVKit

struct LiveGridView: View {
    @ObservedObject private var store = AppStore.shared
    @Environment(\.horizontalSizeClass) private var sizeClass
    // One remembered grid per width: the folded Duo and the unfolded one
    // (or iPad) each keep their own choice. 0 means never chosen.
    @AppStorage(LayoutLogic.columnsStorageKey(for: .compact)) private var compactColumns = 0
    @AppStorage(LayoutLogic.columnsStorageKey(for: .regular)) private var regularColumns = 0
    /// Grid or Focus (WAVE19); only regular width offers Focus.
    @AppStorage(LayoutLogic.liveLayoutStorageKey) private var regularLayout = LayoutLogic.LiveLayout.grid.rawValue
    @State private var path: [V2.Camera] = []

    private var width: LayoutWidth { LayoutWidth(sizeClass) }

    private var layout: LayoutLogic.LiveLayout {
        LayoutLogic.liveLayout(stored: regularLayout, for: width)
    }

    private var columns: Int {
        LayoutLogic.liveColumns(stored: width == .regular ? regularColumns : compactColumns, for: width)
    }

    private func choose(_ count: Int) {
        if width == .regular {
            regularColumns = count
            regularLayout = LayoutLogic.LiveLayout.grid.rawValue
        } else {
            compactColumns = count
        }
    }

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
                        } else if layout == .focus {
                            LiveFocusLayout(cameras: store.cameras)
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
                ForEach(LayoutLogic.columnChoices(for: width), id: \.self) { count in
                    Button {
                        choose(count)
                    } label: {
                        if count == columns && layout == .grid {
                            Label(LayoutLogic.gridLabel(columns: count), systemImage: "checkmark")
                        } else {
                            Text(LayoutLogic.gridLabel(columns: count))
                        }
                    }
                }
                if width == .regular {
                    Divider()
                    Button {
                        regularLayout = LayoutLogic.LiveLayout.focus.rawValue
                    } label: {
                        if layout == .focus {
                            Label("Focus", systemImage: "checkmark")
                        } else {
                            Label("Focus", systemImage: "rectangle.leadinghalf.inset.filled")
                        }
                    }
                }
            } label: {
                Image(systemName: layout == .focus ? "rectangle.leadinghalf.inset.filled" : LayoutLogic.gridSymbol(columns: columns))
                    .font(.title3)
                    .foregroundStyle(.white.opacity(0.75))
                    .frame(width: 40, height: 40)
                    .background(SentinelTheme.panel, in: Circle())
                    .overlay(Circle().stroke(SentinelTheme.line, lineWidth: 1))
            }
            .accessibilityLabel("Grid size")
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

/// Focus (WAVE19): the selected camera big on the left, the others stacked
/// in a column on the right; tapping a small tile swaps it in. Every tile is
/// a LiveStreamView keyed by camera in `VideoPools.live`, so a swap moves a
/// stream between slots without restarting it. The focus lives in the store
/// so a fold and unfold come back to the same camera.
struct LiveFocusLayout: View {
    let cameras: [V2.Camera]
    @ObservedObject private var store = AppStore.shared

    var body: some View {
        let arrangement = LayoutLogic.focusArrangement(cameraIDs: cameras.map(\.id), focused: store.focusedCameraID)
        GeometryReader { geo in
            let column = LayoutLogic.focusColumnWidth(available: geo.size.width)
            HStack(alignment: .top, spacing: 12) {
                if let id = arrangement.main, let main = cameras.first(where: { $0.id == id }) {
                    NavigationLink(value: main) {
                        CameraTile(camera: main, large: true)
                    }
                    // A new identity per camera, so the slot hands its pool
                    // hold over on a swap (the stream itself is pooled).
                    .id(main.id)
                    .buttonStyle(.plain)
                    .frame(maxWidth: .infinity, alignment: .top)
                    .accessibilityHint("Opens the camera")
                }
                if arrangement.others.isEmpty == false {
                    ScrollView(showsIndicators: false) {
                        VStack(spacing: 10) {
                            ForEach(arrangement.others, id: \.self) { id in
                                if let camera = cameras.first(where: { $0.id == id }) {
                                    Button {
                                        Haptics.tap()
                                        withAnimation(.easeInOut(duration: 0.2)) { store.focusedCameraID = id }
                                    } label: {
                                        CameraTile(camera: camera)
                                    }
                                    .buttonStyle(.plain)
                                    .accessibilityHint("Shows this camera large")
                                }
                            }
                        }
                    }
                    .frame(width: column)
                }
            }
            .padding(12)
        }
    }
}

struct CameraTile: View {
    let camera: V2.Camera
    /// The big tile in Live's Focus layout: a larger caption.
    var large = false

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
                    .font(large ? .headline : .footnote.weight(.semibold))
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
