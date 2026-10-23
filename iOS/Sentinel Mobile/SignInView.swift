// SignInView.swift
// First-run welcome (saved sites, add a site, scan a QR code, demo) and the
// sign-in screen for the chosen site: name and password, plus single sign-on
// when the Management Server has it set up.

import SwiftUI
import AuthenticationServices

// MARK: - Welcome / site list

struct WelcomeView: View {
    @ObservedObject var session = SentinelSession.shared
    @State private var showScanner = false
    @State private var scannedCode: String?
    /// Non-nil while the Add Site sheet is open; carries the scanned address.
    @State private var addSite: SitePrefill?
    @State private var lastError: String?

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()

            ScrollView {
                VStack(spacing: 24) {
                    SentinelHeader(subtitle: "Live cameras, alarms, and recordings from\nyour Sentinel site, anywhere.")
                    if session.directory.sites.isEmpty == false { sitesCard }
                    actionsCard
                    if let lastError {
                        ErrorNote(message: lastError)
                    }
                    footer
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 32)
            }
        }
        // item-driven so the sheet is always built with the scanned address
        // (a Bool-driven sheet can capture the old, empty prefill).
        .sheet(item: $addSite) { prefill in
            AddSiteSheet(prefill: prefill)
        }
        // The Add Site sheet opens from onDismiss, once the scanner is fully
        // gone: presenting it while the scanner is still closing gets dropped.
        .sheet(isPresented: $showScanner, onDismiss: {
            guard let code = scannedCode else { return }
            scannedCode = nil
            if let site = SavedSite.fromQR(code) {
                addSite = SitePrefill(address: site.address, remote: site.remote ?? "")
            } else {
                lastError = "That QR code isn't a Sentinel site address. Use the QR code from your Management Server."
            }
        }) {
            QRScannerSheet { code in
                guard scannedCode == nil else { return }
                scannedCode = code
                showScanner = false
            }
        }
    }

    private var sitesCard: some View {
        SentinelCard(padding: 6) {
            VStack(spacing: 0) {
                ForEach(session.directory.sites) { site in
                    Button {
                        lastError = nil
                        session.select(site)
                    } label: {
                        HStack(spacing: 12) {
                            Image(systemName: "server.rack")
                                .foregroundStyle(SentinelTheme.accent)
                                .frame(width: 28)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(site.displayName).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                                Text(site.address).font(.caption).foregroundStyle(.white.opacity(0.5)).lineLimit(1)
                            }
                            Spacer()
                            Image(systemName: "chevron.right").font(.caption).foregroundStyle(.white.opacity(0.3))
                        }
                        .padding(12)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .contextMenu {
                        Button("Remove", role: .destructive) { session.remove(site) }
                    }
                    if site.id != session.directory.sites.last?.id {
                        Rectangle().fill(SentinelTheme.line).frame(height: 1).padding(.leading, 52)
                    }
                }
            }
        }
    }

    private var actionsCard: some View {
        VStack(spacing: 10) {
            Button {
                lastError = nil
                addSite = SitePrefill()
            } label: {
                Label("Add a Site", systemImage: "plus.circle.fill")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .tint(SentinelTheme.accent)

            Button {
                lastError = nil
                showScanner = true
            } label: {
                Label("Scan Site QR Code", systemImage: "qrcode.viewfinder")
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
                    .foregroundStyle(.white)
            }
            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1))

            HStack(spacing: 8) {
                Rectangle().fill(SentinelTheme.line).frame(height: 1)
                Text("OR")
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(.white.opacity(0.4))
                Rectangle().fill(SentinelTheme.line).frame(height: 1)
            }
            .padding(.vertical, 4)

            Button {
                lastError = nil
                session.enterDemoMode()
            } label: {
                Label("Try Demo Mode", systemImage: "play.rectangle.fill")
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
                    .foregroundStyle(SentinelTheme.recording)
            }
            .background(SentinelTheme.recording.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.recording.opacity(0.35), lineWidth: 1))

            Text("Loads sample cameras and alarms so you can explore the app without a server.")
                .font(.caption2)
                .foregroundStyle(.white.opacity(0.5))
                .multilineTextAlignment(.center)
                .padding(.horizontal, 8)
                .padding(.top, 2)
        }
    }

    private var footer: some View {
        HStack(spacing: 4) {
            Image(systemName: "lock.shield")
                .font(.caption2)
            Text("You sign in with your Sentinel user name and password.")
                .font(.caption2)
        }
        .foregroundStyle(.white.opacity(0.4))
        .padding(.top, 8)
    }
}

/// Logo and product name at the top of the welcome and sign-in screens.
struct SentinelHeader: View {
    let subtitle: String

    var body: some View {
        VStack(spacing: 14) {
            ZStack {
                Image("AppLogo")
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .frame(width: 80, height: 80)
                    .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .stroke(.white.opacity(0.15), lineWidth: 1)
                    .frame(width: 80, height: 80)
            }
            .shadow(color: SentinelTheme.accent.opacity(0.4), radius: 20, y: 6)

            VStack(spacing: 6) {
                Text("Sentinel")
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                Text("Mobile")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(SentinelTheme.accent)
                    .textCase(.uppercase)
                    .tracking(1.2)
            }

            Text(subtitle)
                .font(.subheadline)
                .multilineTextAlignment(.center)
                .foregroundStyle(.white.opacity(0.65))
        }
        .padding(.top, 12)
    }
}

struct ErrorNote: View {
    let message: String

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(SentinelTheme.alarm)
            Text(message)
                .font(.caption)
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(12)
        .background(SentinelTheme.alarm.opacity(0.14), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.alarm.opacity(0.4), lineWidth: 1))
    }
}

// MARK: - Add / edit a site

struct SitePrefill: Equatable, Identifiable {
    let id = UUID()
    var address = ""
    var remote = ""
}

struct AddSiteSheet: View {
    var prefill = SitePrefill()
    @ObservedObject private var session = SentinelSession.shared
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var address = ""
    @State private var remote = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Name (optional)", text: $name)
                    TextField("192.168.1.20 or https://sentinel.example.com", text: $address)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                } header: {
                    Text("Management Server")
                } footer: {
                    Text("The address of your Sentinel Management Server. Plain hosts use port 8090.")
                }
                Section {
                    TextField("https://sentinel.example.com", text: $remote)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                } header: {
                    Text("Remote address (optional)")
                } footer: {
                    Text("Tried when the address above doesn't answer, for example a Cloudflare tunnel when you're away from the local network.")
                }
                if let error {
                    Section { Text(error).font(.caption).foregroundStyle(SentinelTheme.alarm) }
                }
            }
            .navigationTitle("Add a Site")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if busy {
                        ProgressView()
                    } else {
                        Button("Connect") { connect() }
                            .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
            }
            .onAppear {
                if address.isEmpty { address = prefill.address }
                if remote.isEmpty { remote = prefill.remote }
            }
        }
    }

    private func connect() {
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                let fromQR = prefill.remote.isEmpty == false && remote.trimmingCharacters(in: .whitespacesAndNewlines) == prefill.remote // WAVE12(D)
                let site = try await session.addSite(name: name, address: address, remoteAddress: remote,
                                                     remoteSource: fromQR ? .qr : .manual) // WAVE12(D)
                dismiss()
                session.select(site)
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Rename a site and change its addresses.
struct SiteEditSheet: View {
    let site: SavedSite
    @ObservedObject private var session = SentinelSession.shared
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var address = ""
    @State private var remote = ""
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section("Name") { TextField("Name", text: $name) }
                Section {
                    TextField("Address", text: $address)
                        .textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                } header: { Text("Management Server") }
                Section {
                    TextField("https://sentinel.example.com", text: $remote)
                        .textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                } header: {
                    Text("Remote address")
                } footer: {
                    Text("Tried when the address above doesn't answer, for example a Cloudflare tunnel. Leave empty for none.")
                }
                if let error {
                    Section { Text(error).font(.caption).foregroundStyle(SentinelTheme.alarm) }
                }
            }
            .navigationTitle("Site")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) { Button("Save") { save() } }
            }
            .onAppear {
                name = site.name
                address = site.address
                remote = site.remoteAddress ?? ""
            }
        }
    }

    private func save() {
        guard let url = SavedSite.normalizedURL(address) else {
            error = "The address isn't a valid web address."
            return
        }
        let trimmedRemote = remote.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmedRemote.isEmpty == false, SavedSite.normalizedURL(trimmedRemote) == nil {
            error = "The remote address isn't a valid web address."
            return
        }
        var updated = site
        updated.name = name
        updated.address = url.absoluteString
        updated.remoteAddress = trimmedRemote.isEmpty ? nil : SavedSite.normalizedURL(trimmedRemote)?.absoluteString
        if trimmedRemote != (site.remoteAddress ?? "") { updated.remoteAddressSource = trimmedRemote.isEmpty ? nil : .manual } // WAVE12(D)
        session.updateSite(updated)
        dismiss()
    }
}

// MARK: - Sign in

struct SignInView: View {
    @ObservedObject var session = SentinelSession.shared
    @State private var userName = ""
    @State private var password = ""
    @State private var providers: V2.AuthProviders?
    @State private var providersError: String?
    @State private var busy = false
    @State private var error: String?
    @State private var showEdit = false
    @FocusState private var focus: Field?

    private enum Field { case user, password }

    private var site: SavedSite? { session.currentSite }

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()

            ScrollView {
                VStack(spacing: 24) {
                    SentinelHeader(subtitle: site?.address ?? "")
                    if let notice = session.notice {
                        ErrorNote(message: notice)
                    }
                    formCard
                    if let error { ErrorNote(message: error) }
                    VStack(spacing: 8) {
                        Button("Site settings") { showEdit = true }
                            .font(.subheadline)
                        Button("Choose another site") { session.chooseAnotherSite() }
                            .font(.subheadline)
                    }
                    .foregroundStyle(SentinelTheme.accent)
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 32)
            }
            .scrollDismissesKeyboard(.interactively)
        }
        .sheet(isPresented: $showEdit) {
            if let site { SiteEditSheet(site: site) }
        }
        .task(id: site?.id) {
            userName = site?.lastUserName ?? ""
            await loadProviders()
        }
    }

    private var formCard: some View {
        SentinelCard(padding: 18) {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 8) {
                    Image(systemName: "person.badge.key.fill").foregroundStyle(SentinelTheme.accent)
                    Text("Sign in to \(site?.displayName ?? "the site")")
                        .font(.headline).foregroundStyle(.white).lineLimit(2)
                }

                if let providersError {
                    ErrorNote(message: providersError)
                    Button("Try again") { Task { await loadProviders() } }
                        .font(.subheadline.weight(.semibold))
                } else if providers?.local ?? true {
                    TextField("User name", text: $userName)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .textContentType(.username)
                        .focused($focus, equals: .user)
                        .submitLabel(.next)
                        .onSubmit { focus = .password }
                        .fieldStyle()
                    SecureField("Password", text: $password)
                        .textContentType(.password)
                        .focused($focus, equals: .password)
                        .submitLabel(.go)
                        .onSubmit(signIn)
                        .fieldStyle()
                    Button(action: signIn) {
                        HStack {
                            if busy { ProgressView().tint(.white) }
                            Text("Sign In").font(.headline)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 8)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .tint(SentinelTheme.accent)
                    .disabled(busy || userName.trimmingCharacters(in: .whitespaces).isEmpty || password.isEmpty)
                }

                if let oidc = providers?.oidc, oidc.enabled {
                    Button(action: signInWithSSO) {
                        Label(oidc.displayName?.isEmpty == false ? oidc.displayName! : "Sign in with single sign-on",
                              systemImage: "person.crop.circle.badge.checkmark")
                            .font(.subheadline.weight(.semibold))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .foregroundStyle(.white)
                    }
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
                    .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1))
                    .disabled(busy)
                }
            }
        }
    }

    private func loadProviders() async {
        providersError = nil
        guard let site else { return }
        do {
            providers = try await session.providers(for: site)
        } catch {
            providers = nil
            providersError = error.localizedDescription
        }
    }

    private func signIn() {
        guard busy == false else { return }
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                try await session.signIn(name: userName, password: password)
                password = ""
            } catch {
                self.error = Self.message(for: error)
            }
        }
    }

    private func signInWithSSO() {
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                try await session.signInWithSSO()
            } catch {
                if let auth = error as? ASWebAuthenticationSessionError, auth.code == .canceledLogin { return }
                if error is CancellationError { return }
                self.error = Self.message(for: error)
            }
        }
    }

    private static func message(for error: Error) -> String {
        if let api = error as? SentinelAPIError, api.status == 401 {
            return "The user name or password isn't right."
        }
        return error.localizedDescription
    }
}

private extension View {
    func fieldStyle() -> some View {
        self
            .padding(12)
            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1))
            .foregroundStyle(.white)
    }
}
