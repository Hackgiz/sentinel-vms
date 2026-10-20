// BackupAccess.swift
// Sentinel Cloud on the phone, the parts that aren't screens:
//  - the read-only `scb_` backup token the Management Server hands a
//    signed-in phone (`GET /api/v2/cloud/backup-access`), kept in the Keychain
//    so the offsite backup stays browsable when the server is off or gone,
//    renewed when it has less than 7 days left, dropped on sign-out;
//  - `BackupClient`, which reads the backup straight from Sentinel Cloud
//    (`GET /v1/backup/objects`, `GET /v1/backup/objects/{key}`);
//  - `CloudSync`, run after a sign-in or a resumed sign-in: saves the
//    server's remote address (`GET /api/v2/cloud/remote`) as the site's
//    secondary address and renews the backup token.

import Foundation
import Security

// MARK: - API v2 calls

extension SentinelAPI {
    /// `GET /api/v2/cloud/remote` (viewLive).
    func cloudRemote() async throws -> V2.CloudRemote {
        try await send("GET", "/api/v2/cloud/remote")
    }

    /// `GET /api/v2/cloud/backup-access` (viewLive). 404 when backup is off or
    /// the server isn't linked to Sentinel Cloud.
    func cloudBackupAccess() async throws -> V2.CloudBackupAccess {
        try await send("GET", "/api/v2/cloud/backup-access")
    }
}

// MARK: - Token storage

/// Where a site's backup token is kept between launches (one per site).
protocol BackupTokenStoring: Sendable {
    func load(siteID: String) -> V2.CloudBackupAccess?
    func save(_ grant: V2.CloudBackupAccess, siteID: String)
    func delete(siteID: String)
}

/// Backup tokens in the iOS Keychain, one generic-password item per site,
/// next to (not inside) the site's sign-in tokens.
struct KeychainBackupTokenStore: BackupTokenStoring {
    static let defaultService = "com.handoffgrid.sentinel.mobile.backup-tokens"
    private let service: String

    init(service: String = KeychainBackupTokenStore.defaultService) {
        self.service = service
    }

    private func query(_ siteID: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: siteID
        ]
    }

    func load(siteID: String) -> V2.CloudBackupAccess? {
        var q = query(siteID)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data else { return nil }
        return try? JSONDecoder().decode(V2.CloudBackupAccess.self, from: data)
    }

    func save(_ grant: V2.CloudBackupAccess, siteID: String) {
        guard let data = try? JSONEncoder().encode(grant) else { return }
        let update: [String: Any] = [kSecValueData as String: data]
        let status = SecItemUpdate(query(siteID) as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var add = query(siteID)
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            add[kSecAttrLabel as String] = "Sentinel Mobile offsite backup"
            SecItemAdd(add as CFDictionary, nil)
        }
    }

    func delete(siteID: String) {
        SecItemDelete(query(siteID) as CFDictionary)
    }
}

/// In-memory backup tokens (tests, previews).
final class MemoryBackupTokenStore: BackupTokenStoring, @unchecked Sendable {
    private let lock = NSLock()
    private var grants: [String: V2.CloudBackupAccess] = [:]

    init() {}

    func load(siteID: String) -> V2.CloudBackupAccess? {
        lock.lock(); defer { lock.unlock() }
        return grants[siteID]
    }

    func save(_ grant: V2.CloudBackupAccess, siteID: String) {
        lock.lock(); defer { lock.unlock() }
        grants[siteID] = grant
    }

    func delete(siteID: String) {
        lock.lock(); defer { lock.unlock() }
        grants.removeValue(forKey: siteID)
    }
}

// MARK: - The current site's backup token

/// The backup token of the site the phone is signed in to.
@MainActor
final class BackupAccess: ObservableObject {
    static let shared = BackupAccess()

    /// Renew the token when it has less than this left (tokens live 30 days).
    nonisolated static let renewalWindow: TimeInterval = 7 * 24 * 3600

    /// The token for `siteID`, when there is a usable one.
    @Published private(set) var grant: V2.CloudBackupAccess?
    @Published private(set) var siteID: String?

    private let store: BackupTokenStoring
    private var renewing: Set<String> = []

    init(store: BackupTokenStoring = KeychainBackupTokenStore()) {
        self.store = store
    }

    /// True when the token is missing or has less than 7 days left.
    nonisolated static func needsRenewal(_ grant: V2.CloudBackupAccess?, now: Date = Date()) -> Bool {
        guard let grant else { return true }
        return grant.expiresAt.timeIntervalSince(now) < renewalWindow
    }

    /// True while the token can still open the backup.
    nonisolated static func isUsable(_ grant: V2.CloudBackupAccess?, now: Date = Date()) -> Bool {
        guard let grant, grant.token.isEmpty == false else { return false }
        return grant.expiresAt > now
    }

    /// The usable token for `siteID`, if that's the active site.
    func grant(forSite siteID: String?, now: Date = Date()) -> V2.CloudBackupAccess? {
        guard let siteID, siteID == self.siteID, Self.isUsable(grant, now: now) else { return nil }
        return grant
    }

    /// Makes `siteID` the active site and loads its saved token (works offline).
    func activate(siteID: String, now: Date = Date()) {
        var saved = store.load(siteID: siteID)
        if let token = saved, Self.isUsable(token, now: now) == false {
            store.delete(siteID: siteID)
            saved = nil
        }
        self.siteID = siteID
        grant = saved
    }

    /// Asks the Management Server for a fresh token when the saved one is
    /// missing or within 7 days of expiry. "Backup is off" or "not linked"
    /// (404) and "not allowed" (403) drop the saved token; a server that
    /// can't be reached leaves it alone.
    func refreshIfNeeded(api: SentinelAPI, now: Date = Date()) async {
        let siteID = api.siteID
        if self.siteID != siteID { activate(siteID: siteID, now: now) }
        guard Self.needsRenewal(grant, now: now), renewing.contains(siteID) == false else { return }
        renewing.insert(siteID)
        defer { renewing.remove(siteID) }
        do {
            let fresh = try await api.cloudBackupAccess()
            guard fresh.token.isEmpty == false else { return }
            store.save(fresh, siteID: siteID)
            if self.siteID == siteID { grant = fresh }
        } catch let error as SentinelAPIError {
            if error.isNotFound || error.isForbidden { clear(siteID: siteID) }
        } catch {
            // Cancelled or unexpected: keep what we have.
        }
    }

    /// Forgets a site's token (sign-out, sign-in ended, site removed).
    func clear(siteID: String) {
        store.delete(siteID: siteID)
        if self.siteID == siteID { grant = nil }
    }
}

// MARK: - Reading the backup

/// Reads one server's offsite backup from Sentinel Cloud with its `scb_`
/// token. JSON and images go with `Authorization: Bearer`; clips are played
/// by AVPlayer, which can't set headers, so their URL carries `?token=`.
struct BackupClient: Sendable {
    let grant: V2.CloudBackupAccess
    let session: URLSession

    static let pageSize = 200

    init(grant: V2.CloudBackupAccess, session: URLSession = .shared) {
        self.grant = grant
        self.session = session
    }

    /// `grant.baseURL` without a trailing slash, when it's an http(s) address.
    var base: URL? {
        var text = grant.baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
        while text.hasSuffix("/") { text.removeLast() }
        guard let url = URL(string: text), let scheme = url.scheme?.lowercased(),
              scheme == "https" || scheme == "http", url.host?.isEmpty == false else { return nil }
        return url
    }

    /// `{base}/objects?after=<key>&limit=200`
    func listURL(after: String? = nil, limit: Int = BackupClient.pageSize) -> URL? {
        guard let base, var components = URLComponents(string: base.absoluteString + "/objects") else { return nil }
        var items: [URLQueryItem] = []
        if let after, after.isEmpty == false { items.append(URLQueryItem(name: "after", value: after)) }
        items.append(URLQueryItem(name: "limit", value: String(limit)))
        components.queryItems = items
        return components.url
    }

    /// `{base}/objects/<key>`: each key segment escaped, the slashes kept.
    func objectURL(key: String) -> URL? {
        guard let base else { return nil }
        return URL(string: base.absoluteString + "/objects/" + Self.escapeKey(key))
    }

    /// `objectURL` with the token as `?token=`, for AVPlayer.
    func playbackURL(key: String) -> URL? {
        guard let url = objectURL(key: key), var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
        components.percentEncodedQueryItems = [URLQueryItem(name: "token", value: Self.escapeQueryValue(grant.token))]
        return components.url
    }

    static func escapeKey(_ key: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#;%")
        return key.split(separator: "/", omittingEmptySubsequences: false)
            .map { $0.addingPercentEncoding(withAllowedCharacters: allowed) ?? String($0) }
            .joined(separator: "/")
    }

    /// Strict query escaping (the token is base64url, but never trust that).
    static func escapeQueryValue(_ value: String) -> String {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
    }

    // MARK: Requests

    func list(after: String? = nil) async throws -> Cloud.BackupListing {
        guard let url = listURL(after: after) else { throw Self.badAddress }
        let data = try await get(url, accept: "application/json")
        return try SentinelAPI.decode(Cloud.BackupListing.self, from: data)
    }

    func data(key: String) async throws -> Data {
        guard let url = objectURL(key: key) else { throw Self.badAddress }
        return try await get(url, accept: "*/*")
    }

    func alarmRecord(key: String) async throws -> Cloud.BackupAlarmRecord {
        try SentinelAPI.decode(Cloud.BackupAlarmRecord.self, from: try await data(key: key))
    }

    private func get(_ url: URL, accept: String) async throws -> Data {
        var request = URLRequest(url: url, timeoutInterval: 30)
        request.httpMethod = "GET"
        request.setValue(accept, forHTTPHeaderField: "Accept")
        request.setValue("Bearer \(grant.token)", forHTTPHeaderField: "Authorization")
        let data: Data, response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            if error.code == .cancelled { throw CancellationError() }
            throw SentinelAPIError.transport(message: Self.describe(error))
        }
        guard let http = response as? HTTPURLResponse else {
            throw SentinelAPIError.transport(message: "Sentinel Cloud's answer wasn't HTTP.")
        }
        guard (200..<300).contains(http.statusCode) else {
            throw SentinelAPIError.from(status: http.statusCode, body: data)
        }
        return data
    }

    static let badAddress = SentinelAPIError.transport(message: "The offsite backup address isn't valid. Sign in to your site again to renew it.")

    static func describe(_ error: URLError) -> String {
        switch error.code {
        case .notConnectedToInternet, .networkConnectionLost, .dataNotAllowed, .internationalRoamingOff:
            return "This iPhone is offline."
        case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed, .timedOut:
            return "Can't reach Sentinel Cloud right now. Try again in a moment."
        default:
            return error.localizedDescription
        }
    }

    /// What to tell a person when reading the backup failed.
    static func message(for error: Error) -> String {
        if let api = error as? SentinelAPIError {
            if api.isUnauthorized || api.isForbidden {
                return "This offsite backup link has expired. Connect to your site and sign in again to renew it."
            }
            if api.isNotFound { return "That item is no longer in the offsite backup." }
            return api.localizedDescription
        }
        return error.localizedDescription
    }
}

// MARK: - After sign-in

/// What the phone does with Sentinel Cloud after a sign-in or a resumed sign-in.
enum CloudSync {
    /// The remote address to save for a site, or nil to leave it as it is.
    /// The server's address (https only, as from a QR code) is saved when it
    /// differs from the saved one and
    ///  - the site has no remote address, or
    ///  - the saved one came from the server too, or
    ///  - the server's is a Sentinel Cloud address (`via: "cloud"`), which
    ///    is stable and beats a scanned quick-tunnel address;
    /// but never over an address someone typed (`manual`, which includes
    /// addresses saved before the source was tracked). A null never clears
    /// anything.
    static func remoteAddressToSave(for site: SavedSite, reported: V2.CloudRemote) -> String? {
        guard let text = reported.remoteURL?.trimmingCharacters(in: .whitespacesAndNewlines),
              text.lowercased().hasPrefix("https://"),
              let url = SavedSite.normalizedURL(text) else { return nil }
        guard let current = site.remoteURL else { return url.absoluteString }
        if current.absoluteString.lowercased() == url.absoluteString.lowercased() { return nil }
        switch site.remoteSource {
        case .server?: return url.absoluteString
        case .qr?: return reported.isCloud ? url.absoluteString : nil
        case .manual?, nil: return nil
        }
    }

    /// Asks the server for its remote address; the site with that address
    /// (marked as from the server) when it should be saved, otherwise nil
    /// (also when the server is too old to know, or can't be reached).
    static func updatedSite(_ site: SavedSite, api: SentinelAPI) async -> SavedSite? {
        guard let remote = try? await api.cloudRemote(),
              let address = remoteAddressToSave(for: site, reported: remote) else { return nil }
        var updated = site
        updated.remoteAddress = address
        updated.remoteAddressSource = .server
        return updated
    }

    /// Waits out a refresh in flight on `api` (also a forced one, which a
    /// plain token request wouldn't join), then leaves its access token good
    /// for at least `SentinelAPI.refreshMargin`: the store holds the newest
    /// tokens and the old client has no reason to refresh again.
    static func settleTokens(of api: SentinelAPI) async {
        await api.settleRefresh()
        _ = try? await api.accessToken()
    }
}

extension SentinelSession {
    /// The phone is on the site's secondary address and it's a Sentinel Cloud relay.
    var isViaSentinelCloud: Bool {
        guard isDemoMode == false, let addresses = api?.addresses else { return false }
        return isUsingRemote && Cloud.isViaRelay(isUsingSecondary: addresses.isUsingSecondary, secondary: addresses.secondary)
    }

    /// Run after a sign-in or a resumed sign-in (WAVE12(D) hook in `loadMe`):
    /// loads the site's saved backup token (works offline), then, when the
    /// server answered, saves its remote address and renews the backup token.
    func syncCloud(api: SentinelAPI) async {
        guard isDemoMode == false, currentSite?.id == api.siteID else { return }
        BackupAccess.shared.activate(siteID: api.siteID)
        // `me` didn't load: the server can't be reached, so don't ask it more.
        guard me != nil else { return }
        if let site = currentSite, let updated = await CloudSync.updatedSite(site, api: api),
           self.api === api, currentSite?.id == updated.id {
            await adoptRemoteAddress(updated)
        }
        await BackupAccess.shared.refreshIfNeeded(api: self.api ?? api)
    }

    /// Saves a new remote address for the current site and switches to it
    /// now, with a fresh client whose secondary address is the new one.
    ///
    /// One live client per site: two would each rotate the refresh token,
    /// and the server ends a sign-in whose refresh token is presented twice.
    /// So the hand-over is ordered: the event stream (the old client's only
    /// long-lived user) stops first; then any refresh in flight on the old
    /// client finishes and lands in the token store; only then is the fresh
    /// client made (it reads the tokens from the store) and the stream
    /// started on it. Nothing else keeps the old client past a request.
    func adoptRemoteAddress(_ site: SavedSite) async {
        let old = api
        let wasSignedIn = isSignedIn
        if wasSignedIn {
            AppStore.shared.stopAuto()
            if let old { await CloudSync.settleTokens(of: old) }
            // Another site, a sign-out or another hand-over got in meanwhile.
            guard api === old, currentSite?.id == site.id else { return }
        }
        updateSite(site)
        refreshConnection()
        if wasSignedIn, isSignedIn { AppStore.shared.startAuto() }
    }
}
