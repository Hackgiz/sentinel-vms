import Foundation

// PTZ calls (wave 5 stream B).

public extension SentinelAPI {
    func ptzState(cameraID: String) async throws -> V2.PTZState {
        try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/ptz")
    }

    /// Starts a continuous move until `ptzStop`. Velocities are clamped to [-1, 1] first.
    @discardableResult
    func ptzMove(cameraID: String, _ move: V2.PTZMove) async throws -> V2.PTZMove {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/move", body: move.clamped)
    }

    func ptzStop(cameraID: String) async throws {
        try await sendNoContent("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/stop")
    }

    func ptzGoto(cameraID: String, presetToken: String) async throws {
        try await sendNoContent("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/presets/\(Self.escape(presetToken))/goto")
    }

    func ptzSavePreset(cameraID: String, name: String) async throws -> V2.PTZPreset {
        struct Body: Encodable { var name: String }
        return try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/presets", body: Body(name: name))
    }

    func ptzDeletePreset(cameraID: String, presetToken: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/presets/\(Self.escape(presetToken))")
    }
}
