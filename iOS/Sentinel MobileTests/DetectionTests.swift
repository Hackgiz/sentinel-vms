import XCTest
@testable import Sentinel_Mobile

/// Detection depth on the phone (WAVE17-CLIENT-PARITY.md, iPhone): the new
/// event fields, people and semantic decoding, chips, "Describe it"
/// availability and errors, and the API calls. No network: `MockURLProtocol`
/// answers everything.
final class DetectionTests: XCTestCase {
    private let site = URL(string: "http://10.0.0.1:8090")!

    private func api() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(TokenPair(accessToken: "sat", expiresAt: Date().addingTimeInterval(800).unixMillis,
                             refreshToken: "srt", refreshExpiresAt: Date().addingTimeInterval(86400).unixMillis, userName: "op"),
                   siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: mockSession())
    }

    private func event(personName: String? = nil, plate: String? = nil) -> V2.Event {
        V2.Event(id: "e1", cameraId: "cam-1", kind: "Person", label: nil, score: 0.9, box: nil, createdAt: 1,
                 hasThumbnail: false, alarmId: nil, description: nil, threat: nil, anomaly: nil, reason: nil, tags: nil,
                 personName: personName, plate: plate)
    }

    private func hit(_ score: Double) -> V2.SemanticHit {
        V2.SemanticHit(event: event(), score: score)
    }

    // MARK: - Decoding

    func testEventDecodesTheWave16FieldsAndStillDecodesWithoutThem() throws {
        let full = #"{"id":"e1","cameraId":"c","kind":"Vehicle","score":0.8,"createdAt":5,"hasThumbnail":true,"trackId":"t9","personId":"p1","personName":"Sam","plate":"7XKA492"}"#
        let e = try SentinelAPI.decode(V2.Event.self, from: Data(full.utf8))
        XCTAssertEqual(e.trackId, "t9")
        XCTAssertEqual(e.personId, "p1")
        XCTAssertEqual(e.personName, "Sam")
        XCTAssertEqual(e.plate, "7XKA492")

        let old = #"{"id":"e1","cameraId":"c","kind":"Vehicle","score":0.8,"createdAt":5,"hasThumbnail":true}"#
        let o = try SentinelAPI.decode(V2.Event.self, from: Data(old.utf8))
        XCTAssertNil(o.trackId)
        XCTAssertNil(o.personName)
        XCTAssertNil(o.plate)
    }

    func testPersonDecodesWithNullFacesAndUnknownAlert() throws {
        let json = #"{"id":"p1","name":"Sam","alert":"sometimes","faces":null,"createdAt":1}"#
        let p = try SentinelAPI.decode(V2.Person.self, from: Data(json.utf8))
        XCTAssertEqual(p.faces, [])
        XCTAssertEqual(p.alert.rawValue, "sometimes")
        XCTAssertEqual(DetectionLogic.alertLabel(p.alert), "sometimes")
        XCTAssertNil(p.lastSeenAt)
    }

    func testSemanticStatusAndResultsDecode() throws {
        let status = #"{"available":true,"model":"clip","indexed":1234,"backfill":{"taskId":"t","state":"running","total":10,"processed":3,"indexed":3,"skipped":0,"startedAt":1}}"#
        let s = try SentinelAPI.decode(V2.SemanticStatus.self, from: Data(status.utf8))
        XCTAssertEqual(s.indexed, 1234)
        XCTAssertEqual(s.backfill?.state, "running")

        let results = #"{"model":"clip","items":[{"score":0.31,"event":{"id":"e1","cameraId":"c","kind":"Vehicle","score":0.8,"createdAt":5,"hasThumbnail":true}}]}"#
        let r = try SentinelAPI.decode(V2.SemanticResults.self, from: Data(results.utf8))
        XCTAssertEqual(r.items.first?.event.id, "e1")
        XCTAssertEqual(r.items.first?.score, 0.31)
    }

    // MARK: - Chips

    func testChipsShowOnlyWhatWasMatched() {
        XCTAssertNil(DetectionLogic.personChip(event()))
        XCTAssertNil(DetectionLogic.personChip(event(personName: "  ")))
        XCTAssertNil(DetectionLogic.plateChip(event(plate: "")))
        XCTAssertEqual(DetectionLogic.personChip(event(personName: " Sam ")), "Sam")
        XCTAssertEqual(DetectionLogic.plateChip(event(plate: "7xka492")), "7XKA492")
        XCTAssertEqual(DetectionLogic.detailLines(event(personName: "Sam", plate: "ABC1")), ["Recognized: Sam", "Plate: ABC1"])
        XCTAssertEqual(DetectionLogic.detailLines(event()), [])
    }

    func testWatchlistKindHasItsFlag() {
        XCTAssertEqual(SentinelTheme.kindSymbol("Watchlist"), "flag.fill")
        XCTAssertEqual(AlarmCard.symbol(for: "Watchlist", severity: "Critical"), "flag.fill")
    }

    // MARK: - Semantic availability, text, scores, errors

    func testAvailabilityShowsTheFieldOnlyWhenAvailable() {
        XCTAssertEqual(DetectionLogic.availability(V2.SemanticStatus(available: true, indexed: 5)), .available(indexed: 5))
        XCTAssertEqual(DetectionLogic.availability(V2.SemanticStatus(available: false, reason: "clip_text.onnx is missing")),
                       .unavailable(reason: "clip_text.onnx is missing"))
        XCTAssertEqual(DetectionLogic.availability(V2.SemanticStatus(available: false)),
                       .unavailable(reason: "no semantic search model is installed"))
        XCTAssertTrue(DetectionLogic.SemanticAvailability.available(indexed: 0).showsField)
        XCTAssertFalse(DetectionLogic.SemanticAvailability.unavailable(reason: "x").showsField)
        XCTAssertFalse(DetectionLogic.SemanticAvailability.checking.showsField)
        XCTAssertEqual(DetectionLogic.availability(failedWith: SentinelAPIError.server(status: 404, code: "not_found", message: "Not found.")),
                       .unsupported)
        XCTAssertEqual(DetectionLogic.availability(failedWith: SentinelAPIError.server(status: 403, code: "forbidden", message: "no")),
                       .unsupported)
        XCTAssertEqual(DetectionLogic.indexedNote(1234), "Searches snapshots by what they show, on this server. 1,234 events indexed.")
        XCTAssertEqual(DetectionLogic.indexedNote(1), "Searches snapshots by what they show, on this server. 1 event indexed.")
    }

    func testTextProblemAndRequest() {
        XCTAssertNotNil(DetectionLogic.textProblem(" a "))
        XCTAssertNotNil(DetectionLogic.textProblem(String(repeating: "x", count: 201)))
        XCTAssertNil(DetectionLogic.textProblem("red truck"))
        let body = DetectionLogic.semanticRequest("  red truck  ", limit: 500)
        XCTAssertEqual(body.text, "red truck")
        XCTAssertEqual(body.limit, 100)
        XCTAssertEqual(DetectionLogic.semanticRequest("ok").limit, 30)
    }

    func testScoreTextAndBars() {
        XCTAssertEqual(DetectionLogic.scoreText(0.2768), "0.28")
        XCTAssertEqual(DetectionLogic.scoreText(.nan), "—")
        XCTAssertEqual(DetectionLogic.scoreBars([hit(0.3), hit(0.15), hit(-1)]), [1, 0.5, 0])
        XCTAssertEqual(DetectionLogic.scoreBars([]), [])
        XCTAssertEqual(DetectionLogic.scoreBars([hit(0)]), [0])
    }

    func testSemanticErrorsShowTheServersMessage() {
        let unavailable = DetectionLogic.semanticError(
            SentinelAPIError.server(status: 503, code: "unavailable", message: "semantic search is unavailable: clip_text.onnx is missing"))
        XCTAssertTrue(unavailable.unavailable)
        XCTAssertEqual(unavailable.text, "Semantic search is unavailable: clip_text.onnx is missing")

        let bad = DetectionLogic.semanticError(SentinelAPIError.server(status: 400, code: "bad_request", message: "text is too short"))
        XCTAssertFalse(bad.unavailable)
        XCTAssertEqual(bad.text, "text is too short")
    }

    // MARK: - People

    func testPeopleLabels() {
        XCTAssertEqual(DetectionLogic.alertLabel(.default), "Name them in the alarm")
        XCTAssertEqual(DetectionLogic.alertLabel(.never), "Don't alarm (known person)")
        XCTAssertEqual(DetectionLogic.alertLabel(.always), "Critical watchlist alarm")
        XCTAssertEqual(DetectionLogic.alertLabel(nil), "Name them in the alarm")

        let sorted = DetectionLogic.sortPeople([V2.Person(id: "1", name: "bob"), V2.Person(id: "2", name: "Alice")])
        XCTAssertEqual(sorted.map(\.name), ["Alice", "bob"])

        let now: UnixMillis = 10_000_000_000
        XCTAssertEqual(DetectionLogic.ago(nil, now: now), "Never")
        XCTAssertEqual(DetectionLogic.ago(now - 30_000, now: now), "Just now")
        XCTAssertEqual(DetectionLogic.ago(now - 5 * 60_000, now: now), "5 min ago")
        XCTAssertEqual(DetectionLogic.ago(now - 3 * 3_600_000, now: now), "3 h ago")
        XCTAssertEqual(DetectionLogic.ago(now - 86_400_000, now: now), "1 day ago")
        XCTAssertEqual(DetectionLogic.ago(now - 2 * 86_400_000, now: now), "2 days ago")

        let seen = V2.Person(id: "1", name: "Sam", lastSeenAt: now - 3 * 3_600_000, lastSeenCameraId: "cam-1")
        XCTAssertEqual(DetectionLogic.lastSeen(seen, cameraName: { $0 == "cam-1" ? "Front door" : nil }, now: now), "3 h ago · Front door")
        XCTAssertEqual(DetectionLogic.lastSeen(seen, cameraName: { _ in nil }, now: now), "3 h ago")
        XCTAssertEqual(DetectionLogic.lastSeen(V2.Person(id: "2", name: "X"), cameraName: { _ in "C" }, now: now), "Never")
    }

    func testPeopleErrorExplainsAnOlderServer() {
        XCTAssertTrue(DetectionLogic.peopleError(SentinelAPIError.server(status: 404, code: "not_found", message: "Not found."))
            .contains("Update the Sentinel server"))
        XCTAssertEqual(DetectionLogic.peopleError(SentinelAPIError.server(status: 403, code: "forbidden", message: "Your role can't do this.")),
                       "Your role can't do this.")
    }

    // MARK: - API

    func testPeopleCallReadsTheItems() async throws {
        MockURLProtocol.reset { _ in .init(body: Data(#"{"items":[{"id":"p1","name":"Sam","alert":"always","faces":[{"id":"f1","eventId":"e1","cameraId":"c","at":1,"score":0.9}],"createdAt":1}]}"#.utf8)) }
        let people = try await api().people()
        XCTAssertEqual(people.first?.faces.first?.id, "f1")
        XCTAssertEqual(people.first?.alert, .always)
        let req = MockURLProtocol.seen.last!
        XCTAssertEqual(req.httpMethod, "GET")
        XCTAssertEqual(req.url?.path, "/api/v2/people")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer sat")
    }

    func testFaceCropAndPersonPathsAreEscaped() async throws {
        MockURLProtocol.reset { _ in .init(body: Data([0xFF, 0xD8])) }
        _ = try await api().faceCrop("f 1")
        XCTAssertEqual(MockURLProtocol.seen.last?.url?.absoluteString, "http://10.0.0.1:8090/api/v2/faces/f%201/crop.jpg")

        MockURLProtocol.reset { _ in .init(body: Data(#"{"id":"p 1","name":"Sam","alert":"default","faces":[],"createdAt":1}"#.utf8)) }
        _ = try await api().person("p 1")
        XCTAssertEqual(MockURLProtocol.seen.last?.url?.absoluteString, "http://10.0.0.1:8090/api/v2/people/p%201")
    }

    func testSemanticSearchPostsTheBody() async throws {
        MockURLProtocol.reset { _ in .init(body: Data(#"{"model":"clip","items":[]}"#.utf8)) }
        let results = try await api().semanticSearch(DetectionLogic.semanticRequest("red truck"))
        XCTAssertEqual(results.items, [])
        let req = MockURLProtocol.seen.last!
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/api/v2/events/semantic")
        let body = try JSONSerialization.jsonObject(with: req.httpBody ?? Data()) as? [String: Any]
        XCTAssertEqual(body?["text"] as? String, "red truck")
        XCTAssertEqual(body?["limit"] as? Int, 30)
        XCTAssertNil(body?["cameras"])
    }

    func testSemanticSearchErrorsCarryTheServersMessage() async {
        for (status, message) in [(400, "text must be 2 to 200 characters"), (503, "semantic search is unavailable: no model")] {
            MockURLProtocol.reset { _ in
                .init(status: status, body: Data(#"{"error":{"code":"x","message":"\#(message)"}}"#.utf8))
            }
            do {
                _ = try await api().semanticSearch(DetectionLogic.semanticRequest("red truck"))
                XCTFail("HTTP \(status) should throw")
            } catch {
                XCTAssertEqual((error as? SentinelAPIError)?.status, status)
                XCTAssertTrue(DetectionLogic.semanticError(error).text.hasSuffix(message.replacingOccurrences(of: "semantic search is unavailable: ", with: "")))
            }
        }
    }
}

/// Parity 3.4.2 (PARITY-342.md §2, §3): every detection kind has its own
/// symbol, and Playback opens 5 s before an event and 10 s before an alarm.
/// (Kept in this file so the test target needs no project change.)
@MainActor
final class ParityTests: XCTestCase {
    func testPreRollConstants() {
        XCTAssertEqual(PlaybackModel.eventPreRoll, 5_000)
        XCTAssertEqual(PlaybackModel.alarmPreRoll, 10_000)
        XCTAssertEqual(PlaybackModel.eventStart(100_000), 95_000)
        XCTAssertEqual(PlaybackModel.alarmStart(100_000), 90_000)
    }

    func testMarkerJumpUsesEachPreRollAndSkipsTheCurrentMarker() {
        let events: [UnixMillis] = [100_000, 200_000]
        let alarms: [UnixMillis] = [150_000]
        // Forward from the start lands on the first event's pre-roll.
        XCTAssertEqual(PlaybackModel.markerTarget(events: events, alarms: alarms, position: 0, forward: true), 95_000)
        // Having just jumped there, forward goes on to the alarm, not back to the same event.
        XCTAssertEqual(PlaybackModel.markerTarget(events: events, alarms: alarms, position: 95_000, forward: true), 140_000)
        XCTAssertEqual(PlaybackModel.markerTarget(events: events, alarms: alarms, position: 140_000, forward: true), 195_000)
        XCTAssertNil(PlaybackModel.markerTarget(events: events, alarms: alarms, position: 195_000, forward: true))
        // Back from just after the alarm's start skips it (still playing it) and returns to the event.
        XCTAssertEqual(PlaybackModel.markerTarget(events: events, alarms: alarms, position: 142_000, forward: false), 95_000)
        XCTAssertEqual(PlaybackModel.markerTarget(events: events, alarms: alarms, position: 150_000, forward: false), 140_000)
    }

    func testEveryDetectionKindHasItsOwnSymbol() {
        let kinds = ["Person", "Vehicle", "Face", "License Plate", "Animal", "Package",
                     "Loitering", "Line crossing", "Intrusion", "audio", "Sound", "Watchlist", "Motion"]
        for kind in kinds {
            XCTAssertTrue(SentinelTheme.hasKindSymbol(kind), kind)
            XCTAssertNotEqual(SentinelTheme.kindSymbol(kind), SentinelTheme.fallbackKindSymbol, kind)
        }
        XCTAssertEqual(SentinelTheme.kindSymbol("Motion"), "figure.walk.circle")
        XCTAssertEqual(SentinelTheme.kindSymbol("Sound"), SentinelTheme.kindSymbol("audio"))
        XCTAssertEqual(SentinelTheme.kindSymbol("Something new"), SentinelTheme.fallbackKindSymbol)
        XCTAssertFalse(SentinelTheme.hasKindSymbol("Something new"))
    }

    func testAlarmSymbolsFollowKindSymbolsForDetections() {
        for kind in ["Person", "Vehicle", "Motion", "Line crossing", "Intrusion", "Sound", "Package", "Watchlist"] {
            XCTAssertEqual(AlarmCard.symbol(for: kind, severity: "Warning"), SentinelTheme.kindSymbol(kind), kind)
        }
        XCTAssertEqual(AlarmCard.symbol(for: "Camera Offline", severity: "Critical"), "video.slash.fill")
        XCTAssertEqual(AlarmCard.symbol(for: nil, severity: "Critical"), "exclamationmark.octagon.fill")
        XCTAssertEqual(AlarmCard.symbol(for: "Mystery", severity: "Warning"), "exclamationmark.triangle.fill")
    }
}
