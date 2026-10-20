import Foundation

// Talk-down (two-way audio through a camera speaker) — the view-free part:
// the socket URL, the server's messages, what a close code means to the
// operator, PCM framing and the session state machine. Wire protocol:
// `GET /api/v2/cameras/{id}/talkback` in linux/api/openapi.yaml; behaviour
// mirrors linux/web/src/operator/talkback.ts and TalkButton.tsx.

public enum TalkProtocol {
    /// The rate the Mac sends (the server downsamples to PCMU/8000).
    public static let rate = 16000
    /// Rates the server accepts.
    public static let rates: Set<Int> = [8000, 16000]
    /// Samples per 20 ms frame at `rate`.
    public static var frameSamples: Int { rate / 50 }
    /// Frames kept while the camera session is being set up (1 s).
    public static let queueFrames = 50

    // Close codes (api/openapi.yaml).
    public static let closeNormal = 1000
    public static let closeNoStatus = 1005
    public static let closeAbnormal = 1006
    public static let closeUnsupported = 4400
    public static let closeSignIn = 4401
    public static let closeForbidden = 4403
    public static let closeNotFound = 4404
    public static let closeIdle = 4408
    public static let closeBusy = 4409
    public static let closeLimit = 4410
    public static let closeCamera = 4502

    /// `ws(s)://…/api/v2/cameras/{id}/talkback?rate=…` for a site address.
    public static func url(baseURL: URL, cameraID: String, rate: Int = TalkProtocol.rate) -> URL? {
        var base = baseURL.absoluteString
        while base.hasSuffix("/") { base.removeLast() }
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#")
        let id = cameraID.addingPercentEncoding(withAllowedCharacters: allowed) ?? cameraID
        guard var components = URLComponents(string: base + "/api/v2/cameras/\(id)/talkback") else { return nil }
        switch components.scheme?.lowercased() {
        case "https": components.scheme = "wss"
        case "http": components.scheme = "ws"
        default: break
        }
        if rate != 8000 {
            components.queryItems = [URLQueryItem(name: "rate", value: String(rate))]
        }
        return components.url
    }

    // MARK: Messages

    /// `{"type":"ready",…}` once the camera accepted the backchannel.
    public struct Ready: Codable, Equatable, Sendable {
        public var cameraId: String?
        public var codec: String?
        public var rate: Int?
        public var idleTimeoutMs: Int?
        public var maxDurationMs: Int?

        public init(cameraId: String? = nil, codec: String? = nil, rate: Int? = nil,
                    idleTimeoutMs: Int? = nil, maxDurationMs: Int? = nil) {
            self.cameraId = cameraId
            self.codec = codec
            self.rate = rate
            self.idleTimeoutMs = idleTimeoutMs
            self.maxDurationMs = maxDurationMs
        }
    }

    public enum ServerMessage: Equatable, Sendable {
        case ready(Ready)
        /// `{"type":"error","message"}` — sent before a 4502 close.
        case error(String)
        /// Anything else (unknown types, bad JSON): ignored.
        case other
    }

    public static func parse(_ text: String) -> ServerMessage {
        struct Envelope: Decodable {
            var type: String?
            var message: String?
        }
        let data = Data(text.utf8)
        guard let envelope = try? JSONDecoder().decode(Envelope.self, from: data) else { return .other }
        switch envelope.type {
        case "ready":
            return .ready((try? JSONDecoder().decode(Ready.self, from: data)) ?? Ready())
        case "error":
            guard let message = envelope.message, message.isEmpty == false else { return .other }
            return .error(message)
        default:
            return .other
        }
    }

    /// The first-message auth, for proxies that drop the bearer header (the
    /// server ignores it once the header was accepted).
    public static func authMessage(accessToken: String) -> String {
        jsonText(["type": "auth", "accessToken": accessToken])
    }

    /// Hang up.
    public static let stopMessage = #"{"type":"stop"}"#

    private static func jsonText(_ object: [String: String]) -> String {
        let data = (try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])) ?? Data()
        return String(decoding: data, as: UTF8.self)
    }

    // MARK: Close codes

    /// The close code a refused upgrade stands for. With the bearer header
    /// the server answers its checks as HTTP statuses; with first-message
    /// auth the same refusals are close codes.
    public static func closeCode(forHTTPStatus status: Int) -> Int {
        switch status {
        case 400: return closeUnsupported
        case 401: return closeSignIn
        case 403: return closeForbidden
        case 404: return closeNotFound
        case 409: return closeBusy
        default: return closeCamera
        }
    }

    /// What a close means to the operator; nil = nothing worth saying.
    /// `whileHeld`: the button was held when the session ended (an idle
    /// timeout between presses is the normal end; while holding it means
    /// the microphone produced nothing).
    public static func closeMessage(code: Int, reason: String = "", whileHeld: Bool = false) -> String? {
        let reason = reason.trimmingCharacters(in: .whitespacesAndNewlines)
        switch code {
        case closeNormal, closeNoStatus:
            return nil
        case closeIdle:
            return whileHeld ? "No sound reached the camera for 30 seconds, so two-way audio hung up. Check the microphone." : nil
        case closeUnsupported:
            return "This camera doesn't support two-way audio."
        case closeSignIn:
            return "Sign in again to talk through cameras."
        case closeForbidden:
            return "Your role can't talk through cameras."
        case closeNotFound:
            return "Camera not found."
        case closeBusy:
            if reason.lowercased().contains("talking"), reason.lowercased().hasPrefix("someone") == false {
                // "alice is already talking through this camera" (names keep their case).
                return reason.hasSuffix(".") ? reason : reason + "."
            }
            return "Someone else is talking through this camera."
        case closeLimit:
            return "Talk time limit reached (5 minutes)."
        case closeCamera:
            let detail = cameraDetail(reason)
            return detail.isEmpty ? "The camera refused two-way audio." : "The camera refused two-way audio: \(detail)"
        default:
            return "Two-way audio disconnected."
        }
    }

    /// The server's 4502 text without its own "the camera refused two-way
    /// audio" lead-in, so the operator doesn't read it twice.
    static func cameraDetail(_ reason: String) -> String {
        var text = reason.trimmingCharacters(in: .whitespacesAndNewlines)
        let lead = "the camera refused two-way audio"
        if text.lowercased().hasPrefix(lead) {
            text = String(text.dropFirst(lead.count))
            while let first = text.first, first == ":" || first == " " || first == "." { text.removeFirst() }
        }
        return text
    }

    // MARK: Microphone texts

    public static let micDenied =
        "Sentinel Operator isn't allowed to use the microphone. Allow it in System Settings › Privacy & Security › Microphone, then try again."
    public static let micMissing = "No microphone is connected to this Mac."
    public static let micFailed = "The microphone could not be opened."
}

// MARK: - PCM

public enum TalkPCM {
    /// One float sample (−1…1) as signed 16-bit.
    public static func pcm16(_ x: Float) -> Int16 {
        guard x.isFinite else { return 0 }
        let c = max(-1, min(1, x))
        return Int16((c < 0 ? c * 32768 : c * 32767).rounded())
    }

    /// Little-endian bytes of the samples (the wire format).
    public static func encode<S: Sequence>(_ samples: S) -> Data where S.Element == Int16 {
        var data = Data()
        data.reserveCapacity(samples.underestimatedCount * 2)
        for s in samples {
            let v = UInt16(bitPattern: s.littleEndian)
            data.append(UInt8(truncatingIfNeeded: v))
            data.append(UInt8(truncatingIfNeeded: v >> 8))
        }
        return data
    }

    /// A meter level 0…1 for a frame: RMS in dBFS mapped linearly from
    /// −60 dB (0) to 0 dB (1).
    public static func level<S: Sequence>(_ samples: S) -> Double where S.Element == Int16 {
        var sum = 0.0
        var n = 0
        for s in samples {
            let v = Double(s) / 32768
            sum += v * v
            n += 1
        }
        guard n > 0, sum > 0 else { return 0 }
        let db = 10 * log10(sum / Double(n))
        return max(0, min(1, (db + 60) / 60))
    }
}

/// Cuts a stream of samples into fixed-size frames (20 ms by default);
/// leftovers carry over to the next call.
public struct TalkFramer: Sendable {
    public let frameSamples: Int
    private var pending: [Int16] = []

    public init(frameSamples: Int = TalkProtocol.frameSamples) {
        precondition(frameSamples > 0)
        self.frameSamples = frameSamples
    }

    public var pendingCount: Int { pending.count }

    public mutating func push<S: Sequence>(_ samples: S) -> [[Int16]] where S.Element == Int16 {
        pending.append(contentsOf: samples)
        var frames: [[Int16]] = []
        var start = 0
        while pending.count - start >= frameSamples {
            frames.append(Array(pending[start..<start + frameSamples]))
            start += frameSamples
        }
        if start > 0 { pending.removeFirst(start) }
        return frames
    }

    public mutating func reset() { pending.removeAll() }
}

// MARK: - State machine

/// Where a talk session is. `ready` = the camera is listening and the
/// button is up; `talking` = connected and held.
public enum TalkPhase: Equatable, Sendable {
    case idle
    case connecting
    case ready
    case talking
    /// Final. `message` is for the operator (nil = a quiet end).
    case ended(code: Int, message: String?)

    public var isEnded: Bool {
        if case .ended = self { return true }
        return false
    }

    public var isConnected: Bool { self == .ready || self == .talking }
}

/// One session's state: the phase, whether the button is held, and the
/// audio queued while the camera is being set up (so the first words
/// aren't lost). Frames go out only while held.
public struct TalkMachine: Sendable {
    public private(set) var phase: TalkPhase = .idle
    public private(set) var held = false
    public private(set) var queue: [Data] = []
    /// The server's `{"type":"error"}` text before a 4502.
    public private(set) var lastError = ""
    public let queueLimit: Int

    public init(queueLimit: Int = TalkProtocol.queueFrames) {
        self.queueLimit = queueLimit
    }

    /// The socket is being opened (idle → connecting). False when the
    /// session already ended.
    @discardableResult
    public mutating func connect() -> Bool {
        guard phase == .idle else { return phase == .connecting }
        phase = .connecting
        return true
    }

    public mutating func press() {
        held = true
        if phase == .ready { phase = .talking }
    }

    public mutating func release() {
        held = false
        queue.removeAll()
        if phase == .talking { phase = .ready }
    }

    /// A server message. Returns the queued frames to send now (on ready
    /// while held).
    public mutating func receive(_ message: TalkProtocol.ServerMessage) -> [Data] {
        switch message {
        case .ready:
            guard phase == .connecting else { return [] }
            phase = held ? .talking : .ready
            let flush = held ? queue : []
            queue.removeAll()
            return flush
        case .error(let text):
            lastError = text
            return []
        case .other:
            return []
        }
    }

    /// A captured frame: returns it when it should go out now; queues it
    /// while connecting; drops it when the button is up or the session ended.
    public mutating func frame(_ data: Data) -> Data? {
        guard held else { return nil }
        switch phase {
        case .talking:
            return data
        case .idle, .connecting:
            queue.append(data)
            if queue.count > queueLimit { queue.removeFirst(queue.count - queueLimit) }
            return nil
        case .ready, .ended:
            return nil
        }
    }

    /// The socket closed. Returns false when the session had already ended.
    @discardableResult
    public mutating func closed(code: Int, reason: String = "") -> Bool {
        guard phase.isEnded == false else { return false }
        let detail = code == TalkProtocol.closeCamera && lastError.isEmpty == false ? lastError
            : (reason.isEmpty ? lastError : reason)
        phase = .ended(code: code, message: TalkProtocol.closeMessage(code: code, reason: detail, whileHeld: held))
        queue.removeAll()
        return true
    }

    /// The operator hung up (or the tile went away): a quiet end.
    @discardableResult
    public mutating func hangUp() -> Bool {
        closed(code: TalkProtocol.closeNormal)
    }

    /// May the session reconnect with a fresh token after this close? Only
    /// a 4401 before the camera ever answered, once.
    public func shouldRetry(afterClose code: Int, retried: Bool) -> Bool {
        code == TalkProtocol.closeSignIn && retried == false && phase == .connecting
    }
}
