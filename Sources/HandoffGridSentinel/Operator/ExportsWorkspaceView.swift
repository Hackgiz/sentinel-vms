import AppKit
import SwiftUI
import SentinelCore

/// Exports: lock any camera and time range as evidence, and manage the cases
/// (including those locked from alarms).
///
/// Wiring (coordinator, at merge):
/// ```
/// ExportsWorkspaceView()                                  // plain tab
/// ExportsWorkspaceView(prefill: ExportPrefill(...))        // from Playback
/// ExportsWorkspaceView(prefill: p, onNotice: { text, isError in notices.post(...) })
/// ```
/// - `prefill`: when non-nil (and each time it changes) the create panel
///   takes that camera and range. Playback's export sheet raises it through
///   its `onLockAsEvidence` callback (see `PlaybackExportSheet`).
/// - `onNotice`: optional hook the coordinator can point at stream A's
///   notice bus; called once per finished batch ("Locked 2 clips…").
///
/// The create POST is synchronous (the recording server cuts the clip before
/// answering), so a progress row stands in for the case while it runs.
struct ExportsWorkspaceView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var directory: CameraDirectory

    var prefill: ExportPrefill?
    var onNotice: ((_ message: String, _ isError: Bool) -> Void)?

    init(prefill: ExportPrefill? = nil, onNotice: ((_ message: String, _ isError: Bool) -> Void)? = nil) {
        self.prefill = prefill
        self.onNotice = onNotice
    }

    @State private var cases: [V2.EvidenceCase]?
    @State private var error: String?
    @State private var status: String?
    @State private var chosen: [String] = []
    @State private var from = Date().addingTimeInterval(-300)
    @State private var to = Date()
    @State private var title = ""
    @State private var note = ""
    @State private var progress: ExportProgress?
    @State private var verifies: [String: VerifyState] = [:]
    @State private var downloading: Set<String> = []
    @State private var playing: Set<String> = [] // wave 23
    @State private var copy = SHACopyModel()
    @State private var deleting: V2.EvidenceCase?
    @State private var pickingCameras = false
    @State private var lockOptions = ExportOptions() // WAVE11(B): watermark + pixelate regions

    private var canExport: Bool { session.can(.exportEvidence) }
    private var canDelete: Bool { session.can(.manageUsers) }
    private var sortedCameras: [V2.Camera] { directory.cameras.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending } }
    private var blocker: String? {
        ExportRules.blocker(cameraIDs: chosen, from: from.unixMillis, to: to.unixMillis)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if canExport {
                createPanel
                Divider()
            } else {
                Text("Your role can view cases but not create exports.")
                    .font(.callout).foregroundStyle(.secondary).padding(14)
                Divider()
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true).padding(.horizontal, 14).padding(.top, 8)
            }
            if let progress {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text(progress.text).font(.callout)
                    Text("The recording server is cutting the clip.").font(.caption).foregroundStyle(.secondary)
                }
                .padding(.horizontal, 14).padding(.vertical, 8)
            }
            casesTable
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .task { await load() }
        .task(id: prefill) { applyPrefill() }
        .confirmationDialog("Release this evidence?", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })) {
            Button("Delete Evidence", role: .destructive) {
                if let target = deleting { Task { await delete(target) } }
            }
        } message: {
            if let deleting {
                Text("\(deleting.caseId) (\(deleting.cameraName)) and its video file will be deleted from the recording server. This can't be undone.")
            }
        }
        .modifier(ExportsHubFrame(prefill: prefill)) // WAVE11(B): Evidence|Scheduled segments, Long export panel, Shared Links toolbar button
    }

    // MARK: Create

    private var createPanel: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Lock footage as evidence").font(.headline)
            HStack(spacing: 10) {
                Button {
                    pickingCameras.toggle()
                } label: {
                    Label(chosen.isEmpty ? "Pick cameras" : "\(chosen.count) camera\(chosen.count == 1 ? "" : "s")", systemImage: "video")
                }
                .popover(isPresented: $pickingCameras, arrowEdge: .bottom) { cameraPicker }
                DatePicker("From", selection: $from, displayedComponents: [.date, .hourAndMinute])
                DatePicker("To", selection: $to, displayedComponents: [.date, .hourAndMinute])
                Spacer(minLength: 0)
            }
            HStack(spacing: 10) {
                TextField("Title (optional)", text: $title).frame(width: 220)
                TextField("Note (optional)", text: $note)
                ExportOptionsFields(options: $lockOptions, camera: chosen.count == 1 ? directory.camera(chosen[0]) : nil, disabled: progress != nil) // WAVE11(B)
                Button(chosen.count > 1 ? "Lock \(chosen.count) Clips" : "Lock Clip") { create() }
                    .buttonStyle(.borderedProminent)
                    .disabled(blocker != nil || progress != nil)
                    .help(blocker ?? "Cut each camera's range into the evidence vault")
            }
            if let n = lockOptions.note { Text(n).font(.caption).foregroundStyle(.secondary) } // WAVE11(B)
            if chosen.isEmpty == false {
                Text(chosen.map { directory.name(of: $0) }.joined(separator: ", ") + (blocker.map { " — \($0)" } ?? ""))
                    .font(.caption).foregroundStyle(.secondary).lineLimit(2)
            }
            if let status, error == nil {
                Text(status).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(14)
    }

    private var cameraPicker: some View {
        VStack(alignment: .leading, spacing: 6) {
            ScrollView {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(sortedCameras) { camera in
                        Toggle(camera.name, isOn: Binding(
                            get: { chosen.contains(camera.id) },
                            set: { on in
                                if on { chosen.append(camera.id) } else { chosen.removeAll { $0 == camera.id } }
                            }))
                    }
                    if sortedCameras.isEmpty { Text("No cameras.").foregroundStyle(.secondary) }
                }
                .padding(.trailing, 6)
            }
            .frame(width: 240, height: min(CGFloat(max(sortedCameras.count, 1)) * 24 + 8, 300))
            if chosen.isEmpty == false {
                Button("Clear") { chosen = [] }.controlSize(.small)
            }
        }
        .padding(12)
    }

    /// Takes the handed-over camera and range, limited to cameras this session can see.
    private func applyPrefill() {
        guard let prefill else { return }
        let visible = directory.cameras.map(\.id)
        chosen = ExportRules.limit([prefill.cameraID], toVisible: visible)
        if prefill.from > 0 { from = Date(unixMillis: prefill.from) }
        if prefill.to > 0 { to = Date(unixMillis: prefill.to) }
    }

    private func create() {
        guard blocker == nil, progress == nil else { return }
        let ids = ExportRules.limit(chosen, toVisible: directory.cameras.map(\.id))
        let label = ExportRules.batchLabel(count: ids.count, at: Date())
        let fullNote = ExportRules.composeNote(label: label, note: note)
        let request = (from: from.unixMillis, to: to.unixMillis, title: ExportRules.title(title),
                       note: String(fullNote.prefix(ExportRules.noteLimit)))
        let api = runtime.api
        let options = lockOptions // WAVE11(B)
        let names = Dictionary(uniqueKeysWithValues: ids.map { ($0, directory.name(of: $0)) })
        error = nil
        status = nil
        Task {
            let outcome = await ExportBatch.run(
                cameraIDs: ids, name: { names[$0] ?? "Camera" },
                progress: { progress = $0 },
                create: { id in
                    _ = try await api.createEvidence(withOptions: V2.EvidenceOptionsRequest(
                        cameraId: id, fromMs: request.from, toMs: request.to, title: request.title,
                        note: request.note.isEmpty ? nil : request.note,
                        watermark: options.watermarkField, blurRegions: options.regionsField))
                })
            progress = nil
            if outcome.failures.isEmpty == false { error = outcome.failures.joined(separator: "\n") }
            status = outcome.summary
            onNotice?(outcome.summary, outcome.failures.isEmpty == false)
            if outcome.made > 0 {
                title = ""
                note = ""
            }
            await load()
        }
    }

    // MARK: Cases

    private var casesTable: some View {
        Group {
            if let cases {
                if cases.isEmpty {
                    VStack(spacing: 6) {
                        Image(systemName: "lock.doc").font(.system(size: 28)).foregroundStyle(.secondary)
                        Text("No evidence yet").font(.headline)
                        Text("Lock a range above, or use “Lock as Evidence…” from the Playback export sheet.")
                            .font(.callout).foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    Table(cases) {
                        TableColumn("Locked") { c in
                            VStack(alignment: .leading, spacing: 1) {
                                Text(EvidenceDisplay.defaultFormat(c.lockedAt))
                                Text(c.caseId).font(.caption.monospaced()).foregroundStyle(.secondary)
                            }
                        }.width(min: 130, ideal: 160)
                        TableColumn("Camera") { c in
                            VStack(alignment: .leading, spacing: 1) {
                                HStack(spacing: 4) {
                                    Text(EvidenceDisplay.sourceName(c)).fontWeight(.semibold)
                                    if c.alarmId != nil { Text("alarm").font(.caption2).padding(.horizontal, 4).background(SentinelTheme.amber.opacity(0.3), in: Capsule()) }
                                }
                                if c.isUpload { UploadedFromIPhoneTag() } // wave 23
                            }
                        }.width(min: 90, ideal: 130)
                        TableColumn("Range") { c in
                            VStack(alignment: .leading, spacing: 1) {
                                Text(EvidenceDisplay.whenText(c)).font(.callout)
                                if c.title.isEmpty == false { Text(c.title).font(.caption).foregroundStyle(.secondary) }
                                if c.isUpload { // wave 23: uploader · device · recorded at
                                    Text(EvidenceDisplay.uploadDetail(c)).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                }
                            }
                        }.width(min: 150, ideal: 220)
                        TableColumn("Size") { c in Text(EvidenceDisplay.bytes(c.size)) }.width(min: 60, ideal: 70)
                        TableColumn("SHA-256") { c in shaCell(c) }.width(min: 170, ideal: 220)
                        TableColumn("By") { c in Text(c.lockedBy) }.width(min: 60, ideal: 80)
                        TableColumn("Note") { c in Text(c.note).font(.caption).lineLimit(2) }
                        TableColumn("") { c in actions(c) }.width(min: 270, ideal: 300)
                    }
                }
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    private func shaCell(_ c: V2.EvidenceCase) -> some View {
        HStack(spacing: 6) {
            Text(EvidenceDisplay.shortSHA(c.sha256)).font(.caption.monospaced()).help(c.sha256)
            Button(copy.label(for: c.id)) {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(copy.text(for: c), forType: .string)
                copy.markCopied(c.id)
                Task {
                    try? await Task.sleep(nanoseconds: 1_500_000_000)
                    copy.reset(c.id)
                }
            }
            .controlSize(.small)
            .accessibilityLabel("Copy SHA-256 of \(c.caseId)")
        }
    }

    private func actions(_ c: V2.EvidenceCase) -> some View {
        HStack(spacing: 6) {
            Button(playing.contains(c.id) ? "Opening…" : "Play") { Task { await play(c) } }
                .disabled(playing.contains(c.id))
                .help("Open the clip in the default video player")
            Button(downloading.contains(c.id) ? "Downloading…" : "Download") { Task { await download(c) } }
                .disabled(downloading.contains(c.id))
            Button(verifies[c.id] == .running ? "Verifying…" : "Verify") { Task { await verify(c) } }
                .disabled(verifies[c.id] == .running)
            switch verifies[c.id] {
            case .ok:
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green).help("The file matches its recorded SHA-256")
            case .failed(let why):
                Image(systemName: "xmark.circle.fill").foregroundStyle(.red).help(why)
            default:
                EmptyView()
            }
            ExportsCaseActions(evidence: c) // WAVE11(B): Package… / Share… menu
            if canDelete {
                Button("Delete", role: .destructive) { deleting = c }
            }
        }
        .controlSize(.small)
    }

    // MARK: Actions

    private func load() async {
        do {
            cases = try await runtime.api.evidenceCases()
            error = nil
        } catch {
            if cases == nil { cases = [] }
            self.error = ExportRules.failureText(error)
        }
    }

    private func download(_ c: V2.EvidenceCase) async {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = EvidenceDisplay.downloadName(c) // wave 23: .mov for QuickTime uploads
        panel.allowedContentTypes = [EvidenceDisplay.fileExtension(c) == "mov" ? .quickTimeMovie : .mpeg4Movie]
        panel.canCreateDirectories = true
        guard panel.runModal() == .OK, let url = panel.url else { return }
        downloading.insert(c.id)
        defer { downloading.remove(c.id) }
        do {
            try await runtime.api.downloadEvidence(c.id, to: url)
            error = nil
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }

    /// Downloads to a temporary file and opens it in the default player (QuickTime).
    private func play(_ c: V2.EvidenceCase) async {
        playing.insert(c.id)
        defer { playing.remove(c.id) }
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("SentinelEvidence", isDirectory: true)
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let url = folder.appendingPathComponent(EvidenceDisplay.downloadName(c))
        do {
            try await runtime.api.downloadEvidence(c.id, to: url)
            error = nil
            NSWorkspace.shared.open(url)
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }

    private func verify(_ c: V2.EvidenceCase) async {
        verifies[c.id] = .running
        do {
            let result = try await runtime.api.verifyEvidence(c.id)
            verifies[c.id] = VerifyState.from(result)
            if result.ok == false { onNotice?("Evidence \(c.caseId) does not match its recorded SHA-256.", true) }
        } catch {
            verifies[c.id] = nil
            self.error = ExportRules.failureText(error)
        }
    }

    private func delete(_ c: V2.EvidenceCase) async {
        do {
            try await runtime.api.deleteEvidence(c.id)
            deleting = nil
            await load()
        } catch {
            deleting = nil
            self.error = ExportRules.failureText(error)
        }
    }
}
