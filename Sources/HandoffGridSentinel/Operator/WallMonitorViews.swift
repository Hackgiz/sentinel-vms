import AppKit
import SwiftUI
import SentinelCore

/// A monitor window: a live grid with its own layout, remembered per window.
/// Wave 10: it can run a tour (cycling saved views, pausing while the
/// pointer is over it) and be an alarm monitor (a new alarm takes it over
/// full-tile until acknowledged or dismissed).
struct MonitorWindowContent: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var views: SavedViewsStore
    @EnvironmentObject private var wall: WallStore
    @EnvironmentObject private var alarmMonitor: AlarmMonitorModel
    @StateObject private var workspace: WorkspaceModel
    @ObservedObject var state: MonitorWindowState
    let windowID: String

    @State private var hovering = false
    @State private var tourSetup = false

    init(windowID: String, state: MonitorWindowState) {
        self.windowID = windowID
        self.state = state
        _workspace = StateObject(wrappedValue: WorkspaceModel(windowID: windowID))
    }

    private var showingAlarm: Bool { state.alarmMonitor && alarmMonitor.queue.current != nil }

    var body: some View {
        WorkspaceGridView(workspace: workspace)
            .padding(.top, 28)
            .background(SentinelTheme.background)
            .overlay(alignment: .top) { tourCaption }
            .overlay {
                if showingAlarm {
                    AlarmMonitorOverlay(model: alarmMonitor)
                        .padding(.top, 28)
                        .transition(.opacity)
                }
            }
            .overlay(alignment: .topTrailing) { controls }
            .onHover { inside in
                hovering = inside
                state.setPaused(inside, reason: "hover")
            }
            .simultaneousGesture(TapGesture().onEnded { state.setPaused(true, reason: "hover") })
            .sheet(isPresented: $tourSetup) {
                TourSetupSheet(views: wall.catalog, initial: state.tourPlan) { plan in
                    if let plan { state.startTour(plan, startViewID: workspace.activeViewID) } else { state.stopTour() }
                }
            }
            .onAppear {
                state.workspace = workspace
                if let site = session.site { workspace.bind(siteID: site.id) }
                applyPendingContent()
                workspace.seedIfNeeded(with: cameras.cameras.map(\.id), limit: 16)
                openTourView()
                state.setPaused(showingAlarm, reason: "alarm")
            }
            .onChange(of: cameras.cameras.map(\.id)) { ids in
                workspace.seedIfNeeded(with: ids, limit: 16)
            }
            .onChange(of: wall.catalog.map(\.id)) { _ in
                applyPendingContent()
                openTourView()
            }
            .onChange(of: state.tourStep) { _ in openTourView() }
            .onChange(of: showingAlarm) { showing in state.setPaused(showing, reason: "alarm") }
            .animation(.easeInOut(duration: 0.2), value: showingAlarm)
            .animation(.easeInOut(duration: 0.25), value: state.captionVisible)
    }

    // MARK: Content

    private func savedView(_ id: String) -> V2.View? {
        wall.view(id)?.asView ?? views.view(id)
    }

    /// A recalled layout's content, once the views it names are known.
    private func applyPendingContent() {
        guard let content = state.pendingContent else { return }
        switch content {
        case .view(let id):
            if let view = savedView(id) {
                workspace.openView(view)
            } else if wall.isLoaded {
                workspace.clearGrid() // the view is gone or not visible to this user
            } else {
                return // wait for the catalog
            }
        case .cameras(let ids):
            let visible = ids.filter { cameras.camera($0) != nil || cameras.isLoaded == false }
            if visible.isEmpty { workspace.clearGrid() } else { workspace.openAsGrid(visible) }
        case .tour(let plan, let start):
            state.startTour(plan, startViewID: start)
        case .empty:
            workspace.clearGrid()
        }
        workspace.markSeeded()
        state.pendingContent = nil
    }

    private func openTourView() {
        guard let id = state.tourViewID, let view = savedView(id) else { return }
        if workspace.activeViewID != id || workspace.grid != WorkspaceGrid(view: view) { workspace.openView(view) }
    }

    // MARK: Chrome

    @ViewBuilder
    private var tourCaption: some View {
        if state.captionVisible, let id = state.tourViewID {
            Text(savedView(id).map(\.name) ?? wall.view(id)?.name ?? "View")
                .font(.title3.weight(.semibold))
                .foregroundStyle(.white)
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
                .background(.black.opacity(0.65), in: Capsule())
                .padding(.top, 40)
                .allowsHitTesting(false)
                .transition(.opacity)
        }
    }

    private var controls: some View {
        HStack(spacing: 6) {
            if state.alarmMonitor {
                Label("Alarm monitor", systemImage: "bell.badge.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.white)
                    .padding(.horizontal, 8).padding(.vertical, 4)
                    .background(Color.red.opacity(0.75), in: Capsule())
                    .help("New alarms take over this window")
            }
            if state.tourPlan != nil {
                Label(state.tourPaused ? "Tour paused" : "Tour", systemImage: state.tourPaused ? "pause.fill" : "play.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.white)
                    .padding(.horizontal, 8).padding(.vertical, 4)
                    .background(.black.opacity(0.6), in: Capsule())
                    .help("This window cycles saved views; it pauses while the pointer is over it")
            }
            if hovering {
                Menu {
                    monitorMenu
                } label: {
                    Image(systemName: "rectangle.3.group")
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .padding(6)
                .background(.black.opacity(0.6), in: RoundedRectangle(cornerRadius: 6))
                .help("Tour, alarm monitor and full screen for this window")
            }
        }
        .padding(.top, 6)
        .padding(.trailing, 10)
    }

    @ViewBuilder
    private var monitorMenu: some View {
        Toggle("Alarm Monitor", isOn: $state.alarmMonitor)
        if state.alarmMonitor, alarmMonitor.queue.isEmpty == false {
            Button("Clear Alarms from Monitor") { alarmMonitor.clear() }
        }
        Divider()
        Button(state.tourPlan == nil ? "Start Tour…" : "Edit Tour…") { tourSetup = true }
            .disabled(wall.catalog.isEmpty)
        if state.tourPlan != nil {
            Button("Next View") { state.skip(by: 1) }
            Button("Previous View") { state.skip(by: -1) }
            Button("Stop Tour") { state.stopTour() }
        }
        Divider()
        Button("Full Screen") { NSApp.keyWindow?.toggleFullScreen(nil) }
        Button("Fullscreen All Monitors") { OperatorWindows.shared.toggleFullscreenAllMonitors() }
    }
}

/// The alarm monitor's takeover: the newest alarm's camera full-tile, a
/// strip to acknowledge or dismiss it, and the others queued beside it.
struct AlarmMonitorOverlay: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var cameras: CameraDirectory
    @ObservedObject var model: AlarmMonitorModel
    @State private var working = false
    /// Play/Pause for the alarm's footage (wave 24): its own one-camera Playback
    /// controller, opened a few seconds before the alarm, like the main Playback.
    @StateObject private var playback = PlaybackController()
    @State private var showingClip = false

    var body: some View {
        if let alarm = model.queue.current {
            HStack(spacing: 8) {
                VStack(spacing: 0) {
                    tile(for: alarm)
                    strip(for: alarm)
                }
                .onChange(of: alarm.id) { _ in closeClip() }
                .onDisappear { closeClip() }
                .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(alarm.severity.tint, lineWidth: 3))
                .clipShape(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
                if model.queue.queued.isEmpty == false {
                    queue
                }
            }
            .padding(8)
            .background(Color.black)
        }
    }

    @ViewBuilder
    private func tile(for alarm: V2.Alarm) -> some View {
        if let camera = cameras.camera(alarm.cameraId) {
            if showingClip, let lane = playback.lane(camera.id) {
                PlaybackTile(lane: lane, camera: camera, error: playback.loadErrors[camera.id])
                    .id(alarm.id)
            } else {
                LiveTile(camera: camera, isSelected: false, fill: false, showsControls: false)
                    .id(alarm.id)
            }
        } else {
            ZStack {
                Color.black
                Text("\(alarm.cameraName ?? "This camera") isn't available to you.").foregroundStyle(.secondary)
            }
        }
    }

    private func strip(for alarm: V2.Alarm) -> some View {
        HStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(alarm.severity.tint).font(.title2)
            VStack(alignment: .leading, spacing: 2) {
                Text(alarm.title).font(.headline).foregroundStyle(.white).lineLimit(1)
                Text("\(alarm.cameraName ?? cameras.name(of: alarm.cameraId)) · \(alarm.severity.rawValue) · \(OperatorFormat.relative(alarm.createdAt))")
                    .font(.caption).foregroundStyle(.white.opacity(0.75)).lineLimit(1)
                if let error = model.lastError {
                    Text(error).font(.caption2).foregroundStyle(.red).lineLimit(1)
                }
            }
            Spacer()
            if model.queue.queued.isEmpty == false {
                Text("+\(model.queue.queued.count) more").font(.caption.weight(.semibold)).foregroundStyle(.white.opacity(0.8))
            }
            if cameras.camera(alarm.cameraId) != nil {
                Button { togglePlay(alarm) } label: {
                    Image(systemName: showingClip && playback.isPlaying ? "pause.fill" : "play.fill").frame(width: 22)
                }
                .help(showingClip && playback.isPlaying ? "Pause" : "Play the footage from just before this alarm")
                if showingClip {
                    Button("Live") { closeClip() }
                        .help("Back to the live picture")
                }
            }
            Button("Dismiss") { model.dismiss(alarm.id) }
                .help("Take this alarm off the monitor; it stays open in the alarm queue")
            if session.can(.acknowledgeAlarms) {
                Button {
                    working = true
                    Task {
                        await model.acknowledge(alarm, runtime: runtime)
                        working = false
                    }
                } label: {
                    Text("Acknowledge")
                }
                .keyboardShortcut(.defaultAction)
                .disabled(working)
            }
        }
        .controlSize(.large)
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(Color.black.opacity(0.92))
    }

    /// First press opens the footage 10 s before the alarm and plays; after that it toggles.
    private func togglePlay(_ alarm: V2.Alarm) {
        if showingClip {
            playback.togglePlay()
            return
        }
        playback.attach(api: runtime.api, video: runtime.video)
        playback.setCameras([alarm.cameraId].compactMap { $0 })
        playback.leaveLive()
        playback.go(to: PlaybackJump.start(of: alarm))
        showingClip = true
        playback.play()
    }

    private func closeClip() {
        guard showingClip || playback.isLive == false else { return }
        showingClip = false
        playback.enterLive()
        playback.stop()
    }

    private var queue: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 6) {
                Text("Waiting").font(.caption.weight(.bold)).foregroundStyle(.secondary)
                ForEach(model.queue.queued) { alarm in
                    Button { model.promote(alarm.id) } label: {
                        HStack(alignment: .top, spacing: 6) {
                            Circle().fill(alarm.severity.tint).frame(width: 8, height: 8).padding(.top, 4)
                            VStack(alignment: .leading, spacing: 1) {
                                Text(alarm.title).font(.caption.weight(.semibold)).foregroundStyle(.white).lineLimit(2)
                                Text("\(alarm.cameraName ?? cameras.name(of: alarm.cameraId)) · \(OperatorFormat.relative(alarm.createdAt))")
                                    .font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                        }
                        .padding(8)
                        .background(Color.white.opacity(0.08), in: RoundedRectangle(cornerRadius: 6))
                    }
                    .buttonStyle(.plain)
                    .help("Show this alarm full-tile")
                }
            }
            .padding(8)
        }
        .frame(width: 220)
        .background(Color.white.opacity(0.04), in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
    }
}

/// Picks the views a monitor tours and how long each stays up.
struct TourSetupSheet: View {
    let views: [V2.SavedView]
    let initial: WallTourPlan?
    let onDone: (WallTourPlan?) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var selected: [String] = []
    @State private var seconds = 20

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Tour").font(.title3.weight(.semibold))
            Text("The window cycles these views in the order you tick them, pausing while the pointer is over it.")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            List(views) { view in
                Toggle(isOn: Binding(
                    get: { selected.contains(view.id) },
                    set: { on in
                        if on { if selected.contains(view.id) == false { selected.append(view.id) } } else { selected.removeAll { $0 == view.id } }
                    }
                )) {
                    HStack {
                        Text(view.name)
                        if view.isShared { Text("Shared").font(.caption2).foregroundStyle(.secondary) }
                        Spacer()
                        if let position = selected.firstIndex(of: view.id) {
                            Text("\(position + 1)").font(.caption.monospacedDigit()).foregroundStyle(SentinelTheme.accent)
                        }
                    }
                }
            }
            .frame(minHeight: 220)
            Stepper(value: $seconds, in: V2.WallTour.secondsRange, step: 5) {
                Text("Each view for \(seconds) seconds")
            }
            HStack {
                if initial != nil {
                    Button("Stop Tour", role: .destructive) { onDone(nil); dismiss() }
                }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Start") {
                    onDone(WallTourPlan(viewIDs: selected, seconds: seconds))
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(selected.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 420, height: 440)
        .onAppear {
            if let initial {
                selected = initial.viewIDs.filter { id in views.contains { $0.id == id } }
                seconds = initial.seconds
            }
        }
    }
}

/// The toolbar's wall picker: recall a layout, save the open monitors,
/// update or delete a layout, and take every monitor full screen.
struct WallPicker: View {
    @EnvironmentObject private var wall: WallStore

    var body: some View {
        Menu {
            WallMenuItems(wall: wall)
        } label: {
            Label("Wall", systemImage: "rectangle.3.group")
        }
        .menuStyle(.button)
        .fixedSize()
        .help("Video wall: save and recall monitor window layouts")
    }
}

/// The wall commands as SwiftUI menu items.
struct WallMenuItems: View {
    @ObservedObject var wall: WallStore

    var body: some View {
        Button("Save Current Monitors as Layout…") { WallActions.saveCurrentMonitors() }
        Button("Fullscreen All Monitors") { // toggles back when all already are
            OperatorWindows.shared.toggleFullscreenAllMonitors()
        }
        Divider()
        if wall.layouts.isEmpty {
            Text(wall.isLoaded ? "No saved wall layouts" : "Loading wall layouts…")
        } else {
            Section("Recall") {
                ForEach(wall.layouts) { layout in
                    Button(layout.shared ? "\(layout.name)  (Shared)" : layout.name) { WallActions.recall(layout) }
                }
            }
            let editable = wall.layouts.filter(WallActions.canEdit)
            if editable.isEmpty == false {
                Menu("Update with Current Monitors") {
                    ForEach(editable) { layout in Button(layout.name) { WallActions.updateWithCurrentMonitors(layout) } }
                }
                Menu("Delete Layout") {
                    ForEach(editable) { layout in Button(layout.name) { WallActions.delete(layout) } }
                }
            }
        }
        Divider()
        Button("Refresh Layouts") { WallActions.refresh() }
    }
}
