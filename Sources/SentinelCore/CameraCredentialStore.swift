import Foundation
import LocalAuthentication

@MainActor
public final class CameraCredentialStore: ObservableObject {
    @Published public private(set) var didChooseSessionAccess = false
    @Published public private(set) var isAuthenticating = false
    @Published public private(set) var unlockedWithTouchID = false
    @Published public private(set) var lastAccessError: String?
    @Published public private(set) var lastAccessMessage = "Touch ID required."
    @Published public private(set) var isTouchIDLockedOut = false
    @Published public private(set) var cachedCredentialCount = 0

    private var authenticatedContext: LAContext?
    private var sessionPasswords: [UUID: String] = [:]

    public init() {
        // The operator login now unlocks the SecretVault, which holds every
        // camera password. Once it's unlocked, credential access is implicitly
        // granted — so the separate Touch ID gate (AppAccessLockView) no longer
        // needs to appear. React to the unlock to flip the gate open.
        NotificationCenter.default.addObserver(
            forName: SecretVault.didUnlock, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.grantSessionAccessFromVault() }
        }
        if SecretVault.shared.isUnlocked {
            didChooseSessionAccess = true
        }
    }

    /// Marks the session as unlocked because the SecretVault (operator password)
    /// is open. Camera passwords are read straight from the vault, so no Touch ID
    /// or Keychain prompt is needed.
    public func grantSessionAccessFromVault() {
        didChooseSessionAccess = true
        lastAccessError = nil
        lastAccessMessage = "Unlocked."
    }

    public var hasSessionAccess: Bool {
        didChooseSessionAccess
    }

    public var accessStatusLabel: String {
        if unlockedWithTouchID {
            return "Touch ID"
        }

        if didChooseSessionAccess {
            return "Unlocked"
        }

        return "Locked"
    }

    public var shouldOfferMacUnlockReset: Bool {
        isTouchIDLockedOut || (lastAccessError?.localizedCaseInsensitiveContains("locked") ?? false)
    }

    public func requiresSessionUnlock(for cameras: [CameraFeed]) -> Bool {
        didChooseSessionAccess == false &&
        cameras.contains { camera in
            camera.isLocalCamera == false &&
            camera.rtspURL.isEmpty == false &&
            camera.hasEmbeddedRTSPCredentials == false
        }
    }

    /// Whether any camera relies on the Keychain credential-unlock mechanism at
    /// all. False when every camera is local or uses embedded "unattended"
    /// credentials — in which case the Touch ID unlock control is unnecessary
    /// and can be hidden from the toolbar.
    public func usesManagedCredentials(in cameras: [CameraFeed]) -> Bool {
        cameras.contains { camera in
            camera.isLocalCamera == false &&
            camera.rtspURL.isEmpty == false &&
            camera.hasEmbeddedRTSPCredentials == false
        }
    }

    @discardableResult
    public func unlockWithTouchID(for cameras: [CameraFeed] = []) async -> Bool {
        isAuthenticating = true
        lastAccessError = nil
        lastAccessMessage = "Checking Touch ID..."
        isTouchIDLockedOut = false

        let context = LAContext()
        context.localizedCancelTitle = "Cancel"
        context.localizedFallbackTitle = ""
        // Require a fresh fingerprint each time we re-evaluate the policy.
        // 60s of silent reuse was a risk on a shared / borrowed Mac.
        context.touchIDAuthenticationAllowableReuseDuration = 0

        var authenticationError: NSError?
        let policy = LAPolicy.deviceOwnerAuthenticationWithBiometrics
        guard context.canEvaluatePolicy(policy, error: &authenticationError) else {
            isAuthenticating = false
            isTouchIDLockedOut = isBiometryLockout(authenticationError)
            lastAccessError = touchIDUnavailableMessage(authenticationError)
            isTouchIDLockedOut = shouldOfferMacUnlockReset
            lastAccessMessage = "Touch ID could not start."
            return false
        }

        lastAccessMessage = "Touch the fingerprint sensor."

        do {
            let didAuthenticate = try await context.evaluatePolicy(
                policy,
                localizedReason: "Unlock Sentinel VMS and enable camera, recording, playback, alerts, exports, and administration."
            )
            isAuthenticating = false

            if didAuthenticate {
                authenticatedContext = context
                unlockedWithTouchID = true
                didChooseSessionAccess = true
                lastAccessError = nil
                lastAccessMessage = "Unlocked with Touch ID."
                return true
            }
        } catch {
            isAuthenticating = false
            isTouchIDLockedOut = isBiometryLockout(error)
            lastAccessError = touchIDFailureMessage(error)
            isTouchIDLockedOut = shouldOfferMacUnlockReset
            lastAccessMessage = "Touch ID did not unlock."
        }

        return false
    }

    public func clearSessionAccess() {
        authenticatedContext?.invalidate()
        authenticatedContext = nil
        sessionPasswords.removeAll()
        cachedCredentialCount = 0
        unlockedWithTouchID = false
        didChooseSessionAccess = false
        lastAccessError = nil
        lastAccessMessage = "Touch ID required."
        isTouchIDLockedOut = false
    }

    public func sleepDisplayForMacUnlock() {
        lastAccessMessage = "Locking display. Unlock the Mac, then Sentinel VMS will retry Touch ID."

        #if os(macOS)
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/pmset")
        process.arguments = ["displaysleepnow"]

        do {
            try process.run()
        } catch {
            lastAccessError = "Could not lock the display: \(error.localizedDescription)"
        }
        #endif
    }

    public func rtspURL(for camera: CameraFeed) -> String {
        if camera.hasEmbeddedRTSPCredentials {
            return camera.rtspURL
        }

        if let sessionPassword = sessionPasswords[camera.id],
           sessionPassword.isEmpty == false {
            return RTSPCredentialFormatter.url(
                camera.rtspURL,
                username: camera.username,
                password: sessionPassword
            )
        }

        if let storedPassword = try? CameraSecrets.password(for: camera.id, context: authenticatedContext),
           storedPassword.isEmpty == false {
            sessionPasswords[camera.id] = storedPassword
            cachedCredentialCount = sessionPasswords.count
            return RTSPCredentialFormatter.url(
                camera.rtspURL,
                username: camera.username,
                password: storedPassword
            )
        }

        return camera.rtspURL
    }

    public func subStreamRTSPURL(for camera: CameraFeed) -> String {
        // Low-res live is OPT-IN per camera: only when an explicit sub-stream
        // URL is set. (Auto-deriving it silently can break recording on budget
        // cameras that won't serve main + sub at once — recording is sacred.)
        // `RTSPURLPresets.deriveSubStream` is still used to *suggest* a URL in
        // the camera editor, but never applied automatically here.
        guard camera.subStreamRTSPURL.isEmpty == false else { return "" }

        if camera.hasEmbeddedRTSPCredentials {
            return camera.subStreamRTSPURL
        }

        let password = sessionPasswords[camera.id]
            ?? (try? CameraSecrets.password(for: camera.id, context: authenticatedContext))
            ?? ""
        guard password.isEmpty == false else { return camera.subStreamRTSPURL }
        return RTSPCredentialFormatter.url(camera.subStreamRTSPURL, username: camera.username, password: password)
    }

    public func cacheSavedPasswords(for cameras: [CameraFeed]) {
        cacheSavedPasswords(for: cameras, context: authenticatedContext)
    }

    private func cacheSavedPasswords(for cameras: [CameraFeed], context: LAContext?) {
        for camera in cameras where camera.isLocalCamera == false && camera.rtspURL.isEmpty == false {
            guard sessionPasswords[camera.id] == nil,
                  let storedPassword = try? CameraSecrets.password(for: camera.id, context: context),
                  storedPassword.isEmpty == false else {
                continue
            }

            sessionPasswords[camera.id] = storedPassword
        }

        cachedCredentialCount = sessionPasswords.count
    }

    private func touchIDUnavailableMessage(_ error: NSError?) -> String {
        guard let error else {
            return "Touch ID is not available on this Mac."
        }

        switch LAError.Code(rawValue: error.code) {
        case .biometryNotAvailable:
            return "Touch ID is not available on this Mac."
        case .biometryNotEnrolled:
            return "Set up Touch ID in System Settings to unlock HandoffGrid with your fingerprint."
        case .biometryLockout:
            return "Touch ID is locked by macOS. Lock and unlock your Mac once, then press Retry Touch ID."
        default:
            return error.localizedDescription
        }
    }

    private func isBiometryLockout(_ error: Error?) -> Bool {
        if let laError = error as? LAError {
            return laError.code == .biometryLockout
        }

        guard let error = error as NSError? else {
            return false
        }

        return LAError.Code(rawValue: error.code) == .biometryLockout ||
            error.localizedDescription.localizedCaseInsensitiveContains("locked")
    }

    private func touchIDFailureMessage(_ error: Error) -> String {
        guard let laError = error as? LAError else {
            return error.localizedDescription
        }

        switch laError.code {
        case .userCancel, .appCancel, .systemCancel:
            return "Touch ID was cancelled."
        case .authenticationFailed:
            return "Touch ID did not match. Try again."
        case .biometryLockout:
            return "Touch ID is locked by macOS. Lock and unlock your Mac once, then press Retry Touch ID."
        default:
            return laError.localizedDescription
        }
    }
}
