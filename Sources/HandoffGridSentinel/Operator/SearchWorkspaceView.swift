import AppKit
import SwiftUI
import SentinelCore

/// Forensic search over detection events: time range, cameras, kind and label
/// chips with facet counts, text, threat and confidence filters; a thumbnail
/// grid that pages into the past on a keyset cursor; click a result to open
/// playback at that moment. When the server says AI is on, a question field
/// turns plain English into the filter bar (and says what it understood).
///
/// Wiring (coordinator, at merge):
/// `SearchWorkspaceView(onPlayback: { cameraID, start in navigator.openPlayback(cameraIDs: [cameraID], at: start) })`.
/// `onPlayback` runs on the main actor with the event's camera id and the
/// time playback should start (the event's timestamp minus a 5 s lead,
/// `SearchFilter.playbackLead`). Requires the usual site environment objects
/// (`SiteRuntime`, `CameraDirectory`).
struct SearchWorkspaceView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    /// Called with the camera id and the start time when a result is clicked.
    var onPlayback: (_ cameraID: String, _ start: Date) -> Void

    init(onPlayback: @escaping (_ cameraID: String, _ start: Date) -> Void = { _, _ in }) {
        self.onPlayback = onPlayback
    }

    var body: some View {
        // The model needs the runtime's API, so it is built once the environment exists.
        SearchWorkspaceBody(model: SearchModel(backend: .init(api: runtime.api)), onPlayback: onPlayback)
    }
}

private struct SearchWorkspaceBody: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @StateObject private var model: SearchModel
    let onPlayback: (String, Date) -> Void

    init(model: @autoclosure @escaping () -> SearchModel, onPlayback: @escaping (String, Date) -> Void) {
        _model = StateObject(wrappedValue: model())
        self.onPlayback = onPlayback
    }

    var body: some View {
        VStack(spacing: 0) {
            filterBar
            Divider().overlay(SentinelTheme.line)
            results
        }
        // Any change to the bar searches again (typing is debounced).
        .task(id: model.filter) {
            try? await Task.sleep(nanoseconds: 250_000_000)
            guard Task.isCancelled == false else { return }
            await model.search()
        }
        .task(id: model.filter.facetKey) { await model.loadFacets() }
    }

    // MARK: Filter bar

    private var filterBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            if model.askAvailable { askRow }
            if let understood = model.understood {
                Text("Understood \u{201C}\(understood)\u{201D} as the filters below. Adjust them to refine.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            rangeRow
            HStack(spacing: 8) {
                cameraMenu
                TextField("Label, description or tag", text: $model.filter.q)
                    .textFieldStyle(.roundedBorder).frame(maxWidth: 240)
                Toggle(isOn: Binding(get: { model.filter.threatOnly },
                                     set: { model.filter.threatOnly = $0; model.filter.threats = [] })) {
                    Text("Elevated or high threat" + (model.facets == nil ? "" : " (\(model.threatTotal))"))
                }
                .toggleStyle(.button)
                scoreMenu
                if model.filter.activeCount > 0 {
                    Button("Clear Filters") { model.clearFilters() }
                }
                Spacer()
                Text(resultCount).font(.caption).foregroundStyle(.secondary)
                Button { Task { await model.search(force: true); await model.loadFacets(force: true) } } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .help("Search again")
            }
            chips
        }
        .controlSize(.small)
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(SentinelTheme.chrome)
    }

    private var askRow: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles").foregroundStyle(SentinelTheme.accent)
                TextField("Ask in plain English: a person in a red jacket at the front door last night", text: $model.question)
                    .textFieldStyle(.roundedBorder)
                    .disabled(model.askLimitMessage != nil)
                    .onSubmit { Task { await model.ask() } }
                Button(model.isAsking ? "Thinking…" : "Ask") { Task { await model.ask() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.isAsking || model.question.trimmingCharacters(in: .whitespaces).isEmpty || model.askLimitMessage != nil)
            }
            if let failure = model.askFailure {
                Text(model.askLimitMessage != nil ? "\(failure.message) Searches by filter still work." : failure.message)
                    .font(.caption).foregroundStyle(.orange)
            }
        }
    }

    private var rangeRow: some View {
        HStack(spacing: 8) {
            Picker("Time range", selection: Binding(get: { model.filter.range }, set: setRange)) {
                ForEach(SearchRange.allCases) { Text($0.label).tag($0) }
            }
            .pickerStyle(.segmented).labelsHidden().frame(maxWidth: 520)
            if model.filter.range == .custom {
                DatePicker("From", selection: dateBinding(\.from, fallback: Date().addingTimeInterval(-86_400)),
                           displayedComponents: [.date, .hourAndMinute])
                Toggle("To", isOn: Binding(get: { model.filter.to > 0 },
                                           set: { model.filter.to = $0 ? Date().unixMillis : 0 }))
                    .toggleStyle(.checkbox)
                if model.filter.to > 0 {
                    DatePicker("To", selection: dateBinding(\.to, fallback: Date()), displayedComponents: [.date, .hourAndMinute])
                        .labelsHidden()
                }
            }
            Spacer()
        }
    }

    private func setRange(_ range: SearchRange) {
        if range == .custom, model.filter.from == 0 {
            model.filter.from = Date().addingTimeInterval(-86_400).unixMillis
        }
        model.filter.range = range
    }

    private func dateBinding(_ keyPath: WritableKeyPath<SearchFilter, UnixMillis>, fallback: Date) -> Binding<Date> {
        Binding(get: { model.filter[keyPath: keyPath] > 0 ? Date(unixMillis: model.filter[keyPath: keyPath]) : fallback },
                set: { model.filter[keyPath: keyPath] = $0.unixMillis })
    }

    private var cameraMenu: some View {
        Menu {
            ForEach(cameras.cameras) { camera in
                Toggle(camera.name, isOn: Binding(
                    get: { model.filter.cameras.contains(camera.id) },
                    set: { _ in model.filter.cameras = SearchFilter.toggled(model.filter.cameras, camera.id) }))
            }
            if model.filter.cameras.isEmpty == false {
                Divider()
                Button("All Cameras") { model.filter.cameras = [] }
            }
        } label: {
            Label(model.filter.cameras.isEmpty ? "All Cameras"
                  : "\(model.filter.cameras.count) camera\(model.filter.cameras.count == 1 ? "" : "s")",
                  systemImage: "video")
        }
        .fixedSize()
    }

    private var scoreMenu: some View {
        Menu {
            Button("Any confidence") { model.filter.minScore = 0 }
            ForEach([0.5, 0.7, 0.9], id: \.self) { value in
                Button("At least \(Int(value * 100))%") { model.filter.minScore = value }
            }
        } label: {
            Text(model.filter.minScore > 0 ? "Confidence ≥ \(Int((model.filter.minScore * 100).rounded()))%" : "Any confidence")
        }
        .fixedSize()
    }

    private var chips: some View {
        FlowLayout(spacing: 6) {
            ForEach(model.kindChips, id: \.value) { chip in
                chipButton(chip.value, count: chip.count, on: chip.selected) {
                    model.filter.kinds = SearchFilter.toggled(model.filter.kinds, chip.value)
                }
            }
            ForEach(model.labelChips, id: \.value) { chip in
                chipButton(chip.value, count: chip.count, on: chip.selected) {
                    model.filter.labels = SearchFilter.toggled(model.filter.labels, chip.value)
                }
            }
            ForEach(model.filter.threats, id: \.self) { threat in
                chipButton("threat: \(threat) ×", count: nil, on: true) {
                    model.filter.threats.removeAll { $0 == threat }
                }
            }
            if let anomaly = model.filter.anomaly {
                chipButton(anomaly ? "anomalies only ×" : "no anomalies ×", count: nil, on: true) { model.filter.anomaly = nil }
            }
        }
    }

    private func chipButton(_ title: String, count: Int?, on: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 4) {
                Text(title)
                if let count { Text("\(count)").foregroundStyle(on ? Color.white.opacity(0.75) : Color.secondary) }
            }
            .font(.caption)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(on ? SentinelTheme.accent : Color.primary.opacity(0.10), in: Capsule())
            .foregroundStyle(on ? Color.white : Color.primary)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    private var resultCount: String {
        if model.isLoading, model.events.isEmpty { return "Searching…" }
        let n = model.events.count
        return "\(n)\(model.hasMore ? "+" : "") result\(n == 1 ? "" : "s")"
    }

    // MARK: Results

    @ViewBuilder
    private var results: some View {
        if let error = model.error, model.events.isEmpty {
            message("exclamationmark.triangle", error, color: .red)
        } else if model.hasSearched, model.events.isEmpty, model.isLoading == false {
            message("magnifyingglass", "No events match these filters.", color: .secondary)
        } else {
            ScrollView {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 220, maximum: 320), spacing: 10)], spacing: 10) {
                    ForEach(model.events) { event in
                        SearchResultCard(event: event, cameraName: cameras.name(of: event.cameraId)) {
                            onPlayback(event.cameraId, SearchFilter.playbackStart(of: event))
                        }
                        .onAppear {
                            if event.id == model.events.last?.id { Task { await model.loadMore() } }
                        }
                    }
                }
                .padding(14)
                if model.isLoading { ProgressView().controlSize(.small).padding() }
                if let error = model.error, model.events.isEmpty == false {
                    Text(error).font(.caption).foregroundStyle(.red).padding(.bottom, 10)
                }
            }
        }
    }

    private func message(_ symbol: String, _ text: String, color: Color) -> some View {
        VStack(spacing: 8) {
            Image(systemName: symbol).font(.largeTitle).foregroundStyle(.secondary)
            Text(text).foregroundStyle(color)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

private struct SearchResultCard: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let event: V2.Event
    let cameraName: String
    let play: () -> Void
    @State private var image: NSImage?

    var body: some View {
        Button(action: play) {
            VStack(alignment: .leading, spacing: 0) {
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
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text(event.label?.isEmpty == false ? event.label!.capitalized : event.kind)
                            .font(.callout.weight(.semibold)).lineLimit(1)
                        if let threat = event.threat, threat != "none" {
                            Text(threat).font(.caption2.weight(.bold))
                                .foregroundStyle(threat == "high" ? .red : SentinelTheme.amber)
                        }
                        if event.anomaly == true {
                            Text("anomaly").font(.caption2.weight(.bold)).foregroundStyle(SentinelTheme.amber)
                        }
                        Spacer()
                        if event.score > 0 { MetricPill(text: "\(Int((event.score * 100).rounded()))%") }
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
        .overlay(alignment: .topTrailing) { FindSimilarButton(event: event) } // WAVE11(C): Find similar
        .help("Play the recording from just before this event")
        .task(id: event.id) {
            guard event.hasThumbnail else { return }
            image = await runtime.thumbnails.image(for: event.id)
        }
    }
}

/// Left-to-right wrapping rows (macOS 13 `Layout`).
private struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        arrange(width: proposal.width ?? .infinity, subviews: subviews).size
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let layout = arrange(width: bounds.width, subviews: subviews)
        for (index, origin) in layout.origins.enumerated() {
            subviews[index].place(at: CGPoint(x: bounds.minX + origin.x, y: bounds.minY + origin.y), proposal: .unspecified)
        }
    }

    private func arrange(width: CGFloat, subviews: Subviews) -> (size: CGSize, origins: [CGPoint]) {
        var origins: [CGPoint] = []
        var x: CGFloat = 0, y: CGFloat = 0, rowHeight: CGFloat = 0, maxX: CGFloat = 0
        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x > 0, x + size.width > width {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            origins.append(CGPoint(x: x, y: y))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
            maxX = max(maxX, x - spacing)
        }
        return (CGSize(width: maxX, height: y + rowHeight), origins)
    }
}
