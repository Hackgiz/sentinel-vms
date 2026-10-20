import XCTest
@testable import SentinelCore

final class LocalServerServiceTests: XCTestCase {
    private func job(_ label: String, _ args: [String], log: String? = nil) -> [String: Any] {
        var job: [String: Any] = ["Label": label, "ProgramArguments": args]
        if let log { job["StandardErrorPath"] = log }
        return job
    }

    func testReadsAManagementServerAgent() throws {
        let s = try XCTUnwrap(LocalServerService.from(
            job: job("com.example.site-ms",
                     ["/Users/me/site/bin/sentinel", "-role", "management", "-data", "/x", "-listen", "127.0.0.1:18090"],
                     log: "/Users/me/site/log/ms.log"),
            plistPath: "/p.plist", domain: .user))
        XCTAssertEqual(s.role, "management")
        XCTAssertTrue(s.servesAPI)
        XCTAssertEqual(s.address, URL(string: "http://127.0.0.1:18090"))
        XCTAssertEqual(s.logPath, "/Users/me/site/log/ms.log")
    }

    func testRecordingServerDoesNotServeTheAPI() throws {
        let s = try XCTUnwrap(LocalServerService.from(
            job: job("rs1", ["/opt/sentinel", "-role", "recording", "-listen=127.0.0.1:18190"]),
            plistPath: "/p", domain: .user))
        XCTAssertFalse(s.servesAPI)
        XCTAssertEqual(s.listen, "127.0.0.1:18190")
    }

    func testAllInterfacesMeansLoopbackAndRoleDefaultsToAll() throws {
        let s = try XCTUnwrap(LocalServerService.from(
            job: job("com.handoffgrid.sentinel.server", ["/usr/local/sentinel/sentinel", "--data", "/d", "--listen", ":8090"]),
            plistPath: "/p", domain: .system))
        XCTAssertEqual(s.role, "all")
        XCTAssertEqual(s.address, URL(string: "http://127.0.0.1:8090"))
    }

    func testDefaultListenFillsInWhenTheJobHasNone() throws {
        let s = try XCTUnwrap(LocalServerService.from(
            job: job("d", ["/usr/local/sentinel/sentinel", "-data", "/d"]),
            plistPath: "/p", domain: .system, defaultListen: ":8090"))
        XCTAssertEqual(s.address, URL(string: "http://127.0.0.1:8090"))
    }

    func testIgnoresOtherProgramsAndHelperSubcommands() {
        XCTAssertNil(LocalServerService.from(job: job("menu", ["/Apps/Menu.app/Contents/MacOS/SentinelSiteMenu"]), plistPath: "/p", domain: .user))
        XCTAssertNil(LocalServerService.from(job: job("helper", ["/usr/local/sentinel/sentinel", "service", "plist"]), plistPath: "/p", domain: .user))
        XCTAssertNil(LocalServerService.from(job: ["Label": "x"], plistPath: "/p", domain: .user))
    }

    func testBadPortHasNoAddress() {
        let s = LocalServerService(label: "x", plistPath: "/p", domain: .user, role: "all", listen: "127.0.0.1:http", logPath: nil)
        XCTAssertNil(s.address)
    }

    func testStartOrderPutsTheManagementServerFirst() {
        let rs = LocalServerService(label: "a-rs", plistPath: "/a", domain: .user, role: "recording", listen: nil, logPath: nil)
        let ms = LocalServerService(label: "z-ms", plistPath: "/z", domain: .user, role: "management", listen: nil, logPath: nil)
        XCTAssertEqual(LocalServerService.startOrder([rs, ms]).map(\.label), ["z-ms", "a-rs"])
    }
}
