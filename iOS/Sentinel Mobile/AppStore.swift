// AppStore.swift
// Shared, app-wide data + navigation state so Home, Live, and Events stay in
// sync off a single refresh loop (instead of three independent timers), and so
// a tapped push notification can route to the right place.

import SwiftUI
import Combine

enum AppTab: Hashable { case home, live, events, settings }

/// The Events tab shows either the Mac's alarm queue or the AI detection feed.
enum EventsMode: Hashable { case alarms, detections }

@MainActor
final class AppStore: ObservableObject {
    static let shared = AppStore()

    @Published var cameras: [SentinelCameraSummary] = []
    @Published var events: [SentinelEvent] = []
    @Published var alerts: [SentinelAlert] = []
    @Published var lastError: String?
    @Published var isLoading = false

    // Navigation / deep-link state.
    @Published var selectedTab: AppTab = .home
    @Published var eventsMode: EventsMode = .detections
    @Published var routedCameraID: UUID?

    /// Alarms with an action in flight (spinner on their buttons).
    @Published var busyAlarmIDs: Set<UUID> = []
    /// Last alarm-action failure, shown as a dismissible banner.
    @Published var actionError: String?
    /// Dev/screenshot only: hide the in-app "Demo Mode" banner for clean captures.
    @Published var suppressDemoBanner = false

    private var timer: Timer?

    var onlineCount: Int { cameras.filter { $0.status != "offline" }.count }
    var recordingCount: Int { cameras.filter { $0.isRecording }.count }
    var newAlertCount: Int { alerts.filter { $0.state == "New" }.count }

    func camera(id: UUID) -> SentinelCameraSummary? { cameras.first { $0.id == id } }

    /// Alarms ordered for triage: needs-attention first, then severity, newest first.
    var sortedAlarms: [SentinelAlert] {
        func rank(_ severity: String) -> Int { ["Critical": 0, "Warning": 1][severity] ?? 2 }
        return alerts.sorted {
            if $0.needsAttention != $1.needsAttention { return $0.needsAttention }
            if rank($0.severity) != rank($1.severity) { return rank($0.severity) < rank($1.severity) }
            return $0.lastEventDate > $1.lastEventDate
        }
    }

    func cameraName(for alert: SentinelAlert) -> String? {
        alert.cameraName ?? alert.cameraID.flatMap { camera(id: $0)?.name }
    }

    func openAlarms() {
        eventsMode = .alarms
        selectedTab = .events
    }

    func acknowledge(_ alert: SentinelAlert) async {
        await runAction(on: alert) {
            let updated = try await SentinelSession.shared.acknowledge(alertID: alert.id)
            self.replace(updated)
        }
    }

    func lockEvidence(_ alert: SentinelAlert) async {
        await runAction(on: alert) {
            let clip = try await SentinelSession.shared.lockEvidence(alertID: alert.id)
            if let i = self.alerts.firstIndex(where: { $0.id == alert.id }) {
                self.alerts[i].evidence = clip
            }
        }
    }

    private func runAction(on alert: SentinelAlert, _ work: () async throws -> Void) async {
        guard busyAlarmIDs.contains(alert.id) == false else { return }
        busyAlarmIDs.insert(alert.id)
        defer { busyAlarmIDs.remove(alert.id) }
        do {
            try await work()
            actionError = nil
            Haptics.success()
        } catch {
            actionError = error.localizedDescription
            Haptics.warning()
        }
    }

    private func replace(_ alert: SentinelAlert) {
        if let i = alerts.firstIndex(where: { $0.id == alert.id }) { alerts[i] = alert }
    }

    func refreshAll() async {
        isLoading = true
        defer { isLoading = false }
        async let c = try? await SentinelSession.shared.cameras()
        async let e = try? await SentinelSession.shared.events()
        async let a = try? await SentinelSession.shared.alerts()
        let (cams, evs, als) = await (c, e, a)
        if let cams { cameras = cams; lastError = nil }
        else if cameras.isEmpty { lastError = "Couldn't reach Sentinel" }
        if let evs { events = evs.sorted { $0.createdAt > $1.createdAt } }
        if let als { alerts = als.sorted { $0.createdAt > $1.createdAt } }
    }

    func startAuto(interval: TimeInterval = 8) {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.refreshAll() }
        }
        Task { await refreshAll() }
    }

    func stopAuto() {
        timer?.invalidate()
        timer = nil
    }

    /// Routes from a tapped push notification (or anywhere) to a camera's live view.
    func route(toCameraID id: UUID) {
        routedCameraID = id
        selectedTab = .live
    }
}
