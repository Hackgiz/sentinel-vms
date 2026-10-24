import XCTest
import CryptoKit
@testable import Sentinel_Mobile

/// WAVE23 (stream I): video uploads into the evidence vault: part plan,
/// streaming SHA-256, the Sentinel Cloud availability decision, error
/// mapping (402 cloud_required) and the job's pause / resume / retry.

final class FakeUploadService: VideoUploadService, @unchecked Sendable {
    let parts = Locked<[(Int, Data)]>([])
    let starts = Locked<[V2.UploadStart]>([])
    let completes = Locked(0)
    let aborts = Locked<[String]>([])
    var partSize: Int64 = 1000
    var startError: Error?
    var completeError: Error?
    /// (call number, part index) → throw to fail.
    var partHook: (Int, Int) async throws -> Void = { _, _ in }
    private let partCalls = Locked(0)

    func availability() async -> UploadAvailability { .available }

    func start(_ start: V2.UploadStart) async throws -> V2.UploadCreated {
        starts.mutate { $0.append(start) }
        if let startError { throw startError }
        return V2.UploadCreated(uploadId: "u\(starts.get.count)", partSize: partSize)
    }

    func putPart(uploadID: String, index: Int, data: Data, progress: @escaping @Sendable (Int64) -> Void) async throws {
        partCalls.mutate { $0 += 1 }
        try await partHook(partCalls.get, index)
        progress(Int64(data.count))
        parts.mutate { $0.append((index, data)) }
    }

    func complete(uploadID: String) async throws -> V2.UploadedEvidence {
        completes.mutate { $0 += 1 }
        if let completeError { throw completeError }
        return V2.UploadedEvidence(id: "ev1", title: "Clip", sha256: nil)
    }

    func abort(uploadID: String) async { aborts.mutate { $0.append(uploadID) } }
}

@MainActor
final class VideoUploadTests: XCTestCase {
    private var file: URL!
    private var bytes = Data()

    override func setUp() async throws {
        bytes = Data((0..<2500).map { UInt8($0 % 251) })
        file = FileManager.default.temporaryDirectory.appendingPathComponent("upload-\(UUID().uuidString).mp4")
        try bytes.write(to: file)
    }

    override func tearDown() async throws {
        try? FileManager.default.removeItem(at: file)
    }

    private static let cloudRequired = SentinelAPIError.from(status: 402, body: Data(
        #"{"error":{"code":"cloud_required","message":"Uploading videos is part of Sentinel Cloud."}}"#.utf8))

    private func job(_ service: FakeUploadService, sleeps: Locked<[TimeInterval]> = Locked([])) -> VideoUploadJob {
        let job = VideoUploadJob(file: file, context: UploadContext(cameraID: "cam1", alarmID: "al1"), service: service,
                                 sleep: { d in sleeps.mutate { $0.append(d) } })
        job.title = "  Porch  "
        return job
    }

    // MARK: Plan and checksum

    func testPartPlan() {
        let mib: Int64 = 1024 * 1024
        let plan = UploadPartPlan(size: 20 * mib, partSize: nil)
        XCTAssertEqual(plan.partSize, 8 * mib)
        XCTAssertEqual(plan.count, 3)
        XCTAssertEqual(plan.range(of: 0), 0..<(8 * mib))
        XCTAssertEqual(plan.range(of: 2), (16 * mib)..<(20 * mib))
        XCTAssertEqual(plan.bytes(before: 2), 16 * mib)
        XCTAssertEqual(plan.bytes(before: 3), 20 * mib)
        XCTAssertEqual(UploadPartPlan(size: 16 * mib, partSize: 8 * mib).count, 2, "an exact multiple has no empty tail")
        XCTAssertEqual(UploadPartPlan(size: 0, partSize: 8 * mib).count, 0)
        XCTAssertEqual(UploadPartPlan(size: 1, partSize: 0).partSize, 8 * mib, "a bad part size falls back")
    }

    func testStreamingSHA256MatchesOneShot() throws {
        let count: Int = 3 * 1024 * 1024 + 517
        let big = Data((0..<count).map { (i: Int) -> UInt8 in UInt8(truncatingIfNeeded: i &* 7) })
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("sha-\(UUID().uuidString)")
        try big.write(to: url)
        defer { try? FileManager.default.removeItem(at: url) }
        var reported: [Int64] = []
        let hash = try StreamingSHA256.hash(fileAt: url) { reported.append($0) }
        let expected = SHA256.hash(data: big).map { String(format: "%02x", $0) }.joined()
        XCTAssertEqual(hash, expected)
        XCTAssertEqual(reported.count, 4, "1 MiB at a time")
        XCTAssertEqual(reported.last, Int64(big.count))
    }

    func testFileFacts() throws {
        XCTAssertEqual(UploadFile.contentType(for: URL(fileURLWithPath: "/a/IMG_1.MOV")), "video/quicktime")
        XCTAssertEqual(UploadFile.contentType(for: URL(fileURLWithPath: "/a/clip.mp4")), "video/mp4")
        XCTAssertEqual(UploadFile.size(of: file), 2500)
        XCTAssertEqual(try UploadFile.readPart(file, range: 1000..<2000), bytes.subdata(in: 1000..<2000))
        XCTAssertEqual(UploadFile.userAgent(version: "3.6.0", model: "iPhone17,2", system: "18.1"),
                       "Sentinel Mobile/3.6.0 (iPhone17,2; iOS 18.1)")
    }

    // MARK: Sentinel Cloud

    func testAvailabilityDecision() {
        let flags = V2.CloudFeatures(uploadsAvailable: false, phoneCamerasAvailable: true)
        XCTAssertEqual(CloudFeatureAvailability.decide(.success(flags), feature: .uploads), .cloudRequired)
        XCTAssertEqual(CloudFeatureAvailability.decide(.success(flags), feature: .phoneCamera), .available)
        let other = V2.CloudFeatures(uploadsAvailable: true, phoneCamerasAvailable: false)
        XCTAssertEqual(CloudFeatureAvailability.decide(.success(other), feature: .uploads), .available)
        XCTAssertEqual(CloudFeatureAvailability.decide(.success(other), feature: .phoneCamera), .cloudRequired)
        let old = V2.CloudFeatures(uploadsAvailable: nil, phoneCamerasAvailable: nil)
        XCTAssertEqual(CloudFeatureAvailability.decide(.success(old), feature: .uploads), .available, "older server: rely on the 402")
        XCTAssertEqual(CloudFeatureAvailability.decide(.success(old), feature: .phoneCamera), .available)
        XCTAssertEqual(CloudFeatureAvailability.decide(.failure(SentinelAPIError.transport(message: "x")), feature: .uploads), .available)
    }

    func testCloudFeaturesDecodeFromTheCloudStatus() async throws {
        MockURLProtocol.reset { _ in
            .init(body: Data(#"{"linked":true,"plan":"cloud","remote":{"enabled":true},"uploadsAvailable":false,"phoneCamerasAvailable":true}"#.utf8))
        }
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "a", expiresAt: Date().addingTimeInterval(800).unixMillis, refreshToken: "r",
                             refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://10.0.0.1:8090")!, siteID: "s1", tokenStore: store, session: mockSession())
        let uploads = await CloudFeatureAvailability.check(.uploads, api: api)
        let camera = await CloudFeatureAvailability.check(.phoneCamera, api: api)
        XCTAssertEqual(uploads, .cloudRequired)
        XCTAssertEqual(camera, .available)
        XCTAssertEqual(MockURLProtocol.seen.last?.url?.path, "/api/v2/cloud")
    }

    func testCloudSheetTextHasNoPurchaseLink() {
        XCTAssertEqual(CloudRequiredSheet.title, "Needs Sentinel Cloud")
        XCTAssertEqual(CloudRequiredSheet.Feature.uploads.message,
                       "Uploading videos from your iPhone is part of Sentinel Cloud. The site's administrator can turn it on.")
        XCTAssertEqual(CloudRequiredSheet.Feature.phoneCamera.message,
                       "Using your iPhone as a camera is part of Sentinel Cloud. The site's administrator can turn it on.")
        for text in [CloudRequiredSheet.Feature.uploads.message, CloudRequiredSheet.Feature.phoneCamera.message] {
            XCTAssertFalse(text.contains("http"), "App Store: no purchase links")
        }
    }

    func test402Mapping() {
        XCTAssertTrue(Self.cloudRequired.isCloudRequired)
        XCTAssertEqual(Self.cloudRequired.code, "cloud_required")
        XCTAssertEqual(UploadFailure.classify(Self.cloudRequired, step: .start), .cloudRequired)
        XCTAssertEqual(UploadFailure.classify(Self.cloudRequired, step: .part), .cloudRequired)
        // A bare 402 without the v2 body still means it.
        XCTAssertEqual(UploadFailure.classify(SentinelAPIError.from(status: 402, body: Data()), step: .complete), .cloudRequired)
        XCTAssertEqual(UploadFailure.classify(SentinelAPIError.from(status: 404, body: Data()), step: .start), .unsupported)
        XCTAssertEqual(UploadFailure.classify(SentinelAPIError.from(status: 404, body: Data()), step: .part), .expired)
        if case .retryable = UploadFailure.classify(SentinelAPIError.from(status: 503, body: Data()), step: .part) {} else { XCTFail() }
        if case .retryable = UploadFailure.classify(SentinelAPIError.transport(message: "offline"), step: .part) {} else { XCTFail() }
        if case .fatal = UploadFailure.classify(SentinelAPIError.from(status: 400, body: Data()), step: .complete) {} else { XCTFail() }
    }

    // MARK: The job

    func testUploadsAllPartsInOrderThenCompletes() async {
        let service = FakeUploadService()
        let job = job(service)
        job.start()
        await job.wait()
        guard case .done(let item) = job.state else { return XCTFail("\(job.state)") }
        XCTAssertEqual(item.id, "ev1")
        XCTAssertEqual(service.parts.get.map(\.0), [0, 1, 2])
        XCTAssertEqual(service.parts.get.map(\.1).reduce(Data(), +), bytes, "the parts add up to the file")
        let start = service.starts.get[0]
        XCTAssertEqual(start.size, 2500)
        XCTAssertEqual(start.sha256, SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined())
        XCTAssertEqual(start.title, "Porch", "trimmed")
        XCTAssertNil(start.note, "an empty note isn't sent")
        XCTAssertEqual(start.alarmId, "al1")
        XCTAssertEqual(start.contentType, "video/mp4")
        XCTAssertEqual(service.completes.get, 1)
    }

    func testStartBodyCarriesOnlyContractFields() throws {
        let body = V2.UploadStart(fileName: "a.mov", size: 1, sha256: "ab", contentType: "video/quicktime",
                                  title: nil, note: nil, recordedAt: nil, cameraId: nil, eventId: "e1", alarmId: nil, incidentId: nil)
        let json = String(decoding: try SentinelAPI.jsonEncoder.encode(body), as: UTF8.self)
        XCTAssertEqual(json, #"{"contentType":"video\/quicktime","eventId":"e1","fileName":"a.mov","sha256":"ab","size":1}"#)
    }

    func testRetriesAPartWithBackoffThenGivesUpAndResumesFromIt() async {
        let service = FakeUploadService()
        // Part 1 fails 4 times (one job run), then works.
        service.partHook = { call, index in
            if index == 1, call <= 5 { throw SentinelAPIError.transport(message: "offline") }
        }
        let sleeps = Locked<[TimeInterval]>([])
        let job = job(service, sleeps: sleeps)
        job.start()
        await job.wait()
        guard case .failed(_, let canRetry) = job.state else { return XCTFail("\(job.state)") }
        XCTAssertTrue(canRetry)
        XCTAssertEqual(sleeps.get, [1, 2, 4], "backoff between the 4 attempts")
        XCTAssertEqual(job.nextPart, 1)
        job.start() // Retry
        await job.wait()
        guard case .done = job.state else { return XCTFail("\(job.state)") }
        XCTAssertEqual(service.parts.get.map(\.0), [0, 1, 2], "part 0 isn't sent twice")
        XCTAssertEqual(service.starts.get.count, 1, "the same upload continues")
    }

    func testPauseAndResume() async {
        let service = FakeUploadService()
        let blocked = Locked(true)
        service.partHook = { _, index in
            if index == 1, blocked.get {
                blocked.mutate { $0 = false }
                try await Task.sleep(nanoseconds: 5_000_000_000) // cancelled by pause
            }
        }
        let job = job(service)
        job.start()
        await waitUntil { blocked.get == false }
        job.pause()
        XCTAssertEqual(job.state, .paused(sent: 1000, total: 2500))
        try? await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(job.state, .paused(sent: 1000, total: 2500), "stays paused")
        job.start()
        await job.wait()
        guard case .done = job.state else { return XCTFail("\(job.state)") }
        XCTAssertEqual(service.parts.get.map(\.0), [0, 1, 2])
    }

    func testCloudRequiredAtStartOrMidFlow() async {
        let service = FakeUploadService()
        service.startError = Self.cloudRequired
        let first = job(service)
        first.start()
        await first.wait()
        XCTAssertEqual(first.state, .cloudRequired)

        let mid = FakeUploadService()
        mid.partHook = { _, index in if index == 2 { throw Self.cloudRequired } }
        let second = job(mid)
        second.start()
        await second.wait()
        XCTAssertEqual(second.state, .cloudRequired, "a 402 mid-flow shows the Cloud sheet")
        second.start()
        XCTAssertEqual(second.state, .cloudRequired, "no retry loop against a 402")
    }

    func testOlderServerIsUnsupported() async {
        let service = FakeUploadService()
        service.startError = SentinelAPIError.from(status: 404, body: Data("404 page not found".utf8))
        let job = job(service)
        job.start()
        await job.wait()
        XCTAssertEqual(job.state, .unsupported)
    }

    func testExpiredUploadStartsOverOnRetry() async {
        let service = FakeUploadService()
        service.partHook = { call, index in
            if call == 2 { throw SentinelAPIError.from(status: 404, body: Data()) }
        }
        let job = job(service)
        job.start()
        await job.wait()
        guard case .failed(_, true) = job.state else { return XCTFail("\(job.state)") }
        XCTAssertNil(job.uploadID)
        job.start()
        await job.wait()
        guard case .done = job.state else { return XCTFail("\(job.state)") }
        XCTAssertEqual(service.starts.get.count, 2)
        XCTAssertEqual(service.parts.get.map(\.0), [0, 0, 1, 2])
    }

    func testEmptyFileIsRefused() async throws {
        try Data().write(to: file)
        let service = FakeUploadService()
        let job = VideoUploadJob(file: file, service: service)
        job.start()
        await job.wait()
        XCTAssertEqual(job.state, .failed(message: "This video is empty.", canRetry: false))
        XCTAssertTrue(service.starts.get.isEmpty)
    }

    func testCancelAbortsOnTheServer() async {
        let service = FakeUploadService()
        service.partHook = { _, index in if index == 1 { throw SentinelAPIError.from(status: 400, body: Data()) } }
        let job = job(service)
        job.start()
        await job.wait()
        await job.cancel()
        XCTAssertEqual(service.aborts.get, ["u1"])
    }

    // MARK: Over the API

    func testAPIServiceSendsPartsAndMaps402() async throws {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "a", expiresAt: Date().addingTimeInterval(800).unixMillis, refreshToken: "r",
                             refreshExpiresAt: Date().addingTimeInterval(3600).unixMillis, userName: "op"), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://10.0.0.1:8090")!, siteID: "s1", tokenStore: store, session: mockSession())
        let service = APIVideoUploadService(api: api, userAgent: "Sentinel Mobile/3.6.0 (iPhone17,2; iOS 18.1)")
        MockURLProtocol.reset { req in
            switch req.url!.path {
            case "/api/v2/evidence/uploads": return .init(body: Data(#"{"uploadId":"up1","partSize":1000,"expiresAt":1}"#.utf8))
            case "/api/v2/evidence/uploads/up1/complete": return .init(body: Data(#"{"id":"ev9","title":"Porch","source":"upload"}"#.utf8))
            default: return .init(status: 204)
            }
        }
        let job = VideoUploadJob(file: file, context: .none, service: service)
        job.start()
        await job.wait()
        guard case .done(let item) = job.state else { return XCTFail("\(job.state)") }
        XCTAssertEqual(item.id, "ev9")
        let seen = MockURLProtocol.seen
        XCTAssertEqual(seen.map { $0.url!.path }, ["/api/v2/evidence/uploads", "/api/v2/evidence/uploads/up1/parts/0",
                                                   "/api/v2/evidence/uploads/up1/parts/1", "/api/v2/evidence/uploads/up1/parts/2",
                                                   "/api/v2/evidence/uploads/up1/complete"])
        XCTAssertEqual(seen[1].httpMethod, "PUT")
        XCTAssertEqual(seen[1].httpBody?.count, 1000)
        XCTAssertEqual(seen[3].httpBody?.count, 500)
        XCTAssertEqual(seen[0].value(forHTTPHeaderField: "User-Agent"), "Sentinel Mobile/3.6.0 (iPhone17,2; iOS 18.1)")
        XCTAssertEqual(seen[1].value(forHTTPHeaderField: "Authorization"), "Bearer a")

        MockURLProtocol.reset { _ in
            .init(status: 402, body: Data(#"{"error":{"code":"cloud_required","message":"Uploading videos is part of Sentinel Cloud."}}"#.utf8))
        }
        let gated = VideoUploadJob(file: file, context: .none, service: service)
        gated.start()
        await gated.wait()
        XCTAssertEqual(gated.state, .cloudRequired)
    }
}
