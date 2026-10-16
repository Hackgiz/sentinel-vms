import AppKit
import SwiftUI
import SentinelCore

/// What the view tree can ask a dialog for.
enum ViewTreeDialog: Identifiable {
    case saveCurrent
    case rename(V2.SavedView)
    case move(V2.SavedView)
    case newFolder(parentID: String)
    case renameFolder(V2.ViewGroup)
    case deleteFolder(V2.ViewGroup)
    case deleteView(V2.SavedView)

    var id: String {
        switch self {
        case .saveCurrent: return "save"
        case .rename(let v): return "rename-\(v.id)"
        case .move(let v): return "move-\(v.id)"
        case .newFolder(let p): return "folder-new-\(p)"
        case .renameFolder(let g): return "folder-rename-\(g.id)"
        case .deleteFolder(let g): return "folder-delete-\(g.id)"
        case .deleteView(let v): return "delete-\(v.id)"
        }
    }
}

/// The sidebar's saved views as a tree: Private and Shared roots, folders
/// nested by `parentId`, a lock on Shared without `manageViews`. A click
/// opens a view in the Live grid, a double-click (or the menu) in its own tab.
struct ViewTreeSection: View {
    @EnvironmentObject private var session: OperatorSession
    @ObservedObject var library: ViewLibrary
    @ObservedObject var workspace: WorkspaceModel
    let query: String
    @Binding var dialog: ViewTreeDialog?
    let onOpenInLive: (V2.SavedView) -> Void
    let onOpenInTab: (V2.SavedView) -> Void
    let onUpdateFromGrid: (V2.SavedView) -> Void

    private var canManage: Bool { session.can(.manageViews) }
    private var isSearching: Bool { query.trimmingCharacters(in: .whitespaces).isEmpty == false }

    private var roots: [ViewTree.Root] {
        ViewTree.build(views: library.privateViews, shared: library.sharedViews, groups: library.groups, query: query, canManage: canManage)
    }

    var body: some View {
        Section {
            ForEach(roots) { root in rootRow(root) }
            if isSearching, roots.isEmpty {
                Text("No views match.").font(.caption).foregroundStyle(.secondary)
            }
            Button { dialog = .saveCurrent } label: {
                Label("Save current as view…", systemImage: "plus")
                    .font(.callout)
                    .foregroundStyle(workspace.cameraIDs.isEmpty ? Color.secondary : SentinelTheme.accent)
            }
            .buttonStyle(.plain)
            .disabled(workspace.cameraIDs.isEmpty)
            if let message = library.lastError {
                Text(message).font(.caption).foregroundStyle(.red)
            }
        } header: {
            Text("Views")
        }
    }

    // MARK: Roots and nodes

    private func expanded(_ key: String) -> Binding<Bool> {
        Binding(get: { isSearching || workspace.collapsedParents.contains(key) == false },
                set: { open in if open == workspace.collapsedParents.contains(key) { workspace.toggleParent(key) } })
    }

    private func rootRow(_ root: ViewTree.Root) -> some View {
        DisclosureGroup(isExpanded: expanded("r:\(root.id.rawValue)")) {
            ForEach(root.nodes) { node in nodeRow(node) }
            if root.nodes.isEmpty, isSearching == false, library.isLoaded {
                Text(emptyText(root)).font(.caption).foregroundStyle(.secondary)
            }
        } label: {
            HStack(spacing: 6) {
                Image(systemName: root.id == .private ? "person.fill" : "person.2.fill").font(.caption).foregroundStyle(.secondary)
                Text(root.label).fontWeight(.semibold)
                if root.readOnly {
                    Image(systemName: "lock.fill").font(.caption2).foregroundStyle(.secondary)
                        .help("Shared views are read-only for your role")
                        .accessibilityLabel("Read-only")
                }
                Spacer(minLength: 4)
                if root.id == .shared, canManage {
                    Button { dialog = .newFolder(parentID: "") } label: { Image(systemName: "folder.badge.plus") }
                        .buttonStyle(.plain)
                        .foregroundStyle(.secondary)
                        .help("New folder")
                }
                Text("\(root.viewCount)").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            }
        }
    }

    private func emptyText(_ root: ViewTree.Root) -> String {
        root.id == .private ? "No saved views yet." : (canManage ? "Nothing shared yet. Save a view as shared." : "Nothing is shared with you.")
    }

    private func nodeRow(_ node: ViewTree.Node) -> AnyView {
        switch node {
        case .folder(let id, let name, let children, let count):
            return AnyView(
                DisclosureGroup(isExpanded: expanded("f:\(id)")) {
                    ForEach(children) { child in nodeRow(child) }
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: "folder").font(.caption).foregroundStyle(.secondary)
                        Text(name).lineLimit(1)
                        Spacer(minLength: 4)
                        Text("\(count)").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .contentShape(Rectangle())
                    .contextMenu { folderMenu(id: id) }
                }
            )
        case .view(let view):
            return AnyView(viewRow(view))
        }
    }

    @ViewBuilder
    private func folderMenu(id: String) -> some View {
        if canManage, let group = library.groups.first(where: { $0.id == id }) {
            Button("New Folder Inside…") { dialog = .newFolder(parentID: group.id) }
            Button("Rename…") { dialog = .renameFolder(group) }
            Divider()
            Button("Delete…", role: .destructive) { dialog = .deleteFolder(group) }
        }
    }

    private func viewRow(_ view: V2.SavedView) -> some View {
        let active = workspace.activeViewID == view.id
        let edited = active && workspace.grid != WorkspaceGrid(view: view.asView)
        return HStack(spacing: 8) {
            Image(systemName: "rectangle.grid.2x2").foregroundStyle(active ? SentinelTheme.accent : .secondary)
            Text(view.name).lineLimit(1)
            if edited {
                Text("edited").font(.caption2.weight(.semibold)).foregroundStyle(SentinelTheme.amber)
            }
            Spacer(minLength: 4)
            Menu {
                viewMenu(view)
            } label: {
                Image(systemName: "ellipsis")
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
            .help("Open, rename, update or delete this view")
        }
        .padding(.vertical, 1)
        .contentShape(Rectangle())
        .onTapGesture(count: 2) { onOpenInTab(view) }
        .onTapGesture { onOpenInLive(view) }
        .contextMenu { viewMenu(view) }
        .help("Open \(view.name) (\(view.columns)×\(view.rows)); double-click for a new tab")
        .listRowBackground(active ? SentinelTheme.accent.opacity(0.15) : Color.clear)
    }

    @ViewBuilder
    private func viewMenu(_ view: V2.SavedView) -> some View {
        Button("Open in Live") { onOpenInLive(view) }
        Button("Open in New Tab") { onOpenInTab(view) }
        Menu("Open on Display") {
            Button("New Window") { OperatorWindows.shared.openMonitorWindow(viewID: view.id) }
            ForEach(Array(NSScreen.screens.enumerated()), id: \.offset) { _, screen in
                Button(screen.localizedName) { OperatorWindows.shared.openMonitorWindow(viewID: view.id, screen: screen) }
            }
        }
        Divider()
        if ViewInputs.canEdit(view, canManage: canManage) {
            Button("Rename…") { dialog = .rename(view) }
            Button("Update with Current Grid") { onUpdateFromGrid(view) }
                .disabled(workspace.cameraIDs.isEmpty)
            if view.isShared {
                Button("Move to Folder…") { dialog = .move(view) }
            }
            Divider()
            Button("Delete…", role: .destructive) { dialog = .deleteView(view) }
        } else {
            Text("Shared views are read-only for your role.")
        }
    }
}

/// Applies the sheets and confirmations behind the view tree's menus.
struct ViewTreeDialogs: ViewModifier {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var session: OperatorSession
    @ObservedObject var library: ViewLibrary
    @ObservedObject var workspace: WorkspaceModel
    @Binding var dialog: ViewTreeDialog?
    let onError: (String?) -> Void

    private var canManage: Bool { session.can(.manageViews) }

    func body(content: Content) -> some View {
        content
            .sheet(item: sheetBinding) { request in
                switch request {
                case .saveCurrent:
                    SaveViewSheet(initialName: workspace.activeViewID.flatMap { library.view($0)?.name }.map { "\($0) copy" } ?? "",
                                  summary: gridSummary(workspace.grid),
                                  canManage: canManage, folders: ViewTree.folderOptions(groups: library.groups),
                                  initialTarget: nil) { target in
                        let saved = try await library.save(ViewInputs.new(grid: workspace.grid, target: target), replacing: nil, api: runtime.api)
                        workspace.adopt(saved.asView)
                    }
                case .rename(let view):
                    ViewNameSheet(title: "Rename View", initial: view.name, action: "Rename") { name in
                        _ = try await library.save(ViewInputs.update(view, name: name), replacing: view.id, api: runtime.api)
                    }
                case .move(let view):
                    MoveToFolderSheet(current: view.groupId ?? "", folders: ViewTree.folderOptions(groups: library.groups)) { groupID in
                        _ = try await library.save(ViewInputs.move(view, toGroup: groupID), replacing: view.id, api: runtime.api)
                    }
                case .newFolder(let parentID):
                    ViewNameSheet(title: "New Folder", initial: "", action: "Create") { name in
                        try await library.createFolder(name: name, parentID: parentID, api: runtime.api)
                    }
                case .renameFolder(let group):
                    ViewNameSheet(title: "Rename Folder", initial: group.name, action: "Rename") { name in
                        try await library.renameFolder(group, to: name, api: runtime.api)
                    }
                case .deleteFolder, .deleteView:
                    EmptyView()
                }
            }
            .confirmationDialog(confirmTitle, isPresented: confirmBinding, titleVisibility: .visible) {
                Button("Delete", role: .destructive) { confirmDelete() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text(confirmMessage)
            }
    }

    // The sheet shows for the editing dialogs; the delete ones use the confirmation.
    private var sheetBinding: Binding<ViewTreeDialog?> {
        Binding(get: {
            switch dialog {
            case .deleteFolder, .deleteView, .none: return nil
            default: return dialog
            }
        }, set: { if $0 == nil { dialog = nil } })
    }

    private var confirmBinding: Binding<Bool> {
        Binding(get: {
            switch dialog {
            case .deleteFolder, .deleteView: return true
            default: return false
            }
        }, set: { if $0 == false { dialog = nil } })
    }

    private var confirmTitle: String {
        switch dialog {
        case .deleteFolder(let g): return "Delete the folder “\(g.name)”?"
        case .deleteView(let v): return "Delete the view “\(v.name)”?"
        default: return ""
        }
    }

    private var confirmMessage: String {
        switch dialog {
        case .deleteFolder: return "Only an empty folder can be deleted."
        case .deleteView(let v): return v.isShared ? "Everyone who sees this shared view loses it." : "This can't be undone."
        default: return ""
        }
    }

    private func confirmDelete() {
        let request = dialog
        dialog = nil
        Task {
            do {
                switch request {
                case .deleteFolder(let group): try await library.deleteFolder(group, api: runtime.api)
                case .deleteView(let view): try await library.delete(view, api: runtime.api)
                default: break
                }
                onError(nil)
            } catch {
                onError(error.localizedDescription)
            }
        }
    }
}

func gridSummary(_ grid: WorkspaceGrid) -> String {
    let count = grid.cameraIDs.count
    return "\(grid.columns)×\(grid.rows), \(count) camera\(count == 1 ? "" : "s")."
}

/// A one-field sheet for naming a view or folder.
struct ViewNameSheet: View {
    @Environment(\.dismiss) private var dismiss
    let title: String
    let initial: String
    let action: String
    let perform: (String) async throws -> Void

    @State private var name = ""
    @State private var error: String?
    @State private var isWorking = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(title).font(.headline)
            TextField("Name", text: $name)
                .textFieldStyle(.roundedBorder)
                .onSubmit(commit)
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(action, action: commit)
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(isWorking || trimmed.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 340)
        .onAppear { name = initial }
    }

    private var trimmed: String { name.trimmingCharacters(in: .whitespaces) }

    private func commit() {
        guard trimmed.isEmpty == false, isWorking == false else { return }
        isWorking = true
        Task {
            defer { isWorking = false }
            do {
                try await perform(trimmed)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Save As…: a name and, with `manageViews`, Private or Shared in a folder.
struct SaveViewSheet: View {
    @Environment(\.dismiss) private var dismiss
    let initialName: String
    let summary: String
    let canManage: Bool
    let folders: [ViewTree.FolderOption]
    let initialTarget: ViewSaveTarget?
    let perform: (ViewSaveTarget) async throws -> Void

    @State private var target = ViewSaveTarget(name: "")
    @State private var error: String?
    @State private var isWorking = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Save View").font(.headline)
            VStack(alignment: .leading, spacing: 4) {
                TextField("Name", text: $target.name)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit(commit)
                Text(summary).font(.caption).foregroundStyle(.secondary)
            }
            if canManage {
                Picker("Visibility", selection: $target.shared) {
                    Text("Private").tag(false)
                    Text("Shared").tag(true)
                }
                .pickerStyle(.segmented)
                if target.shared {
                    Picker("Folder", selection: $target.groupId) {
                        Text("Top level").tag("")
                        ForEach(folders) { folder in Text(folder.label).tag(folder.id) }
                    }
                    Text("Everyone on this site sees shared views.").font(.caption).foregroundStyle(.secondary)
                }
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save", action: commit)
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(isWorking || target.name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 380)
        .onAppear { target = initialTarget ?? ViewSaveTarget(name: initialName) }
    }

    private func commit() {
        guard isWorking == false else { return }
        let checked: ViewSaveTarget
        do {
            checked = try target.validated(canManage: canManage, folderIDs: Set(folders.map(\.id)))
        } catch {
            self.error = error.localizedDescription
            return
        }
        isWorking = true
        Task {
            defer { isWorking = false }
            do {
                try await perform(checked)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Move a shared view into another folder (or the top level).
struct MoveToFolderSheet: View {
    @Environment(\.dismiss) private var dismiss
    let current: String
    let folders: [ViewTree.FolderOption]
    let perform: (String) async throws -> Void

    @State private var choice = ""
    @State private var error: String?
    @State private var isWorking = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Move to Folder").font(.headline)
            Picker("Folder", selection: $choice) {
                Text("Top level").tag("")
                ForEach(folders) { folder in Text(folder.label).tag(folder.id) }
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Move") {
                    isWorking = true
                    Task {
                        defer { isWorking = false }
                        do {
                            try await perform(choice)
                            dismiss()
                        } catch {
                            self.error = error.localizedDescription
                        }
                    }
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
                .disabled(isWorking || choice == current)
            }
        }
        .padding(20)
        .frame(width: 360)
        .onAppear { choice = current }
    }
}
