import Foundation

// Wave 13 stream D (`WAVE13-CLOUD-ONBOARDING.md`): the account side of the
// Management window's one Sentinel Cloud card, pure and tested.
//
// - Requests to Supabase (sign in / create account) and to the two billing
//   functions (Stripe Checkout, Billing Portal), built from the server's
//   `CloudStatus.signIn`. HTTPS only: an address that isn't `https://` is
//   refused before anything is sent, so the password only ever goes to the
//   account service, never to the Sentinel server.
// - Their answers, and every error, as one sentence for the card.
// - The server's link errors (`cloud_sign_in`, `cloud_server_limit`, …).
// - `CloudCard`, the card's state machine: loading → notLinked(form) →
//   linking → linked, with the buttons by plan.
// - The three status lines of a linked server.
//
// The account session (a Supabase JWT) never lives here: the pane keeps it
// in a `@State` only, and it is never written to the Keychain, disk or the
// server. `CloudAccountClient` runs the requests over an ephemeral
// URLSession (no cache, no cookies).

/// What Stripe Checkout bills: `month | year`.
public enum CloudBillingInterval: String, CaseIterable, Codable, Hashable, Sendable {
    case month, year

    public var label: String { self == .month ? "Monthly" : "Yearly" }
}

/// What signing in (or creating the account) gave.
public enum CloudAuthOutcome: Equatable, Sendable {
    /// A session; its access token goes to `POST /api/v2/cloud/link` or to
    /// checkout/portal and stays in the card's memory.
    case session(accessToken: String)
    /// The account was created, but its email has to be confirmed first.
    case confirmEmail
}

/// A failed account request, as one sentence.
public struct CloudAccountError: Error, Equatable, LocalizedError, Sendable {
    public enum Kind: Equatable, Sendable {
        /// The account service said no (wrong password, unconfirmed email,
        /// account exists, weak password, too many attempts).
        case rejected
        /// The session was refused (it expires after an hour): sign in again.
        case sessionExpired
        /// The server handed out an address that isn't HTTPS (or none), so
        /// nothing was sent.
        case insecure
        /// No answer: offline, DNS, TLS.
        case network
        /// Anything else.
        case failed
    }

    public var kind: Kind
    public var message: String

    public init(_ kind: Kind, _ message: String) {
        self.kind = kind
        self.message = message
    }

    public var errorDescription: String? { message }
}

/// How the server's `POST /api/v2/cloud/link` failed.
public enum CloudLinkFailure: Equatable, Sendable {
    /// `cloud_sign_in` (401): Sentinel Cloud refused the session; sign in again.
    case signIn
    /// `cloud_server_limit` (402): the plan has no room for another server.
    case serverLimit
    /// `cloud_already_linked` (409): reload; the server is linked.
    case alreadyLinked
    /// `cloud_unreachable` (502).
    case unreachable
    case other
}

/// One of the linked card's three status lines.
public struct CloudStatusLine: Equatable, Identifiable, Sendable {
    /// `remote | push | backup`.
    public var id: String
    public var label: String
    public var value: String
    public var tone: CloudTone
    /// What the Copy button puts on the pasteboard; nil = no button.
    public var copyValue: String?

    public init(id: String, label: String, value: String, tone: CloudTone = .neutral, copyValue: String? = nil) {
        self.id = id
        self.label = label
        self.value = value
        self.tone = tone
        self.copyValue = copyValue
    }
}

public enum CloudAccount {
    /// Where Stripe sends the browser back after paying (the Mac has no local
    /// page to return to; the card notices the plan by polling the server).
    public static let checkoutSuccessURL = "https://sentvms.com/cloud?checkout=success"
    public static let checkoutCancelURL = "https://sentvms.com/cloud"
    public static let portalReturnURL = "https://sentvms.com/cloud"
    /// After opening Checkout: `POST /api/v2/cloud/refresh` this often …
    public static let paymentPollSeconds: UInt64 = 10
    /// … for this long.
    public static let paymentPollWindow: TimeInterval = 180
    /// Per request to the account service.
    public static let requestTimeout: TimeInterval = 30

    // MARK: Addresses

    /// `text` as a URL only when it is `https://` with a host; nothing else
    /// is sent a password or a session, or opened in the browser.
    public static func secureURL(_ text: String) -> URL? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: trimmed), url.scheme?.lowercased() == "https",
              let host = url.host, host.isEmpty == false else { return nil }
        return url
    }

    /// Whether the server's `signIn` lets the card sign in (an HTTPS
    /// Supabase address and a key).
    public static func canSignIn(_ config: V2.CloudSignIn?) -> Bool {
        guard let config else { return false }
        return secureURL(config.supabaseUrl) != nil && config.supabaseAnonKey.isEmpty == false
    }

    static let insecureAddress = CloudAccountError(.insecure,
        "This server handed out a Sentinel Cloud address that isn't secure (https), so nothing was sent.")

    // MARK: Requests

    /// `POST {supabaseUrl}/auth/v1/token?grant_type=password` (sign in) or
    /// `POST {supabaseUrl}/auth/v1/signup` (create account), headers
    /// `apikey` + `content-type: application/json`, body `{email, password}`.
    public static func authRequest(_ config: V2.CloudSignIn, email: String, password: String,
                                   createAccount: Bool) throws -> URLRequest {
        guard canSignIn(config), let base = secureURL(config.supabaseUrl) else { throw insecureAddress }
        var root = base.absoluteString
        while root.hasSuffix("/") { root.removeLast() }
        // WAVE13: the confirmation e-mail's link lands on the cloud page, not the
        // home page (which starts a Mac download by itself). Same URL the web card
        // and cloud.html use, so it is on Supabase's redirect allow-list.
        let site = config.siteBase.isEmpty ? "https://sentvms.com" : config.siteBase
        let redirect = (site + "/cloud").addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? ""
        let path = createAccount ? "/auth/v1/signup?redirect_to=\(redirect)" : "/auth/v1/token?grant_type=password"
        guard let url = URL(string: root + path) else { throw insecureAddress }
        var request = jsonRequest(url, apikey: config.supabaseAnonKey)
        request.httpBody = try encode(["email": normalizedEmail(email), "password": password])
        return request
    }

    /// `POST checkoutURL` with `Authorization: Bearer <jwt>` and `apikey`,
    /// body `{plan, interval, success_url, cancel_url}` → `{url}`.
    public static func checkoutRequest(_ config: V2.CloudSignIn, accessToken: String, plan: V2.CloudPlan,
                                       interval: CloudBillingInterval) throws -> URLRequest {
        guard let url = secureURL(config.checkoutURL) else { throw insecureAddress }
        var request = jsonRequest(url, apikey: config.supabaseAnonKey, bearer: accessToken)
        request.httpBody = try encode(["plan": plan.rawValue, "interval": interval.rawValue,
                                       "success_url": checkoutSuccessURL, "cancel_url": checkoutCancelURL])
        return request
    }

    /// `POST portalURL`, same headers, body `{return_url}` → `{url}`.
    public static func portalRequest(_ config: V2.CloudSignIn, accessToken: String) throws -> URLRequest {
        guard let url = secureURL(config.portalURL) else { throw insecureAddress }
        var request = jsonRequest(url, apikey: config.supabaseAnonKey, bearer: accessToken)
        request.httpBody = try encode(["return_url": portalReturnURL])
        return request
    }

    /// Trimmed; Supabase compares emails case-insensitively anyway.
    public static func normalizedEmail(_ email: String) -> String {
        email.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func jsonRequest(_ url: URL, apikey: String, bearer: String? = nil) -> URLRequest {
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: requestTimeout)
        request.httpMethod = "POST"
        request.setValue(apikey, forHTTPHeaderField: "apikey")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let bearer { request.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization") }
        return request
    }

    private static func encode(_ body: [String: String]) throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return try encoder.encode(body)
    }

    // MARK: Responses

    /// The answer of `authRequest`: a session (`access_token` at the top, or
    /// under `session`), or, after creating an account that needs
    /// confirming (`{user, session: null}` or a bare user), `.confirmEmail`.
    public static func authOutcome(status: Int, body: Data, createAccount: Bool) throws -> CloudAuthOutcome {
        guard (200..<300).contains(status) else {
            throw CloudAccountError(status >= 500 ? .failed : .rejected, authErrorText(status: status, body: body))
        }
        let json = object(body)
        if let token = json["access_token"] as? String, token.isEmpty == false {
            return .session(accessToken: token)
        }
        if let session = json["session"] as? [String: Any], let token = session["access_token"] as? String, token.isEmpty == false {
            return .session(accessToken: token)
        }
        guard createAccount else {
            throw CloudAccountError(.failed, "Sentinel Cloud's sign-in answered without a session; try again.")
        }
        return .confirmEmail
    }

    /// The `{url}` answer of checkout/portal: an `https://` page only. A
    /// refused session (401) is `.sessionExpired`.
    public static func redirectURL(status: Int, body: Data) throws -> URL {
        if status == 401 {
            throw CloudAccountError(.sessionExpired, "Your Sentinel Cloud sign-in expired; enter your password again.")
        }
        guard (200..<300).contains(status) else {
            let message = errorMessage(in: body).map { sentence($0.message) }
            throw CloudAccountError(.failed, message ?? statusText(status, service: "billing"))
        }
        guard let text = object(body)["url"] as? String, let url = secureURL(text) else {
            throw CloudAccountError(.failed, "Sentinel Cloud's billing answered without a page to open; try again.")
        }
        return url
    }

    /// One sentence for a failed sign-in / sign-up, from Supabase's
    /// `{error_description | msg | message}` (and the error code).
    public static func authErrorText(status: Int, body: Data) -> String {
        let found = errorMessage(in: body)
        let code = found?.code.lowercased() ?? ""
        let message = found?.message ?? ""
        let lower = message.lowercased()
        if code == "invalid_credentials" || lower.contains("invalid login credentials") {
            return "The email or password is wrong."
        }
        if code == "email_not_confirmed" || lower.contains("email not confirmed") {
            return "Confirm your email first (open the link Sentinel Cloud sent you), then sign in."
        }
        if code == "user_already_exists" || code == "email_exists" || lower.contains("already registered") {
            return "There is already an account with this email; switch off Create account and sign in."
        }
        if status == 429 || code.contains("rate_limit") {
            return "Too many attempts; wait a minute and try again."
        }
        if message.isEmpty == false { return sentence(message) }
        return statusText(status, service: "sign-in")
    }

    /// The first message in an error body: `error_description`, `msg`,
    /// `message`, then `error` (a string, or `{message}`), with the code
    /// beside it (`error_code`, `code`, or `error` when it is a code).
    public static func errorMessage(in body: Data) -> (code: String, message: String)? {
        let json = object(body)
        guard json.isEmpty == false else { return nil }
        func text(_ key: String) -> String? {
            guard let value = json[key] as? String else { return nil }
            let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? nil : trimmed
        }
        let nested = json["error"] as? [String: Any]
        let code = text("error_code") ?? text("code") ?? (nested?["code"] as? String)
            ?? (text("error_description") != nil ? text("error") : nil) ?? ""
        let message = text("error_description") ?? text("msg") ?? text("message")
            ?? text("error") ?? (nested?["message"] as? String)
        guard let message, message.isEmpty == false else { return code.isEmpty ? nil : (code, "") }
        return (code, message)
    }

    private static func statusText(_ status: Int, service: String) -> String {
        switch status {
        case 429: return "Too many attempts; wait a minute and try again."
        case 500...: return "Sentinel Cloud's \(service) isn't answering right now; try again in a minute."
        default: return "Sentinel Cloud's \(service) failed (HTTP \(status))."
        }
    }

    private static func object(_ body: Data) -> [String: Any] {
        ((try? JSONSerialization.jsonObject(with: body)) as? [String: Any]) ?? [:]
    }

    /// `text` as one sentence: whitespace collapsed, only the first
    /// sentence kept, capitalized, ending in a full stop.
    public static func sentence(_ text: String) -> String {
        var out = text.replacingOccurrences(of: #"\s+"#, with: " ", options: .regularExpression)
            .trimmingCharacters(in: .whitespaces)
        guard out.isEmpty == false else { return "" }
        if let end = out.range(of: #"[.!?](?=\s+[A-Z])"#, options: .regularExpression) {
            out = String(out[..<end.upperBound])
        }
        out = out.prefix(1).uppercased() + out.dropFirst()
        if let last = out.last, ".!?…".contains(last) == false { out += "." }
        return out
    }

    /// Any error the card shows, as one sentence.
    public static func errorText(_ error: Error) -> String {
        if let account = error as? CloudAccountError { return account.message }
        if let url = error as? URLError { return sentence(SentinelAPI.describe(url)) }
        return sentence(CloudLogic.errorText(error))
    }

    // MARK: Server link errors

    /// Classifies a failed `POST /api/v2/cloud/link` by the contract's code,
    /// or by its status when the body isn't the v2 error shape.
    public static func linkFailure(_ error: Error) -> CloudLinkFailure {
        guard case let .server(status, code, _)? = error as? SentinelAPIError else { return .other }
        switch code {
        case "cloud_sign_in": return .signIn
        case "cloud_server_limit": return .serverLimit
        case "cloud_already_linked": return .alreadyLinked
        case "cloud_unreachable": return .unreachable
        default: break
        }
        switch status {
        case 401: return .signIn
        case 402: return .serverLimit
        case 409: return .alreadyLinked
        case 502: return .unreachable
        default: return .other
        }
    }

    /// One sentence for a failed link.
    public static func linkErrorText(_ error: Error) -> String {
        switch linkFailure(error) {
        case .signIn: return "Sentinel Cloud didn't accept the sign-in; enter your password again."
        case .serverLimit: return "Your Sentinel Cloud plan has no room for another server; upgrade it, or unlink a server, on sentvms.com."
        case .alreadyLinked: return "This server is already linked to Sentinel Cloud."
        case .unreachable: return "This server can't reach Sentinel Cloud right now; try again in a minute."
        case .other: return errorText(error)
        }
    }

    // MARK: Linked card

    /// The buttons a linked server gets (contract "UI states"): free or
    /// inactive → Subscribe; active or grace on a paid plan → Manage plan;
    /// canceled → Subscribe again.
    public static func offer(for status: V2.CloudStatus) -> CloudCard.Offer {
        if status.plan == .free { return .subscribe }
        switch status.status {
        case .active, .grace: return .manage
        case .canceled: return .subscribeAgain
        default: return .subscribe
        }
    }

    /// "Sentinel Cloud · Active", "Free", "Sentinel Cloud Business · Payment past due".
    public static func planSummary(_ status: V2.CloudStatus) -> String {
        let plan = CloudLogic.planLabel(status.plan)
        if status.plan == .free { return plan }
        return "\(plan) · \(CloudLogic.statusLabel(status.status).text)"
    }

    /// The three lines: remote address (with Copy when connected), push
    /// notifications, offsite backup.
    public static func statusLines(_ s: V2.CloudStatus) -> [CloudStatusLine] {
        [remoteLine(s), pushLine(s), backupLine(s)]
    }

    /// `connected · https://<id>-cloud.sentvms.com` / `connecting…` / `off`.
    public static func remoteLine(_ s: V2.CloudStatus) -> CloudStatusLine {
        let url = s.remote.url.trimmingCharacters(in: .whitespaces)
        guard s.remote.enabled else { return CloudStatusLine(id: "remote", label: "Remote address", value: "off") }
        switch s.remote.state {
        case .connected:
            return CloudStatusLine(id: "remote", label: "Remote address", value: url.isEmpty ? "connected" : "connected · \(url)",
                                   tone: .good, copyValue: url.isEmpty ? nil : url)
        case .connecting, .retrying:
            return CloudStatusLine(id: "remote", label: "Remote address", value: "connecting…", tone: .working)
        default:
            return CloudStatusLine(id: "remote", label: "Remote address", value: "off")
        }
    }

    /// `through Sentinel Cloud` / `your own APNs key` / `off`.
    public static func pushLine(_ s: V2.CloudStatus) -> CloudStatusLine {
        switch s.push.state {
        case .cloud: return CloudStatusLine(id: "push", label: "Push notifications", value: "through Sentinel Cloud", tone: .good)
        case .direct: return CloudStatusLine(id: "push", label: "Push notifications", value: "your own APNs key", tone: .good)
        default: return CloudStatusLine(id: "push", label: "Push notifications", value: "off")
        }
    }

    /// `on · 1.2 GB of 10 GB` / `off` (off too when the plan has no backup).
    public static func backupLine(_ s: V2.CloudStatus) -> CloudStatusLine {
        guard s.backup.enabled, s.has(.backup) else {
            return CloudStatusLine(id: "backup", label: "Offsite backup", value: "off")
        }
        let quota = s.quotas.backupBytes
        let amount = quota > 0
            ? "\(CloudLogic.bytes(s.backup.bytesUsed)) of \(CloudLogic.bytes(quota))"
            : "\(CloudLogic.bytes(s.backup.bytesUsed)) used"
        let tone = CloudLogic.usageTone(used: s.backup.bytesUsed, quota: quota)
        return CloudStatusLine(id: "backup", label: "Offsite backup", value: "on · \(amount)", tone: tone == .neutral ? .good : tone)
    }

    // MARK: Plans for sale

    /// "$4.99", "$49".
    public static func price(_ amount: Double) -> String {
        let cents = (amount * 100).rounded()
        if cents.truncatingRemainder(dividingBy: 100) == 0 { return "$\(Int(cents / 100))" }
        return String(format: "$%.2f", cents / 100)
    }

    /// "Sentinel Cloud" stays; "Sentinel Cloud Business" reads "Business".
    public static func shortName(_ offer: V2.CloudPlanOffer) -> String {
        let name = offer.name.trimmingCharacters(in: .whitespaces)
        if name.isEmpty { return CloudLogic.planLabel(offer.id) }
        let prefix = "Sentinel Cloud "
        if name.hasPrefix(prefix), name.count > prefix.count { return String(name.dropFirst(prefix.count)) }
        return name
    }

    /// The picker's entry: "Sentinel Cloud  $4.99/mo · $49/yr".
    public static func offerLabel(_ offer: V2.CloudPlanOffer) -> String {
        "\(shortName(offer))  \(price(offer.month))/mo · \(price(offer.year))/yr"
    }

    /// "1 server · 10 GB backup · 30 days"; parts that are 0 are left out.
    public static func offerDetail(_ offer: V2.CloudPlanOffer) -> String {
        var parts: [String] = []
        if offer.servers > 0 { parts.append(offer.servers == 1 ? "1 server" : "\(offer.servers) servers") }
        if offer.backupGB > 0 { parts.append("\(offer.backupGB) GB backup") }
        if offer.days > 0 { parts.append(offer.days == 1 ? "1 day" : "\(offer.days) days") }
        return parts.joined(separator: " · ")
    }

    /// The Subscribe button: "Subscribe · $4.99/month".
    public static func subscribeTitle(_ offer: V2.CloudPlanOffer?, interval: CloudBillingInterval, again: Bool) -> String {
        let verb = again ? "Subscribe Again" : "Subscribe"
        guard let offer else { return verb }
        let amount = interval == .month ? offer.month : offer.year
        guard amount > 0 else { return verb }
        return "\(verb) · \(price(amount))/\(interval.rawValue)"
    }
}

// MARK: - The card's state machine

/// The Sentinel Cloud card (contract "UI states"): `loading` →
/// `notLinked(form)` → `linking` → `linked`, whose buttons follow the plan.
/// Errors are one sentence under the button. The pane keeps one in a
/// `@State` and feeds it events; the account JWT and the password stay in
/// the pane's own `@State`s, never in here.
public struct CloudCard: Equatable, Sendable {
    public enum Phase: Equatable, Sendable {
        case loading
        /// The sign-in form: email, password, Create account, Turn on.
        case notLinked
        /// Signing in to the account, then the server's one call (~2 s).
        case linking(LinkStep)
        case linked
    }

    public enum LinkStep: Equatable, Sendable {
        case signingIn, linking
    }

    /// A linked card's buttons.
    public enum Offer: Equatable, Sendable {
        /// Free or inactive: Subscribe (plan picker).
        case subscribe
        /// Active or grace: Manage plan, Turn off.
        case manage
        /// Canceled: Subscribe again.
        case subscribeAgain
    }

    /// What the account session is needed for.
    public enum AccountAction: Equatable, Sendable {
        case link
        case checkout(V2.CloudPlan, CloudBillingInterval)
        case portal
    }

    public private(set) var phase: Phase = .loading
    public private(set) var status: V2.CloudStatus?
    public var email = ""
    /// The Create account switch (not-linked form only).
    public var createAccount = false
    /// An account was created that needs its email confirmed: the card says
    /// so and the button reads "I confirmed it, sign me in".
    public private(set) var awaitingConfirmation = false
    /// Linked, and Subscribe or Manage plan needs the account session the
    /// card no longer has: the inline email + password.
    public private(set) var pendingAction: AccountAction?
    /// The one sentence under the button.
    public private(set) var error: String?
    /// A neutral line under the button (check your email, waiting for payment).
    public private(set) var note: String?
    /// An account or server call is running while linked.
    public private(set) var busy = false
    /// After opening Stripe Checkout, the card refreshes the plan until then.
    public private(set) var paymentWaitUntil: Date?
    /// The status couldn't be read (the pane's header line).
    public private(set) var loadError: String?
    /// The last link failed because the plan is full: offer the plans page.
    public private(set) var serverLimitReached = false

    public init() {}

    // MARK: Derived

    public var isLinked: Bool { status?.linked ?? false }

    /// The linked card's buttons; nil when not linked.
    public var offer: Offer? {
        guard let status, status.linked else { return nil }
        return CloudAccount.offer(for: status)
    }

    /// The server handed out a usable sign-in (else the form explains why it's off).
    public var canSignIn: Bool { CloudAccount.canSignIn(status?.signIn) }

    /// Whether the email/password form is showing.
    public var showsForm: Bool {
        switch phase {
        case .notLinked: return true
        case .linked: return pendingAction != nil
        default: return false
        }
    }

    /// Whether the form's button can be pressed.
    public func canSubmit(password: String) -> Bool {
        guard canSignIn, showsForm, busy == false else { return false }
        let email = CloudAccount.normalizedEmail(self.email)
        return email.contains("@") && email.count >= 3 && password.isEmpty == false
    }

    /// The form's button.
    public var submitTitle: String {
        if phase == .linked, pendingAction != nil { return "Sign In and Continue" }
        if awaitingConfirmation { return "I Confirmed It, Sign Me In" }
        return "Turn On Sentinel Cloud"
    }

    /// The spinner's words while linking.
    public var linkingText: String {
        switch phase {
        case .linking(.signingIn): return createAccount ? "Creating your account…" : "Signing in to Sentinel Cloud…"
        case .linking(.linking): return "Linking this server…"
        default: return ""
        }
    }

    public func isWaitingForPayment(now: Date) -> Bool {
        guard let until = paymentWaitUntil else { return false }
        return now < until
    }

    // MARK: Events

    /// A status read (`GET /api/v2/cloud`, a refresh, or any write's answer).
    public mutating func loaded(_ next: V2.CloudStatus, now: Date = Date()) {
        status = next
        loadError = nil
        if next.linked {
            if phase != .linked { pendingAction = nil }
            phase = .linked
            if let until = paymentWaitUntil {
                if CloudAccount.offer(for: next) == .manage {
                    paymentWaitUntil = nil
                    note = "Your Sentinel Cloud plan is active."
                } else if now >= until {
                    paymentWaitTimedOut()
                }
            }
        } else {
            pendingAction = nil
            paymentWaitUntil = nil
            // A background read never interrupts a sign-in in flight.
            if case .linking = phase { return }
            if phase != .notLinked { busy = false }
            phase = .notLinked
        }
    }

    public mutating func loadFailed(_ message: String) {
        loadError = message
    }

    /// The form's button was pressed (linking from the not-linked form, or
    /// the inline sign-in for `pendingAction`).
    public mutating func startSignIn() {
        error = nil
        note = nil
        serverLimitReached = false
        if phase == .linked {
            busy = true
        } else {
            phase = .linking(.signingIn)
        }
    }

    /// The account answered with a session; the action runs next.
    public mutating func signedIn() {
        awaitingConfirmation = false
        createAccount = false
        if case .linking = phase { phase = .linking(.linking) }
    }

    /// Created an account that needs its email confirmed.
    public mutating func needsEmailConfirmation() {
        awaitingConfirmation = true
        createAccount = false
        busy = false
        error = nil
        note = "Check your email, then sign in."
        if case .linking = phase { phase = .notLinked }
    }

    public mutating func signInFailed(_ message: String) {
        error = message
        busy = false
        if case .linking = phase { phase = .notLinked }
    }

    /// The server linked (its answer).
    public mutating func linked(_ next: V2.CloudStatus, now: Date = Date()) {
        busy = false
        error = nil
        note = nil
        awaitingConfirmation = false
        serverLimitReached = false
        phase = next.linked ? .linked : .notLinked
        loaded(next, now: now)
    }

    public mutating func linkFailed(_ failure: CloudLinkFailure, message: String) {
        busy = false
        error = message
        serverLimitReached = failure == .serverLimit
        if case .linking = phase { phase = .notLinked }
    }

    /// Linked: Subscribe or Manage plan was pressed without a session in memory.
    public mutating func needsAccount(for action: AccountAction, message: String? = nil) {
        guard phase == .linked else { return }
        pendingAction = action
        busy = false
        error = message
        note = nil
    }

    public mutating func cancelAccountPrompt() {
        pendingAction = nil
        error = nil
        busy = false
    }

    /// A linked-card call (checkout, portal, turn off) started.
    public mutating func beginAction() {
        busy = true
        error = nil
        note = nil
    }

    /// A linked-card server call (a switch under Options) answered.
    public mutating func actionSucceeded(_ next: V2.CloudStatus, now: Date = Date()) {
        busy = false
        loaded(next, now: now)
    }

    /// The Billing Portal opened in the browser.
    public mutating func openedPortal() {
        busy = false
        pendingAction = nil
        note = "Sentinel Cloud's billing page opened in your browser."
    }

    /// Stripe Checkout opened in the browser: refresh the plan every
    /// `paymentPollSeconds` for `paymentPollWindow`.
    public mutating func openedCheckout(now: Date = Date()) {
        busy = false
        pendingAction = nil
        paymentWaitUntil = now.addingTimeInterval(CloudAccount.paymentPollWindow)
        note = "Finish paying in your browser; this card updates by itself."
    }

    /// The poll after Checkout gave up (or the pane stopped waiting).
    public mutating func paymentWaitTimedOut() {
        guard paymentWaitUntil != nil else { return }
        paymentWaitUntil = nil
        note = "If you finished paying, press Refresh in a minute to see your plan."
    }

    /// A checkout/portal/turn-off call failed. An expired session asks for
    /// the password again, then repeats `retry`.
    public mutating func actionFailed(_ message: String, sessionExpired: Bool = false, retry: AccountAction? = nil) {
        busy = false
        if sessionExpired, let retry {
            needsAccount(for: retry, message: message)
        } else {
            error = message
        }
    }

    /// Turned off (the server's answer to `DELETE /api/v2/cloud/link`).
    public mutating func unlinked(_ next: V2.CloudStatus) {
        busy = false
        error = nil
        note = nil
        pendingAction = nil
        paymentWaitUntil = nil
        awaitingConfirmation = false
        phase = next.linked ? .linked : .notLinked
        status = next
    }
}

// MARK: - Client

/// Runs `CloudAccount`'s requests (HTTPS only) and parses their answers.
/// Tests hand it a stub-protocol session; the app uses `ephemeral`.
public struct CloudAccountClient: @unchecked Sendable {
    public let session: URLSession

    /// No URL cache, no cookies: the password and the session never touch disk.
    public static let ephemeral: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.urlCache = nil
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.timeoutIntervalForRequest = CloudAccount.requestTimeout
        return URLSession(configuration: config)
    }()

    public init(session: URLSession = CloudAccountClient.ephemeral) {
        self.session = session
    }

    /// Signs in (or creates the account) with Supabase.
    public func signIn(_ config: V2.CloudSignIn, email: String, password: String,
                       createAccount: Bool) async throws -> CloudAuthOutcome {
        let request = try CloudAccount.authRequest(config, email: email, password: password, createAccount: createAccount)
        let (data, status) = try await send(request)
        return try CloudAccount.authOutcome(status: status, body: data, createAccount: createAccount)
    }

    /// The Stripe Checkout page for `plan`/`interval`.
    public func checkoutURL(_ config: V2.CloudSignIn, accessToken: String, plan: V2.CloudPlan,
                            interval: CloudBillingInterval) async throws -> URL {
        let request = try CloudAccount.checkoutRequest(config, accessToken: accessToken, plan: plan, interval: interval)
        let (data, status) = try await send(request)
        return try CloudAccount.redirectURL(status: status, body: data)
    }

    /// The Stripe Billing Portal page.
    public func portalURL(_ config: V2.CloudSignIn, accessToken: String) async throws -> URL {
        let request = try CloudAccount.portalRequest(config, accessToken: accessToken)
        let (data, status) = try await send(request)
        return try CloudAccount.redirectURL(status: status, body: data)
    }

    private func send(_ request: URLRequest) async throws -> (Data, Int) {
        // Belt and braces: the builders only make https requests.
        guard request.url?.scheme?.lowercased() == "https" else { throw CloudAccount.insecureAddress }
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else {
                throw CloudAccountError(.failed, "Sentinel Cloud's answer wasn't understood; try again.")
            }
            return (data, http.statusCode)
        } catch let error as CloudAccountError {
            throw error
        } catch let error as URLError {
            throw CloudAccountError(.network, CloudAccount.networkText(error))
        } catch {
            throw CloudAccountError(.network, CloudAccount.sentence(error.localizedDescription))
        }
    }
}

extension CloudAccount {
    /// One sentence for a request that got no answer.
    static func networkText(_ error: URLError) -> String {
        switch error.code {
        case .notConnectedToInternet, .networkConnectionLost:
            return "This Mac is offline; connect to the internet and try again."
        case .cannotFindHost, .cannotConnectToHost, .dnsLookupFailed, .timedOut:
            return "Can't reach Sentinel Cloud from this Mac; check the internet connection and try again."
        case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateNotYetValid,
             .serverCertificateHasUnknownRoot, .secureConnectionFailed:
            return "Sentinel Cloud's secure connection couldn't be verified, so nothing was sent."
        default:
            return sentence(error.localizedDescription)
        }
    }
}
