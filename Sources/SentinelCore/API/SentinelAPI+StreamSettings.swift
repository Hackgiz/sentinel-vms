import Foundation

public extension SentinelAPI {
    /// `GET /api/v2/cameras/{id}/stream-settings` (manageCameras). 409 when
    /// the camera was added without ONVIF; 502 when it can't be reached.
    func streamSettings(cameraID: String) async throws -> V2.StreamSettings {
        try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/stream-settings")
    }

    /// `PUT …/stream-settings/{configToken}`. A camera that refuses the
    /// change answers 502 with its own error text in the message.
    func applyStreamSettings(cameraID: String, configToken: String, change: V2.StreamSettingsChange) async throws {
        try await sendNoContent("PUT", "/api/v2/cameras/\(Self.escape(cameraID))/stream-settings/\(Self.escape(configToken))", body: change)
    }
}
