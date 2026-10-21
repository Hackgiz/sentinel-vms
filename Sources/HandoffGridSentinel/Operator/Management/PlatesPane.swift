import AppKit
import SwiftUI
import SentinelCore

// Management › Security › Plates (wave 17, the server's wave 16 plate
// reading): the switch and status, the watchlist with add and edit, and a
// plate's recent sightings. Reads need viewLive; changes are hidden without
// changeSettings. Rules and wording are in SentinelCore's PlateLogic.

struct PlatesPane: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator

    @State private var status: V2.LPRStatus?
    @State private var plates: [V2.Plate]?
    @State private var query = ""
    @State private var error: String?
    @State private var sheet: PlatesSheet?
    @State private var removing: V2.Plate?

    private var canEdit: Bool { session.can(.changeSettings) }
    private var all: [V2.Plate] { PlateLogic.sorted(plates ?? []) }

    var body: some View {
        PaneFrame(title: "Plates", subtitle: "Read license plates on vehicle events and watch for the ones you list",
                  error: error, trailing: {
            HStack {
                if canEdit { Button { sheet = .edit(nil) } label: { Label("Add Plate", systemImage: "plus") } }
                Button { Task { await reload() } } label: { Image(systemName: "arrow.clockwise") }.help("Reload")
            }
        }) {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let status {
                        LPRSwitchCard(status: status, canEdit: canEdit) { self.status = $0 }
                    }
                    watchlist
                }
                .padding(14)
            }
        }
        .task { await reload() }
        .sheet(item: $sheet) { which in
            switch which {
            case let .sightings(id):
                PlateSightingsSheet(plateID: id) { event in
                    navigator.openPlayback(cameraIDs: [event.cameraId], at: SearchFilter.playbackStart(of: event))
                    (NSApp.delegate as? SentinelOperatorApplication)?.openMainWindow()
                }
            case let .edit(plate):
                PlateEditorSheet(plate: plate, existing: all) { Task { await reload() } }
            }
        }
        .alert(removing.map { "Remove \($0.plate)?" } ?? "", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
               presenting: removing) { plate in
            Button("Remove Plate", role: .destructive) { Task { await remove(plate) } }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Sightings stay on their events; the plate is just no longer watched.")
        }
    }

    private func reload() async {
        do {
            async let s = runtime.api.lprStatus()
            async let p = runtime.api.plates()
            let (st, list) = try await (s, p)
            status = st
            plates = list
            error = nil
        } catch {
            self.error = DetectionText.errorText(error, feature: "plate reading")
            if plates == nil { plates = [] }
        }
    }

    private func remove(_ plate: V2.Plate) async {
        do {
            try await runtime.api.deletePlate(plate.id)
            removing = nil
            await reload()
        } catch {
            self.error = managementErrorText(error)
        }
    }

    @ViewBuilder
    private var watchlist: some View {
        DetectionCardBox(title: nil) {
            if all.count > 8 {
                TextField("Filter by plate or name", text: $query).textFieldStyle(.roundedBorder).frame(maxWidth: 260)
            }
            if plates == nil {
                ProgressView().frame(maxWidth: .infinity)
            } else if all.isEmpty {
                VStack(spacing: 6) {
                    Image(systemName: "car.rear").font(.largeTitle).foregroundStyle(.secondary)
                    Text("The watchlist is empty").font(.headline)
                    Text(canEdit ? "Add a plate to name it in alarms, ignore it, or raise a Critical alarm when it's read." : "An administrator adds plates here.")
                        .font(.caption).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity).padding(20)
            } else {
                Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) {
                    GridRow {
                        Text("Plate"); Text("Name"); Text("When read"); Text("Last seen"); Text("")
                    }
                    .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    Divider()
                    ForEach(PlateLogic.filter(all, query)) { plate in
                        GridRow {
                            Button(plate.plate) { sheet = .sightings(plate.id) }
                                .buttonStyle(.link).font(.callout.monospaced().weight(.semibold))
                            VStack(alignment: .leading, spacing: 2) {
                                Text(plate.name?.isEmpty == false ? plate.name! : "—")
                                    .foregroundStyle(plate.name?.isEmpty == false ? .primary : .secondary)
                                if let notes = plate.notes, notes.isEmpty == false {
                                    Text(notes).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                }
                            }
                            WatchAlertBadge(alert: plate.alert, label: PlateLogic.alertLabel(plate.alert))
                            Text(DetectionText.lastSeen(plate.lastSeenAt, cameraId: plate.lastSeenCameraId,
                                                        cameraName: { cameras.camera($0)?.name }, now: Date()))
                                .font(.caption)
                            HStack(spacing: 6) {
                                Button("Sightings") { sheet = .sightings(plate.id) }
                                if canEdit {
                                    Button("Edit") { sheet = .edit(plate) }
                                    Button("Remove…", role: .destructive) { removing = plate }
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

private enum PlatesSheet: Identifiable {
    case sightings(String)
    case edit(V2.Plate?)

    var id: String {
        switch self {
        case let .sightings(id): return "sightings-\(id)"
        case let .edit(p): return "edit-\(p?.id ?? "new")"
        }
    }
}

private struct LPRSwitchCard: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let status: V2.LPRStatus
    let canEdit: Bool
    let onSaved: (V2.LPRStatus) -> Void

    @State private var busy = false
    @State private var error: String?

    var body: some View {
        DetectionCardBox(title: "Plate reading") {
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true) }
            if canEdit {
                Toggle("Read plates on Vehicle events", isOn: Binding(get: { status.enabled },
                                                                      set: { on in Task { await save(on) } }))
                    .toggleStyle(.checkbox)
                    .disabled(busy || PlateLogic.canToggle(status) == false)
            }
            Text(PlateLogic.statusLine(status))
                .font(.caption).foregroundStyle(status.available ? Color.primary : Color.orange)
                .fixedSize(horizontal: false, vertical: true)
            Text("Plates are read on this server from the vehicle in each event's snapshot. Accuracy depends on the camera: a plate needs to be sharp and at least a few dozen pixels wide.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }

    private func save(_ enabled: Bool) async {
        busy = true
        error = nil
        defer { busy = false }
        do {
            onSaved(try await runtime.api.setLPR(enabled: enabled))
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

private struct PlateSightingsSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    let plateID: String
    let play: (V2.Event) -> Void

    @State private var plate: V2.Plate?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(plate.map(PlateLogic.title) ?? "Plate").font(.headline)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
            if let plate {
                let sightings = plate.sightings ?? []
                if sightings.isEmpty {
                    Text("Not read on any event yet.").font(.caption).foregroundStyle(.secondary)
                } else {
                    Text("The newest \(sightings.count) events with this plate.").font(.caption).foregroundStyle(.secondary)
                    ScrollView {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 180), spacing: 10)], spacing: 10) {
                            ForEach(sightings) { event in
                                Button { dismiss(); play(event) } label: {
                                    VStack(alignment: .leading, spacing: 4) {
                                        EventThumbnail(event: event)
                                        Text(cameras.name(of: event.cameraId)).font(.caption.weight(.semibold)).lineLimit(1)
                                        Text(OperatorFormat.dateTime.string(from: Date(unixMillis: event.createdAt)))
                                            .font(.caption2).foregroundStyle(.secondary)
                                    }
                                }
                                .buttonStyle(.plain)
                                .help("Play the recording from just before this event")
                            }
                        }
                    }
                    .frame(minHeight: 160, maxHeight: 440)
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
        .frame(width: 620)
        .task {
            do {
                plate = try await runtime.api.plate(plateID)
            } catch {
                self.error = managementErrorText(error)
            }
        }
    }
}

private struct PlateEditorSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    let plate: V2.Plate?
    let existing: [V2.Plate]
    let onSaved: () -> Void

    @State private var form = PlateLogic.Form()
    @State private var busy = false
    @State private var error: String?

    private var problem: String? { PlateLogic.problem(form, existing: existing, editingID: plate?.id) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(plate.map { "Edit \($0.plate)" } ?? "Add plate").font(.headline)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true) }
            Form {
                VStack(alignment: .leading, spacing: 4) {
                    TextField("Plate", text: $form.plate).font(.body.monospaced()).disabled(plate != nil)
                    Text(PlateLogic.plateHint(form, editing: plate != nil)).font(.caption).foregroundStyle(.secondary)
                }
                TextField("Name (optional)", text: $form.name, prompt: Text("Delivery van"))
                Picker("When it is read", selection: $form.alert) {
                    ForEach(PlateLogic.alertChoices) { Text($0.label).tag($0.value) }
                }
                Text((PlateLogic.alertChoices.first { $0.value == form.alert }?.help ?? "")
                     + " A plate of 6 or more characters also matches with one character misread.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                TextField("Notes (optional)", text: $form.notes, axis: .vertical).lineLimit(2...4)
            }
            .formStyle(.grouped)
            if let problem, form.plate.trimmingCharacters(in: .whitespaces).isEmpty == false {
                Text(problem).font(.caption).foregroundStyle(.orange)
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
        .onAppear { form = PlateLogic.Form(plate) }
    }

    private func save() async {
        guard problem == nil else { return }
        busy = true
        error = nil
        do {
            if let plate {
                let patch = PlateLogic.patch(plate, form)
                if patch.isEmpty == false { _ = try await runtime.api.updatePlate(plate.id, patch) }
            } else {
                _ = try await runtime.api.createPlate(PlateLogic.input(form))
            }
            onSaved()
            dismiss()
        } catch {
            self.error = managementErrorText(error)
            busy = false
        }
    }
}

/// An event's snapshot at 16:9 (placeholder while it loads or when there is none).
struct EventThumbnail: View {
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
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .task(id: event.id) {
            guard event.hasThumbnail else { return }
            image = await runtime.thumbnails.image(for: event.id)
        }
    }
}
