import AppKit
import Foundation
import SentinelCore

/// Live state of the signed-in site, kept current by the event stream:
/// cameras, the alarm queue, recent events and saved views. Created when
/// the operator signs in, torn down when they sign out or switch sites.
@MainActor
final class SiteRuntime: ObservableObject {
    let api: SentinelAPI
    let video: VideoTokenBroker
    let cameras = CameraDirectory()
    let alarms = AlarmQueue()
    let events = EventFeed()
    let views = SavedViewsStore()
    let thumbnails: ThumbnailCache

    @Published private(set) var streamState: SentinelEventStream.State = .idle
    @Published private(set) var serverVersion: String?

    private var stream: SentinelEventStream?
    private var consumer: Task<Void, Never>?

    init(api: SentinelAPI, video: VideoTokenBroker) {
        self.api = api
        self.video = video
        self.thumbnails = ThumbnailCache(api: api)
    }

    func start() {
        stop()
        Task { await reloadAll() }
        let stream = SentinelEventStream(api: api)
        self.stream = stream
        let updates = stream.start()
        consumer = Task { [weak self] in
            for await update in updates {
                guard let self else { return }
                self.handle(update)
            }
        }
    }

    func stop() {
        consumer?.cancel()
        consumer = nil
        stream?.stop()
        stream = nil
        streamState = .idle
    }

    func reloadAll() async {
        async let c: Void = cameras.reload(api: api)
        async let a: Void = alarms.reload(api: api)
        async let e: Void = events.reload(api: api)
        async let v: Void = views.reload(api: api)
        _ = await (c, a, e, v)
    }

    private func handle(_ update: SentinelEventStream.Update) {
        switch update {
        case .state(let state):
            streamState = state
            if case .connected(let version) = state { serverVersion = version }
        case .resync:
            Task { await reloadAll() }
        case .message(let message, _):
            switch message {
            case .alarmNew(let alarm):
                alarms.apply(alarm, isNew: true)
            case .alarmUpdated(let alarm):
                alarms.apply(alarm, isNew: false)
            case .eventNew(let event):
                events.apply(event, isNew: true)
            case .eventUpdated(let event):
                events.apply(event, isNew: false)
            case .cameraStatus(let status):
                cameras.setOnline(status.cameraId, online: status.online)
            case .configChanged(let change):
                Task {
                    await cameras.reload(api: api)
                    for id in change.cameraIds { await video.invalidate(cameraID: id) }
                }
            case .recorderStatus:
                Task { await cameras.reloadRecorders(api: api) }
            case .hello, .lagged, .cameraActivity, .unknown:
                break
            }
        }
    }

    var streamSummary: (label: String, color: NSColor) {
        switch streamState {
        case .connected: return ("Live", .systemGreen)
        case .connecting, .idle: return ("Connecting", .systemOrange)
        case .waiting: return ("Reconnecting", .systemOrange)
        case .stopped: return ("Offline", .systemRed)
        }
    }
}

// MARK: - Cameras

@MainActor
final class CameraDirectory: ObservableObject {
    @Published private(set) var cameras: [V2.Camera] = []
    @Published private(set) var recorders: [V2.RecordingServer] = []
    @Published private(set) var groups: [V2.CameraGroup] = []
    @Published private(set) var isLoaded = false
    @Published private(set) var lastError: String?

    func camera(_ id: String?) -> V2.Camera? {
        guard let id else { return nil }
        return cameras.first { $0.id == id }
    }

    func name(of id: String?) -> String {
        camera(id)?.name ?? "Camera"
    }

    func recorderName(_ id: String?) -> String {
        guard let id, id.isEmpty == false else { return "—" }
        return recorders.first { $0.id == id }?.name ?? id
    }

    var onlineCount: Int { cameras.filter(\.status.online).count }

    func reload(api: SentinelAPI) async {
        do {
            cameras = try await api.cameras().sorted { ($0.sortOrder, $0.name) < ($1.sortOrder, $1.name) }
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
        isLoaded = true
        async let r: Void = reloadRecorders(api: api)
        async let g: Void = reloadGroups(api: api)
        _ = await (r, g)
    }

    func reloadRecorders(api: SentinelAPI) async {
        if let list = try? await api.recordingServers() { recorders = list }
    }

    func reloadGroups(api: SentinelAPI) async {
        if let list = try? await api.cameraGroups() { groups = list }
    }

    func setOnline(_ id: String, online: Bool) {
        guard let index = cameras.firstIndex(where: { $0.id == id }) else { return }
        cameras[index].status.online = online
        cameras[index].status.message = online ? "Online" : "No signal"
    }

    func replace(_ camera: V2.Camera) {
        if let index = cameras.firstIndex(where: { $0.id == camera.id }) {
            cameras[index] = camera
        } else {
            cameras.append(camera)
        }
    }

    func remove(_ id: String) {
        cameras.removeAll { $0.id == id }
    }
}

// MARK: - Alarms

@MainActor
final class AlarmQueue: ObservableObject {
    /// Open alarms (New, Acknowledged, Investigating, Snoozed), newest first.
    @Published private(set) var open: [V2.Alarm] = []
    /// Alarms closed during this session, newest first (for "Recently closed").
    @Published private(set) var recentlyClosed: [V2.Alarm] = []
    /// The newest critical alarm that arrived while running (drives the banner).
    @Published var attention: V2.Alarm?
    @Published private(set) var lastError: String?

    var newCount: Int { open.filter { $0.state == .new }.count }

    func alarm(_ id: String?) -> V2.Alarm? {
        guard let id else { return nil }
        return open.first { $0.id == id } ?? recentlyClosed.first { $0.id == id }
    }

    func reload(api: SentinelAPI) async {
        do {
            var all: [V2.Alarm] = []
            var cursor: String?
            repeat {
                let page = try await api.alarms(openOnly: true, cursor: cursor, limit: 200)
                all += page.items
                cursor = page.nextCursor
            } while cursor != nil && all.count < 2000
            open = sorted(all)
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    func apply(_ alarm: V2.Alarm, isNew: Bool) {
        open.removeAll { $0.id == alarm.id }
        recentlyClosed.removeAll { $0.id == alarm.id }
        if alarm.isOpen {
            open = sorted(open + [alarm])
        } else {
            recentlyClosed.insert(alarm, at: 0)
            if recentlyClosed.count > 100 { recentlyClosed.removeLast(recentlyClosed.count - 100) }
        }
        if isNew, alarm.severity == .critical, alarm.state == .new {
            attention = alarm
            NSSound.beep()
        }
        if attention?.id == alarm.id, alarm.state != .new {
            attention = nil
        }
        NSApp.dockTile.badgeLabel = newCount > 0 ? "\(newCount)" : nil
    }

    func setState(_ alarm: V2.Alarm, to state: V2.AlarmState, note: String?, snoozeMinutes: Int? = nil, api: SentinelAPI) async throws {
        let trimmed = note?.trimmingCharacters(in: .whitespacesAndNewlines)
        let updated = try await api.setAlarmState(alarm.id, V2.AlarmStateChange(
            state: state,
            note: trimmed?.isEmpty == false ? String(trimmed!.prefix(500)) : nil,
            snoozeMinutes: state == .snoozed ? snoozeMinutes : nil
        ))
        apply(updated, isNew: false)
    }

    private func sorted(_ alarms: [V2.Alarm]) -> [V2.Alarm] {
        alarms.sorted {
            if $0.state == .new, $1.state != .new { return true }
            if $1.state == .new, $0.state != .new { return false }
            if $0.severity.rank != $1.severity.rank { return $0.severity.rank < $1.severity.rank }
            return $0.lastEventAt > $1.lastEventAt
        }
    }
}

// MARK: - Events

@MainActor
final class EventFeed: ObservableObject {
    @Published private(set) var events: [V2.Event] = []
    @Published private(set) var nextCursor: String?
    @Published private(set) var isLoading = false
    @Published private(set) var lastError: String?
    /// nil = all cameras.
    @Published private(set) var cameraFilter: String?

    func reload(api: SentinelAPI, cameraID: String? = nil) async {
        cameraFilter = cameraID
        isLoading = true
        defer { isLoading = false }
        do {
            let page = try await api.events(cameraID: cameraID, limit: 200)
            events = page.items
            nextCursor = page.nextCursor
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    func loadMore(api: SentinelAPI) async {
        guard let cursor = nextCursor, isLoading == false else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let page = try await api.events(cameraID: cameraFilter, cursor: cursor, limit: 200)
            let known = Set(events.map(\.id))
            events += page.items.filter { known.contains($0.id) == false }
            nextCursor = page.nextCursor
        } catch {
            lastError = error.localizedDescription
        }
    }

    func apply(_ event: V2.Event, isNew: Bool) {
        if let filter = cameraFilter, event.cameraId != filter { return }
        if let index = events.firstIndex(where: { $0.id == event.id }) {
            events[index] = event
        } else if isNew {
            events.insert(event, at: 0)
            if events.count > 1000 { events.removeLast(events.count - 1000) }
        }
    }
}

/// Event snapshots, fetched once per event and kept in memory.
actor ThumbnailCache {
    private let api: SentinelAPI
    private let cache = NSCache<NSString, NSImage>()
    private var failed: Set<String> = []

    init(api: SentinelAPI) {
        self.api = api
        cache.countLimit = 400
    }

    func image(for eventID: String) async -> NSImage? {
        if let hit = cache.object(forKey: eventID as NSString) { return hit }
        guard failed.contains(eventID) == false else { return nil }
        guard let data = try? await api.eventThumbnail(eventID), let image = NSImage(data: data) else {
            failed.insert(eventID)
            return nil
        }
        cache.setObject(image, forKey: eventID as NSString)
        return image
    }
}

// MARK: - Saved views

@MainActor
final class SavedViewsStore: ObservableObject {
    @Published private(set) var views: [V2.View] = []
    @Published private(set) var lastError: String?

    func view(_ id: String?) -> V2.View? {
        guard let id else { return nil }
        return views.first { $0.id == id }
    }

    func reload(api: SentinelAPI) async {
        do {
            views = try await api.views().sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    @discardableResult
    func save(_ input: V2.ViewInput, replacing id: String?, api: SentinelAPI) async throws -> V2.View {
        let saved: V2.View
        if let id {
            saved = try await api.replaceView(id, input)
        } else {
            saved = try await api.createView(input)
        }
        views.removeAll { $0.id == saved.id }
        views.append(saved)
        views.sort { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        return saved
    }

    func delete(_ view: V2.View, api: SentinelAPI) async throws {
        try await api.deleteView(view.id)
        views.removeAll { $0.id == view.id }
    }

    /// Builds a view from an ordered camera list laid out row by row.
    static func input(name: String, columns: Int, cameraIDs: [String]) -> V2.ViewInput {
        let cols = min(max(columns, 1), 8)
        let rows = min(max(Int(ceil(Double(max(cameraIDs.count, 1)) / Double(cols))), 1), 8)
        let tiles = cameraIDs.prefix(cols * rows).enumerated().map { index, id in
            V2.ViewTile(cameraId: id, x: index % cols, y: index / cols, w: 1, h: 1)
        }
        return V2.ViewInput(name: name, columns: cols, rows: rows, tiles: Array(tiles))
    }
}
