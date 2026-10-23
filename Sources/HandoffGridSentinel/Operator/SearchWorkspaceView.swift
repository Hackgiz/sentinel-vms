import AppKit
import SwiftUI
import SentinelCore

/// Forensic search over detection events: time range, cameras, kind and label
/// chips with facet counts, text, threat and confidence filters; a thumbnail
/// grid that pages into the past on a keyset cursor; click a result to open
/// playback at that moment. When the server says AI is on, a question field
/// turns plain English into the filter bar (and says what it understood).
/// Wave 17: "Describe it" searches snapshots by what they show (local semantic
/// search), a plate filter, person and plate chips on results, and "Same
/// object" lists one tracked object's detections.
///
/// Wiring (coordinator, at merge):
/// `SearchWorkspaceView(onPlayback: { cameraID, start in navigator.openPlayback(cameraIDs: [cameraID], at: start) })`.
/// `onPlayback` runs on the main actor with the event's camera id and the
/// time playback should start (the event's timestamp minus a 5 s lead,
/// `PlaybackJump.eventPreRoll`). Requires the usual site environment objects
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
    @EnvironmentObject private var session: OperatorSession
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
        .task { await model.loadSemanticStatus() }
    }

    // MARK: Filter bar

    private var filterBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            if model.askAvailable { askRow }
            describeRow
            if let understood = model.understood {
                Text("Understood \u{201C}\(understood)\u{201D} as the filters below. Adjust them to refine.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            rangeRow
            HStack(spacing: 8) {
                cameraMenu
                TextField("Label, description or tag", text: $model.filter.q)
                    .textFieldStyle(.roundedBorder).frame(maxWidth: 240)
                TextField("Plate", text: $model.filter.plate)
                    .textFieldStyle(.roundedBorder).frame(maxWidth: 100)
                    .help("Plate text; the start of a plate matches")
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
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.top, SentinelTheme.Space.xs)
        .padding(.bottom, SentinelTheme.Space.md)
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

    /// "Describe it" (wave 16 semantic search): disabled with the reason as help when unavailable.
    private var describeRow: some View {
        let availability = model.semanticAvailability
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Image(systemName: "text.viewfinder").foregroundStyle(availability.enabled ? SentinelTheme.accent : .secondary)
                TextField("Describe it: a white van in the driveway, a person with an umbrella", text: $model.describeText)
                    .textFieldStyle(.roundedBorder)
                    .disabled(availability.enabled == false)
                    .onSubmit { Task { await model.describe() } }
                    .onChange(of: model.describeText) { text in
                        if text.count > SemanticLogic.textMax { model.describeText = String(text.prefix(SemanticLogic.textMax)) }
                    }
                Button(model.isDescribing ? "Searching…" : "Describe it") { Task { await model.describe() } }
                    .disabled(availability.enabled == false || model.isDescribing
                              || model.describeText.trimmingCharacters(in: .whitespaces).count < SemanticLogic.textMin)
            }
            .help(availability.help)
            if let error = model.semanticError {
                Text(error).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
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
                chipButton(chip.value, count: chip.count, on: chip.selected, color: SentinelTheme.kindColor(chip.value)) {
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
            if model.filter.person.isEmpty == false {
                chipButton("person: \(model.personLabel) ×", count: nil, on: true) { model.filter.person = "" }
            }
            if model.filter.track.isEmpty == false {
                chipButton("same object ×", count: nil, on: true) { model.filter.track = "" }
            }
        }
    }

    /// A filter chip in the iPhone's style: tinted when off, filled when on.
    private func chipButton(_ title: String, count: Int?, on: Bool, color: Color = SentinelTheme.accent,
                            action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 4) {
                Text(title).fontWeight(.semibold)
                if let count { Text("\(count)").foregroundStyle(on ? Color.white.opacity(0.75) : color.opacity(0.7)) }
            }
            .font(.caption)
            .padding(.horizontal, 10).padding(.vertical, 4)
            .background(on ? color : color.opacity(0.16), in: Capsule())
            .foregroundStyle(on ? Color.white : color)
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
        if let hits = model.semanticHits {
            semanticResults(hits)
        } else {
            filteredResults
        }
    }

    private func semanticResults(_ hits: [V2.SemanticHit]) -> some View {
        let bars = model.semanticBars
        return VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 10) {
                Button { model.closeDescribe() } label: { Label("Back to filters", systemImage: "chevron.left") }
                Text("Snapshots that look like \u{201C}\(model.semanticQuery)\u{201D}").font(.callout.weight(.semibold))
                Text("\(hits.count) match\(hits.count == 1 ? "" : "es") · time range and cameras from the filter bar")
                    .font(.caption).foregroundStyle(.secondary)
                Spacer()
            }
            .controlSize(.small)
            .padding(.horizontal, 14).padding(.top, 10)
            if let status = model.semanticStatus, status.available { semanticIndexRow(status) }
            if hits.isEmpty {
                message("text.viewfinder", "Nothing looks like that in this range. Older events may not be indexed yet.", color: .secondary)
            } else {
                ScrollView {
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 240, maximum: 340), spacing: SentinelTheme.Space.md)], spacing: SentinelTheme.Space.md) {
                        ForEach(Array(hits.enumerated()), id: \.element.event.id) { index, hit in
                            resultCard(hit.event, score: (hit.score, index < bars.count ? bars[index] : 0))
                        }
                    }
                    .padding(SentinelTheme.Space.lg)
                }
            }
        }
    }

    private func semanticIndexRow(_ status: V2.SemanticStatus) -> some View {
        HStack(spacing: 8) {
            Text(status.backfill.map(SimilarLogic.backfillLabel) ?? "\((status.indexed ?? 0).formatted()) events indexed")
                .font(.caption).foregroundStyle(.secondary)
            if session.can(.changeSettings) {
                if status.backfill?.state == .running {
                    Button("Cancel") { Task { await model.indexSemantic(start: false) } }
                } else {
                    Button("Index Recent Events") { Task { await model.indexSemantic(start: true) } }
                        .help("Index the newest 50,000 events that have no semantic vector yet")
                }
                Button("Refresh") { Task { await model.loadSemanticStatus() } }
            }
        }
        .controlSize(.small)
        .padding(.horizontal, 14).padding(.top, 6)
    }

    private func resultCard(_ event: V2.Event, score: (value: Double, bar: Double)? = nil) -> some View {
        SearchResultCard(event: event, cameraName: cameras.name(of: event.cameraId), score: score,
                         sameObject: event.trackId?.isEmpty == false ? { model.showSameObject(event) } : nil) {
            onPlayback(event.cameraId, PlaybackJump.start(of: event))
        }
    }

    @ViewBuilder
    private var filteredResults: some View {
        if let error = model.error, model.events.isEmpty {
            message("exclamationmark.triangle", error, color: .red)
        } else if model.hasSearched, model.events.isEmpty, model.isLoading == false {
            message("magnifyingglass", "No events match these filters.", color: .secondary)
        } else {
            ScrollView {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 240, maximum: 340), spacing: SentinelTheme.Space.md)], spacing: SentinelTheme.Space.md) {
                    ForEach(model.events) { event in
                        resultCard(event)
                        .onAppear {
                            if event.id == model.events.last?.id { Task { await model.loadMore() } }
                        }
                    }
                }
                .padding(SentinelTheme.Space.lg)
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
    /// A "Describe it" match: the raw score and its bar (0–1, relative to the best hit).
    var score: (value: Double, bar: Double)?
    /// Re-runs the search for this event's track ("Same object").
    var sameObject: (() -> Void)?
    let play: () -> Void
    @State private var image: NSImage?

    var body: some View {
        Button(action: play) {
            VStack(alignment: .leading, spacing: 0) {
                ZStack {
                    LinearGradient(colors: [Color(white: 0.14), Color(white: 0.05)], startPoint: .topLeading, endPoint: .bottomTrailing)
                    if let image {
                        Image(nsImage: image).resizable().aspectRatio(contentMode: .fit)
                    } else {
                        Image(systemName: event.hasThumbnail ? "photo" : HomeModel.kindSymbol(event.kind))
                            .font(.title2)
                            .foregroundStyle(SentinelTheme.kindColor(event.kind).opacity(0.75))
                    }
                }
                .aspectRatio(16.0 / 9.0, contentMode: .fit)
                .clipped()
                .overlay(alignment: .bottom) {
                    HStack {
                        KindChip(kind: event.kind, filled: true)
                        Spacer()
                        Text(HomeModel.relative(event.createdAt, now: Date()))
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.white)
                            .padding(.horizontal, 7)
                            .padding(.vertical, 3)
                            .background(.black.opacity(0.6), in: Capsule())
                    }
                    .padding(SentinelTheme.Space.sm)
                }
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(event.label?.isEmpty == false ? event.label!.capitalized : event.kind)
                            .font(.headline).lineLimit(1)
                        if let threat = event.threat, threat != "none" {
                            ToneChip(text: threat, color: threat == "high" ? SentinelTheme.alarm : SentinelTheme.amber, uppercase: true)
                        }
                        if event.anomaly == true {
                            ToneChip(text: "anomaly", color: SentinelTheme.amber, uppercase: true)
                        }
                        Spacer()
                        if event.score > 0 { MetricPill(text: "\(Int((event.score * 100).rounded()))%") }
                    }
                    Text("\(cameraName) · \(OperatorFormat.relative(event.createdAt))")
                        .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    EventChipsRow(event: event)
                    if let score { SemanticScoreBar(score: score.value, bar: score.bar) }
                    if let description = event.description, description.isEmpty == false {
                        Text(description).font(.callout).foregroundStyle(.primary.opacity(0.8)).lineLimit(1)
                    }
                    // Room for the "Same object" button, which sits outside the card's own button.
                    if sameObject != nil { Color.clear.frame(height: 18) }
                }
                .padding(SentinelTheme.Space.md)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .operatorCard()
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .overlay(alignment: .topTrailing) { FindSimilarButton(event: event) } // WAVE11(C): Find similar
        .overlay(alignment: .bottomTrailing) {
            if let sameObject {
                Button("Same object", action: sameObject)
                    .buttonStyle(.link).font(.caption)
                    .help("Every detection of this tracked object")
                    .padding(8)
            }
        }
        .help("Play the recording from just before this event · \(OperatorFormat.dateTime.string(from: Date(unixMillis: event.createdAt)))")
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

/// The person-name and plate chips under an event's kind (wave 16).
struct EventChipsRow: View {
    let event: V2.Event

    var body: some View {
        let chips = EventChip.chips(for: event)
        if chips.isEmpty == false {
            HStack(spacing: 4) {
                ForEach(chips, id: \.kind) { chip in
                    Label(chip.text, systemImage: chip.symbol)
                        .font(chip.kind == .plate ? .caption2.monospaced().weight(.semibold) : .caption2.weight(.semibold))
                        .lineLimit(1)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(SentinelTheme.accent.opacity(0.15), in: Capsule())
                        .foregroundStyle(SentinelTheme.accent)
                        .help(chip.help)
                }
            }
        }
    }
}

/// A semantic match's score: a small bar relative to the best hit and the raw cosine.
private struct SemanticScoreBar: View {
    let score: Double
    let bar: Double

    var body: some View {
        HStack(spacing: 6) {
            Capsule().fill(SentinelTheme.line)
                .frame(width: 70, height: 5)
                .overlay(alignment: .leading) {
                    Capsule().fill(SentinelTheme.accent).frame(width: 70 * min(1, max(0, bar)), height: 5)
                }
            Text(SemanticLogic.scoreText(score)).font(.caption2.monospacedDigit()).foregroundStyle(.secondary)
        }
        .help("Similarity to the description: \(SemanticLogic.scoreText(score))")
    }
}
