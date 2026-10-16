import XCTest
@testable import SentinelCore

/// Exports workspace logic (range/batch validation, SHA copy model) and the
/// evidence API paths.
final class OpsExportsTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() {
        super.setUp()
        MockURLProtocol.reset()
    }

    override func tearDown() {
        MockURLProtocol.reset()
        super.tearDown()
    }

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
    }

    // MARK: Validation

    func testBlockerCoversEveryRule() {
        let now: Int64 = 1_760_000_000_000
        let min: Int64 = 60_000
        XCTAssertEqual(ExportRules.blocker(cameraIDs: [], from: now - min, to: now, now: now), "Pick at least one camera.")
        XCTAssertEqual(ExportRules.blocker(cameraIDs: ["c1"], from: 0, to: now, now: now), "Pick a start and an end.")
        XCTAssertEqual(ExportRules.blocker(cameraIDs: ["c1"], from: now, to: now, now: now), "The end must be after the start.")
        XCTAssertEqual(ExportRules.blocker(cameraIDs: ["c1"], from: now + min, to: now + 2 * min, now: now), "The start is in the future.")
        XCTAssertEqual(ExportRules.blocker(cameraIDs: ["c1"], from: now - 31 * min, to: now, now: now), "Exports are limited to 30 minutes.")
        XCTAssertNil(ExportRules.blocker(cameraIDs: ["c1"], from: now - 30 * min, to: now, now: now))
    }

    func testCameraSelectionIsLimitedToVisible() {
        XCTAssertEqual(ExportRules.limit(["a", "x", "b", "a"], toVisible: ["a", "b", "c"]), ["a", "b"])
    }

    func testBatchLabelNoteAndTitle() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(secondsFromGMT: 0)!
        let date = Date(timeIntervalSince1970: 1_760_000_000)
        XCTAssertEqual(ExportRules.batchLabel(count: 1, at: date, calendar: cal), "")
        XCTAssertEqual(ExportRules.batchLabel(count: 3, at: date, calendar: cal), "Batch 2025-10-09 08:53 (3 cameras)")
        XCTAssertEqual(ExportRules.composeNote(label: "Batch", note: "  hi "), "Batch · hi")
        XCTAssertEqual(ExportRules.composeNote(label: "", note: " "), "")
        XCTAssertEqual(ExportRules.title("  "), "Clip Export")
        XCTAssertEqual(ExportRules.title(String(repeating: "x", count: 200)).count, 120)
    }

    // MARK: Batch

    func testBatchRunsSequentiallyReportsProgressAndContinuesAfterFailure() async {
        var seen: [String] = []
        var texts: [String] = []
        let outcome = await ExportBatch.run(
            cameraIDs: ["a", "b", "c"],
            name: { "cam-\($0)" },
            progress: { texts.append($0.text) },
            create: { id in
                seen.append(id)
                if id == "b" { throw SentinelAPIError.server(status: 400, code: "bad_request", message: "range too long (max 30 minutes)") }
            })
        XCTAssertEqual(seen, ["a", "b", "c"])
        XCTAssertEqual(texts, ["Locking cam-a (1 of 3)…", "Locking cam-b (2 of 3)…", "Locking cam-c (3 of 3)…"])
        XCTAssertEqual(outcome.made, 2)
        XCTAssertEqual(outcome.failures, ["cam-b: range too long (max 30 minutes)"])
        XCTAssertEqual(outcome.summary, "Locked 2 clips as evidence. 1 export failed.")
    }

    // MARK: Display + copy

    func testBytesShaAndRangeText() {
        XCTAssertEqual(EvidenceDisplay.bytes(512), "512 B")
        XCTAssertEqual(EvidenceDisplay.bytes(1536), "1.5 KB")
        XCTAssertEqual(EvidenceDisplay.bytes(5 * 1024 * 1024), "5.0 MB")
        XCTAssertEqual(EvidenceDisplay.shortSHA(String(repeating: "ab", count: 32)), "abababababababab…")
        XCTAssertEqual(EvidenceDisplay.shortSHA("abc"), "abc")
        var c = V2.EvidenceCase(id: "e1", caseId: "EV-1", cameraId: "c1", cameraName: "Front", range: "Alarm window", size: 1, sha256: "f", lockedAt: 0, lockedBy: "m")
        XCTAssertEqual(EvidenceDisplay.rangeText(c), "Alarm window")
        c.fromMs = 1000; c.toMs = 2000
        XCTAssertEqual(EvidenceDisplay.rangeText(c, formatter: { "F\($0)" }, timeFormatter: { "T\($0)" }), "F1000 – T2000")
    }

    func testShaCopyModelCopiesFullHashAndResets() {
        let sha = String(repeating: "0123456789abcdef", count: 4)
        let c = V2.EvidenceCase(id: "e1", caseId: "EV-1", cameraId: "c1", cameraName: "Front", size: 1, sha256: sha, lockedAt: 0, lockedBy: "m")
        var model = SHACopyModel()
        XCTAssertEqual(model.text(for: c), sha)
        XCTAssertEqual(model.label(for: "e1"), "Copy")
        model.markCopied("e1")
        XCTAssertEqual(model.label(for: "e1"), "Copied")
        model.markCopied("e2")
        model.reset("e1") // stale timer must not clear the newer copy
        XCTAssertEqual(model.copiedID, "e2")
        model.reset("e2")
        XCTAssertNil(model.copiedID)
    }

    func testVerifyState() {
        XCTAssertEqual(VerifyState.from(.init(ok: true, sha256: "x", message: nil)), .ok)
        if case .failed = VerifyState.from(.init(ok: false, sha256: "", message: nil)) {} else { XCTFail() }
    }

    // MARK: API

    private static let caseJSON = """
    {"id":"e1","caseId":"EV-1","cameraId":"c1","cameraName":"Front Door","title":"T","range":"r","size":2048,
     "sha256":"abc","lockedAt":1760000000000,"lockedBy":"maria","note":"n","fromMs":1759999000000,"toMs":1759999600000,"recorderId":"local"}
    """

    func testCreateEvidencePostsRangeAndDecodesCase() async throws {
        MockURLProtocol.handler = { _, _ in .json(201, Self.caseJSON) }
        let made = try await makeAPI().createEvidence(.init(cameraId: "c1", fromMs: 10, toMs: 20, title: "T", note: "n"))
        XCTAssertEqual(made.caseId, "EV-1")
        XCTAssertEqual(made.toMs, 1_759_999_600_000)
        let req = try XCTUnwrap(MockURLProtocol.requests.last)
        XCTAssertEqual(req.request.httpMethod, "POST")
        XCTAssertEqual(req.request.url?.path, "/api/v2/evidence")
        let body = try JSONSerialization.jsonObject(with: XCTUnwrap(req.body)) as? [String: Any]
        XCTAssertEqual(body?["cameraId"] as? String, "c1")
        XCTAssertEqual(body?["fromMs"] as? Int, 10)
        XCTAssertEqual(body?["note"] as? String, "n")
    }

    func testOverLimitRangeSurfacesServerMessage() async {
        MockURLProtocol.handler = { _, _ in
            .json(400, #"{"error":{"code":"bad_request","message":"a clip can be at most 30 minutes"}}"#)
        }
        do {
            _ = try await makeAPI().createEvidence(.init(cameraId: "c1", fromMs: 0, toMs: 9_999_999_999, title: "T"))
            XCTFail("expected 400")
        } catch {
            XCTAssertEqual(ExportRules.failureText(error), "a clip can be at most 30 minutes")
        }
    }

    func testListVerifyAndDelete() async throws {
        let api = makeAPI()
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("GET", "/api/v2/evidence"): return .json(200, "{\"items\":[\(Self.caseJSON)]}")
            case ("POST", "/api/v2/evidence/e1/verify"): return .json(200, #"{"ok":false,"sha256":"zzz","message":"changed"}"#)
            case ("DELETE", "/api/v2/evidence/e1"): return MockURLProtocol.Reply(status: 204, body: Data())
            default: return .json(404, #"{"error":{"code":"not_found","message":"no"}}"#)
            }
        }
        let list = try await api.evidenceCases()
        XCTAssertEqual(list.map(\.id), ["e1"])
        let verdict = try await api.verifyEvidence("e1")
        XCTAssertFalse(verdict.ok)
        XCTAssertEqual(VerifyState.from(verdict), .failed("changed"))
        try await api.deleteEvidence("e1")
    }

    func testDownloadStreamsToFile() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/evidence/e1/download")
            XCTAssertNotNil(request.value(forHTTPHeaderField: "Authorization"))
            return MockURLProtocol.Reply(status: 200, body: Data("mp4bytes".utf8), headers: ["Content-Type": "video/mp4"])
        }
        let dest = FileManager.default.temporaryDirectory.appendingPathComponent("evidence-\(UUID().uuidString).mp4")
        defer { try? FileManager.default.removeItem(at: dest) }
        try await makeAPI().downloadEvidence("e1", to: dest, session: MockURLProtocol.session())
        XCTAssertEqual(try Data(contentsOf: dest), Data("mp4bytes".utf8))
    }
}
