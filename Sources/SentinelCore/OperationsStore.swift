import CryptoKit
import Foundation
import SwiftUI

public struct IncidentRecord: Codable, Identifiable, Hashable {
    public let id: UUID
    public var title: String
    public var source: String
    public var status: String
    public var owner: String
    public var priority: String
    public var createdAt: Date
    public var updatedAt: Date
    public var linkedClips: [String]
    public var comments: [String]
    public var timeline: [String]

    public init(
        id: UUID = UUID(),
        title: String,
        source: String,
        status: String = "Open",
        owner: String = "Unassigned",
        priority: String = "Medium",
        createdAt: Date = Date(),
        updatedAt: Date = Date(),
        linkedClips: [String] = [],
        comments: [String] = [],
        timeline: [String] = []
    ) {
        self.id = id
        self.title = title
        self.source = source
        self.status = status
        self.owner = owner
        self.priority = priority
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.linkedClips = linkedClips
        self.comments = comments
        self.timeline = timeline
    }
}

@MainActor
public final class CaseworkStore: ObservableObject {
    @Published public private(set) var alerts: [AlertEvent] = []
    @Published public private(set) var incidents: [IncidentRecord] = []
    @Published public private(set) var evidenceClips: [EvidenceClip] = []
    @Published public private(set) var playbackBookmarks: [String] = []
    @Published public private(set) var lastAuditMessage: String?

    private let stateURL: URL
    private var nextIncidentSequence: Int = 1

    /// (area, action, detail, user) — wired by the app to WorkflowStore.recordAudit
    /// so operator decisions on alarms, incidents and evidence land in the audit
    /// log. `user` is nil for the signed-in Mac operator, or names a remote actor
    /// (e.g. a paired iPhone) so its actions aren't attributed to whoever is at the Mac.
    public var auditRecorder: ((String, String, String, String?) -> Void)?

    private struct CaseworkPersistenceState: Codable {
        var alerts: [AlertEvent]
        var incidents: [IncidentRecord]
        var evidenceClips: [EvidenceClip]
        var playbackBookmarks: [String]
    }

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)

        stateURL = supportDirectory.appendingPathComponent("casework-state.json")
        loadState()
    }

    public var activeAlerts: [AlertEvent] {
        sortedAlerts(alerts.filter(\.isOpen))
    }

    public func alerts(for state: AlertState?) -> [AlertEvent] {
        guard let state else {
            return activeAlerts
        }

        return sortedAlerts(alerts.filter { $0.alertState == state })
    }

    public func acknowledgeNewAlerts() {
        let newAlerts = alerts.filter { $0.alertState == .new }
        for alert in newAlerts {
            setAlertState(alert, to: .acknowledged, owner: "Current Operator", note: "Bulk acknowledged")
        }
    }

    public func createIncidentFromNewestAlert() {
        guard let alert = activeAlerts.first else {
            return
        }

        createIncident(from: alert)
    }

    public func createIncident(from alert: AlertEvent) {
        if let index = incidents.firstIndex(where: { $0.title == alert.title && $0.source == alert.source && $0.status != "Closed" }) {
            incidents[index].updatedAt = Date()
            incidents[index].timeline.insert("Duplicate incident request ignored", at: 0)
            setAlertState(alert, to: .investigating, owner: "Current Operator", note: "Existing incident opened")
            saveState()
            return
        }

        incidents.insert(
            IncidentRecord(
                title: alert.title,
                source: alert.source,
                status: "Open",
                owner: "Current Operator",
                priority: alert.severity == .critical ? "Critical" : "Medium",
                createdAt: Date(),
                updatedAt: Date(),
                linkedClips: alert.linkedClipPath.map { [URL(fileURLWithPath: $0).lastPathComponent] } ?? [],
                comments: [],
                timeline: ["\(alert.time) alert promoted to incident"]
            ),
            at: 0
        )

        setAlertState(alert, to: .investigating, owner: "Current Operator", note: "Promoted to incident")
        saveState()
        auditRecorder?("Incidents", "Created incident", "\(alert.title) · \(alert.source)", nil)
    }

    public func acknowledgeAlert(_ alert: AlertEvent) {
        setAlertState(alert, to: .acknowledged, owner: "Current Operator", note: "Acknowledged")
    }

    public func investigateAlert(_ alert: AlertEvent) {
        setAlertState(alert, to: .investigating, owner: "Current Operator", note: "Investigation started")
    }

    public func resolveAlert(_ alert: AlertEvent) {
        setAlertState(alert, to: .resolved, note: "Resolved")
    }

    public func markFalseAlarm(_ alert: AlertEvent) {
        setAlertState(alert, to: .falseAlarm, note: "Marked false alarm")
    }

    public func snoozeAlert(_ alert: AlertEvent, minutes: Int = 15) {
        guard let index = alerts.firstIndex(where: { $0.id == alert.id }) else {
            return
        }

        let until = Date().addingTimeInterval(Double(minutes) * 60)
        alerts[index].state = .snoozed
        alerts[index].owner = "Current Operator"
        alerts[index].snoozedUntil = until
        alerts[index].updatedAt = Date()
        alerts[index].responseLog.insert("Snoozed until \(RecordingFormatters.timeFormatter.string(from: until))", at: 0)
        saveState()
        auditRecorder?("Alarms", "Snoozed alarm \(minutes) min", "\(alert.title) · \(alert.source)", nil)
    }

    public func assignAlertToCurrentOperator(_ alert: AlertEvent) {
        guard let index = alerts.firstIndex(where: { $0.id == alert.id }) else {
            return
        }

        alerts[index].owner = "Current Operator"
        alerts[index].updatedAt = Date()
        alerts[index].responseLog.insert("Assigned to Current Operator", at: 0)
        saveState()
    }

    public func addResponseNote(to alert: AlertEvent, note: String) {
        let trimmedNote = note.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedNote.isEmpty == false,
              let index = alerts.firstIndex(where: { $0.id == alert.id }) else {
            return
        }

        alerts[index].updatedAt = Date()
        alerts[index].responseLog.insert(trimmedNote, at: 0)
        saveState()
    }

    public func syncOperationalAlarms(cameras: [CameraFeed], mediaIngestStore: MediaIngestProviding) {
        normalizeExpiredSnoozes()

        for camera in cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
            let streamError = mediaIngestStore.streamErrors[camera.id]
            let status = mediaIngestStore.effectiveStatus(for: camera)

            // Only alarm if GStreamer actually tried and failed — not just because no bridge is running
            let everConnected = mediaIngestStore.lastLiveFrameAt[camera.id] != nil
            let hadExplicitError = streamError != nil
            let shouldAlarmOffline = status == .offline && (everConnected || hadExplicitError)

            if shouldAlarmOffline {
                upsertSingletonAlert(
                    kind: .cameraOffline,
                    camera: camera,
                    title: "Camera offline",
                    severity: camera.isRecording ? .critical : .warning,
                    detail: streamError ?? "\(camera.name) stopped delivering live frames from \(camera.ipAddress).",
                    linkedClipPath: latestClipPath(for: camera.id, mediaIngestStore: mediaIngestStore)
                )
            } else {
                resolveOperationalAlert(kind: .cameraOffline, cameraID: camera.id, note: "Camera returned online")
            }

            // Only alarm for failed recording when the process actually crashed/exited with an error
            let recordingFailed = streamError?.localizedCaseInsensitiveContains("recording") == true ||
                                  streamError?.localizedCaseInsensitiveContains("ended") == true
            if camera.isRecording && mediaIngestStore.recordingSession(for: camera.id) == nil && recordingFailed {
                upsertSingletonAlert(
                    kind: .recording,
                    camera: camera,
                    title: "Recording stopped unexpectedly",
                    severity: .critical,
                    detail: streamError ?? "\(camera.name) recording process exited.",
                    linkedClipPath: latestClipPath(for: camera.id, mediaIngestStore: mediaIngestStore)
                )
            } else {
                resolveOperationalAlert(kind: .recording, cameraID: camera.id, note: "Recording process active")
            }

            let recentEvents = mediaIngestStore.motionEvents(for: camera.id).filter { event in
                Date().timeIntervalSince(event.timestamp) < 180
            }

            for event in recentEvents {
                upsertDetectionAlert(
                    event: event,
                    camera: camera,
                    linkedClipPath: clipPath(containing: event, mediaIngestStore: mediaIngestStore)
                )
            }
        }

        saveState()
    }

    public func resolveAllActiveAlerts() {
        let now = Date()
        for i in alerts.indices where alerts[i].isOpen {
            alerts[i].state = .resolved
            alerts[i].updatedAt = now
            alerts[i].responseLog.append("Bulk resolved")
        }
        saveState()
        auditRecorder?("Alarms", "Resolved all active alarms", "", nil)
    }

    public func clearResolvedAlerts() {
        alerts.removeAll { $0.isOpen == false }
        saveState()
    }

    public func assignIncident(_ incident: IncidentRecord, to owner: String) {
        guard let index = incidents.firstIndex(where: { $0.id == incident.id }) else {
            return
        }

        incidents[index].owner = owner
        incidents[index].updatedAt = Date()
        incidents[index].timeline.insert("Assigned to \(owner)", at: 0)
        saveState()
    }

    public func addComment(to incident: IncidentRecord, comment: String) {
        let trimmedComment = comment.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedComment.isEmpty == false,
              let index = incidents.firstIndex(where: { $0.id == incident.id }) else {
            return
        }

        incidents[index].comments.insert(trimmedComment, at: 0)
        incidents[index].updatedAt = Date()
        incidents[index].timeline.insert("Comment added", at: 0)
        saveState()
    }

    public func closeIncident(_ incident: IncidentRecord) {
        guard let index = incidents.firstIndex(where: { $0.id == incident.id }) else {
            return
        }

        incidents[index].status = "Closed"
        incidents[index].updatedAt = Date()
        incidents[index].timeline.insert("Incident closed", at: 0)
        saveState()
        auditRecorder?("Incidents", "Closed incident", incident.title, nil)
    }

    public func linkLatestClip(to incident: IncidentRecord) {
        guard let index = incidents.firstIndex(where: { $0.id == incident.id }),
              let clip = evidenceClips.first else {
            return
        }

        if incidents[index].linkedClips.contains(clip.caseID) == false {
            incidents[index].linkedClips.insert(clip.caseID, at: 0)
            incidents[index].updatedAt = Date()
            incidents[index].timeline.insert("Linked evidence \(clip.caseID)", at: 0)
            saveState()
        }
    }

    private func nextCaseID() -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd"
        let dateStr = formatter.string(from: Date())
        let seq = String(format: "%05d", nextIncidentSequence)
        nextIncidentSequence += 1
        return "HG-\(dateStr)-\(seq)"
    }

    @discardableResult
    public func createEvidencePackage(
        from segment: RecordingSegment?,
        cameraName: String? = nil,
        operator operatorName: String = "Current Operator",
        alertID: UUID? = nil,
        actor: String? = nil
    ) -> EvidenceClip? {
        guard let segment else { return nil }

        let caseID = nextCaseID()
        let clip = EvidenceClip(
            caseID: caseID,
            title: "Recorded Segment Export",
            camera: cameraName ?? segment.cameraID.uuidString.prefix(8).uppercased(),
            range: "\(segment.dateLabel) \(segment.timeLabel)",
            status: "Draft",
            filePath: segment.fileURL.path,
            sha256Hash: nil,
            exportedBy: operatorName,
            alertID: alertID
        )
        evidenceClips.insert(clip, at: 0)
        saveState()
        auditRecorder?("Evidence", "Created evidence package", "\(caseID) · \(clip.camera) · \(clip.range)", actor)

        // Hash off the main actor: a 15-minute segment can be hundreds of MB.
        let fileURL = segment.fileURL
        Task {
            let hash = await Task.detached(priority: .utility) { try? EvidenceVault.sha256(of: fileURL) }.value
            guard let hash, let index = evidenceClips.firstIndex(where: { $0.id == clip.id }),
                  evidenceClips[index].sha256Hash == nil else { return }
            evidenceClips[index].sha256Hash = hash
            saveState()
        }
        return clip
    }

    public func lockFirstDraftClip() {
        guard let clip = evidenceClips.first(where: { $0.status == "Draft" }) else {
            return
        }

        Task { await lockClip(clip) }
    }

    /// Locks a clip: preserves its footage in `EvidenceVault` (outside every
    /// retention path) and pins its SHA-256. Returns an error message on failure,
    /// in which case the clip stays unlocked so the operator can retry.
    @discardableResult
    public func lockClip(_ clip: EvidenceClip, operator operatorName: String = "Current Operator") async -> String? {
        guard let filePath = clip.filePath else { return "This clip has no recording file." }
        let source = URL(fileURLWithPath: filePath)
        let caseID = clip.caseID
        let knownHash = clip.sha256Hash

        let outcome: Result<(URL, String), Error> = await Task.detached(priority: .userInitiated) {
            Result {
                let protected = EvidenceVault.contains(source.path) ? source : try EvidenceVault.preserve(source, caseID: caseID)
                return (protected, try EvidenceVault.sha256(of: protected))
            }
        }.value

        guard let index = evidenceClips.firstIndex(where: { $0.id == clip.id }) else { return nil }
        switch outcome {
        case .failure(let error):
            lastAuditMessage = "Could not lock \(caseID): \(error.localizedDescription)"
            return lastAuditMessage
        case .success(let (protectedURL, hash)):
            // A hash recorded at package time that no longer matches means the
            // file changed in between (e.g. it was still being recorded). Pin the
            // locked bytes' hash and note the change for the chain of custody.
            if let knownHash, knownHash != hash {
                evidenceClips[index].title += " · Re-hashed at lock"
            }
            evidenceClips[index].filePath = protectedURL.path
            evidenceClips[index].sha256Hash = hash
            evidenceClips[index].status = "Locked"
            evidenceClips[index].exportedBy = operatorName
            evidenceClips[index].lockedAt = Date()
            evidenceClips[index].integrityVerifiedAt = Date()
            evidenceClips[index].integrityOK = true
            lastAuditMessage = "Evidence \(caseID) locked by \(operatorName) at \(RecordingFormatters.timeFormatter.string(from: Date()))"
            saveState()
            return nil
        }
    }

    /// Releases a lock after supervisor approval (see `OperatorSessionStore.authorize`).
    /// The footage stays preserved in the vault — unlocking reopens the clip for
    /// editing, it never deletes evidence — and re-locking re-hashes the file, so
    /// any change made while it was open is caught.
    public func unlockClip(_ clip: EvidenceClip) {
        guard let index = evidenceClips.firstIndex(where: { $0.id == clip.id }) else { return }
        evidenceClips[index].status = "Draft"
        evidenceClips[index].lockedAt = nil
        evidenceClips[index].integrityOK = nil
        evidenceClips[index].integrityVerifiedAt = nil
        lastAuditMessage = "Evidence \(clip.caseID) unlocked at \(RecordingFormatters.timeFormatter.string(from: Date()))"
        saveState()
    }

    /// Re-hashes the clip's file and compares against the pinned SHA-256.
    @discardableResult
    public func verifyIntegrity(of clip: EvidenceClip) async -> Bool {
        guard let filePath = clip.filePath, let expected = clip.sha256Hash else { return false }
        let url = URL(fileURLWithPath: filePath)
        let actual = await Task.detached(priority: .userInitiated) { try? EvidenceVault.sha256(of: url) }.value
        let ok = actual == expected
        if let index = evidenceClips.firstIndex(where: { $0.id == clip.id }) {
            evidenceClips[index].integrityVerifiedAt = Date()
            evidenceClips[index].integrityOK = ok
            saveState()
        }
        return ok
    }

    public func addReviewNoteToFirstClip() {
        guard let index = evidenceClips.firstIndex(where: { $0.status != "Exported" }) else {
            return
        }

        evidenceClips[index].title = "\(evidenceClips[index].title) · Note"
        saveState()
    }

    public func addReviewNote(to clip: EvidenceClip) {
        guard let index = evidenceClips.firstIndex(where: { $0.id == clip.id }) else {
            return
        }

        if evidenceClips[index].title.localizedCaseInsensitiveContains("note") == false {
            evidenceClips[index].title = "\(evidenceClips[index].title) · Note"
        }
        saveState()
    }

    public func exportFirstReadyClip() {
        guard let index = evidenceClips.firstIndex(where: { $0.status == "Locked" || $0.status == "Shared" || $0.status == "Draft" }) else {
            return
        }

        evidenceClips[index].status = "Exported"
        saveState()
    }

    public func exportClip(_ clip: EvidenceClip) {
        guard let index = evidenceClips.firstIndex(where: { $0.id == clip.id }) else {
            return
        }

        evidenceClips[index].status = "Exported"
        saveState()
    }

    public func bookmarkPlayback(segment: RecordingSegment?, cameraName: String?) {
        let cameraLabel = cameraName ?? "Selected camera"
        let bookmark: String

        if let segment {
            bookmark = "\(cameraLabel) · \(segment.dateLabel) \(segment.timeLabel)"
        } else {
            bookmark = "\(cameraLabel) · live review bookmark"
        }

        playbackBookmarks.insert(bookmark, at: 0)
        saveState()
    }

    public func bulkUpdateAlerts(_ ids: Set<UUID>, to state: AlertState) {
        for id in ids {
            guard let index = alerts.firstIndex(where: { $0.id == id }) else { continue }
            alerts[index].state = state
            alerts[index].updatedAt = Date()
            if state != .snoozed { alerts[index].snoozedUntil = nil }
        }
        saveState()
        auditRecorder?("Alarms", "Bulk set \(ids.count) alarm(s) to \(state.rawValue)", "", nil)
    }

    public func setAlertState(_ alert: AlertEvent, to state: AlertState, owner: String? = nil, note: String? = nil, actor: String? = nil) {
        guard let index = alerts.firstIndex(where: { $0.id == alert.id }) else {
            return
        }

        alerts[index].state = state
        alerts[index].updatedAt = Date()

        if let owner {
            alerts[index].owner = owner
        }

        if state != .snoozed {
            alerts[index].snoozedUntil = nil
        }

        if let note {
            alerts[index].responseLog.insert(note, at: 0)
        }

        saveState()
        auditRecorder?("Alarms", "Set alarm to \(state.rawValue)", "\(alert.title) · \(alert.source)", actor)
    }

    private func upsertSingletonAlert(
        kind: AlertKind,
        camera: CameraFeed,
        title: String,
        severity: AlertSeverity,
        detail: String,
        linkedClipPath: String?
    ) {
        if let index = alerts.firstIndex(where: { alert in
            alert.kind == kind &&
            alert.cameraID == camera.id &&
            alert.isOpen
        }) {
            guard alerts[index].isSnoozedNow == false else {
                return
            }

            alerts[index].severity = severity
            alerts[index].detail = detail
            alerts[index].updatedAt = Date()
            alerts[index].lastEventAt = Date()
            alerts[index].eventCount += 1
            if alerts[index].state == .resolved {
                alerts[index].state = .new
            }
            if alerts[index].linkedClipPath == nil {
                alerts[index].linkedClipPath = linkedClipPath
            }
            return
        }

        alerts.insert(
            AlertEvent(
                source: camera.name,
                title: title,
                severity: severity,
                kind: kind,
                cameraID: camera.id,
                cameraName: camera.name,
                detail: detail,
                linkedClipPath: linkedClipPath,
                responseLog: ["Automatically generated by stream supervisor"]
            ),
            at: 0
        )
    }

    private func upsertDetectionAlert(event: MotionEvent, camera: CameraFeed, linkedClipPath: String?) {
        if alerts.contains(where: { $0.eventID == event.id }) {
            return
        }

        let kind: AlertKind = event.kind == .person ? .person : .motion
        let title = event.kind == .person ? "Person detected" : "Motion detected"
        let severity: AlertSeverity = event.kind == .person ? .critical : .warning
        let detail = event.kind == .person
            ? "\(camera.name) detected a person at \(RecordingFormatters.timeFormatter.string(from: event.timestamp))."
            : "\(camera.name) detected motion at \(RecordingFormatters.timeFormatter.string(from: event.timestamp))."

        if let index = alerts.firstIndex(where: { alert in
            alert.kind == kind &&
            alert.cameraID == camera.id &&
            alert.isOpen &&
            event.timestamp.timeIntervalSince(alert.lastEventAt) >= 0 &&
            event.timestamp.timeIntervalSince(alert.lastEventAt) < 60
        }) {
            guard alerts[index].isSnoozedNow == false else {
                return
            }

            alerts[index].eventID = event.id
            alerts[index].lastEventAt = event.timestamp
            alerts[index].updatedAt = Date()
            alerts[index].eventCount += 1
            alerts[index].detail = "\(detail) Grouped with \(alerts[index].eventCount) related event\(alerts[index].eventCount == 1 ? "" : "s")."
            if alerts[index].linkedClipPath == nil {
                alerts[index].linkedClipPath = linkedClipPath
            }
            return
        }

        alerts.insert(
            AlertEvent(
                time: RecordingFormatters.timeFormatter.string(from: event.timestamp),
                source: camera.name,
                title: title,
                severity: severity,
                kind: kind,
                cameraID: camera.id,
                cameraName: camera.name,
                detail: detail,
                createdAt: event.timestamp,
                updatedAt: Date(),
                lastEventAt: event.timestamp,
                linkedClipPath: linkedClipPath,
                eventID: event.id,
                responseLog: ["Automatically generated by video analytics"]
            ),
            at: 0
        )
    }

    /// Raises (or clears) the single "recording disk low" alarm for the volume
    /// holding the recordings. Warning under 10% / 20 GB free, critical under
    /// 5% / 5 GB — at that point MediaMTX starts failing segment writes.
    public func syncStorageAlarm(recordingRootURL: URL) {
        let keys: Set<URLResourceKey> = [.volumeAvailableCapacityForImportantUsageKey, .volumeTotalCapacityKey, .volumeNameKey]
        guard let values = try? recordingRootURL.resourceValues(forKeys: keys),
              let free = values.volumeAvailableCapacityForImportantUsage,
              let total = values.volumeTotalCapacity, total > 0 else { return }

        let gb = Double(free) / 1_000_000_000
        let ratio = Double(free) / Double(total)
        let severity: AlertSeverity? = (ratio < 0.05 || gb < 5) ? .critical
            : (ratio < 0.10 || gb < 20) ? .warning
            : nil
        let volume = values.volumeName ?? "Recording disk"
        let openIndex = alerts.firstIndex { $0.kind == .storage && $0.cameraID == nil && $0.isOpen }

        guard let severity else {
            if let openIndex {
                alerts[openIndex].state = .resolved
                alerts[openIndex].updatedAt = Date()
                alerts[openIndex].responseLog.insert("Free space recovered (\(String(format: "%.0f", gb)) GB)", at: 0)
                saveState()
            }
            return
        }

        let detail = "\(volume) has \(String(format: "%.1f", gb)) GB free (\(Int(ratio * 100))%). Free space or shorten retention before recording stops."
        if let openIndex {
            // Only touch the alert when it escalates, so a steady low-disk state
            // doesn't bump the event count on every supervisor tick.
            guard severity.rank < alerts[openIndex].severity.rank else { return }
            alerts[openIndex].severity = severity
            alerts[openIndex].detail = detail
            alerts[openIndex].updatedAt = Date()
            alerts[openIndex].lastEventAt = Date()
            if alerts[openIndex].state != .snoozed { alerts[openIndex].state = .new }
        } else {
            alerts.insert(
                AlertEvent(
                    source: volume,
                    title: "Recording disk low",
                    severity: severity,
                    kind: .storage,
                    detail: detail,
                    responseLog: ["Automatically generated by storage monitor"]
                ),
                at: 0
            )
        }
        saveState()
    }

    private func resolveOperationalAlert(kind: AlertKind, cameraID: UUID, note: String) {
        for index in alerts.indices where alerts[index].kind == kind && alerts[index].cameraID == cameraID && alerts[index].isOpen {
            alerts[index].state = .resolved
            alerts[index].updatedAt = Date()
            alerts[index].responseLog.insert(note, at: 0)
        }
    }

    private func normalizeExpiredSnoozes() {
        for index in alerts.indices where alerts[index].alertState == .snoozed {
            if let snoozedUntil = alerts[index].snoozedUntil,
               snoozedUntil <= Date() {
                alerts[index].state = .acknowledged
                alerts[index].snoozedUntil = nil
                alerts[index].updatedAt = Date()
                alerts[index].responseLog.insert("Snooze expired", at: 0)
            }
        }
    }

    private func latestClipPath(for cameraID: UUID, mediaIngestStore: MediaIngestProviding) -> String? {
        mediaIngestStore.segments(for: cameraID).last?.fileURL.path
    }

    private func clipPath(containing event: MotionEvent, mediaIngestStore: MediaIngestProviding) -> String? {
        mediaIngestStore.segments(for: event.cameraID).first { segment in
            let start = min(segment.createdAt, segment.modifiedAt).addingTimeInterval(-5)
            let end = max(segment.createdAt, segment.modifiedAt).addingTimeInterval(10)
            return event.timestamp >= start && event.timestamp <= end
        }?.fileURL.path ?? latestClipPath(for: event.cameraID, mediaIngestStore: mediaIngestStore)
    }

    private func sortedAlerts(_ alerts: [AlertEvent]) -> [AlertEvent] {
        alerts.sorted { first, second in
            if first.severity.rank != second.severity.rank {
                return first.severity.rank < second.severity.rank
            }

            if first.alertState != second.alertState {
                return first.alertState.rawValue < second.alertState.rawValue
            }

            return first.lastEventAt > second.lastEventAt
        }
    }

    private func loadState() {
        let directory = stateURL.deletingLastPathComponent()
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        guard FileManager.default.fileExists(atPath: stateURL.path),
              let data = try? Data(contentsOf: stateURL),
              let state = try? JSONDecoder().decode(CaseworkPersistenceState.self, from: data) else {
            saveState()
            return
        }

        alerts = state.alerts
        incidents = state.incidents
        evidenceClips = state.evidenceClips
        playbackBookmarks = state.playbackBookmarks

        // Derive next sequence from highest existing case ID
        let existingNumbers = evidenceClips.compactMap { clip -> Int? in
            guard clip.caseID.hasPrefix("HG-") else { return nil }
            return Int(clip.caseID.dropFirst(3))
        }
        nextIncidentSequence = (existingNumbers.max() ?? 0) + 1

        cleanupLegacyDemoState()
    }

    private func cleanupLegacyDemoState() {
        let originalAlerts = alerts.count
        let originalIncidents = incidents.count
        let originalClips = evidenceClips.count

        alerts.removeAll { alert in
            LegacyRuntimeData.demoCameraNames.contains(alert.source)
        }

        incidents.removeAll { incident in
            LegacyRuntimeData.demoCameraNames.contains(incident.source) ||
            incident.linkedClips.contains { LegacyRuntimeData.demoCaseIDs.contains($0) }
        }

        evidenceClips.removeAll { clip in
            LegacyRuntimeData.demoCaseIDs.contains(clip.caseID) ||
            LegacyRuntimeData.demoCameraNames.contains(clip.camera)
        }

        if alerts.count != originalAlerts ||
            incidents.count != originalIncidents ||
            evidenceClips.count != originalClips {
            saveState()
        }
    }

    private func saveState() {
        do {
            let directory = stateURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let data = try JSONEncoder.prettySentinel.encode(
                CaseworkPersistenceState(
                    alerts: alerts,
                    incidents: incidents,
                    evidenceClips: evidenceClips,
                    playbackBookmarks: playbackBookmarks
                )
            )
            try data.write(to: stateURL, options: .atomic)
        } catch {
            // Keep the UI responsive even if persistence fails.
        }
    }
}

@MainActor
public final class UserDirectoryStore: ObservableObject {
    @Published public private(set) var users: [UserAccount] = []
    private let usersURL: URL

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)

        usersURL = supportDirectory.appendingPathComponent("users.json")
        loadUsers()
    }

    public func setPin(_ pin: String, for userID: UUID) {
        guard let index = users.firstIndex(where: { $0.id == userID }) else { return }
        users[index].passwordHash = pin.isEmpty ? nil : OperatorSessionStore.pinHash(for: pin)
        saveUsers()
        // Keep the SecretVault key slots in step with passwords. Requires the
        // vault to be unlocked (the admin/operator making the change is signed
        // in); on a fresh install the subsequent login() creates the vault.
        if pin.isEmpty {
            SecretVault.shared.removeSlot(userKey: userID.uuidString)
        } else {
            SecretVault.shared.provisionSlot(userKey: userID.uuidString, password: pin)
        }
    }

    public func markSeen(_ user: UserAccount) {
        guard let index = users.firstIndex(where: { $0.id == user.id }) else { return }
        let formatter = DateFormatter()
        formatter.timeStyle = .short
        formatter.dateStyle = .short
        users[index].lastSeen = formatter.string(from: Date())
        users[index].status = "Active"
        saveUsers()
    }

    /// First-run setup: create the initial administrator with a password.
    /// Returns the created account so the caller can sign in immediately.
    /// No-op (returns nil) if any users already exist — there's only ever one
    /// "first" admin.
    @discardableResult
    public func createAdministrator(name: String, password: String) -> UserAccount? {
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedName.isEmpty == false, users.isEmpty else { return nil }
        let admin = UserAccount(
            name: trimmedName,
            role: "Admin",
            status: "Active",
            lastSeen: "Never",
            passwordHash: password.isEmpty ? nil : OperatorSessionStore.pinHash(for: password)
        )
        users.insert(admin, at: 0)
        saveUsers()
        return admin
    }

    public func invite(name: String, role: String) {
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedName.isEmpty == false else {
            return
        }

        users.insert(
            UserAccount(
                name: trimmedName,
                role: role,
                status: "Invited",
                lastSeen: "Never"
            ),
            at: 0
        )
        saveUsers()
    }

    private func loadUsers() {
        let directory = usersURL.deletingLastPathComponent()
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        guard FileManager.default.fileExists(atPath: usersURL.path),
              let data = try? Data(contentsOf: usersURL),
              let loaded = try? JSONDecoder().decode([UserAccount].self, from: data) else {
            saveUsers()
            return
        }
        users = loaded
        cleanupLegacyUsers()
    }

    private func cleanupLegacyUsers() {
        let originalCount = users.count
        users.removeAll { LegacyRuntimeData.demoPeople.contains($0.name) }
        if users.count != originalCount {
            saveUsers()
        }
    }

    private func saveUsers() {
        do {
            let directory = usersURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let data = try JSONEncoder.prettySentinel.encode(users)
            try data.write(to: usersURL, options: .atomic)
        } catch {
            // User management stays usable even if the local cache cannot write.
        }
    }
}

@MainActor
public final class FloorplanStore: ObservableObject {
    @Published public var importedFloorplanURL: URL?
    @Published public var pinPositions: [UUID: CGPoint] = [:]
    private let floorplanURL: URL
    private var pinPositionsURL: URL { floorplanURL.deletingLastPathComponent().appendingPathComponent("pin-positions.json") }

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)

        floorplanURL = supportDirectory.appendingPathComponent("imported-floorplan")
        loadFloorplan()
        loadPinPositions()
    }

    public func updatePinPosition(for cameraID: UUID, x: Double, y: Double) {
        pinPositions[cameraID] = CGPoint(x: x, y: y)
        savePinPositions()
    }

    private func loadPinPositions() {
        guard let data = try? Data(contentsOf: pinPositionsURL),
              let decoded = try? JSONDecoder().decode([String: [Double]].self, from: data) else { return }
        pinPositions = Dictionary(uniqueKeysWithValues: decoded.compactMap { key, val in
            guard let id = UUID(uuidString: key), val.count >= 2 else { return nil }
            return (id, CGPoint(x: val[0], y: val[1]))
        })
    }

    private func savePinPositions() {
        let encodable = Dictionary(uniqueKeysWithValues: pinPositions.map { ($0.key.uuidString, [$0.value.x, $0.value.y]) })
        if let data = try? JSONEncoder().encode(encodable) {
            try? data.write(to: pinPositionsURL)
        }
    }

    public func importFloorplan(from sourceURL: URL) {
        do {
            let directory = floorplanURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let destination = floorplanURL.appendingPathExtension(sourceURL.pathExtension.isEmpty ? "dat" : sourceURL.pathExtension)

            let existingFiles = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            for existing in existingFiles where existing.deletingPathExtension().lastPathComponent == "imported-floorplan" {
                try FileManager.default.removeItem(at: existing)
            }

            let didStartAccess = sourceURL.startAccessingSecurityScopedResource()
            defer {
                if didStartAccess {
                    sourceURL.stopAccessingSecurityScopedResource()
                }
            }

            try FileManager.default.copyItem(at: sourceURL, to: destination)
            importedFloorplanURL = destination
        } catch {
            importedFloorplanURL = sourceURL
        }
    }

    private func loadFloorplan() {
        let directory = floorplanURL.deletingLastPathComponent()
        guard let files = try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil),
              let existing = files.first(where: { $0.deletingPathExtension().lastPathComponent == "imported-floorplan" }) else {
            return
        }

        importedFloorplanURL = existing
    }
}

public enum OperatorPermission {
    case addCamera
    case deleteCamera
    case changeSettings
    case exportEvidence
    case manageUsers
    case viewAuditLog
    case acknowledgeAlerts
    case playback
}

/// Actions that need a supervisor's sign-off (or a reason on record) before they run.
public enum SensitiveAction: String, Sendable {
    case unlockEvidence = "Unlock evidence"
    case changeRetention = "Change retention"
    case applyRetention = "Delete recordings past retention"

    /// Unlocking evidence must say why — the reason lands in the audit log.
    public var requiresReason: Bool { self == .unlockEvidence }
}

public enum ApprovalResult: Equatable {
    case approved(approver: String)
    case denied(message: String)
}

@MainActor
public final class OperatorSessionStore: ObservableObject {
    @Published public private(set) var currentOperator: UserAccount?
    @Published public private(set) var loginError: String?
    @Published public private(set) var guestMode = false
    @Published public private(set) var failedLoginAttempts: Int = 0
    @Published public private(set) var lockedUntil: Date? = nil

    // Wired by SentinelAppDependencies after init: lets us push login/logout
    // events into the audit log without OperatorSessionStore needing a hard
    // dependency on WorkflowStore. `recordAudit(user, action, detail)`.
    public var auditRecorder: ((String, String, String) -> Void)?

    @Published public private(set) var approvalLockedUntil: Date?
    private var approvalFailures = 0

    public init() {}

    public var isLoggedIn: Bool { currentOperator != nil || guestMode }

    public func can(_ permission: OperatorPermission) -> Bool {
        let role = currentOperator?.role ?? (guestMode ? "Viewer" : "Admin")
        switch permission {
        case .addCamera, .deleteCamera, .changeSettings, .manageUsers:
            return role == "Admin"
        case .viewAuditLog:
            return role == "Admin" || role == "Supervisor"
        case .exportEvidence, .acknowledgeAlerts:
            return role != "Viewer"
        case .playback:
            return true
        }
    }

    private func isSupervisorRole(_ role: String) -> Bool { role == "Admin" || role == "Supervisor" }

    /// Admins and Supervisors already hold authority over retention, so they
    /// aren't stopped for it; everyone else (Operator, Viewer, guest) needs a
    /// supervisor's PIN. Unlocking evidence always needs sign-off + a reason.
    public func needsApproval(_ action: SensitiveAction) -> Bool {
        switch action {
        case .unlockEvidence:
            return true
        case .changeRetention, .applyRetention:
            return isSupervisorRole(roleLabel) == false
        }
    }

    /// Supervisors/Admins who can approve with a PIN.
    public func approvers(in users: [UserAccount]) -> [UserAccount] {
        users.filter { isSupervisorRole($0.role) && $0.hasPin }
    }

    /// A signed-in Admin/Supervisor with no PIN (single-operator shop) can
    /// confirm for themselves — otherwise they'd be locked out of their own
    /// system. The reason and their name still go to the audit log.
    public var canSelfConfirmWithoutPin: Bool {
        guard let op = currentOperator else { return false }
        return isSupervisorRole(op.role) && op.hasPin == false
    }

    /// Verifies an approval and records it. `approver` is the supervisor whose
    /// PIN was entered (nil only for the no-PIN self-confirm case).
    public func authorize(
        _ action: SensitiveAction,
        approver: UserAccount?,
        pin: String,
        reason: String,
        detail: String
    ) -> ApprovalResult {
        let requester = currentOperator?.name ?? (guestMode ? "Guest" : "Unknown")
        let trimmedReason = reason.trimmingCharacters(in: .whitespacesAndNewlines)

        if let until = approvalLockedUntil, Date() < until {
            return .denied(message: "Too many failed approvals. Try again at \(RecordingFormatters.timeFormatter.string(from: until)).")
        }
        if action.requiresReason, trimmedReason.isEmpty {
            return .denied(message: "Enter a reason — it's recorded in the audit log.")
        }

        let approverName: String
        if let approver {
            guard isSupervisorRole(approver.role), let hash = approver.passwordHash else {
                return .denied(message: "\(approver.name) can't approve this action.")
            }
            let entered = OperatorSessionStore.pinHash(for: pin)
            guard PairingTokenStore.constantTimeEqual(entered, hash) else {
                approvalFailures += 1
                auditRecorder?(requester, "Approval denied: \(action.rawValue)", "Wrong PIN for \(approver.name). \(detail)")
                if approvalFailures >= 3 {
                    approvalLockedUntil = Date().addingTimeInterval(5 * 60)
                    approvalFailures = 0
                    return .denied(message: "Too many failed approvals. Locked for 5 minutes.")
                }
                return .denied(message: "Incorrect PIN.")
            }
            approverName = approver.name
        } else if canSelfConfirmWithoutPin, let name = currentOperator?.name {
            approverName = name
        } else {
            return .denied(message: "Choose a supervisor or admin to approve.")
        }

        approvalFailures = 0
        approvalLockedUntil = nil
        let who = approverName == requester ? "self-approved" : "approved by \(approverName)"
        auditRecorder?(requester, "Approval granted: \(action.rawValue)", "\(detail) — \(who)\(trimmedReason.isEmpty ? "" : ". Reason: \(trimmedReason)")")
        return .approved(approver: approverName)
    }

    public var roleLabel: String {
        currentOperator?.role ?? (guestMode ? "Viewer" : "Admin")
    }

    public func login(_ user: UserAccount, pin: String) -> Bool {
        loginError = nil
        if let lockedUntil, Date() < lockedUntil {
            loginError = "Account locked. Try again at \(RecordingFormatters.timeFormatter.string(from: lockedUntil))."
            return false
        }
        if let hash = user.passwordHash {
            let entered = SHA256.hash(data: Data(pin.utf8))
                .compactMap { String(format: "%02x", $0) }.joined()
            guard entered == hash else {
                failedLoginAttempts += 1
                if failedLoginAttempts >= 3 {
                    lockedUntil = Date().addingTimeInterval(15 * 60) // 15 min lockout
                    loginError = "Too many failed attempts. Account locked for 15 minutes."
                } else {
                    loginError = "Incorrect PIN. \(3 - failedLoginAttempts) attempt\(3 - failedLoginAttempts == 1 ? "" : "s") remaining."
                }
                return false
            }
        }
        failedLoginAttempts = 0
        lockedUntil = nil
        currentOperator = user
        guestMode = false
        // This password is also the single key to every local secret: unlocking
        // (or first-run creating) the SecretVault here is what lets the app drop
        // the separate macOS Keychain prompt and Touch ID credential gate.
        SecretVault.shared.unlock(userKey: user.id.uuidString, password: pin)
        auditRecorder?(user.name, "Logged in", "Role: \(user.role)")
        return true
    }

    public func loginWithoutPin(_ user: UserAccount) {
        guard user.hasPin == false else { return }
        loginError = nil
        currentOperator = user
        guestMode = false
        auditRecorder?(user.name, "Logged in (no PIN)", "Role: \(user.role)")
    }

    public func continueAsGuest() {
        currentOperator = nil
        loginError = nil
        guestMode = true
        auditRecorder?("Guest", "Continued as guest", "Read-only session")
    }

    public func logout() {
        let previousName = currentOperator?.name ?? (guestMode ? "Guest" : "Unknown")
        currentOperator = nil
        loginError = nil
        guestMode = false
        // Clear decrypted secrets from memory so the next operator must sign in
        // with their own password to reach camera/cloud credentials.
        SecretVault.shared.lockVault()
        auditRecorder?(previousName, "Logged out", "")
    }

    public static func pinHash(for pin: String) -> String {
        SHA256.hash(data: Data(pin.utf8))
            .compactMap { String(format: "%02x", $0) }.joined()
    }
}
