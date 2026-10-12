import Foundation

/// Live notifications from `GET /api/v2/events/stream` (WebSocket).
///
/// Authenticates with a bearer header on the upgrade and, for proxies that
/// drop that header, also with the first-message `{"type":"auth"}` (the
/// server ignores it once the header was accepted). Reconnects with backoff;
/// after a reconnect or a `lagged` message it emits `.resync` so stores
/// re-read their state instead of trusting a stream with holes in it.
public final class SentinelEventStream: @unchecked Sendable {
    public enum State: Equatable, Sendable {
        case idle
        case connecting
        case connected(serverVersion: String)
        case waiting(retryIn: TimeInterval, reason: String)
        /// Won't reconnect on its own (signed out, or no longer allowed).
        case stopped(reason: String)
    }

    public enum Update: Sendable {
        case state(State)
        case message(V2.StreamMessage, at: UnixMillis)
        /// Messages may have been missed: re-read everything.
        case resync(reason: String)
    }

    /// WebSocket close codes from the contract.
    public static let closeSignInEnded = 4401
    public static let closeForbidden = 4403

    private let api: SentinelAPI
    private let topics: [String]
    private let session: URLSession
    private let lock = NSLock()
    private var runner: Task<Void, Never>?
    private var continuation: AsyncStream<Update>.Continuation?

    public init(api: SentinelAPI, topics: [String] = ["alarm", "event", "camera", "config"], session: URLSession = .shared) {
        self.api = api
        self.topics = topics
        self.session = session
    }

    deinit {
        runner?.cancel()
    }

    /// The `ws(s)://…/api/v2/events/stream?topics=…` URL for a site address.
    public static func streamURL(baseURL: URL, topics: [String]) -> URL? {
        var base = baseURL.absoluteString
        while base.hasSuffix("/") { base.removeLast() }
        guard var components = URLComponents(string: base + "/api/v2/events/stream") else { return nil }
        switch components.scheme?.lowercased() {
        case "https": components.scheme = "wss"
        case "http": components.scheme = "ws"
        default: break
        }
        if topics.isEmpty == false {
            components.queryItems = [URLQueryItem(name: "topics", value: topics.joined(separator: ","))]
        }
        return components.url
    }

    /// Starts (or restarts) the connection and returns its updates. One
    /// consumer at a time: calling again finishes the previous stream.
    public func start() -> AsyncStream<Update> {
        stop()
        let (stream, continuation) = AsyncStream<Update>.makeStream(bufferingPolicy: .bufferingNewest(512))
        lock.lock()
        self.continuation = continuation
        runner = Task { [weak self] in
            await self?.run(continuation)
            continuation.finish()
        }
        lock.unlock()
        return stream
    }

    public func stop() {
        lock.lock()
        runner?.cancel()
        runner = nil
        continuation?.finish()
        continuation = nil
        lock.unlock()
    }

    /// Backoff before reconnect attempt `n` (0-based): 1, 2, 4 … 30 s, ±20 %.
    public static func backoff(attempt: Int) -> TimeInterval {
        let base = min(30, pow(2, Double(min(attempt, 6))))
        return base * Double.random(in: 0.8...1.2)
    }

    private func run(_ out: AsyncStream<Update>.Continuation) async {
        guard let url = Self.streamURL(baseURL: api.baseURL, topics: topics) else {
            out.yield(.state(.stopped(reason: "The site address isn't valid.")))
            return
        }
        var attempt = 0
        var everConnected = false
        var needFreshToken = false

        while Task.isCancelled == false {
            out.yield(.state(.connecting))
            let task: URLSessionWebSocketTask
            do {
                let token = try await api.accessToken(forceRefresh: needFreshToken)
                needFreshToken = false
                var request = URLRequest(url: url)
                request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
                task = session.webSocketTask(with: request)
                task.resume()
                let auth = try JSONSerialization.data(withJSONObject: ["type": "auth", "accessToken": token])
                try await task.send(.string(String(decoding: auth, as: UTF8.self)))
            } catch let error as SentinelAPIError {
                if case .signInRequired(let message) = error {
                    out.yield(.state(.stopped(reason: message)))
                    return
                }
                attempt = await wait(after: error.localizedDescription, attempt: attempt, out: out)
                continue
            } catch {
                attempt = await wait(after: error.localizedDescription, attempt: attempt, out: out)
                continue
            }

            let pinger = Task {
                while Task.isCancelled == false {
                    try? await Task.sleep(nanoseconds: 30_000_000_000)
                    guard Task.isCancelled == false else { return }
                    let alive = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                        task.sendPing { done.resume(returning: $0 == nil) }
                    }
                    if alive == false {
                        task.cancel(with: .goingAway, reason: nil)
                        return
                    }
                }
            }

            var failure = "The connection to the server dropped."
            do {
                while Task.isCancelled == false {
                    let frame = try await task.receive()
                    let data: Data
                    switch frame {
                    case .string(let text): data = Data(text.utf8)
                    case .data(let bytes): data = bytes
                    @unknown default: continue
                    }
                    guard let (message, at) = try? V2.StreamMessage.decode(data) else { continue }
                    switch message {
                    case .hello(let hello):
                        attempt = 0
                        out.yield(.state(.connected(serverVersion: hello.serverVersion)))
                        if everConnected {
                            out.yield(.resync(reason: "reconnected"))
                        }
                        everConnected = true
                    case .lagged(let lagged):
                        out.yield(.message(message, at: at))
                        out.yield(.resync(reason: "missed \(lagged.dropped) notifications"))
                        continue
                    default:
                        break
                    }
                    out.yield(.message(message, at: at))
                }
            } catch {
                failure = error.localizedDescription
            }
            pinger.cancel()
            let code = task.closeCode.rawValue
            task.cancel(with: .goingAway, reason: nil)
            guard Task.isCancelled == false else { return }

            switch code {
            case Self.closeSignInEnded:
                // Token expired or revoked: reconnect at once with a fresh one.
                needFreshToken = true
                failure = "Signing in again…"
            case Self.closeForbidden:
                out.yield(.state(.stopped(reason: "Your role may no longer view live video.")))
                return
            default:
                break
            }
            attempt = await wait(after: failure, attempt: attempt, out: out, immediate: code == Self.closeSignInEnded)
        }
    }

    private func wait(after reason: String, attempt: Int, out: AsyncStream<Update>.Continuation, immediate: Bool = false) async -> Int {
        let delay = immediate ? 0.2 : Self.backoff(attempt: attempt)
        out.yield(.state(.waiting(retryIn: delay, reason: reason)))
        try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
        return attempt + 1
    }
}
