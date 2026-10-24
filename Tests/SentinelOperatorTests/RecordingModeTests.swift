import XCTest
@testable import SentinelCore

/// Wave 22 on the Mac: recording-mode fields on the camera (old and new
/// servers), the Recording defaults calls, the editor/list logic and the
/// timeline's low-res spans.
final class RecordingModeTests: XCTestCase {
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

    private func object(_ data: Data?) throws -> NSDictionary {
        try XCTUnwrap(JSONSerialization.jsonObject(with: XCTUnwrap(data)) as? NSDictionary)
    }

    private func camera(_ extra: String = "", recording: Bool = true, sub: String = "") throws -> V2.Camera {
        var json = Fixtures.camera
            .replacingOccurrences(of: #""recording":true"#, with: #""recording":\#(recording)"#)
            .replacingOccurrences(of: #""subRtspUrl":"""#, with: #""subRtspUrl":"\#(sub)""#)
        if extra.isEmpty == false {
            json = json.replacingOccurrences(of: #""recorderId":"local""#, with: #""recorderId":"local"\#(extra)"#)
        }
        return try SentinelAPI.decode(V2.Camera.self, from: Data(json.utf8))
    }

    // MARK: Decoding

    func testOldCameraJSONDecodesWithoutModeFields() throws {
        let c = try camera()
        XCTAssertNil(c.recordingMode)
        XCTAssertNil(c.effectiveRecordingMode)
        XCTAssertNil(c.recordTrigger)
        XCTAssertNil(c.preRollSeconds)
        XCTAssertNil(c.lowResRetentionDays)
        XCTAssertFalse(RecordingModeLogic.supportsModes(c))
        XCTAssertEqual(RecordingModeLogic.effective(c), .continuous)
        XCTAssertEqual(RecordingModeLogic.listLabel(c), "On")
        XCTAssertEqual(RecordingModeLogic.listLabel(try camera(recording: false)), "Off")
        XCTAssertEqual(RecordingModeLogic.form(of: try camera(recording: false)).mode, .off)
    }

    func testNewCameraJSONDecodesModeFields() throws {
        let c = try camera(#","recordingMode":"default","effectiveRecordingMode":"dual","recordTrigger":"objects","preRollSeconds":8,"postRollSeconds":30,"lowResRetentionDays":21"#,
                           sub: "rtsp://10.0.0.5/sub")
        XCTAssertEqual(c.recordingMode, .default)
        XCTAssertEqual(c.effectiveRecordingMode, .dual)
        XCTAssertEqual(c.recordTrigger, .objects)
        XCTAssertEqual(c.preRollSeconds, 8)
        XCTAssertEqual(c.postRollSeconds, 30)
        XCTAssertEqual(c.lowResRetentionDays, 21)
        XCTAssertTrue(RecordingModeLogic.supportsModes(c))
        XCTAssertEqual(RecordingModeLogic.listLabel(c), "24/7 low + events")
        let form = RecordingModeLogic.form(of: c)
        XCTAssertEqual(form, .init(mode: .default, trigger: .objects, preRollSeconds: 8, postRollSeconds: 30, lowResRetentionDays: 21))

        // Unknown future modes still decode (open enum).
        XCTAssertEqual(try camera(#","recordingMode":"schedule""#).recordingMode?.rawValue, "schedule")
    }

    func testListLabels() throws {
        XCTAssertEqual(RecordingModeLogic.listLabel(try camera(#","recordingMode":"continuous","effectiveRecordingMode":"continuous""#)), "24/7")
        var events = try camera(#","recordingMode":"events","effectiveRecordingMode":"events""#)
        XCTAssertEqual(RecordingModeLogic.listLabel(events), "Events")
        events.retentionDays = 3
        XCTAssertEqual(RecordingModeLogic.listLabel(events), "Events · 3 d")
        XCTAssertEqual(RecordingModeLogic.listLabel(try camera(#","recordingMode":"off","effectiveRecordingMode":"off""#, recording: false)), "Live only")
        // No effective mode yet: the camera's own concrete mode.
        XCTAssertEqual(RecordingModeLogic.listLabel(try camera(#","recordingMode":"events""#)), "Events")
    }

    func testSpanQualityDecodes() throws {
        let tl = try SentinelAPI.decode(V2.Timeline.self, from: Data("""
        {"from":0,"to":6000,"now":6000,"spans":[{"start":0,"end":10},{"start":10,"end":40,"quality":"low"},
         {"start":40,"end":5000,"quality":"full"},{"start":5000,"end":6000,"quality":"low"}],
         "pending":null,"writing":true,"events":null,"alarms":null}
        """.utf8))
        XCTAssertEqual(tl.spans.map(\.isLowRes), [false, true, false, true])
        XCTAssertNil(tl.spans[0].quality)
        XCTAssertEqual(RecordingModeLogic.lowResRanges(tl.spans).map { [$0.start, $0.end] }, [[10, 40], [5000, 6000]])
        XCTAssertTrue(RecordingModeLogic.hasLowRes(tl.spans, from: 30, to: 45))
        XCTAssertFalse(RecordingModeLogic.hasLowRes(tl.spans, from: 0, to: 10))
        XCTAssertFalse(RecordingModeLogic.hasLowRes(tl.spans, from: 40, to: 5000))
    }

    func testRecordingSettingsDecodeWithDefaultsForMissingFields() throws {
        let full = try SentinelAPI.decode(V2.RecordingSettings.self, from: Data("""
        {"defaultMode":"dual","defaultTrigger":"objects","defaultPreRollSeconds":10,"defaultPostRollSeconds":60,
         "defaultRetentionDays":30,"defaultLowResRetentionDays":60,"minFreePercent":15}
        """.utf8))
        XCTAssertEqual(full, V2.RecordingSettings(defaultMode: .dual, defaultTrigger: .objects, defaultPreRollSeconds: 10,
                                                  defaultPostRollSeconds: 60, defaultRetentionDays: 30,
                                                  defaultLowResRetentionDays: 60, minFreePercent: 15))
        let sparse = try SentinelAPI.decode(V2.RecordingSettings.self, from: Data(#"{"defaultMode":"events"}"#.utf8))
        XCTAssertEqual(sparse.defaultMode, .events)
        XCTAssertEqual(sparse.defaultTrigger, .motion)
        XCTAssertEqual(sparse.defaultPreRollSeconds, 5)
        XCTAssertEqual(sparse.defaultPostRollSeconds, 15)
        XCTAssertEqual(sparse.defaultRetentionDays, 7)
        XCTAssertEqual(sparse.defaultLowResRetentionDays, 14)
        XCTAssertEqual(sparse.minFreePercent, 0)
    }

    // MARK: Encoding

    func testPatchAndInputEncodeOnlyWhatIsSet() throws {
        let empty = try object(SentinelAPI.jsonEncoder.encode(V2.CameraPatch()))
        XCTAssertNil(empty["recordingMode"])
        XCTAssertNil(empty["recording"])

        let c = try camera(#","recordingMode":"default","effectiveRecordingMode":"continuous","recordTrigger":"default","preRollSeconds":0,"postRollSeconds":0,"lowResRetentionDays":0"#)
        var patch = V2.CameraPatch()
        RecordingModeLogic.merge(c, RecordingModeLogic.form(of: c), into: &patch)
        XCTAssertEqual(try object(SentinelAPI.jsonEncoder.encode(patch)), [:], "nothing changed")

        var form = RecordingModeLogic.form(of: c)
        form.mode = .events
        form.postRollSeconds = 30
        RecordingModeLogic.merge(c, form, into: &patch)
        XCTAssertEqual(try object(SentinelAPI.jsonEncoder.encode(patch)), ["recordingMode": "events", "postRollSeconds": 30])

        var input = V2.CameraInput(name: "Gate", rtspUrl: "rtsp://x")
        XCTAssertNil(try object(SentinelAPI.jsonEncoder.encode(input))["recordingMode"])
        RecordingModeLogic.apply(.init(mode: .dual, trigger: .objects, preRollSeconds: 3, postRollSeconds: 20, lowResRetentionDays: 30), to: &input)
        let body = try object(SentinelAPI.jsonEncoder.encode(input))
        XCTAssertEqual(body["recordingMode"] as? String, "dual")
        XCTAssertEqual(body["recordTrigger"] as? String, "objects")
        XCTAssertEqual(body["preRollSeconds"] as? Int, 3)
        XCTAssertEqual(body["postRollSeconds"] as? Int, 20)
        XCTAssertEqual(body["lowResRetentionDays"] as? Int, 30)
    }

    func testRollNullAndZeroAreDistinctOnPatch() throws {
        // Camera on "server default" (null) and one with a literal 0.
        let dflt = try camera(#","recordingMode":"default","effectiveRecordingMode":"events","recordTrigger":"default","preRollSeconds":null,"postRollSeconds":null,"lowResRetentionDays":0"#)
        XCTAssertNil(RecordingModeLogic.form(of: dflt).preRollSeconds)
        var form = RecordingModeLogic.form(of: dflt)
        var patch = V2.CameraPatch()
        RecordingModeLogic.merge(dflt, form, into: &patch)
        XCTAssertEqual(try object(SentinelAPI.jsonEncoder.encode(patch)), [:], "unchanged nil is omitted")
        form.preRollSeconds = 0
        RecordingModeLogic.merge(dflt, form, into: &patch)
        XCTAssertEqual(try object(SentinelAPI.jsonEncoder.encode(patch)) as NSDictionary, ["preRollSeconds": 0] as NSDictionary)

        let zero = try camera(#","recordingMode":"default","effectiveRecordingMode":"events","recordTrigger":"default","preRollSeconds":0,"postRollSeconds":0,"lowResRetentionDays":0"#)
        XCTAssertEqual(RecordingModeLogic.form(of: zero).postRollSeconds, 0)
        var back = RecordingModeLogic.form(of: zero)
        back.postRollSeconds = nil
        var p2 = V2.CameraPatch()
        RecordingModeLogic.merge(zero, back, into: &p2)
        let json = String(decoding: try SentinelAPI.jsonEncoder.encode(p2), as: UTF8.self)
        XCTAssertEqual(json, #"{"postRollSeconds":null}"#, "server default is an explicit null")
    }

    // MARK: Editor logic

    func testChoicesRecommendAndDisableDual() {
        let withSub = RecordingModeLogic.choices(hasSubStream: true, serverDefault: .continuous)
        XCTAssertEqual(withSub.map(\.mode), [.default, .continuous, .events, .dual, .off])
        XCTAssertEqual(withSub.filter(\.recommended).map(\.mode), [.dual])
        XCTAssertTrue(withSub.allSatisfy(\.enabled))
        XCTAssertEqual(withSub[0].label, "Server default (Continuous)")

        let noSub = RecordingModeLogic.choices(hasSubStream: false, serverDefault: .dual)
        XCTAssertEqual(noSub.filter(\.recommended).map(\.mode), [.continuous])
        XCTAssertEqual(noSub.first { $0.mode == .dual }?.enabled, false)
        XCTAssertEqual(noSub[0].label, "Server default (Events only)", "dual without a sub-stream falls back to events")
        XCTAssertEqual(RecordingModeLogic.choices(hasSubStream: false, serverDefault: nil)[0].label, "Server default")
        XCTAssertEqual(RecordingModeLogic.label(.dual), "Low-res 24/7 + full-res on events")
    }

    func testResolveStepsAndWarnings() {
        XCTAssertEqual(RecordingModeLogic.resolve(.default, serverDefault: nil, hasSubStream: true), .continuous)
        XCTAssertEqual(RecordingModeLogic.resolve(.default, serverDefault: .dual, hasSubStream: true), .dual)
        XCTAssertEqual(RecordingModeLogic.resolve(.dual, serverDefault: nil, hasSubStream: false), .events)
        XCTAssertEqual(RecordingModeLogic.resolve(.off, serverDefault: .dual, hasSubStream: true), .off)

        XCTAssertEqual(RecordingModeLogic.stepPreRoll(0, up: true), 1)
        XCTAssertEqual(RecordingModeLogic.stepPreRoll(30, up: true), 30)
        XCTAssertEqual(RecordingModeLogic.stepPreRoll(0, up: false), nil)
        XCTAssertEqual(RecordingModeLogic.stepPreRoll(nil, up: true), 0)
        XCTAssertEqual(RecordingModeLogic.stepPostRoll(0, up: true), 1)
        XCTAssertEqual(RecordingModeLogic.stepPostRoll(nil, up: true), 0)
        XCTAssertNil(RecordingModeLogic.stepPostRoll(0, up: false))
        XCTAssertEqual(RecordingModeLogic.stepPostRoll(5, up: true), 10)
        XCTAssertEqual(RecordingModeLogic.stepPostRoll(5, up: false), 4)
        XCTAssertEqual(RecordingModeLogic.stepPostRoll(120, up: true), 120)
        XCTAssertEqual(RecordingModeLogic.stepPostRoll(20, up: false), 15)

        XCTAssertEqual(RecordingModeLogic.secondsLabel(nil, serverDefault: 5), "server default (5 s)")
        XCTAssertEqual(RecordingModeLogic.secondsLabel(0, serverDefault: 5), "0 s")
        XCTAssertEqual(RecordingModeLogic.secondsLabel(12, serverDefault: 5), "12 s")
        XCTAssertEqual(RecordingModeLogic.daysLabel(0, serverDefault: 14), "server default (14 days)")
        XCTAssertEqual(RecordingModeLogic.daysLabel(1, serverDefault: nil), "1 day")

        XCTAssertNotNil(RecordingModeLogic.noTriggerWarning(effective: .events, motionLevel: 0, cameraAnalytics: false))
        XCTAssertNil(RecordingModeLogic.noTriggerWarning(effective: .events, motionLevel: 1, cameraAnalytics: false))
        XCTAssertNil(RecordingModeLogic.noTriggerWarning(effective: .dual, motionLevel: 0, cameraAnalytics: true))
        XCTAssertNil(RecordingModeLogic.noTriggerWarning(effective: .continuous, motionLevel: 0, cameraAnalytics: false))

        XCTAssertEqual(RecordingModeLogic.clampMinFree(0), 0)
        XCTAssertEqual(RecordingModeLogic.clampMinFree(2), 5)
        XCTAssertEqual(RecordingModeLogic.clampMinFree(80), 50)
    }

    func testDefaultsPatchHasOnlyChanges() {
        let old = V2.RecordingSettings()
        XCTAssertTrue(RecordingModeLogic.patch(from: old, to: old).isEmpty)
        var new = old
        new.defaultMode = .dual
        new.minFreePercent = 12
        let p = RecordingModeLogic.patch(from: old, to: new)
        XCTAssertEqual(p.defaultMode, .dual)
        XCTAssertEqual(p.minFreePercent, 12)
        XCTAssertNil(p.defaultTrigger)
        XCTAssertNil(p.defaultRetentionDays)
    }

    // MARK: Calls

    func testRecordingSettingsCallsAndOlderServer404() async throws {
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("GET", "/api/v2/settings/recording"):
                return .json(200, #"{"defaultMode":"continuous","defaultTrigger":"motion","defaultPreRollSeconds":5,"defaultPostRollSeconds":15,"defaultRetentionDays":7,"defaultLowResRetentionDays":14,"minFreePercent":0}"#)
            case ("PATCH", "/api/v2/settings/recording"):
                return .json(200, #"{"defaultMode":"dual","defaultTrigger":"motion","defaultPreRollSeconds":5,"defaultPostRollSeconds":15,"defaultRetentionDays":7,"defaultLowResRetentionDays":14,"minFreePercent":10}"#)
            case ("PATCH", "/api/v2/cameras/c1"):
                return .json(400, #"{"error":{"code":"bad_request","message":"Low-res 24/7 needs a sub-stream RTSP URL."}}"#)
            default:
                return .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#)
            }
        }
        let api = makeAPI()
        let s = try await api.recordingSettings()
        XCTAssertEqual(s.defaultMode, .continuous)
        var patch = V2.RecordingSettingsPatch()
        patch.defaultMode = .dual
        patch.minFreePercent = 10
        let saved = try await api.updateRecordingSettings(patch)
        XCTAssertEqual(saved.minFreePercent, 10)
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["defaultMode": "dual", "minFreePercent": 10])

        var cam = V2.CameraPatch()
        cam.recordingMode = .dual
        do {
            _ = try await api.updateCamera("c1", cam)
            XCTFail("expected 400")
        } catch {
            XCTAssertEqual((error as? LocalizedError)?.errorDescription, "Low-res 24/7 needs a sub-stream RTSP URL.")
        }

        // An older server: 404, worded for the pane.
        MockURLProtocol.handler = { _, _ in .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#) }
        do {
            _ = try await api.recordingSettings()
            XCTFail("expected 404")
        } catch {
            XCTAssertTrue((error as? SentinelAPIError)?.isNotFound == true)
            XCTAssertTrue(RecordingModeLogic.errorText(error).contains("doesn't offer recording modes"))
        }
    }

    func testRecordingLeafInManagementTree() {
        let admin = ManagementNavModel(can: { $0 == .changeSettings })
        XCTAssertEqual(admin.node(ManagementCatalog.Leaf.recording)?.title, "Recording")
        XCTAssertEqual(admin.breadcrumb(for: ManagementCatalog.Leaf.recording, siteName: "HQ"), ["System Dashboard", "Recording"])
        XCTAssertNil(ManagementNavModel(can: { $0 == .viewAudit }).node(ManagementCatalog.Leaf.recording))
    }
}
