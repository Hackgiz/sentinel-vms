import AppKit
import SwiftUI
import SentinelCore

/// The site-wide alarm queue: open alarms (newest critical first), the chosen
/// alarm's instructions, live video, response log, state changes and evidence.
struct AlarmQueueView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var navigator: OperatorNavigator

    /// Wave 5 hook: when set, the list header offers "Open in Alarm Manager"
    /// for the selected alarm. The coordinator passes a closure that switches
    /// to the Alarm Manager tab and sets its `focusAlarmID`. Default nil = no button.
    var onOpenInAlarmManager: ((_ alarmID: String) -> Void)? = nil

    @State private var showClosed = false
    @State private var closedHistory: [V2.Alarm] = []
    @State private var historyCursor: String?
    @State private var loadingHistory = false

    private var list: [V2.Alarm] {
        if showClosed {
            let known = Set(closedHistory.map(\.id))
            return alarms.recentlyClosed.filter { known.contains($0.id) == false } + closedHistory
        }
        return alarms.open
    }

    private var selected: V2.Alarm? {
        alarms.alarm(navigator.selectedAlarmID) ?? closedHistory.first { $0.id == navigator.selectedAlarmID }
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                listHeader
                Divider().overlay(SentinelTheme.line)
                if list.isEmpty {
                    VStack(spacing: 8) {
                        Image(systemName: showClosed ? "tray" : "checkmark.shield.fill")
                            .font(.largeTitle)
                            .foregroundStyle(showClosed ? Color.secondary : Color.green)
                        Text(showClosed ? "No closed alarms." : "No open alarms.")
                            .foregroundStyle(.secondary)
                        if let error = alarms.lastError {
                            Text(error).font(.caption).foregroundStyle(.red)
                        }
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List(selection: Binding(get: { navigator.selectedAlarmID }, set: { navigator.selectedAlarmID = $0 })) {
                        ForEach(list) { alarm in
                            AlarmRow(alarm: alarm).tag(alarm.id)
                        }
                        if showClosed, historyCursor != nil {
                            Button(loadingHistory ? "Loading…" : "Load More") { Task { await loadHistory(more: true) } }
                                .disabled(loadingHistory)
                        }
                    }
                    .listStyle(.inset)
                    .scrollContentBackground(.hidden)
                }
            }
            .frame(width: 360)
            .background(SentinelTheme.panel)

            Divider().overlay(SentinelTheme.line)

            Group {
                if let selected {
                    AlarmDetailView(alarm: selected)
                        .id(selected.id)
                } else {
                    VStack(spacing: 10) {
                        Image(systemName: "exclamationmark.octagon").font(.largeTitle).foregroundStyle(.secondary)
                        Text("Choose an alarm to respond.").foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        }
        .onAppear {
            if navigator.selectedAlarmID == nil { navigator.selectedAlarmID = alarms.open.first?.id }
        }
        .onChange(of: showClosed) { closed in
            if closed { Task { await loadHistory(more: false) } }
        }
    }

    private var listHeader: some View {
        HStack(spacing: 8) {
            Picker("", selection: $showClosed) {
                Text("Open (\(alarms.open.count))").tag(false)
                Text("Closed").tag(true)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            Spacer()
            if let onOpenInAlarmManager, let id = navigator.selectedAlarmID {
                Button { onOpenInAlarmManager(id) } label: { Image(systemName: "rectangle.and.text.magnifyingglass") }
                    .controlSize(.small)
                    .help("Open in Alarm Manager")
            }
            if session.can(.acknowledgeAlarms), showClosed == false, alarms.newCount > 0 {
                Button("Acknowledge All New") { acknowledgeAllNew() }
                    .controlSize(.small)
            }
            Button {
                Task {
                    await alarms.reload(api: runtime.api)
                    if showClosed { await loadHistory(more: false) }
                }
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .controlSize(.small)
            .help("Reload from the server")
        }
        .padding(10)
    }

    private func loadHistory(more: Bool) async {
        loadingHistory = true
        defer { loadingHistory = false }
        do {
            let page = try await runtime.api.alarms(openOnly: false, cursor: more ? historyCursor : nil, limit: 100)
            let closed = page.items.filter { $0.isOpen == false }
            closedHistory = more ? closedHistory + closed : closed
            historyCursor = page.nextCursor
        } catch {
            historyCursor = nil
        }
    }

    private func acknowledgeAllNew() {
        let fresh = alarms.open.filter { $0.state == .new }
        Task {
            for alarm in fresh {
                try? await alarms.setState(alarm, to: .acknowledged, note: "Acknowledged with all new alarms", api: runtime.api)
            }
        }
    }
}

private struct AlarmRow: View {
    let alarm: V2.Alarm

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: alarm.severity.symbol)
                .foregroundStyle(alarm.severity.tint)
                .frame(width: 18)
            VStack(alignment: .leading, spacing: 3) {
                HStack {
                    Text(alarm.title).font(.callout.weight(.semibold)).lineLimit(1)
                    Spacer(minLength: 4)
                    Text(OperatorFormat.relative(alarm.lastEventAt)).font(.caption2).foregroundStyle(.secondary)
                }
                Text([alarm.cameraName ?? "", alarm.kind].filter { $0.isEmpty == false }.joined(separator: " · "))
                    .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                HStack(spacing: 6) {
                    AlarmStateBadge(state: alarm.state)
                    if alarm.eventCount > 1 {
                        MetricPill(text: "\(alarm.eventCount) events")
                    }
                    if alarm.owner.isEmpty == false {
                        Text(alarm.owner).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
        }
        .padding(.vertical, 4)
    }
}

struct AlarmStateBadge: View {
    let state: V2.AlarmState

    var body: some View {
        Text(state.rawValue)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .foregroundStyle(state.tint)
            .background(state.tint.opacity(0.15), in: Capsule())
    }
}

/// One alarm: what happened, what to do, live video, and the response.
struct AlarmDetailView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator

    let alarm: V2.Alarm

    @State private var note = ""
    @State private var snoozeMinutes = 15
    @State private var isWorking = false
    @State private var error: String?
    @State private var evidence: V2.Evidence?
    @State private var snapshot: NSImage?

    private var canRespond: Bool { session.can(.acknowledgeAlarms) }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                HStack(alignment: .top, spacing: 14) {
                    video
                    if let snapshot {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Snapshot").font(.caption.weight(.bold)).foregroundStyle(.secondary)
                            Image(nsImage: snapshot)
                                .resizable()
                                .aspectRatio(contentMode: .fit)
                                .frame(maxWidth: 260)
                                .clipShape(RoundedRectangle(cornerRadius: 6))
                        }
                    }
                }
                instructions
                response
                evidenceSection
                log
            }
            .padding(18)
        }
        .task(id: alarm.eventId) {
            guard let eventID = alarm.eventId else { return }
            snapshot = await runtime.thumbnails.image(for: eventID)
        }
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: alarm.severity.symbol)
                .font(.title)
                .foregroundStyle(alarm.severity.tint)
            VStack(alignment: .leading, spacing: 4) {
                Text(alarm.title).font(.title3.weight(.semibold))
                if alarm.detail.isEmpty == false {
                    Text(alarm.detail).font(.callout).foregroundStyle(.secondary)
                }
                HStack(spacing: 8) {
                    SeverityBadge(severity: alarm.severity)
                    AlarmStateBadge(state: alarm.state)
                    MetricPill(text: alarm.kind)
                    if let name = alarm.cameraName ?? cameras.camera(alarm.cameraId)?.name {
                        MetricPill(text: name)
                    }
                }
                Text("Raised \(OperatorFormat.dateTime.string(from: Date(unixMillis: alarm.createdAt))) · last event \(OperatorFormat.relative(alarm.lastEventAt)) · \(alarm.eventCount) event\(alarm.eventCount == 1 ? "" : "s")\(alarm.owner.isEmpty ? "" : " · owner \(alarm.owner)")")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
        }
    }

    @ViewBuilder
    private var video: some View {
        if let camera = cameras.camera(alarm.cameraId) {
            VStack(alignment: .leading, spacing: 6) {
                LiveTile(camera: camera, isSelected: false, fill: false)
                    .frame(width: 420, height: 236)
                HStack {
                    Button {
                        navigator.openPlayback(cameraIDs: [camera.id], at: Date(unixMillis: alarm.createdAt).addingTimeInterval(-10))
                    } label: {
                        Label("Play from Alarm", systemImage: "play.rectangle")
                    }
                    Button {
                        OperatorWindows.shared.openCamera(camera.id, name: camera.name)
                    } label: {
                        Label("Pop Out", systemImage: "macwindow.on.rectangle")
                    }
                }
                .controlSize(.small)
            }
        } else if alarm.cameraId != nil {
            Text("The camera for this alarm isn't visible to you.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var instructions: some View {
        if alarm.instructions.isEmpty == false {
            SentinelPanel("WHAT TO DO", systemImage: "list.bullet.clipboard") {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(Array(alarm.instructions.enumerated()), id: \.offset) { _, item in
                        VStack(alignment: .leading, spacing: 3) {
                            Text(item.rule).font(.caption.weight(.bold)).foregroundStyle(.secondary)
                            Text(item.text).font(.callout).textSelection(.enabled)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        }
    }

    private var response: some View {
        SentinelPanel("RESPOND", systemImage: "hand.raised.fill") {
            VStack(alignment: .leading, spacing: 10) {
                if canRespond {
                    TextField("Note for the response log (optional)", text: $note, axis: .vertical)
                        .textFieldStyle(.roundedBorder)
                        .lineLimit(1...4)
                    HStack(spacing: 8) {
                        stateButton("Acknowledge", .acknowledged, symbol: "checkmark")
                        stateButton("Investigate", .investigating, symbol: "magnifyingglass")
                        Menu {
                            ForEach([5, 15, 30, 60, 240], id: \.self) { minutes in
                                Button("\(minutes < 60 ? "\(minutes) min" : "\(minutes / 60) h")") {
                                    snoozeMinutes = minutes
                                    change(.snoozed)
                                }
                            }
                        } label: {
                            Label("Snooze", systemImage: "moon.zzz")
                        }
                        .fixedSize()
                        .disabled(isWorking)
                        Spacer()
                        stateButton("False Alarm", .falseAlarm, symbol: "xmark.circle")
                        stateButton("Resolve", .resolved, symbol: "checkmark.seal.fill", prominent: true)
                    }
                    .controlSize(.small)
                    if alarm.isOpen == false {
                        Button("Reopen") { change(.acknowledged) }.controlSize(.small).disabled(isWorking)
                    }
                } else {
                    Text("Your role can view alarms but not respond to them.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if let error {
                    Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
                }
            }
        }
    }

    @ViewBuilder
    private func stateButton(_ title: String, _ state: V2.AlarmState, symbol: String, prominent: Bool = false) -> some View {
        let button = Button {
            change(state)
        } label: {
            Label(title, systemImage: symbol)
        }
        .disabled(isWorking || alarm.state == state)
        if prominent {
            button.buttonStyle(.borderedProminent)
        } else {
            button
        }
    }

    @ViewBuilder
    private var evidenceSection: some View {
        if session.can(.exportEvidence), alarm.cameraId != nil {
            SentinelPanel("EVIDENCE", systemImage: "lock.doc.fill") {
                VStack(alignment: .leading, spacing: 8) {
                    if let evidence {
                        DetailRow(label: "Case", value: evidence.caseId)
                        DetailRow(label: "Range", value: evidence.range)
                        DetailRow(label: "Size", value: OperatorFormat.bytes(evidence.size))
                        DetailRow(label: "Locked", value: "\(OperatorFormat.dateTime.string(from: Date(unixMillis: evidence.lockedAt))) by \(evidence.lockedBy)")
                        HStack {
                            Text("SHA-256").font(.caption).foregroundStyle(.secondary)
                            Spacer()
                            Text(evidence.sha256).font(.caption.monospaced()).lineLimit(1).truncationMode(.middle).textSelection(.enabled)
                        }
                    } else {
                        Text("Lock the recording behind this alarm so retention never deletes it. The Recording Server keeps the file and its SHA-256.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                    Button {
                        lockEvidence()
                    } label: {
                        Label(evidence == nil ? "Lock as Evidence" : "Locked", systemImage: evidence == nil ? "lock.fill" : "checkmark.shield.fill")
                    }
                    .controlSize(.small)
                    .disabled(isWorking || evidence != nil)
                }
            }
        }
    }

    @ViewBuilder
    private var log: some View {
        if alarm.responseLog.isEmpty == false {
            SentinelPanel("RESPONSE LOG", systemImage: "text.bubble") {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(alarm.responseLog.enumerated()), id: \.offset) { _, line in
                        Text(line).font(.caption).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        }
    }

    private func change(_ state: V2.AlarmState) {
        isWorking = true
        error = nil
        Task {
            defer { isWorking = false }
            do {
                try await alarms.setState(alarm, to: state, note: note, snoozeMinutes: state == .snoozed ? snoozeMinutes : nil, api: runtime.api)
                note = ""
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func lockEvidence() {
        isWorking = true
        error = nil
        Task {
            defer { isWorking = false }
            do {
                evidence = try await runtime.api.lockEvidence(alarmID: alarm.id)
                if let updated = try? await runtime.api.alarm(alarm.id) {
                    alarms.apply(updated, isNew: false)
                }
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
