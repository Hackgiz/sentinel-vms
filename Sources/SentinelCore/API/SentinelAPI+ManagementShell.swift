import Foundation

// The Management window's one call (wave 6 stream E): the site name for the
// tree root. Stream F owns the full site-info DTO/PUT; this decodes only `name`.

public extension SentinelAPI {
    /// `GET /api/v2/site` -> `name`, or "Sentinel" while the server 404s,
    /// answers an error, or the name is blank.
    func managementSiteName() async -> String {
        struct Site: Decodable { var name: String? }
        do {
            let site: Site = try await send("GET", "/api/v2/site")
            let name = site.name?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return name.isEmpty ? ManagementCatalog.fallbackSiteName : name
        } catch {
            return ManagementCatalog.fallbackSiteName
        }
    }
}
