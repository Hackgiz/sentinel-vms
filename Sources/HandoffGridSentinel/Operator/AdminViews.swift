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
    @State private var showsUseIPhone = false // wave 23
    @ObservedObject private var links = ManagementLinks.shared

    private var canManage: Bool { session.can(.manageCameras) }

    /// Insights' "Open camera" lands here: open that camera's settings once.
    private func openLinkedCamera() {
        guard canManage, let id = links.cameraID, let camera = cameras.camera(id) else { return }
        links.cameraID = nil
        editing = .existing(camera)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("\(cameras.cameras.count) camera\(cameras.cameras.count == 1 ? "" : "s"), \(cameras.onlineCount) online")
                    .font(.callout).foregroundStyle(.secondary)
                Spacer()
                if canManage {
                    Menu {
                        Button("RTSP or ONVIF Camera…") { editing = .new }
                        Button("Use an iPhone… (\(PhoneCameraLogic.cloudNote.dropLast()))") { showsUseIPhone = true } // wave 23, addendum 2
                    } label: {
                        Label("Add Camera…", systemImage: "plus")
                    } primaryAction: {
                        editing = .new
                    }
                    .fixedSize()
                    .help("Add a network camera, or use an iPhone as a camera")
                }
                Button { Task { await cameras.reload(api: runtime.api) } } label: { Image(systemName: "arrow.clockwise") }
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
            }
            Table(cameras.cameras) {
                TableColumn("Camera") { camera in
                    VStack(alignment: .leading, spacing: 1) {
                        HStack(spacing: 4) {
                            Text(camera.name).font(.callout.weight(.semibold))
                            if camera.isPhone { PhoneCameraBadge(onVideo: false) } // wave 23
                        }
                        if camera.location.isEmpty == false {
                            Text(camera.location).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
                TableColumn("Status") { camera in
                    if camera.isPhone { // wave 23: "iPhone camera · Streaming / Not streaming · last seen"
                        Text(PhoneCameraLogic.summary(camera))
                            .foregroundStyle(PhoneCameraLogic.needsCloud(camera) ? Color.orange : camera.status.online ? Color.green : Color.red)
                            .help(PhoneCameraLogic.statusMessage(camera))
                    } else {
                        Text(camera.status.message.isEmpty ? (camera.status.online ? "Online" : "Offline") : camera.status.message)
                            .foregroundStyle(camera.status.online ? Color.green : Color.red)
                    }
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
                    Text(RecordingModeLogic.listLabel(camera)) // wave 22: 24/7 / Events / 24/7 low + events / Live only
                        .help(RecordingModeLogic.label(RecordingModeLogic.effective(camera)))
                }
                .width(min: 70, ideal: 120)
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
        .onAppear(perform: openLinkedCamera)
        .onChange(of: cameras.cameras.map(\.id)) { _ in openLinkedCamera() }
        .sheet(item: $editing) { target in
            CameraEditSheet(target: target)
        }
        .sheet(isPresented: $showsUseIPhone) { UseIPhoneSheet() }
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
    @State private var audio = AudioDetectionSettings.Form() // WAVE11(E): audio detection settings
    @State private var loiterSeconds = 0 // wave 17
    @State private var recordingForm = RecordingModeLogic.Form() // wave 22
    @State private var recordingDefaults: V2.RecordingSettings?
    /// Wave 22: the server knows recording modes (else the plain Record toggle).
    @State private var modesSupported = false
    @State private var error: String?
    @State private var isSaving = false
    @State private var showsStreamSettings = false
    @State private var showsStreamFinder = false // wave 22 addendum
    /// False once the server answered 404 to Find streams (older than the addendum).
    @State private var discoverSupported = true
    @State private var showsUseIPhone = false // wave 23

    private var existing: V2.Camera? {
        if case .existing(let camera) = target { return camera }
        return nil
    }

    /// Wave 23: an iPhone camera has no RTSP/ONVIF/second stream.
    private var isPhone: Bool { PhoneCameraLogic.isPhone(existing) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(existing == nil ? "Add Camera" : "Edit \(existing!.name)").font(.headline)
            Form {
                TextField("Name", text: $name)
                TextField("Location", text: $location)
                if let existing, isPhone {
                    PhoneCameraKeySection(camera: existing) // wave 23
                } else {
                    TextField("RTSP URL", text: $rtspUrl, prompt: Text("rtsp://192.168.1.50:554/stream1"))
                    SecondStreamField(url: $subRtspUrl, canFind: canFindStreams, isSaved: existing != nil,
                                      serverSupportsDiscover: discoverSupported) { showsStreamFinder = true }
                    TextField("User name", text: $username)
                    if existing == nil || changePassword {
                        SecureField("Password", text: $password)
                    } else {
                        Toggle(existing?.hasPassword == true ? "Change the saved password" : "Set a password", isOn: $changePassword)
                    }
                    TextField("ONVIF address (optional)", text: $onvifUrl)
                }
                if existing == nil {
                    HStack(spacing: 6) {
                        Button("Adding an iPhone? Use an iPhone…") { showsUseIPhone = true } // wave 23
                            .buttonStyle(.link).font(.caption)
                        NeedsCloudNote() // addendum 2
                    }
                }
                if modesSupported == false { // a server older than wave 22
                    Toggle("Record", isOn: $recording)
                    Stepper("Keep recordings: \(retentionDays == 0 ? "site default" : "\(retentionDays) days")", value: $retentionDays, in: 0...3650)
                }
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
                if existing != nil { AudioDetectionFields(form: $audio) } // WAVE11(E): audio detection settings
                LoiterFields(seconds: $loiterSeconds) // wave 17
                if modesSupported { // wave 22
                    CameraRecordingFields(form: $recordingForm, retentionDays: $retentionDays,
                                          hasSubStream: subRtspUrl.trimmingCharacters(in: .whitespaces).isEmpty == false,
                                          defaults: recordingDefaults, motionLevel: motionLevel, cameraAnalytics: cameraAnalytics,
                                          findStreams: canFindStreams ? { showsStreamFinder = true } : nil,
                                          isPhone: isPhone)
                }
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
            .frame(height: 500)
            if let existing, recorderId != existing.recorderId, recorderId.isEmpty == false {
                Text("Moving the camera: footage recorded so far stays on \(cameras.recorderName(existing.recorderId)) and stays playable.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if let existing, isPhone == false { // Stream settings hook (wave 5): needs the saved ONVIF address.
                    Button("Stream Settings…") { showsStreamSettings = true }
                        .disabled(existing.onvifUrl.isEmpty).help(existing.onvifUrl.isEmpty ? "Add an ONVIF address and save first" : "Resolution, frame rate, bitrate…")
                        .sheet(isPresented: $showsStreamSettings) { StreamSettingsSheet(camera: existing) }
                }
                Spacer()
                Button("Cancel") { dismiss() }
                Button(existing == nil ? "Add" : "Save") { save() }
                    .buttonStyle(.borderedProminent)
                    .disabled(isSaving || PhoneCameraLogic.canSave(name: name, rtspUrl: rtspUrl, isPhone: isPhone) == false)
            }
        }
        .padding(20)
        .frame(width: 560)
        .onAppear(perform: fill)
        .task { await loadRecordingDefaults() }
        .sheet(isPresented: $showsUseIPhone) { UseIPhoneSheet() }
        .sheet(isPresented: $showsStreamFinder) {
            if let existing {
                let login = StreamFinderLogic.credentials(username: username, typedPassword: changePassword ? password : nil)
                StreamFinderSheet(api: runtime.api, cameraID: existing.id, cameraName: existing.name,
                                  mainURL: rtspUrl, currentSub: subRtspUrl,
                                  username: login.username, password: login.password,
                                  onPick: { subRtspUrl = $0 },
                                  onOlderServer: { discoverSupported = false })
            }
        }
    }

    /// Wave 22 addendum: Find streams needs a saved camera and a server that has the endpoint.
    private var canFindStreams: Bool {
        StreamFinderLogic.canFind(isSaved: existing != nil, serverSupportsDiscover: discoverSupported)
    }

    /// Wave 22: the server defaults label "Server default (…)"; a 404 means an older
    /// server, so the editor keeps the plain Record toggle.
    private func loadRecordingDefaults() async {
        do {
            recordingDefaults = try await runtime.api.recordingSettings()
            modesSupported = true
        } catch {
            // 403 (no changeSettings) still means a wave 22 server; only a 404 is an older one.
            let notFound = (error as? SentinelAPIError)?.isNotFound == true
            modesSupported = RecordingModeLogic.supportsModes(existing) || notFound == false
        }
    }

    private func fill() {
        modesSupported = RecordingModeLogic.supportsModes(existing)
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
        audio = AudioDetectionSettings.form(of: camera)
        loiterSeconds = LoiterSettings.seconds(of: camera)
        recordingForm = RecordingModeLogic.form(of: camera)
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
                    if modesSupported {
                        RecordingModeLogic.merge(camera, recordingForm, into: &patch) // wave 22
                    } else {
                        patch.recording = recording
                    }
                    patch.retentionDays = retentionDays
                    patch.motionLevel = motionLevel
                    patch.alertOn = alertOn
                    patch.onvifUrl = onvifUrl
                    patch.cameraAnalytics = cameraAnalytics
                    if recorderId.isEmpty == false, recorderId != camera.recorderId { patch.recorderId = recorderId }
                    if Set(camera.groupIds) != groupIds { patch.groupIds = Array(groupIds).sorted() }
                    AudioDetectionSettings.merge(AudioDetectionSettings.patch(camera, audio), into: &patch)
                    LoiterSettings.merge(camera, loiterSeconds, into: &patch)
                    if camera.isPhone { // wave 23: no RTSP/ONVIF/login, and never dual
                        PhoneCameraLogic.stripNetworkFields(&patch)
                        if let mode = patch.recordingMode { patch.recordingMode = PhoneCameraLogic.allowedMode(mode) }
                    }
                    saved = try await runtime.api.updateCamera(camera.id, patch)
                } else {
                    var input = V2.CameraInput(
                        name: name, location: location, rtspUrl: rtspUrl,
                        subRtspUrl: subRtspUrl.isEmpty ? nil : subRtspUrl,
                        username: username.isEmpty ? nil : username, password: password.isEmpty ? nil : password,
                        recording: modesSupported ? nil : recording, retentionDays: retentionDays, motionLevel: motionLevel, alertOn: alertOn,
                        onvifUrl: onvifUrl.isEmpty ? nil : onvifUrl, cameraAnalytics: cameraAnalytics,
                        recorderId: recorderId.isEmpty ? nil : recorderId,
                        groupIds: groupIds.isEmpty ? nil : Array(groupIds).sorted(),
                        loiterSeconds: loiterSeconds > 0 ? loiterSeconds : nil
                    )
                    if modesSupported { RecordingModeLogic.apply(recordingForm, to: &input) } // wave 22
                    saved = try await runtime.api.createCamera(input)
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
    @State private var loaded = false
    @State private var error: String?
    @State private var editing: RuleEditTarget?
    @State private var describe = ""
    @State private var drafting = false
    @State private var draftError: String?
    @State private var templates: [V2.RuleTemplate] = []
    @State private var templatesError: String?
    @State private var templatesOpen: Bool?
    @ObservedObject private var links = ManagementLinks.shared

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
            if canEdit {
                describeBox
                templateGallery
            }
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
        .task { if canEdit { await loadTemplates() } }
        .onChange(of: links.templateID) { _ in openLinkedTemplate() }
        .sheet(item: $editing) { target in
            RuleEditSheet(target: target) { input, id in
                let problem = await save(input, id: id)
                if problem == nil, case .prefilled(let p) = target, p.explanation != nil { describe = "" }
                return problem
            }
        }
    }

    // MARK: Describe a rule (AI draft) and templates

    private var describeBox: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Describe a rule").font(.headline)
            HStack(alignment: .top, spacing: 8) {
                TextField("What should raise an alarm?", text: $describe,
                          prompt: Text("e.g. Tell me about people at the loading dock after 6pm"), axis: .vertical)
                    .lineLimit(1...4)
                    .textFieldStyle(.roundedBorder)
                    .disabled(drafting)
                    .onChange(of: describe) { text in
                        if text.count > RuleCatalog.draftMaxLength { describe = String(text.prefix(RuleCatalog.draftMaxLength)) }
                    }
                    .onSubmit { Task { await draft() } }
                Button { Task { await draft() } } label: {
                    if drafting { ProgressView().controlSize(.small) } else { Text("Draft rule") }
                }
                .buttonStyle(.borderedProminent)
                .disabled(drafting || RuleCatalog.canDraft(describe) == false)
            }
            Text("Sentinel drafts a rule for you to check. Nothing is saved until you save it. Uses one AI request.")
                .font(.caption).foregroundStyle(.secondary)
            if let draftError { Text(draftError).font(.caption.weight(.semibold)).foregroundStyle(.red) }
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private var templateGallery: some View {
        DisclosureGroup(isExpanded: Binding(get: { templatesOpen ?? (loaded && rules.isEmpty) },
                                            set: { templatesOpen = $0 })) {
            VStack(alignment: .leading, spacing: 10) {
                if let templatesError { Text(templatesError).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                ForEach(RuleCatalog.groupTemplates(templates)) { group in
                    Text(group.label).font(.subheadline.weight(.semibold)).foregroundStyle(.secondary)
                    ScrollView(.horizontal) {
                        HStack(alignment: .top, spacing: 10) {
                            ForEach(group.items) { template in
                                RuleTemplateCard(template: template) { editing = .prefilled(PrefilledRule(template: template)) }
                            }
                        }
                        .padding(.bottom, 4)
                    }
                }
            }
            .padding(.top, 6)
        } label: {
            Text("Start from a template").font(.headline)
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private func draft() async {
        guard RuleCatalog.canDraft(describe), drafting == false else { return }
        drafting = true
        draftError = nil
        defer { drafting = false }
        do {
            // The draft comes back off and unsaved; the editor's Save is what saves it.
            let draft = try await runtime.api.draftRule(describe.trimmingCharacters(in: .whitespacesAndNewlines))
            editing = .prefilled(PrefilledRule(draft: draft))
        } catch {
            draftError = AIFailure(error).message
        }
    }

    private func loadTemplates() async {
        do {
            templates = try await runtime.api.ruleTemplates()
            templatesError = nil
            openLinkedTemplate()
        } catch {
            templatesError = error.localizedDescription
        }
    }

    /// Insights' "Use template" lands here: open that template once.
    private func openLinkedTemplate() {
        guard canEdit, let id = links.templateID, let template = templates.first(where: { $0.id == id }) else { return }
        links.templateID = nil
        editing = .prefilled(PrefilledRule(template: template))
    }

    private func load() async {
        do {
            rules = try await runtime.api.rules()
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
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
    /// A new rule from a draft or a template; not saved until the admin saves.
    case prefilled(PrefilledRule)

    var id: String {
        switch self {
        case .new: return "new"
        case .existing(let rule): return rule.id
        case .prefilled(let p): return p.id
        }
    }
}

/// What the editor opens with for a draft or a template.
struct PrefilledRule {
    let id = UUID().uuidString
    var rule: V2.RuleInput
    /// The draft's one-sentence explanation; nil for a template.
    var explanation: String?
    var warnings: [String] = []

    init(draft: V2.RuleDraft) {
        rule = draft.rule
        explanation = draft.explanation
        warnings = draft.warnings
    }

    init(template: V2.RuleTemplate) {
        rule = template.rule
    }
}

private struct RuleTemplateCard: View {
    let template: V2.RuleTemplate
    let use: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(template.title).font(.callout.weight(.semibold))
            Text(template.summary).font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Text(pills).font(.caption2).foregroundStyle(.secondary)
            Spacer(minLength: 0)
            Button("Use template", action: use).controlSize(.small)
        }
        .padding(10)
        .frame(width: 230, alignment: .topLeading)
        .frame(minHeight: 130, alignment: .topLeading)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private var pills: String {
        let rule = template.rule
        var parts = [rule.kinds.isEmpty ? "Any alarm" : rule.kinds.map(InsightsLogic.kindLabel).joined(separator: ", "),
                     rule.minSeverity == "Info" ? "Any severity" : "\(rule.minSeverity)+"]
        if rule.schedule { parts.append("\(RuleEditSheet.clock(rule.fromMinute))–\(RuleEditSheet.clock(rule.toMinute))") }
        parts.append(rule.delivery == "push" ? "Operators + phone push" : "Operators only")
        return parts.joined(separator: " · ")
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

    static let knownKinds = RuleCatalog.kinds
    static let dayLetters = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(existingID == nil ? "New Rule" : "Edit Rule").font(.headline)
            if let prefill, let explanation = prefill.explanation {
                VStack(alignment: .leading, spacing: 4) {
                    Text(explanation).font(.callout)
                    ForEach(prefill.warnings, id: \.self) { warning in
                        Label(warning, systemImage: "exclamationmark.triangle.fill")
                            .font(.caption).foregroundStyle(SentinelTheme.amber)
                    }
                    Text("Check the rule below. Nothing is saved until you save it.").font(.caption).foregroundStyle(.secondary)
                }
            }
            Form {
                TextField("Name", text: $input.name)
                Toggle("Enabled", isOn: $input.enabled)
                Section("Matches") {
                    TextField("Alarm kinds (comma-separated, empty = any)", text: $kindsText,
                              prompt: Text(Self.knownKinds.prefix(3).joined(separator: ", ")))
                    Picker("Minimum severity", selection: $input.minSeverity) {
                        Text("Info").tag("Info"); Text("Warning").tag("Warning"); Text("Critical").tag("Critical")
                    }
                    if prefill != nil, input.cameraIds.isEmpty {
                        Text("All cameras: applies to every camera. Choose cameras to narrow it.")
                            .font(.caption).foregroundStyle(.secondary)
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
            switch target {
            case .existing(let rule):
                input = V2.RuleInput(rule)
                kindsText = rule.kinds.joined(separator: ", ")
            case .prefilled(let p):
                input = p.rule
                kindsText = p.rule.kinds.joined(separator: ", ")
            case .new:
                break
            }
        }
    }

    private var prefill: PrefilledRule? {
        if case .prefilled(let p) = target { return p }
        return nil
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
