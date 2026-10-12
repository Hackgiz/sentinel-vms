import AppKit
import SwiftUI
import SentinelCore

/// The bar along the bottom in Playback mode (~110 pt): a ruler you pan by
/// dragging and zoom with scroll or pinch (1 minute to 24 hours), recording
/// lanes, event and alarm markers, the playhead readout, transport controls
/// and the in/out export selection. The playhead is the ruler's center, so
/// dragging the ruler scrubs every tile on the shared clock.
struct PlaybackTimelineBar: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var cameras: CameraDirectory
    @ObservedObject var controller: PlaybackController
    let selectedCameraID: String?
    let onLive: () -> Void
    let onExport: () -> Void

    @State private var showsCalendar = false

    static let height: CGFloat = 116

    /// The camera whose recordings fill the band.
    private var bandCameraID: String? {
        if let id = selectedCameraID, controller.cameraIDs.contains(id) { return id }
        return controller.cameraIDs.first
    }

    var body: some View {
        VStack(spacing: 0) {
            topRow
                .frame(height: 28)
            ruler
                .frame(height: 54)
            transport
                .frame(height: 34)
        }
        .frame(height: Self.height)
        .background(SentinelTheme.chrome)
        .overlay(alignment: .top) { Rectangle().fill(SentinelTheme.line).frame(height: 1) }
    }

    // MARK: Top row: date, readout, export selection

    private var topRow: some View {
        ZStack {
            HStack(spacing: 8) {
                DatePicker("", selection: Binding(get: { Date(unixMillis: controller.position) },
                                                  set: { controller.seek(to: $0.unixMillis) }),
                           displayedComponents: [.date, .hourAndMinute])
                    .labelsHidden()
                    .datePickerStyle(.field)
                    .fixedSize()
                    .help("Jump to a date and time")
                Button { showsCalendar.toggle() } label: { Image(systemName: "calendar") }
                    .help("Pick a day")
                    .popover(isPresented: $showsCalendar, arrowEdge: .bottom) {
                        DatePicker("", selection: Binding(get: { Date(unixMillis: controller.position) },
                                                          set: { controller.seek(to: $0.unixMillis); showsCalendar = false }),
                                   in: ...Date(), displayedComponents: [.date])
                            .labelsHidden()
                            .datePickerStyle(.graphical)
                            .padding(12)
                    }
                if controller.isLoading { ProgressView().controlSize(.small) }
                Spacer()
                exportControls
            }
            Text(Self.readout(controller.position))
                .font(.system(size: 21, weight: .semibold, design: .monospaced))
                .monospacedDigit()
                .allowsHitTesting(false)
        }
        .controlSize(.small)
        .padding(.horizontal, 12)
    }

    static func readout(_ millis: UnixMillis) -> String {
        OperatorFormat.time.string(from: Date(unixMillis: millis))
    }

    private var exportControls: some View {
        HStack(spacing: 6) {
            Button { controller.selection.setIn(controller.position) } label: { Text("[ In") }
                .help("Mark the start of the export range ([)")
            Button { controller.selection.setOut(controller.position) } label: { Text("Out ]") }
                .help("Mark the end of the export range (])")
            if controller.selection.isEmpty == false {
                if let range = controller.selection.range {
                    Text(Self.duration(range.to - range.from)).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                }
                Button { controller.selection.clear() } label: { Image(systemName: "xmark.circle") }
                    .buttonStyle(.plain)
                    .help("Clear the selection")
            }
            if controller.selection.range != nil, session.can(.exportEvidence) {
                Button(action: onExport) { Label("Export…", systemImage: "square.and.arrow.up") }
                    .buttonStyle(.borderedProminent)
            }
        }
    }

    static func duration(_ millis: Int64) -> String {
        let total = Int(millis / 1000)
        let h = total / 3600, m = (total / 60) % 60, s = total % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, s) : String(format: "%d:%02d", m, s)
    }

    // MARK: Transport

    private var transport: some View {
        ZStack {
            HStack(spacing: 6) {
                Button { controller.jumpToGap(forward: false, cameraID: bandCameraID) } label: { Image(systemName: "backward.end.fill") }
                    .help("Previous gap in recording")
                Button { controller.step(seconds: -1) } label: { Image(systemName: "chevron.left") }
                    .help("Step back 1 second (←)")
                Button { controller.togglePlay() } label: {
                    Image(systemName: controller.isPlaying ? "pause.fill" : "play.fill").frame(width: 22)
                }
                .help(controller.isPlaying ? "Pause (space)" : "Play (space)")
                Button { controller.step(seconds: 1) } label: { Image(systemName: "chevron.right") }
                    .help("Step forward 1 second (→)")
                Button { controller.jumpToGap(forward: true, cameraID: bandCameraID) } label: { Image(systemName: "forward.end.fill") }
                    .help("Next gap in recording")
                Menu {
                    Picker("Speed", selection: $controller.rate) {
                        ForEach(PlaybackController.rates, id: \.self) { rate in
                            Text(Self.rateLabel(rate)).tag(rate)
                        }
                    }
                    .pickerStyle(.inline)
                } label: {
                    Text(Self.rateLabel(controller.rate)).monospacedDigit().frame(minWidth: 38)
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .help("Playback speed")
            }
            HStack {
                zoomControls
                Spacer()
                Button(action: onLive) {
                    Label("Live", systemImage: "dot.radiowaves.left.and.right")
                }
                .buttonStyle(.borderedProminent)
                .tint(SentinelTheme.alarm)
                .help("Back to Live (L)")
            }
        }
        .controlSize(.small)
        .padding(.horizontal, 12)
    }

    static func rateLabel(_ rate: Float) -> String {
        rate == rate.rounded() ? "\(Int(rate))×" : String(format: "%g×", rate)
    }

    private var zoomControls: some View {
        HStack(spacing: 4) {
            Button { controller.zoom(by: 0.5) } label: { Image(systemName: "minus.magnifyingglass") }
                .help("Zoom out")
            Text(Self.spanLabel(controller.visibleSpan)).font(.caption.monospacedDigit()).foregroundStyle(.secondary).frame(minWidth: 44)
            Button { controller.zoom(by: 2) } label: { Image(systemName: "plus.magnifyingglass") }
                .help("Zoom in")
        }
        .buttonStyle(.plain)
    }

    static func spanLabel(_ span: Int64) -> String {
        span >= 3_600_000 ? String(format: "%g h", Double(span) / 3_600_000) : String(format: "%g min", Double(span) / 60_000)
    }

    // MARK: Ruler

    private var ruler: some View {
        GeometryReader { proxy in
            let width = Double(proxy.size.width)
            let center = controller.position
            let span = controller.visibleSpan
            let range = TimelineRuler.visibleRange(center: center, span: span)
            let x: (Int64) -> CGFloat = { CGFloat(TimelineRuler.x(for: $0, width: width, center: center, span: span)) }
            let timeline = bandCameraID.flatMap { controller.timelines[$0] }

            ZStack(alignment: .topLeading) {
                RulerCanvas(range: range, span: span, width: width,
                            selectedSpans: timeline?.spans ?? [], pendingSpans: timeline?.pending ?? [],
                            otherSpans: otherSpans(), selection: controller.selection)
                markers(timeline: timeline, range: range, x: x)
                RulerInteraction(
                    onBegan: { controller.beginScrub() },
                    onPan: { dx in
                        controller.scrub(to: TimelineRuler.panned(center: controller.position, dx: Double(dx), width: width, span: controller.visibleSpan))
                    },
                    onEnded: { controller.endScrub() },
                    onZoom: { factor in controller.zoom(by: factor) }
                )
                selectionHandles(range: range, x: x, width: width, center: center, span: span)
                playhead.position(x: proxy.size.width / 2, y: proxy.size.height / 2)
            }
            .clipped()
        }
        .padding(.horizontal, 12)
    }

    private func otherSpans() -> [TimelineGaps.Range] {
        let others = controller.cameraIDs.filter { $0 != bandCameraID }
        let all = others.flatMap { id in (controller.timelines[id]?.spans ?? []).map { TimelineGaps.Range(start: $0.start, end: $0.end) } }
        return TimelineGaps.merge(all)
    }

    private var playhead: some View {
        ZStack(alignment: .top) {
            Rectangle().fill(Color.red).frame(width: 2)
            Image(systemName: "arrowtriangle.down.fill").font(.system(size: 8)).foregroundStyle(.red).offset(y: -1)
        }
        .allowsHitTesting(false)
    }

    @ViewBuilder
    private func markers(timeline: V2.Timeline?, range: (from: Int64, to: Int64), x: @escaping (Int64) -> CGFloat) -> some View {
        if let timeline {
            ZStack(alignment: .topLeading) {
                ForEach(timeline.events.filter { $0.createdAt >= range.from && $0.createdAt <= range.to }) { event in
                    Rectangle().fill(SentinelTheme.amber)
                        .frame(width: 2, height: 16)
                        .offset(x: x(event.createdAt) - 1, y: 22)
                        .help("\(event.label ?? event.kind) · \(OperatorFormat.time.string(from: Date(unixMillis: event.createdAt)))")
                }
                ForEach(timeline.alarms.filter { $0.createdAt >= range.from && $0.createdAt <= range.to }) { alarm in
                    Image(systemName: "triangle.fill")
                        .font(.system(size: 8))
                        .foregroundStyle(alarm.severity.tint)
                        .offset(x: x(alarm.createdAt) - 4, y: 24)
                        .help("\(alarm.title) · \(OperatorFormat.time.string(from: Date(unixMillis: alarm.createdAt)))")
                }
            }
        }
    }

    /// `[` and `]` handles on the ruler; drag to adjust.
    @ViewBuilder
    private func selectionHandles(range: (from: Int64, to: Int64), x: @escaping (Int64) -> CGFloat,
                                  width: Double, center: Int64, span: Int64) -> some View {
        ZStack(alignment: .topLeading) {
            if let markIn = controller.selection.markIn {
                RulerHandle(symbol: "[")
                    .offset(x: x(markIn) - 5, y: 0)
                    .gesture(DragGesture(minimumDistance: 1, coordinateSpace: .named("ruler")).onChanged { value in
                        controller.selection.setIn(TimelineRuler.time(atX: Double(value.location.x), width: width, center: center, span: span))
                    })
                    .help("In point: drag to adjust")
            }
            if let markOut = controller.selection.markOut {
                RulerHandle(symbol: "]")
                    .offset(x: x(markOut) - 5, y: 0)
                    .gesture(DragGesture(minimumDistance: 1, coordinateSpace: .named("ruler")).onChanged { value in
                        controller.selection.setOut(TimelineRuler.time(atX: Double(value.location.x), width: width, center: center, span: span))
                    })
                    .help("Out point: drag to adjust")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .coordinateSpace(name: "ruler")
    }
}

private struct RulerHandle: View {
    let symbol: String
    var body: some View {
        Text(symbol)
            .font(.system(size: 15, weight: .bold, design: .monospaced))
            .foregroundStyle(SentinelTheme.accent)
            .frame(width: 10, height: 44, alignment: .top)
            .contentShape(Rectangle())
            .overlay(CursorRegion(cursor: .resizeLeftRight))
    }
}

/// Ticks, the "any recording" lane, the selected camera's band and the export range.
private struct RulerCanvas: View {
    let range: (from: Int64, to: Int64)
    let span: Int64
    let width: Double
    let selectedSpans: [V2.Span]
    let pendingSpans: [V2.Span]
    let otherSpans: [TimelineGaps.Range]
    let selection: ExportSelection

    var body: some View {
        Canvas { ctx, size in
            let w = Double(size.width)
            func x(_ t: Int64) -> CGFloat { CGFloat(Double(t - range.from) / Double(span) * w) }
            func bar(_ start: Int64, _ end: Int64, y: CGFloat, height: CGFloat, color: Color) {
                guard end > range.from, start < range.to else { return }
                let x0 = x(max(start, range.from)), x1 = x(min(end, range.to))
                ctx.fill(Path(roundedRect: CGRect(x: x0, y: y, width: max(x1 - x0, 1), height: height), cornerRadius: 2), with: .color(color))
            }

            // Lane backgrounds.
            ctx.fill(Path(CGRect(x: 0, y: 14, width: size.width, height: 4)), with: .color(SentinelTheme.line.opacity(0.35)))
            ctx.fill(Path(roundedRect: CGRect(x: 0, y: 22, width: size.width, height: 16), cornerRadius: 3), with: .color(SentinelTheme.line.opacity(0.25)))

            if let range = selection.range {
                let x0 = x(range.from), x1 = x(range.to)
                ctx.fill(Path(CGRect(x: x0, y: 0, width: max(x1 - x0, 1), height: size.height)), with: .color(SentinelTheme.accent.opacity(0.2)))
            }

            // Ticks.
            let ticks = TimelineRuler.ticks(from: range.from, to: range.to, targetCount: max(Int(w / 90), 2),
                                            utcOffsetSeconds: TimeZone.current.secondsFromGMT(for: Date(unixMillis: (range.from + range.to) / 2)))
            for tick in ticks {
                let tx = x(tick.time)
                let tall: CGFloat = tick.level == .minor ? 3 : 8
                ctx.fill(Path(CGRect(x: tx, y: size.height - tall, width: 1, height: tall)), with: .color(.secondary.opacity(tick.level == .minor ? 0.4 : 0.8)))
                if tick.level != .minor {
                    ctx.fill(Path(CGRect(x: tx, y: 0, width: 1, height: size.height - tall)), with: .color(SentinelTheme.line.opacity(0.5)))
                    ctx.draw(Text(tick.label).font(.system(size: 9, weight: tick.level == .day ? .bold : .regular).monospacedDigit()).foregroundColor(.secondary),
                             at: CGPoint(x: tx + 3, y: size.height - 16), anchor: .leading)
                }
            }

            // "Any recording" lane: every other tile's camera, merged.
            for r in otherSpans { bar(r.start, r.end, y: 14, height: 4, color: SentinelTheme.recording.opacity(0.5)) }

            // The selected camera's recordings, shaded by Recording Server.
            let recorders = Array(Set(selectedSpans.compactMap(\.recorderId))).sorted()
            for s in selectedSpans {
                let shade = recorders.firstIndex(of: s.recorderId ?? "") ?? 0
                bar(s.start, s.end, y: 22, height: 16, color: SentinelTheme.recording.opacity(shade.isMultiple(of: 2) ? 0.85 : 0.55))
            }
            for s in pendingSpans { bar(s.start, s.end, y: 22, height: 16, color: SentinelTheme.amber.opacity(0.45)) }
        }
    }
}

// MARK: - Mouse and trackpad

/// Drag to pan, scroll or pinch to zoom, with the hand cursor on the ruler.
private struct RulerInteraction: NSViewRepresentable {
    var onBegan: () -> Void
    var onPan: (CGFloat) -> Void
    var onEnded: () -> Void
    var onZoom: (Double) -> Void

    func makeNSView(context: Context) -> RulerNSView {
        let view = RulerNSView()
        update(view)
        return view
    }

    func updateNSView(_ view: RulerNSView, context: Context) { update(view) }

    private func update(_ view: RulerNSView) {
        view.onBegan = onBegan
        view.onPan = onPan
        view.onEnded = onEnded
        view.onZoom = onZoom
    }
}

final class RulerNSView: NSView {
    var onBegan: () -> Void = {}
    var onPan: (CGFloat) -> Void = { _ in }
    var onEnded: () -> Void = {}
    var onZoom: (Double) -> Void = { _ in }
    private var isDragging = false { didSet { window?.invalidateCursorRects(for: self) } }
    private var isScrubbingByScroll = false

    override var acceptsFirstResponder: Bool { false }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func resetCursorRects() {
        addCursorRect(bounds, cursor: isDragging ? .closedHand : .openHand)
    }

    override func mouseDown(with event: NSEvent) {
        isDragging = true
        onBegan()
    }

    override func mouseDragged(with event: NSEvent) {
        onPan(event.deltaX)
    }

    override func mouseUp(with event: NSEvent) {
        isDragging = false
        onEnded()
    }

    override func scrollWheel(with event: NSEvent) {
        let precise = event.hasPreciseScrollingDeltas
        if abs(event.scrollingDeltaX) > abs(event.scrollingDeltaY) {
            // Sideways scrolling pans like a drag.
            if isScrubbingByScroll == false { isScrubbingByScroll = true; onBegan() }
            onPan(event.scrollingDeltaX)
            let finished: Bool
            if precise {
                finished = event.phase == .cancelled || event.momentumPhase == .ended
                    || (event.phase == .ended && event.momentumPhase.isEmpty)
            } else {
                finished = true
            }
            if finished { isScrubbingByScroll = false; onEnded() }
        } else if event.scrollingDeltaY != 0 {
            onZoom(exp(Double(event.scrollingDeltaY) * (precise ? 0.01 : 0.08)))
        }
    }

    override func magnify(with event: NSEvent) {
        onZoom(1 + Double(event.magnification))
    }
}
