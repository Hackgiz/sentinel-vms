@preconcurrency import AVFoundation
import AppKit
import SwiftUI
import UniformTypeIdentifiers
import SentinelCore
import SentinelMediaServer

enum PlaybackEventFilter: String, CaseIterable, Identifiable {
    case all = "All"
    case motion = "Motion"
    case person = "AI Person"

    var id: String { rawValue }

    func includes(_ event: MotionEvent) -> Bool {
        switch self {
        case .all:
            return true
        case .motion:
            return event.kind == .motion
        case .person:
            return event.kind == .person
        }
    }
}

struct PlaybackView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @State private var selectedCameraID: UUID?
    @State private var selectedSegmentID: String?
    @State private var selectedMotionEventID: UUID?
    @State private var selectedDate = Date()
    @State private var speed = "1x"
    @State private var isPlaybackPaused = false
    @State private var seekNudge: PlaybackSeekNudge?
    @State private var eventFilter = PlaybackEventFilter.all
    @State private var scrubberSeekOffset: TimeInterval?
    @State private var playheadOffset: TimeInterval?
    @State private var isScrubbing = false

    private var selectedCamera: CameraFeed? {
        cameraStore.cameras.first { $0.id == selectedCameraID } ?? cameraStore.cameras.first
    }

    private var effectiveSelectedCameraID: UUID? {
        selectedCamera?.id
    }

    private var selectedSegments: [RecordingSegment] {
        selectedCamera.map { camera in
            mediaIngestStore.segments(for: camera.id)
                .filter { Calendar.current.isDate($0.createdAt, inSameDayAs: selectedDate) }
        } ?? []
    }

    private var selectedSegment: RecordingSegment? {
        selectedSegments.first { $0.id == selectedSegmentID } ?? selectedSegments.first
    }

    private var selectedMotionEvent: MotionEvent? {
        selectedSegments
            .flatMap { mediaIngestStore.motionEvents(for: $0) }
            .filter(eventFilter.includes)
            .first { $0.id == selectedMotionEventID }
    }

    private var selectedSegmentMotionEvents: [MotionEvent] {
        selectedSegment.map { mediaIngestStore.motionEvents(for: $0).filter(eventFilter.includes) } ?? []
    }

    private var totalSegmentsLabel: String {
        let count = selectedSegments.count
        return count == 0 ? "No recordings" : "\(count) clip\(count == 1 ? "" : "s")"
    }

    private var selectedMotionSeekOffset: TimeInterval? {
        guard let selectedSegment,
              let selectedMotionEvent,
              selectedMotionEvent.cameraID == selectedSegment.cameraID,
              selectedMotionEvent.timestamp >= segmentStart(selectedSegment),
              selectedMotionEvent.timestamp <= segmentEnd(selectedSegment) else {
            return nil
        }

        return max(0, selectedMotionEvent.timestamp.timeIntervalSince(segmentStart(selectedSegment)))
    }

    private var effectiveSeekOffset: TimeInterval? {
        selectedMotionEventID != nil ? selectedMotionSeekOffset : scrubberSeekOffset
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                // Date navigation
                VStack(spacing: 10) {
                    HStack(spacing: 6) {
                        Button {
                            selectedDate = Calendar.current.date(byAdding: .day, value: -1, to: selectedDate) ?? selectedDate
                        } label: { Image(systemName: "chevron.left") }
                        .buttonStyle(.plain)

                        Spacer()

                        VStack(spacing: 1) {
                            Text(selectedDate, style: .date)
                                .font(.caption.weight(.semibold))
                            Text(totalSegmentsLabel)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }

                        Spacer()

                        Button {
                            selectedDate = Calendar.current.date(byAdding: .day, value: 1, to: selectedDate) ?? selectedDate
                        } label: { Image(systemName: "chevron.right") }
                        .buttonStyle(.plain)
                        .disabled(Calendar.current.isDateInToday(selectedDate))
                    }

                    DatePicker("", selection: $selectedDate, displayedComponents: .date)
                        .datePickerStyle(.compact)
                        .labelsHidden()
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(14)

                Divider().overlay(SentinelTheme.line)

                // Playback controls
                VStack(alignment: .leading, spacing: 8) {
                    Text("PLAYBACK".uppercased())
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)

                    Picker("Speed", selection: $speed) {
                        Text("0.5×").tag("0.5x")
                        Text("1×").tag("1x")
                        Text("2×").tag("2x")
                        Text("4×").tag("4x")
                    }
                    .pickerStyle(.segmented)

                    Picker("Events", selection: $eventFilter) {
                        ForEach(PlaybackEventFilter.allCases) { filter in
                            Text(filter.rawValue).tag(filter)
                        }
                    }
                    .pickerStyle(.segmented)

                    Button {
                        mediaIngestStore.refreshRecordingSegments()
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.bordered)
                }
                .padding(14)

                Divider().overlay(SentinelTheme.line)

                // Camera list with recording counts
                VStack(alignment: .leading, spacing: 6) {
                    Text("CAMERAS".uppercased())
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .padding(.bottom, 2)

                    ForEach(cameraStore.cameras) { camera in
                        let count = mediaIngestStore.segments(for: camera.id)
                            .filter { Calendar.current.isDate($0.createdAt, inSameDayAs: selectedDate) }.count
                        Button {
                            selectedCameraID = camera.id
                        } label: {
                            HStack(spacing: 8) {
                                Circle()
                                    .fill(count > 0 ? SentinelTheme.accent : Color.secondary.opacity(0.4))
                                    .frame(width: 8, height: 8)

                                Text(camera.name)
                                    .font(.caption.weight(.semibold))
                                    .lineLimit(1)
                                    .foregroundStyle(count > 0 ? .primary : .secondary)

                                Spacer()

                                if count > 0 {
                                    Text("\(count)")
                                        .font(.caption2.weight(.bold))
                                        .foregroundStyle(.white)
                                        .padding(.horizontal, 5).padding(.vertical, 2)
                                        .background(SentinelTheme.accent, in: Capsule())
                                } else {
                                    Text("–")
                                        .font(.caption2)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            .padding(.horizontal, 10)
                            .padding(.vertical, 7)
                            .background(
                                effectiveSelectedCameraID == camera.id
                                    ? SentinelTheme.accent.opacity(0.15) : .clear,
                                in: RoundedRectangle(cornerRadius: 7)
                            )
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(14)

                Spacer()
            }
            .frame(width: 240)
            .background(SentinelTheme.chrome)

            Divider()
                .overlay(SentinelTheme.line)

            VStack(spacing: 0) {
                ZStack {
                    if let selectedCamera {
                        ZStack {
                            if let selectedSegment {
                                RecordingPlaybackSurface(
                                    segment: selectedSegment,
                                    speed: speed,
                                    seekOffset: effectiveSeekOffset,
                                    isPaused: isPlaybackPaused,
                                    seekNudge: seekNudge,
                                    isScrubbing: isScrubbing,
                                    onPlaybackOffset: { playheadOffset = $0 }
                                )
                            } else {
                                CameraSignalSurface(camera: selectedCamera)
                            }

                            VStack {
                                HStack {
                                    VStack(alignment: .leading, spacing: 4) {
                                        Text(selectedCamera.name)
                                            .font(.title3.weight(.semibold))

                                        Text(selectedSegment.map { "Playback · \($0.dateLabel) · \($0.timeLabel)" } ?? "No local recordings")
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    }

                                    Spacer()
                                }
                                .padding(14)

                                Spacer()
                            }
                        }
                        .clipShape(RoundedRectangle(cornerRadius: 8))
                        .overlay {
                            RoundedRectangle(cornerRadius: 8)
                                .stroke(SentinelTheme.line, lineWidth: 1)
                        }
                        .padding(14)
                    } else {
                        EmptyStateLine(text: "Add a camera to use playback.")
                            .padding(14)
                    }
                }

                PlaybackControls(
                    speed: speed,
                    selectedSegment: selectedSegment,
                    cameraName: selectedCamera?.name,
                    selectedEvent: selectedMotionEvent,
                    isPaused: $isPlaybackPaused,
                    skipBackward: { seekNudge = PlaybackSeekNudge(seconds: -10) },
                    skipForward: { seekNudge = PlaybackSeekNudge(seconds: 10) },
                    selectPreviousEvent: selectPreviousEvent,
                    selectNextEvent: selectNextEvent
                )

                Divider()
                    .overlay(SentinelTheme.line)

                RecordingTimeline(
                    segments: selectedSegments,
                    aiDetections: effectiveSelectedCameraID.map { aiDetectionStore.detections(for: $0) } ?? [],
                    currentPlaybackOffset: isScrubbing ? scrubberSeekOffset : (playheadOffset ?? effectiveSeekOffset),
                    eventFilter: eventFilter,
                    selectedMotionEventID: selectedMotionEventID,
                    selectedSegmentID: Binding(
                        get: { selectedSegmentID },
                        set: { selectedSegmentID = $0 }
                    ),
                    selectSegment: { segment in
                        selectedSegmentID = segment.id
                        selectedMotionEventID = nil
                        scrubberSeekOffset = nil
                        playheadOffset = nil
                    },
                    selectMotionEvent: { segment, event in
                        selectedSegmentID = segment.id
                        selectedMotionEventID = event.id
                        scrubberSeekOffset = nil
                        playheadOffset = nil
                        isPlaybackPaused = false
                    },
                    selectSegmentWithOffset: { segment, offset, scrubbing in
                        selectedSegmentID = segment.id
                        selectedMotionEventID = nil
                        scrubberSeekOffset = offset
                        isScrubbing = scrubbing
                        if scrubbing == false { isPlaybackPaused = false }
                    }
                )
            }

            Divider()
                .overlay(SentinelTheme.line)

            PlaybackEventInspector(
                segment: selectedSegment,
                motionEvents: selectedSegmentMotionEvents,
                selectedEventID: selectedMotionEventID,
                selectEvent: { event in
                    selectedMotionEventID = event.id
                }
            )
            .frame(width: 286)
        }
        .background(SentinelTheme.background)
        .onChange(of: selectedCamera?.id) { _ in
            selectedSegmentID = nil
            selectedMotionEventID = nil
            scrubberSeekOffset = nil
            playheadOffset = nil
            isScrubbing = false
            isPlaybackPaused = false
        }
        .onChange(of: selectedDate) { _ in
            selectedSegmentID = nil
            selectedMotionEventID = nil
            scrubberSeekOffset = nil
            playheadOffset = nil
            isScrubbing = false
            isPlaybackPaused = false
        }
        .onChange(of: eventFilter) { _ in
            selectedMotionEventID = nil
            scrubberSeekOffset = nil
            playheadOffset = nil
            isScrubbing = false
            isPlaybackPaused = false
        }
        .onAppear {
            selectedCameraID = effectiveSelectedCameraID
            mediaIngestStore.refreshRecordingSegments()
        }
    }

    private func segmentStart(_ segment: RecordingSegment) -> Date {
        min(segment.createdAt, segment.modifiedAt)
    }

    private func segmentEnd(_ segment: RecordingSegment) -> Date {
        let start = segmentStart(segment)
        let end = max(segment.createdAt, segment.modifiedAt)
        return end.timeIntervalSince(start) < 1 ? start.addingTimeInterval(60) : end
    }

    private func selectPreviousEvent() {
        selectAdjacentEvent(direction: -1)
    }

    private func selectNextEvent() {
        selectAdjacentEvent(direction: 1)
    }

    private func selectAdjacentEvent(direction: Int) {
        guard selectedSegment != nil else {
            return
        }

        let events = selectedSegmentMotionEvents.sorted { $0.timestamp < $1.timestamp }
        guard events.isEmpty == false else {
            return
        }

        let currentIndex = selectedMotionEventID.flatMap { id in
            events.firstIndex { $0.id == id }
        }
        let fallbackIndex = direction > 0 ? -1 : events.count
        let nextIndex = min(max((currentIndex ?? fallbackIndex) + direction, 0), events.count - 1)
        selectedMotionEventID = events[nextIndex].id
        isPlaybackPaused = false
    }
}

struct PlaybackSeekNudge: Equatable {
    let id = UUID()
    let seconds: Double
}

struct RecordingPlaybackSurface: View {
    let segment: RecordingSegment
    let speed: String
    let seekOffset: TimeInterval?
    let isPaused: Bool
    let seekNudge: PlaybackSeekNudge?
    /// While true, scrub for a fast preview frame (tolerant seek, held paused);
    /// on false, commit a frame-accurate seek and resume per `isPaused`.
    var isScrubbing: Bool = false
    /// Reports the player's current offset (seconds within this segment) ~4×/sec
    /// so callers can advance a timeline playhead that tracks actual playback.
    var onPlaybackOffset: ((TimeInterval) -> Void)? = nil
    @State private var player: AVPlayer?
    @State private var timeObserver: Any?
    @State private var isSeekInFlight = false

    var body: some View {
        ZStack {
            if let player {
                AVPlayerLayerSurface(player: player)
                    .onAppear {
                        play(player)
                    }
                    .onDisappear {
                        player.pause()
                    }
            } else {
                Color.black
            }
        }
        .background(.black)
        .task(id: segment.id) {
            teardownObserver()
            player?.pause()
            let player = AVPlayer(url: segment.fileURL)
            player.automaticallyWaitsToMinimizeStalling = false
            self.player = player
            installObserver(on: player)
            applySeek(to: player, precise: true)
            play(player)
        }
        .onDisappear { teardownObserver() }
        .onChange(of: speed) { _ in
            if let player {
                play(player)
            }
        }
        .onChange(of: seekOffset) { _ in
            if let player {
                applySeek(to: player, precise: isScrubbing == false)
                if isScrubbing == false { play(player) }
            }
        }
        .onChange(of: isScrubbing) { scrubbing in
            guard let player else { return }
            if scrubbing {
                player.pause()                       // hold the preview frame
            } else {
                applySeek(to: player, precise: true) // commit exact frame
                play(player)                         // resume (respects isPaused)
            }
        }
        .onChange(of: isPaused) { _ in
            guard let player else {
                return
            }

            isPaused ? player.pause() : play(player)
        }
        .onChange(of: seekNudge?.id) { _ in
            guard let player,
                  let seekNudge else {
                return
            }

            let currentSeconds = CMTimeGetSeconds(player.currentTime())
            let targetSeconds = max(currentSeconds + seekNudge.seconds, 0)
            let time = CMTime(seconds: targetSeconds, preferredTimescale: 600)
            player.seek(to: time, toleranceBefore: .zero, toleranceAfter: .zero)
            play(player)
        }
    }

    private func installObserver(on player: AVPlayer) {
        guard onPlaybackOffset != nil else { return }
        let interval = CMTime(seconds: 0.25, preferredTimescale: 600)
        timeObserver = player.addPeriodicTimeObserver(forInterval: interval, queue: .main) { time in
            onPlaybackOffset?(CMTimeGetSeconds(time))
        }
    }

    private func teardownObserver() {
        if let timeObserver, let player {
            player.removeTimeObserver(timeObserver)
        }
        timeObserver = nil
    }

    private func play(_ player: AVPlayer) {
        guard isPaused == false else {
            player.pause()
            return
        }

        player.playImmediately(atRate: playbackRate)
    }

    /// Precise = frame-accurate (zero tolerance); otherwise a tolerant seek for
    /// fast scrub preview, coalesced so drags don't queue a backlog of seeks.
    private func applySeek(to player: AVPlayer, precise: Bool) {
        guard let seekOffset else {
            return
        }

        let time = CMTime(seconds: seekOffset, preferredTimescale: 600)
        if precise {
            player.seek(to: time, toleranceBefore: .zero, toleranceAfter: .zero)
        } else {
            guard isSeekInFlight == false else { return }
            isSeekInFlight = true
            let tol = CMTime(seconds: 0.5, preferredTimescale: 600)
            player.seek(to: time, toleranceBefore: tol, toleranceAfter: tol) { _ in
                isSeekInFlight = false
            }
        }
    }

    private var playbackRate: Float {
        let cleaned = speed.replacingOccurrences(of: "x", with: "")
        return Float(cleaned) ?? 1
    }
}

struct AVPlayerLayerSurface: NSViewRepresentable {
    let player: AVPlayer

    func makeNSView(context: Context) -> PlayerLayerView {
        let view = PlayerLayerView()
        view.playerLayer.player = player
        return view
    }

    func updateNSView(_ nsView: PlayerLayerView, context: Context) {
        nsView.playerLayer.player = player
    }
}

final class PlayerLayerView: NSView {
    let playerLayer = AVPlayerLayer()

    override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        wantsLayer = true
        layer = playerLayer
        playerLayer.videoGravity = .resizeAspect
        playerLayer.backgroundColor = NSColor.black.cgColor
    }

    required init?(coder: NSCoder) {
        nil
    }
}

struct PlaybackEventInspector: View {
    let segment: RecordingSegment?
    let motionEvents: [MotionEvent]
    let selectedEventID: UUID?
    let selectEvent: (MotionEvent) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            SentinelPanel("Selected Clip", systemImage: "film.stack.fill") {
                VStack(alignment: .leading, spacing: 10) {
                    if let segment {
                        DetailRow(label: "Date", value: segment.dateLabel)
                        DetailRow(label: "Start", value: segment.timeLabel)
                        DetailRow(label: "Size", value: segment.sizeLabel)
                        DetailRow(label: "Events", value: "\(motionEvents.count)")
                    } else {
                        EmptyStateLine(text: "Select a recording to review events.")
                    }
                }
            }

            SentinelPanel("Motion & AI Events", systemImage: "figure.walk.motion") {
                VStack(spacing: 8) {
                    if motionEvents.isEmpty {
                        EmptyStateLine(text: "No motion or AI person markers match this clip/filter.")
                    } else {
                        ForEach(motionEvents.sorted { $0.timestamp < $1.timestamp }) { event in
                            Button {
                                selectEvent(event)
                            } label: {
                                HStack(spacing: 10) {
                                    Circle()
                                        .fill(event.kind == .person ? .red : SentinelTheme.amber)
                                        .frame(width: 8, height: 8)

                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(event.kind == .person ? "AI Person" : event.kind.label)
                                            .font(.caption.weight(.semibold))

                                        Text(RecordingFormatters.timeFormatter.string(from: event.timestamp))
                                            .font(.caption2.monospacedDigit())
                                            .foregroundStyle(.secondary)
                                    }

                                    Spacer()

                                    Image(systemName: "play.fill")
                                        .font(.caption2.weight(.bold))
                                        .foregroundStyle(selectedEventID == event.id ? SentinelTheme.accent : .secondary)
                                }
                                .padding(10)
                                .background(
                                    selectedEventID == event.id ? SentinelTheme.accent.opacity(0.14) : SentinelTheme.panelRaised,
                                    in: RoundedRectangle(cornerRadius: 8)
                                )
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }

            SentinelPanel("Export", systemImage: "square.and.arrow.up") {
                VStack(alignment: .leading, spacing: 8) {
                    EmptyStateLine(text: "Use Export MP4 below playback to save the selected recording.")
                    EmptyStateLine(text: "Bookmarks and evidence packages stay in the Evidence locker.")
                }
            }

            Spacer()
        }
        .padding(14)
        .background(SentinelTheme.chrome)
    }
}

struct _RemovedPlaybackScrubberTrack_Unused: View {
    let segments: [RecordingSegment]
    let allEvents: [MotionEvent]
    let aiDetections: [AIDetectionEvent]
    let eventFilter: PlaybackEventFilter
    let selectedSegmentID: String?
    let currentPlaybackOffset: TimeInterval?
    let selectSegmentWithOffset: (RecordingSegment, TimeInterval) -> Void

    private var sortedSegments: [RecordingSegment] {
        segments.sorted { min($0.createdAt, $0.modifiedAt) < min($1.createdAt, $1.modifiedAt) }
    }

    private var timelineStart: Date? {
        sortedSegments.map { min($0.createdAt, $0.modifiedAt) }.min()
    }

    private var timelineEnd: Date? {
        sortedSegments.map { max($0.createdAt, $0.modifiedAt) }.max()
    }

    private var totalDuration: TimeInterval {
        guard let start = timelineStart, let end = timelineEnd else { return 3600 }
        return max(end.timeIntervalSince(start), 60)
    }

    private var filteredEvents: [MotionEvent] {
        allEvents.filter(eventFilter.includes).sorted { $0.timestamp < $1.timestamp }
    }

    var body: some View {
        GeometryReader { proxy in
            let trackWidth = proxy.size.width
            ZStack(alignment: .topLeading) {
                RoundedRectangle(cornerRadius: 3)
                    .fill(SentinelTheme.panelRaised)
                    .overlay {
                        RoundedRectangle(cornerRadius: 3)
                            .stroke(SentinelTheme.line, lineWidth: 1)
                    }
                    .frame(height: 36)

                ForEach(sortedSegments) { segment in
                    segmentBar(segment, trackWidth: trackWidth)
                }

                ForEach(filteredEvents.indices, id: \.self) { i in
                    eventTick(filteredEvents[i], trackWidth: trackWidth)
                }

                ForEach(aiDetections.indices, id: \.self) { i in
                    aiDetectionTick(aiDetections[i], trackWidth: trackWidth)
                }

                if let offset = currentPlaybackOffset,
                   let start = timelineStart {
                    let playheadX = CGFloat(offset / totalDuration) * trackWidth
                    Rectangle()
                        .fill(Color.white)
                        .frame(width: 2, height: 36)
                        .shadow(color: .black.opacity(0.5), radius: 2)
                        .offset(x: max(0, playheadX - 1))
                        .allowsHitTesting(false)
                    let _ = start
                }

                Rectangle()
                    .fill(Color.clear)
                    .contentShape(Rectangle())
                    .frame(height: 36)
                    .gesture(
                        DragGesture(minimumDistance: 0)
                            .onChanged { value in
                                seek(to: value.location.x, in: trackWidth)
                            }
                    )
            }
            .frame(height: 36)
        }
        .frame(height: 36)
    }

    @ViewBuilder
    private func segmentBar(_ segment: RecordingSegment, trackWidth: CGFloat) -> some View {
        if let start = timelineStart {
            let segStart = min(segment.createdAt, segment.modifiedAt)
            let segEnd = max(segment.createdAt, segment.modifiedAt)
            let x = CGFloat(segStart.timeIntervalSince(start) / totalDuration) * trackWidth
            let w = max(CGFloat(segEnd.timeIntervalSince(segStart) / totalDuration) * trackWidth, 4)
            let isSelected = segment.id == selectedSegmentID

            RoundedRectangle(cornerRadius: 2)
                .fill(isSelected ? SentinelTheme.amber : SentinelTheme.accent.opacity(0.8))
                .frame(width: w, height: 28)
                .offset(x: x, y: 4)
        }
    }

    @ViewBuilder
    private func eventTick(_ event: MotionEvent, trackWidth: CGFloat) -> some View {
        if let start = timelineStart {
            let x = CGFloat(event.timestamp.timeIntervalSince(start) / totalDuration) * trackWidth
            let isPerson = event.kind == .person
            Rectangle()
                .fill(isPerson ? Color.red : SentinelTheme.motion)
                .frame(width: 2, height: isPerson ? 22 : 14)
                .offset(x: max(0, x - 1), y: isPerson ? 4 : 12)
        }
    }

    @ViewBuilder
    private func aiDetectionTick(_ event: AIDetectionEvent, trackWidth: CGFloat) -> some View {
        if let start = timelineStart {
            let x = CGFloat(event.timestamp.timeIntervalSince(start) / totalDuration) * trackWidth
            let c = event.kind.overlayColor
            Rectangle()
                .fill(Color(red: c.0, green: c.1, blue: c.2))
                .frame(width: 2, height: 16)
                .offset(x: max(0, x - 1), y: 10)
        }
    }

    private func seek(to x: CGFloat, in trackWidth: CGFloat) {
        guard let start = timelineStart else { return }
        let progress = min(max(x / trackWidth, 0), 1)
        let targetTime = start.addingTimeInterval(progress * totalDuration)

        if let segment = sortedSegments.first(where: { segment in
            let segStart = min(segment.createdAt, segment.modifiedAt)
            let segEnd = max(segment.createdAt, segment.modifiedAt)
            return targetTime >= segStart && targetTime <= segEnd
        }) {
            let offset = max(0, targetTime.timeIntervalSince(min(segment.createdAt, segment.modifiedAt)))
            selectSegmentWithOffset(segment, offset)
        }
    }
}

struct RecordingTimeline: View {
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    let segments: [RecordingSegment]
    let aiDetections: [AIDetectionEvent]
    let currentPlaybackOffset: TimeInterval?
    let eventFilter: PlaybackEventFilter
    let selectedMotionEventID: UUID?
    @Binding var selectedSegmentID: String?
    let selectSegment: (RecordingSegment) -> Void
    let selectMotionEvent: (RecordingSegment, MotionEvent) -> Void
    let selectSegmentWithOffset: (RecordingSegment, TimeInterval, Bool) -> Void

    @State private var zoomSeconds: Double = 86400

    private var sortedSegments: [RecordingSegment] {
        segments.sorted { min($0.createdAt, $0.modifiedAt) < min($1.createdAt, $1.modifiedAt) }
    }

    private var allEvents: [MotionEvent] {
        guard let id = segments.first?.cameraID else { return [] }
        return mediaIngestStore.motionEvents(for: id).filter(eventFilter.includes)
    }

    private var dayStart: Date {
        let anchor = sortedSegments.first.map { min($0.createdAt, $0.modifiedAt) } ?? Date()
        return Calendar.current.startOfDay(for: anchor)
    }

    private var windowStart: Date {
        guard zoomSeconds < 86400 else { return dayStart }
        let center = selectedSegmentCenter ?? dayStart.addingTimeInterval(43200)
        return center.addingTimeInterval(-zoomSeconds / 2)
    }

    private var windowEnd: Date { windowStart.addingTimeInterval(max(zoomSeconds, 600)) }

    private var selectedSegmentCenter: Date? {
        guard let id = selectedSegmentID,
              let seg = sortedSegments.first(where: { $0.id == id }) else { return nil }
        let s = min(seg.createdAt, seg.modifiedAt)
        let e = max(seg.createdAt, seg.modifiedAt)
        return s.addingTimeInterval(e.timeIntervalSince(s) / 2)
    }

    private var playheadDate: Date? {
        guard let offset = currentPlaybackOffset,
              let id = selectedSegmentID,
              let seg = sortedSegments.first(where: { $0.id == id }) else { return nil }
        return min(seg.createdAt, seg.modifiedAt).addingTimeInterval(offset)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 10) {
                Label("Timeline", systemImage: "timeline.selection")
                    .font(.caption.weight(.semibold))

                if let first = sortedSegments.first {
                    Text(first.dateLabel)
                        .font(.caption2).foregroundStyle(.secondary)
                }

                let evtCount = allEvents.count
                if evtCount > 0 {
                    Text("· \(evtCount) event\(evtCount == 1 ? "" : "s")")
                        .font(.caption2).foregroundStyle(.secondary)
                }

                Spacer()

                HStack(spacing: 8) {
                    TimelineLegendSwatch(label: "Recording", tint: SentinelTheme.accent)
                    TimelineLegendSwatch(label: "Motion", tint: SentinelTheme.motion)
                    TimelineLegendSwatch(label: "Person", tint: .red)
                }

                HStack(spacing: 2) {
                    ForEach([("10m", 600.0), ("1h", 3600.0), ("4h", 14400.0), ("24h", 86400.0)], id: \.0) { label, secs in
                        Button { zoomSeconds = secs } label: {
                            Text(label)
                                .font(.caption2.weight(.semibold))
                                .foregroundStyle(zoomSeconds == secs ? .white : .primary)
                                .padding(.horizontal, 7).padding(.vertical, 3)
                                .background(
                                    zoomSeconds == secs ? SentinelTheme.accent : Color.secondary.opacity(0.12),
                                    in: RoundedRectangle(cornerRadius: 4)
                                )
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .padding(.horizontal, 18)
            .padding(.top, 10)
            .padding(.bottom, 6)

            if segments.isEmpty {
                EmptyStateLine(text: "No recordings for this camera on this day. Start recording from the Live inspector.")
                    .padding(.horizontal, 18).padding(.bottom, 10)
            } else {
                GeometryReader { proxy in
                    let w = max(proxy.size.width - 36, 100)
                    ContinuousRecordingTrack(
                        segments: sortedSegments,
                        events: allEvents,
                        windowStart: windowStart,
                        windowEnd: windowEnd,
                        selectedSegmentID: selectedSegmentID ?? sortedSegments.first?.id,
                        playheadDate: playheadDate,
                        width: w,
                        selectSegmentWithOffset: selectSegmentWithOffset
                    )
                    .padding(.horizontal, 18)
                }
                .frame(height: 80)
                .padding(.bottom, 8)
            }
        }
        .background(.thinMaterial)
    }
}

struct ContinuousRecordingTrack: View {
    let segments: [RecordingSegment]
    let events: [MotionEvent]
    let windowStart: Date
    let windowEnd: Date
    let selectedSegmentID: String?
    let playheadDate: Date?
    let width: CGFloat
    /// Third arg = isScrubbing: true during the drag (preview), false on release.
    let selectSegmentWithOffset: (RecordingSegment, TimeInterval, Bool) -> Void

    private var windowDuration: TimeInterval { windowEnd.timeIntervalSince(windowStart) }

    var body: some View {
        VStack(spacing: 3) {
            ZStack(alignment: .topLeading) {
                RoundedRectangle(cornerRadius: 4)
                    .fill(Color.black.opacity(0.2))
                    .frame(height: 44)

                ForEach(segments) { seg in segmentBlock(seg) }
                ForEach(events)   { ev  in eventMarker(ev) }

                if let ph = playheadDate {
                    let x = xPos(ph)
                    if (0...width).contains(x) {
                        Rectangle()
                            .fill(Color.white.opacity(0.9))
                            .frame(width: 2, height: 44)
                            .offset(x: x - 1)
                            .shadow(color: .black.opacity(0.4), radius: 2)
                            .allowsHitTesting(false)

                        Rectangle()
                            .fill(Color.white)
                            .frame(width: 8, height: 8)
                            .rotationEffect(.degrees(45))
                            .offset(x: x - 4, y: -4)
                            .allowsHitTesting(false)
                    }
                }

                Rectangle()
                    .fill(Color.clear).contentShape(Rectangle())
                    .frame(height: 44)
                    .gesture(
                        DragGesture(minimumDistance: 0)
                            .onChanged { seek(to: $0.location.x, isScrubbing: true) }
                            .onEnded { seek(to: $0.location.x, isScrubbing: false) }
                    )
            }
            .frame(width: width, height: 44)

            PlaybackTimeAxis(windowStart: windowStart, windowEnd: windowEnd, width: width)
                .frame(width: width, height: 24)
        }
    }

    @ViewBuilder
    private func segmentBlock(_ seg: RecordingSegment) -> some View {
        let s = min(seg.createdAt, seg.modifiedAt)
        let e = max(seg.createdAt, seg.modifiedAt)
        let isSelected = seg.id == selectedSegmentID
        if e > windowStart && s < windowEnd {
            let cs = max(s, windowStart); let ce = min(e, windowEnd)
            let x = xPos(cs); let w = max(xPos(ce) - x, 3)
            RoundedRectangle(cornerRadius: 3)
                .fill(isSelected ? SentinelTheme.amber : SentinelTheme.accent.opacity(0.82))
                .overlay {
                    if isSelected {
                        RoundedRectangle(cornerRadius: 3).stroke(Color.white.opacity(0.4), lineWidth: 1)
                    }
                }
                .frame(width: w, height: 34)
                .offset(x: x, y: 5)
                .help("\(seg.timeLabel) · \(seg.sizeLabel)")
        }
    }

    @ViewBuilder
    private func eventMarker(_ ev: MotionEvent) -> some View {
        if ev.timestamp >= windowStart && ev.timestamp < windowEnd {
            let x = xPos(ev.timestamp)
            let isPerson = ev.kind == .person
            Rectangle()
                .fill(isPerson ? Color.red : SentinelTheme.motion)
                .frame(width: isPerson ? 3 : 2, height: isPerson ? 28 : 18)
                .offset(x: max(0, x - 1), y: isPerson ? 2 : 10)
                .allowsHitTesting(false)
        }
    }

    private func xPos(_ date: Date) -> CGFloat {
        CGFloat(date.timeIntervalSince(windowStart) / windowDuration) * width
    }

    private func seek(to x: CGFloat, isScrubbing: Bool) {
        let progress = min(max(x / width, 0), 1)
        let target = windowStart.addingTimeInterval(progress * windowDuration)
        if let seg = segments.first(where: {
            let s = min($0.createdAt, $0.modifiedAt)
            let e = max($0.createdAt, $0.modifiedAt)
            return target >= s && target <= e
        }) {
            let offset = max(0, target.timeIntervalSince(min(seg.createdAt, seg.modifiedAt)))
            selectSegmentWithOffset(seg, offset, isScrubbing)
        }
    }
}

struct PlaybackTimeAxis: View {
    let windowStart: Date
    let windowEnd: Date
    let width: CGFloat

    private var labelInterval: TimeInterval {
        let d = windowEnd.timeIntervalSince(windowStart)
        if d <= 900   { return 60 }
        if d <= 3600  { return 600 }
        if d <= 14400 { return 3600 }
        return 14400
    }

    private var labelDates: [Date] {
        var result: [Date] = []
        var t = Date(timeIntervalSince1970: ceil(windowStart.timeIntervalSince1970 / labelInterval) * labelInterval)
        while t <= windowEnd { result.append(t); t = t.addingTimeInterval(labelInterval) }
        return result
    }

    var body: some View {
        ZStack(alignment: .topLeading) {
            ForEach(labelDates, id: \.timeIntervalSince1970) { date in
                let progress = date.timeIntervalSince(windowStart) / windowEnd.timeIntervalSince(windowStart)
                let x = CGFloat(progress) * width
                Rectangle()
                    .fill(Color.secondary.opacity(0.3))
                    .frame(width: 1, height: 5)
                    .offset(x: x)
                Text(timeLabel(date))
                    .font(.system(size: 9, weight: .regular, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .frame(width: 44, alignment: .center)
                    .offset(x: min(max(x - 22, 0), width - 44), y: 7)
            }
        }
    }

    private func timeLabel(_ date: Date) -> String {
        let f = DateFormatter(); f.dateFormat = "HH:mm"; return f.string(from: date)
    }
}

struct PlaybackTimelineSegment: View {
    let segment: RecordingSegment
    let events: [MotionEvent]
    let isSelected: Bool
    let selectedEventID: UUID?
    let width: CGFloat
    let selectSegment: () -> Void
    let selectEvent: (MotionEvent) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 8) {
                Text(segment.timeLabel)
                    .font(.caption2.monospacedDigit().weight(.semibold))

                Spacer()

                Text(segment.sizeLabel)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 4)
                    .fill(isSelected ? SentinelTheme.accent.opacity(0.24) : SentinelTheme.panelRaised)
                    .overlay(alignment: .topLeading) {
                        RoundedRectangle(cornerRadius: 3)
                            .fill(isSelected ? SentinelTheme.amber : SentinelTheme.accent)
                            .frame(width: width, height: 8)
                    }
                    .overlay {
                        RoundedRectangle(cornerRadius: 4)
                            .stroke(isSelected ? SentinelTheme.accent.opacity(0.75) : SentinelTheme.line, lineWidth: isSelected ? 1.5 : 1)
                    }
                    .contentShape(Rectangle())
                    .onTapGesture(perform: selectSegment)

                PlaybackDetectionMarkerTrack(
                    events: events,
                    start: segmentStart(segment),
                    end: segmentEnd(segment),
                    width: width,
                    height: 42,
                    selectedEventID: selectedEventID,
                    onSelect: selectEvent
                )
                .padding(.top, 8)
            }
            .frame(width: width, height: 50)

            PlaybackTimelineRuler(width: width)
        }
        .padding(8)
        .background(isSelected ? SentinelTheme.accent.opacity(0.12) : Color.clear, in: RoundedRectangle(cornerRadius: 8))
        .help(segment.fileURL.path)
    }

    private func segmentStart(_ segment: RecordingSegment) -> Date {
        min(segment.createdAt, segment.modifiedAt)
    }

    private func segmentEnd(_ segment: RecordingSegment) -> Date {
        let start = segmentStart(segment)
        let end = max(segment.createdAt, segment.modifiedAt)
        return end.timeIntervalSince(start) < 1 ? start.addingTimeInterval(60) : end
    }
}

struct PlaybackDetectionMarkerTrack: View {
    let events: [MotionEvent]
    let start: Date
    let end: Date
    let width: CGFloat
    let height: CGFloat
    let selectedEventID: UUID?
    let onSelect: (MotionEvent) -> Void

    var body: some View {
        ZStack(alignment: .leading) {
            ForEach(events.sorted { $0.timestamp < $1.timestamp }) { event in
                Button {
                    onSelect(event)
                } label: {
                    RoundedRectangle(cornerRadius: 2)
                        .fill(eventTint(event))
                        .frame(width: selectedEventID == event.id ? 7 : markerWidth(for: event), height: markerHeight(for: event))
                        .overlay {
                            if selectedEventID == event.id {
                                RoundedRectangle(cornerRadius: 2)
                                    .stroke(.white.opacity(0.86), lineWidth: 1)
                            }
                        }
                }
                .buttonStyle(.plain)
                .offset(x: markerOffset(for: event), y: markerYOffset(for: event))
                .help("\(event.kind.label) · \(RecordingFormatters.timeFormatter.string(from: event.timestamp))")
                .accessibilityLabel("\(event.kind.label) marker")
            }
        }
        .frame(width: width, height: height, alignment: .leading)
    }

    private func markerOffset(for event: MotionEvent) -> CGFloat {
        let duration = max(end.timeIntervalSince(start), 1)
        let progress = min(max(event.timestamp.timeIntervalSince(start) / duration, 0), 1)
        return CGFloat(progress) * max(width - markerWidth(for: event), 0)
    }

    private func markerYOffset(for event: MotionEvent) -> CGFloat {
        event.kind == .person ? 7 : 16
    }

    private func markerWidth(for event: MotionEvent) -> CGFloat {
        event.kind == .person ? 5 : 3
    }

    private func markerHeight(for event: MotionEvent) -> CGFloat {
        event.kind == .person ? 30 : CGFloat(12 + min(max(event.intensity * 38, 2), 14))
    }

    private func eventTint(_ event: MotionEvent) -> Color {
        event.kind == .person ? .red : SentinelTheme.motion
    }
}

struct PlaybackTimelineRuler: View {
    let width: CGFloat

    var body: some View {
        HStack(spacing: 0) {
            ForEach(0..<tickCount, id: \.self) { index in
                Rectangle()
                    .fill(SentinelTheme.line)
                    .frame(width: 1, height: index.isMultiple(of: 2) ? 8 : 4)

                if index < tickCount - 1 {
                    Spacer(minLength: 0)
                }
            }
        }
        .frame(width: width, height: 10, alignment: .bottom)
    }

    private var tickCount: Int {
        max(Int(width / 48), 3)
    }
}

struct TimelineLegendSwatch: View {
    let label: String
    let tint: Color

    var body: some View {
        HStack(spacing: 4) {
            RoundedRectangle(cornerRadius: 1.5)
                .fill(tint)
                .frame(width: 10, height: 4)

            Text(label)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
    }
}

struct PlaybackControls: View {
    @EnvironmentObject private var caseworkStore: CaseworkStore
    let speed: String
    let selectedSegment: RecordingSegment?
    let cameraName: String?
    let selectedEvent: MotionEvent?
    @Binding var isPaused: Bool
    let skipBackward: () -> Void
    let skipForward: () -> Void
    let selectPreviousEvent: () -> Void
    let selectNextEvent: () -> Void
    @State private var actionMessage: String?
    @State private var exportStartOffset = 0.0
    @State private var exportEndOffset = 60.0
    @State private var isExporting = false

    var body: some View {
        let duration = selectedSegment.map(segmentDuration) ?? 60

        VStack(spacing: 8) {
            HStack(spacing: 10) {
                Text(selectedSegment == nil ? "No recording selected" : "Reviewing selected recording")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)

                if let selectedEvent {
                    Text("\(selectedEvent.kind == .person ? "AI Person" : selectedEvent.kind.label) \(RecordingFormatters.timeFormatter.string(from: selectedEvent.timestamp))")
                        .font(.caption2.monospacedDigit().weight(.semibold))
                        .foregroundStyle(selectedEvent.kind == .person ? .red : SentinelTheme.motion)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 4)
                        .background((selectedEvent.kind == .person ? Color.red : SentinelTheme.motion).opacity(0.14), in: Capsule())
                }

                Spacer()

                Button(action: skipBackward) {
                    Label("Back 10", systemImage: "gobackward.10")
                }
                .help("Skip back 10 seconds")
                .disabled(selectedSegment == nil)

                Button {
                    isPaused.toggle()
                } label: {
                    Label(isPaused ? "Play" : "Pause", systemImage: isPaused ? "play.fill" : "pause.fill")
                }
                .keyboardShortcut("k", modifiers: [])
                .help("Play or pause")
                .disabled(selectedSegment == nil)

                Button(action: skipForward) {
                    Label("Forward 10", systemImage: "goforward.10")
                }
                .help("Skip forward 10 seconds")
                .disabled(selectedSegment == nil)

                Button(action: selectPreviousEvent) {
                    Label("Previous Event", systemImage: "backward.end.fill")
                }
                .help("Jump to previous motion or person marker")
                .disabled(selectedSegment == nil)

                Button(action: selectNextEvent) {
                    Label("Next Event", systemImage: "forward.end.fill")
                }
                .help("Jump to next motion or person marker")
                .disabled(selectedSegment == nil)

                Button {
                    caseworkStore.bookmarkPlayback(segment: selectedSegment, cameraName: cameraName)
                    actionMessage = "Bookmark saved"
                } label: {
                    Label("Bookmark", systemImage: "bookmark.fill")
                }
                .disabled(selectedSegment == nil)

                Button {
                    caseworkStore.createEvidencePackage(from: selectedSegment, cameraName: cameraName)
                    actionMessage = "Evidence package created"
                } label: {
                    Label("Create Evidence", systemImage: "shippingbox.fill")
                }
                .disabled(selectedSegment == nil)
            }

            HStack(spacing: 12) {
                Text("Export Range")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)

                Text(timecode(exportStartOffset))
                    .font(.caption2.monospacedDigit())
                    .frame(width: 52, alignment: .leading)

                RangeSlider(
                    lowerValue: $exportStartOffset,
                    upperValue: $exportEndOffset,
                    bounds: 0...max(duration, 1)
                )
                .frame(height: 22)
                .disabled(selectedSegment == nil)

                Text(timecode(exportEndOffset))
                    .font(.caption2.monospacedDigit())
                    .frame(width: 52, alignment: .trailing)

                Button {
                    exportSelectedRange()
                } label: {
                    Label(isExporting ? "Exporting" : "Export Clip", systemImage: "square.and.arrow.up")
                }
                .buttonStyle(.borderedProminent)
                .disabled(selectedSegment == nil || isExporting || exportEndOffset - exportStartOffset < 1)

                if let actionMessage {
                    Text(actionMessage)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: 260, alignment: .leading)
                }
            }
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 10)
        .background(SentinelTheme.chrome)
        .onAppear {
            resetExportRange()
        }
        .onChange(of: selectedSegment?.id) { _ in
            resetExportRange()
            isPaused = false
            actionMessage = nil
        }
    }

    private func exportSelectedRange() {
        guard let selectedSegment else {
            return
        }

        let panel = NSSavePanel()
        panel.canCreateDirectories = true
        panel.nameFieldStringValue = exportFileName(for: selectedSegment)
        panel.allowedContentTypes = [.mpeg4Movie]

        guard panel.runModal() == .OK,
              let destinationURL = panel.url else {
            return
        }

        isExporting = true
        actionMessage = "Exporting selected range..."

        Task {
            do {
                try await exportSegment(selectedSegment, to: destinationURL)
                actionMessage = "Exported \(destinationURL.lastPathComponent)"
            } catch {
                actionMessage = "Export failed: \(error.localizedDescription)"
            }
            isExporting = false
        }
    }

    private func resetExportRange() {
        exportStartOffset = 0
        exportEndOffset = selectedSegment.map(segmentDuration) ?? 60
    }

    private func segmentDuration(_ segment: RecordingSegment) -> Double {
        let fileDuration = segment.modifiedAt.timeIntervalSince(segment.createdAt)
        return fileDuration > 1 ? fileDuration : 60
    }

    private func exportSegment(_ segment: RecordingSegment, to destinationURL: URL) async throws {
        if FileManager.default.fileExists(atPath: destinationURL.path) {
            try FileManager.default.removeItem(at: destinationURL)
        }

        let asset = AVURLAsset(url: segment.fileURL)
        guard let exporter = AVAssetExportSession(asset: asset, presetName: AVAssetExportPresetHighestQuality) else {
            throw PlaybackExportError.exporterUnavailable
        }

        exporter.outputURL = destinationURL
        exporter.outputFileType = .mp4
        exporter.timeRange = CMTimeRange(
            start: CMTime(seconds: exportStartOffset, preferredTimescale: 600),
            duration: CMTime(seconds: max(exportEndOffset - exportStartOffset, 1), preferredTimescale: 600)
        )
        exporter.shouldOptimizeForNetworkUse = true
        let box = PlaybackExportSessionBox(exporter)

        try await withCheckedThrowingContinuation { continuation in
            box.exporter.exportAsynchronously {
                switch box.exporter.status {
                case .completed:
                    continuation.resume()
                case .failed, .cancelled:
                    continuation.resume(throwing: box.exporter.error ?? PlaybackExportError.exportFailed)
                default:
                    continuation.resume(throwing: PlaybackExportError.exportFailed)
                }
            }
        }
    }

    private func exportFileName(for segment: RecordingSegment) -> String {
        let baseName = segment.fileURL.deletingPathExtension().lastPathComponent
        return "\(baseName)-\(timecode(exportStartOffset))-\(timecode(exportEndOffset)).mp4"
            .replacingOccurrences(of: ":", with: "")
    }

    private func timecode(_ seconds: Double) -> String {
        let totalSeconds = max(Int(seconds.rounded()), 0)
        return String(format: "%02d:%02d", totalSeconds / 60, totalSeconds % 60)
    }
}

struct RangeSlider: View {
    @Binding var lowerValue: Double
    @Binding var upperValue: Double
    let bounds: ClosedRange<Double>

    var body: some View {
        HStack(spacing: 8) {
            Slider(
                value: Binding(
                    get: { lowerValue },
                    set: { lowerValue = min(max($0, bounds.lowerBound), upperValue - 1) }
                ),
                in: bounds
            )
            .help("Export start")

            Slider(
                value: Binding(
                    get: { upperValue },
                    set: { upperValue = max(min($0, bounds.upperBound), lowerValue + 1) }
                ),
                in: bounds
            )
            .help("Export end")
        }
    }
}

enum PlaybackExportError: LocalizedError {
    case exporterUnavailable
    case exportFailed

    var errorDescription: String? {
        switch self {
        case .exporterUnavailable:
            return "Could not create an MP4 exporter for this recording."
        case .exportFailed:
            return "The selected range could not be exported."
        }
    }
}

final class PlaybackExportSessionBox: @unchecked Sendable {
    let exporter: AVAssetExportSession

    init(_ exporter: AVAssetExportSession) {
        self.exporter = exporter
    }
}

// Genetec-style investigation search: dense 3-column layout with a left
// filter rail, center result list with severity stripes, and a right preview
// panel. Filters are multi-select; time range uses chip presets + custom.
struct SearchView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var licenseStore: LicenseStore
    @State private var query = ""
    @State private var selectedSegmentID: String?
    @State private var selectedDetectionID: UUID?
    @State private var actionMessage: String?
    @State private var filterCameraIDs: Set<UUID> = []
    @State private var filterKinds: Set<AIDetectionKind> = []
    @State private var activeTab: SearchTab = .recordings
    @State private var isPlaybackPaused = false
    @State private var seekNudge: PlaybackSeekNudge?
    @State private var datePreset: SearchDatePreset = .last24Hours
    @State private var customStart: Date = Date().addingTimeInterval(-86_400)
    @State private var customEnd: Date = Date()
    // ── Claude AI: natural-language search + daily digest ──
    @State private var aiQuery = ""
    @State private var aiAnswer: String?
    @State private var aiMatchIDs: Set<UUID>?   // nil = no AI filter active
    @State private var aiBusy = false
    @State private var aiError: String?
    @State private var digestText: String?
    @State private var digestBusy = false
    @State private var showDigest = false

    enum SearchTab: String, CaseIterable, Identifiable {
        case recordings = "Recordings"
        case detections = "Events"
        var id: String { rawValue }
        var symbol: String {
            switch self {
            case .recordings: return "film.stack"
            case .detections: return "sparkle.magnifyingglass"
            }
        }
    }

    private var dateRange: ClosedRange<Date> {
        switch datePreset {
        case .lastHour:     return Date().addingTimeInterval(-3600)...Date()
        case .last24Hours:  return Date().addingTimeInterval(-86_400)...Date()
        case .last7Days:    return Date().addingTimeInterval(-7 * 86_400)...Date()
        case .last30Days:   return Date().addingTimeInterval(-30 * 86_400)...Date()
        case .custom:
            let lo = min(customStart, customEnd)
            let hi = max(customStart, customEnd)
            return lo...hi
        }
    }

    private var filteredSegments: [RecordingSegment] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        let range = dateRange
        return mediaIngestStore.recordingSegments
            .filter { segment in
                if filterCameraIDs.isEmpty == false && filterCameraIDs.contains(segment.cameraID) == false { return false }
                guard range.contains(segment.createdAt) || range.contains(segment.modifiedAt) else { return false }
                guard q.isEmpty == false else { return true }
                let name = cameraName(for: segment.cameraID)
                return [segment.fileName, segment.dateLabel, segment.timeLabel, name]
                    .joined(separator: " ").localizedCaseInsensitiveContains(q)
            }
            .sorted { $0.createdAt > $1.createdAt }
    }

    private var filteredDetections: [AIDetectionEvent] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        let range = dateRange
        let allDetections = cameraStore.cameras.flatMap { camera in
            aiDetectionStore.detections(for: camera.id)
        }

        return allDetections
            .filter { event in
                // When a Claude search is active, show only the events it matched.
                if let ai = aiMatchIDs, ai.contains(event.id) == false { return false }
                if filterCameraIDs.isEmpty == false && filterCameraIDs.contains(event.cameraID) == false { return false }
                if filterKinds.isEmpty == false && filterKinds.contains(event.kind) == false { return false }
                guard range.contains(event.timestamp) else { return false }
                guard q.isEmpty == false else { return true }
                let name = cameraName(for: event.cameraID)
                let searchable = [event.kind.rawValue, event.detectedText ?? "", name, event.timeLabel].joined(separator: " ")
                return searchable.localizedCaseInsensitiveContains(q)
            }
            .sorted { $0.timestamp > $1.timestamp }
    }

    /// Lightweight event dicts for the digest/search edge functions.
    private func aiEventPayload(limit: Int) -> [[String: String]] {
        let inRange = cameraStore.cameras.flatMap { aiDetectionStore.detections(for: $0.id) }
            .filter { dateRange.contains($0.timestamp) }
        // Over the cap, keep the events Claude can actually match on (the ones
        // with a scene description) before bare detector hits, newest first
        // within each group — then present the kept set chronologically.
        let kept = inRange
            .sorted { lhs, rhs in
                let l = lhs.sceneDescription != nil, r = rhs.sceneDescription != nil
                return l != r ? l : lhs.timestamp > rhs.timestamp
            }
            .prefix(limit)
            .sorted { $0.timestamp > $1.timestamp }
        return kept.map { e in
            var item = [
                "id": e.id.uuidString,
                "time": SentinelAIClient.searchDateFormatter.string(from: e.timestamp),
                "camera": cameraName(for: e.cameraID),
                "kind": e.kind.rawValue,
                "description": e.sceneDescription ?? e.overlayLabel,
            ]
            if e.tags.isEmpty == false { item["tags"] = e.tags.joined(separator: ", ") }
            if e.threat != .none {
                item["threat"] = e.threat.rawValue + (e.threatReason.map { " — \($0)" } ?? "")
            }
            return item
        }
    }

    private func runAISearch() {
        let q = aiQuery.trimmingCharacters(in: .whitespacesAndNewlines)
        guard q.isEmpty == false, aiBusy == false else { return }
        aiBusy = true; aiError = nil; activeTab = .detections
        let events = aiEventPayload(limit: 600)
        Task {
            do {
                let result = try await licenseStore.searchEvents(query: q, events: events)
                await MainActor.run {
                    aiAnswer = result.answer
                    aiMatchIDs = Set(result.matches)
                    aiBusy = false
                }
            } catch {
                await MainActor.run {
                    aiError = error.localizedDescription
                    aiBusy = false
                }
            }
        }
    }

    private func clearAISearch() {
        aiQuery = ""; aiAnswer = nil; aiMatchIDs = nil; aiError = nil
    }

    private func loadDigest() {
        guard digestBusy == false else { return }
        digestBusy = true; showDigest = true; digestText = nil; aiError = nil
        let events = aiEventPayload(limit: 400)
        Task {
            do {
                let text = try await licenseStore.dailyDigest(label: rangeLabel.lowercased(), events: events)
                await MainActor.run { digestText = text; digestBusy = false }
            } catch {
                await MainActor.run {
                    digestText = "Couldn't build the digest: " + (error.localizedDescription)
                    digestBusy = false
                }
            }
        }
    }

    private var selectedSegment: RecordingSegment? {
        filteredSegments.first { $0.id == selectedSegmentID } ?? filteredSegments.first
    }

    private var selectedDetection: AIDetectionEvent? {
        filteredDetections.first { $0.id == selectedDetectionID }
    }

    private var resultCount: Int {
        activeTab == .recordings ? filteredSegments.count : filteredDetections.count
    }

    private var histogramTimestamps: [Date] {
        activeTab == .recordings ? filteredSegments.map(\.createdAt) : filteredDetections.map(\.timestamp)
    }

    private var hasActiveFilters: Bool {
        filterCameraIDs.isEmpty == false || filterKinds.isEmpty == false || datePreset != .last24Hours || query.isEmpty == false
    }

    var body: some View {
        HStack(spacing: 0) {
            // LEFT — filter rail
            filterRail
                .frame(width: 230)
                .background(SentinelTheme.chrome)

            Divider().overlay(SentinelTheme.line)

            // CENTER — header strip + histogram + result list
            VStack(spacing: 0) {
                headerStrip
                Divider().overlay(SentinelTheme.line)
                if activeTab == .detections {
                    aiAssistBar
                    Divider().overlay(SentinelTheme.line)
                }
                resultHistogram
                Divider().overlay(SentinelTheme.line)
                resultList
            }
            .background(SentinelTheme.background)
            .sheet(isPresented: $showDigest) { digestSheet }

            Divider().overlay(SentinelTheme.line)

            ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if activeTab == .recordings {
                    SentinelPanel("Preview", systemImage: "play.rectangle.fill") {
                        VStack(alignment: .leading, spacing: 10) {
                            if let seg = selectedSegment {
                                RecordingPlaybackSurface(
                                    segment: seg,
                                    speed: "1x",
                                    seekOffset: nil,
                                    isPaused: isPlaybackPaused,
                                    seekNudge: seekNudge
                                )
                                .aspectRatio(16 / 9, contentMode: .fit)
                                .clipShape(RoundedRectangle(cornerRadius: 8))
                                .overlay {
                                    RoundedRectangle(cornerRadius: 8)
                                        .stroke(SentinelTheme.line, lineWidth: 1)
                                }

                                HStack(spacing: 8) {
                                    Button {
                                        seekNudge = PlaybackSeekNudge(seconds: -10)
                                    } label: {
                                        Label("Back 10s", systemImage: "gobackward.10")
                                    }
                                    .labelStyle(.iconOnly)

                                    Button {
                                        isPlaybackPaused.toggle()
                                    } label: {
                                        Label(isPlaybackPaused ? "Play" : "Pause",
                                              systemImage: isPlaybackPaused ? "play.fill" : "pause.fill")
                                    }
                                    .buttonStyle(.borderedProminent)
                                    .controlSize(.small)

                                    Button {
                                        seekNudge = PlaybackSeekNudge(seconds: 10)
                                    } label: {
                                        Label("Forward 10s", systemImage: "goforward.10")
                                    }
                                    .labelStyle(.iconOnly)

                                    Spacer()

                                    Button {
                                        NSWorkspace.shared.open(seg.fileURL)
                                    } label: {
                                        Label("QuickTime", systemImage: "arrow.up.forward.app")
                                    }
                                    .controlSize(.small)
                                }
                                .buttonStyle(.bordered)

                                Divider().overlay(SentinelTheme.line)

                                DetailRow(label: "Camera", value: cameraName(for: seg.cameraID))
                                DetailRow(label: "Date", value: seg.dateLabel)
                                DetailRow(label: "Time", value: seg.timeLabel)
                                DetailRow(label: "Size", value: seg.sizeLabel)
                            } else {
                                ZStack {
                                    RoundedRectangle(cornerRadius: 8).fill(.black.opacity(0.32))
                                    Image(systemName: "play.rectangle")
                                        .font(.system(size: 32))
                                        .foregroundStyle(.secondary)
                                }
                                .aspectRatio(16 / 9, contentMode: .fit)

                                EmptyStateLine(text: "Select a recording to preview it here.")
                            }
                        }
                    }
                } else {
                    SentinelPanel("Selected Detection", systemImage: "brain") {
                        VStack(alignment: .leading, spacing: 10) {
                            if let det = selectedDetection {
                                if let desc = det.sceneDescription {
                                    VStack(alignment: .leading, spacing: 4) {
                                        Label("AI Description", systemImage: "sparkles")
                                            .font(.caption2.weight(.bold))
                                            .foregroundStyle(SentinelTheme.accent)
                                        Text(desc)
                                            .font(.callout)
                                            .fixedSize(horizontal: false, vertical: true)
                                    }
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(10)
                                    .background(SentinelTheme.accent.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
                                }
                                DetailRow(label: "Camera", value: cameraName(for: det.cameraID))
                                DetailRow(label: "Type", value: det.kind.rawValue)
                                DetailRow(label: "Time", value: det.timeLabel)
                                DetailRow(label: "Confidence", value: det.confidenceLabel)
                                if let text = det.detectedText {
                                    DetailRow(label: "Value", value: text)
                                }
                            } else {
                                EmptyStateLine(text: "Select a detection event.")
                            }
                        }
                    }
                }

                SentinelPanel("Fast Actions", systemImage: "bolt.fill") {
                    VStack(spacing: 8) {
                        Button {
                            commandCenter.requestOpen(.live)
                            actionMessage = "Opened Playback."
                        } label: {
                            Label("Open in Playback", systemImage: "play.rectangle.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }

                        Button {
                            caseworkStore.createEvidencePackage(from: selectedSegment)
                            actionMessage = "Evidence clip created."
                        } label: {
                            Label("Create Evidence Clip", systemImage: "shippingbox.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(selectedSegment == nil && activeTab == .recordings)

                        Button {
                            if let incident = caseworkStore.incidents.first {
                                caseworkStore.linkLatestClip(to: incident)
                                actionMessage = "Linked to active incident."
                            } else {
                                actionMessage = "Create an incident first."
                            }
                        } label: {
                            Label("Pin to Incident", systemImage: "pin.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(caseworkStore.incidents.isEmpty)

                        if let actionMessage {
                            Text(actionMessage)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(10)
                                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                        }
                    }
                    .buttonStyle(.bordered)
                }
            }
            .padding(14)
            } // ScrollView
            .frame(width: 380)
            .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.background)
        .onChange(of: query) { _ in selectedSegmentID = nil; selectedDetectionID = nil; actionMessage = nil }
        .onChange(of: activeTab) { _ in selectedSegmentID = nil; selectedDetectionID = nil }
        .onChange(of: selectedSegmentID) { _ in isPlaybackPaused = false; seekNudge = nil }
        .onAppear {
            // Arriving via the AI workspace's "Open AI Search" lands directly on
            // the Claude natural-language (Events) tab, not recordings search.
            if commandCenter.pendingAISearch {
                activeTab = .detections
                commandCenter.pendingAISearch = false
            }
        }
    }

    // MARK: - Genetec-style sub-views

    private var filterRail: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                SearchFilterSection(title: "Time Range", symbol: "clock") {
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(SearchDatePreset.allCases) { preset in
                            SearchPresetChip(
                                label: preset.label,
                                isSelected: datePreset == preset
                            ) { datePreset = preset }
                        }
                        if datePreset == .custom {
                            VStack(alignment: .leading, spacing: 4) {
                                DatePicker("From", selection: $customStart)
                                    .datePickerStyle(.compact)
                                    .labelsHidden()
                                DatePicker("To", selection: $customEnd)
                                    .datePickerStyle(.compact)
                                    .labelsHidden()
                            }
                            .padding(.top, 4)
                        }
                    }
                }

                SearchFilterSection(
                    title: "Cameras",
                    symbol: "video.fill",
                    trailing: filterCameraIDs.isEmpty ? nil : "\(filterCameraIDs.count)"
                ) {
                    VStack(alignment: .leading, spacing: 4) {
                        HStack(spacing: 8) {
                            Button("All") {
                                filterCameraIDs = Set(cameraStore.cameras.map(\.id))
                            }
                            Button("None") {
                                filterCameraIDs.removeAll()
                            }
                        }
                        .buttonStyle(.plain)
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(SentinelTheme.accent)

                        if cameraStore.cameras.isEmpty {
                            Text("No cameras configured.")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        } else {
                            ForEach(cameraStore.cameras) { camera in
                                SearchCheckRow(
                                    label: camera.name,
                                    secondary: camera.location,
                                    isOn: filterCameraIDs.contains(camera.id),
                                    symbol: nil,
                                    color: nil
                                ) {
                                    if filterCameraIDs.contains(camera.id) {
                                        filterCameraIDs.remove(camera.id)
                                    } else {
                                        filterCameraIDs.insert(camera.id)
                                    }
                                }
                            }
                        }
                    }
                }

                if activeTab == .detections {
                    SearchFilterSection(
                        title: "Event Types",
                        symbol: "sparkle.magnifyingglass",
                        trailing: filterKinds.isEmpty ? nil : "\(filterKinds.count)"
                    ) {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(AIDetectionKind.allCases) { kind in
                                let c = kind.overlayColor
                                SearchCheckRow(
                                    label: kind.rawValue,
                                    secondary: nil,
                                    isOn: filterKinds.contains(kind),
                                    symbol: kind.symbol,
                                    color: Color(red: c.0, green: c.1, blue: c.2)
                                ) {
                                    if filterKinds.contains(kind) {
                                        filterKinds.remove(kind)
                                    } else {
                                        filterKinds.insert(kind)
                                    }
                                }
                            }
                        }
                    }
                }

                if hasActiveFilters {
                    Button {
                        clearFilters()
                    } label: {
                        Label("Clear All Filters", systemImage: "xmark.circle")
                            .font(.caption.weight(.semibold))
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                }

                Spacer(minLength: 4)
            }
            .padding(14)
        }
    }

    private var headerStrip: some View {
        VStack(spacing: 8) {
            HStack(spacing: 10) {
                Image(systemName: "magnifyingglass")
                    .foregroundStyle(.secondary)
                TextField(
                    activeTab == .recordings
                        ? "Search recordings by camera, file, date…"
                        : "Search events by type, plate, camera…",
                    text: $query
                )
                .textFieldStyle(.plain)
                .font(.body)

                if query.isEmpty == false {
                    Button { query = "" } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.secondary)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(SentinelTheme.line, lineWidth: 1))

            HStack(spacing: 12) {
                HStack(spacing: 0) {
                    ForEach(SearchTab.allCases) { tab in
                        Button {
                            activeTab = tab
                        } label: {
                            HStack(spacing: 6) {
                                Image(systemName: tab.symbol)
                                Text(tab.rawValue)
                            }
                            .font(.caption.weight(.semibold))
                            .padding(.horizontal, 12)
                            .padding(.vertical, 6)
                            .foregroundStyle(activeTab == tab ? .white : .secondary)
                            .background(
                                activeTab == tab ? SentinelTheme.accent : Color.clear,
                                in: RoundedRectangle(cornerRadius: 5)
                            )
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(2)
                .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: 7))
                .overlay(RoundedRectangle(cornerRadius: 7).stroke(SentinelTheme.line, lineWidth: 1))

                Spacer()

                Text("\(resultCount) result\(resultCount == 1 ? "" : "s")")
                    .font(.caption.monospacedDigit().weight(.semibold))
                    .foregroundStyle(.secondary)

                Text("·")
                    .foregroundStyle(.secondary)

                Text(rangeLabel)
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(SentinelTheme.chrome)
    }

    // Claude-powered natural-language search + a one-tap "digest" of the range.
    private var aiAssistBar: some View {
        VStack(spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles").foregroundStyle(SentinelTheme.accent)
                TextField("Ask Claude — “anyone in a red shirt last night?”", text: $aiQuery)
                    .textFieldStyle(.plain)
                    .font(.body)
                    .onSubmit(runAISearch)
                if aiMatchIDs != nil || aiAnswer != nil {
                    Button { clearAISearch() } label: {
                        Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
                    }
                    .buttonStyle(.plain)
                    .help("Clear AI search")
                }
                Button(action: runAISearch) {
                    HStack(spacing: 5) {
                        if aiBusy { ProgressView().controlSize(.small) }
                        Text("Ask").font(.caption.weight(.semibold))
                    }
                }
                .buttonStyle(.borderedProminent).controlSize(.small)
                .disabled(aiBusy || aiQuery.trimmingCharacters(in: .whitespaces).isEmpty)

                Button(action: loadDigest) {
                    HStack(spacing: 5) {
                        Image(systemName: "doc.text.magnifyingglass")
                        Text("Digest").font(.caption.weight(.semibold))
                    }
                }
                .buttonStyle(.bordered).controlSize(.small)
                .disabled(digestBusy)
                .help("Summarize this range with Claude")
            }
            .padding(.horizontal, 12).padding(.vertical, 7)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6)
                .stroke(aiMatchIDs != nil ? SentinelTheme.accent.opacity(0.55) : SentinelTheme.line, lineWidth: 1))

            if let answer = aiAnswer {
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: "sparkles").font(.caption2).foregroundStyle(SentinelTheme.accent)
                    Text(answer).font(.caption).foregroundStyle(.primary)
                    Spacer(minLength: 8)
                    Text("\(aiMatchIDs?.count ?? 0) match\((aiMatchIDs?.count ?? 0) == 1 ? "" : "es")")
                        .font(.caption2.monospacedDigit().weight(.semibold)).foregroundStyle(.secondary)
                }
                .padding(.horizontal, 4)
            }
            if let err = aiError {
                Text(err).font(.caption2).foregroundStyle(Color(red: 1, green: 0.42, blue: 0.4))
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .background(SentinelTheme.chrome)
    }

    private var digestSheet: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles").foregroundStyle(SentinelTheme.accent)
                Text("Digest · \(rangeLabel)").font(.headline)
                Spacer()
                Button { showDigest = false } label: {
                    Image(systemName: "xmark.circle.fill").font(.title3).foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
            }
            Divider().overlay(SentinelTheme.line)
            if digestBusy {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Summarizing with Claude…").foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let text = digestText {
                ScrollView {
                    Text(text).font(.body).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .padding(20)
        .frame(width: 480, height: 380)
        .background(SentinelTheme.background)
    }

    private var resultHistogram: some View {
        SearchResultHistogram(timestamps: histogramTimestamps, range: dateRange)
            .frame(height: 36)
            .background(SentinelTheme.background)
    }

    @ViewBuilder
    private var resultList: some View {
        ScrollView {
            LazyVStack(spacing: 1) {
                if activeTab == .recordings {
                    if filteredSegments.isEmpty {
                        EmptyStateLine(text: mediaIngestStore.recordingSegments.isEmpty
                            ? "No recordings indexed yet."
                            : "No recordings match the current filters.")
                            .padding(.top, 24)
                    } else {
                        ForEach(filteredSegments.prefix(300)) { segment in
                            SearchRecordingRow(
                                segment: segment,
                                cameraName: cameraName(for: segment.cameraID),
                                isSelected: selectedSegment?.id == segment.id
                            ) { selectedSegmentID = segment.id }
                        }
                    }
                } else {
                    if filteredDetections.isEmpty {
                        EmptyStateLine(text: "No events match the current filters.")
                            .padding(.top, 24)
                    } else {
                        ForEach(filteredDetections.prefix(300)) { event in
                            detectionRow(event)
                        }
                    }
                }
            }
            .padding(.vertical, 4)
        }
    }

    private var rangeLabel: String {
        let r = dateRange
        let formatter = DateFormatter()
        formatter.dateFormat = "MMM d, HH:mm"
        return "\(formatter.string(from: r.lowerBound)) — \(formatter.string(from: r.upperBound))"
    }

    private func clearFilters() {
        filterCameraIDs.removeAll()
        filterKinds.removeAll()
        datePreset = .last24Hours
        query = ""
    }

    private func detectionRow(_ event: AIDetectionEvent) -> some View {
        let c = event.kind.overlayColor
        let color = Color(red: c.0, green: c.1, blue: c.2)
        let isSelected = selectedDetectionID == event.id
        return Button {
            selectedDetectionID = event.id
        } label: {
            HStack(spacing: 0) {
                Rectangle()
                    .fill(color)
                    .frame(width: 3)

                HStack(spacing: 10) {
                    Image(systemName: event.kind.symbol)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(color)
                        .frame(width: 20)

                    VStack(alignment: .leading, spacing: 2) {
                        // Prefer the AI description as the headline when present;
                        // fall back to the plain "PERSON 92%" label otherwise.
                        Text(event.sceneDescription ?? event.overlayLabel)
                            .font(.caption.weight(.semibold))
                            .lineLimit(2)
                        HStack(spacing: 6) {
                            if event.sceneDescription != nil {
                                Image(systemName: "sparkles")
                                    .font(.system(size: 8, weight: .bold))
                                    .foregroundStyle(SentinelTheme.accent)
                            }
                            if event.threat.isNotable {
                                ThreatBadge(threat: event.threat, reason: event.threatReason)
                            }
                            Text(cameraName(for: event.cameraID))
                                .font(.caption2.weight(.medium))
                            Text("·").foregroundStyle(.secondary)
                            Text(event.timeLabel)
                                .font(.caption2.monospacedDigit())
                                .foregroundStyle(.secondary)
                        }
                        .lineLimit(1)
                    }

                    Spacer()

                    Text(event.confidenceLabel)
                        .font(.caption2.monospacedDigit().weight(.semibold))
                        .foregroundStyle(.secondary)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 8)
            }
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.18) : Color.clear
            )
            .overlay(alignment: .bottom) {
                Divider().overlay(SentinelTheme.line.opacity(0.5))
            }
        }
        .buttonStyle(.plain)
    }

    private func cameraName(for cameraID: UUID) -> String {
        cameraStore.cameras.first { $0.id == cameraID }?.name ?? cameraID.uuidString.prefix(8).uppercased()
    }
}

// MARK: - Search rail helpers (Genetec-style)

enum SearchDatePreset: String, CaseIterable, Identifiable {
    case lastHour
    case last24Hours
    case last7Days
    case last30Days
    case custom

    var id: String { rawValue }

    var label: String {
        switch self {
        case .lastHour:    return "Last hour"
        case .last24Hours: return "Last 24 hours"
        case .last7Days:   return "Last 7 days"
        case .last30Days:  return "Last 30 days"
        case .custom:      return "Custom range…"
        }
    }
}

struct SearchFilterSection<Content: View>: View {
    let title: String
    let symbol: String
    var trailing: String? = nil
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: symbol)
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(SentinelTheme.accent)
                Text(title.uppercased())
                    .font(.caption2.weight(.bold))
                    .tracking(0.6)
                    .foregroundStyle(.secondary)
                Spacer()
                if let trailing {
                    Text(trailing)
                        .font(.caption2.monospacedDigit().weight(.bold))
                        .foregroundStyle(.white)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 1)
                        .background(SentinelTheme.accent, in: Capsule())
                }
            }
            content()
        }
    }
}

struct SearchPresetChip: View {
    let label: String
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                Image(systemName: isSelected ? "largecircle.fill.circle" : "circle")
                    .font(.caption2)
                    .foregroundStyle(isSelected ? SentinelTheme.accent : .secondary)
                Text(label)
                    .font(.caption.weight(isSelected ? .semibold : .regular))
                    .foregroundStyle(isSelected ? .primary : .secondary)
                Spacer()
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.10) : Color.clear,
                in: RoundedRectangle(cornerRadius: 5)
            )
        }
        .buttonStyle(.plain)
    }
}

struct SearchCheckRow: View {
    let label: String
    let secondary: String?
    let isOn: Bool
    let symbol: String?
    let color: Color?
    let toggle: () -> Void

    var body: some View {
        Button(action: toggle) {
            HStack(spacing: 7) {
                Image(systemName: isOn ? "checkmark.square.fill" : "square")
                    .font(.caption)
                    .foregroundStyle(isOn ? SentinelTheme.accent : .secondary)
                if let symbol {
                    Image(systemName: symbol)
                        .font(.caption2)
                        .foregroundStyle(color ?? .secondary)
                        .frame(width: 14)
                }
                VStack(alignment: .leading, spacing: 1) {
                    Text(label)
                        .font(.caption.weight(isOn ? .semibold : .regular))
                        .lineLimit(1)
                    if let secondary, secondary.isEmpty == false {
                        Text(secondary)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                Spacer()
            }
            .padding(.horizontal, 6)
            .padding(.vertical, 4)
            .background(
                isOn ? SentinelTheme.accent.opacity(0.08) : Color.clear,
                in: RoundedRectangle(cornerRadius: 4)
            )
        }
        .buttonStyle(.plain)
    }
}

struct SearchResultHistogram: View {
    let timestamps: [Date]
    let range: ClosedRange<Date>

    private let bucketCount = 48

    private var buckets: [Int] {
        var counts = Array(repeating: 0, count: bucketCount)
        let total = max(range.upperBound.timeIntervalSince(range.lowerBound), 1)
        for ts in timestamps {
            let offset = max(0, ts.timeIntervalSince(range.lowerBound))
            let idx = min(bucketCount - 1, Int((offset / total) * Double(bucketCount)))
            if idx >= 0 { counts[idx] += 1 }
        }
        return counts
    }

    var body: some View {
        let counts = buckets
        let maxCount = max(counts.max() ?? 1, 1)
        GeometryReader { geo in
            HStack(alignment: .bottom, spacing: 1) {
                ForEach(0..<bucketCount, id: \.self) { i in
                    let h = CGFloat(counts[i]) / CGFloat(maxCount) * (geo.size.height - 4)
                    Rectangle()
                        .fill(counts[i] > 0 ? SentinelTheme.accent : SentinelTheme.line)
                        .frame(height: max(2, h))
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 4)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
        }
    }
}

struct SearchRecordingRow: View {
    let segment: RecordingSegment
    let cameraName: String
    let isSelected: Bool
    let select: () -> Void

    var body: some View {
        Button(action: select) {
            HStack(spacing: 0) {
                Rectangle()
                    .fill(isSelected ? SentinelTheme.accent : SentinelTheme.accent.opacity(0.35))
                    .frame(width: 3)

                HStack(spacing: 10) {
                    Image(systemName: "film")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(isSelected ? SentinelTheme.accent : .secondary)
                        .frame(width: 20)

                    VStack(alignment: .leading, spacing: 2) {
                        Text(cameraName)
                            .font(.caption.weight(.semibold))
                            .lineLimit(1)
                        HStack(spacing: 6) {
                            Text(segment.dateLabel)
                                .font(.caption2.monospacedDigit())
                            Text("·").foregroundStyle(.secondary)
                            Text(segment.timeLabel)
                                .font(.caption2.monospacedDigit())
                            Text("·").foregroundStyle(.secondary)
                            Text(segment.fileName)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                        .foregroundStyle(.secondary)
                    }

                    Spacer()

                    Text(segment.sizeLabel)
                        .font(.caption2.monospacedDigit().weight(.semibold))
                        .foregroundStyle(.secondary)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 9)
            }
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.18) : Color.clear
            )
            .overlay(alignment: .bottom) {
                Divider().overlay(SentinelTheme.line.opacity(0.5))
            }
        }
        .buttonStyle(.plain)
    }
}

enum AlertQueueFilter: String, CaseIterable, Identifiable {
    case active = "Active"
    case new = "New"
    case investigating = "Investigating"
    case snoozed = "Snoozed"
    case resolved = "Resolved"

    var id: String { rawValue }

    var state: AlertState? {
        switch self {
        case .active: return nil
        case .new: return .new
        case .investigating: return .investigating
        case .snoozed: return .snoozed
        case .resolved: return .resolved
        }
    }
}

struct AlertsView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @State private var filter = AlertQueueFilter.active
    @State private var selectedAlertID: UUID?
    @State private var selectionMode = false
    @State private var selectedAlertIDs = Set<UUID>()

    private var filteredAlerts: [AlertEvent] {
        caseworkStore.alerts(for: filter.state)
    }

    private var selectedAlert: AlertEvent? {
        filteredAlerts.first { $0.id == selectedAlertID } ??
        caseworkStore.alerts.first { $0.id == selectedAlertID } ??
        filteredAlerts.first
    }

    private var newCount: Int {
        caseworkStore.alerts.filter { $0.alertState == .new }.count
    }

    var body: some View {
        GeometryReader { proxy in
            let showsResponsePanel = proxy.size.width >= 1120
            let queueWidth = min(390, max(310, proxy.size.width * 0.32))

            HStack(spacing: 0) {
                alarmQueue
                    .frame(width: queueWidth)

                Divider()
                    .overlay(SentinelTheme.line)

                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        AlertDetailPane(alert: selectedAlert)

                        if showsResponsePanel == false {
                            AlertResponsePanel(alert: selectedAlert)
                        }
                    }
                    .padding(14)
                }
                .frame(maxWidth: .infinity)

                if showsResponsePanel {
                    Divider()
                        .overlay(SentinelTheme.line)

                    AlertResponsePanel(alert: selectedAlert)
                        .frame(width: 328)
                        .background(SentinelTheme.chrome)
                }
            }
        }
        .background(SentinelTheme.background)
        .onAppear {
            caseworkStore.syncOperationalAlarms(cameras: cameraStore.cameras, mediaIngestStore: mediaIngestStore)
            selectedAlertID = selectedAlert?.id
        }
        .onChange(of: filteredAlerts.map(\.id)) { _ in
            if selectedAlert == nil {
                selectedAlertID = filteredAlerts.first?.id
            }
        }
    }

    private var alarmQueue: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Label("Alarm Manager", systemImage: "bell.badge.fill")
                        .font(.headline)
                    Text("\(caseworkStore.activeAlerts.count) active · \(newCount) new")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button {
                    caseworkStore.syncOperationalAlarms(cameras: cameraStore.cameras, mediaIngestStore: mediaIngestStore)
                } label: { Image(systemName: "arrow.clockwise") }
                .help("Refresh alarms")

                Button {
                    selectionMode.toggle()
                    if selectionMode == false { selectedAlertIDs = [] }
                } label: {
                    Text(selectionMode ? "Done" : "Select")
                        .font(.caption.weight(.semibold))
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
            }

            Picker("Queue", selection: $filter) {
                ForEach(AlertQueueFilter.allCases) { queueFilter in
                    Text(queueFilter.rawValue).tag(queueFilter)
                }
            }
            .pickerStyle(.segmented)

            if selectionMode && selectedAlertIDs.isEmpty == false {
                // Bulk action bar
                HStack(spacing: 6) {
                    Text("\(selectedAlertIDs.count) selected")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(SentinelTheme.accent)
                    Spacer()
                    Button {
                        caseworkStore.bulkUpdateAlerts(selectedAlertIDs, to: .acknowledged)
                        selectedAlertIDs = []
                    } label: { Label("Acknowledge", systemImage: "checkmark.circle.fill") }
                    .buttonStyle(.bordered).controlSize(.small)

                    Button {
                        caseworkStore.bulkUpdateAlerts(selectedAlertIDs, to: .resolved)
                        selectedAlertIDs = []
                    } label: { Label("Resolve", systemImage: "xmark.circle.fill") }
                    .buttonStyle(.bordered).controlSize(.small)

                    Button {
                        caseworkStore.bulkUpdateAlerts(selectedAlertIDs, to: .falseAlarm)
                        selectedAlertIDs = []
                    } label: { Label("False Alarm", systemImage: "minus.circle") }
                    .buttonStyle(.bordered).controlSize(.small)
                }
                .padding(10)
                .background(SentinelTheme.accent.opacity(0.10), in: RoundedRectangle(cornerRadius: 8))
            } else if selectionMode == false {
                HStack(spacing: 8) {
                    Button {
                        caseworkStore.acknowledgeNewAlerts()
                    } label: {
                        Label("Acknowledge New", systemImage: "checkmark.circle.fill")
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.bordered)
                    .disabled(newCount == 0)

                    Menu {
                        Button { caseworkStore.resolveAllActiveAlerts() } label: {
                            Label("Resolve All Active", systemImage: "checkmark.circle")
                        }
                        .disabled(caseworkStore.activeAlerts.isEmpty)

                        Button(role: .destructive) { caseworkStore.clearResolvedAlerts() } label: {
                            Label("Clear Dismissed", systemImage: "trash")
                        }
                        .disabled(caseworkStore.alerts.filter { !$0.isOpen }.isEmpty)

                        Divider()

                        Button(role: .destructive) {
                            caseworkStore.resolveAllActiveAlerts()
                            caseworkStore.clearResolvedAlerts()
                        } label: { Label("Clear All Alarms", systemImage: "trash.fill") }
                        .disabled(caseworkStore.alerts.isEmpty)
                    } label: { Image(systemName: "ellipsis.circle") }
                    .menuStyle(.button).buttonStyle(.bordered)
                    .help("Bulk alarm actions")
                }
            }

            ScrollView {
                LazyVStack(spacing: 8) {
                    if filteredAlerts.isEmpty {
                        EmptyStateLine(text: filter == .active ? "No active alarms." : "No \(filter.rawValue.lowercased()) alarms.")
                    } else {
                        ForEach(filteredAlerts) { alert in
                            HStack(spacing: 8) {
                                if selectionMode {
                                    Image(systemName: selectedAlertIDs.contains(alert.id) ? "checkmark.circle.fill" : "circle")
                                        .foregroundStyle(selectedAlertIDs.contains(alert.id) ? SentinelTheme.accent : .secondary)
                                        .font(.title3)
                                        .onTapGesture {
                                            if selectedAlertIDs.contains(alert.id) {
                                                selectedAlertIDs.remove(alert.id)
                                            } else {
                                                selectedAlertIDs.insert(alert.id)
                                            }
                                        }
                                }
                                AlertQueueRow(
                                    alert: alert,
                                    isSelected: selectionMode == false && selectedAlert?.id == alert.id
                                ) {
                                    if selectionMode {
                                        if selectedAlertIDs.contains(alert.id) {
                                            selectedAlertIDs.remove(alert.id)
                                        } else {
                                            selectedAlertIDs.insert(alert.id)
                                        }
                                    } else {
                                        selectedAlertID = alert.id
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        .padding(14)
        .background(SentinelTheme.background)
    }
}

struct AlertQueueRow: View {
    @EnvironmentObject private var caseworkStore: CaseworkStore
    let alert: AlertEvent
    let isSelected: Bool
    let select: () -> Void

    var body: some View {
        Button(action: select) {
            HStack(spacing: 0) {
                Rectangle()
                    .fill(alert.severity.tint)
                    .frame(width: 3)
                    .clipShape(UnevenRoundedRectangle(
                        topLeadingRadius: 8, bottomLeadingRadius: 8,
                        bottomTrailingRadius: 0, topTrailingRadius: 0
                    ))

                VStack(alignment: .leading, spacing: 7) {
                    HStack(spacing: 8) {
                        Image(systemName: alert.kind.symbol)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(alert.severity.tint)

                        Text(alert.title)
                            .font(.caption.weight(.semibold))
                            .lineLimit(1)

                        Spacer()

                        if alert.alertState == .new {
                            Button {
                                caseworkStore.setAlertState(
                                    alert, to: .acknowledged,
                                    owner: "Current Operator", note: "Quick acknowledged")
                            } label: {
                                Image(systemName: "checkmark.circle.fill")
                                    .foregroundStyle(.green)
                                    .font(.caption)
                            }
                            .buttonStyle(.plain)
                            .help("Acknowledge")
                        } else if alert.eventCount > 1 {
                            Text("×\(alert.eventCount)")
                                .font(.caption2.weight(.bold))
                                .foregroundStyle(.secondary)
                        }
                    }

                    Text(alert.detail.isEmpty ? alert.source : alert.detail)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)

                    HStack(spacing: 8) {
                        AlertStatePill(state: alert.alertState)
                        Spacer()
                        Text(alert.lastEventLabel)
                            .font(.caption2.monospacedDigit())
                            .foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 9)
            }
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.16) :
                alert.alertState == .new ? alert.severity.tint.opacity(0.07) : SentinelTheme.panelRaised,
                in: RoundedRectangle(cornerRadius: 8)
            )
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(
                        isSelected ? SentinelTheme.accent.opacity(0.76) :
                        alert.alertState == .new ? alert.severity.tint.opacity(0.35) : SentinelTheme.line.opacity(0.5),
                        lineWidth: 1
                    )
            }
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button {
                caseworkStore.setAlertState(alert, to: .acknowledged, owner: "Operator", note: "Acknowledged via context menu")
            } label: {
                Label("Acknowledge", systemImage: "checkmark.circle")
            }
            .disabled(alert.alertState != .new)

            Button {
                caseworkStore.resolveAlert(alert)
            } label: {
                Label("Resolve", systemImage: "checkmark.circle.fill")
            }
            .disabled(!alert.isOpen)

            Button {
                caseworkStore.markFalseAlarm(alert)
            } label: {
                Label("Mark False Alarm", systemImage: "exclamationmark.triangle")
            }
            .disabled(!alert.isOpen)
        }
    }
}

struct AlertDetailPane: View {
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var workflowStore: WorkflowStore
    let alert: AlertEvent?

    var body: some View {
        if let alert {
            VStack(alignment: .leading, spacing: 14) {
                SentinelPanel("Alarm Detail", systemImage: alert.kind.symbol) {
                    VStack(alignment: .leading, spacing: 12) {
                        HStack(alignment: .top, spacing: 12) {
                            VStack(alignment: .leading, spacing: 5) {
                                Text(alert.title)
                                    .font(.title3.weight(.semibold))

                                Text(alert.source)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }

                            Spacer()

                            SeverityBadge(severity: alert.severity)
                            AlertStatePill(state: alert.alertState)
                        }

                        Text(alert.detail.isEmpty ? "No extra alarm detail was recorded." : alert.detail)
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)

                        LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                            AlarmFact(title: "Type", value: alert.kind.rawValue, tint: alert.severity.tint)
                            AlarmFact(title: "Owner", value: alert.owner, tint: alert.owner == "Unassigned" ? .secondary : SentinelTheme.accent)
                            AlarmFact(title: "Events", value: "\(alert.eventCount)", tint: alert.eventCount > 1 ? SentinelTheme.amber : .secondary)
                        }
                    }
                }

                let instructions = workflowStore.instructions(for: alert)
                if instructions.isEmpty == false {
                    SentinelPanel("Operator Instructions", systemImage: "list.clipboard.fill") {
                        VStack(alignment: .leading, spacing: 10) {
                            ForEach(Array(instructions.enumerated()), id: \.offset) { _, item in
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(item.rule)
                                        .font(.caption.weight(.semibold))
                                        .foregroundStyle(SentinelTheme.accent)
                                    Text(item.text)
                                        .font(.callout)
                                        .fixedSize(horizontal: false, vertical: true)
                                }
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }

                AlertVideoContext(alert: alert)

                SentinelPanel("Response Timeline", systemImage: "list.bullet.rectangle") {
                    VStack(spacing: 8) {
                        if alert.responseLog.isEmpty {
                            EmptyStateLine(text: "No operator response yet.")
                        } else {
                            ForEach(Array(alert.responseLog.prefix(6).enumerated()), id: \.offset) { _, entry in
                                EmptyStateLine(text: entry)
                            }
                        }
                    }
                }
            }
        } else {
            EmptyStateLine(text: "No alarm selected.")
        }
    }
}

struct AlertVideoContext: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    let alert: AlertEvent

    private var camera: CameraFeed? {
        guard let cameraID = alert.cameraID else {
            return nil
        }

        return cameraStore.cameras.first { $0.id == cameraID }
    }

    var body: some View {
        SentinelPanel("Video Context", systemImage: "play.rectangle.fill") {
            VStack(alignment: .leading, spacing: 10) {
                ZStack {
                    if let camera,
                       let stream = mediaIngestStore.liveStream(for: camera.id) {
                        LocalLivePreviewSurface(stream: stream, contentMode: .fit)
                    } else if let camera {
                        CameraSignalSurface(camera: camera)
                    } else {
                        CameraSignalSurface(
                            camera: CameraFeed(
                                name: alert.source,
                                location: "Alarm source",
                                status: .offline,
                                resolution: "Pending",
                                fps: 0,
                                bitrate: "Pending",
                                ipAddress: "Unknown",
                                profile: "Alarm",
                                isRecording: false
                            )
                        )
                    }
                }
                .aspectRatio(16 / 9, contentMode: .fit)
                .clipShape(RoundedRectangle(cornerRadius: 8))
                .overlay {
                    RoundedRectangle(cornerRadius: 8)
                        .stroke(SentinelTheme.line, lineWidth: 1)
                }

                HStack {
                    Button {
                        commandCenter.requestOpen(.live)
                    } label: {
                        Label("Open Playback", systemImage: "play.rectangle.fill")
                    }

                    if let linkedClipPath = alert.linkedClipPath {
                        Button {
                            NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: linkedClipPath)])
                        } label: {
                            Label("Reveal Clip", systemImage: "arrow.up.forward.app")
                        }
                    }

                    Spacer()

                    if let linkedClipPath = alert.linkedClipPath {
                        Text(URL(fileURLWithPath: linkedClipPath).lastPathComponent)
                            .font(.caption2.monospaced())
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                }
                .buttonStyle(.bordered)
            }
        }
    }
}

struct AlertResponsePanel: View {
    @EnvironmentObject private var caseworkStore: CaseworkStore
    let alert: AlertEvent?
    @State private var responseNote = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            SentinelPanel("Response", systemImage: "hand.raised.fill") {
                VStack(spacing: 8) {
                    if let alert {
                        Button {
                            caseworkStore.acknowledgeAlert(alert)
                        } label: {
                            Label("Acknowledge", systemImage: "checkmark.circle.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(alert.alertState == .acknowledged || alert.isOpen == false)

                        Button {
                            caseworkStore.investigateAlert(alert)
                        } label: {
                            Label("Investigate", systemImage: "person.crop.circle.badge.checkmark")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(alert.alertState == .investigating || alert.isOpen == false)

                        Button {
                            caseworkStore.snoozeAlert(alert, minutes: 15)
                        } label: {
                            Label("Snooze 15m", systemImage: "moon.zzz.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(alert.isOpen == false)

                        Button {
                            caseworkStore.resolveAlert(alert)
                        } label: {
                            Label("Resolve", systemImage: "checkmark.seal.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(alert.isOpen == false)

                        Button {
                            caseworkStore.markFalseAlarm(alert)
                        } label: {
                            Label("False Alarm", systemImage: "xmark.seal.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(alert.isOpen == false)

                        Divider()
                            .overlay(SentinelTheme.line)

                        Button {
                            caseworkStore.createIncident(from: alert)
                        } label: {
                            Label("Create Incident", systemImage: "exclamationmark.bubble.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }

                        Button {
                            caseworkStore.assignAlertToCurrentOperator(alert)
                        } label: {
                            Label("Assign to Me", systemImage: "person.fill.checkmark")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }

                        TextField("Operator note", text: $responseNote)
                            .textFieldStyle(.roundedBorder)

                        Button {
                            caseworkStore.addResponseNote(to: alert, note: responseNote)
                            responseNote = ""
                        } label: {
                            Label("Add Note", systemImage: "text.bubble.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(responseNote.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    } else {
                        EmptyStateLine(text: "Select an alarm to respond.")
                    }
                }
                .buttonStyle(.bordered)
            }

            SentinelPanel("Queue Summary", systemImage: "chart.bar.fill") {
                VStack(spacing: 8) {
                    DetailRow(label: "New", value: "\(caseworkStore.alerts.filter { $0.alertState == .new }.count)")
                    DetailRow(label: "Investigating", value: "\(caseworkStore.alerts.filter { $0.alertState == .investigating }.count)")
                    DetailRow(label: "Snoozed", value: "\(caseworkStore.alerts.filter { $0.alertState == .snoozed }.count)")
                    DetailRow(label: "Resolved", value: "\(caseworkStore.alerts.filter { $0.alertState == .resolved }.count)")
                    DetailRow(label: "Incidents", value: "\(caseworkStore.incidents.count)")
                }
            }

            Spacer(minLength: 0)
        }
        .padding(14)
    }
}

struct AlarmFact: View {
    let title: String
    let value: String
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Circle()
                    .fill(tint)
                    .frame(width: 7, height: 7)

                Text(title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }

            Text(value)
                .font(.headline)
                .lineLimit(1)
                .minimumScaleFactor(0.78)
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct AlertStatePill: View {
    let state: AlertState

    var body: some View {
        Text(state.rawValue)
            .font(.caption2.weight(.semibold))
            .foregroundStyle(state.tint)
            .lineLimit(1)
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .background(state.tint.opacity(0.14), in: RoundedRectangle(cornerRadius: 4))
    }
}

struct EvidenceView: View {
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var evidenceExporter: EvidenceExporter
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @State private var selectedClipID: UUID?
    @State private var exportErrorMessage: String?
    @State private var showExportErrorAlert = false
    @State private var isLocking = false
    @State private var isVerifying = false
    @State private var approvalRequest: ApprovalRequest?

    private var operatorName: String {
        operatorSessionStore.currentOperator?.name ?? "Current Operator"
    }

    private var latestSegment: RecordingSegment? {
        mediaIngestStore.recordingSegments.first
    }

    private var selectedClip: EvidenceClip? {
        caseworkStore.evidenceClips.first { $0.id == selectedClipID } ?? caseworkStore.evidenceClips.first
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label("Evidence Locker", systemImage: "shippingbox.fill")
                        .font(.headline)

                    Spacer()

                    Button {
                        caseworkStore.createEvidencePackage(from: latestSegment)
                    } label: {
                        Label("New Package", systemImage: "plus.circle.fill")
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(latestSegment == nil)
                }

                SentinelPanel("Case Clips") {
                    ScrollView {
                        VStack(spacing: 8) {
                            if caseworkStore.evidenceClips.isEmpty {
                                EmptyStateLine(text: "No evidence packages have been created.")
                            } else {
                                ForEach(caseworkStore.evidenceClips) { clip in
                                    EvidenceClipRow(
                                        clip: clip,
                                        isSelected: selectedClip?.id == clip.id,
                                        select: { selectedClipID = clip.id }
                                    )
                                }
                            }
                        }
                    }
                }

                SentinelPanel("Chain Of Custody", systemImage: "checkmark.shield.fill") {
                    LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                        EmptyStateLine(text: "\(caseworkStore.evidenceClips.filter { $0.status == "Locked" }.count) locked packages")
                        EmptyStateLine(text: "\(caseworkStore.evidenceClips.filter { $0.status == "Draft" }.count) drafts")
                    }
                }

                Spacer()
            }
            .padding(14)

            Divider()
                .overlay(SentinelTheme.line)

            VStack(alignment: .leading, spacing: 14) {
                SentinelPanel("Export Builder", systemImage: "square.and.arrow.up") {
                    VStack(alignment: .leading, spacing: 10) {
                        DetailRow(label: "Package", value: selectedClip?.title ?? "None")
                        DetailRow(label: "Camera", value: selectedClip?.camera ?? "None")
                        DetailRow(label: "Range", value: selectedClip?.range ?? "None")
                        DetailRow(label: "Status", value: selectedClip?.status ?? "None")
                        DetailRow(label: "Selected", value: selectedClip?.caseID ?? "None")
                        DetailRow(label: "SHA-256", value: selectedClip?.sha256Hash.map { String($0.prefix(16)) + "…" } ?? (selectedClip == nil ? "None" : "Hashing…"))
                        DetailRow(label: "Retention", value: selectedClip.map { ($0.filePath.map(EvidenceVault.contains) ?? false) ? "Protected" : "Subject to cleanup" } ?? "None")
                        DetailRow(label: "Integrity", value: integrityLabel(for: selectedClip))
                    }
                }

                SentinelPanel("Actions", systemImage: "bolt.fill") {
                    VStack(spacing: 8) {
                        Button {
                            guard let selectedClip else { return }
                            isLocking = true
                            Task {
                                if let error = await caseworkStore.lockClip(selectedClip, operator: operatorName) {
                                    exportErrorMessage = error
                                    showExportErrorAlert = true
                                } else {
                                    workflowStore.recordAudit(area: "Evidence", action: "Locked evidence", detail: selectedClip.caseID, user: operatorName)
                                }
                                isLocking = false
                            }
                        } label: {
                            Label(isLocking ? "Locking…" : "Lock Clip", systemImage: "lock.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.bordered)
                        .disabled(selectedClip == nil || selectedClip?.isLocked == true || isLocking)
                        .help("Protect this footage from retention cleanup and pin its SHA-256 hash")

                        if selectedClip?.isLocked == true {
                            Button {
                                guard let clip = selectedClip else { return }
                                approvalRequest = ApprovalRequest(
                                    action: .unlockEvidence,
                                    detail: "\(clip.caseID) · \(clip.camera) · \(clip.range)"
                                ) { approver, reason in
                                    caseworkStore.unlockClip(clip)
                                    workflowStore.recordAudit(
                                        area: "Evidence",
                                        action: "Unlocked evidence",
                                        detail: "\(clip.caseID) — approved by \(approver). Reason: \(reason)",
                                        user: operatorName
                                    )
                                }
                            } label: {
                                Label("Unlock Clip…", systemImage: "lock.open.fill")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .buttonStyle(.bordered)
                            .help("Requires a supervisor's approval and a reason. The footage stays preserved.")
                        }

                        Button {
                            guard let selectedClip else { return }
                            isVerifying = true
                            Task {
                                let ok = await caseworkStore.verifyIntegrity(of: selectedClip)
                                workflowStore.recordAudit(area: "Evidence", action: ok ? "Integrity verified" : "Integrity check FAILED", detail: selectedClip.caseID, user: operatorName)
                                isVerifying = false
                            }
                        } label: {
                            Label(isVerifying ? "Verifying…" : "Verify Integrity", systemImage: "checkmark.seal")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.bordered)
                        .disabled(selectedClip?.sha256Hash == nil || isVerifying)
                        .help("Re-hash the file and compare it with the pinned SHA-256")

                        Button {
                            if let selectedClip {
                                caseworkStore.addReviewNote(to: selectedClip)
                            }
                        } label: {
                            Label("Add Review Note", systemImage: "note.text")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.bordered)
                        .disabled(selectedClip == nil)

                        Button {
                            if let selectedClip {
                                caseworkStore.exportClip(selectedClip)
                            }
                        } label: {
                            Label("Mark Exported", systemImage: "shippingbox.and.arrow.backward.fill")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(selectedClip == nil)

                        HStack(spacing: 8) {
                            Button {
                                if let selectedClip {
                                    beginPackageExport(for: selectedClip)
                                }
                            } label: {
                                Label("Export Package...", systemImage: "archivebox.fill")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .buttonStyle(.borderedProminent)
                            .disabled(
                                selectedClip == nil ||
                                evidenceExporter.isExporting ||
                                (selectedClip?.filePath.map { FileManager.default.fileExists(atPath: $0) } ?? false) == false
                            )

                            if evidenceExporter.isExporting {
                                ProgressView(value: evidenceExporter.progress)
                                    .progressViewStyle(.linear)
                                    .frame(width: 80)
                                    .help("Exporting evidence package...")
                            }
                        }
                    }
                }

                Spacer()
            }
            .frame(width: 330)
            .padding(14)
            .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.background)
        .onAppear {
            selectedClipID = selectedClip?.id
        }
        .onChange(of: evidenceExporter.lastResult) { newValue in
            if case .failure(let message) = newValue {
                exportErrorMessage = message
                showExportErrorAlert = true
            }
        }
        .approvalSheet($approvalRequest)
        .alert("Export Failed", isPresented: $showExportErrorAlert, presenting: exportErrorMessage) { _ in
            Button("OK", role: .cancel) { }
        } message: { msg in
            Text(msg)
        }
    }

    private func integrityLabel(for clip: EvidenceClip?) -> String {
        guard let clip else { return "None" }
        guard let ok = clip.integrityOK, let at = clip.integrityVerifiedAt else { return "Not verified" }
        let when = RecordingFormatters.timeFormatter.string(from: at)
        return ok ? "Intact · \(when)" : "MISMATCH · \(when)"
    }

    private func beginPackageExport(for clip: EvidenceClip) {
        guard let filePath = clip.filePath else { return }
        let segmentURL = URL(fileURLWithPath: filePath)
        guard FileManager.default.fileExists(atPath: segmentURL.path) else {
            exportErrorMessage = "The source recording file is missing."
            showExportErrorAlert = true
            return
        }

        let timestamp: String = {
            let f = DateFormatter()
            f.dateFormat = "yyyyMMdd-HHmmss"
            return f.string(from: Date())
        }()

        let safeCamera = clip.camera
            .replacingOccurrences(of: "/", with: "-")
            .replacingOccurrences(of: " ", with: "_")

        let panel = NSSavePanel()
        panel.nameFieldStringValue = "\(clip.caseID)-package"
        panel.title = "Export Evidence Package"
        panel.message = "Choose where to save the .sentinelevidence bundle."
        panel.canCreateDirectories = true

        panel.begin { response in
            guard response == .OK, let chosen = panel.url else { return }
            // Force the .sentinelevidence directory name.
            let bundleName = "\(clip.caseID)-\(safeCamera)-\(timestamp).sentinelevidence"
            let destination = chosen.deletingLastPathComponent().appendingPathComponent(bundleName, isDirectory: true)

            let cameraEntry = cameraStore.cameras.first { $0.name == clip.camera }
            let cameraIP = cameraEntry?.ipAddress ?? ""
            let cameraName = cameraEntry?.name ?? clip.camera
            let operatorName = operatorSessionStore.currentOperator?.name ?? "Current Operator"
            let auditEntries = workflowStore.auditLog

            Task { @MainActor in
                await evidenceExporter.export(
                    clip: clip,
                    segmentURL: segmentURL,
                    to: destination,
                    operatorName: operatorName,
                    auditLog: auditEntries,
                    cameraName: cameraName,
                    cameraIPAddress: cameraIP
                )
            }
        }
    }
}

struct EvidenceClipRow: View {
    let clip: EvidenceClip
    let isSelected: Bool
    let select: () -> Void

    var body: some View {
        Button(action: select) {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(clip.title)
                        .font(.headline)

                    Text("\(clip.caseID) · \(clip.camera) · \(clip.range)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                Spacer()

                if let filePath = clip.filePath, FileManager.default.fileExists(atPath: filePath) {
                    Button {
                        exportClip(filePath: filePath, clip: clip)
                    } label: {
                        Image(systemName: "square.and.arrow.up")
                            .font(.system(size: 12, weight: .semibold))
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .help("Export recording file")
                }

                Text(clip.status)
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 8)
                    .padding(.vertical, 5)
                    .foregroundStyle(clip.isLocked ? .green : SentinelTheme.accent)
                    .background((clip.isLocked ? Color.green : SentinelTheme.accent).opacity(0.16), in: Capsule())
            }
            .padding(12)
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.16) : SentinelTheme.panelRaised,
                in: RoundedRectangle(cornerRadius: 8)
            )
        }
        .buttonStyle(.plain)
    }

    private func exportClip(filePath: String, clip: EvidenceClip) {
        let sourceURL = URL(fileURLWithPath: filePath)
        let panel = NSSavePanel()
        panel.nameFieldStringValue = sourceURL.lastPathComponent
        panel.allowedContentTypes = [.mpeg4Movie, .movie]
        panel.begin { response in
            guard response == .OK, let dest = panel.url else { return }
            do {
                if FileManager.default.fileExists(atPath: dest.path) {
                    try FileManager.default.removeItem(at: dest)
                }
                try FileManager.default.copyItem(at: sourceURL, to: dest)
            } catch {
                // Export failed silently; user can retry
            }
        }
    }
}

struct MapsView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var floorplanStore: FloorplanStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    @EnvironmentObject private var caseworkStore: CaseworkStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @AppStorage("handoffgrid.maps.followAlarms") private var followAlarms = true
    @State private var showCameras = true
    @State private var isImportingFloorplan = false
    @State private var selectedPinID: UUID?

    private var pins: [RuntimeMapPin] {
        RuntimeMapPin.pins(for: cameraStore.cameras, storedPositions: floorplanStore.pinPositions, alerts: caseworkStore.activeAlerts)
    }

    private var selectedPin: RuntimeMapPin? {
        pins.first { $0.id == selectedPinID } ?? pins.first
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 14) {
                HStack {
                    Label("Floorplan", systemImage: "map.fill")
                        .font(.headline)

                    Spacer()

                    Button {
                        isImportingFloorplan = true
                    } label: {
                        Label("Floorplan", systemImage: "doc.badge.plus")
                    }
                }

                FloorplanView(
                    floorplanURL: floorplanStore.importedFloorplanURL,
                    pins: pins,
                    showCameras: showCameras,
                    selectedPinID: $selectedPinID,
                    onPinMoved: { id, x, y in floorplanStore.updatePinPosition(for: id, x: x, y: y) }
                )

                Spacer()
            }
            .padding(14)

            Divider()
                .overlay(SentinelTheme.line)

            VStack(alignment: .leading, spacing: 14) {
                SentinelPanel("Map Layers", systemImage: "square.3.layers.3d") {
                    VStack(alignment: .leading, spacing: 10) {
                        Toggle("Cameras", isOn: $showCameras)
                        Toggle("Follow alarms", isOn: $followAlarms)
                            .help("Select the camera when a new alarm arrives")
                    }
                    .toggleStyle(.checkbox)
                }

                SentinelPanel("Camera Pins", systemImage: "mappin.and.ellipse") {
                    VStack(spacing: 8) {
                        if pins.isEmpty {
                            EmptyStateLine(text: "Add cameras to place them on the floorplan.")
                        } else {
                            ForEach(pins) { pin in
                                Button {
                                    selectedPinID = pin.id
                                } label: {
                                    HStack {
                                        Circle()
                                            .fill(pin.status.tint)
                                            .frame(width: 8, height: 8)

                                        Text(pin.camera.name)
                                            .font(.caption.weight(.semibold))

                                        Spacer()

                                        if let alert = pin.alert {
                                            Image(systemName: alert.kind.symbol)
                                                .font(.caption)
                                                .foregroundStyle(alert.severity.tint)
                                                .help(alert.title)
                                        }
                                    }
                                    .padding(8)
                                    .background(
                                        selectedPinID == pin.id ? SentinelTheme.accent.opacity(0.16) : .clear,
                                        in: RoundedRectangle(cornerRadius: 8)
                                    )
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }

                if let selectedPin {
                    SentinelPanel("Selected Camera", systemImage: "video.circle.fill") {
                        VStack(alignment: .leading, spacing: 10) {
                            // Live feed of the pinned camera, right on the map.
                            ZStack {
                                RoundedRectangle(cornerRadius: 8).fill(.black)
                                if mediaMTXStore.isRunning, selectedPin.camera.rtspURL.isEmpty == false {
                                    AVPlayerRTSPSurface(
                                        rtspURL: mediaMTXStore.hlsURL(for: selectedPin.camera.id),
                                        gravity: .resizeAspectFill
                                    )
                                    .clipShape(RoundedRectangle(cornerRadius: 8))
                                } else {
                                    VStack(spacing: 6) {
                                        Image(systemName: "video.slash.fill").foregroundStyle(.secondary)
                                        Text("Preview unavailable")
                                            .font(.caption2).foregroundStyle(.secondary)
                                    }
                                }
                                // Status chip overlay
                                VStack {
                                    HStack {
                                        Spacer()
                                        HStack(spacing: 4) {
                                            Circle().fill(selectedPin.status.tint).frame(width: 6, height: 6)
                                            Text(selectedPin.status.label.uppercased())
                                                .font(.system(size: 8, weight: .bold))
                                        }
                                        .padding(.horizontal, 6).padding(.vertical, 3)
                                        .background(.black.opacity(0.55), in: Capsule())
                                        .foregroundStyle(.white)
                                        .padding(6)
                                    }
                                    Spacer()
                                }
                            }
                            .frame(height: 150)
                            .id(selectedPin.id)  // rebuild the player when the selected pin changes

                            DetailRow(label: "Camera", value: selectedPin.camera.name)
                            DetailRow(label: "Status", value: selectedPin.status.label)
                            DetailRow(label: "Coordinates", value: "\(Int(selectedPin.x * 100))%, \(Int(selectedPin.y * 100))%")

                            if let alert = selectedPin.alert {
                                VStack(alignment: .leading, spacing: 8) {
                                    Label(alert.title, systemImage: alert.kind.symbol)
                                        .font(.callout.weight(.semibold))
                                        .foregroundStyle(alert.severity.tint)
                                    Text("\(alert.alertState.rawValue) · \(alert.lastEventLabel)\(alert.eventCount > 1 ? " · \(alert.eventCount) events" : "")")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                    HStack {
                                        if alert.alertState == .new {
                                            Button("Acknowledge") { caseworkStore.acknowledgeAlert(alert) }
                                        }
                                        Button("Open Alarm") { commandCenter.requestOpen(.alerts) }
                                            .buttonStyle(.borderedProminent)
                                    }
                                }
                                .padding(10)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(alert.severity.tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
                            }
                        }
                    }
                }

                Spacer()
            }
            .frame(width: 310)
            .padding(14)
            .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.background)
        .onAppear {
            selectedPinID = selectedPin?.id
        }
        .onChange(of: caseworkStore.activeAlerts.filter { $0.alertState == .new }.map(\.id)) { newIDs in
            guard followAlarms,
                  let newest = caseworkStore.activeAlerts
                    .filter({ newIDs.contains($0.id) && $0.cameraID != nil })
                    .max(by: { $0.lastEventAt < $1.lastEventAt }) else { return }
            selectedPinID = newest.cameraID
        }
        .fileImporter(
            isPresented: $isImportingFloorplan,
            allowedContentTypes: [.image, .pdf],
            allowsMultipleSelection: false
        ) { result in
            if case .success(let urls) = result {
                if let url = urls.first {
                    floorplanStore.importFloorplan(from: url)
                }
            }
        }
    }
}

struct FloorplanView: View {
    let floorplanURL: URL?
    let pins: [RuntimeMapPin]
    let showCameras: Bool
    @Binding var selectedPinID: UUID?
    var onPinMoved: ((UUID, Double, Double) -> Void)? = nil

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .topLeading) {
                RoundedRectangle(cornerRadius: 8)
                    .fill(SentinelTheme.well)

                // Blueprint-style dotted grid so the canvas reads as a map even
                // before a floorplan is imported.
                MapGridCanvas()
                    .clipShape(RoundedRectangle(cornerRadius: 8))

                if let floorplanURL,
                   let image = NSImage(contentsOf: floorplanURL) {
                    Image(nsImage: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .padding(18)
                } else {
                    VStack(spacing: 14) {
                        ZStack {
                            Circle()
                                .fill(SentinelTheme.accent.opacity(0.12))
                                .frame(width: 76, height: 76)
                            Image(systemName: "map.fill")
                                .font(.system(size: 32, weight: .semibold))
                                .foregroundStyle(SentinelTheme.accent)
                        }
                        VStack(spacing: 5) {
                            Text("Add a floorplan")
                                .font(.title3.weight(.bold))
                            Text("Import a PNG, JPG, or PDF, then drag each camera\nonto its location to build your site map.")
                                .font(.callout)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.center)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .padding(36)
                    .background(
                        RoundedRectangle(cornerRadius: 14)
                            .fill(SentinelTheme.panel.opacity(0.6))
                            .overlay(
                                RoundedRectangle(cornerRadius: 14)
                                    .strokeBorder(style: StrokeStyle(lineWidth: 1.5, dash: [7, 5]))
                                    .foregroundStyle(SentinelTheme.line)
                            )
                    )
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }

                if showCameras {
                    ForEach(pins) { pin in
                        RuntimeMapCameraPin(
                            pin: pin,
                            isSelected: selectedPinID == pin.id
                        )
                        .onTapGesture { selectedPinID = pin.id }
                        .gesture(
                            DragGesture()
                                .onEnded { value in
                                    let newX = min(max(value.location.x / proxy.size.width, 0.02), 0.98)
                                    let newY = min(max(value.location.y / proxy.size.height, 0.02), 0.98)
                                    onPinMoved?(pin.id, newX, newY)
                                }
                        )
                        .position(
                            x: proxy.size.width * pin.x,
                            y: proxy.size.height * pin.y
                        )
                    }
                }
            }
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(SentinelTheme.line, lineWidth: 1)
            }
        }
        .frame(minHeight: 520)
    }
}

struct RuntimeMapPin: Identifiable, Hashable {
    let id: UUID
    let camera: CameraFeed
    let x: Double
    let y: Double
    let status: CameraStatus
    /// The most urgent open alarm on this camera (critical first, then newest).
    var alert: AlertEvent?

    static func pins(for cameras: [CameraFeed], storedPositions: [UUID: CGPoint] = [:], alerts: [AlertEvent] = []) -> [RuntimeMapPin] {
        var worstByCamera: [UUID: AlertEvent] = [:]
        for alert in alerts where alert.isOpen && alert.isSnoozedNow == false {
            guard let cameraID = alert.cameraID else { continue }
            if let existing = worstByCamera[cameraID],
               (existing.severity.rank, -existing.lastEventAt.timeIntervalSince1970) <= (alert.severity.rank, -alert.lastEventAt.timeIntervalSince1970) {
                continue
            }
            worstByCamera[cameraID] = alert
        }
        return cameras.enumerated().map { index, camera in
            let column = index % 4
            let row = index / 4
            let defaultX = 0.18 + Double(column) * 0.21
            let defaultY = min(0.20 + Double(row) * 0.18, 0.86)
            let stored = storedPositions[camera.id]
            return RuntimeMapPin(
                id: camera.id,
                camera: camera,
                x: stored.map { Double($0.x) } ?? defaultX,
                y: stored.map { Double($0.y) } ?? defaultY,
                status: camera.status,
                alert: worstByCamera[camera.id]
            )
        }
    }
}

struct RuntimeMapCameraPin: View {
    let pin: RuntimeMapPin
    let isSelected: Bool
    @State private var pulse = false

    private var isAlerting: Bool { pin.status == .offline || pin.alert != nil }

    /// Alarmed pins take the alarm's severity color; otherwise camera status.
    private var tint: Color { pin.alert?.severity.tint ?? tint }

    var body: some View {
        VStack(spacing: 4) {
            ZStack {
                // Soft status halo (pulses when the camera is offline).
                Circle()
                    .fill(tint.opacity(isAlerting && pulse ? 0.05 : 0.20))
                    .frame(width: isSelected ? 42 : 36, height: isSelected ? 42 : 36)
                    .scaleEffect(isAlerting && pulse ? 1.35 : 1)

                Circle()
                    .fill(Color.black.opacity(0.55))
                    .frame(width: 28, height: 28)

                Image(systemName: "video.fill")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(tint)

                Circle()
                    .stroke(isSelected ? SentinelTheme.accent : tint,
                            lineWidth: isSelected ? 3 : 2)
                    .frame(width: 28, height: 28)

                if let alert = pin.alert {
                    Image(systemName: alert.kind.symbol)
                        .font(.system(size: 8, weight: .bold))
                        .foregroundStyle(.white)
                        .padding(3)
                        .background(tint, in: Circle())
                        .offset(x: 12, y: -12)
                }
            }
            .shadow(color: .black.opacity(0.45), radius: 3, y: 1)

            // Always-visible name label so the map is readable at a glance.
            Text(pin.camera.name)
                .font(.system(size: 9, weight: .bold))
                .lineLimit(1)
                .foregroundStyle(.white)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(Color.black.opacity(0.62), in: Capsule())
                .overlay(Capsule().stroke(isSelected ? SentinelTheme.accent : .clear, lineWidth: 1))
                .fixedSize()
        }
        .help(pin.alert.map { "\(pin.camera.name) — \($0.title) (\($0.alertState.rawValue))" } ?? "\(pin.camera.name) — \(pin.status.label)")
        .onAppear { updatePulse() }
        .onChange(of: isAlerting) { _ in updatePulse() }
    }

    private func updatePulse() {
        if isAlerting {
            withAnimation(.easeInOut(duration: 1.0).repeatForever(autoreverses: true)) { pulse = true }
        } else {
            withAnimation(.default) { pulse = false }
        }
    }
}

/// Subtle dotted grid drawn behind the floorplan so the map canvas reads as an
/// intentional surface (blueprint feel) rather than a blank panel.
struct MapGridCanvas: View {
    var body: some View {
        Canvas { ctx, size in
            let spacing: CGFloat = 26
            let dot: CGFloat = 1.2
            var y = spacing
            while y < size.height {
                var x = spacing
                while x < size.width {
                    ctx.fill(
                        Path(ellipseIn: CGRect(x: x - dot / 2, y: y - dot / 2, width: dot, height: dot)),
                        with: .color(.gray.opacity(0.22))
                    )
                    x += spacing
                }
                y += spacing
            }
        }
        .allowsHitTesting(false)
    }
}

struct AccessControlMarker: View {
    let label: String

    var body: some View {
        Label(label, systemImage: "lock.shield.fill")
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .foregroundStyle(.green)
            .background(.green.opacity(0.16), in: Capsule())
    }
}

struct FloorplanRooms: View {
    var body: some View {
        VStack(spacing: 10) {
            HStack(spacing: 10) {
                FloorplanRoom("Lobby")
                FloorplanRoom("Admin")
                FloorplanRoom("IT")
            }

            HStack(spacing: 10) {
                FloorplanRoom("Warehouse")
                    .frame(maxWidth: .infinity)
                FloorplanRoom("Loading")
            }

            HStack(spacing: 10) {
                FloorplanRoom("Exterior")
                FloorplanRoom("Mechanical")
            }
        }
    }
}

struct FloorplanRoom: View {
    let title: String

    init(_ title: String) {
        self.title = title
    }

    var body: some View {
        ZStack(alignment: .topLeading) {
            RoundedRectangle(cornerRadius: 8)
                .fill(SentinelTheme.panelRaised)
                .overlay {
                    RoundedRectangle(cornerRadius: 8)
                        .stroke(.white.opacity(0.08), lineWidth: 1)
                }

            Text(title)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .padding(10)
        }
        .frame(minHeight: 130)
    }
}

struct MapCameraPin: View {
    let pin: CameraPin
    let showAlert: Bool
    let isSelected: Bool

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Image(systemName: "video.circle.fill")
                .font(.title2)
                .foregroundStyle(pin.status.tint)
                .background(.black.opacity(0.50), in: Circle())
                .overlay {
                    Circle()
                        .stroke(isSelected ? SentinelTheme.accent : .clear, lineWidth: 3)
                        .frame(width: 34, height: 34)
                }

            if showAlert {
                Circle()
                    .fill(.red)
                    .frame(width: 8, height: 8)
                    .offset(x: 2, y: -2)
            }
        }
        .help(pin.cameraName)
    }
}

/// Small capsule shown on detection events Claude flagged as elevated/high
/// threat — amber for "unusual", red for "suspicious". Reason on hover.
struct ThreatBadge: View {
    let threat: ThreatLevel
    let reason: String?

    private var isHigh: Bool { threat >= .high }
    private var tint: Color {
        isHigh ? Color(red: 1.0, green: 0.28, blue: 0.22) : Color(red: 0.98, green: 0.62, blue: 0.10)
    }

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: isHigh ? "exclamationmark.triangle.fill" : "exclamationmark.circle.fill")
                .font(.system(size: 8, weight: .bold))
            Text(isHigh ? "SUSPICIOUS" : "UNUSUAL")
                .font(.system(size: 8, weight: .heavy))
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 5)
        .padding(.vertical, 1.5)
        .background(tint, in: Capsule())
        .help(reason.map { "AI: \($0)" } ?? "Flagged by AI as worth attention")
    }
}
