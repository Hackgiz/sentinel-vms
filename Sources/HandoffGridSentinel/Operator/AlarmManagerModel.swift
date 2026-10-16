import Foundation
import SentinelCore

/// State of the Alarm Manager workspace: filter bar, paged table, state-chip
/// counts, selection and bulk actions. The decisions (query building, sorting,
/// batching, live merging) live in SentinelCore types; this object sequences
/// the server calls and publishes the results.
@MainActor
final class AlarmManagerModel: ObservableObject {
    static let pageSize = 100

    @Published var filter = AlarmFilter.default {
        didSet { if filter != oldValue { filterChanged(from: oldValue) } }
    }
    @Published var sort = AlarmSort()
    @Published private(set) var pager = CursorPager<V2.Alarm>()
    @Published private(set) var counts = V2.AlarmCounts()
    @Published private(set) var isLoading = false
    @Published private(set) var error: String?
    @Published var selection: Set<String> = []
    @Published private(set) var isBusy = false
    /// Whether the server offers AI (hides the Triage button when false).
    @Published private(set) var aiEnabled = false
    /// The server's own words after an AI call said off / limit reached.
    @Published var aiNotice: String?
    @Published private(set) var aiBlocked = false

    private var api: SentinelAPI?
    private var sync = AlarmLiveSync()
    private var generation = 0
    private var reloadTask: Task<Void, Never>?
    private var countsTask: Task<Void, Never>?

    var rows: [V2.Alarm] { AlarmSorting.sorted(pager.items, by: sort) }
    var chips: [AlarmChip] { AlarmChips.make(filter: filter, counts: counts) }

    func alarm(_ id: String?) -> V2.Alarm? {
        guard let id else { return nil }
        return pager.items.first { $0.id == id }
    }

    /// The single selected alarm the inspector shows.
    var inspected: V2.Alarm? {
        selection.count == 1 ? alarm(selection.first) : nil
    }

    // MARK: Loading

    func start(api: SentinelAPI, queue: AlarmQueue) {
        guard self.api == nil else { return }
        self.api = api
        sync.markKnown((queue.open + queue.recentlyClosed).map(\.id))
        Task { aiEnabled = await api.aiEnabled() }
        reload()
        reloadCounts()
    }

    private func filterChanged(from old: AlarmFilter) {
        // Any change of the bar reloads (text is debounced); counts only care
        // about everything except the state chips.
        scheduleReload(after: old.q == filter.q ? 0.05 : 0.3)
        var a = old; a.states = filter.states
        if a != filter { reloadCounts(after: filter.q == old.q ? 0.05 : 0.3) }
    }

    func scheduleReload(after delay: TimeInterval = 0.05) {
        reloadTask?.cancel()
        reloadTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self?.selection = []
            await self?.load(more: false)
        }
    }

    func reload() { scheduleReload(after: 0) }

    func loadMore() { Task { await load(more: true) } }

    private func load(more: Bool) async {
        guard let api else { return }
        if more, !pager.hasMore { return }
        generation += 1
        let mine = generation
        isLoading = true
        error = nil
        defer { if mine == generation { isLoading = false } }
        do {
            let page = try await api.alarmsPage(filter: filter, limit: Self.pageSize, before: more ? pager.nextCursor : nil)
            guard mine == generation else { return }
            if more { pager.append(page) } else { pager.replace(with: page) }
            sync.markKnown(pager.items.map(\.id))
        } catch {
            if mine == generation { self.error = error.localizedDescription }
        }
    }

    func reloadCounts(after delay: TimeInterval = 0) {
        countsTask?.cancel()
        countsTask = Task { [weak self] in
            if delay > 0 { try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) }
            guard !Task.isCancelled, let self, let api = self.api else { return }
            // The chips just lose their counts when this fails.
            if let fresh = try? await api.alarmCounts(filter: self.filter), !Task.isCancelled {
                self.counts = fresh
            }
        }
    }

    // MARK: Live updates (from the app's single event-stream consumer)

    /// Called when `AlarmQueue` changed: it is fed by `SentinelEventStream`
    /// (`alarm.new` / `alarm.updated`), so the table follows without polling.
    func ingestLive(open: [V2.Alarm], closed: [V2.Alarm]) {
        guard api != nil, pager.isLoaded else { return }
        var next = pager
        let result = sync.ingest(open + closed, into: &next, filter: filter)
        guard result.changed else { return }
        pager = next
        selection = selection.filter { id in pager.items.contains { $0.id == id } }
        reloadCounts(after: 0.6)
        if result.needsReload { scheduleReload(after: 0.6) }
    }

    /// Deep link from the live queue: show every state and select that alarm.
    func focus(alarmID: String) async {
        guard let api else { return }
        do {
            let target = try await api.alarm(alarmID)
            filter = AlarmFilter(states: [])
            await load(more: false)
            if !pager.items.contains(where: { $0.id == target.id }) {
                pager.insertAtFront(target)
                sync.markKnown([target.id])
            }
            selection = [target.id]
        } catch {
            self.error = error.localizedDescription
        }
    }

    // MARK: Changes

    /// Puts an updated alarm into the table (and the shared queue).
    func put(_ alarm: V2.Alarm, queue: AlarmQueue) {
        queue.apply(alarm, isNew: false)
        if filter.includesState(of: alarm) {
            pager.replaceExisting(alarm)
        } else {
            pager.remove(id: alarm.id)
            selection.remove(alarm.id)
        }
        reloadCounts(after: 0.3)
    }

    func bulk(_ action: AlarmBulk.Action, queue: AlarmQueue) async {
        guard let api else { return }
        let target = AlarmBulk.targets(action, alarms: pager.items, selected: selection)
        guard !target.ids.isEmpty else { return }
        isBusy = true
        error = nil
        let state = target.state
        let note = action == .acknowledge ? "Acknowledged in bulk" : "Closed in bulk"
        let outcome = await AlarmBulk.run(ids: target.ids) { id in
            try await api.setAlarmState(id, V2.AlarmStateChange(state: state, note: note))
        }
        finish(outcome, queue: queue)
    }

    func bulkAssign(owner: String, queue: AlarmQueue) async {
        guard let api, !selection.isEmpty else { return }
        isBusy = true
        error = nil
        let outcome = await AlarmBulk.run(ids: Array(selection)) { id in
            try await api.assignAlarm(id, owner: owner)
        }
        finish(outcome, queue: queue)
    }

    private func finish(_ outcome: AlarmBulk.Outcome<V2.Alarm>, queue: AlarmQueue) {
        for alarm in outcome.succeeded { put(alarm, queue: queue) }
        if let summary = outcome.summary {
            error = [summary, outcome.firstFailure].compactMap { $0 }.joined(separator: " ")
        }
        selection = []
        isBusy = false
        reload()
        reloadCounts()
    }

    // MARK: AI

    /// Records an AI failure: the server's message is shown and, when AI is
    /// off or capped, the Triage button stops offering itself.
    func note(aiFailure failure: AIFailure) {
        aiNotice = failure.message
        if failure.disablesAI { aiBlocked = true }
        if case .off = failure { aiEnabled = false }
    }
}
