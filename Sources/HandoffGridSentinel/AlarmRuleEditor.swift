import SwiftUI
import SentinelCore

/// Create / edit an alarm rule: what triggers it, which cameras, when it's
/// active, how it's delivered, and what the operator should do when it fires.
struct AlarmRuleEditorSheet: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @Environment(\.dismiss) private var dismiss

    @State private var rule: NotificationRule
    @State private var allCameras: Bool
    @State private var selectedCameraIDs: Set<UUID>
    @State private var hasSchedule: Bool
    @State private var fromHour: Int
    @State private var toHour: Int
    @State private var instructions: String
    @State private var confirmingDelete = false
    private let isNew: Bool

    private static let triggers: [String] = ["Any alarm"] + AlertKind.allCases.map(\.rawValue)
    private static let deliveries = ["Desktop + Center", "Center"]
    private static let severities = [AlertSeverity.critical, .warning, .info].map(\.rawValue)

    init(rule: NotificationRule?) {
        let base = rule ?? NotificationRule(
            name: "",
            trigger: AlertKind.person.rawValue,
            delivery: "Desktop + Center",
            severity: AlertSeverity.info.rawValue,
            isEnabled: true,
            isSnoozed: false
        )
        isNew = rule == nil
        _rule = State(initialValue: base)
        _allCameras = State(initialValue: (base.cameraIDs ?? []).isEmpty)
        _selectedCameraIDs = State(initialValue: Set(base.cameraIDs ?? []))
        _hasSchedule = State(initialValue: base.hasSchedule)
        _fromHour = State(initialValue: base.activeFromHour ?? 22)
        _toHour = State(initialValue: base.activeToHour ?? 6)
        _instructions = State(initialValue: base.instructions ?? "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(isNew ? "New Alarm Rule" : "Edit Alarm Rule")
                .font(.title3.weight(.semibold))
                .padding([.horizontal, .top], 20)

            Form {
                Section {
                    TextField("Name", text: $rule.name, prompt: Text("e.g. After-hours person at loading dock"))
                    Picker("When", selection: $rule.trigger) {
                        ForEach(Self.triggers, id: \.self) { Text($0).tag($0) }
                    }
                    Picker("Minimum severity", selection: $rule.severity) {
                        ForEach(Self.severities, id: \.self) { Text($0).tag($0) }
                    }
                }

                Section("Cameras") {
                    Toggle("All cameras", isOn: $allCameras)
                    if allCameras == false {
                        ForEach(cameraStore.cameras) { camera in
                            Toggle(camera.name, isOn: Binding(
                                get: { selectedCameraIDs.contains(camera.id) },
                                set: { on in
                                    if on { selectedCameraIDs.insert(camera.id) } else { selectedCameraIDs.remove(camera.id) }
                                }
                            ))
                        }
                    }
                }

                Section("Schedule") {
                    Toggle("Only during certain hours", isOn: $hasSchedule)
                    if hasSchedule {
                        HStack {
                            Picker("From", selection: $fromHour) {
                                ForEach(0..<24, id: \.self) { Text(Self.hourLabel($0)).tag($0) }
                            }
                            Picker("Until", selection: $toHour) {
                                ForEach(0..<24, id: \.self) { Text(Self.hourLabel($0)).tag($0) }
                            }
                        }
                        if fromHour > toHour {
                            Text("Runs overnight, past midnight.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }

                Section("Delivery") {
                    Picker("Notify", selection: $rule.delivery) {
                        ForEach(Self.deliveries, id: \.self) { Text($0).tag($0) }
                    }
                }

                Section("Operator instructions") {
                    TextEditor(text: $instructions)
                        .font(.callout)
                        .frame(minHeight: 70)
                    Text("Shown on every alarm this rule matches, e.g. \"Call site manager at 555-0100, then review the dock camera.\"")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .formStyle(.grouped)

            HStack {
                if isNew == false {
                    Button("Delete Rule", role: .destructive) { confirmingDelete = true }
                }
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(isNew ? "Create Rule" : "Save") { save() }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(canSave == false)
            }
            .padding(20)
        }
        .frame(width: 480, height: 640)
        .confirmationDialog("Delete \"\(rule.name)\"?", isPresented: $confirmingDelete) {
            Button("Delete Rule", role: .destructive) {
                workflowStore.deleteNotificationRule(rule)
                dismiss()
            }
        }
    }

    private var canSave: Bool {
        rule.name.trimmingCharacters(in: .whitespaces).isEmpty == false &&
            (allCameras || selectedCameraIDs.isEmpty == false) &&
            (hasSchedule == false || fromHour != toHour)
    }

    private func save() {
        var saved = rule
        saved.name = rule.name.trimmingCharacters(in: .whitespaces)
        saved.cameraIDs = allCameras ? nil : cameraStore.cameras.map(\.id).filter(selectedCameraIDs.contains)
        saved.activeFromHour = hasSchedule ? fromHour : nil
        saved.activeToHour = hasSchedule ? toHour : nil
        let trimmed = instructions.trimmingCharacters(in: .whitespacesAndNewlines)
        saved.instructions = trimmed.isEmpty ? nil : trimmed
        workflowStore.saveNotificationRule(saved)
        dismiss()
    }

    static func hourLabel(_ hour: Int) -> String {
        var components = DateComponents()
        components.hour = hour
        let date = Calendar.current.date(from: components) ?? Date()
        return date.formatted(date: .omitted, time: .shortened)
    }
}
