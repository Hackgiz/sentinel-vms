import Foundation

/// A management server the operator has signed in to.
public struct SavedSite: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var name: String
    /// The management server's address, e.g. `https://sentinel.example.com` or `http://192.168.1.20:8090`.
    public var address: String
    /// Name last used to sign in (pre-fills the sign-in form).
    public var lastUserName: String?
    /// True for the server this Mac hosts (installed from Sentinel Operator).
    public var isLocal: Bool
    public var lastUsedAt: Date?

    public init(id: String = UUID().uuidString, name: String, address: String, lastUserName: String? = nil,
                isLocal: Bool = false, lastUsedAt: Date? = nil) {
        self.id = id
        self.name = name
        self.address = address
        self.lastUserName = lastUserName
        self.isLocal = isLocal
        self.lastUsedAt = lastUsedAt
    }

    public var url: URL? { SavedSite.normalizedURL(address) }

    /// Turns what someone typed ("192.168.1.20", "nvr.local:8090",
    /// "https://sentinel.example.com/") into a base URL. Plain hosts get
    /// `http://` and the server's default port 8090 when none is given.
    public static func normalizedURL(_ typed: String) -> URL? {
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
}

/// The saved sites list, persisted in UserDefaults (addresses and names
/// only — tokens live in the Keychain, see `KeychainTokenStore`).
public final class SiteDirectory: ObservableObject {
    @Published public private(set) var sites: [SavedSite] = []
    @Published public var currentSiteID: String? {
        didSet { defaults.set(currentSiteID, forKey: currentKey) }
    }

    private let defaults: UserDefaults
    private let sitesKey = "sentinel.operator.sites"
    private let currentKey = "sentinel.operator.currentSite"

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        if let data = defaults.data(forKey: sitesKey),
           let decoded = try? JSONDecoder().decode([SavedSite].self, from: data) {
            sites = decoded
        }
        currentSiteID = defaults.string(forKey: currentKey)
    }

    public var currentSite: SavedSite? {
        sites.first { $0.id == currentSiteID }
    }

    public func site(id: String) -> SavedSite? {
        sites.first { $0.id == id }
    }

    /// The saved site for an address, if any (matched on the normalized URL).
    public func site(forAddress address: String) -> SavedSite? {
        guard let url = SavedSite.normalizedURL(address) else { return nil }
        return sites.first { $0.url == url }
    }

    /// Adds or updates a site (matched by id).
    public func upsert(_ site: SavedSite) {
        if let index = sites.firstIndex(where: { $0.id == site.id }) {
            sites[index] = site
        } else {
            sites.append(site)
        }
        save()
    }

    public func remove(_ site: SavedSite, tokenStore: TokenStoring) {
        sites.removeAll { $0.id == site.id }
        tokenStore.delete(siteID: site.id)
        if currentSiteID == site.id { currentSiteID = nil }
        save()
    }

    private func save() {
        if let data = try? JSONEncoder().encode(sites) {
            defaults.set(data, forKey: sitesKey)
        }
    }
}
