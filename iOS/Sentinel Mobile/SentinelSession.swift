// SentinelSession.swift
// Which Management Server the phone is signed in to, and everything that
// hangs off that: the API client, the video token broker, the signed-in
// user, push registration. One session per app run; switching sites tears
// the old one down.

import Foundation
import SwiftUI
import UIKit
import AuthenticationServices

@MainActor
final class SentinelSession: ObservableObject {
    static let shared = SentinelSession()

    enum Phase: Equatable {
        /// No site chosen: show the welcome / site list.
        case noSite
        /// A site is chosen but there is no usable sign-in.
        case signedOut(siteID: String)
        case signedIn(siteID: String)
        case demo
    }

    @Published private(set) var phase: Phase = .noSite
    @Published private(set) var me: V2.Me?
    @Published private(set) var api: SentinelAPI?
    @Published private(set) var broker: VideoTokenBroker?
    /// True when the last request used the site's secondary (remote) address.
    @Published private(set) var isUsingRemote = false
    /// Shown on the sign-in screen after a sign-in ended on its own.
    @Published var notice: String?

    let directory: SiteDirectory
    let tokenStore: TokenStoring
    private let defaults: UserDefaults
    private static let pushTokenKey = "sentinel.mobile.apnsToken"

    init(directory: SiteDirectory? = nil, tokenStore: TokenStoring = KeychainTokenStore(),
         defaults: UserDefaults = .standard) {
        self.directory = directory ?? SiteDirectory(defaults: defaults)
        self.tokenStore = tokenStore
        self.defaults = defaults
    }

    var isDemoMode: Bool { phase == .demo }
    var isSignedIn: Bool { if case .signedIn = phase { return true } else { return false } }

    var currentSite: SavedSite? {
        switch phase {
        case .signedOut(let id), .signedIn(let id): return directory.site(id: id)
        default: return nil
        }
    }

    /// Name for headers ("Warehouse").
    var serverName: String? {
        if isDemoMode { return "Demo" }
        return currentSite?.displayName
    }

    var userName: String? { me?.name ?? currentSite?.lastUserName }

    /// Whether the signed-in role may do something. Unknown (offline start)
    /// counts as yes; the server enforces it either way.
    func can(_ permission: V2.Permission) -> Bool {
        if isDemoMode { return true }
        return me?.can(permission) ?? true
    }

    // MARK: - Launch

    /// Resumes the last site. Called once when the app starts.
    func start() {
        guard phase == .noSite, let id = directory.currentSiteID, let site = directory.site(id: id) else { return }
        select(site)
    }

    // MARK: - Choosing a site

    /// Makes `site` current. Goes straight in when its tokens are still good,
    /// otherwise to the sign-in screen.
    func select(_ site: SavedSite) {
        tearDown()
        guard let url = site.url else {
            phase = .noSite
            return
        }
        directory.currentSiteID = site.id
        let api = makeAPI(for: site, url: url)
        self.api = api
        self.broker = VideoTokenBroker(api: api)
        Task {
            if await api.isSignedIn {
                phase = .signedIn(siteID: site.id)
                await loadMe(api: api)
                registerPushIfPossible()
            } else {
                phase = .signedOut(siteID: site.id)
            }
        }
    }

    private func makeAPI(for site: SavedSite, url: URL) -> SentinelAPI {
        let addresses = SiteAddresses(primary: url, secondary: site.remoteURL)
        let api = SentinelAPI(addresses: addresses, siteID: site.id, tokenStore: tokenStore,
                              clientLabel: "Sentinel Mobile (\(UIDevice.current.name))")
        let siteID = site.id
        Task {
            await api.setSignInEndedHandler { [weak self] message in
                Task { @MainActor in self?.signInEnded(siteID: siteID, message: message) }
            }
        }
        return api
    }

    private func signInEnded(siteID: String, message: String) {
        BackupAccess.shared.clear(siteID: siteID) // WAVE12(D): the offsite backup token ends with the sign-in
        guard currentSite?.id == siteID else { return }
        me = nil
        notice = message
        phase = .signedOut(siteID: siteID)
    }

    private func loadMe(api: SentinelAPI) async {
        if let me = try? await api.me() {
            self.me = me
        }
        refreshConnection()
        Task { await syncCloud(api: api) } // WAVE12(D): Sentinel Cloud remote address and offsite backup token
    }

    func refreshConnection() {
        isUsingRemote = api?.addresses.isUsingSecondary ?? false
    }

    // MARK: - Adding sites

    /// Checks that `address` is a Management Server that speaks API v2 and
    /// saves it. Throws a readable error otherwise (including "too old").
    @discardableResult
    func addSite(name: String, address: String, remoteAddress: String?,
                 remoteSource: RemoteAddressSource = .manual) async throws -> SavedSite { // WAVE12(D)
        guard let url = SavedSite.normalizedURL(address) else {
            throw SentinelAPIError.transport(message: "Enter the server's address, e.g. 192.168.1.20 or https://sentinel.example.com.")
        }
        var remote = remoteAddress?.trimmingCharacters(in: .whitespacesAndNewlines)
        if remote?.isEmpty == true { remote = nil }
        if let remote, SavedSite.normalizedURL(remote) == nil {
            throw SentinelAPIError.transport(message: "The remote address isn't a valid web address.")
        }
        var site = directory.site(forAddress: address) ?? SavedSite(name: name, address: url.absoluteString)
        if name.trimmingCharacters(in: .whitespaces).isEmpty == false { site.name = name }
        site.address = url.absoluteString
        if let remote { site.remoteAddress = remote; site.remoteAddressSource = remoteSource } // WAVE12(D)
        // Probe with a throwaway client; this also tells a 1.x server apart.
        let probe = SentinelAPI(addresses: SiteAddresses(primary: url, secondary: site.remoteURL),
                                siteID: "probe-\(site.id)", tokenStore: MemoryTokenStore())
        _ = try await Self.checkServer(probe)
        directory.upsert(site)
        return site
    }

    /// `GET /api/v2/auth/providers`; a server without API v2 answers 404 or
    /// with its web page, both of which mean "too old".
    static func checkServer(_ api: SentinelAPI) async throws -> V2.AuthProviders {
        do {
            return try await api.providers()
        } catch let error as SentinelAPIError {
            switch error {
            case .decoding:
                throw tooOld
            case .server(let status, _, _) where status == 404 || status == 405:
                throw tooOld
            default:
                throw error
            }
        }
    }

    static let tooOld = SentinelAPIError.server(
        status: 404, code: "server_too_old",
        message: "This server is too old for this version of Sentinel Mobile. Update the Management Server to Sentinel 2 or later, then try again.")

    func updateSite(_ site: SavedSite) {
        directory.upsert(site)
        // A changed address takes effect on the next connection.
        if currentSite?.id == site.id, let url = site.url, let api {
            let wasSignedIn = isSignedIn
            let fresh = makeAPI(for: site, url: url)
            self.api = fresh
            self.broker = VideoTokenBroker(api: fresh)
            phase = wasSignedIn ? .signedIn(siteID: site.id) : .signedOut(siteID: site.id)
        }
    }

    func providers(for site: SavedSite) async throws -> V2.AuthProviders {
        guard let api else { throw SentinelAPIError.transport(message: "Choose a site first.") }
        return try await Self.checkServer(api)
    }

    // MARK: - Signing in

    func signIn(name: String, password: String) async throws {
        guard let api, var site = currentSite else { return }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        try await api.signIn(name: trimmed, password: password)
        site.lastUserName = trimmed
        finishSignIn(api: api, site: site)
    }

    /// Native single sign-on: the server's OIDC page in a system browser
    /// sheet, back to `sentinel-operator://oidc` with a one-time code.
    func signInWithSSO() async throws {
        guard let api, var site = currentSite else { return }
        let state = UUID().uuidString
        let start = api.oidcStartURL(redirectURI: SSOAuthenticator.redirectURI, state: state)
        let callback = try await SSOAuthenticator.shared.authenticate(url: start)
        let code = try Self.oidcCode(from: callback, expectedState: state)
        let response = try await api.signIn(oidcCode: code)
        site.lastUserName = response.user.name
        finishSignIn(api: api, site: site)
    }

    /// The `code` in the redirect back from the server, or the reason there isn't one.
    nonisolated static func oidcCode(from callback: URL, expectedState: String) throws -> String {
        let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
        if let error = items.first(where: { $0.name == "error" })?.value {
            let detail = items.first(where: { $0.name == "error_description" })?.value ?? error
            throw SentinelAPIError.server(status: 403, code: "forbidden", message: "Single sign-on failed: \(detail)")
        }
        guard items.first(where: { $0.name == "state" })?.value == expectedState,
              let code = items.first(where: { $0.name == "code" })?.value else {
            throw SentinelAPIError.server(status: 400, code: "bad_request",
                                          message: "Single sign-on didn't return a sign-in code. Try again.")
        }
        return code
    }

    private func finishSignIn(api: SentinelAPI, site: SavedSite) {
        var site = site
        site.lastUsedAt = Date()
        directory.upsert(site)
        notice = nil
        phase = .signedIn(siteID: site.id)
        Task {
            await loadMe(api: api)
            registerPushIfPossible()
        }
    }

    // MARK: - Leaving

    /// Signs out of the current site (the site stays in the list).
    func signOut() async {
        guard let api, let site = currentSite else {
            leaveDemo()
            return
        }
        AlarmActivityController.shared.sessionEnded()
        await unregisterPush(api: api)
        await api.signOut()
        BackupAccess.shared.clear(siteID: site.id) // WAVE12(D): drop the offsite backup token on sign-out
        // Re-select the site: a fresh client for the sign-in screen, no tokens.
        select(site)
        phase = .signedOut(siteID: site.id)
    }

    /// Back to the site list (the sign-in stays saved for next time).
    func chooseAnotherSite() {
        AlarmActivityController.shared.sessionEnded()
        if let api { Task { await unregisterPush(api: api) } }
        tearDown()
        directory.currentSiteID = nil
        phase = .noSite
    }

    func remove(_ site: SavedSite) {
        if currentSite?.id == site.id { chooseAnotherSite() }
        directory.remove(site, tokenStore: tokenStore)
        BackupAccess.shared.clear(siteID: site.id) // WAVE12(D): and its offsite backup token
    }

    private func tearDown() {
        api = nil
        broker = nil
        me = nil
        isUsingRemote = false
        AppStore.shared.reset()
    }

    // MARK: - Demo

    func enterDemoMode() {
        tearDown()
        phase = .demo
    }

    func leaveDemo() {
        AlarmActivityController.shared.sessionEnded()
        tearDown()
        phase = .noSite
    }

    // MARK: - Push

    var apnsToken: String? { defaults.string(forKey: Self.pushTokenKey) }

    /// Called when iOS hands the app a device token.
    func updateAPNSToken(_ hex: String) {
        defaults.set(hex, forKey: Self.pushTokenKey)
        registerPushIfPossible()
    }

    private func registerPushIfPossible() {
        guard isSignedIn, let api, let token = apnsToken else { return }
        let name = UIDevice.current.name
        Task {
            try? await api.registerPush(apnsToken: token, deviceName: name)
            // A followed alarm's Live Activity token hangs off this registration.
            AlarmActivityController.shared.resendToken()
        }
    }

    private func unregisterPush(api: SentinelAPI) async {
        guard let token = apnsToken else { return }
        try? await api.unregisterPush(apnsToken: token)
    }
}

// MARK: - Single sign-on

/// Runs `ASWebAuthenticationSession` for the native OIDC flow. The server
/// accepts the `sentinel-operator:` scheme for native clients; the session
/// intercepts it, so the app doesn't register it.
@MainActor
final class SSOAuthenticator: NSObject, ASWebAuthenticationPresentationContextProviding {
    static let shared = SSOAuthenticator()
    static let callbackScheme = "sentinel-operator"
    static let redirectURI = "sentinel-operator://oidc"

    private var session: ASWebAuthenticationSession?

    func authenticate(url: URL) async throws -> URL {
        try await withCheckedThrowingContinuation { continuation in
            let session = ASWebAuthenticationSession(url: url, callbackURLScheme: Self.callbackScheme) { callback, error in
                if let callback {
                    continuation.resume(returning: callback)
                } else {
                    continuation.resume(throwing: error ?? CancellationError())
                }
            }
            session.presentationContextProvider = self
            session.prefersEphemeralWebBrowserSession = false
            self.session = session
            if session.start() == false {
                continuation.resume(throwing: SentinelAPIError.transport(message: "Couldn't open the sign-in page."))
            }
        }
    }

    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated {
            let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
            return scenes.flatMap(\.windows).first(where: \.isKeyWindow) ?? ASPresentationAnchor()
        }
    }
}
