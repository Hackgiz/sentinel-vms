import XCTest
@testable import SentinelCore

/// Wave 23 on the Mac: phone cameras (source, push key rotate/revoke, editor
/// and tile wording) and evidence uploaded from the iPhone.
final class PhoneCameraTests: XCTestCase {
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

    private func camera(_ extra: String = "", online: Bool? = nil, message: String? = nil) throws -> V2.Camera {
        var json = Fixtures.camera
        if extra.isEmpty == false {
            json = json.replacingOccurrences(of: #""recorderId":"local""#, with: #""recorderId":"local"\#(extra)"#)
        }
        var c = try SentinelAPI.decode(V2.Camera.self, from: Data(json.utf8))
        if let online { c.status.online = online }
        if let message { c.status.message = message }
        return c
    }

    private func phone(online: Bool = true, message: String = "", lastSeen: UnixMillis? = nil) throws -> V2.Camera {
        var extra = #","source":"phone""#
        if let lastSeen { extra += #","lastSeenAt":\#(lastSeen)"# }
        return try camera(extra, online: online, message: message)
    }

    // MARK: Decoding

    func testOlderCameraJSONIsRTSP() throws {
        let c = try camera()
        XCTAssertNil(c.source)
        XCTAssertFalse(c.isPhone)
        XCTAssertTrue(PhoneCameraLogic.showsNetworkFields(c))
        XCTAssertTrue(PhoneCameraLogic.showsNetworkFields(nil), "a new camera is RTSP")
    }

    func testPhoneCameraDecodes() throws {
        let c = try phone(lastSeen: 1_760_000_000_000)
        XCTAssertEqual(c.source, .phone)
        XCTAssertTrue(c.isPhone)
        XCTAssertEqual(c.lastSeenAt, 1_760_000_000_000)
        XCTAssertFalse(PhoneCameraLogic.showsNetworkFields(c))
        let rtsp = try camera(#","source":"rtsp""#)
        XCTAssertEqual(rtsp.source, .rtsp)
        XCTAssertFalse(rtsp.isPhone)
    }

    // MARK: Status wording

    func testSummaryAndTileMessage() throws {
        let now = Date(timeIntervalSince1970: 1_760_000_600)
        let streaming = try phone(online: true, message: "Streaming from EJ's iPhone", lastSeen: 1_760_000_000_000)
        XCTAssertEqual(PhoneCameraLogic.summary(streaming, now: now), "iPhone camera · Streaming")
        XCTAssertEqual(PhoneCameraLogic.statusMessage(streaming), "Streaming from EJ's iPhone")

        let idle = try phone(online: false, message: "", lastSeen: 1_760_000_000_000)
        XCTAssertEqual(PhoneCameraLogic.summary(idle, now: now), "iPhone camera · Not streaming · last seen 10 min ago")
        XCTAssertEqual(PhoneCameraLogic.offlineMessage(idle), "Phone not streaming")
        XCTAssertEqual(PhoneCameraLogic.offlineMessage(try phone(online: false, message: "No signal")), "Phone not streaming")
        XCTAssertEqual(PhoneCameraLogic.offlineMessage(try phone(online: false, message: "Recording server offline")),
                       "Recording server offline")
        XCTAssertEqual(PhoneCameraLogic.summary(try phone(online: false)), "iPhone camera · Not streaming",
                       "no last-seen part when the server doesn't send it")

        // RTSP cameras keep the old tile wording.
        XCTAssertEqual(PhoneCameraLogic.offlineMessage(try camera(online: false, message: "")), "No signal")
        XCTAssertEqual(PhoneCameraLogic.offlineMessage(try camera(online: false, message: "Media engine starting")), "Media engine starting")
    }

    func testNeedsSentinelCloudIsNeverRelabelled() throws {
        let offline = try phone(online: false, message: "Needs Sentinel Cloud")
        XCTAssertTrue(PhoneCameraLogic.needsCloud(offline))
        XCTAssertEqual(PhoneCameraLogic.offlineMessage(offline), "Needs Sentinel Cloud")
        XCTAssertEqual(PhoneCameraLogic.statusMessage(offline), "Needs Sentinel Cloud")
        XCTAssertEqual(PhoneCameraLogic.summary(offline), "iPhone camera · Needs Sentinel Cloud")
        let online = try phone(online: true, message: "Needs Sentinel Cloud")
        XCTAssertEqual(PhoneCameraLogic.statusMessage(online), "Needs Sentinel Cloud")
        XCTAssertFalse(PhoneCameraLogic.needsCloud(try camera(online: false, message: "Needs Sentinel Cloud")),
                       "only phone cameras")
        XCTAssertFalse(PhoneCameraLogic.needsCloud(try phone(online: false, message: "")))
        XCTAssertEqual(PhoneCameraLogic.cloudNote, "Needs Sentinel Cloud.")
        XCTAssertEqual(PhoneCameraLogic.learnMoreURL.absoluteString, "https://sentvms.com/cloud")
    }

    func testCloudStatusAvailabilityFlags() throws {
        let old = try SentinelAPI.decode(V2.CloudStatus.self, from: Data(#"{"linked":false,"features":null}"#.utf8))
        XCTAssertNil(old.phoneCamerasAvailable)
        XCTAssertNil(old.uploadsAvailable)
        let new = try SentinelAPI.decode(V2.CloudStatus.self,
                                         from: Data(#"{"linked":true,"features":[],"phoneCamerasAvailable":false,"uploadsAvailable":true}"#.utf8))
        XCTAssertEqual(new.phoneCamerasAvailable, false)
        XCTAssertEqual(new.uploadsAvailable, true)
    }

    func testLastSeen() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        XCTAssertEqual(PhoneCameraLogic.lastSeen(999_990_000, now: now), "just now")
        XCTAssertEqual(PhoneCameraLogic.lastSeen((1_000_000 - 3 * 3600) * 1000, now: now), "3 h ago")
        XCTAssertEqual(PhoneCameraLogic.lastSeen((1_000_000 - 86_400) * 1000, now: now), "1 day ago")
        XCTAssertEqual(PhoneCameraLogic.lastSeen((1_000_000 - 5 * 86_400) * 1000, now: now), "5 days ago")
        XCTAssertEqual(PhoneCameraLogic.lastSeen(2_000_000_000, now: now), "just now", "a clock ahead of ours is not negative")
    }

    // MARK: Editor

    func testDualIsDisabledForPhoneCameras() {
        let choices = RecordingModeLogic.choices(hasSubStream: true, serverDefault: .dual, isPhone: true)
        let dual = choices.first { $0.mode == .dual }
        XCTAssertEqual(dual?.enabled, false)
        XCTAssertEqual(dual?.detail, "Not available for iPhone cameras")
        XCTAssertTrue(choices.filter { $0.mode != .dual }.allSatisfy(\.enabled))
        XCTAssertEqual(choices.first?.label, "Server default (Events only)", "a dual server default falls back to Events only")
        // An RTSP camera with a second stream still gets dual.
        XCTAssertEqual(RecordingModeLogic.choices(hasSubStream: true, serverDefault: nil).first { $0.mode == .dual }?.enabled, true)
        XCTAssertEqual(PhoneCameraLogic.allowedMode(.dual), .events)
        XCTAssertEqual(PhoneCameraLogic.allowedMode(.continuous), .continuous)
    }

    func testCanSaveAndStripNetworkFields() {
        XCTAssertTrue(PhoneCameraLogic.canSave(name: "Porch phone", rtspUrl: "", isPhone: true))
        XCTAssertFalse(PhoneCameraLogic.canSave(name: "Door", rtspUrl: " ", isPhone: false))
        XCTAssertFalse(PhoneCameraLogic.canSave(name: " ", rtspUrl: "", isPhone: true))
        XCTAssertTrue(PhoneCameraLogic.canSave(name: "Door", rtspUrl: "rtsp://h/s", isPhone: false))

        var patch = V2.CameraPatch()
        patch.name = "Porch"
        patch.rtspUrl = ""
        patch.subRtspUrl = ""
        patch.username = ""
        patch.password = "x"
        patch.onvifUrl = ""
        PhoneCameraLogic.stripNetworkFields(&patch)
        XCTAssertEqual(patch.name, "Porch")
        XCTAssertNil(patch.rtspUrl)
        XCTAssertNil(patch.subRtspUrl)
        XCTAssertNil(patch.username)
        XCTAssertNil(patch.password)
        XCTAssertNil(patch.onvifUrl)
        let body = String(decoding: try! SentinelAPI.jsonEncoder.encode(patch), as: UTF8.self)
        XCTAssertFalse(body.contains("rtspUrl"))
        XCTAssertFalse(body.contains("onvifUrl"))
    }

    func testUseIPhoneInstructionsPointAtSettings() {
        XCTAssertTrue(PhoneCameraLogic.useIPhoneSteps.contains { $0.contains("Settings → Use this iPhone as a camera") })
        XCTAssertTrue(PhoneCameraLogic.rotateMessage.contains("Use this iPhone as a camera"))
    }

    // MARK: API

    func testRotatePushKey() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/api/v2/cameras/c1/push-key")
            return .json(200, #"{"pushKey":"k-new"}"#)
        }
        let answer = try await makeAPI().rotatePushKey(cameraID: "c1")
        XCTAssertEqual(answer.pushKey, "k-new")
        XCTAssertNil(answer.camera)
    }

    func testRevokePushKey() async throws {
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.httpMethod, "DELETE")
            XCTAssertEqual(request.url?.path, "/api/v2/cameras/c1/push-key")
            return MockURLProtocol.Reply(status: 204, body: Data())
        }
        try await makeAPI().revokePushKey(cameraID: "c1")
        XCTAssertEqual(MockURLProtocol.requests.count, 1)
    }

    func testCreatePhoneCamera() async throws {
        let cam = Fixtures.camera.replacingOccurrences(of: #""recorderId":"local""#, with: #""recorderId":"local","source":"phone""#)
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/phone-cameras")
            return .json(201, #"{"camera":\#(cam),"pushKey":"k1"}"#)
        }
        let answer = try await makeAPI().createPhoneCamera(V2.PhoneCameraInput(name: "Porch"))
        XCTAssertEqual(answer.pushKey, "k1")
        XCTAssertEqual(answer.camera?.isPhone, true)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: XCTUnwrap(MockURLProtocol.requests.last?.body)) as? NSDictionary)
        XCTAssertEqual(body["name"] as? String, "Porch")
        XCTAssertNil(body["recorderId"])
    }

    // MARK: Evidence uploads

    private let exportJSON = """
    {"id":"e1","caseId":"CASE-1","cameraId":"c1","cameraName":"Front Door","title":"Door","range":"",
     "size":1048576,"sha256":"ab","lockedAt":1760000000000,"lockedBy":"maria","note":"","fromMs":1759999000000,"toMs":1759999060000}
    """

    private let uploadJSON = """
    {"id":"e2","caseId":"CASE-2","cameraName":"","title":"Parking lot","range":"",
     "size":2048,"sha256":"cd","lockedAt":1760000000000,"lockedBy":"alex","note":"",
     "source":"upload","uploadedBy":"alex","device":"Alex's iPhone","recordedAt":1759990000000,
     "fileName":"IMG_0042.MOV","contentType":"video/quicktime"}
    """

    func testExportDecodesWithoutUploadFields() throws {
        let c = try SentinelAPI.decode(V2.EvidenceCase.self, from: Data(exportJSON.utf8))
        XCTAssertNil(c.source)
        XCTAssertFalse(c.isUpload)
        XCTAssertEqual(EvidenceDisplay.uploadDetail(c), "")
        XCTAssertEqual(EvidenceDisplay.sourceName(c), "Front Door")
        XCTAssertEqual(EvidenceDisplay.downloadName(c), "CASE-1.mp4")
        XCTAssertEqual(EvidenceDisplay.whenText(c, formatter: { "\($0)" }),
                       EvidenceDisplay.rangeText(c, formatter: { "\($0)" }))
    }

    func testUploadDecodesAndDisplays() throws {
        let c = try SentinelAPI.decode(V2.EvidenceCase.self, from: Data(uploadJSON.utf8))
        XCTAssertTrue(c.isUpload)
        XCTAssertEqual(c.cameraId, "", "an upload without a camera still decodes")
        XCTAssertEqual(c.uploadedBy, "alex")
        XCTAssertEqual(c.device, "Alex's iPhone")
        XCTAssertEqual(c.recordedAt, 1_759_990_000_000)
        XCTAssertEqual(EvidenceDisplay.uploadedBadge, "Uploaded from iPhone")
        XCTAssertEqual(EvidenceDisplay.uploadDetail(c, formatter: { "t\($0)" }), "by alex · Alex's iPhone · recorded t1759990000000")
        XCTAssertEqual(EvidenceDisplay.sourceName(c), "iPhone video")
        XCTAssertEqual(EvidenceDisplay.whenText(c, formatter: { "t\($0)" }), "Recorded t1759990000000")
        XCTAssertEqual(EvidenceDisplay.downloadName(c), "CASE-2.mov")
    }

    func testUploadFallbacks() throws {
        var c = try SentinelAPI.decode(V2.EvidenceCase.self, from: Data(uploadJSON.utf8))
        c.recordedAt = nil
        c.device = nil
        c.uploadedBy = nil
        c.contentType = nil
        c.fileName = "clip.mp4"
        c.cameraName = "Back Gate"
        XCTAssertEqual(EvidenceDisplay.uploadDetail(c), "by alex", "falls back to lockedBy")
        XCTAssertEqual(EvidenceDisplay.whenText(c, formatter: { "t\($0)" }), "Uploaded t1760000000000")
        XCTAssertEqual(EvidenceDisplay.downloadName(c), "CASE-2.mp4")
        XCTAssertEqual(EvidenceDisplay.sourceName(c), "Back Gate")
        c.fileName = "IMG.mov"
        XCTAssertEqual(EvidenceDisplay.fileExtension(c), "mov")
    }
}
