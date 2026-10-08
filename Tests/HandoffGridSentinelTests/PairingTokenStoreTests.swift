import XCTest
@testable import SentinelCore

@MainActor
final class PairingTokenStoreTests: XCTestCase {
    func testConstantTimeEqualMatches() {
        XCTAssertTrue(PairingTokenStore.constantTimeEqual("ABCD1234", "ABCD1234"))
    }

    func testConstantTimeEqualRejectsDifferentValue() {
        XCTAssertFalse(PairingTokenStore.constantTimeEqual("ABCD1234", "ABCD1235"))
    }

    func testConstantTimeEqualRejectsDifferentLength() {
        XCTAssertFalse(PairingTokenStore.constantTimeEqual("ABCD", "ABCD1234"))
    }

    func testRedeemWithCorrectCodeSucceeds() {
        let store = PairingTokenStore()
        let pairing = store.generatePairingCode()
        let result = store.redeem(pairingCode: pairing.code, deviceName: "Test iPhone")
        switch result {
        case .success(let device):
            XCTAssertEqual(device.name, "Test iPhone")
            XCTAssertFalse(device.token.isEmpty)
        case .failure(let reason):
            XCTFail("Expected success, got failure: \(reason)")
        }
    }

    func testRedeemConsumesCode() {
        let store = PairingTokenStore()
        let pairing = store.generatePairingCode()
        _ = store.redeem(pairingCode: pairing.code, deviceName: "First")
        // Same code, second attempt — must fail because the code is one-shot.
        let second = store.redeem(pairingCode: pairing.code, deviceName: "Second")
        XCTAssertEqual(second, .failure(.noActiveCode))
    }

    func testRedeemWrongCodeIncrementsAndEventuallyLocksOut() {
        let store = PairingTokenStore()
        _ = store.generatePairingCode()
        // 4 wrong attempts return invalidCode
        for _ in 0..<4 {
            let r = store.redeem(pairingCode: "WRONG999", deviceName: "Attacker")
            XCTAssertEqual(r, .failure(.invalidCode))
        }
        // 5th wrong attempt triggers lockout, which invalidates the code.
        let fifth = store.redeem(pairingCode: "WRONG999", deviceName: "Attacker")
        XCTAssertEqual(fifth, .failure(.lockedOut))
        // Subsequent attempts find no active code.
        let sixth = store.redeem(pairingCode: "WRONG999", deviceName: "Attacker")
        XCTAssertEqual(sixth, .failure(.noActiveCode))
    }

    func testRedeemWithNoActiveCode() {
        let store = PairingTokenStore()
        // Never generated a code.
        let result = store.redeem(pairingCode: "ANYTHING", deviceName: "Attacker")
        XCTAssertEqual(result, .failure(.noActiveCode))
    }

    func testRedeemLogsAuditEntries() {
        let store = PairingTokenStore()
        let pairing = store.generatePairingCode()
        _ = store.redeem(pairingCode: "WRONG", deviceName: "Attacker")
        _ = store.redeem(pairingCode: pairing.code, deviceName: "iPhone")
        let kinds = store.pairingAuditLog.map(\.kind)
        XCTAssertTrue(kinds.contains(.failure))
        XCTAssertTrue(kinds.contains(.success))
    }
}
