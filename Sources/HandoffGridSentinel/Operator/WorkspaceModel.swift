import AppKit
import SwiftUI
import SentinelCore

/// Live or recorded video: flips the whole workspace.
enum WorkspaceMode: String, CaseIterable, Identifiable {
    case live = "Live"
    case playback = "Playback"

    var id: String { rawValue }
}

/// What a workspace window shows and remembers: the camera grid, the
/// selection, and the sidebar. Persisted per site (and per window for
/// monitor windows) so a relaunch or a site switch comes back as left.
@MainActor
final class WorkspaceModel: ObservableObject {
    static let defaultSidebarWidth: CGFloat = 260
    static let sidebarWidthRange: ClosedRange<CGFloat> = 200...420

    @Published private(set) var grid = WorkspaceGrid(columns: 3, rows: 3)
    /// The saved view the grid was opened from (cleared when the grid changes).
    @Published private(set) var activeViewID: String?
    @Published var selectedCameraID: String?
    @Published var focusedCameraID: String?

    @Published var sidebarVisible = true { didSet { save() } }
    @Published var sidebarWidth = WorkspaceModel.defaultSidebarWidth { didSet { save() } }
    @Published var grouping: CameraTree.Grouping = .server { didSet { save() } }
    @Published var collapsedParents: Set<String> = [] { didSet { save() } }
    @Published var fillTiles = false { didSet { save() } }

    private let windowID: String
    private var siteID: String?
    private var isRestoring = false
    /// Set once the grid has been filled from the site's cameras the first time.
    private var hasSeeded = false

    private struct Stored: Codable {
        var grid: WorkspaceGrid
        var activeViewID: String?
        var sidebarVisible: Bool
        var sidebarWidth: Double
        var grouping: CameraTree.Grouping
        var collapsedParents: [String]
        var fillTiles: Bool
        var hasSeeded: Bool
    }

    init(windowID: String = "main") {
        self.windowID = windowID
    }

    private var storageKey: String? {
        siteID.map { "sentinel.workspace.\($0).\(windowID)" }
    }

    /// Loads this site's saved layout and sidebar state.
    func bind(siteID: String) {
        guard self.siteID != siteID else { return }
        self.siteID = siteID
        isRestoring = true
        defer { isRestoring = false }
        guard let key = storageKey,
              let data = UserDefaults.standard.data(forKey: key),
              let stored = try? JSONDecoder().decode(Stored.self, from: data) else { return }
        grid = stored.grid
        activeViewID = stored.activeViewID
        sidebarVisible = stored.sidebarVisible
        sidebarWidth = min(max(CGFloat(stored.sidebarWidth), Self.sidebarWidthRange.lowerBound), Self.sidebarWidthRange.upperBound)
        grouping = stored.grouping
        collapsedParents = Set(stored.collapsedParents)
        fillTiles = stored.fillTiles
        hasSeeded = stored.hasSeeded
    }

    private func save() {
        guard isRestoring == false, let key = storageKey else { return }
        let stored = Stored(grid: grid, activeViewID: activeViewID, sidebarVisible: sidebarVisible,
                            sidebarWidth: Double(sidebarWidth), grouping: grouping,
                            collapsedParents: collapsedParents.sorted(), fillTiles: fillTiles, hasSeeded: hasSeeded)
        if let data = try? JSONEncoder().encode(stored) { UserDefaults.standard.set(data, forKey: key) }
    }

    // MARK: Grid

    var cameraIDs: [String] { grid.cameraIDs }

    /// First visit to a site: show its cameras instead of an empty wall.
    func seedIfNeeded(with cameraIDs: [String], limit: Int = 9) {
        guard hasSeeded == false, cameraIDs.isEmpty == false else { return }
        hasSeeded = true
        if grid.cameraIDs.isEmpty { grid.fill(with: Array(cameraIDs.prefix(limit))) }
        save()
    }

    /// Cameras the user may no longer see leave the grid.
    func prune(available: Set<String>) {
        var next = grid
        next.prune { available.contains($0) }
        guard next != grid else { return }
        grid = next
        save()
    }

    private func edit(keepingView: Bool = false, _ change: (inout WorkspaceGrid) -> Void) {
        var next = grid
        change(&next)
        guard next != grid else { return }
        grid = next
        if keepingView == false { activeViewID = nil }
        if let focused = focusedCameraID, next.cameraIDs.contains(focused) == false { focusedCameraID = nil }
        save()
    }

    func place(_ cameraIDs: [String], at index: Int) {
        edit { $0.place(cameraIDs, startingAt: index) }
        selectedCameraID = cameraIDs.first
    }

    /// Double-click in the sidebar: the first free tile (or just select it if it's already on the grid).
    func open(_ cameraID: String) {
        if grid.tileIndex(of: cameraID) == nil, grid.freeCount == 0 {
            // Full: grow to the next layout that has room.
            let size = WorkspaceGrid.dimensions(forCount: grid.cameraIDs.count + 1)
            edit { $0.resize(columns: max(size.columns, $0.columns), rows: max(size.rows, $0.rows)) }
        }
        edit { $0.placeInFirstFree(cameraID) }
        selectedCameraID = cameraID
    }

    /// Show `cameraID` (e.g. from an alarm): select it, adding it to the grid if it isn't there.
    func reveal(_ cameraID: String) {
        open(cameraID)
    }

    /// "Open as grid": the selected cameras fill a layout of their own.
    func openAsGrid(_ cameraIDs: [String]) {
        guard cameraIDs.isEmpty == false else { return }
        edit { $0.fill(with: cameraIDs) }
        selectedCameraID = cameraIDs.first
    }

    func setLayout(columns: Int, rows: Int) {
        edit { $0.resize(columns: columns, rows: rows) }
    }

    func clearTile(_ index: Int) {
        edit { $0.clearTile(index) }
    }

    func clearGrid() {
        edit { $0.clearAll() }
        selectedCameraID = nil
    }

    func openView(_ view: V2.View) {
        grid = WorkspaceGrid(view: view)
        activeViewID = view.id
        focusedCameraID = nil
        save()
    }

    /// Marks the grid as matching `view` again (after saving it).
    func adopt(_ view: V2.View) {
        activeViewID = view.id
        save()
    }

    func toggleParent(_ id: String) {
        if collapsedParents.contains(id) { collapsedParents.remove(id) } else { collapsedParents.insert(id) }
    }
}

/// Camera ids being dragged between the sidebar and the grid.
enum CameraDrag {
    private static let prefix = "sentinel-cameras:"

    static func provider(_ ids: [String]) -> NSItemProvider {
        NSItemProvider(object: (prefix + ids.joined(separator: ",")) as NSString)
    }

    static func decode(_ providers: [NSItemProvider], completion: @escaping ([String]) -> Void) -> Bool {
        guard let provider = providers.first(where: { $0.canLoadObject(ofClass: NSString.self) }) else { return false }
        _ = provider.loadObject(ofClass: NSString.self) { object, _ in
            guard let text = object as? String, text.hasPrefix(prefix) else { return }
            let ids = text.dropFirst(prefix.count).split(separator: ",").map(String.init)
            DispatchQueue.main.async { completion(ids) }
        }
        return true
    }
}
