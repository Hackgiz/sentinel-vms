import AppKit
import SwiftUI
import SentinelCore

/// Incidents: case files that tie alarms, events, evidence and notes together.
/// A filterable, cursor-paged list on the left; on the right the incident's
/// title / severity / status, its chronological timeline, an add-note box and
/// the report (Generate Report fills an editable summary that is saved only
/// when the operator says so).
///
/// Wiring (done by the coordinator at merge):
///
///     IncidentsWorkspaceView(
///         onPlayback: { cameraID, at in navigator.openPlayback(cameraIDs: [cameraID], at: at) },
///         onOpenAlarm: { id in /* switch to the Alarm Manager tab and set its focusAlarmID */ }
///     )
///
/// - `onPlayback(cameraID, at)`: open Playback at `at` (already lead-adjusted).
/// - `onOpenAlarm(alarmID)`: optional; when nil the "Open in Alarm Manager"
///   button on alarm items is not shown.
///
/// Needs the environment objects `OperatorSession`, `SiteRuntime`, `CameraDirectory`.
struct IncidentsWorkspaceView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime

    let onPlayback: (_ cameraID: String, _ at: Date) -> Void
    let onOpenAlarm: ((_ alarmID: String) -> Void)?

    @StateObject private var list = IncidentListModel()
    @State private var selected: String?
    @State private var creating = false

    init(onPlayback: @escaping (_ cameraID: String, _ at: Date) -> Void,
         onOpenAlarm: ((_ alarmID: String) -> Void)? = nil) {
        self.onPlayback = onPlayback
        self.onOpenAlarm = onOpenAlarm
    }

    private var mayAct: Bool { session.can(.acknowledgeAlarms) }

    var body: some View {
        HStack(spacing: 0) {
            listPane
                .frame(width: 340)
                .background(SentinelTheme.panel)
            Divider().overlay(SentinelTheme.line)
            Group {
                if let selected {
                    IncidentDetailView(incidentID: selected, onChanged: { list.put($0) },
                                       onPlayback: onPlayback, onOpenAlarm: onOpenAlarm)
                        .id(selected)
                } else {
                    VStack(spacing: 10) {
                        Image(systemName: "folder").font(.largeTitle).foregroundStyle(.secondary)
                        Text("Select an incident\(mayAct ? " or start a new one" : "").").foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        }
        .task { list.start(api: runtime.api) }
        .sheet(isPresented: $creating) {
            NewIncidentSheet { incident in
                list.put(incident, insertIfMissing: true)
                selected = incident.id
            }
        }
    }

    private var listPane: some View {
        VStack(spacing: 0) {
            VStack(spacing: 8) {
                Picker("Status", selection: Binding(get: { list.filter.status?.rawValue ?? "" },
                                                    set: { list.filter.status = $0.isEmpty ? nil : V2.IncidentStatus(rawValue: $0) })) {
                    Text("All").tag("")
                    ForEach(V2.IncidentStatus.all, id: \.rawValue) { Text($0.label).tag($0.rawValue) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                HStack {
                    TextField("Search incidents", text: $list.filter.q).textFieldStyle(.roundedBorder)
                    if mayAct {
                        Button("New") { creating = true }.buttonStyle(.borderedProminent)
                    }
                }
            }
            .controlSize(.small)
            .padding(10)
            Divider().overlay(SentinelTheme.line)
            if let error = list.error {
                Text(error).font(.caption).foregroundStyle(.red).padding(10)
            }
            if list.pager.items.isEmpty, list.pager.isLoaded, !list.isLoading {
                Text("No incidents.").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List(selection: $selected) {
                    ForEach(list.pager.items) { incident in
                        IncidentRow(incident: incident).tag(incident.id)
                    }
                    if list.pager.hasMore {
                        Button(list.isLoading ? "Loading…" : "Load More") { list.loadMore() }
                            .disabled(list.isLoading)
                    }
                }
                .listStyle(.inset)
                .scrollContentBackground(.hidden)
            }
        }
    }
}

// MARK: - List model

@MainActor
final class IncidentListModel: ObservableObject {
    @Published var filter = IncidentFilter() {
        didSet { if filter != oldValue { schedule(after: oldValue.q == filter.q ? 0.05 : 0.25) } }
    }
    @Published private(set) var pager = CursorPager<V2.Incident>()
    @Published private(set) var isLoading = false
    @Published private(set) var error: String?

    private var api: SentinelAPI?
    private var task: Task<Void, Never>?
    private var generation = 0

    func start(api: SentinelAPI) {
        guard self.api == nil else { return }
        self.api = api
        schedule(after: 0)
    }

    private func schedule(after delay: TimeInterval) {
        task?.cancel()
        task = Task { [weak self] in
            if delay > 0 { try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) }
            guard !Task.isCancelled else { return }
            await self?.load(more: false)
        }
    }

    func loadMore() { Task { await load(more: true) } }

    private func load(more: Bool) async {
        guard let api, !more || pager.hasMore else { return }
        generation += 1
        let mine = generation
        isLoading = true
        error = nil
        defer { if mine == generation { isLoading = false } }
        do {
            let page = try await api.incidentsPage(filter: filter, limit: 100, before: more ? pager.nextCursor : nil)
            guard mine == generation else { return }
            if more { pager.append(page) } else { pager.replace(with: page) }
        } catch {
            if mine == generation { self.error = error.localizedDescription }
        }
    }

    /// Reflects an edit made in the detail pane (or a new incident) in the list.
    func put(_ incident: V2.Incident, insertIfMissing: Bool = false) {
        if !pager.replaceExisting(incident), insertIfMissing { pager.insertAtFront(incident) }
    }
}

private struct IncidentRow: View {
    let incident: V2.Incident

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                IncidentBadge(text: incident.severity.label, tint: incidentTint(for: incident.severity))
                Text(incident.title).font(.callout.weight(.semibold)).lineLimit(1)
                Spacer(minLength: 4)
                IncidentBadge(text: incident.status.label, tint: incidentTint(for: incident.status))
            }
            Text("\(incident.itemCount) item\(incident.itemCount == 1 ? "" : "s") · \(incident.createdBy) · \(OperatorFormat.relative(incident.updatedAt))")
                .font(.caption).foregroundStyle(.secondary)
        }
        .padding(.vertical, 3)
    }
}

private func incidentTint(for severity: V2.IncidentSeverity) -> Color {
    switch severity {
    case .critical, .high: return .red
    case .medium: return SentinelTheme.amber
    default: return .blue
    }
}

private func incidentTint(for status: V2.IncidentStatus) -> Color {
    switch status {
    case .open: return .red
    case .investigating: return SentinelTheme.amber
    case .closed: return .green
    default: return .secondary
    }
}

private struct IncidentBadge: View {
    let text: String
    let tint: Color

    var body: some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 7).padding(.vertical, 3)
            .foregroundStyle(tint)
            .background(tint.opacity(0.15), in: Capsule())
    }
}

// MARK: - New incident

private struct NewIncidentSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    let onCreated: (V2.Incident) -> Void

    @State private var title = ""
    @State private var severity = V2.IncidentSeverity.medium
    @State private var isWorking = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("New Incident").font(.title3.weight(.semibold))
            if let error { Text(error).font(.caption).foregroundStyle(.red) }
            TextField("Title", text: $title).textFieldStyle(.roundedBorder)
            Picker("Severity", selection: $severity) {
                ForEach(V2.IncidentSeverity.all, id: \.rawValue) { Text($0.label).tag($0) }
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Create") { create() }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(isWorking || title.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 380)
    }

    private func create() {
        isWorking = true
        Task {
            defer { isWorking = false }
            do {
                let incident = try await runtime.api.createIncident(title: title.trimmingCharacters(in: .whitespaces), severity: severity)
                onCreated(incident)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - Detail

private struct IncidentDetailView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    let incidentID: String
    let onChanged: (V2.Incident) -> Void
    let onPlayback: (String, Date) -> Void
    let onOpenAlarm: ((String) -> Void)?

    @State private var incident: V2.Incident?
    @State private var items: [V2.IncidentItem] = []
    @State private var error: String?
    @State private var isWorking = false
    @State private var title = ""
    @State private var summary = ""
    @State private var note = ""
    @State private var aiEnabled = false
    @State private var aiBlocked = false
    @State private var aiNotice: String?
    @State private var isDrafting = false
    @State private var confirmOverwrite = false

    private var mayAct: Bool { session.can(.acknowledgeAlarms) }
    private static let playbackLead: TimeInterval = 5

    var body: some View {
        Group {
            if let incident {
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        header(incident)
                        if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                        if mayAct { statusButtons(incident) }
                        timeline
                        if mayAct { addNote }
                        report(incident)
                    }
                    .padding(18)
                }
            } else if let error {
                Text(error).foregroundStyle(.red).padding()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task {
            await reload()
            aiEnabled = await runtime.api.aiEnabled()
        }
        .confirmationDialog("Replace the current summary with a new AI draft?", isPresented: $confirmOverwrite) {
            Button("Replace Summary", role: .destructive) { Task { await generate() } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The summary now in the editor will be overwritten. Nothing is saved until you press Save.")
        }
    }

    // MARK: Pieces

    private func header(_ incident: V2.Incident) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                IncidentBadge(text: incident.status.label, tint: incidentTint(for: incident.status))
                if mayAct {
                    TextField("Title", text: $title)
                        .textFieldStyle(.roundedBorder)
                        .font(.title3.weight(.semibold))
                        .onSubmit { commitTitle() }
                    Picker("Severity", selection: Binding(get: { incident.severity }, set: { patch(V2.IncidentPatch(severity: $0)) })) {
                        ForEach(V2.IncidentSeverity.all, id: \.rawValue) { Text($0.label).tag($0) }
                    }
                    .labelsHidden()
                    .frame(width: 110)
                } else {
                    Text(incident.title).font(.title3.weight(.semibold))
                    Spacer()
                    IncidentBadge(text: incident.severity.label, tint: incidentTint(for: incident.severity))
                }
            }
            Text("Opened by \(incident.createdBy) · \(OperatorFormat.dateTime.string(from: Date(unixMillis: incident.createdAt))) · updated \(OperatorFormat.relative(incident.updatedAt))")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private func statusButtons(_ incident: V2.Incident) -> some View {
        HStack(spacing: 6) {
            ForEach(V2.IncidentStatus.all, id: \.rawValue) { status in
                Button(status.label) { patch(V2.IncidentPatch(status: status)) }
                    .disabled(isWorking || incident.status == status)
            }
        }
        .controlSize(.small)
    }

    private var timeline: some View {
        let ordered = IncidentTimeline.ordered(items)
        return SentinelPanel("TIMELINE (\(ordered.count))", systemImage: "clock.arrow.circlepath") {
            if ordered.isEmpty {
                Text("Nothing here yet. Add alarms from the Alarm Manager, or write a note below.")
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(ordered) { item in
                        TimelineRow(item: item, mayAct: mayAct, cameraName: cameras.name(of:),
                                    playbackLead: Self.playbackLead, onPlayback: onPlayback, onOpenAlarm: onOpenAlarm) {
                            removeItem(item)
                        }
                        if item.id != ordered.last?.id { Divider().overlay(SentinelTheme.line) }
                    }
                }
            }
        }
    }

    private var addNote: some View {
        HStack {
            TextField("Add a note to the timeline", text: $note)
                .textFieldStyle(.roundedBorder)
                .onSubmit { addNoteItem() }
            Button("Add Note") { addNoteItem() }
                .disabled(isWorking || note.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .controlSize(.small)
    }

    @ViewBuilder
    private func report(_ incident: V2.Incident) -> some View {
        let dirty = IncidentRules.isDirty(savedSummary: incident.summary, draft: summary)
        SentinelPanel("REPORT", systemImage: "doc.text") {
            VStack(alignment: .leading, spacing: 8) {
                if mayAct || !incident.summary.isEmpty {
                    TextEditor(text: $summary)
                        .font(.system(.callout, design: .monospaced))
                        .frame(minHeight: 220)
                        .disabled(!mayAct)
                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(SentinelTheme.line))
                        .overlay(alignment: .topLeading) {
                            if summary.isEmpty {
                                Text("Write the incident summary in markdown, or generate a draft from the timeline.")
                                    .foregroundStyle(.secondary).font(.callout)
                                    .padding(8).allowsHitTesting(false)
                            }
                        }
                    if mayAct {
                        HStack(spacing: 8) {
                            if aiEnabled {
                                Button {
                                    if IncidentRules.needsOverwriteConfirmation(savedSummary: incident.summary, draft: summary) {
                                        confirmOverwrite = true
                                    } else {
                                        Task { await generate() }
                                    }
                                } label: {
                                    Label(isDrafting ? "Drafting…" : "Generate Report", systemImage: "sparkles")
                                }
                                .disabled(isDrafting || isWorking || aiBlocked)
                                .help(aiBlocked ? (aiNotice ?? "AI isn't available right now.") : "Draft a markdown report from the timeline")
                            }
                            Button("Save") { patch(V2.IncidentPatch(summary: summary)) }
                                .buttonStyle(.borderedProminent)
                                .disabled(isWorking || !dirty)
                            Button("Revert") { summary = incident.summary }
                                .disabled(!dirty)
                        }
                        .controlSize(.small)
                    }
                } else {
                    Text("No report written.").font(.caption).foregroundStyle(.secondary)
                }
                if let aiNotice {
                    Text(aiNotice).font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.amber)
                }
            }
        }
    }

    // MARK: Actions

    private func reload() async {
        do {
            let detail = try await runtime.api.incident(incidentID)
            incident = detail.incident
            items = detail.items
            title = detail.incident.title
            summary = detail.incident.summary
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func run(_ work: @escaping () async throws -> Void) {
        isWorking = true
        error = nil
        Task {
            defer { isWorking = false }
            do { try await work() } catch { self.error = error.localizedDescription }
        }
    }

    private func adopt(_ updated: V2.Incident, keepDraft: Bool = false) {
        incident = updated
        title = updated.title
        if !keepDraft { summary = updated.summary }
        onChanged(updated)
    }

    private func patch(_ change: V2.IncidentPatch) {
        run {
            let updated = try await runtime.api.updateIncident(incidentID, change)
            // Only a summary save replaces the editor; other edits keep unsaved text.
            adopt(updated, keepDraft: change.summary == nil)
        }
    }

    private func commitTitle() {
        guard let incident, let changed = IncidentRules.titleChange(current: incident.title, edited: title) else {
            title = incident?.title ?? title
            return
        }
        patch(V2.IncidentPatch(title: changed))
    }

    private func addNoteItem() {
        let text = note.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        run {
            try await runtime.api.addIncidentItem(incidentID, .note(text))
            note = ""
            await reload()
        }
    }

    private func removeItem(_ item: V2.IncidentItem) {
        run {
            try await runtime.api.removeIncidentItem(incidentID, itemID: item.id)
            await reload()
        }
    }

    private func generate() async {
        isDrafting = true
        error = nil
        aiNotice = nil
        defer { isDrafting = false }
        do {
            let updated = try await runtime.api.generateIncidentReport(incidentID)
            adopt(updated)
        } catch {
            let failure = AIFailure(error)
            aiNotice = failure.message
            if failure.disablesAI { aiBlocked = true }
            if case .off = failure { aiEnabled = false }
        }
    }
}

// MARK: - Timeline row

private struct TimelineRow: View {
    let item: V2.IncidentItem
    let mayAct: Bool
    let cameraName: (String?) -> String
    let playbackLead: TimeInterval
    let onPlayback: (String, Date) -> Void
    let onOpenAlarm: ((String) -> Void)?
    let remove: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Text("\(OperatorFormat.dateTime.string(from: Date(unixMillis: item.at))) · \(item.kind.rawValue)")
                    .font(.caption).foregroundStyle(.secondary)
                Spacer()
                if mayAct {
                    Button { remove() } label: { Image(systemName: "minus.circle") }
                        .buttonStyle(.borderless)
                        .help("Remove from the incident")
                }
            }
            content
            if let note = item.note, !note.isEmpty, case .note = item.resolution {} else if let note = item.note, !note.isEmpty {
                Text(note).font(.callout).textSelection(.enabled)
            }
            Text("added by \(item.addedBy)").font(.caption2).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var content: some View {
        switch item.resolution {
        case .restricted:
            Label("Restricted: this item belongs to a camera outside your access.", systemImage: "lock.fill")
                .font(.callout).foregroundStyle(.secondary)
        case .missing:
            Label("No longer available.", systemImage: "questionmark.folder")
                .font(.callout).foregroundStyle(.secondary)
        case .unresolved(let kind):
            Label("This \(kind) couldn't be loaded.", systemImage: "exclamationmark.triangle")
                .font(.callout).foregroundStyle(.secondary)
        case .note(let text):
            Text(text).font(.callout).textSelection(.enabled)
        case .alarm(let alarm):
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    SeverityBadge(severity: alarm.severity)
                    Text(alarm.title).fontWeight(.semibold)
                    if let name = alarm.cameraName { Text("· \(name)").foregroundStyle(.secondary) }
                    AlarmStateBadge(state: alarm.state)
                }
                HStack(spacing: 8) {
                    if let onOpenAlarm {
                        Button("Open in Alarm Manager") { onOpenAlarm(alarm.id) }
                    }
                    if let cameraID = alarm.cameraId {
                        Button { onPlayback(cameraID, Date(unixMillis: alarm.createdAt).addingTimeInterval(-10)) } label: {
                            Label("Play Recording", systemImage: "play.rectangle")
                        }
                    }
                }
                .controlSize(.small)
            }
        case .event(let event):
            Button {
                onPlayback(event.cameraId, Date(unixMillis: event.createdAt).addingTimeInterval(-playbackLead))
            } label: {
                HStack(alignment: .top, spacing: 10) {
                    AlarmEventCard(event: event).frame(width: 190)
                    Text(cameraName(event.cameraId)).font(.caption).foregroundStyle(.secondary)
                }
            }
            .buttonStyle(.plain)
            .help("Play the recording from this event")
        case .evidence(let evidence):
            VStack(alignment: .leading, spacing: 2) {
                Label("Evidence case \(evidence.caseId) · \(evidence.cameraName) · \(evidence.range)", systemImage: "lock.doc.fill")
                    .font(.callout)
                Text("SHA-256 \(evidence.sha256)").font(.caption.monospaced()).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle).textSelection(.enabled)
            }
        }
    }
}
