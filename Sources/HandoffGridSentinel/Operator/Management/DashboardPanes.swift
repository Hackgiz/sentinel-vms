import AppKit
import SwiftUI
import SentinelCore

// MARK: - Current Tasks

/// Current Tasks: long-running work on the management server. One refresh
/// interval, only while the pane is on screen.
struct TasksPane: View {
    let api: SentinelAPI
    var isVisible: Bool = true

    @State private var tasks: [V2.SystemTask]?
    @State private var error: String?
    @State private var onScreen = false

    private var active: Bool { isVisible && onScreen }

    var body: some View {
        PaneFrame(title: "Current Tasks", subtitle: "Finished tasks stay listed for 15 minutes.", error: error, trailing: {
            Button { Task { await load() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
        }) {
            if let tasks {
                let rows = TaskBoard.rows(tasks)
                if rows.isEmpty {
                    Text("Nothing is running.").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List(rows) { row($0) }
                }
            } else if error == nil {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Spacer()
            }
        }
        .trackOnScreen($onScreen)
        .task { await load() }
        .task(id: active) { await visibleInterval(active, seconds: TaskBoard.refreshSeconds) { await load() } }
    }

    private func row(_ t: V2.SystemTask) -> some View {
        HStack(spacing: 10) {
            Image(systemName: icon(t)).foregroundStyle(color(t)).frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(t.title)
                Text(t.kind).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if t.state == .running {
                if let f = TaskBoard.fraction(t) {
                    ProgressView(value: f).frame(width: 120)
                } else {
                    ProgressView().progressViewStyle(.linear).frame(width: 120)
                }
            }
            Text(TaskBoard.progressText(t)).font(.caption.monospacedDigit())
                .foregroundStyle(t.state == .failed ? Color.red : Color.secondary)
                .frame(maxWidth: 220, alignment: .trailing).lineLimit(2)
            Text(TaskBoard.duration(t)).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                .frame(width: 60, alignment: .trailing)
        }
        .padding(.vertical, 2)
    }

    private func icon(_ t: V2.SystemTask) -> String {
        switch t.state {
        case .running: return "arrow.triangle.2.circlepath"
        case .done: return "checkmark.circle.fill"
        case .failed: return "exclamationmark.triangle.fill"
        default: return "circle"
        }
    }

    private func color(_ t: V2.SystemTask) -> Color {
        switch t.state {
        case .running: return .accentColor
        case .done: return .green
        case .failed: return .red
        default: return .secondary
        }
    }

    private func load() async {
        do {
            tasks = try await api.systemTasks()
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Server Logs

/// Server Logs: a tail of the management server's log ring, or a recording
/// server's, with a minimum-level filter. One refresh interval while visible.
struct ServerLogsPane: View {
    let api: SentinelAPI
    var isVisible: Bool = true

    @State private var servers: [V2.RecordingServer] = []
    @State private var server = ""
    @State private var tail = LogTail()
    @State private var error: String?
    @State private var follow = true
    @State private var onScreen = false

    private var active: Bool { isVisible && onScreen }

    var body: some View {
        PaneFrame(title: "Server Logs", subtitle: "Newest \(LogTail.capacity) lines.", error: error, trailing: {
            HStack {
                Picker("Server", selection: $server) {
                    Text("Management server").tag("")
                    ForEach(servers) { Text($0.name).tag($0.id) }
                }
                .frame(maxWidth: 220)
                Picker("Level", selection: $tail.minimumLevel) {
                    ForEach(LogLevel.allCases, id: \.self) { Text($0.rawValue.capitalized + (($0 == .debug) ? "" : " and up")).tag($0) }
                }
                .frame(maxWidth: 170)
                Toggle("Follow", isOn: $follow).toggleStyle(.checkbox)
                Button { copy() } label: { Image(systemName: "doc.on.doc") }.help("Copy visible lines")
            }
        }) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 1) {
                        let lines = tail.visible
                        ForEach(Array(lines.enumerated()), id: \.offset) { index, line in
                            HStack(alignment: .firstTextBaseline, spacing: 8) {
                                Text(LogTail.defaultTime(line.ts)).foregroundStyle(.secondary)
                                Text(line.level.uppercased()).foregroundStyle(levelColor(line.level)).frame(width: 44, alignment: .leading)
                                Text(line.message).textSelection(.enabled)
                            }
                            .font(.system(size: 11, design: .monospaced))
                            .id(index)
                        }
                    }
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .onChange(of: tail.visible.count) { count in
                    if follow, count > 0 { proxy.scrollTo(count - 1, anchor: .bottom) }
                }
            }
        }
        .trackOnScreen($onScreen)
        .task { await loadServers(); await load() }
        .task(id: server) { await load() }
        .task(id: tail.minimumLevel) { await load() }
        .task(id: active) { await visibleInterval(active, seconds: LogTail.refreshSeconds) { await load() } }
    }

    private func levelColor(_ level: String) -> Color {
        switch LogLevel.rank(level) {
        case 3: return .red
        case 2: return .orange
        case 0: return .secondary
        default: return .primary
        }
    }

    private func loadServers() async {
        servers = (try? await api.recordingServers()) ?? []
    }

    private func load() async {
        do {
            // The level filter is applied server-side too, so a quiet level
            // doesn't fill the limit with debug lines.
            let lines = try await api.serverLogs(server: server, level: tail.minimumLevel.rawValue, limit: LogTail.capacity)
            tail.replace(with: lines)
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func copy() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(tail.text(), forType: .string)
    }
}

// MARK: - Thresholds

/// Thresholds: when System Monitor meters turn amber and red.
struct ThresholdsPane: View {
    let api: SentinelAPI

    @State private var editor = ThresholdEditor()
    @State private var loaded = false
    @State private var error: String?
    @State private var status: String?
    @State private var busy = false

    var body: some View {
        PaneFrame(title: "Monitor Thresholds", subtitle: "Percent used at which a meter turns amber and red.", error: error, status: status) {
            Form {
                ForEach(Array(zip(ThresholdEditor.Meter.allCases, ["CPU", "Memory", "Disk"])), id: \.1) { meter, label in
                    Section(label) {
                        stepper("Amber from", value: editor.pair(meter).amber) { editor.setAmber($0, meter) }
                        stepper("Red from", value: editor.pair(meter).red) { editor.setRed($0, meter) }
                    }
                }
                HStack {
                    Button("Reset to 80 / 95") { editor.resetToDefaults() }
                    Spacer()
                    Button("Save") { Task { await save() } }
                        .buttonStyle(.borderedProminent).disabled(editor.isDirty == false || busy || loaded == false)
                }
            }
            .formStyle(.grouped)
        }
        .task { await load() }
    }

    private func stepper(_ label: String, value: Double, set: @escaping (Double) -> Void) -> some View {
        Stepper(value: Binding(get: { value }, set: set), in: 1...100, step: 1) {
            HStack {
                Text(label)
                Spacer()
                Text("\(Int(value))%").monospacedDigit().foregroundStyle(.secondary)
            }
        }
    }

    private func load() async {
        do {
            editor = ThresholdEditor(try await api.monitorThresholds())
            loaded = true
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func save() async {
        busy = true
        defer { busy = false }
        do {
            editor = ThresholdEditor(try await api.updateMonitorThresholds(editor.values))
            status = "Saved."
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Configuration Report

/// Configuration Report: a redacted snapshot of the site, previewed here and
/// saved through a save panel as JSON or a printable page.
struct ConfigReportPane: View {
    let api: SentinelAPI

    @State private var format = ReportFormat.json
    @State private var preview: String?
    @State private var loading = false
    @State private var error: String?
    @State private var status: String?
    @State private var siteName = ""

    var body: some View {
        PaneFrame(title: "Configuration Report", subtitle: "Servers, cameras, users, roles, rules and settings. Credentials, hashes and secrets are removed.",
                  error: error, status: status, trailing: {
            HStack {
                Picker("Format", selection: $format) {
                    ForEach(ReportFormat.allCases, id: \.self) { Text($0.label).tag($0) }
                }
                .pickerStyle(.segmented).frame(width: 220)
                Button { Task { await generate() } } label: { Label("Generate", systemImage: "doc.text.magnifyingglass") }.disabled(loading)
                Button { Task { await save() } } label: { Label("Save…", systemImage: "square.and.arrow.down") }.disabled(loading)
            }
        }) {
            if let preview {
                ScrollView([.vertical, .horizontal]) {
                    Text(preview).font(.system(size: 11, design: .monospaced)).textSelection(.enabled)
                        .padding(10).frame(maxWidth: .infinity, alignment: .leading)
                }
            } else if loading {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Text("Generate a report to preview it, or save one straight to a file.")
                    .foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task { siteName = (try? await api.siteInfo().name) ?? "" }
    }

    private func generate() async {
        loading = true
        defer { loading = false }
        do {
            let data = try await api.configReport(html: format == .html)
            preview = ConfigReportModel.preview(data)
            status = "\(EvidenceDisplay.bytes(Int64(data.count))) generated."
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func save() async {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = ConfigReportModel.suggestedFileName(site: siteName, format: format)
        panel.canCreateDirectories = true
        guard panel.runModal() == .OK, let url = panel.url else { return }
        loading = true
        defer { loading = false }
        do {
            let size = try await ConfigReportModel.save(api: api, format: format, to: url)
            status = "Saved \(url.lastPathComponent) (\(EvidenceDisplay.bytes(Int64(size))))."
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Evidence Locks

/// Evidence Locks: read-only list of locked clips with Verify. Creating,
/// downloading and deleting stay in the Exports workspace.
struct EvidenceLocksPane: View {
    let api: SentinelAPI

    @State private var cases: [V2.EvidenceCase]?
    @State private var verifies: [String: VerifyState] = [:]
    @State private var copy = SHACopyModel()
    @State private var error: String?

    var body: some View {
        PaneFrame(title: "Evidence Locks", subtitle: "Clips protected from retention. Manage them in Exports.", error: error, trailing: {
            Button { Task { await load() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
        }) {
            if let cases {
                if cases.isEmpty {
                    Text("No evidence is locked.").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List(cases) { row($0) }
                }
            } else if error == nil {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Spacer()
            }
        }
        .task { await load() }
    }

    private func row(_ c: V2.EvidenceCase) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: c.isUpload ? "iphone" : "lock.fill").foregroundStyle(.secondary).frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                let name = EvidenceDisplay.sourceName(c)
                Text(c.title.isEmpty ? name : "\(name): \(c.title)")
                if c.isUpload { // wave 23
                    HStack(spacing: 6) {
                        UploadedFromIPhoneTag()
                        Text(EvidenceDisplay.uploadDetail(c)).font(.caption).foregroundStyle(.secondary)
                    }
                    Text("\(EvidenceDisplay.whenText(c)) · \(EvidenceDisplay.bytes(c.size))")
                        .font(.caption).foregroundStyle(.secondary)
                } else {
                    Text("\(EvidenceDisplay.rangeText(c)) · \(EvidenceDisplay.bytes(c.size)) · locked by \(c.lockedBy)")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if c.note.isEmpty == false { Text(c.note).font(.caption).foregroundStyle(.secondary) }
                HStack(spacing: 6) {
                    Text(EvidenceDisplay.shortSHA(c.sha256)).font(.system(size: 11, design: .monospaced)).help(c.sha256)
                    Button(copy.label(for: c.id)) { copySHA(c) }.buttonStyle(.link).font(.caption)
                }
            }
            Spacer()
            verifyView(c)
        }
        .padding(.vertical, 3)
    }

    @ViewBuilder private func verifyView(_ c: V2.EvidenceCase) -> some View {
        switch verifies[c.id] {
        case .running: ProgressView().controlSize(.small)
        case .ok: Label("Verified", systemImage: "checkmark.seal.fill").foregroundStyle(.green).font(.caption)
        case .failed(let message):
            Label(message, systemImage: "xmark.octagon.fill").foregroundStyle(.red).font(.caption).lineLimit(2).frame(maxWidth: 240)
        case nil: Button("Verify") { Task { await verify(c) } }
        }
    }

    private func load() async {
        do {
            cases = try await api.evidenceCases()
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func verify(_ c: V2.EvidenceCase) async {
        verifies[c.id] = .running
        do {
            verifies[c.id] = VerifyState.from(try await api.verifyEvidence(c.id))
        } catch {
            verifies[c.id] = .failed(managementErrorText(error))
        }
    }

    private func copySHA(_ c: V2.EvidenceCase) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(copy.text(for: c), forType: .string)
        copy.markCopied(c.id)
        Task {
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            copy.reset(c.id)
        }
    }
}

#Preview("Current Tasks") { TasksPane(api: ManagementPreview.api()).frame(width: 720, height: 360) }
#Preview("Server Logs") { ServerLogsPane(api: ManagementPreview.api()).frame(width: 820, height: 420) }
#Preview("Thresholds") { ThresholdsPane(api: ManagementPreview.api()).frame(width: 480, height: 520) }
#Preview("Configuration Report") { ConfigReportPane(api: ManagementPreview.api()).frame(width: 720, height: 460) }
#Preview("Evidence Locks") { EvidenceLocksPane(api: ManagementPreview.api()).frame(width: 720, height: 400) }
