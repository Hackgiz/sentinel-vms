import AppKit
import SwiftUI
import SentinelCore

/// The layouts being edited in view tabs. Nothing is stored until the
/// operator saves; the edit survives switching tabs and leaving setup mode.
@MainActor
final class ViewEditorStore: ObservableObject {
    @Published private(set) var edits: [String: WorkspaceGrid] = [:]

    /// The grid a tab shows: the edit in progress, else the stored layout.
    func grid(for view: V2.SavedView) -> WorkspaceGrid {
        edits[view.id] ?? WorkspaceGrid(view: view.asView)
    }

    func isDirty(_ view: V2.SavedView) -> Bool {
        guard let edit = edits[view.id] else { return false }
        return edit != WorkspaceGrid(view: view.asView)
    }

    func edit(_ view: V2.SavedView, _ change: (inout WorkspaceGrid) -> Void) {
        var next = grid(for: view)
        change(&next)
        let stored = WorkspaceGrid(view: view.asView)
        // Going back to what is stored is no edit at all.
        edits[view.id] = next == stored ? nil : next
    }

    /// Throws the edit away (Revert, a closed tab, a saved view).
    func discard(_ viewID: String) {
        edits[viewID] = nil
    }

    func prune(keeping ids: Set<String>) {
        for id in edits.keys where ids.contains(id) == false { edits[id] = nil }
    }
}

/// A saved view in its own tab: the view's live grid. Read-only until setup
/// mode is on; in setup mode the grid takes cameras dragged from the tree,
/// swaps tiles, clears them, resizes to a preset, and saves (update, or
/// Save As… with a name and a Private/Shared target).
struct ViewTabView: View {
    let view: V2.SavedView
    @ObservedObject var editor: ViewEditorStore
    let setup: Bool
    /// The viewer may overwrite this view (not a shared one without `manageViews`).
    let canUpdate: Bool
    let busy: Bool
    let onSave: () -> Void
    let onSaveAs: () -> Void
    let onRevert: () -> Void
    let onSetup: () -> Void

    @State private var focused: String?

    private static let presets: [(label: String, columns: Int, rows: Int)] = [("1", 1, 1), ("2×2", 2, 2), ("3×3", 3, 3), ("4×4", 4, 4)]

    private var grid: WorkspaceGrid { editor.grid(for: view) }
    private var dirty: Bool { editor.isDirty(view) }

    var body: some View {
        VStack(spacing: 0) {
            bar
            Divider().overlay(SentinelTheme.line)
            ViewTabGrid(grid: grid, setup: setup, focused: $focused,
                        onChange: { change in editor.edit(view, change) })
        }
        .background(SentinelTheme.background)
        .onChange(of: view.id) { _ in focused = nil }
    }

    private var bar: some View {
        HStack(spacing: 10) {
            Text(view.name).font(.headline).lineLimit(1)
            if view.isShared {
                Label("Shared", systemImage: canUpdate ? "person.2.fill" : "lock.fill")
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 7).padding(.vertical, 2)
                    .background(Color.secondary.opacity(0.15), in: Capsule())
                    .help(canUpdate ? "Everyone on this site sees this view" : "Shared views are read-only for your role")
            }
            if setup {
                Picker("Layout", selection: Binding(get: { currentPreset }, set: { applyPreset($0) })) {
                    ForEach(Array(Self.presets.enumerated()), id: \.offset) { index, preset in Text(preset.label).tag(Optional(index)) }
                    if currentPreset == nil { Text("\(grid.columns)×\(grid.rows)").tag(Optional<Int>.none) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .frame(width: 190)
                .help("Grid layout")
                Text("Drag cameras from the sidebar onto tiles; drag tiles to swap.")
                    .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                Spacer()
                if dirty {
                    Text("Unsaved changes").font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.amber)
                    Button("Revert", action: onRevert).disabled(busy)
                }
                if canUpdate {
                    Button("Save", action: onSave)
                        .buttonStyle(.borderedProminent)
                        .disabled(busy || dirty == false)
                }
                Button("Save As…", action: onSaveAs).disabled(busy)
                Button("Done", action: onSetup)
            } else {
                Text("\(view.columns)×\(view.rows)").font(.caption).foregroundStyle(.secondary)
                if dirty { Text("Unsaved changes").font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.amber) }
                Spacer()
                Button(action: onSetup) { Label("Edit Layout", systemImage: "pencil") }
                    .help("Setup mode: edit this view")
            }
        }
        .controlSize(.small)
        .padding(.horizontal, 14)
        .padding(.vertical, 7)
        .background(SentinelTheme.chrome)
    }

    private var currentPreset: Int? {
        Self.presets.firstIndex { $0.columns == grid.columns && $0.rows == grid.rows }
    }

    private func applyPreset(_ index: Int?) {
        guard let index, Self.presets.indices.contains(index) else { return }
        let preset = Self.presets[index]
        editor.edit(view) { $0.resize(columns: preset.columns, rows: preset.rows) }
    }
}

/// The tiles of a view tab. Live video; in setup mode every tile takes a
/// dropped camera, can be dragged onto another tile to swap, and cleared.
private struct ViewTabGrid: View {
    @EnvironmentObject private var cameras: CameraDirectory
    let grid: WorkspaceGrid
    let setup: Bool
    @Binding var focused: String?
    let onChange: ((inout WorkspaceGrid) -> Void) -> Void

    private let spacing: CGFloat = 8

    var body: some View {
        GeometryReader { proxy in
            Group {
                if let id = focused, grid.cameraIDs.contains(id) {
                    tile(cameraID: id, index: grid.tileIndex(of: id) ?? 0).padding(spacing)
                } else {
                    layout(in: proxy.size)
                }
            }
            .frame(width: proxy.size.width, height: proxy.size.height)
        }
    }

    private func layout(in size: CGSize) -> some View {
        let cols = CGFloat(grid.columns), rows = CGFloat(grid.rows)
        let cellW = max((size.width - spacing * (cols + 1)) / cols, 40)
        let cellH = max((size.height - spacing * (rows + 1)) / rows, 30)
        return ZStack(alignment: .topLeading) {
            ForEach(Array(grid.slots.enumerated()), id: \.offset) { index, cameraID in
                Group {
                    if let cameraID {
                        tile(cameraID: cameraID, index: index)
                    } else {
                        emptyTile(index: index)
                    }
                }
                .frame(width: cellW, height: cellH)
                .offset(x: spacing + CGFloat(index % grid.columns) * (cellW + spacing),
                        y: spacing + CGFloat(index / grid.columns) * (cellH + spacing))
            }
        }
        .frame(width: size.width, height: size.height, alignment: .topLeading)
    }

    @ViewBuilder
    private func tile(cameraID: String, index: Int) -> some View {
        if let camera = cameras.camera(cameraID) {
            LiveTile(camera: camera, showsControls: setup == false)
                .overlay { if setup { SetupOutline() } }
                .overlay(alignment: .topTrailing) {
                    if setup {
                        Button { onChange { $0.clearTile(index) } } label: {
                            Image(systemName: "xmark.circle.fill").font(.system(size: 16))
                                .foregroundStyle(.white, .black.opacity(0.6))
                        }
                        .buttonStyle(.plain)
                        .padding(6)
                        .help("Remove from this tile")
                    }
                }
                .onTapGesture(count: 2) { if setup == false { focused = focused == nil ? cameraID : nil } }
                .modifier(SetupDrag(enabled: setup, cameraID: cameraID))
                .onDrop(of: [.plainText], isTargeted: nil) { providers in
                    guard setup else { return false }
                    return CameraDrag.decode(providers) { ids in onChange { $0.place(ids, startingAt: index) } }
                }
                .contextMenu {
                    if setup {
                        Button { onChange { $0.clearTile(index) } } label: { Label("Remove from Tile", systemImage: "xmark") }
                    } else {
                        Button { focused = cameraID } label: { Label("Fill Grid", systemImage: "arrow.up.left.and.arrow.down.right") }
                        Button { OperatorWindows.shared.openCamera(cameraID, name: camera.name) } label: {
                            Label("Pop Out Window", systemImage: "macwindow.on.rectangle")
                        }
                    }
                }
        } else {
            unavailableTile(index: index)
        }
    }

    private func unavailableTile(index: Int) -> some View {
        ZStack {
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).fill(Color.black.opacity(0.85))
            VStack(spacing: 6) {
                Image(systemName: "lock.slash").foregroundStyle(.secondary)
                Text("No access to this camera").font(.caption).foregroundStyle(.secondary)
            }
            if setup {
                Button("Clear") { onChange { $0.clearTile(index) } }.controlSize(.small).padding(.top, 60)
            }
        }
    }

    private func emptyTile(index: Int) -> some View {
        ViewTabDropTile(setup: setup) { ids in onChange { $0.place(ids, startingAt: index) } }
    }
}

private struct SetupDrag: ViewModifier {
    let enabled: Bool
    let cameraID: String

    func body(content: Content) -> some View {
        if enabled {
            content.onDrag { CameraDrag.provider([cameraID]) }
        } else {
            content
        }
    }
}

private struct SetupOutline: View {
    var body: some View {
        RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
            .strokeBorder(SentinelTheme.accent.opacity(0.7), style: StrokeStyle(lineWidth: 1.5, dash: [6, 4]))
            .allowsHitTesting(false)
    }
}

private struct ViewTabDropTile: View {
    let setup: Bool
    let onDrop: ([String]) -> Void
    @State private var isTargeted = false

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                .fill(Color.black.opacity(isTargeted ? 0.6 : 0.82))
            RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                .strokeBorder(isTargeted ? SentinelTheme.accent : Color.white.opacity(setup ? 0.3 : 0.14),
                              style: StrokeStyle(lineWidth: isTargeted ? 2 : 1, dash: [5, 4]))
            VStack(spacing: 6) {
                Image(systemName: "plus.rectangle.on.rectangle").foregroundStyle(.secondary)
                Text(setup ? "Drag a camera here" : "Empty tile").font(.caption).foregroundStyle(.secondary)
            }
        }
        .contentShape(Rectangle())
        .onDrop(of: [.plainText], isTargeted: $isTargeted) { providers in
            guard setup else { return false }
            return CameraDrag.decode(providers, completion: onDrop)
        }
    }
}
