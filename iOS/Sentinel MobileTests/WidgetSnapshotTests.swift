import XCTest
@testable import Sentinel_Mobile

@MainActor
final class WidgetSnapshotTests: XCTestCase {
    private func camera(_ id: String, online: Bool, recording: Bool) throws -> V2.Camera {
        let json = """
        {"id":"\(id)","name":"Cam \(id)","location":"","rtspUrl":"","subRtspUrl":"","username":"","hasPassword":false,
         "recording":\(recording),"retentionDays":7,"fps":15,"motionLevel":0,"alertOn":"all","onvifUrl":"",
         "cameraAnalytics":false,"sortOrder":0,"createdAt":0,"recorderId":"local","groupIds":null,
         "status":{"online":\(online),"viewers":0,"message":""}}
        """
        return try JSONDecoder().decode(V2.Camera.self, from: Data(json.utf8))
    }

    private func alarm(_ id: String, state: V2.AlarmState = .new, severity: V2.Severity = .warning,
                       createdAt: UnixMillis, cameraID: String? = "c1") -> V2.Alarm {
        V2.Alarm(id: id, cameraId: cameraID, cameraName: nil, kind: "Person", title: "Person detected \(id)", detail: "",
                 severity: severity, state: state, owner: "Unassigned", createdAt: createdAt, updatedAt: createdAt + 5,
                 lastEventAt: createdAt + 10, eventCount: 2, eventId: "e-\(id)", instructions: [], responseLog: [])
    }

    func testMakeCountsCamerasAndPicksNewestOpenAlarm() throws {
        let cams = [try camera("c1", online: true, recording: true),
                    try camera("c2", online: true, recording: false),
                    try camera("c3", online: false, recording: true)]
        let alarms = [alarm("a1", createdAt: 1_000), alarm("a2", state: .acknowledged, createdAt: 3_000),
                      alarm("a3", state: .resolved, createdAt: 9_000)]
        let now = Date(timeIntervalSince1970: 1_700_000_000)
        let s = WidgetSnapshotWriter.make(siteName: "HQ", signedIn: true, isDemo: false, cameras: cams, alarms: alarms,
                                          cameraName: { _ in "Dock" }, imageAlarmID: "a2", now: now)
        XCTAssertEqual(s.camerasTotal, 3)
        XCTAssertEqual(s.camerasOnline, 2)
        XCTAssertEqual(s.camerasRecording, 1, "an offline camera isn't recording")
        XCTAssertEqual(s.recordingState, .some)
        XCTAssertEqual(s.openAlarms, 2)
        XCTAssertEqual(s.newAlarms, 1)
        XCTAssertEqual(s.lastAlarm?.id, "a2", "newest open alarm; the resolved one is skipped")
        XCTAssertEqual(s.lastAlarm?.cameraName, "Dock")
        XCTAssertEqual(s.lastAlarm?.hasImage, true)
        XCTAssertEqual(s.lastAlarm?.raisedAt, Date(unixMillis: 3_000))
        XCTAssertEqual(s.generatedAt, now)
    }

    func testSignedOutSnapshotCarriesNoSiteData() throws {
        let s = WidgetSnapshotWriter.make(siteName: nil, signedIn: false, isDemo: false,
                                          cameras: [try camera("c1", online: true, recording: true)],
                                          alarms: [alarm("a1", createdAt: 1)], cameraName: { _ in nil }, imageAlarmID: nil)
        XCTAssertFalse(s.signedIn)
        XCTAssertEqual(s.camerasTotal, 0)
        XCTAssertNil(s.lastAlarm)
    }

    func testStoreRoundTripAndSameContent() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("widget-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let files = WidgetSnapshotStore(directory: dir)
        XCTAssertNil(files.read())
        var snap = WidgetSnapshot.placeholder
        snap.generatedAt = Date(timeIntervalSince1970: 1_700_000_123)
        snap.lastAlarm?.raisedAt = Date(timeIntervalSince1970: 1_700_000_000)
        XCTAssertTrue(files.write(snap))
        XCTAssertEqual(files.read(), snap)

        var later = snap
        later.generatedAt = snap.generatedAt.addingTimeInterval(60)
        XCTAssertTrue(later.sameContent(as: snap), "only the timestamp differs")
        later.camerasOnline -= 1
        XCTAssertFalse(later.sameContent(as: snap))

        // The shared JSON never names a token or a server address.
        let raw = try String(contentsOf: dir.appendingPathComponent(WidgetSnapshotStore.fileName), encoding: .utf8)
        XCTAssertFalse(raw.lowercased().contains("token"))
        XCTAssertFalse(raw.contains("http"))

        // Thumbnails over the limit are not shared.
        XCTAssertFalse(files.writeImage(Data(count: WidgetSnapshotStore.maxImageBytes + 1)))
        XCTAssertNil(files.readImage())
        XCTAssertTrue(files.writeImage(Data([1, 2, 3])))
        XCTAssertEqual(files.readImage(), Data([1, 2, 3]))
        files.clear()
        XCTAssertNil(files.read())
        XCTAssertNil(files.readImage())
    }

    func testShrinkFitsTheWidgetBudget() {
        let big = UIGraphicsImageRenderer(size: CGSize(width: 1920, height: 1080)).image { ctx in
            for i in 0..<200 {
                UIColor(hue: CGFloat(i) / 200, saturation: 1, brightness: 1, alpha: 1).setFill()
                ctx.fill(CGRect(x: i * 9, y: (i * 37) % 1000, width: 40, height: 80))
            }
        }
        let data = big.pngData()!
        let small = WidgetSnapshotWriter.shrink(data, maxBytes: WidgetSnapshotStore.maxImageBytes)
        XCTAssertNotNil(small)
        XCTAssertLessThanOrEqual(small!.count, WidgetSnapshotStore.maxImageBytes)
        XCTAssertLessThanOrEqual(UIImage(data: small!)!.size.width, 360)
        XCTAssertNil(WidgetSnapshotWriter.shrink(Data("not an image".utf8), maxBytes: 1000))
    }

    func testDeepLinks() {
        XCTAssertEqual(SentinelDeepLink(url: SentinelDeepLink.alarm(id: "abc-1").url), .alarm(id: "abc-1"))
        XCTAssertEqual(SentinelDeepLink(url: SentinelDeepLink.home.url), .home)
        XCTAssertEqual(SentinelDeepLink(url: URL(string: "sentinelmobile://alarm/")!), .alarms)
        XCTAssertNil(SentinelDeepLink(url: URL(string: "https://example.com/alarm/1")!))
        XCTAssertEqual(SentinelDeepLink.alarm(id: "x").url.absoluteString, "sentinelmobile://alarm/x")
    }

    func testDeepLinkOpensTheAlarm() {
        let store = AppStore.shared
        store.open(.alarm(id: "a9"))
        XCTAssertEqual(store.selectedTab, .events)
        XCTAssertEqual(store.eventsMode, .alarms)
        XCTAssertEqual(store.routedAlarmID, "a9")
        store.routedAlarmID = nil
        store.selectedTab = .home
    }

    // MARK: Live Activity

    func testActivityContentMatchesTheServerWireShape() throws {
        let a = alarm("a1", state: .investigating, severity: .critical, createdAt: 1_000)
        let content = AlarmActivityController.content(for: a)
        XCTAssertEqual(content.state, "Investigating")
        XCTAssertEqual(content.phase, .acknowledged)
        XCTAssertEqual(content.updatedAt, 1_010, "the later of updatedAt / lastEventAt")
        XCTAssertNil(content.assignee)

        // What internal/push/liveactivity.go sends as "content-state".
        let wire = #"{"state":"Resolved","owner":"alice","severity":"Warning","updatedAt":1700000000000,"eventCount":3}"#
        let decoded = try JSONDecoder().decode(AlarmActivityContent.self, from: Data(wire.utf8))
        XCTAssertEqual(decoded.phase, .closed)
        XCTAssertEqual(decoded.assignee, "alice")
        XCTAssertEqual(decoded.eventCount, 3)
        XCTAssertEqual(AlarmActivityPhase(serverState: "False Alarm"), .closed)
        XCTAssertEqual(AlarmActivityPhase(serverState: "New"), .new)
        XCTAssertEqual(AlarmActivityPhase(serverState: "Snoozed"), .acknowledged)
    }

    func testAutoFollowOnlyCriticalInForegroundWhenNothingFollowed() {
        let critical = alarm("a1", severity: .critical, createdAt: 1)
        let warning = alarm("a2", severity: .warning, createdAt: 1)
        XCTAssertTrue(AlarmActivityController.shouldAutoFollow(critical, enabled: true, following: nil, isForeground: true))
        XCTAssertFalse(AlarmActivityController.shouldAutoFollow(critical, enabled: false, following: nil, isForeground: true))
        XCTAssertFalse(AlarmActivityController.shouldAutoFollow(critical, enabled: true, following: "x", isForeground: true))
        XCTAssertFalse(AlarmActivityController.shouldAutoFollow(critical, enabled: true, following: nil, isForeground: false))
        XCTAssertFalse(AlarmActivityController.shouldAutoFollow(warning, enabled: true, following: nil, isForeground: true))
    }
}
