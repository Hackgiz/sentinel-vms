// SiteDirectory.swift
// The Management Servers this phone knows: address, display name and an
// optional secondary (remote) address. Tokens are NOT stored here; they live
// in the Keychain (see KeychainTokenStore).

import Foundation
import Security

/// Where a site's remote address came from. WAVE12(D)
enum RemoteAddressSource: String, Codable, Hashable, Sendable {
    /// Typed or edited by a person: never replaced automatically.
    case manual
    /// From the Management Server's pairing QR code.
    case qr
    /// Reported by the Management Server (`GET /api/v2/cloud/remote`).
    case server
}

/// A Management Server the phone has been set up for.
struct SavedSite: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    /// The Management Server's address, e.g. `https://sentinel.example.com` or `http://192.168.1.20:8090`.
    var address: String
    /// Secondary address tried when `address` doesn't answer (a Cloudflare tunnel).
    var remoteAddress: String?
    /// Where `remoteAddress` came from; see `remoteSource`. WAVE12(D)
    var remoteAddressSource: RemoteAddressSource?
    /// Name last used to sign in (pre-fills the sign-in form).
    var lastUserName: String?
    var lastUsedAt: Date?

    init(id: String = UUID().uuidString, name: String, address: String, remoteAddress: String? = nil,
         lastUserName: String? = nil, lastUsedAt: Date? = nil,
         remoteAddressSource: RemoteAddressSource? = nil) { // WAVE12(D)
        self.id = id
        self.name = name
        self.address = address
        self.remoteAddress = remoteAddress
        self.lastUserName = lastUserName
        self.lastUsedAt = lastUsedAt
        self.remoteAddressSource = remoteAddressSource // WAVE12(D)
    }

    var url: URL? { SavedSite.normalizedURL(address) }

    var remoteURL: URL? {
        guard let remoteAddress, remoteAddress.trimmingCharacters(in: .whitespaces).isEmpty == false else { return nil }
        return SavedSite.normalizedURL(remoteAddress)
    }

    /// Where the remote address came from; nil when there is none. A saved
    /// address from before this was tracked counts as typed by hand. WAVE12(D)
    var remoteSource: RemoteAddressSource? {
        guard remoteURL != nil else { return nil }
        return remoteAddressSource ?? .manual
    }

    /// What to call the site in lists when no name was given.
    var displayName: String {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty == false { return trimmed }
        return url?.host ?? address
    }

    /// Turns what someone typed ("192.168.1.20", "nvr.local:8090",
    /// "https://sentinel.example.com/") into a base URL. Plain hosts get
    /// `http://` and the server's default port 8090 when none is given.
    static func normalizedURL(_ typed: String) -> URL? {
        var text = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        guard text.isEmpty == false else { return nil }
        if text.contains("://") == false {
            text = "http://" + text
        }
        guard var components = URLComponents(string: text),
              let scheme = components.scheme?.lowercased(), scheme == "http" || scheme == "https",
              let host = components.host, host.isEmpty == false else { return nil }
        components.scheme = scheme
        if components.port == nil, scheme == "http", typed.contains("://") == false {
            components.port = 8090
        }
        while components.path.hasSuffix("/") { components.path.removeLast() }
        components.query = nil
        components.fragment = nil
        return components.url
    }

    /// The site address inside a pairing QR code. Accepts the Management
    /// Server's `sentinel-vms://pair?<percent-encoded JSON {url, code, v,
    /// remoteURL?}>` payload (the pairing code itself is not used), or a plain
    /// http(s) address.
    static func fromQR(_ payload: String) -> (address: String, remote: String?)? {
        let text = payload.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.hasPrefix("http://") || text.hasPrefix("https://") {
            return normalizedURL(text).map { ($0.absoluteString, nil) }
        }
        guard text.hasPrefix("sentinel-vms://pair?") else { return nil }
        let json = String(text.dropFirst("sentinel-vms://pair?".count)).removingPercentEncoding ?? ""
        guard let data = json.data(using: .utf8),
              let dict = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let url = (dict["url"] as? String).flatMap(normalizedURL) else { return nil }
        var remote: String?
        if let r = (dict["remoteURL"] as? String).flatMap(normalizedURL), r.scheme == "https" {
            remote = r.absoluteString
        }
        return (url.absoluteString, remote)
    }
}

/// The saved sites list, persisted in UserDefaults (addresses and names only).
@MainActor
final class SiteDirectory: ObservableObject {
    @Published private(set) var sites: [SavedSite] = []
    @Published var currentSiteID: String? {
        didSet { defaults.set(currentSiteID, forKey: Self.currentKey) }
    }

    private let defaults: UserDefaults
    static let sitesKey = "sentinel.mobile.sites"
    static let currentKey = "sentinel.mobile.currentSite"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        if let data = defaults.data(forKey: Self.sitesKey),
           let decoded = try? JSONDecoder().decode([SavedSite].self, from: data) {
            sites = decoded
        }
        currentSiteID = defaults.string(forKey: Self.currentKey)
        migrateLegacyPairing()
    }

    var currentSite: SavedSite? {
        sites.first { $0.id == currentSiteID }
    }

    func site(id: String) -> SavedSite? {
        sites.first { $0.id == id }
    }

    /// The saved site for an address, if any (matched on the normalized URL).
    func site(forAddress address: String) -> SavedSite? {
        guard let url = SavedSite.normalizedURL(address) else { return nil }
        return sites.first { $0.url == url }
    }

    /// Adds or updates a site (matched by id).
    func upsert(_ site: SavedSite) {
        if let index = sites.firstIndex(where: { $0.id == site.id }) {
            sites[index] = site
        } else {
            sites.append(site)
        }
        save()
    }

    func remove(_ site: SavedSite, tokenStore: TokenStoring) {
        sites.removeAll { $0.id == site.id }
        tokenStore.delete(siteID: site.id)
        if currentSiteID == site.id { currentSiteID = nil }
        save()
    }

    private func save() {
        if let data = try? JSONEncoder().encode(sites) {
            defaults.set(data, forKey: Self.sitesKey)
        }
    }

    // MARK: Migration

    /// Keys of the pre-2.0 companion pairing (one server, a pairing token).
    private static let legacyURLKey = "sentinel.serverURL"
    private static let legacyRemoteKey = "sentinel.remoteURL"
    private static let legacyNameKey = "sentinel.serverName"
    private static let legacyKeychainService = "com.handoffgrid.sentinel.mobile"

    /// An upgraded install keeps its server: the old pairing becomes a saved
    /// site (the pairing token is dropped; the user signs in once).
    private func migrateLegacyPairing() {
        guard let address = defaults.string(forKey: Self.legacyURLKey) else { return }
        if site(forAddress: address) == nil, SavedSite.normalizedURL(address) != nil {
            let site = SavedSite(name: defaults.string(forKey: Self.legacyNameKey) ?? "",
                                 address: address,
                                 remoteAddress: defaults.string(forKey: Self.legacyRemoteKey))
            sites.append(site)
            if currentSiteID == nil { currentSiteID = site.id }
            save()
        }
        for key in [Self.legacyURLKey, Self.legacyRemoteKey, Self.legacyNameKey] {
            defaults.removeObject(forKey: key)
        }
        SecItemDelete([
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.legacyKeychainService,
            kSecAttrAccount as String: "auth"
        ] as CFDictionary)
    }
}
