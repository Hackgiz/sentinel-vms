import SwiftUI
import SentinelCore

// MARK: - Time Profiles

/// Paint-by-drag weekly grid: a row per day, a column per half hour.
struct WeeklyGridView: View {
    @Binding var grid: WeeklyGridModel
    @State private var painting: Bool?

    private let labelWidth: CGFloat = 36
    private let rowHeight: CGFloat = 22

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 0) {
                Color.clear.frame(width: labelWidth, height: 12)
                GeometryReader { geo in
                    ZStack(alignment: .topLeading) {
                        ForEach(Array(stride(from: 0, to: WeeklyGridModel.cellsPerDay, by: 4)), id: \.self) { slot in
                            Text(String(slot / 2)).font(.system(size: 9)).foregroundStyle(.secondary)
                                .offset(x: geo.size.width * CGFloat(slot) / CGFloat(WeeklyGridModel.cellsPerDay))
                        }
                    }
                }
                .frame(height: 12)
            }
            HStack(spacing: 0) {
                VStack(spacing: 1) {
                    ForEach(0..<7, id: \.self) { d in
                        Text(String(WeeklyGridModel.dayNames[d].prefix(3))).font(.caption).foregroundStyle(.secondary)
                            .frame(width: labelWidth, height: rowHeight, alignment: .leading)
                    }
                }
                GeometryReader { geo in
                    let cellW = geo.size.width / CGFloat(WeeklyGridModel.cellsPerDay)
                    VStack(spacing: 1) {
                        ForEach(0..<7, id: \.self) { d in
                            HStack(spacing: 0) {
                                ForEach(0..<WeeklyGridModel.cellsPerDay, id: \.self) { s in
                                    Rectangle()
                                        .fill(grid[d, s] ? Color.accentColor : Color.secondary.opacity(s % 2 == 0 ? 0.14 : 0.08))
                                        .frame(width: cellW, height: rowHeight)
                                        .overlay(alignment: .leading) {
                                            if s % 2 == 0 { Rectangle().fill(Color.secondary.opacity(0.25)).frame(width: 0.5) }
                                        }
                                }
                            }
                        }
                    }
                    .contentShape(Rectangle())
                    .gesture(DragGesture(minimumDistance: 0)
                        .onChanged { value in
                            let day = min(6, max(0, Int(value.location.y / (rowHeight + 1))))
                            let slot = min(WeeklyGridModel.cellsPerDay - 1, max(0, Int(value.location.x / cellW)))
                            if painting == nil { painting = !grid[day, slot] }
                            grid[day, slot] = painting ?? true
                        }
                        .onEnded { _ in painting = nil })
                }
                .frame(height: 7 * (rowHeight + 1))
            }
        }
        .accessibilityLabel("Weekly schedule grid")
    }
}

/// Time Profiles: named weekly schedules rules can be limited to.
struct TimeProfilesPane: View {
    let api: SentinelAPI

    @State private var profiles: [V2.TimeProfile] = []
    @State private var selection: String?
    @State private var creating = false
    @State private var name = ""
    @State private var grid = WeeklyGridModel()
    @State private var baseline: (name: String, cells: [[Bool]]) = ("", WeeklyGridModel().cells)
    @State private var error: String?
    @State private var status: String?
    @State private var busy = false
    @State private var confirmDelete: V2.TimeProfile?

    private var dirty: Bool { name != baseline.name || grid.cells != baseline.cells }
    private var blocker: String? { TimeProfileRules.blocker(name: name, grid: grid) }

    var body: some View {
        PaneFrame(title: "Time Profiles", subtitle: "Weekly schedules. A rule with a profile only fires inside it (server local time).",
                  error: error, status: status, trailing: {
            Button { startNew() } label: { Label("New Profile", systemImage: "plus") }
        }) {
            HSplitView {
                List(selection: Binding(get: { selection }, set: { select($0) })) {
                    ForEach(profiles) { p in
                        Label(p.name, systemImage: "calendar.badge.clock").tag(p.id)
                    }
                }
                .frame(minWidth: 180, idealWidth: 210, maxWidth: 280)
                editor.frame(minWidth: 460)
            }
        }
        .task { await load() }
        .confirmationDialog("Delete \(confirmDelete?.name ?? "profile")?", isPresented: Binding(
            get: { confirmDelete != nil }, set: { if $0 == false { confirmDelete = nil } })) {
            Button("Delete Profile", role: .destructive) {
                if let p = confirmDelete { Task { await delete(p) } }
            }
        } message: {
            Text("Rules that use it must be changed first.")
        }
    }

    @ViewBuilder private var editor: some View {
        if selection == nil && creating == false {
            Text("Select a profile, or create one.").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            VStack(alignment: .leading, spacing: 12) {
                TextField("Name", text: $name)
                WeeklyGridView(grid: $grid)
                HStack {
                    Text(String(format: "%.1f hours per week. Drag to paint; start on an empty cell to erase.", grid.hoursPerWeek))
                        .font(.caption).foregroundStyle(.secondary)
                    Spacer()
                }
                if grid.widenedOnLoad {
                    Text("This profile has times between half hours; saving rounds them out to the grid.")
                        .font(.caption).foregroundStyle(.orange)
                }
                HStack {
                    Button("Weekdays 9–17") { grid.setRange(days: [1, 2, 3, 4, 5], startSlot: 18, endSlot: 34) }
                    Button("All day") { grid.setRange(days: Array(0..<7), startSlot: 0, endSlot: 48) }
                    Button("Clear") { grid.clear() }
                    Spacer()
                    if let blocker, dirty { Text(blocker).font(.caption).foregroundStyle(.secondary) }
                    if let p = profiles.first(where: { $0.id == selection }) {
                        Button("Delete…", role: .destructive) { confirmDelete = p }.disabled(busy)
                    }
                    Button("Revert") { revert() }.disabled(dirty == false || busy)
                    Button(creating ? "Create" : "Save") { Task { await save() } }
                        .buttonStyle(.borderedProminent).disabled(dirty == false || blocker != nil || busy)
                }
                Spacer(minLength: 0)
            }
            .padding(14)
        }
    }

    private func load() async {
        do {
            profiles = try await api.timeProfiles()
            error = nil
            if let selection, let p = profiles.first(where: { $0.id == selection }) { adopt(p) }
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func adopt(_ p: V2.TimeProfile?) {
        name = p?.name ?? ""
        grid = WeeklyGridModel(windows: p?.weekly ?? [])
        baseline = (name, grid.cells)
    }

    private func select(_ id: String?) {
        selection = id
        creating = false
        status = nil
        adopt(profiles.first { $0.id == id })
    }

    private func startNew() {
        selection = nil
        creating = true
        status = nil
        adopt(nil)
    }

    private func revert() {
        adopt(profiles.first { $0.id == selection })
    }

    private func save() async {
        busy = true
        defer { busy = false }
        let input = V2.TimeProfileInput(name: name.trimmingCharacters(in: .whitespacesAndNewlines), weekly: grid.windows())
        do {
            let result: V2.TimeProfile
            if let selection, creating == false {
                result = try await api.updateTimeProfile(selection, input)
            } else {
                result = try await api.createTimeProfile(input)
            }
            creating = false
            selection = result.id
            status = "Saved \(result.name)."
            error = nil
            await load()
            adopt(profiles.first { $0.id == result.id } ?? result)
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func delete(_ p: V2.TimeProfile) async {
        confirmDelete = nil
        busy = true
        defer { busy = false }
        do {
            try await api.deleteTimeProfile(p.id)
            selection = nil
            creating = false
            adopt(nil)
            status = "Deleted \(p.name)."
            error = nil
            await load()
        } catch {
            self.error = managementErrorText(error)   // 409 while a rule uses it
        }
    }
}

// MARK: - Webhooks

/// Webhooks: outbound HTTP calls for rules and event subscriptions. The
/// signing secret is write-only: the server reports only whether one is set.
struct WebhooksPane: View {
    let api: SentinelAPI

    @State private var hooks: [V2.Webhook] = []
    @State private var selection: String?
    @State private var creating = false
    @State private var draft = WebhookDraft()
    @State private var baseline = WebhookDraft()
    @State private var error: String?
    @State private var status: String?
    @State private var testing = false
    @State private var busy = false
    @State private var confirmDelete: V2.Webhook?

    private var dirty: Bool { draft != baseline }

    var body: some View {
        PaneFrame(title: "Webhooks", subtitle: "Signed JSON posts (X-Sentinel-Signature, HMAC-SHA256) for rules and subscribed events.",
                  error: error, status: status, trailing: {
            Button { startNew() } label: { Label("New Webhook", systemImage: "plus") }
        }) {
            HSplitView {
                List(selection: Binding(get: { selection }, set: { select($0) })) {
                    ForEach(hooks) { hook in
                        HStack(spacing: 8) {
                            Circle().fill(color(WebhookStatusDot.of(hook))).frame(width: 9, height: 9)
                            VStack(alignment: .leading, spacing: 1) {
                                Text(hook.name.isEmpty ? hook.url : hook.name)
                                Text(WebhookStatusDot.caption(hook)).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        .tag(hook.id)
                    }
                }
                .frame(minWidth: 230, idealWidth: 270, maxWidth: 340)
                editor.frame(minWidth: 380)
            }
        }
        .task { await load() }
        .confirmationDialog("Delete \(confirmDelete?.name ?? "webhook")?", isPresented: Binding(
            get: { confirmDelete != nil }, set: { if $0 == false { confirmDelete = nil } })) {
            Button("Delete Webhook", role: .destructive) {
                if let h = confirmDelete { Task { await delete(h) } }
            }
        }
    }

    private func color(_ dot: WebhookStatusDot) -> Color {
        switch dot {
        case .ok: return .green
        case .failing: return .red
        case .never, .disabled: return .gray
        }
    }

    @ViewBuilder private var editor: some View {
        if selection == nil && creating == false {
            Text("Select a webhook, or create one.").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            Form {
                TextField("Name", text: $draft.name)
                TextField("URL", text: $draft.url, prompt: Text("https://example.com/hook"))
                Toggle("Enabled", isOn: $draft.enabled)
                TextField("Event kinds", text: $draft.eventsText, prompt: Text("Leave empty for rule actions only"))
                VStack(alignment: .leading, spacing: 4) {
                    SecureField("Signing secret", text: $draft.newSecret, prompt: Text(draft.hasSecret ? "Set. Type to replace" : "Optional"))
                        .disabled(draft.clearSecret)
                    HStack {
                        Text(draft.secretStatus).font(.caption).foregroundStyle(.secondary)
                        Spacer()
                        if draft.hasSecret {
                            Toggle("Remove secret", isOn: $draft.clearSecret).toggleStyle(.checkbox).font(.caption)
                        }
                    }
                }
                HStack {
                    if creating == false, let h = hooks.first(where: { $0.id == selection }) {
                        Button("Delete…", role: .destructive) { confirmDelete = h }.disabled(busy)
                        Button { Task { await test() } } label: {
                            if testing { ProgressView().controlSize(.small) } else { Text("Send Test") }
                        }
                        .disabled(testing || dirty)
                        .help(dirty ? "Save your changes first; the test uses the saved webhook." : "Posts a sample event to the saved URL.")
                    }
                    Spacer()
                    if let reason = draft.blocker, dirty { Text(reason).font(.caption).foregroundStyle(.secondary) }
                    Button("Revert") { draft = baseline }.disabled(dirty == false || busy)
                    Button(creating ? "Create" : "Save") { Task { await save() } }
                        .buttonStyle(.borderedProminent).disabled(dirty == false || draft.blocker != nil || busy)
                }
            }
            .formStyle(.grouped)
        }
    }

    private func load() async {
        do {
            hooks = try await api.webhooks()
            error = nil
            if let selection, creating == false, let h = hooks.first(where: { $0.id == selection }), dirty == false {
                adopt(WebhookDraft(h))
            }
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func adopt(_ d: WebhookDraft) {
        draft = d
        baseline = d
    }

    private func select(_ id: String?) {
        selection = id
        creating = false
        status = nil
        adopt(hooks.first { $0.id == id }.map(WebhookDraft.init) ?? WebhookDraft())
    }

    private func startNew() {
        selection = nil
        creating = true
        status = nil
        adopt(WebhookDraft())
    }

    private func save() async {
        busy = true
        defer { busy = false }
        do {
            let result: V2.Webhook
            if let selection, creating == false {
                result = try await api.updateWebhook(selection, draft.input())
            } else {
                result = try await api.createWebhook(draft.input())
            }
            creating = false
            selection = result.id
            status = "Saved \(result.name)."
            error = nil
            hooks = (try? await api.webhooks()) ?? hooks
            adopt(WebhookDraft(hooks.first { $0.id == result.id } ?? result))
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func test() async {
        guard let id = selection else { return }
        testing = true
        defer { testing = false }
        do {
            let result = try await api.testWebhook(id)
            status = WebhookStatusDot.testSummary(result)
            error = nil
            hooks = (try? await api.webhooks()) ?? hooks
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func delete(_ h: V2.Webhook) async {
        confirmDelete = nil
        busy = true
        defer { busy = false }
        do {
            try await api.deleteWebhook(h.id)
            selection = nil
            creating = false
            adopt(WebhookDraft())
            status = "Deleted \(h.name)."
            error = nil
            await load()
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Notifications

/// Notifications: the push and MQTT settings, over their existing v1
/// endpoints (a regrouping, no new server surface).
struct NotificationsPane: View {
    let api: SentinelAPI

    // Push
    @State private var push: V2.PushStatus?
    @State private var relayURL = ""
    @State private var quietEnabled = false
    @State private var quietFrom = 22 * 60
    @State private var quietTo = 7 * 60
    @State private var pushStatusText: String?
    // MQTT
    @State private var mqtt: V2.MQTTSettings?
    @State private var mqttEnabled = false
    @State private var host = ""
    @State private var port = 1883
    @State private var tls = false
    @State private var username = ""
    @State private var password = ""
    @State private var clearPassword = false
    @State private var topicPrefix = "sentinel"
    @State private var discoveryPrefix = "homeassistant"
    @State private var allowCommands = false
    @State private var mqttStatusText: String?
    @State private var error: String?
    @State private var busy = false

    var body: some View {
        PaneFrame(title: "Notifications", subtitle: "Phone push alerts and the MQTT bridge.", error: error) {
            Form {
                pushSection
                mqttSection
            }
            .formStyle(.grouped)
        }
        .task { await load() }
    }

    private var pushSection: some View {
        Section("Push (Sentinel Mobile)") {
            if let push {
                LabeledContent("Status", value: push.ready ? "Ready" : "Not set up")
                LabeledContent("Phones", value: "\(push.pushDevices) allowing alerts of \(push.devices) paired")
                LabeledContent("Delivered", value: "\(push.sent) sent, \(push.failed) failed, \(push.dropped) dropped")
                if push.lastError.isEmpty == false { Text(push.lastError).font(.caption).foregroundStyle(.red) }
            }
            TextField("Relay URL", text: $relayURL, prompt: Text("Leave as is unless you run your own relay"))
            Toggle("Quiet hours", isOn: $quietEnabled)
            if quietEnabled {
                HStack {
                    clockPicker("From", minutes: $quietFrom)
                    clockPicker("To", minutes: $quietTo)
                }
            }
            HStack {
                if let pushStatusText { Text(pushStatusText).font(.caption).foregroundStyle(.secondary) }
                Spacer()
                Button("Send Test") { Task { await testPush() } }.disabled(busy)
                Button("Save") { Task { await savePush() } }.buttonStyle(.borderedProminent).disabled(busy || push == nil)
            }
        }
    }

    private var mqttSection: some View {
        Section("MQTT") {
            if let mqtt { LabeledContent("Status", value: NotificationRules.mqttStateText(mqtt.status)) }
            Toggle("Enable the MQTT bridge", isOn: $mqttEnabled)
            TextField("Broker", text: $host, prompt: Text("broker.local"))
            HStack {
                TextField("Port", value: $port, format: .number.grouping(.never))
                Toggle("TLS", isOn: $tls)
            }
            TextField("Username", text: $username)
            VStack(alignment: .leading, spacing: 4) {
                SecureField("Password", text: $password, prompt: Text(mqtt?.hasPassword == true ? "Set. Type to replace" : "Optional"))
                    .disabled(clearPassword)
                if mqtt?.hasPassword == true {
                    Toggle("Remove saved password", isOn: $clearPassword).toggleStyle(.checkbox).font(.caption)
                }
            }
            TextField("Topic prefix", text: $topicPrefix)
            TextField("Discovery prefix", text: $discoveryPrefix)
            Toggle("Accept recording/detection commands", isOn: $allowCommands)
            HStack {
                if let blocker = NotificationRules.mqttBlocker(host: host, port: port, enabled: mqttEnabled) {
                    Text(blocker).font(.caption).foregroundStyle(.secondary)
                } else if let mqttStatusText {
                    Text(mqttStatusText).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button("Test Connection") { Task { await testMQTT() } }.disabled(busy)
                Button("Save") { Task { await saveMQTT() } }.buttonStyle(.borderedProminent)
                    .disabled(busy || mqtt == nil || NotificationRules.mqttBlocker(host: host, port: port, enabled: mqttEnabled) != nil)
            }
        }
    }

    private func clockPicker(_ label: String, minutes: Binding<Int>) -> some View {
        Picker(label, selection: minutes) {
            ForEach(Array(stride(from: 0, to: 1440, by: 30)), id: \.self) { Text(NotificationRules.clock($0)).tag($0) }
        }
    }

    // MARK: Actions

    private func load() async {
        do {
            let p = try await api.pushStatus()
            adopt(push: p)
        } catch {
            self.error = managementErrorText(error)
        }
        do {
            adopt(mqtt: try await api.mqttSettings())
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func adopt(push p: V2.PushStatus) {
        push = p
        relayURL = p.relayURL
        quietEnabled = p.quietEnabled
        quietFrom = p.quietFrom
        quietTo = p.quietTo
    }

    private func adopt(mqtt m: V2.MQTTSettings) {
        mqtt = m
        mqttEnabled = m.enabled
        host = m.host
        port = m.port
        tls = m.tls
        username = m.username
        topicPrefix = m.topicPrefix
        discoveryPrefix = m.discoveryPrefix
        allowCommands = m.allowCommands
        password = ""
        clearPassword = false
    }

    private func mqttUpdate() -> V2.MQTTUpdate {
        V2.MQTTUpdate(enabled: mqttEnabled, host: host, port: port, tls: tls, username: username,
                      password: clearPassword ? "" : (password.isEmpty ? nil : password),
                      topicPrefix: topicPrefix, discoveryPrefix: discoveryPrefix, allowCommands: allowCommands)
    }

    private func savePush() async {
        busy = true
        defer { busy = false }
        do {
            let update = V2.PushUpdate(relayURL: relayURL, quietEnabled: quietEnabled, quietFrom: quietFrom, quietTo: quietTo)
            adopt(push: try await api.updatePush(update))
            pushStatusText = "Saved."
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func testPush() async {
        busy = true
        defer { busy = false }
        do {
            pushStatusText = NotificationRules.pushTestSummary(try await api.testPush())
            error = nil
        } catch {
            pushStatusText = nil
            self.error = managementErrorText(error)
        }
    }

    private func saveMQTT() async {
        busy = true
        defer { busy = false }
        do {
            adopt(mqtt: try await api.updateMQTT(mqttUpdate()))
            mqttStatusText = "Saved."
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func testMQTT() async {
        busy = true
        defer { busy = false }
        do {
            let result = try await api.testMQTT(mqttUpdate())
            mqttStatusText = result.ok ? "Connected to the broker." : "Couldn't connect: \(result.error ?? "no answer")"
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

#Preview("Time Profiles") { TimeProfilesPane(api: ManagementPreview.api()).frame(width: 820, height: 480) }
#Preview("Webhooks") { WebhooksPane(api: ManagementPreview.api()).frame(width: 760, height: 460) }
#Preview("Notifications") { NotificationsPane(api: ManagementPreview.api()).frame(width: 560, height: 760) }
