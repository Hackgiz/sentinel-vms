import AppKit
import AuthenticationServices
import Foundation
import SentinelCore

/// The signed-in connection to one site's management server: its API client,
/// the signed-in user and the video token broker. Owns the switch between
/// saved sites. Everything that talks to the server gets it from here.
@MainActor
final class OperatorSession: ObservableObject {
    enum Phase: Equatable {
        /// No usable sign-in: show the sign-in screen.
        case signedOut
        /// Saved tokens are being checked (`GET /me`).
        case restoring
        case signedIn
    }

    let sites: SiteDirectory
    let tokenStore: TokenStoring

    @Published private(set) var phase: Phase = .signedOut
    @Published private(set) var site: SavedSite?
    @Published private(set) var me: V2.Me?
    /// Shown on the sign-in screen (e.g. "Your sign-in ended. Sign in again.").
    @Published var notice: String?
    /// Fills the sign-in form (e.g. "connect to the server on this Mac").
    @Published var prefill: SignInPrefill?
    /// Last connection problem while restoring a sign-in (server unreachable).
    @Published private(set) var connectionProblem: String?

    private(set) var api: SentinelAPI?
    private(set) var video: VideoTokenBroker?
    /// Live site state while signed in.
    @Published private(set) var runtime: SiteRuntime?

    init(sites: SiteDirectory = SiteDirectory(), tokenStore: TokenStoring = KeychainTokenStore()) {
        self.sites = sites
        self.tokenStore = tokenStore
    }

    var isSignedIn: Bool { phase == .signedIn }

    func can(_ permission: V2.Permission) -> Bool {
        me?.can(permission) ?? false
    }

    var isAdmin: Bool { me?.role == .admin }

    /// On launch: reconnect to the last site if its sign-in is still saved.
    func restore() async {
        guard let site = sites.currentSite, tokenStore.load(siteID: site.id) != nil else {
            phase = .signedOut
            return
        }
        await connect(to: site)
    }

    /// Uses a saved site's stored sign-in. Leaves the sign-in screen up when
    /// there is none or it ended.
    func connect(to site: SavedSite) async {
        guard let url = site.url else { return }
        let api = makeAPI(for: site, url: url)
        guard await api.isSignedIn else {
            self.site = site
            phase = .signedOut
            return
        }
        phase = .restoring
        connectionProblem = nil
        do {
            let me = try await api.me()
            adopt(site: site, api: api, me: me)
        } catch let error as SentinelAPIError {
            if case .signInRequired = error {
                self.site = site
                phase = .signedOut
            } else if case .transport(let message) = error {
                // Keep the sign-in; the server may come back. Show the operator why.
                connectionProblem = message
                self.site = site
                phase = .signedOut
                notice = "\(site.name) isn't reachable: \(message)"
            } else {
                self.site = site
                phase = .signedOut
                notice = error.localizedDescription
            }
        } catch {
            phase = .signedOut
            notice = error.localizedDescription
        }
    }

    /// Signs in with a name and password, saving (or updating) the site.
    func signIn(address: String, siteName: String, name: String, password: String) async throws {
        let site = try prepareSite(address: address, siteName: siteName, userName: name)
        let api = makeAPI(for: site, url: site.url!)
        try await api.signIn(name: name, password: password)
        let me = try await api.me()
        adopt(site: site, api: api, me: me)
    }

    /// Native single sign-on: the server's OIDC start page in a browser
    /// sheet, back to `sentinel-operator://oidc` with a one-time code.
    func signInWithSSO(address: String, siteName: String) async throws {
        let site = try prepareSite(address: address, siteName: siteName, userName: nil)
        let api = makeAPI(for: site, url: site.url!)
        let state = UUID().uuidString
        let start = api.oidcStartURL(redirectURI: SSOAuthenticator.redirectURI, state: state)
        let callback = try await SSOAuthenticator.shared.authenticate(url: start)
        let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
        if let error = items.first(where: { $0.name == "error" })?.value {
            let detail = items.first(where: { $0.name == "error_description" })?.value ?? error
            throw SentinelAPIError.server(status: 403, code: "forbidden", message: "Single sign-on failed: \(detail)")
        }
        guard items.first(where: { $0.name == "state" })?.value == state,
              let code = items.first(where: { $0.name == "code" })?.value else {
            throw SentinelAPIError.server(status: 400, code: "bad_request", message: "Single sign-on didn't return a sign-in code. Try again.")
        }
        let response = try await api.signIn(oidcCode: code)
        var named = site
        named.lastUserName = response.user.name
        sites.upsert(named)
        let me = try await api.me()
        adopt(site: named, api: api, me: me)
    }

    func signOut() async {
        let api = self.api
        tearDown()
        await api?.signOut()
    }

    /// Leaves the current site without signing out of it (its sign-in stays saved).
    func switchSite() {
        tearDown()
    }

    private func tearDown() {
        OperatorWindows.shared.closeAll()
        runtime?.stop()
        runtime = nil
        api = nil
        video = nil
        me = nil
        phase = .signedOut
        NSApp.dockTile.badgeLabel = nil
    }

    func refreshMe() async {
        guard let api else { return }
        if let me = try? await api.me() { self.me = me }
    }

    // MARK: -

    private func prepareSite(address: String, siteName: String, userName: String?) throws -> SavedSite {
        guard let url = SavedSite.normalizedURL(address) else {
            throw SentinelAPIError.server(status: 400, code: "bad_request", message: "Enter the server's address, e.g. 192.168.1.20 or https://sentinel.example.com.")
        }
        let trimmedName = siteName.trimmingCharacters(in: .whitespacesAndNewlines)
        var site = sites.site(forAddress: url.absoluteString)
            ?? SavedSite(name: trimmedName.isEmpty ? (url.host ?? "Site") : trimmedName, address: url.absoluteString)
        if trimmedName.isEmpty == false { site.name = trimmedName }
        if let userName { site.lastUserName = userName }
        site.isLocal = site.isLocal || LocalServerManager.isLocalAddress(url)
        sites.upsert(site)
        return site
    }

    private func makeAPI(for site: SavedSite, url: URL) -> SentinelAPI {
        let api = SentinelAPI(baseURL: url, siteID: site.id, tokenStore: tokenStore)
        Task { [weak self] in
            await api.setSignInEndedHandler { message in
                Task { @MainActor in self?.signInEnded(message, siteID: site.id) }
            }
        }
        return api
    }

    private func adopt(site: SavedSite, api: SentinelAPI, me: V2.Me) {
        var used = site
        used.lastUsedAt = Date()
        used.lastUserName = me.name
        sites.upsert(used)
        sites.currentSiteID = used.id
        self.site = used
        self.api = api
        let video = VideoTokenBroker(api: api)
        self.video = video
        self.me = me
        runtime?.stop()
        let runtime = SiteRuntime(api: api, video: video)
        self.runtime = runtime
        runtime.start()
        notice = nil
        connectionProblem = nil
        phase = .signedIn
        OperatorWindows.shared.restoreMonitors()
    }

    private func signInEnded(_ message: String, siteID: String) {
        guard site?.id == siteID else { return }
        tearDown()
        notice = message
    }
}

/// Runs `ASWebAuthenticationSession` for the native OIDC flow.
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
                } else if let error = error as? ASWebAuthenticationSessionError, error.code == .canceledLogin {
                    continuation.resume(throwing: CancellationError())
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
            NSApp.keyWindow ?? NSApp.windows.first ?? ASPresentationAnchor()
        }
    }
}

struct SignInPrefill: Equatable {
    var address: String
    var siteName: String
}
