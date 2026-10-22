// PeopleView.swift
// People (WAVE17): the server's face-recognition gallery, read-only, reached
// from Home next to Insights. Each person shows their alert setting and when
// they were last seen; a person's screen shows their enrolled face crops.
// Enrolling, renaming and deleting stay in Sentinel Admin and on the Mac.

import SwiftUI

@MainActor
final class PeopleModel: ObservableObject {
    @Published private(set) var people: [V2.Person]?
    @Published private(set) var status: V2.FacesStatus?
    @Published private(set) var loading = false
    @Published var error: String?

    func reload(api: SentinelAPI?) async {
        guard let api else { return }
        loading = true
        defer { loading = false }
        do {
            people = DetectionLogic.sortPeople(try await api.people())
            error = nil
        } catch is CancellationError {
            return
        } catch {
            self.error = DetectionLogic.peopleError(error)
            return
        }
        // The status line is a nicety; the list stands without it.
        status = try? await api.facesStatus()
    }
}

struct PeopleView: View {
    @ObservedObject private var session = SentinelSession.shared
    @ObservedObject private var store = AppStore.shared
    @StateObject private var model = PeopleModel()
    @Environment(\.horizontalSizeClass) private var sizeClass
    /// Survives a fold: compact pushes it, regular shows it beside the list.
    /// Kept in the store because a fold rebuilds this view (WAVE19).
    private var selected: V2.Person? {
        get { store.selectedPerson }
        nonmutating set { store.selectedPerson = newValue }
    }

    private var regular: Bool { LayoutLogic.usesSplit(for: LayoutWidth(sizeClass)) }

    var body: some View {
        Group {
            if regular {
                // A sidebar section (WAVE19): header, then the list at the
                // same width as Events' with the person beside it.
                VStack(spacing: 0) {
                    PageHeader(title: "People", subtitle: subtitle)
                    if session.isDemoMode {
                        list   // one centred note; no list to choose from
                    } else {
                    GeometryReader { geo in
                        HStack(spacing: 0) {
                            list
                                .frame(width: LayoutLogic.listColumnWidth(available: geo.size.width))
                            Rectangle().fill(SentinelTheme.line).frame(width: 1).ignoresSafeArea(edges: .bottom)
                            Group {
                                if let selected {
                                    PersonDetailView(person: selected, embedded: true)
                                        .id(selected.id)
                                } else {
                                    nothingSelected
                                }
                            }
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                            .background(SentinelTheme.background.ignoresSafeArea())
                        }
                    }
                    }
                }
                .background(SentinelTheme.background.ignoresSafeArea())
            } else {
                list
                    .navigationDestination(isPresented: Binding(get: { selected != nil },
                                                                set: { if $0 == false { selected = nil } })) {
                        if let selected { PersonDetailView(person: selected) }
                    }
            }
        }
        .navigationTitle("People")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(regular ? .hidden : .visible, for: .navigationBar)
        .task(id: session.me != nil) {
            guard session.me != nil else { return }
            await model.reload(api: session.api)
        }
    }

    private var subtitle: String {
        guard let people = model.people else { return session.isDemoMode ? "Sample data has no people" : "Recognized people" }
        return people.count == 1 ? "1 person" : "\(people.count) people"
    }

    private var nothingSelected: some View {
        VStack(spacing: SentinelTheme.Space.md) {
            ZStack {
                Circle().fill(SentinelTheme.accent.opacity(0.12)).frame(width: 76, height: 76)
                Image(systemName: "person.2.fill")
                    .font(.system(size: 28, weight: .semibold)).foregroundStyle(SentinelTheme.accent)
            }
            Text("Choose a person on the left to see their faces and when they were last seen.")
                .font(.subheadline).foregroundStyle(.white.opacity(0.6))
                .multilineTextAlignment(.center)
                .frame(maxWidth: 280)
        }
    }

    private var list: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            if session.isDemoMode {
                DemoUnavailable(symbol: "person.2", title: "People need a site",
                                text: "Recognized people come from your Sentinel server's face recognition. Connect to a site to see them.")
            } else if session.me == nil {
                ProgressView().tint(SentinelTheme.accent)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
                        if let error = model.error {
                            ActionErrorBanner(message: error) { model.error = nil }
                        }
                        if let status = model.status {
                            statusLine(status)
                        }
                        content
                    }
                    .padding(SentinelTheme.Space.lg)
                }
                .refreshable { await model.reload(api: session.api) }
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        if let people = model.people {
            if people.isEmpty {
                VStack(spacing: 10) {
                    Image(systemName: "person.2").font(.system(size: 36)).foregroundStyle(SentinelTheme.accent)
                    Text("No people yet").font(.headline).foregroundStyle(.white)
                    Text("Add people in Sentinel Admin or on the Mac; recognized faces then name them on events.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
                .padding(.top, 60)
            } else {
                VStack(spacing: SentinelTheme.Space.sm) {
                    ForEach(people) { person in
                        SelectOrLink(person, select: { selected = $0 }) { row(person) }
                    }
                }
                .opacity(model.loading ? 0.6 : 1)
            }
        } else if model.error == nil {
            ProgressView().tint(SentinelTheme.accent).frame(maxWidth: .infinity).padding(.top, 60)
        }
    }

    private func statusLine(_ s: V2.FacesStatus) -> some View {
        let text: String
        if s.available == false {
            text = "Face recognition is unavailable: \(s.reason ?? "the face models are not installed")."
        } else if s.enabled == false {
            text = "Face recognition is off. Turn it on in Sentinel Admin or on the Mac."
        } else {
            text = "Face recognition is on."
        }
        return Text(text).font(.caption).foregroundStyle(.white.opacity(0.55))
    }

    private func row(_ person: V2.Person) -> some View {
        HStack(spacing: SentinelTheme.Space.md) {
            Group {
                if let face = person.faces.first {
                    FaceCrop(faceID: face.id)
                } else {
                    ZStack {
                        SentinelTheme.panelRaised
                        Image(systemName: "person.fill").foregroundStyle(.white.opacity(0.4))
                    }
                }
            }
            .frame(width: 44, height: 44)
            .clipShape(Circle())

            VStack(alignment: .leading, spacing: 3) {
                Text(person.name).font(.subheadline.weight(.semibold)).foregroundStyle(.white).lineLimit(1)
                Text("Last seen \(DetectionLogic.lastSeen(person, cameraName: { store.cameraName(forID: $0) }, now: Date().unixMillis))")
                    .font(.caption).foregroundStyle(.white.opacity(0.55)).lineLimit(1)
            }
            Spacer(minLength: 4)
            AlertBadge(alert: person.alert)
            Image(systemName: "chevron.right").font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.4))
        }
        .padding(SentinelTheme.Space.md)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm)
            .stroke(selected?.id == person.id ? SentinelTheme.accent : SentinelTheme.line,
                    lineWidth: selected?.id == person.id ? 2 : 1))
    }
}

/// A person's alert setting, coloured as on the web: always red, never green.
struct AlertBadge: View {
    let alert: V2.WatchAlert

    var body: some View {
        let color: Color = alert == .always ? SentinelTheme.alarm : alert == .never ? SentinelTheme.recording : SentinelTheme.accent
        Chip(text: DetectionLogic.alertLabel(alert), systemImage: alert == .always ? SentinelTheme.kindSymbol("Watchlist") : nil,
             color: color)
            .lineLimit(1)
    }
}

/// One person: alert setting, last seen, notes and their enrolled face crops.
struct PersonDetailView: View {
    let person: V2.Person
    /// Beside the People list (regular width): the bar keeps saying People,
    /// so the name goes at the top of the pane.
    var embedded = false
    @ObservedObject private var session = SentinelSession.shared
    @ObservedObject private var store = AppStore.shared
    @State private var loaded: V2.Person?
    @State private var error: String?

    private var current: V2.Person { loaded ?? person }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
                if let error {
                    ActionErrorBanner(message: error) { self.error = nil }
                }
                if embedded {
                    Text(current.name).font(.title2.weight(.bold)).foregroundStyle(.white)
                }
                VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                    AlertBadge(alert: current.alert)
                    detailRow("Last seen", DetectionLogic.lastSeen(current, cameraName: { store.cameraName(forID: $0) },
                                                                   now: Date().unixMillis))
                    if let notes = current.notes, notes.isEmpty == false {
                        detailRow("Notes", notes)
                    }
                }

                SectionHeader(title: "Faces (\(current.faces.count))")
                if current.faces.isEmpty {
                    Text("No faces enrolled.").font(.caption).foregroundStyle(.white.opacity(0.55))
                } else {
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 96), spacing: SentinelTheme.Space.sm)],
                              spacing: SentinelTheme.Space.sm) {
                        ForEach(current.faces) { face in
                            VStack(spacing: 4) {
                                FaceCrop(faceID: face.id)
                                    .aspectRatio(1, contentMode: .fit)
                                    .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                                Text(store.cameraName(forID: face.cameraId) ?? "Camera")
                                    .font(.caption2.weight(.semibold)).foregroundStyle(.white).lineLimit(1)
                                Text(Date(unixMillis: face.at), format: .dateTime.month().day().hour().minute())
                                    .font(.caption2).foregroundStyle(.white.opacity(0.5))
                            }
                        }
                    }
                }
                Text("Enroll faces, rename or remove people in Sentinel Admin or on the Mac.")
                    .font(.caption).foregroundStyle(.white.opacity(0.45))
            }
            .padding(SentinelTheme.Space.lg)
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        .navigationTitle(embedded ? "People" : current.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(embedded ? .hidden : .visible, for: .navigationBar)
        .task(id: person.id) {
            guard let api = session.api else { return }
            do {
                loaded = try await api.person(person.id)
                error = nil
            } catch is CancellationError {
            } catch {
                self.error = DetectionLogic.errorMessage(error)
            }
        }
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

/// A face crop, fetched with the session's bearer token
/// (`GET /api/v2/faces/{id}/crop.jpg`) and cached next to event thumbnails.
struct FaceCrop: View {
    let faceID: String

    @State private var image: UIImage?
    @State private var failed = false

    private var cacheKey: NSString { "face:\(faceID)" as NSString }

    var body: some View {
        ZStack {
            if let image {
                Image(uiImage: image).resizable().scaledToFill()
            } else {
                SentinelTheme.panelRaised
                if failed {
                    Image(systemName: "person.crop.square").foregroundStyle(.white.opacity(0.3))
                } else {
                    ProgressView().tint(.white.opacity(0.4))
                }
            }
        }
        .clipped()
        .task(id: faceID) { await load() }
    }

    private func load() async {
        if let cached = ThumbnailCache.images.object(forKey: cacheKey) {
            image = cached
            return
        }
        image = nil; failed = false
        guard let api = SentinelSession.shared.api else { failed = true; return }
        do {
            let data = try await api.faceCrop(faceID)
            if let img = UIImage(data: data) {
                ThumbnailCache.images.setObject(img, forKey: cacheKey)
                image = img
            } else { failed = true }
        } catch { failed = true }
    }
}
