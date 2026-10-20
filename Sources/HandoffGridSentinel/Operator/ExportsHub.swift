import AppKit
import SwiftUI
import UniformTypeIdentifiers
import SentinelCore

// Wave 11 (stream B): the Exports workspace hub. `ExportsWorkspaceView`
// stays as it is apart from two hook lines:
//
//   .modifier(ExportsHubFrame())        // end of its body: segment bar
//                                       // (Evidence | Scheduled), Long
//                                       // export panel, Shared links button
//   ExportsCaseActions(evidence: c)     // its actions column: Package…, Share…
//
// The existing "Lock Clip" panel carries the same export options
// (`ExportOptionsFields` + `createEvidence(withOptions:)`), wired at merge.

enum ExportsSegment: String, CaseIterable, Identifiable {
    case evidence = "Evidence"
    case scheduled = "Scheduled"

    var id: String { rawValue }
}

/// Shared between the hub and the case table's action cells (which get it
/// from the environment, so a cell never needs an environment object).
@MainActor
final class ExportsHubRouter: ObservableObject {
    @Published var sharing: V2.EvidenceCase?
    @Published var packaging: Set<String> = []
    @Published var error: String?
    /// Share URLs made in this run (Copy in the Shared links list).
    @Published var shareURLs = ShareLinkURLCache()
    var api: SentinelAPI?

    func share(_ c: V2.EvidenceCase) {
        error = nil
        sharing = c
    }

    /// NSSavePanel (`<caseId>-package.zip`), then the streaming download.
    func package(_ c: V2.EvidenceCase) async {
        guard let api, packaging.contains(c.id) == false else { return }
        let panel = NSSavePanel()
        panel.nameFieldStringValue = EvidencePackageRules.defaultName(caseID: c.caseId)
        panel.allowedContentTypes = [.zip]
        panel.canCreateDirectories = true
        panel.message = "The package holds the clip, its manifest and audit chain, and an offline player that verifies the SHA-256."
        guard panel.runModal() == .OK, let url = panel.url else { return }
        packaging.insert(c.id)
        defer { packaging.remove(c.id) }
        do {
            try await api.downloadEvidencePackage(id: c.id, to: url)
            error = nil
            NoticeCenter.shared.post(.success, "Saved the evidence package for \(c.caseId).")
        } catch {
            self.error = "\(c.caseId): \(EvidencePackageRules.failureText(error))"
        }
    }
}

private struct ExportsHubRouterKey: EnvironmentKey {
    static let defaultValue: ExportsHubRouter? = nil
}

extension EnvironmentValues {
    var exportsHubRouter: ExportsHubRouter? {
        get { self[ExportsHubRouterKey.self] }
        set { self[ExportsHubRouterKey.self] = newValue }
    }
}

/// Wraps the Exports workspace: a segment bar (Evidence | Scheduled) with
/// the Shared links button, the Long export panel above the evidence list,
/// and the share sheet. Without `exportEvidence` it shows the workspace as is
/// (the web's ExportsHub does the same).
struct ExportsHubFrame: ViewModifier {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    /// Playback's "Lock as Evidence" hand-over: a new prefill shows the Evidence segment.
    var prefill: ExportPrefill?
    @StateObject private var router = ExportsHubRouter()
    @State private var segment: ExportsSegment = .evidence
    @State private var showsLinks = false
    @State private var refresh = 0

    private var canExport: Bool { session.can(.exportEvidence) }

    @ViewBuilder
    func body(content: Content) -> some View {
        if canExport {
            hub(content)
        } else {
            content
        }
    }

    private func hub(_ content: Content) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            bar
            Divider()
            ZStack(alignment: .topLeading) {
                VStack(alignment: .leading, spacing: 0) {
                    LongExportPanel(onFiled: { refresh += 1 })
                    Divider()
                    // A long export that filed a case remounts the list, so it reloads.
                    content.id(refresh)
                }
                .opacity(segment == .evidence ? 1 : 0)
                .allowsHitTesting(segment == .evidence)
                .accessibilityHidden(segment != .evidence)
                if segment == .scheduled {
                    ScheduledExportsView()
                        .background(SentinelTheme.background)
                }
            }
        }
        .environment(\.exportsHubRouter, router)
        .onAppear { router.api = runtime.api }
        .onChange(of: prefill) { _ in segment = .evidence }
        .sheet(item: $router.sharing) { c in
            ShareEvidenceSheet(evidence: c, onCreated: { link, url in router.shareURLs.remember(id: link.id, url: url) })
        }
        .sheet(isPresented: $showsLinks) {
            SharedLinksView(router: router)
        }
    }

    private var bar: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 10) {
                Picker("Exports", selection: $segment) {
                    ForEach(ExportsSegment.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .fixedSize()
                if router.packaging.isEmpty == false {
                    ProgressView().controlSize(.small)
                    Text(router.packaging.count == 1 ? "Building the evidence package…" : "Building \(router.packaging.count) evidence packages…")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
                Button {
                    showsLinks = true
                } label: {
                    Label("Shared Links", systemImage: "link")
                }
                .help("Expiring links made from evidence cases, with their uses; revoke them here")
            }
            if let error = router.error {
                HStack(spacing: 6) {
                    Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
                        .fixedSize(horizontal: false, vertical: true)
                    Button("Dismiss") { router.error = nil }.controlSize(.small).buttonStyle(.borderless)
                }
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 8)
    }
}

/// The case table's extra actions (Package…, Share…) as one compact menu.
/// Shown only inside `ExportsHubFrame` (which also gates on `exportEvidence`).
struct ExportsCaseActions: View {
    let evidence: V2.EvidenceCase
    @Environment(\.exportsHubRouter) private var router

    var body: some View {
        if let router {
            ExportsCaseMenu(evidence: evidence, router: router)
        }
    }
}

private struct ExportsCaseMenu: View {
    let evidence: V2.EvidenceCase
    @ObservedObject var router: ExportsHubRouter

    var body: some View {
        let busy = router.packaging.contains(evidence.id)
        Menu {
            Button("Package…") { Task { await router.package(evidence) } }
                .disabled(busy)
            Button("Share…") { router.share(evidence) }
        } label: {
            if busy {
                ProgressView().controlSize(.small)
            } else {
                Image(systemName: "ellipsis.circle")
            }
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .help("Package \(evidence.caseId) as a self-verifying zip, or share it with an expiring link")
        .accessibilityLabel("More actions for \(evidence.caseId)")
    }
}
