import Foundation

// Appearance search calls ("Find similar"). Wave 11 stream C.

public extension SentinelAPI {
    /// `GET /appearance`: whether a re-identification model is installed, how
    /// many events are indexed, and the latest index job.
    func appearanceStatus() async throws -> V2.AppearanceStatus {
        try await send("GET", "/api/v2/appearance")
    }

    /// `POST /events/{id}/similar`: matches best first. 503 when no model is
    /// installed, 400 when the event has no usable box, 409 when its snapshot
    /// can't be fetched.
    func similarEvents(eventID: String, request: V2.SimilarEventsRequest = .init()) async throws -> V2.SimilarEvents {
        try await send("POST", "/api/v2/events/\(Self.escape(eventID))/similar", body: request)
    }

    /// `POST /appearance/backfill` (changeSettings): starts indexing the newest
    /// events without a vector, or answers with the job already running.
    func startAppearanceBackfill() async throws -> V2.AppearanceBackfill {
        try await send("POST", "/api/v2/appearance/backfill")
    }

    /// `DELETE /appearance/backfill` (changeSettings): stops the running job.
    func cancelAppearanceBackfill() async throws -> V2.AppearanceBackfill {
        try await send("DELETE", "/api/v2/appearance/backfill")
    }
}
