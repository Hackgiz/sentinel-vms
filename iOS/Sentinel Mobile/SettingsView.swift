// SettingsView.swift

import SwiftUI

struct SettingsView: View {
    @ObservedObject var session = SentinelSession.shared
    @ObservedObject var store = AppStore.shared
    @ObservedObject var activities = AlarmActivityController.shared
    @State private var editing: SavedSite?
    @State private var showAdd = false
    @State private var confirmSignOut = false

    var body: some View {
        NavigationStack {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()

                VStack(spacing: 0) {
                    settingsHeader
                    ScrollView {
                        VStack(spacing: 14) {
                            siteCard
                            connectionCard
                            CloudSettingsRow() // WAVE12(D): Sentinel Cloud → CloudStatusView
                            PhoneVideoSettingsCard() // WAVE23(I): send a video, use this iPhone as a camera
                            liveActivityCard
                            if session.isDemoMode == false { sitesCard }
                            aboutCard
                        }
                        .padding(14)
                    }
                }
            }
            // Hide the big empty large-title bar; use a compact custom header.
            .toolbar(.hidden, for: .navigationBar)
            .sheet(item: $editing) { SiteEditSheet(site: $0) }
            .sheet(isPresented: $showAdd) { AddSiteSheet() }
            .confirmationDialog("Sign out of \(session.serverName ?? "this site")?", isPresented: $confirmSignOut,
                                titleVisibility: .visible) {
                Button("Sign Out", role: .destructive) { Task { await session.signOut() } }
            } message: {
                Text("Push notifications from this site stop until you sign in again.")
            }
        }
    }

    private var settingsHeader: some View {
        HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
            VStack(alignment: .leading, spacing: 3) {
                Text("Settings")
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                Text(headerSubtitle)
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.white.opacity(0.6))
            }
            Spacer(minLength: 8)
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.top, 4)
        .padding(.bottom, SentinelTheme.Space.sm)
    }

    private var headerSubtitle: String {
        if session.isDemoMode { return "Demo mode" }
        if let name = session.serverName, session.isSignedIn { return "Signed in to \(name)" }
        return "Not signed in"
    }

    private var siteCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 12) {
                sectionHeader(icon: "server.rack", title: "Site")

                if session.isDemoMode {
                    infoRow(label: "Status", value: "Demo Mode", valueColor: SentinelTheme.recording)
                } else {
                    infoRow(label: "Status", value: session.isSignedIn ? "Signed in" : "Signed out",
                            valueColor: session.isSignedIn ? SentinelTheme.recording : .gray)
                    if let site = session.currentSite {
                        infoRow(label: "Name", value: site.displayName)
                        infoRow(label: "Address", value: site.address, mono: true)
                    }
                    if let user = session.userName {
                        infoRow(label: "User", value: user)
                    }
                    if let role = session.me?.role {
                        infoRow(label: "Role", value: role.rawValue)
                    }
                }

                Button(role: .destructive) {
                    if session.isDemoMode { session.leaveDemo() } else { confirmSignOut = true }
                } label: {
                    Label(session.isDemoMode ? "Exit Demo Mode" : "Sign Out", systemImage: "rectangle.portrait.and.arrow.right")
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 8)
                }
                .background(SentinelTheme.alarm.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(SentinelTheme.alarm.opacity(0.35), lineWidth: 1))
                .foregroundStyle(SentinelTheme.alarm)
                .padding(.top, 4)
            }
        }
    }

    /// Alarm Live Activities (Lock Screen / Dynamic Island).
    private var liveActivityCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 10) {
                sectionHeader(icon: "bell.badge.fill", title: "Live Activities")
                if activities.isAvailable {
                    Toggle(isOn: $activities.autoFollowCritical) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Follow Critical alarms").font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                            Text("While Sentinel is open, a new Critical alarm appears on the Lock Screen and in the Dynamic Island.")
                                .font(.caption).foregroundStyle(.white.opacity(0.55))
                        }
                    }
                    .tint(SentinelTheme.accent)
                    if activities.followedAlarmID != nil {
                        Button("Stop following") { activities.unfollow() }
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(SentinelTheme.accent)
                    }
                } else {
                    Text("Live Activities are off for Sentinel. Turn them on in iOS Settings › Sentinel.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                }
            }
        }
    }

    private var connectionCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 10) {
                sectionHeader(icon: "network", title: "Connection")

                HStack(spacing: 6) {
                    Circle()
                        .fill(connectionColor)
                        .frame(width: 7, height: 7)
                    Text(connectionLabel)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.white)
                }

                if session.isDemoMode == false, let api = session.api {
                    infoRow(label: "Via", value: api.baseURL.host ?? api.baseURL.absoluteString, mono: true)
                    infoRow(label: "Live updates", value: streamLabel,
                            valueColor: store.streamState.isConnected ? SentinelTheme.recording : .gray)
                }

                if let site = session.currentSite {
                    if let remote = site.remoteAddress, remote.isEmpty == false {
                        infoRow(label: "Remote address", value: remote, mono: true)
                    } else {
                        Text("No remote address. Add one (for example a Cloudflare tunnel) to reach this site away from the local network.")
                            .font(.caption)
                            .foregroundStyle(.white.opacity(0.55))
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    Button { editing = site } label: {
                        Label("Edit site and remote address", systemImage: "pencil")
                            .font(.subheadline.weight(.semibold))
                    }
                    .foregroundStyle(SentinelTheme.accent)
                }
            }
        }
    }

    private var connectionLabel: String {
        guard session.isSignedIn else { return session.isDemoMode ? "Demo data" : "Not connected" }
        return session.isUsingRemote ? "Remote address" : "Local network"
    }

    private var connectionColor: Color {
        guard session.isSignedIn else { return session.isDemoMode ? SentinelTheme.recording : .gray }
        return session.isUsingRemote ? SentinelTheme.amber : .green
    }

    private var streamLabel: String {
        switch store.streamState {
        case .connected: return "Connected"
        case .connecting: return "Connecting…"
        case .waiting: return "Reconnecting…"
        case .stopped(let reason): return reason
        case .idle: return "Off"
        }
    }

    private var sitesCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 10) {
                sectionHeader(icon: "list.bullet.rectangle", title: "Sites")
                ForEach(session.directory.sites) { site in
                    let isCurrent = site.id == session.currentSite?.id
                    Button {
                        if isCurrent == false { Haptics.tap(); session.select(site) }
                    } label: {
                        HStack(spacing: 10) {
                            Image(systemName: isCurrent ? "checkmark.circle.fill" : "circle")
                                .foregroundStyle(isCurrent ? SentinelTheme.recording : .white.opacity(0.3))
                            VStack(alignment: .leading, spacing: 1) {
                                Text(site.displayName).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                                Text(site.address).font(.caption).foregroundStyle(.white.opacity(0.5)).lineLimit(1)
                            }
                            Spacer()
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .contextMenu {
                        Button("Edit") { editing = site }
                        Button("Remove", role: .destructive) { session.remove(site) }
                    }
                }
                Button { showAdd = true } label: {
                    Label("Add a Site", systemImage: "plus.circle")
                        .font(.subheadline.weight(.semibold))
                }
                .foregroundStyle(SentinelTheme.accent)
                .padding(.top, 2)
            }
        }
    }

    private var aboutCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 12) {
                sectionHeader(icon: "info.circle", title: "About")

                infoRow(label: "App", value: "Sentinel Mobile")
                infoRow(label: "Version", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "—")
                infoRow(label: "Build", value: Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? "—")
            }
        }
    }

    private func sectionHeader(icon: String, title: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(SentinelTheme.accent)
            Text(title)
                .font(.headline)
                .foregroundStyle(.white)
            Spacer()
        }
    }

    private func infoRow(label: String, value: String, valueColor: Color = .white, mono: Bool = false) -> some View {
        HStack {
            Text(label)
                .font(.subheadline)
                .foregroundStyle(.white.opacity(0.6))
            Spacer()
            Text(value)
                .font(mono ? .system(.caption, design: .monospaced) : .subheadline.weight(.medium))
                .foregroundStyle(valueColor)
                .lineLimit(1)
                .truncationMode(.middle)
        }
    }
}
