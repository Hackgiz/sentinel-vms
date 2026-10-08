import AVFoundation
import AVKit
import AppKit
import SwiftUI
import Vision
import SentinelCore
import SentinelMediaServer

enum CameraVideoContentMode: String, CaseIterable, Identifiable {
    case fit = "Fit"
    case fill = "Fill"

    var id: String { rawValue }

    var swiftUIContentMode: ContentMode {
        switch self {
        case .fit: return .fit
        case .fill: return .fill
        }
    }

    var layerGravity: AVLayerVideoGravity {
        switch self {
        case .fit: return .resizeAspect
        case .fill: return .resizeAspectFill
        }
    }
}

/// Shared state that drives the merged Live⇄Playback monitoring surface.
/// One timeline writes `targetDate`; every visible tile reads it, so scrubbing
/// moves all cameras together (synchronized multi-camera playback). Seeks are
/// driven only by `lastSeekDate` (set on explicit scrub/jump), while `targetDate`
/// is also advanced by the primary tile's playback observer for the playhead —
/// keeping the two decoupled avoids a seek-feedback loop during free playback.
@MainActor
final class MonitoringPlaybackController: ObservableObject {
    @Published var isLive = true
    @Published var targetDate = Date()
    @Published var lastSeekDate = Date()
    @Published var isScrubbing = false
    @Published var isPaused = false
    @Published var speed = "1x"

    /// The recording segment covering `date` for a camera, if any.
    func segment(for cameraID: UUID, at date: Date, using store: MediaIngestStore) -> RecordingSegment? {
        store.segments(for: cameraID).first {
            let s = min($0.createdAt, $0.modifiedAt)
            let e = max($0.createdAt, $0.modifiedAt)
            let end = e.timeIntervalSince(s) < 1 ? s.addingTimeInterval(60) : e
            return date >= s && date <= end
        }
    }

    func offset(in segment: RecordingSegment, at date: Date) -> TimeInterval {
        max(0, date.timeIntervalSince(min(segment.createdAt, segment.modifiedAt)))
    }

    func beginScrub(to date: Date) {
        isLive = false
        isScrubbing = true
        targetDate = date
        lastSeekDate = date
    }

    func scrub(to date: Date) {
        targetDate = date
        lastSeekDate = date
    }

    func endScrub(at date: Date) {
        targetDate = date
        lastSeekDate = date
        isScrubbing = false
    }

    /// Jump to a specific moment and play (used by event markers, deep links).
    func enterPlayback(at date: Date) {
        isLive = false
        isScrubbing = false
        isPaused = false
        targetDate = date
        lastSeekDate = date
    }

    func goLive() {
        isLive = true
        isScrubbing = false
        isPaused = false
        targetDate = Date()
        lastSeekDate = Date()
    }

    /// Jump the playhead by a relative amount (transport ±10s), preserving the
    /// current play/pause state.
    func nudge(_ seconds: TimeInterval) {
        isLive = false
        let next = targetDate.addingTimeInterval(seconds)
        targetDate = next
        lastSeekDate = next
    }

    /// Called ~4×/sec by the primary tile so the playhead tracks playback.
    func reportPlayback(date: Date) {
        guard isScrubbing == false, isLive == false else { return }
        targetDate = date
    }
}

/// A grid tile's recorded-playback surface. Reuses `RecordingPlaybackSurface`
/// (the same AVPlayer/AVPlayerLayer path as the standalone player) and reports
/// playback position back to the controller when it is the primary tile.
struct MonitoringPlaybackTileSurface: View {
    @EnvironmentObject private var playback: MonitoringPlaybackController
    let segment: RecordingSegment
    let isPrimary: Bool

    var body: some View {
        RecordingPlaybackSurface(
            segment: segment,
            speed: playback.speed,
            seekOffset: playback.offset(in: segment, at: playback.lastSeekDate),
            isPaused: playback.isPaused,
            seekNudge: nil,
            isScrubbing: playback.isScrubbing,
            onPlaybackOffset: isPrimary ? { seconds in
                let date = min(segment.createdAt, segment.modifiedAt).addingTimeInterval(seconds)
                playback.reportPlayback(date: date)
            } : nil
        )
    }
}

struct LiveMonitorView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @StateObject private var playback = MonitoringPlaybackController()
    @State private var gridColumns = 2
    @State private var selectedCameraID: UUID?
    @State private var fullscreenCameraID: UUID?
    @State private var tileContentMode: CameraVideoContentMode = .fit
    // Hidden by default to keep the live wall uncluttered; the toolbar's
    // sidebar.trailing button reveals it, and the choice persists.
    @AppStorage("handoffgrid.live.inspectorVisible") private var isInspectorVisible = false
    @State private var criticalAlertToShow: AlertEvent?

    /// Cameras shown in the grid: the applied saved layout's cameras (in the
    /// operator's camera order), or every camera when no layout is applied.
    private var gridCameras: [CameraFeed] {
        guard let layout = workflowStore.activeLayout else { return cameraStore.cameras }
        let filtered = cameraStore.cameras.filter { layout.cameraNames.contains($0.name) }
        return filtered.isEmpty ? cameraStore.cameras : filtered
    }

    private var selectedCamera: CameraFeed? {
        gridCameras.first { $0.id == selectedCameraID } ?? gridCameras.first
    }

    /// The camera the operator is actively watching — fullscreen wins, else the
    /// selected tile, else the first. Only this camera runs high-frame-rate
    /// person tracking (see AIDetectionStore.setTrackingCamera).
    private var focusedCameraID: UUID? {
        fullscreenCameraID ?? selectedCameraID ?? gridCameras.first?.id
    }

    private let gridSpacing: CGFloat = 12
    private let gridPadding: CGFloat = 14
    private let minimumTileWidth: CGFloat = 260

    var body: some View {
        GeometryReader { proxy in
            let showsInspector = isInspectorVisible && proxy.size.width >= 980

            HStack(spacing: 0) {
                VStack(spacing: 0) {
                    LiveGridToolbar(
                        gridColumns: $gridColumns,
                        tileContentMode: $tileContentMode,
                        isInspectorVisible: $isInspectorVisible,
                        cameraCount: gridCameras.count,
                        gridCameraNames: gridCameras.map(\.name)
                    )

                    GeometryReader { gridProxy in
                        if let fsID = fullscreenCameraID,
                           let fsCamera = cameraStore.cameras.first(where: { $0.id == fsID }) {
                            ZStack(alignment: .topTrailing) {
                                CameraTile(
                                    camera: fsCamera,
                                    isSelected: true,
                                    contentMode: tileContentMode,
                                    onSingleTap: { selectedCameraID = fsCamera.id },
                                    onDoubleTap: { fullscreenCameraID = nil }
                                )
                                Button {
                                    fullscreenCameraID = nil
                                } label: {
                                    Image(systemName: "arrow.down.right.and.arrow.up.left")
                                        .font(.system(size: 13, weight: .semibold))
                                        .foregroundStyle(.white)
                                        .padding(9)
                                        .background(.black.opacity(0.6), in: Circle())
                                }
                                .buttonStyle(.plain)
                                .padding(14)
                                .help("Exit fullscreen (or double-click tile)")
                            }
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                        } else {
                            let columnCount = resolvedColumnCount(for: gridProxy.size.width)
                            let rowCount = max(1, Int(ceil(Double(gridCameras.count) / Double(columnCount))))
                            // Cap each tile's height so every row fits the visible
                            // area — no scrolling, no camera clipped by the timeline.
                            // Tiles stay 16:9, so the frame letterboxes within the
                            // cell and is never cropped.
                            let availableHeight = gridProxy.size.height - gridPadding * 2 - gridSpacing * CGFloat(rowCount - 1)
                            let cellHeight = max(minimumTileWidth * 9 / 16, availableHeight / CGFloat(rowCount))
                            ScrollView {
                                LazyVGrid(
                                    columns: gridItems(for: gridProxy.size.width),
                                    spacing: gridSpacing
                                ) {
                                    ForEach(gridCameras) { camera in
                                        CameraTile(
                                            camera: camera,
                                            isSelected: camera.id == (selectedCameraID ?? gridCameras.first?.id),
                                            contentMode: tileContentMode,
                                            onSingleTap: { selectedCameraID = camera.id },
                                            onDoubleTap: { fullscreenCameraID = camera.id }
                                        )
                                        .frame(maxHeight: cellHeight)
                                        .contextMenu {
                                            Button {
                                                CameraPopOutWindows.shared.open(camera)
                                            } label: {
                                                Label("Pop Out Window", systemImage: "macwindow.on.rectangle")
                                            }
                                            Button {
                                                fullscreenCameraID = camera.id
                                            } label: {
                                                Label("Fill Grid", systemImage: "arrow.up.left.and.arrow.down.right")
                                            }
                                        }
                                        .draggable(camera.id.uuidString)
                                        .dropDestination(for: String.self) { items, _ in
                                            guard let sourceIDStr = items.first,
                                                  let sourceID = UUID(uuidString: sourceIDStr),
                                                  let srcIdx = cameraStore.cameras.firstIndex(where: { $0.id == sourceID }),
                                                  let dstIdx = cameraStore.cameras.firstIndex(where: { $0.id == camera.id }),
                                                  srcIdx != dstIdx else { return false }
                                            cameraStore.moveCamera(from: IndexSet([srcIdx]), to: dstIdx > srcIdx ? dstIdx + 1 : dstIdx)
                                            return true
                                        }
                                    }
                                }
                                .padding(gridPadding)
                            }
                        }
                    }

                    Divider()
                        .overlay(SentinelTheme.line)

                    TimelineStrip(
                        segments: selectedCamera.map { mediaIngestStore.segments(for: $0.id) } ?? [],
                        activeRecording: selectedCamera.flatMap { mediaIngestStore.recordingSession(for: $0.id) },
                        cameraName: selectedCamera?.name ?? "Selected camera",
                        motionEvents: selectedCamera.map { mediaIngestStore.motionEvents(for: $0.id) } ?? []
                    )
                    .frame(height: 166)
                }
                .frame(minWidth: 0, maxWidth: .infinity)
                .layoutPriority(1)

                if showsInspector {
                    Divider()
                        .overlay(SentinelTheme.line)

                    CameraInspector(camera: selectedCamera)
                        .frame(width: inspectorWidth(for: proxy.size.width))
                }
            }
            .background(SentinelTheme.background)
            .environmentObject(playback)
        }
        .onAppear {
            applyDefaultGridColumns()
            mediaIngestStore.refreshRecordingSegments()
            aiDetectionStore.setTrackingCamera(focusedCameraID)
        }
        .onChange(of: focusedCameraID) { newID in
            aiDetectionStore.setTrackingCamera(newID)
        }
        .onDisappear {
            aiDetectionStore.setTrackingCamera(nil)
        }
        .onChange(of: workflowStore.preferences.defaultGridColumns) { _ in
            applyDefaultGridColumns()
        }
        .onChange(of: caseworkStore.activeAlerts.filter({ $0.severity == .critical && $0.alertState == .new }).count) { newCount in
            if newCount > 0, let alert = caseworkStore.activeAlerts.first(where: { $0.severity == .critical && $0.alertState == .new }) {
                criticalAlertToShow = alert
            }
        }
        .alert("Critical Alert", isPresented: Binding(
            get: { criticalAlertToShow != nil },
            set: { if !$0 { criticalAlertToShow = nil } }
        )) {
            Button("Acknowledge") {
                if let alert = criticalAlertToShow {
                    caseworkStore.acknowledgeAlert(alert)
                }
                criticalAlertToShow = nil
            }
            Button("Dismiss", role: .cancel) {
                criticalAlertToShow = nil
            }
        } message: {
            if let alert = criticalAlertToShow {
                Text("\(alert.source): \(alert.detail)")
            }
        }
    }

    private func applyDefaultGridColumns() {
        let columns = workflowStore.activeLayout?.gridColumns ?? workflowStore.preferences.defaultGridColumns
        gridColumns = min(max(columns, 1), 4)
    }

    private func resolvedColumnCount(for availableWidth: CGFloat) -> Int {
        let innerWidth = max(availableWidth - (gridPadding * 2), minimumTileWidth)
        let possibleColumns = Int((innerWidth + gridSpacing) / (minimumTileWidth + gridSpacing))
        return min(max(gridColumns, 1), max(possibleColumns, 1))
    }

    private func gridItems(for availableWidth: CGFloat) -> [GridItem] {
        Array(
            repeating: GridItem(.flexible(minimum: minimumTileWidth), spacing: gridSpacing),
            count: resolvedColumnCount(for: availableWidth)
        )
    }

    private func inspectorWidth(for totalWidth: CGFloat) -> CGFloat {
        min(340, max(286, totalWidth * 0.24))
    }
}

struct LiveGridToolbar: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @Binding var gridColumns: Int
    @Binding var tileContentMode: CameraVideoContentMode
    @Binding var isInspectorVisible: Bool
    let cameraCount: Int
    var gridCameraNames: [String] = []
    @State private var isSavingLayout = false
    @State private var layoutNameDraft = ""
    @State private var tourModeActive = false
    @State private var tourViewIndex = 0

    var body: some View {
        HStack(spacing: 12) {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 12) {
                    Label("Live Monitor", systemImage: "dot.radiowaves.left.and.right")
                        .font(.headline)
                        .lineLimit(1)

                    Text("\(cameraCount) cameras")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }

                Label("Live", systemImage: "dot.radiowaves.left.and.right")
                    .font(.headline)
                    .lineLimit(1)
            }

            Spacer()

            HStack(spacing: 10) {
                Menu {
                    Button {
                        workflowStore.applyLayout(nil)
                    } label: {
                        if workflowStore.activeLayout == nil {
                            Label("All Cameras", systemImage: "checkmark")
                        } else {
                            Text("All Cameras")
                        }
                    }
                    if workflowStore.personalViews.isEmpty == false {
                        Divider()
                        ForEach(workflowStore.personalViews) { view in
                            Button {
                                workflowStore.applyLayout(view)
                                gridColumns = max(1, min(view.gridColumns, 4))
                            } label: {
                                if workflowStore.activeLayoutID == view.id {
                                    Label(view.name, systemImage: "checkmark")
                                } else {
                                    Text(view.name)
                                }
                            }
                        }
                    }
                    Divider()
                    Button {
                        layoutNameDraft = ""
                        isSavingLayout = true
                    } label: {
                        Label("Save Current Grid as Layout…", systemImage: "plus.rectangle.on.rectangle")
                    }
                    .disabled(cameraCount == 0)
                } label: {
                    Label(workflowStore.activeLayout?.name ?? "All Cameras", systemImage: "rectangle.grid.2x2")
                }
                .menuStyle(.button)
                .fixedSize()
                .help("Switch between saved camera layouts")
                .popover(isPresented: $isSavingLayout, arrowEdge: .bottom) {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("Save Layout").font(.headline)
                        Text("Saves the \(cameraCount) camera\(cameraCount == 1 ? "" : "s") in the grid at \(gridColumns)x\(gridColumns).")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        TextField("Layout name", text: $layoutNameDraft)
                            .textFieldStyle(.roundedBorder)
                            .frame(width: 240)
                            .onSubmit(saveCurrentLayout)
                        HStack {
                            Spacer()
                            Button("Cancel") { isSavingLayout = false }
                            Button("Save", action: saveCurrentLayout)
                                .buttonStyle(.borderedProminent)
                                .disabled(layoutNameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
                        }
                    }
                    .padding(14)
                }

                Picker("Grid", selection: $gridColumns) {
                    Text("1x1").tag(1)
                    Text("2x2").tag(2)
                    Text("3x3").tag(3)
                    Text("4x4").tag(4)
                }
                .pickerStyle(.segmented)
                .frame(width: 214)
                .help("The grid automatically uses fewer columns when the window is too narrow.")

                Picker("Tile Mode", selection: $tileContentMode) {
                    ForEach(CameraVideoContentMode.allCases) { mode in
                        Text(mode.rawValue).tag(mode)
                    }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .frame(width: 108)
                .help("Fit shows the whole camera frame. Fill crops to cover the tile.")

                Button {
                    isInspectorVisible.toggle()
                } label: {
                    Label(isInspectorVisible ? "Hide Inspector" : "Show Inspector", systemImage: "sidebar.trailing")
                }
                .labelStyle(.iconOnly)
                .help("Show or hide the selected camera inspector")

                Button {
                    mediaIngestStore.refreshRecordingSegments()
                } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .labelStyle(.iconOnly)
                .help("Refresh recording segments")

                if workflowStore.personalViews.isEmpty == false {
                    Button {
                        tourModeActive.toggle()
                    } label: {
                        Label(tourModeActive ? "Stop Tour" : "Tour Mode", systemImage: tourModeActive ? "stop.circle.fill" : "play.rectangle.fill")
                    }
                    .labelStyle(.iconOnly)
                    .foregroundStyle(tourModeActive ? SentinelTheme.amber : .primary)
                    .help(tourModeActive ? "Stop cycling through saved views" : "Auto-cycle through Personal Views (30s each)")
                    .task(id: tourModeActive) {
                        guard tourModeActive else { return }
                        while Task.isCancelled == false && tourModeActive {
                            let views = workflowStore.personalViews
                            guard views.isEmpty == false else { break }
                            tourViewIndex = (tourViewIndex + 1) % views.count
                            let cols = max(1, min(views[tourViewIndex].gridColumns, 4))
                            gridColumns = cols
                            workflowStore.applyLayout(views[tourViewIndex])
                            try? await Task.sleep(nanoseconds: 30_000_000_000)
                        }
                    }
                }

                Menu {
                    Button {
                        Task {
                            await startAllLive()
                        }
                    } label: {
                        Label("Start Live", systemImage: "play.fill")
                    }
                    .disabled(canStartLive == false)

                    Button {
                        Task {
                            await startAutoRecording()
                        }
                    } label: {
                        Label("Start Auto Recording", systemImage: "record.circle")
                    }
                    .disabled(canStartRecording == false)

                    Button {
                        stopAllRecordings()
                    } label: {
                        Label("Stop Recording", systemImage: "stop.circle.fill")
                    }
                    .disabled(mediaIngestStore.activeRecordings.isEmpty)

                    Button {
                        mediaIngestStore.stopAllLiveBridges()
                    } label: {
                        Label("Stop Live", systemImage: "stop.fill")
                    }
                    .disabled(mediaIngestStore.liveStreams.isEmpty)
                } label: {
                    Label("Actions", systemImage: "ellipsis.circle")
                }
                .menuStyle(.button)
            }
            .fixedSize(horizontal: true, vertical: false)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 7)
        .background(SentinelTheme.chrome)
        .overlay(alignment: .bottom) {
            Rectangle()
                .fill(Color.white.opacity(0.06))
                .frame(height: 1)
        }
    }

    private func saveCurrentLayout() {
        let name = layoutNameDraft.trimmingCharacters(in: .whitespaces)
        guard name.isEmpty == false, gridCameraNames.isEmpty == false else { return }
        workflowStore.createPersonalView(
            name: name,
            owner: "",
            site: "",
            cameraNames: gridCameraNames,
            gridColumns: gridColumns,
            quality: workflowStore.preferences.defaultStreamQuality
        )
        // createPersonalView inserts at index 0.
        workflowStore.applyLayout(workflowStore.personalViews.first)
        isSavingLayout = false
    }

    private var canStartLive: Bool {
        mediaEngineStore.snapshot.gstreamerLaunchPath != nil &&
        cameraStore.cameras.contains { camera in
            camera.isLocalCamera == false &&
            camera.rtspURL.isEmpty == false &&
            mediaIngestStore.liveStream(for: camera.id) == nil
        }
    }

    private var canStartRecording: Bool {
        mediaEngineStore.snapshot.gstreamerLaunchPath != nil &&
        cameraStore.cameras.contains { camera in
            camera.isLocalCamera == false &&
            camera.rtspURL.isEmpty == false &&
            camera.isRecording &&
            mediaIngestStore.recordingSession(for: camera.id) == nil
        }
    }

    private func startAllLive() async {
        for camera in cameraStore.cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
            if mediaIngestStore.liveStream(for: camera.id) == nil {
                _ = await mediaIngestStore.startLiveBridge(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore
                )
            }
        }
    }

    private func startAutoRecording() async {
        for camera in cameraStore.cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false && camera.isRecording {
            if mediaIngestStore.recordingSession(for: camera.id) == nil {
                _ = await mediaIngestStore.startRecording(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore
                )
            }
        }
    }

    private func stopAllRecordings() {
        for cameraID in Array(mediaIngestStore.activeRecordings.keys) {
            mediaIngestStore.stopRecording(for: cameraID)
        }
    }
}

struct CameraTile: View {
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    @EnvironmentObject private var playback: MonitoringPlaybackController
    let camera: CameraFeed
    let isSelected: Bool
    let contentMode: CameraVideoContentMode
    var onSingleTap: () -> Void = {}
    var onDoubleTap: () -> Void = {}
    @State private var launchResult: MediaPipelineLaunchResult?
    @State private var recPulse = false
    @State private var showPTZPanel = false

    var body: some View {
        ZStack {
            if playback.isLive {
                liveSurface
            } else {
                playbackSurface
            }
        }
        .aspectRatio(16 / 9, contentMode: .fit)
        .overlay(alignment: .topLeading) {
            LinearGradient(
                colors: [.black.opacity(0.72), .clear],
                startPoint: .top,
                endPoint: .center
            )
            .frame(height: 64)
            .overlay(alignment: .topLeading) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(camera.name)
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(.white)
                        .lineLimit(1)
                    Text(camera.location)
                        .font(.system(size: 9, weight: .regular))
                        .foregroundStyle(.white.opacity(0.65))
                        .lineLimit(1)
                }
                .padding(.horizontal, 10)
                .padding(.top, 8)
            }
        }
        .overlay(alignment: .topTrailing) {
            HStack(spacing: 6) {
                if isSelected {
                    Button {
                        saveSnapshot()
                    } label: {
                        Image(systemName: "camera.fill")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(.white)
                            .padding(7)
                            .background(.black.opacity(0.6), in: Circle())
                    }
                    .buttonStyle(.plain)
                    .help("Save snapshot to disk")

                    Button {
                        CameraPopOutWindows.shared.open(camera)
                    } label: {
                        Image(systemName: "macwindow.on.rectangle")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(.white)
                            .padding(7)
                            .background(.black.opacity(0.6), in: Circle())
                    }
                    .buttonStyle(.plain)
                    .help("Pop out into its own window")
                }
                if camera.supportsPTZ {
                    Button {
                        showPTZPanel.toggle()
                    } label: {
                        Image(systemName: "dpad")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(showPTZPanel ? SentinelTheme.accent : .white)
                            .padding(7)
                            .background(
                                showPTZPanel ? SentinelTheme.accent.opacity(0.25) : Color.black.opacity(0.6),
                                in: Circle()
                            )
                    }
                    .buttonStyle(.plain)
                    .help("PTZ controls")
                    .popover(isPresented: $showPTZPanel, arrowEdge: .top) {
                        PTZControlPanel(camera: camera)
                            .padding(10)
                    }
                }
                StatusBadge(status: mediaIngestStore.effectiveStatus(for: camera))
                    .padding(.vertical, 5).padding(.horizontal, 9)
                    .background(.black.opacity(0.22), in: Capsule())
            }
            .padding(10)
        }
        .overlay(alignment: .bottom) {
            CameraTileFooter(
                camera: camera,
                transportLabel: transportLabel,
                isRecording: mediaIngestStore.isRecording(cameraID: camera.id),
                events: mediaIngestStore.motionEvents(for: camera.id)
            )
        }
        .overlay {
            AIDetectionOverlay(
                // Focused tile: live per-frame person tracks when present, else
                // the recent event log (an empty live-track frame must not blank
                // the tile). Other tiles: the event-log overlay.
                detections: aiDetectionStore.overlayDetections(for: camera.id),
                contentMode: contentMode
            )
        }
        .overlay {
            if playback.isLive && shouldShowStartLiveButton {
                VStack(spacing: 8) {
                    Button {
                        Task {
                            launchResult = await mediaIngestStore.startLiveBridge(
                                for: camera,
                                mediaEngine: mediaEngineStore,
                                credentials: cameraCredentialStore
                            )
                        }
                    } label: {
                        Label("Start Live", systemImage: "play.fill")
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)

                    if let launchResult, launchResult.didLaunch == false {
                        Text(launchResult.title)
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.red)
                            .padding(.horizontal, 8)
                            .padding(.vertical, 4)
                            .background(.black.opacity(0.62), in: Capsule())
                    }
                }
            }
        }
        .overlay {
            if playback.isLive, let message = streamStatusMessage {
                let isReconnecting = message.localizedCaseInsensitiveContains("reconnect")
                VStack(spacing: 6) {
                    Image(systemName: isReconnecting ? "arrow.triangle.2.circlepath" : "exclamationmark.triangle.fill")
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(isReconnecting ? SentinelTheme.amber : .red)
                    Text(message)
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(isReconnecting ? SentinelTheme.amber : .red)
                        .multilineTextAlignment(.center)
                        .lineLimit(3)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(10)
                .background(.black.opacity(0.72), in: RoundedRectangle(cornerRadius: 6))
                .padding(12)
            }
        }
        .overlay {
            if hasActiveAlert && !isSelected {
                PulsingAlertRing(cornerRadius: SentinelMetrics.cardRadius, color: alertRingColor)
            } else {
                RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                    .stroke(
                        isSelected ? SentinelTheme.accent : Color.white.opacity(0.08),
                        lineWidth: isSelected ? 2 : 1
                    )
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        // REC indicator lives at top-LEFT so it doesn't collide with the
        // top-right cluster (snapshot button + Online status badge).
        .overlay(alignment: .topLeading) {
            if mediaIngestStore.isRecording(cameraID: camera.id) {
                HStack(spacing: 4) {
                    Circle()
                        .fill(Color.red)
                        .frame(width: 6, height: 6)
                        .scaleEffect(recPulse ? 1.3 : 0.85)
                        .opacity(recPulse ? 1.0 : 0.5)
                        .onAppear {
                            withAnimation(.easeInOut(duration: 0.8).repeatForever(autoreverses: true)) {
                                recPulse = true
                            }
                        }
                    Text("REC")
                        .font(.system(size: 8, weight: .bold))
                        .foregroundStyle(.white)
                }
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(.black.opacity(0.55), in: Capsule())
                // Slide REC down a bit so it sits BELOW the camera name row,
                // not on top of it.
                .padding(.top, 36)
                .padding(.leading, 10)
            }
        }
        .shadow(color: .black.opacity(isSelected ? 0.46 : 0.24), radius: isSelected ? 10 : 4, y: 4)
        // PTZ controls now live in a popover attached to the dpad button in
        // the top-right cluster. The always-visible overlay was crowding the
        // tile; popover gives operators full-size controls only when needed.
        .animation(.spring(response: 0.25, dampingFraction: 0.8), value: showPTZPanel)
        // simultaneousGesture (instead of .gesture) lets child controls like
        // the PTZ panel's Buttons receive their own clicks. With plain .gesture
        // the tile-level tap recognizer was eating events before they reached
        // the d-pad buttons.
        .simultaneousGesture(
            TapGesture(count: 2).onEnded { onDoubleTap() }
                .exclusively(before: TapGesture(count: 1).onEnded { onSingleTap() })
        )
    }

    // Live (HLS / GStreamer / local) rendering — unchanged from the original
    // tile; only reachable while `playback.isLive` so live streaming behaves
    // exactly as before.
    @ViewBuilder
    private var liveSurface: some View {
        if camera.isLocalCamera {
            LocalCameraPreviewSurface(camera: camera, contentMode: contentMode)
        } else if let liveStream = mediaIngestStore.liveStream(for: camera.id) {
            // GStreamer JPEG bridge (used for motion detection, fallback display)
            LocalLivePreviewSurface(stream: liveStream, contentMode: contentMode)
        } else if mediaMTXStore.isAvailable && mediaMTXStore.isRunning == false {
            // MediaMTX is installed but still starting — show a connecting state
            CameraConnectingSurface(camera: camera)
        } else if mediaMTXStore.isRunning && camera.rtspURL.isEmpty == false {
            // MediaMTX proxies the camera — use HLS which AVPlayer handles natively on macOS.
            // RTSP is not supported by AVPlayer without third-party plugins.
            AVPlayerRTSPSurface(
                rtspURL: mediaMTXStore.hlsURL(for: camera.id),
                gravity: contentMode.layerGravity
            )
        } else if camera.rtspURL.isEmpty == false {
            // Fallback: direct RTSP via AVPlayer (may fail on single-slot cameras)
            AVPlayerRTSPSurface(
                rtspURL: camera.rtspURLWithStoredCredentials,
                gravity: contentMode.layerGravity
            )
        } else {
            CameraSignalSurface(camera: camera)
        }
    }

    // Recorded-playback rendering — shown when the timeline is scrubbed off the
    // live edge. Resolves this camera's segment at the shared target time.
    @ViewBuilder
    private var playbackSurface: some View {
        if let segment = playback.segment(for: camera.id, at: playback.targetDate, using: mediaIngestStore) {
            MonitoringPlaybackTileSurface(segment: segment, isPrimary: isSelected)
        } else {
            ZStack {
                CameraSignalSurface(camera: camera)
                Text("No recording at \(playback.targetDate, format: .dateTime.hour().minute().second())")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.white.opacity(0.8))
                    .padding(.horizontal, 8).padding(.vertical, 4)
                    .background(.black.opacity(0.55), in: Capsule())
            }
        }
    }

    private var hasActiveAlert: Bool {
        caseworkStore.activeAlerts.contains { $0.cameraID == camera.id }
    }

    private var alertRingColor: Color {
        let worst = caseworkStore.activeAlerts
            .filter { $0.cameraID == camera.id }
            .map(\.severity)
            .min { $0.rank < $1.rank }
        return worst?.tint ?? .red
    }

    private var shouldShowStartLiveButton: Bool {
        camera.isLocalCamera == false &&
        camera.rtspURL.isEmpty == false &&
        mediaEngineStore.snapshot.gstreamerLaunchPath != nil &&
        mediaIngestStore.liveStream(for: camera.id) == nil &&
        mediaIngestStore.recordingSession(for: camera.id) == nil &&
        mediaMTXStore.isRunning == false &&
        mediaMTXStore.isAvailable == false
    }

    private var streamStatusMessage: String? {
        guard camera.isLocalCamera == false,
              mediaIngestStore.liveStream(for: camera.id) == nil,
              mediaIngestStore.recordingSession(for: camera.id) == nil,
              mediaMTXStore.isRunning == false,
              mediaMTXStore.isAvailable == false else { return nil }
        return mediaIngestStore.streamErrors[camera.id]
    }

    private var transportLabel: String {
        if mediaIngestStore.liveStream(for: camera.id) != nil {
            return "LOCAL PREVIEW"
        }

        if camera.isLocalCamera {
            return "LOCAL"
        }

        return camera.rtspURL.isEmpty == false ? "RTSP" : ""
    }

    private func saveSnapshot() {
        guard let image = latestJPEGFrame() else { return }
        let panel = NSSavePanel()
        let timestamp = DateFormatter.localizedString(from: Date(), dateStyle: .short, timeStyle: .medium)
            .replacingOccurrences(of: "/", with: "-")
            .replacingOccurrences(of: ":", with: "-")
            .replacingOccurrences(of: " ", with: "_")
        panel.nameFieldStringValue = "\(camera.name)_\(timestamp).jpg"
        panel.allowedContentTypes = [.jpeg]
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            if let tiff = image.tiffRepresentation,
               let rep = NSBitmapImageRep(data: tiff),
               let jpegData = rep.representation(using: .jpeg, properties: [.compressionFactor: 0.92]) {
                try? jpegData.write(to: url)
            }
        }
    }

    private func latestJPEGFrame() -> NSImage? {
        guard let stream = mediaIngestStore.liveStream(for: camera.id) else { return nil }
        guard let files = try? FileManager.default.contentsOfDirectory(
            at: stream.frameDirectoryURL,
            includingPropertiesForKeys: [.contentModificationDateKey, .fileSizeKey]
        ) else { return nil }
        let latest = files
            .filter { $0.pathExtension.lowercased() == "jpg" }
            .compactMap { url -> (URL, Date)? in
                let v = try? url.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
                guard let mod = v?.contentModificationDate, (v?.fileSize ?? 0) > 0 else { return nil }
                return (url, mod)
            }
            .max { $0.1 < $1.1 }?.0
        return latest.flatMap { NSImage(contentsOf: $0) }
    }
}

struct CameraTileFooter: View {
    let camera: CameraFeed
    let transportLabel: String
    let isRecording: Bool
    let events: [MotionEvent]

    private var isDualStreamLowResActive: Bool {
        camera.recordingMode == .dualStream &&
        camera.isRecording &&
        camera.subStreamRTSPURL.isEmpty == false
    }

    var body: some View {
        // Minimal footer: one row of essentials — resolution chip on the left,
        // optional state chip in the middle, transport chip on the right. No
        // mini-timeline (the main timeline at the bottom of the page already
        // shows the same data) and no FPS pill (the inspector panel has it).
        HStack(spacing: 6) {
            MetricPill(text: camera.resolution)

            if isRecording, isDualStreamLowResActive {
                Text("LOW")
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(.cyan)
                    .padding(.horizontal, 6).padding(.vertical, 3)
                    .background(.cyan.opacity(0.15), in: Capsule())
            } else if camera.isRecording, isRecording == false {
                MetricPill(text: "ARMED")
            }

            Spacer(minLength: 8)

            if transportLabel.isEmpty == false {
                MetricPill(text: transportLabel == "LOCAL PREVIEW" ? "PREVIEW" : transportLabel)
            }
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 8)
        .frame(maxWidth: .infinity, alignment: .bottom)
    }
}

struct PTZControlPanel: View {
    let camera: CameraFeed
    @State private var presets: [(token: String, name: String)] = []
    @State private var isLoadingPresets = false
    @State private var isSavingPreset = false
    @State private var newPresetName = ""
    @State private var showSaveField = false
    @State private var ptzError: String?

    var body: some View {
        // Compact 3x3 d-pad with zoom buttons on the side. No presets, no
        // save/reload chrome — operators who need presets can use the
        // dedicated camera detail view. The whole panel is ~90px square so it
        // never obscures more than a corner of the live tile.
        HStack(spacing: 4) {
            VStack(spacing: 3) {
                ptzButton("chevron.up", pan: 0, tilt: 0.6, zoom: 0)
                HStack(spacing: 3) {
                    ptzButton("chevron.left",  pan: -0.6, tilt: 0, zoom: 0)
                    stopButton
                    ptzButton("chevron.right", pan: 0.6,  tilt: 0, zoom: 0)
                }
                ptzButton("chevron.down", pan: 0, tilt: -0.6, zoom: 0)
            }
            VStack(spacing: 3) {
                ptzButton("plus",  pan: 0, tilt: 0, zoom: 0.6)
                ptzButton("minus", pan: 0, tilt: 0, zoom: -0.6)
            }
        }
        .padding(6)
        .background(.black.opacity(0.6), in: RoundedRectangle(cornerRadius: 8))
        .overlay(alignment: .top) {
            if let ptzError {
                Text(ptzError)
                    .font(.system(size: 8))
                    .foregroundStyle(.red)
                    .lineLimit(2)
                    .padding(4)
                    .background(.black.opacity(0.85), in: RoundedRectangle(cornerRadius: 4))
                    .offset(y: -22)
                    .onTapGesture { self.ptzError = nil }
            }
        }
    }

    // Single-tap PTZ pattern: send the move, wait ~500ms, send stop. This is
    // more reliable on macOS than `DragGesture(minimumDistance: 0)` (which
    // doesn't always emit `.onChanged` for click-without-drag) and matches the
    // step-style PTZ that operators expect in VMS UIs.
    private func ptzButton(_ symbol: String, pan: Double, tilt: Double, zoom: Double) -> some View {
        Button {
            FileHandle.standardError.write(Data("[PTZ] click pan=\(pan) tilt=\(tilt) zoom=\(zoom)\n".utf8))
            ptzError = "Sending move…"
            Task {
                do {
                    try await sendMove(pan: pan, tilt: tilt, zoom: zoom)
                    FileHandle.standardError.write(Data("[PTZ] move OK\n".utf8))
                    await MainActor.run { ptzError = nil }
                    try? await Task.sleep(nanoseconds: 500_000_000)
                    await sendStop()
                    FileHandle.standardError.write(Data("[PTZ] stop sent\n".utf8))
                } catch {
                    FileHandle.standardError.write(Data("[PTZ] FAILED: \(error)\n".utf8))
                    await MainActor.run { ptzError = error.localizedDescription }
                }
            }
        } label: {
            Image(systemName: symbol)
                .font(.system(size: 11, weight: .bold))
                .foregroundStyle(.white)
                .frame(width: 22, height: 22)
                .background(Color.white.opacity(0.12), in: RoundedRectangle(cornerRadius: 4))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private var stopButton: some View {
        Button {
            Task { await sendStop() }
        } label: {
            Image(systemName: "stop.fill")
                .font(.system(size: 10, weight: .bold))
                .foregroundStyle(.red.opacity(0.95))
                .frame(width: 22, height: 22)
                .background(Color.white.opacity(0.06), in: RoundedRectangle(cornerRadius: 4))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func sendMove(pan: Double, tilt: Double, zoom: Double) async throws {
        guard let urlStr = camera.onvifServiceURL,
              let serviceURL = URL(string: urlStr),
              let token = camera.onvifProfileToken else { return }
        try await ONVIFSOAPClient.ptzContinuousMove(
            deviceServiceURL: serviceURL, profileToken: token,
            pan: pan, tilt: tilt, zoom: zoom, credentials: ptzCredentials)
    }

    private func sendStop() async {
        guard let urlStr = camera.onvifServiceURL,
              let serviceURL = URL(string: urlStr),
              let token = camera.onvifProfileToken else { return }
        await ONVIFSOAPClient.ptzStop(deviceServiceURL: serviceURL,
                                      profileToken: token,
                                      credentials: ptzCredentials)
    }

    private var ptzCredentials: ONVIFCredentials {
        let sanitized = RTSPCredentialFormatter.sanitize(camera.rtspURL)
        let user = camera.username.isEmpty ? sanitized.username : camera.username
        let pass = sanitized.password.isEmpty ? ((try? CameraSecrets.password(for: camera.id)) ?? "") : sanitized.password
        return ONVIFCredentials(username: user, password: pass)
    }

    private func loadPresets() async {
        guard let urlStr = camera.onvifServiceURL,
              let serviceURL = URL(string: urlStr),
              let token = camera.onvifProfileToken else { return }
        isLoadingPresets = true
        presets = (try? await ONVIFSOAPClient.ptzGetPresets(
            deviceServiceURL: serviceURL, profileToken: token, credentials: ptzCredentials)) ?? []
        isLoadingPresets = false
    }

    private func savePreset() async {
        guard let urlStr = camera.onvifServiceURL,
              let serviceURL = URL(string: urlStr),
              let token = camera.onvifProfileToken else { return }
        let name = newPresetName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard name.isEmpty == false else { return }
        isSavingPreset = true
        _ = try? await ONVIFSOAPClient.ptzSetPreset(
            deviceServiceURL: serviceURL, profileToken: token,
            presetName: name, credentials: ptzCredentials)
        newPresetName = ""
        showSaveField = false
        isSavingPreset = false
        await loadPresets()
    }

    private func gotoPreset(_ presetToken: String) async {
        guard let urlStr = camera.onvifServiceURL,
              let serviceURL = URL(string: urlStr),
              let token = camera.onvifProfileToken else { return }
        try? await ONVIFSOAPClient.ptzGotoPreset(
            deviceServiceURL: serviceURL, profileToken: token,
            presetToken: presetToken, credentials: ptzCredentials)
    }
}

struct AIDetectionOverlay: View {
    let detections: [AIDetectionEvent]
    let contentMode: CameraVideoContentMode

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .topLeading) {
                ForEach(detections) { detection in
                    if let rect = overlayRect(for: detection, size: proxy.size) {
                        let c = detection.kind.overlayColor
                        let color = Color(red: c.0, green: c.1, blue: c.2)
                        RoundedRectangle(cornerRadius: 4)
                            .stroke(color.opacity(0.92), lineWidth: 2)
                            .frame(width: rect.width, height: rect.height)
                            .position(x: rect.midX, y: rect.midY)

                        Text(detection.overlayLabel)
                            .font(.caption2.weight(.bold))
                            .foregroundStyle(.white)
                            .lineLimit(1)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 3)
                            .background(color.opacity(0.86), in: RoundedRectangle(cornerRadius: 4))
                            .position(x: rect.midX, y: max(rect.minY - 11, 12))
                    }
                }
            }
        }
        .allowsHitTesting(false)
        .opacity(detections.isEmpty ? 0 : 1)
        // Boxes carry stable IDs across frames, so animating on the set lets each
        // box glide to its new position rather than popping — reads as tracking.
        .animation(.linear(duration: 0.18), value: detections)
    }

    private func overlayRect(for detection: AIDetectionEvent, size: CGSize) -> CGRect? {
        let imageRect = contentRect(for: detection.imageSize, in: size)
        let boundingBox = detection.boundingBox
        let width = max(18, boundingBox.width * imageRect.width)
        let height = max(18, boundingBox.height * imageRect.height)
        let x = imageRect.minX + boundingBox.minX * imageRect.width
        let y = imageRect.minY + (1 - boundingBox.maxY) * imageRect.height
        let rect = CGRect(x: x, y: y, width: width, height: height)
        let visibleRect = rect.intersection(CGRect(origin: .zero, size: size))

        guard visibleRect.isNull == false else {
            return nil
        }

        return visibleRect
    }

    private func contentRect(for imageSize: CGSize?, in containerSize: CGSize) -> CGRect {
        guard let imageSize,
              imageSize.width > 0,
              imageSize.height > 0,
              containerSize.width > 0,
              containerSize.height > 0 else {
            return CGRect(origin: .zero, size: containerSize)
        }

        let imageAspect = imageSize.width / imageSize.height
        let containerAspect = containerSize.width / containerSize.height
        let fittedSize: CGSize

        switch contentMode {
        case .fit:
            if imageAspect > containerAspect {
                fittedSize = CGSize(width: containerSize.width, height: containerSize.width / imageAspect)
            } else {
                fittedSize = CGSize(width: containerSize.height * imageAspect, height: containerSize.height)
            }
        case .fill:
            if imageAspect > containerAspect {
                fittedSize = CGSize(width: containerSize.height * imageAspect, height: containerSize.height)
            } else {
                fittedSize = CGSize(width: containerSize.width, height: containerSize.width / imageAspect)
            }
        }

        return CGRect(
            x: (containerSize.width - fittedSize.width) / 2,
            y: (containerSize.height - fittedSize.height) / 2,
            width: fittedSize.width,
            height: fittedSize.height
        )
    }
}

struct CameraTileTimeline: View {
    let events: [MotionEvent]
    let isRecording: Bool

    private var recentEvents: [MotionEvent] {
        let cutoff = Date().addingTimeInterval(-600)
        return events.filter { $0.timestamp >= cutoff }
    }

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(.black.opacity(0.68))

                // Recording presence — blue when recording, dim when idle
                Rectangle()
                    .fill(isRecording
                          ? Color(red: 0.15, green: 0.52, blue: 0.92).opacity(0.80)
                          : Color.white.opacity(0.12))
                    .frame(height: 3)
                    .offset(y: 6)

                // Event spikes — amber for motion, red for person
                ForEach(recentEvents) { event in
                    RoundedRectangle(cornerRadius: 1)
                        .fill(event.kind == .person
                              ? Color(red: 1.0, green: 0.28, blue: 0.22)
                              : Color(red: 0.98, green: 0.72, blue: 0.10))
                        .frame(width: event.kind == .person ? 3 : 2, height: markerHeight(for: event))
                        .offset(x: markerOffset(for: event, width: proxy.size.width))
                }

                // Live cursor — white, not red (red is reserved for person events)
                Rectangle()
                    .fill(Color.white.opacity(0.70))
                    .frame(width: 1.5)
                    .offset(x: max(proxy.size.width - 1.5, 0))
            }
        }
        .frame(height: 16)
        .help("Tile timeline: blue = archived recording, green = live recording, amber = motion, red = person, white = cursor.")
    }

    private func markerHeight(for event: MotionEvent) -> CGFloat {
        event.kind == .person ? 14 : CGFloat(6 + min(max(event.intensity * 40, 2), 8))
    }

    private func markerOffset(for event: MotionEvent, width: CGFloat) -> CGFloat {
        let start = Date().addingTimeInterval(-600)
        let progress = min(max(event.timestamp.timeIntervalSince(start) / 600, 0), 1)
        return CGFloat(progress) * max(width - 3, 0)
    }
}

// MARK: - AVPlayer RTSP surface (smooth live preview when GStreamer bridge is idle)

struct AVPlayerRTSPSurface: NSViewRepresentable {
    let rtspURL: String
    let gravity: AVLayerVideoGravity

    func makeNSView(context: Context) -> AVPlayerLayerView {
        let view = AVPlayerLayerView()
        view.setup(url: rtspURL, gravity: gravity)
        return view
    }

    func updateNSView(_ nsView: AVPlayerLayerView, context: Context) {
        nsView.playerLayer.videoGravity = gravity
    }

    static func dismantleNSView(_ nsView: AVPlayerLayerView, coordinator: ()) {
        nsView.stop()
    }
}

final class AVPlayerLayerView: NSView {
    let playerLayer = AVPlayerLayer()
    private var player: AVPlayer?
    private var sourceURL: URL?
    private var itemStatusObs: NSKeyValueObservation?
    private var failureObs: NSObjectProtocol?
    private var stallObs: NSObjectProtocol?
    private var watchdogTimer: Timer?
    private var lastProgressTime: CMTime = .zero
    private var lastProgressAt: Date = Date()
    private var reloadAttempts = 0
    private let maxReloadAttempts = 8

    // Return playerLayer as the backing layer so it is guaranteed to be in the
    // layer tree when the view enters a window. Using layer?.addSublayer() from
    // setup() is unreliable because the backing layer may not exist yet when
    // makeNSView calls setup() before the view is added to a window.
    override func makeBackingLayer() -> CALayer {
        return playerLayer
    }

    func setup(url: String, gravity: AVLayerVideoGravity) {
        wantsLayer = true
        playerLayer.videoGravity = gravity
        playerLayer.backgroundColor = NSColor.black.cgColor

        let lc = url.lowercased()
        guard (lc.hasPrefix("rtsp://") || lc.hasPrefix("http://") || lc.hasPrefix("https://")),
              let u = URL(string: url) else { return }
        sourceURL = u
        startPlayback()
        startWatchdog()
    }

    private func startPlayback() {
        guard let u = sourceURL else { return }
        clearObservers()
        let item = AVPlayerItem(url: u)
        // Start playback after a 1s prefill instead of AVPlayer's default ~3s,
        // and stay close to the live edge. Combined with MediaMTX's 3×2s segment
        // window this targets ~6-8s glass-to-glass latency.
        item.preferredForwardBufferDuration = 1.0
        if #available(macOS 13.0, *) {
            item.configuredTimeOffsetFromLive = CMTime(seconds: 1, preferredTimescale: 600)
        }
        let p = AVPlayer(playerItem: item)
        p.isMuted = true
        // Keep playing through transient stalls instead of buffering forever —
        // for live HLS we'd rather drop a few frames than freeze on a hiccup.
        p.automaticallyWaitsToMinimizeStalling = false
        player = p
        playerLayer.player = p
        lastProgressTime = .zero
        lastProgressAt = Date()

        itemStatusObs = item.observe(\.status, options: [.new]) { [weak self] item, _ in
            if item.status == .failed {
                DispatchQueue.main.async { self?.scheduleReload() }
            }
        }
        failureObs = NotificationCenter.default.addObserver(
            forName: .AVPlayerItemFailedToPlayToEndTime,
            object: item, queue: .main
        ) { [weak self] _ in self?.scheduleReload() }
        stallObs = NotificationCenter.default.addObserver(
            forName: .AVPlayerItemPlaybackStalled,
            object: item, queue: .main
        ) { [weak self] _ in self?.scheduleReload() }

        p.play()
    }

    // Periodically check that playback is actually advancing. AVPlayer can
    // silently freeze on a malformed HLS playlist (segment-duration jumps,
    // discontinuities) without raising `failedToPlayToEndTime`. If currentTime
    // hasn't moved in 6s, treat it as a stall and reload.
    private func startWatchdog() {
        watchdogTimer?.invalidate()
        watchdogTimer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            guard let self, let player = self.player else { return }
            let current = player.currentTime()
            if CMTimeCompare(current, self.lastProgressTime) != 0 {
                self.lastProgressTime = current
                self.lastProgressAt = Date()
                self.reloadAttempts = 0
                return
            }
            // Time hasn't advanced. If 6 seconds elapsed and we're supposed to
            // be playing, force a reload.
            if Date().timeIntervalSince(self.lastProgressAt) > 6,
               player.timeControlStatus != .paused {
                self.scheduleReload()
            }
        }
    }

    private func scheduleReload() {
        guard sourceURL != nil else { return }
        guard reloadAttempts < maxReloadAttempts else { return }
        reloadAttempts += 1
        // Small exponential backoff capped at 5s to avoid hammering MediaMTX
        // during an extended camera outage.
        let delay = min(5.0, 0.5 * pow(1.6, Double(reloadAttempts - 1)))
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in
            self?.startPlayback()
        }
    }

    private func clearObservers() {
        itemStatusObs?.invalidate()
        itemStatusObs = nil
        if let failureObs { NotificationCenter.default.removeObserver(failureObs) }
        if let stallObs { NotificationCenter.default.removeObserver(stallObs) }
        failureObs = nil
        stallObs = nil
    }

    func stop() {
        watchdogTimer?.invalidate()
        watchdogTimer = nil
        clearObservers()
        player?.pause()
        playerLayer.player = nil
        player = nil
        sourceURL = nil
    }

    deinit {
        watchdogTimer?.invalidate()
        if let failureObs { NotificationCenter.default.removeObserver(failureObs) }
        if let stallObs { NotificationCenter.default.removeObserver(stallObs) }
    }

    override func layout() {
        super.layout()
        playerLayer.frame = bounds
    }
}

struct LocalCameraPreviewSurface: NSViewRepresentable {
    let camera: CameraFeed
    let contentMode: CameraVideoContentMode

    func makeCoordinator() -> Coordinator {
        Coordinator()
    }

    func makeNSView(context: Context) -> LocalCameraPreviewView {
        let view = LocalCameraPreviewView()
        view.previewLayer.videoGravity = contentMode.layerGravity
        view.wantsLayer = true
        view.layer = view.previewLayer
        context.coordinator.configure(camera: camera, previewLayer: view.previewLayer)
        return view
    }

    func updateNSView(_ nsView: LocalCameraPreviewView, context: Context) {
        nsView.previewLayer.videoGravity = contentMode.layerGravity
        context.coordinator.configure(camera: camera, previewLayer: nsView.previewLayer)
    }

    static func dismantleNSView(_ nsView: LocalCameraPreviewView, coordinator: Coordinator) {
        coordinator.stop()
    }

    final class Coordinator {
        private let session = AVCaptureSession()
        private let sessionQueue = DispatchQueue(label: "HandoffGridSentinel.LocalCameraSession")
        private var configuredDeviceID: String?

        func configure(camera: CameraFeed, previewLayer: AVCaptureVideoPreviewLayer) {
            guard configuredDeviceID != camera.localDeviceID else {
                return
            }

            configuredDeviceID = camera.localDeviceID
            previewLayer.session = session

            AVCaptureDevice.requestAccess(for: .video) { [weak self] granted in
                guard granted else {
                    return
                }

                self?.sessionQueue.async {
                    self?.start(camera: camera)
                }
            }
        }

        func stop() {
            sessionQueue.async { [session] in
                if session.isRunning {
                    session.stopRunning()
                }
            }
        }

        private func start(camera: CameraFeed) {
            session.beginConfiguration()
            defer { session.commitConfiguration() }

            session.inputs.forEach { session.removeInput($0) }
            session.sessionPreset = .high

            guard let deviceID = camera.localDeviceID,
                  let device = LocalCameraCatalog.availableDevices().first(where: { $0.uniqueID == deviceID }) else {
                return
            }

            do {
                let input = try AVCaptureDeviceInput(device: device)
                guard session.canAddInput(input) else {
                    return
                }

                session.addInput(input)
                if session.isRunning == false {
                    session.startRunning()
                }
            } catch {
                return
            }
        }
    }
}

final class LocalCameraPreviewView: NSView {
    let previewLayer = AVCaptureVideoPreviewLayer()

    override func layout() {
        super.layout()
        previewLayer.frame = bounds
    }
}

struct LocalLivePreviewSurface: View {
    let stream: LocalLiveStream
    let contentMode: CameraVideoContentMode
    @State private var frameImage: NSImage?
    @State private var isWaitingForPlaylist = true
    @State private var startupError: String?

    var body: some View {
        ZStack {
            if let frameImage {
                Image(nsImage: frameImage)
                    .resizable()
                    .aspectRatio(contentMode: contentMode.swiftUIContentMode)
            } else {
                CameraBridgeStartupSurface(
                    stream: stream,
                    isWaitingForPlaylist: isWaitingForPlaylist,
                    errorMessage: startupError
                )
            }
        }
        .background(.black)
        .task(id: stream.frameDirectoryURL) {
            await waitForFrames()
        }
    }

    private func waitForFrames() async {
        isWaitingForPlaylist = true
        startupError = nil
        frameImage = nil
        let deadline = Date().addingTimeInterval(20)

        while Task.isCancelled == false {
            if let latestFrameURL = latestFrameURL(),
               let image = NSImage(contentsOf: latestFrameURL) {
                frameImage = image
                isWaitingForPlaylist = false
            }

            if frameImage == nil && Date() > deadline {
                startupError = latestLogSummary()
                isWaitingForPlaylist = false
                return
            }

            try? await Task.sleep(nanoseconds: 333_000_000)
        }
    }

    private func latestFrameURL() -> URL? {
        guard let files = try? FileManager.default.contentsOfDirectory(
            at: stream.frameDirectoryURL,
            includingPropertiesForKeys: [.contentModificationDateKey, .fileSizeKey]
        ) else {
            return nil
        }

        return files
            .filter { $0.lastPathComponent.lowercased().contains(".jpg") }
            .compactMap { url -> (URL, Date)? in
                let values = try? url.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
                guard let modified = values?.contentModificationDate,
                      (values?.fileSize ?? 0) > 0 else {
                    return nil
                }
                return (url, modified)
            }
            .max { $0.1 < $1.1 }?
            .0
    }

    private func latestLogSummary() -> String {
        guard let log = try? String(contentsOf: stream.logURL),
              log.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false else {
            return "No preview frames were produced. Check the RTSP path, credentials, and camera codec."
        }

        return log
            .split(separator: "\n")
            .suffix(3)
            .joined(separator: "\n")
    }
}

struct CameraBridgeStartupSurface: View {
    let stream: LocalLiveStream
    let isWaitingForPlaylist: Bool
    let errorMessage: String?

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [
                    Color(red: 0.03, green: 0.08, blue: 0.10),
                    Color(red: 0.01, green: 0.02, blue: 0.025)
                ],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )

            VStack(spacing: 10) {
                ProgressView()
                    .controlSize(.small)
                    .opacity(errorMessage == nil ? 1 : 0)

                Text(errorMessage == nil ? (isWaitingForPlaylist ? "STARTING PREVIEW" : "WAITING FOR FRAMES") : "PREVIEW START FAILED")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(errorMessage == nil ? .white.opacity(0.62) : .red)
                    .tracking(1.2)

                Text(errorMessage ?? stream.cameraName)
                    .font(.caption)
                    .foregroundStyle(.white.opacity(0.42))
                    .multilineTextAlignment(.center)
                    .lineLimit(4)
                    .padding(.horizontal, 18)
            }
        }
    }
}

struct CameraConnectingSurface: View {
    let camera: CameraFeed
    @State private var dots = 0

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [
                    Color(red: 0.03, green: 0.08, blue: 0.12),
                    Color(red: 0.01, green: 0.03, blue: 0.05)
                ],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )

            VStack(spacing: 10) {
                ProgressView()
                    .controlSize(.small)
                    .tint(SentinelTheme.accent)

                Text("CONNECTING")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(SentinelTheme.accent.opacity(0.9))
                    .tracking(1.4)

                Text("Starting MediaMTX proxy\(String(repeating: ".", count: dots))")
                    .font(.caption2)
                    .foregroundStyle(.white.opacity(0.38))
                    .frame(width: 180)
                    .animation(nil, value: dots)
            }
        }
        .task {
            while !Task.isCancelled {
                do {
                    try await Task.sleep(nanoseconds: 500_000_000)
                } catch {
                    return
                }
                dots = (dots + 1) % 4
            }
        }
    }
}

struct CameraSignalSurface: View {
    let camera: CameraFeed

    private var centerText: String {
        if camera.rtspURL.isEmpty == false && camera.status == .offline {
            return "RTSP CONFIGURED"
        }

        if camera.status == .offline {
            return "NO SIGNAL"
        }

        return "NO STREAM CONFIGURED"
    }

    private var gradient: LinearGradient {
        if camera.status == .offline {
            return LinearGradient(
                colors: [
                    Color(red: 0.04, green: 0.07, blue: 0.12),
                    Color(red: 0.02, green: 0.04, blue: 0.06)
                ],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
        }

        return LinearGradient(
            colors: [
                Color(red: 0.05, green: 0.09, blue: 0.14),
                Color(red: 0.03, green: 0.06, blue: 0.10)
            ],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }

    var body: some View {
        ZStack {
            gradient

            VStack(spacing: 12) {
                Image(systemName: camera.status == .offline ? "video.slash.fill" : "video.fill")
                    .font(.system(size: 36, weight: .light))
                    .foregroundStyle(.white.opacity(camera.status == .offline ? 0.38 : 0.30))

                Text(centerText)
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.white.opacity(0.48))
                    .tracking(1.4)
            }

            VStack {
                Spacer()

                Canvas { context, size in
                    let barWidth: CGFloat = 4
                    let spacing: CGFloat = 3
                    let opacity = camera.status == .offline ? 0.08 : 0.16
                    let color = Color.white.opacity(opacity)
                    for index in 0..<42 {
                        let h = CGFloat(8 + ((index * 7) % 22))
                        let x = CGFloat(index) * (barWidth + spacing)
                        let y = size.height - h
                        let rect = CGRect(x: x, y: y, width: barWidth, height: h)
                        context.fill(Path(roundedRect: rect, cornerRadius: 2), with: .color(color))
                    }
                }
                .frame(width: CGFloat(42 * 7), height: 30)
                .padding(.bottom, 34)
            }
        }
    }
}

struct CameraInspector: View {
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    let camera: CameraFeed?
    @State private var probeResult = StreamProbeResult.idle
    @State private var isTestingStream = false
    @State private var quickActionMessage: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text("Inspector")
                    .font(.headline)

                if let camera {
                    SentinelPanel("Selected Camera", systemImage: "camera.fill") {
                        let health = mediaIngestStore.healthSnapshot(for: camera)

                        VStack(alignment: .leading, spacing: 12) {
                            VStack(alignment: .leading, spacing: 3) {
                                Text(camera.name)
                                    .font(.headline)
                                    .lineLimit(1)

                                Text(camera.location)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                            }

                            Divider()
                                .overlay(SentinelTheme.line)

                            DetailRow(label: "Status", value: mediaIngestStore.effectiveStatus(for: camera).label)
                            DetailRow(label: "Resolution", value: camera.resolution)
                            DetailRow(label: "Frame Rate", value: "\(camera.fps) FPS")
                            DetailRow(label: "Bitrate", value: camera.bitrate)
                            DetailRow(label: "IP Address", value: camera.ipAddress)
                            DetailRow(label: "Profile", value: camera.profile)
                            DetailRow(label: "Source", value: camera.isLocalCamera ? "Local Camera" : "RTSP")
                            DetailRow(label: "RTSP", value: camera.rtspURL.isEmpty ? "Not configured" : "Configured")
                            DetailRow(label: "Credentials", value: credentialStatus(for: camera))
                            DetailRow(label: "Recording", value: health.isRecording ? "Active" : (camera.isRecording ? "Armed" : "Manual"))
                            DetailRow(label: "Recording Mode", value: camera.recordingMode.label)
                        DetailRow(label: "Recording Codec", value: camera.recordingCodec.label)
                        DetailRow(label: "AI Detection Events", value: "\(aiDetectionStore.detections(for: camera.id).count)")
                        DetailRow(label: "Last Frame", value: health.frameAgeLabel)
                            DetailRow(label: "Received FPS", value: health.fpsLabel)

                            if let lastError = health.lastError {
                                Text(lastError)
                                    .font(.caption2)
                                    .foregroundStyle(.orange)
                                    .lineLimit(3)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                    }

                    StreamReadinessPanel(
                        camera: camera,
                        probeResult: $probeResult,
                        isTestingStream: $isTestingStream
                    )

                    SentinelPanel("Quick Actions", systemImage: "bolt.fill") {
                        VStack(spacing: 8) {
                            Button {
                                commandCenter.requestOpen(.live)
                                quickActionMessage = "Opened Playback for review."
                            } label: {
                                Label("Open Playback", systemImage: "play.rectangle.fill")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }

                            Button {
                                caseworkStore.createIncidentFromNewestAlert()
                                quickActionMessage = "Incident created from the newest active alert."
                            } label: {
                                Label("Create Incident", systemImage: "exclamationmark.bubble.fill")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .disabled(caseworkStore.activeAlerts.isEmpty)
                            .help(caseworkStore.activeAlerts.isEmpty ? "No active alert is available for incident creation" : "Create an incident from the newest active alert")

                            Button {
                                let segment = mediaIngestStore.segments(for: camera.id).last
                                caseworkStore.createEvidencePackage(from: segment, cameraName: camera.name)
                                quickActionMessage = "Evidence package started for \(camera.name)."
                            } label: {
                                Label("Package Latest Recording", systemImage: "square.and.arrow.up")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .disabled(mediaIngestStore.segments(for: camera.id).isEmpty)

                            Button {
                                commandCenter.requestOpen(.cameras)
                                quickActionMessage = "Opened Camera Settings."
                            } label: {
                                Label("Camera Settings", systemImage: "gearshape.fill")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }

                            if let quickActionMessage {
                                Text(quickActionMessage)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(10)
                                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                            }
                        }
                        .buttonStyle(.bordered)
                    }

                    SentinelPanel("Recent Events", systemImage: "clock.fill") {
                        VStack(spacing: 8) {
                            if caseworkStore.activeAlerts.isEmpty {
                                EmptyStateLine(text: "No camera events.")
                            } else {
                                ForEach(caseworkStore.activeAlerts.prefix(3)) { alert in
                                    HStack(spacing: 8) {
                                        Circle()
                                            .fill(alert.severity.tint)
                                            .frame(width: 7, height: 7)

                                        VStack(alignment: .leading, spacing: 2) {
                                            Text(alert.title)
                                                .font(.caption.weight(.semibold))
                                                .lineLimit(1)

                                            Text("\(alert.time) · \(alert.source)")
                                                .font(.caption2)
                                                .foregroundStyle(.secondary)
                                                .lineLimit(1)
                                        }

                                        Spacer()
                                    }
                                }
                            }
                        }
                    }
                } else {
                    EmptyStateLine(text: "Select a camera to inspect status and actions.")
                }
            }
            .padding(14)
        }
        .background(SentinelTheme.chrome)
        .onChange(of: camera?.id) { _ in
            probeResult = .idle
            isTestingStream = false
            quickActionMessage = nil
        }
    }

    private func credentialStatus(for camera: CameraFeed) -> String {
        if camera.isLocalCamera {
            return "Not required"
        }

        if camera.hasEmbeddedRTSPCredentials {
            return "Unattended recording"
        }

        return camera.username.isEmpty ? "None configured" : "Username configured"
    }
}

struct StreamReadinessPanel: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    let camera: CameraFeed
    @Binding var probeResult: StreamProbeResult
    @Binding var isTestingStream: Bool
    @State private var launchResult: MediaPipelineLaunchResult?
    @State private var probeRequestID = UUID()
    @State private var unattendedPassword = ""

    var body: some View {
        SentinelPanel("Stream Readiness", systemImage: "antenna.radiowaves.left.and.right") {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    Circle()
                        .fill(probeResult.state.tint)
                        .frame(width: 8, height: 8)

                    Text(isTestingStream ? StreamProbeState.checking.title : probeResult.title)
                        .font(.caption.weight(.semibold))

                    Spacer()
                }

                Text(isTestingStream ? "Checking RTSP host and port..." : probeResult.detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                if camera.isLocalCamera {
                    Text("This camera uses AVFoundation directly and does not require RTSP or GStreamer.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }

                if camera.rtspURL.isEmpty == false {
                    Text(RTSPCredentialFormatter.redacted(camera.rtspURL))
                        .font(.caption2.monospaced())
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .truncationMode(.middle)
                }

                if camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
                    VStack(alignment: .leading, spacing: 8) {
                        Label(
                            camera.hasEmbeddedRTSPCredentials ? "Unattended credential saved" : "Unattended credential needed",
                            systemImage: camera.hasEmbeddedRTSPCredentials ? "key.fill" : "key"
                        )
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(camera.hasEmbeddedRTSPCredentials ? Color.green : SentinelTheme.amber)

                        SecureField("Camera password", text: $unattendedPassword)
                            .textFieldStyle(.roundedBorder)

                        Button {
                            saveUnattendedCredential()
                        } label: {
                            Label("Save for Unattended Recording", systemImage: "key.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(unattendedPassword.isEmpty || camera.username.isEmpty)
                    }
                    .padding(10)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                }

                HStack {
                    Text("Media Engine")
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    Spacer()

                    Text(mediaEngineStore.snapshot.readiness.rawValue)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(mediaEngineStore.snapshot.readiness.tint)
                }

                HStack {
                    Text("VLC")
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    Spacer()

                    Text(mediaEngineStore.snapshot.vlcAppPath == nil ? "Missing" : "Available")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(mediaEngineStore.snapshot.vlcAppPath == nil ? Color.secondary : Color.green)
                }

                HStack {
                    Text("Local Segments")
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    Spacer()

                    Text("\(mediaIngestStore.segments(for: camera.id).count)")
                        .font(.caption.weight(.semibold))
                }

                // ── Recording control ────────────────────────────────────────
                // Single toggle = the only control needed.
                // MediaMTX reloads immediately on change; no separate Start/Stop buttons.
                VStack(alignment: .leading, spacing: 10) {
                    Toggle(
                        isOn: Binding(
                            get: { camera.isRecording },
                            set: { newValue in
                                cameraStore.setRecordingEnabled(newValue, for: camera.id)
                                if mediaMTXStore.isRunning {
                                    // Tell MediaMTX to start/stop recording for this path
                                    mediaMTXStore.reload(
                                        cameras: cameraStore.cameras,
                                        recordingRootURL: mediaIngestStore.recordingRootURL,
                                        credentials: cameraCredentialStore
                                    )
                                    if newValue {
                                        mediaIngestStore.syncMediaMTXRecordingSessions(
                                            cameras: cameraStore.cameras,
                                            mediaMTXStore: mediaMTXStore
                                        )
                                    } else {
                                        mediaIngestStore.stopRecording(for: camera.id)
                                    }
                                } else if newValue {
                                    Task {
                                        launchResult = await mediaIngestStore.startRecording(
                                            for: camera,
                                            mediaEngine: mediaEngineStore,
                                            credentials: cameraCredentialStore
                                        )
                                    }
                                } else {
                                    mediaIngestStore.stopRecording(for: camera.id)
                                }
                            }
                        )
                    ) {
                        HStack(spacing: 6) {
                            Image(systemName: camera.isRecording ? "record.circle.fill" : "record.circle")
                                .foregroundStyle(camera.isRecording ? Color(red: 0.12, green: 0.82, blue: 0.45) : .secondary)
                            Text("Recording")
                                .font(.caption.weight(.semibold))
                        }
                    }
                    .toggleStyle(.switch)
                    .disabled(camera.isLocalCamera || camera.rtspURL.isEmpty)

                    // Recording status badge
                    if mediaIngestStore.recordingSession(for: camera.id) != nil {
                        Label(mediaMTXStore.isRunning ? "MediaMTX recording" : "GStreamer recording",
                              systemImage: "circle.fill")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(Color(red: 0.12, green: 0.82, blue: 0.45))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    Picker(
                        "Mode",
                        selection: Binding(
                            get: { camera.recordingMode },
                            set: { cameraStore.setRecordingMode($0, for: camera.id) }
                        )
                    ) {
                        ForEach(CameraRecordingMode.allCases) { mode in
                            Text(mode.label).tag(mode)
                        }
                    }
                    .pickerStyle(.segmented)
                    .disabled(camera.isLocalCamera)
                    .help("Continuous keeps every segment. Motion keeps only clips with detected activity.")

                    Picker(
                        "Codec",
                        selection: Binding(
                            get: { camera.recordingCodec },
                            set: { cameraStore.setRecordingCodec($0, for: camera.id) }
                        )
                    ) {
                        ForEach(CameraRecordingCodec.allCases) { codec in
                            Text(codec.shortLabel).tag(codec)
                        }
                    }
                    .pickerStyle(.segmented)
                    .disabled(camera.isLocalCamera)
                    .help("H.265/HEVC uses less storage. Matches what your camera streams.")
                }
                .padding(10)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))

                Menu {
                    Section("Diagnostics") {
                        Button {
                            testStream()
                        } label: {
                            Label("Test RTSP Connection", systemImage: "network")
                        }
                        .disabled(isTestingStream || camera.isLocalCamera)

                        Button {
                            reconnect()
                        } label: {
                            Label("Reconnect Stream", systemImage: "arrow.triangle.2.circlepath")
                        }
                        .disabled(camera.isLocalCamera || camera.rtspURL.isEmpty || mediaEngineStore.snapshot.gstreamerLaunchPath == nil)
                    }

                    Section("External Previews") {
                        Button {
                            launchResult = mediaEngineStore.launchExternalPreview(
                                for: camera, credentials: cameraCredentialStore)
                        } label: {
                            Label("Open GStreamer Window", systemImage: "play.rectangle.fill")
                        }
                        .disabled(camera.isLocalCamera || camera.rtspURL.isEmpty || mediaEngineStore.snapshot.gstreamerLaunchPath == nil)

                        Button {
                            launchResult = mediaEngineStore.launchVLCPreview(
                                for: camera, credentials: cameraCredentialStore)
                        } label: {
                            Label("Open in VLC", systemImage: "play.display")
                        }
                        .disabled(camera.isLocalCamera || camera.rtspURL.isEmpty || mediaEngineStore.snapshot.vlcAppPath == nil)

                        if mediaEngineStore.activePreviewProcessIDs.isEmpty == false {
                            Button(role: .destructive) {
                                mediaEngineStore.stopExternalPreviews()
                            } label: {
                                Label("Stop External Previews", systemImage: "stop.fill")
                            }
                        }
                    }

                    Section("Motion Detection Bridge") {
                        if mediaIngestStore.liveStream(for: camera.id) == nil {
                            Button {
                                Task {
                                    launchResult = await mediaIngestStore.startLiveBridge(
                                        for: camera, mediaEngine: mediaEngineStore,
                                        credentials: cameraCredentialStore,
                                        mediaMTXStore: mediaMTXStore)
                                }
                            } label: {
                                Label("Start Detection Bridge", systemImage: "waveform.and.magnifyingglass")
                            }
                            .disabled(camera.isLocalCamera || camera.rtspURL.isEmpty || mediaEngineStore.snapshot.gstreamerLaunchPath == nil)
                        } else {
                            Button(role: .destructive) {
                                mediaIngestStore.stopLiveBridge(for: camera.id)
                            } label: {
                                Label("Stop Detection Bridge", systemImage: "stop.circle")
                            }
                        }
                    }
                } label: {
                    Label("More Actions", systemImage: "ellipsis.circle")
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .menuStyle(.button)
                .buttonStyle(.bordered)

                if let launchResult {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(launchResult.title)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(launchResult.didLaunch ? .green : .red)

                        Text(launchResult.detail)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    .padding(10)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                }
            }
        }
        .onChange(of: camera.id) { _ in
            probeRequestID = UUID()
        }
    }

    private func testStream() {
        let requestID = UUID()
        probeRequestID = requestID
        isTestingStream = true
        probeResult = StreamProbeResult(
            state: .checking,
            title: "Checking",
            detail: "Opening TCP connection to the RTSP endpoint."
        )

        Task {
            let result = await RTSPStreamProbe.check(camera: camera)
            guard requestID == probeRequestID else {
                return
            }

            probeResult = result
            isTestingStream = false
        }
    }

    private func reconnect() {
        Task {
            let shouldRecord = camera.isRecording || mediaIngestStore.recordingSession(for: camera.id) != nil
            mediaIngestStore.stopLiveBridge(for: camera.id)
            mediaIngestStore.stopRecording(for: camera.id)
            try? await Task.sleep(nanoseconds: 800_000_000)

            if shouldRecord {
                launchResult = await mediaIngestStore.startRecording(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore
                )
            } else {
                launchResult = await mediaIngestStore.startLiveBridge(
                    for: camera,
                    mediaEngine: mediaEngineStore,
                    credentials: cameraCredentialStore
                )
            }
        }
    }

    private func saveUnattendedCredential() {
        guard cameraStore.saveUnattendedPassword(unattendedPassword, for: camera.id) else {
            launchResult = MediaPipelineLaunchResult.failure(
                "Credential Not Saved",
                detail: cameraStore.lastError ?? "The unattended credential could not be saved."
            )
            return
        }

        unattendedPassword = ""
        launchResult = MediaPipelineLaunchResult(
            didLaunch: true,
            title: "Unattended Recording Ready",
            detail: "\(camera.name) can record without a macOS Keychain prompt.",
            commandPreview: ""
        )

        guard let updatedCamera = cameraStore.cameras.first(where: { $0.id == camera.id }),
              updatedCamera.isRecording,
              mediaIngestStore.recordingSession(for: updatedCamera.id) == nil else {
            return
        }

        Task {
            launchResult = await mediaIngestStore.startRecording(
                for: updatedCamera,
                mediaEngine: mediaEngineStore,
                credentials: cameraCredentialStore
            )
        }
    }
}

// MARK: - Professional VMS Timeline

struct TimelineStrip: View {
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var playback: MonitoringPlaybackController
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var evidenceExporter: EvidenceExporter
    let segments: [RecordingSegment]
    let activeRecording: RecordingSessionStatus?
    let cameraName: String
    let motionEvents: [MotionEvent]
    @State private var windowSeconds = 3_600.0
    @State private var showExport = false
    // The visible window's right edge, anchored once the operator leaves the live
    // edge. Holding this steady (instead of re-deriving it from `targetDate` every
    // frame) is what makes the cursor track the mouse 1:1 while scrubbing — without
    // it the window re-centers on the playhead mid-drag and the pointer/video drift
    // apart. `nil` means "follow live / auto-center" (see `windowEnd`).
    @State private var anchorEnd: Date?
    @Environment(\.colorScheme) private var scheme

    // Foreground "ink" for grid/text/axis/cursor: white on the dark theme, black
    // on light. Lets the whole strip adapt instead of being hardcoded white-on-
    // near-black (which was invisible / looked broken in Light mode).
    private var ink: Color { scheme == .dark ? .white : .black }

    init(
        segments: [RecordingSegment] = [],
        activeRecording: RecordingSessionStatus? = nil,
        cameraName: String = "Selected camera",
        motionEvents: [MotionEvent] = []
    ) {
        self.segments = segments
        self.activeRecording = activeRecording
        self.cameraName = cameraName
        self.motionEvents = motionEvents
    }

    // Live: window ends at "now" so the cursor sits at the live edge.
    // Off-live: the window is pinned to `anchorEnd` (a fixed right edge) so it stays
    // put while scrubbing — the cursor then tracks the mouse exactly. We only re-anchor
    // on discrete events (scrub start, playback reaching an edge, window-size change),
    // never continuously, so the strip never slides out from under the pointer.
    // The `nil` fallback (auto-center on targetDate) only applies before the first
    // anchor is established.
    private var windowEnd: Date {
        if playback.isLive { return Date() }
        if let anchorEnd { return min(anchorEnd, Date()) }
        return min(playback.targetDate.addingTimeInterval(windowSeconds * 0.5), Date())
    }
    private var windowStart: Date { windowEnd.addingTimeInterval(-windowSeconds) }
    private var cursorDate: Date { playback.isLive ? windowEnd : playback.targetDate }

    var body: some View {
        VStack(spacing: 0) {
            timelineHeader
            GeometryReader { geo in
                timelineTrack(w: geo.size.width, h: geo.size.height)
            }
        }
        .background(SentinelTheme.well)
        .overlay(alignment: .top) {
            Rectangle().fill(SentinelTheme.accent.opacity(0.7)).frame(height: 2)
        }
        // Back at the live edge → drop the anchor so the window follows "now".
        .onChange(of: playback.isLive) { live in
            if live { anchorEnd = nil }
        }
        // While playing/nudging recorded video (not scrubbing), keep the window put
        // and let the playhead travel across it; only re-center when it nears an edge
        // so it never scrolls out of view. Skipped during scrubbing so the drag-frozen
        // window stays locked to the mouse.
        .onChange(of: playback.targetDate) { t in
            guard playback.isLive == false, playback.isScrubbing == false else { return }
            if let anchorEnd {
                let frac = t.timeIntervalSince(anchorEnd.addingTimeInterval(-windowSeconds)) / windowSeconds
                if frac > 0.92 || frac < 0.08 {
                    self.anchorEnd = min(t.addingTimeInterval(windowSeconds * 0.5), Date())
                }
            } else {
                anchorEnd = min(t.addingTimeInterval(windowSeconds * 0.5), Date())
            }
        }
        // Changing the zoom (10m/1h/24h) re-centers the anchored window on the playhead.
        .onChange(of: windowSeconds) { _ in
            if anchorEnd != nil, playback.isLive == false {
                anchorEnd = min(playback.targetDate.addingTimeInterval(windowSeconds * 0.5), Date())
            }
        }
    }

    // ── Header ──────────────────────────────────────────────────────────────

    private var timelineHeader: some View {
        HStack(spacing: 10) {
            Image(systemName: "chart.bar.xaxis")
                .font(.caption.weight(.semibold))
                .foregroundStyle(SentinelTheme.accent)

            Text(cameraName)
                .font(.caption.weight(.semibold))
                .foregroundStyle(ink.opacity(0.85))
                .lineLimit(1)

            if let rec = activeRecording {
                HStack(spacing: 5) {
                    Circle().fill(Color(red: 0.12, green: 0.82, blue: 0.45)).frame(width: 5, height: 5)
                    Text("REC · \(RecordingFormatters.timeFormatter.string(from: rec.startedAt))")
                        .font(.system(size: 9, weight: .bold))
                        .foregroundStyle(Color(red: 0.12, green: 0.82, blue: 0.45))
                }
                .padding(.horizontal, 8).padding(.vertical, 3)
                .background(Color(red: 0.12, green: 0.82, blue: 0.45).opacity(0.12), in: Capsule())
            } else {
                Text(segments.isEmpty ? "No recordings" : "\(segments.count) clips · \(visibleEvents.count) events · \(Int(windowSeconds / 60))m window")
                    .font(.system(size: 9, weight: .regular))
                    .foregroundStyle(ink.opacity(0.35))
            }

            transportControls

            Spacer()

            // Color legend lives in the header (own row) so it never overlaps
            // the time-axis labels at the bottom of the track.
            HStack(spacing: 10) {
                tlLegend(clrRecorded, "Recorded", shape: .bar)
                tlLegend(clrLiveRec,  "Live REC",  shape: .bar)
                tlLegend(clrMotion,   "Motion",    shape: .spike)
                tlLegend(clrPerson,   "Person",    shape: .spike)
            }
            .padding(.trailing, 10)

            Button { showExport = true } label: {
                HStack(spacing: 4) {
                    Image(systemName: "square.and.arrow.up").font(.system(size: 10, weight: .semibold))
                    Text("Export").font(.system(size: 9, weight: .semibold))
                }
                .foregroundStyle(ink.opacity(0.85))
                .padding(.horizontal, 8).padding(.vertical, 4)
                .background(ink.opacity(0.08), in: Capsule())
            }
            .buttonStyle(.plain)
            .disabled(segments.isEmpty)
            .help("Export a clip for a precise time range")

            HStack(spacing: 3) {
                windowBtn("10m", 600)
                windowBtn("1h",  3_600)
                windowBtn("24h", 86_400)
            }
        }
        .padding(.horizontal, 16)
        .frame(height: 40)
        .background(SentinelTheme.panel)
        .sheet(isPresented: $showExport) {
            let exportCameraID = segments.first?.cameraID ?? UUID()
            ClipExportSheet(
                segments: segments,
                cameraID: exportCameraID,
                cameraName: cameraName,
                around: cursorDate,
                operatorName: operatorSessionStore.currentOperator?.name ?? "Operator",
                auditLog: workflowStore.auditLog,
                cameraIPAddress: cameraStore.cameras.first(where: { $0.id == exportCameraID })?.ipAddress ?? "",
                evidenceExporter: evidenceExporter
            )
        }
    }

    // Go-Live / playback transport. Live shows a green LIVE pill; once scrubbed
    // off the edge it becomes a "Go Live" button plus play-pause + ±10s.
    @ViewBuilder
    private var transportControls: some View {
        HStack(spacing: 8) {
            if playback.isLive {
                HStack(spacing: 5) {
                    Circle().fill(clrLiveRec).frame(width: 6, height: 6)
                    Text("LIVE").font(.system(size: 9, weight: .bold)).foregroundStyle(clrLiveRec)
                }
                .padding(.horizontal, 9).padding(.vertical, 4)
                .background(clrLiveRec.opacity(0.14), in: Capsule())
            } else {
                Button { playback.goLive() } label: {
                    HStack(spacing: 5) {
                        Image(systemName: "forward.end.fill").font(.system(size: 8, weight: .bold))
                        Text("GO LIVE").font(.system(size: 9, weight: .bold))
                    }
                    .foregroundStyle(.white)
                    .padding(.horizontal, 9).padding(.vertical, 4)
                    .background(clrPerson.opacity(0.9), in: Capsule())
                }
                .buttonStyle(.plain)
                .help("Return to live")

                Button { playback.nudge(-10) } label: {
                    Image(systemName: "gobackward.10").font(.system(size: 11, weight: .semibold)).foregroundStyle(ink.opacity(0.85))
                }
                .buttonStyle(.plain)
                .help("Back 10s")

                Button { playback.isPaused.toggle() } label: {
                    Image(systemName: playback.isPaused ? "play.fill" : "pause.fill")
                        .font(.system(size: 11, weight: .semibold)).foregroundStyle(ink)
                }
                .buttonStyle(.plain)
                .help(playback.isPaused ? "Play" : "Pause")

                Button { playback.nudge(10) } label: {
                    Image(systemName: "goforward.10").font(.system(size: 11, weight: .semibold)).foregroundStyle(ink.opacity(0.85))
                }
                .buttonStyle(.plain)
                .help("Forward 10s")
            }
        }
    }

    private func windowBtn(_ label: String, _ secs: Double) -> some View {
        Button { windowSeconds = secs } label: {
            Text(label)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(windowSeconds == secs ? .white : ink.opacity(0.42))
                .padding(.horizontal, 9).padding(.vertical, 5)
                .background(
                    windowSeconds == secs ? SentinelTheme.accent.opacity(0.9) : ink.opacity(0.08),
                    in: RoundedRectangle(cornerRadius: 5)
                )
        }
        .buttonStyle(.plain)
    }

    // Professional VMS color palette — matches Milestone/Genetec/Axis conventions:
    //   Blue  = archived recording presence
    //   Green = live/active recording
    //   Amber = motion activity
    //   Red   = person / alarm detection (exclusive — not shared with recording)
    //   White = scrubber cursor

    private let clrRecorded  = Color(red: 0.15, green: 0.52, blue: 0.92)  // blue  – archived
    private let clrLiveRec   = Color(red: 0.12, green: 0.82, blue: 0.45)  // green – live write
    private let clrMotion    = Color(red: 0.98, green: 0.72, blue: 0.10)  // amber – motion
    private let clrPerson    = Color(red: 1.00, green: 0.28, blue: 0.22)  // red   – person/alarm

    // Lane layout constants (measured from top of track area, below the 40px header)
    // ┌────────── recording band  ──────────┐  y=6,  h=10
    // ┌─── motion spikes ───────────────────┐  y=20, up to 32px tall, grows upward
    // ┌─── person spikes ───────────────────┐  y=18, up to 38px tall, grows upward
    // ──────────────── time axis ────────────  y = h-18
    private let recBandY: CGFloat  = 6
    private let recBandH: CGFloat  = 10
    private let eventBaseY: CGFloat = 56  // baseline the spikes grow UP from

    private func timelineTrack(w: CGFloat, h: CGFloat) -> some View {
        let axisY = h - 20
        let cx = xf(cursorDate, w)

        return ZStack(alignment: .topLeading) {
            // Background
            SentinelTheme.well

            // ── Grid lines ───────────────────────────────────────────────────
            ForEach(labelDates, id: \.timeIntervalSinceReferenceDate) { t in
                Rectangle()
                    .fill(ink.opacity(0.04))
                    .frame(width: 1, height: axisY - recBandY)
                    .offset(x: xf(t, w), y: recBandY)
            }

            // ── Lane dividers ─────────────────────────────────────────────────
            // Subtle separator between recording and event lanes
            Rectangle()
                .fill(ink.opacity(0.06))
                .frame(height: 1)
                .offset(y: recBandY + recBandH + 4)

            // ── Lane labels (left edge) ───────────────────────────────────────
            Group {
                laneLabel("REC", y: recBandY + 1)
                laneLabel("EVT", y: recBandY + recBandH + 8)
            }

            // ── Recording presence band ───────────────────────────────────────
            // Track rail (empty segments shown as very faint trough)
            ZStack(alignment: .leading) {
                Capsule()
                    .fill(ink.opacity(0.05))
                    .frame(height: recBandH)

                // Archived segments — blue
                ForEach(visibleSegments) { seg in
                    let sx = xf(segStart(seg), w)
                    let bw = max(2, xf(segEnd(seg), w) - sx)
                    RoundedRectangle(cornerRadius: 2)
                        .fill(
                            LinearGradient(
                                colors: [clrRecorded.opacity(0.9), clrRecorded.opacity(0.75)],
                                startPoint: .top, endPoint: .bottom
                            )
                        )
                        .frame(width: bw, height: recBandH)
                        .offset(x: sx)
                        .help("Recorded · \(seg.timeLabel) · \(seg.sizeLabel)")
                }

                // Active recording session. The footage it has already written is
                // recorded-on-disk like any other clip, so its span is BLUE — only the
                // live write-head at the leading (now) edge is GREEN, so "green = live,
                // blue = recorded" holds even during continuous 24/7 recording.
                if let rec = activeRecording {
                    let rx = xf(max(rec.startedAt, windowStart), w)
                    let edge = xf(windowEnd, w)
                    let rw = max(3, edge - rx)
                    ZStack(alignment: .trailing) {
                        RoundedRectangle(cornerRadius: 2)
                            .fill(
                                LinearGradient(
                                    colors: [clrRecorded.opacity(0.9), clrRecorded.opacity(0.75)],
                                    startPoint: .top, endPoint: .bottom
                                )
                            )
                            .frame(width: rw, height: recBandH)
                        // Live write-head — bright green cap + pulse at the now edge.
                        RoundedRectangle(cornerRadius: 2)
                            .fill(
                                LinearGradient(
                                    colors: [clrLiveRec.opacity(0.0), clrLiveRec.opacity(0.95)],
                                    startPoint: .leading, endPoint: .trailing
                                )
                            )
                            .frame(width: min(rw, 24), height: recBandH)
                        Rectangle()
                            .fill(clrLiveRec)
                            .frame(width: 2, height: recBandH)
                    }
                    .offset(x: rx)
                    .help("Recording since \(RecordingFormatters.timeFormatter.string(from: rec.startedAt))")
                }

                // Motion markers laid directly on the recording band, so each
                // detection is visible against the footage it belongs to (the
                // EVT lane below still shows intensity). Amber notch + bright cap.
                ForEach(visibleEvents.filter { $0.kind == .motion }) { ev in
                    let mx = xf(ev.timestamp, w)
                    ZStack(alignment: .top) {
                        Rectangle()
                            .fill(clrMotion.opacity(0.9))
                            .frame(width: 2, height: recBandH)
                        Circle()
                            .fill(clrMotion)
                            .frame(width: 4, height: 4)
                            .offset(y: -3)
                    }
                    .offset(x: max(0, mx - 1))
                    .help("Motion · \(RecordingFormatters.timeFormatter.string(from: ev.timestamp))")
                }
            }
            .frame(maxWidth: .infinity)
            .offset(y: recBandY)

            // ── Event spikes (grow upward from eventBaseY) ────────────────────
            // Motion — amber, shorter spikes with slight gradient
            ForEach(visibleEvents.filter { $0.kind == .motion }) { ev in
                let x = xf(ev.timestamp, w)
                let spikeH = CGFloat(12 + min(ev.intensity * 36, 22))
                RoundedRectangle(cornerRadius: 1.5)
                    .fill(
                        LinearGradient(
                            colors: [clrMotion.opacity(0.22), clrMotion.opacity(0.85)],
                            startPoint: .top, endPoint: .bottom
                        )
                    )
                    .frame(width: 2, height: spikeH)
                    .offset(x: max(0, x - 1), y: eventBaseY - spikeH)
                    .help("Motion · \(RecordingFormatters.timeFormatter.string(from: ev.timestamp))")
            }

            // Person — red, taller, wider, more prominent
            ForEach(visibleEvents.filter { $0.kind == .person }) { ev in
                let x = xf(ev.timestamp, w)
                ZStack(alignment: .bottom) {
                    // Glow halo
                    RoundedRectangle(cornerRadius: 2)
                        .fill(clrPerson.opacity(0.15))
                        .frame(width: 7, height: 38)
                    // Core spike
                    RoundedRectangle(cornerRadius: 1.5)
                        .fill(
                            LinearGradient(
                                colors: [clrPerson.opacity(0.30), clrPerson.opacity(1.0)],
                                startPoint: .top, endPoint: .bottom
                            )
                        )
                        .frame(width: 3, height: 38)
                    // Diamond tip
                    Rectangle()
                        .fill(clrPerson)
                        .frame(width: 5, height: 5)
                        .rotationEffect(.degrees(45))
                        .offset(y: -34)
                }
                .offset(x: max(0, x - 3.5), y: eventBaseY - 38)
                .help("Person detected · \(RecordingFormatters.timeFormatter.string(from: ev.timestamp))")
            }

            // ── Time axis ─────────────────────────────────────────────────────
            Rectangle()
                .fill(ink.opacity(0.08))
                .frame(height: 1)
                .offset(y: axisY - 2)

            ForEach(labelDates, id: \.timeIntervalSinceReferenceDate) { t in
                let lx = xf(t, w)
                // Tick mark
                Rectangle()
                    .fill(ink.opacity(0.16))
                    .frame(width: 1, height: 4)
                    .offset(x: lx, y: axisY - 2)
                // Label
                Text(labelText(t))
                    .font(.system(size: 9, weight: .regular, design: .monospaced))
                    .foregroundStyle(ink.opacity(0.38))
                    .frame(width: 44, alignment: .center)
                    .offset(x: min(max(lx - 22, 34), w - 44), y: axisY + 2)
            }

            // Legend removed: it overlapped the time labels and the
            // REC/EVT colors are self-evident from the lane labels on the left.

            // ── Scrubber cursor ───────────────────────────────────────────────
            // Full-height white line — clearly distinct from event colors
            Rectangle()
                .fill(
                    LinearGradient(
                        colors: [ink.opacity(0.9), ink.opacity(0.4)],
                        startPoint: .top, endPoint: .bottom
                    )
                )
                .frame(width: 1, height: axisY - recBandY + 2)
                .offset(x: cx, y: recBandY)

            // Top nub (white diamond)
            Rectangle()
                .fill(ink.opacity(0.95))
                .frame(width: 8, height: 8)
                .rotationEffect(.degrees(45))
                .offset(x: cx - 4, y: recBandY - 4)

            // Time pill above axis
            Text(RecordingFormatters.timeFormatter.string(from: cursorDate))
                .font(.system(size: 9.5, weight: .semibold, design: .monospaced))
                .foregroundStyle(.white)
                .padding(.horizontal, 7).padding(.vertical, 2.5)
                .background {
                    Capsule().fill(Color(red: 0.12, green: 0.16, blue: 0.24))
                    Capsule().stroke(ink.opacity(0.28), lineWidth: 1)
                }
                .offset(x: min(max(cx - 28, 34), w - 86), y: axisY + 2)

            // ── Drag to scrub ─────────────────────────────────────────────────
            // Dragging leaves the live edge and drives synchronized playback
            // across every visible tile via the shared controller.
            Color.clear
                .contentShape(Rectangle())
                .gesture(
                    DragGesture(minimumDistance: 0)
                        .onChanged { v in
                            // Freeze the window the instant the drag starts — before
                            // `beginScrub` moves `targetDate` — so the mouse→time mapping
                            // (and the cursor) stay locked to the strip for the whole drag.
                            if playback.isScrubbing == false { anchorEnd = windowEnd }
                            let date = resolvedScrubDate(dateAt(x: v.location.x, width: w))
                            if playback.isScrubbing {
                                playback.scrub(to: date)
                            } else {
                                playback.beginScrub(to: date)
                            }
                        }
                        .onEnded { v in
                            playback.endScrub(at: resolvedScrubDate(dateAt(x: v.location.x, width: w)))
                        }
                )
        }
        .clipShape(Rectangle())
    }

    private func dateAt(x: CGFloat, width: CGFloat) -> Date {
        let ratio = Double(min(max(x, 0), width) / max(width, 1))
        return windowStart.addingTimeInterval(ratio * windowEnd.timeIntervalSince(windowStart))
    }

    // If the raw scrub point is inside a recorded segment, keep it exact;
    // otherwise snap to the nearest segment so sparse/short recordings are
    // reachable instead of landing on empty (black) time.
    private func resolvedScrubDate(_ raw: Date) -> Date {
        if segments.contains(where: { raw >= segStart($0) && raw <= segEnd($0) }) {
            return raw
        }
        guard let nearest = segments.min(by: { gapToSegment($0, raw) < gapToSegment($1, raw) }) else {
            return raw
        }
        return min(max(raw, segStart(nearest)), segEnd(nearest))
    }

    private func gapToSegment(_ seg: RecordingSegment, _ date: Date) -> TimeInterval {
        if date < segStart(seg) { return segStart(seg).timeIntervalSince(date) }
        if date > segEnd(seg) { return date.timeIntervalSince(segEnd(seg)) }
        return 0
    }

    private enum LegendShape { case bar, spike }

    private func tlLegend(_ color: Color, _ label: String, shape: LegendShape) -> some View {
        HStack(spacing: 4) {
            switch shape {
            case .bar:
                RoundedRectangle(cornerRadius: 1).fill(color).frame(width: 12, height: 4)
            case .spike:
                RoundedRectangle(cornerRadius: 1).fill(color).frame(width: 3, height: 10)
            }
            Text(label).font(.system(size: 9, weight: .semibold)).foregroundStyle(ink.opacity(0.70))
        }
    }

    private func laneLabel(_ text: String, y: CGFloat) -> some View {
        Text(text)
            .font(.system(size: 7, weight: .bold, design: .monospaced))
            .foregroundStyle(ink.opacity(0.20))
            .frame(width: 28, alignment: .leading)
            .offset(x: 4, y: y)
    }

    // ── Coordinate math ──────────────────────────────────────────────────────

    private func xf(_ date: Date, _ w: CGFloat) -> CGFloat {
        let total = windowEnd.timeIntervalSince(windowStart)
        return CGFloat(min(max(date.timeIntervalSince(windowStart) / total, 0), 1)) * w
    }

    private func segStart(_ s: RecordingSegment) -> Date { min(s.createdAt, s.modifiedAt) }
    private func segEnd(_ s: RecordingSegment) -> Date {
        let e = max(s.createdAt, s.modifiedAt)
        return e.timeIntervalSince(segStart(s)) < 1 ? segStart(s).addingTimeInterval(60) : e
    }

    // ── Data ─────────────────────────────────────────────────────────────────

    private var visibleSegments: [RecordingSegment] {
        segments.filter { segEnd($0) >= windowStart && segStart($0) <= windowEnd }
    }

    private var visibleEvents: [MotionEvent] {
        motionEvents.filter { $0.timestamp >= windowStart && $0.timestamp <= windowEnd }
    }

    private var labelDates: [Date] {
        let interval: Double = windowSeconds > 7200 ? 14400 : windowSeconds > 900 ? 900 : 120
        var result: [Date] = []
        var t = Date(timeIntervalSince1970: ceil(windowStart.timeIntervalSince1970 / interval) * interval)
        while t <= windowEnd {
            if t >= windowStart { result.append(t) }
            t = t.addingTimeInterval(interval)
        }
        return result
    }

    private func labelText(_ date: Date) -> String {
        let f = DateFormatter()
        f.dateFormat = "HH:mm"
        return f.string(from: date)
    }
}
