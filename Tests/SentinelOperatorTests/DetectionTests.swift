import XCTest
@testable import SentinelCore

/// Wave 17 on the Mac: the wave 16 models and calls (detector, models, faces,
/// people, plates, semantic search), the People/Plates/Detection pane logic,
/// the search workspace's plate/person/track filters and "Describe it", event
/// chips, the Watchlist kind and the camera's loitering field.
final class DetectionTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!
    private let now = Date(unixMillis: 1_760_000_000_000)

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

    private func event(_ extra: String = "") throws -> V2.Event {
        try SentinelAPI.decode(V2.Event.self, from: Data("""
        {"id":"e1","cameraId":"c1","kind":"Vehicle","score":0.9,"createdAt":1,"hasThumbnail":true\(extra)}
        """.utf8))
    }

    private func model(_ id: String, purpose: String = "detector", state: String = "installed", bundled: Bool = false,
                       source: String? = "https://example.test/m.onnx", download: String = "") throws -> V2.Model {
        let src = source.map { ",\"source\":\"\($0)\"" } ?? ""
        return try SentinelAPI.decode(V2.Model.self, from: Data("""
        {"id":"\(id)","title":"\(id.uppercased())","purpose":"\(purpose)","file":"\(id).onnx","sizeBytes":1000,
         "license":"Apache-2.0","bundled":\(bundled),"installed":\(state == "installed"),"state":"\(state)"\(src)\(download)}
        """.utf8))
    }

    // MARK: Models (decoding)

    func testEventDecodesWave16FieldsAndOlderEventsStillDecode() throws {
        let e = try event(#","trackId":"t9","personId":"p1","personName":"Ana","plate":"AB123""#)
        XCTAssertEqual(e.trackId, "t9")
        XCTAssertEqual(e.personId, "p1")
        XCTAssertEqual(e.personName, "Ana")
        XCTAssertEqual(e.plate, "AB123")
        let old = try event()
        XCTAssertNil(old.trackId)
        XCTAssertNil(old.plate)
    }

    func testCameraLoiterSecondsDecodesAndPatchEncodesOnlyWhenSet() throws {
        var patch = V2.CameraPatch()
        XCTAssertNil(try object(SentinelAPI.jsonEncoder.encode(patch))["loiterSeconds"])
        patch.loiterSeconds = 45
        XCTAssertEqual(try object(SentinelAPI.jsonEncoder.encode(patch))["loiterSeconds"] as? Int, 45)
        let input = V2.CameraInput(name: "Gate", rtspUrl: "rtsp://x", loiterSeconds: 30)
        XCTAssertEqual(try object(SentinelAPI.jsonEncoder.encode(input))["loiterSeconds"] as? Int, 30)
    }

    func testDetectorAndCatalogDecode() throws {
        let det = try SentinelAPI.decode(V2.DetectorSettings.self, from: Data("""
        {"model":"","rate":0,"status":{"model":"yolox-tiny","engine":"CoreML","device":"ANE","effectiveRate":5,"pending":false,"inferMs":12.34}}
        """.utf8))
        XCTAssertEqual(DetectionLogic.savedModel(det), "yolox-tiny")
        XCTAssertEqual(det.status.inferMs ?? 0, 12.34, accuracy: 0.001)
        let catalog = try SentinelAPI.decode(V2.ModelCatalog.self, from: Data("""
        {"items":[{"id":"yolox-s","title":"YOLOX-S","purpose":"detector","file":"yolox_s.onnx","sizeBytes":36000000,
          "license":"Apache-2.0","bundled":false,"installed":false,"state":"downloading",
          "download":{"modelId":"yolox-s","taskId":"t1","state":"running","done":500,"total":1000}},
         {"id":"future","title":"Future","purpose":"hologram","file":"f","sizeBytes":1,"license":"MIT","bundled":false,
          "installed":false,"state":"quantum"}],"dir":"/var/lib/sentinel/models"}
        """.utf8))
        XCTAssertEqual(catalog.dir, "/var/lib/sentinel/models")
        XCTAssertEqual(catalog.items[0].download?.done, 500)
        XCTAssertEqual(DetectionLogic.downloadFraction(catalog.items[0]), 0.5)
        // Unknown purposes and states don't fail the list.
        XCTAssertEqual(catalog.items[1].purpose.rawValue, "hologram")
        XCTAssertEqual(DetectionLogic.purposeLabel(catalog.items[1].purpose), "hologram")
        XCTAssertEqual(DetectionLogic.stateLabel(catalog.items[1]), "Quantum")
    }

    func testPersonPlateAndFacesStatusDecode() throws {
        let person = try SentinelAPI.decode(V2.Person.self, from: Data("""
        {"id":"p1","name":"Ana","alert":"always","faces":null,"createdAt":5}
        """.utf8))
        XCTAssertEqual(person.alert, .always)
        XCTAssertTrue(person.faces.isEmpty)
        let plate = try SentinelAPI.decode(V2.Plate.self, from: Data("""
        {"id":"pl1","plate":"AB123","alert":"never","createdAt":5,
         "sightings":[{"id":"e1","cameraId":"c1","kind":"Vehicle","score":0.8,"createdAt":9,"hasThumbnail":true,"plate":"AB123"}]}
        """.utf8))
        XCTAssertEqual(plate.sightings?.first?.plate, "AB123")
        let faces = try SentinelAPI.decode(V2.FacesStatus.self, from: Data("""
        {"enabled":false,"available":false,"reason":"face-detect model missing","people":0,"unassigned":0,"unassignedDays":7}
        """.utf8))
        XCTAssertFalse(PeopleLogic.canToggle(faces))
    }

    // MARK: Calls

    func testDetectionCallsUsePathsMethodsAndBodies() async throws {
        MockURLProtocol.handler = { request, _ in
            switch (request.httpMethod ?? "", request.url?.path ?? "") {
            case ("PUT", "/api/v2/detector"):
                return .json(200, #"{"model":"yolox-s","rate":2,"status":{"model":"yolox-tiny","engine":"CPU","effectiveRate":2,"pending":true}}"#)
            case ("POST", "/api/v2/models/yolox-s/download"), ("DELETE", "/api/v2/models/yolox-s/download"):
                return .json(201, #"{"modelId":"yolox-s","taskId":"t1","state":"running","done":0,"total":0}"#)
            case ("DELETE", _):
                return .json(204, "")
            case ("PUT", "/api/v2/faces"):
                return .json(200, #"{"enabled":true,"available":true,"people":1,"unassigned":2,"unassignedDays":14}"#)
            case ("PUT", "/api/v2/lpr"):
                return .json(200, #"{"enabled":true,"available":true,"plates":3}"#)
            case ("GET", "/api/v2/faces/unassigned"):
                return .json(200, #"{"items":[{"id":"f1","eventId":"e1","cameraId":"c1","at":1,"score":0.9}]}"#)
            case ("POST", "/api/v2/people"), ("PATCH", "/api/v2/people/p1"), ("POST", "/api/v2/people/p1/faces"):
                return .json(200, #"{"id":"p1","name":"Ana","alert":"default","faces":[],"createdAt":1}"#)
            case ("POST", "/api/v2/plates"), ("PATCH", "/api/v2/plates/pl1"):
                return .json(200, #"{"id":"pl1","plate":"AB123","alert":"default","createdAt":1}"#)
            case ("GET", "/api/v2/faces/f1/crop.jpg"):
                return MockURLProtocol.Reply(status: 200, body: Data([0xFF, 0xD8]))
            case ("POST", "/api/v2/events/semantic"):
                return .json(200, #"{"items":[{"event":{"id":"e1","cameraId":"c1","kind":"Vehicle","score":0.9,"createdAt":1,"hasThumbnail":true},"score":0.31}],"model":"clip"}"#)
            default:
                return .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#)
            }
        }
        let api = makeAPI()
        let det = try await api.updateDetector(.init(model: "yolox-s", rate: 2))
        XCTAssertEqual(DetectionLogic.pendingNote(det), "Restart Sentinel to apply the model change. The rate has already changed.")
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["model": "yolox-s", "rate": 2])

        _ = try await api.downloadModel("yolox-s")
        _ = try await api.cancelModelDownload("yolox-s")
        try await api.deleteModel("yolox-s")
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.path, "/api/v2/models/yolox-s")

        let faces = try await api.updateFaces(.init(unassignedDays: 14))
        XCTAssertEqual(faces.unassignedDays, 14)
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["unassignedDays": 14])

        let lpr = try await api.setLPR(enabled: true)
        XCTAssertTrue(lpr.enabled)
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["enabled": true])

        let unassigned = try await api.unassignedFaces(limit: 60)
        XCTAssertEqual(unassigned.map(\.id), ["f1"])
        XCTAssertEqual(MockURLProtocol.requests.last?.request.url?.query, "limit=60")

        _ = try await api.createPerson(PeopleLogic.input(.init(name: " Ana ", notes: "", alert: .default), faceId: "f1"))
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["name": "Ana", "notes": "", "alert": "default", "faceId": "f1"])
        _ = try await api.updatePerson("p1", .init(alert: .never))
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["alert": "never"])
        _ = try await api.enrollFace(personID: "p1", faceID: "f2")
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body), ["faceId": "f2"])
        try await api.deletePerson("p1")
        try await api.deleteFace("f1")

        _ = try await api.createPlate(PlateLogic.input(.init(plate: "ab-123", name: "Van")))
        XCTAssertEqual(try object(MockURLProtocol.requests.last?.body)["plate"] as? String, "AB123")
        _ = try await api.updatePlate("pl1", .init(name: "Truck"))
        XCTAssertEqual(MockURLProtocol.requests.last?.request.httpMethod, "PATCH")
        try await api.deletePlate("pl1")

        let crop = try await api.faceCrop("f1")
        XCTAssertEqual(crop, Data([0xFF, 0xD8]))
        XCTAssertEqual(MockURLProtocol.requests.last?.request.value(forHTTPHeaderField: "Authorization"), "Bearer access-1")

        var filter = SearchFilter()
        filter.cameras = ["c1"]
        let res = try await api.semanticSearch(SemanticLogic.request("  white van ", filter: filter, now: now))
        XCTAssertEqual(res.items.first?.score, 0.31)
        let body = try object(MockURLProtocol.requests.last?.body)
        XCTAssertEqual(body["text"] as? String, "white van")
        XCTAssertEqual(body["cameras"] as? [String], ["c1"])
        XCTAssertEqual(body["from"] as? Int64, 1_759_913_600_000)
        XCTAssertNil(body["to"])
        XCTAssertEqual(body["limit"] as? Int, 30)
    }

    func testOlderServer404SaysTheServerNeedsUpdating() async {
        MockURLProtocol.handler = { _, _ in .json(404, #"{"error":{"code":"not_found","message":"Not found."}}"#) }
        do {
            _ = try await makeAPI().facesStatus()
            XCTFail("expected 404")
        } catch {
            XCTAssertEqual(DetectionText.errorText(error, feature: "face recognition"),
                           "This server doesn't offer face recognition. Update Sentinel on the Management Server to use it.")
        }
        let conflict = SentinelAPIError.server(status: 409, code: "conflict", message: "plate-rec model is not installed")
        XCTAssertEqual(DetectionText.errorText(conflict, feature: "plate reading"), "plate-rec model is not installed")
    }

    // MARK: People

    func testPeopleStatusLineSortAndForm() {
        XCTAssertEqual(PeopleLogic.statusLine(.init(available: false, reason: "face-embed model missing")),
                       "Unavailable: face-embed model missing. Download \"Faces (find)\" and \"Faces (recognize)\" under System → Models.")
        XCTAssertEqual(PeopleLogic.statusLine(.init(enabled: false, available: true)), "Off. Turn it on to recognize the people you add here.")
        XCTAssertEqual(PeopleLogic.statusLine(.init(enabled: true, available: true, people: 1, unassigned: 3)),
                       "On · 1 person · 3 unassigned faces")
        XCTAssertTrue(PeopleLogic.canToggle(.init(enabled: true, available: false)))
        XCTAssertEqual(PeopleLogic.clampDays(0), 1)
        XCTAssertEqual(PeopleLogic.clampDays(200), 90)
        XCTAssertEqual(PeopleLogic.clampDays(.nan), 7)
        XCTAssertEqual(PeopleLogic.sorted([.init(id: "1", name: "bob"), .init(id: "2", name: "Ana"), .init(id: "3", name: "carl")]).map(\.name),
                       ["Ana", "bob", "carl"])
        XCTAssertEqual(PeopleLogic.alertLabel(nil), "Name them in the alarm")
        XCTAssertEqual(PeopleLogic.alertLabel(.always), "Critical watchlist alarm")

        XCTAssertEqual(PeopleLogic.problem(.init(name: "  ")), "Enter a name.")
        XCTAssertEqual(PeopleLogic.problem(.init(name: String(repeating: "x", count: 81))), "Use 80 characters or fewer for the name.")
        XCTAssertEqual(PeopleLogic.problem(.init(name: "A", notes: String(repeating: "x", count: 501))), "Use 500 characters or fewer for notes.")
        XCTAssertNil(PeopleLogic.problem(.init(name: "Ana")))

        let ana = V2.Person(id: "p1", name: "Ana", notes: nil, alert: .default)
        XCTAssertTrue(PeopleLogic.patch(ana, PeopleLogic.Form(ana)).isEmpty)
        let patch = PeopleLogic.patch(ana, .init(name: "Ana B ", notes: "", alert: .never))
        XCTAssertEqual(patch, V2.PersonInput(name: "Ana B", alert: .never))
    }

    func testAgoAndLastSeen() {
        let t = now.unixMillis
        XCTAssertEqual(DetectionText.ago(nil, now: now), "Never")
        XCTAssertEqual(DetectionText.ago(t - 30_000, now: now), "Just now")
        XCTAssertEqual(DetectionText.ago(t - 5 * 60_000, now: now), "5 min ago")
        XCTAssertEqual(DetectionText.ago(t - 3 * 3_600_000, now: now), "3 h ago")
        XCTAssertEqual(DetectionText.ago(t - 86_400_000, now: now), "1 day ago")
        XCTAssertEqual(DetectionText.ago(t - 2 * 86_400_000, now: now), "2 days ago")
        XCTAssertFalse(DetectionText.ago(t - 30 * 86_400_000, now: now).hasSuffix("ago"))
        let names = ["c1": "Front door"]
        XCTAssertEqual(DetectionText.lastSeen(t - 3 * 3_600_000, cameraId: "c1", cameraName: { names[$0] }, now: now), "3 h ago · Front door")
        XCTAssertEqual(DetectionText.lastSeen(t - 3 * 3_600_000, cameraId: "gone", cameraName: { names[$0] }, now: now), "3 h ago")
        XCTAssertEqual(DetectionText.lastSeen(nil, cameraId: "c1", cameraName: { names[$0] }, now: now), "Never")
    }

    // MARK: Plates

    func testPlateNormalizeValidateAndPatch() {
        XCTAssertEqual(PlateLogic.normalize(" ab-12 3é"), "AB123")
        let existing = [V2.Plate(id: "1", plate: "AB123")]
        XCTAssertEqual(PlateLogic.problem(.init(plate: "a"), existing: existing), "Enter the plate: at least 2 letters or digits.")
        XCTAssertEqual(PlateLogic.problem(.init(plate: "ABCDEFGHIJK")), "A plate has at most 10 letters and digits.")
        XCTAssertEqual(PlateLogic.problem(.init(plate: "ab 123"), existing: existing), "AB123 is already on the watchlist.")
        XCTAssertNil(PlateLogic.problem(.init(plate: "AB123"), existing: existing, editingID: "1"))
        XCTAssertEqual(PlateLogic.plateHint(.init(plate: "ab 1"), editing: false), "Saved as AB1 (letters and digits only).")
        XCTAssertEqual(PlateLogic.plateHint(.init(plate: "AB1"), editing: false), "Letters and digits; spaces and dashes are ignored.")
        XCTAssertEqual(PlateLogic.plateHint(.init(plate: "AB1"), editing: true), "To change the plate, remove it and add the new one.")

        let p = V2.Plate(id: "1", plate: "AB123", name: "Van", alert: .default)
        XCTAssertTrue(PlateLogic.patch(p, PlateLogic.Form(p)).isEmpty)
        // The plate itself never goes into a PATCH.
        XCTAssertEqual(PlateLogic.patch(p, .init(plate: "ZZ9", name: "Van", notes: " gate ", alert: .always)),
                       V2.PlateInput(notes: "gate", alert: .always))
    }

    func testPlateStatusSortFilterAndTitle() {
        XCTAssertEqual(PlateLogic.statusLine(.init(available: false)),
                       "Unavailable: the plate models are not installed. Download \"Plates (find)\" and \"Plates (read)\" under System → Models.")
        XCTAssertEqual(PlateLogic.statusLine(.init(enabled: false, available: true)), "Off. Turn it on to read plates on vehicle events.")
        XCTAssertEqual(PlateLogic.statusLine(.init(enabled: true, available: true, plates: 4, readLast24h: 1)),
                       "On · 4 on the watchlist · 1 plate read in the last 24 h")
        let list = [V2.Plate(id: "2", plate: "ZX1", name: "Delivery van"), V2.Plate(id: "1", plate: "AB123")]
        XCTAssertEqual(PlateLogic.sorted(list).map(\.plate), ["AB123", "ZX1"])
        XCTAssertEqual(PlateLogic.filter(list, "b-12").map(\.id), ["1"])
        XCTAssertEqual(PlateLogic.filter(list, "delivery").map(\.id), ["2"])
        XCTAssertEqual(PlateLogic.filter(list, " ").count, 2)
        XCTAssertEqual(PlateLogic.title(list[0]), "ZX1 · Delivery van")
        XCTAssertEqual(PlateLogic.title(list[1]), "AB123")
    }

    // MARK: Detection pane

    func testDetectorChoicesStatusAndRates() throws {
        let models = [try model("yolox-tiny", bundled: true), try model("yolox-s", state: "missing"), try model("yolox-m"),
                      try model("osnet", purpose: "reid")]
        XCTAssertEqual(DetectionLogic.detectorChoices(models, saved: "yolox-tiny").map(\.id), ["yolox-tiny", "yolox-m"])
        // A saved model whose file is gone is still listed, marked not installed.
        let missing = DetectionLogic.detectorChoices(models, saved: "yolox-s")
        XCTAssertEqual(missing.map(\.id), ["yolox-tiny", "yolox-s", "yolox-m"])
        XCTAssertEqual(missing[1].installed, false)
        XCTAssertEqual(DetectionLogic.detectorChoices(models, saved: "gone").first?.id, "gone")

        XCTAssertEqual(DetectionLogic.rateOptions.map(\.value), [0, 1, 2, 3, 4, 5])
        XCTAssertEqual(DetectionLogic.rateOptions[0].label, "Auto (1/s on the CPU, 5/s with an accelerator)")

        let s = V2.DetectorSettings(model: "", rate: 0, status: .init(model: "yolox-tiny", engine: "CoreML", device: "ANE",
                                                                      effectiveRate: 5, inferMs: 12.34))
        XCTAssertEqual(DetectionLogic.statusLine(s, models: models), "YOLOX-TINY · CoreML (ANE) · 5/s per camera · 12.3 ms per frame")
        XCTAssertNil(DetectionLogic.pendingNote(s))
        var failing = s
        failing.status.reason = "yolox-s is missing"
        XCTAssertEqual(DetectionLogic.pendingNote(failing), "The chosen model is not running: yolox-s is missing.")
    }

    func testModelActionsStatesAndOrder() throws {
        let bundled = try model("yolox-tiny", bundled: true)
        let selected = try model("yolox-m")
        let other = try model("yolox-l")
        let missing = try model("clip-text", purpose: "clip-text", state: "missing")
        let noSource = try model("x", purpose: "audio", state: "missing", source: nil)
        let corrupt = try model("plate", purpose: "plate-rec", state: "corrupt")
        let downloading = try model("face", purpose: "face-detect", state: "downloading",
                                    download: #","download":{"modelId":"face","taskId":"t","state":"running","done":1,"total":0}"#)
        let failed = try model("reid", purpose: "reid", state: "missing",
                               download: #","download":{"modelId":"reid","taskId":"t","state":"failed","done":0,"total":0,"error":"checksum"}"#)

        XCTAssertEqual(DetectionLogic.actions(bundled, selected: "yolox-m", canChange: true), [])
        XCTAssertEqual(DetectionLogic.actions(selected, selected: "yolox-m", canChange: true), [])
        XCTAssertEqual(DetectionLogic.actions(other, selected: "yolox-m", canChange: true), [.delete])
        XCTAssertEqual(DetectionLogic.actions(other, selected: "yolox-m", canChange: false), [])
        XCTAssertEqual(DetectionLogic.actions(missing, selected: "yolox-m", canChange: true), [.download])
        XCTAssertEqual(DetectionLogic.actions(noSource, selected: "yolox-m", canChange: true), [])
        XCTAssertEqual(DetectionLogic.actions(corrupt, selected: "yolox-m", canChange: true), [.download, .delete])
        XCTAssertEqual(DetectionLogic.actions(downloading, selected: "yolox-m", canChange: true), [.cancel])

        XCTAssertEqual(DetectionLogic.stateLabel(bundled), "Bundled")
        XCTAssertEqual(DetectionLogic.stateLabel(other), "Installed")
        XCTAssertEqual(DetectionLogic.stateLabel(missing), "Not installed")
        XCTAssertEqual(DetectionLogic.stateLabel(corrupt), "Damaged")
        XCTAssertEqual(DetectionLogic.stateLabel(failed), "Download failed")
        XCTAssertEqual(DetectionLogic.stateLabel(downloading), "Downloading")
        XCTAssertNil(DetectionLogic.downloadFraction(downloading)) // total unknown: "Starting…"

        XCTAssertTrue(DetectionLogic.anyDownloading([other, downloading]))
        XCTAssertEqual(DetectionLogic.pollInterval([other, downloading]), 1.5)
        XCTAssertEqual(DetectionLogic.pollInterval([other]), 15)

        let order = DetectionLogic.sorted([missing, corrupt, failed, other, bundled]).map(\.id)
        XCTAssertEqual(order, ["yolox-l", "yolox-tiny", "reid", "plate", "clip-text"])
        XCTAssertEqual(DetectionLogic.handInstallNote(dir: "/m"),
                       "To install by hand, put the model file in /m on the management server. It counts once its SHA-256 matches.")
        XCTAssertNil(DetectionLogic.handInstallNote(dir: ""))
    }

    // MARK: Semantic search

    func testSemanticAvailabilityTextScoresAndErrors() {
        XCTAssertFalse(SemanticLogic.availability(nil).enabled)
        let off = SemanticLogic.availability(.init(available: false, reason: "clip-text model missing"))
        XCTAssertFalse(off.enabled)
        XCTAssertEqual(off.help, "Semantic search is unavailable: clip-text model missing. Download the \"Semantic search\" models under System → Models.")
        let on = SemanticLogic.availability(.init(available: true, indexed: 1))
        XCTAssertTrue(on.enabled)
        XCTAssertEqual(on.help, "Searches snapshots by what they show, on this server. 1 event indexed.")

        XCTAssertEqual(SemanticLogic.textProblem(" a "), "Describe what you're looking for in a few words.")
        XCTAssertEqual(SemanticLogic.textProblem(String(repeating: "x", count: 201)), "Keep the description under 200 characters.")
        XCTAssertNil(SemanticLogic.textProblem("red car"))

        XCTAssertEqual(SemanticLogic.scoreBars([0.3, 0.15, -0.1, .nan]), [1, 0.5, 0, 0])
        XCTAssertEqual(SemanticLogic.scoreBars([]), [])
        XCTAssertEqual(SemanticLogic.scoreText(0.314), "0.31")
        XCTAssertEqual(SemanticLogic.scoreText(.infinity), "—")

        let unavailable = SemanticLogic.failure(SentinelAPIError.server(status: 503, code: "unavailable",
                                                                         message: "semantic search is unavailable: clip-text model missing"))
        XCTAssertEqual(unavailable.text, "Semantic search is unavailable: clip-text model missing")
        XCTAssertTrue(unavailable.unavailable)
        let bad = SemanticLogic.failure(SentinelAPIError.server(status: 400, code: "bad_request", message: "text is too short"))
        XCTAssertEqual(bad.text, "text is too short")
        XCTAssertFalse(bad.unavailable)

        var f = SearchFilter()
        f.range = .custom
        f.from = 1000
        f.to = 2000
        let req = SemanticLogic.request(String(repeating: "y", count: 250), filter: f, now: now, limit: 500)
        XCTAssertEqual(req.text.count, 200)
        XCTAssertEqual(req.from, 1000)
        XCTAssertEqual(req.to, 2000)
        XCTAssertNil(req.cameras)
        XCTAssertEqual(req.limit, 100)
    }

    @MainActor
    func testSearchModelDescribeShowsHitsAndFallsBackWhenUnavailable() async throws {
        let hit = V2.SemanticHit(event: try event(#","trackId":"t1""#), score: 0.3)
        final class Box: @unchecked Sendable { var available = true; var requests: [V2.SemanticRequest] = [] }
        let box = Box()
        let backend = SearchModel.Backend(
            search: { _, _ in V2.Page(items: []) },
            facets: { _ in V2.EventFacets() },
            ask: { _ in throw SentinelAPIError.server(status: 409, code: "conflict", message: "AI is off") },
            semanticStatus: { V2.SemanticStatus(available: box.available, indexed: 10) },
            semanticSearch: { request in
                box.requests.append(request)
                if box.available == false {
                    throw SentinelAPIError.server(status: 503, code: "unavailable", message: "semantic search is unavailable: model missing")
                }
                return V2.SemanticResults(items: [hit], model: "clip")
            })
        let model = SearchModel(backend: backend)
        await model.loadSemanticStatus()
        XCTAssertTrue(model.semanticAvailability.enabled)

        model.describeText = "x"
        await model.describe(now: now)
        XCTAssertEqual(model.semanticError, "Describe what you're looking for in a few words.")
        XCTAssertTrue(box.requests.isEmpty)

        model.describeText = "  white van  "
        model.filter.cameras = ["c1"]
        await model.describe(now: now)
        XCTAssertEqual(model.semanticHits?.count, 1)
        XCTAssertEqual(model.semanticQuery, "white van")
        XCTAssertEqual(model.semanticBars, [1])
        XCTAssertEqual(box.requests.last?.cameras, ["c1"])
        XCTAssertNil(model.semanticError)

        // "Same object" leaves the semantic results for a track search over any time.
        model.showSameObject(model.semanticHits![0].event)
        XCTAssertNil(model.semanticHits)
        XCTAssertEqual(model.filter.track, "t1")
        XCTAssertEqual(model.filter.range, .any)
        XCTAssertEqual(model.filter.cameras, [])

        // 503: the server's reason shows and the box disables.
        box.available = false
        await model.describe(now: now)
        XCTAssertEqual(model.semanticError, "Semantic search is unavailable: model missing")
        XCTAssertFalse(model.semanticAvailability.enabled)
        XCTAssertNil(model.semanticHits)
    }

    @MainActor
    func testSearchModelTreatsMissingSemanticRouteAsUnavailable() async {
        let model = SearchModel(backend: .init(search: { _, _ in V2.Page(items: []) }, facets: { _ in V2.EventFacets() },
                                               ask: { _ in throw SentinelAPIError.transport(message: "x") }))
        await model.loadSemanticStatus()
        XCTAssertEqual(model.semanticStatus?.available, false)
        XCTAssertEqual(model.semanticStatus?.reason, "this server does not offer semantic search")
        XCTAssertFalse(model.semanticAvailability.enabled)
    }

    // MARK: Search filter

    func testPlatePersonAndTrackFilters() {
        var f = SearchFilter()
        f.plate = " ab-12 "
        f.person = "p1"
        f.track = "t1"
        let q = Dictionary(uniqueKeysWithValues: f.queryItems(now: now).map { ($0.name, $0.value ?? "") })
        XCTAssertEqual(q["plate"], "AB12")
        XCTAssertEqual(q["person"], "p1")
        XCTAssertEqual(q["track"], "t1")
        XCTAssertEqual(f.activeCount, 3)
        XCTAssertEqual(f.cleared.plate, "")
        XCTAssertEqual(f.cleared.track, "")
        f.plate = "--"
        XCTAssertNil(f.queryItems(now: now).first { $0.name == "plate" })
        XCTAssertEqual(f.activeCount, 2)

        let understood = SearchFilter.from(V2.EventSearchFilter(plate: "AB1", person: "p2", track: "t3"))
        XCTAssertEqual(understood.plate, "AB1")
        XCTAssertEqual(understood.person, "p2")
        XCTAssertEqual(understood.track, "t3")
        let same = SearchFilter.sameObject(track: "t9")
        XCTAssertEqual(same.range, .any)
        XCTAssertEqual(same.activeCount, 1)
    }

    // MARK: Chips, kinds, loitering

    func testEventChipsWatchlistKindAndRuleKinds() throws {
        XCTAssertEqual(EventChip.chips(for: try event(#","personName":" Ana ","plate":"AB123""#)),
                       [EventChip(kind: .person, text: "Ana"), EventChip(kind: .plate, text: "AB123")])
        XCTAssertEqual(EventChip.chips(for: try event(#","personName":"  ""#)), [])
        XCTAssertEqual(AlarmKindStyle.symbol("Watchlist"), "flag.fill")
        XCTAssertNil(AlarmKindStyle.symbol("Person"))
        XCTAssertTrue(RuleCatalog.kinds.contains("Watchlist"))
    }

    func testLoiterSettings() throws {
        XCTAssertEqual(LoiterSettings.clamp(30), 30)
        XCTAssertEqual(LoiterSettings.clamp(-5), 0)
        XCTAssertEqual(LoiterSettings.clamp(.nan), 0)
        XCTAssertEqual(LoiterSettings.clamp(9000), 3600)
        XCTAssertEqual(LoiterSettings.clamp(12.4), 12)
        XCTAssertEqual(LoiterSettings.seconds(of: nil), 0)
        XCTAssertEqual(LoiterSettings.hint(0), "Off. Set a number of seconds to raise a Loitering event when a person stays that long.")
        XCTAssertEqual(LoiterSettings.hint(90),
                       "A person seen for 1 min 30 s or more raises one Loitering event (a Warning alarm). Needs object detection on this camera.")
        XCTAssertTrue(LoiterSettings.hint(45).contains("45 s or more"))
        XCTAssertTrue(LoiterSettings.hint(120).contains("2 min or more"))

        var patch = V2.CameraPatch()
        LoiterSettings.merge(nil, 0, into: &patch)
        XCTAssertNil(patch.loiterSeconds)
        LoiterSettings.merge(nil, 60, into: &patch)
        XCTAssertEqual(patch.loiterSeconds, 60)
    }
}
