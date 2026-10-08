// SettingsView.swift

import SwiftUI

struct SettingsView: View {
    @ObservedObject var session = SentinelSession.shared

    var body: some View {
        NavigationStack {
            ZStack {
                SentinelTheme.background.ignoresSafeArea()

                VStack(spacing: 0) {
                    settingsHeader
                    ScrollView {
                        VStack(spacing: 14) {
                            serverCard
                            remoteAccessCard
                            aboutCard
                        }
                        .padding(14)
                    }
                }
            }
            // Hide the big empty large-title bar; use a compact custom header.
            .toolbar(.hidden, for: .navigationBar)
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
        if session.isPaired, let name = session.serverName, name.isEmpty == false { return "Paired to \(name)" }
        if session.isPaired { return "Paired" }
        return "Not paired"
    }

    private var serverCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 12) {
                sectionHeader(icon: "server.rack", title: "Server")

                if session.isDemoMode {
                    infoRow(label: "Status", value: "Demo Mode", valueColor: SentinelTheme.recording)
                } else {
                    infoRow(label: "Status", value: session.isPaired ? "Paired" : "Not paired", valueColor: session.isPaired ? SentinelTheme.recording : .gray)
                }

                if let name = session.serverName {
                    infoRow(label: "Name", value: name)
                }
                if let url = session.serverURL {
                    infoRow(label: "Address", value: url.absoluteString, mono: true)
                }

                Button(role: .destructive) {
                    session.unpair()
                } label: {
                    Label(session.isDemoMode ? "Exit Demo Mode" : "Unpair", systemImage: "xmark.circle")
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

    private var remoteAccessCard: some View {
        SentinelCard {
            VStack(alignment: .leading, spacing: 10) {
                sectionHeader(icon: "network", title: "Connection")

                // Connection type badge
                HStack(spacing: 6) {
                    Circle()
                        .fill(connectionColor)
                        .frame(width: 7, height: 7)
                    Text(connectionLabel)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.white)
                }

                if let active = session.activeServerURL {
                    infoRow(label: "Via", value: active.host ?? active.absoluteString, mono: true)
                }

                if let remote = session.remoteURL {
                    infoRow(label: "Remote URL", value: remote.absoluteString, mono: true)
                } else {
                    Text("No remote URL stored. Start the Cloudflare tunnel on the Mac and re-pair to enable away access.")
                        .font(.caption)
                        .foregroundStyle(.white.opacity(0.55))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private var connectionLabel: String {
        guard session.isPaired && !session.isDemoMode else { return "Not connected" }
        return session.isUsingRemote ? "Remote (Cloudflare)" : "Local network"
    }

    private var connectionColor: Color {
        guard session.isPaired && !session.isDemoMode else { return .gray }
        return session.isUsingRemote ? SentinelTheme.amber : .green
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
