import AppKit
import SwiftUI
import SentinelCore

/// The workspace sidebar: a search field, then Views (saved views) and
/// Cameras (a tree by recording server or by camera group).
struct WorkspaceSidebar: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var views: SavedViewsStore
    @EnvironmentObject private var navigator: OperatorNavigator
    @ObservedObject var workspace: WorkspaceModel

    @State private var query = ""
    @State private var selection: Set<String> = []
    @State private var naming: NamingRequest?
    @State private var error: String?

    private enum NamingRequest: Identifiable {
        case saveNew
        case rename(V2.View)

        var id: String {
            switch self {
            case .saveNew: return "new"
            case .rename(let view): return "rename-\(view.id)"
            }
        }
    }

    private var isSearching: Bool { query.trimmingCharacters(in: .whitespaces).isEmpty == false }

    private var tree: [CameraTree.Node] {
        let items = cameras.cameras.map {
            CameraTree.Item(id: $0.id, name: $0.name, location: $0.location, serverID: $0.recorderId,
                            groupIDs: $0.groupIds, online: $0.status.online)
        }
        let parents: [CameraTree.Parent]
        switch workspace.grouping {
        case .server:
            parents = cameras.recorders.map { CameraTree.Parent(id: $0.id, name: $0.name, online: $0.online) }
        case .group:
            parents = cameras.groups.map { CameraTree.Parent(id: $0.id, name: $0.name, online: true) }
        }
        return CameraTree.build(items: items, parents: parents, grouping: workspace.grouping, query: query)
    }

    var body: some View {
        VStack(spacing: 0) {
            searchField
            List(selection: $selection) {
                viewsSection
                camerasSection
            }
            .listStyle(.sidebar)
            .scrollContentBackground(.hidden)
            .onDeleteCommand { selection.removeAll() }
            if selection.count > 1 {
                Button {
                    workspace.openAsGrid(orderedSelection)
                } label: {
                    Label("Open \(selection.count) as grid", systemImage: "square.grid.2x2")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .padding(8)
            }
        }
        .background(SentinelTheme.chrome)
        .sheet(item: $naming) { request in
            switch request {
            case .saveNew:
                NameSheet(title: "Save Current as View", initial: "", action: "Save") { name in
                    try await save(name: name)
                }
            case .rename(let view):
                NameSheet(title: "Rename View", initial: view.name, action: "Rename") { name in
                    var input = view.input
                    input.name = name
                    let saved = try await views.save(input, replacing: view.id, api: runtime.api)
                    if workspace.activeViewID == view.id { workspace.adopt(saved) }
                }
            }
        }
    }

    // MARK: Search

    private var searchField: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass").foregroundStyle(.secondary).font(.caption)
            TextField("Search views and cameras", text: $query)
                .textFieldStyle(.plain)
                .font(.callout)
            if query.isEmpty == false {
                Button { query = "" } label: { Image(systemName: "xmark.circle.fill") }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .background(SentinelTheme.well, in: RoundedRectangle(cornerRadius: 7))
        .padding(.horizontal, 10)
        .padding(.top, 10)
        .padding(.bottom, 6)
    }

    // MARK: Views

    private var visibleViews: [V2.View] {
        CameraTree.filter(views.views, name: \.name, query: query)
    }

    private var viewsSection: some View {
        Section {
            ForEach(visibleViews) { view in
                HStack(spacing: 8) {
                    Image(systemName: "rectangle.grid.2x2").foregroundStyle(workspace.activeViewID == view.id ? SentinelTheme.accent : .secondary)
                    Text(view.name).lineLimit(1)
                    Spacer(minLength: 4)
                    Menu {
                        viewActions(view)
                    } label: {
                        Image(systemName: "ellipsis")
                    }
                    .menuStyle(.borderlessButton)
                    .menuIndicator(.hidden)
                    .fixedSize()
                    .help("Rename, update or delete this view")
                }
                .padding(.vertical, 1)
                .contentShape(Rectangle())
                .onTapGesture { workspace.openView(view) }
                .contextMenu { viewActions(view) }
                .listRowBackground(workspace.activeViewID == view.id ? SentinelTheme.accent.opacity(0.15) : Color.clear)
            }
            if isSearching == false || visibleViews.isEmpty == false || views.views.isEmpty {
                Button { naming = .saveNew } label: {
                    Label("Save current as view…", systemImage: "plus")
                        .font(.callout)
                        .foregroundStyle(workspace.cameraIDs.isEmpty ? Color.secondary : SentinelTheme.accent)
                }
                .buttonStyle(.plain)
                .disabled(workspace.cameraIDs.isEmpty)
            }
            if let message = error ?? views.lastError {
                Text(message).font(.caption).foregroundStyle(.red)
            }
        } header: {
            Text("Views")
        }
    }

    @ViewBuilder
    private func viewActions(_ view: V2.View) -> some View {
        Button("Open") { workspace.openView(view) }
        Divider()
        Button("Rename…") { naming = .rename(view) }
        Button("Update with current grid") {
            Task {
                do {
                    let saved = try await views.save(workspace.grid.viewInput(name: view.name), replacing: view.id, api: runtime.api)
                    workspace.adopt(saved)
                    error = nil
                } catch { self.error = error.localizedDescription }
            }
        }
        .disabled(workspace.cameraIDs.isEmpty)
        Menu("Open on Display") {
            Button("New Window") { OperatorWindows.shared.openMonitorWindow(viewID: view.id) }
            ForEach(Array(NSScreen.screens.enumerated()), id: \.offset) { _, screen in
                Button(screen.localizedName) { OperatorWindows.shared.openMonitorWindow(viewID: view.id, screen: screen) }
            }
        }
        Divider()
        Button("Delete", role: .destructive) {
            Task {
                do { try await views.delete(view, api: runtime.api); error = nil } catch { self.error = error.localizedDescription }
            }
        }
    }

    private func save(name: String) async throws {
        let saved = try await views.save(workspace.grid.viewInput(name: name), replacing: nil, api: runtime.api)
        workspace.adopt(saved)
    }

    // MARK: Cameras

    private var camerasSection: some View {
        Section {
            if cameras.cameras.isEmpty {
                Text(cameras.isLoaded ? "No cameras you can view." : "Loading cameras…")
                    .font(.caption).foregroundStyle(.secondary)
            }
            ForEach(tree) { node in
                DisclosureGroup(isExpanded: expansion(node.id)) {
                    ForEach(node.cameras, id: \.id) { item in
                        cameraRow(item)
                    }
                } label: {
                    HStack(spacing: 6) {
                        if workspace.grouping == .server {
                            Circle().fill(node.parent.online ? Color.green : Color.red).frame(width: 7, height: 7)
                        } else {
                            Image(systemName: "folder").font(.caption).foregroundStyle(.secondary)
                        }
                        Text(node.parent.name).fontWeight(.semibold).lineLimit(1)
                        Spacer(minLength: 4)
                        Text(isSearching ? "\(node.cameras.count)/\(node.totalCount)" : "\(node.totalCount)")
                            .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .help(node.parent.online || workspace.grouping == .group ? node.parent.name : "\(node.parent.name) is offline")
                }
            }
        } header: {
            HStack {
                Text("Cameras")
                Spacer()
                Picker("Group by", selection: $workspace.grouping) {
                    Text("By server").tag(CameraTree.Grouping.server)
                    Text("By group").tag(CameraTree.Grouping.group)
                }
                .pickerStyle(.menu)
                .labelsHidden()
                .controlSize(.mini)
                .fixedSize()
                .help("Group cameras by recording server or by camera group")
            }
        }
    }

    private func expansion(_ id: String) -> Binding<Bool> {
        Binding(get: { isSearching || workspace.collapsedParents.contains(id) == false },
                set: { open in if open == workspace.collapsedParents.contains(id) { workspace.toggleParent(id) } })
    }

    private func cameraRow(_ item: CameraTree.Item) -> some View {
        HStack(spacing: 8) {
            Circle().fill(item.online ? Color.green : Color.gray).frame(width: 7, height: 7)
            Text(item.name).lineLimit(1)
            Spacer(minLength: 4)
            if workspace.cameraIDs.contains(item.id) {
                Image(systemName: "rectangle.grid.2x2.fill").font(.caption2).foregroundStyle(.secondary)
                    .help("On the grid")
            }
        }
        .tag(item.id)
        .contentShape(Rectangle())
        .onTapGesture(count: 2) { workspace.open(item.id) }
        .onDrag {
            CameraDrag.provider(selection.contains(item.id) ? orderedSelection : [item.id])
        }
        .contextMenu {
            Button("Add to Grid") { workspace.open(item.id) }
            if selection.count > 1 {
                Button("Open \(selection.count) as Grid") { workspace.openAsGrid(orderedSelection) }
            }
            Button("Pop Out Window") { OperatorWindows.shared.openCamera(item.id, name: item.name) }
        }
    }

    /// Selected cameras in the order the tree shows them.
    private var orderedSelection: [String] {
        var seen = Set<String>()
        return tree.flatMap(\.cameras).map(\.id).filter { selection.contains($0) && seen.insert($0).inserted }
    }
}

/// A one-field sheet for naming or renaming a view.
private struct NameSheet: View {
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
