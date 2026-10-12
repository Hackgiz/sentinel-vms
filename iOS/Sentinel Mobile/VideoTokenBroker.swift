import Foundation

/// Where to fetch one camera's video with one token: the recording server
/// directly (`baseUrl`) or through the management server (`gatewayUrl`).
struct VideoEndpoint: Hashable, Sendable {
    var token: V2.VideoToken
    /// `…/v/{token}` without a trailing slash.
    var base: URL
    var isGateway: Bool

    var expiresAt: Date { Date(unixMillis: token.expiresAt) }

    /// Live HLS (LL-HLS when the server has it on): `{base}/live/{camera}/index.m3u8`.
    var liveHLS: URL {
        base.appendingPathComponent("live/\(token.cameraId)/index.m3u8")
    }

    /// Live WebRTC signalling (WHEP).
    var whep: URL {
        base.appendingPathComponent("webrtc/\(token.cameraId)/whep")
    }

    /// Recorded HLS for a window (rights `playback` or `export`).
    func playback(from: UnixMillis, to: UnixMillis) -> URL {
        var components = URLComponents(url: base.appendingPathComponent("api/v1/cameras/\(token.cameraId)/playback.m3u8"),
                                       resolvingAgainstBaseURL: false)!
        components.queryItems = [URLQueryItem(name: "from", value: String(from)), URLQueryItem(name: "to", value: String(to))]
        return components.url!
    }

    /// One recorded segment file.
    func recording(file: String) -> URL {
        base.appendingPathComponent("recordings/\(token.cameraId)/\(file)")
    }
}

/// Hands out video tokens, reusing one until shortly before it expires, and
/// remembers recording servers whose direct address didn't work so their
/// video goes through the management server's gateway for a while.
actor VideoTokenBroker {
    /// A token is reused while it has more than this left.
    static let reuseMargin: TimeInterval = 90
    /// After a direct failure, use the gateway for this long.
    static let gatewayStickiness: TimeInterval = 300

    private struct Key: Hashable {
        var cameraID: String
        var rights: String
        var recorderID: String
    }

    private let api: SentinelAPI
    private var cache: [Key: V2.VideoToken] = [:]
    private var inflight: [Key: Task<V2.VideoToken, Error>] = [:]
    private var directFailedAt: [String: Date] = [:]

    init(api: SentinelAPI) {
        self.api = api
    }

    /// An endpoint with a token valid for at least `reuseMargin` seconds.
    func endpoint(cameraID: String, rights: [V2.VideoRight] = [.live], recorderID: String? = nil,
                         preferGateway: Bool = false) async throws -> VideoEndpoint {
        let token = try await token(cameraID: cameraID, rights: rights, recorderID: recorderID)
        return endpoint(for: token, preferGateway: preferGateway)
    }

    func token(cameraID: String, rights: [V2.VideoRight], recorderID: String?) async throws -> V2.VideoToken {
        let key = Key(cameraID: cameraID, rights: rights.map(\.rawValue).sorted().joined(separator: ","), recorderID: recorderID ?? "")
        if let cached = cache[key], Date(unixMillis: cached.expiresAt).timeIntervalSinceNow > Self.reuseMargin {
            return cached
        }
        if let running = inflight[key] {
            return try await running.value
        }
        let api = self.api
        let task = Task { try await api.videoToken(cameraID: cameraID, rights: rights, recorderID: recorderID) }
        inflight[key] = task
        defer { inflight[key] = nil }
        let token = try await task.value
        cache[key] = token
        return token
    }

    func endpoint(for token: V2.VideoToken, preferGateway: Bool = false) -> VideoEndpoint {
        let direct = Self.resolve(token.baseUrl, against: api.baseURL)
        let gateway = Self.resolve(token.gatewayUrl, against: api.baseURL)
        let directIsGateway = direct == gateway
        var useGateway = preferGateway
        if let failed = directFailedAt[token.recorderId], Date().timeIntervalSince(failed) < Self.gatewayStickiness {
            useGateway = true
        }
        if useGateway, let gateway {
            return VideoEndpoint(token: token, base: gateway, isGateway: true)
        }
        if let direct {
            return VideoEndpoint(token: token, base: direct, isGateway: directIsGateway)
        }
        return VideoEndpoint(token: token, base: gateway ?? api.baseURL, isGateway: true)
    }

    /// The direct address of `recorderID` failed: use the gateway for a while.
    func reportDirectFailure(recorderID: String) {
        directFailedAt[recorderID] = Date()
    }

    /// Forget cached tokens for a camera (e.g. after a `403` because it moved).
    func invalidate(cameraID: String) {
        cache = cache.filter { $0.key.cameraID != cameraID }
    }

    func invalidateAll() {
        cache.removeAll()
        directFailedAt.removeAll()
    }

    /// Resolves `baseUrl`/`gatewayUrl` (absolute, or a path on the management
    /// server) and drops a trailing slash.
    static func resolve(_ value: String, against site: URL) -> URL? {
        guard value.isEmpty == false else { return nil }
        let absolute: URL?
        if value.hasPrefix("http://") || value.hasPrefix("https://") {
            absolute = URL(string: value)
        } else {
            absolute = URL(string: value, relativeTo: site)?.absoluteURL
        }
        guard var text = absolute?.absoluteString else { return nil }
        while text.hasSuffix("/") { text.removeLast() }
        return URL(string: text)
    }
}
