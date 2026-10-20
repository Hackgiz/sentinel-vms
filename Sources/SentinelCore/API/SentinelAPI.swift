import Foundation

/// Client for Sentinel API v2 on one site's management server.
///
/// - Bearer access tokens (15 min) are refreshed shortly before they expire
///   and once more on a `401`. Refreshes are single-flight: presenting a
///   refresh token twice would look like token theft to the server
///   (`token_reused`) and end the sign-in everywhere.
/// - Tokens are persisted through `TokenStoring` (the Keychain in the app).
/// - When the sign-in ends (refresh rejected), the tokens are dropped, the
///   `signInEnded` handler runs and calls throw `.signInRequired`.
///
/// Keep exactly one instance per site in a process.
public actor SentinelAPI {
    public nonisolated let baseURL: URL
    public nonisolated let siteID: String

    private let session: URLSession
    private let tokenStore: TokenStoring
    private let clientLabel: String
    private var tokens: TokenPair?
    private var refreshTask: Task<TokenPair, Error>?
    private var signInEnded: (@Sendable (String) -> Void)?

    /// Refresh the access token when it has less than this left.
    public static let refreshMargin: TimeInterval = 60

    public static let jsonDecoder = JSONDecoder()
    public static let jsonEncoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }()

    public init(baseURL: URL, siteID: String, tokenStore: TokenStoring, session: URLSession = .shared,
                clientLabel: String = SentinelAPI.defaultClientLabel) {
        self.baseURL = baseURL
        self.siteID = siteID
        self.tokenStore = tokenStore
        self.session = session
        self.clientLabel = clientLabel
        self.tokens = tokenStore.load(siteID: siteID)
    }

    /// "Sentinel Operator (Mac mini)" — the label the server puts in its audit log.
    public static var defaultClientLabel: String {
        let host = Host.current().localizedName ?? ProcessInfo.processInfo.hostName
        return String("Sentinel Operator (\(host))".prefix(100))
    }

    public func setSignInEndedHandler(_ handler: (@Sendable (String) -> Void)?) {
        signInEnded = handler
    }

    public var isSignedIn: Bool {
        guard let tokens else { return false }
        return tokens.refreshExpired() == false
    }

    public var signedInUserName: String? { tokens?.userName }

    // MARK: - Sign-in

    @discardableResult
    public func signIn(name: String, password: String) async throws -> V2.TokenResponse {
        try await exchange(.password(name: name, password: password, client: clientLabel))
    }

    /// Completes the native single sign-on flow with the one-time code the
    /// server sent to the `redirectUri`.
    @discardableResult
    public func signIn(oidcCode code: String) async throws -> V2.TokenResponse {
        try await exchange(.oidcCode(code: code, client: clientLabel))
    }

    private func exchange(_ grant: V2.TokenGrant) async throws -> V2.TokenResponse {
        let response: V2.TokenResponse = try await send("POST", "/api/v2/auth/token", body: grant, authorized: false)
        adopt(TokenPair(response))
        return response
    }

    /// Ends this client's sign-in on the server (best effort) and forgets the tokens.
    public func signOut() async {
        if let refresh = tokens?.refreshToken {
            struct Revoke: Encodable { var refreshToken: String }
            let body = try? Self.jsonEncoder.encode(Revoke(refreshToken: refresh))
            _ = try? await raw("POST", "/api/v2/auth/revoke", body: body, contentType: "application/json", authorized: false)
        }
        clearTokens()
    }

    /// `GET /api/v2/auth/oidc/start` for a native client.
    public nonisolated func oidcStartURL(redirectURI: String, state: String) -> URL {
        url("/api/v2/auth/oidc/start", query: [
            URLQueryItem(name: "redirectUri", value: redirectURI),
            URLQueryItem(name: "state", value: state)
        ])
    }

    /// A valid access token, refreshed first if it's about to expire. For
    /// clients that authenticate outside `send` (the event stream).
    public func accessToken(forceRefresh: Bool = false) async throws -> String {
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

    public nonisolated func url(_ path: String, query: [URLQueryItem] = []) -> URL {
        var base = baseURL.absoluteString
        while base.hasSuffix("/") { base.removeLast() }
        var components = URLComponents(string: base + path) ?? URLComponents()
        let items = query.filter { $0.value != nil }
        if items.isEmpty == false {
            components.queryItems = items
        }
        return components.url ?? baseURL
    }

    /// Sends a JSON request and decodes a JSON response.
    public func send<Response: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [],
                                          body: (any Encodable)? = nil, authorized: Bool = true) async throws -> Response {
        let payload = try body.map { try Self.jsonEncoder.encode($0) }
        let (data, _) = try await raw(method, path, query: query, body: payload,
                                      contentType: payload == nil ? nil : "application/json",
                                      authorized: authorized)
        return try Self.decode(Response.self, from: data)
    }

    /// Sends a request whose answer has no body we need.
    public func sendNoContent(_ method: String, _ path: String, query: [URLQueryItem] = [],
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
    public func raw(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil,
                    contentType: String? = nil, authorized: Bool = true) async throws -> (Data, HTTPURLResponse) {
        let target = url(path, query: query)
        func request(_ token: String?) -> URLRequest {
            var r = URLRequest(url: target)
            r.httpMethod = method
            r.httpBody = body
            r.setValue("application/json", forHTTPHeaderField: "Accept")
            if let contentType { r.setValue(contentType, forHTTPHeaderField: "Content-Type") }
            if let token { r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
            return r
        }
        return try await perform(authorized: authorized) { token in
            try await self.transport { try await self.session.data(for: request(token)) }
        }
    }

    /// Uploads a file as the request body (streamed from disk).
    public func upload(_ method: String, _ path: String, fileURL: URL,
                       contentType: String = "application/octet-stream") async throws {
        let target = url(path)
        _ = try await perform(authorized: true) { token in
            var r = URLRequest(url: target)
            r.httpMethod = method
            r.setValue(contentType, forHTTPHeaderField: "Content-Type")
            r.setValue("Bearer \(token ?? "")", forHTTPHeaderField: "Authorization")
            return try await self.transport { try await self.session.upload(for: r, fromFile: fileURL) }
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
            // WAVE13: a `cloud_*` 401 is about the Sentinel Cloud account token in the
            // body, not our session; refreshing and resending it would only create a
            // second rejected link attempt.
            if case .server(_, let code, _) = SentinelAPIError.from(status: 401, body: data), code.hasPrefix("cloud_") {
                throw SentinelAPIError.from(status: 401, body: data)
            }
            // The access token was revoked or the clock is off: one refresh, one retry.
            token = try await refreshTokens().accessToken
            (data, response) = try await attempt(token)
        }
        guard (200..<300).contains(response.statusCode) else {
            throw SentinelAPIError.from(status: response.statusCode, body: data)
        }
        return (data, response)
    }

    private nonisolated func transport(_ call: () async throws -> (Data, URLResponse)) async throws -> (Data, HTTPURLResponse) {
        do {
            let (data, response) = try await call()
            guard let http = response as? HTTPURLResponse else {
                throw SentinelAPIError.transport(message: "The server's answer wasn't HTTP.")
            }
            return (data, http)
        } catch let error as SentinelAPIError {
            throw error
        } catch let error as URLError {
            throw SentinelAPIError.transport(message: Self.describe(error))
        } catch {
            throw SentinelAPIError.transport(message: error.localizedDescription)
        }
    }

    static func describe(_ error: URLError) -> String {
        switch error.code {
        case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed:
            return "Can't reach the server. Check the address and that the server is running."
        case .timedOut:
            return "The server didn't answer in time."
        case .notConnectedToInternet, .networkConnectionLost:
            return "This Mac is offline."
        case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateNotYetValid,
             .serverCertificateHasUnknownRoot, .secureConnectionFailed:
            return "The server's certificate isn't trusted (\(error.localizedDescription))."
        case .appTransportSecurityRequiresSecureConnection:
            return "macOS only allows plain http:// to this Mac and the local network. Use https:// for this address."
        default:
            return error.localizedDescription
        }
    }
}

// MARK: - Resources

public extension SentinelAPI {
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

    func camera(_ id: String) async throws -> V2.Camera {
        try await send("GET", "/api/v2/cameras/\(Self.escape(id))")
    }

    func createCamera(_ input: V2.CameraInput) async throws -> V2.Camera {
        try await send("POST", "/api/v2/cameras", body: input)
    }

    func updateCamera(_ id: String, _ patch: V2.CameraPatch) async throws -> V2.Camera {
        try await send("PATCH", "/api/v2/cameras/\(Self.escape(id))", body: patch)
    }

    func deleteCamera(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/cameras/\(Self.escape(id))")
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

    func recordings(cameraID: String) async throws -> [V2.Segment] {
        let list: V2.Items<V2.Segment> = try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/recordings")
        return list.items
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

    func event(_ id: String) async throws -> V2.Event {
        try await send("GET", "/api/v2/events/\(Self.escape(id))")
    }

    func eventThumbnail(_ id: String) async throws -> Data {
        try await raw("GET", "/api/v2/events/\(Self.escape(id))/thumbnail.jpg").0
    }

    // Rules

    func rules() async throws -> [V2.Rule] {
        let list: V2.Items<V2.Rule> = try await send("GET", "/api/v2/rules")
        return list.items
    }

    func createRule(_ input: V2.RuleInput) async throws -> V2.Rule {
        try await send("POST", "/api/v2/rules", body: input)
    }

    func replaceRule(_ id: String, _ input: V2.RuleInput) async throws -> V2.Rule {
        try await send("PUT", "/api/v2/rules/\(Self.escape(id))", body: input)
    }

    func deleteRule(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/rules/\(Self.escape(id))")
    }

    // Users

    func users() async throws -> [V2.User] {
        let list: V2.Items<V2.User> = try await send("GET", "/api/v2/users")
        return list.items
    }

    func createUser(_ input: V2.NewUser) async throws -> V2.User {
        try await send("POST", "/api/v2/users", body: input)
    }

    func updateUser(_ id: String, _ patch: V2.UserPatch) async throws -> V2.User {
        try await send("PATCH", "/api/v2/users/\(Self.escape(id))", body: patch)
    }

    func deleteUser(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/users/\(Self.escape(id))")
    }

    // Site

    func recordingServers() async throws -> [V2.RecordingServer] {
        let list: V2.Items<V2.RecordingServer> = try await send("GET", "/api/v2/recording-servers")
        return list.items
    }

    func cameraGroups() async throws -> [V2.CameraGroup] {
        let list: V2.Items<V2.CameraGroup> = try await send("GET", "/api/v2/camera-groups")
        return list.items
    }

    func audit(serverID: String? = nil, cursor: String? = nil, limit: Int? = nil) async throws -> V2.Page<V2.AuditEntry> {
        try await send("GET", "/api/v2/audit", query: [
            URLQueryItem(name: "serverId", value: serverID),
            URLQueryItem(name: "cursor", value: cursor),
            URLQueryItem(name: "limit", value: limit.map(String.init))
        ])
    }

    func verifyAudit() async throws -> V2.AuditVerify {
        try await send("GET", "/api/v2/audit/verify")
    }

    // Saved views

    func views() async throws -> [V2.View] {
        let list: V2.Items<V2.View> = try await send("GET", "/api/v2/views")
        return list.items
    }

    func createView(_ input: V2.ViewInput) async throws -> V2.View {
        try await send("POST", "/api/v2/views", body: input)
    }

    func replaceView(_ id: String, _ input: V2.ViewInput) async throws -> V2.View {
        try await send("PUT", "/api/v2/views/\(Self.escape(id))", body: input)
    }

    func deleteView(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/views/\(Self.escape(id))")
    }

    // Mac import

    func importMac(_ payload: V2.MacImport) async throws -> V2.MacImportResult {
        try await send("POST", "/api/v2/import/mac", body: payload)
    }

    func uploadImportedEvidence(caseID: String, fileURL: URL) async throws {
        try await upload("PUT", "/api/v2/import/mac/evidence/\(Self.escape(caseID))", fileURL: fileURL)
    }

    /// Percent-escapes one path segment.
    static func escape(_ segment: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#")
        return segment.addingPercentEncoding(withAllowedCharacters: allowed) ?? segment
    }
}
