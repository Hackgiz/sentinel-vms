import SwiftUI
import SentinelCore

/// Add or edit a bookmark (Wave 11, stream A): title, note, color and —
/// when an in/out range is marked — whether it covers that range.
struct BookmarkEditorSheet: View {
    @ObservedObject var store: BookmarkStore
    let cameraName: String
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let draft = store.draft {
                Text(draft.isEditing ? "Edit Bookmark" : "Add Bookmark").font(.headline)
                Text(summary(draft)).font(.callout).foregroundStyle(.secondary)
                form
                if let error = store.sheetError ?? store.draft?.problem {
                    Text(error).font(.caption).foregroundStyle(SentinelTheme.alarm)
                }
                HStack {
                    Spacer()
                    Button("Cancel") { store.draft = nil; dismiss() }.keyboardShortcut(.cancelAction)
                    Button(draft.isEditing ? "Save" : "Add") {
                        Task { if await store.save() { dismiss() } }
                    }
                    .keyboardShortcut(.defaultAction)
                    .disabled(store.isSaving || draft.problem != nil)
                }
            }
        }
        .padding(20)
        .frame(width: 420)
    }

    private func summary(_ d: BookmarkLogic.Draft) -> String {
        let t = d.time
        let at = OperatorFormat.dateTime.string(from: Date(unixMillis: t.atMs))
        return t.durationMs > 0 ? "\(cameraName) · \(at) · \(BookmarkLogic.durationText(t.durationMs))" : "\(cameraName) · \(at)"
    }

    private var form: some View {
        let draft = Binding(get: { store.draft ?? BookmarkLogic.Draft(cameraID: "", at: 0, selection: nil) },
                            set: { store.draft = $0; store.sheetError = nil })
        return Form {
            TextField("Title", text: draft.title, prompt: Text("What happened here"))
            TextField("Note", text: draft.note, prompt: Text("Optional"), axis: .vertical)
                .lineLimit(2...5)
            Picker("Color", selection: draft.color) {
                ForEach(V2.BookmarkColor.all, id: \.self) { color in
                    Label { Text(color.label) } icon: {
                        Image(systemName: "circle.fill").foregroundStyle(PlaybackRulerDecorations.color(color))
                    }
                    .tag(color)
                }
            }
            if let sel = draft.wrappedValue.selection, draft.wrappedValue.isEditing == false {
                Toggle("Cover the marked range (\(BookmarkLogic.durationText(sel.to - sel.from)))", isOn: draft.useSelection)
            }
        }
        .formStyle(.grouped)
        .frame(minHeight: 200)
    }
}

/// The day's bookmarks of the timeline's cameras (Wave 11, stream A):
/// time, camera, title and color; click to go there; Export… prefills the
/// export range; Edit and Delete when the server says `canEdit`.
struct BookmarkDayList: View {
    @EnvironmentObject private var cameras: CameraDirectory
    @ObservedObject var store: BookmarkStore
    @ObservedObject var controller: PlaybackController
    let selectedCameraID: String?
    let canExport: Bool
    let onExport: (V2.Bookmark) -> Void
    @State private var confirmDelete: V2.Bookmark?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Bookmarks").font(.headline)
                Text(controller.day.formatted(date: .abbreviated, time: .omitted)).foregroundStyle(.secondary)
                Spacer()
                Button {
                    store.beginAdd(cameraID: selectedCameraID, controller: controller)
                } label: { Label("Add at Playhead", systemImage: "plus") }
                    .disabled(controller.cameraIDs.isEmpty)
                    .help("Bookmark the playhead (B)")
            }
            .padding(12)
            Divider()
            let list = store.book.all
            if list.isEmpty {
                Text("No bookmarks on this day. Press B to pin the playhead.")
                    .font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 80)
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(list) { b in
                            row(b)
                            Divider()
                        }
                    }
                }
                .frame(maxHeight: 360)
            }
        }
        .frame(width: 460)
        .confirmationDialog(confirmDelete.map { "Delete the bookmark “\(BookmarkLogic.label($0))”?" } ?? "",
                            isPresented: Binding(get: { confirmDelete != nil }, set: { if $0 == false { confirmDelete = nil } }),
                            titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                if let b = confirmDelete { Task { await store.delete(b) } }
                confirmDelete = nil
            }
            Button("Cancel", role: .cancel) { confirmDelete = nil }
        }
    }

    private func row(_ b: V2.Bookmark) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Circle().fill(PlaybackRulerDecorations.color(b.color)).frame(width: 9, height: 9)
            Text(OperatorFormat.time.string(from: Date(unixMillis: b.atMs)))
                .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Button { controller.seek(to: b.atMs) } label: {
                    HStack(spacing: 4) {
                        Text(BookmarkLogic.label(b)).fontWeight(.medium)
                        if b.isRange { Text("(\(BookmarkLogic.durationText(b.durationMs)))").foregroundStyle(.secondary) }
                    }
                }
                .buttonStyle(.plain)
                .help("Go to this bookmark")
                Text(cameras.name(of: b.cameraId)).font(.caption2).foregroundStyle(.secondary)
                if b.note.isEmpty == false {
                    Text(b.note).font(.caption).foregroundStyle(.secondary).lineLimit(2).help(b.note)
                }
            }
            Spacer(minLength: 4)
            if canExport {
                Button("Export…") { onExport(b) }
                    .help(b.isRange ? "Open the export with this range" : "Open the export with 30 seconds either side")
            }
            if b.canEdit {
                Button { store.beginEdit(b) } label: { Image(systemName: "pencil") }.help("Edit")
                Button { confirmDelete = b } label: { Image(systemName: "trash") }.help("Delete")
            }
        }
        .controlSize(.small)
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
    }
}
