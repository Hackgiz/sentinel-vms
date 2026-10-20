import SwiftUI
import SentinelCore

/// "Recover from camera" (Wave 11, stream A): one edge recovery at a time
/// per playback session. Its progress is read by the timeline bar's visible
/// interval while the job runs; when it ends the store posts a notice and,
/// when footage came back, re-reads the timelines. Texts and rules are in
/// `EdgeRecoveryLogic` (SentinelCore, tested).
@MainActor
final class EdgeRecoveryStore: ObservableObject {
    @Published private(set) var job: V2.EdgeRecovery?
    /// The camera the sheet is open for; nil = closed.
    @Published var sheetCameraID: String?
    @Published private(set) var isStarting = false
    @Published var error: String?

    var isRunning: Bool { EdgeRecoveryLogic.isActive(job) }

    // MARK: One per controller

    private struct Entry {
        weak var controller: PlaybackController?
        let store: EdgeRecoveryStore
    }
    private static var stores: [ObjectIdentifier: Entry] = [:]

    static func `for`(_ controller: PlaybackController) -> EdgeRecoveryStore {
        let key = ObjectIdentifier(controller)
        if let entry = stores[key], entry.controller === controller { return entry.store }
        stores = stores.filter { $0.value.controller != nil }
        let store = EdgeRecoveryStore()
        stores[key] = Entry(controller: controller, store: store)
        return store
    }

    // MARK: Job

    func open(cameraID: String) {
        error = nil
        if let job, job.cameraId != cameraID, isRunning == false { self.job = nil }
        sheetCameraID = cameraID
    }

    /// Forgets a finished job so the sheet offers a new range.
    func clearFinished() {
        if isRunning == false { job = nil }
    }

    func start(api: SentinelAPI, cameraID: String, range: TimelineGaps.Range, controller: PlaybackController) async {
        guard isStarting == false, isRunning == false else { return }
        if let problem = EdgeRecoveryLogic.problem(range, now: Date().unixMillis) {
            error = problem
            return
        }
        error = nil
        isStarting = true
        defer { isStarting = false }
        do {
            let started = try await api.startEdgeRecovery(cameraID: cameraID, from: range.start, to: range.end)
            job = started
            if EdgeRecoveryLogic.isActive(started) == false { await finished(started, controller: controller) }
        } catch {
            self.error = EdgeRecoveryLogic.startError(error)
        }
    }

    /// One read of the running job (the visible interval calls this).
    func refresh(api: SentinelAPI, controller: PlaybackController) async {
        guard let current = job, EdgeRecoveryLogic.isActive(current) else { return }
        do {
            let next = try await api.edgeRecovery(taskID: current.taskId)
            job = next
            if EdgeRecoveryLogic.isActive(next) == false { await finished(next, controller: controller) }
        } catch {
            self.error = error.localizedDescription
            if (error as? SentinelAPIError)?.isNotFound == true { job = nil } // expired or another server restarted
        }
    }

    func cancel(api: SentinelAPI, controller: PlaybackController) async {
        guard let current = job, EdgeRecoveryLogic.isActive(current) else { return }
        do {
            let next = try await api.cancelEdgeRecovery(taskID: current.taskId)
            job = next
            await finished(next, controller: controller)
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func finished(_ j: V2.EdgeRecovery, controller: PlaybackController) async {
        NoticeCenter.shared.post(EdgeRecoveryLogic.noticeKind(j), EdgeRecoveryLogic.text(j), key: "edge-recovery")
        if EdgeRecoveryLogic.shouldReload(j) {
            await controller.reloadTimelines(resettingLanes: controller.isLive == false)
        }
    }
}
