import AppKit
import SwiftUI
import SentinelCore

/// Sites, appearance, and what this Mac runs.
struct OperatorSettingsView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var appearance: OperatorAppearance
    @ObservedObject private var importer = MacImporter.shared

    var body: some View {
        Form {
            Section("Signed in") {
                if let me = session.me, let site = session.site {
                    LabeledContent("Site", value: site.name)
                    LabeledContent("Address", value: site.address)
                    LabeledContent("User", value: "\(me.name) (\(me.role.rawValue))")
                    LabeledContent("Cameras", value: me.role == .admin || me.scope.allCameras ? "All" : "\(me.scope.groupIds.count) group(s)")
                }
                HStack {
                    Button("Open Sentinel Admin") { if let url = session.site?.url { NSWorkspace.shared.open(url) } }
                    Button("Sign Out") { Task { await session.signOut() } }
                }
            }
            Section("Saved sites") {
                ForEach(session.sites.sites) { site in
                    HStack {
                        VStack(alignment: .leading) {
                            Text(site.name + (site.isLocal ? " (this Mac)" : ""))
                            Text(site.address).font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        if site.id == session.site?.id {
                            Text("Current").font(.caption).foregroundStyle(.secondary)
                        } else {
                            Button("Switch") {
                                session.switchSite()
                                Task { await session.connect(to: site) }
                            }
                            Button("Forget", role: .destructive) {
                                session.sites.remove(site, tokenStore: session.tokenStore)
                            }
                        }
                    }
                }
            }
            Section("Appearance") {
                Picker("Theme", selection: Binding(get: { appearance.appearance }, set: { appearance.set($0) })) {
                    Text("Light").tag(OperatorAppearance.Mode.light)
                    Text("Dark").tag(OperatorAppearance.Mode.dark)
                }
                .pickerStyle(.segmented)
            }
            Section("About") {
                LabeledContent("Sentinel Operator", value: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev")
                if case .done(let summary) = importer.phase {
                    LabeledContent("Sentinel 1.x data", value: summary)
                }
            }
        }
        .formStyle(.grouped)
        .padding(10)
    }
}
