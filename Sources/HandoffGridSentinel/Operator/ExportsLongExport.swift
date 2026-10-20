import AppKit
import SwiftUI
import UniformTypeIdentifiers
import SentinelCore

/// Long-export jobs started from this Mac, per site, kept while the app runs
/// so leaving the Exports workspace doesn't lose a running job.
@MainActor
final class ExportJobsStore: ObservableObject {
    static let shared = ExportJobsStore()

    @Published private var lists: [String: ExportJobList] = [:]

    func list(_ siteID: String) -> ExportJobList { lists[siteID] ?? ExportJobList() }

    func update(_ siteID: String, _ change: (inout ExportJobList) -> Void) {
        var list = self.list(siteID)
        change(&list)
        lists[siteID] = list
    }
}

/// Long export: up to 12 hours of one camera handed to an export job on the
/// server (cut in keyframe-aligned pieces and joined). Running jobs are
/// polled with one visible-view interval that stops when none runs; done
/// download jobs get "Save…" (streamed to disk), evidence jobs refresh the
/// case list.
struct LongExportPanel: View {
    var onFiled: () -> Void

    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var directory: CameraDirectory
    @ObservedObject private var store = ExportJobsStore.shared

    @AppStorage("exports.longExport.expanded") private var expanded = true
    @State private var cameraID = ""
    @State private var from = Date().addingTimeInterval(-8 * 3600)
    @State private var to = Date()
    @State private var dest: V2.ExportDest = .evidence
    @State private var title = ""
    @State private var note = ""
    @State private var options = ExportOptions()
    @State private var starting = false
    @State private var error: String?
    @State private var onScreen = false
    @State private var saving: Set<String> = []

    private var siteID: String { runtime.api.siteID }
    private var jobs: ExportJobList { store.list(siteID) }
    private var sortedCameras: [V2.Camera] { directory.cameras.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending } }
    private var blocker: String? { LongExportRules.blocker(cameraID: cameraID, from: from.unixMillis, to: to.unixMillis) }
    private var polling: Bool { onScreen && jobs.hasRunning }

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            form.padding(.top, 8)
        } label: {
            HStack(spacing: 8) {
                Text("Long export").font(.headline)
                Text("up to 12 hours of one camera as a single file").font(.caption).foregroundStyle(.secondary)
                if jobs.hasRunning {
                    ProgressView().controlSize(.small)
                    Text("\(jobs.running.count) running").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .padding(14)
        .trackOnScreen($onScreen)
        .task(id: polling) {
            await visibleInterval(polling, seconds: LongExportRules.pollSeconds) { await poll() }
        }
    }

    private var form: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("The server cuts the range in pieces and joins them (without re-encoding unless a watermark or pixelated regions are on); it needs about twice the file's size free on the recording server.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 10) {
                Picker("Camera", selection: $cameraID) {
                    Text("Pick a camera").tag("")
                    ForEach(sortedCameras) { Text($0.name).tag($0.id) }
                }
                .frame(width: 240)
                DatePicker("From", selection: $from, displayedComponents: [.date, .hourAndMinute])
                DatePicker("To", selection: $to, displayedComponents: [.date, .hourAndMinute])
                if to > from {
                    Text(LongExportRules.spanText(to.unixMillis - from.unixMillis)).font(.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            HStack(spacing: 10) {
                Picker("Destination", selection: $dest) {
                    Text("Evidence vault").tag(V2.ExportDest.evidence)
                    Text("Download").tag(V2.ExportDest.download)
                }
                .frame(width: 220)
                if dest == .evidence {
                    TextField("Title (optional)", text: $title).frame(width: 180)
                    TextField("Note (optional)", text: $note).frame(minWidth: 120)
                }
                ExportOptionsFields(options: $options, camera: directory.camera(cameraID.isEmpty ? nil : cameraID), disabled: starting)
                Spacer(minLength: 0)
                Button(starting ? "Starting…" : "Start Long Export") { Task { await start() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(blocker != nil || starting)
                    .help(blocker ?? "Start the export on the recording server")
            }
            if let n = options.note {
                Text(n).font(.caption).foregroundStyle(.secondary)
            }
            if let blocker, cameraID.isEmpty == false {
                Text(blocker).font(.caption).foregroundStyle(.secondary)
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            if jobs.jobs.isEmpty == false { jobList }
        }
    }

    private var jobList: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Jobs").font(.subheadline.weight(.semibold))
                Spacer()
                if jobs.jobs.count > jobs.running.count {
                    Button("Clear Finished") { store.update(siteID) { $0.clearFinished() } }
                        .controlSize(.small).buttonStyle(.borderless)
                }
            }
            ForEach(jobs.jobs) { job in jobRow(job) }
        }
        .padding(10)
        .background(SentinelTheme.well, in: RoundedRectangle(cornerRadius: SentinelMetrics.controlRadius))
    }

    private func jobRow(_ job: V2.ExportJob) -> some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                Text(directory.name(of: job.cameraId)).fontWeight(.semibold)
                Text("\(EvidenceDisplay.defaultFormat(job.fromMs)) – \(EvidenceDisplay.defaultTime(job.toMs)) · \(job.dest == .evidence ? "evidence" : "download")")
                    .font(.caption).foregroundStyle(.secondary)
            }
            .frame(width: 300, alignment: .leading)
            if LongExportRules.isActive(job) {
                ProgressView(value: Double(min(max(job.progress, 0), 100)), total: 100).frame(width: 140)
            }
            Text(LongExportRules.jobText(job))
                .font(.callout)
                .foregroundStyle(job.state == .failed ? Color.red : Color.primary)
                .lineLimit(2)
            Spacer(minLength: 0)
            if LongExportRules.isActive(job) {
                Button("Cancel", role: .destructive) { Task { await cancel(job) } }.controlSize(.small)
            }
            if LongExportRules.canSave(job) {
                Text("Kept for 30 minutes.").font(.caption).foregroundStyle(.secondary)
                Button(saving.contains(job.taskId) ? "Saving…" : "Save…") { Task { await save(job) } }
                    .controlSize(.small)
                    .buttonStyle(.borderedProminent)
                    .disabled(saving.contains(job.taskId))
            }
            if LongExportRules.isActive(job) == false {
                Button {
                    store.update(siteID) { $0.remove(taskID: job.taskId) }
                } label: {
                    Image(systemName: "xmark")
                }
                .buttonStyle(.borderless)
                .help("Remove from the list")
            }
        }
    }

    // MARK: Actions

    private func start() async {
        guard blocker == nil, starting == false else { return }
        starting = true
        error = nil
        defer { starting = false }
        let request = LongExportRules.request(cameraID: cameraID, from: from.unixMillis, to: to.unixMillis, dest: dest,
                                              title: title, note: note, options: options)
        do {
            let job = try await runtime.api.startExportJob(request)
            store.update(siteID) { $0.upsert(job) }
            if LongExportRules.isActive(job) == false { announce(job) }
            title = ""
            note = ""
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }

    private func poll() async {
        let before = jobs
        for job in before.running {
            do {
                let fresh = try await runtime.api.exportJob(taskID: job.taskId)
                store.update(siteID) { $0.upsert(fresh) }
            } catch let failure as SentinelAPIError {
                if case .server(let status, _, _) = failure, status == 404 {
                    // The server forgets finished jobs after an hour (or restarted): stop polling it.
                    var gone = job
                    gone.state = .failed
                    gone.error = "The server no longer has this job."
                    store.update(siteID) { $0.upsert(gone) }
                } else {
                    error = ExportRules.failureText(failure)
                }
            } catch {
                self.error = ExportRules.failureText(error)
            }
        }
        for job in jobs.finished(since: before) { announce(job) }
    }

    private func announce(_ job: V2.ExportJob) {
        let camera = directory.name(of: job.cameraId)
        NoticeCenter.shared.post(job.state == .failed ? .error : .success, "Long export of \(camera): \(LongExportRules.jobText(job))")
        if job.state == .done, job.dest == .evidence { onFiled() }
    }

    private func cancel(_ job: V2.ExportJob) async {
        do {
            let stopped = try await runtime.api.cancelExportJob(taskID: job.taskId)
            store.update(siteID) { $0.upsert(stopped) }
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }

    private func save(_ job: V2.ExportJob) async {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = LongExportRules.suggestedFilename(job, cameraName: directory.name(of: job.cameraId))
        panel.allowedContentTypes = [.mpeg4Movie]
        panel.canCreateDirectories = true
        guard panel.runModal() == .OK, let url = panel.url else { return }
        saving.insert(job.taskId)
        defer { saving.remove(job.taskId) }
        do {
            try await runtime.api.downloadExportJob(taskID: job.taskId, to: url)
            error = nil
            NoticeCenter.shared.post(.success, "Saved \(url.lastPathComponent).")
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }
}
