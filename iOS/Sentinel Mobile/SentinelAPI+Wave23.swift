// SentinelAPI+Wave23.swift
// WAVE23 (stream I): phone cameras (§A) and video uploads into the evidence
// vault (§B) of WAVE23-PHONE-CAMERA.md. The server rejects unknown JSON
// fields, so the request bodies carry exactly the contract's fields.

import Foundation

extension V2 {
    /// `POST /api/v2/phone-cameras`.
    struct PhoneCameraCreate: Codable, Hashable, Sendable {
        var name: String
        var recorderId: String?
    }

    /// The new phone camera and its push key (shown once).
    struct PhoneCameraCreated: Codable, Hashable, Sendable {
        var camera: Camera
        var pushKey: String
    }

    /// `POST /api/v2/cameras/{id}/push-key` (rotate).
    struct PushKeyReply: Codable, Hashable, Sendable {
        var pushKey: String
    }

    /// `POST /api/v2/cameras/{id}/push-token`: like a video token, right `push`.
    struct PushToken: Codable, Hashable, Sendable {
        var token: String
        var baseUrl: String
        var gatewayUrl: String
        var recorderId: String
        var expiresAt: UnixMillis
    }

    /// `POST {base}/push/{cameraId}/sessions`.
    struct PushSessionRequest: Hashable, Sendable, Encodable {
        var video: String = "h264"
        var width: Int
        var height: Int
        var fps: Int
        /// "aac" or nil (sent as JSON `null`).
        var audio: String?
        /// The phone's name, shown as "Streaming from <device>" (server default "iPhone").
        var device: String? = nil

        private enum CodingKeys: String, CodingKey { case video, width, height, fps, audio, device }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(video, forKey: .video)
            try c.encode(width, forKey: .width)
            try c.encode(height, forKey: .height)
            try c.encode(fps, forKey: .fps)
            if let audio { try c.encode(audio, forKey: .audio) } else { try c.encodeNil(forKey: .audio) }
            try c.encodeIfPresent(device, forKey: .device)
        }
    }

    struct PushSession: Codable, Hashable, Sendable {
        var sessionId: String
        var maxSegmentBytes: Int?
    }

    /// `POST /api/v2/evidence/uploads`.
    struct UploadStart: Codable, Hashable, Sendable {
        var fileName: String
        var size: Int64
        var sha256: String
        /// "video/mp4" | "video/quicktime".
        var contentType: String
        var title: String?
        var note: String?
        var recordedAt: UnixMillis?
        var cameraId: String?
        var eventId: String?
        var alarmId: String?
        var incidentId: String?
    }

    struct UploadCreated: Codable, Hashable, Sendable {
        var uploadId: String
        var partSize: Int64?
    }

    /// What the phone needs from the evidence item `complete` returns. The
    /// rest of an uploaded item (no case, no camera) doesn't fit `Evidence`.
    struct UploadedEvidence: Codable, Hashable, Sendable {
        var id: String
        var title: String?
        var sha256: String?
    }

    /// The Sentinel Cloud feature flags of `GET /api/v2/cloud` (wave 23
    /// addenda 1 and 2). Absent on older servers.
    struct CloudFeatures: Codable, Hashable, Sendable {
        var uploadsAvailable: Bool?
        var phoneCamerasAvailable: Bool?
    }
}

extension SentinelAPIError {
    /// 402 `cloud_required`: uploads are a Sentinel Cloud feature.
    var isCloudRequired: Bool { status == 402 || code == "cloud_required" }
}

extension SentinelAPI {
    // Cameras

    func camera(_ id: String) async throws -> V2.Camera {
        try await send("GET", "/api/v2/cameras/\(Self.escape(id))")
    }

    // Phone cameras

    func createPhoneCamera(name: String, recorderID: String? = nil) async throws -> V2.PhoneCameraCreated {
        try await send("POST", "/api/v2/phone-cameras", body: V2.PhoneCameraCreate(name: name, recorderId: recorderID))
    }

    func rotatePushKey(cameraID: String) async throws -> V2.PushKeyReply {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/push-key")
    }

    /// Authenticated by the push key alone: works after the sign-in ended.
    func pushToken(cameraID: String, pushKey: String) async throws -> V2.PushToken {
        let (data, _) = try await raw("POST", "/api/v2/cameras/\(Self.escape(cameraID))/push-token",
                                      authorized: false, headers: ["X-Sentinel-Push-Key": pushKey])
        return try Self.decode(V2.PushToken.self, from: data)
    }

    // Uploads

    func cloudFeatures() async throws -> V2.CloudFeatures {
        try await send("GET", "/api/v2/cloud")
    }

    func startUpload(_ start: V2.UploadStart, userAgent: String) async throws -> V2.UploadCreated {
        let body = try Self.jsonEncoder.encode(start)
        let (data, _) = try await raw("POST", "/api/v2/evidence/uploads", body: body,
                                      contentType: "application/json", headers: ["User-Agent": userAgent])
        return try Self.decode(V2.UploadCreated.self, from: data)
    }

    func uploadPart(uploadID: String, index: Int, data: Data, userAgent: String,
                    progress: URLSessionTaskDelegate? = nil) async throws {
        _ = try await raw("PUT", "/api/v2/evidence/uploads/\(Self.escape(uploadID))/parts/\(index)", body: data,
                          contentType: "application/octet-stream", headers: ["User-Agent": userAgent],
                          taskDelegate: progress, minimumTimeout: 120)
    }

    func completeUpload(uploadID: String, userAgent: String) async throws -> V2.UploadedEvidence {
        // The server reads the whole file back to check it: allow time.
        // A big file may still be filing after 60 s: the server answers 202
        // {"status":"processing"} and the same call is repeated until it's done.
        for attempt in 0..<60 {
            let (data, response) = try await raw("POST", "/api/v2/evidence/uploads/\(Self.escape(uploadID))/complete",
                                                 headers: ["User-Agent": userAgent], minimumTimeout: 300)
            if response.statusCode != 202 { return try Self.decode(V2.UploadedEvidence.self, from: data) }
            try await Task.sleep(nanoseconds: UInt64(min(5, 1 + attempt)) * 1_000_000_000)
        }
        throw URLError(.timedOut)
    }

    func abortUpload(uploadID: String) async throws {
        _ = try await raw("DELETE", "/api/v2/evidence/uploads/\(Self.escape(uploadID))")
    }
}
