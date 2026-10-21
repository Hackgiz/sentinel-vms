import AppKit
import SwiftUI
import SentinelCore

// Management › System Dashboard › Detection (wave 17, the server's wave 16):
// which detector model runs and how often, what is actually running, and the
// Models table (download, cancel, delete) for every optional model. Labels and
// rules are in SentinelCore's DetectionLogic, shared with the web card.

struct DetectionPane: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime

    @State private var settings: V2.DetectorSettings?
    @State private var models: [V2.Model] = []
    @State private var dir = ""
    @State private var loaded = false
    @State private var busy = ""
    @State private var error: String?
    @State private var saved: String?
    @State private var deleting: V2.Model?
    @State private var onScreen = false

    private var canChange: Bool { session.can(.changeSettings) }
    private var downloading: Bool { DetectionLogic.anyDownloading(models) }

    var body: some View {
        PaneFrame(title: "Detection", subtitle: "Object detection, and the optional models behind appearance search, audio classes, faces, plates and semantic search",
                  error: error, status: saved, trailing: {
            Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }.help("Reload")
        }) {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let settings {
                        detectorCard(settings)
                        modelsCard(selected: DetectionLogic.savedModel(settings))
                    } else if loaded == false {
                        ProgressView().frame(maxWidth: .infinity)
                    }
                }
                .padding(14)
            }
        }
        .trackOnScreen($onScreen)
        .task { await load() }
        // Poll quickly while a download runs, slowly otherwise, only while on screen.
        .task(id: "\(downloading)-\(onScreen)") {
            guard onScreen else { return }
            let interval = DetectionLogic.pollInterval(models)
            while Task.isCancelled == false {
                try? await Task.sleep(nanoseconds: UInt64(interval * 1_000_000_000))
                if Task.isCancelled { return }
                await load(quiet: true)
            }
        }
        .alert(deleting.map { "Delete \($0.title)?" } ?? "", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
               presenting: deleting) { model in
            Button("Delete", role: .destructive) { Task { await act(model, .delete) } }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Features that use it stop after the next restart.")
        }
    }

    // MARK: Loading and actions

    private func load(quiet: Bool = false) async {
        do {
            async let d = runtime.api.detectorSettings()
            async let m = runtime.api.models()
            let (det, catalog) = try await (d, m)
            settings = det
            models = catalog.items
            dir = catalog.dir
            if quiet == false { error = nil }
        } catch {
            if quiet == false || settings == nil { self.error = DetectionText.errorText(error, feature: "detection settings") }
        }
        loaded = true
    }

    private func save(_ input: V2.DetectorInput, ok: String) async {
        busy = "settings"
        error = nil
        saved = nil
        defer { busy = "" }
        do {
            settings = try await runtime.api.updateDetector(input)
            saved = ok
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func act(_ model: V2.Model, _ action: DetectionLogic.ModelAction) async {
        busy = model.id
        error = nil
        saved = nil
        defer { busy = "" }
        do {
            switch action {
            case .download: _ = try await runtime.api.downloadModel(model.id)
            case .cancel: _ = try await runtime.api.cancelModelDownload(model.id)
            case .delete: try await runtime.api.deleteModel(model.id)
            }
            await load(quiet: true)
        } catch {
            self.error = managementErrorText(error)
        }
    }

    // MARK: Detector

    private func detectorCard(_ s: V2.DetectorSettings) -> some View {
        let selected = DetectionLogic.savedModel(s)
        let choices = DetectionLogic.detectorChoices(models, saved: selected)
        return DetectionCardBox(title: "Detector") {
            HStack(alignment: .firstTextBaseline) {
                Text("Running").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                Text(DetectionLogic.statusLine(s, models: models)).font(.callout).textSelection(.enabled)
            }
            if let note = DetectionLogic.pendingNote(s) {
                Label(note, systemImage: "exclamationmark.triangle").font(.caption).foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Form {
                Picker("Detector model", selection: Binding(get: { selected }, set: { id in
                    Task { await save(.init(model: id), ok: "Model saved. Restart Sentinel to switch.") }
                })) {
                    ForEach(choices) { c in
                        Text(c.title + (c.installed ? "" : " (not installed)")).tag(c.id)
                    }
                }
                .disabled(canChange == false || busy.isEmpty == false)
                Text(choices.first { $0.id == selected }?.note.isEmpty == false
                     ? choices.first { $0.id == selected }!.note
                     : "Bigger models find smaller and farther objects but need more time per frame. Download more in the Models table.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Picker("Detection rate while there is motion", selection: Binding(get: { s.rate }, set: { rate in
                    Task { await save(.init(rate: rate), ok: "Rate saved. Cameras use it now.") }
                })) {
                    ForEach(DetectionLogic.rateOptions, id: \.value) { Text($0.label).tag($0.value) }
                }
                .disabled(canChange == false || busy.isEmpty == false)
                Text("Passes per second per camera. More passes track moving objects better and use more CPU.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            .formStyle(.grouped)
        }
    }

    // MARK: Models

    private func modelsCard(selected: String) -> some View {
        DetectionCardBox(title: "Models") {
            Text("Optional models behind detection, appearance search, audio classes, faces, plates and semantic search. Downloads come from the source each model lists and are checked against a pinned SHA-256.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 8) {
                GridRow {
                    Text("Model"); Text("Used for"); Text("Size"); Text("License"); Text("State")
                    if canChange { Text("") }
                }
                .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                Divider()
                ForEach(DetectionLogic.sorted(models)) { m in
                    GridRow {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(m.title).font(.callout.weight(.semibold))
                            if let note = m.note, note.isEmpty == false {
                                Text(note).font(.caption).foregroundStyle(.secondary).lineLimit(3)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .frame(minWidth: 180, maxWidth: 320, alignment: .leading)
                        Text(DetectionLogic.purposeLabel(m.purpose)).font(.caption)
                        Text(DetectionLogic.formatBytes(m.sizeBytes)).font(.caption.monospacedDigit())
                        licenseView(m)
                        stateView(m).frame(minWidth: 120, alignment: .leading)
                        if canChange {
                            HStack(spacing: 6) {
                                ForEach(DetectionLogic.actions(m, selected: selected, canChange: canChange), id: \.self) { action in
                                    Button(busy == m.id ? "Working…" : action.label, role: action == .delete ? .destructive : nil) {
                                        if action == .delete { deleting = m } else { Task { await act(m, action) } }
                                    }
                                    .disabled(busy.isEmpty == false)
                                }
                            }
                            .controlSize(.small)
                        }
                    }
                }
            }
            if let note = DetectionLogic.handInstallNote(dir: dir) {
                Text(note).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder
    private func licenseView(_ m: V2.Model) -> some View {
        if let source = m.source, let url = URL(string: source), source.isEmpty == false {
            Link(m.license, destination: url).font(.caption)
        } else {
            Text(m.license).font(.caption)
        }
    }

    private func stateView(_ m: V2.Model) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(DetectionLogic.stateLabel(m))
                .font(.caption2.weight(.semibold))
                .padding(.horizontal, 7).padding(.vertical, 3)
                .background(toneColor(m).opacity(0.18), in: Capsule())
                .foregroundStyle(toneColor(m))
                .help(m.download?.error ?? "")
            if m.state == .downloading {
                if let fraction = DetectionLogic.downloadFraction(m) {
                    ProgressView(value: fraction).frame(width: 110)
                        .accessibilityLabel("\(m.title) download")
                } else {
                    Text("Starting…").font(.caption2).foregroundStyle(.secondary)
                }
            }
            if m.download?.state == .failed, let err = m.download?.error, err.isEmpty == false {
                Text(err).font(.caption2).foregroundStyle(.red).lineLimit(3).frame(maxWidth: 200, alignment: .leading)
            }
        }
    }

    private func toneColor(_ m: V2.Model) -> Color {
        switch DetectionLogic.tone(m) {
        case .good: return .green
        case .busy: return SentinelTheme.accent
        case .bad: return SentinelTheme.alarm
        case .neutral: return .secondary
        }
    }
}
