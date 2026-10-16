import Foundation
import SentinelCore

/// Every saved view the signed-in user can open: their own (Private) and
/// the Shared tree with its folders. Backs the sidebar's view tree, the
/// "+" picker and the view tabs. Read from `GET /api/v2/views` and
/// `GET /api/v2/view-groups`; refreshed after every change made here.
@MainActor
final class ViewLibrary: ObservableObject {
    @Published private(set) var privateViews: [V2.SavedView] = []
    @Published private(set) var sharedViews: [V2.SavedView] = []
    @Published private(set) var groups: [V2.ViewGroup] = []
    /// Both lists have been read at least once (an empty list is real).
    @Published private(set) var isLoaded = false
    @Published private(set) var lastError: String?

    /// The older layout-only store other windows read; kept in step.
    weak var mirror: SavedViewsStore?

    func view(_ id: String?) -> V2.SavedView? {
        guard let id else { return nil }
        return ViewTree.find(id, views: privateViews, shared: sharedViews)
    }

    /// Ids of every view, for noticing deletions.
    var allIDs: [String] { (privateViews + sharedViews).map(\.id) }

    var folderIDs: Set<String> { Set(groups.map(\.id)) }

    func reload(api: SentinelAPI) async {
        do {
            async let mine = api.savedViews()
            async let tree = api.sharedViewTree()
            let (own, shared) = try await (mine, tree)
            privateViews = own
            sharedViews = shared.views
            groups = shared.groups
            lastError = nil
            isLoaded = true
        } catch {
            lastError = error.localizedDescription
        }
    }

    private func refresh(api: SentinelAPI) async {
        await reload(api: api)
        if let mirror { await mirror.reload(api: api) }
    }

    /// Creates (`replacing: nil`) or replaces a view, then re-reads the lists.
    @discardableResult
    func save(_ input: V2.SavedViewInput, replacing id: String?, api: SentinelAPI) async throws -> V2.SavedView {
        let saved: V2.SavedView
        if let id {
            saved = try await api.replaceSavedView(id, input)
        } else {
            saved = try await api.createSavedView(input)
        }
        await refresh(api: api)
        return saved
    }

    func delete(_ view: V2.SavedView, api: SentinelAPI) async throws {
        try await api.deleteView(view.id)
        await refresh(api: api)
    }

    @discardableResult
    func createFolder(name: String, parentID: String, api: SentinelAPI) async throws -> V2.ViewGroup {
        let group = try await api.createViewGroup(V2.ViewGroupInput(name: name, parentId: parentID))
        await refresh(api: api)
        return group
    }

    func renameFolder(_ group: V2.ViewGroup, to name: String, api: SentinelAPI) async throws {
        _ = try await api.updateViewGroup(group.id, V2.ViewGroupInput(name: name, parentId: group.parentId, sortOrder: group.sortOrder))
        await refresh(api: api)
    }

    /// Only an empty folder; the server's 409 text explains otherwise.
    func deleteFolder(_ group: V2.ViewGroup, api: SentinelAPI) async throws {
        try await api.deleteViewGroup(group.id)
        await refresh(api: api)
    }
}

/// Feeds the notice bus from the live stream: recording servers and
/// cameras going offline and coming back. Called by `SiteRuntime` with each
/// message *before* the stores apply it, so the previous state is still there.
@MainActor
enum NoticeFeed {
    static func observe(_ message: V2.StreamMessage, cameras: CameraDirectory, center: NoticeCenter? = nil) {
        let center = center ?? NoticeCenter.shared
        switch message {
        case .cameraStatus(let status):
            guard let camera = cameras.camera(status.cameraId) else { return }
            center.post(NoticeRules.camera(name: camera.name, wasOnline: camera.status.online, online: status.online, id: camera.id))
        case .recorderStatus(let status):
            guard let recorder = cameras.recorders.first(where: { $0.id == status.recorderId }) else { return }
            center.post(NoticeRules.recorder(name: recorder.name, wasOnline: recorder.online, online: status.online, id: recorder.id))
        default:
            break
        }
    }
}
