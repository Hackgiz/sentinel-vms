import AppKit
import Foundation
import SentinelCore

/// The video wall's site state (Wave 10, stream A): the wall layouts the
/// user can recall, every saved view a monitor window may show (own and
/// shared, for tours and recalled layouts), and the alarm monitor's queue.
/// Lives in `SiteRuntime`; the queue is fed from the live event stream.
@MainActor
final class WallStore: ObservableObject {
    @Published private(set) var layouts: [V2.WallLayout] = []
    /// Private and shared saved views, by name.
    @Published private(set) var catalog: [V2.SavedView] = []
    @Published private(set) var isLoaded = false
    @Published private(set) var lastError: String?
    let alarmMonitor = AlarmMonitorModel()

    func view(_ id: String?) -> V2.SavedView? {
        guard let id else { return nil }
        return catalog.first { $0.id == id }
    }

    func reload(api: SentinelAPI) async {
        async let walls = try? api.wallLayouts()
        async let mine = try? api.savedViews()
        async let tree = try? api.sharedViewTree()
        let (w, own, shared) = await (walls, mine, tree)
        if let own, let shared {
            var seen = Set<String>()
            catalog = (own + shared.views)
                .filter { seen.insert($0.id).inserted }
                .sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        }
        if let w {
            layouts = w
            lastError = nil
        } else {
            lastError = "Couldn't load wall layouts."
        }
        isLoaded = true
    }

    @discardableResult
    func create(_ input: V2.WallLayoutInput, api: SentinelAPI) async throws -> V2.WallLayout {
        let saved = try await api.createWallLayout(input)
        upsert(saved)
        return saved
    }

    @discardableResult
    func update(_ id: String, _ patch: V2.WallLayoutPatch, api: SentinelAPI) async throws -> V2.WallLayout {
        let saved = try await api.updateWallLayout(id, patch)
        upsert(saved)
        return saved
    }

    func delete(_ layout: V2.WallLayout, api: SentinelAPI) async throws {
        try await api.deleteWallLayout(layout.id)
        layouts.removeAll { $0.id == layout.id }
    }

    private func upsert(_ layout: V2.WallLayout) {
        layouts.removeAll { $0.id == layout.id }
        layouts.append(layout)
        layouts.sort { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
    }
}

/// The alarms alarm-monitor windows show, newest first. Every alarm monitor
/// window shows the same queue (dismissing on one dismisses on all).
@MainActor
final class AlarmMonitorModel: ObservableObject {
    @Published private(set) var queue = AlarmMonitorQueue()
    @Published private(set) var lastError: String?

    /// From `SiteRuntime.handle` for `alarm.new` / `alarm.updated`.
    func apply(_ alarm: V2.Alarm, isNew: Bool) {
        queue.apply(alarm, isNew: isNew)
    }

    func dismiss(_ id: String) {
        queue.dismiss(id)
    }

    func promote(_ id: String) {
        queue.promote(id)
    }

    func clear() {
        queue.removeAll()
    }

    /// Acknowledges the alarm on the server; it leaves the monitor once the
    /// answer (or the stream's `alarm.updated`) says it is no longer New.
    func acknowledge(_ alarm: V2.Alarm, runtime: SiteRuntime) async {
        do {
            try await runtime.alarms.setState(alarm, to: .acknowledged, note: "Acknowledged on the alarm monitor", api: runtime.api)
            if let updated = runtime.alarms.alarm(alarm.id) { queue.apply(updated, isNew: false) } else { queue.dismiss(alarm.id) }
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }
}

/// One monitor window's wall state: alarm-monitor flag and tour. Owned by
/// `OperatorWindows` (so saving a layout can read it) and shown by the
/// window's content.
@MainActor
final class MonitorWindowState: ObservableObject {
    let id: String
    @Published var alarmMonitor: Bool { didSet { if alarmMonitor != oldValue { OperatorWindows.shared.monitorStateChanged() } } }
    @Published private(set) var tourPlan: WallTourPlan?
    /// The view the tour shows now (the window opens it).
    @Published private(set) var tourViewID: String?
    /// Bumped each time the tour moves on, even back to the same view.
    @Published private(set) var tourStep = 0
    @Published private(set) var captionVisible = false
    @Published private(set) var tourPaused = false

    /// What to show once the window's content is up (a recalled layout).
    var pendingContent: WallMonitorContent?
    /// The window's grid, set by its content.
    weak var workspace: WorkspaceModel?

    private var schedule: TourSchedule?
    private var pauseReasons: Set<String> = []
    private var timer: Task<Void, Never>?

    init(id: String, alarmMonitor: Bool = false) {
        self.id = id
        self.alarmMonitor = alarmMonitor
    }

    // MARK: Tour

    func startTour(_ plan: WallTourPlan, startViewID: String? = nil) {
        guard let plan = plan.normalized else { stopTour(); return }
        tourPlan = plan
        let start = startViewID.flatMap { plan.viewIDs.firstIndex(of: $0) } ?? 0
        var next = TourSchedule(count: plan.viewIDs.count, seconds: TimeInterval(plan.seconds), start: Date(), startIndex: start)
        if pauseReasons.isEmpty == false { next.pause(at: Date()) }
        schedule = next
        tourViewID = nil
        tick()
        OperatorWindows.shared.monitorStateChanged()
    }

    func stopTour() {
        timer?.cancel()
        timer = nil
        schedule = nil
        tourPlan = nil
        tourViewID = nil
        captionVisible = false
        tourPaused = false
        OperatorWindows.shared.monitorStateChanged()
    }

    /// The window is going away: stop the timer without touching what's remembered.
    func halt() {
        timer?.cancel()
        timer = nil
        schedule = nil
    }

    func skip(by offset: Int) {
        schedule?.skip(by: offset, at: Date())
        tick(forceStep: true)
    }

    /// Pauses the tour while any reason holds (hover, an alarm on screen).
    func setPaused(_ paused: Bool, reason: String) {
        let was = pauseReasons.isEmpty
        if paused { pauseReasons.insert(reason) } else { pauseReasons.remove(reason) }
        guard was != pauseReasons.isEmpty, schedule != nil else { return }
        if pauseReasons.isEmpty { schedule?.resume(at: Date()) } else { schedule?.pause(at: Date()) }
        tick()
    }

    private func tick(forceStep: Bool = false) {
        timer?.cancel()
        guard let schedule, let plan = tourPlan, plan.viewIDs.isEmpty == false else { return }
        let now = Date()
        let id = plan.viewIDs[schedule.index(at: now)]
        if id != tourViewID || forceStep {
            tourViewID = id
            tourStep += 1
        }
        captionVisible = schedule.captionVisible(at: now)
        tourPaused = schedule.isPaused
        guard let next = schedule.nextEvent(after: now) else { return }
        let delay = max(next.timeIntervalSince(now), 0.05)
        timer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard Task.isCancelled == false else { return }
            self?.tick()
        }
    }
}
