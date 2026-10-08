import Foundation
import Network
import SentinelCore

/// Read-only data source the HTTP server queries to serve API requests.
/// `SentinelAppDependencies` will conform to this and bridge into the
/// main-actor stores so the server doesn't have to know about them.
public protocol SentinelHTTPDataSource: AnyObject, Sendable {
    func camerasResponse() async -> Data
    func cameraDetailResponse(cameraID: UUID) async -> Data?
    func alertsResponse() async -> Data
    /// Recent AI detection events across all cameras (for the iOS event feed).
    func eventsResponse() async -> Data
    /// JPEG thumbnail for a single detection event, by event ID.
    func eventThumbnail(eventID: UUID) async -> (Data, String)?
    func segmentsResponse(cameraID: UUID, from: Date?, to: Date?) async -> Data
    func segmentFile(cameraID: UUID, name: String) async -> (Data, String)?
    func snapshotFile(cameraID: UUID) async -> (Data, String)?
    func redeemPairing(code: String, deviceName: String) async -> Data?
    func authorize(token: String) async -> UUID?
    func registerDevice(deviceID: UUID, apnsToken: String) async
    /// MediaMTX path to view a camera (low-res sub-stream when available, else
    /// main), so the iOS HLS proxy serves the lightweight feed.
    func liveStreamPath(cameraID: UUID) async -> String
    /// The current public (Cloudflare tunnel) URL, if remote access is live.
    /// Advertised to paired phones via a response header so they keep their
    /// stored remote URL fresh even though the quick-tunnel URL rotates on
    /// every Mac restart — pair once on LAN and remote access self-heals.
    func currentRemoteURL() async -> String?

    // MARK: Remote actions (paired phone)
    //
    // Deliberately limited to non-destructive actions: acknowledging an alarm
    // and locking (preserving) evidence. Unlocking evidence, deleting footage and
    // changing retention stay on the Mac behind supervisor approval — a stolen
    // phone token must never be able to destroy evidence.

    /// Marks a New alarm Acknowledged, attributed to the paired device.
    func acknowledgeAlert(alertID: UUID, deviceID: UUID) async -> SentinelRemoteActionResult
    /// Packages the alarm's linked recording as evidence and locks it.
    func lockEvidence(alertID: UUID, deviceID: UUID) async -> SentinelRemoteActionResult
}

public enum SentinelRemoteActionResult: Sendable {
    case ok(Data)
    case notFound(String)
    case conflict(String)

    var response: SentinelHTTPResponse {
        switch self {
        case .ok(let body): return SentinelHTTPResponse(status: 200, body: body, contentType: "application/json")
        case .notFound(let message): return SentinelHTTPResponse(status: 404, body: Self.errorBody(message), contentType: "application/json")
        case .conflict(let message): return SentinelHTTPResponse(status: 409, body: Self.errorBody(message), contentType: "application/json")
        }
    }

    /// Properly escaped `{"error": message}` (messages can contain quotes).
    private static func errorBody(_ message: String) -> Data {
        (try? JSONSerialization.data(withJSONObject: ["error": message])) ?? Data("{}".utf8)
    }
}

@MainActor
public final class SentinelHTTPServer: ObservableObject {
    public static let defaultPort: UInt16 = 8090

    @Published public private(set) var isRunning = false
    @Published public private(set) var listenURLs: [String] = []
    @Published public private(set) var lastError: String?

    private let port: NWEndpoint.Port
    private var listener: NWListener?
    private weak var dataSource: SentinelHTTPDataSource?

    public init(port: UInt16 = SentinelHTTPServer.defaultPort) {
        self.port = NWEndpoint.Port(rawValue: port) ?? .init(integerLiteral: 8090)
    }

    public func start(dataSource: SentinelHTTPDataSource) {
        stop()
        self.dataSource = dataSource
        do {
            let params = NWParameters.tcp
            params.allowLocalEndpointReuse = true
            let listener = try NWListener(using: params, on: port)
            listener.stateUpdateHandler = { [weak self] state in
                Task { @MainActor in
                    guard let self else { return }
                    switch state {
                    case .ready:
                        self.isRunning = true
                        self.refreshListenURLs()
                    case .failed(let error):
                        self.lastError = error.localizedDescription
                        self.isRunning = false
                    case .cancelled:
                        self.isRunning = false
                    default:
                        break
                    }
                }
            }
            let source = self.dataSource
            listener.newConnectionHandler = { connection in
                Task.detached(priority: .userInitiated) {
                    await SentinelHTTPSession.run(connection: connection, dataSource: source)
                }
            }
            listener.start(queue: .global(qos: .userInitiated))
            self.listener = listener
        } catch {
            self.lastError = error.localizedDescription
            self.isRunning = false
        }
    }

    public func stop() {
        listener?.cancel()
        listener = nil
        isRunning = false
        listenURLs = []
    }

    private func refreshListenURLs() {
        var urls: [String] = []
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0, let first = ifaddr else {
            self.listenURLs = []
            return
        }
        defer { freeifaddrs(ifaddr) }
        var cursor: UnsafeMutablePointer<ifaddrs>? = first
        while let ptr = cursor {
            defer { cursor = ptr.pointee.ifa_next }
            let flags = Int32(ptr.pointee.ifa_flags)
            let isUp = (flags & IFF_UP) == IFF_UP
            let isLoopback = (flags & IFF_LOOPBACK) == IFF_LOOPBACK
            guard isUp, !isLoopback else { continue }
            guard let addrPtr = ptr.pointee.ifa_addr,
                  addrPtr.pointee.sa_family == sa_family_t(AF_INET) else { continue }
            let name = String(cString: ptr.pointee.ifa_name)
            if name.hasPrefix("utun") || name.hasPrefix("awdl") || name.hasPrefix("llw") ||
                name.hasPrefix("anpi") || name.hasPrefix("bridge") || name.hasPrefix("ap") {
                continue
            }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            let length = socklen_t(MemoryLayout<sockaddr_in>.size)
            if getnameinfo(addrPtr, length, &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                let ip = String(cString: host)
                if !ip.hasPrefix("169.254.") {
                    urls.append("http://\(ip):\(port.rawValue)")
                }
            }
        }
        self.listenURLs = urls
    }
}

// MARK: - HTTP session

private actor SentinelHTTPSession {
    // Cap concurrent in-flight connections so the public tunnel endpoint can't be
    // pinned by a flood of slow connections (each is also bounded by the receive
    // timeout). Guarded by a lock; access is serialized so the count is correct.
    private static let maxConcurrent = 64
    private static let slotLock = NSLock()
    private nonisolated(unsafe) static var activeConnections = 0
    private static func acquireSlot() -> Bool {
        slotLock.lock(); defer { slotLock.unlock() }
        guard activeConnections < maxConcurrent else { return false }
        activeConnections += 1
        return true
    }
    private static func releaseSlot() {
        slotLock.lock(); activeConnections -= 1; slotLock.unlock()
    }

    static func run(connection: NWConnection, dataSource: SentinelHTTPDataSource?) async {
        guard acquireSlot() else { connection.cancel(); return }
        defer { releaseSlot() }
        connection.start(queue: .global(qos: .userInitiated))
        defer { connection.cancel() }

        guard let raw = await receive(connection: connection, atLeast: 4) else { return }
        guard let request = SentinelHTTPRequest.parse(raw) else {
            await send(connection: connection, response: SentinelHTTPResponse.badRequest)
            return
        }

        var response = await route(request: request, dataSource: dataSource)
        // Advertise the current remote URL to authenticated callers so a phone
        // that's on home Wi-Fi refreshes its stored tunnel URL automatically.
        if request.bearerToken != nil,
           let remote = await dataSource?.currentRemoteURL() {
            response.extraHeaders["X-Sentinel-Remote-URL"] = remote
        }
        await send(connection: connection, response: response)
    }

    private static func route(request: SentinelHTTPRequest, dataSource: SentinelHTTPDataSource?) async -> SentinelHTTPResponse {
        guard let dataSource else {
            return SentinelHTTPResponse(status: 503, body: jsonError("server not ready"))
        }

        // Public endpoints — no auth required
        switch (request.method, request.pathComponents) {
        case ("GET", ["health"]):
            return .json(["status": "ok", "service": "sentinel-vms"])
        case ("POST", ["pair"]):
            guard let payload = try? JSONSerialization.jsonObject(with: request.body) as? [String: Any],
                  let code = payload["code"] as? String else {
                return SentinelHTTPResponse(status: 400, body: jsonError("missing code"))
            }
            let deviceName = (payload["deviceName"] as? String) ?? "iOS Device"
            guard let body = await dataSource.redeemPairing(code: code, deviceName: deviceName) else {
                return SentinelHTTPResponse(status: 401, body: jsonError("invalid or expired code"))
            }
            return SentinelHTTPResponse(status: 200, body: body, contentType: "application/json")
        default:
            break
        }

        // Authenticated endpoints
        guard let token = request.bearerToken,
              let deviceID = await dataSource.authorize(token: token) else {
            return SentinelHTTPResponse(status: 401, body: jsonError("unauthorized"))
        }

        let parts = request.pathComponents
        switch (request.method, parts.count, parts.first) {
        case ("GET", 1, "cameras"):
            let data = await dataSource.camerasResponse()
            return SentinelHTTPResponse(status: 200, body: data, contentType: "application/json")

        case ("GET", 1, "alerts"):
            let data = await dataSource.alertsResponse()
            return SentinelHTTPResponse(status: 200, body: data, contentType: "application/json")

        case ("POST", 3, "alerts") where parts[2] == "acknowledge" || parts[2] == "lock-evidence":
            guard let alertID = UUID(uuidString: parts[1]) else {
                return SentinelHTTPResponse(status: 400, body: jsonError("bad alert id"))
            }
            let result = parts[2] == "acknowledge"
                ? await dataSource.acknowledgeAlert(alertID: alertID, deviceID: deviceID)
                : await dataSource.lockEvidence(alertID: alertID, deviceID: deviceID)
            return result.response

        case ("GET", 1, "events"):
            let data = await dataSource.eventsResponse()
            return SentinelHTTPResponse(status: 200, body: data, contentType: "application/json")

        case ("GET", 3, "events") where parts[2] == "thumbnail.jpg":
            guard let eventID = UUID(uuidString: parts[1]) else {
                return SentinelHTTPResponse(status: 400, body: jsonError("bad event id"))
            }
            guard let (data, mime) = await dataSource.eventThumbnail(eventID: eventID) else {
                return SentinelHTTPResponse(status: 404, body: jsonError("no thumbnail"))
            }
            return SentinelHTTPResponse(status: 200, body: data, contentType: mime)

        case ("POST", 2, "devices") where parts[1] == "register":
            guard let payload = try? JSONSerialization.jsonObject(with: request.body) as? [String: Any],
                  let apnsToken = payload["apnsToken"] as? String else {
                return SentinelHTTPResponse(status: 400, body: jsonError("missing apnsToken"))
            }
            await dataSource.registerDevice(deviceID: deviceID, apnsToken: apnsToken)
            return SentinelHTTPResponse(status: 200, body: Data("{\"ok\":true}".utf8), contentType: "application/json")

        // HLS proxy — forwards requests to MediaMTX (127.0.0.1:8888) and
        // rewrites m3u8 segment URLs to go back through this server so that
        // the single Cloudflare tunnel URL covers both the API and video.
        // Path: GET /hls/:cameraID/index.m3u8   (playlist, segments rewritten)
        //        GET /hls/:cameraID/:segment.ts  (raw segment pass-through)
        case ("GET", _, "hls") where parts.count >= 3:
            guard let cameraUUID = UUID(uuidString: parts[1]) else {
                return SentinelHTTPResponse(status: 400, body: jsonError("bad camera id"))
            }
            let filename = parts[2]
            let upstreamPath = await dataSource.liveStreamPath(cameraID: cameraUUID)
            return await Self.proxyHLS(cameraID: cameraUUID, upstreamPath: upstreamPath, filename: filename, token: token, query: request.query)

        case ("GET", _, "cameras") where parts.count >= 2:
            guard let uuid = UUID(uuidString: parts[1]) else {
                return SentinelHTTPResponse(status: 400, body: jsonError("bad camera id"))
            }
            if parts.count == 2 {
                guard let body = await dataSource.cameraDetailResponse(cameraID: uuid) else {
                    return SentinelHTTPResponse(status: 404, body: jsonError("not found"))
                }
                return SentinelHTTPResponse(status: 200, body: body, contentType: "application/json")
            }
            if parts.count == 3, parts[2] == "snapshot.jpg" {
                guard let (data, mime) = await dataSource.snapshotFile(cameraID: uuid) else {
                    return SentinelHTTPResponse(status: 404, body: jsonError("not found"))
                }
                return SentinelHTTPResponse(status: 200, body: data, contentType: mime)
            }
            if parts.count == 3, parts[2] == "segments" {
                let from = request.queryDate("from")
                let to = request.queryDate("to")
                let data = await dataSource.segmentsResponse(cameraID: uuid, from: from, to: to)
                return SentinelHTTPResponse(status: 200, body: data, contentType: "application/json")
            }
            if parts.count == 4, parts[2] == "segments" {
                guard let (data, mime) = await dataSource.segmentFile(cameraID: uuid, name: parts[3]) else {
                    return SentinelHTTPResponse(status: 404, body: jsonError("not found"))
                }
                return SentinelHTTPResponse(status: 200, body: data, contentType: mime)
            }
            return SentinelHTTPResponse(status: 404, body: jsonError("not found"))

        default:
            return SentinelHTTPResponse(status: 404, body: jsonError("not found"))
        }
    }

    // MARK: - HLS proxy

    private static func proxyHLS(cameraID: UUID, upstreamPath: String, filename: String, token: String, query: [String: String]) async -> SentinelHTTPResponse {
        // Guard against path traversal — only allow simple filenames.
        guard filename.contains("/") == false,
              filename.contains("..") == false else {
            return SentinelHTTPResponse(status: 400, body: jsonError("bad filename"))
        }
        // Fetch from the camera's live path (sub-stream when available); segment
        // URLs are still rewritten back to the iOS-facing /hls/<cameraID>/… path.
        // MediaMTX >= 1.21 ties playlist and segment requests together with a
        // `?session=` parameter — forward everything except our own token, or
        // MediaMTX answers "session not found".
        var components = URLComponents(string: "http://127.0.0.1:8888/\(upstreamPath)/\(filename)")
        let forwarded = query.filter { $0.key != "token" }.sorted { $0.key < $1.key }
        if forwarded.isEmpty == false {
            components?.queryItems = forwarded.map { URLQueryItem(name: $0.key, value: $0.value) }
        }
        guard let url = components?.url else {
            return SentinelHTTPResponse(status: 400, body: jsonError("bad filename"))
        }
        guard let (data, response) = try? await URLSession.shared.data(from: url),
              let http = response as? HTTPURLResponse,
              http.statusCode == 200 else {
            return SentinelHTTPResponse(status: 502, body: jsonError("stream not available"))
        }
        // Rewrite EVERY playlist (master index.m3u8 AND the media playlist it
        // points to, e.g. main_stream.m3u8) so the auth token propagates all the
        // way down to the .ts segment URLs. MediaMTX serves a 2-level playlist:
        // index.m3u8 → main_stream.m3u8 → *.ts.
        if filename.hasSuffix(".m3u8") {
            let rewritten = rewriteM3U8(data: data, cameraID: cameraID.uuidString, token: token)
            return SentinelHTTPResponse(status: 200, body: rewritten,
                                        contentType: "application/vnd.apple.mpegurl")
        }
        let mime = filename.hasSuffix(".ts") ? "video/MP2T" : "application/octet-stream"
        return SentinelHTTPResponse(status: 200, body: data, contentType: mime)
    }

    // Rewrites relative segment lines in an HLS playlist so they resolve back
    // through the Sentinel API server (with the caller's auth token embedded).
    // This keeps a single tunnel URL covering both the API and video segments.
    private static func rewriteM3U8(data: Data, cameraID: String, token: String) -> Data {
        guard let text = String(data: data, encoding: .utf8) else { return data }
        let rewritten = text.components(separatedBy: "\n").map { line -> String in
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            // Leave HLS directives and blank lines untouched.
            guard trimmed.isEmpty == false,
                  trimmed.hasPrefix("#") == false,
                  trimmed.hasPrefix("http") == false else { return line }
            // Lines may already carry a query (MediaMTX's `?session=`); append
            // the token with `&` then, or it would swallow the token.
            let separator = trimmed.contains("?") ? "&" : "?"
            return "/hls/\(cameraID)/\(trimmed)\(separator)token=\(token)"
        }
        return Data(rewritten.joined(separator: "\n").utf8)
    }

    /// Hard ceiling on a single request (headers + body). This is a small
    /// JSON / pairing API exposed over the public Cloudflare tunnel, so anything
    /// larger is junk or abuse — never buffer it unbounded.
    private static let maxRequestBytes = 1 * 1024 * 1024
    /// Overall deadline so a slow or never-completing request can't pin the
    /// connection (and its detached Task) indefinitely.
    private static let receiveTimeout: TimeInterval = 15

    private static func receive(connection: NWConnection, atLeast: Int) async -> Data? {
        await withCheckedContinuation { (continuation: CheckedContinuation<Data?, Never>) in
            let lock = NSLock()
            var finished = false
            var collected = Data()
            // Resume exactly once, whichever of {complete, cap hit, timeout} wins.
            func finish(_ value: Data?) {
                lock.lock(); let already = finished; finished = true; lock.unlock()
                guard already == false else { return }
                continuation.resume(returning: value)
            }
            // Slow-loris / never-finishing guard. Rejects (nil) rather than read
            // `collected` from another thread, so there's no data race.
            DispatchQueue.global().asyncAfter(deadline: .now() + receiveTimeout) { finish(nil) }
            func readMore() {
                connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { data, _, isComplete, error in
                    if let data { collected.append(data) }
                    // Bound total buffered bytes regardless of Content-Length.
                    if collected.count > maxRequestBytes { finish(collected); return }
                    let hasHeaders = collected.range(of: Data("\r\n\r\n".utf8)) != nil
                    if hasHeaders {
                        // Check Content-Length to know if we need more
                        if let headerEnd = collected.range(of: Data("\r\n\r\n".utf8)),
                           let headerStr = String(data: collected.subdata(in: 0..<headerEnd.lowerBound), encoding: .utf8) {
                            let contentLength = headerStr
                                .split(separator: "\r\n")
                                .first(where: { $0.lowercased().hasPrefix("content-length:") })
                                .flatMap { Int($0.split(separator: ":")[1].trimmingCharacters(in: .whitespaces)) }
                                ?? 0
                            // Reject an absurd advertised body up front.
                            if contentLength > maxRequestBytes { finish(collected); return }
                            let bodyStart = headerEnd.upperBound
                            let bodyLen = collected.count - bodyStart
                            if bodyLen >= contentLength {
                                finish(collected)
                                return
                            }
                        }
                    }
                    if error != nil || isComplete {
                        finish(collected.isEmpty ? nil : collected)
                        return
                    }
                    readMore()
                }
            }
            readMore()
        }
    }

    private static func send(connection: NWConnection, response: SentinelHTTPResponse) async {
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            connection.send(content: response.serialize(), completion: .contentProcessed { _ in
                continuation.resume()
            })
        }
    }

    private static func jsonError(_ message: String) -> Data {
        Data("{\"error\":\"\(message)\"}".utf8)
    }
}

// MARK: - Request

struct SentinelHTTPRequest {
    let method: String
    let path: String
    let query: [String: String]
    let headers: [String: String]
    let body: Data

    var pathComponents: [String] {
        path.split(separator: "/").map { String($0).removingPercentEncoding ?? String($0) }
    }

    var bearerToken: String? {
        if let auth = headers["authorization"] ?? headers["Authorization"],
           auth.lowercased().hasPrefix("bearer ") {
            return String(auth.dropFirst(7)).trimmingCharacters(in: .whitespaces)
        }
        // Fallback: token in query string, used by AVPlayer / <img> consumers
        // that can't attach an Authorization header (snapshot.jpg, segments/*.mp4).
        if let queryToken = query["token"]?.trimmingCharacters(in: .whitespaces),
           !queryToken.isEmpty {
            return queryToken
        }
        return nil
    }

    func queryDate(_ key: String) -> Date? {
        guard let raw = query[key] else { return nil }
        if let seconds = TimeInterval(raw) { return Date(timeIntervalSince1970: seconds) }
        let formatter = ISO8601DateFormatter()
        return formatter.date(from: raw)
    }

    static func parse(_ data: Data) -> SentinelHTTPRequest? {
        guard let headerEnd = data.range(of: Data("\r\n\r\n".utf8)) else { return nil }
        let headerStr = String(data: data.subdata(in: 0..<headerEnd.lowerBound), encoding: .utf8) ?? ""
        let body = data.subdata(in: headerEnd.upperBound..<data.count)

        var lines = headerStr.components(separatedBy: "\r\n")
        guard let requestLine = lines.first else { return nil }
        lines.removeFirst()
        let parts = requestLine.split(separator: " ", maxSplits: 2).map(String.init)
        guard parts.count >= 2 else { return nil }
        let method = parts[0]
        let rawTarget = parts[1]

        let (path, query) = splitPathQuery(rawTarget)

        var headers: [String: String] = [:]
        for line in lines where line.isEmpty == false {
            if let colon = line.firstIndex(of: ":") {
                let name = String(line[..<colon]).lowercased()
                let value = String(line[line.index(after: colon)...]).trimmingCharacters(in: .whitespaces)
                headers[name] = value
            }
        }

        return SentinelHTTPRequest(method: method, path: path, query: query, headers: headers, body: body)
    }

    private static func splitPathQuery(_ target: String) -> (String, [String: String]) {
        guard let q = target.firstIndex(of: "?") else { return (target, [:]) }
        let path = String(target[..<q])
        var dict: [String: String] = [:]
        let queryStr = target[target.index(after: q)...]
        for pair in queryStr.split(separator: "&") {
            let kv = pair.split(separator: "=", maxSplits: 1)
            if kv.count == 2 {
                let key = String(kv[0]).removingPercentEncoding ?? String(kv[0])
                let value = String(kv[1]).removingPercentEncoding ?? String(kv[1])
                dict[key] = value
            }
        }
        return (path, dict)
    }
}

// MARK: - Response

struct SentinelHTTPResponse {
    let status: Int
    let body: Data
    let contentType: String
    /// Optional extra response headers (e.g. X-Sentinel-Remote-URL).
    var extraHeaders: [String: String] = [:]

    init(status: Int, body: Data, contentType: String = "application/json") {
        self.status = status
        self.body = body
        self.contentType = contentType
    }

    static func json(_ object: [String: Any]) -> SentinelHTTPResponse {
        let data = (try? JSONSerialization.data(withJSONObject: object)) ?? Data("{}".utf8)
        return SentinelHTTPResponse(status: 200, body: data)
    }

    static let badRequest = SentinelHTTPResponse(
        status: 400,
        body: Data("{\"error\":\"bad request\"}".utf8)
    )

    func serialize() -> Data {
        let reason = Self.reason(for: status)
        var headers = [
            "HTTP/1.1 \(status) \(reason)",
            "Content-Type: \(contentType)",
            "Content-Length: \(body.count)",
            "Access-Control-Allow-Origin: *",
            "Connection: close"
        ]
        for (name, value) in extraHeaders {
            headers.append("\(name): \(value)")
        }
        headers.append("")
        headers.append("")
        let head = headers.joined(separator: "\r\n")
        var data = Data(head.utf8)
        data.append(body)
        return data
    }

    private static func reason(for status: Int) -> String {
        switch status {
        case 200: return "OK"
        case 400: return "Bad Request"
        case 401: return "Unauthorized"
        case 404: return "Not Found"
        case 409: return "Conflict"
        case 502: return "Bad Gateway"
        case 500: return "Internal Server Error"
        case 503: return "Service Unavailable"
        default: return "OK"
        }
    }
}
