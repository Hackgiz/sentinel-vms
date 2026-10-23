import Foundation

public extension SentinelAPI {
    /// `GET /api/v2/reports/activity` (viewLive): event counts per camera,
    /// kind and hour and alarm counts per camera, kind and severity, within
    /// the caller's camera scope. The server caps the range at 31 days; nil
    /// bounds leave its default, the last 24 hours.
    func activityReport(from: UnixMillis? = nil, to: UnixMillis? = nil) async throws -> V2.Activity {
        try await send("GET", "/api/v2/reports/activity", query: [
            URLQueryItem(name: "from", value: from.map(String.init)),
            URLQueryItem(name: "to", value: to.map(String.init))
        ])
    }
}
