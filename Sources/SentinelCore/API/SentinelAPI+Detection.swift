import Foundation

// Detection depth calls (wave 16 server, wave 17 Mac). Reads need viewLive;
// people, plates, faces/lpr/detector settings and model downloads need
// changeSettings. A server older than wave 16 answers 404 (see DetectionText).

public extension SentinelAPI {
    // MARK: Models and detector

    /// `GET /models`: the catalog with each model's state, and the models directory.
    func models() async throws -> V2.ModelCatalog {
        try await send("GET", "/api/v2/models")
    }

    /// `DELETE /models/{id}`: removes a downloaded model file (409 when bundled,
    /// selected as the detector, or downloading).
    func deleteModel(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/models/\(Self.escape(id))")
    }

    /// `POST /models/{id}/download`: starts the download, or answers with the running job.
    func downloadModel(_ id: String) async throws -> V2.ModelDownload {
        try await send("POST", "/api/v2/models/\(Self.escape(id))/download")
    }

    /// `DELETE /models/{id}/download`: cancels it; the partial file is deleted.
    func cancelModelDownload(_ id: String) async throws -> V2.ModelDownload {
        try await send("DELETE", "/api/v2/models/\(Self.escape(id))/download")
    }

    func detectorSettings() async throws -> V2.DetectorSettings {
        try await send("GET", "/api/v2/detector")
    }

    /// `PUT /detector`: model "" = yolox-tiny; rate 0 = auto, else 1–5.
    func updateDetector(_ input: V2.DetectorInput) async throws -> V2.DetectorSettings {
        try await send("PUT", "/api/v2/detector", body: input)
    }

    // MARK: Faces and people

    func facesStatus() async throws -> V2.FacesStatus {
        try await send("GET", "/api/v2/faces")
    }

    /// `PUT /faces`: 409 with the reason when turning it on without the models.
    func updateFaces(_ input: V2.FacesInput) async throws -> V2.FacesStatus {
        try await send("PUT", "/api/v2/faces", body: input)
    }

    /// Recent faces that matched nobody, newest first (changeSettings).
    func unassignedFaces(limit: Int = 60) async throws -> [V2.Face] {
        let list: V2.Items<V2.Face> = try await send("GET", "/api/v2/faces/unassigned",
                                                     query: [URLQueryItem(name: "limit", value: String(limit))])
        return list.items
    }

    func deleteFace(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/faces/\(Self.escape(id))")
    }

    /// The face crop, JPEG bytes (an authenticated load, like event thumbnails).
    func faceCrop(_ id: String) async throws -> Data {
        try await raw("GET", "/api/v2/faces/\(Self.escape(id))/crop.jpg").0
    }

    /// Faces found on an event's thumbnail, best first.
    func eventFaces(eventID: String) async throws -> [V2.Face] {
        let list: V2.Items<V2.Face> = try await send("GET", "/api/v2/events/\(Self.escape(eventID))/faces")
        return list.items
    }

    /// The people gallery, by name.
    func people() async throws -> [V2.Person] {
        let list: V2.Items<V2.Person> = try await send("GET", "/api/v2/people")
        return list.items
    }

    /// One person with their enrolled faces.
    func person(_ id: String) async throws -> V2.Person {
        try await send("GET", "/api/v2/people/\(Self.escape(id))")
    }

    /// `POST /people`, optionally enrolling `input.faceId`.
    func createPerson(_ input: V2.PersonInput) async throws -> V2.Person {
        try await send("POST", "/api/v2/people", body: input)
    }

    /// `PATCH /people/{id}`: a rename also updates `personName` on past events.
    func updatePerson(_ id: String, _ input: V2.PersonInput) async throws -> V2.Person {
        try await send("PATCH", "/api/v2/people/\(Self.escape(id))", body: input)
    }

    /// Deletes a person and their faces; past events keep the name.
    func deletePerson(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/people/\(Self.escape(id))")
    }

    /// Enrolls an unassigned face (409 when it already belongs to someone).
    func enrollFace(personID: String, faceID: String) async throws -> V2.Person {
        struct Body: Encodable { var faceId: String }
        return try await send("POST", "/api/v2/people/\(Self.escape(personID))/faces", body: Body(faceId: faceID))
    }

    // MARK: Plates

    func lprStatus() async throws -> V2.LPRStatus {
        try await send("GET", "/api/v2/lpr")
    }

    /// `PUT /lpr`: 409 with the reason when turning it on without the models.
    func setLPR(enabled: Bool) async throws -> V2.LPRStatus {
        struct Body: Encodable { var enabled: Bool }
        return try await send("PUT", "/api/v2/lpr", body: Body(enabled: enabled))
    }

    /// The watchlist, by plate.
    func plates() async throws -> [V2.Plate] {
        let list: V2.Items<V2.Plate> = try await send("GET", "/api/v2/plates")
        return list.items
    }

    /// One entry with its newest 20 sightings.
    func plate(_ id: String) async throws -> V2.Plate {
        try await send("GET", "/api/v2/plates/\(Self.escape(id))")
    }

    /// 409 when the plate is already listed.
    func createPlate(_ input: V2.PlateInput) async throws -> V2.Plate {
        try await send("POST", "/api/v2/plates", body: input)
    }

    func updatePlate(_ id: String, _ input: V2.PlateInput) async throws -> V2.Plate {
        try await send("PATCH", "/api/v2/plates/\(Self.escape(id))", body: input)
    }

    func deletePlate(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/plates/\(Self.escape(id))")
    }

    // MARK: Semantic search

    func semanticStatus() async throws -> V2.SemanticStatus {
        try await send("GET", "/api/v2/semantic")
    }

    /// Indexes the newest events without a semantic vector, or answers with the running job.
    func startSemanticBackfill() async throws -> V2.AppearanceBackfill {
        try await send("POST", "/api/v2/semantic/backfill")
    }

    func cancelSemanticBackfill() async throws -> V2.AppearanceBackfill {
        try await send("DELETE", "/api/v2/semantic/backfill")
    }

    /// `POST /events/semantic`: matches best first; 503 when no model is installed.
    func semanticSearch(_ request: V2.SemanticRequest) async throws -> V2.SemanticResults {
        try await send("POST", "/api/v2/events/semantic", body: request)
    }
}
