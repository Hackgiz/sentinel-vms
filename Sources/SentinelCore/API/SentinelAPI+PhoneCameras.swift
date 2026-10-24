import Foundation

// Wave 23 phone cameras. The iPhone creates them (it keeps the push key in
// its Keychain); the Mac rotates or revokes the key. All need manageCameras.
// A server older than wave 23 answers 404.

public extension SentinelAPI {
    /// `POST /phone-cameras`: creates an iPhone camera and answers its push key (shown once).
    func createPhoneCamera(_ input: V2.PhoneCameraInput) async throws -> V2.PhonePushKey {
        try await send("POST", "/api/v2/phone-cameras", body: input)
    }

    /// `POST /cameras/{id}/push-key`: a new push key. The old one stops at once, so the
    /// phone stops streaming until it is set up again.
    func rotatePushKey(cameraID: String) async throws -> V2.PhonePushKey {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/push-key")
    }

    /// `DELETE /cameras/{id}/push-key`: the phone can't stream until a new key is made.
    func revokePushKey(cameraID: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/cameras/\(Self.escape(cameraID))/push-key")
    }
}
