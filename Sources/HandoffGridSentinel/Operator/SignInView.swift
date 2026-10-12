import SwiftUI
import SentinelCore

/// Sign in to a site's management server: pick a saved site or type an
/// address, then a name and password and/or "Sign in with …" (single sign-on),
/// depending on what the server offers (`GET /api/v2/auth/providers`).
struct SignInView: View {
    @EnvironmentObject private var session: OperatorSession
    @ObservedObject var sites: SiteDirectory
    var hostServer: (() -> Void)?

    @State private var address = ""
    @State private var siteName = ""
    @State private var userName = ""
    @State private var password = ""
    @State private var providers: V2.AuthProviders?
    @State private var providersError: String?
    @State private var isCheckingAddress = false
    @State private var isWorking = false
    @State private var error: String?
    @FocusState private var focus: Field?

    private enum Field { case address, name, password }

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            VStack(spacing: 26) {
                HandoffGridMark(size: 52)

                VStack(spacing: 6) {
                    Text("Sign in to Sentinel")
                        .font(.title2.weight(.semibold))
                    Text("Sentinel Operator connects to your site's Management Server.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }

                if let notice = session.notice {
                    Label(notice, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(SentinelTheme.amber)
                        .padding(10)
                        .frame(maxWidth: 360, alignment: .leading)
                        .background(SentinelTheme.amber.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
                }

                VStack(alignment: .leading, spacing: 12) {
                    siteSection
                    Divider().overlay(SentinelTheme.line)
                    credentialsSection
                }
                .padding(18)
                .frame(width: 380)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
                .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))

                if let hostServer {
                    Button(action: hostServer) {
                        Label("Run a server on this Mac…", systemImage: "server.rack")
                    }
                    .buttonStyle(.link)
                }
            }
            .padding(40)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .onAppear(perform: prefill)
        .onChange(of: session.prefill) { _ in applyPrefill() }
        .task(id: address) { await loadProviders() }
    }

    // MARK: Site

    private var siteSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Site").font(.caption.weight(.bold)).kerning(0.6).foregroundStyle(.secondary)
                Spacer()
                if sites.sites.isEmpty == false {
                    Menu {
                        ForEach(sites.sites) { site in
                            Button {
                                choose(site)
                            } label: {
                                Text("\(site.name) — \(site.address)")
                            }
                        }
                        Divider()
                        Button("New Site…") {
                            address = ""
                            siteName = ""
                            userName = ""
                            focus = .address
                        }
                    } label: {
                        Label("Saved Sites", systemImage: "building.2")
                    }
                    .menuStyle(.borderlessButton)
                    .fixedSize()
                }
            }

            TextField("Server address (e.g. 192.168.1.20 or https://sentinel.example.com)", text: $address)
                .textFieldStyle(.roundedBorder)
                .focused($focus, equals: .address)
                .onSubmit { focus = .name }
            TextField("Site name (optional)", text: $siteName)
                .textFieldStyle(.roundedBorder)

            if isCheckingAddress {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small)
                    Text("Contacting the server…").font(.caption).foregroundStyle(.secondary)
                }
            } else if let providersError {
                Text(providersError)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // MARK: Credentials

    private var showsPassword: Bool {
        guard let providers else { return true }
        return providers.local || providers.ldap
    }

    private var credentialsSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            if showsPassword {
                TextField("Name", text: $userName)
                    .textFieldStyle(.roundedBorder)
                    .focused($focus, equals: .name)
                    .onSubmit { focus = .password }
                SecureField("Password", text: $password)
                    .textFieldStyle(.roundedBorder)
                    .focused($focus, equals: .password)
                    .onSubmit(signIn)
                if providers?.ldap == true {
                    Text("Directory accounts work here too.")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }

            if let error {
                Text(error)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if showsPassword {
                Button(action: signIn) {
                    HStack {
                        if isWorking { ProgressView().controlSize(.small) }
                        Label("Sign In", systemImage: "person.badge.key.fill")
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .keyboardShortcut(.return)
                .disabled(isWorking || address.isEmpty || userName.isEmpty || password.isEmpty)
            }

            if let oidc = providers?.oidc, oidc.enabled {
                Button(action: signInWithSSO) {
                    Label(oidc.displayName?.isEmpty == false ? oidc.displayName! : "Sign in with single sign-on",
                          systemImage: "globe")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
                .disabled(isWorking)
            }
        }
    }

    // MARK: Actions

    private func applyPrefill() {
        guard let prefill = session.prefill else { return }
        session.prefill = nil
        if let site = sites.site(forAddress: prefill.address) {
            choose(site)
        } else {
            address = prefill.address
            siteName = prefill.siteName
            userName = ""
            password = ""
            focus = .name
        }
    }

    private func prefill() {
        if session.prefill != nil {
            applyPrefill()
            return
        }
        if let site = session.site ?? sites.currentSite ?? sites.sites.first {
            choose(site)
        } else {
            focus = .address
        }
    }

    private func choose(_ site: SavedSite) {
        address = site.address
        siteName = site.name
        userName = site.lastUserName ?? ""
        password = ""
        error = nil
        focus = userName.isEmpty ? .name : .password
    }

    private func loadProviders() async {
        providers = nil
        providersError = nil
        guard let url = SavedSite.normalizedURL(address) else { return }
        // Debounce typing.
        try? await Task.sleep(nanoseconds: 450_000_000)
        guard Task.isCancelled == false else { return }
        isCheckingAddress = true
        defer { isCheckingAddress = false }
        let probe = SentinelAPI(baseURL: url, siteID: "probe", tokenStore: MemoryTokenStore())
        do {
            let result = try await probe.providers()
            guard Task.isCancelled == false else { return }
            providers = result
        } catch {
            guard Task.isCancelled == false else { return }
            providersError = (error as? SentinelAPIError)?.isNotFound == true
                ? "That address answers, but not as a Sentinel Management Server (API v2)."
                : error.localizedDescription
        }
    }

    private func signIn() {
        guard isWorking == false else { return }
        error = nil
        isWorking = true
        let typedPassword = password
        Task {
            defer { isWorking = false }
            do {
                try await session.signIn(address: address, siteName: siteName, name: userName, password: typedPassword)
                password = ""
            } catch {
                self.error = (error as? SentinelAPIError)?.isUnauthorized == true
                    ? "Wrong name or password."
                    : error.localizedDescription
            }
        }
    }

    private func signInWithSSO() {
        guard isWorking == false else { return }
        error = nil
        isWorking = true
        Task {
            defer { isWorking = false }
            do {
                try await session.signInWithSSO(address: address, siteName: siteName)
            } catch is CancellationError {
                // The operator closed the sign-in sheet.
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
