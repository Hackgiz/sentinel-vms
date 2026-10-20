import XCTest
@testable import SentinelCore

final class TalkProtocolTests: XCTestCase {
    func testURL() {
        let url = TalkProtocol.url(baseURL: URL(string: "https://nvr.test:8443/")!, cameraID: "cam 1")
        XCTAssertEqual(url?.absoluteString, "wss://nvr.test:8443/api/v2/cameras/cam%201/talkback?rate=16000")
        let plain = TalkProtocol.url(baseURL: URL(string: "http://nvr.test:8090")!, cameraID: "c1", rate: 8000)
        XCTAssertEqual(plain?.absoluteString, "ws://nvr.test:8090/api/v2/cameras/c1/talkback")
        XCTAssertEqual(TalkProtocol.frameSamples, 320)
    }

    func testParse() {
        let ready = TalkProtocol.parse(#"{"type":"ready","cameraId":"c1","codec":"PCMU/8000","rate":16000,"idleTimeoutMs":30000,"maxDurationMs":300000}"#)
        XCTAssertEqual(ready, .ready(.init(cameraId: "c1", codec: "PCMU/8000", rate: 16000, idleTimeoutMs: 30000, maxDurationMs: 300000)))
        XCTAssertEqual(TalkProtocol.parse(#"{"type":"error","message":"boom"}"#), .error("boom"))
        XCTAssertEqual(TalkProtocol.parse(#"{"type":"error"}"#), .other)
        XCTAssertEqual(TalkProtocol.parse(#"{"type":"hello"}"#), .other)
        XCTAssertEqual(TalkProtocol.parse("not json"), .other)
    }

    func testOutgoingMessages() throws {
        let auth = try JSONSerialization.jsonObject(with: Data(TalkProtocol.authMessage(accessToken: "t\"1").utf8)) as? [String: String]
        XCTAssertEqual(auth, ["type": "auth", "accessToken": "t\"1"])
        XCTAssertEqual(TalkProtocol.stopMessage, #"{"type":"stop"}"#)
    }

    func testHTTPStatusToCloseCode() {
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 400), 4400)
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 401), 4401)
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 403), 4403)
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 404), 4404)
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 409), 4409)
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 502), 4502)
        XCTAssertEqual(TalkProtocol.closeCode(forHTTPStatus: 500), 4502)
    }

    func testCloseMessages() {
        XCTAssertNil(TalkProtocol.closeMessage(code: 1000))
        XCTAssertNil(TalkProtocol.closeMessage(code: 1005))
        XCTAssertNil(TalkProtocol.closeMessage(code: 4408), "idle between presses is the normal end")
        XCTAssertNotNil(TalkProtocol.closeMessage(code: 4408, whileHeld: true))
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4400), "This camera doesn't support two-way audio.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4401), "Sign in again to talk through cameras.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4403), "Your role can't talk through cameras.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4404), "Camera not found.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4409), "Someone else is talking through this camera.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4409, reason: "someone is already talking through this camera"),
                       "Someone else is talking through this camera.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4409, reason: "alice is already talking through this camera"),
                       "alice is already talking through this camera.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4410), "Talk time limit reached (5 minutes).")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4502), "The camera refused two-way audio.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4502, reason: "the camera refused two-way audio: 454 Session Not Found"),
                       "The camera refused two-way audio: 454 Session Not Found")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 4502, reason: "the camera refused two-way audio"),
                       "The camera refused two-way audio.")
        XCTAssertEqual(TalkProtocol.closeMessage(code: 1006), "Two-way audio disconnected.")
    }

    func testPCM() {
        XCTAssertEqual(TalkPCM.pcm16(0), 0)
        XCTAssertEqual(TalkPCM.pcm16(1), 32767)
        XCTAssertEqual(TalkPCM.pcm16(-1), -32768)
        XCTAssertEqual(TalkPCM.pcm16(4), 32767)
        XCTAssertEqual(TalkPCM.pcm16(.nan), 0)
        XCTAssertEqual(TalkPCM.encode([1, -2, 0x1234]), Data([0x01, 0x00, 0xFE, 0xFF, 0x34, 0x12]))
        XCTAssertEqual(TalkPCM.level([Int16](repeating: 0, count: 320)), 0)
        XCTAssertEqual(TalkPCM.level([Int16](repeating: .max, count: 320)), 1, accuracy: 0.001)
        let quiet = TalkPCM.level([Int16](repeating: 328, count: 320)) // ≈ −40 dBFS
        XCTAssertEqual(quiet, 1.0 / 3, accuracy: 0.01)
    }

    func testFramer() {
        var framer = TalkFramer(frameSamples: 4)
        XCTAssertEqual(framer.push([1, 2, 3]), [])
        XCTAssertEqual(framer.push([4, 5, 6, 7, 8, 9]), [[1, 2, 3, 4], [5, 6, 7, 8]])
        XCTAssertEqual(framer.pendingCount, 1)
        framer.reset()
        XCTAssertEqual(framer.push([1, 2, 3, 4]), [[1, 2, 3, 4]])
    }
}

final class TalkMachineTests: XCTestCase {
    private let a = Data([1]), b = Data([2]), c = Data([3])

    func testHappyPath() {
        var m = TalkMachine()
        XCTAssertEqual(m.phase, .idle)
        m.press()
        XCTAssertTrue(m.connect())
        XCTAssertEqual(m.phase, .connecting)
        XCTAssertNil(m.frame(a), "queued while connecting")
        XCTAssertNil(m.frame(b))
        XCTAssertEqual(m.receive(.ready(.init())), [a, b], "the first words go out on ready")
        XCTAssertEqual(m.phase, .talking)
        XCTAssertEqual(m.frame(c), c)
        m.release()
        XCTAssertEqual(m.phase, .ready)
        XCTAssertNil(m.frame(a), "nothing goes out with the button up")
        m.press()
        XCTAssertEqual(m.phase, .talking)
        m.release()
        XCTAssertTrue(m.closed(code: 4408))
        XCTAssertEqual(m.phase, .ended(code: 4408, message: nil))
        XCTAssertFalse(m.closed(code: 1006), "ended is final")
        XCTAssertFalse(m.connect())
        XCTAssertNil(m.frame(a))
    }

    func testReleaseWhileConnectingDropsQueue() {
        var m = TalkMachine()
        m.connect()
        m.press()
        _ = m.frame(a)
        m.release()
        XCTAssertTrue(m.queue.isEmpty)
        XCTAssertEqual(m.receive(.ready(.init())), [])
        XCTAssertEqual(m.phase, .ready)
    }

    func testQueueIsBounded() {
        var m = TalkMachine(queueLimit: 2)
        m.connect()
        m.press()
        _ = m.frame(a); _ = m.frame(b); _ = m.frame(c)
        XCTAssertEqual(m.queue, [b, c])
    }

    func testErrorTextFeedsCameraRefusal() {
        var m = TalkMachine()
        m.connect()
        m.press()
        _ = m.receive(.error("the camera refused two-way audio: 461 Unsupported Transport"))
        m.closed(code: 4502, reason: "the camera refused two-way audio")
        XCTAssertEqual(m.phase, .ended(code: 4502, message: "The camera refused two-way audio: 461 Unsupported Transport"))
    }

    func testIdleWhileHeldSpeaks() {
        var m = TalkMachine()
        m.connect()
        _ = m.receive(.ready(.init()))
        m.press()
        m.closed(code: 4408)
        guard case .ended(4408, let message?) = m.phase else { return XCTFail("expected a message, got \(m.phase)") }
        XCTAssertTrue(message.contains("30 seconds"))
    }

    func testRetryOnlyOnceAndOnlyBeforeReady() {
        var m = TalkMachine()
        m.connect()
        XCTAssertTrue(m.shouldRetry(afterClose: 4401, retried: false))
        XCTAssertFalse(m.shouldRetry(afterClose: 4401, retried: true))
        XCTAssertFalse(m.shouldRetry(afterClose: 4409, retried: false))
        _ = m.receive(.ready(.init()))
        XCTAssertFalse(m.shouldRetry(afterClose: 4401, retried: false))
    }

    func testHangUpIsQuiet() {
        var m = TalkMachine()
        m.connect()
        XCTAssertTrue(m.hangUp())
        XCTAssertEqual(m.phase, .ended(code: 1000, message: nil))
    }
}

// MARK: - Session with a fake socket

private final class FakeTalkSocket: TalkSocket, @unchecked Sendable {
    let request: URLRequest
    private let lock = NSLock()
    private var inbox: [Result<TalkSocketMessage, Error>] = []
    private var waiters: [CheckedContinuation<TalkSocketMessage, Error>] = []
    private(set) var sent: [TalkSocketMessage] = []
    private(set) var closedWith: Int?
    var info: (code: Int, reason: String) = (1006, "")

    init(request: URLRequest) { self.request = request }

    func resume() {}

    func send(_ message: TalkSocketMessage) async throws {
        lock.lock(); sent.append(message); lock.unlock()
    }

    var sentSnapshot: [TalkSocketMessage] { lock.lock(); defer { lock.unlock() }; return sent }

    func receive() async throws -> TalkSocketMessage {
        try await withCheckedThrowingContinuation { continuation in
            lock.lock()
            if inbox.isEmpty == false {
                let next = inbox.removeFirst()
                lock.unlock()
                continuation.resume(with: next)
            } else {
                waiters.append(continuation)
                lock.unlock()
            }
        }
    }

    func deliver(_ result: Result<TalkSocketMessage, Error>) {
        lock.lock()
        if waiters.isEmpty == false {
            let waiter = waiters.removeFirst()
            lock.unlock()
            waiter.resume(with: result)
        } else {
            inbox.append(result)
            lock.unlock()
        }
    }

    func serverClose(code: Int, reason: String = "") {
        lock.lock(); info = (code, reason); lock.unlock()
        deliver(.failure(URLError(.networkConnectionLost)))
    }

    func closeInfo() -> (code: Int, reason: String) { lock.lock(); defer { lock.unlock() }; return info }

    func close(code: Int) {
        lock.lock(); closedWith = closedWith ?? code; lock.unlock()
        deliver(.failure(URLError(.cancelled)))
    }
}

private final class Recorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _phases: [TalkPhase] = []
    private var _sockets: [FakeTalkSocket] = []
    private var _tokens: [Bool] = []

    var phases: [TalkPhase] { lock.lock(); defer { lock.unlock() }; return _phases }
    var sockets: [FakeTalkSocket] { lock.lock(); defer { lock.unlock() }; return _sockets }
    var tokens: [Bool] { lock.lock(); defer { lock.unlock() }; return _tokens }

    func phase(_ p: TalkPhase) { lock.lock(); _phases.append(p); lock.unlock() }
    func socket(_ s: FakeTalkSocket) { lock.lock(); _sockets.append(s); lock.unlock() }
    func token(_ force: Bool) -> String { lock.lock(); _tokens.append(force); lock.unlock(); return force ? "fresh" : "stale" }
}

final class TalkSessionTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    private func makeSession(_ rec: Recorder) -> TalkSession {
        TalkSession(baseURL: site, cameraID: "c1",
                    token: { rec.token($0) },
                    socket: { let s = FakeTalkSocket(request: $0); rec.socket(s); return s },
                    onPhase: { rec.phase($0) })
    }

    private func waitUntil(_ what: String, timeout: TimeInterval = 2, _ condition: () -> Bool) async {
        let deadline = Date().addingTimeInterval(timeout)
        while condition() == false {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
    }

    func testReadyQueuedFramesAndStop() async throws {
        let rec = Recorder()
        let session = makeSession(rec)
        session.setTalking(true)
        session.open()
        await waitUntil("socket") { rec.sockets.count == 1 }
        let sock = rec.sockets[0]
        XCTAssertEqual(sock.request.url?.absoluteString, "ws://nvr.test:8090/api/v2/cameras/c1/talkback?rate=16000")
        XCTAssertEqual(sock.request.value(forHTTPHeaderField: "Authorization"), "Bearer stale")

        let early = TalkPCM.encode([1, 2])
        session.send(frame: early)
        sock.deliver(.success(.text(#"{"type":"ready","rate":16000}"#)))
        await waitUntil("talking") { session.phase == .talking }
        let late = TalkPCM.encode([3, 4])
        session.send(frame: late)
        await waitUntil("frames") { sock.sentSnapshot.count >= 3 }
        XCTAssertEqual(sock.sentSnapshot.first, .text(TalkProtocol.authMessage(accessToken: "stale")))
        XCTAssertEqual(Array(sock.sentSnapshot.dropFirst()), [.data(early), .data(late)])

        session.setTalking(false)
        XCTAssertEqual(session.phase, .ready)
        session.send(frame: late)
        session.close()
        XCTAssertEqual(session.phase, .ended(code: 1000, message: nil))
        await waitUntil("stop + close") { sock.closedWith != nil }
        XCTAssertEqual(sock.sentSnapshot.last, .text(TalkProtocol.stopMessage))
        XCTAssertEqual(sock.sentSnapshot.filter { $0 == .data(late) }.count, 1, "released frames are dropped")
        XCTAssertEqual(rec.phases.first, .connecting)
        XCTAssertTrue(rec.phases.contains(.talking))
    }

    func testSignInRefreshOnceOn4401() async throws {
        let rec = Recorder()
        let session = makeSession(rec)
        session.open()
        await waitUntil("first socket") { rec.sockets.count == 1 }
        rec.sockets[0].serverClose(code: 4401)
        await waitUntil("second socket") { rec.sockets.count == 2 }
        XCTAssertEqual(rec.tokens, [false, true])
        XCTAssertEqual(rec.sockets[1].request.value(forHTTPHeaderField: "Authorization"), "Bearer fresh")
        rec.sockets[1].serverClose(code: 4401)
        await waitUntil("ended") { session.phase.isEnded }
        XCTAssertEqual(session.phase, .ended(code: 4401, message: "Sign in again to talk through cameras."))
        XCTAssertEqual(rec.sockets.count, 2, "only one refresh")
    }

    func testBusyAndCameraRefusal() async throws {
        let rec = Recorder()
        let busy = makeSession(rec)
        busy.open()
        await waitUntil("socket") { rec.sockets.count == 1 }
        rec.sockets[0].serverClose(code: 4409)
        await waitUntil("ended") { busy.phase.isEnded }
        XCTAssertEqual(busy.phase, .ended(code: 4409, message: "Someone else is talking through this camera."))

        let refused = makeSession(rec)
        refused.setTalking(true)
        refused.open()
        await waitUntil("socket") { rec.sockets.count == 2 }
        rec.sockets[1].deliver(.success(.text(#"{"type":"error","message":"the camera refused two-way audio: no backchannel"}"#)))
        rec.sockets[1].serverClose(code: 4502, reason: "the camera refused two-way audio")
        await waitUntil("ended") { refused.phase.isEnded }
        XCTAssertEqual(refused.phase, .ended(code: 4502, message: "The camera refused two-way audio: no backchannel"))
    }

    func testLimitAfterTalking() async throws {
        let rec = Recorder()
        let session = makeSession(rec)
        session.open()
        await waitUntil("socket") { rec.sockets.count == 1 }
        rec.sockets[0].deliver(.success(.text(#"{"type":"ready"}"#)))
        await waitUntil("ready") { session.phase == .ready }
        rec.sockets[0].serverClose(code: 4410, reason: "talk time limit reached (300s)")
        await waitUntil("ended") { session.phase.isEnded }
        XCTAssertEqual(session.phase, .ended(code: 4410, message: "Talk time limit reached (5 minutes)."))
        XCTAssertEqual(rec.sockets.count, 1)
    }

    func testCloseBeforeConnectIsQuiet() async throws {
        let rec = Recorder()
        let session = makeSession(rec)
        session.close()
        XCTAssertEqual(session.phase, .ended(code: 1000, message: nil))
        session.open()
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertTrue(rec.sockets.isEmpty, "an ended session never connects")
    }
}
