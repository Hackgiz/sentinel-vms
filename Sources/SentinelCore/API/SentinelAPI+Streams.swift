import Foundation

// Wave 22 addendum: find a camera's second stream. The recording server that
// owns the camera does the network work. A server older than the addendum
// answers 404 (the Mac then hides Find streams); a camera that can't be
// reached or rejects the login answers 422, surfaced as
// `SentinelAPIError.server` with the server's message (`isAuthFailed` for a
// rejected login). Both calls need manageCameras.

public extension SentinelAPI {
    /// `POST /cameras/{id}/streams/discover`. Leave both credentials nil to use the
    /// camera's saved login.
    func discoverStreams(cameraID: String, username: String? = nil, password: String? = nil) async throws -> V2.StreamDiscovery {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/streams/discover",
                       body: V2.StreamDiscoverRequest(username: username, password: password))
    }

    /// `POST /cameras/{id}/streams/resize`: sets an encoder's resolution over ONVIF
    /// and answers the updated stream.
    func resizeStream(cameraID: String, encoderToken: String, width: Int, height: Int, fps: Int? = nil) async throws -> V2.DiscoveredStream {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/streams/resize",
                       body: V2.StreamResizeRequest(encoderToken: encoderToken, width: width, height: height, fps: fps))
    }
}
