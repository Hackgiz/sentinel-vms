import Foundation

// `/api/v2/incidents` calls.

public extension SentinelAPI {
    func incidentsPage(filter: IncidentFilter = IncidentFilter(), limit: Int = 100, before: String? = nil) async throws -> V2.Page<V2.Incident> {
        try await send("GET", "/api/v2/incidents", query: filter.queryItems(limit: limit, before: before))
    }

    /// One incident with its items resolved to live objects (`restricted` / `missing` flagged).
    func incident(_ id: String) async throws -> V2.IncidentDetail {
        try await send("GET", "/api/v2/incidents/\(Self.escape(id))")
    }

    func createIncident(title: String, severity: V2.IncidentSeverity? = nil) async throws -> V2.Incident {
        try await send("POST", "/api/v2/incidents", body: V2.NewIncident(title: title, severity: severity))
    }

    func updateIncident(_ id: String, _ patch: V2.IncidentPatch) async throws -> V2.Incident {
        try await send("PATCH", "/api/v2/incidents/\(Self.escape(id))", body: patch)
    }

    /// Adds an alarm, event, evidence case or note. 409 when already in the incident.
    @discardableResult
    func addIncidentItem(_ id: String, _ item: V2.IncidentItemInput) async throws -> V2.IncidentItem {
        try await send("POST", "/api/v2/incidents/\(Self.escape(id))/items", body: item)
    }

    func removeIncidentItem(_ id: String, itemID: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/incidents/\(Self.escape(id))/items/\(Self.escape(itemID))")
    }

    /// AI-drafted markdown report; the draft replaces `summary`. 409 when AI
    /// is off, 429 at the daily limit (see `AIFailure`).
    func generateIncidentReport(_ id: String) async throws -> V2.Incident {
        try await send("POST", "/api/v2/incidents/\(Self.escape(id))/report")
    }
}
