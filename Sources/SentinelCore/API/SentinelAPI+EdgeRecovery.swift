import Foundation

public extension SentinelAPI {
    /// Starts pulling the camera's SD-card footage into the gaps of
    /// `[from, to)` (at most 24 h; ExportEvidence). Answers as soon as the
    /// recording server accepted the job — or with a finished job when there
    /// was nothing to recover. 400 / 409 / 429 are mapped by `EdgeRecoveryText.startError`.
    func startEdgeRecovery(cameraID: String, from: UnixMillis, to: UnixMillis) async throws -> V2.EdgeRecovery {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/edge-recovery",
                       body: V2.EdgeRecoveryRequest(fromMs: from, toMs: to))
    }

    /// A job's state (kept an hour after it finished).
    func edgeRecovery(taskID: String) async throws -> V2.EdgeRecovery {
        try await send("GET", "/api/v2/edge-recovery/\(Self.escape(taskID))")
    }

    /// Cancels a running job; segments already recovered stay.
    func cancelEdgeRecovery(taskID: String) async throws -> V2.EdgeRecovery {
        try await send("DELETE", "/api/v2/edge-recovery/\(Self.escape(taskID))")
    }
}
