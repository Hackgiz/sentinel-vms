import SwiftUI
import SentinelCore

/// A camera's encoder settings, per stream, bounded by what the camera
/// itself reports (`GET /api/v2/cameras/{id}/stream-settings`). Apply sends
/// only the fields that changed (`PUT …/{configToken}`). Needs manageCameras
/// and a camera added with an ONVIF address.
///
/// Present it with `.sheet { StreamSettingsSheet(camera: camera) }`; it takes
/// `SiteRuntime` from the environment.
struct StreamSettingsSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    let camera: V2.Camera

    private enum Phase {
        case loading
        case unsupported(String)
        case ready([V2.EncoderStream])
    }

    @State private var phase = Phase.loading

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("Stream settings — \(camera.name)").font(.headline)
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
            switch phase {
            case .loading:
                ProgressView("Asking the camera…").frame(maxWidth: .infinity, minHeight: 120)
            case .unsupported(let detail):
                unsupported(detail)
            case .ready(let streams):
                if streams.isEmpty {
                    unsupported("The camera reported no video encoders.")
                } else {
                    ScrollView {
                        VStack(spacing: 12) {
                            ForEach(streams) { stream in
                                StreamCard(stream: stream, apply: { change in
                                    try await runtime.api.applyStreamSettings(cameraID: camera.id, configToken: stream.configToken, change: change)
                                    await reload(keepingCards: true)
                                })
                            }
                        }
                    }
                    // TODO: codec switching, Media2 (H.265-only cameras), audio, OSD and bulk apply are follow-ups.
                }
            }
        }
        .padding(20)
        .frame(width: 560, height: 520)
        .task { await reload(keepingCards: false) }
    }

    private func unsupported(_ detail: String) -> some View {
        VStack(spacing: 8) {
            Image(systemName: "slider.horizontal.3").font(.system(size: 28)).foregroundStyle(.secondary)
            Text(StreamSettingsModel.unsupportedText).font(.headline)
            Text("Sentinel can only change encoder settings on cameras that answer ONVIF. Use the camera's own web page instead.")
                .font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
            if detail.isEmpty == false {
                Text(detail).font(.caption).foregroundStyle(.tertiary)
            }
        }
        .frame(maxWidth: .infinity, minHeight: 200)
    }

    private func reload(keepingCards: Bool) async {
        if keepingCards == false { phase = .loading }
        do {
            let settings = try await runtime.api.streamSettings(cameraID: camera.id)
            phase = .ready(settings.streams)
        } catch {
            if keepingCards { return }
            phase = .unsupported(StreamSettingsModel.unsupportedDetail(error))
        }
    }
}

private struct StreamCard: View {
    let stream: V2.EncoderStream
    let apply: (V2.StreamSettingsChange) async throws -> Void

    @State private var form: StreamForm
    @State private var isApplying = false
    @State private var error: String?
    @State private var applied = false

    init(stream: V2.EncoderStream, apply: @escaping (V2.StreamSettingsChange) async throws -> Void) {
        self.stream = stream
        self.apply = apply
        _form = State(initialValue: StreamForm(stream))
    }

    private var change: V2.StreamSettingsChange? { StreamSettingsModel.change(from: stream, to: form) }
    private var problem: String? { StreamSettingsModel.problem(stream, form) }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(StreamSettingsModel.title(stream)).font(.headline)
                Text(stream.encoding)
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 7).padding(.vertical, 2)
                    .background(.quaternary, in: Capsule())
                    .help("The codec can't be changed from here")
                Spacer()
            }
            Picker("Resolution", selection: Binding(
                get: { StreamSettingsModel.resolutionKey(form.width, form.height) },
                set: { key in
                    let parts = key.split(separator: "x").compactMap { Int($0) }
                    if parts.count == 2 { form.width = parts[0]; form.height = parts[1] }
                })) {
                ForEach(StreamSettingsModel.resolutionChoices(stream), id: \.self) { r in
                    Text(StreamSettingsModel.resolutionLabel(r)).tag(StreamSettingsModel.resolutionKey(r.width, r.height))
                }
            }
            NumberRow(title: "Frame rate", unit: "fps", value: $form.fps, range: stream.options.fps)
            NumberRow(title: "Bitrate", unit: "kbit/s", value: $form.bitrate, range: stream.options.bitrate)
            NumberRow(title: "Keyframe interval", unit: "frames", value: $form.govLength, range: stream.options.gov)
            NumberRow(title: "Quality", unit: "", value: $form.quality, range: stream.options.quality)

            if StreamSettingsModel.reconnects(change) {
                Label("Changing the resolution or frame rate makes the live stream briefly reconnect.", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(SentinelTheme.amber)
            }
            if let problem {
                Text(problem).font(.caption).foregroundStyle(.red)
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if applied { Label("Applied", systemImage: "checkmark.circle.fill").font(.caption).foregroundStyle(.green) }
                Spacer()
                Button("Revert") { form = StreamForm(stream); error = nil }
                    .disabled(change == nil || isApplying)
                Button(isApplying ? "Applying…" : "Apply") { run() }
                    .buttonStyle(.borderedProminent)
                    .disabled(change == nil || problem != nil || isApplying)
            }
        }
        .padding(14)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private func run() {
        guard let change, problem == nil else { return }
        isApplying = true
        error = nil
        applied = false
        Task {
            defer { isApplying = false }
            do {
                try await apply(change)
                applied = true
            } catch {
                // The camera's own refusal text, verbatim.
                self.error = error.localizedDescription
            }
        }
    }
}

/// A bounded value is a slider (tight range) or a stepper; one the camera
/// didn't bound is a plain numeric field.
private struct NumberRow: View {
    let title: String
    let unit: String
    @Binding var value: Int
    let range: V2.StreamRange

    var body: some View {
        HStack {
            Text(title).frame(width: 130, alignment: .leading)
            if StreamSettingsModel.usesSlider(range) {
                Slider(value: Binding(get: { Double(value) }, set: { value = StreamSettingsModel.clamp(Int($0.rounded()), range) }),
                       in: Double(range.min)...Double(range.max), step: 1)
                Text("\(value)").monospacedDigit().frame(width: 52, alignment: .trailing)
            } else if StreamSettingsModel.isBounded(range) {
                Stepper(value: Binding(get: { value }, set: { value = StreamSettingsModel.clamp($0, range) }),
                        in: range.min...range.max, step: step) {
                    Text("\(value)").monospacedDigit()
                }
                Text("\(range.min)–\(range.max)").font(.caption).foregroundStyle(.secondary)
            } else {
                TextField("", value: $value, format: .number.grouping(.never))
                    .multilineTextAlignment(.trailing)
                    .frame(width: 100)
                Text("no limit reported").font(.caption).foregroundStyle(.secondary)
            }
            if unit.isEmpty == false { Text(unit).font(.caption).foregroundStyle(.secondary) }
            Spacer(minLength: 0)
        }
    }

    private var step: Int { max(1, (range.max - range.min) / 100) }
}
