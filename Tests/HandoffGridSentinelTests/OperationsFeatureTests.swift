import XCTest
@testable import SentinelCore

final class AlarmRuleTests: XCTestCase {
    private func date(hour: Int) -> Date {
        Calendar.current.date(bySettingHour: hour, minute: 30, second: 0, of: Date())!
    }

    private func rule(from: Int? = nil, to: Int? = nil, cameras: [UUID]? = nil) -> NotificationRule {
        NotificationRule(
            name: "Test", trigger: "Any alarm", delivery: "Center",
            severity: AlertSeverity.info.rawValue, isEnabled: true, isSnoozed: false,
            cameraIDs: cameras, activeFromHour: from, activeToHour: to
        )
    }

    func testOvernightScheduleWrapsPastMidnight() {
        let afterHours = rule(from: 22, to: 6)
        XCTAssertTrue(afterHours.isActive(at: date(hour: 23)))
        XCTAssertTrue(afterHours.isActive(at: date(hour: 2)))
        XCTAssertFalse(afterHours.isActive(at: date(hour: 6)))
        XCTAssertFalse(afterHours.isActive(at: date(hour: 12)))
    }

    func testDaytimeSchedule() {
        let business = rule(from: 9, to: 17)
        XCTAssertTrue(business.isActive(at: date(hour: 9)))
        XCTAssertFalse(business.isActive(at: date(hour: 17)))
        XCTAssertTrue(rule().isActive(at: date(hour: 3)), "No schedule = always active")
    }

    func testCameraScope() {
        let dock = UUID()
        let scoped = rule(cameras: [dock])
        XCTAssertTrue(scoped.matches(alert: AlertEvent(source: "Dock", title: "Person", severity: .warning, kind: .person, cameraID: dock)))
        XCTAssertFalse(scoped.matches(alert: AlertEvent(source: "Lobby", title: "Person", severity: .warning, kind: .person, cameraID: UUID())))
        XCTAssertTrue(scoped.matches(alert: AlertEvent(source: "Disk", title: "Low", severity: .warning, kind: .storage)),
                      "Camera-less alerts (storage/system) pass the camera filter")
    }

    func testLegacyRuleJSONStillDecodes() throws {
        let json = #"{"id":"\#(UUID().uuidString)","name":"Old","trigger":"Critical","delivery":"Center","severity":"Critical","isEnabled":true,"isSnoozed":false}"#
        let decoded = try JSONDecoder().decode(NotificationRule.self, from: Data(json.utf8))
        XCTAssertNil(decoded.cameraIDs)
        XCTAssertFalse(decoded.hasSchedule)
    }
}

@MainActor
final class AuditChainTests: XCTestCase {
    func testChainHashDetectsEdits() {
        var entry = AuditLogEntry(time: Date(), user: "Ana", area: "Evidence", action: "Locked", detail: "HG-1")
        let hash = entry.computedChainHash(previous: "abc")
        XCTAssertEqual(hash, entry.computedChainHash(previous: "abc"))
        XCTAssertNotEqual(hash, entry.computedChainHash(previous: "abd"))
        entry.detail = "HG-2"
        XCTAssertNotEqual(hash, entry.computedChainHash(previous: "abc"))
    }

    func testCSVEscapesQuotesAndCommasOldestFirst() {
        let newer = AuditLogEntry(time: Date(timeIntervalSince1970: 200), user: "B", area: "X", action: "said \"hi\", twice", detail: "")
        let older = AuditLogEntry(time: Date(timeIntervalSince1970: 100), user: "A", area: "X", action: "first", detail: "")
        let lines = WorkflowStore.auditCSV([newer, older]).components(separatedBy: "\r\n")
        XCTAssertEqual(lines[0], "time,user,area,action,detail,chain_hash")
        XCTAssertTrue(lines[1].contains("\"A\""))
        XCTAssertTrue(lines[2].contains("\"said \"\"hi\"\", twice\""))
    }
}

final class EvidenceVaultTests: XCTestCase {
    func testStreamingHashMatchesKnownDigest() throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("vault-\(UUID().uuidString).bin")
        try Data("abc".utf8).write(to: url)
        defer { try? FileManager.default.removeItem(at: url) }
        XCTAssertEqual(try EvidenceVault.sha256(of: url),
                       "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
    }
}

@MainActor
final class SupervisorApprovalTests: XCTestCase {
    private func user(_ name: String, _ role: String, pin: String? = nil) -> UserAccount {
        UserAccount(name: name, role: role, status: "Active", lastSeen: "", passwordHash: pin.map(OperatorSessionStore.pinHash(for:)))
    }

    private func session(signedInAs operatorUser: UserAccount) -> OperatorSessionStore {
        let store = OperatorSessionStore()
        store.loginWithoutPin(operatorUser)   // PIN-less so the test never touches the secret vault
        return store
    }

    func testRetentionNeedsApprovalOnlyBelowSupervisor() {
        XCTAssertTrue(session(signedInAs: user("Op", "Operator")).needsApproval(.changeRetention))
        XCTAssertTrue(session(signedInAs: user("Op", "Operator")).needsApproval(.applyRetention))
        XCTAssertFalse(session(signedInAs: user("Sup", "Supervisor")).needsApproval(.changeRetention))
        XCTAssertFalse(session(signedInAs: user("Boss", "Admin")).needsApproval(.applyRetention))
    }

    func testUnlockEvidenceAlwaysNeedsApproval() {
        XCTAssertTrue(session(signedInAs: user("Boss", "Admin")).needsApproval(.unlockEvidence))
    }

    func testSupervisorPinApprovesAndIsAudited() {
        let sup = user("Sam", "Supervisor", pin: "4321")
        let store = session(signedInAs: user("Op", "Operator"))
        var audit: [(String, String, String)] = []
        store.auditRecorder = { audit.append(($0, $1, $2)) }

        let result = store.authorize(.unlockEvidence, approver: sup, pin: "4321", reason: "Court request", detail: "HG-1")
        XCTAssertEqual(result, .approved(approver: "Sam"))
        XCTAssertEqual(audit.last?.0, "Op")
        XCTAssertTrue(audit.last?.1.contains("Approval granted") == true)
        XCTAssertTrue(audit.last?.2.contains("approved by Sam") == true)
        XCTAssertTrue(audit.last?.2.contains("Court request") == true)
    }

    func testWrongPinIsDeniedAndLocksAfterThree() {
        let sup = user("Sam", "Supervisor", pin: "4321")
        let store = session(signedInAs: user("Op", "Operator"))
        for _ in 0..<2 {
            XCTAssertEqual(store.authorize(.changeRetention, approver: sup, pin: "0000", reason: "", detail: ""), .denied(message: "Incorrect PIN."))
        }
        guard case .denied(let message) = store.authorize(.changeRetention, approver: sup, pin: "0000", reason: "", detail: "") else { return XCTFail() }
        XCTAssertTrue(message.contains("Locked"))
        // Even the right PIN is refused while locked.
        guard case .denied = store.authorize(.changeRetention, approver: sup, pin: "4321", reason: "", detail: "") else { return XCTFail("lockout must hold") }
    }

    func testOperatorCannotApproveEvenWithOwnPin() {
        let other = user("Pat", "Operator", pin: "1111")
        let store = session(signedInAs: user("Op", "Operator"))
        guard case .denied = store.authorize(.changeRetention, approver: other, pin: "1111", reason: "", detail: "") else { return XCTFail() }
    }

    func testUnlockRequiresReason() {
        let sup = user("Sam", "Supervisor", pin: "4321")
        let store = session(signedInAs: user("Op", "Operator"))
        guard case .denied(let message) = store.authorize(.unlockEvidence, approver: sup, pin: "4321", reason: "   ", detail: "") else { return XCTFail() }
        XCTAssertTrue(message.contains("reason"))
    }

    func testPinlessAdminCanSelfConfirmButOperatorCannot() {
        let admin = session(signedInAs: user("Boss", "Admin"))
        XCTAssertTrue(admin.canSelfConfirmWithoutPin)
        XCTAssertEqual(admin.authorize(.unlockEvidence, approver: nil, pin: "", reason: "Closing case", detail: ""), .approved(approver: "Boss"))

        let op = session(signedInAs: user("Op", "Operator"))
        XCTAssertFalse(op.canSelfConfirmWithoutPin)
        guard case .denied = op.authorize(.unlockEvidence, approver: nil, pin: "", reason: "x", detail: "") else { return XCTFail() }
    }

    func testApproversListExcludesPinlessAndNonSupervisors() {
        let users = [user("A", "Admin", pin: "1"), user("B", "Supervisor"), user("C", "Operator", pin: "1"), user("D", "Supervisor", pin: "1")]
        XCTAssertEqual(OperatorSessionStore().approvers(in: users).map(\.name), ["A", "D"])
    }
}
