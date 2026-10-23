import AppKit
import SwiftUI
import SentinelCore

/// The Alarm Manager: a full-page alarm workspace. State chips with live
/// counts, a filter bar (severity, camera, owner, time, text), a sortable
/// table with multi-select and bulk acknowledge / close / assign, and an
/// inspector-style detail panel (event thumbnails, jump to playback, response
/// log, AI triage with an explicit Apply, "Add to Incident…").
///
/// Wiring (done by the coordinator at merge):
///
///     AlarmManagerWorkspaceView(
///         focusAlarmID: $navigator.selectedAlarmID?,   // optional deep link, see below
///         onPlayback: { cameraID, at in navigator.openPlayback(cameraIDs: [cameraID], at: at) }
///     )
///
/// - `onPlayback(cameraID, at)`: open Playback for that camera at `at`
///   (already lead-adjusted a few seconds before the alarm/event).
/// - `focusAlarmID`: when it becomes non-nil the view clears its filters to
///   "all states", selects that alarm and sets the binding back to nil. Pass
///   `.constant(nil)` (the default) to disable the deep link.
///
/// Needs the usual environment objects: `OperatorSession`, `SiteRuntime`,
/// `AlarmQueue`, `CameraDirectory`.
struct AlarmManagerWorkspaceView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var cameras: CameraDirectory

    @Binding var focusAlarmID: String?
    let onPlayback: (_ cameraID: String, _ at: Date) -> Void

    @StateObject private var model = AlarmManagerModel()
    @State private var assignee = ""
    @State private var showAddToIncident = false
    /// Where a ⇧-click range starts.
    @State private var anchorID: String?

    init(focusAlarmID: Binding<String?> = .constant(nil),
         onPlayback: @escaping (_ cameraID: String, _ at: Date) -> Void) {
        self._focusAlarmID = focusAlarmID
        self.onPlayback = onPlayback
    }

    private var mayAct: Bool { session.can(.acknowledgeAlarms) }

    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                chipBar
                filterBar
                Divider().overlay(SentinelTheme.line)
                if mayAct, model.selection.count > 0 { bulkBar; Divider().overlay(SentinelTheme.line) }
                if let error = model.error {
                    Text(error).font(.caption).foregroundStyle(.red)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14).padding(.vertical, 6)
                }
                table
                if model.pager.hasMore {
                    Button(model.isLoading ? "Loading…" : "Load More") { model.loadMore() }
                        .disabled(model.isLoading)
                        .padding(8)
                }
            }
            .frame(maxWidth: .infinity)

            if let alarm = model.inspected {
                Divider().overlay(SentinelTheme.line)
                AlarmInspectorView(alarm: alarm, model: model, onPlayback: onPlayback, addToIncident: { showAddToIncident = true })
                    .id(alarm.id)
                    .frame(width: 400)
                    .background(SentinelTheme.panel)
            } else if model.selection.count > 1 {
                Divider().overlay(SentinelTheme.line)
                VStack(spacing: 8) {
                    Image(systemName: "checklist").font(.largeTitle).foregroundStyle(.secondary)
                    Text("\(model.selection.count) alarms selected").foregroundStyle(.secondary)
                }
                .frame(width: 400)
                .frame(maxHeight: .infinity)
                .background(SentinelTheme.panel)
            }
        }
        .task { model.start(api: runtime.api, queue: alarms) }
        .onChange(of: alarms.open) { _ in model.ingestLive(open: alarms.open, closed: alarms.recentlyClosed) }
        .onChange(of: alarms.recentlyClosed) { _ in model.ingestLive(open: alarms.open, closed: alarms.recentlyClosed) }
        .task(id: focusAlarmID) {
            guard let id = focusAlarmID else { return }
            model.start(api: runtime.api, queue: alarms)
            await model.focus(alarmID: id)
            focusAlarmID = nil
        }
        .sheet(isPresented: $showAddToIncident) {
            if let alarm = model.inspected {
                AddToIncidentSheet(alarm: alarm)
            }
        }
    }

    // MARK: Bars

    private var chipBar: some View {
        HStack(spacing: 6) {
            ForEach(model.chips) { chip in
                Toggle(isOn: Binding(get: { chip.isOn }, set: { _ in model.filter.toggle(state: chip.state) })) {
                    HStack(spacing: 4) {
                        Text(chip.state.rawValue)
                        Text("\(chip.count)").foregroundStyle(.secondary).monospacedDigit()
                    }
                }
                .toggleStyle(.button)
                .controlSize(.small)
            }
            Button("All States") { model.filter.states = [] }
                .controlSize(.small)
                .help("Show every state")
            Spacer()
            sortMenu
            if mayAct, model.rows.isEmpty == false {
                Button("Select All") { model.selection = Set(model.rows.map(\.id)) }
                    .controlSize(.small)
                    .help("Select every alarm shown, for the bulk actions")
            }
            Text(summary).font(.caption).foregroundStyle(.secondary)
            Button { model.reload(); model.reloadCounts() } label: { Image(systemName: "arrow.clockwise") }
                .controlSize(.small)
                .help("Reload from the server")
        }
        .padding(.horizontal, SentinelTheme.Space.lg).padding(.vertical, SentinelTheme.Space.sm)
    }

    private var summary: String {
        if model.isLoading, model.pager.items.isEmpty { return "Loading…" }
        let n = model.pager.items.count
        return "\(n)\(model.pager.hasMore ? "+" : "") alarm\(n == 1 ? "" : "s")"
    }

    private var filterBar: some View {
        HStack(spacing: 8) {
            ForEach(AlarmFilter.severities, id: \.self) { severity in
                Toggle(severity.rawValue, isOn: Binding(
                    get: { model.filter.severities.contains(severity) },
                    set: { _ in model.filter.toggle(severity: severity) }))
                    .toggleStyle(.button)
            }
            Menu {
                ForEach(cameras.cameras) { camera in
                    Toggle(camera.name, isOn: Binding(
                        get: { model.filter.cameras.contains(camera.id) },
                        set: { _ in model.filter.toggle(camera: camera.id) }))
                }
                if !model.filter.cameras.isEmpty {
                    Divider()
                    Button("All Cameras") { model.filter.cameras = [] }
                }
            } label: {
                Text(model.filter.cameras.isEmpty ? "All Cameras" : "\(model.filter.cameras.count) camera\(model.filter.cameras.count == 1 ? "" : "s")")
            }
            .fixedSize()
            Picker("Owner", selection: ownerSelection) {
                Text("Any Owner").tag("")
                Text("Unassigned").tag(AlarmFilter.unassignedOwner)
                if let me = session.me?.name { Text("Mine").tag(me) }
                let custom = model.filter.owner
                if !custom.isEmpty, custom != AlarmFilter.unassignedOwner, custom != session.me?.name {
                    Text(custom).tag(custom)
                }
            }
            .labelsHidden()
            .frame(maxWidth: 140)
            Picker("Time", selection: $model.filter.range) {
                ForEach(AlarmFilter.Range.allCases) { Text($0.label).tag($0) }
            }
            .labelsHidden()
            .frame(maxWidth: 130)
            if model.filter.range == .custom {
                DatePicker("From", selection: millisBinding(\.from, fallback: Date().addingTimeInterval(-86_400)),
                           displayedComponents: [.date, .hourAndMinute])
                    .labelsHidden()
                Text("to").foregroundStyle(.secondary)
                DatePicker("To", selection: millisBinding(\.to, fallback: Date()),
                           displayedComponents: [.date, .hourAndMinute])
                    .labelsHidden()
            }
            TextField("Title or detail", text: $model.filter.q)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 220)
            if model.filter.activeCount > 0 {
                Button("Clear Filters") { model.filter = model.filter.clearedKeepingStates() }
            }
            Spacer()
        }
        .controlSize(.small)
        .padding(.horizontal, SentinelTheme.Space.lg).padding(.bottom, SentinelTheme.Space.sm)
    }

    private var ownerSelection: Binding<String> {
        Binding(get: { model.filter.owner }, set: { model.filter.owner = $0 })
    }

    private func millisBinding(_ keyPath: WritableKeyPath<AlarmFilter, UnixMillis>, fallback: Date) -> Binding<Date> {
        Binding(
            get: {
                let ms = model.filter[keyPath: keyPath]
                return ms > 0 ? Date(unixMillis: ms) : fallback
            },
            set: { model.filter[keyPath: keyPath] = $0.unixMillis })
    }

    private var bulkBar: some View {
        let ack = AlarmBulk.targets(.acknowledge, alarms: model.pager.items, selected: model.selection).ids.count
        let close = AlarmBulk.targets(.close, alarms: model.pager.items, selected: model.selection).ids.count
        let count = model.selection.count
        return HStack(spacing: 8) {
            Text("\(count) selected").font(.callout.weight(.semibold))
            Button("Acknowledge\(ack != count ? " (\(ack))" : "")") { Task { await model.bulk(.acknowledge, queue: alarms) } }
                .buttonStyle(.borderedProminent)
                .disabled(model.isBusy || ack == 0)
            Button("Close\(close != count ? " (\(close))" : "")") { Task { await model.bulk(.close, queue: alarms) } }
                .disabled(model.isBusy || close == 0)
            TextField("Assign to user…", text: $assignee)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 160)
            Button("Assign") {
                let owner = assignee.trimmingCharacters(in: .whitespaces)
                Task { await model.bulkAssign(owner: owner, queue: alarms); assignee = "" }
            }
            .disabled(model.isBusy || assignee.trimmingCharacters(in: .whitespaces).isEmpty)
            if let me = session.me?.name {
                Button("Assign to Me") { Task { await model.bulkAssign(owner: me, queue: alarms) } }
                    .disabled(model.isBusy)
            }
            Button("Clear Selection") { model.selection = [] }
            Spacer()
            if model.isBusy { ProgressView().controlSize(.small) }
        }
        .controlSize(.small)
        .padding(.horizontal, 14).padding(.vertical, 6)
        .background(SentinelTheme.panelRaised)
    }

    // MARK: Cards

    /// The queue as cards (wave 20). Click selects one, ⌘-click adds or
    /// removes, ⇧-click selects a range; ↑/↓ move the selection. The action
    /// row shows on the selected or hovered card.
    private var table: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: SentinelTheme.Space.sm) {
                    ForEach(model.rows) { alarm in
                        AlarmCard(alarm: alarm, cameraName: alarm.cameraName ?? cameras.name(of: alarm.cameraId),
                                  selected: model.selection.contains(alarm.id)) {
                            cardActions(alarm)
                        }
                        .id(alarm.id)
                        .onTapGesture { click(alarm.id) }
                        .help(OperatorFormat.dateTime.string(from: Date(unixMillis: alarm.createdAt)))
                    }
                }
                .padding(.horizontal, SentinelTheme.Space.lg)
                .padding(.vertical, SentinelTheme.Space.md)
            }
            .focusable()
            .onMoveCommand { direction in
                guard let next = AlarmCardSelection.step(ids: model.rows.map(\.id), selection: model.selection,
                                                         anchor: anchorID, down: direction == .down) else { return }
                if direction == .up || direction == .down {
                    model.selection = [next]
                    anchorID = next
                    withAnimation(.easeInOut(duration: 0.1)) { proxy.scrollTo(next) }
                }
            }
        }
        .overlay {
            if model.pager.isLoaded, model.pager.items.isEmpty, !model.isLoading, model.error == nil {
                VStack(spacing: 8) {
                    Image(systemName: "checkmark.shield").font(.largeTitle).foregroundStyle(.secondary)
                    Text("No alarms match these filters.").foregroundStyle(.secondary)
                }
            }
        }
    }

    @ViewBuilder
    private func cardActions(_ alarm: V2.Alarm) -> some View {
        if HomeModel.canAcknowledge(alarm, mayAct: mayAct) {
            Button { acknowledge(alarm) } label: { Label("Acknowledge", systemImage: "checkmark.circle.fill") }
                .buttonStyle(CardPrimaryButtonStyle())
                .disabled(model.isBusy)
        } else {
            Spacer()
        }
        if let cameraID = alarm.cameraId {
            Button { onPlayback(cameraID, PlaybackJump.start(of: alarm)) } label: {
                Image(systemName: "play.rectangle.fill")
            }
            .buttonStyle(CardIconButtonStyle())
            .help("Play the recording from the alarm")
        }
        Button { model.selection = [alarm.id]; anchorID = alarm.id } label: { Image(systemName: "sidebar.right") }
            .buttonStyle(CardIconButtonStyle())
            .help("Show the details")
    }

    private func click(_ id: String) {
        let flags = NSEvent.modifierFlags
        model.selection = AlarmCardSelection.click(id, ids: model.rows.map(\.id), selection: model.selection, anchor: anchorID,
                                                   command: flags.contains(.command), shift: flags.contains(.shift))
        if flags.contains(.shift) == false { anchorID = id }
    }

    private func acknowledge(_ alarm: V2.Alarm) {
        Task {
            do {
                model.put(try await runtime.api.setAlarmState(alarm.id, V2.AlarmStateChange(state: .acknowledged)), queue: alarms)
            } catch {
                NoticeCenter.shared.post(.error, "Couldn't acknowledge “\(alarm.title)”: \(error.localizedDescription)", key: "alarm-ack")
            }
        }
    }

    /// The sort the table's column headers used to set.
    private var sortMenu: some View {
        Menu {
            ForEach(AlarmSortKey.allCases, id: \.self) { key in
                Button {
                    model.sort = model.sort.toggled(to: key)
                } label: {
                    Text(Self.sortLabel(key) + (model.sort.key == key ? (model.sort.ascending ? "  ↑" : "  ↓") : ""))
                }
            }
        } label: {
            Label("Sort: \(Self.sortLabel(model.sort.key))", systemImage: model.sort.ascending ? "arrow.up" : "arrow.down")
        }
        .fixedSize()
        .help("Sort the alarms; choose the same key again to reverse")
    }

    static func sortLabel(_ key: AlarmSortKey) -> String {
        switch key {
        case .severity: return "Severity"
        case .state: return "State"
        case .camera: return "Camera"
        case .owner: return "Owner"
        case .time: return "Raised"
        case .events: return "Events"
        }
    }
}

// MARK: - Inspector

/// The detail panel for one alarm.
private struct AlarmInspectorView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue

    let alarm: V2.Alarm
    @ObservedObject var model: AlarmManagerModel
    let onPlayback: (String, Date) -> Void
    let addToIncident: () -> Void

    @State private var events: [V2.Event]?
    @State private var note = ""
    @State private var owner = ""
    @State private var isWorking = false
    @State private var error: String?
    @State private var triage: V2.AlarmTriage?
    @State private var isTriaging = false

    private var mayAct: Bool { session.can(.acknowledgeAlarms) }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                header
                if !alarm.instructions.isEmpty { instructions }
                actions
                if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                if triage != nil || model.aiNotice != nil { triagePanel }
                if mayAct { respond }
                eventsSection
                if !alarm.responseLog.isEmpty { responseLog }
            }
            .padding(16)
        }
        .task(id: "\(alarm.id)#\(alarm.eventCount)") {
            events = (try? await runtime.api.alarmEvents(alarm.id)) ?? []
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top) {
                Image(systemName: alarm.severity.symbol).foregroundStyle(alarm.severity.tint).font(.title3)
                Text(alarm.title).font(.title3.weight(.semibold))
                Spacer()
                Button { model.selection = [] } label: { Image(systemName: "xmark") }
                    .buttonStyle(.borderless)
                    .help("Close details")
            }
            if !alarm.detail.isEmpty { Text(alarm.detail).font(.callout).foregroundStyle(.secondary) }
            HStack(spacing: 6) {
                SeverityBadge(severity: alarm.severity)
                AlarmStateBadge(state: alarm.state)
                AlarmKindPill(kind: alarm.kind, severity: alarm.severity)
            }
            Text("\(alarm.cameraName ?? "Site") · raised \(OperatorFormat.dateTime.string(from: Date(unixMillis: alarm.createdAt)))\(alarm.owner.isEmpty ? "" : " · owner \(alarm.owner)")")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var instructions: some View {
        SentinelPanel("WHAT TO DO", systemImage: "list.bullet.clipboard") {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(alarm.instructions.enumerated()), id: \.offset) { _, item in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(item.rule).font(.caption.weight(.bold)).foregroundStyle(.secondary)
                        Text(item.text).font(.callout).textSelection(.enabled)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
    }

    private var actions: some View {
        HStack(spacing: 8) {
            if let cameraID = alarm.cameraId {
                Button {
                    onPlayback(cameraID, PlaybackJump.start(of: alarm))
                } label: { Label("Play Recording", systemImage: "play.rectangle") }
            }
            if mayAct, model.aiEnabled {
                Button { runTriage() } label: {
                    Label(isTriaging ? "Thinking…" : "Triage", systemImage: "sparkles")
                }
                .disabled(isTriaging || model.aiBlocked)
                .help(model.aiBlocked ? (model.aiNotice ?? "AI isn't available right now.") : "Ask the AI for a summary and a suggested severity (advice only)")
            }
            if mayAct {
                Button { addToIncident() } label: { Label("Add to Incident…", systemImage: "folder.badge.plus") }
            }
        }
        .controlSize(.small)
    }

    private var triagePanel: some View {
        SentinelPanel("AI TRIAGE", systemImage: "sparkles") {
            VStack(alignment: .leading, spacing: 8) {
                if let triage {
                    Text(triage.summary).font(.callout).textSelection(.enabled)
                    if !triage.rationale.isEmpty {
                        Text(triage.rationale).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                    if let suggested = triage.suggested {
                        HStack(spacing: 8) {
                            Text("Suggested severity").font(.caption).foregroundStyle(.secondary)
                            SeverityBadge(severity: suggested)
                            Spacer()
                            if suggested == alarm.severity {
                                Text("already set").font(.caption).foregroundStyle(.secondary)
                            } else {
                                // Never applied automatically: the operator presses Apply.
                                Button("Apply") { apply(suggested) }
                                    .buttonStyle(.borderedProminent)
                                    .controlSize(.small)
                                    .disabled(isWorking || !mayAct)
                            }
                        }
                    }
                }
                if let notice = model.aiNotice {
                    Text(notice).font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.amber)
                }
            }
        }
    }

    private var respond: some View {
        SentinelPanel("RESPOND", systemImage: "hand.raised.fill") {
            VStack(alignment: .leading, spacing: 8) {
                TextField("Note for the response log (optional)", text: $note)
                    .textFieldStyle(.roundedBorder)
                HStack(spacing: 6) {
                    stateButton("Acknowledge", .acknowledged)
                    stateButton("Investigate", .investigating)
                    Menu("Snooze") {
                        ForEach([5, 15, 30, 60, 240], id: \.self) { minutes in
                            Button(minutes < 60 ? "\(minutes) min" : "\(minutes / 60) h") { change(.snoozed, snooze: minutes) }
                        }
                    }
                    .fixedSize()
                }
                HStack(spacing: 6) {
                    stateButton("False Alarm", .falseAlarm)
                    stateButton("Resolve", .resolved)
                    if !alarm.isOpen { stateButton("Reopen", .acknowledged) }
                }
                HStack(spacing: 6) {
                    TextField("Assign to user…", text: $owner).textFieldStyle(.roundedBorder)
                    Button("Assign") { assign(owner.trimmingCharacters(in: .whitespaces)) }
                        .disabled(isWorking || owner.trimmingCharacters(in: .whitespaces).isEmpty)
                    if let me = session.me?.name {
                        Button("Me") { assign(me) }.disabled(isWorking)
                    }
                }
            }
            .controlSize(.small)
        }
    }

    private func stateButton(_ title: String, _ state: V2.AlarmState) -> some View {
        Button(title) { change(state) }.disabled(isWorking || alarm.state == state)
    }

    private var eventsSection: some View {
        SentinelPanel("EVENTS\(events.map { " (\($0.count))" } ?? "")", systemImage: "bolt.horizontal") {
            if let events, events.isEmpty {
                Text("No detection events are attached to this alarm.").font(.caption).foregroundStyle(.secondary)
            } else if events == nil {
                ProgressView().controlSize(.small)
            } else {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 150), spacing: 8)], spacing: 8) {
                    ForEach(events ?? []) { event in
                        Button {
                            onPlayback(event.cameraId, PlaybackJump.start(of: event))
                        } label: { AlarmEventCard(event: event) }
                        .buttonStyle(.plain)
                        .help("Play the recording from this event")
                        .overlay(alignment: .topTrailing) { FindSimilarButton(event: event) } // WAVE11(C): Find similar
                    }
                }
            }
        }
    }

    private var responseLog: some View {
        SentinelPanel("RESPONSE LOG", systemImage: "text.bubble") {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(alarm.responseLog.enumerated()), id: \.offset) { _, line in
                    Text(line).font(.caption).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
    }

    // MARK: Actions

    private func change(_ state: V2.AlarmState, snooze: Int? = nil) {
        let trimmed = note.trimmingCharacters(in: .whitespacesAndNewlines)
        perform {
            try await runtime.api.setAlarmState(alarm.id, V2.AlarmStateChange(
                state: state, note: trimmed.isEmpty ? nil : String(trimmed.prefix(500)),
                snoozeMinutes: state == .snoozed ? snooze : nil))
        } done: { note = "" }
    }

    private func assign(_ name: String) {
        perform { try await runtime.api.assignAlarm(alarm.id, owner: name) } done: { owner = "" }
    }

    private func apply(_ severity: V2.Severity) {
        perform { try await runtime.api.setAlarmSeverity(alarm.id, severity) } done: {}
    }

    private func perform(_ call: @escaping () async throws -> V2.Alarm, done: @escaping () -> Void) {
        isWorking = true
        error = nil
        Task {
            defer { isWorking = false }
            do {
                model.put(try await call(), queue: alarms)
                done()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func runTriage() {
        isTriaging = true
        error = nil
        model.aiNotice = nil
        Task {
            defer { isTriaging = false }
            do {
                triage = try await runtime.api.triageAlarm(alarm.id)
            } catch {
                model.note(aiFailure: AIFailure(error))
            }
        }
    }
}

/// An event's snapshot with its kind, threat and time.
struct AlarmEventCard: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let event: V2.Event
    @State private var image: NSImage?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            ZStack {
                RoundedRectangle(cornerRadius: 6).fill(Color.black.opacity(0.85))
                if let image {
                    Image(nsImage: image).resizable().aspectRatio(contentMode: .fit)
                } else {
                    Image(systemName: event.hasThumbnail ? "photo" : HomeModel.kindSymbol(event.kind))
                        .foregroundStyle(SentinelTheme.kindColor(event.kind).opacity(0.8))
                }
            }
            .aspectRatio(16.0 / 9.0, contentMode: .fit)
            .clipShape(RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(SentinelTheme.kindColor(event.kind).opacity(0.7), lineWidth: 1.5))
            HStack(spacing: 4) {
                Image(systemName: HomeModel.kindSymbol(event.kind))
                    .font(.caption2.weight(.bold)).foregroundStyle(SentinelTheme.kindColor(event.kind))
                Text(event.label?.isEmpty == false ? event.label!.capitalized : event.kind)
                    .font(.caption.weight(.semibold)).lineLimit(1)
                if let threat = event.threat, threat != "none" {
                    Text(threat).font(.caption2.weight(.bold)).foregroundStyle(threat == "high" ? .red : SentinelTheme.amber)
                }
                Spacer(minLength: 0)
                Text(OperatorFormat.shortTime.string(from: Date(unixMillis: event.createdAt)))
                    .font(.caption2).foregroundStyle(.secondary)
            }
            if let description = event.description, !description.isEmpty {
                Text(description).font(.caption2).foregroundStyle(.secondary).lineLimit(3)
            }
        }
        .task(id: event.id) {
            guard event.hasThumbnail else { return }
            image = await runtime.thumbnails.image(for: event.id)
        }
    }
}

// MARK: - Add to incident

/// Pick an open incident to put an alarm in, or start a new one.
struct AddToIncidentSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime

    let alarm: V2.Alarm

    @State private var incidents: [V2.Incident]?
    @State private var title: String
    @State private var isWorking = false
    @State private var error: String?
    @State private var added: String?

    init(alarm: V2.Alarm) {
        self.alarm = alarm
        self._title = State(initialValue: IncidentPicking.defaultTitle(for: alarm))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Add to Incident").font(.title3.weight(.semibold))
            if let error { Text(error).font(.caption).foregroundStyle(.red) }
            if let added {
                Text("Added to incident “\(added)”.").foregroundStyle(.green)
            } else {
                if incidents == nil, error == nil { ProgressView().controlSize(.small) }
                if let incidents, !incidents.isEmpty {
                    Text("EXISTING INCIDENTS").font(.caption.weight(.bold)).foregroundStyle(.secondary)
                    List(incidents) { incident in
                        Button { add(to: incident) } label: {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(incident.title).fontWeight(.semibold)
                                Text("\(incident.status.label) · \(incident.itemCount) item\(incident.itemCount == 1 ? "" : "s")")
                                    .font(.caption).foregroundStyle(.secondary)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .disabled(isWorking)
                    }
                    .frame(minHeight: 140, maxHeight: 220)
                }
                Text("OR START A NEW ONE").font(.caption.weight(.bold)).foregroundStyle(.secondary)
                TextField("Incident title", text: $title).textFieldStyle(.roundedBorder)
            }
            HStack {
                Spacer()
                Button(added == nil ? "Cancel" : "Done") { dismiss() }.keyboardShortcut(.cancelAction)
                if added == nil {
                    Button("Create New Incident") { add(to: nil) }
                        .buttonStyle(.borderedProminent)
                        .disabled(isWorking || title.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
        }
        .padding(20)
        .frame(width: 440)
        .task {
            do {
                incidents = IncidentPicking.pickable(try await runtime.api.incidentsPage(limit: 200).items)
            } catch {
                self.error = error.localizedDescription
                incidents = []
            }
        }
    }

    private func add(to existing: V2.Incident?) {
        isWorking = true
        error = nil
        Task {
            defer { isWorking = false }
            do {
                let target: V2.Incident
                if let existing {
                    target = existing
                } else {
                    target = try await runtime.api.createIncident(
                        title: title.trimmingCharacters(in: .whitespaces),
                        severity: V2.IncidentSeverity(alarm: alarm.severity))
                }
                try await runtime.api.addIncidentItem(target.id, .alarm(alarm.id))
                added = target.title
            } catch let failure as SentinelAPIError where failure.isConflict {
                error = "That alarm is already in the incident."
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
