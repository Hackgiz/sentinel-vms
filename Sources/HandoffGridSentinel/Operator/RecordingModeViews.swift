import SwiftUI
import SentinelCore

// Wave 22 recording modes on the Mac: the camera editor's Recording section,
// the Management › System Dashboard › Recording defaults pane, and the
// timeline's low-res legend. Wording and rules are in SentinelCore's
// RecordingModeLogic, shared with the web.

/// One mode as a radio row: the label, an optional "Recommended" tag and a one-line explanation.
struct RecordingModeRadioList: View {
    let choices: [RecordingModeLogic.ModeChoice]
    @Binding var selection: V2.RecordingMode
    var disabled = false
    /// Wave 22 addendum: when set, the disabled dual option's hint is a button that runs Find streams.
    var findStreams: (() -> Void)? = nil

    private func hintIsAction(_ choice: RecordingModeLogic.ModeChoice) -> Bool {
        findStreams != nil && choice.mode == .dual && choice.enabled == false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(choices) { choice in
              VStack(alignment: .leading, spacing: 2) {
                Button {
                    if choice.enabled { selection = choice.mode }
                } label: {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Image(systemName: selection == choice.mode ? "largecircle.fill.circle" : "circle")
                            .foregroundStyle(selection == choice.mode ? Color.accentColor : Color.secondary)
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(spacing: 6) {
                                Text(choice.label).font(.callout.weight(selection == choice.mode ? .semibold : .regular))
                                if choice.recommended { RecommendedTag() }
                            }
                            if hintIsAction(choice) == false {
                                Text(choice.detail).font(.caption).foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        Spacer(minLength: 0)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(disabled || choice.enabled == false)
                .opacity(choice.enabled ? 1 : 0.5)
                .accessibilityAddTraits(selection == choice.mode ? .isSelected : [])
                if hintIsAction(choice), let findStreams {
                    Button(choice.detail) { findStreams() }
                        .buttonStyle(.link)
                        .font(.caption)
                        .padding(.leading, 22)
                        .disabled(disabled)
                        .help("Ask the camera which streams it has")
                }
              }
            }
        }
        .padding(.vertical, 2)
    }
}

private struct RecommendedTag: View {
    var body: some View {
        Text("Recommended")
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background(Color.accentColor.opacity(0.18), in: Capsule())
            .foregroundStyle(Color.accentColor)
    }
}

/// The camera editor's Recording section (shown when the server knows recording modes).
struct CameraRecordingFields: View {
    @Binding var form: RecordingModeLogic.Form
    @Binding var retentionDays: Int
    let hasSubStream: Bool
    let defaults: V2.RecordingSettings?
    let motionLevel: Int
    let cameraAnalytics: Bool
    /// Wave 22 addendum: runs Find streams (nil = not available).
    var findStreams: (() -> Void)? = nil
    /// Wave 23: an iPhone camera (dual shown disabled, no Find streams).
    var isPhone = false

    private var hasSub: Bool { hasSubStream && isPhone == false }

    private var effective: V2.RecordingMode {
        RecordingModeLogic.resolve(form.mode, serverDefault: defaults?.defaultMode, hasSubStream: hasSub)
    }

    var body: some View {
        Section("Recording") {
            RecordingModeRadioList(choices: RecordingModeLogic.choices(hasSubStream: hasSub, serverDefault: defaults?.defaultMode, isPhone: isPhone),
                                   selection: $form.mode, findStreams: isPhone ? nil : findStreams)
            if form.mode == .dual && hasSub == false {
                Text(isPhone ? "Low-res 24/7 is not available for iPhone cameras. Pick another mode."
                             : "Low-res 24/7 needs a second stream. Add one above or pick another mode.")
                    .font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            } else if form.mode == .default && defaults?.defaultMode == .dual && hasSub == false {
                Text(isPhone ? "iPhone cameras have no second stream: this camera records Events only."
                             : "No second stream: this camera records Events only.").font(.caption).foregroundStyle(.secondary)
            }
            if RecordingModeLogic.usesEvents(effective) {
                Picker("Start recording on", selection: $form.trigger) {
                    Text(defaults.map { "Server default (\(RecordingModeLogic.triggerLabel($0.defaultTrigger)))" } ?? "Server default")
                        .tag(V2.RecordTrigger.default)
                    ForEach(RecordingModeLogic.triggers, id: \.self) { Text(RecordingModeLogic.triggerLabel($0)).tag($0) }
                }
                Text(RecordingModeLogic.triggerDetail(form.trigger == .default ? (defaults?.defaultTrigger ?? .default) : form.trigger))
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Stepper("Before the event: \(RecordingModeLogic.secondsLabel(form.preRollSeconds, serverDefault: defaults?.defaultPreRollSeconds))",
                        onIncrement: { form.preRollSeconds = RecordingModeLogic.stepPreRoll(form.preRollSeconds, up: true) },
                        onDecrement: { form.preRollSeconds = RecordingModeLogic.stepPreRoll(form.preRollSeconds, up: false) })
                Stepper("After the event: \(RecordingModeLogic.secondsLabel(form.postRollSeconds, serverDefault: defaults?.defaultPostRollSeconds))",
                        onIncrement: { form.postRollSeconds = RecordingModeLogic.stepPostRoll(form.postRollSeconds, up: true) },
                        onDecrement: { form.postRollSeconds = RecordingModeLogic.stepPostRoll(form.postRollSeconds, up: false) })
                if let warning = RecordingModeLogic.noTriggerWarning(effective: effective, motionLevel: motionLevel, cameraAnalytics: cameraAnalytics) {
                    Label(warning, systemImage: "exclamationmark.triangle").font(.caption).foregroundStyle(.orange)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            if effective != .off {
                Stepper("Keep full-res: \(RecordingModeLogic.daysLabel(retentionDays, serverDefault: defaults?.defaultRetentionDays))",
                        value: $retentionDays, in: 0...RecordingModeLogic.retentionMax)
            }
            if effective == .dual {
                Stepper("Keep low-res: \(RecordingModeLogic.daysLabel(form.lowResRetentionDays, serverDefault: defaults?.defaultLowResRetentionDays))",
                        value: $form.lowResRetentionDays, in: 0...RecordingModeLogic.lowResRetentionMax)
            }
        }
    }
}

/// The lighter band's legend in the timeline bar; hover for the explanation.
struct LowResLegend: View {
    var body: some View {
        HStack(spacing: 4) {
            RoundedRectangle(cornerRadius: 2).fill(SentinelTheme.timelineBand.opacity(RecordingBand.lowResOpacity))
                .frame(width: 14, height: 8)
            Text(RecordingModeLogic.lowResLegend).font(.caption2).foregroundStyle(.secondary)
        }
        .help(RecordingModeLogic.lowResHelp)
        .accessibilityElement(children: .combine)
    }
}

enum RecordingBand {
    /// Low-res bands are the same colour as full-res, lighter.
    static let lowResOpacity: Double = 0.4
}

// MARK: - Management › System Dashboard › Recording

struct RecordingDefaultsPane: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime

    @State private var saved: V2.RecordingSettings?
    @State private var draft = V2.RecordingSettings()
    @State private var loaded = false
    @State private var busy = false
    @State private var error: String?
    @State private var status: String?

    private var canChange: Bool { session.can(.changeSettings) }
    private var pending: V2.RecordingSettingsPatch {
        saved.map { RecordingModeLogic.patch(from: $0, to: draft) } ?? V2.RecordingSettingsPatch()
    }

    var body: some View {
        PaneFrame(title: "Recording", subtitle: "How cameras set to “Server default” record, how long footage is kept, and what happens when the disk fills up",
                  error: error, status: status, trailing: {
            Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }.help("Reload")
        }) {
            if saved != nil {
                form
            } else if loaded == false {
                ProgressView().frame(maxWidth: .infinity).padding(20)
            } else {
                Spacer()
            }
        }
        .task { await load() }
    }

    private var form: some View {
        Form {
            Section("Default recording mode") {
                RecordingModeRadioList(choices: RecordingModeLogic.defaultChoices(), selection: $draft.defaultMode, disabled: canChange == false)
                Text("Each camera can override this in its settings.").font(.caption).foregroundStyle(.secondary)
            }
            Section("Events") {
                Picker("Start recording on", selection: $draft.defaultTrigger) {
                    ForEach(RecordingModeLogic.triggers, id: \.self) { Text(RecordingModeLogic.triggerLabel($0)).tag($0) }
                }
                Text(RecordingModeLogic.triggerDetail(draft.defaultTrigger)).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Stepper("Before the event: \(draft.defaultPreRollSeconds) s", value: $draft.defaultPreRollSeconds, in: RecordingModeLogic.preRollRange)
                Stepper("After the event: \(draft.defaultPostRollSeconds) s", value: $draft.defaultPostRollSeconds, in: RecordingModeLogic.postRollRange, step: 5)
                Text("Footage is kept in whole one-minute pieces, so a clip can hold up to a minute more than this.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Section("Keep footage") {
                Stepper("Full-res: \(RecordingModeLogic.daysLabel(draft.defaultRetentionDays, serverDefault: nil))",
                        value: $draft.defaultRetentionDays, in: 1...RecordingModeLogic.retentionMax)
                Stepper("Low-res (Low-res 24/7 mode): \(RecordingModeLogic.daysLabel(draft.defaultLowResRetentionDays, serverDefault: nil))",
                        value: $draft.defaultLowResRetentionDays, in: 1...RecordingModeLogic.lowResRetentionMax)
            }
            Section("Disk space") {
                Toggle("Delete the oldest video when free space runs low", isOn: Binding(
                    get: { draft.minFreePercent > 0 },
                    set: { draft.minFreePercent = $0 ? max(draft.minFreePercent, 10) : 0 }))
                if draft.minFreePercent > 0 {
                    Stepper("Free space to keep: \(draft.minFreePercent) %", value: $draft.minFreePercent, in: RecordingModeLogic.minFreeRange)
                }
                Text(RecordingModeLogic.minFreeLabel(draft.minFreePercent)).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if canChange {
                HStack {
                    Spacer()
                    Button("Revert") { if let saved { draft = saved } }
                        .disabled(pending.isEmpty || busy)
                    Button(busy ? "Saving…" : "Save") { Task { await save() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(pending.isEmpty || busy)
                }
            }
        }
        .formStyle(.grouped)
        .disabled(canChange == false)
    }

    private func load() async {
        do {
            let s = try await runtime.api.recordingSettings()
            saved = s
            draft = s
            error = nil
        } catch {
            if let api = error as? SentinelAPIError, api.isNotFound { saved = nil }
            self.error = RecordingModeLogic.errorText(error)
        }
        loaded = true
    }

    private func save() async {
        let patch = pending
        guard patch.isEmpty == false else { return }
        busy = true
        error = nil
        status = nil
        defer { busy = false }
        do {
            let s = try await runtime.api.updateRecordingSettings(patch)
            saved = s
            draft = s
            status = "Saved. Cameras on “Server default” use it now."
        } catch {
            self.error = managementErrorText(error)
        }
    }
}
