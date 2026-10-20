import AppKit
import SwiftUI
import SentinelCore

private func copyToPasteboard(_ text: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
}

/// "Share…" on an evidence case: expiry (1 h / 24 h / 7 d / custom), max
/// uses and a note; the link is then shown once (the server keeps only its
/// hash) with Copy Link.
struct ShareEvidenceSheet: View {
    let evidence: V2.EvidenceCase
    var onCreated: (V2.ShareLink, String) -> Void

    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @State private var preset: SharePreset = .oneDay
    @State private var customAt = Date().addingTimeInterval(3 * 24 * 3600)
    @State private var maxUses = ""
    @State private var note = ""
    @State private var busy = false
    @State private var error: String?
    @State private var url: String?
    @State private var copied = false

    private var blocker: String? { ShareRules.blocker(preset: preset, customAt: customAt.unixMillis, maxUses: maxUses) }

    var body: some View {
        Group {
            if let url { created(url) } else { form }
        }
        .padding(20)
        .frame(width: 480)
    }

    private var form: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Share \(evidence.caseId)").font(.headline)
            Text("Makes an expiring link to this case's video. The page is served on this site's local network only.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Form {
                Picker("Expires after", selection: $preset) {
                    ForEach(SharePreset.allCases) { Text($0.label).tag($0) }
                }
                if preset == .custom {
                    DatePicker("Expires at", selection: $customAt, in: Date()...Date().addingTimeInterval(Double(ShareRules.maxMs) / 1000),
                               displayedComponents: [.date, .hourAndMinute])
                }
                TextField("Max uses", text: $maxUses, prompt: Text("blank = unlimited"))
                TextField("Note", text: $note, prompt: Text("shown on the page"))
            }
            if let blocker {
                Text(blocker).font(.caption).foregroundStyle(.secondary)
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(busy ? "Creating…" : "Create Link") { Task { await create() } }
                    .keyboardShortcut(.defaultAction)
                    .buttonStyle(.borderedProminent)
                    .disabled(blocker != nil || busy)
                    .help(blocker ?? "")
            }
        }
    }

    private func created(_ url: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Share link created").font(.headline)
            Text(ShareRules.createdText(caseID: evidence.caseId))
                .font(.callout).fixedSize(horizontal: false, vertical: true)
            Text(url)
                .font(.callout.monospaced())
                .textSelection(.enabled)
                .lineLimit(3)
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(SentinelTheme.well, in: RoundedRectangle(cornerRadius: SentinelMetrics.controlRadius))
                .accessibilityLabel("Share link")
            HStack {
                Button(copied ? "Copied" : "Copy Link") {
                    copyToPasteboard(url)
                    copied = true
                }
                Spacer()
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
                    .buttonStyle(.borderedProminent)
            }
        }
    }

    private func create() async {
        guard blocker == nil, busy == false else { return }
        busy = true
        error = nil
        defer { busy = false }
        let request = ShareRules.request(preset: preset, customAt: customAt.unixMillis, maxUses: maxUses, note: note)
        do {
            let made = try await runtime.api.shareEvidence(id: evidence.id, request)
            url = made.url
            onCreated(made.link, made.url)
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }
}

/// Every expiring link made from evidence, with its uses, expiry and status;
/// Copy (for links made in this run of the app — the server never returns
/// a token again) and Revoke. Loads when opened; no polling.
struct SharedLinksView: View {
    @ObservedObject var router: ExportsHubRouter

    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var runtime: SiteRuntime
    @State private var links: [V2.ShareLink]?
    @State private var error: String?
    @State private var revoking: V2.ShareLink?
    @State private var copiedID: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Shared links").font(.title3.weight(.semibold))
                    Text("Links that can still be opened first. A link is shown in full only once, when it is made.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button {
                    Task { await load() }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .buttonStyle(.borderless)
                .help("Reload")
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
            .padding(14)
            Divider()
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true).padding(.horizontal, 14).padding(.top, 8)
            }
            content
        }
        .frame(width: 900, height: 480)
        .task { await load() }
        .confirmationDialog("Revoke this link?", isPresented: Binding(get: { revoking != nil }, set: { if !$0 { revoking = nil } })) {
            Button("Revoke Link", role: .destructive) {
                if let target = revoking { Task { await revoke(target) } }
            }
        } message: {
            if let revoking { Text(ShareRules.revokeText(caseID: revoking.caseId)) }
        }
    }

    @ViewBuilder
    private var content: some View {
        if let links {
            if links.isEmpty {
                VStack(spacing: 6) {
                    Image(systemName: "link").font(.system(size: 28)).foregroundStyle(.secondary)
                    Text("No shared links").font(.headline)
                    Text("Use “Share…” in a case's actions menu.").font(.callout).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Table(links) {
                    TableColumn("Case") { l in
                        VStack(alignment: .leading, spacing: 1) {
                            Text(l.caseId.isEmpty ? "(released)" : l.caseId).font(.callout.monospaced()).fontWeight(.semibold)
                            if l.title.isEmpty == false { Text(l.title).font(.caption).foregroundStyle(.secondary) }
                        }
                    }.width(min: 110, ideal: 150)
                    TableColumn("Created") { l in
                        VStack(alignment: .leading, spacing: 1) {
                            Text(EvidenceDisplay.defaultFormat(l.createdAt))
                            Text(l.createdBy).font(.caption).foregroundStyle(.secondary)
                        }
                    }.width(min: 130, ideal: 160)
                    TableColumn("Expires") { l in Text(EvidenceDisplay.defaultFormat(l.expiresAt)) }.width(min: 130, ideal: 160)
                    TableColumn("Uses") { l in Text(ShareRules.usesText(l)).monospacedDigit() }.width(min: 50, ideal: 60)
                    TableColumn("Status") { l in statusBadge(l.status) }.width(min: 70, ideal: 80)
                    TableColumn("Note") { l in Text(l.note).font(.caption).lineLimit(2) }
                    TableColumn("") { l in actions(l) }.width(min: 130, ideal: 140)
                }
            }
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func statusBadge(_ status: V2.ShareLinkStatus) -> some View {
        Text(status.rawValue)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background((status == .active ? SentinelTheme.recording : SentinelTheme.amber).opacity(0.3), in: Capsule())
    }

    private func actions(_ l: V2.ShareLink) -> some View {
        HStack(spacing: 6) {
            let url = router.shareURLs.url(for: l)
            Button(copiedID == l.id ? "Copied" : "Copy") {
                if let url {
                    copyToPasteboard(url)
                    copiedID = l.id
                }
            }
            .disabled(url == nil)
            .help(url == nil ? "The full link is shown only once, when it is made; make a new one to share again" : "Copy the link")
            if ShareRules.canRevoke(l) {
                Button("Revoke", role: .destructive) { revoking = l }
            }
        }
        .controlSize(.small)
    }

    private func load() async {
        do {
            let list = try await runtime.api.shareLinks()
            links = ShareRules.sorted(list)
            router.shareURLs.prune(keeping: list)
            error = nil
        } catch {
            if links == nil { links = [] }
            self.error = ExportRules.failureText(error)
        }
    }

    private func revoke(_ l: V2.ShareLink) async {
        revoking = nil
        do {
            try await runtime.api.revokeShareLink(id: l.id)
            router.shareURLs.forget(id: l.id)
            await load()
        } catch {
            self.error = ExportRules.failureText(error)
        }
    }
}
