import AppKit
import SwiftUI
import SentinelCore

// Appearance search ("Find similar", wave 11 stream C). The button goes on an
// event card (search results, the alarm inspector's events); it opens the
// panel as a sheet over the results. The panel's state and every rule it
// follows live in `SimilarModel` / `SimilarLogic` (SentinelCore, tested).

/// The "Find similar" corner button for an event card. Renders nothing for
/// events without an appearance crop (no box, no snapshot, motion, audio).
/// Owns its sheet, so a host adds it with one `.overlay` line.
struct FindSimilarButton: View {
    let event: V2.Event
    @State private var showing = false

    var body: some View {
        if SimilarLogic.canFindSimilar(event) {
            Button("Find similar") { showing = true }
                .controlSize(.small)
                .buttonStyle(.bordered)
                .help("Find this person or vehicle elsewhere")
                .padding(6)
                .sheet(isPresented: $showing) {
                    SimilarPanel(query: event)
                }
        }
    }
}

/// Matches for one event: the query snapshot, the controls (window, this
/// camera only, any kind, minimum similarity), the index job, and a grid of
/// matches best first. Clicking a match plays its recording, like a search
/// result does. Requires the usual site environment objects.
struct SimilarPanel: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let query: V2.Event

    var body: some View {
        SimilarPanelBody(model: SimilarModel(query: query, backend: .init(api: runtime.api)))
    }
}

private struct SimilarPanelBody: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: SimilarModel
    @State private var onScreen = false

    init(model: @autoclosure @escaping () -> SimilarModel) {
        _model = StateObject(wrappedValue: model())
    }

    private var query: V2.Event { model.query }
    private var canIndex: Bool { session.can(.changeSettings) }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(SentinelTheme.line)
            if let banner = model.banner {
                unavailable(banner)
            } else {
                controls
                Divider().overlay(SentinelTheme.line)
                results
            }
        }
        .frame(minWidth: 760, idealWidth: 920, minHeight: 520, idealHeight: 680)
        .background(SentinelTheme.background)
        .trackOnScreen($onScreen)
        .task { await model.loadStatus() }
        // Any change to the controls searches again (the slider is debounced).
        .task(id: model.options) {
            try? await Task.sleep(nanoseconds: 250_000_000)
            guard Task.isCancelled == false else { return }
            await model.search()
        }
        // The one visible interval: the index job's progress while it runs.
        .task(id: model.isBackfillRunning && onScreen) {
            await visibleInterval(model.isBackfillRunning && onScreen, seconds: 2) { await model.loadStatus() }
        }
        .onChange(of: model.isBackfillRunning) { running in
            // Freshly indexed events can now match.
            if running == false { Task { await model.search() } }
        }
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: 10) {
            Image(systemName: "person.crop.rectangle.stack").foregroundStyle(SentinelTheme.accent)
            Text("Similar to this \((query.label?.isEmpty == false ? query.label! : query.kind).lowercased())")
                .font(.headline)
            Text("\(cameras.name(of: query.cameraId)) · \(OperatorFormat.dateTime.string(from: Date(unixMillis: query.createdAt)))")
                .font(.caption).foregroundStyle(.secondary)
            Spacer()
            Button("Done") { dismiss() }.keyboardShortcut(.cancelAction)
        }
        .padding(.horizontal, SentinelMetrics.gutter)
        .padding(.vertical, 10)
        .background(SentinelTheme.chrome)
    }

    // MARK: Unavailable

    private func unavailable(_ text: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle").font(.largeTitle).foregroundStyle(SentinelTheme.amber)
            Text(text).multilineTextAlignment(.center).frame(maxWidth: 520)
                .textSelection(.enabled)
            Button("Check Again") {
                Task {
                    await model.loadStatus()
                    if model.status?.available == true { await model.search() }
                }
            }
        }
        .padding(30)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: Controls

    private var controls: some View {
        HStack(alignment: .top, spacing: 14) {
            SimilarThumbnail(event: query)
                .frame(width: 200)
                .clipShape(RoundedRectangle(cornerRadius: SentinelMetrics.controlRadius))
                .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.controlRadius).stroke(SentinelTheme.accent, lineWidth: 2))
            VStack(alignment: .leading, spacing: 8) {
                Picker("Time window", selection: $model.options.window) {
                    ForEach(SimilarWindow.allCases) { Text($0.label).tag($0) }
                }
                .pickerStyle(.segmented).labelsHidden().frame(maxWidth: 480)
                HStack(spacing: 14) {
                    Toggle("This camera only", isOn: $model.options.sameCamera).toggleStyle(.checkbox)
                    Toggle("Any kind", isOn: $model.options.anyKind).toggleStyle(.checkbox)
                        .help("Compare against every event kind, not only \(query.kind)")
                    HStack(spacing: 6) {
                        Text("Minimum match").foregroundStyle(.secondary)
                        Slider(value: $model.options.minScore, in: 0...0.95, step: 0.05).frame(width: 140)
                        Text(model.options.minScore > 0 ? "\(SimilarLogic.scorePercent(model.options.minScore))%" : "Any")
                            .monospacedDigit().frame(width: 36, alignment: .leading)
                    }
                }
                Text(model.summary).font(.caption).foregroundStyle(.secondary)
                if model.status?.available == true { indexRow }
                if let error = model.error {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
            }
            Spacer(minLength: 0)
            Button { Task { await model.search() } } label: { Image(systemName: "arrow.clockwise") }
                .help("Search again")
        }
        .controlSize(.small)
        .padding(SentinelMetrics.gutter)
        .background(SentinelTheme.chrome)
    }

    private var indexRow: some View {
        HStack(spacing: 8) {
            if let progress = SimilarLogic.backfillProgress(model.backfill) {
                ProgressView(value: progress).frame(width: 120)
            }
            Text(model.indexLine).font(.caption).foregroundStyle(.secondary)
            if canIndex {
                if model.isBackfillRunning {
                    Button("Cancel") { Task { await model.cancelBackfill() } }.disabled(model.isBusy)
                } else {
                    Button("Index Recent Events") { Task { await model.startBackfill() } }
                        .disabled(model.isBusy)
                        .help("Index the newest 50,000 events that have no appearance data yet")
                }
                Button("Refresh") { Task { await model.loadStatus() } }.disabled(model.isBusy)
            }
        }
    }

    // MARK: Results

    @ViewBuilder
    private var results: some View {
        if let hits = model.hits, hits.isEmpty, model.isLoading == false, model.error == nil {
            VStack(spacing: 8) {
                Image(systemName: "person.crop.rectangle.stack").font(.largeTitle).foregroundStyle(.secondary)
                Text("No similar events in this window. Older events may not be indexed yet.").foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if model.hits == nil, model.isLoading {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollView {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 200, maximum: 300), spacing: 10)], spacing: 10) {
                    ForEach(model.hits ?? [], id: \.event.id) { hit in
                        SimilarHitCard(hit: hit, cameraName: cameras.name(of: hit.event.cameraId)) { open(hit.event) }
                    }
                }
                .padding(SentinelMetrics.gutter)
            }
        }
    }

    /// What clicking a search result does: playback from just before the event.
    private func open(_ event: V2.Event) {
        dismiss()
        navigator.openPlayback(cameraIDs: [event.cameraId], at: SearchFilter.playbackStart(of: event))
    }
}

/// One match: snapshot, kind, score as a percentage, camera and time.
private struct SimilarHitCard: View {
    let hit: V2.SimilarEventHit
    let cameraName: String
    let play: () -> Void

    private var event: V2.Event { hit.event }

    private var toneColor: Color {
        switch SimilarLogic.tone(hit.score) {
        case .strong: return SentinelTheme.recording
        case .likely: return SentinelTheme.amber
        case .weak: return Color.secondary
        }
    }

    var body: some View {
        Button(action: play) {
            VStack(alignment: .leading, spacing: 0) {
                SimilarThumbnail(event: event)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text(event.label?.isEmpty == false ? event.label!.capitalized : event.kind)
                            .font(.callout.weight(.semibold)).lineLimit(1)
                        Spacer()
                        Text("\(SimilarLogic.scorePercent(hit.score))% match")
                            .font(.caption2.weight(.bold))
                            .padding(.horizontal, 7).padding(.vertical, 3)
                            .foregroundStyle(SimilarLogic.tone(hit.score) == .weak ? Color.primary : Color.black)
                            .background(toneColor.opacity(SimilarLogic.tone(hit.score) == .weak ? 0.18 : 0.9), in: Capsule())
                            .help("Appearance similarity")
                    }
                    Text("\(cameraName) · \(OperatorFormat.dateTime.string(from: Date(unixMillis: event.createdAt)))")
                        .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    if let description = event.description, description.isEmpty == false {
                        Text(description).font(.caption).lineLimit(2)
                    }
                }
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(SentinelTheme.panelRaised)
            .clipShape(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
            .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help("Play the recording from just before this event")
    }
}

/// An event's snapshot at 16:9 (placeholder while it loads or when there is none).
private struct SimilarThumbnail: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let event: V2.Event
    @State private var image: NSImage?

    var body: some View {
        ZStack {
            Rectangle().fill(Color.black.opacity(0.85))
            if let image {
                Image(nsImage: image).resizable().aspectRatio(contentMode: .fit)
            } else {
                Image(systemName: event.hasThumbnail ? "photo" : "bolt.fill").foregroundStyle(.secondary)
            }
        }
        .aspectRatio(16.0 / 9.0, contentMode: .fit)
        .clipped()
        .task(id: event.id) {
            guard event.hasThumbnail else { return }
            image = await runtime.thumbnails.image(for: event.id)
        }
    }
}
