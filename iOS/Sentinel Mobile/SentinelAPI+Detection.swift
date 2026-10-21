// SentinelAPI+Detection.swift
// The wave 16 detection calls the phone makes (WAVE17-CLIENT-PARITY.md,
// iPhone). All reads need viewLive; the phone changes nothing here.

import Foundation

extension SentinelAPI {
    /// `GET /api/v2/people`: the gallery, by name. 404 from a server older than wave 16.
    func people() async throws -> [V2.Person] {
        let page: V2.Items<V2.Person> = try await send("GET", "/api/v2/people")
        return page.items
    }

    /// `GET /api/v2/people/{personId}`: one person with their enrolled faces.
    func person(_ id: String) async throws -> V2.Person {
        try await send("GET", "/api/v2/people/\(Self.escape(id))")
    }

    /// `GET /api/v2/faces`: face recognition status.
    func facesStatus() async throws -> V2.FacesStatus {
        try await send("GET", "/api/v2/faces")
    }

    /// `GET /api/v2/faces/{faceId}/crop.jpg` (camera scope applies through the event).
    func faceCrop(_ id: String) async throws -> Data {
        try await raw("GET", "/api/v2/faces/\(Self.escape(id))/crop.jpg").0
    }

    /// `GET /api/v2/semantic`: whether "Describe it" can run.
    func semanticStatus() async throws -> V2.SemanticStatus {
        try await send("GET", "/api/v2/semantic")
    }

    /// `POST /api/v2/events/semantic`. 400 for a bad description, 503 with the
    /// reason when no model is installed.
    func semanticSearch(_ body: V2.SemanticSearch) async throws -> V2.SemanticResults {
        try await send("POST", "/api/v2/events/semantic", body: body)
    }
}
