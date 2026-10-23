import AppKit
import SwiftUI
import SentinelCore

/// "Run a server on this Mac": install the bundled Sentinel Server as a
/// launchd service, create its first administrator, and connect to it.
/// Works signed in (Admin section) and signed out (from the sign-in screen).
struct LocalServerView: View {
    @EnvironmentObject private var session: OperatorSession
    @ObservedObject private var manager = LocalServerManager.shared

    @State private var adminName = ""
    @State private var adminPassword = ""
    @State private var adminPassword2 = ""
    @State private var error: String?
    @State private var isWorking = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text("Sentinel Server on this Mac")
                    .font(.title3.weight(.semibold))
                Text("Sentinel Operator can install the Sentinel Server on this Mac as a background service. It records your cameras, runs detection and serves Sentinel Admin, also while nobody is signed in to the Mac. Other Macs and iPhones connect to it over the network.")
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)

                statusCard

                if let status = manager.status, status.setupRequired {
                    firstAdminCard
                }

                if case .failed(let message) = manager.phase {
                    Text(message).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                }
                if let error {
                    Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                }

                VStack(alignment: .leading, spacing: 4) {
                    Text("Details").font(.caption.weight(.bold)).foregroundStyle(.secondary)
                    Text("Program: /usr/local/sentinel · Data and recordings: /Library/Application Support/Sentinel · Log: /Library/Logs/Sentinel/sentinel.log · Service: com.handoffgrid.sentinel.server (runs as _sentinel). Removing the service keeps recordings.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(22)
            .frame(maxWidth: 720, alignment: .leading)
        }
        .task { await manager.refresh() }
    }

    private var statusCard: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: manager.isRunning ? "checkmark.circle.fill" : manager.isInstalled ? "exclamationmark.triangle.fill" : "server.rack")
                .font(.title)
                .foregroundStyle(manager.isRunning ? Color.green : manager.isInstalled ? SentinelTheme.amber : SentinelTheme.accent)
            VStack(alignment: .leading, spacing: 6) {
                if let status = manager.status {
                    Text("Running\(status.version.isEmpty ? "" : " — version \(status.version)")").font(.callout.weight(.semibold))
                    Text(LocalServerManager.defaultAddress).font(.caption.monospaced()).foregroundStyle(.secondary)
                } else if manager.isInstalled {
                    Text("Installed, but not answering").font(.callout.weight(.semibold))
                    Text("It may still be starting. See /Library/Logs/Sentinel/sentinel.log.").font(.caption).foregroundStyle(.secondary)
                } else {
                    Text("Not installed").font(.callout.weight(.semibold))
                    if manager.otherServerOnPort {
                        Text("Something else answers on \(LocalServerManager.defaultAddress) — a virtual machine, Docker or a development build, not the Sentinel service. Stop it before installing.")
                            .font(.caption).foregroundStyle(SentinelTheme.amber).fixedSize(horizontal: false, vertical: true)
                    }
                    Text(manager.bundledArchive == nil
                         ? "This build of Sentinel Operator doesn't include the server. Download the macOS server from sentvms.com."
                         : "Installing needs an administrator's password.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                if case .working(let text) = manager.phase {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text(text).font(.caption)
                    }
                }
                HStack(spacing: 8) {
                    if manager.bundledArchive != nil {
                        Button(manager.isInstalled ? "Reinstall / Update" : "Install Server…") {
                            Task { await manager.install() }
                        }
                        .buttonStyle(.borderedProminent)
                    }
                    if manager.isRunning, manager.status?.setupRequired == false {
                        Button(isConnectedHere ? "Connected" : "Connect") { connect() }
                            .disabled(isConnectedHere)
                        Button("Open Sentinel Admin") { NSWorkspace.shared.open(URL(string: LocalServerManager.defaultAddress)!) }
                    }
                    Button("Check Again") { Task { await manager.refresh() } }
                    if manager.isInstalled {
                        Button("Remove Service…", role: .destructive) { Task { await manager.uninstall() } }
                    }
                }
                .controlSize(.small)
                .disabled(isBusy)
            }
            Spacer()
        }
        .padding(14)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private var firstAdminCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Create the administrator").font(.callout.weight(.semibold))
            Text("The new server has no users yet. This account can do everything, including adding users in Sentinel Admin.")
                .font(.caption).foregroundStyle(.secondary)
            TextField("Name", text: $adminName).textFieldStyle(.roundedBorder)
            SecureField("Password (8+ characters)", text: $adminPassword).textFieldStyle(.roundedBorder)
            SecureField("Password again", text: $adminPassword2).textFieldStyle(.roundedBorder)
            Button("Create and Sign In") { createAdmin() }
                .buttonStyle(.borderedProminent)
                .disabled(isWorking || adminName.isEmpty || adminPassword.count < 8 || adminPassword != adminPassword2)
        }
        .padding(14)
        .frame(maxWidth: 420, alignment: .leading)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private var isBusy: Bool {
        if case .working = manager.phase { return true }
        return isWorking
    }

    private var isConnectedHere: Bool {
        session.isSignedIn && session.site.flatMap(\.url).map(LocalServerManager.isLocalAddress) == true
    }

    private func connect() {
        if let site = session.sites.site(forAddress: LocalServerManager.defaultAddress) {
            session.switchSite()
            Task { await session.connect(to: site) }
        } else {
            session.prefill = SignInPrefill(address: LocalServerManager.defaultAddress, siteName: "This Mac")
            session.switchSite()
        }
    }

    private func createAdmin() {
        isWorking = true
        error = nil
        let name = adminName
        let password = adminPassword
        Task {
            defer { isWorking = false }
            do {
                try await manager.createFirstAdmin(name: name, password: password)
                session.switchSite()
                try await session.signIn(address: LocalServerManager.defaultAddress, siteName: "This Mac", name: name, password: password)
                adminPassword = ""
                adminPassword2 = ""
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Bring a Sentinel for Mac 1.x install into the signed-in site.
struct MacImportView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @ObservedObject private var importer = MacImporter.shared
    @ObservedObject private var local = LocalServerManager.shared

    @State private var preview: MacImporter.Preview?
    @State private var operatorName = ""
    @State private var operatorPassword = ""
    @State private var accessMessage: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text("Import from Sentinel 1.x").font(.title3.weight(.semibold))
                Text("Earlier versions of this app recorded cameras by themselves. Their cameras, camera passwords, users, alarm rules, locked evidence and audit history move to a Sentinel Server once; recordings stay where they are and the server's local Recording Server plays them.")
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)

                if importer.hasLegacyData == false, importer.isComplete == false {
                    EmptyStateLine(text: "No Sentinel 1.x data was found on this Mac.")
                } else {
                    if let preview { found(preview) }
                    if session.isAdmin == false {
                        PermissionNote(text: "Only an Admin can import.")
                    } else if importer.isComplete == false {
                        runCard
                    }
                    resultCard
                }
            }
            .padding(22)
            .frame(maxWidth: 760, alignment: .leading)
        }
        .task {
            preview = importer.preview()
            if operatorName.isEmpty { operatorName = preview?.userNames.first ?? "" }
        }
    }

    private func found(_ preview: MacImporter.Preview) -> some View {
        SentinelPanel("FOUND ON THIS MAC", systemImage: "externaldrive") {
            VStack(alignment: .leading, spacing: 6) {
                DetailRow(label: "Cameras", value: "\(preview.cameras)")
                DetailRow(label: "Users", value: "\(preview.users)")
                DetailRow(label: "Alarm rules", value: "\(preview.rules)")
                DetailRow(label: "Locked evidence", value: "\(preview.evidence)")
                DetailRow(label: "Audit entries", value: "\(preview.audit)")
                DetailRow(label: "Cameras with recordings", value: "\(preview.recordingFolders.count)")
            }
        }
    }

    private var runCard: some View {
        SentinelPanel("IMPORT INTO \((session.site?.name ?? "THIS SITE").uppercased())", systemImage: "square.and.arrow.down.on.square") {
            VStack(alignment: .leading, spacing: 10) {
                if session.site.flatMap(\.url).map(LocalServerManager.isLocalAddress) != true {
                    Label("Cameras go to the site's local Recording Server, which must be able to read this Mac's recordings folder. Usually you import into the server running on this Mac.", systemImage: "info.circle")
                        .font(.caption).foregroundStyle(SentinelTheme.amber).fixedSize(horizontal: false, vertical: true)
                }
                if preview?.hasVault == true {
                    Text("Camera passwords are locked with a Sentinel 1.x operator's password. Enter one to bring them along (leave empty to enter them later in Sentinel Admin).")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Picker("Operator", selection: $operatorName) {
                            ForEach(preview?.userNames ?? [], id: \.self) { Text($0).tag($0) }
                        }
                        .frame(maxWidth: 240)
                        SecureField("Sentinel 1.x password", text: $operatorPassword).textFieldStyle(.roundedBorder)
                    }
                }
                if case .running(let text, let fraction) = importer.phase {
                    ProgressView(value: fraction) { Text(text).font(.caption) }
                } else {
                    Button("Import") {
                        guard let site = session.site else { return }
                        Task {
                            await importer.run(api: runtime.api, siteAddress: site.address,
                                               operatorName: operatorName, operatorPassword: operatorPassword)
                            operatorPassword = ""
                            await runtime.reloadAll()
                        }
                    }
                    .buttonStyle(.borderedProminent)
                }
                if case .failed(let message) = importer.phase {
                    Text(message).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    @ViewBuilder
    private var resultCard: some View {
        if let record = importer.record, let result = record.result {
            SentinelPanel("RESULT", systemImage: "checkmark.seal") {
                VStack(alignment: .leading, spacing: 6) {
                    if case .done(let summary) = importer.phase {
                        Text(summary).font(.callout.weight(.semibold))
                    }
                    Text("Into \(record.siteAddress). Evidence files uploaded: \(record.uploaded.count) of \(result.evidence).")
                        .font(.caption).foregroundStyle(.secondary)
                    let skipped = record.skipped + result.skipped
                    if skipped.isEmpty == false {
                        Text("Not imported").font(.caption.weight(.bold)).foregroundStyle(.secondary).padding(.top, 4)
                        ForEach(skipped, id: \.self) { line in
                            Label(line, systemImage: "minus.circle").font(.caption).fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    if let folders = preview?.recordingFolders, folders.isEmpty == false,
                       session.site.flatMap(\.url).map(LocalServerManager.isLocalAddress) == true {
                        Divider()
                        Text("The server runs as its own account (_sentinel) and needs permission to read the old recordings in your Library folder.")
                            .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        Button("Let the Server Read Old Recordings…") {
                            Task {
                                do {
                                    try await local.grantRecordingsAccess(folders)
                                    accessMessage = "Done. The server can now play recordings from Sentinel 1.x."
                                } catch is CancellationError {
                                } catch {
                                    accessMessage = error.localizedDescription
                                }
                            }
                        }
                        .controlSize(.small)
                        if let accessMessage { Text(accessMessage).font(.caption) }
                    }
                    if importer.isComplete {
                        Button("Import Again…") { importer.reset() }.controlSize(.small)
                    }
                }
            }
        }
    }
}
