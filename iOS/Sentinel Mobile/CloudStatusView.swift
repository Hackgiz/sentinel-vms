// CloudStatusView.swift
// Sentinel Cloud status for the current site: how the phone is connected
// (through the Sentinel Cloud relay, the local network, or another remote
// address), whether remote access and offsite backup are on, and the way into
// the backup. Strictly informational: Sentinel Cloud is set up and managed in
// Sentinel Admin on the Management Server, never bought or linked from here
// (no prices, no purchase links).

import SwiftUI

/// The headline of the Sentinel Cloud screen.
enum CloudConnectionStatus: Equatable {
    case viaCloud
    case localNetwork
    case remoteAddress
    case notLinked
    case checking
    case unreachable

    var title: String {
        switch self {
        case .viaCloud: return "Connected via Sentinel Cloud"
        case .localNetwork: return "Local network"
        case .remoteAddress: return "Remote address"
        case .notLinked: return "Not linked — set up in Sentinel Admin on your server"
        case .checking: return "Checking…"
        case .unreachable: return "Can't reach your server"
        }
    }

    /// - Parameters:
    ///   - viaCloud: on the secondary address, and it's a Sentinel Cloud relay.
    ///   - remote: the server's `GET /api/v2/cloud/remote`, when it answered.
    ///   - remoteKnown: the server answered that question (a server too old
    ///     to know counts as "answered: not linked").
    ///   - hasBackup: a usable offsite backup token is saved for the site.
    ///   - siteRemote: the site's saved secondary address.
    static func resolve(viaCloud: Bool, usingRemote: Bool, remote: V2.CloudRemote?, remoteKnown: Bool,
                        failed: Bool, hasBackup: Bool, siteRemote: URL?) -> CloudConnectionStatus {
        if viaCloud { return .viaCloud }
        let linked = remote?.isCloud == true || hasBackup || Cloud.isRelayHost(siteRemote)
        if linked { return usingRemote ? .remoteAddress : .localNetwork }
        if remoteKnown { return .notLinked }
        return failed ? .unreachable : .checking
    }
}

struct CloudStatusView: View {
    @ObservedObject private var session = SentinelSession.shared
    @ObservedObject private var access = BackupAccess.shared
    @State private var remote: V2.CloudRemote?
    @State private var remoteKnown = false
    @State private var failed = false

    private var grant: V2.CloudBackupAccess? { access.grant(forSite: session.currentSite?.id) }

    private var status: CloudConnectionStatus {
        CloudConnectionStatus.resolve(viaCloud: session.isViaSentinelCloud, usingRemote: session.isUsingRemote,
                                      remote: remote, remoteKnown: remoteKnown, failed: failed,
                                      hasBackup: grant != nil, siteRemote: session.currentSite?.remoteURL)
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 14) {
                statusCard
                if session.isDemoMode {
                    note("Sentinel Cloud status isn't available in Demo Mode.")
                } else {
                    switch status {
                    case .notLinked:
                        note("Remote access and offsite backup show here once your site is linked to Sentinel Cloud. Your administrator links it in Sentinel Admin on the Management Server.")
                    case .unreachable:
                        note("Sentinel Cloud status shows here when Sentinel Mobile can reach your server. Pull down to try again.")
                    case .checking:
                        EmptyView()
                    case .viaCloud, .localNetwork, .remoteAddress:
                        remoteCard
                        backupCard
                    }
                }
            }
            .padding(14)
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        .navigationTitle("Sentinel Cloud")
        .navigationBarTitleDisplayMode(.inline)
        // Settings hides its nav bar; keep the back button on this pushed screen.
        .toolbar(.visible, for: .navigationBar)
        .task { await load() }
        .refreshable { await load() }
    }

    private func load() async {
        guard session.isDemoMode == false, let api = session.api else { return }
        failed = false
        do {
            remote = try await api.cloudRemote()
            remoteKnown = true
        } catch let error as SentinelAPIError where error.isNotFound || error.isForbidden {
            // A server from before Sentinel Cloud, or a role that can't view live.
            remote = nil
            remoteKnown = true
        } catch {
            failed = true
        }
        session.refreshConnection()
        if remoteKnown { await access.refreshIfNeeded(api: api) }
    }

    // MARK: Cards

    private var statusCard: some View {
        SentinelCard {
            HStack(alignment: .top, spacing: 12) {
                ZStack {
                    Circle().fill(statusColor.opacity(0.16)).frame(width: 44, height: 44)
                    Image(systemName: statusSymbol).font(.title3.weight(.semibold)).foregroundStyle(statusColor)
                }
                VStack(alignment: .leading, spacing: 4) {
                    Text(session.isDemoMode ? "Demo Mode" : status.title)
                        .font(.headline).foregroundStyle(.white)
                        .fixedSize(horizontal: false, vertical: true)
                    if let site = session.serverName {
                        Text(site).font(.caption).foregroundStyle(.white.opacity(0.55))
                    }
                }
                Spacer(minLength: 0)
                if status == .checking, session.isDemoMode == false {
                    ProgressView().tint(.white.opacity(0.6))
                }
            }
        }
    }

    private var remoteCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 10) {
                header(icon: "globe", title: "Remote access")
                if let url = remoteAddress {
                    row("Status", "On", color: SentinelTheme.recording)
                    row("Address", url.host ?? url.absoluteString, mono: true)
                    Text("Away from the local network, Sentinel Mobile reaches this site through Sentinel Cloud. Nothing to set up on this iPhone.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .fixedSize(horizontal: false, vertical: true)
                } else {
                    row("Status", "Off", color: .gray)
                    Text("Remote access is turned on in Sentinel Admin on the Management Server.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private var backupCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 10) {
                header(icon: "externaldrive.badge.icloud", title: "Offsite backup")
                if let grant {
                    row("Status", "On", color: SentinelTheme.recording)
                    Text("Alarm snapshots and clips are copied off site as they happen, so you can see them even when the server is off.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .fixedSize(horizontal: false, vertical: true)
                    NavigationLink {
                        BackupBrowserView(grant: grant)
                    } label: {
                        Label("Browse offsite backup", systemImage: "photo.stack")
                            .font(.subheadline.weight(.semibold))
                    }
                    .foregroundStyle(SentinelTheme.accent)
                } else {
                    row("Status", "Off", color: .gray)
                    Text("Offsite backup is turned on in Sentinel Admin on the Management Server.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    /// The relay address: the server's answer, else the saved secondary
    /// address when it's a relay.
    private var remoteAddress: URL? {
        if let text = remote?.remoteURL, remote?.isCloud == true, let url = SavedSite.normalizedURL(text) { return url }
        if let saved = session.currentSite?.remoteURL, Cloud.isRelayHost(saved) { return saved }
        return nil
    }

    private var statusColor: Color {
        switch status {
        case .viaCloud: return SentinelTheme.accent
        case .localNetwork: return SentinelTheme.recording
        case .remoteAddress: return SentinelTheme.amber
        case .notLinked, .checking: return .gray
        case .unreachable: return SentinelTheme.alarm
        }
    }

    private var statusSymbol: String {
        switch status {
        case .viaCloud: return "cloud.fill"
        case .localNetwork: return "wifi"
        case .remoteAddress: return "globe"
        case .notLinked: return "link"
        case .checking: return "cloud"
        case .unreachable: return "exclamationmark.icloud"
        }
    }

    private func note(_ text: String) -> some View {
        Text(text)
            .font(.subheadline).foregroundStyle(.white.opacity(0.65))
            .frame(maxWidth: .infinity, alignment: .leading)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 4)
    }

    private func header(icon: String, title: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon).foregroundStyle(SentinelTheme.accent)
            Text(title).font(.headline).foregroundStyle(.white)
            Spacer()
        }
    }

    private func row(_ label: String, _ value: String, color: Color = .white, mono: Bool = false) -> some View {
        HStack {
            Text(label).font(.subheadline).foregroundStyle(.white.opacity(0.6))
            Spacer()
            Text(value)
                .font(mono ? .system(.caption, design: .monospaced) : .subheadline.weight(.medium))
                .foregroundStyle(color)
                .lineLimit(1)
                .truncationMode(.middle)
        }
    }
}

/// The Settings row that opens the Sentinel Cloud screen (not in Demo Mode).
struct CloudSettingsRow: View {
    @ObservedObject private var session = SentinelSession.shared
    @ObservedObject private var access = BackupAccess.shared

    var body: some View {
        if session.isDemoMode == false, session.currentSite != nil {
            SentinelCard {
                NavigationLink {
                    CloudStatusView()
                } label: {
                    HStack(spacing: 10) {
                        Image(systemName: "cloud.fill").foregroundStyle(SentinelTheme.accent)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Sentinel Cloud").font(.headline).foregroundStyle(.white)
                            Text(subtitle).font(.caption).foregroundStyle(.white.opacity(0.55)).lineLimit(1)
                        }
                        Spacer()
                        Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.white.opacity(0.3))
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
    }

    private var subtitle: String {
        if session.isViaSentinelCloud { return "Connected via Sentinel Cloud" }
        if access.grant(forSite: session.currentSite?.id) != nil { return "Offsite backup on" }
        if Cloud.isRelayHost(session.currentSite?.remoteURL) { return "Remote access on" }
        return "Remote access and offsite backup"
    }
}

/// "via Sentinel Cloud", next to the connection label.
struct CloudPill: View {
    var body: some View {
        Chip(text: "via Sentinel Cloud", systemImage: "cloud.fill", color: SentinelTheme.accent)
            .accessibilityLabel("Connected via Sentinel Cloud")
    }
}
