import Foundation
import Security
import SwiftUI

// MARK: - License store
//
// Per-camera subscription licensing backed by Supabase auth + a Stripe
// subscription whose quantity == number of *paid* cameras. Every install also
// gets `freeCameras` for free, granted locally even with no account/network.
//
// Entitlement is cached to the Keychain so a 24/7 VMS keeps working offline;
// `offlineGraceInterval` bounds how long a lapsed/past-due subscription is still
// honored before falling back to the free tier.
@MainActor
public final class LicenseStore: ObservableObject {
    public static let freeCameras = 2

    @Published public private(set) var account: SentinelAccount?
    @Published public private(set) var snapshot: LicenseSnapshot?
    @Published public private(set) var isBusy = false
    @Published public var lastError: String?

    private let backend: SentinelBackend
    private let local = LicenseLocalStore()
    private var session: SentinelSession?

    public init(backend: SentinelBackend = SentinelBackend()) {
        self.backend = backend
        // The cloud session token now lives in the SecretVault, which is locked
        // until the operator signs in. So we do NOT touch it here (that read was
        // the launch-time macOS Keychain prompt). We load only the non-secret
        // cached entitlement snapshot so the UI can render the last-known plan,
        // and wait for the vault to unlock to load the session.
        self.snapshot = local.loadSnapshot()
        NotificationCenter.default.addObserver(
            forName: SecretVault.didUnlock, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.activate()
                self?.refreshAIAvailability()  // key lives in the now-unlocked vault
            }
        }
        // Re-read AI availability whenever the key or the enable toggle changes.
        NotificationCenter.default.addObserver(
            forName: SentinelAISettings.didChange, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.refreshAIAvailability() }
        }
        if SecretVault.shared.isUnlocked {
            activate()
            refreshAIAvailability()
        }
    }

    /// Loads the cloud session from the (now-unlocked) vault and refreshes the
    /// entitlement. Safe to call repeatedly — no-ops once a session is loaded.
    public func activate() {
        guard session == nil else { return }
        guard let loaded = local.loadSession() else { return }
        session = loaded
        account = loaded.account
        Task { await refreshEntitlement() }
    }

    // MARK: Derived entitlement

    public var isSignedIn: Bool { account != nil }
    public var licensedCameras: Int { snapshot?.licensedCameras ?? 0 }
    public var status: LicenseStatus { snapshot?.status ?? .free }

    /// Paid cameras currently honored, accounting for the offline grace window.
    public var effectiveLicensedCameras: Int {
        guard let snap = snapshot else { return 0 }
        switch snap.status {
        case .active:
            return snap.licensedCameras
        case .pastDue:
            let anchor = snap.currentPeriodEnd ?? snap.fetchedAt
            let withinGrace = Date() < anchor.addingTimeInterval(SentinelBackendConfig.offlineGraceInterval)
            return withinGrace ? snap.licensedCameras : 0
        case .free, .canceled, .inactive:
            return 0
        }
    }

    // Sentinel VMS is FREE — unlimited cameras, no per-camera charge. The Stripe
    // licensing code below is left in place but dormant; these gates report
    // unlimited so nothing in the app is ever paywalled.
    public var cameraLimit: Int { .max }

    /// Unused camera slots — always effectively unlimited now.
    public func availableCameraSlots(inUse used: Int) -> Int { .max }

    /// Everything is free → treat as the unlimited tier everywhere.
    public var isUnlimited: Bool { true }

    // MARK: AI features (bring-your-own Anthropic key)
    //
    // AI is no longer a subscription. The operator supplies their own Anthropic
    // API key (stored in the encrypted SecretVault) and switches AI on; Sentinel
    // then calls Claude directly and the user owns the bill. `aiAvailable`
    // mirrors `SentinelAISettings` and is published so SwiftUI re-renders when
    // the key or the toggle changes.

    @Published public private(set) var aiAvailable: Bool = SentinelAISettings.isAvailable

    /// Whether AI scene descriptions / search / digests can run right now
    /// (a valid key is stored AND the operator enabled AI).
    public var aiActive: Bool { aiAvailable }

    /// Re-reads the BYOK settings into the published flag. Called on vault
    /// unlock and whenever the key or toggle changes.
    public func refreshAIAvailability() { aiAvailable = SentinelAISettings.isAvailable }

    // MARK: Remote access (now free)

    /// Off-LAN remote viewing (the Cloudflare tunnel) is now FREE for everyone —
    /// no subscription. Always available.
    public var remoteActive: Bool { true }

    /// Sentinel VMS is free; there is no paid plan to label.
    public var planLabel: String { "Free" }

    // MARK: Auth

    public func signIn(email: String, password: String) async {
        await run {
            let session = try await self.backend.signIn(email: email, password: password)
            self.adopt(session)
            try? await self.fetchAndStoreEntitlement()
        }
    }

    public func signUp(email: String, password: String) async {
        await run {
            let session = try await self.backend.signUp(email: email, password: password)
            // If email confirmation is required, GoTrue returns no usable token.
            guard !session.accessToken.isEmpty else {
                self.lastError = "Check your email to confirm your account, then sign in."
                return
            }
            self.adopt(session)
            try? await self.fetchAndStoreEntitlement()
        }
    }

    public func signOut() {
        session = nil
        account = nil
        snapshot = nil
        local.clear()
    }

    // MARK: Entitlement refresh

    public func refreshEntitlement() async {
        guard session != nil else { return }
        await run { try await self.fetchAndStoreEntitlement() }
    }

    /// Fetches the license row, transparently refreshing the access token once on 401.
    private func fetchAndStoreEntitlement() async throws {
        guard let current = session else { return }
        do {
            let snap = try await backend.fetchLicense(accessToken: current.accessToken,
                                                      userID: current.account.userID)
            apply(snap)
        } catch let SentinelBackendError.http(code, _) where code == 401 {
            let refreshed = try await backend.refresh(refreshToken: current.refreshToken)
            adopt(refreshed)
            let snap = try await backend.fetchLicense(accessToken: refreshed.accessToken,
                                                      userID: refreshed.account.userID)
            apply(snap)
        }
    }

    // MARK: Checkout / portal

    /// Returns a Stripe Checkout URL for buying `quantity` paid cameras.
    public func beginCheckout(quantity: Int) async -> URL? {
        guard let session else {
            lastError = "Sign in to add licensed cameras."
            return nil
        }
        var url: URL?
        await run { url = try await self.backend.createCheckout(accessToken: session.accessToken,
                                                                quantity: max(1, quantity)) }
        return url
    }

    /// Returns a Stripe Checkout URL to subscribe to the monthly remote-access plan.
    public func beginRemoteCheckout() async -> URL? {
        guard let session else {
            lastError = "Sign in to subscribe to remote access."
            return nil
        }
        var url: URL?
        await run { url = try await self.backend.createCheckout(accessToken: session.accessToken,
                                                                quantity: 1, plan: "remote") }
        return url
    }

    /// Returns a Stripe Billing Portal URL to manage/cancel the subscription.
    public func openBillingPortal() async -> URL? {
        guard let session else {
            lastError = "Sign in to manage your subscription."
            return nil
        }
        var url: URL?
        await run { url = try await self.backend.openPortal(accessToken: session.accessToken) }
        return url
    }

    /// Analyzes a frame (or short clip) via Claude vision for Ring-style alerts
    /// + a routine-vs-suspicious threat read. Returns nil if AI is off / no key,
    /// or the request fails — analysis is a best-effort enhancement and must
    /// never block detection. Calls Anthropic directly with the operator's key.
    public func analyzeScene(jpegs: [Data]) async -> SceneAnalysis? {
        guard aiActive, let client = SentinelAISettings.makeClient() else { return nil }
        return try? await client.analyzeScene(jpegs: jpegs)
    }

    /// Single-frame convenience used by the live detector's describer hook.
    public func describeScene(jpeg: Data) async -> SceneAnalysis? {
        await analyzeScene(jpegs: [jpeg])
    }

    /// "What happened today?" — summarizes a window of events. Throws so the UI
    /// can show why it failed (no key, bad key, offline).
    public func dailyDigest(label: String, events: [[String: String]]) async throws -> String {
        guard aiActive else { throw SentinelAIClient.AIError.disabled }
        guard let client = SentinelAISettings.makeClient() else { throw SentinelAIClient.AIError.noKey }
        return try await client.dailyDigest(label: label, events: events)
    }

    /// Natural-language search over the event log. Returns the answer plus the
    /// ids of matching events. Gated on `aiActive` so a user who turned AI off
    /// never incurs Anthropic charges from an on-demand search.
    public func searchEvents(query: String, events: [[String: String]]) async throws -> (answer: String, matches: [UUID]) {
        guard aiActive else { throw SentinelAIClient.AIError.disabled }
        guard let client = SentinelAISettings.makeClient() else { throw SentinelAIClient.AIError.noKey }
        return try await client.searchEvents(query: query, events: events)
    }

    // MARK: Plumbing

    private func adopt(_ session: SentinelSession) {
        self.session = session
        self.account = session.account
        local.saveSession(session)
    }

    private func apply(_ snap: LicenseSnapshot) {
        self.snapshot = snap
        local.saveSnapshot(snap)
    }

    /// Runs an async block with busy/error bookkeeping.
    private func run(_ work: @escaping () async throws -> Void) async {
        isBusy = true
        lastError = nil
        defer { isBusy = false }
        do { try await work() }
        catch { lastError = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription }
    }
}

// MARK: - Local persistence (Keychain session + cached entitlement snapshot)

struct LicenseLocalStore {
    private let service = "com.handoffgrid.sentinel.license"
    private let sessionAccount = "session"
    private let snapshotURL: URL = {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("license.json")
    }()

    // Session (contains refresh token) → SecretVault (one encrypted file unlocked
    // by the operator login), not the macOS login Keychain. Keeping it out of the
    // Keychain is what removes the OS authorization prompt at launch.
    func saveSession(_ session: SentinelSession) {
        guard let data = try? JSONEncoder().encode(session) else { return }
        SecretVault.shared.setValue(data, service: service, account: sessionAccount)
        legacyDelete()
    }

    func loadSession() -> SentinelSession? {
        if let data = SecretVault.shared.value(service: service, account: sessionAccount),
           let session = try? JSONDecoder().decode(SentinelSession.self, from: data) {
            return session
        }
        // Prompt-free migration from the legacy login Keychain (UISkip).
        guard let data = legacyLoad() else { return nil }
        if SecretVault.shared.isUnlocked {
            SecretVault.shared.setValue(data, service: service, account: sessionAccount)
            legacyDelete()
        }
        return try? JSONDecoder().decode(SentinelSession.self, from: data)
    }

    private func legacyLoad() -> Data? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: sessionAccount,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
            kSecUseAuthenticationUI as String: kSecUseAuthenticationUISkip,
        ]
        return SecretVault.withoutLegacyKeychainPrompt {
            var item: CFTypeRef?
            guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
                  let data = item as? Data else { return nil }
            return data
        }
    }

    private func legacyDelete() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: sessionAccount,
        ]
        SecretVault.withoutLegacyKeychainPrompt {
            SecItemDelete(query as CFDictionary)
        }
    }

    // Cached entitlement → file (non-secret; signed status only).
    func saveSnapshot(_ snapshot: LicenseSnapshot) {
        if let data = try? JSONEncoder().encode(snapshot) {
            try? data.write(to: snapshotURL, options: .atomic)
        }
    }

    func loadSnapshot() -> LicenseSnapshot? {
        guard let data = try? Data(contentsOf: snapshotURL) else { return nil }
        return try? JSONDecoder().decode(LicenseSnapshot.self, from: data)
    }

    func clear() {
        SecretVault.shared.removeValue(service: service, account: sessionAccount)
        legacyDelete()
        try? FileManager.default.removeItem(at: snapshotURL)
    }
}
