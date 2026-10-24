import XCTest
@testable import Sentinel_Mobile

/// WAVE23 (stream I): the phone camera pipeline with synthetic segments (the
/// Simulator has no camera): backlog policy, token timing, state machine,
/// the sequential uplink and the HTTP push plane.

/// Thread-safe box for test state touched from the uplink actor.
final class Locked<Value>: @unchecked Sendable {
    private var value: Value
    private let lock = NSLock()
    init(_ value: Value) { self.value = value }
    var get: Value { lock.lock(); defer { lock.unlock() }; return value }
    func mutate(_ change: (inout Value) -> Void) { lock.lock(); change(&value); lock.unlock() }
}

/// Polls until `condition` holds (the uplink runs on its own task).
func waitUntil(timeout: TimeInterval = 3, _ condition: () -> Bool, file: StaticString = #filePath, line: UInt = #line) async {
    let end = Date().addingTimeInterval(timeout)
    while condition() == false {
        if Date() > end { XCTFail("timed out", file: file, line: line); return }
        try? await Task.sleep(nanoseconds: 5_000_000)
    }
}

final class FakePushTransport: PushTransport, @unchecked Sendable {
    enum Call: Equatable {
        case token, start, initSegment(String), segment(String, Int, Data), end(String)
    }
    let calls = Locked<[Call]>([])
    var tokenResult: (Int) throws -> V2.PushToken = { n in
        V2.PushToken(token: "t\(n)", baseUrl: "/v/t\(n)", gatewayUrl: "/v/t\(n)", recorderId: "rs1",
                     expiresAt: Date().addingTimeInterval(300).unixMillis)
    }
    var segmentResult: (Int, Int) throws -> Void = { _, _ in } // (call index, seq)
    private let tokens = Locked(0)
    private let sessions = Locked(0)
    private let segmentCalls = Locked(0)

    var tokenCount: Int { tokens.get }

    func pushToken() async throws -> V2.PushToken {
        calls.mutate { $0.append(.token) }
        tokens.mutate { $0 += 1 }
        return try tokenResult(tokens.get)
    }

    func endpoint(for token: V2.PushToken, preferGateway: Bool) -> PushEndpoint {
        PushEndpoint.resolve(token, site: URL(string: "http://10.0.0.1:8090")!, preferGateway: preferGateway)
    }

    func startSession(_ endpoint: PushEndpoint, format: V2.PushSessionRequest) async throws -> V2.PushSession {
        calls.mutate { $0.append(.start) }
        sessions.mutate { $0 += 1 }
        return V2.PushSession(sessionId: "s\(sessions.get)", maxSegmentBytes: 8_388_608)
    }

    func putInit(_ endpoint: PushEndpoint, sessionID: String, data: Data) async throws {
        calls.mutate { $0.append(.initSegment(sessionID)) }
    }

    func putSegment(_ endpoint: PushEndpoint, sessionID: String, seq: Int, data: Data) async throws {
        segmentCalls.mutate { $0 += 1 }
        try segmentResult(segmentCalls.get, seq)
        calls.mutate { $0.append(.segment(sessionID, seq, data)) }
    }

    func endSession(_ endpoint: PushEndpoint, sessionID: String) async {
        calls.mutate { $0.append(.end(sessionID)) }
    }

    var sentSegments: [(String, Int, Data)] {
        calls.get.compactMap { if case let .segment(s, q, d) = $0 { return (s, q, d) } else { return nil } }
    }
}

final class PhoneCameraTests: XCTestCase {
    private let format = V2.PushSessionRequest(width: 1280, height: 720, fps: 30, audio: nil)

    private func seg(_ n: Int, duration: TimeInterval = 1) -> PushSegment {
        PushSegment(data: Data("seg\(n)".utf8), duration: duration)
    }

    private func makeUplink(_ transport: FakePushTransport, clock: Locked<Date> = Locked(Date()),
                            sleeps: Locked<[TimeInterval]> = Locked([]),
                            events: Locked<[UplinkEvent]> = Locked([])) -> PhoneStreamUplink {
        PhoneStreamUplink(transport: transport, now: { clock.get },
                          sleep: { d in sleeps.mutate { $0.append(d) } },
                          onEvent: { e in events.mutate { $0.append(e) } })
    }

    // MARK: Backlog policy

    func testBacklogDropsOldestWholeSegmentsPast20Seconds() {
        var backlog = SegmentBacklog()
        var dropped = 0
        for n in 0..<25 { dropped += backlog.append(seg(n)) }
        XCTAssertEqual(dropped, 5)
        XCTAssertEqual(backlog.count, 20)
        XCTAssertEqual(backlog.seconds, 20, accuracy: 0.001)
        XCTAssertEqual(backlog.popFirst()?.data, Data("seg5".utf8), "the oldest five went")
    }

    func testBacklogKeepsTheNewestEvenWhenItAloneIsTooLong() {
        var backlog = SegmentBacklog(maxBacklog: 2)
        backlog.append(seg(0))
        XCTAssertEqual(backlog.append(seg(1, duration: 5)), 1)
        XCTAssertEqual(backlog.count, 1)
        XCTAssertEqual(backlog.popFirst()?.data, Data("seg1".utf8))
        XCTAssertNil(backlog.popFirst())
    }

    // MARK: Timing

    func testPushTokenRefreshAtFourMinutes() {
        let t0 = Date()
        let expires = t0.addingTimeInterval(300)
        XCTAssertFalse(PushTokenClock.needsRefresh(issuedAt: t0, expiresAt: expires, now: t0))
        XCTAssertFalse(PushTokenClock.needsRefresh(issuedAt: t0, expiresAt: expires, now: t0.addingTimeInterval(239)))
        XCTAssertTrue(PushTokenClock.needsRefresh(issuedAt: t0, expiresAt: expires, now: t0.addingTimeInterval(240)))
        XCTAssertTrue(PushTokenClock.needsRefresh(issuedAt: t0, expiresAt: t0.addingTimeInterval(30), now: t0),
                      "a token with under a minute left is replaced")
        XCTAssertTrue(PushTokenClock.needsRefresh(issuedAt: nil, expiresAt: nil, now: t0))
    }

    func testBackoff() {
        XCTAssertEqual((0...6).map { PushBackoff.delay(failures: $0) }, [0, 0.5, 1, 2, 4, 8, 8])
        XCTAssertEqual(VideoUploadJob.backoff(attempt: 1), 1)
        XCTAssertEqual(VideoUploadJob.backoff(attempt: 3), 4)
    }

    func testRateMeter() {
        var meter = UplinkRateMeter(window: 5)
        let t0 = Date()
        meter.record(bytes: 312_500, at: t0)           // 2.5 Mbit
        meter.record(bytes: 312_500, at: t0.addingTimeInterval(1))
        XCTAssertEqual(meter.kilobitsPerSecond(now: t0.addingTimeInterval(1)), 1000)
        XCTAssertEqual(meter.kilobitsPerSecond(now: t0.addingTimeInterval(20)), 0, "old samples age out")
    }

    // MARK: State machine

    func testPhaseMachine() {
        var p = PhoneCameraPhase.idle
        p = p.next(.start); XCTAssertEqual(p, .settingUp)
        p = p.next(.captureStarted); XCTAssertEqual(p, .connecting)
        p = p.next(.segmentSent); XCTAssertEqual(p, .streaming)
        p = p.next(.trouble("offline")); XCTAssertEqual(p, .reconnecting("offline"))
        p = p.next(.segmentSent); XCTAssertEqual(p, .streaming)
        p = p.next(.interrupted); XCTAssertEqual(p, .paused(.interrupted))
        p = p.next(.resumed); XCTAssertEqual(p, .connecting)
        p = p.next(.backgrounded); XCTAssertEqual(p, .paused(.background))
        XCTAssertTrue(p.isActive)
        p = p.next(.captureStarted); XCTAssertEqual(p, .connecting, "back from the background: a new session")
        p = p.next(.fatal("gone")); XCTAssertEqual(p, .failed("gone"))
        XCTAssertFalse(p.isActive)
        XCTAssertEqual(p.next(.segmentSent), .failed("gone"), "a late segment doesn't revive it")
        XCTAssertEqual(p.next(.start), .settingUp)
        XCTAssertEqual(PhoneCameraPhase.streaming.next(.stop), .idle)
        XCTAssertEqual(PhoneCameraPhase.idle.next(.segmentSent), .idle)
    }

    // MARK: Wire shapes

    func testSessionRequestSendsAudioNull() throws {
        let json = String(decoding: try SentinelAPI.jsonEncoder.encode(format), as: UTF8.self)
        XCTAssertEqual(json, #"{"audio":null,"fps":30,"height":720,"video":"h264","width":1280}"#)
        let withAudio = V2.PushSessionRequest(width: 720, height: 1280, fps: 15, audio: "aac")
        XCTAssertTrue(String(decoding: try SentinelAPI.jsonEncoder.encode(withAudio), as: UTF8.self).contains(#""audio":"aac""#))
    }

    func testEndpointURLs() {
        let site = URL(string: "https://site.example")!
        let token = V2.PushToken(token: "t", baseUrl: "http://10.0.0.7:8090/v/t", gatewayUrl: "/v/t", recorderId: "rs1", expiresAt: 0)
        let direct = PushEndpoint.resolve(token, site: site, preferGateway: false)
        XCTAssertFalse(direct.isGateway)
        XCTAssertEqual(direct.segment("cam1", "sid", 7).absoluteString, "http://10.0.0.7:8090/v/t/push/cam1/sessions/sid/segments/7")
        let gateway = PushEndpoint.resolve(token, site: site, preferGateway: true)
        XCTAssertTrue(gateway.isGateway)
        XCTAssertEqual(gateway.initSegment("cam1", "sid").absoluteString, "https://site.example/v/t/push/cam1/sessions/sid/init")
        XCTAssertEqual(gateway.sessions("cam1").absoluteString, "https://site.example/v/t/push/cam1/sessions")
    }

    // MARK: Uplink

    func testUplinkSendsInitThenSegmentsInOrder() async {
        let transport = FakePushTransport()
        let events = Locked<[UplinkEvent]>([])
        let uplink = makeUplink(transport, events: events)
        await uplink.setInit(Data("init".utf8), format: format)
        for n in 0..<3 { await uplink.enqueue(seg(n)) }
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 3 }
        XCTAssertEqual(transport.calls.get.prefix(3), [.token, .start, .initSegment("s1")])
        XCTAssertEqual(transport.sentSegments.map(\.1), [0, 1, 2])
        XCTAssertEqual(transport.sentSegments.map(\.2), [Data("seg0".utf8), Data("seg1".utf8), Data("seg2".utf8)])
        XCTAssertTrue(events.get.contains(.sessionStarted("s1")))
        await uplink.stop()
        XCTAssertEqual(transport.calls.get.last, .end("s1"), "stop ends the session")
    }

    func testSegmentsBeforeInitAreIgnored() async {
        let transport = FakePushTransport()
        let uplink = makeUplink(transport)
        await uplink.enqueue(seg(0))
        let queued = await uplink.queuedSeconds
        XCTAssertEqual(queued, 0)
    }

    func testRetryWithBackoffKeepsTheSameSequenceNumber() async {
        let transport = FakePushTransport()
        transport.segmentResult = { call, _ in if call <= 2 { throw PushError.transient("offline") } }
        let sleeps = Locked<[TimeInterval]>([])
        let events = Locked<[UplinkEvent]>([])
        let uplink = makeUplink(transport, sleeps: sleeps, events: events)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.enqueue(seg(1))
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 2 }
        XCTAssertEqual(transport.sentSegments.map(\.1), [0, 1])
        XCTAssertEqual(sleeps.get, [0.5, 1])
        XCTAssertTrue(events.get.contains(.trouble("offline")))
        await uplink.stop()
    }

    func testBacklogOverflowSkipsAheadWithoutSequenceGaps() async {
        let transport = FakePushTransport()
        let events = Locked<[UplinkEvent]>([])
        let uplink = makeUplink(transport, events: events)
        await uplink.setInit(Data("init".utf8), format: format)
        for n in 0..<25 { await uplink.enqueue(seg(n)) } // the uplink isn't sending yet: 25 s behind
        XCTAssertEqual(events.get.filter { if case .skipped = $0 { return true } else { return false } }.count, 5)
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 20 }
        XCTAssertEqual(transport.sentSegments.first?.2, Data("seg5".utf8))
        XCTAssertEqual(transport.sentSegments.map(\.1), Array(0..<20), "numbers are given out when sent")
        await uplink.stop()
    }

    func testRetriedSegmentIsDroppedWhenNewerOnesWereSkipped() async {
        let transport = FakePushTransport()
        let failing = Locked(true)
        transport.segmentResult = { _, _ in if failing.get { throw PushError.transient("slow") } }
        let uplink = makeUplink(transport)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.start()
        await waitUntil { transport.calls.get.contains(.initSegment("s1")) }
        // seg0 is being retried; 22 more seconds arrive, so the backlog skips.
        for n in 1...22 { await uplink.enqueue(seg(n)) }
        failing.mutate { $0 = false }
        await waitUntil { transport.sentSegments.count >= 20 }
        XCTAssertNotEqual(transport.sentSegments.first?.2, Data("seg0".utf8), "the stale retried segment went")
        await uplink.stop()
    }

    func testOutOfOrderResyncsToExpected() async {
        let transport = FakePushTransport()
        transport.segmentResult = { call, _ in if call == 1 { throw PushError.outOfOrder(expected: 5) } }
        let uplink = makeUplink(transport)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.enqueue(seg(1))
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 2 }
        XCTAssertEqual(transport.sentSegments.map(\.1), [5, 6])
        await uplink.stop()
    }

    func testSessionGoneStartsANewSessionWithInit() async {
        let transport = FakePushTransport()
        transport.segmentResult = { call, _ in if call == 2 { throw PushError.sessionGone } }
        let uplink = makeUplink(transport)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.enqueue(seg(1))
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 2 }
        XCTAssertEqual(transport.sentSegments.map(\.0), ["s1", "s2"])
        XCTAssertEqual(transport.sentSegments.map(\.1), [0, 0], "the new session counts from 0")
        XCTAssertTrue(transport.calls.get.contains(.initSegment("s2")))
        await uplink.stop()
    }

    func testNewInitSegmentStartsANewSession() async {
        let transport = FakePushTransport()
        let uplink = makeUplink(transport)
        await uplink.setInit(Data("init1".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 1 }
        await uplink.setInit(Data("init2".utf8), format: format) // the writer restarted after an interruption
        await uplink.enqueue(seg(1))
        await waitUntil { transport.sentSegments.count == 2 }
        XCTAssertEqual(transport.sentSegments.last?.0, "s2")
        XCTAssertEqual(transport.sentSegments.last?.1, 0)
        await uplink.stop()
    }

    func testTokenRefreshedAfterFourMinutes() async {
        let transport = FakePushTransport()
        let clock = Locked(Date())
        let uplink = makeUplink(transport, clock: clock)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 1 }
        await uplink.enqueue(seg(1))
        await waitUntil { transport.sentSegments.count == 2 }
        XCTAssertEqual(transport.tokenCount, 1, "reused within 4 minutes")
        clock.mutate { $0 = $0.addingTimeInterval(241) }
        await uplink.enqueue(seg(2))
        await waitUntil { transport.sentSegments.count == 3 }
        XCTAssertEqual(transport.tokenCount, 2)
        XCTAssertEqual(transport.sentSegments.map(\.0), ["s1", "s1", "s1"], "a new token keeps the session")
        await uplink.stop()
    }

    func testMidStream402StopsCleanly() async {
        let transport = FakePushTransport()
        transport.tokenResult = { n in
            if n >= 2 { throw PushError.cloudRequired }
            return V2.PushToken(token: "t", baseUrl: "/v/t", gatewayUrl: "/v/t", recorderId: "rs1",
                                expiresAt: Date().addingTimeInterval(300).unixMillis)
        }
        let clock = Locked(Date())
        let events = Locked<[UplinkEvent]>([])
        let uplink = makeUplink(transport, clock: clock, events: events)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.start()
        await waitUntil { transport.sentSegments.count == 1 }
        clock.mutate { $0 = $0.addingTimeInterval(241) } // subscription ended; the next token is refused
        await uplink.enqueue(seg(1))
        await waitUntil { events.get.contains(.fatal(.cloudRequired)) }
        XCTAssertEqual(transport.calls.get.last, .end("s1"), "the session is ended")
        XCTAssertEqual(transport.sentSegments.count, 1)
        let running = await uplink.isRunning
        XCTAssertFalse(running)
    }

    func testRevokedKeyIsFatal() async {
        let transport = FakePushTransport()
        transport.tokenResult = { _ in throw PushError.keyRejected }
        let events = Locked<[UplinkEvent]>([])
        let uplink = makeUplink(transport, events: events)
        await uplink.setInit(Data("init".utf8), format: format)
        await uplink.enqueue(seg(0))
        await uplink.start()
        await waitUntil { events.get.contains(.fatal(.keyRejected)) }
        XCTAssertEqual(transport.sentSegments.count, 0)
    }

    // MARK: HTTP push plane

    private func api() -> SentinelAPI {
        SentinelAPI(baseURL: URL(string: "http://10.0.0.1:8090")!, siteID: "s1", tokenStore: MemoryTokenStore(), session: mockSession())
    }

    func testPushTokenUsesThePushKeyNotTheSession() async throws {
        MockURLProtocol.reset { _ in
            .init(body: Data(#"{"token":"p1","baseUrl":"/v/p1","gatewayUrl":"/v/p1","recorderId":"rs1","expiresAt":1}"#.utf8))
        }
        let transport = HTTPPushTransport(api: api(), cameraID: "cam1", pushKey: "secret", session: mockSession())
        let token = try await transport.pushToken()
        XCTAssertEqual(token.token, "p1")
        let request = MockURLProtocol.seen[0]
        XCTAssertEqual(request.url?.path, "/api/v2/cameras/cam1/push-token")
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-Sentinel-Push-Key"), "secret")
        XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"))
    }

    func testPushTokenErrorsMap() async {
        let transport = HTTPPushTransport(api: api(), cameraID: "cam1", pushKey: "k", session: mockSession())
        let cases: [(Int, String, PushError)] = [
            (402, #"{"error":{"code":"cloud_required","message":"Using an iPhone as a camera is part of Sentinel Cloud."}}"#, .cloudRequired),
            (401, #"{"error":{"code":"unauthorized","message":"bad key"}}"#, .keyRejected),
            (404, #"{"error":{"code":"not_found","message":"no camera"}}"#, .cameraGone)
        ]
        for (status, body, expected) in cases {
            MockURLProtocol.reset { _ in .init(status: status, body: Data(body.utf8)) }
            do {
                _ = try await transport.pushToken()
                XCTFail("\(status) should throw")
            } catch {
                XCTAssertEqual(error as? PushError, expected, "\(status)")
            }
        }
    }

    func testVideoPlaneRequestsAndErrors() async throws {
        let transport = HTTPPushTransport(api: api(), cameraID: "cam1", pushKey: "k", session: mockSession())
        let endpoint = PushEndpoint(base: URL(string: "http://10.0.0.7:8090/v/p1")!, isGateway: false, recorderID: "rs1")
        MockURLProtocol.reset { _ in .init(body: Data(#"{"sessionId":"sid1","maxSegmentBytes":8388608}"#.utf8)) }
        let session = try await transport.startSession(endpoint, format: format)
        XCTAssertEqual(session.sessionId, "sid1")
        XCTAssertEqual(MockURLProtocol.seen[0].url?.path, "/v/p1/push/cam1/sessions")

        MockURLProtocol.reset { _ in .init(status: 204) }
        try await transport.putSegment(endpoint, sessionID: "sid1", seq: 3, data: Data("x".utf8))
        XCTAssertEqual(MockURLProtocol.seen[0].httpMethod, "PUT")
        XCTAssertEqual(MockURLProtocol.seen[0].url?.path, "/v/p1/push/cam1/sessions/sid1/segments/3")

        MockURLProtocol.reset { _ in .init(status: 409, body: Data(#"{"expected":2}"#.utf8)) }
        do { try await transport.putSegment(endpoint, sessionID: "sid1", seq: 12, data: Data()); XCTFail() } catch {
            XCTAssertEqual(error as? PushError, .outOfOrder(expected: 2))
        }
        MockURLProtocol.reset { _ in .init(status: 404) }
        do { try await transport.putInit(endpoint, sessionID: "sid1", data: Data()); XCTFail() } catch {
            XCTAssertEqual(error as? PushError, .sessionGone)
        }
        MockURLProtocol.reset { _ in .init(error: URLError(.notConnectedToInternet)) }
        do { try await transport.putSegment(endpoint, sessionID: "sid1", seq: 0, data: Data()); XCTFail() } catch {
            guard case .transient = error as? PushError else { return XCTFail("\(error)") }
        }
    }

    func testCreatePhoneCameraBody() async throws {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "a", expiresAt: Date().addingTimeInterval(800).unixMillis, refreshToken: "r",
                             refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "admin"), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://10.0.0.1:8090")!, siteID: "s1", tokenStore: store, session: mockSession())
        MockURLProtocol.reset { _ in
            .init(body: Data("""
            {"camera":{"id":"cam9","name":"Porch iPhone","location":"","rtspUrl":"","subRtspUrl":"","username":"","hasPassword":false,
             "recording":true,"retentionDays":30,"fps":30,"motionLevel":50,"alertOn":"auto","onvifUrl":"","cameraAnalytics":false,
             "sortOrder":0,"createdAt":1,"recorderId":"rs1","groupIds":null,"status":{"online":false,"viewers":0,"message":"Phone not streaming"},
             "source":"phone"},"pushKey":"pk_123"}
            """.utf8))
        }
        let created = try await api.createPhoneCamera(name: "Porch iPhone")
        XCTAssertEqual(created.pushKey, "pk_123")
        XCTAssertTrue(created.camera.isPhoneCamera)
        XCTAssertEqual(String(decoding: MockURLProtocol.seen[0].httpBody ?? Data(), as: UTF8.self), #"{"name":"Porch iPhone"}"#)
    }

    // MARK: Screen texts and storage

    func testWarnings() {
        XCTAssertNil(PhoneCameraWarnings.battery(level: 0.5, charging: false))
        XCTAssertNil(PhoneCameraWarnings.battery(level: 0.05, charging: true))
        XCTAssertNil(PhoneCameraWarnings.battery(level: -1, charging: false), "unknown level")
        XCTAssertNotNil(PhoneCameraWarnings.battery(level: 0.18, charging: false))
        XCTAssertNil(PhoneCameraWarnings.thermal(.fair))
        XCTAssertNotNil(PhoneCameraWarnings.thermal(.serious))
        XCTAssertNotNil(PhoneCameraWarnings.thermal(.critical))
    }

    func testCredentialsAndSettingsRoundTrip() {
        let store = MemoryPhoneCameraStore()
        XCTAssertNil(store.load(siteID: "s1"))
        store.save(PhoneCameraCredentials(cameraId: "c", pushKey: "k", name: "n"), siteID: "s1")
        XCTAssertEqual(store.load(siteID: "s1")?.pushKey, "k")
        store.delete(siteID: "s1")
        XCTAssertNil(store.load(siteID: "s1"))

        let defaults = UserDefaults(suiteName: "phone-camera-tests")!
        defaults.removePersistentDomain(forName: "phone-camera-tests")
        XCTAssertEqual(PhoneCameraSettings.load(defaults).quality, .hd720p30, "720p30 is the default")
        var settings = PhoneCameraSettings()
        settings.quality = .hd720p15
        settings.lens = .front
        settings.save(defaults)
        XCTAssertEqual(PhoneCameraSettings.load(defaults), settings)
        XCTAssertEqual(PhoneCameraQuality.hd1080p30.width, 1920)
        XCTAssertEqual(PhoneCameraQuality.hd720p15.fps, 15)
    }
}
