import AppKit
import SwiftUI
import SentinelCore

// Management › Security › People (wave 17, the server's wave 16 faces): the
// face recognition switch with its privacy note, the people table, one
// person's faces, and the strip of unassigned faces to enroll. Reads need
// viewLive; every change (and the unassigned strip, which the server limits to
// changeSettings) is hidden without changeSettings. Wording and rules are in
// SentinelCore's DetectionModel.swift, shared with the web Admin.

struct PeoplePane: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    @State private var status: V2.FacesStatus?
    @State private var people: [V2.Person]?
    @State private var unassigned: [V2.Face] = []
    @State private var error: String?
    @State private var sheet: PeopleSheet?
    @State private var deleting: V2.Person?

    private var canEdit: Bool { session.can(.changeSettings) }
    private var list: [V2.Person] { PeopleLogic.sorted(people ?? []) }

    var body: some View {
        PaneFrame(title: "People", subtitle: "Recognize the people you add here on Person events, and decide what their alarms do",
                  error: error, trailing: {
            HStack {
                if canEdit { Button { sheet = .edit(nil, faceId: nil) } label: { Label("Add Person", systemImage: "plus") } }
                Button { Task { await reload() } } label: { Image(systemName: "arrow.clockwise") }.help("Reload")
            }
        }) {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let status {
                        FacesSwitchCard(status: status, canEdit: canEdit) { saved in
                            self.status = saved
                            Task { await loadUnassigned() }
                        }
                    }
                    if canEdit, unassigned.isEmpty == false { unassignedCard }
                    peopleCard
                }
                .padding(14)
            }
        }
        .task { await reload() }
        .sheet(item: $sheet) { which in
            switch which {
            case let .detail(id):
                PersonDetailSheet(personID: id, canEdit: canEdit) { Task { await reload() } }
            case let .edit(person, faceId):
                PersonEditorSheet(person: person, faceID: faceId) { Task { await reload() } }
            case let .addTo(face):
                AddFaceToPersonSheet(face: face, people: list) { Task { await reload() } }
            }
        }
        .alert(deleting.map { "Delete \($0.name)?" } ?? "", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
               presenting: deleting) { person in
            Button("Delete Person", role: .destructive) { Task { await delete(person) } }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Their stored faces are deleted too, and they are no longer recognized. Past events keep the name.")
        }
    }

    // MARK: Loading

    private func reload() async {
        do {
            async let s = runtime.api.facesStatus()
            async let p = runtime.api.people()
            let (st, list) = try await (s, p)
            status = st
            people = list
            error = nil
        } catch {
            self.error = DetectionText.errorText(error, feature: "face recognition")
            if people == nil { people = [] }
        }
        await loadUnassigned()
    }

    private func loadUnassigned() async {
        guard canEdit else { unassigned = []; return }
        unassigned = (try? await runtime.api.unassignedFaces(limit: 60)) ?? []
    }

    private func delete(_ person: V2.Person) async {
        do {
            try await runtime.api.deletePerson(person.id)
            deleting = nil
            await reload()
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func removeFace(_ face: V2.Face) async {
        do {
            try await runtime.api.deleteFace(face.id)
            await reload()
        } catch {
            self.error = managementErrorText(error)
        }
    }

    // MARK: Unassigned

    private var unassignedCard: some View {
        DetectionCardBox(title: "Unassigned faces") {
            Text("Faces that matched nobody, newest first. Add one to a person to recognize them from now on.")
                .font(.caption).foregroundStyle(.secondary)
            ScrollView(.horizontal) {
                HStack(alignment: .top, spacing: 10) {
                    ForEach(unassigned) { face in
                        VStack(alignment: .leading, spacing: 4) {
                            FaceCropView(faceID: face.id)
                            Text("\(cameras.name(of: face.cameraId)) · \(OperatorFormat.dateTime.string(from: Date(unixMillis: face.at)))")
                                .font(.caption2).foregroundStyle(.secondary).lineLimit(2).frame(width: 120, alignment: .leading)
                            HStack(spacing: 4) {
                                Button("Add to person") { sheet = .addTo(face) }
                                    .disabled(list.isEmpty)
                                    .help(list.isEmpty ? "Add a person first" : "Enroll this face for someone in the list")
                                Button { Task { await removeFace(face) } } label: { Image(systemName: "xmark") }
                                    .help("Delete this face")
                            }
                            Button("New person") { sheet = .edit(nil, faceId: face.id) }
                        }
                        .controlSize(.small)
                    }
                }
                .padding(.bottom, 6)
            }
        }
    }

    // MARK: People

    @ViewBuilder
    private var peopleCard: some View {
        DetectionCardBox(title: nil) {
            if people == nil {
                ProgressView().frame(maxWidth: .infinity)
            } else if list.isEmpty {
                VStack(spacing: 6) {
                    Image(systemName: "person.crop.square").font(.largeTitle).foregroundStyle(.secondary)
                    Text("No people yet").font(.headline)
                    Text(canEdit ? "Add a person, then add faces to them from the unassigned strip." : "An administrator adds people here.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity).padding(20)
            } else {
                Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) {
                    GridRow {
                        Text("Name"); Text("When seen"); Text("Faces"); Text("Last seen"); Text("")
                    }
                    .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    Divider()
                    ForEach(list) { person in
                        GridRow {
                            VStack(alignment: .leading, spacing: 2) {
                                Button(person.name) { sheet = .detail(person.id) }
                                    .buttonStyle(.link).font(.callout.weight(.semibold))
                                if let notes = person.notes, notes.isEmpty == false {
                                    Text(notes).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                }
                            }
                            WatchAlertBadge(alert: person.alert, label: PeopleLogic.alertLabel(person.alert))
                            Text("\(person.faces.count)").monospacedDigit()
                            Text(DetectionText.lastSeen(person.lastSeenAt, cameraId: person.lastSeenCameraId,
                                                        cameraName: { cameras.camera($0)?.name }, now: Date()))
                                .font(.caption)
                            HStack(spacing: 6) {
                                Button("Faces") { sheet = .detail(person.id) }
                                if canEdit {
                                    Button("Edit") { sheet = .edit(person, faceId: nil) }
                                    Button("Delete…", role: .destructive) { deleting = person }
                                }
                            }
                            .controlSize(.small)
                        }
                    }
                }
            }
        }
    }
}

private enum PeopleSheet: Identifiable {
    case detail(String)
    case edit(V2.Person?, faceId: String?)
    case addTo(V2.Face)

    var id: String {
        switch self {
        case let .detail(id): return "detail-\(id)"
        case let .edit(p, faceId): return "edit-\(p?.id ?? "new")-\(faceId ?? "")"
        case let .addTo(face): return "add-\(face.id)"
        }
    }
}

// MARK: - Switch

private struct FacesSwitchCard: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let status: V2.FacesStatus
    let canEdit: Bool
    let onSaved: (V2.FacesStatus) -> Void

    @State private var days = PeopleLogic.daysDefault
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        DetectionCardBox(title: "Face recognition") {
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true) }
            if canEdit {
                Toggle("Recognize faces on Person events", isOn: Binding(get: { status.enabled },
                                                                         set: { on in Task { await save(.init(enabled: on)) } }))
                    .toggleStyle(.checkbox)
                    .disabled(busy || PeopleLogic.canToggle(status) == false)
            }
            Text(PeopleLogic.statusLine(status))
                .font(.caption).foregroundStyle(status.available ? Color.primary : Color.orange)
                .fixedSize(horizontal: false, vertical: true)
            Text(PeopleLogic.privacyNote).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 8) {
                Text("Keep unassigned faces for (days)")
                TextField("Days", value: $days, format: .number)
                    .frame(width: 60).textFieldStyle(.roundedBorder)
                    .disabled(canEdit == false || busy)
                    .onSubmit { days = PeopleLogic.clampDays(Double(days)) }
                Stepper("", value: $days, in: PeopleLogic.daysMin...PeopleLogic.daysMax).labelsHidden()
                    .disabled(canEdit == false || busy)
                if canEdit, days != status.unassignedDays {
                    Button("Save") { Task { await save(.init(unassignedDays: PeopleLogic.clampDays(Double(days)))) } }
                        .disabled(busy)
                }
            }
            .font(.callout)
        }
        .onAppear { days = status.unassignedDays }
        .onChange(of: status.unassignedDays) { days = $0 }
    }

    private func save(_ input: V2.FacesInput) async {
        busy = true
        error = nil
        defer { busy = false }
        do {
            onSaved(try await runtime.api.updateFaces(input))
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Sheets

private struct PersonDetailSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    let personID: String
    let canEdit: Bool
    let onChanged: () -> Void

    @State private var person: V2.Person?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(person?.name ?? "Person").font(.headline)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
            if let p = person {
                Text(([PeopleLogic.alertLabel(p.alert),
                       "last seen \(DetectionText.lastSeen(p.lastSeenAt, cameraId: p.lastSeenCameraId, cameraName: { cameras.camera($0)?.name }, now: Date()))"]
                      + (p.notes?.isEmpty == false ? [p.notes!] : [])).joined(separator: " · "))
                    .font(.caption).foregroundStyle(.secondary)
                if p.faces.isEmpty {
                    Text("No faces yet. Add one from the unassigned faces.").font(.caption).foregroundStyle(.secondary)
                } else {
                    ScrollView {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 120), spacing: 10)], spacing: 10) {
                            ForEach(p.faces) { face in
                                VStack(alignment: .leading, spacing: 4) {
                                    FaceCropView(faceID: face.id)
                                    Text(faceCaption(face)).font(.caption2).foregroundStyle(.secondary).lineLimit(2)
                                    if canEdit {
                                        Button("Delete face", role: .destructive) { Task { await removeFace(face) } }
                                            .controlSize(.small)
                                    }
                                }
                            }
                        }
                    }
                    .frame(minHeight: 160, maxHeight: 420)
                }
            } else if error == nil {
                ProgressView().frame(maxWidth: .infinity)
            }
            HStack {
                Spacer()
                Button("Close") { dismiss() }.keyboardShortcut(.cancelAction)
            }
        }
        .padding(20)
        .frame(width: 560)
        .task { await load() }
    }

    private func faceCaption(_ face: V2.Face) -> String {
        var s = "\(cameras.name(of: face.cameraId)) · \(OperatorFormat.dateTime.string(from: Date(unixMillis: face.at)))"
        if let sim = face.similarity { s += " · \(Int((sim * 100).rounded()))%" }
        return s
    }

    private func load() async {
        do {
            person = try await runtime.api.person(personID)
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func removeFace(_ face: V2.Face) async {
        do {
            try await runtime.api.deleteFace(face.id)
            await load()
            onChanged()
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

private struct PersonEditorSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    let person: V2.Person?
    let faceID: String?
    let onSaved: () -> Void

    @State private var form = PeopleLogic.Form()
    @State private var busy = false
    @State private var error: String?

    private var problem: String? { PeopleLogic.problem(form) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(person.map { "Edit \($0.name)" } ?? (faceID != nil ? "New person from this face" : "Add person")).font(.headline)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true) }
            if let faceID { FaceCropView(faceID: faceID) }
            Form {
                TextField("Name", text: $form.name)
                Picker("When they are seen", selection: $form.alert) {
                    ForEach(PeopleLogic.alertChoices) { Text($0.label).tag($0.value) }
                }
                Text(PeopleLogic.alertChoices.first { $0.value == form.alert }?.help ?? "")
                    .font(.caption).foregroundStyle(.secondary)
                TextField("Notes (optional)", text: $form.notes, axis: .vertical).lineLimit(2...4)
            }
            .formStyle(.grouped)
            if let problem, form.name.trimmingCharacters(in: .whitespaces).isEmpty == false {
                Text(problem).font(.caption).foregroundStyle(.orange)
            }
            if let person, form.name.trimmingCharacters(in: .whitespacesAndNewlines) != person.name {
                Text("Renaming also updates the name on their past events.").font(.caption).foregroundStyle(.secondary)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(busy ? "Saving…" : "Save") { Task { await save() } }
                    .buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(busy || problem != nil)
            }
        }
        .padding(20)
        .frame(width: 480)
        .onAppear { form = PeopleLogic.Form(person) }
    }

    private func save() async {
        guard problem == nil else { return }
        busy = true
        error = nil
        do {
            if let person {
                let patch = PeopleLogic.patch(person, form)
                if patch.isEmpty == false { _ = try await runtime.api.updatePerson(person.id, patch) }
            } else {
                _ = try await runtime.api.createPerson(PeopleLogic.input(form, faceId: faceID))
            }
            onSaved()
            dismiss()
        } catch {
            self.error = managementErrorText(error)
            busy = false
        }
    }
}

private struct AddFaceToPersonSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    let face: V2.Face
    let people: [V2.Person]
    let onSaved: () -> Void

    @State private var personID = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Add face to person").font(.headline)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
            FaceCropView(faceID: face.id)
            Picker("Person", selection: $personID) {
                ForEach(people) { Text($0.name).tag($0.id) }
            }
            Text("The face's event is labelled with their name too.").font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(busy ? "Adding…" : "Add") { Task { await save() } }
                    .buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(busy || personID.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 400)
        .onAppear { personID = people.first?.id ?? "" }
    }

    private func save() async {
        busy = true
        error = nil
        do {
            _ = try await runtime.api.enrollFace(personID: personID, faceID: face.id)
            onSaved()
            dismiss()
        } catch {
            self.error = managementErrorText(error)
            busy = false
        }
    }
}

// MARK: - Shared pieces

/// A face crop from `GET /faces/{id}/crop.jpg`, loaded with the session's token.
struct FaceCropView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let faceID: String
    var size: CGFloat = 96
    @State private var image: NSImage?
    @State private var failed = false

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 6).fill(Color.black.opacity(0.8))
            if let image {
                Image(nsImage: image).resizable().aspectRatio(contentMode: .fill)
            } else {
                Image(systemName: failed ? "person.crop.square" : "photo").foregroundStyle(.secondary)
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .accessibilityLabel("Face")
        .task(id: faceID) {
            if let data = try? await runtime.api.faceCrop(faceID), let img = NSImage(data: data) {
                image = img
            } else {
                failed = true
            }
        }
    }
}

/// The alert setting as a coloured badge: red for watchlist, green for never.
struct WatchAlertBadge: View {
    let alert: V2.WatchAlert
    let label: String

    var body: some View {
        Text(label)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 7).padding(.vertical, 3)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
            .lineLimit(1)
    }

    private var color: Color {
        switch alert {
        case .always: return SentinelTheme.alarm
        case .never: return .green
        default: return SentinelTheme.accent
        }
    }
}

/// A titled card in the detection panes.
struct DetectionCardBox<Content: View>: View {
    let title: String?
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let title { Text(title).font(.headline) }
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }
}
