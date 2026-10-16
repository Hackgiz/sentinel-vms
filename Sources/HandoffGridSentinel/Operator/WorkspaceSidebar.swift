import AppKit
import SwiftUI
import SentinelCore

/// The workspace sidebar: a search field, then the view tree (Private and
/// Shared roots) and Cameras (a tree by recording server or by camera group).
struct WorkspaceSidebar: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator
    @ObservedObject var workspace: WorkspaceModel
    @ObservedObject var library: ViewLibrary
    /// A saved view opened in the Live grid / in its own tab.
    let onOpenViewInLive: (V2.SavedView) -> Void
    let onOpenViewInTab: (V2.SavedView) -> Void
    /// A camera chosen by double-click: setup-aware (the open view tab while
    /// editing, else the Live grid).
    let onPickCamera: (String) -> Void
    let onAddToGrid: (String) -> Void
    let onOpenAsGrid: ([String]) -> Void

    @State private var query = ""
    @State private var selection: Set<String> = []
    @State private var dialog: ViewTreeDialog?
    @State private var error: String?

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
                ViewTreeSection(library: library, workspace: workspace, query: query, dialog: $dialog,
                                onOpenInLive: onOpenViewInLive, onOpenInTab: onOpenViewInTab,
                                onUpdateFromGrid: updateFromGrid)
                if let error {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
                camerasSection
            }
            .listStyle(.sidebar)
            .scrollContentBackground(.hidden)
            .onDeleteCommand { selection.removeAll() }
            if selection.count > 1 {
                Button {
                    onOpenAsGrid(orderedSelection)
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
        .modifier(ViewTreeDialogs(library: library, workspace: workspace, dialog: $dialog, onError: { error = $0 }))
    }

    private func updateFromGrid(_ view: V2.SavedView) {
        Task {
            do {
                let saved = try await library.save(ViewInputs.update(view, grid: workspace.grid), replacing: view.id, api: runtime.api)
                workspace.adopt(saved.asView)
                error = nil
            } catch {
                self.error = error.localizedDescription
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
        .onTapGesture(count: 2) { onPickCamera(item.id) }
        .onDrag {
            CameraDrag.provider(selection.contains(item.id) ? orderedSelection : [item.id])
        }
        .contextMenu {
            Button("Add to Grid") { onAddToGrid(item.id) }
            if selection.count > 1 {
                Button("Open \(selection.count) as Grid") { onOpenAsGrid(orderedSelection) }
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
