import XCTest
@testable import SentinelCore

final class LatestLoadTests: XCTestCase {
    func testOnlyTheLatestLoadClearsTheFlag() {
        var gate = LatestLoad()
        XCTAssertFalse(gate.isLoading)
        let first = gate.begin()
        let second = gate.begin() // the first was cancelled by a new range
        XCTAssertTrue(gate.isLoading)
        XCTAssertFalse(gate.finish(first), "the cancelled load must not clear the flag")
        XCTAssertTrue(gate.isLoading)
        XCTAssertFalse(gate.isLatest(first))
        XCTAssertTrue(gate.finish(second))
        XCTAssertFalse(gate.isLoading)
    }

    func testLoadFinishingInOrderClears() {
        var gate = LatestLoad()
        let t = gate.begin()
        XCTAssertTrue(gate.finish(t))
        XCTAssertFalse(gate.isLoading)
    }
}
