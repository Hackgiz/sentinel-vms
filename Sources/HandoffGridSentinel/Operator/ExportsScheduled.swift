import SwiftUI
import SentinelCore

/// Scheduled exports: "this camera, 22:00 to 06:00, every night". When a
/// window ends the server files it into the evidence vault as a long export.
/// Loaded on entering the segment; no timers.
struct ScheduledExportsView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var directory: CameraDirectory

    @State private var schedules: [V2.ExportSchedule]?
    @State private var error: String?
    @State private var editing: ScheduleEditTarget?
    @State private var deleting: V2.ExportSchedule?
    @State private var toggling: Set<String> = []

    private var sortedCameras: [V2.Camera] { directory.cameras.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text("Scheduled exports").font(.headline)
                    Spacer()
                    Button {
                        Task { await load() }
                    } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                    .buttonStyle(.borderless)
                    .help("Reload")
                    Button("Add Schedule") {
                        editing = ScheduleEditTarget(scheduleID: nil, draft: .blank(cameraID: sortedCameras.first?.id ?? ""))
                    }
                    .buttonStyle(.borderedProminent)
                }
                Text(ScheduleRules.explanation)
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .padding(14)
            Divider()
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true).padding(.horizontal, 14).padding(.top, 8)
            }
            table
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .task { await load() }
        .sheet(item: $editing) { target in
            ScheduleEditorSheet(target: target, cameras: sortedCameras) { await load() }
        }
        .confirmationDialog("Delete this schedule?", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })) {
            Button("Delete Schedule", role: .destructive) {
                if let target = deleting { Task { await delete(target) } }
            }
        } message: {
            if let deleting {
                Text("\(deleting.name) will stop exporting. Evidence it already filed is kept.")
            }
        }
    }

    @ViewBuilder
    private var table: some View {
        if let schedules {
            if schedules.isEmpty {
                VStack(spacing: 6) {
                    Image(systemName: "calendar.badge.clock").font(.system(size: 28)).foregroundStyle(.secondary)
                    Text("No scheduled exports yet").font(.headline)
                    Text("Add one to export a camera's window into the evidence vault every day it runs.")
                        .font(.callout).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Table(schedules) {
                    TableColumn("Name") { s in Text(s.name).fontWeight(.semibold) }.width(min: 100, ideal: 160)
                    TableColumn("Camera") { s in Text(directory.name(of: s.cameraId)) }.width(min: 90, ideal: 140)
                    TableColumn("Days") { s in Text(ScheduleRules.daysText(s.days)) }.width(min: 90, ideal: 140)
                    TableColumn("Window") { s in
                        Text("\(s.start) → \(s.end)\(ScheduleRules.isOvernight(start: s.start, end: s.end) ? " (next day)" : "")")
                            .font(.callout.monospacedDigit())
                    }.width(min: 110, ideal: 150)
                    TableColumn("Last Run") { s in
                        Text(ScheduleRules.lastRunText(s))
                            .font(.callout)
                            .foregroundStyle(ScheduleRules.lastRunFailed(s) ? Color.red : Color.primary)
                            .lineLimit(2)
                            .help(s.lastStatus)
                    }.width(min: 130, ideal: 220)
                    TableColumn("Enabled") { s in
                        Toggle("", isOn: Binding(get: { s.enabled }, set: { on in Task { await setEnabled(s, on) } }))
                            .labelsHidden()
                            .toggleStyle(.checkbox)
                            .disabled(toggling.contains(s.id))
                            .accessibilityLabel("\(s.name) enabled")
                    }.width(60)
                    TableColumn("") { s in
                        HStack(spacing: 6) {
                            Button("Edit") { editing = ScheduleEditTarget(scheduleID: s.id, draft: ScheduleDraft(s)) }
                            Button("Delete", role: .destructive) { deleting = s }
                        }
                        .controlSize(.small)
                    }.width(min: 110, ideal: 120)
                }
            }
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func load() async {
        do {
            schedules = ScheduleRules.sorted(try await runtime.api.exportSchedules())
            error = nil
        } catch {
            if schedules == nil { schedules = [] }
            self.error = ExportRules.failureText(error)
        }
    }

    private func setEnabled(_ s: V2.ExportSchedule, _ on: Bool) async {
        toggling.insert(s.id)
        defer { toggling.remove(s.id) }
        do {
            _ = try await runtime.api.patchExportSchedule(id: s.id, V2.ExportSchedulePatch(enabled: on))
            await load()
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }

    private func delete(_ s: V2.ExportSchedule) async {
        deleting = nil
        do {
            try await runtime.api.deleteExportSchedule(id: s.id)
            await load()
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }
}

struct ScheduleEditTarget: Identifiable {
    /// nil for a new schedule.
    var scheduleID: String?
    var draft: ScheduleDraft
    var id: String { scheduleID ?? "new" }
}

/// Add / edit sheet: name, camera, days (Sun–Sat), HH:MM start and end with
/// the overnight rule, enabled. Destination is always the evidence vault.
struct ScheduleEditorSheet: View {
    let target: ScheduleEditTarget
    let cameras: [V2.Camera]
    let onSaved: () async -> Void

    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @State private var draft = ScheduleDraft()
    @State private var loaded = false
    @State private var busy = false
    @State private var error: String?

    private var isNew: Bool { target.scheduleID == nil }
    private var blocker: String? { ScheduleRules.blocker(draft) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(isNew ? "Add Scheduled Export" : "Edit Scheduled Export").font(.headline)
            Form {
                TextField("Name", text: $draft.name)
                Picker("Camera", selection: $draft.cameraID) {
                    Text("Pick a camera").tag("")
                    ForEach(cameras) { Text($0.name).tag($0.id) }
                }
                LabeledContent("Days the window starts on") {
                    HStack(spacing: 8) {
                        ForEach(0..<7, id: \.self) { day in
                            Toggle(ScheduleRules.dayNames[day], isOn: Binding(
                                get: { draft.days.contains(day) },
                                set: { _ in draft.toggle(day: day) }))
                                .toggleStyle(.checkbox)
                        }
                    }
                }
                LabeledContent("Window") {
                    HStack(spacing: 6) {
                        TextField("From", text: $draft.start, prompt: Text("HH:MM"))
                            .frame(width: 70).labelsHidden()
                            .font(.body.monospacedDigit())
                        Text("to")
                        TextField("To", text: $draft.end, prompt: Text("HH:MM"))
                            .frame(width: 70).labelsHidden()
                            .font(.body.monospacedDigit())
                        if let minutes = ScheduleRules.windowMinutes(start: draft.start, end: draft.end) {
                            Text(LongExportRules.spanText(Int64(minutes) * 60_000)
                                 + (ScheduleRules.isOvernight(start: draft.start, end: draft.end) ? ", overnight" : ""))
                                .font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
                Text(ScheduleRules.windowHelp).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Toggle("Enabled", isOn: $draft.enabled)
            }
            if let blocker {
                Text(blocker).font(.caption).foregroundStyle(.secondary)
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(busy ? "Saving…" : "Save") { Task { await save() } }
                    .keyboardShortcut(.defaultAction)
                    .buttonStyle(.borderedProminent)
                    .disabled(blocker != nil || busy)
                    .help(blocker ?? "")
            }
        }
        .padding(20)
        .frame(width: 560)
        .onAppear {
            guard loaded == false else { return }
            loaded = true
            draft = target.draft
        }
    }

    private func save() async {
        guard blocker == nil, busy == false else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            if let id = target.scheduleID {
                _ = try await runtime.api.patchExportSchedule(id: id, draft.patch)
            } else {
                _ = try await runtime.api.createExportSchedule(draft.input)
            }
            NoticeCenter.shared.post(.success, isNew ? "Schedule added." : "Schedule saved.")
            await onSaved()
            dismiss()
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }
}
