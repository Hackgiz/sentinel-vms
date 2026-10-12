import AppKit
import SwiftUI
import SentinelCore

// Native pages for the Admin tasks an operator reaches for often (cameras,
// rules, users, audit). Site-structure work — enrolling Recording Servers,
// camera groups, directory sign-in, storage — links to Sentinel Admin.

// MARK: - Cameras

struct CamerasAdminView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    @State private var editing: CameraEditTarget?
    @State private var deleting: V2.Camera?
    @State private var error: String?

    private var canManage: Bool { session.can(.manageCameras) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("\(cameras.cameras.count) camera\(cameras.cameras.count == 1 ? "" : "s"), \(cameras.onlineCount) online")
                    .font(.callout).foregroundStyle(.secondary)
                Spacer()
                if canManage {
                    Button { editing = .new } label: { Label("Add Camera…", systemImage: "plus") }
                }
                Button { Task { await cameras.reload(api: runtime.api) } } label: { Image(systemName: "arrow.clockwise") }
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
            }
            Table(cameras.cameras) {
                TableColumn("Camera") { camera in
                    VStack(alignment: .leading, spacing: 1) {
                        Text(camera.name).font(.callout.weight(.semibold))
                        if camera.location.isEmpty == false {
                            Text(camera.location).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
                TableColumn("Status") { camera in
                    Text(camera.status.message.isEmpty ? (camera.status.online ? "Online" : "Offline") : camera.status.message)
                        .foregroundStyle(camera.status.online ? Color.green : Color.red)
                }
                .width(min: 90, ideal: 130)
                TableColumn("Recording Server") { camera in
                    Text(cameras.recorderName(camera.recorderId))
                }
                .width(min: 90, ideal: 140)
                TableColumn("Groups") { camera in
                    Text(camera.groupIds.compactMap { id in cameras.groups.first { $0.id == id }?.name }.joined(separator: ", "))
                        .foregroundStyle(.secondary)
                }
                TableColumn("Recording") { camera in
                    Text(camera.recording ? "On\(camera.retentionDays > 0 ? " · \(camera.retentionDays) d" : "")" : "Off")
                }
                .width(min: 70, ideal: 90)
                TableColumn("") { camera in
                    HStack(spacing: 6) {
                        if canManage {
                            Button("Edit") { editing = .existing(camera) }
                            Button(role: .destructive) { deleting = camera } label: { Image(systemName: "trash") }
                        }
                    }
                    .controlSize(.small)
                }
                .width(canManage ? 110 : 10)
            }
            OpenInAdminCard(title: "Groups, discovery and storage",
                            detail: "Camera groups, ONVIF discovery, moving cameras between Recording Servers in bulk and retention per server are in Sentinel Admin.")
        }
        .padding(18)
        .sheet(item: $editing) { target in
            CameraEditSheet(target: target)
        }
        .confirmationDialog("Remove \(deleting?.name ?? "camera")?", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })) {
            Button("Remove Camera", role: .destructive) {
                guard let camera = deleting else { return }
                Task {
                    do {
                        try await runtime.api.deleteCamera(camera.id)
                        cameras.remove(camera.id)
                    } catch {
                        self.error = error.localizedDescription
                    }
                }
            }
        } message: {
            Text("Its recordings are kept on the Recording Server.")
        }
    }
}

enum CameraEditTarget: Identifiable {
    case new
    case existing(V2.Camera)

    var id: String {
        switch self {
        case .new: return "new"
        case .existing(let camera): return camera.id
        }
    }
}

private struct CameraEditSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    let target: CameraEditTarget

    @State private var name = ""
    @State private var location = ""
    @State private var rtspUrl = ""
    @State private var subRtspUrl = ""
    @State private var username = ""
    @State private var password = ""
    @State private var changePassword = false
    @State private var recording = true
    @State private var retentionDays = 0
    @State private var motionLevel = 2
    @State private var alertOn = V2.AlertOn.auto
    @State private var onvifUrl = ""
    @State private var cameraAnalytics = true
    @State private var recorderId = ""
    @State private var groupIds: Set<String> = []
    @State private var error: String?
    @State private var isSaving = false

    private var existing: V2.Camera? {
        if case .existing(let camera) = target { return camera }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(existing == nil ? "Add Camera" : "Edit \(existing!.name)").font(.headline)
            Form {
                TextField("Name", text: $name)
                TextField("Location", text: $location)
                TextField("RTSP URL", text: $rtspUrl, prompt: Text("rtsp://192.168.1.50:554/stream1"))
                TextField("Sub-stream RTSP URL (optional)", text: $subRtspUrl)
                TextField("User name", text: $username)
                if existing == nil || changePassword {
                    SecureField("Password", text: $password)
                } else {
                    Toggle(existing?.hasPassword == true ? "Change the saved password" : "Set a password", isOn: $changePassword)
                }
                TextField("ONVIF address (optional)", text: $onvifUrl)
                Toggle("Record", isOn: $recording)
                Stepper("Keep recordings: \(retentionDays == 0 ? "site default" : "\(retentionDays) days")", value: $retentionDays, in: 0...3650)
                Picker("Motion sensitivity", selection: $motionLevel) {
                    Text("Off").tag(0); Text("Low").tag(1); Text("Medium").tag(2); Text("High").tag(3)
                }
                Picker("Raise alarms for", selection: $alertOn) {
                    Text("Automatic").tag(V2.AlertOn.auto)
                    Text("Any motion").tag(V2.AlertOn.motion)
                    Text("People and vehicles").tag(V2.AlertOn.peopleAndVehicles)
                    Text("People").tag(V2.AlertOn.people)
                }
                Toggle("Use the camera's own analytics", isOn: $cameraAnalytics)
                if cameras.recorders.isEmpty == false {
                    Picker("Recording Server", selection: $recorderId) {
                        if existing == nil { Text("Default").tag("") }
                        ForEach(cameras.recorders.filter { $0.revoked == false }) { server in
                            Text(server.name + (server.online ? "" : " (offline)")).tag(server.id)
                        }
                    }
                }
                if cameras.groups.isEmpty == false {
                    Section("Groups") {
                        ForEach(cameras.groups) { group in
                            Toggle(group.name, isOn: Binding(get: { groupIds.contains(group.id) },
                                                             set: { if $0 { groupIds.insert(group.id) } else { groupIds.remove(group.id) } }))
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .frame(height: 440)
            if let existing, recorderId != existing.recorderId, recorderId.isEmpty == false {
                Text("Moving the camera: footage recorded so far stays on \(cameras.recorderName(existing.recorderId)) and stays playable.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button(existing == nil ? "Add" : "Save") { save() }
                    .buttonStyle(.borderedProminent)
                    .disabled(isSaving || name.trimmingCharacters(in: .whitespaces).isEmpty || rtspUrl.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 520)
        .onAppear(perform: fill)
    }

    private func fill() {
        guard let camera = existing else { return }
        name = camera.name
        location = camera.location
        rtspUrl = camera.rtspUrl
        subRtspUrl = camera.subRtspUrl
        username = camera.username
        recording = camera.recording
        retentionDays = camera.retentionDays
        motionLevel = camera.motionLevel
        alertOn = camera.alertOn
        onvifUrl = camera.onvifUrl
        cameraAnalytics = camera.cameraAnalytics
        recorderId = camera.recorderId
        groupIds = Set(camera.groupIds)
    }

    private func save() {
        isSaving = true
        error = nil
        Task {
            defer { isSaving = false }
            do {
                let saved: V2.Camera
                if let camera = existing {
                    var patch = V2.CameraPatch()
                    patch.name = name
                    patch.location = location
                    patch.rtspUrl = rtspUrl
                    patch.subRtspUrl = subRtspUrl
                    patch.username = username
                    if changePassword { patch.password = password }
                    patch.recording = recording
                    patch.retentionDays = retentionDays
                    patch.motionLevel = motionLevel
                    patch.alertOn = alertOn
                    patch.onvifUrl = onvifUrl
                    patch.cameraAnalytics = cameraAnalytics
                    if recorderId.isEmpty == false, recorderId != camera.recorderId { patch.recorderId = recorderId }
                    if Set(camera.groupIds) != groupIds { patch.groupIds = Array(groupIds).sorted() }
                    saved = try await runtime.api.updateCamera(camera.id, patch)
                } else {
                    saved = try await runtime.api.createCamera(V2.CameraInput(
                        name: name, location: location, rtspUrl: rtspUrl,
                        subRtspUrl: subRtspUrl.isEmpty ? nil : subRtspUrl,
                        username: username.isEmpty ? nil : username, password: password.isEmpty ? nil : password,
                        recording: recording, retentionDays: retentionDays, motionLevel: motionLevel, alertOn: alertOn,
                        onvifUrl: onvifUrl.isEmpty ? nil : onvifUrl, cameraAnalytics: cameraAnalytics,
                        recorderId: recorderId.isEmpty ? nil : recorderId,
                        groupIds: groupIds.isEmpty ? nil : Array(groupIds).sorted()
                    ))
                }
                cameras.replace(saved)
                await runtime.video.invalidate(cameraID: saved.id)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - Rules

struct RulesView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    @State private var rules: [V2.Rule] = []
    @State private var error: String?
    @State private var editing: RuleEditTarget?

    private var canEdit: Bool { session.can(.changeSettings) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("Rules decide which alarms notify whom and what operators are told to do.")
                    .font(.callout).foregroundStyle(.secondary)
                Spacer()
                if canEdit {
                    Button { editing = .new } label: { Label("New Rule…", systemImage: "plus") }
                }
                Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
            }
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
            ScrollView {
                VStack(spacing: 8) {
                    ForEach(rules) { rule in
                        RuleCard(rule: rule, cameraNames: rule.cameraIds.map { cameras.name(of: $0) }, canEdit: canEdit) {
                            editing = .existing(rule)
                        } toggle: { enabled in
                            var input = V2.RuleInput(rule)
                            input.enabled = enabled
                            Task { await save(input, id: rule.id) }
                        } delete: {
                            Task {
                                do {
                                    try await runtime.api.deleteRule(rule.id)
                                    rules.removeAll { $0.id == rule.id }
                                } catch { self.error = error.localizedDescription }
                            }
                        }
                    }
                    if rules.isEmpty {
                        EmptyStateLine(text: "No rules yet. Alarms still appear in the queue; rules add notifications and instructions.")
                    }
                }
            }
        }
        .padding(18)
        .task { await load() }
        .sheet(item: $editing) { target in
            RuleEditSheet(target: target) { input, id in
                await save(input, id: id)
            }
        }
    }

    private func load() async {
        do {
            rules = try await runtime.api.rules()
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    @discardableResult
    private func save(_ input: V2.RuleInput, id: String?) async -> String? {
        do {
            let saved = id == nil ? try await runtime.api.createRule(input) : try await runtime.api.replaceRule(id!, input)
            if let index = rules.firstIndex(where: { $0.id == saved.id }) { rules[index] = saved } else { rules.append(saved) }
            error = nil
            return nil
        } catch {
            self.error = error.localizedDescription
            return error.localizedDescription
        }
    }
}

enum RuleEditTarget: Identifiable {
    case new
    case existing(V2.Rule)

    var id: String {
        switch self {
        case .new: return "new"
        case .existing(let rule): return rule.id
        }
    }
}

private struct RuleCard: View {
    let rule: V2.Rule
    let cameraNames: [String]
    let canEdit: Bool
    let edit: () -> Void
    let toggle: (Bool) -> Void
    let delete: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text(rule.name).font(.callout.weight(.semibold))
                Text(summary).font(.caption).foregroundStyle(.secondary)
                if rule.instructions.isEmpty == false {
                    Text(rule.instructions).font(.caption).lineLimit(2)
                }
            }
            Spacer()
            if canEdit {
                Toggle("", isOn: Binding(get: { rule.enabled }, set: toggle)).toggleStyle(.switch).labelsHidden()
                Button("Edit", action: edit).controlSize(.small)
                Button(role: .destructive, action: delete) { Image(systemName: "trash") }.controlSize(.small)
            } else {
                Text(rule.enabled ? "On" : "Off").font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private var summary: String {
        var parts: [String] = []
        parts.append(rule.kinds.isEmpty ? "Any alarm" : rule.kinds.joined(separator: ", "))
        parts.append("\(rule.minSeverity) or worse")
        parts.append(cameraNames.isEmpty ? "every camera" : cameraNames.joined(separator: ", "))
        if rule.schedule {
            parts.append("\(RuleEditSheet.clock(rule.fromMinute))–\(RuleEditSheet.clock(rule.toMinute)) on \(RuleEditSheet.dayNames(rule.days))")
        }
        if rule.delivery.isEmpty == false { parts.append("→ \(rule.delivery)") }
        return parts.joined(separator: " · ")
    }
}

private struct RuleEditSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var cameras: CameraDirectory
    let target: RuleEditTarget
    let save: (V2.RuleInput, String?) async -> String?

    @State private var input = V2.RuleInput(name: "")
    @State private var kindsText = ""
    @State private var error: String?
    @State private var isSaving = false

    static let knownKinds = ["Motion", "Person", "Vehicle", "Animal", "Line crossing", "Camera Offline", "Storage"]
    static let dayLetters = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(existingID == nil ? "New Rule" : "Edit Rule").font(.headline)
            Form {
                TextField("Name", text: $input.name)
                Toggle("Enabled", isOn: $input.enabled)
                Section("Matches") {
                    TextField("Alarm kinds (comma-separated, empty = any)", text: $kindsText,
                              prompt: Text(Self.knownKinds.prefix(3).joined(separator: ", ")))
                    Picker("Minimum severity", selection: $input.minSeverity) {
                        Text("Info").tag("Info"); Text("Warning").tag("Warning"); Text("Critical").tag("Critical")
                    }
                    DisclosureGroup("Cameras (\(input.cameraIds.isEmpty ? "all" : "\(input.cameraIds.count)"))") {
                        ForEach(cameras.cameras) { camera in
                            Toggle(camera.name, isOn: Binding(
                                get: { input.cameraIds.contains(camera.id) },
                                set: { on in
                                    if on { input.cameraIds.append(camera.id) } else { input.cameraIds.removeAll { $0 == camera.id } }
                                }))
                        }
                    }
                }
                Section("When") {
                    Toggle("Only on a schedule", isOn: $input.schedule)
                    if input.schedule {
                        Stepper("From \(Self.clock(input.fromMinute))", value: $input.fromMinute, in: 0...1439, step: 15)
                        Stepper("To \(Self.clock(input.toMinute))", value: $input.toMinute, in: 0...1439, step: 15)
                        HStack {
                            ForEach(0..<7, id: \.self) { day in
                                Toggle(Self.dayLetters[day], isOn: Binding(
                                    get: { input.days.contains(day) },
                                    set: { on in
                                        if on { input.days.append(day); input.days.sort() } else { input.days.removeAll { $0 == day } }
                                    }))
                                .toggleStyle(.button)
                            }
                        }
                    }
                }
                Section("Response") {
                    TextField("Delivery (e.g. Desktop, Push, Email)", text: $input.delivery)
                    TextField("Instructions for operators", text: $input.instructions, axis: .vertical).lineLimit(3...8)
                }
            }
            .formStyle(.grouped)
            .frame(height: 460)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Save") {
                    isSaving = true
                    input.kinds = kindsText.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { $0.isEmpty == false }
                    let snapshot = input
                    Task {
                        defer { isSaving = false }
                        if let problem = await save(snapshot, existingID) { error = problem } else { dismiss() }
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(isSaving || input.name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 520)
        .onAppear {
            if case .existing(let rule) = target {
                input = V2.RuleInput(rule)
                kindsText = rule.kinds.joined(separator: ", ")
            }
        }
    }

    private var existingID: String? {
        if case .existing(let rule) = target { return rule.id }
        return nil
    }

    static func clock(_ minute: Int) -> String {
        String(format: "%02d:%02d", minute / 60, minute % 60)
    }

    static func dayNames(_ days: [Int]) -> String {
        days.isEmpty || days.count == 7 ? "every day" : days.sorted().map { dayLetters[$0 % 7] }.joined(separator: " ")
    }
}

// MARK: - Users

struct UsersAdminView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    @State private var users: [V2.User] = []
    @State private var error: String?
    @State private var editing: UserEditTarget?
    @State private var deleting: V2.User?

    var body: some View {
        Group {
            if session.can(.manageUsers) {
                VStack(alignment: .leading, spacing: 12) {
                    HStack {
                        Text("Changing a user's role, scope or password signs them out everywhere.")
                            .font(.callout).foregroundStyle(.secondary)
                        Spacer()
                        Button { editing = .new } label: { Label("New User…", systemImage: "person.badge.plus") }
                        Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                    }
                    if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                    Table(users) {
                        TableColumn("Name") { user in Text(user.name).font(.callout.weight(.semibold)) }
                        TableColumn("Role") { user in Text(user.role.rawValue) }.width(min: 80, ideal: 100)
                        TableColumn("Cameras") { user in Text(scopeText(user)).foregroundStyle(.secondary) }
                        TableColumn("Sign-in") { user in Text(user.source == "local" ? "Local" : user.source.uppercased()) }.width(min: 60, ideal: 70)
                        TableColumn("Since") { user in Text(OperatorFormat.dateTime.string(from: Date(unixMillis: user.createdAt))).foregroundStyle(.secondary) }
                        TableColumn("") { user in
                            HStack(spacing: 6) {
                                Button("Edit") { editing = .existing(user) }
                                Button(role: .destructive) { deleting = user } label: { Image(systemName: "trash") }
                                    .disabled(user.id == session.me?.id)
                            }
                            .controlSize(.small)
                        }
                        .width(110)
                    }
                    OpenInAdminCard(title: "Directory sign-in",
                                    detail: "LDAP / Active Directory and single sign-on, and which directory groups map to which role, are set up in Sentinel Admin.")
                }
                .padding(18)
                .task { await load() }
            } else {
                PermissionNote(text: "Only Admins manage users.")
            }
        }
        .sheet(item: $editing) { target in
            UserEditSheet(target: target) { await load() }
        }
        .confirmationDialog("Delete \(deleting?.name ?? "user")?", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })) {
            Button("Delete User", role: .destructive) {
                guard let user = deleting else { return }
                Task {
                    do {
                        try await runtime.api.deleteUser(user.id)
                        users.removeAll { $0.id == user.id }
                    } catch { self.error = error.localizedDescription }
                }
            }
        }
    }

    private func scopeText(_ user: V2.User) -> String {
        if user.role == .admin || user.scope.allCameras { return "All cameras" }
        let names = user.scope.groupIds.compactMap { id in cameras.groups.first { $0.id == id }?.name }
        return names.isEmpty ? "None" : names.joined(separator: ", ")
    }

    private func load() async {
        do {
            users = try await runtime.api.users().sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}

enum UserEditTarget: Identifiable {
    case new
    case existing(V2.User)

    var id: String {
        switch self {
        case .new: return "new"
        case .existing(let user): return user.id
        }
    }
}

private struct UserEditSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    let target: UserEditTarget
    let saved: () async -> Void

    @State private var name = ""
    @State private var role = V2.Role.operator
    @State private var password = ""
    @State private var allCameras = true
    @State private var groupIds: Set<String> = []
    @State private var error: String?
    @State private var isSaving = false

    private var existing: V2.User? {
        if case .existing(let user) = target { return user }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(existing == nil ? "New User" : "Edit \(existing!.name)").font(.headline)
            Form {
                if existing == nil {
                    TextField("Name", text: $name)
                }
                Picker("Role", selection: $role) {
                    ForEach(V2.Role.all, id: \.self) { Text($0.rawValue).tag($0) }
                }
                if existing == nil || existing?.source == "local" {
                    SecureField(existing == nil ? "Password (8+ characters)" : "New password (leave empty to keep)", text: $password)
                }
                if role != .admin {
                    Section("Cameras") {
                        Toggle("All cameras", isOn: $allCameras)
                        if allCameras == false {
                            if cameras.groups.isEmpty {
                                Text("Create camera groups in Sentinel Admin to limit a user to some cameras.")
                                    .font(.caption).foregroundStyle(.secondary)
                            }
                            ForEach(cameras.groups) { group in
                                Toggle(group.name, isOn: Binding(get: { groupIds.contains(group.id) },
                                                                 set: { if $0 { groupIds.insert(group.id) } else { groupIds.remove(group.id) } }))
                            }
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .frame(height: 320)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Save") { save() }
                    .buttonStyle(.borderedProminent)
                    .disabled(isSaving || (existing == nil && (name.isEmpty || password.count < 8)) || (password.isEmpty == false && password.count < 8))
            }
        }
        .padding(20)
        .frame(width: 440)
        .onAppear {
            if let user = existing {
                role = user.role
                allCameras = user.scope.allCameras
                groupIds = Set(user.scope.groupIds)
            }
        }
    }

    private func save() {
        isSaving = true
        error = nil
        let scope = V2.UserScope(allCameras: role == .admin || allCameras, groupIds: allCameras ? [] : Array(groupIds).sorted())
        Task {
            defer { isSaving = false }
            do {
                if let user = existing {
                    var patch = V2.UserPatch()
                    if role != user.role { patch.role = role }
                    if password.isEmpty == false { patch.password = password }
                    if scope != user.scope { patch.scope = scope }
                    _ = try await runtime.api.updateUser(user.id, patch)
                } else {
                    _ = try await runtime.api.createUser(V2.NewUser(name: name, role: role, password: password, scope: scope))
                }
                await saved()
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - Recording servers

struct RecordingServersView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("\(cameras.recorders.count) Recording Server\(cameras.recorders.count == 1 ? "" : "s")")
                    .font(.callout).foregroundStyle(.secondary)
                Spacer()
                Button { Task { await cameras.reloadRecorders(api: runtime.api) } } label: { Image(systemName: "arrow.clockwise") }
            }
            ScrollView {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 320), spacing: 12)], spacing: 12) {
                    ForEach(cameras.recorders) { server in
                        RecorderCard(server: server)
                    }
                }
            }
            OpenInAdminCard(title: "Enroll, rename or revoke Recording Servers",
                            detail: "Adding a Recording Server takes a one-time enrollment token from Sentinel Admin, run on the new machine.")
        }
        .padding(18)
        .task { await cameras.reloadRecorders(api: runtime.api) }
    }
}

private struct RecorderCard: View {
    let server: V2.RecordingServer

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Image(systemName: "server.rack").foregroundStyle(server.online ? Color.green : Color.red)
                Text(server.name).font(.callout.weight(.semibold))
                if server.local { MetricPill(text: "Built in") }
                Spacer()
                Text(server.revoked ? "Revoked" : server.online ? "Online" : "Offline")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(server.revoked ? Color.secondary : server.online ? Color.green : Color.red)
            }
            DetailRow(label: "Cameras", value: "\(server.cameras)")
            DetailRow(label: "Version", value: server.version.isEmpty ? "—" : server.version)
            DetailRow(label: "Media engine", value: server.mediaRunning ? "Running" : (server.mediaError ?? "Stopped"))
            if let storage = server.storage {
                DetailRow(label: "Recordings", value: OperatorFormat.bytes(storage.recordingsBytes))
                if let free = storage.diskFreeBytes, let total = storage.diskTotalBytes, total > 0 {
                    MeterBar(value: 1 - Double(free) / Double(total), tint: Double(free) / Double(total) < 0.1 ? .red : SentinelTheme.accent)
                    Text("\(OperatorFormat.bytes(free)) free of \(OperatorFormat.bytes(total))").font(.caption2).foregroundStyle(.secondary)
                }
            }
            DetailRow(label: "Video address", value: server.publicUrl.isEmpty ? "Through the Management Server" : server.publicUrl)
            HStack(spacing: 6) {
                if server.capabilities.detection { MetricPill(text: "Motion") }
                if server.capabilities.objects { MetricPill(text: "Objects") }
                if server.capabilities.webrtc { MetricPill(text: "WebRTC") }
            }
            if server.online == false, server.lastSeenAt > 0 {
                Text("Last seen \(OperatorFormat.relative(server.lastSeenAt))").font(.caption2).foregroundStyle(.secondary)
            }
        }
        .padding(14)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }
}

// MARK: - Audit

struct AuditLogV2View: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    @State private var entries: [V2.AuditEntry] = []
    @State private var cursor: String?
    @State private var serverFilter = "*"
    @State private var error: String?
    @State private var verify: V2.AuditVerify?
    @State private var isLoading = false

    var body: some View {
        Group {
            if session.can(.viewAudit) {
                VStack(alignment: .leading, spacing: 12) {
                    HStack {
                        Picker("Source", selection: $serverFilter) {
                            Text("Whole site").tag("*")
                            Text("Management Server").tag("")
                            ForEach(cameras.recorders.filter { $0.local == false }) { Text($0.name).tag($0.id) }
                        }
                        .frame(maxWidth: 280)
                        Spacer()
                        if let verify {
                            Label(verify.ok ? "Chain intact (\(verify.entries))" : "Chain broken at \(verify.brokenAt ?? "?")",
                                  systemImage: verify.ok ? "checkmark.seal.fill" : "xmark.seal.fill")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(verify.ok && verify.servers.allSatisfy(\.ok) ? Color.green : Color.red)
                        }
                        Button("Verify Chain") {
                            Task {
                                do { verify = try await runtime.api.verifyAudit() } catch { self.error = error.localizedDescription }
                            }
                        }
                        Button { Task { await load(more: false) } } label: { Image(systemName: "arrow.clockwise") }
                    }
                    .controlSize(.small)
                    if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                    Table(entries) {
                        TableColumn("Time") { entry in Text(OperatorFormat.dateTime.string(from: Date(unixMillis: entry.at))).monospacedDigit() }
                            .width(min: 140, ideal: 170)
                        TableColumn("User") { entry in Text(entry.user) }.width(min: 80, ideal: 120)
                        TableColumn("Area") { entry in Text(entry.area) }.width(min: 70, ideal: 100)
                        TableColumn("Action") { entry in Text(entry.action).fontWeight(.semibold) }
                        TableColumn("Detail") { entry in Text(entry.detail).foregroundStyle(.secondary).help(entry.detail) }
                        TableColumn("Server") { entry in Text(entry.serverId.isEmpty ? "Management" : cameras.recorderName(entry.serverId)) }
                            .width(min: 70, ideal: 100)
                    }
                    if cursor != nil {
                        Button(isLoading ? "Loading…" : "Load Older Entries") { Task { await load(more: true) } }
                            .disabled(isLoading)
                    }
                }
                .padding(18)
                .task(id: serverFilter) { await load(more: false) }
            } else {
                PermissionNote(text: "Your role can't view the audit log.")
            }
        }
    }

    private func load(more: Bool) async {
        isLoading = true
        defer { isLoading = false }
        do {
            let page = try await runtime.api.audit(serverID: serverFilter == "*" ? nil : serverFilter,
                                                   cursor: more ? cursor : nil, limit: 200)
            entries = more ? entries + page.items : page.items
            cursor = page.nextCursor
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}
