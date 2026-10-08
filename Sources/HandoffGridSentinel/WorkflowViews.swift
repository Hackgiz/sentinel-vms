import SwiftUI
import SentinelCore
import SentinelMediaServer
import UniformTypeIdentifiers

enum WorkflowDateFormatters {
    static let time: DateFormatter = {
        let formatter = DateFormatter()
        formatter.timeStyle = .short
        formatter.dateStyle = .none
        return formatter
    }()

    static let shortDateTime: DateFormatter = {
        let formatter = DateFormatter()
        formatter.timeStyle = .short
        formatter.dateStyle = .short
        return formatter
    }()
}

struct OperatorHomeView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    let openSection: (SentinelSection) -> Void

    private var onlineCount: Int {
        cameraStore.cameras.filter { mediaIngestStore.effectiveStatus(for: $0) != .offline }.count
    }

    private var activeIncidentCount: Int {
        caseworkStore.incidents.filter { $0.status != "Closed" }.count
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible()), count: 4), spacing: 12) {
                    MetricCard(title: "Cameras Online", value: "\(onlineCount)/\(cameraStore.cameras.count)", detail: cameraStore.cameras.isEmpty ? "No cameras configured" : "Configured cameras", tint: onlineCount == cameraStore.cameras.count ? .green : .orange)
                    MetricCard(title: "Open Alerts", value: "\(caseworkStore.alerts.filter(\.isOpen).count)", detail: "Needs operator review", tint: .orange)
                    MetricCard(title: "Incidents", value: "\(activeIncidentCount)", detail: "Open casework", tint: activeIncidentCount == 0 ? .green : SentinelTheme.accent)
                    MetricCard(title: "Recording", value: "\(mediaIngestStore.activeRecordings.count)", detail: "\(mediaIngestStore.recordingSegments.count) local segments", tint: mediaIngestStore.activeRecordings.isEmpty ? .secondary : .red)
                }

                HStack(alignment: .top, spacing: 14) {
                    VStack(spacing: 14) {
                        SentinelPanel("Priority Queue", systemImage: "exclamationmark.triangle.fill") {
                            VStack(spacing: 8) {
                                if caseworkStore.activeAlerts.isEmpty {
                                    EmptyStateLine(text: "No active alerts.")
                                } else {
                                    ForEach(caseworkStore.activeAlerts.prefix(4)) { alert in
                                        AlertHomeRow(alert: alert)
                                    }
                                }

                                Button {
                                    openSection(.alerts)
                                } label: {
                                    Label("Open Alerts", systemImage: "bell.badge.fill")
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(.bordered)
                            }
                        }

                        SentinelPanel("Case Handoff", systemImage: "arrow.left.arrow.right.circle.fill") {
                            VStack(spacing: 8) {
                                if let handoff = workflowStore.handoffs.first {
                                    HandoffCompactCard(handoff: handoff)
                                } else {
                                    EmptyStateLine(text: "No active handoff notes.")
                                }

                                Button {
                                    openSection(.handoff)
                                } label: {
                                    Label("Open Case Handoff", systemImage: "arrow.right.circle.fill")
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(.bordered)
                            }
                        }
                    }

                    VStack(spacing: 14) {
                        SentinelPanel("Personal Views", systemImage: "rectangle.grid.2x2.fill") {
                            VStack(spacing: 8) {
                                if workflowStore.personalViews.isEmpty {
                                    EmptyStateLine(text: "No saved views.")
                                } else {
                                    ForEach(workflowStore.personalViews.prefix(3)) { view in
                                        SavedViewMiniRow(view: view)
                                    }
                                }

                                Button {
                                    openSection(.personalViews)
                                } label: {
                                    Label("Manage Views", systemImage: "rectangle.grid.2x2")
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(.bordered)
                            }
                        }

                        SentinelPanel("Recent Audit", systemImage: "list.bullet.rectangle.portrait.fill") {
                            VStack(spacing: 8) {
                                if workflowStore.auditLog.isEmpty {
                                    EmptyStateLine(text: "No audit events yet.")
                                } else {
                                    ForEach(workflowStore.auditLog.prefix(4)) { entry in
                                        AuditCompactRow(entry: entry)
                                    }
                                }

                                Button {
                                    openSection(.audit)
                                } label: {
                                    Label("Open Audit Log", systemImage: "list.bullet.rectangle")
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(.bordered)
                            }
                        }
                    }

                    VStack(spacing: 14) {
                        SentinelPanel("Notification Center", systemImage: "bell.and.waves.left.and.right.fill") {
                            VStack(spacing: 8) {
                                if workflowStore.unreadNotifications.isEmpty {
                                    EmptyStateLine(text: "No notifications.")
                                } else {
                                    ForEach(workflowStore.unreadNotifications.prefix(3)) { notification in
                                        NotificationCompactRow(notification: notification)
                                    }
                                }

                                Button {
                                    openSection(.notifications)
                                } label: {
                                    Label("Open Notifications", systemImage: "bell.fill")
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(.bordered)
                            }
                        }

                        SentinelPanel("Camera Groups", systemImage: "folder.fill.badge.gearshape") {
                            VStack(spacing: 8) {
                                if workflowStore.cameraGroups.isEmpty {
                                    EmptyStateLine(text: "No camera groups.")
                                } else {
                                    ForEach(workflowStore.cameraGroups) { group in
                                        CameraGroupMiniRow(group: group)
                                    }
                                }

                                Button {
                                    openSection(.cameras)
                                } label: {
                                    Label("Open Camera Inventory", systemImage: "camera.fill")
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(.bordered)
                            }
                        }
                    }
                }
            }
            .padding(14)
        }
        .background(SentinelTheme.background)
    }
}

struct AlertHomeRow: View {
    let alert: AlertEvent

    var body: some View {
        HStack(spacing: 10) {
            SeverityBadge(severity: alert.severity)

            VStack(alignment: .leading, spacing: 3) {
                Text(alert.title)
                    .font(.caption.weight(.semibold))

                Text("\(alert.source) · \(alert.time) · \(alert.state.rawValue)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Spacer()
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct HandoffCompactCard: View {
    let handoff: ShiftHandoffRecord

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(handoff.status)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(handoff.status == "Open" ? SentinelTheme.amber : .green)

                Spacer()

                Text(WorkflowDateFormatters.time.string(from: handoff.createdAt))
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Text(handoff.summary)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(3)
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct SavedViewMiniRow: View {
    let view: PersonalCameraView

    var body: some View {
        HStack {
            Image(systemName: view.isDefault ? "star.fill" : "rectangle.grid.2x2.fill")
                .foregroundStyle(view.isDefault ? SentinelTheme.amber : SentinelTheme.accent)

            VStack(alignment: .leading, spacing: 2) {
                Text(view.name)
                    .font(.caption.weight(.semibold))

                Text("\(view.cameraNames.count) cameras · \(view.quality)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Spacer()
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct AuditCompactRow: View {
    let entry: AuditLogEntry

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "checkmark.shield.fill")
                .foregroundStyle(SentinelTheme.accent)

            VStack(alignment: .leading, spacing: 2) {
                Text(entry.action)
                    .font(.caption.weight(.semibold))

                Text("\(entry.area) · \(entry.user)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Spacer()
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct NotificationCompactRow: View {
    let notification: OperatorNotification

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: notification.category.symbol)
                .foregroundStyle(notification.severity.tint)
                .frame(width: 16)

            VStack(alignment: .leading, spacing: 2) {
                Text(notification.title)
                    .font(.caption.weight(.semibold))
                    .lineLimit(1)

                Text("\(notification.time) · \(notification.detail)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }

            Spacer()

            if notification.isRead == false {
                Circle()
                    .fill(notification.severity.tint)
                    .frame(width: 7, height: 7)
            }
        }
        .padding(10)
        .background(notification.isRead ? SentinelTheme.panelRaised.opacity(0.55) : SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct CameraGroupMiniRow: View {
    let group: CameraGroup

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(group.name)
                    .font(.caption.weight(.semibold))

                Text("\(group.cameraNames.count) cameras · \(group.priority)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Text(group.site)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(SentinelTheme.accent)
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct PersonalViewsView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var workflowStore: WorkflowStore
    @State private var selectedViewID: UUID?
    @State private var isCreatingView = false

    private var selectedView: PersonalCameraView? {
        workflowStore.personalViews.first { $0.id == selectedViewID } ?? workflowStore.personalViews.first
    }

    private var selectedCameras: [CameraFeed] {
        guard let selectedView else {
            return []
        }

        return cameraStore.cameras.filter { selectedView.cameraNames.contains($0.name) }
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label("Saved Operator Views", systemImage: "rectangle.grid.2x2.fill")
                        .font(.headline)

                    Spacer()

                    Button {
                        isCreatingView = true
                    } label: {
                        Label("New View", systemImage: "plus.circle.fill")
                    }
                    .buttonStyle(.borderedProminent)
                }

                SentinelPanel("Personal Layouts") {
                    ScrollView {
                        VStack(spacing: 8) {
                            ForEach(workflowStore.personalViews) { view in
                                PersonalViewRow(
                                    view: view,
                                    isSelected: view.id == selectedView?.id,
                                    select: { selectedViewID = view.id },
                                    setDefault: { workflowStore.setDefaultPersonalView(view) },
                                    delete: { workflowStore.deletePersonalView(view) },
                                    openInLive: {
                                        workflowStore.applyLayout(view)
                                        commandCenter.requestOpen(.live)
                                    }
                                )
                            }
                        }
                    }
                    .frame(maxHeight: 520)
                }

                Spacer()
            }
            .frame(width: 360)
            .padding(14)

            Divider()
                .overlay(SentinelTheme.line)

            VStack(spacing: 0) {
                if let selectedView {
                    SavedViewPreview(view: selectedView, cameras: selectedCameras)
                } else {
                    EmptyStateLine(text: "Create a saved view to preview cameras.")
                        .padding(14)
                }
            }
        }
        .background(SentinelTheme.background)
        .sheet(isPresented: $isCreatingView) {
            CreatePersonalViewSheet(isPresented: $isCreatingView)
        }
        .onAppear {
            selectedViewID = selectedView?.id
        }
    }
}

struct PersonalViewRow: View {
    let view: PersonalCameraView
    let isSelected: Bool
    let select: () -> Void
    let setDefault: () -> Void
    let delete: () -> Void
    var openInLive: () -> Void = {}

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Image(systemName: view.isDefault ? "star.fill" : "rectangle.grid.2x2.fill")
                    .foregroundStyle(view.isDefault ? SentinelTheme.amber : SentinelTheme.accent)

                Text(view.name)
                    .font(.headline)

                Spacer()

                Text("\(view.gridColumns)x\(view.gridColumns)")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }

            Text("\(view.owner) · \(view.site) · \(view.quality)")
                .font(.caption)
                .foregroundStyle(.secondary)

            Text(view.cameraNames.joined(separator: ", "))
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(2)

            HStack {
                Button("Open in Live", action: openInLive)
                    .buttonStyle(.borderedProminent)

                Button("Default", action: setDefault)
                    .disabled(view.isDefault)

                Button("Delete", role: .destructive, action: delete)
                    .disabled(view.isDefault)

                Spacer()
            }
            .buttonStyle(.bordered)
        }
        .padding(12)
        .background(
            isSelected ? SentinelTheme.accent.opacity(0.16) : SentinelTheme.panelRaised,
            in: RoundedRectangle(cornerRadius: 8)
        )
        .contentShape(RoundedRectangle(cornerRadius: 8))
        .onTapGesture(perform: select)
    }
}

struct SavedViewPreview: View {
    let view: PersonalCameraView
    let cameras: [CameraFeed]

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text(view.name)
                        .font(.title3.weight(.semibold))

                    Text("\(view.owner) · \(view.cameraNames.count) cameras · \(view.quality)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                Spacer()

                MetricPill(text: view.isDefault ? "DEFAULT" : "SAVED")
            }
            .padding(14)
            .background(SentinelTheme.chrome)

            GeometryReader { proxy in
                let availableWidth = max(proxy.size.width - 28, 240)
                let maxColumns = max(1, Int(availableWidth / 252))
                let columnCount = min(max(1, view.gridColumns), maxColumns)

                ScrollView {
                    LazyVGrid(
                        columns: Array(repeating: GridItem(.flexible(minimum: 180), spacing: 12), count: columnCount),
                        spacing: 12
                    ) {
                        ForEach(cameras) { camera in
                            CameraTile(camera: camera, isSelected: false, contentMode: .fit)
                        }
                    }
                    .padding(14)
                }
            }
        }
    }
}

struct CreatePersonalViewSheet: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @Binding var isPresented: Bool
    @State private var name = ""
    @State private var owner = "Current Operator"
    @State private var site = ""
    @State private var gridColumns = 2
    @State private var quality = "Auto"
    @State private var selectedCameraNames = Set<String>()

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("New Personal View")
                .font(.title3.weight(.semibold))

            TextField("View name", text: $name)
                .textFieldStyle(.roundedBorder)

            HStack {
                TextField("Owner", text: $owner)
                    .textFieldStyle(.roundedBorder)

                Picker("Grid", selection: $gridColumns) {
                    Text("1x1").tag(1)
                    Text("2x2").tag(2)
                    Text("3x3").tag(3)
                    Text("4x4").tag(4)
                }
                .frame(width: 180)
            }

            Picker("Quality", selection: $quality) {
                Text("Auto").tag("Auto")
                Text("Main").tag("Main")
                Text("Substream").tag("Substream")
                Text("Low Latency").tag("Low Latency")
            }
            .pickerStyle(.segmented)

            SentinelPanel("Cameras") {
                ScrollView {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(cameraStore.cameras) { camera in
                            Toggle(isOn: Binding(
                                get: { selectedCameraNames.contains(camera.name) },
                                set: { isSelected in
                                    if isSelected {
                                        selectedCameraNames.insert(camera.name)
                                    } else {
                                        selectedCameraNames.remove(camera.name)
                                    }
                                }
                            )) {
                                HStack {
                                    Circle()
                                        .fill(camera.status.tint)
                                        .frame(width: 8, height: 8)

                                    Text(camera.name)
                                    Spacer()
                                    Text(camera.location)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            .toggleStyle(.checkbox)
                        }
                    }
                }
                .frame(height: 220)
            }

            HStack {
                Spacer()

                Button("Cancel") {
                    isPresented = false
                }

                Button {
                    workflowStore.createPersonalView(
                        name: name,
                        owner: owner,
                        site: site,
                        cameraNames: cameraStore.cameras.map(\.name).filter { selectedCameraNames.contains($0) },
                        gridColumns: gridColumns,
                        quality: quality
                    )
                    isPresented = false
                } label: {
                    Label("Save View", systemImage: "checkmark.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .disabled(name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || selectedCameraNames.isEmpty)
            }
        }
        .padding(22)
        .frame(width: 560)
        .background(SentinelTheme.background)
    }
}

struct ShiftHandoffView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @State private var isCreatingHandoff = false

    var body: some View {
        HStack(spacing: 0) {
            ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label("Case Handoff", systemImage: "arrow.left.arrow.right.circle.fill")
                        .font(.headline)

                    Spacer()

                    Button {
                        isCreatingHandoff = true
                    } label: {
                        Label("New Case Handoff", systemImage: "square.and.pencil")
                    }
                    .buttonStyle(.borderedProminent)
                }

                SentinelPanel("Case Handoff Log") {
                    VStack(spacing: 8) {
                        if workflowStore.handoffs.isEmpty {
                            EmptyStateLine(text: "No handoffs have been created.")
                        } else {
                            ForEach(workflowStore.handoffs) { handoff in
                                HandoffRecordRow(
                                    handoff: handoff,
                                    acknowledge: { workflowStore.acknowledgeHandoff(handoff) }
                                )
                            }
                        }
                    }
                }
            }
            .padding(14)
            } // ScrollView

            Divider()
                .overlay(SentinelTheme.line)

            VStack(alignment: .leading, spacing: 14) {
                SentinelPanel("Current Case Summary", systemImage: "clipboard.fill") {
                    VStack(alignment: .leading, spacing: 10) {
                        DetailRow(label: "New alerts", value: "\(caseworkStore.alerts.filter { $0.alertState == .new }.count)")
                        DetailRow(label: "Open incidents", value: "\(caseworkStore.incidents.filter { $0.status != "Closed" }.count)")
                        DetailRow(label: "Offline cameras", value: "\(cameraStore.cameras.filter { $0.status == .offline }.count)")
                        DetailRow(label: "Unread notifications", value: "\(workflowStore.unreadNotificationCount)")
                    }
                }

                SentinelPanel("Offline Cameras", systemImage: "video.slash.fill") {
                    VStack(spacing: 8) {
                        let offlineCameras = cameraStore.cameras.filter { $0.status == .offline }
                        if offlineCameras.isEmpty {
                            EmptyStateLine(text: "No offline cameras.")
                        } else {
                            ForEach(offlineCameras) { camera in
                                EmptyStateLine(text: "\(camera.name) · \(camera.ipAddress)")
                            }
                        }
                    }
                }

                Spacer()
            }
            .frame(width: 330)
            .padding(14)
            .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.background)
        .sheet(isPresented: $isCreatingHandoff) {
            CreateHandoffSheet(isPresented: $isCreatingHandoff)
        }
    }
}

struct HandoffRecordRow: View {
    let handoff: ShiftHandoffRecord
    let acknowledge: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("\(handoff.fromOperator) to \(handoff.toOperator)")
                        .font(.headline)

                    Text(WorkflowDateFormatters.shortDateTime.string(from: handoff.createdAt))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                Spacer()

                Text(handoff.status)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(handoff.status == "Open" ? SentinelTheme.amber : .green)
            }

            Text(handoff.summary)
                .font(.caption)
                .foregroundStyle(.secondary)

            if handoff.unresolvedItems.isEmpty == false {
                VStack(alignment: .leading, spacing: 5) {
                    ForEach(Array(handoff.unresolvedItems.enumerated()), id: \.offset) { _, item in
                        Label(item, systemImage: "circle")
                            .font(.caption)
                    }
                }
            }

            if handoff.offlineCameras.isEmpty == false {
                Label("Offline: \(handoff.offlineCameras.joined(separator: ", "))", systemImage: "video.slash.fill")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Button {
                acknowledge()
            } label: {
                Label("Acknowledge Case Handoff", systemImage: "checkmark.circle.fill")
            }
            .buttonStyle(.bordered)
            .disabled(handoff.status == "Acknowledged")
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct CreateHandoffSheet: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @Binding var isPresented: Bool
    @State private var toOperator = "Next Shift"
    @State private var summary = ""

    private var unresolvedItems: [String] {
        let alertItems = caseworkStore.alerts
            .filter(\.isOpen)
            .prefix(4)
            .map { "\($0.title) on \($0.source)" }
        return Array(alertItems)
    }

    private var offlineCameras: [String] {
        cameraStore.cameras.filter { $0.status == .offline }.map(\.name)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("New Case Handoff")
                .font(.title3.weight(.semibold))

            TextField("To operator or shift", text: $toOperator)
                .textFieldStyle(.roundedBorder)

            TextEditor(text: $summary)
                .font(.body)
                .frame(height: 120)
                .scrollContentBackground(.hidden)
                .padding(8)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))

            SentinelPanel("Included Context") {
                VStack(alignment: .leading, spacing: 8) {
                    if unresolvedItems.isEmpty && offlineCameras.isEmpty {
                        EmptyStateLine(text: "No active alerts or offline cameras.")
                    } else {
                        ForEach(Array(unresolvedItems.enumerated()), id: \.offset) { _, item in
                            EmptyStateLine(text: item)
                        }
                    }

                    if offlineCameras.isEmpty == false {
                        EmptyStateLine(text: "Offline: \(offlineCameras.joined(separator: ", "))")
                    }
                }
            }

            HStack {
                Spacer()

                Button("Cancel") {
                    isPresented = false
                }

                Button {
                    workflowStore.createHandoff(
                        summary: summary,
                        toOperator: toOperator,
                        unresolvedItems: unresolvedItems,
                        offlineCameras: offlineCameras
                    )
                    isPresented = false
                } label: {
                    Label("Save Case Handoff", systemImage: "checkmark.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .disabled(summary.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(22)
        .frame(width: 560)
        .background(SentinelTheme.background)
    }
}

struct NotificationCenterView: View {
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var workflowStore: WorkflowStore
    @State private var filter = NotificationInboxFilter.unread
    @State private var editingRule: NotificationRule?
    @State private var isCreatingRule = false

    private var filteredNotifications: [OperatorNotification] {
        workflowStore.notifications(matching: filter)
    }

    var body: some View {
        GeometryReader { proxy in
            let showsRulePanel = proxy.size.width >= 1080

            HStack(spacing: 0) {
                ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    notificationHeader

                    NotificationMetricStrip(
                        unread: workflowStore.unreadNotificationCount,
                        critical: workflowStore.notifications.filter { $0.severity == .critical && $0.isRead == false }.count,
                        rules: workflowStore.notificationRules.filter(\.isEnabled).count
                    )

                    Picker("Inbox", selection: $filter) {
                        ForEach(NotificationInboxFilter.allCases) { inboxFilter in
                            Text(inboxFilter.rawValue).tag(inboxFilter)
                        }
                    }
                    .pickerStyle(.segmented)

                    SentinelPanel("Live Notifications") {
                        LazyVStack(spacing: 8) {
                            if filteredNotifications.isEmpty {
                                EmptyStateLine(text: filter == .unread ? "No unread notifications." : "No \(filter.rawValue.lowercased()) notifications.")
                            } else {
                                ForEach(filteredNotifications) { notification in
                                    NotificationRow(
                                        notification: notification,
                                        markRead: { workflowStore.markNotificationRead(notification) },
                                        openAction: {
                                            workflowStore.markNotificationRead(notification)
                                            commandCenter.requestOpen(notification.actionSection)
                                        },
                                        delete: { workflowStore.deleteNotification(notification) }
                                    )
                                }
                            }
                        }
                    }

                    if showsRulePanel == false {
                        notificationRulesPanel
                    }
                }
                .padding(14)
                } // ScrollView

                if showsRulePanel {
                    Divider()
                        .overlay(SentinelTheme.line)

                    VStack(alignment: .leading, spacing: 14) {
                        notificationRulesPanel
                        Spacer(minLength: 0)
                    }
                    .frame(width: 420)
                    .padding(14)
                    .background(SentinelTheme.chrome)
                }
            }
        }
        .background(SentinelTheme.background)
        .onAppear {
            workflowStore.syncNotifications(from: caseworkStore.alerts)
        }
        .sheet(item: $editingRule) { rule in
            AlarmRuleEditorSheet(rule: rule)
        }
        .sheet(isPresented: $isCreatingRule) {
            AlarmRuleEditorSheet(rule: nil)
        }
    }

    private var notificationHeader: some View {
        HStack {
            VStack(alignment: .leading, spacing: 3) {
                Label("Notification Center", systemImage: "bell.and.waves.left.and.right.fill")
                    .font(.headline)

                Text("Desktop and in-app dispatch for alarm activity")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Button {
                workflowStore.syncNotifications(from: caseworkStore.alerts)
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .help("Sync from current alarms")

            Button {
                workflowStore.markAllNotificationsRead()
            } label: {
                Label("Read All", systemImage: "checkmark.circle.fill")
            }
            .disabled(workflowStore.unreadNotificationCount == 0)

            Button {
                workflowStore.clearReadNotifications()
            } label: {
                Label("Clear Read", systemImage: "trash")
            }
            .disabled(workflowStore.notifications.contains(where: \.isRead) == false)
        }
        .buttonStyle(.bordered)
    }

    private var notificationRulesPanel: some View {
        SentinelPanel("Rules", systemImage: "slider.horizontal.3") {
            ScrollView {
                VStack(spacing: 8) {
                    Button {
                        isCreatingRule = true
                    } label: {
                        Label("New Rule", systemImage: "plus.circle.fill")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)

                    if workflowStore.notificationRules.isEmpty {
                        EmptyStateLine(text: "No notification rules configured.")
                    } else {
                        ForEach(workflowStore.notificationRules) { rule in
                            NotificationRuleRow(
                                rule: rule,
                                toggle: { workflowStore.toggleNotificationRule(rule) },
                                snooze: { workflowStore.snoozeNotificationRule(rule) },
                                edit: { editingRule = rule }
                            )
                        }
                    }
                }
            }
        }
    }
}

struct NotificationRow: View {
    let notification: OperatorNotification
    let markRead: () -> Void
    let openAction: () -> Void
    let delete: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: notification.category.symbol)
                .font(.title3)
                .foregroundStyle(notification.severity.tint)
                .frame(width: 28)

            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    Text(notification.title)
                        .font(.headline)
                        .lineLimit(1)

                    if notification.isRead == false {
                        Text("Unread")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(notification.severity.tint)
                            .padding(.horizontal, 7)
                            .padding(.vertical, 3)
                            .background(notification.severity.tint.opacity(0.12), in: Capsule())
                    }
                }

                Text("\(notification.source) · \(notification.time) · \(notification.detail)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)

                HStack(spacing: 8) {
                    SeverityBadge(severity: notification.severity)
                    MetricPill(text: notification.category.rawValue)

                    if notification.isDesktopDelivered {
                        Label("Desktop sent", systemImage: "display")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.secondary)
                    }
                }
            }

            Spacer()

            VStack(spacing: 7) {
                Button {
                    openAction()
                } label: {
                    Label("Open", systemImage: "arrow.right.circle.fill")
                }

                Button(notification.isRead ? "Read" : "Mark Read", action: markRead)
                    .disabled(notification.isRead)

                Button(role: .destructive, action: delete) {
                    Image(systemName: "trash")
                }
                .help("Delete notification")
            }
            .buttonStyle(.bordered)
        }
        .padding(12)
        .background(notification.isRead ? SentinelTheme.panelRaised.opacity(0.55) : SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(notification.isRead ? SentinelTheme.line.opacity(0.45) : notification.severity.tint.opacity(0.38), lineWidth: 1)
        }
    }
}

struct NotificationRuleRow: View {
    let rule: NotificationRule
    let toggle: () -> Void
    let snooze: () -> Void
    var edit: () -> Void = {}

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(rule.name)
                        .font(.headline)
                        .lineLimit(1)

                    Text(rule.trigger)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }

                Spacer()

                Toggle("", isOn: Binding(get: { rule.isEnabled }, set: { _ in toggle() }))
                    .labelsHidden()
            }

            HStack {
                MetricPill(text: rule.severity)
                MetricPill(text: rule.delivery)
                if let ids = rule.cameraIDs, ids.isEmpty == false {
                    MetricPill(text: "\(ids.count) camera\(ids.count == 1 ? "" : "s")")
                }
                if rule.hasSchedule, let from = rule.activeFromHour, let to = rule.activeToHour {
                    MetricPill(text: "\(AlarmRuleEditorSheet.hourLabel(from))–\(AlarmRuleEditorSheet.hourLabel(to))")
                }

                Spacer()

                Button("Edit", action: edit)
                    .buttonStyle(.bordered)

                Button(rule.isSnoozed ? "Unsnooze" : "Snooze", action: snooze)
                    .buttonStyle(.bordered)
            }
        }
        .padding(12)
        .background(rule.isEnabled ? SentinelTheme.panelRaised : SentinelTheme.panelRaised.opacity(0.52), in: RoundedRectangle(cornerRadius: 8))
    }
}

struct NotificationMetricStrip: View {
    let unread: Int
    let critical: Int
    let rules: Int

    var body: some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible()), count: 3), spacing: 10) {
            MetricCard(title: "Unread", value: "\(unread)", detail: "Needs review", tint: unread == 0 ? .green : SentinelTheme.amber)
            MetricCard(title: "Critical", value: "\(critical)", detail: "Desktop eligible", tint: critical == 0 ? .green : .red)
            MetricCard(title: "Rules", value: "\(rules)", detail: "Enabled", tint: rules == 0 ? .secondary : SentinelTheme.accent)
        }
    }
}

struct AuditLogView: View {
    @EnvironmentObject private var workflowStore: WorkflowStore
    @State private var query = ""
    @State private var area = "All areas"
    @State private var chainStatus: AuditChainStatus?
    @State private var exportMessage: String?

    private var areas: [String] {
        ["All areas"] + Set(workflowStore.auditLog.map(\.area)).sorted()
    }

    private var filteredEntries: [AuditLogEntry] {
        let trimmedQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        return workflowStore.auditLog.filter { entry in
            (area == "All areas" || entry.area == area) &&
                (trimmedQuery.isEmpty || [entry.user, entry.area, entry.action, entry.detail]
                    .joined(separator: " ")
                    .localizedCaseInsensitiveContains(trimmedQuery))
        }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label("Audit Log", systemImage: "list.bullet.rectangle.portrait.fill")
                        .font(.headline)

                    Spacer()

                    Picker("Area", selection: $area) {
                        ForEach(areas, id: \.self) { Text($0).tag($0) }
                    }
                    .labelsHidden()
                    .frame(width: 160)

                    TextField("Filter audit entries", text: $query)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 260)

                    Button {
                        chainStatus = workflowStore.verifyAuditChain()
                    } label: {
                        Label("Verify Chain", systemImage: "checkmark.seal")
                    }
                    .help("Check that no audit entry has been edited, deleted or reordered on disk")

                    Button {
                        exportCSV()
                    } label: {
                        Label("Export CSV", systemImage: "square.and.arrow.up")
                    }
                    .disabled(filteredEntries.isEmpty)
                }

                if let chainStatus {
                    chainBanner(chainStatus)
                }
                if let exportMessage {
                    Text(exportMessage)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                SentinelPanel("Operator Activity · \(filteredEntries.count) entries") {
                    LazyVStack(spacing: 8) {
                        if filteredEntries.isEmpty {
                            EmptyStateLine(text: "No audit entries match this filter.")
                        } else {
                            ForEach(filteredEntries) { entry in
                                AuditLogRow(entry: entry)
                            }
                        }
                    }
                }
            }
            .padding(14)
        }
        .background(SentinelTheme.background)
        .onChange(of: workflowStore.auditLog.count) { _ in
            // A new entry makes the last verification stale.
            chainStatus = nil
        }
    }

    @ViewBuilder
    private func chainBanner(_ status: AuditChainStatus) -> some View {
        let (symbol, tint, text): (String, Color, String) = {
            switch status {
            case .intact(let checked):
                return ("checkmark.seal.fill", .green, "Chain intact — \(checked) signed entries verified.")
            case .broken(_, let time):
                return ("exclamationmark.octagon.fill", .red, "Chain BROKEN at the entry from \(WorkflowDateFormatters.shortDateTime.string(from: time)). The log file was modified outside Sentinel.")
            case .empty:
                return ("info.circle.fill", .secondary, "No signed entries yet — entries recorded from now on are chained.")
            }
        }()
        Label(text, systemImage: symbol)
            .font(.callout)
            .foregroundStyle(tint)
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
    }

    private func exportCSV() {
        let entries = filteredEntries
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "sentinel-audit-\(Date().formatted(.iso8601.year().month().day())).csv"
        panel.allowedContentTypes = [.commaSeparatedText]
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            do {
                try WorkflowStore.auditCSV(entries).write(to: url, atomically: true, encoding: .utf8)
                exportMessage = "Exported \(entries.count) entries to \(url.lastPathComponent)."
                workflowStore.recordAudit(area: "Audit", action: "Exported audit log", detail: "\(entries.count) entries")
            } catch {
                exportMessage = "Export failed: \(error.localizedDescription)"
            }
        }
    }
}

struct AuditLogRow: View {
    let entry: AuditLogEntry

    var body: some View {
        HStack(spacing: 12) {
            Text(WorkflowDateFormatters.shortDateTime.string(from: entry.time))
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
                .frame(width: 110, alignment: .leading)

            VStack(alignment: .leading, spacing: 3) {
                Text(entry.action)
                    .font(.headline)

                Text(entry.detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Text(entry.area)
                .font(.caption.weight(.semibold))
                .foregroundStyle(SentinelTheme.accent)
                .frame(width: 120, alignment: .leading)

            Text(entry.user)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 130, alignment: .trailing)
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct SettingsPreferencesView: View {
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var powerManager: PowerManager
    @AppStorage("handoffgrid.motionSensitivity") private var motionSensitivity = 0.04

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Label("Settings", systemImage: "gearshape.fill")
                    .font(.headline)

                HStack(alignment: .top, spacing: 14) {
                    SentinelPanel("Operator Defaults", systemImage: "person.crop.circle.badge.checkmark") {
                        VStack(alignment: .leading, spacing: 12) {
                            Picker("Landing Page", selection: preferenceBinding(\.defaultLandingSectionRaw, detail: "Default landing page")) {
                                ForEach(SentinelSection.allCases) { section in
                                    Text(section.rawValue).tag(section.rawValue)
                                }
                            }

                            Picker("Default Grid", selection: preferenceBinding(\.defaultGridColumns, detail: "Default grid")) {
                                Text("1x1").tag(1)
                                Text("2x2").tag(2)
                                Text("3x3").tag(3)
                                Text("4x4").tag(4)
                            }

                            Picker("Stream Quality", selection: preferenceBinding(\.defaultStreamQuality, detail: "Default stream quality")) {
                                Text("Auto").tag("Auto")
                                Text("Main").tag("Main")
                                Text("Substream").tag("Substream")
                                Text("Low Latency").tag("Low Latency")
                            }

                            Picker("Date Format", selection: preferenceBinding(\.dateFormat, detail: "Date format")) {
                                Text("Local 24-hour").tag("Local 24-hour")
                                Text("Local 12-hour").tag("Local 12-hour")
                                Text("UTC").tag("UTC")
                            }
                        }
                    }

                    SentinelPanel("Alert Behavior", systemImage: "bell.badge.fill") {
                        VStack(alignment: .leading, spacing: 12) {
                            Toggle("Desktop notifications", isOn: preferenceBinding(\.desktopNotifications, detail: "Desktop notifications"))
                            Toggle("Sound alerts", isOn: preferenceBinding(\.soundAlerts, detail: "Sound alerts"))
                            Toggle("Prefer low latency previews", isOn: preferenceBinding(\.preferLowLatency, detail: "Low latency previews"))
                            Toggle("Auto-start local recording", isOn: preferenceBinding(\.autoStartRecording, detail: "Auto-start local recording"))
                            Toggle("Require incident before export", isOn: preferenceBinding(\.requireIncidentForExport, detail: "Incident required for export"))
                        }
                        .toggleStyle(.checkbox)
                    }
                }

                SentinelPanel("Motion Detection", systemImage: "figure.walk.motion") {
                    VStack(alignment: .leading, spacing: 12) {
                        HStack {
                            Text("Motion sensitivity")
                                .font(.caption.weight(.semibold))

                            Slider(value: $motionSensitivity, in: 0.02...0.20, step: 0.01)

                            Text("\(Int(motionSensitivity * 100))")
                                .font(.caption.monospacedDigit())
                                .frame(width: 34, alignment: .trailing)
                        }

                        Text("Lower values mark smaller frame changes as motion; higher values reduce noise from shadows and camera compression.")
                            .font(.caption)
                            .foregroundStyle(.secondary)

                        Text("Person, vehicle, license-plate, animal, loitering, and AI scene-description settings now live in the AI section.")
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                SentinelPanel("System", systemImage: "gearshape.2.fill") {
                    VStack(alignment: .leading, spacing: 12) {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Launch at Login")
                                    .font(.caption.weight(.semibold))
                                Text("Start Sentinel VMS automatically when you log into macOS.")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            Toggle("", isOn: Binding(
                                get: { AutoStartManager.isEnabled },
                                set: { AutoStartManager.setEnabled($0) }
                            ))
                            .toggleStyle(.switch)
                        }

                        Divider().overlay(SentinelTheme.line)

                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Keep Mac awake")
                                    .font(.caption.weight(.semibold))
                                Text("Prevent system sleep so recording never stops. The Mac stays awake automatically while recording; turn this on to keep it awake the whole time Sentinel is open.")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                                HStack(spacing: 5) {
                                    Circle()
                                        .fill(powerManager.isHoldingAssertion ? Color.green : .secondary)
                                        .frame(width: 6, height: 6)
                                    Text(powerManager.statusDescription)
                                        .font(.caption2)
                                        .foregroundStyle(.secondary)
                                }
                                .padding(.top, 2)
                            }
                            Spacer()
                            Toggle("", isOn: $powerManager.keepAwakeAlways)
                                .toggleStyle(.switch)
                        }

                        Text("Note: closing a laptop lid still sleeps the Mac unless it's on power with an external display. For 24/7 recording, keep the lid open or use an external monitor.")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                IOSCompanionPanel()

                SentinelPanel("Saved State", systemImage: "externaldrive.fill") {
                    LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                        EmptyStateLine(text: "\(workflowStore.personalViews.count) personal views saved locally")
                        EmptyStateLine(text: "\(workflowStore.notificationRules.filter { $0.isEnabled }.count) notification rules enabled")
                        EmptyStateLine(text: "\(workflowStore.auditLog.count) audit entries retained")
                    }
                }
            }
            .padding(14)
        }
        .background(SentinelTheme.background)
    }

    private func preferenceBinding<T>(_ keyPath: WritableKeyPath<OperatorPreferences, T>, detail: String) -> Binding<T> {
        Binding(
            get: { workflowStore.preferences[keyPath: keyPath] },
            set: { workflowStore.updatePreference(keyPath, value: $0, detail: detail) }
        )
    }
}

enum AutoStartManager {
    private static var plistURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/LaunchAgents/com.handoffgrid.sentinel.vms.plist")
    }

    static var isEnabled: Bool {
        FileManager.default.fileExists(atPath: plistURL.path)
    }

    static func setEnabled(_ enabled: Bool) {
        enabled ? enable() : disable()
    }

    /// If auto-start is on but points at a different (moved, rebuilt or
    /// deleted) copy of the app, repoint it at this one. Only the file is
    /// rewritten — launchd reads it at next login; loading it now would
    /// RunAtLoad a second instance.
    static func repairIfMoved() {
        guard isEnabled,
              Bundle.main.bundlePath.hasSuffix(".app"),
              let execPath = Bundle.main.executablePath,
              let data = try? Data(contentsOf: plistURL),
              let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
              let args = plist["ProgramArguments"] as? [String],
              args.first != execPath else { return }
        try? plistXML(execPath: execPath).write(to: plistURL, atomically: true, encoding: .utf8)
        SentinelLog.shared.info("Auto-start repointed to \(execPath) (was \(args.first ?? "none"))", category: "lifecycle")
    }

    private static func enable() {
        let execPath = Bundle.main.executablePath ?? ProcessInfo.processInfo.arguments[0]
        try? plistXML(execPath: execPath).write(to: plistURL, atomically: true, encoding: .utf8)
        run("/bin/launchctl", args: ["load", plistURL.path])
    }

    private static func plistXML(execPath: String) -> String {
        let escaped = execPath
            .replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
        return """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
            <key>Label</key>
            <string>com.handoffgrid.sentinel.vms</string>
            <key>ProgramArguments</key>
            <array>
                <string>\(escaped)</string>
            </array>
            <key>RunAtLoad</key>
            <true/>
            <key>KeepAlive</key>
            <dict>
                <key>SuccessfulExit</key>
                <false/>
            </dict>
        </dict>
        </plist>
        """
    }

    private static func disable() {
        run("/bin/launchctl", args: ["unload", plistURL.path])
        try? FileManager.default.removeItem(at: plistURL)
    }

    private static func run(_ path: String, args: [String]) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        try? p.run()
    }
}
