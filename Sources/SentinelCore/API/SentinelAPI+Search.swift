import Foundation

// Forensic search calls (wave 5 stream B).

public extension SentinelAPI {
    /// `GET /events/search`: one page, newest first. Pass the previous page's
    /// `nextCursor` as `before`.
    func searchEvents(_ filter: SearchFilter, now: Date = Date(), limit: Int = SearchFilter.pageSize,
                      before: String? = nil) async throws -> V2.Page<V2.Event> {
        try await send("GET", "/api/v2/events/search", query: filter.queryItems(now: now, limit: limit, before: before))
    }

    /// `GET /events/facets` for the filter's cameras and time window.
    func eventFacets(_ filter: SearchFilter, now: Date = Date()) async throws -> V2.EventFacets {
        try await send("GET", "/api/v2/events/facets", query: filter.facetQueryItems(now: now))
    }

    /// `POST /events/ask`. 409 when AI is off, 429 at the daily limit.
    func askEvents(_ question: String) async throws -> V2.EventAnswer {
        struct Body: Encodable { var query: String }
        return try await send("POST", "/api/v2/events/ask", body: Body(query: question))
    }
}
