import CryptoKit
import Foundation
@preconcurrency import UserNotifications

public struct PersonalCameraView: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var name: String
    public var owner: String
    public var site: String
    public var cameraNames: [String]
    public var gridColumns: Int
    public var quality: String
    public var isDefault: Bool
    public var updatedAt: Date
}

public struct ShiftHandoffRecord: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var fromOperator: String
    public var toOperator: String
    public var summary: String
    public var unresolvedItems: [String]
    public var offlineCameras: [String]
    public var status: String
    public var createdAt: Date
    public var acknowledgedAt: Date?
}

public struct NotificationRule: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var name: String
    public var trigger: String
    public var delivery: String
    public var severity: String
    public var isEnabled: Bool
    public var isSnoozed: Bool
    /// Cameras this rule watches; nil or empty = every camera. Alerts with no
    /// camera (storage, system) always pass the camera filter.
    public var cameraIDs: [UUID]?
    /// Active window in local hours [start, end); nil = always. Wraps past
    /// midnight when end < start (e.g. 22 → 6 for "after hours").
    public var activeFromHour: Int?
    public var activeToHour: Int?
    /// What the operator should do when this rule fires — shown on the alarm.
    public var instructions: String?

    public init(
        id: UUID = UUID(),
        name: String,
        trigger: String,
        delivery: String,
        severity: String,
        isEnabled: Bool,
        isSnoozed: Bool,
        cameraIDs: [UUID]? = nil,
        activeFromHour: Int? = nil,
        activeToHour: Int? = nil,
        instructions: String? = nil
    ) {
        self.id = id
        self.name = name
        self.trigger = trigger
        self.delivery = delivery
        self.severity = severity
        self.isEnabled = isEnabled
        self.isSnoozed = isSnoozed
        self.cameraIDs = cameraIDs
        self.activeFromHour = activeFromHour
        self.activeToHour = activeToHour
        self.instructions = instructions
    }

    public var minimumSeverity: AlertSeverity {
        AlertSeverity(rawValue: severity) ?? .info
    }

    public var matchesDesktopDelivery: Bool {
        delivery.localizedCaseInsensitiveContains("Desktop")
    }

    public var hasSchedule: Bool { activeFromHour != nil && activeToHour != nil }

    public func isActive(at date: Date) -> Bool {
        guard let from = activeFromHour, let to = activeToHour, from != to else { return true }
        let hour = Calendar.current.component(.hour, from: date)
        return from < to ? (hour >= from && hour < to) : (hour >= from || hour < to)
    }

    public func matches(alert: AlertEvent) -> Bool {
        guard isEnabled, isSnoozed == false, isActive(at: alert.lastEventAt) else {
            return false
        }

        if let cameraIDs, cameraIDs.isEmpty == false,
           let cameraID = alert.cameraID, cameraIDs.contains(cameraID) == false {
            return false
        }

        let severityMatches = alert.severity.rank <= minimumSeverity.rank
        let triggerMatches = trigger == "Any alarm" ||
            trigger == alert.kind.rawValue ||
            trigger == alert.severity.rawValue ||
            trigger.localizedCaseInsensitiveContains(alert.kind.rawValue) ||
            trigger.localizedCaseInsensitiveContains(alert.severity.rawValue)

        return severityMatches && triggerMatches
    }
}

public struct OperatorNotification: Codable, Identifiable, Hashable {
    public var id: UUID
    public var title: String
    public var detail: String
    public var severity: AlertSeverity
    public var time: String
    public var isRead: Bool
    public var category: AlertKind
    public var source: String
    public var alertID: UUID?
    public var createdAt: Date
    public var updatedAt: Date
    public var isDesktopDelivered: Bool
    public var actionSectionRaw: String

    public var actionSection: SentinelSection {
        SentinelSection(rawValue: actionSectionRaw) ?? .alerts
    }

    public init(
        id: UUID = UUID(),
        title: String,
        detail: String,
        severity: AlertSeverity,
        time: String? = nil,
        isRead: Bool = false,
        category: AlertKind = .system,
        source: String = "System",
        alertID: UUID? = nil,
        createdAt: Date = Date(),
        updatedAt: Date = Date(),
        isDesktopDelivered: Bool = false,
        actionSection: SentinelSection = .alerts
    ) {
        self.id = id
        self.title = title
        self.detail = detail
        self.severity = severity
        self.time = time ?? RecordingFormatters.timeFormatter.string(from: createdAt)
        self.isRead = isRead
        self.category = category
        self.source = source
        self.alertID = alertID
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.isDesktopDelivered = isDesktopDelivered
        self.actionSectionRaw = actionSection.rawValue
    }

    public enum CodingKeys: String, CodingKey {
        case id
        case title
        case detail
        case severity
        case time
        case isRead
        case category
        case source
        case alertID
        case createdAt
        case updatedAt
        case isDesktopDelivered
        case actionSectionRaw
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let fallbackDate = Date()

        id = try container.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
        title = try container.decodeIfPresent(String.self, forKey: .title) ?? "Notification"
        detail = try container.decodeIfPresent(String.self, forKey: .detail) ?? ""
        severity = try container.decodeIfPresent(AlertSeverity.self, forKey: .severity) ?? .info
        time = try container.decodeIfPresent(String.self, forKey: .time) ?? RecordingFormatters.timeFormatter.string(from: fallbackDate)
        isRead = try container.decodeIfPresent(Bool.self, forKey: .isRead) ?? false
        category = try container.decodeIfPresent(AlertKind.self, forKey: .category) ?? .system
        source = try container.decodeIfPresent(String.self, forKey: .source) ?? "System"
        alertID = try container.decodeIfPresent(UUID.self, forKey: .alertID)
        createdAt = try container.decodeIfPresent(Date.self, forKey: .createdAt) ?? fallbackDate
        updatedAt = try container.decodeIfPresent(Date.self, forKey: .updatedAt) ?? createdAt
        isDesktopDelivered = try container.decodeIfPresent(Bool.self, forKey: .isDesktopDelivered) ?? false
        actionSectionRaw = try container.decodeIfPresent(String.self, forKey: .actionSectionRaw) ?? SentinelSection.alerts.rawValue
    }
}

public enum NotificationInboxFilter: String, CaseIterable, Identifiable {
    case all = "All"
    case unread = "Unread"
    case critical = "Critical"
    case system = "System"

    public var id: String { rawValue }
}

public struct AuditLogEntry: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var time: Date
    public var user: String
    public var area: String
    public var action: String
    public var detail: String
    /// Tamper-evidence chain: `chainHash` = SHA-256(previous entry's chainHash
    /// + this entry's fields). Editing, deleting or reordering any entry in the
    /// JSON on disk breaks every link after it. nil on pre-chain entries.
    public var previousHash: String?
    public var chainHash: String?

    public func computedChainHash(previous: String) -> String {
        let payload = [
            previous,
            id.uuidString,
            String(format: "%.3f", time.timeIntervalSince1970),
            user, area, action, detail
        ].joined(separator: "\u{1F}")
        return SHA256.hash(data: Data(payload.utf8)).map { String(format: "%02x", $0) }.joined()
    }
}

public enum AuditChainStatus: Equatable {
    case intact(checked: Int)
    /// The first (oldest) entry whose link doesn't verify.
    case broken(entryID: UUID, time: Date)
    case empty
}

public struct OperatorPreferences: Codable, Hashable {
    public var defaultLandingSectionRaw: String
    public var defaultGridColumns: Int
    public var defaultStreamQuality: String
    public var dateFormat: String
    public var soundAlerts: Bool
    public var desktopNotifications: Bool
    public var autoStartRecording: Bool
    public var preferLowLatency: Bool
    public var requireIncidentForExport: Bool

    public static let standard = OperatorPreferences(
        defaultLandingSectionRaw: SentinelSection.home.rawValue,
        defaultGridColumns: 2,
        defaultStreamQuality: "Auto",
        dateFormat: "Local 24-hour",
        soundAlerts: true,
        desktopNotifications: true,
        autoStartRecording: false,
        preferLowLatency: true,
        requireIncidentForExport: true
    )

    public var defaultLandingSection: SentinelSection {
        // "Playback" was merged into the Live view; migrate any stale preference.
        if defaultLandingSectionRaw == "Playback" { return .live }
        return SentinelSection(rawValue: defaultLandingSectionRaw) ?? .home
    }
}

public struct CameraGroup: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var name: String
    public var site: String
    public var cameraNames: [String]
    public var priority: String
}

public struct RoleProfile: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var name: String
    public var description: String
    public var permissions: [String]
}

@MainActor
public final class WorkflowStore: ObservableObject {
    @Published public private(set) var personalViews: [PersonalCameraView] = []
    @Published public private(set) var handoffs: [ShiftHandoffRecord] = []
    @Published public private(set) var notificationRules: [NotificationRule] = []
    @Published public private(set) var notifications: [OperatorNotification] = []
    @Published public private(set) var auditLog: [AuditLogEntry] = []
    @Published public private(set) var cameraGroups: [CameraGroup] = []
    @Published public private(set) var roleProfiles: [RoleProfile] = []
    @Published public var preferences = OperatorPreferences.standard {
        didSet {
            savePreferences()
        }
    }

    /// The saved layout currently applied to the Live grid; nil = all cameras.
    /// Per-Mac UI state, so it lives in UserDefaults rather than workflow-state.
    @Published public private(set) var activeLayoutID: UUID? = UUID(
        uuidString: UserDefaults.standard.string(forKey: "handoffgrid.live.activeLayoutID") ?? ""
    )

    public var activeLayout: PersonalCameraView? {
        personalViews.first { $0.id == activeLayoutID }
    }

    public func applyLayout(_ view: PersonalCameraView?) {
        activeLayoutID = view?.id
        UserDefaults.standard.set(view?.id.uuidString, forKey: "handoffgrid.live.activeLayoutID")
    }

    @Published public private(set) var sessionIdleSeconds: TimeInterval = 0
    private var lastActivityDate = Date()
    private var idleTimerTask: Task<Void, Never>?

    private let personalViewsURL: URL
    private let preferencesURL: URL
    private let workflowStateURL: URL

    private struct WorkflowPersistenceState: Codable {
        var handoffs: [ShiftHandoffRecord]
        var notificationRules: [NotificationRule]
        var notifications: [OperatorNotification]
        var auditLog: [AuditLogEntry]
        var cameraGroups: [CameraGroup]
    }

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)

        personalViewsURL = supportDirectory.appendingPathComponent("personal-views.json")
        preferencesURL = supportDirectory.appendingPathComponent("operator-preferences.json")
        workflowStateURL = supportDirectory.appendingPathComponent("workflow-state.json")
        loadPersistentState()
        loadRuntimeReferenceData()
    }

    public func recordUserActivity() {
        lastActivityDate = Date()
        sessionIdleSeconds = 0
    }

    public func startIdleMonitor(onTimeout: @escaping () -> Void) {
        idleTimerTask?.cancel()
        idleTimerTask = Task { [weak self] in
            while Task.isCancelled == false {
                try? await Task.sleep(nanoseconds: 30_000_000_000) // check every 30s
                guard let self else { return }
                let idle = Date().timeIntervalSince(self.lastActivityDate)
                await MainActor.run {
                    self.sessionIdleSeconds = idle
                    if idle >= 1800 { // 30 minutes
                        onTimeout()
                    }
                }
            }
        }
    }

    public var unreadNotifications: [OperatorNotification] {
        notifications
            .filter { $0.isRead == false }
            .sorted(by: notificationSort)
    }

    public var unreadNotificationCount: Int {
        unreadNotifications.count
    }

    public func notifications(matching filter: NotificationInboxFilter) -> [OperatorNotification] {
        let filtered: [OperatorNotification]
        switch filter {
        case .all:
            filtered = notifications
        case .unread:
            filtered = notifications.filter { $0.isRead == false }
        case .critical:
            filtered = notifications.filter { $0.severity == .critical }
        case .system:
            filtered = notifications.filter { [.cameraOffline, .recording, .storage, .credential, .system].contains($0.category) }
        }

        return filtered.sorted(by: notificationSort)
    }

    public func createPersonalView(
        name: String,
        owner: String,
        site: String,
        cameraNames: [String],
        gridColumns: Int,
        quality: String
    ) {
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedName.isEmpty == false, cameraNames.isEmpty == false else {
            return
        }

        let shouldBeDefault = personalViews.isEmpty
        personalViews.insert(
            PersonalCameraView(
                name: trimmedName,
                owner: owner.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Current Operator" : owner,
                site: site.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Unassigned" : site,
                cameraNames: cameraNames,
                gridColumns: max(1, min(gridColumns, 4)),
                quality: quality,
                isDefault: shouldBeDefault,
                updatedAt: Date()
            ),
            at: 0
        )
        savePersonalViews()
        recordAudit(area: "Personal Views", action: "Created view", detail: trimmedName)
    }

    public func setDefaultPersonalView(_ view: PersonalCameraView) {
        personalViews = personalViews.map { existing in
            var updated = existing
            updated.isDefault = existing.id == view.id
            updated.updatedAt = existing.id == view.id ? Date() : existing.updatedAt
            return updated
        }
        savePersonalViews()
        recordAudit(area: "Personal Views", action: "Set default view", detail: view.name)
    }

    public func deletePersonalView(_ view: PersonalCameraView) {
        personalViews.removeAll { $0.id == view.id }
        if activeLayoutID == view.id {
            applyLayout(nil)
        }
        if personalViews.contains(where: \.isDefault) == false,
           personalViews.isEmpty == false {
            personalViews[0].isDefault = true
        }
        savePersonalViews()
        recordAudit(area: "Personal Views", action: "Deleted view", detail: view.name)
    }

    public func createHandoff(
        summary: String,
        toOperator: String,
        unresolvedItems: [String],
        offlineCameras: [String]
    ) {
        let trimmedSummary = summary.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedSummary.isEmpty == false else {
            return
        }

        handoffs.insert(
            ShiftHandoffRecord(
                fromOperator: "Current Operator",
                toOperator: toOperator.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Next Shift" : toOperator,
                summary: trimmedSummary,
                unresolvedItems: unresolvedItems,
                offlineCameras: offlineCameras,
                status: "Open",
                createdAt: Date(),
                acknowledgedAt: nil
            ),
            at: 0
        )
        recordAudit(area: "Case Handoff", action: "Created handoff", detail: trimmedSummary)
    }

    public func acknowledgeHandoff(_ handoff: ShiftHandoffRecord) {
        guard let index = handoffs.firstIndex(where: { $0.id == handoff.id }) else {
            return
        }

        handoffs[index].status = "Acknowledged"
        handoffs[index].acknowledgedAt = Date()
        recordAudit(area: "Case Handoff", action: "Acknowledged handoff", detail: handoff.summary)
    }

    public func toggleNotificationRule(_ rule: NotificationRule) {
        guard let index = notificationRules.firstIndex(where: { $0.id == rule.id }) else {
            return
        }

        notificationRules[index].isEnabled.toggle()
        recordAudit(
            area: "Notifications",
            action: notificationRules[index].isEnabled ? "Enabled rule" : "Disabled rule",
            detail: rule.name
        )
    }

    /// Inserts a new rule or replaces the one with the same id.
    public func saveNotificationRule(_ rule: NotificationRule) {
        let isNew: Bool
        if let index = notificationRules.firstIndex(where: { $0.id == rule.id }) {
            notificationRules[index] = rule
            isNew = false
        } else {
            notificationRules.append(rule)
            isNew = true
        }
        recordAudit(area: "Alarm Rules", action: isNew ? "Created rule" : "Edited rule", detail: rule.name)
    }

    public func deleteNotificationRule(_ rule: NotificationRule) {
        notificationRules.removeAll { $0.id == rule.id }
        recordAudit(area: "Alarm Rules", action: "Deleted rule", detail: rule.name)
    }

    /// Operator instructions from every enabled rule that matches this alert.
    public func instructions(for alert: AlertEvent) -> [(rule: String, text: String)] {
        notificationRules.compactMap { rule in
            guard rule.matches(alert: alert),
                  let text = rule.instructions?.trimmingCharacters(in: .whitespacesAndNewlines),
                  text.isEmpty == false else { return nil }
            return (rule.name, text)
        }
    }

    public func snoozeNotificationRule(_ rule: NotificationRule) {
        guard let index = notificationRules.firstIndex(where: { $0.id == rule.id }) else {
            return
        }

        notificationRules[index].isSnoozed.toggle()
        recordAudit(
            area: "Notifications",
            action: notificationRules[index].isSnoozed ? "Snoozed rule" : "Unsnoozed rule",
            detail: rule.name
        )
    }

    public func markNotificationRead(_ notification: OperatorNotification) {
        guard let index = notifications.firstIndex(where: { $0.id == notification.id }) else {
            return
        }

        notifications[index].isRead = true
        recordAudit(area: "Notifications", action: "Read notification", detail: notification.title)
    }

    public func markAllNotificationsRead() {
        guard notifications.contains(where: { $0.isRead == false }) else {
            return
        }

        for index in notifications.indices {
            notifications[index].isRead = true
        }

        recordAudit(area: "Notifications", action: "Read all notifications", detail: "\(notifications.count) notifications")
    }

    public func clearReadNotifications() {
        let originalCount = notifications.count
        notifications.removeAll { $0.isRead }
        guard notifications.count != originalCount else {
            return
        }

        recordAudit(area: "Notifications", action: "Cleared read notifications", detail: "\(originalCount - notifications.count) removed")
    }

    public func deleteNotification(_ notification: OperatorNotification) {
        notifications.removeAll { $0.id == notification.id }
        recordAudit(area: "Notifications", action: "Deleted notification", detail: notification.title)
    }

    public func syncNotifications(from alerts: [AlertEvent]) {
        var didUpdate = false

        for alert in alerts where alert.isOpen {
            let matchingRules = notificationRules.filter { $0.matches(alert: alert) }
            guard matchingRules.isEmpty == false else {
                continue
            }
            // Desktop delivery is decided here, where the alert (and so its camera
            // and time) is known — scoped rules can't be re-checked later from the
            // notification alone. Center-only matches are pre-marked as delivered.
            let wantsDesktop = matchingRules.contains(where: \.matchesDesktopDelivery)

            if let index = notifications.firstIndex(where: { $0.alertID == alert.id }) {
                let title = notificationTitle(for: alert)
                let detail = notificationDetail(for: alert)
                let didEscalate = alert.severity.rank < notifications[index].severity.rank

                if notifications[index].title != title ||
                    notifications[index].detail != detail ||
                    notifications[index].severity != alert.severity {
                    notifications[index].title = notificationTitle(for: alert)
                    notifications[index].detail = notificationDetail(for: alert)
                    notifications[index].severity = alert.severity
                    notifications[index].category = alert.kind
                    notifications[index].source = alert.source
                    notifications[index].updatedAt = Date()
                    notifications[index].time = RecordingFormatters.timeFormatter.string(from: Date())
                    if didEscalate {
                        notifications[index].isRead = false
                        notifications[index].isDesktopDelivered = wantsDesktop == false
                    }
                    didUpdate = true
                }
            } else {
                let notification = OperatorNotification(
                    title: notificationTitle(for: alert),
                    detail: notificationDetail(for: alert),
                    severity: alert.severity,
                    category: alert.kind,
                    source: alert.source,
                    alertID: alert.id,
                    createdAt: Date(),
                    updatedAt: Date(),
                    isDesktopDelivered: wantsDesktop == false,
                    actionSection: .alerts
                )
                notifications.insert(notification, at: 0)
                didUpdate = true
            }
        }

        if notifications.count > 200 {
            let droppedCritical = notifications.suffix(notifications.count - 200).filter { $0.severity == .critical && $0.isRead == false }
            if droppedCritical.isEmpty == false {
                recordAudit(area: "Notifications", action: "Critical notifications dropped by pruning", detail: "\(droppedCritical.count) unread critical notifications were removed (inbox full)")
            }
            notifications.removeLast(notifications.count - 200)
            didUpdate = true
        }

        if didUpdate {
            saveWorkflowState()
        }

        deliverPendingDesktopNotifications()
    }

    public func updatePreference<T>(_ keyPath: WritableKeyPath<OperatorPreferences, T>, value: T, detail: String) {
        preferences[keyPath: keyPath] = value
        recordAudit(area: "Settings", action: "Updated preference", detail: detail)
    }

    public static let auditLogCap = 2000

    /// Supplies the signed-in operator's name for audit entries that don't
    /// name a user explicitly. Set by the app from OperatorSessionStore.
    public var operatorNameProvider: (() -> String?)?

    public func recordAudit(area: String, action: String, detail: String, user: String? = nil) {
        appendAuditEntry(area: area, action: action, detail: detail, user: user ?? operatorNameProvider?() ?? "Current Operator")
        saveWorkflowState()
    }

    /// Chains and inserts an entry without saving (so the save-failure paths
    /// can log through here without recursing).
    private func appendAuditEntry(area: String, action: String, detail: String, user: String) {
        var entry = AuditLogEntry(time: Date(), user: user, area: area, action: action, detail: detail)
        let previous = auditLog.first?.chainHash ?? ""
        entry.previousHash = previous
        entry.chainHash = entry.computedChainHash(previous: previous)
        auditLog.insert(entry, at: 0)

        if auditLog.count > Self.auditLogCap {
            auditLog.removeLast(auditLog.count - Self.auditLogCap)
        }
    }

    /// Walks the chain oldest → newest. Each entry must hash to its stored
    /// chainHash and link to the entry before it. The oldest retained entry may
    /// point at one pruned by the size cap; legacy unchained entries are skipped.
    public func verifyAuditChain() -> AuditChainStatus {
        let chained = auditLog.reversed().filter { $0.chainHash != nil }
        guard chained.isEmpty == false else { return .empty }
        var expectedPrevious: String?
        for entry in chained {
            if let expectedPrevious, entry.previousHash != expectedPrevious {
                return .broken(entryID: entry.id, time: entry.time)
            }
            if entry.computedChainHash(previous: entry.previousHash ?? "") != entry.chainHash {
                return .broken(entryID: entry.id, time: entry.time)
            }
            expectedPrevious = entry.chainHash
        }
        return .intact(checked: chained.count)
    }

    /// RFC 4180 CSV of the (optionally filtered) log, oldest first.
    public static func auditCSV(_ entries: [AuditLogEntry]) -> String {
        let formatter = ISO8601DateFormatter()
        func field(_ value: String) -> String {
            "\"" + value.replacingOccurrences(of: "\"", with: "\"\"") + "\""
        }
        let rows = entries.reversed().map { entry in
            [formatter.string(from: entry.time), entry.user, entry.area, entry.action, entry.detail, entry.chainHash ?? ""]
                .map(field)
                .joined(separator: ",")
        }
        return (["time,user,area,action,detail,chain_hash"] + rows).joined(separator: "\r\n") + "\r\n"
    }

    private func loadPersistentState() {
        do {
            let directory = personalViewsURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

            if FileManager.default.fileExists(atPath: personalViewsURL.path) {
                let data = try Data(contentsOf: personalViewsURL)
                personalViews = try JSONDecoder().decode([PersonalCameraView].self, from: data)
            }

            if FileManager.default.fileExists(atPath: preferencesURL.path) {
                let data = try Data(contentsOf: preferencesURL)
                preferences = try JSONDecoder().decode(OperatorPreferences.self, from: data)
            }

            if FileManager.default.fileExists(atPath: workflowStateURL.path) {
                let data = try Data(contentsOf: workflowStateURL)
                let state = try JSONDecoder().decode(WorkflowPersistenceState.self, from: data)
                handoffs = state.handoffs
                notificationRules = state.notificationRules
                // Both lists are stored newest-first, so keep the PREFIX (newest).
                notifications = Array(state.notifications.prefix(300))
                auditLog = Array(state.auditLog.prefix(Self.auditLogCap))
                cameraGroups = state.cameraGroups
                cleanupLegacyDemoWorkflowState()
            }
        } catch {
            personalViews = []
            preferences = .standard
        }
    }

    private func loadRuntimeReferenceData() {
        ensureDefaultNotificationRules()

        roleProfiles = [
            RoleProfile(name: "Admin", description: "Full system control", permissions: ["Manage cameras", "Manage users", "Export evidence", "Change retention", "View audit log"]),
            RoleProfile(name: "Supervisor", description: "Operations and evidence control", permissions: ["Create incidents", "Assign operators", "Lock evidence", "Review case handoffs"]),
            RoleProfile(name: "Operator", description: "Daily monitoring workflow", permissions: ["Live view", "Playback", "Acknowledge alerts", "Create incidents"]),
            RoleProfile(name: "Viewer", description: "Read-only monitoring", permissions: ["Live view", "Floorplans"])
        ]
    }

    private func cleanupLegacyDemoWorkflowState() {
        let originalPersonalViews = personalViews.count
        let originalHandoffs = handoffs.count
        let originalRules = notificationRules.count
        let originalNotifications = notifications.count
        let originalAudit = auditLog.count
        let originalGroups = cameraGroups.count

        personalViews.removeAll { view in
            view.site == "Broward HQ" ||
            view.cameraNames.contains { LegacyRuntimeData.demoCameraNames.contains($0) }
        }

        handoffs.removeAll { handoff in
            handoff.offlineCameras.contains { LegacyRuntimeData.demoCameraNames.contains($0) } ||
            handoff.fromOperator == "Marisol Vega" ||
            handoff.summary.localizedCaseInsensitiveContains("Back Gate")
        }

        notificationRules.removeAll { rule in
            ["Critical camera offline", "After-hours motion", "Evidence export completed", "Recording gap"].contains(rule.name)
        }

        notifications.removeAll { notification in
            notification.title.localizedCaseInsensitiveContains("Back Gate") ||
            notification.detail.localizedCaseInsensitiveContains("Parking Lot East") ||
            notification.detail.localizedCaseInsensitiveContains("HG-1042")
        }

        auditLog.removeAll { entry in
            LegacyRuntimeData.demoPeople.contains(entry.user) ||
            entry.detail.localizedCaseInsensitiveContains("Back Gate") ||
            entry.detail.localizedCaseInsensitiveContains("Night Perimeter") ||
            entry.detail.localizedCaseInsensitiveContains("perimeter rule")
        }

        cameraGroups.removeAll { group in
            group.site == "Broward HQ" ||
            group.cameraNames.contains { LegacyRuntimeData.demoCameraNames.contains($0) }
        }

        if personalViews.count != originalPersonalViews {
            savePersonalViews()
        }

        if handoffs.count != originalHandoffs ||
            notificationRules.count != originalRules ||
            notifications.count != originalNotifications ||
            auditLog.count != originalAudit ||
            cameraGroups.count != originalGroups {
            saveWorkflowState()
        }
    }

    private func ensureDefaultNotificationRules() {
        guard notificationRules.isEmpty else {
            return
        }

        notificationRules = [
            NotificationRule(
                name: "Critical alarms",
                trigger: "Critical",
                delivery: "Desktop + Center",
                severity: AlertSeverity.critical.rawValue,
                isEnabled: true,
                isSnoozed: false
            ),
            NotificationRule(
                name: "Person detection",
                trigger: AlertKind.person.rawValue,
                delivery: "Desktop + Center",
                severity: AlertSeverity.warning.rawValue,
                isEnabled: true,
                isSnoozed: false
            ),
            NotificationRule(
                name: "Motion events",
                trigger: AlertKind.motion.rawValue,
                delivery: "Center",
                severity: AlertSeverity.warning.rawValue,
                isEnabled: true,
                isSnoozed: false
            ),
            NotificationRule(
                name: "Recording and camera health",
                trigger: "Any alarm",
                delivery: "Desktop + Center",
                severity: AlertSeverity.critical.rawValue,
                isEnabled: true,
                isSnoozed: false
            )
        ]
        saveWorkflowState()
    }

    private func notificationSort(_ first: OperatorNotification, _ second: OperatorNotification) -> Bool {
        if first.isRead != second.isRead {
            return first.isRead == false
        }

        if first.severity.rank != second.severity.rank {
            return first.severity.rank < second.severity.rank
        }

        return first.updatedAt > second.updatedAt
    }

    private func notificationTitle(for alert: AlertEvent) -> String {
        "\(alert.severity.rawValue): \(alert.title)"
    }

    private func notificationDetail(for alert: AlertEvent) -> String {
        let eventText = alert.eventCount > 1 ? " · \(alert.eventCount) events" : ""
        return "\(alert.source) · \(alert.alertState.rawValue)\(eventText)"
    }

    private func deliverPendingDesktopNotifications() {
        guard preferences.desktopNotifications else {
            return
        }

        var didUpdate = false
        for index in notifications.indices where notifications[index].isDesktopDelivered == false && notifications[index].isRead == false {
            guard notificationRules.contains(where: { rule in
                rule.isEnabled &&
                rule.isSnoozed == false &&
                rule.matchesDesktopDelivery &&
                notifications[index].severity.rank <= rule.minimumSeverity.rank &&
                (rule.trigger == "Any alarm" ||
                 rule.trigger == notifications[index].category.rawValue ||
                 rule.trigger == notifications[index].severity.rawValue ||
                 rule.trigger.localizedCaseInsensitiveContains(notifications[index].category.rawValue) ||
                 rule.trigger.localizedCaseInsensitiveContains(notifications[index].severity.rawValue))
            }) else {
                continue
            }

            let notification = notifications[index]
            notifications[index].isDesktopDelivered = true
            didUpdate = true
            Self.deliverDesktopNotification(notification, sound: preferences.soundAlerts)
        }

        if didUpdate {
            saveWorkflowState()
        }
    }

    private nonisolated static func deliverDesktopNotification(_ notification: OperatorNotification, sound: Bool) {
        guard canUseDesktopNotifications else {
            return
        }

        let center = UNUserNotificationCenter.current()
        center.getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .authorized, .provisional, .ephemeral:
                scheduleDesktopNotification(notification, sound: sound)
            case .notDetermined:
                UNUserNotificationCenter.current().requestAuthorization(options: sound ? [.alert, .sound] : [.alert]) { granted, _ in
                    if granted {
                        scheduleDesktopNotification(notification, sound: sound)
                    }
                }
            default:
                break
            }
        }
    }

    private nonisolated static func scheduleDesktopNotification(_ notification: OperatorNotification, sound: Bool) {
        guard canUseDesktopNotifications else {
            return
        }

        let content = UNMutableNotificationContent()
        content.title = notification.title
        content.body = notification.detail
        content.categoryIdentifier = notification.category.rawValue
        if sound {
            content.sound = .default
        }

        let request = UNNotificationRequest(
            identifier: notification.id.uuidString,
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request)
    }

    private nonisolated static var canUseDesktopNotifications: Bool {
        Bundle.main.bundleIdentifier?.isEmpty == false
    }

    private func savePersonalViews() {
        do {
            let directory = personalViewsURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let data = try JSONEncoder.prettySentinel.encode(personalViews)
            try data.write(to: personalViewsURL, options: .atomic)
        } catch {
            recordAudit(area: "System", action: "Save failed", detail: error.localizedDescription)
        }
    }

    private func savePreferences() {
        do {
            let directory = preferencesURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let data = try JSONEncoder.prettySentinel.encode(preferences)
            try data.write(to: preferencesURL, options: .atomic)
        } catch {
            appendAuditEntry(area: "Settings", action: "Save failed", detail: error.localizedDescription, user: "System")
        }
    }

    private func saveWorkflowState() {
        do {
            let directory = workflowStateURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let data = try JSONEncoder.prettySentinel.encode(
                WorkflowPersistenceState(
                    handoffs: handoffs,
                    notificationRules: notificationRules,
                    notifications: notifications,
                    auditLog: auditLog,
                    cameraGroups: cameraGroups
                )
            )
            try data.write(to: workflowStateURL, options: .atomic)
        } catch {
            appendAuditEntry(area: "Workflow", action: "Save failed", detail: error.localizedDescription, user: "System")
        }
    }
}
