import Foundation

// Recording modes (wave 22): the server-wide recording defaults. A server
// older than wave 22 answers 404, and the Mac then keeps the plain Record
// toggle (see RecordingModeLogic). Reads and writes need changeSettings.

public extension SentinelAPI {
    /// `GET /settings/recording`.
    func recordingSettings() async throws -> V2.RecordingSettings {
        try await send("GET", "/api/v2/settings/recording")
    }

    /// `PATCH /settings/recording`: absent fields are kept; answers the saved settings.
    func updateRecordingSettings(_ patch: V2.RecordingSettingsPatch) async throws -> V2.RecordingSettings {
        try await send("PATCH", "/api/v2/settings/recording", body: patch)
    }
}
