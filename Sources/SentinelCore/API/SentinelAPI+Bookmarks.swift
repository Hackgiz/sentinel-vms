import Foundation

public extension SentinelAPI {
    /// A camera's bookmarks overlapping `[from, to]`, oldest first. Either
    /// bound may be nil (no bound). A camera outside the caller's scope is 404.
    func bookmarks(cameraID: String, from: UnixMillis? = nil, to: UnixMillis? = nil) async throws -> [V2.Bookmark] {
        let list: V2.Items<V2.Bookmark> = try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/bookmarks", query: [
            URLQueryItem(name: "from", value: from.map(String.init)),
            URLQueryItem(name: "to", value: to.map(String.init))
        ])
        return list.items
    }

    /// Pins a moment (durationMs 0 or absent) or a range. Audited.
    func addBookmark(cameraID: String, _ input: V2.BookmarkInput) async throws -> V2.Bookmark {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/bookmarks", body: input)
    }

    /// Edits a bookmark (its author or a manager; 403 otherwise).
    func patchBookmark(id: String, _ patch: V2.BookmarkPatch) async throws -> V2.Bookmark {
        try await send("PATCH", "/api/v2/bookmarks/\(Self.escape(id))", body: patch)
    }

    func deleteBookmark(id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/bookmarks/\(Self.escape(id))")
    }
}
