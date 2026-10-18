import Foundation

/// Video wall layouts (Wave 10, stream A).
public extension SentinelAPI {
    /// `GET /api/v2/wall-layouts`: the user's own layouts and every shared one.
    func wallLayouts() async throws -> [V2.WallLayout] {
        let list: V2.Items<V2.WallLayout> = try await send("GET", "/api/v2/wall-layouts")
        return list.items
    }

    func wallLayout(_ id: String) async throws -> V2.WallLayout {
        try await send("GET", "/api/v2/wall-layouts/\(Self.escape(id))")
    }

    /// `POST /api/v2/wall-layouts`; `shared: true` needs `manageViews`.
    func createWallLayout(_ input: V2.WallLayoutInput) async throws -> V2.WallLayout {
        try await send("POST", "/api/v2/wall-layouts", body: input)
    }

    func updateWallLayout(_ id: String, _ patch: V2.WallLayoutPatch) async throws -> V2.WallLayout {
        try await send("PATCH", "/api/v2/wall-layouts/\(Self.escape(id))", body: patch)
    }

    func deleteWallLayout(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/wall-layouts/\(Self.escape(id))")
    }
}
