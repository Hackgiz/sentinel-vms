import SwiftUI
import AppKit
import SentinelCore

// Where bug-report emails land when the silent upload can't reach the server.
// Change this in one place if support routing moves.
enum SentinelSupport {
    static let email = "help@handoffgrid.com"
}

// MARK: - Diagnostics & bug report panel
//
// Lives on the Health screen. Sentinel is free and still under active
// development, so this gives operators a one-click way to send us what they're
// seeing — recent activity, app/OS/hardware, and an optional note — without ever
// shipping credentials (the log is redacted; only a camera count is included).

struct DiagnosticsPanel: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @ObservedObject private var log = SentinelLog.shared

    @State private var showingReportSheet = false
    @State private var levelFilter: SentinelLog.Level? = nil

    private var filteredEntries: [SentinelLog.Entry] {
        let all = log.entries
        guard let levelFilter else { return all }
        return all.filter { $0.level.rank >= levelFilter.rank }
    }

    var body: some View {
        SentinelPanel("Diagnostics & Bug Report", systemImage: "ladybug") {
            VStack(alignment: .leading, spacing: 12) {
                Text("Sentinel is free and still improving. If something looks wrong, send a report — it includes recent activity (with passwords and keys stripped out), your app and macOS version, and Mac model. No camera URLs or credentials are sent.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                HStack(spacing: 8) {
                    Button {
                        showingReportSheet = true
                    } label: {
                        Label("Send Bug Report…", systemImage: "paperplane.fill")
                    }
                    .buttonStyle(.borderedProminent)

                    Button {
                        NSWorkspace.shared.activateFileViewerSelecting([SentinelLog.shared.logFileURL])
                    } label: {
                        Label("Reveal Log in Finder", systemImage: "folder")
                    }

                    Button {
                        exportDiagnostics()
                    } label: {
                        Label("Export Diagnostics…", systemImage: "square.and.arrow.up")
                    }

                    Spacer()
                }

                Divider().overlay(SentinelTheme.line)

                HStack {
                    Text("RECENT ACTIVITY")
                        .font(.caption.weight(.bold))
                        .kerning(0.8)
                        .foregroundStyle(.secondary)
                    Spacer()
                    Picker("", selection: $levelFilter) {
                        Text("All").tag(SentinelLog.Level?.none)
                        Text("Warnings+").tag(SentinelLog.Level?.some(.warning))
                        Text("Errors").tag(SentinelLog.Level?.some(.error))
                    }
                    .pickerStyle(.segmented)
                    .fixedSize()
                }

                LogConsole(entries: filteredEntries)
            }
        }
        .sheet(isPresented: $showingReportSheet) {
            FeedbackForm(cameraCount: cameraStore.cameras.count, initialKind: .bug) {
                showingReportSheet = false
            }
        }
    }

    private func exportDiagnostics() {
        let report = DiagnosticsCollector.build(
            cameraCount: cameraStore.cameras.count,
            userMessage: "",
            contactEmail: "",
            crashedLastLaunch: false
        )
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "Sentinel-Diagnostics.txt"
        panel.allowedContentTypes = [.plainText]
        if panel.runModal() == .OK, let url = panel.url {
            try? report.summaryText().write(to: url, atomically: true, encoding: .utf8)
        }
    }
}

// MARK: - Log console

private struct LogConsole: View {
    let entries: [SentinelLog.Entry]

    private static let timeFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        return f
    }()

    var body: some View {
        Group {
            if entries.isEmpty {
                EmptyStateLine(text: "No activity recorded yet. Events, recording, and any errors will appear here.")
            } else {
                ScrollViewReader { proxy in
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 2) {
                            ForEach(entries) { entry in
                                HStack(alignment: .top, spacing: 8) {
                                    Text(Self.timeFormatter.string(from: entry.date))
                                        .foregroundStyle(.secondary)
                                    Image(systemName: entry.level.symbol)
                                        .foregroundStyle(tint(entry.level))
                                        .frame(width: 14)
                                    Text(entry.message)
                                        .foregroundStyle(entry.level == .error ? SentinelTheme.alarm : .primary)
                                        .textSelection(.enabled)
                                    Spacer(minLength: 0)
                                }
                                .font(.system(size: 11, design: .monospaced))
                                .id(entry.id)
                            }
                        }
                        .padding(8)
                    }
                    .onChange(of: entries.count) { _ in
                        if let last = entries.last { proxy.scrollTo(last.id, anchor: .bottom) }
                    }
                }
                .frame(height: 220)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
            }
        }
    }

    private func tint(_ level: SentinelLog.Level) -> Color {
        switch level {
        case .debug:   return .secondary
        case .info:    return SentinelTheme.accent
        case .warning: return SentinelTheme.amber
        case .error:   return SentinelTheme.alarm
        }
    }
}

// MARK: - Feedback form
//
// One form for bug reports, feature ideas and questions. Shown as a sheet from
// the Diagnostics panel (preset to "bug") and as its own window from
// Help → Send Feedback… (preset to "idea"). Everything lands in the same
// inbox the website's download service writes to.

enum FeedbackKind: String, CaseIterable, Identifiable {
    case bug, idea, question
    var id: String { rawValue }
    var label: String {
        switch self {
        case .bug: return "Something's wrong"
        case .idea: return "Idea"
        case .question: return "Question"
        }
    }
    var prompt: String {
        switch self {
        case .bug: return "What went wrong / steps to reproduce"
        case .idea: return "What would make Sentinel better for you?"
        case .question: return "What would you like to know?"
        }
    }
}

struct FeedbackForm: View {
    let cameraCount: Int
    var initialKind: FeedbackKind = .bug
    let onDone: () -> Void

    @State private var kind: FeedbackKind = .bug
    @State private var didSetKind = false
    @State private var message = ""
    @State private var contactEmail = ""
    @State private var isSubmitting = false
    @State private var resultText: String?
    @State private var didSucceed = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Send Feedback")
                .font(.title3.weight(.bold))

            Picker("Type", selection: $kind) {
                ForEach(FeedbackKind.allCases) { Text($0.label).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()

            Text(kind == .bug
                 ? "We'll attach recent activity (passwords and keys removed), your app and macOS version, and Mac model. No camera addresses or credentials are sent."
                 : "We read every message. Your app and macOS version are included so we know which build you're on.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            VStack(alignment: .leading, spacing: 4) {
                Text(kind.prompt).font(.caption.weight(.semibold))
                TextEditor(text: $message)
                    .font(.body)
                    .frame(height: 120)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(SentinelTheme.line))
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Your email (optional, so we can reply)").font(.caption.weight(.semibold))
                TextField("you@example.com", text: $contactEmail)
                    .textFieldStyle(.roundedBorder)
            }

            if let resultText {
                Text(resultText)
                    .font(.caption)
                    .foregroundStyle(didSucceed ? SentinelTheme.recording : SentinelTheme.alarm)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack {
                Button("Cancel") { onDone() }
                Spacer()
                Button {
                    Task { await submit() }
                } label: {
                    if isSubmitting {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Send")
                    }
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
                .disabled(isSubmitting || message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 460)
        .onAppear {
            if !didSetKind { kind = initialKind; didSetKind = true }
        }
    }

    private func submit() async {
        isSubmitting = true
        resultText = nil
        var report = DiagnosticsCollector.build(
            cameraCount: cameraCount,
            userMessage: message,
            contactEmail: contactEmail,
            crashedLastLaunch: false,
            kind: kind.rawValue
        )
        // Ideas and questions don't need the activity log.
        if kind != .bug { report.logTail = "" }
        do {
            try await BugReportSubmitter().submit(report)
            SentinelLog.shared.info("Feedback submitted (\(kind.rawValue)).", category: "diagnostics")
            didSucceed = true
            resultText = "Thanks! Your feedback was sent. We read every one."
            try? await Task.sleep(nanoseconds: 1_400_000_000)
            onDone()
        } catch {
            // Upload failed — fall back to an email draft so nothing is lost.
            SentinelLog.shared.warning("Feedback upload failed (\(error.localizedDescription)); opening email draft.", category: "diagnostics")
            openMailFallback(report)
            didSucceed = true
            resultText = "Couldn't reach the server, so we opened an email draft for you. Just press Send."
        }
        isSubmitting = false
    }

    private func openMailFallback(_ report: DiagnosticsReport) {
        let subject = "Sentinel VMS \(report.kind == "bug" ? "bug report" : "feedback") (\(report.appVersion))"
        let body = report.summaryText()
        var components = URLComponents()
        components.scheme = "mailto"
        components.path = SentinelSupport.email
        components.queryItems = [
            URLQueryItem(name: "subject", value: subject),
            URLQueryItem(name: "body", value: body)
        ]
        if let url = components.url {
            NSWorkspace.shared.open(url)
        }
    }
}
