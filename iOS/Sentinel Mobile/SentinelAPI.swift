// SentinelAPI.swift
// Client for Sentinel API v2 on one site's Management Server
// (linux/api/openapi.yaml is the contract). Mirrors the macOS Operator's
// client; the phone adds a secondary address (a Cloudflare tunnel) that is
// tried when the primary one can't be reached.

import Foundation

/// The primary and secondary address of one site, and which one answered last.
/// Shared between the API client, the video broker and the event stream, so
/// they all follow whichever address works right now.
final class SiteAddresses: @unchecked Sendable {
    let primary: URL
    let secondary: URL?
    private let lock = NSLock()
    private var activeURL: URL
    private var lastPrimaryProbe = Date.distantPast

    /// While on the secondary address, look at the primary again this often.
    static let reprobeInterval: TimeInterval = 60
    /// How long the primary gets to answer when a secondary exists.
    static let primaryTimeout: TimeInterval = 2.5

    init(primary: URL, secondary: URL?) {
        self.primary = primary
        self.secondary = secondary == primary ? nil : secondary
        self.activeURL = primary
    }

    var active: URL {
        lock.lock(); defer { lock.unlock() }
        return activeURL
    }

    var isUsingSecondary: Bool { secondary != nil && active == secondary }

    func markWorking(_ url: URL) {
        lock.lock(); defer { lock.unlock() }
        // Having just failed on the primary counts as a probe of it.
        if url != primary, activeURL == primary { lastPrimaryProbe = Date() }
        activeURL = url
    }

    /// Addresses to try, in order, with the timeout for each.
    func attempts(now: Date = Date()) -> [(url: URL, timeout: TimeInterval)] {
        guard let secondary else { return [(primary, 30)] }
        lock.lock(); defer { lock.unlock() }
        if activeURL == primary {
            return [(primary, Self.primaryTimeout), (secondary, 30)]
        }
        if now.timeIntervalSince(lastPrimaryProbe) > Self.reprobeInterval {
            lastPrimaryProbe = now
            return [(primary, Self.primaryTimeout), (secondary, 30)]
        }
        return [(secondary, 30), (primary, Self.primaryTimeout)]
    }
}

/// Keep exactly one instance per site in a process.
///
/// - Bearer access tokens (15 min) are refreshed shortly before they expire
///   and once more on a `401`. Refreshes are single-flight: presenting a
///   refresh token twice looks like token theft to the server
///   (`token_reused`) and ends the sign-in everywhere.
/// - Tokens are persisted through `TokenStoring` (the Keychain in the app).
/// - When the sign-in ends (refresh rejected), the tokens are dropped, the
///   `signInEnded` handler runs and calls throw `.signInRequired`.
actor SentinelAPI {
    /// The address that answered last (changes when the phone moves between
    /// the local network and cellular).
    nonisolated var baseURL: URL { addresses.active }
    nonisolated let addresses: SiteAddresses
    nonisolated let siteID: String

    private let session: URLSession
    private let tokenStore: TokenStoring
    private let clientLabel: String
    private var tokens: TokenPair?
    private var refreshTask: Task<TokenPair, Error>?
    private var signInEnded: (@Sendable (String) -> Void)?

    /// Refresh the access token when it has less than this left.
    static let refreshMargin: TimeInterval = 60

    static let jsonDecoder = JSONDecoder()
    static let jsonEncoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }()

    init(addresses: SiteAddresses, siteID: String, tokenStore: TokenStoring, session: URLSession = .shared,
         clientLabel: String = "Sentinel Mobile") {
        self.addresses = addresses
        self.siteID = siteID
        self.tokenStore = tokenStore
        self.session = session
        self.clientLabel = String(clientLabel.prefix(100))
        self.tokens = tokenStore.load(siteID: siteID)
    }

    init(baseURL: URL, siteID: String, tokenStore: TokenStoring, session: URLSession = .shared,
         clientLabel: String = "Sentinel Mobile") {
        self.init(addresses: SiteAddresses(primary: baseURL, secondary: nil), siteID: siteID,
                  tokenStore: tokenStore, session: session, clientLabel: clientLabel)
    }

    func setSignInEndedHandler(_ handler: (@Sendable (String) -> Void)?) {
        signInEnded = handler
    }

    var isSignedIn: Bool {
        guard let tokens else { return false }
        return tokens.refreshExpired() == false
    }

    var signedInUserName: String? { tokens?.userName }

    // MARK: - Sign-in

    @discardableResult
    func signIn(name: String, password: String) async throws -> V2.TokenResponse {
        try await exchange(.password(name: name, password: password, client: clientLabel))
    }

    /// Completes the native single sign-on flow with the one-time code the
    /// server sent to the `redirectUri`.
    @discardableResult
    func signIn(oidcCode code: String) async throws -> V2.TokenResponse {
        try await exchange(.oidcCode(code: code, client: clientLabel))
    }

    private func exchange(_ grant: V2.TokenGrant) async throws -> V2.TokenResponse {
        let response: V2.TokenResponse = try await send("POST", "/api/v2/auth/token", body: grant, authorized: false)
        adopt(TokenPair(response))
        return response
    }

    /// Ends this client's sign-in on the server (best effort) and forgets the tokens.
    func signOut() async {
        if let refresh = tokens?.refreshToken {
            struct Revoke: Encodable { var refreshToken: String }
            let body = try? Self.jsonEncoder.encode(Revoke(refreshToken: refresh))
            _ = try? await raw("POST", "/api/v2/auth/revoke", body: body, contentType: "application/json", authorized: false)
        }
        clearTokens()
    }

    /// `GET /api/v2/auth/oidc/start` for a native client.
    nonisolated func oidcStartURL(redirectURI: String, state: String) -> URL {
        url("/api/v2/auth/oidc/start", query: [
            URLQueryItem(name: "redirectUri", value: redirectURI),
            URLQueryItem(name: "state", value: state)
        ])
    }

    /// A valid access token, refreshed first if it's about to expire. For
    /// clients that authenticate outside `send` (the event stream).
    func accessToken(forceRefresh: Bool = false) async throws -> String {
        if forceRefresh {
            return try await refreshTokens().accessToken
        }
        return try await validAccessToken()
    }

    private func adopt(_ pair: TokenPair) {
        tokens = pair
        tokenStore.save(pair, siteID: siteID)
    }

    private func clearTokens() {
        tokens = nil
        tokenStore.delete(siteID: siteID)
    }

    private func endSignIn(_ message: String) -> SentinelAPIError {
        clearTokens()
        signInEnded?(message)
        return .signInRequired(message: message)
    }

    private func validAccessToken() async throws -> String {
        guard let current = tokens else {
            throw SentinelAPIError.signInRequired(message: "Sign in to continue.")
        }
        if current.accessExpires(within: Self.refreshMargin) {
            return try await refreshTokens().accessToken
        }
        return current.accessToken
    }

    /// Single-flight refresh: concurrent callers share one request.
    private func refreshTokens() async throws -> TokenPair {
        if let refreshTask {
            return try await refreshTask.value
        }
        let task = Task { try await self.performRefresh() }
        refreshTask = task
        defer { refreshTask = nil }
        return try await task.value
    }

    private func performRefresh() async throws -> TokenPair {
        guard let current = tokens else {
            throw SentinelAPIError.signInRequired(message: "Sign in to continue.")
        }
        guard current.refreshExpired() == false else {
            throw endSignIn("Your sign-in expired. Sign in again.")
        }
        do {
            let response: V2.TokenResponse = try await send(
                "POST", "/api/v2/auth/token",
                body: V2.TokenGrant.refresh(refreshToken: current.refreshToken),
                authorized: false
            )
            let pair = TokenPair(response)
            adopt(pair)
            return pair
        } catch let error as SentinelAPIError {
            switch error {
            case .server(let status, let code, _) where status == 401 || status == 400 || code == "token_reused":
                throw endSignIn(code == "token_reused"
                    ? "Your sign-in was ended because it was used from somewhere else. Sign in again."
                    : "Your sign-in ended. Sign in again.")
            default:
                throw error
            }
        }
    }

    // MARK: - Requests

    nonisolated func url(_ path: String, query: [URLQueryItem] = [], base: URL? = nil) -> URL {
        var text = (base ?? baseURL).absoluteString
        while text.hasSuffix("/") { text.removeLast() }
        var components = URLComponents(string: text + path) ?? URLComponents()
        let items = query.filter { $0.value != nil }
        if items.isEmpty == false {
            components.queryItems = items
        }
        return components.url ?? (base ?? baseURL)
    }

    /// Sends a JSON request and decodes a JSON response.
    func send<Response: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [],
                                   body: (any Encodable)? = nil, authorized: Bool = true) async throws -> Response {
        let payload = try body.map { try Self.jsonEncoder.encode($0) }
        let (data, _) = try await raw(method, path, query: query, body: payload,
                                      contentType: payload == nil ? nil : "application/json",
                                      authorized: authorized)
        return try Self.decode(Response.self, from: data)
    }

    /// Sends a request whose answer has no body we need.
    func sendNoContent(_ method: String, _ path: String, query: [URLQueryItem] = [],
                       body: (any Encodable)? = nil) async throws {
        let payload = try body.map { try Self.jsonEncoder.encode($0) }
        _ = try await raw(method, path, query: query, body: payload,
                          contentType: payload == nil ? nil : "application/json", authorized: true)
    }

    static func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try jsonDecoder.decode(T.self, from: data)
        } catch let DecodingError.keyNotFound(key, context) {
            throw SentinelAPIError.decoding(message: "missing \(Self.path(context.codingPath + [key]))")
        } catch let DecodingError.typeMismatch(_, context), let DecodingError.valueNotFound(_, context) {
            throw SentinelAPIError.decoding(message: "unexpected value at \(Self.path(context.codingPath))")
        } catch let DecodingError.dataCorrupted(context) {
            throw SentinelAPIError.decoding(message: context.debugDescription)
        } catch {
            throw SentinelAPIError.decoding(message: error.localizedDescription)
        }
    }

    private static func path(_ keys: [CodingKey]) -> String {
        keys.map { $0.intValue.map { "[\($0)]" } ?? $0.stringValue }.joined(separator: ".")
    }

    /// The bytes of a successful response. Retries once with a refreshed
    /// token on `401`.
    func raw(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil,
             contentType: String? = nil, authorized: Bool = true) async throws -> (Data, HTTPURLResponse) {
        func request(_ token: String?, base: URL, timeout: TimeInterval) -> URLRequest {
            var r = URLRequest(url: url(path, query: query, base: base), timeoutInterval: timeout)
            r.httpMethod = method
            r.httpBody = body
            r.setValue("application/json", forHTTPHeaderField: "Accept")
            if let contentType { r.setValue(contentType, forHTTPHeaderField: "Content-Type") }
            if let token { r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
            return r
        }
        return try await perform(authorized: authorized) { token in
            try await self.transport(request: { base, timeout in request(token, base: base, timeout: timeout) })
        }
    }

    private func perform(authorized: Bool,
                         _ attempt: (String?) async throws -> (Data, HTTPURLResponse)) async throws -> (Data, HTTPURLResponse) {
        var token: String?
        if authorized {
            token = try await validAccessToken()
        }
        var (data, response) = try await attempt(token)
        if authorized, response.statusCode == 401 {
            // The access token was revoked or the clock is off: one refresh, one retry.
            token = try await refreshTokens().accessToken
            (data, response) = try await attempt(token)
        }
        guard (200..<300).contains(response.statusCode) else {
            throw SentinelAPIError.from(status: response.statusCode, body: data)
        }
        return (data, response)
    }

    /// Runs a request against the site's addresses: the one that worked last
    /// first, and the other one when the first can't be reached at all. An
    /// answer with an error status is an answer: it never triggers a fallback.
    private nonisolated func transport(request: (URL, TimeInterval) -> URLRequest) async throws -> (Data, HTTPURLResponse) {
        var lastError: SentinelAPIError?
        for (base, timeout) in addresses.attempts() {
            do {
                let (data, response) = try await session.data(for: request(base, timeout))
                guard let http = response as? HTTPURLResponse else {
                    throw SentinelAPIError.transport(message: "The server's answer wasn't HTTP.")
                }
                addresses.markWorking(base)
                return (data, http)
            } catch let error as SentinelAPIError {
                throw error
            } catch let error as URLError {
                if error.code == .cancelled { throw CancellationError() }
                lastError = .transport(message: Self.describe(error))
                if Self.isUnreachable(error) { continue }
                throw lastError!
            } catch {
                throw SentinelAPIError.transport(message: error.localizedDescription)
            }
        }
        throw lastError ?? SentinelAPIError.transport(message: "Can't reach the server.")
    }

    /// Failures that mean "this address doesn't answer" (try the other one),
    /// as opposed to "answered badly" (TLS trust, ATS policy).
    static func isUnreachable(_ error: URLError) -> Bool {
        switch error.code {
        case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed, .timedOut,
             .notConnectedToInternet, .networkConnectionLost, .internationalRoamingOff, .dataNotAllowed:
            return true
        default:
            return false
        }
    }

    static func describe(_ error: URLError) -> String {
        switch error.code {
        case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed:
            return "Can't reach the server. Check the address and that the Management Server is running."
        case .timedOut:
            return "The server didn't answer in time."
        case .notConnectedToInternet, .networkConnectionLost:
            return "This iPhone is offline."
        case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateNotYetValid,
             .serverCertificateHasUnknownRoot, .secureConnectionFailed:
            return "The server's certificate isn't trusted (\(error.localizedDescription))."
        case .appTransportSecurityRequiresSecureConnection:
            return "iOS only allows plain http:// on the local network. Use https:// for this address."
        default:
            return error.localizedDescription
        }
    }
}

// MARK: - Resources

extension SentinelAPI {
    // Auth

    func providers() async throws -> V2.AuthProviders {
        try await send("GET", "/api/v2/auth/providers", authorized: false)
    }

    func me() async throws -> V2.Me {
        try await send("GET", "/api/v2/me")
    }

    // Cameras

    func cameras() async throws -> [V2.Camera] {
        let list: V2.Items<V2.Camera> = try await send("GET", "/api/v2/cameras")
        return list.items
    }

    // Video

    func videoToken(cameraID: String, rights: [V2.VideoRight] = [.live], recorderID: String? = nil) async throws -> V2.VideoToken {
        try await send("POST", "/api/v2/cameras/\(Self.escape(cameraID))/video-token",
                       body: V2.VideoTokenRequest(rights: rights, recorderId: recorderID))
    }

    // Recordings

    func timeline(cameraID: String, from: UnixMillis, to: UnixMillis) async throws -> V2.Timeline {
        try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/timeline", query: [
            URLQueryItem(name: "from", value: String(from)),
            URLQueryItem(name: "to", value: String(to))
        ])
    }

    // Alarms

    func alarms(openOnly: Bool = true, cameraID: String? = nil, cursor: String? = nil, limit: Int? = nil) async throws -> V2.Page<V2.Alarm> {
        try await send("GET", "/api/v2/alarms", query: [
            URLQueryItem(name: "state", value: openOnly ? "open" : "all"),
            URLQueryItem(name: "cameraId", value: cameraID),
            URLQueryItem(name: "cursor", value: cursor),
            URLQueryItem(name: "limit", value: limit.map(String.init))
        ])
    }

    func alarm(_ id: String) async throws -> V2.Alarm {
        try await send("GET", "/api/v2/alarms/\(Self.escape(id))")
    }

    func setAlarmState(_ id: String, _ change: V2.AlarmStateChange) async throws -> V2.Alarm {
        try await send("POST", "/api/v2/alarms/\(Self.escape(id))/state", body: change)
    }

    func lockEvidence(alarmID: String) async throws -> V2.Evidence {
        try await send("POST", "/api/v2/alarms/\(Self.escape(alarmID))/evidence")
    }

    // Events

    func events(cameraID: String? = nil, cursor: String? = nil, limit: Int? = nil) async throws -> V2.Page<V2.Event> {
        try await send("GET", "/api/v2/events", query: [
            URLQueryItem(name: "cameraId", value: cameraID),
            URLQueryItem(name: "cursor", value: cursor),
            URLQueryItem(name: "limit", value: limit.map(String.init))
        ])
    }

    func eventThumbnail(_ id: String) async throws -> Data {
        try await raw("GET", "/api/v2/events/\(Self.escape(id))/thumbnail.jpg").0
    }

    // Push

    func registerPush(apnsToken: String, deviceName: String?) async throws {
        try await sendNoContent("PUT", "/api/v2/push/registration",
                                body: V2.PushRegistration(apnsToken: apnsToken, deviceName: deviceName))
    }

    func unregisterPush(apnsToken: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/push/registration",
                                query: [URLQueryItem(name: "apnsToken", value: apnsToken)])
    }

    /// Percent-escapes one path segment.
    static func escape(_ segment: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#")
        return segment.addingPercentEncoding(withAllowedCharacters: allowed) ?? segment
    }
}
