import Foundation

/// Sentinel Cloud calls: wave 12 (`WAVE12-CLOUD.md` section 4) and the
/// wave 13 one-call link (`WAVE13-CLOUD-ONBOARDING.md`). Management Server
/// only (a Recording Server answers 404); every call needs `changeSettings`
/// and answers the full `CloudStatus`.
public extension SentinelAPI {
    func cloudStatus() async throws -> V2.CloudStatus {
        try await send("GET", "/api/v2/cloud")
    }

    /// Turns remote access and/or offsite backup on or off.
    func updateCloud(_ patch: V2.CloudSettingsPatch) async throws -> V2.CloudStatus {
        try await send("PUT", "/api/v2/cloud", body: patch)
    }

    /// Starts linking: the answer carries `link` (the code to approve on
    /// sentvms.com); the server polls the control plane in the background
    /// until the code is approved or expires. (The wave 12 code flow; the
    /// Mac app now links with `linkCloud(accessToken:)`.)
    func startCloudLink() async throws -> V2.CloudStatus {
        try await send("POST", "/api/v2/cloud/link")
    }

    /// Wave 13: links this server with the Sentinel Cloud account's session
    /// (the Supabase access token the app got by signing in over HTTPS; the
    /// password never reaches the server). The server finishes the link by
    /// itself in about 2 s and answers the linked status. Errors carry the
    /// contract's codes: `cloud_sign_in` (401), `cloud_server_limit` (402),
    /// `cloud_already_linked` (409), `cloud_unreachable` (502);
    /// `CloudAccount.linkErrorText` turns them into one sentence.
    func linkCloud(accessToken: String) async throws -> V2.CloudStatus {
        try await send("POST", "/api/v2/cloud/link", body: V2.CloudLinkRequest(accessToken: accessToken))
    }

    /// Wave 13: refreshes the plan from Sentinel Cloud right away (the
    /// server allows it at most once per 10 s) and answers the status. The
    /// Mac polls it after opening Stripe Checkout in the browser.
    func refreshCloud() async throws -> V2.CloudStatus {
        try await send("POST", "/api/v2/cloud/refresh")
    }

    /// Unlinks this server (the server also tells Sentinel Cloud to forget
    /// it) and drops a pending link code.
    func unlinkCloud() async throws -> V2.CloudStatus {
        try await send("DELETE", "/api/v2/cloud/link")
    }
}
