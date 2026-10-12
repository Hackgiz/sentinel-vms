// AppStore.swift
// Shared, app-wide data + navigation state so Home, Live, and Events stay in
// sync off one source: cameras, events and alarms over API v2, kept current by
// the v2 WebSocket event stream (a slow poll covers the gaps while it is down).
// Also routes a tapped push notification to the right place.

import SwiftUI
import Combine

enum AppTab: Hashable { case home, live, events, settings }

/// The Events tab shows either the alarm queue or the AI detection feed.
enum EventsMode: Hashable { case alarms, detections }

@MainActor
final class AppStore: ObservableObject {
    static let shared = AppStore()

    @Published var cameras: [V2.Camera] = []
    @Published var events: [V2.Event] = []
    @Published var alarms: [V2.Alarm] = []
    /// Evidence locked from this phone this session, by alarm id.
    @Published var evidence: [String: V2.Evidence] = [:]
    @Published var lastError: String?
    @Published var isLoading = false
    /// True once the first load has finished (so "no cameras" isn't shown while loading).
    @Published private(set) var hasLoaded = false
    @Published private(set) var streamState: SentinelEventStream.State = .idle

    // Navigation / deep-link state.
    @Published var selectedTab: AppTab = .home
    @Published var eventsMode: EventsMode = .detections
    @Published var routedCameraID: String?

    /// Alarms with an action in flight (spinner on their buttons).
    @Published var busyAlarmIDs: Set<String> = []
    /// Last alarm-action failure, shown as a dismissible banner.
    @Published var actionError: String?
    /// Dev/screenshot only: hide the in-app "Demo Mode" banner for clean captures.
    @Published var suppressDemoBanner = false

    private var stream: SentinelEventStream?
    private var streamTask: Task<Void, Never>?
    private var pollTimer: Timer?
    private var eventsCursor: String?
    private(set) var hasMoreEvents = false
    private var session: SentinelSession { SentinelSession.shared }

    static let maxEvents = 300

    // MARK: Derived

    var onlineCount: Int { cameras.filter { $0.status.online }.count }
    var recordingCount: Int { cameras.filter { $0.recording && $0.status.online }.count }
    var newAlertCount: Int { alarms.filter { $0.state == .new }.count }

    func camera(id: String) -> V2.Camera? { cameras.first { $0.id == id } }

    func cameraName(forID id: String?) -> String? {
        id.flatMap { camera(id: $0)?.name }
    }

    func cameraName(for alarm: V2.Alarm) -> String? {
        if let name = alarm.cameraName, name.isEmpty == false { return name }
        return cameraName(forID: alarm.cameraId)
    }

    /// Alarms ordered for triage: needs-attention first, then severity, newest first.
    var sortedAlarms: [V2.Alarm] {
        func rank(_ severity: V2.Severity) -> Int { [V2.Severity.critical: 0, .warning: 1][severity] ?? 2 }
        return alarms.sorted {
            if $0.needsAttention != $1.needsAttention { return $0.needsAttention }
            if rank($0.severity) != rank($1.severity) { return rank($0.severity) < rank($1.severity) }
            return $0.lastEventDate > $1.lastEventDate
        }
    }

    func openAlarms() {
        eventsMode = .alarms
        selectedTab = .events
    }

    /// Routes from a tapped push notification (or anywhere) to a camera's live view.
    func route(toCameraID id: String) {
        routedCameraID = id
        selectedTab = .live
    }

    // MARK: Alarm actions

    func setState(_ alarm: V2.Alarm, to state: V2.AlarmState, note: String? = nil, snoozeMinutes: Int? = nil) async {
        await runAction(on: alarm) {
            let trimmed = note?.trimmingCharacters(in: .whitespacesAndNewlines)
            let change = V2.AlarmStateChange(state: state, note: trimmed?.isEmpty == false ? trimmed : nil,
                                             snoozeMinutes: state == .snoozed ? (snoozeMinutes ?? 15) : nil)
            let updated: V2.Alarm
            if let api = self.session.api, self.session.isDemoMode == false {
                updated = try await api.setAlarmState(alarm.id, change)
            } else {
                updated = DemoData.apply(change, to: alarm)
            }
            self.apply(alarm: updated)
        }
    }

    func acknowledge(_ alarm: V2.Alarm) async {
        await setState(alarm, to: .acknowledged)
    }

    func lockEvidence(_ alarm: V2.Alarm) async {
        await runAction(on: alarm) {
            let clip: V2.Evidence
            if let api = self.session.api, self.session.isDemoMode == false {
                clip = try await api.lockEvidence(alarmID: alarm.id)
            } else {
                clip = DemoData.evidence(for: alarm)
            }
            self.evidence[alarm.id] = clip
        }
    }

    private func runAction(on alarm: V2.Alarm, _ work: () async throws -> Void) async {
        guard busyAlarmIDs.contains(alarm.id) == false else { return }
        busyAlarmIDs.insert(alarm.id)
        defer { busyAlarmIDs.remove(alarm.id) }
        do {
            try await work()
            actionError = nil
            Haptics.success()
        } catch {
            actionError = error.localizedDescription
            Haptics.warning()
        }
    }

    /// Puts a changed alarm into the list; a closed one leaves the open queue.
    func apply(alarm: V2.Alarm) {
        if let i = alarms.firstIndex(where: { $0.id == alarm.id }) {
            if alarm.isOpen { alarms[i] = alarm } else { alarms.remove(at: i) }
        } else if alarm.isOpen {
            alarms.insert(alarm, at: 0)
        }
    }

    // MARK: Loading

    func refreshAll() async {
        if session.isDemoMode {
            cameras = DemoData.cameras
            alarms = DemoData.openAlarms
            events = DemoData.events
            hasLoaded = true
            return
        }
        guard let api = session.api, session.isSignedIn else { return }
        isLoading = true
        defer { isLoading = false }
        async let c = try? await api.cameras()
        async let a = try? await api.alarms(openOnly: true, limit: 200)
        async let e = try? await api.events(limit: 100)
        let (cams, als, evs) = await (c, a, e)
        if let cams { cameras = cams; lastError = nil }
        else if cameras.isEmpty {
            lastError = (try? await api.me()) == nil ? "Couldn't reach the Management Server." : "Couldn't load the cameras."
        }
        if let als { alarms = als.items }
        if let evs {
            events = evs.items.sorted { $0.createdAt > $1.createdAt }
            eventsCursor = evs.nextCursor
            hasMoreEvents = evs.nextCursor != nil
        }
        hasLoaded = true
        session.refreshConnection()
    }

    /// Next page of detections, when the list scrolls to its end.
    func loadMoreEvents() async {
        guard let api = session.api, session.isDemoMode == false,
              let cursor = eventsCursor, events.count < Self.maxEvents else { return }
        eventsCursor = nil
        if let page = try? await api.events(cursor: cursor, limit: 100) {
            let known = Set(events.map(\.id))
            events += page.items.filter { known.contains($0.id) == false }
            eventsCursor = page.nextCursor
            hasMoreEvents = page.nextCursor != nil
        }
    }

    // MARK: Live updates

    /// Starts the event stream and the safety poll for the current site.
    func startAuto() {
        stopAuto()
        Task { await refreshAll() }
        guard session.isDemoMode == false, let api = session.api else { return }
        let stream = SentinelEventStream(api: api)
        self.stream = stream
        let updates = stream.start()
        streamTask = Task { [weak self] in
            for await update in updates {
                guard Task.isCancelled == false else { return }
                await self?.handle(update)
            }
        }
        // While the stream is down, poll like the app used to.
        pollTimer = Timer.scheduledTimer(withTimeInterval: 15, repeats: true) { [weak self] _ in
            Task { @MainActor in
                guard let self, self.streamState.isConnected == false else { return }
                await self.refreshAll()
            }
        }
    }

    func stopAuto() {
        stream?.stop()
        stream = nil
        streamTask?.cancel()
        streamTask = nil
        pollTimer?.invalidate()
        pollTimer = nil
        streamState = .idle
    }

    /// Forgets everything (site switch, sign-out).
    func reset() {
        stopAuto()
        cameras = []
        events = []
        alarms = []
        evidence = [:]
        lastError = nil
        hasLoaded = false
        eventsCursor = nil
        hasMoreEvents = false
        routedCameraID = nil
    }

    func handle(_ update: SentinelEventStream.Update) async {
        switch update {
        case .state(let state):
            streamState = state
            if case .stopped = state { session.refreshConnection() }
        case .resync:
            await refreshAll()
        case .message(let message, _):
            apply(message)
        }
    }

    func apply(_ message: V2.StreamMessage) {
        switch message {
        case .alarmNew(let alarm), .alarmUpdated(let alarm):
            apply(alarm: alarm)
        case .eventNew(let event), .eventUpdated(let event):
            if let i = events.firstIndex(where: { $0.id == event.id }) {
                events[i] = event
            } else {
                events.insert(event, at: 0)
                if events.count > Self.maxEvents { events.removeLast(events.count - Self.maxEvents) }
            }
        case .cameraStatus(let change):
            if let i = cameras.firstIndex(where: { $0.id == change.cameraId }) {
                cameras[i].status.online = change.online
                cameras[i].status.message = change.online ? "Online" : "No signal"
            }
        case .configChanged:
            Task { await refreshCameras() }
        case .recorderStatus:
            Task { await refreshCameras() }
        case .hello, .cameraActivity, .lagged, .unknown:
            break
        }
    }

    private func refreshCameras() async {
        guard let api = session.api, session.isDemoMode == false else { return }
        if let cams = try? await api.cameras() { cameras = cams }
    }
}

extension SentinelEventStream.State {
    var isConnected: Bool {
        if case .connected = self { return true }
        return false
    }
}

// MARK: - Alarm helpers

extension V2.Alarm {
    var createdAtDate: Date { Date(unixMillis: createdAt) }
    var lastEventDate: Date { Date(unixMillis: lastEventAt) }
    var needsAttention: Bool { state == .new }
    var canAcknowledge: Bool { state == .new || state == .snoozed }
    /// "Unassigned" is the server's placeholder, not a person.
    var assignee: String? {
        guard owner.isEmpty == false, owner != "Unassigned", owner != "Current Operator" else { return nil }
        return owner
    }
}

extension V2.Event {
    var createdAtDate: Date { Date(unixMillis: createdAt) }
}

extension V2.Camera {
    var isOnline: Bool { status.online }
    /// "online" / "offline" for the status badge.
    var statusLabel: String { status.online ? "online" : "offline" }
}
