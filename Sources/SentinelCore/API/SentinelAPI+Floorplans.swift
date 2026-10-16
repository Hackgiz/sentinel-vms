import Foundation

// Floorplan calls (wave 5 stream B).

/// The answer to a conditional image fetch.
public struct FloorplanImageResponse: Sendable, Equatable {
    /// nil when the server answered 304 (the cached copy is current).
    public var data: Data?
    public var etag: String?
    public var notModified: Bool { data == nil }
}

public extension SentinelAPI {
    /// Floorplans in the server's order, each with its (scope-filtered) pins.
    func floorplans() async throws -> [V2.Floorplan] {
        let list: V2.Items<V2.Floorplan> = try await send("GET", "/api/v2/floorplans")
        return list.items.sorted { ($0.sortOrder, $0.name) < ($1.sortOrder, $1.name) }
    }

    /// `GET /floorplans/{id}/image` with `If-None-Match`. `SentinelAPI.raw`
    /// treats 304 as an error and can't carry the header, so this builds its
    /// own request with the same bearer token (and one refresh on 401).
    /// `session` defaults to the same `.shared` session the client uses.
    func floorplanImage(id: String, ifNoneMatch etag: String? = nil,
                        session: URLSession = .shared) async throws -> FloorplanImageResponse {
        let target = url("/api/v2/floorplans/\(Self.escape(id))/image")
        func fetch(_ token: String) async throws -> (Data, HTTPURLResponse) {
            var request = URLRequest(url: target)
            request.httpMethod = "GET"
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            if let etag { request.setValue(etag, forHTTPHeaderField: "If-None-Match") }
            do {
                let (data, response) = try await session.data(for: request)
                guard let http = response as? HTTPURLResponse else {
                    throw SentinelAPIError.transport(message: "The server's answer wasn't HTTP.")
                }
                return (data, http)
            } catch let error as SentinelAPIError {
                throw error
            } catch let error as URLError {
                throw SentinelAPIError.transport(message: Self.describe(error))
            }
        }
        var (data, response) = try await fetch(try await accessToken())
        if response.statusCode == 401 {
            (data, response) = try await fetch(try await accessToken(forceRefresh: true))
        }
        switch response.statusCode {
        case 304:
            return FloorplanImageResponse(data: nil, etag: etag)
        case 200..<300:
            return FloorplanImageResponse(data: data, etag: response.value(forHTTPHeaderField: "ETag"))
        default:
            throw SentinelAPIError.from(status: response.statusCode, body: data)
        }
    }
}
