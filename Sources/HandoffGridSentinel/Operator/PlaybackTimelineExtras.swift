import SwiftUI
import SentinelCore

/// Wave 11 (stream A) controls in the timeline bar's top row: the
/// **Bookmarks** day list button and, when the focused camera keeps its own
/// recordings and the visible window has gaps, **Recover from camera**.
/// It also owns the bookmark sheet (opened by **B**, a glyph or the list),
/// loads the day's bookmarks for the timeline's cameras, and runs the edge
/// recovery's visible interval while a job is going.
struct PlaybackTimelineExtras: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @ObservedObject var controller: PlaybackController
    @ObservedObject private var bookmarks: BookmarkStore
    @ObservedObject private var edge: EdgeRecoveryStore
    /// The camera whose band the ruler shows.
    let cameraID: String?
    /// Opens the export sheet (with the selection already set).
    let onExport: () -> Void

    init(controller: PlaybackController, cameraID: String?, onExport: @escaping () -> Void) {
        self.controller = controller
        self.bookmarks = BookmarkStore.for(controller)
        self.edge = EdgeRecoveryStore.for(controller)
        self.cameraID = cameraID
        self.onExport = onExport
    }

    private var loadKey: String {
        "\(controller.windowFrom)|\(controller.cameraIDs.joined(separator: ","))"
    }

    var body: some View {
        HStack(spacing: 6) {
            bookmarksButton
            recoverButton
        }
        .task(id: loadKey) {
            await bookmarks.load(api: runtime.api, cameraIDs: controller.cameraIDs, from: controller.windowFrom, to: controller.windowTo)
        }
        .task(id: edge.isRunning) {
            await visibleInterval(edge.isRunning, seconds: EdgeRecoveryLogic.pollSeconds) {
                await edge.refresh(api: runtime.api, controller: controller)
            }
        }
        .sheet(isPresented: Binding(get: { bookmarks.draft != nil }, set: { if $0 == false { bookmarks.draft = nil } })) {
            BookmarkEditorSheet(store: bookmarks, cameraName: cameras.name(of: bookmarks.draft?.cameraID))
        }
        .sheet(isPresented: Binding(get: { edge.sheetCameraID != nil }, set: { if $0 == false { edge.sheetCameraID = nil } })) {
            if let id = edge.sheetCameraID {
                EdgeRecoverySheet(store: edge, controller: controller, camera: cameras.camera(id), cameraID: id,
                                  initial: recoverTarget(cameraID: id))
            }
        }
    }

    // MARK: Bookmarks

    private var bookmarksButton: some View {
        Button { bookmarks.showsList.toggle() } label: {
            Label("\(bookmarks.book.all.count)", systemImage: "bookmark")
                .monospacedDigit()
        }
        .help("Bookmarks on this day (B adds one at the playhead)")
        .popover(isPresented: $bookmarks.showsList, arrowEdge: .bottom) {
            BookmarkDayList(store: bookmarks, controller: controller, selectedCameraID: cameraID,
                            canExport: session.can(.exportEvidence),
                            onExport: { b in
                                let r = BookmarkLogic.exportRange(b, now: Date().unixMillis)
                                bookmarks.showsList = false
                                controller.seek(to: r.from)
                                controller.selection = ExportSelection(markIn: r.from, markOut: r.to)
                                onExport()
                            })
                .environmentObject(cameras)
        }
    }

    // MARK: Recover from camera

    private var visibleWindow: TimelineGaps.Range {
        let r = TimelineRuler.visibleRange(center: controller.position, span: controller.visibleSpan)
        return TimelineGaps.Range(start: r.from, end: r.to)
    }

    /// Recorded and pending footage of `id` as ranges (pending counts as covered, like the web).
    private func covered(_ id: String) -> [TimelineGaps.Range] {
        guard let tl = controller.timelines[id] else { return [] }
        return (tl.spans + tl.pending).map { TimelineGaps.Range(start: $0.start, end: $0.end) }
    }

    private func recoverTarget(cameraID id: String) -> TimelineGaps.Range? {
        let selection = controller.selection.range.map { TimelineGaps.Range(start: $0.from, end: $0.to) }
        return EdgeRecoveryLogic.target(selection: selection, window: visibleWindow, spans: covered(id), now: Date().unixMillis)
    }

    @ViewBuilder
    private var recoverButton: some View {
        if let id = cameraID, let camera = cameras.camera(id), controller.timelines[id] != nil {
            let runningHere = edge.isRunning && edge.job?.cameraId == id
            let offered = EdgeRecoveryLogic.offers(edgeRecording: camera.edgeRecording == true, canExport: session.can(.exportEvidence),
                                                   spans: covered(id), window: visibleWindow, now: Date().unixMillis)
            if offered || runningHere {
                Button { edge.open(cameraID: id) } label: {
                    if runningHere {
                        Label("Recovering… \(edge.job?.progress ?? 0)%", systemImage: "sdcard")
                    } else {
                        Label("Recover from camera", systemImage: "sdcard")
                    }
                }
                .disabled(edge.isRunning && runningHere == false)
                .help(edge.isRunning && runningHere == false
                      ? "A recovery is running for another camera."
                      : EdgeRecoveryLogic.title(recoverTarget(cameraID: id)))
            }
        }
    }
}
