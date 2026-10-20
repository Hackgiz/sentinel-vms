import Foundation

// Zones & privacy calls (wave 11 stream E).

public extension SentinelAPI {
    /// The camera's detection zones, privacy masks and lines, in creation order.
    func zones(cameraID: String) async throws -> [V2.CameraZone] {
        let list: V2.Items<V2.CameraZone> = try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/zones")
        return list.items
    }

    /// Replaces the camera's full zone set atomically (ManageCameras) and
    /// answers the stored zones.
    func putZones(cameraID: String, _ zones: [V2.CameraZoneInput]) async throws -> [V2.CameraZone] {
        let list: V2.Items<V2.CameraZone> = try await send("PUT", "/api/v2/cameras/\(Self.escape(cameraID))/zones",
                                                          body: V2.CameraZonesPut(zones: zones))
        return list.items
    }
}
