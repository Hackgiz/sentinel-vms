import Foundation

public extension SentinelAPI {
    /// Evidence cases, newest first, within the caller's camera scope.
    func evidenceCases() async throws -> [V2.EvidenceCase] {
        let list: V2.Items<V2.EvidenceCase> = try await send("GET", "/api/v2/evidence")
        return list.items
    }

    /// Locks one camera's range in the evidence vault. Synchronous on the
    /// server: answers once the recording server has cut the clip.
    func createEvidence(_ request: V2.EvidenceRequest) async throws -> V2.EvidenceCase {
        try await send("POST", "/api/v2/evidence", body: request)
    }

    func verifyEvidence(_ id: String) async throws -> V2.EvidenceVerification {
        try await send("POST", "/api/v2/evidence/\(Self.escape(id))/verify")
    }

    func deleteEvidence(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/evidence/\(Self.escape(id))")
    }

    /// Streams `…/download` to `destination` (the clip never sits in memory).
    /// Retries once with a refreshed token on `401`, like `raw`.
    func downloadEvidence(_ id: String, to destination: URL, session: URLSession = .shared) async throws {
        let target = url("/api/v2/evidence/\(Self.escape(id))/download")
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
