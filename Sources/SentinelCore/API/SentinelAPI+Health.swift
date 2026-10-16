import Foundation

public extension SentinelAPI {
    /// `GET /api/v2/system/health` (viewLive): every recording server with
    /// metrics, plus per-camera health within the caller's scope.
    func systemHealth() async throws -> [V2.ServerHealth] {
        let health: V2.SystemHealth = try await send("GET", "/api/v2/system/health")
        return health.servers
    }
}
