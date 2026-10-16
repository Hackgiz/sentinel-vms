import XCTest
@testable import SentinelCore

/// System Monitor thresholds/formatting, the health feed, and tile activity.
final class OpsHealthTests: XCTestCase {
    func testMeterLevelsAtThresholds() {
        XCTAssertEqual(HealthModel.level(of: nil), .off)
        XCTAssertEqual(HealthModel.level(of: .nan), .off)
        XCTAssertEqual(HealthModel.level(of: 79.9), .ok)
        XCTAssertEqual(HealthModel.level(of: 80), .warn)
        XCTAssertEqual(HealthModel.level(of: 94.9), .warn)
        XCTAssertEqual(HealthModel.level(of: 95), .crit)
        XCTAssertEqual(HealthModel.level(of: 100), .crit)
    }

    func testFormatting() {
        XCTAssertEqual(HealthModel.bytes(412_000_000_000), "412 GB")
        XCTAssertEqual(HealthModel.bytes(1_800_000_000_000), "1.8 TB")
        XCTAssertEqual(HealthModel.bytes(999), "999 B")
        XCTAssertEqual(HealthModel.uptime(59), "0m")
        XCTAssertEqual(HealthModel.uptime(3 * 3600 + 120), "3h 2m")
        XCTAssertEqual(HealthModel.uptime(2 * 86_400 + 5 * 3600), "2d 5h")
        XCTAssertEqual(HealthModel.age(12), "12 s ago")
        XCTAssertEqual(HealthModel.age(180), "3 min ago")
        XCTAssertEqual(HealthModel.age(7200), "2 h ago")
        XCTAssertEqual(HealthModel.kbps(850.4), "850 kbps")
        XCTAssertEqual(HealthModel.kbps(2500), "2.5 Mbps")
    }

    func testServerCardLevelsAndOfflineNulls() {
        let busy = V2.ServerHealth(id: "r1", name: "Rack", online: true, latencyMs: 12, cpu: 30,
                                   mem: .init(used: 50, total: 100), disk: .init(used: 96, free: 4, total: 100),
                                   dbBytes: 5_000_000, uptimeSec: 90_000, version: "1.2.3",
                                   cameras: [.init(id: "c1", name: "A", online: true, recording: true),
                                             .init(id: "c2", name: "B", online: false, recording: false)])
        let card = HealthModel.card(busy)
        XCTAssertEqual(card.state, .crit)
        XCTAssertEqual(card.cpu.label, "30%")
        XCTAssertEqual(card.mem.level, .ok)
        XCTAssertEqual(card.disk.level, .crit)
        XCTAssertEqual(card.uptime, "1d 1h")
        XCTAssertEqual(card.latency, "12 ms")
        XCTAssertEqual(card.cameras, 2)
        XCTAssertEqual(card.camerasOnline, 1)

        let warm = V2.ServerHealth(id: "r2", name: "W", online: true, cpu: 85, version: "1")
        XCTAssertEqual(HealthModel.card(warm).state, .warn)

        let down = V2.ServerHealth(id: "r3", name: "Off", online: false, cpu: 40, uptimeSec: 100, version: "1",
                                   cameras: [.init(id: "c9", name: "Gate", online: false, recording: true, lastSegmentAgeSec: 4000)])
        let off = HealthModel.card(down)
        XCTAssertEqual(off.state, .crit)
        XCTAssertEqual(off.cpu.label, "n/a")
        XCTAssertEqual(off.cpu.level, .off)
        XCTAssertEqual(off.uptime, "")
        XCTAssertEqual(HealthModel.rows([down]).count, 1) // last-known cameras still render
    }

    func testCameraRowsSegmentLevelsAndSorting() {
        XCTAssertEqual(HealthModel.segmentLevel(online: true, recording: true, ageSec: 119), .ok)
        XCTAssertEqual(HealthModel.segmentLevel(online: true, recording: true, ageSec: 120), .warn)
        XCTAssertEqual(HealthModel.segmentLevel(online: true, recording: true, ageSec: 300), .crit)
        XCTAssertEqual(HealthModel.segmentLevel(online: true, recording: false, ageSec: 900), .off)
        XCTAssertEqual(HealthModel.segmentLevel(online: true, recording: true, ageSec: nil), .warn)
        let servers = [
            V2.ServerHealth(id: "b", name: "Beta", online: true, version: "1", cameras: [
                .init(id: "c2", name: "Zed", online: true, recording: true, lastSegmentAgeSec: 10, kbps: 1500)]),
            V2.ServerHealth(id: "a", name: "Alpha", online: true, version: "1", cameras: [
                .init(id: "c3", name: "Yard", online: true, recording: true, lastSegmentAgeSec: 400),
                .init(id: "c1", name: "Door", online: false, recording: true)])
        ]
        let rows = HealthModel.rows(servers)
        XCTAssertEqual(rows.map(\.name), ["Door", "Yard", "Zed"])
        XCTAssertEqual(rows[0].state, .crit)
        XCTAssertEqual(rows[0].lastSegment, "none")
        XCTAssertEqual(rows[1].state, .crit)
        XCTAssertEqual(rows[2].kbps, "1.5 Mbps")
        XCTAssertEqual(rows[2].state, .ok)
    }

    func testHealthFeedDecodesNullsAndOfflineServer() async throws {
        MockURLProtocol.reset()
        defer { MockURLProtocol.reset() }
        MockURLProtocol.handler = { request, _ in
            XCTAssertEqual(request.url?.path, "/api/v2/system/health")
            return .json(200, """
            {"servers":[
             {"id":"l","name":"Local","online":true,"latencyMs":3,"cpu":12.5,"mem":{"used":1,"total":4},
              "disk":{"used":1,"free":3,"total":4},"dbBytes":1024,"uptimeSec":60,"version":"2.0",
              "cameras":[{"id":"c1","name":"Door","online":true,"recording":true,"lastSegmentAgeSec":5,"kbps":900.5}]},
             {"id":"r","name":"Remote","online":false,"latencyMs":null,"cpu":null,"mem":null,"disk":null,
              "dbBytes":null,"uptimeSec":null,"version":"2.0","cameras":null}]}
            """)
        }
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        let api = SentinelAPI(baseURL: URL(string: "http://nvr.test:8090")!, siteID: "s1", tokenStore: store,
                              session: MockURLProtocol.session(), clientLabel: "Test")
        let servers = try await api.systemHealth()
        XCTAssertEqual(servers.count, 2)
        XCTAssertNil(servers[1].cpu)
        XCTAssertEqual(servers[1].cameras, [])
        XCTAssertEqual(HealthModel.card(servers[1]).state, .crit)
        XCTAssertEqual(HealthModel.rows(servers).first?.kbps, "901 kbps")
    }

    // MARK: Activity glyphs

    func testActivityKindMapping() {
        XCTAssertEqual(ActivityKind(eventKind: "Person"), .person)
        XCTAssertEqual(ActivityKind(eventKind: "face match"), .person)
        XCTAssertEqual(ActivityKind(eventKind: "Vehicle"), .vehicle)
        XCTAssertEqual(ActivityKind(eventKind: "License plate"), .vehicle)
        XCTAssertEqual(ActivityKind(eventKind: "Animal"), .animal)
        XCTAssertEqual(ActivityKind(eventKind: "Motion"), .motion)
        XCTAssertEqual(ActivityKind(eventKind: "Line crossing"), .other)
        XCTAssertEqual(ActivityKind(eventKind: ""), .other)
    }

    @MainActor
    func testTrackerFadesAfterFiveSeconds() {
        let tracker = ActivityTracker()
        let t0 = Date(timeIntervalSince1970: 1_000)
        tracker.record(cameraID: "c1", kind: "Person", at: t0)
        XCTAssertEqual(tracker.active("c1", now: t0.addingTimeInterval(4.9))?.kind, .person)
        XCTAssertNil(tracker.active("c1", now: t0.addingTimeInterval(5)))
        XCTAssertNil(tracker.active("c2", now: t0))
        XCTAssertEqual(tracker.opacity("c1", now: t0), 1, accuracy: 0.001)
        XCTAssertEqual(tracker.opacity("c1", now: t0.addingTimeInterval(2.5)), 0.5, accuracy: 0.001)
        XCTAssertEqual(tracker.opacity("c1", now: t0.addingTimeInterval(9)), 0)
        tracker.record(cameraID: "c1", kind: "Vehicle", at: t0.addingTimeInterval(4))
        XCTAssertEqual(tracker.active("c1", now: t0.addingTimeInterval(6))?.kind, .vehicle)
    }
}
