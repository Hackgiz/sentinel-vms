import Foundation

// Wave 11 (stream B): long exports, scheduled exports, evidence packages and
// share links. File downloads stream to disk like `downloadEvidence`.

public extension SentinelAPI {
    // MARK: Long exports

    /// Starts a long export (≤ 12 h). Answers once the recording server accepted the job.
    func startExportJob(_ request: V2.ExportJobRequest) async throws -> V2.ExportJob {
        try await send("POST", "/api/v2/export-jobs", body: request)
    }

    func exportJob(taskID: String) async throws -> V2.ExportJob {
        try await send("GET", "/api/v2/export-jobs/\(Self.escape(taskID))")
    }

    /// Cancels a running long export; answers with the stopped job.
    func cancelExportJob(taskID: String) async throws -> V2.ExportJob {
        try await send("DELETE", "/api/v2/export-jobs/\(Self.escape(taskID))")
    }

    /// Streams a finished download-destination job's MP4 to `destination`.
    func downloadExportJob(taskID: String, to destination: URL, session: URLSession = .shared) async throws {
        try await streamDownload("/api/v2/export-jobs/\(Self.escape(taskID))/download", to: destination, session: session)
    }

    // MARK: Evidence with options

    /// `POST /api/v2/evidence` with the watermark / pixelate options.
    func createEvidence(withOptions request: V2.EvidenceOptionsRequest) async throws -> V2.EvidenceCase {
        try await send("POST", "/api/v2/evidence", body: request)
    }

    // MARK: Evidence package

    /// Streams the case's self-verifying zip (`…/package`) to `destination`.
    func downloadEvidencePackage(id: String, to destination: URL, session: URLSession = .shared) async throws {
        try await streamDownload("/api/v2/evidence/\(Self.escape(id))/package", to: destination, session: session)
    }

    // MARK: Scheduled exports

    func exportSchedules() async throws -> [V2.ExportSchedule] {
        let list: V2.Items<V2.ExportSchedule> = try await send("GET", "/api/v2/export-schedules")
        return list.items
    }

    func createExportSchedule(_ input: V2.ExportScheduleInput) async throws -> V2.ExportSchedule {
        try await send("POST", "/api/v2/export-schedules", body: input)
    }

    func patchExportSchedule(id: String, _ patch: V2.ExportSchedulePatch) async throws -> V2.ExportSchedule {
        try await send("PATCH", "/api/v2/export-schedules/\(Self.escape(id))", body: patch)
    }

    func deleteExportSchedule(id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/export-schedules/\(Self.escape(id))")
    }

    // MARK: Share links

    /// Creates an expiring link; the `url`/`token` are returned only here.
    func shareEvidence(id: String, _ request: V2.ShareLinkRequest) async throws -> V2.ShareLinkCreated {
        try await send("POST", "/api/v2/evidence/\(Self.escape(id))/share", body: request)
    }

    /// Share links, newest first, including expired and revoked ones.
    func shareLinks() async throws -> [V2.ShareLink] {
        let list: V2.Items<V2.ShareLink> = try await send("GET", "/api/v2/share-links")
        return list.items
    }

    /// Revokes a link (idempotent).
    func revokeShareLink(id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/share-links/\(Self.escape(id))")
    }

    // MARK: Streaming

    /// Streams `path` to `destination` (the file never sits in memory).
    /// Retries once with a refreshed token on `401`, like `raw`.
    private func streamDownload(_ path: String, to destination: URL, session: URLSession) async throws {
        let target = url(path)
        var forceRefresh = false
        while true {
            let token = try await accessToken(forceRefresh: forceRefresh)
            var request = URLRequest(url: target)
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            let temp: URL
            let response: URLResponse
            do {
                (temp, response) = try await session.download(for: request)
            } catch let error as URLError {
                throw SentinelAPIError.transport(message: Self.describe(error))
            }
            guard let http = response as? HTTPURLResponse else {
                try? FileManager.default.removeItem(at: temp)
                throw SentinelAPIError.transport(message: "The server's answer wasn't HTTP.")
            }
            if http.statusCode == 401, forceRefresh == false {
                try? FileManager.default.removeItem(at: temp)
                forceRefresh = true
                continue
            }
            guard (200..<300).contains(http.statusCode) else {
                let body = (try? Data(contentsOf: temp)) ?? Data()
                try? FileManager.default.removeItem(at: temp)
                throw SentinelAPIError.from(status: http.statusCode, body: body)
            }
            try? FileManager.default.removeItem(at: destination)
            try FileManager.default.moveItem(at: temp, to: destination)
            return
        }
    }
}
