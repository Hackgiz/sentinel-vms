import Foundation
import CryptoKit

/// Sends Apple Push Notifications directly from the Mac VMS — no cloud relay.
///
/// Setup is intentionally cloudflared-style: drop the APNs Auth Key the Apple
/// Developer portal hands you (a file named `AuthKey_<KEYID>.p8`) into
/// `~/Library/Application Support/HandoffGridSentinel/`. The 10-char Key ID is
/// parsed straight from the filename, so the only other things needed are the
/// Team ID and the app's bundle ID (topic), both of which default correctly.
///
/// Auth is an ES256 JWT signed with the .p8 key (Apple's token-based provider
/// auth). The JWT is cached and re-minted every ~45 min (Apple accepts a token
/// for up to 60). Each push is one HTTP/2 POST to APNs; on `BadDeviceToken` we
/// transparently retry the other environment (sandbox vs production) so the
/// same code path works for both Xcode-installed (sandbox) and TestFlight /
/// App Store (production) builds without configuration.
@MainActor
public final class APNsPushService: ObservableObject {
    @Published public private(set) var isConfigured = false
    @Published public private(set) var statusMessage = "Not configured"
    @Published public private(set) var lastError: String?
    @Published public private(set) var lastSentAt: Date?

    /// Apple Developer Team ID that owns the APNs key. Read from the app's
    /// Info.plist key `SentinelAPNsTeamID` (set yours there, or put
    /// `{"teamID": "..."}` in apns-config.json in the support folder).
    public static let defaultTeamID =
        (Bundle.main.object(forInfoDictionaryKey: "SentinelAPNsTeamID") as? String) ?? ""
    /// APNs topic == the iOS app's bundle identifier.
    public static let defaultTopic = "com.handoffgrid.sentinel.mobile"

    private struct Config {
        let keyID: String
        let teamID: String
        let topic: String
        let privateKey: P256.Signing.PrivateKey
    }

    private let supportDir: URL
    private var config: Config?
    private var cachedJWT: (token: String, issuedAt: Date)?
    /// Host that last worked for a given device token (sandbox vs production).
    private var hostForToken: [String: String] = [:]

    private static let productionHost = "api.push.apple.com"
    private static let sandboxHost = "api.sandbox.push.apple.com"

    public init() {
        supportDir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        loadConfiguration()
    }

    /// Folder the user drops the AuthKey_*.p8 into. Surfaced in the UI.
    public var keyDirectory: URL { supportDir }

    // MARK: - Configuration

    /// (Re)scans the support directory for an `AuthKey_<KEYID>.p8` file and
    /// loads it. Call after the user adds the key so the UI updates live.
    public func loadConfiguration() {
        config = nil
        cachedJWT = nil
        lastError = nil

        let fm = FileManager.default
        guard let entries = try? fm.contentsOfDirectory(at: supportDir,
                                                        includingPropertiesForKeys: nil) else {
            isConfigured = false
            statusMessage = "Not configured"
            return
        }

        // Apple names the file AuthKey_XXXXXXXXXX.p8 — the 10-char suffix is the Key ID.
        guard let keyURL = entries.first(where: {
            $0.lastPathComponent.hasPrefix("AuthKey_") && $0.pathExtension == "p8"
        }) else {
            isConfigured = false
            statusMessage = "No AuthKey_*.p8 found"
            return
        }

        let keyID = keyURL.deletingPathExtension().lastPathComponent
            .replacingOccurrences(of: "AuthKey_", with: "")
        guard keyID.count == 10 else {
            isConfigured = false
            statusMessage = "Key filename must be AuthKey_<10-char KeyID>.p8"
            return
        }

        guard let pem = try? String(contentsOf: keyURL, encoding: .utf8) else {
            isConfigured = false
            lastError = "Could not read \(keyURL.lastPathComponent)"
            statusMessage = "Key unreadable"
            return
        }

        let privateKey: P256.Signing.PrivateKey
        do {
            privateKey = try P256.Signing.PrivateKey(pemRepresentation: pem)
        } catch {
            isConfigured = false
            lastError = "Invalid .p8 key: \(error.localizedDescription)"
            statusMessage = "Key invalid"
            return
        }

        // Team ID / topic come from an optional override file, else defaults.
        var teamID = Self.defaultTeamID
        var topic = Self.defaultTopic
        let overrideURL = supportDir.appendingPathComponent("apns-config.json")
        if let data = try? Data(contentsOf: overrideURL),
           let dict = try? JSONSerialization.jsonObject(with: data) as? [String: String] {
            teamID = dict["teamID"] ?? teamID
            topic = dict["topic"] ?? topic
        }

        guard !teamID.isEmpty else {
            isConfigured = false
            lastError = "No Apple Team ID. Set SentinelAPNsTeamID in Info.plist or teamID in apns-config.json."
            statusMessage = "Team ID missing"
            return
        }

        config = Config(keyID: keyID, teamID: teamID, topic: topic, privateKey: privateKey)
        isConfigured = true
        statusMessage = "Ready · Key \(keyID)"
        lastError = nil
    }

    // MARK: - Sending

    /// Sends one alert push to every supplied device token. No-op (logs a
    /// status) when not configured so callers don't need to guard.
    ///
    /// `cameraID`, when provided, rides as a custom top-level key (siblings of
    /// `aps`, per APNs) so a tapped notification can deep-link straight to that
    /// camera's live view on the phone.
    public func sendAlert(title: String, body: String, cameraID: UUID? = nil, deviceTokens: [String]) async {
        guard let config else {
            statusMessage = "Push not configured — add AuthKey_*.p8"
            return
        }
        let tokens = deviceTokens.filter { $0.isEmpty == false }
        guard tokens.isEmpty == false else { return }

        let jwt: String
        do {
            jwt = try currentJWT(config: config)
        } catch {
            lastError = "JWT signing failed: \(error.localizedDescription)"
            return
        }

        var payloadDict: [String: Any] = [
            "aps": [
                "alert": ["title": title, "body": body],
                "sound": "default",
                "interruption-level": "time-sensitive"
            ]
        ]
        if let cameraID { payloadDict["cameraID"] = cameraID.uuidString }
        let payload = try? JSONSerialization.data(withJSONObject: payloadDict)
        guard let payload else { return }

        var anySucceeded = false
        for token in tokens {
            if await sendOne(token: token, payload: payload, jwt: jwt, config: config) {
                anySucceeded = true
            }
        }
        if anySucceeded {
            lastSentAt = Date()
            statusMessage = "Sent \(tokens.count) push\(tokens.count == 1 ? "" : "es")"
        }
    }

    /// POSTs to APNs, retrying the opposite environment on BadDeviceToken so the
    /// caller never has to know whether a token is sandbox or production.
    private func sendOne(token: String, payload: Data, jwt: String, config: Config) async -> Bool {
        let firstHost = hostForToken[token] ?? Self.sandboxHost
        let secondHost = firstHost == Self.sandboxHost ? Self.productionHost : Self.sandboxHost

        for host in [firstHost, secondHost] {
            guard let url = URL(string: "https://\(host)/3/device/\(token)") else { continue }
            var request = URLRequest(url: url)
            request.httpMethod = "POST"
            request.httpBody = payload
            request.setValue("bearer \(jwt)", forHTTPHeaderField: "authorization")
            request.setValue(config.topic, forHTTPHeaderField: "apns-topic")
            request.setValue("alert", forHTTPHeaderField: "apns-push-type")
            request.setValue("10", forHTTPHeaderField: "apns-priority")

            guard let (data, response) = try? await URLSession.shared.data(for: request),
                  let http = response as? HTTPURLResponse else {
                lastError = "APNs unreachable"
                continue
            }
            if http.statusCode == 200 {
                hostForToken[token] = host   // remember the working environment
                return true
            }
            let reason = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["reason"] as? String
            // Wrong environment → fall through to the other host.
            if reason == "BadDeviceToken" || reason == "BadEnvironmentKeyInToken" {
                continue
            }
            lastError = "APNs \(http.statusCode): \(reason ?? "unknown")"
            return false
        }
        return false
    }

    // MARK: - JWT

    private func currentJWT(config: Config) throws -> String {
        if let cached = cachedJWT, Date().timeIntervalSince(cached.issuedAt) < 2_700 {
            return cached.token   // < 45 min old, reuse
        }
        let header = ["alg": "ES256", "kid": config.keyID]
        let payload = ["iss": config.teamID, "iat": Int(Date().timeIntervalSince1970)] as [String: Any]
        let headerB64 = try base64url(JSONSerialization.data(withJSONObject: header))
        let payloadB64 = try base64url(JSONSerialization.data(withJSONObject: payload))
        let signingInput = "\(headerB64).\(payloadB64)"
        let signature = try config.privateKey.signature(for: Data(signingInput.utf8))
        let sigB64 = base64url(signature.rawRepresentation)
        let token = "\(signingInput).\(sigB64)"
        cachedJWT = (token, Date())
        return token
    }

    private func base64url(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
