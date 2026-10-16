import Foundation

/// Saved views with the shared-tree fields, and the folders of the Shared
/// tree. (The original `views()` / `createView` / `replaceView` calls keep
/// working for layout-only use; deleting a view uses the existing
/// `deleteView(_:)`.)
public extension SentinelAPI {
    /// `GET /api/v2/views`: the user's own (private) views.
    func savedViews() async throws -> [V2.SavedView] {
        let list: V2.Items<V2.SavedView> = try await send("GET", "/api/v2/views")
        return list.items
    }

    /// `POST /api/v2/views`; with `shared: true` this needs `manageViews`.
    func createSavedView(_ input: V2.SavedViewInput) async throws -> V2.SavedView {
        try await send("POST", "/api/v2/views", body: input)
    }

    func replaceSavedView(_ id: String, _ input: V2.SavedViewInput) async throws -> V2.SavedView {
        try await send("PUT", "/api/v2/views/\(Self.escape(id))", body: input)
    }

    /// `GET /api/v2/view-groups`: the Shared tree (flat folders + shared views).
    func sharedViewTree() async throws -> V2.SharedViewTree {
        try await send("GET", "/api/v2/view-groups")
    }

    func createViewGroup(_ input: V2.ViewGroupInput) async throws -> V2.ViewGroup {
        try await send("POST", "/api/v2/view-groups", body: input)
    }

    func updateViewGroup(_ id: String, _ input: V2.ViewGroupInput) async throws -> V2.ViewGroup {
        try await send("PUT", "/api/v2/view-groups/\(Self.escape(id))", body: input)
    }

    /// Only an empty folder; the server answers 409 otherwise.
    func deleteViewGroup(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/view-groups/\(Self.escape(id))")
    }
}
