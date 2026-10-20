import Foundation

/// One frame on the talkback socket.
public enum TalkSocketMessage: Equatable, Sendable {
    case text(String)
    case data(Data)
}

/// The WebSocket surface `TalkSession` needs (tests pass a fake).
public protocol TalkSocket: AnyObject, Sendable {
    func resume()
    func send(_ message: TalkSocketMessage) async throws
    func receive() async throws -> TalkSocketMessage
    /// After `receive` failed: the close code and reason (a refused upgrade
    /// reports its HTTP status as the matching close code; a dropped
    /// connection is 1006).
    func closeInfo() -> (code: Int, reason: String)
    func close(code: Int)
}

/// `URLSessionWebSocketTask` as a `TalkSocket`.
public final class URLSessionTalkSocket: TalkSocket, @unchecked Sendable {
    private let task: URLSessionWebSocketTask

    public init(task: URLSessionWebSocketTask) {
        self.task = task
    }

    public func resume() { task.resume() }

    public func send(_ message: TalkSocketMessage) async throws {
        switch message {
        case .text(let text): try await task.send(.string(text))
        case .data(let data): try await task.send(.data(data))
        }
    }

    public func receive() async throws -> TalkSocketMessage {
        switch try await task.receive() {
        case .string(let text): return .text(text)
        case .data(let data): return .data(data)
        @unknown default: return .data(Data())
        }
    }

    public func closeInfo() -> (code: Int, reason: String) {
        let reason = task.closeReason.map { String(decoding: $0, as: UTF8.self) } ?? ""
        if task.closeCode != .invalid {
            return (task.closeCode.rawValue, reason)
        }
        if let http = task.response as? HTTPURLResponse, http.statusCode != 101 {
            return (TalkProtocol.closeCode(forHTTPStatus: http.statusCode), "")
        }
        return (TalkProtocol.closeAbnormal, reason)
    }

    public func close(code: Int) {
        task.cancel(with: URLSessionWebSocketTask.CloseCode(rawValue: code) ?? .normalClosure, reason: nil)
    }
}

/// One talk-down session to a camera's speaker over
/// `GET /api/v2/cameras/{id}/talkback` (WebSocket).
///
/// Bearer header on the upgrade (plus first-message auth for proxies that
/// drop the header), refreshed once on 4401 the way `SentinelAPI.raw` does
/// on 401. Waits for `{"type":"ready"}`, then sends binary little-endian
/// 16-bit mono PCM at `rate` while the button is held; frames captured
/// while connecting are queued (1 s). Sends `{"type":"stop"}` on hang-up.
/// The session stays open between presses until the server's 30 s idle
/// limit ends it, like the web.
///
/// Thread-safe: `send(frame:)` may be called from the audio thread.
public final class TalkSession: @unchecked Sendable {
    public typealias TokenProvider = @Sendable (_ forceRefresh: Bool) async throws -> String
    public typealias SocketFactory = @Sendable (URLRequest) -> TalkSocket

    public let cameraID: String
    public let rate: Int
    private let url: URL?
    private let token: TokenProvider
    private let makeSocket: SocketFactory
    private let onPhase: @Sendable (TalkPhase) -> Void

    private let lock = NSLock()
    private var machine = TalkMachine()
    private var socket: TalkSocket?
    private var outbox: AsyncStream<TalkSocketMessage>.Continuation?
    private var runner: Task<Void, Never>?
    private var opened = false

    public init(baseURL: URL, cameraID: String, rate: Int = TalkProtocol.rate,
                token: @escaping TokenProvider, socket: @escaping SocketFactory,
                onPhase: @escaping @Sendable (TalkPhase) -> Void) {
        self.cameraID = cameraID
        self.rate = TalkProtocol.rates.contains(rate) ? rate : TalkProtocol.rate
        self.url = TalkProtocol.url(baseURL: baseURL, cameraID: cameraID, rate: self.rate)
        self.token = token
        self.makeSocket = socket
        self.onPhase = onPhase
    }

    public convenience init(api: SentinelAPI, cameraID: String, rate: Int = TalkProtocol.rate,
                            session: URLSession = .shared, onPhase: @escaping @Sendable (TalkPhase) -> Void) {
        self.init(baseURL: api.baseURL, cameraID: cameraID, rate: rate,
                  token: { try await api.accessToken(forceRefresh: $0) },
                  socket: { URLSessionTalkSocket(task: session.webSocketTask(with: $0)) },
                  onPhase: onPhase)
    }

    deinit {
        runner?.cancel()
    }

    public var phase: TalkPhase { locked { machine.phase } }
    public var isHeld: Bool { locked { machine.held } }

    /// Opens the socket (once). Safe to call before or after `setTalking`.
    public func open() {
        let started: Bool = locked {
            guard opened == false, machine.connect() else { return false }
            opened = true
            return true
        }
        guard started else { return }
        publish()
        let task = Task<Void, Never> { [weak self] in await self?.run() }
        locked { runner = task }
    }

    /// Hold (true) or release (false) the talk button.
    public func setTalking(_ on: Bool) {
        let changed: Bool = locked {
            let before = machine.phase
            if on { machine.press() } else { machine.release() }
            return machine.phase != before
        }
        if changed { publish() }
    }

    /// One PCM frame (little-endian Int16 at `rate`).
    public func send(frame: Data) {
        let out: (Data, AsyncStream<TalkSocketMessage>.Continuation)? = locked {
            guard let now = machine.frame(frame), let outbox else { return nil }
            return (now, outbox)
        }
        if let (data, outbox) = out { outbox.yield(.data(data)) }
    }

    /// Hang up: `{"type":"stop"}`, then close. A quiet end.
    public func close() {
        let (ended, sock, box, task): (Bool, TalkSocket?, AsyncStream<TalkSocketMessage>.Continuation?, Task<Void, Never>?) = locked {
            let ended = machine.hangUp()
            let wasConnected = socket != nil
            let result = (ended, socket, wasConnected ? outbox : nil, runner)
            return result
        }
        guard ended else { return }
        publish()
        if let box, let sock {
            box.yield(.text(TalkProtocol.stopMessage))
            box.finish()
            // Give the writer a moment to deliver the stop before the close.
            Task {
                try? await Task.sleep(nanoseconds: 150_000_000)
                sock.close(code: TalkProtocol.closeNormal)
                task?.cancel()
            }
        } else {
            sock?.close(code: TalkProtocol.closeNormal)
            task?.cancel()
        }
    }

    // MARK: - Connection

    private func run() async {
        guard let url else {
            finish(code: TalkProtocol.closeAbnormal, reason: "")
            return
        }
        var retried = false
        while Task.isCancelled == false {
            let accessToken: String
            do {
                accessToken = try await token(retried)
            } catch SentinelAPIError.signInRequired {
                finish(code: TalkProtocol.closeSignIn, reason: "")
                return
            } catch {
                finish(code: TalkProtocol.closeAbnormal, reason: "")
                return
            }
            if locked({ machine.phase.isEnded }) { return }

            var request = URLRequest(url: url)
            request.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
            let sock = makeSocket(request)
            let (stream, box) = AsyncStream<TalkSocketMessage>.makeStream(bufferingPolicy: .bufferingNewest(256))
            let live: Bool = locked {
                guard machine.phase.isEnded == false else { return false }
                socket = sock
                outbox = box
                return true
            }
            guard live else { return }
            sock.resume()
            box.yield(.text(TalkProtocol.authMessage(accessToken: accessToken)))
            let writer = Task {
                for await message in stream {
                    do { try await sock.send(message) } catch { return }
                }
            }

            await receiveLoop(sock)
            let (code, reason) = sock.closeInfo()
            writer.cancel()
            box.finish()

            let retry: Bool = locked {
                socket = nil
                outbox = nil
                return machine.shouldRetry(afterClose: code, retried: retried)
            }
            if retry {
                retried = true
                continue
            }
            sock.close(code: TalkProtocol.closeNormal)
            finish(code: code, reason: reason)
            return
        }
    }

    private func receiveLoop(_ sock: TalkSocket) async {
        while Task.isCancelled == false {
            let message: TalkSocketMessage
            do { message = try await sock.receive() } catch { return }
            guard case .text(let text) = message else { continue }
            let parsed = TalkProtocol.parse(text)
            let (flush, box, changed): ([Data], AsyncStream<TalkSocketMessage>.Continuation?, Bool) = locked {
                let before = machine.phase
                let flush = machine.receive(parsed)
                return (flush, outbox, machine.phase != before)
            }
            for frame in flush { box?.yield(.data(frame)) }
            if changed { publish() }
        }
    }

    private func finish(code: Int, reason: String) {
        let changed = locked { machine.closed(code: code, reason: reason) }
        if changed { publish() }
    }

    private func publish() {
        onPhase(locked { machine.phase })
    }

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}
