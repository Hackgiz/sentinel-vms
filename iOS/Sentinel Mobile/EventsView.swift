// EventsView.swift
// Event feed: each AI detection shows a frame thumbnail, the scene
// description, camera, and time. Tappable into a detail view.

import SwiftUI

struct EventsView: View {
    @ObservedObject var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared
    @StateObject private var describe = DescribeSearchModel()
    @State private var filter: String? = nil   // nil = All, else a kind

    private var kinds: [String] {
        Array(Set(store.events.map(\.kind))).sorted()
    }
    private var filtered: [V2.Event] {
        guard let filter else { return store.events }
        return store.events.filter { $0.kind == filter }
    }

    var body: some View {
        NavigationStack {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()
                VStack(spacing: 0) {
                    eventsHeader
                    modePicker
                    if store.eventsMode == .alarms {
                        ScrollView {
                            AlarmsListView()
                                .padding(SentinelTheme.Space.lg)
                        }
                        .refreshable { await store.refreshAll() }
                    } else {
                        detections
                    }
                }
            }
            // Hide the big empty large-title bar; use a compact custom header.
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: V2.Event.self) { EventDetailView(event: $0) }
            .navigationDestination(for: V2.Camera.self) { CameraDetailView(camera: $0) }
            .navigationDestination(for: V2.Alarm.self) { AlarmDetailView(alarmID: $0.id) }
            // "Describe it" waits for /api/v2/me, like the other wave 15+ screens.
            .task(id: session.me?.can(.viewLive) == true) {
                guard session.me?.can(.viewLive) == true else { return }
                await describe.loadStatus(api: session.api)
            }
        }
    }

    /// The detections feed, with "Describe it" above it when the server offers
    /// semantic search; its results replace the feed until cleared.
    @ViewBuilder
    private var detections: some View {
        if describe.availability.showsField {
            DescribeSearchField(model: describe) {
                Task { await describe.search(api: session.api) }
            }
            .padding(.horizontal, SentinelTheme.Space.lg)
            .padding(.bottom, SentinelTheme.Space.sm)
        }
        if describe.showingResults {
            DescribeResultsList(model: describe)
        } else {
            detectionsList
        }
    }

    @ViewBuilder
    private var detectionsList: some View {
        if store.events.isEmpty {
            emptyState.frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollView {
                LazyVStack(spacing: SentinelTheme.Space.md, pinnedViews: [.sectionHeaders]) {
                    Section {
                        ForEach(filtered) { event in
                            NavigationLink(value: event) { EventRow(event: event) }
                                .buttonStyle(.plain)
                                .onAppear {
                                    if event.id == filtered.last?.id { Task { await store.loadMoreEvents() } }
                                }
                        }
                    } header: {
                        if kinds.count > 1 { filterBar }
                    }
                }
                .padding(SentinelTheme.Space.lg)
            }
            .refreshable { await store.refreshAll() }
        }
    }

    private var eventsHeader: some View {
        HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
            VStack(alignment: .leading, spacing: 3) {
                Text("Events")
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                Text(subtitle)
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.white.opacity(0.6))
            }
            Spacer(minLength: 8)
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.top, 4)
        .padding(.bottom, SentinelTheme.Space.sm)
    }

    private var subtitle: String {
        switch store.eventsMode {
        case .alarms:
            let open = store.alarms.count
            return open == 0 ? "No open alarms" : "\(open) open alarm\(open == 1 ? "" : "s")"
        case .detections:
            return "\(store.events.count) AI detection\(store.events.count == 1 ? "" : "s")"
        }
    }

    /// Alarms (the site's queue — what needs a response) vs. Detections (the AI
    /// feed — what was seen). The tab badge counts new alarms, so alarms lead.
    private var modePicker: some View {
        HStack(spacing: 4) {
            modeButton("Alarms", mode: .alarms, badge: store.newAlertCount)
            modeButton("Detections", mode: .detections, badge: 0)
        }
        .padding(4)
        .background(SentinelTheme.panel, in: Capsule())
        .overlay(Capsule().stroke(SentinelTheme.line, lineWidth: 1))
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.bottom, SentinelTheme.Space.sm)
    }

    private func modeButton(_ title: String, mode: EventsMode, badge: Int) -> some View {
        let selected = store.eventsMode == mode
        return Button {
            Haptics.tap()
            withAnimation(.easeInOut(duration: 0.2)) { store.eventsMode = mode }
        } label: {
            HStack(spacing: 6) {
                Text(title)
                if badge > 0 {
                    Text("\(badge)")
                        .font(.caption2.weight(.heavy))
                        .foregroundStyle(.white)
                        .padding(.horizontal, 6).padding(.vertical, 1)
                        .background(SentinelTheme.alarm, in: Capsule())
                }
            }
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(selected ? .white : .white.opacity(0.6))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 8)
            .background(selected ? SentinelTheme.accent : .clear, in: Capsule())
        }
        .buttonStyle(.plain)
    }

    private var filterBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: SentinelTheme.Space.sm) {
                filterChip(label: "All", kind: nil)
                ForEach(kinds, id: \.self) { filterChip(label: $0, kind: $0) }
            }
            .padding(.vertical, 6)
        }
        .background(SentinelTheme.background)
    }

    private func filterChip(label: String, kind: String?) -> some View {
        let selected = filter == kind
        let color = kind.map(SentinelTheme.kindColor) ?? SentinelTheme.accent
        return Button {
            Haptics.tap(); filter = kind
        } label: {
            Text(label)
                .font(.caption.weight(.semibold))
                .foregroundStyle(selected ? .white : color)
                .padding(.horizontal, 12).padding(.vertical, 6)
                .background(selected ? color : color.opacity(0.14), in: Capsule())
        }
        .buttonStyle(.plain)
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "sparkles")
                .font(.system(size: 40)).foregroundStyle(SentinelTheme.accent)
            Text("No events yet").font(.headline).foregroundStyle(.white)
            Text("AI detections — people, vehicles, and more — will appear here.")
                .font(.caption).foregroundStyle(.white.opacity(0.55))
                .multilineTextAlignment(.center).padding(.horizontal, 40)
        }
    }
}

struct EventRow: View {
    let event: V2.Event
    /// A "Describe it" match: the raw score and its bar relative to the best hit.
    var match: Match? = nil
    @ObservedObject private var store = AppStore.shared

    struct Match: Hashable {
        var score: Double
        var bar: Double
    }

    var body: some View {
        let color = SentinelTheme.kindColor(event.kind)
        return VStack(alignment: .leading, spacing: 0) {
            ZStack(alignment: .bottomLeading) {
                EventThumbnail(event: event)
                    .frame(height: 188)
                    .clipped()
                LinearGradient(colors: [.clear, .black.opacity(0.7)], startPoint: .center, endPoint: .bottom)
                    .frame(height: 188)
                    .allowsHitTesting(false)
                HStack(alignment: .bottom) {
                    VStack(alignment: .leading, spacing: 4) {
                        Chip(text: event.kind.uppercased(), systemImage: SentinelTheme.kindSymbol(event.kind), color: color, filled: true)
                        DetectionChips(event: event)
                    }
                    Spacer()
                    Text(event.createdAtDate, format: .relative(presentation: .named))
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.white.opacity(0.85))
                }
                .padding(SentinelTheme.Space.sm)
            }
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(store.cameraName(forID: event.cameraId) ?? "Camera").font(.subheadline.weight(.bold)).foregroundStyle(.white)
                    Spacer(minLength: 8)
                    if let match { ScoreBar(score: match.score, bar: match.bar) }
                }
                Text(descriptionText)
                    .font(.callout)
                    .foregroundStyle(.white.opacity(0.8))
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(SentinelTheme.Space.md)
            .background(SentinelTheme.panel)
        }
        .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
    }

    private var descriptionText: String {
        if let d = event.description, d.isEmpty == false { return d }
        if let l = event.label, l.isEmpty == false, l.lowercased() != event.kind.lowercased() { return "\(event.kind): \(l)" }
        return "\(event.kind) detected (\(Int(event.score * 100))%)"
    }
}

struct EventDetailView: View {
    let event: V2.Event
    @ObservedObject var store = AppStore.shared

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
                EventThumbnail(event: event, contentMode: .fit)
                    .frame(maxWidth: .infinity)
                    .frame(height: 240)
                    .background(.black)
                    .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))

                HStack(spacing: SentinelTheme.Space.sm) {
                    Chip(text: event.kind.uppercased(), systemImage: SentinelTheme.kindSymbol(event.kind),
                         color: SentinelTheme.kindColor(event.kind), filled: true)
                    Chip(text: "\(Int(event.score * 100))% confidence")
                    Spacer()
                }
                DetectionChips(event: event)

                let recognized = DetectionLogic.detailLines(event)
                if recognized.isEmpty == false {
                    VStack(alignment: .leading, spacing: 4) {
                        ForEach(recognized, id: \.self) { line in
                            Text(line).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                        }
                    }
                }

                if let d = event.description, d.isEmpty == false {
                    VStack(alignment: .leading, spacing: 6) {
                        Label("AI Description", systemImage: "sparkles")
                            .font(.caption.weight(.bold)).foregroundStyle(SentinelTheme.accent)
                        Text(d).font(.body).foregroundStyle(.white)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(SentinelTheme.Space.md)
                    .background(SentinelTheme.accent.opacity(0.08), in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                }

                if let reason = event.reason, reason.isEmpty == false {
                    detailRow("Why it matters", reason)
                }
                if let threat = event.threat, threat != "none" {
                    detailRow("Threat level", threat.capitalized)
                }
                detailRow("Camera", store.cameraName(forID: event.cameraId) ?? "Camera")
                detailRow("Time", event.createdAtDate.formatted(date: .abbreviated, time: .standard))

                if let camera = store.camera(id: event.cameraId) {
                    NavigationLink(value: camera) {
                        Label("View on camera", systemImage: "video.fill")
                            .font(.headline)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, SentinelTheme.Space.md)
                            .background(SentinelTheme.accent, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                            .foregroundStyle(.white)
                    }
                }
            }
            .padding(SentinelTheme.Space.lg)
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        .navigationTitle(event.kind)
        .navigationBarTitleDisplayMode(.inline)
        // Events hides its nav bar; force this pushed view's bar visible so the
        // back button always shows (guards the iOS 16 toolbar-hidden quirk).
        .toolbar(.visible, for: .navigationBar)
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).font(.subheadline).foregroundStyle(.white.opacity(0.55))
            Spacer()
            Text(value).font(.subheadline.weight(.medium)).foregroundStyle(.white)
                .multilineTextAlignment(.trailing)
        }
    }
}

/// The recognized person's name and the plate read, when the server matched them (wave 16).
struct DetectionChips: View {
    let event: V2.Event

    var body: some View {
        let person = DetectionLogic.personChip(event)
        let plate = DetectionLogic.plateChip(event)
        if person != nil || plate != nil {
            HStack(spacing: 4) {
                if let person {
                    Chip(text: person, systemImage: "person.crop.circle.fill", color: SentinelTheme.accent, filled: true)
                        .lineLimit(1)
                        .accessibilityLabel("Recognized: \(person)")
                }
                if let plate {
                    Chip(text: plate, systemImage: "car.fill", color: SentinelTheme.amber, filled: true)
                        .lineLimit(1)
                        .accessibilityLabel("Plate: \(plate)")
                }
            }
        }
    }
}
