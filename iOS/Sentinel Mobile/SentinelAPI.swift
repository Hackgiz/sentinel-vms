// SentinelAPI.swift
// Drop into your Sentinel Mobile Xcode target.
//
// All network access to the Mac VMS goes through SentinelAPI. The session
// holds the paired server URL + bearer token in the Keychain, so the user
// only has to scan the QR once.
//
// Remote access: when a Cloudflare tunnel URL is stored alongside the LAN
// URL, execute() tries the LAN address first (1.5s timeout). On success it
// stays on LAN (low latency). On failure it falls back to the remote URL
// transparently — no user action needed when switching between Wi-Fi and 5G.

import Foundation

public struct SentinelCameraSummary: Decodable, Identifiable, Hashable {
    public let id: UUID
    public let name: String
    public let location: String
    public let ipAddress: String
    public let status: String      // "online" | "offline" | "motion"
    public let isRecording: Bool
    public let resolution: String
    public let fps: Int
    public let supportsPTZ: Bool
    /// Relative proxy path, e.g. "/hls/<uuid>/index.m3u8".
    /// Use SentinelSession.shared.hlsURL(for:) to get the full authenticated URL.
    public let hlsURL: String?
}

public struct SentinelAlert: Decodable, Identifiable, Hashable {
    public let id: UUID
    public let title: String
    public let detail: String
    public let severity: String
    public var state: String
    public let createdAt: TimeInterval
    public let cameraID: UUID?

    // Added with Mac VMS remote actions. All optional so the app still decodes
    // alarms from a Mac that hasn't been updated yet.
    public var lastEventAt: TimeInterval? = nil
    public var kind: String? = nil
    public var owner: String? = nil
    public var eventCount: Int? = nil
    public var cameraName: String? = nil
    /// A recording is linked to this alarm, so it can be locked as evidence.
    public var hasClip: Bool? = nil
    /// What the operator should do — from the Mac's alarm rules.
    public var instructions: [SentinelAlarmInstruction]? = nil
    /// Evidence already captured for this alarm, if any.
    public var evidence: SentinelEvidence? = nil

    public var createdAtDate: Date { Date(timeIntervalSince1970: createdAt) }
    public var lastEventDate: Date { Date(timeIntervalSince1970: lastEventAt ?? createdAt) }
    public var needsAttention: Bool { state == "New" }
    public var canAcknowledge: Bool { state == "New" || state == "Snoozed" }
    public var isEvidenceLocked: Bool { evidence?.isLocked == true }
    public var canLockEvidence: Bool { hasClip == true && isEvidenceLocked == false }
    /// "Unassigned" is the Mac's placeholder, not a person.
    public var assignee: String? {
        guard let owner, owner.isEmpty == false, owner != "Unassigned", owner != "Current Operator" else { return nil }
        return owner
    }
}

public struct SentinelAlarmInstruction: Decodable, Hashable {
    public let rule: String
    public let text: String
}

public struct SentinelEvidence: Decodable, Identifiable, Hashable {
    public let id: UUID
    public let caseID: String
    public let title: String
    public let camera: String
    public let range: String
    public let status: String
    public let isLocked: Bool
    public var lockedAt: TimeInterval? = nil
    public var sha256: String? = nil
    public var lockedBy: String? = nil
    public var alertID: UUID? = nil
}

public struct SentinelEvent: Decodable, Identifiable, Hashable {
    public let id: UUID
    public let cameraID: UUID
    public let cameraName: String
    public let kind: String                 // "Person" | "Vehicle" | "Loitering" | ...
    public let confidence: Double
    public let createdAt: TimeInterval
    public let hasThumbnail: Bool
    public let description: String?          // Ring-style AI scene description
    public let detectedText: String?         // e.g. license plate text

    public var createdAtDate: Date { Date(timeIntervalSince1970: createdAt) }
}

/// Live health for one camera, from GET /cameras/:id. Optional fields are
/// omitted by the Mac when unavailable (no frame yet / no error), so they must
/// stay optional here.
public struct SentinelCameraDetail: Decodable, Hashable {
    public let id: UUID
    public let name: String
    public let location: String
    public let status: String
    public let isRecording: Bool
    public let estimatedFPS: Double
    public let segmentCount: Int
    public let eventCount: Int
    public let supportsPTZ: Bool
    public let lastFrameAt: TimeInterval?
    public let lastError: String?
    public let hlsURL: String?

    public var lastFrameDate: Date? { lastFrameAt.map { Date(timeIntervalSince1970: $0) } }
}

public struct SentinelSegment: Decodable, Identifiable, Hashable {
    public var id: String { name }
    public let name: String
    public let createdAt: TimeInterval
    public let modifiedAt: TimeInterval
    public let sizeBytes: Int64

    public var createdAtDate: Date { Date(timeIntervalSince1970: createdAt) }
    public var modifiedAtDate: Date { Date(timeIntervalSince1970: modifiedAt) }
}

public struct SentinelPairResponse: Decodable {
    public let deviceID: UUID
    public let token: String
    public let name: String
    public let serverName: String
    public let remoteURL: String?
}

public enum SentinelAPIError: LocalizedError {
    case notPaired
    case http(Int, String?)
    case decoding(Error)
    case underlying(Error)
    /// The Mac answered but doesn't know this endpoint — it runs an older build.
    case macNeedsUpdate

    public var errorDescription: String? {
        switch self {
        case .notPaired: return "Not paired to a Sentinel server yet."
        case .http(let status, let msg):
            if let msg, msg.isEmpty == false { return msg }
            return "The Mac returned an error (\(status))."
        case .decoding(let err): return "Decoding failed: \(err.localizedDescription)"
        case .underlying(let err): return err.localizedDescription
        case .macNeedsUpdate: return "Update Sentinel VMS on your Mac to use this from your phone."
        }
    }

    /// Pulls `{"error": "..."}` out of a Mac error body so users see the
    /// sentence, not raw JSON.
    static func message(from body: Data) -> String? {
        if let obj = try? JSONSerialization.jsonObject(with: body) as? [String: Any],
           let message = obj["error"] as? String {
            return message
        }
        let text = String(data: body, encoding: .utf8)
        return text?.isEmpty == false ? text : nil
    }
}

@MainActor
public final class SentinelSession: ObservableObject {
    public static let shared = SentinelSession()

    /// Primary (LAN) server URL set at pairing time.
    @Published public private(set) var serverURL: URL?
    /// Cloudflare tunnel URL, set at pairing time or from the pair response.
    @Published public private(set) var remoteURL: URL?
    /// Whichever URL succeeded on the last request — reflects current reachability.
    @Published public private(set) var activeServerURL: URL?
    @Published public private(set) var serverName: String?
    @Published public private(set) var isPaired: Bool = false
    @Published public private(set) var isDemoMode: Bool = false
    /// True when the last successful request used the remote (Cloudflare) URL.
    @Published public private(set) var isUsingRemote: Bool = false

    private var token: String?
    private let keychainService = "com.handoffgrid.sentinel.mobile"
    private let urlKey = "sentinel.serverURL"
    private let remoteURLKey = "sentinel.remoteURL"
    private let nameKey = "sentinel.serverName"

    private init() {
        loadFromKeychain()
        activeServerURL = serverURL
    }

    // MARK: - Demo Mode

    public func enterDemoMode() {
        isDemoMode = true
        serverName = "Demo Server"
        serverURL = URL(string: "http://demo.local")
        activeServerURL = serverURL
        isPaired = true
    }

    // MARK: - Pairing

    public func pairManually(serverURL urlString: String, code: String, deviceName: String) async throws {
        let trimmedURL = urlString.trimmingCharacters(in: .whitespaces)
        guard let url = URL(string: trimmedURL), url.scheme != nil, url.host != nil else {
            throw SentinelAPIError.http(400, "Enter a full URL, e.g. http://192.168.1.10:8090")
        }
        let trimmedCode = code.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedCode.isEmpty else {
            throw SentinelAPIError.http(400, "Pairing code is required")
        }
        let pair = try await sendPairRequest(to: url, code: trimmedCode, deviceName: deviceName)
        apply(pair: pair, localURL: url)
    }

    public func pair(payloadFromQR qrString: String, deviceName: String) async throws {
        // Payload format: sentinel-vms://pair?<percent-encoded JSON {url, code, v, remoteURL?}>
        let trimmed = qrString
            .replacingOccurrences(of: "sentinel-vms://pair?", with: "")
            .removingPercentEncoding ?? qrString
        guard let data = trimmed.data(using: .utf8),
              let dict = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let urlString = dict["url"] as? String,
              let code = dict["code"] as? String,
              let url = URL(string: urlString) else {
            throw SentinelAPIError.http(400, "Bad pairing payload")
        }
        // Only accept a remote URL that is https — the tunnel always is, and ATS
        // would silently block a plain-http one off-network (looks "broken").
        let embeddedRemote = Self.httpsURL(dict["remoteURL"] as? String)

        // Try the LAN address first (fast), then fall back to the remote tunnel
        // URL from the QR. This is what makes scanning the QR OFF-network work:
        // on cellular the LAN attempt fails quickly and we pair over the tunnel.
        let pair: SentinelPairResponse
        let pairedVia: URL
        if let lanPair = try? await sendPairRequest(to: url, code: code, deviceName: deviceName, timeout: 2.5) {
            pair = lanPair
            pairedVia = url
        } else if let remote = embeddedRemote {
            pair = try await sendPairRequest(to: remote, code: code, deviceName: deviceName, timeout: 30)
            pairedVia = remote
        } else {
            // No remote URL in the QR and the LAN address didn't answer — retry
            // the LAN address with a normal timeout so the real error surfaces.
            pair = try await sendPairRequest(to: url, code: code, deviceName: deviceName)
            pairedVia = url
        }
        // Prefer remoteURL from the server response; fall back to the QR embed.
        let resolvedRemote = Self.httpsURL(pair.remoteURL) ?? embeddedRemote
        apply(pair: pair, localURL: url, remoteURL: resolvedRemote, pairedVia: pairedVia)
    }

    /// Returns the URL only if it parses and is https (ATS-safe for the tunnel).
    private static func httpsURL(_ string: String?) -> URL? {
        guard let string, let url = URL(string: string),
              url.scheme?.lowercased() == "https" else { return nil }
        return url
    }

    private func sendPairRequest(to url: URL, code: String, deviceName: String, timeout: TimeInterval = 10) async throws -> SentinelPairResponse {
        var request = URLRequest(url: url.appendingPathComponent("pair"))
        request.httpMethod = "POST"
        request.timeoutInterval = timeout
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["code": code, "deviceName": deviceName])
        let (body, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            let status = (response as? HTTPURLResponse)?.statusCode ?? -1
            throw SentinelAPIError.http(status, String(data: body, encoding: .utf8))
        }
        return try JSONDecoder().decode(SentinelPairResponse.self, from: body)
    }

    private func apply(pair: SentinelPairResponse, localURL: URL, remoteURL: URL? = nil, pairedVia: URL? = nil) {
        let resolvedRemote = remoteURL ?? pair.remoteURL.flatMap { URL(string: $0) }
        // Start the session on whichever URL actually answered the pair request.
        // If we paired over the tunnel (off-network), the first cameras() call
        // must go to the remote URL, not the unreachable LAN address.
        let startURL = pairedVia ?? localURL
        let startedRemote = (resolvedRemote != nil && startURL == resolvedRemote)
        self.token = pair.token
        self.serverURL = localURL
        self.remoteURL = resolvedRemote
        self.activeServerURL = startURL
        self.serverName = pair.serverName
        self.isPaired = true
        self.isDemoMode = false
        self.isUsingRemote = startedRemote
        saveToKeychain(token: pair.token, url: localURL, remoteURL: resolvedRemote, name: pair.serverName)
        flushPendingAPNSToken()
    }

    public func unpair() {
        token = nil
        serverURL = nil
        remoteURL = nil
        activeServerURL = nil
        serverName = nil
        isPaired = false
        isDemoMode = false
        isUsingRemote = false
        deleteFromKeychain()
    }

    // MARK: - HLS URL helper

    /// Returns the full authenticated HLS URL for a camera, resolved against
    /// the currently active server URL (LAN or remote). Returns nil if the
    /// camera has no HLS proxy path or the session isn't paired.
    public func hlsURL(for camera: SentinelCameraSummary) -> URL? {
        guard let path = camera.hlsURL else { return nil }
        let base = activeServerURL ?? serverURL
        let stripped = path.hasPrefix("/") ? String(path.dropFirst()) : path
        return signed(base?.appendingPathComponent(stripped))
    }

    // MARK: - Endpoints

    public func cameras() async throws -> [SentinelCameraSummary] {
        if isDemoMode { return SentinelDemoData.cameras }
        return try await get([SentinelCameraSummary].self, path: "cameras")
    }

    public func alerts() async throws -> [SentinelAlert] {
        if isDemoMode { return SentinelDemoData.alerts }
        return try await get([SentinelAlert].self, path: "alerts")
    }

    /// Live health detail for a single camera (FPS, segment/event counts, last
    /// error). Returns nil in demo mode — callers fall back to the summary.
    public func cameraDetail(cameraID: UUID) async throws -> SentinelCameraDetail? {
        if isDemoMode { return nil }
        return try await get(SentinelCameraDetail.self, path: "cameras/\(cameraID.uuidString)")
    }

    public func events() async throws -> [SentinelEvent] {
        if isDemoMode { return SentinelDemoData.events }
        return try await get([SentinelEvent].self, path: "events")
    }

    /// Signed URL for a detection event's frame thumbnail.
    public func eventThumbnailURL(eventID: UUID) -> URL? {
        signed((activeServerURL ?? serverURL)?.appendingPathComponent("events/\(eventID.uuidString)/thumbnail.jpg"))
    }

    public func segments(cameraID: UUID, from: Date? = nil, to: Date? = nil) async throws -> [SentinelSegment] {
        if isDemoMode { return SentinelDemoData.segments }
        var query: [URLQueryItem] = []
        if let from { query.append(URLQueryItem(name: "from", value: "\(Int(from.timeIntervalSince1970))")) }
        if let to { query.append(URLQueryItem(name: "to", value: "\(Int(to.timeIntervalSince1970))")) }
        return try await get([SentinelSegment].self, path: "cameras/\(cameraID.uuidString)/segments", query: query)
    }

    public func segmentURL(cameraID: UUID, name: String) -> URL? {
        signed((activeServerURL ?? serverURL)?.appendingPathComponent("cameras/\(cameraID.uuidString)/segments/\(name)"))
    }

    public func snapshotURL(cameraID: UUID) -> URL? {
        signed((activeServerURL ?? serverURL)?.appendingPathComponent("cameras/\(cameraID.uuidString)/snapshot.jpg"))
    }

    /// Append the bearer token as a `?token=` query item so that AVPlayer and
    /// other consumers which can't add an Authorization header can still
    /// authenticate. The Mac server accepts this as an alternative to the header.
    private func signed(_ url: URL?) -> URL? {
        guard let url, let token else { return url }
        guard var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return url
        }
        var items = components.queryItems ?? []
        items.append(URLQueryItem(name: "token", value: token))
        components.queryItems = items
        return components.url
    }

    // MARK: Alarm actions

    /// Acknowledges an alarm on the Mac. Returns the alarm as the Mac now sees it.
    public func acknowledge(alertID: UUID) async throws -> SentinelAlert {
        if isDemoMode { return SentinelDemoData.acknowledge(alertID) }
        let data = try await remoteAction(path: "alerts/\(alertID.uuidString)/acknowledge")
        do { return try JSONDecoder().decode(SentinelAlert.self, from: data) }
        catch { throw SentinelAPIError.decoding(error) }
    }

    /// Packages the alarm's recording as evidence and locks it on the Mac, so
    /// retention cleanup can't delete it.
    public func lockEvidence(alertID: UUID) async throws -> SentinelEvidence {
        if isDemoMode { return SentinelDemoData.lockEvidence(alertID) }
        let data = try await remoteAction(path: "alerts/\(alertID.uuidString)/lock-evidence")
        do { return try JSONDecoder().decode(SentinelEvidence.self, from: data) }
        catch { throw SentinelAPIError.decoding(error) }
    }

    /// POST to an action endpoint; a bare 404 "not found" means the Mac build
    /// predates the endpoint (a missing alarm returns a descriptive message).
    private func remoteAction(path: String) async throws -> Data {
        do {
            return try await post(path: path, body: nil)
        } catch SentinelAPIError.http(404, let message) where message == nil || message == "not found" {
            throw SentinelAPIError.macNeedsUpdate
        }
    }

    public func registerAPNSToken(_ token: String) async throws {
        let body = try JSONSerialization.data(withJSONObject: ["apnsToken": token])
        _ = try await post(path: "devices/register", body: body)
    }

    /// The APNs device token, retained so it can be (re)sent to the Mac the
    /// moment we're paired — the registration endpoint requires auth, and the
    /// token often arrives before pairing completes.
    private var pendingAPNSToken: String?

    /// Called by the app delegate when iOS hands us a device token. Sends it
    /// immediately if paired; otherwise it's flushed by `apply(pair:)`.
    public func updateAPNSToken(_ hexToken: String) {
        pendingAPNSToken = hexToken
        guard isPaired, isDemoMode == false else { return }
        Task { try? await registerAPNSToken(hexToken) }
    }

    private func flushPendingAPNSToken() {
        guard let token = pendingAPNSToken, isDemoMode == false else { return }
        Task { try? await registerAPNSToken(token) }
    }

    // MARK: - Helpers

    private func get<T: Decodable>(_ type: T.Type, path: String, query: [URLQueryItem] = []) async throws -> T {
        let (data, _) = try await execute(method: "GET", path: path, query: query, body: nil)
        do { return try JSONDecoder().decode(T.self, from: data) }
        catch { throw SentinelAPIError.decoding(error) }
    }

    @discardableResult
    private func post(path: String, body: Data?) async throws -> Data {
        let (data, _) = try await execute(method: "POST", path: path, query: [], body: body)
        return data
    }

    private func execute(method: String, path: String, query: [URLQueryItem], body: Data?) async throws -> (Data, HTTPURLResponse) {
        guard let localURL = serverURL, let token else { throw SentinelAPIError.notPaired }

        // Try the LAN URL first. If we also have a remote URL, give LAN only
        // 1.5s before falling back — fast enough on a local network, short
        // enough that 5G users don't wait long when the Mac isn't reachable.
        if let remote = remoteURL, remote != localURL {
            if let result = try? await attemptRequest(baseURL: localURL, method: method,
                                                      path: path, query: query, body: body,
                                                      token: token, timeout: 1.5) {
                activeServerURL = localURL
                isUsingRemote = false
                ingestRemoteURLHeader(result.1)
                return result
            }
            let result = try await attemptRequest(baseURL: remote, method: method,
                                                  path: path, query: query, body: body,
                                                  token: token, timeout: 30)
            activeServerURL = remote
            isUsingRemote = true
            ingestRemoteURLHeader(result.1)
            return result
        }

        // Single URL — use it directly.
        let result = try await attemptRequest(baseURL: localURL, method: method,
                                              path: path, query: query, body: body,
                                              token: token, timeout: 30)
        activeServerURL = localURL
        isUsingRemote = false
        ingestRemoteURLHeader(result.1)
        return result
    }

    /// When a LAN request succeeds, the Mac advertises its current Cloudflare
    /// tunnel URL via `X-Sentinel-Remote-URL`. Because that quick-tunnel URL
    /// rotates on every Mac restart, we adopt the fresh value here so the phone
    /// keeps working off-network later without ever re-pairing.
    private func ingestRemoteURLHeader(_ http: HTTPURLResponse) {
        guard let value = http.value(forHTTPHeaderField: "X-Sentinel-Remote-URL"),
              value.isEmpty == false,
              let url = URL(string: value),
              url.scheme?.lowercased() == "https",  // ATS-safe; the tunnel is always https
              url != remoteURL else { return }
        remoteURL = url
        UserDefaults.standard.set(url.absoluteString, forKey: remoteURLKey)
    }

    private func attemptRequest(baseURL: URL, method: String, path: String,
                                query: [URLQueryItem], body: Data?,
                                token: String, timeout: TimeInterval) async throws -> (Data, HTTPURLResponse) {
        var components = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if query.isEmpty == false { components.queryItems = query }
        guard let url = components.url else { throw SentinelAPIError.http(400, "Bad URL") }

        var request = URLRequest(url: url, timeoutInterval: timeout)
        request.httpMethod = method
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = body
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw SentinelAPIError.http(-1, "Not HTTP")
        }
        if http.statusCode == 401 {
            await MainActor.run { unpair() }
            throw SentinelAPIError.notPaired
        }
        guard (200..<300).contains(http.statusCode) else {
            throw SentinelAPIError.http(http.statusCode, SentinelAPIError.message(from: data))
        }
        return (data, http)
    }

    // MARK: - Keychain

    private func saveToKeychain(token: String, url: URL, remoteURL: URL?, name: String?) {
        UserDefaults.standard.set(url.absoluteString, forKey: urlKey)
        UserDefaults.standard.set(remoteURL?.absoluteString, forKey: remoteURLKey)
        if let name { UserDefaults.standard.set(name, forKey: nameKey) }
        let data = Data(token.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: keychainService,
            kSecAttrAccount as String: "auth"
        ]
        SecItemDelete(query as CFDictionary)
        var insert = query
        insert[kSecValueData as String] = data
        SecItemAdd(insert as CFDictionary, nil)
    }

    private func loadFromKeychain() {
        if let urlString = UserDefaults.standard.string(forKey: urlKey),
           let url = URL(string: urlString) {
            self.serverURL = url
        }
        if let remoteString = UserDefaults.standard.string(forKey: remoteURLKey),
           let url = URL(string: remoteString) {
            self.remoteURL = url
        }
        self.serverName = UserDefaults.standard.string(forKey: nameKey)

        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: keychainService,
            kSecAttrAccount as String: "auth",
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]
        var item: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        if status == errSecSuccess,
           let data = item as? Data,
           let token = String(data: data, encoding: .utf8) {
            self.token = token
            self.isPaired = (serverURL != nil)
        }
    }

    private func deleteFromKeychain() {
        UserDefaults.standard.removeObject(forKey: urlKey)
        UserDefaults.standard.removeObject(forKey: remoteURLKey)
        UserDefaults.standard.removeObject(forKey: nameKey)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: keychainService,
            kSecAttrAccount as String: "auth"
        ]
        SecItemDelete(query as CFDictionary)
    }
}

// MARK: - Demo Data

enum SentinelDemoData {
    static let cameraIDs: [UUID] = [
        UUID(uuidString: "11111111-1111-1111-1111-111111111111")!,
        UUID(uuidString: "22222222-2222-2222-2222-222222222222")!,
        UUID(uuidString: "33333333-3333-3333-3333-333333333333")!,
        UUID(uuidString: "44444444-4444-4444-4444-444444444444")!,
        UUID(uuidString: "55555555-5555-5555-5555-555555555555")!,
        UUID(uuidString: "66666666-6666-6666-6666-666666666666")!
    ]

    static let cameras: [SentinelCameraSummary] = [
        .init(id: cameraIDs[0], name: "Front Entrance", location: "Building A", ipAddress: "192.168.1.21", status: "online", isRecording: true, resolution: "1920×1080", fps: 30, supportsPTZ: false, hlsURL: nil),
        .init(id: cameraIDs[1], name: "Parking Lot", location: "North", ipAddress: "192.168.1.22", status: "motion", isRecording: true, resolution: "2560×1440", fps: 25, supportsPTZ: true, hlsURL: nil),
        .init(id: cameraIDs[2], name: "Loading Dock", location: "Rear", ipAddress: "192.168.1.23", status: "online", isRecording: true, resolution: "1920×1080", fps: 30, supportsPTZ: false, hlsURL: nil),
        .init(id: cameraIDs[3], name: "Server Room", location: "Floor 2", ipAddress: "192.168.1.24", status: "online", isRecording: true, resolution: "1280×720", fps: 15, supportsPTZ: false, hlsURL: nil),
        .init(id: cameraIDs[4], name: "Lobby", location: "Floor 1", ipAddress: "192.168.1.25", status: "offline", isRecording: false, resolution: "1920×1080", fps: 30, supportsPTZ: true, hlsURL: nil),
        .init(id: cameraIDs[5], name: "Side Gate", location: "East Fence", ipAddress: "192.168.1.26", status: "online", isRecording: true, resolution: "1920×1080", fps: 30, supportsPTZ: false, hlsURL: nil)
    ]

    /// Mutable so demo-mode Acknowledge / Lock Evidence visibly stick across
    /// the 8-second refresh loop, like they would against a real Mac.
    @MainActor static var alerts: [SentinelAlert] = {
        let now = Date().timeIntervalSince1970
        return [
            .init(id: UUID(), title: "Person detected", detail: "2 people near vehicle row 3", severity: "Warning", state: "New", createdAt: now - 120, cameraID: cameraIDs[1],
                  kind: "Person", eventCount: 3, cameraName: "Parking Lot", hasClip: true,
                  instructions: [.init(rule: "After-hours lot", text: "Check the lot camera, then call the site manager at 555-0100 if anyone is near the vehicles.")]),
            .init(id: UUID(), title: "Camera offline", detail: "Stopped delivering video from 192.168.1.25", severity: "Critical", state: "New", createdAt: now - 480, cameraID: cameraIDs[4],
                  kind: "Camera Offline", cameraName: "Lobby", hasClip: false,
                  instructions: [.init(rule: "Recording and camera health", text: "Power-cycle the Lobby PoE port. If it stays down, open a ticket with IT.")]),
            .init(id: UUID(), title: "Motion at front door", detail: "Sustained motion 14s", severity: "Info", state: "Investigating", createdAt: now - 600, cameraID: cameraIDs[0],
                  kind: "Motion", owner: "Dana", eventCount: 2, cameraName: "Front Entrance", hasClip: true),
            .init(id: UUID(), title: "Loitering detected", detail: "Figure stationary near the dock door for 3 min", severity: "Warning", state: "Acknowledged", createdAt: now - 1800, cameraID: cameraIDs[2],
                  kind: "Person", owner: "Eric's iPhone (iPhone)", cameraName: "Loading Dock", hasClip: true,
                  evidence: .init(id: UUID(), caseID: "HG-20261007-00012", title: "Recorded Segment Export", camera: "Loading Dock", range: "Oct 7 01:10", status: "Locked", isLocked: true)),
            .init(id: UUID(), title: "Recording disk low", detail: "Macintosh HD has 18.2 GB free (8%).", severity: "Warning", state: "Snoozed", createdAt: now - 14400, cameraID: nil,
                  kind: "Storage", cameraName: nil, hasClip: false)
        ]
    }()

    @MainActor static func acknowledge(_ id: UUID) -> SentinelAlert {
        guard let i = alerts.firstIndex(where: { $0.id == id }) else { return alerts[0] }
        if alerts[i].canAcknowledge {
            alerts[i].state = "Acknowledged"
            alerts[i].owner = "This iPhone"
        }
        return alerts[i]
    }

    @MainActor static func lockEvidence(_ id: UUID) -> SentinelEvidence {
        let i = alerts.firstIndex(where: { $0.id == id }) ?? 0
        let clip = alerts[i].evidence ?? SentinelEvidence(
            id: UUID(), caseID: "HG-\(Date().formatted(.iso8601.year().month().day().dateSeparator(.omitted)))-\(Int.random(in: 10...99))",
            title: "Recorded Segment Export", camera: alerts[i].cameraName ?? "Camera",
            range: Date().formatted(date: .abbreviated, time: .shortened), status: "Locked", isLocked: true,
            lockedAt: Date().timeIntervalSince1970, sha256: String(repeating: "a1b2c3d4", count: 8), lockedBy: "This iPhone", alertID: id
        )
        alerts[i].evidence = clip
        return clip
    }

    static let events: [SentinelEvent] = {
        let now = Date().timeIntervalSince1970
        return [
            .init(id: UUID(), cameraID: cameraIDs[0], cameraName: "Front Entrance", kind: "Person", confidence: 0.94, createdAt: now - 90, hasThumbnail: false, description: "A person in a dark jacket carrying a backpack approaches the front door.", detectedText: nil),
            .init(id: UUID(), cameraID: cameraIDs[1], cameraName: "Parking Lot", kind: "Vehicle", confidence: 0.88, createdAt: now - 540, hasThumbnail: false, description: "A white SUV pulls into a parking space near row 3.", detectedText: nil),
            .init(id: UUID(), cameraID: cameraIDs[1], cameraName: "Parking Lot", kind: "License Plate", confidence: 0.79, createdAt: now - 600, hasThumbnail: false, description: nil, detectedText: "7XK A492"),
            .init(id: UUID(), cameraID: cameraIDs[2], cameraName: "Loading Dock", kind: "Loitering", confidence: 1.0, createdAt: now - 1500, hasThumbnail: false, description: "A figure has remained near the dock door for over three minutes.", detectedText: "190"),
            .init(id: UUID(), cameraID: cameraIDs[5], cameraName: "Side Gate", kind: "Person", confidence: 0.91, createdAt: now - 3200, hasThumbnail: false, description: "Two people in hi-vis vests walk past the east fence line.", detectedText: nil)
        ]
    }()

    static let segments: [SentinelSegment] = {
        let now = Date().timeIntervalSince1970
        return (0..<24).map { i in
            let start = now - Double(i) * 600
            return SentinelSegment(name: "seg-\(Int(start)).mp4", createdAt: start, modifiedAt: start + 600, sizeBytes: Int64.random(in: 18_000_000...42_000_000))
        }
    }()
}
