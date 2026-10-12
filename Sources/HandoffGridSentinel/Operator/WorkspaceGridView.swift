import AppKit
import SwiftUI
import SentinelCore

/// The camera grid in the middle of the workspace. Live streams in Live mode,
/// the shared playback clock in Playback mode; same tiles and selection either way.
struct WorkspaceGridView: View {
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator
    @ObservedObject var workspace: WorkspaceModel
    /// nil in windows that only show live video (monitor windows).
    var controller: PlaybackController?
    var mode: WorkspaceMode = .live

    private let spacing: CGFloat = 8

    var body: some View {
        GeometryReader { proxy in
            Group {
                if cameras.cameras.isEmpty {
                    emptyState
                } else if let focused = workspace.focusedCameraID, workspace.cameraIDs.contains(focused) {
                    tile(cameraID: focused, index: workspace.grid.tileIndex(of: focused) ?? 0)
                        .padding(spacing)
                } else {
                    grid(in: proxy.size)
                }
            }
            .frame(width: proxy.size.width, height: proxy.size.height)
        }
        .background(SentinelTheme.background)
    }

    private func grid(in size: CGSize) -> some View {
        let g = workspace.grid
        let cols = CGFloat(g.columns), rows = CGFloat(g.rows)
        let cellW = max((size.width - spacing * (cols + 1)) / cols, 40)
        let cellH = max((size.height - spacing * (rows + 1)) / rows, 30)
        return ZStack(alignment: .topLeading) {
            ForEach(Array(g.slots.enumerated()), id: \.offset) { index, cameraID in
                Group {
                    if let cameraID {
                        tile(cameraID: cameraID, index: index)
                    } else {
                        emptyTile(index: index)
                    }
                }
                .frame(width: cellW, height: cellH)
                .offset(x: spacing + CGFloat(index % g.columns) * (cellW + spacing),
                        y: spacing + CGFloat(index / g.columns) * (cellH + spacing))
            }
        }
        .frame(width: size.width, height: size.height, alignment: .topLeading)
    }

    // MARK: Tiles

    @ViewBuilder
    private func tile(cameraID: String, index: Int) -> some View {
        if let camera = cameras.camera(cameraID) {
            Group {
                if mode == .playback, let controller {
                    PlaybackGridTile(controller: controller, camera: camera, isSelected: workspace.selectedCameraID == cameraID)
                } else {
                    LiveTile(camera: camera, isSelected: workspace.selectedCameraID == cameraID, fill: workspace.fillTiles)
                }
            }
            .onTapGesture(count: 2) {
                workspace.focusedCameraID = workspace.focusedCameraID == nil ? cameraID : nil
            }
            .simultaneousGesture(TapGesture().onEnded { workspace.selectedCameraID = cameraID })
            .onDrag { CameraDrag.provider([cameraID]) }
            .onDrop(of: [.plainText], isTargeted: nil) { providers in
                CameraDrag.decode(providers) { ids in workspace.place(ids, at: index) }
            }
            .contextMenu {
                Button { OperatorWindows.shared.openCamera(cameraID, name: camera.name) } label: {
                    Label("Pop Out Window", systemImage: "macwindow.on.rectangle")
                }
                Button { workspace.focusedCameraID = cameraID } label: {
                    Label("Fill Grid", systemImage: "arrow.up.left.and.arrow.down.right")
                }
                if mode == .live {
                    Button { navigator.openPlayback(cameraIDs: [cameraID], at: Date().addingTimeInterval(-60)) } label: {
                        Label("Play Last Minute", systemImage: "gobackward.60")
                    }
                }
                Divider()
                Button { workspace.clearTile(index) } label: { Label("Remove from Grid", systemImage: "xmark") }
            }
        } else {
            emptyTile(index: index, message: "Camera not available")
        }
    }

    private func emptyTile(index: Int, message: String = "Drag a camera here") -> some View {
        DropTile(message: message) { ids in workspace.place(ids, at: index) }
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "video.slash").font(.largeTitle).foregroundStyle(.secondary)
            Text(cameras.isLoaded ? "No cameras you can view on this site." : "Loading cameras…")
                .foregroundStyle(.secondary)
            if let error = cameras.lastError {
                Text(error).font(.caption).foregroundStyle(.red)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// An empty tile: says what to do and takes a dropped camera.
private struct DropTile: View {
    let message: String
    let onDrop: ([String]) -> Void
    @State private var isTargeted = false

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                .fill(Color.black.opacity(isTargeted ? 0.6 : 0.82))
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                .strokeBorder(isTargeted ? SentinelTheme.accent : Color.white.opacity(0.14),
                              style: StrokeStyle(lineWidth: isTargeted ? 2 : 1, dash: [5, 4]))
            VStack(spacing: 6) {
                Image(systemName: "plus.rectangle.on.rectangle").foregroundStyle(.secondary)
                Text(message).font(.caption).foregroundStyle(.secondary)
            }
        }
        .contentShape(Rectangle())
        .onDrop(of: [.plainText], isTargeted: $isTargeted) { providers in
            CameraDrag.decode(providers, completion: onDrop)
        }
    }
}

/// A playback tile follows the shared clock through its lane; cameras past
/// the nine the clock can drive say so.
private struct PlaybackGridTile: View {
    @ObservedObject var controller: PlaybackController
    let camera: V2.Camera
    let isSelected: Bool

    var body: some View {
        if let lane = controller.lane(camera.id) {
            PlaybackTile(lane: lane, camera: camera, error: controller.loadErrors[camera.id], isSelected: isSelected)
        } else {
            ZStack {
                RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).fill(Color.black.opacity(0.85))
                VStack(spacing: 4) {
                    Text(camera.name).font(.caption.weight(.semibold)).foregroundStyle(.white)
                    Text("Playback follows up to \(PlaybackController.maxCameras) cameras.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
            }
        }
    }
}
