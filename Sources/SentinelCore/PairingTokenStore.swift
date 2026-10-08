import Foundation

public struct PairedDevice: Codable, Hashable, Identifiable {
    public let id: UUID
    public let name: String
    public let token: String
    public let pairedAt: Date
    public var lastSeenAt: Date?
    public var apnsPushToken: String?

    public init(
        id: UUID = UUID(),
        name: String,
        token: String,
        pairedAt: Date = Date(),
        lastSeenAt: Date? = nil,
        apnsPushToken: String? = nil
    ) {
        self.id = id
        self.name = name
        self.token = token
        self.pairedAt = pairedAt
        self.lastSeenAt = lastSeenAt
        self.apnsPushToken = apnsPushToken
    }
}

public struct PairingCode: Hashable {
    public let code: String           // Short user-visible token, expires fast
    public let issuedAt: Date
    public let expiresAt: Date

    public var isExpired: Bool { Date() >= expiresAt }
}

public struct PairingAuditEntry: Hashable, Identifiable {
    public enum Kind: String, Hashable { case success, failure, lockout }
    public let id: UUID
    public let timestamp: Date
    public let kind: Kind
    public let detail: String

    public init(kind: Kind, detail: String, timestamp: Date = Date()) {
        self.id = UUID()
        self.timestamp = timestamp
        self.kind = kind
        self.detail = detail
    }
}

public enum PairingRedemptionFailure: Error, Equatable {
    case invalidCode          // wrong code, attempts remaining
    case expired              // code lifetime elapsed
    case lockedOut            // too many wrong attempts; code invalidated
    case noActiveCode         // no code is active (never generated or already redeemed)
}

/// Manages the two kinds of credentials used by the iOS companion app:
///   1. **Pairing codes** — short, one-shot, 5-minute lifespan. Shown as a
///      QR code on the Mac. The iOS app exchanges it for a long-lived auth
///      token via `POST /pair`.
///   2. **Auth tokens** — long-lived bearer tokens stored per paired device.
///      The iOS app sends them in `Authorization: Bearer <token>`.
@MainActor
public final class PairingTokenStore: ObservableObject {
    @Published public private(set) var pairedDevices: [PairedDevice] = []
    @Published public private(set) var activePairingCode: PairingCode?
    @Published public private(set) var pairingAuditLog: [PairingAuditEntry] = []

    private let storageURL: URL
    private let pairingCodeLifetime: TimeInterval = 300 // 5 minutes
    private let maxFailedAttemptsPerCode = 5
    private let auditLogMaxEntries = 100

    private var failedAttemptsForActiveCode = 0

    public init() {
        let supportDir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        try? FileManager.default.createDirectory(at: supportDir, withIntermediateDirectories: true)
        self.storageURL = supportDir.appendingPathComponent("paired_devices.json")
        load()
    }

    // MARK: - Pairing codes

    @discardableResult
    public func generatePairingCode() -> PairingCode {
        let code = randomCode(length: 8)
        let now = Date()
        let pairing = PairingCode(
            code: code,
            issuedAt: now,
            expiresAt: now.addingTimeInterval(pairingCodeLifetime)
        )
        activePairingCode = pairing
        failedAttemptsForActiveCode = 0
        return pairing
    }

    public func clearPairingCode() {
        activePairingCode = nil
        failedAttemptsForActiveCode = 0
    }

    /// Exchanges a valid pairing code for a long-lived auth token. Consumes
    /// the code (one-shot). After 5 wrong attempts the code is invalidated.
    public func redeem(pairingCode code: String, deviceName: String) -> Result<PairedDevice, PairingRedemptionFailure> {
        guard let active = activePairingCode else {
            recordAudit(.failure, detail: "Redeem attempted with no active code (device: \(deviceName))")
            return .failure(.noActiveCode)
        }

        if active.isExpired {
            activePairingCode = nil
            failedAttemptsForActiveCode = 0
            recordAudit(.failure, detail: "Pairing code expired (device: \(deviceName))")
            return .failure(.expired)
        }

        // Constant-time comparison so a wrong submission can't be distinguished
        // from a near-match by response timing on the local network.
        if Self.constantTimeEqual(active.code, code) {
            let token = randomToken(length: 48)
            let device = PairedDevice(name: deviceName, token: token)
            pairedDevices.append(device)
            activePairingCode = nil
            failedAttemptsForActiveCode = 0
            save()
            recordAudit(.success, detail: "Paired device: \(deviceName)")
            return .success(device)
        }

        failedAttemptsForActiveCode += 1
        if failedAttemptsForActiveCode >= maxFailedAttemptsPerCode {
            activePairingCode = nil
            failedAttemptsForActiveCode = 0
            recordAudit(.lockout, detail: "Pairing code invalidated after \(maxFailedAttemptsPerCode) failed attempts (device: \(deviceName))")
            return .failure(.lockedOut)
        }
        recordAudit(.failure, detail: "Bad pairing code (attempt \(failedAttemptsForActiveCode)/\(maxFailedAttemptsPerCode), device: \(deviceName))")
        return .failure(.invalidCode)
    }

    /// Constant-time string comparison. Returns false on any length mismatch,
    /// otherwise XORs every byte so total time depends only on input length.
    /// Important: only use for fixed-format secrets (codes, tokens) where the
    /// length-mismatch leak is acceptable.
    static func constantTimeEqual(_ a: String, _ b: String) -> Bool {
        let aBytes = Array(a.utf8)
        let bBytes = Array(b.utf8)
        guard aBytes.count == bBytes.count else { return false }
        var diff: UInt8 = 0
        for i in 0..<aBytes.count {
            diff |= aBytes[i] ^ bBytes[i]
        }
        return diff == 0
    }

    private func recordAudit(_ kind: PairingAuditEntry.Kind, detail: String) {
        let entry = PairingAuditEntry(kind: kind, detail: detail)
        pairingAuditLog.append(entry)
        if pairingAuditLog.count > auditLogMaxEntries {
            pairingAuditLog.removeFirst(pairingAuditLog.count - auditLogMaxEntries)
        }
    }

    // MARK: - Auth lookup

    public func device(forToken token: String) -> PairedDevice? {
        pairedDevices.first { $0.token == token }
    }

    public func touch(deviceID: UUID, apnsToken: String? = nil) {
        guard let index = pairedDevices.firstIndex(where: { $0.id == deviceID }) else { return }
        var device = pairedDevices[index]
        device.lastSeenAt = Date()
        if let apnsToken { device.apnsPushToken = apnsToken }
        pairedDevices[index] = device
        save()
    }

    public func revoke(deviceID: UUID) {
        pairedDevices.removeAll { $0.id == deviceID }
        save()
    }

    // MARK: - Persistence

    private func load() {
        guard let data = try? Data(contentsOf: storageURL),
              let decoded = try? JSONDecoder().decode([PairedDevice].self, from: data) else { return }
        pairedDevices = decoded
    }

    private func save() {
        guard let data = try? JSONEncoder().encode(pairedDevices) else { return }
        try? data.write(to: storageURL, options: .atomic)
    }

    private func randomCode(length: Int) -> String {
        let alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"   // skip O/0/I/1
        return String((0..<length).map { _ in alphabet.randomElement()! })
    }

    private func randomToken(length: Int) -> String {
        var bytes = [UInt8](repeating: 0, count: length)
        _ = SecRandomCopyBytes(kSecRandomDefault, length, &bytes)
        return Data(bytes).base64EncodedString()
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "=", with: "")
    }
}
