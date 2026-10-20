import AppKit
import SwiftUI
import SentinelCore

// Basics > Sentinel Cloud (wave 13 stream D; wave 12 stream E before it):
// one card, one sign-in, one button. Not linked: Email, Password, a Create
// account switch and [Turn On Sentinel Cloud]. The Mac signs in to the
// Sentinel Cloud account itself over HTTPS (the password never reaches the
// Sentinel server), then makes one call, `POST /api/v2/cloud/link` with the
// session JWT, and the server links by itself in about 2 s. Linked: remote
// address (with Copy), push notifications, offsite backup, then Subscribe
// (Stripe Checkout in the browser, then the card refreshes the plan every
// 10 s for 3 minutes) or Manage Plan (Billing Portal), and Turn Off. The
// same card as Sentinel Admin's `CloudPage` (WAVE13-CLOUD-ONBOARDING.md).
// Every decision lives in `SentinelCore/Management/CloudAccount.swift`.

/// Hands the site's `SentinelAPI` to `CloudPane` (what the Management window
/// registers for `ManagementCatalog.Leaf.cloud`). Every cloud route needs
/// `changeSettings`, so the pane is gated on it like the Admin page.
struct CloudPaneHost: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime

    var body: some View {
        if session.can(.changeSettings) {
            CloudPane(api: runtime.api)
        } else {
            PaneFrame(title: "Sentinel Cloud") {
                Text("Managing Sentinel Cloud needs the Change settings permission.")
                    .foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }
}

struct CloudPane: View {
    let api: SentinelAPI
    var isVisible: Bool = true
    var account = CloudAccountClient()

    @State private var card = CloudCard()
    /// The Sentinel Cloud account's session (a Supabase JWT), in memory only:
    /// never written to the Keychain, disk or the server, and gone when the
    /// window closes (Subscribe and Manage Plan then ask for the password).
    @State private var accountToken: String?
    @State private var password = ""
    @State private var plan: V2.CloudPlan = .cloud
    @State private var interval: CloudBillingInterval = .month
    @State private var confirmTurnOff = false
    @State private var optionsOpen = false
    @State private var copied: String?
    @State private var onScreen = false

    private var active: Bool { isVisible && onScreen }
    private var waitingForPayment: Bool { card.paymentWaitUntil != nil }
    private var linking: Bool {
        if case .linking = card.phase { return true }
        return false
    }

    var body: some View {
        PaneFrame(title: "Sentinel Cloud", subtitle: "Watch your cameras from anywhere, with push notifications and offsite backup.",
                  error: card.loadError, status: copied, trailing: {
            Button { Task { await load() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                .disabled(card.busy || linking)
        }) {
            ScrollView {
                cardBody
                    .padding(18)
                    .frame(maxWidth: 560, alignment: .leading)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
                    .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
                    .padding(SentinelMetrics.gutter)
                    .frame(maxWidth: .infinity, alignment: .topLeading)
            }
        }
        .trackOnScreen($onScreen)
        .task { await load() }
        // After opening Stripe Checkout: refresh the plan every 10 s for 3 minutes.
        .task(id: active && waitingForPayment) {
            await visibleInterval(active && waitingForPayment, seconds: CloudAccount.paymentPollSeconds) { await refreshPlan() }
        }
        // Otherwise a slow refresh, so the connection and backup figures stay current.
        .task(id: active && waitingForPayment == false) {
            await visibleInterval(active && waitingForPayment == false, seconds: CloudLogic.statusRefreshSeconds) { await load() }
        }
        .confirmationDialog("Turn off Sentinel Cloud for this server?", isPresented: $confirmTurnOff) {
            Button("Turn Off", role: .destructive) { Task { await turnOff() } }
        } message: {
            Text(CloudLogic.unlinkWarning)
        }
    }

    @ViewBuilder private var cardBody: some View {
        switch card.phase {
        case .loading:
            if card.loadError == nil {
                ProgressView().frame(maxWidth: .infinity, minHeight: 120)
            } else {
                VStack(alignment: .leading, spacing: 10) {
                    Text("Sentinel Cloud's status couldn't be read.").font(.headline)
                    Button("Try Again") { Task { await load() } }
                }
            }
        case .notLinked:
            notLinkedCard
        case .linking:
            linkingCard
        case .linked:
            if let status = card.status { linkedCard(status) }
        }
    }

    // MARK: Not linked

    private var notLinkedCard: some View {
        VStack(alignment: .leading, spacing: 14) {
            Label("Turn on Sentinel Cloud", systemImage: "cloud").font(.headline)
            Text(CloudLogic.intro).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(zip(CloudLogic.benefits, ["globe", "bell.badge", "externaldrive.badge.icloud"])), id: \.0) { text, symbol in
                    Label {
                        Text(text).font(.callout).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        Image(systemName: symbol).foregroundStyle(SentinelTheme.accent).frame(width: 20)
                    }
                }
            }
            Divider()
            if card.canSignIn {
                Text(card.createAccount ? "Create your Sentinel Cloud account with your email and a password."
                                        : "Sign in with your Sentinel Cloud account (the one you use on sentvms.com).")
                    .font(.callout).fixedSize(horizontal: false, vertical: true)
                signInFields
                Toggle("Create account", isOn: $card.createAccount)
                    .toggleStyle(.switch).controlSize(.small)
                Button(card.submitTitle) { submit() }
                    .buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(card.canSubmit(password: password) == false)
                messages
                if card.serverLimitReached {
                    Button("See Plans on sentvms.com") { open(CloudLogic.manageSubscriptionURL) }.buttonStyle(.link)
                }
            } else {
                Label("This server can't reach Sentinel Cloud right now, so sign-in is off. Press Refresh in a minute.",
                      systemImage: "exclamationmark.triangle")
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                messages
            }
        }
    }

    private var signInFields: some View {
        VStack(alignment: .leading, spacing: 8) {
            TextField("Email", text: $card.email)
                .textFieldStyle(.roundedBorder)
                .textContentType(.username)
                .autocorrectionDisabled()
            SecureField("Password", text: $password)
                .textFieldStyle(.roundedBorder)
                .textContentType(.password)
                .onSubmit { submit() }
        }
        .frame(maxWidth: 340)
        .disabled(card.busy)
    }

    /// The one-sentence error (or note) under the button.
    @ViewBuilder private var messages: some View {
        if let error = card.error {
            Label(error, systemImage: "exclamationmark.circle")
                .font(.callout).foregroundStyle(SentinelTheme.alarm).fixedSize(horizontal: false, vertical: true)
        }
        if let note = card.note {
            HStack(spacing: 6) {
                if waitingForPayment { ProgressView().controlSize(.small) }
                Text(note).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // MARK: Linking

    private var linkingCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Turn on Sentinel Cloud", systemImage: "cloud").font(.headline)
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(card.linkingText)
            }
            Text("This takes a few seconds.").font(.caption).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: 120, alignment: .topLeading)
    }

    // MARK: Linked

    private func linkedCard(_ s: V2.CloudStatus) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .firstTextBaseline) {
                Label("Sentinel Cloud is on", systemImage: "checkmark.icloud").font(.headline)
                Spacer()
                Text(CloudAccount.planSummary(s))
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 8).padding(.vertical, 3)
                    .background(SentinelTheme.well, in: Capsule())
            }
            if s.accountEmailMasked.isEmpty == false || CloudLogic.periodText(s) != nil {
                Text([s.accountEmailMasked.isEmpty ? nil : "Account \(s.accountEmailMasked)", CloudLogic.periodText(s)]
                        .compactMap { $0 }.joined(separator: " · "))
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            VStack(alignment: .leading, spacing: 8) {
                ForEach(CloudAccount.statusLines(s)) { lineView($0) }
            }
            if s.remote.error.isEmpty == false { caption(s.remote.error) }
            if s.backup.error.isEmpty == false { caption(s.backup.error) }
            Divider()
            if card.pendingAction != nil {
                reauthForm
            } else {
                planButtons(s)
            }
            messages
            DisclosureGroup("Options", isExpanded: $optionsOpen) { options(s) }
                .font(.callout)
        }
    }

    private func lineView(_ line: CloudStatusLine) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(line.label).foregroundStyle(.secondary).frame(width: 130, alignment: .leading)
            Circle().fill(color(line.tone)).frame(width: 7, height: 7)
            Text(line.value).textSelection(.enabled).lineLimit(1).truncationMode(.middle)
            if let value = line.copyValue {
                Button("Copy") { copy(value) }.controlSize(.small).help("Copy the remote address")
            }
            Spacer(minLength: 0)
        }
    }

    @ViewBuilder private func planButtons(_ s: V2.CloudStatus) -> some View {
        switch card.offer {
        case .manage:
            HStack(spacing: 10) {
                Button("Manage Plan…") { Task { await managePlan() } }
                    .buttonStyle(.borderedProminent).disabled(card.busy || CloudAccount.canSignIn(s.signIn) == false)
                Button("Turn Off…", role: .destructive) { confirmTurnOff = true }.disabled(card.busy)
                if card.busy { ProgressView().controlSize(.small) }
            }
        case .subscribe, .subscribeAgain:
            subscribeControls(s, again: card.offer == .subscribeAgain)
        case nil:
            EmptyView()
        }
    }

    @ViewBuilder private func subscribeControls(_ s: V2.CloudStatus, again: Bool) -> some View {
        let plans = s.signIn?.plans ?? []
        let chosen = plans.first { $0.id == plan } ?? plans.first
        VStack(alignment: .leading, spacing: 10) {
            if plans.isEmpty == false {
                Picker("Plan", selection: Binding(get: { chosen?.id ?? plan }, set: { plan = $0 })) {
                    ForEach(plans) { Text(CloudAccount.offerLabel($0)).tag($0.id) }
                }
                .frame(maxWidth: 360)
                Picker("Billing", selection: $interval) {
                    ForEach(CloudBillingInterval.allCases, id: \.self) { Text($0.label).tag($0) }
                }
                .pickerStyle(.segmented).frame(maxWidth: 360)
                if let chosen, CloudAccount.offerDetail(chosen).isEmpty == false {
                    caption(CloudAccount.offerDetail(chosen))
                }
            } else {
                caption("Plans can't be shown while this server can't reach Sentinel Cloud; press Refresh in a minute.")
            }
            HStack(spacing: 10) {
                Button(CloudAccount.subscribeTitle(chosen, interval: interval, again: again)) {
                    guard let id = chosen?.id else { return }
                    Task { await subscribe(id, interval) }
                }
                .buttonStyle(.borderedProminent)
                .disabled(card.busy || chosen == nil || CloudAccount.canSignIn(s.signIn) == false)
                Button("Turn Off…", role: .destructive) { confirmTurnOff = true }.disabled(card.busy)
                if card.busy { ProgressView().controlSize(.small) }
            }
            caption("Stripe Checkout opens in your browser; this card picks up the new plan by itself.")
        }
    }

    /// Subscribe / Manage Plan without the account session in memory.
    private var reauthForm: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Sign in to your Sentinel Cloud account to continue.").font(.callout)
            signInFields
            HStack(spacing: 10) {
                Button(card.submitTitle) { submit() }
                    .buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(card.canSubmit(password: password) == false)
                Button("Cancel") { card.cancelAccountPrompt() }.disabled(card.busy)
                if card.busy { ProgressView().controlSize(.small) }
            }
        }
    }

    private func options(_ s: V2.CloudStatus) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Toggle("Reach this server from anywhere through Sentinel Cloud", isOn: Binding(
                get: { card.status?.remote.enabled ?? false },
                set: { value in Task { await update(V2.CloudSettingsPatch(remote: value)) } }))
            if s.remote.enabled, let note = CloudLogic.featureNote(s, .relay) { caption(note) }
            Toggle("Back up alarm snapshots and clips to Sentinel Cloud", isOn: Binding(
                get: { card.status?.backup.enabled ?? false },
                set: { value in Task { await update(V2.CloudSettingsPatch(backup: value)) } }))
            if s.backup.enabled, let note = CloudLogic.featureNote(s, .backup) { caption(note) }
            caption("Push notifications are set in Rules and Events > Notifications.")
        }
        .disabled(card.busy)
        .padding(.top, 6)
    }

    private func caption(_ text: String) -> some View {
        Text(text).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
    }

    private func color(_ tone: CloudTone) -> Color {
        switch tone {
        case .good: return SentinelTheme.recording
        case .working: return SentinelTheme.accent
        case .warning: return SentinelTheme.amber
        case .bad: return SentinelTheme.alarm
        case .neutral: return .secondary
        }
    }

    // MARK: Actions

    private func load() async {
        do {
            let next = try await api.cloudStatus()
            guard Task.isCancelled == false else { return }
            card.loaded(next, now: Date())
        } catch {
            guard Task.isCancelled == false else { return }
            card.loadFailed(CloudLogic.errorText(error))
        }
    }

    /// One beat of the poll after Checkout.
    private func refreshPlan() async {
        guard card.isWaitingForPayment(now: Date()) else { return card.paymentWaitTimedOut() }
        let next: V2.CloudStatus
        do {
            next = try await api.refreshCloud()
        } catch {
            // Throttled, or a server without the refresh route: a plain read.
            guard let plain = try? await api.cloudStatus() else { return }
            next = plain
        }
        guard Task.isCancelled == false else { return }
        let wasWaiting = waitingForPayment
        card.loaded(next, now: Date())
        if wasWaiting, waitingForPayment == false, card.offer == .manage {
            NoticeCenter.shared.post(.success, "Your Sentinel Cloud plan is active.", key: "sentinel-cloud")
        }
    }

    /// The form's button: sign in to the account (or create it), then link,
    /// subscribe or open the Billing Portal.
    private func submit() {
        guard card.canSubmit(password: password), let config = card.status?.signIn else { return }
        let linked = card.phase == .linked
        guard let action = linked ? card.pendingAction : .link else { return }
        let createAccount = linked == false && card.createAccount
        let email = card.email
        let secret = password
        card.startSignIn()
        Task {
            do {
                switch try await account.signIn(config, email: email, password: secret, createAccount: createAccount) {
                case .confirmEmail:
                    card.needsEmailConfirmation()
                case .session(let token):
                    accountToken = token
                    password = ""
                    card.signedIn()
                    await run(action, token: token)
                }
            } catch {
                card.signInFailed(CloudAccount.errorText(error))
            }
        }
    }

    private func run(_ action: CloudCard.AccountAction, token: String) async {
        switch action {
        case .link: await link(token)
        case let .checkout(plan, interval): await checkout(plan, interval, token: token)
        case .portal: await portal(token: token)
        }
    }

    private func link(_ token: String) async {
        do {
            let next = try await api.linkCloud(accessToken: token)
            if next.linked {
                card.linked(next, now: Date())
                NoticeCenter.shared.post(.success, "Sentinel Cloud is on for this server.", key: "sentinel-cloud")
            } else {
                card.linkFailed(.other, message: "The server didn't finish linking; press Refresh in a moment.")
                card.loaded(next, now: Date())
            }
        } catch {
            let failure = CloudAccount.linkFailure(error)
            if failure == .signIn { accountToken = nil }
            card.linkFailed(failure, message: CloudAccount.linkErrorText(error))
            if failure == .alreadyLinked { await load() }
        }
    }

    private func subscribe(_ plan: V2.CloudPlan, _ interval: CloudBillingInterval) async {
        guard let token = accountToken else { return card.needsAccount(for: .checkout(plan, interval)) }
        card.beginAction()
        await checkout(plan, interval, token: token)
    }

    private func checkout(_ plan: V2.CloudPlan, _ interval: CloudBillingInterval, token: String) async {
        guard let config = card.status?.signIn else {
            return card.actionFailed("This server can't reach Sentinel Cloud right now; press Refresh in a minute.")
        }
        do {
            let url = try await account.checkoutURL(config, accessToken: token, plan: plan, interval: interval)
            open(url)
            card.openedCheckout(now: Date())
        } catch {
            accountFailed(error, retry: .checkout(plan, interval))
        }
    }

    private func managePlan() async {
        guard let token = accountToken else { return card.needsAccount(for: .portal) }
        card.beginAction()
        await portal(token: token)
    }

    private func portal(token: String) async {
        guard let config = card.status?.signIn else {
            return card.actionFailed("This server can't reach Sentinel Cloud right now; press Refresh in a minute.")
        }
        do {
            open(try await account.portalURL(config, accessToken: token))
            card.openedPortal()
        } catch {
            accountFailed(error, retry: .portal)
        }
    }

    /// An expired session asks for the password again, then repeats `retry`.
    private func accountFailed(_ error: Error, retry: CloudCard.AccountAction) {
        let expired = (error as? CloudAccountError)?.kind == .sessionExpired
        if expired {
            accountToken = nil
            password = ""
        }
        card.actionFailed(CloudAccount.errorText(error), sessionExpired: expired, retry: retry)
    }

    private func turnOff() async {
        card.beginAction()
        do {
            let next = try await api.unlinkCloud()
            accountToken = nil
            card.unlinked(next)
            NoticeCenter.shared.post(.success, "Sentinel Cloud is off for this server.", key: "sentinel-cloud")
        } catch {
            card.actionFailed(CloudAccount.errorText(error))
        }
    }

    private func update(_ patch: V2.CloudSettingsPatch) async {
        card.beginAction()
        do {
            card.actionSucceeded(try await api.updateCloud(patch), now: Date())
        } catch {
            card.actionFailed(CloudAccount.errorText(error))
            await load()
        }
    }

    private func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        copied = "Copied the remote address."
    }

    /// Only `https://` pages reach the browser (`CloudAccount` checked them).
    private func open(_ url: URL) {
        NSWorkspace.shared.open(url)
    }
}

#Preview("Sentinel Cloud") { CloudPane(api: ManagementPreview.api()).frame(width: 640, height: 560) }
