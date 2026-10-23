import XCTest
@testable import SentinelCore

/// Building the `MacImport` payload from a 1.x install on disk (fixture files
/// in the shapes the old app wrote).
final class MacImportBuilderTests: XCTestCase {
    private var root: URL!

    private let frontID = UUID(uuidString: "11111111-1111-1111-1111-111111111111")!
    private let yardID = UUID(uuidString: "22222222-2222-2222-2222-222222222222")!
    private let webcamID = UUID(uuidString: "33333333-3333-3333-3333-333333333333")!
    private let adminID = UUID(uuidString: "44444444-4444-4444-4444-444444444444")!

    override func setUpWithError() throws {
        root = FileManager.default.temporaryDirectory.appendingPathComponent("mac-import-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: root)
    }

    private func write(_ name: String, _ json: String) throws {
        try Data(json.utf8).write(to: root.appendingPathComponent(name))
    }

    /// Dates as the old app's JSONEncoder wrote them (seconds since 2001).
    private func ref(_ unixSeconds: Double) -> Double { unixSeconds - 978_307_200 }

    private func makeInstall() throws -> (evidenceFile: URL, Void) {
        try write("cameras.json", """
        [
         {"id":"\(frontID)","name":"Front Door","location":"Lobby","status":"online","resolution":"1080p","fps":15,
          "bitrate":"4 Mbps","ipAddress":"10.0.0.5","profile":"Main","isRecording":true,"recordingMode":"continuous",
          "recordingCodec":"h265","rtspURL":"rtsp://admin:s3cret@10.0.0.5:554/main","subStreamRTSPURL":"rtsp://admin:s3cret@10.0.0.5:554/sub",
          "username":"","onvifServiceURL":"http://10.0.0.5/onvif/device_service","retentionDays":14},
         {"id":"\(yardID)","name":"Yard","location":"Unassigned","rtspURL":"rtsp://10.0.0.6/stream1","username":"viewer",
          "isRecording":false},
         {"id":"\(webcamID)","name":"FaceTime HD Camera","location":"This Mac","rtspURL":"","localDeviceID":"0x1234"}
        ]
        """)
        try write("users.json", """
        [{"id":"\(adminID)","name":"Maria","role":"Admin","status":"Active","lastSeen":"Today","passwordHash":"abc"},
         {"id":"\(UUID())","name":"guard1","role":"operator","status":"Invited","lastSeen":"Never"},
         {"id":"\(UUID())","name":"MARIA","role":"Viewer","status":"Active","lastSeen":"Never"}]
        """)
        try write("workflow-state.json", """
        {"handoffs":[],"notifications":[],"cameraGroups":[],
         "notificationRules":[
           {"id":"\(UUID())","name":"Night people","trigger":"Person","delivery":"Desktop","severity":"Warning",
            "isEnabled":true,"isSnoozed":false,"cameraIDs":["\(frontID)","\(webcamID)"],"activeFromHour":22,"activeToHour":6,
            "instructions":"Call the guard."},
           {"id":"\(UUID())","name":"Everything critical","trigger":"Critical","delivery":"Push","severity":"Info",
            "isEnabled":true,"isSnoozed":true}
         ],
         "auditLog":[
           {"id":"\(UUID())","time":\(ref(1_760_000_100)),"user":"Maria","area":"Cameras","action":"Added camera","detail":"Yard"},
           {"id":"\(UUID())","time":\(ref(1_760_000_000)),"user":"Maria","area":"Operator","action":"Signed in","detail":""}
         ]}
        """)
        let caseDir = root.appendingPathComponent("Evidence/CASE-7", isDirectory: true)
        try FileManager.default.createDirectory(at: caseDir, withIntermediateDirectories: true)
        let file = caseDir.appendingPathComponent("clip.mp4")
        try Data("fake video".utf8).write(to: file)
        try write("casework-state.json", """
        {"alerts":[],"incidents":[],"playbackBookmarks":[],
         "evidenceClips":[
           {"id":"\(UUID())","caseID":"CASE-7","title":"Person at door","camera":"Front Door","range":"10:00–10:01",
            "status":"Locked","filePath":"\(file.path)","exportedBy":"Maria","lockedAt":\(ref(1_760_000_500))},
           {"id":"\(UUID())","caseID":"CASE-7","title":"Second angle","camera":"Yard","range":"10:00–10:01",
            "status":"Locked","filePath":"\(file.path)","sha256Hash":"ABCDEF","lockedAt":\(ref(1_760_000_600))},
           {"id":"\(UUID())","caseID":"CASE-8","title":"Gone","camera":"Yard","range":"x","status":"Locked",
            "filePath":"/nonexistent/clip.mp4"},
           {"id":"\(UUID())","caseID":"CASE-9","title":"Not locked","camera":"Yard","range":"x","status":"Exported"}
         ]}
        """)
        try FileManager.default.createDirectory(at: root.appendingPathComponent("Recordings/\(frontID.uuidString)", isDirectory: true),
                                                withIntermediateDirectories: true)
        return (file, ())
    }

    func testBuildsPayloadFromFixtureInstall() throws {
        let (file, _) = try makeInstall()
        let install = LegacyMacInstall(directory: root)
        XCTAssertTrue(install.hasData)

        let output = MacImportBuilder(install: install, sourceVersion: "1.1.2",
                                      password: { $0 == self.yardID ? "yard-pass" : nil },
                                      hash: { _ in "feedface" }).build()
        let payload = output.payload
        XCTAssertEqual(payload.sourceVersion, "1.1.2")

        // Cameras: credentials leave the URL; the built-in webcam is skipped.
        XCTAssertEqual(payload.cameras.map(\.name), ["Front Door", "Yard"])
        let front = payload.cameras[0]
        XCTAssertEqual(front.sourceId, frontID.uuidString)
        XCTAssertEqual(front.rtspUrl, "rtsp://10.0.0.5:554/main")
        XCTAssertEqual(front.subRtspUrl, "rtsp://10.0.0.5:554/sub")
        XCTAssertEqual(front.username, "admin")
        XCTAssertEqual(front.password, "s3cret")
        XCTAssertEqual(front.recording, true)
        XCTAssertEqual(front.recordingMode, "continuous")
        XCTAssertEqual(front.retentionDays, 14)
        XCTAssertEqual(front.location, "Lobby")
        XCTAssertEqual(front.onvifUrl, "http://10.0.0.5/onvif/device_service")
        XCTAssertEqual(front.recordingsPath, root.appendingPathComponent("Recordings/\(frontID.uuidString)").path)
        let yard = payload.cameras[1]
        XCTAssertEqual(yard.username, "viewer")
        XCTAssertEqual(yard.password, "yard-pass", "password from the old secret store")
        XCTAssertNil(yard.location, "\"Unassigned\" is the old app's placeholder")
        XCTAssertNil(yard.recordingsPath, "no recordings folder")
        XCTAssertNil(yard.recordingMode, "not recording: off")
        XCTAssertTrue(output.skipped.contains { $0.contains("FaceTime HD Camera") })

        // Users: roles normalized, duplicates by name dropped, no hashes (not bcrypt).
        XCTAssertEqual(payload.users.map(\.name), ["Maria", "guard1"])
        XCTAssertEqual(payload.users.map(\.role), [.admin, .operator])
        XCTAssertTrue(payload.users.allSatisfy { $0.passwordHash == nil })

        // Rules
        XCTAssertEqual(payload.rules.count, 2)
        let night = payload.rules[0]
        XCTAssertEqual(night.kinds, ["Person"])
        XCTAssertEqual(night.minSeverity, "Warning")
        XCTAssertEqual(night.cameraIds, [frontID.uuidString], "only imported cameras, by Mac source id")
        XCTAssertTrue(night.schedule)
        XCTAssertEqual(night.fromMinute, 22 * 60)
        XCTAssertEqual(night.toMinute, 6 * 60)
        XCTAssertEqual(night.days, [0, 1, 2, 3, 4, 5, 6])
        XCTAssertEqual(night.instructions, "Call the guard.")
        let critical = payload.rules[1]
        XCTAssertEqual(critical.kinds, [])
        XCTAssertEqual(critical.minSeverity, "Critical", "a severity trigger becomes the minimum severity")
        XCTAssertFalse(critical.enabled, "snoozed rules come over disabled")
        XCTAssertFalse(critical.schedule)

        // Evidence: one file per case id, missing files skipped, unlocked clips ignored.
        XCTAssertEqual(payload.evidence.map(\.caseId), ["CASE-7", "CASE-7-2"])
        XCTAssertEqual(payload.evidence[0].sha256, "feedface", "hashed when the old app stored none")
        XCTAssertEqual(payload.evidence[1].sha256, "abcdef")
        XCTAssertEqual(payload.evidence[0].cameraSourceId, frontID.uuidString)
        XCTAssertEqual(payload.evidence[0].size, 10)
        XCTAssertEqual(payload.evidence[0].lockedAt, 1_760_000_500_000)
        XCTAssertEqual(payload.evidence[0].lockedBy, "Maria")
        XCTAssertEqual(output.files.map(\.caseId), ["CASE-7", "CASE-7-2"])
        XCTAssertEqual(output.files.first?.url, file)
        XCTAssertTrue(output.skipped.contains { $0.contains("CASE-8") })

        // Audit oldest first, Unix ms.
        XCTAssertEqual(payload.audit.map(\.action), ["Signed in", "Added camera"])
        XCTAssertEqual(payload.audit.first?.at, 1_760_000_000_000)
    }

    func testPayloadEncodesToTheContractShape() throws {
        _ = try makeInstall()
        let output = MacImportBuilder(install: LegacyMacInstall(directory: root), sourceVersion: "1.x", hash: { _ in "00" }).build()
        let json = try JSONSerialization.jsonObject(with: SentinelAPI.jsonEncoder.encode(output.payload)) as! [String: Any]
        XCTAssertEqual(Set(json.keys), ["sourceVersion", "cameras", "users", "rules", "evidence", "audit"])
        let camera = (json["cameras"] as! [[String: Any]])[1]
        XCTAssertNil(camera["password"], "no password key when there is none")
        XCTAssertEqual(camera["sourceId"] as? String, yardID.uuidString)
        let rule = (json["rules"] as! [[String: Any]])[0]
        XCTAssertEqual(Set(rule.keys), ["name", "enabled", "cameraIds", "kinds", "minSeverity", "schedule", "fromMinute",
                                        "toMinute", "days", "delivery", "instructions"])
    }

    func testLegacyRecordingModesMapToTheServerModes() {
        XCTAssertEqual(MacImportBuilder.recordingMode(legacy: "continuous", hasSubStream: true), "continuous")
        XCTAssertEqual(MacImportBuilder.recordingMode(legacy: "motion", hasSubStream: false), "events")
        XCTAssertEqual(MacImportBuilder.recordingMode(legacy: "dualStream", hasSubStream: true), "dual")
        XCTAssertEqual(MacImportBuilder.recordingMode(legacy: "dualStream", hasSubStream: false), "events", "dual needs a sub-stream")
        XCTAssertEqual(MacImportBuilder.recordingMode(legacy: "something new", hasSubStream: true), "continuous")
    }

    func testEmptyInstallHasNothingToImport() {
        let install = LegacyMacInstall(directory: root)
        XCTAssertFalse(install.hasData)
        let output = MacImportBuilder(install: install, sourceVersion: "1.x").build()
        XCTAssertTrue(output.payload.cameras.isEmpty)
        XCTAssertTrue(output.files.isEmpty)
    }

    func testOldSecretStoreOpensOnlyWithTheRightPassword() throws {
        let url = root.appendingPathComponent("secrets.vault")
        let writer = SecretVault(fileURL: url)
        XCTAssertFalse(writer.unlockExisting(userKey: adminID.uuidString, password: "pw"), "never creates a vault")
        XCTAssertTrue(writer.unlock(userKey: adminID.uuidString, password: "pw"))
        writer.setValue(Data("cam-pass".utf8), service: CameraSecrets.vaultService, account: frontID.uuidString)

        let reader = SecretVault(fileURL: url)
        XCTAssertTrue(reader.exists)
        XCTAssertFalse(reader.unlockExisting(userKey: adminID.uuidString, password: "wrong"))
        XCTAssertNil(reader.value(service: CameraSecrets.vaultService, account: frontID.uuidString))
        XCTAssertTrue(reader.unlockExisting(userKey: adminID.uuidString.uppercased(), password: "pw"))
        XCTAssertEqual(reader.value(service: CameraSecrets.vaultService, account: frontID.uuidString), Data("cam-pass".utf8))
    }

    func testImportAndEvidenceUploadRequests() async throws {
        MockURLProtocol.reset()
        defer { MockURLProtocol.reset() }
        let (file, _) = try makeInstall()
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://127.0.0.1:8090")!, siteID: "s1", tokenStore: store,
                              session: MockURLProtocol.session(), clientLabel: "test")
        MockURLProtocol.handler = { request, _ in
            if request.url?.path == "/api/v2/import/mac" {
                return .json(200, #"{"cameras":2,"users":2,"rules":2,"evidence":2,"audit":2,"skipped":null,"cameraIds":{"\#(self.frontID)":"c9"}}"#)
            }
            return MockURLProtocol.Reply(status: 204, body: Data())
        }
        let output = MacImportBuilder(install: LegacyMacInstall(directory: root), sourceVersion: "1.x", hash: { _ in "00" }).build()
        let result = try await api.importMac(output.payload)
        XCTAssertEqual(result.cameraIds[frontID.uuidString], "c9")
        XCTAssertEqual(result.skipped, [])
        try await api.uploadImportedEvidence(caseID: "CASE-7", fileURL: file)

        let requests = MockURLProtocol.requests
        XCTAssertEqual(requests.count, 2)
        XCTAssertEqual(requests[0].request.httpMethod, "POST")
        XCTAssertEqual(requests[0].request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(requests[1].request.httpMethod, "PUT")
        XCTAssertEqual(requests[1].request.url?.path, "/api/v2/import/mac/evidence/CASE-7")
        XCTAssertEqual(requests[1].request.value(forHTTPHeaderField: "Content-Type"), "application/octet-stream")
        XCTAssertEqual(requests[1].request.value(forHTTPHeaderField: "Authorization"), "Bearer access-1")
    }
}

/// Wall time ↔ playback playlist time (`Span.media`) across Recording Servers.
final class TimelineMappingTests: XCTestCase {
    private func timeline(_ spans: String) throws -> V2.Timeline {
        try SentinelAPI.decode(V2.Timeline.self, from: Data("""
        {"from":0,"to":100000,"now":100000,"spans":\(spans),"pending":[],"writing":false,"events":[],"alarms":[]}
        """.utf8))
    }

    func testMediaTimeUsesSpanMediaPerRecorder() throws {
        let t = try timeline("""
        [{"start":1000,"end":11000,"media":0,"recorderId":"local"},
         {"start":20000,"end":30000,"media":10,"recorderId":"local"},
         {"start":40000,"end":50000,"media":0,"recorderId":"rs2"}]
        """)
        let a = try XCTUnwrap(t.mediaTime(at: 6000))
        XCTAssertEqual(a.span.recorderId, "local")
        XCTAssertEqual(a.seconds, 5, accuracy: 0.001)
        XCTAssertEqual(try XCTUnwrap(t.mediaTime(at: 25000)).seconds, 15, accuracy: 0.001)
        let b = try XCTUnwrap(t.mediaTime(at: 41500))
        XCTAssertEqual(b.span.recorderId, "rs2")
        XCTAssertEqual(b.seconds, 1.5, accuracy: 0.001)
        XCTAssertNil(t.mediaTime(at: 15000), "a gap")

        XCTAssertEqual(t.wallTime(atMedia: 15, recorderID: "local"), 25000)
        XCTAssertEqual(t.wallTime(atMedia: 1.5, recorderID: "rs2"), 41500)
        XCTAssertNil(t.wallTime(atMedia: 99, recorderID: "local"))
    }

    func testMediaTimeWithoutMediaFieldLaysSpansEndToEnd() throws {
        let t = try timeline("""
        [{"start":1000,"end":11000,"recorderId":"local"},
         {"start":20000,"end":30000,"recorderId":"local"},
         {"start":40000,"end":50000,"recorderId":"rs2"}]
        """)
        XCTAssertEqual(try XCTUnwrap(t.mediaTime(at: 21000)).seconds, 11, accuracy: 0.001)
        XCTAssertEqual(try XCTUnwrap(t.mediaTime(at: 45000)).seconds, 5, accuracy: 0.001, "other recorders don't count")
    }
}
