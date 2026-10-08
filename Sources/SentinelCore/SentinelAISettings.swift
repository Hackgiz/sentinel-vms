import Foundation

// MARK: - Bring-your-own-key AI settings
//
// The operator supplies their own Anthropic API key; Sentinel calls Claude
// directly (see `SentinelAIClient`). The key is a secret and lives in the
// encrypted SecretVault — never UserDefaults, never plaintext on disk. The
// "Enable AI" toggle is a plain preference (no secret), so it lives in
// UserDefaults and can be read before the vault is unlocked.
//
// AI is "available" only when BOTH a key is stored AND the toggle is on.
public enum SentinelAISettings {
    private static let vaultService = "HandoffGridSentinel.AnthropicKey"
    private static let vaultAccount = "anthropic"
    private static let enabledDefaultsKey = "SentinelAIEnabled"

    /// Posted whenever the key or the enabled toggle changes, so observers
    /// (LicenseStore) can refresh their cached availability flags.
    public static let didChange = Notification.Name("SentinelAISettingsDidChange")

    // MARK: API key (secret — in the vault)

    /// Stores (or clears, when nil/empty) the operator's Anthropic API key.
    public static func setAPIKey(_ key: String?) {
        let trimmed = key?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if trimmed.isEmpty {
            SecretVault.shared.removeValue(service: vaultService, account: vaultAccount)
        } else {
            SecretVault.shared.setValue(Data(trimmed.utf8), service: vaultService, account: vaultAccount)
        }
        NotificationCenter.default.post(name: didChange, object: nil)
    }

    /// The stored API key, or nil if none is set (or the vault is locked).
    public static func apiKey() -> String? {
        guard let data = SecretVault.shared.value(service: vaultService, account: vaultAccount),
              let key = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
              !key.isEmpty else { return nil }
        return key
    }

    /// Whether a non-empty key is currently stored.
    public static var hasAPIKey: Bool { apiKey() != nil }

    // MARK: Enabled toggle (preference — in UserDefaults)

    /// Whether the operator has switched AI features on. Defaults to false so a
    /// fresh install (or one with no key) never calls out to Anthropic silently.
    public static var isEnabled: Bool {
        get { UserDefaults.standard.bool(forKey: enabledDefaultsKey) }
        set {
            UserDefaults.standard.set(newValue, forKey: enabledDefaultsKey)
            NotificationCenter.default.post(name: didChange, object: nil)
        }
    }

    /// AI features run only when a key is present AND the toggle is on.
    public static var isAvailable: Bool { isEnabled && hasAPIKey }

    /// A client bound to the stored key, or nil if no key is set.
    public static func makeClient(session: URLSession = .shared) -> SentinelAIClient? {
        guard let key = apiKey() else { return nil }
        return SentinelAIClient(apiKey: key, session: session)
    }
}
