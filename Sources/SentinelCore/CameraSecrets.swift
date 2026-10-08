import Foundation
import LocalAuthentication
import Security

// Camera credentials are now kept in `SecretVault` (one encrypted file, unlocked
// by the operator login password) rather than the macOS login Keychain. The
// public API is unchanged so call sites don't care where the bytes live; the
// `context: LAContext?` parameter is accepted for source compatibility but is
// no longer needed (the operator password already unlocked the vault).
//
// Migration: the first time a value is read after upgrading, we transparently
// import it from the legacy login Keychain (using `kSecUseAuthenticationUISkip`,
// so this never shows the OS prompt) and delete the Keychain copy. After that
// the Keychain is never touched again.
public enum CameraSecrets {
    private static let service = "HandoffGridSentinel.CameraPassword"

    public static func savePassword(_ password: String, for cameraID: UUID) throws {
        SecretVault.shared.setValue(Data(password.utf8), service: service, account: cameraID.uuidString)
        legacyKeychainDelete(for: cameraID)
    }

    public static func password(for cameraID: UUID, context: LAContext? = nil) throws -> String? {
        if let data = SecretVault.shared.value(service: service, account: cameraID.uuidString) {
            return String(data: data, encoding: .utf8)
        }
        // Lazy, prompt-free migration from the legacy login Keychain.
        guard let legacy = legacyKeychainPassword(for: cameraID) else { return nil }
        if SecretVault.shared.isUnlocked {
            SecretVault.shared.setValue(Data(legacy.utf8), service: service, account: cameraID.uuidString)
            legacyKeychainDelete(for: cameraID)
        }
        return legacy
    }

    public static func hasPassword(for cameraID: UUID) -> Bool {
        if SecretVault.shared.value(service: service, account: cameraID.uuidString) != nil {
            return true
        }
        return legacyKeychainPassword(for: cameraID) != nil
    }

    public static func deletePassword(for cameraID: UUID) {
        SecretVault.shared.removeValue(service: service, account: cameraID.uuidString)
        legacyKeychainDelete(for: cameraID)
    }

    // MARK: Legacy login-Keychain access (read for one-time migration only)

    private static func legacyKeychainPassword(for cameraID: UUID) -> String? {
        var query = baseQuery(for: cameraID)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        query[kSecUseAuthenticationUI as String] = kSecUseAuthenticationUISkip
        return SecretVault.withoutLegacyKeychainPrompt {
            var item: CFTypeRef?
            guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
                  let data = item as? Data else { return nil }
            return String(data: data, encoding: .utf8)
        }
    }

    private static func legacyKeychainDelete(for cameraID: UUID) {
        SecretVault.withoutLegacyKeychainPrompt {
            SecItemDelete(baseQuery(for: cameraID) as CFDictionary)
        }
    }

    private static func baseQuery(for cameraID: UUID) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: cameraID.uuidString,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        ]
    }

    // MARK: - Remembered "add camera" credentials (prefill convenience)
    //
    // Most installs share one admin login across every camera. We remember the
    // last-used username + password (in the Keychain, never UserDefaults) so the
    // Add Camera sheet can pre-fill them and the user doesn't re-type for each
    // camera or each session.

    private static let rememberedService = "HandoffGridSentinel.RememberedCameraCredentials"
    private static let rememberedAccount = "default"

    public static func saveRememberedCredentials(username: String, password: String) {
        guard username.isEmpty == false || password.isEmpty == false,
              let data = try? JSONSerialization.data(withJSONObject: ["u": username, "p": password])
        else { return }
        SecretVault.shared.setValue(data, service: rememberedService, account: rememberedAccount)
        legacyDeleteRemembered()
    }

    public static func rememberedCredentials() -> (username: String, password: String)? {
        let data = SecretVault.shared.value(service: rememberedService, account: rememberedAccount)
            ?? legacyRememberedData()
        guard let data,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: String],
              let username = obj["u"], let password = obj["p"]
        else { return nil }
        return (username, password)
    }

    private static func legacyRememberedData() -> Data? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: rememberedService,
            kSecAttrAccount as String: rememberedAccount,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
            kSecUseAuthenticationUI as String: kSecUseAuthenticationUISkip
        ]
        let data: Data? = SecretVault.withoutLegacyKeychainPrompt {
            var item: CFTypeRef?
            guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
                  let data = item as? Data else { return nil }
            return data
        }
        guard let data else { return nil }
        if SecretVault.shared.isUnlocked {
            SecretVault.shared.setValue(data, service: rememberedService, account: rememberedAccount)
            legacyDeleteRemembered()
        }
        return data
    }

    private static func legacyDeleteRemembered() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: rememberedService,
            kSecAttrAccount as String: rememberedAccount
        ]
        SecretVault.withoutLegacyKeychainPrompt {
            SecItemDelete(query as CFDictionary)
        }
    }
}

public struct CameraSecretsError: LocalizedError {
    public let status: OSStatus

    public var errorDescription: String? {
        if let message = SecCopyErrorMessageString(status, nil) as String? {
            return message
        }

        return "Keychain error \(status)"
    }
}
