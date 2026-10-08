import Foundation
import CryptoKit
import Security

// MARK: - Secret vault
//
// A single, password-derived encrypted store for every local secret the app
// keeps: per-camera RTSP passwords, the remembered "add camera" credentials,
// and the cloud (Supabase) session token. It exists so Sentinel needs exactly
// ONE password — the operator login — and never triggers a macOS *login
// Keychain* authorization prompt (which used to appear at launch when the app's
// ad-hoc code signature didn't match the Keychain item's access list).
//
// Design
// ------
//   • A random 256-bit *master key* `K` actually encrypts the secret payload
//     (AES-GCM). `K` is never derived from a password directly.
//   • Each operator who may unlock the vault gets a *key slot*: `K` sealed under
//     a key derived (PBKDF2-HMAC-SHA256) from that operator's password + a
//     per-slot random salt. This lets multiple operators share one secret set,
//     and lets a password change re-wrap a single slot without re-encrypting the
//     payload.
//   • On disk: one JSON file in Application Support. Plaintext secrets exist only
//     in memory, only while unlocked. No Keychain involved.
//
// Thread-safety: all mutable state is guarded by a lock so the static
// `CameraSecrets` helpers (called from several actors) can use it freely.
public final class SecretVault {
    public static let shared = SecretVault()

    /// Posted (on the main queue) right after a successful unlock so dependent
    /// stores — the license store, the camera-credential gate — can react.
    public static let didUnlock = Notification.Name("SentinelSecretVaultDidUnlock")

    private let lock = NSRecursiveLock()
    private let fileURL: URL

    private var masterKey: SymmetricKey?
    private var secrets: [String: Data] = [:]
    private var slots: [String: Slot] = [:]
    private var sealedPayload: Data?
    private var unlocked = false

    // PBKDF2 work factor. The legacy operator gate stored a bare SHA256 of the
    // password, so even a modest stretch here is a strict improvement; 120k
    // keeps a pure-CryptoKit derivation well under a second at login time.
    private static let pbkdf2Rounds = 120_000

    private struct Slot: Codable {
        var salt: Data
        var wrapped: Data   // K sealed (AES-GCM combined) under the password key
    }

    private struct Disk: Codable {
        var version: Int
        var slots: [String: Slot]
        var payload: Data?  // secrets dict sealed (AES-GCM combined) under K
    }

    public init() {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        fileURL = dir.appendingPathComponent("secrets.vault")
        loadDisk()
    }

    // MARK: State

    public var isUnlocked: Bool {
        lock.lock(); defer { lock.unlock() }
        return unlocked
    }

    /// Whether a vault already exists on disk (any operator has a key slot).
    public var exists: Bool {
        lock.lock(); defer { lock.unlock() }
        return slots.isEmpty == false
    }

    // MARK: Unlock / lock

    /// Unlock with an operator's password.
    ///
    /// - If this operator already has a key slot, their password unwraps the
    ///   master key.
    /// - If no vault exists yet (first run, or first launch after upgrade), a
    ///   fresh vault + slot is created for this operator.
    /// - If a vault exists but this operator has no slot and we can't open it,
    ///   unlock fails (an admin must provision the slot while unlocked).
    @discardableResult
    public func unlock(userKey: String, password: String) -> Bool {
        guard password.isEmpty == false else { return false }
        lock.lock()
        let slotID = Self.normalize(userKey)

        if let slot = slots[slotID] {
            let kek = Self.derive(password: password, salt: slot.salt)
            guard let master = Self.unwrap(slot.wrapped, with: kek) else {
                lock.unlock()
                return false
            }
            adoptLocked(master: master)
            lock.unlock()
            postUnlock()
            return true
        }

        if slots.isEmpty {
            // Brand-new vault for the first operator.
            let master = SymmetricKey(size: .bits256)
            masterKey = master
            secrets = [:]
            addSlotLocked(slotID: slotID, password: password, master: master)
            unlocked = true
            persistLocked()
            lock.unlock()
            postUnlock()
            return true
        }

        // Vault exists but not for this operator, and we have no master key to
        // wrap a new slot. Cannot unlock.
        lock.unlock()
        return false
    }

    /// Re-lock the vault (e.g. on operator logout): clears the master key and
    /// decrypted secrets from memory. The on-disk file is untouched.
    public func lockVault() {
        lock.lock(); defer { lock.unlock() }
        masterKey = nil
        secrets = [:]
        unlocked = false
    }

    /// Add (or re-wrap) a key slot for an operator. Requires the vault to be
    /// unlocked — used when an admin sets another operator's password, or when an
    /// operator changes their own password. Re-wrapping the *current* operator's
    /// slot is exactly a password change.
    @discardableResult
    public func provisionSlot(userKey: String, password: String) -> Bool {
        guard password.isEmpty == false else { return false }
        lock.lock(); defer { lock.unlock() }
        guard let master = masterKey else { return false }
        addSlotLocked(slotID: Self.normalize(userKey), password: password, master: master)
        persistLocked()
        return true
    }

    public func removeSlot(userKey: String) {
        lock.lock(); defer { lock.unlock() }
        slots.removeValue(forKey: Self.normalize(userKey))
        persistLocked()
    }

    // MARK: Secret access

    public func value(service: String, account: String) -> Data? {
        lock.lock(); defer { lock.unlock() }
        guard unlocked else { return nil }
        return secrets[Self.itemKey(service, account)]
    }

    public func setValue(_ data: Data, service: String, account: String) {
        lock.lock(); defer { lock.unlock() }
        guard unlocked else { return }
        secrets[Self.itemKey(service, account)] = data
        persistLocked()
    }

    public func removeValue(service: String, account: String) {
        lock.lock(); defer { lock.unlock() }
        guard unlocked else { return }
        secrets.removeValue(forKey: Self.itemKey(service, account))
        persistLocked()
    }

    // MARK: - Internals (call with `lock` held unless noted)

    private func adoptLocked(master: SymmetricKey) {
        masterKey = master
        if let payload = sealedPayload,
           let box = try? AES.GCM.SealedBox(combined: payload),
           let blob = try? AES.GCM.open(box, using: master),
           let dict = try? JSONDecoder().decode([String: Data].self, from: blob) {
            secrets = dict
        } else {
            secrets = [:]
        }
        unlocked = true
    }

    private func addSlotLocked(slotID: String, password: String, master: SymmetricKey) {
        let salt = Self.randomData(16)
        let kek = Self.derive(password: password, salt: salt)
        guard let wrapped = Self.wrap(master, with: kek) else { return }
        slots[slotID] = Slot(salt: salt, wrapped: wrapped)
    }

    private func persistLocked() {
        var payload: Data?
        if let master = masterKey, let blob = try? JSONEncoder().encode(secrets) {
            payload = try? AES.GCM.seal(blob, using: master).combined
            sealedPayload = payload
        }
        let disk = Disk(version: 1, slots: slots, payload: payload ?? sealedPayload)
        if let data = try? JSONEncoder().encode(disk) {
            try? data.write(to: fileURL, options: [.atomic])
        }
    }

    private func loadDisk() {
        guard let data = try? Data(contentsOf: fileURL),
              let disk = try? JSONDecoder().decode(Disk.self, from: data) else { return }
        slots = disk.slots
        sealedPayload = disk.payload
    }

    private func postUnlock() {
        // Always hop to the main queue so SwiftUI observers update safely,
        // regardless of which thread unlocked the vault.
        if Thread.isMainThread {
            NotificationCenter.default.post(name: Self.didUnlock, object: nil)
        } else {
            DispatchQueue.main.async {
                NotificationCenter.default.post(name: Self.didUnlock, object: nil)
            }
        }
    }

    // MARK: Crypto helpers

    /// PBKDF2-HMAC-SHA256 implemented on CryptoKit's HMAC so we take no
    /// CommonCrypto dependency. dkLen == hLen (32 bytes) → a single block.
    private static func derive(password: String, salt: Data) -> SymmetricKey {
        let pwKey = SymmetricKey(data: Data(password.utf8))
        var block = salt
        block.append(contentsOf: [0, 0, 0, 1]) // INT(1), big-endian block index
        var u = Data(HMAC<SHA256>.authenticationCode(for: block, using: pwKey))
        var result = u
        for _ in 1..<pbkdf2Rounds {
            u = Data(HMAC<SHA256>.authenticationCode(for: u, using: pwKey))
            for i in 0..<result.count { result[i] ^= u[i] }
        }
        return SymmetricKey(data: result)
    }

    private static func wrap(_ key: SymmetricKey, with kek: SymmetricKey) -> Data? {
        let raw = key.withUnsafeBytes { Data(bytes: $0.baseAddress!, count: $0.count) }
        return try? AES.GCM.seal(raw, using: kek).combined
    }

    private static func unwrap(_ data: Data, with kek: SymmetricKey) -> SymmetricKey? {
        guard let box = try? AES.GCM.SealedBox(combined: data),
              let raw = try? AES.GCM.open(box, using: kek) else { return nil }
        return SymmetricKey(data: raw)
    }

    private static func randomData(_ count: Int) -> Data {
        var bytes = [UInt8](repeating: 0, count: count)
        _ = SecRandomCopyBytes(kSecRandomDefault, count, &bytes)
        return Data(bytes)
    }

    private static func normalize(_ userKey: String) -> String {
        userKey.lowercased()
    }

    // MARK: Legacy-Keychain migration helper

    /// Runs `body` with the legacy macOS Keychain's interactive prompts disabled.
    /// A read of an item whose access list distrusts the current (ad-hoc) code
    /// signature then fails silently with `errSecInteractionNotAllowed` instead
    /// of showing the "wants to use your confidential information" dialog —
    /// `kSecUseAuthenticationUISkip` does NOT suppress that legacy ACL prompt.
    /// Used only by the one-time migration paths that drain the old Keychain
    /// into this vault.
    public static func withoutLegacyKeychainPrompt<T>(_ body: () -> T) -> T {
        #if os(macOS)
        SecKeychainSetUserInteractionAllowed(false)
        defer { SecKeychainSetUserInteractionAllowed(true) }
        #endif
        return body()
    }

    private static func itemKey(_ service: String, _ account: String) -> String {
        service + "\u{1f}" + account
    }
}
