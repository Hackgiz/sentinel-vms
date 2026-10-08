import Foundation

// MARK: - Backend configuration
//
// Filled in once the dedicated Supabase project exists. Overridable at runtime
// via Info.plist keys so a test build can point at a staging project.
public enum SentinelBackendConfig {
    public static let supabaseURL: URL = {
        let override = Bundle.main.object(forInfoDictionaryKey: "SentinelSupabaseURL") as? String
        return URL(string: override ?? "https://bfmpsvpnxjjrbvbcelqd.supabase.co")!
    }()

    public static let supabaseAnonKey: String = {
        Bundle.main.object(forInfoDictionaryKey: "SentinelSupabaseAnonKey") as? String
            ?? "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6ImJmbXBzdnBueGpqcmJ2YmNlbHFkIiwicm9sZSI6ImFub24iLCJpYXQiOjE3ODAxMDExNjQsImV4cCI6MjA5NTY3NzE2NH0.xOqHs_58thqRdaywrv4SXX0CqGrowUwGOGZO-K5FjRQ"
    }()

    /// How long a cached "active" entitlement keeps working with no network /
    /// failed renewal before the app falls back to the free tier. A 24/7 VMS
    /// must not stop recording because of a transient outage.
    public static let offlineGraceInterval: TimeInterval = 30 * 24 * 60 * 60
}

// MARK: - Models

public struct SentinelAccount: Codable, Equatable {
    public let userID: String
    public let email: String
}

public enum LicenseStatus: String, Codable {
    case free         // no paid subscription
    case active
    case pastDue      = "past_due"
    case canceled
    case inactive
}

/// Snapshot of entitlement persisted to disk for offline grace.
public struct LicenseSnapshot: Codable, Equatable {
    public var status: LicenseStatus
    public var licensedCameras: Int        // Stripe subscription quantity (paid cameras)
    public var currentPeriodEnd: Date?
    public var fetchedAt: Date

    // Monthly AI plan ($4.99, 1000 described alerts). Independent of camera licensing.
    public var aiStatus: LicenseStatus = .free
    public var aiAlertsUsed: Int = 0
    public var aiPeriodEnd: Date?

    // Monthly remote-access plan ($3.99). Gates off-LAN / cellular viewing
    // (the Cloudflare tunnel). Independent of camera licensing and AI.
    public var remoteStatus: LicenseStatus = .free
    public var remotePeriodEnd: Date?

    public init(status: LicenseStatus, licensedCameras: Int, currentPeriodEnd: Date?, fetchedAt: Date,
                aiStatus: LicenseStatus = .free, aiAlertsUsed: Int = 0, aiPeriodEnd: Date? = nil,
                remoteStatus: LicenseStatus = .free, remotePeriodEnd: Date? = nil) {
        self.status = status
        self.licensedCameras = licensedCameras
        self.currentPeriodEnd = currentPeriodEnd
        self.fetchedAt = fetchedAt
        self.aiStatus = aiStatus
        self.aiAlertsUsed = aiAlertsUsed
        self.aiPeriodEnd = aiPeriodEnd
        self.remoteStatus = remoteStatus
        self.remotePeriodEnd = remotePeriodEnd
    }

    enum CodingKeys: String, CodingKey {
        case status, licensedCameras, currentPeriodEnd, fetchedAt, aiStatus, aiAlertsUsed, aiPeriodEnd
        case remoteStatus, remotePeriodEnd
    }

    // Tolerant decode so a cached license.json written before the AI / remote
    // plans existed still loads (keeps offline-grace working across the upgrade).
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        status = try c.decode(LicenseStatus.self, forKey: .status)
        licensedCameras = try c.decode(Int.self, forKey: .licensedCameras)
        currentPeriodEnd = try c.decodeIfPresent(Date.self, forKey: .currentPeriodEnd)
        fetchedAt = try c.decode(Date.self, forKey: .fetchedAt)
        aiStatus = try c.decodeIfPresent(LicenseStatus.self, forKey: .aiStatus) ?? .free
        aiAlertsUsed = try c.decodeIfPresent(Int.self, forKey: .aiAlertsUsed) ?? 0
        aiPeriodEnd = try c.decodeIfPresent(Date.self, forKey: .aiPeriodEnd)
        remoteStatus = try c.decodeIfPresent(LicenseStatus.self, forKey: .remoteStatus) ?? .free
        remotePeriodEnd = try c.decodeIfPresent(Date.self, forKey: .remotePeriodEnd)
    }
}

// MARK: - Auth + entitlement REST client (Supabase GoTrue + PostgREST + Edge Functions)

public struct SentinelSession: Codable, Equatable {
    public var accessToken: String
    public var refreshToken: String
    public var account: SentinelAccount
}

public enum SentinelBackendError: LocalizedError {
    case http(Int, String)
    case malformed
    public var errorDescription: String? {
        switch self {
        case .http(_, let message): return message
        case .malformed: return "Unexpected response from the licensing server."
        }
    }
}

public struct SentinelBackend {
    let baseURL: URL
    let anonKey: String
    let session: URLSession

    public init(baseURL: URL = SentinelBackendConfig.supabaseURL,
                anonKey: String = SentinelBackendConfig.supabaseAnonKey,
                session: URLSession = .shared) {
        self.baseURL = baseURL
        self.anonKey = anonKey
        self.session = session
    }

    // MARK: Auth

    public func signIn(email: String, password: String) async throws -> SentinelSession {
        try await authToken(grant: "password", body: ["email": email, "password": password])
    }

    public func signUp(email: String, password: String) async throws -> SentinelSession {
        // GoTrue /signup returns a session when email confirmation is disabled,
        // otherwise an access_token is absent and the caller must confirm email.
        try await authToken(path: "/auth/v1/signup", body: ["email": email, "password": password])
    }

    public func refresh(refreshToken: String) async throws -> SentinelSession {
        try await authToken(grant: "refresh_token", body: ["refresh_token": refreshToken])
    }

    private func authToken(grant: String? = nil,
                           path: String = "/auth/v1/token",
                           body: [String: String]) async throws -> SentinelSession {
        var comps = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if let grant { comps.queryItems = [URLQueryItem(name: "grant_type", value: grant)] }
        var req = URLRequest(url: comps.url!)
        req.httpMethod = "POST"
        req.setValue(anonKey, forHTTPHeaderField: "apikey")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: body)

        let (data, response) = try await session.data(for: req)
        try Self.ensureOK(response, data)

        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let accessToken = obj["access_token"] as? String,
              let refreshToken = obj["refresh_token"] as? String,
              let user = obj["user"] as? [String: Any],
              let id = user["id"] as? String else {
            throw SentinelBackendError.malformed
        }
        let email = (user["email"] as? String) ?? body["email"] ?? ""
        return SentinelSession(accessToken: accessToken,
                               refreshToken: refreshToken,
                               account: SentinelAccount(userID: id, email: email))
    }

    // MARK: Entitlement

    public func fetchLicense(accessToken: String, userID: String) async throws -> LicenseSnapshot {
        var comps = URLComponents(url: baseURL.appendingPathComponent("/rest/v1/sentinel_licenses"),
                                  resolvingAgainstBaseURL: false)!
        comps.queryItems = [
            URLQueryItem(name: "select", value: "status,licensed_cameras,current_period_end,remote_status,remote_current_period_end"),
            URLQueryItem(name: "user_id", value: "eq.\(userID)"),
        ]
        var req = URLRequest(url: comps.url!)
        req.setValue(anonKey, forHTTPHeaderField: "apikey")
        req.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")

        let (data, response) = try await session.data(for: req)
        try Self.ensureOK(response, data)

        let rows = (try? JSONSerialization.jsonObject(with: data)) as? [[String: Any]] ?? []
        guard let row = rows.first else {
            // No row yet → free tier.
            return LicenseSnapshot(status: .free, licensedCameras: 0, currentPeriodEnd: nil, fetchedAt: Date())
        }
        let parseDate: (String?) -> Date? = { iso in
            guard let iso else { return nil }
            return ISO8601DateFormatter().date(from: iso)
                ?? ISO8601DateFormatter.withFractionalSeconds.date(from: iso)
        }
        let status = LicenseStatus(rawValue: (row["status"] as? String) ?? "free") ?? .free
        let cameras = (row["licensed_cameras"] as? Int) ?? 0
        let remoteStatus = LicenseStatus(rawValue: (row["remote_status"] as? String) ?? "free") ?? .free
        // AI is now bring-your-own-key (no subscription) — see SentinelAISettings.
        return LicenseSnapshot(status: status, licensedCameras: cameras,
                               currentPeriodEnd: parseDate(row["current_period_end"] as? String),
                               fetchedAt: Date(),
                               remoteStatus: remoteStatus,
                               remotePeriodEnd: parseDate(row["remote_current_period_end"] as? String))
    }

    // MARK: Edge functions

    public func createCheckout(accessToken: String, quantity: Int, plan: String = "cameras") async throws -> URL {
        try await edgeFunctionURL("sentinel-create-checkout",
                                  accessToken: accessToken,
                                  body: ["quantity": quantity, "plan": plan])
    }

    public func openPortal(accessToken: String) async throws -> URL {
        try await edgeFunctionURL("sentinel-portal", accessToken: accessToken, body: [:])
    }

    // AI features (scene description, digest, event search) are now bring-your-own
    // Anthropic key and call api.anthropic.com directly — see SentinelAIClient.
    // They no longer route through Supabase edge functions.

    private func edgeFunctionURL(_ name: String, accessToken: String,
                                 body: [String: Any]) async throws -> URL {
        var req = URLRequest(url: baseURL.appendingPathComponent("/functions/v1/\(name)"))
        req.httpMethod = "POST"
        req.setValue(anonKey, forHTTPHeaderField: "apikey")
        req.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: body)

        let (data, response) = try await session.data(for: req)
        try Self.ensureOK(response, data)
        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let urlString = obj["url"] as? String, let url = URL(string: urlString) else {
            throw SentinelBackendError.malformed
        }
        return url
    }

    // MARK: Helpers

    private static func ensureOK(_ response: URLResponse, _ data: Data) throws {
        guard let http = response as? HTTPURLResponse else { return }
        guard (200..<300).contains(http.statusCode) else {
            let message = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?
                .flatMap { ($0["error"] as? String) ?? ($0["msg"] as? String) ?? ($0["error_description"] as? String) }
                ?? String(data: data, encoding: .utf8)
            throw SentinelBackendError.http(http.statusCode, message ?? "Request failed (\(http.statusCode)).")
        }
    }
}

extension ISO8601DateFormatter {
    static let withFractionalSeconds: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
}
