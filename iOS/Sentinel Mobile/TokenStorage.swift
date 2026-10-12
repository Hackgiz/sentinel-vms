import Foundation
import Security

/// An access + refresh token pair for one site.
struct TokenPair: Codable, Hashable, Sendable {
    var accessToken: String
    var expiresAt: UnixMillis
    var refreshToken: String
    var refreshExpiresAt: UnixMillis
    var userName: String

    init(accessToken: String, expiresAt: UnixMillis, refreshToken: String, refreshExpiresAt: UnixMillis, userName: String) {
        self.accessToken = accessToken
        self.expiresAt = expiresAt
        self.refreshToken = refreshToken
        self.refreshExpiresAt = refreshExpiresAt
        self.userName = userName
    }

    init(_ response: V2.TokenResponse) {
        self.init(accessToken: response.accessToken, expiresAt: response.expiresAt,
                  refreshToken: response.refreshToken, refreshExpiresAt: response.refreshExpiresAt,
                  userName: response.user.name)
    }

    /// True when the access token expires within `margin` seconds.
    func accessExpires(within margin: TimeInterval, now: Date = Date()) -> Bool {
        Date(unixMillis: expiresAt).timeIntervalSince(now) < margin
    }

    func refreshExpired(now: Date = Date()) -> Bool {
        Date(unixMillis: refreshExpiresAt) <= now
    }
}

/// Where a site's tokens are kept between launches.
protocol TokenStoring: Sendable {
    func load(siteID: String) -> TokenPair?
    func save(_ tokens: TokenPair, siteID: String)
    func delete(siteID: String)
}

/// Tokens in the iOS Keychain, one generic-password item per site. The
/// refresh token is a 30-day credential, so it never goes to UserDefaults.
struct KeychainTokenStore: TokenStoring {
    static let defaultService = "com.handoffgrid.sentinel.mobile.site-tokens"
    private let service: String

    init(service: String = KeychainTokenStore.defaultService) {
        self.service = service
    }

    private func query(_ siteID: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: siteID
        ]
    }

    func load(siteID: String) -> TokenPair? {
        var q = query(siteID)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data else { return nil }
        return try? JSONDecoder().decode(TokenPair.self, from: data)
    }

    func save(_ tokens: TokenPair, siteID: String) {
        guard let data = try? JSONEncoder().encode(tokens) else { return }
        let update: [String: Any] = [kSecValueData as String: data]
        let status = SecItemUpdate(query(siteID) as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var add = query(siteID)
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            add[kSecAttrLabel as String] = "Sentinel Mobile sign-in"
            SecItemAdd(add as CFDictionary, nil)
        }
    }

    func delete(siteID: String) {
        SecItemDelete(query(siteID) as CFDictionary)
    }
}

/// In-memory token storage (tests, previews).
final class MemoryTokenStore: TokenStoring, @unchecked Sendable {
    private let lock = NSLock()
    private var tokens: [String: TokenPair] = [:]

    init() {}

    func load(siteID: String) -> TokenPair? {
        lock.lock(); defer { lock.unlock() }
        return tokens[siteID]
    }

    func save(_ tokens: TokenPair, siteID: String) {
        lock.lock(); defer { lock.unlock() }
        self.tokens[siteID] = tokens
    }

    func delete(siteID: String) {
        lock.lock(); defer { lock.unlock() }
        tokens.removeValue(forKey: siteID)
    }
}
