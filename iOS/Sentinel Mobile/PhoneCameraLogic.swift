// PhoneCameraLogic.swift
// WAVE23 (stream I): "Use this iPhone as a camera" (WAVE23-PHONE-CAMERA.md
// §A push protocol, §C). The capture side (PhoneCameraCapture) hands this
// file an fMP4 init segment and ~1 s media segments; everything here is
// UI-free and unit tested with synthetic segments:
//
// - SegmentBacklog: the queue and its "more than 20 s behind → drop the
//   oldest whole segments" policy.
// - PushTokenClock: push tokens live 5 min, the phone refreshes at 4.
// - PhoneCameraPhase: the screen's state machine.
// - PushTransport / HTTPPushTransport: the push plane on the video gateway.
// - PhoneStreamUplink: the sequential uploader (retry with backoff, new
//   session after 404 / a new init segment, 409 {expected} resync).

import Foundation
import Security

// MARK: - Settings

enum PhoneCameraQuality: String, Codable, CaseIterable, Identifiable, Sendable {
    case hd720p30
    case hd1080p30
    case hd720p15

    var id: String { rawValue }

    var label: String {
        switch self {
        case .hd720p30: return "720p · 30 fps"
        case .hd1080p30: return "1080p · 30 fps"
        case .hd720p15: return "720p · 15 fps (saves data)"
        }
    }

    /// Landscape size; portrait swaps them.
    var width: Int { self == .hd1080p30 ? 1920 : 1280 }
    var height: Int { self == .hd1080p30 ? 1080 : 720 }
    var fps: Int { self == .hd720p15 ? 15 : 30 }

    /// H.264 average bit rate.
    var bitRate: Int {
        switch self {
        case .hd720p30: return 2_500_000
        case .hd1080p30: return 4_500_000
        case .hd720p15: return 1_200_000
        }
    }
}

enum PhoneCameraLens: String, Codable, CaseIterable, Identifiable, Sendable {
    case back, front
    var id: String { rawValue }
    var label: String { self == .back ? "Back" : "Front" }
}

struct PhoneCameraSettings: Codable, Equatable, Sendable {
    var name: String = "iPhone camera"
    var lens: PhoneCameraLens = .back
    var quality: PhoneCameraQuality = .hd720p30
    var microphone: Bool = false

    private static let key = "sentinel.mobile.phoneCamera.settings"

    static func load(_ defaults: UserDefaults = .standard) -> PhoneCameraSettings {
        guard let data = defaults.data(forKey: key),
              let value = try? JSONDecoder().decode(PhoneCameraSettings.self, from: data) else { return PhoneCameraSettings() }
        return value
    }

    func save(_ defaults: UserDefaults = .standard) {
        if let data = try? JSONEncoder().encode(self) { defaults.set(data, forKey: Self.key) }
    }
}

// MARK: - Credentials (Keychain)

/// The phone camera this iPhone streams as on one site. The push key is a
/// secret shown once by the server; it lets the phone stream after the
/// user's sign-in ends.
struct PhoneCameraCredentials: Codable, Equatable, Sendable {
    var cameraId: String
    var pushKey: String
    var name: String
}

protocol PhoneCameraCredentialStoring: Sendable {
    func load(siteID: String) -> PhoneCameraCredentials?
    func save(_ credentials: PhoneCameraCredentials, siteID: String)
    func delete(siteID: String)
}

struct KeychainPhoneCameraStore: PhoneCameraCredentialStoring {
    static let service = "com.handoffgrid.sentinel.mobile.phone-camera"

    private func query(_ siteID: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: Self.service,
         kSecAttrAccount as String: siteID]
    }

    func load(siteID: String) -> PhoneCameraCredentials? {
        var q = query(siteID)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess, let data = item as? Data else { return nil }
        return try? JSONDecoder().decode(PhoneCameraCredentials.self, from: data)
    }

    func save(_ credentials: PhoneCameraCredentials, siteID: String) {
        guard let data = try? JSONEncoder().encode(credentials) else { return }
        let status = SecItemUpdate(query(siteID) as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status == errSecItemNotFound {
            var add = query(siteID)
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            add[kSecAttrLabel as String] = "Sentinel phone camera key"
            SecItemAdd(add as CFDictionary, nil)
        }
    }

    func delete(siteID: String) {
        SecItemDelete(query(siteID) as CFDictionary)
    }
}

final class MemoryPhoneCameraStore: PhoneCameraCredentialStoring, @unchecked Sendable {
    private var items: [String: PhoneCameraCredentials] = [:]
    private let lock = NSLock()
    func load(siteID: String) -> PhoneCameraCredentials? { lock.lock(); defer { lock.unlock() }; return items[siteID] }
    func save(_ credentials: PhoneCameraCredentials, siteID: String) { lock.lock(); items[siteID] = credentials; lock.unlock() }
    func delete(siteID: String) { lock.lock(); items[siteID] = nil; lock.unlock() }
}

// MARK: - Segments and backlog

struct PushSegment: Equatable, Sendable {
    var data: Data
    /// Media duration in seconds (~1).
    var duration: TimeInterval
}

/// Media segments waiting to go up, oldest first. When the phone falls more
/// than `maxBacklog` seconds behind, the oldest whole segments are dropped
/// (each segment starts on a key frame, so the stream stays decodable).
struct SegmentBacklog: Sendable {
    static let maxBacklog: TimeInterval = 20

    private(set) var items: [PushSegment] = []
    let maxBacklog: TimeInterval

    init(maxBacklog: TimeInterval = SegmentBacklog.maxBacklog) {
        self.maxBacklog = maxBacklog
    }

    var seconds: TimeInterval { items.reduce(0) { $0 + $1.duration } }
    var isEmpty: Bool { items.isEmpty }
    var count: Int { items.count }

    /// Adds a segment; returns how many old segments were dropped to stay
    /// within `maxBacklog` (the newest one is always kept).
    @discardableResult
    mutating func append(_ segment: PushSegment) -> Int {
        items.append(segment)
        var dropped = 0
        while items.count > 1, seconds > maxBacklog {
            items.removeFirst()
            dropped += 1
        }
        return dropped
    }

    mutating func popFirst() -> PushSegment? {
        items.isEmpty ? nil : items.removeFirst()
    }

    mutating func removeAll() { items.removeAll() }
}

// MARK: - Push token timing

enum PushTokenClock {
    /// Push tokens live 5 minutes; the phone gets a new one after 4.
    static let refreshAfter: TimeInterval = 240
    /// …or sooner when the server gave less than this to go.
    static let minimumLeft: TimeInterval = 60

    static func needsRefresh(issuedAt: Date?, expiresAt: Date?, now: Date) -> Bool {
        guard let issuedAt, let expiresAt else { return true }
        return now.timeIntervalSince(issuedAt) >= refreshAfter || expiresAt.timeIntervalSince(now) < minimumLeft
    }
}

enum PushBackoff {
    /// 0.5, 1, 2, 4, 8, 8, … seconds.
    static func delay(failures: Int) -> TimeInterval {
        guard failures > 0 else { return 0 }
        return min(8, 0.5 * pow(2, Double(failures - 1)))
    }
}

/// Uplink kb/s over the last few seconds.
struct UplinkRateMeter: Sendable {
    let window: TimeInterval
    private var samples: [(at: Date, bytes: Int)] = []

    init(window: TimeInterval = 5) { self.window = window }

    mutating func record(bytes: Int, at now: Date) {
        samples.append((now, bytes))
        samples.removeAll { now.timeIntervalSince($0.at) > window }
    }

    func kilobitsPerSecond(now: Date) -> Int {
        let recent = samples.filter { now.timeIntervalSince($0.at) <= window }
        guard recent.isEmpty == false else { return 0 }
        let bytes = recent.reduce(0) { $0 + $1.bytes }
        return Int((Double(bytes) * 8 / 1000 / window).rounded())
    }
}

// MARK: - Screen state machine

enum PhoneCameraPhase: Equatable, Sendable {
    case idle
    /// Creating the phone camera / asking for camera access.
    case settingUp
    /// Capture is running; the first segment hasn't gone up yet.
    case connecting
    case streaming
    /// Segments are failing; the uplink keeps trying.
    case reconnecting(String)
    /// iOS paused the camera (app in the background, a call, Split View).
    case paused(PauseReason)
    case failed(String)

    enum PauseReason: Equatable, Sendable { case background, interrupted }

    var isActive: Bool {
        switch self {
        case .settingUp, .connecting, .streaming, .reconnecting, .paused: return true
        case .idle, .failed: return false
        }
    }
}

enum PhoneCameraEvent: Equatable, Sendable {
    case start
    case captureStarted
    case segmentSent
    case trouble(String)
    case backgrounded
    case interrupted
    case resumed
    case stop
    case fatal(String)
}

extension PhoneCameraPhase {
    func next(_ event: PhoneCameraEvent) -> PhoneCameraPhase {
        switch (self, event) {
        case (_, .stop): return .idle
        case (_, .fatal(let message)): return .failed(message)
        case (.idle, .start), (.failed, .start): return .settingUp
        case (.settingUp, .captureStarted), (.paused, .captureStarted): return .connecting
        case (.connecting, .segmentSent), (.reconnecting, .segmentSent), (.streaming, .segmentSent): return .streaming
        case (.connecting, .trouble(let m)), (.streaming, .trouble(let m)), (.reconnecting, .trouble(let m)):
            return .reconnecting(m)
        case (.settingUp, .backgrounded), (.connecting, .backgrounded), (.streaming, .backgrounded),
             (.reconnecting, .backgrounded), (.paused, .backgrounded):
            return .paused(.background)
        case (.connecting, .interrupted), (.streaming, .interrupted), (.reconnecting, .interrupted):
            return .paused(.interrupted)
        case (.paused, .resumed): return .connecting
        default: return self
        }
    }
}

// MARK: - Push plane

/// Where to push, for one token: the recorder directly or the management
/// server's `/v/{token}` gateway (always when the phone is off the LAN:
/// remote address or the Sentinel Cloud relay).
struct PushEndpoint: Equatable, Sendable {
    var base: URL
    var isGateway: Bool
    var recorderID: String

    static func resolve(_ token: V2.PushToken, site: URL, preferGateway: Bool) -> PushEndpoint {
        let direct = VideoTokenBroker.resolve(token.baseUrl, against: site)
        let gateway = VideoTokenBroker.resolve(token.gatewayUrl, against: site)
        if preferGateway, let gateway { return PushEndpoint(base: gateway, isGateway: true, recorderID: token.recorderId) }
        if let direct { return PushEndpoint(base: direct, isGateway: direct == gateway, recorderID: token.recorderId) }
        return PushEndpoint(base: gateway ?? site, isGateway: true, recorderID: token.recorderId)
    }

    func sessions(_ cameraID: String) -> URL { base.appendingPathComponent("push/\(cameraID)/sessions") }
    func session(_ cameraID: String, _ sid: String) -> URL { sessions(cameraID).appendingPathComponent(sid) }
    func initSegment(_ cameraID: String, _ sid: String) -> URL { session(cameraID, sid).appendingPathComponent("init") }
    func segment(_ cameraID: String, _ sid: String, _ seq: Int) -> URL {
        session(cameraID, sid).appendingPathComponent("segments/\(seq)")
    }
}

enum PushError: Error, Equatable {
    /// 401/403 from push-token: the key was rotated or revoked.
    case keyRejected
    /// 404 from push-token: the phone camera no longer exists.
    case cameraGone
    /// 402 cloud_required from push-token: the site's Sentinel Cloud
    /// subscription ended (addendum 2).
    case cloudRequired
    /// 404/410 on the video plane: the session ended (new one needed).
    case sessionGone
    /// 401/403 on the video plane: the token expired.
    case tokenRejected
    /// 409 {expected}.
    case outOfOrder(expected: Int?)
    /// Network trouble or a 5xx: try again.
    case transient(String)
    /// The server refused the request for good (400, 413…).
    case refused(String)
}

/// The push plane, abstracted for tests.
protocol PushTransport: Sendable {
    func pushToken() async throws -> V2.PushToken
    func endpoint(for token: V2.PushToken, preferGateway: Bool) -> PushEndpoint
    func startSession(_ endpoint: PushEndpoint, format: V2.PushSessionRequest) async throws -> V2.PushSession
    func putInit(_ endpoint: PushEndpoint, sessionID: String, data: Data) async throws
    func putSegment(_ endpoint: PushEndpoint, sessionID: String, seq: Int, data: Data) async throws
    func endSession(_ endpoint: PushEndpoint, sessionID: String) async
}

/// `PushTransport` over HTTPS: push-token through the site API (with the
/// push key header), the rest straight to the endpoint.
struct HTTPPushTransport: PushTransport {
    let api: SentinelAPI
    let cameraID: String
    let pushKey: String
    var session: URLSession = .shared

    func pushToken() async throws -> V2.PushToken {
        do {
            return try await api.pushToken(cameraID: cameraID, pushKey: pushKey)
        } catch let error as SentinelAPIError {
            if error.isCloudRequired { throw PushError.cloudRequired }
            switch error.status {
            case 401?, 403?: throw PushError.keyRejected
            case 404?: throw PushError.cameraGone
            case let status? where status >= 500 || status == 429: throw PushError.transient(error.localizedDescription)
            case .some: throw PushError.refused(error.localizedDescription)
            case nil: throw PushError.transient(error.localizedDescription)
            }
        }
    }

    func endpoint(for token: V2.PushToken, preferGateway: Bool) -> PushEndpoint {
        PushEndpoint.resolve(token, site: api.baseURL, preferGateway: preferGateway || api.addresses.isUsingSecondary)
    }

    func startSession(_ endpoint: PushEndpoint, format: V2.PushSessionRequest) async throws -> V2.PushSession {
        let body = try SentinelAPI.jsonEncoder.encode(format)
        let data = try await send("POST", endpoint.sessions(cameraID), body: body, contentType: "application/json")
        do { return try SentinelAPI.decode(V2.PushSession.self, from: data) } catch {
            throw PushError.refused(error.localizedDescription)
        }
    }

    func putInit(_ endpoint: PushEndpoint, sessionID: String, data: Data) async throws {
        _ = try await send("PUT", endpoint.initSegment(cameraID, sessionID), body: data, contentType: "video/mp4")
    }

    func putSegment(_ endpoint: PushEndpoint, sessionID: String, seq: Int, data: Data) async throws {
        _ = try await send("PUT", endpoint.segment(cameraID, sessionID, seq), body: data, contentType: "video/mp4")
    }

    func endSession(_ endpoint: PushEndpoint, sessionID: String) async {
        _ = try? await send("DELETE", endpoint.session(cameraID, sessionID), body: nil, contentType: nil, timeout: 5)
    }

    private func send(_ method: String, _ url: URL, body: Data?, contentType: String?,
                      timeout: TimeInterval = 15) async throws -> Data {
        var request = URLRequest(url: url, timeoutInterval: timeout)
        request.httpMethod = method
        request.httpBody = body
        if let contentType { request.setValue(contentType, forHTTPHeaderField: "Content-Type") }
        let data: Data, response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            if error.code == .cancelled { throw CancellationError() }
            throw PushError.transient(SentinelAPI.describe(error))
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        switch status {
        case 200..<300: return data
        case 401, 403: throw PushError.tokenRejected
        case 404, 410: throw PushError.sessionGone
        case 409:
            struct Expected: Decodable { var expected: Int? }
            throw PushError.outOfOrder(expected: (try? JSONDecoder().decode(Expected.self, from: data))?.expected)
        case 408, 429, 500...: throw PushError.transient(SentinelAPIError.from(status: status, body: data).localizedDescription)
        default: throw PushError.refused(SentinelAPIError.from(status: status, body: data).localizedDescription)
        }
    }
}

// MARK: - The uplink

enum UplinkEvent: Equatable, Sendable {
    case sessionStarted(String)
    case sent(seq: Int, bytes: Int)
    /// Old segments dropped to catch up ("Connection too slow, skipping ahead").
    case skipped(count: Int)
    case trouble(String)
    /// Streaming can't continue without the user (key revoked, camera removed).
    case fatal(PushError)
}

/// Sends the init segment and media segments one at a time, in order.
///
/// - Segment numbers are given out when a segment is first sent, so
///   segments dropped from the backlog leave no gap on the server.
/// - A failed segment is retried with backoff; while it waits, newer
///   segments pile up and the backlog policy drops the oldest. If segments
///   were dropped behind a retried one, it's dropped too (it's older).
/// - A new init segment (new writer after an interruption) starts a new
///   session: the queue belongs to the old one and is cleared.
/// - The push token is refreshed after 4 minutes; a rejected token forces
///   a refresh; a direct-to-recorder failure sends the next five minutes
///   through the gateway.
actor PhoneStreamUplink {
    static let gatewayStickiness: TimeInterval = 300

    private let transport: PushTransport
    private let now: @Sendable () -> Date
    private let sleep: @Sendable (TimeInterval) async throws -> Void
    private let onEvent: @Sendable (UplinkEvent) -> Void

    private var backlog: SegmentBacklog
    private var initData: Data?
    private var format: V2.PushSessionRequest?
    private var initGeneration = 0

    private var token: V2.PushToken?
    private var tokenIssuedAt: Date?
    private var directFailedAt: Date?
    private var sessionID: String?
    private var sessionEndpoint: PushEndpoint?
    private var sessionGeneration = -1
    private var maxSegmentBytes = 8 * 1024 * 1024
    private var nextSeq = 0
    private var inFlight: PushSegment?
    private var droppedSinceInFlight = false
    private var failures = 0

    private var loop: Task<Void, Never>?
    private var waiter: CheckedContinuation<Void, Never>?
    private(set) var isRunning = false

    init(transport: PushTransport, maxBacklog: TimeInterval = SegmentBacklog.maxBacklog,
         now: @escaping @Sendable () -> Date = { Date() },
         sleep: @escaping @Sendable (TimeInterval) async throws -> Void = { try await Task.sleep(nanoseconds: UInt64($0 * 1e9)) },
         onEvent: @escaping @Sendable (UplinkEvent) -> Void) {
        self.transport = transport
        self.backlog = SegmentBacklog(maxBacklog: maxBacklog)
        self.now = now
        self.sleep = sleep
        self.onEvent = onEvent
    }

    var queuedSeconds: TimeInterval { backlog.seconds }
    var currentSessionID: String? { sessionID }

    func start() {
        guard loop == nil else { return }
        isRunning = true
        loop = Task { await self.run() }
    }

    /// Stops sending and ends the session on the server.
    func stop() async {
        isRunning = false
        loop?.cancel()
        loop = nil
        wake()
        if let sessionID, let sessionEndpoint {
            await transport.endSession(sessionEndpoint, sessionID: sessionID)
        }
        sessionID = nil
        backlog.removeAll()
        inFlight = nil
    }

    /// A new writer's init segment: a new session starts with it.
    func setInit(_ data: Data, format: V2.PushSessionRequest) {
        initData = data
        self.format = format
        initGeneration += 1
        backlog.removeAll()
        inFlight = nil
        wake()
    }

    func enqueue(_ segment: PushSegment) {
        guard initData != nil else { return }
        let dropped = backlog.append(segment)
        if dropped > 0 {
            if inFlight != nil { droppedSinceInFlight = true }
            onEvent(.skipped(count: dropped))
        }
        wake()
    }

    private func wake() {
        waiter?.resume()
        waiter = nil
    }

    private func waitForWork() async {
        if backlog.isEmpty == false || inFlight != nil || isRunning == false { return }
        await withCheckedContinuation { waiter = $0 }
    }

    private func run() async {
        while isRunning, Task.isCancelled == false {
            await waitForWork()
            guard isRunning, Task.isCancelled == false else { break }
            do {
                try await step()
                failures = 0
            } catch is CancellationError {
                break
            } catch let error as PushError {
                switch error {
                case .keyRejected, .cameraGone, .cloudRequired:
                    // End the session cleanly; the controller stops capture.
                    isRunning = false
                    if let sessionID, let sessionEndpoint {
                        await transport.endSession(sessionEndpoint, sessionID: sessionID)
                    }
                    sessionID = nil
                    backlog.removeAll()
                    inFlight = nil
                    loop = nil
                    onEvent(.fatal(error))
                    return
                case .tokenRejected:
                    token = nil
                case .sessionGone:
                    sessionID = nil
                case .outOfOrder(let expected):
                    if let expected, expected >= 0 { nextSeq = expected } else { sessionID = nil }
                case .transient(let message), .refused(let message):
                    if sessionEndpoint?.isGateway == false { directFailedAt = now() }
                    onEvent(.trouble(message))
                }
                await backoff()
            } catch {
                onEvent(.trouble(error.localizedDescription))
                await backoff()
            }
        }
    }

    private func backoff() async {
        failures += 1
        do { try await sleep(PushBackoff.delay(failures: failures)) } catch {}
        // While waiting, newer segments may have pushed older ones out:
        // the one being retried is older still, so let it go.
        if droppedSinceInFlight {
            inFlight = nil
            droppedSinceInFlight = false
        }
    }

    /// One unit of work: make sure of token and session, then send the
    /// oldest segment.
    private func step() async throws {
        guard let initData, let format else {
            backlog.removeAll()
            return
        }
        let at = now()
        if token == nil || PushTokenClock.needsRefresh(issuedAt: tokenIssuedAt,
                                                         expiresAt: token.map { Date(unixMillis: $0.expiresAt) }, now: at) {
            do {
                token = try await transport.pushToken()
                tokenIssuedAt = at
            } catch let error as PushError {
                // A refresh that fails while the old token still works is not fatal.
                if case .transient = error, let token, Date(unixMillis: token.expiresAt) > at {
                    tokenIssuedAt = at.addingTimeInterval(-PushTokenClock.refreshAfter + 30)
                } else {
                    throw error
                }
            }
        }
        guard let token else { return }
        let preferGateway = directFailedAt.map { at.timeIntervalSince($0) < Self.gatewayStickiness } ?? false
        let endpoint = transport.endpoint(for: token, preferGateway: preferGateway)
        sessionEndpoint = endpoint

        if sessionID == nil || sessionGeneration != initGeneration {
            let generation = initGeneration
            let session = try await transport.startSession(endpoint, format: format)
            try await transport.putInit(endpoint, sessionID: session.sessionId, data: initData)
            guard generation == initGeneration else { return } // a newer writer arrived meanwhile
            sessionID = session.sessionId
            sessionGeneration = generation
            maxSegmentBytes = session.maxSegmentBytes ?? maxSegmentBytes
            nextSeq = 0
            onEvent(.sessionStarted(session.sessionId))
        }
        guard let sessionID else { return }

        if inFlight == nil {
            inFlight = backlog.popFirst()
            droppedSinceInFlight = false
        }
        guard let segment = inFlight else { return }
        if segment.data.count > maxSegmentBytes {
            inFlight = nil // can't be sent; the next one starts on a key frame
            onEvent(.skipped(count: 1))
            return
        }
        let generation = initGeneration
        try await transport.putSegment(endpoint, sessionID: sessionID, seq: nextSeq, data: segment.data)
        guard generation == initGeneration else { return }
        onEvent(.sent(seq: nextSeq, bytes: segment.data.count))
        nextSeq += 1
        inFlight = nil
    }
}
