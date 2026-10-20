import SwiftUI
import SentinelCore

/// "Recover from camera" (Wave 11, stream A): confirm the range (default:
/// the marked selection with gaps, else the visible window, clamped to 24 h
/// and to now), start the job, then follow its progress — the timeline bar's
/// visible interval reads it while it runs, so the sheet can be closed.
struct EdgeRecoverySheet: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @ObservedObject var store: EdgeRecoveryStore
    @ObservedObject var controller: PlaybackController
    let camera: V2.Camera?
    let cameraID: String
    @Environment(\.dismiss) private var dismiss
    @State private var from: Date
    @State private var to: Date

    init(store: EdgeRecoveryStore, controller: PlaybackController, camera: V2.Camera?, cameraID: String,
         initial: TimelineGaps.Range?) {
        self.store = store
        self.controller = controller
        self.camera = camera
        self.cameraID = cameraID
        let now = Date().unixMillis
        let r = initial ?? TimelineGaps.Range(start: now - 3_600_000, end: now)
        _from = State(initialValue: Date(unixMillis: r.start))
        _to = State(initialValue: Date(unixMillis: r.end))
    }

    private var range: TimelineGaps.Range { TimelineGaps.Range(start: from.unixMillis, end: to.unixMillis) }
    /// The job shown here: this camera's (running or just finished).
    private var job: V2.EdgeRecovery? { store.job?.cameraId == cameraID ? store.job : nil }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Recover from Camera").font(.headline)
            Text("Pull \(camera?.name ?? "the camera")'s own SD-card footage into the gaps of the recordings. Footage already recorded here is never replaced; recovered footage plays back, exports and expires like any other and shows hatched on the timeline.")
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let job {
                progress(job)
            } else {
                rangeFields
            }
            if let error = store.error {
                Text(error).font(.caption).foregroundStyle(SentinelTheme.alarm).fixedSize(horizontal: false, vertical: true)
            }
            buttons
        }
        .padding(20)
        .frame(width: 440)
    }

    private var rangeFields: some View {
        let problem = EdgeRecoveryLogic.problem(range, now: Date().unixMillis)
        return VStack(alignment: .leading, spacing: 8) {
            DatePicker("From", selection: $from, in: ...Date(), displayedComponents: [.date, .hourAndMinute])
            DatePicker("To", selection: $to, in: ...Date(), displayedComponents: [.date, .hourAndMinute])
            HStack {
                Text(BookmarkLogic.durationText(max(range.end - range.start, 0)))
                    .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                Text("· at most 24 hours").font(.caption).foregroundStyle(.secondary)
            }
            if let problem { Text(problem).font(.caption).foregroundStyle(SentinelTheme.amber) }
        }
    }

    private func progress(_ j: V2.EdgeRecovery) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("\(OperatorFormat.dateTime.string(from: Date(unixMillis: j.fromMs))) – \(OperatorFormat.dateTime.string(from: Date(unixMillis: j.toMs)))")
                .font(.caption).foregroundStyle(.secondary)
            if EdgeRecoveryLogic.isActive(j) {
                if j.pieces > 0 {
                    ProgressView(value: Double(j.progress), total: 100)
                } else {
                    ProgressView().progressViewStyle(.linear)
                }
            }
            Text(EdgeRecoveryLogic.text(j)).font(.callout)
            HStack(spacing: 16) {
                ForEach(EdgeRecoveryLogic.counters(j), id: \.label) { c in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(c.label).font(.caption2).foregroundStyle(.secondary)
                        Text(c.value).font(.callout.monospacedDigit())
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var buttons: some View {
        HStack {
            if let job, EdgeRecoveryLogic.isActive(job) {
                Button("Stop Recovery", role: .destructive) {
                    Task { await store.cancel(api: runtime.api, controller: controller) }
                }
                Spacer()
                Button("Close") { close() }.keyboardShortcut(.defaultAction)
                    .help("The recovery keeps going; its progress shows on the timeline bar.")
            } else if job != nil {
                Button("Recover Another Range") { store.error = nil; store.clearFinished() }
                Spacer()
                Button("Done") { close() }.keyboardShortcut(.defaultAction)
            } else {
                Spacer()
                Button("Cancel") { close() }.keyboardShortcut(.cancelAction)
                Button("Start Recovery") {
                    Task { await store.start(api: runtime.api, cameraID: cameraID, range: range, controller: controller) }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(store.isStarting || store.isRunning || EdgeRecoveryLogic.problem(range, now: Date().unixMillis) != nil)
            }
        }
    }

    private func close() {
        store.sheetCameraID = nil
        dismiss()
    }
}
