import SwiftUI
import SentinelCore

// Audio event detection settings in the camera editor (wave 11 stream E).
// Form logic (range, default, the changed-fields PATCH) lives in
// `SentinelCore/Zones/AudioDetectionSettings.swift`. The section edits the
// editor's own form; CameraEditSheet folds the changed fields into its one
// camera PATCH on Save.

/// A Form section: "Detect loud sounds" + the dB-over-background slider (6–60).
struct AudioDetectionFields: View {
    @Binding var form: AudioDetectionSettings.Form

    var body: some View {
        Section("Audio detection") {
            Toggle("Detect loud and distinctive sounds (needs a camera with a microphone)", isOn: $form.audioDetect)
            if form.audioDetect {
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text("Loud noise threshold")
                        Spacer()
                        Text("\(form.audioThresholdDb) dB over background").monospacedDigit().foregroundStyle(.secondary)
                    }
                    Slider(value: Binding(get: { Double(form.audioThresholdDb) },
                                          set: { form.audioThresholdDb = AudioDetectionSettings.clamp($0) }),
                           in: Double(AudioDetectionSettings.minDb)...Double(AudioDetectionSettings.maxDb), step: 1) {
                        Text("Threshold")
                    } minimumValueLabel: {
                        Text("\(AudioDetectionSettings.minDb)").font(.caption)
                    } maximumValueLabel: {
                        Text("\(AudioDetectionSettings.maxDb)").font(.caption)
                    }
                    .labelsHidden()
                    Text("Raises an \"audio\" event when the sound stays this far above the room's background level. Lower is more sensitive; \(AudioDetectionSettings.defaultDb) suits most rooms.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }
}

/// Wave 17: "Loitering after" seconds (0 = off), saved through the camera PATCH.
struct LoiterFields: View {
    @Binding var seconds: Int

    var body: some View {
        Section("Loitering") {
            HStack {
                Text("Loitering after (seconds, 0 = off)")
                Spacer()
                TextField("Seconds", value: Binding(get: { seconds }, set: { seconds = LoiterSettings.clamp(Double($0)) }),
                          format: .number)
                    .frame(width: 70).textFieldStyle(.roundedBorder).multilineTextAlignment(.trailing)
                Stepper("", value: $seconds, in: 0...LoiterSettings.maxSeconds, step: 5).labelsHidden()
            }
            Text(LoiterSettings.hint(seconds))
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }
}
