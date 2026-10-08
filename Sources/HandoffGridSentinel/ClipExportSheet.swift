import SwiftUI
import AppKit
import SentinelCore

/// Operator-facing sheet for exporting a precise time range. Picks an arbitrary
/// in/out window and produces either a plain MP4 or a full evidence package
/// (clip.mp4 + manifest + chain-of-custody), stitching the overlapping 15-minute
/// segments and trimming to the exact range. Lossless passthrough by default; an
/// optional per-second timestamp overlay re-encodes the video.
struct ClipExportSheet: View {
    enum OutputMode: String, CaseIterable, Identifiable {
        case mp4 = "MP4 File"
        case package = "Evidence Package"
        var id: String { rawValue }
    }

    let segments: [RecordingSegment]
    let cameraID: UUID
    let cameraName: String
    let operatorName: String
    let auditLog: [AuditLogEntry]
    let cameraIPAddress: String
    @ObservedObject var evidenceExporter: EvidenceExporter

    @Environment(\.dismiss) private var dismiss

    @State private var start: Date
    @State private var end: Date
    @State private var outputMode: OutputMode = .mp4
    @State private var burnTimestamp = false
    @State private var isExporting = false
    @State private var progress: Double = 0
    @State private var errorMessage: String?
    /// Set the instant Export is tapped (before the save panel opens) so a second
    /// tap can't launch a concurrent export to a different file.
    @State private var isPreparing = false

    init(
        segments: [RecordingSegment],
        cameraID: UUID,
        cameraName: String,
        around playhead: Date,
        operatorName: String,
        auditLog: [AuditLogEntry],
        cameraIPAddress: String,
        evidenceExporter: EvidenceExporter
    ) {
        self.segments = segments
        self.cameraID = cameraID
        self.cameraName = cameraName
        self.operatorName = operatorName
        self.auditLog = auditLog
        self.cameraIPAddress = cameraIPAddress
        self.evidenceExporter = evidenceExporter
        _start = State(initialValue: playhead.addingTimeInterval(-30))
        _end = State(initialValue: playhead.addingTimeInterval(30))
    }

    private var duration: TimeInterval { max(0, end.timeIntervalSince(start)) }

    /// Recorded seconds that actually exist inside the chosen window.
    private var coveredSeconds: TimeInterval {
        guard end > start else { return 0 }
        var total: TimeInterval = 0
        for seg in segments where seg.cameraID == cameraID {
            let s = min(seg.createdAt, seg.modifiedAt)
            let e = max(seg.createdAt, seg.modifiedAt)
            let lo = max(s, start), hi = min(e, end)
            // Mirror ClipExporter's per-segment skip so the UI never enables
            // Export for slivers the exporter would discard.
            if hi.timeIntervalSince(lo) > 0.05 { total += hi.timeIntervalSince(lo) }
        }
        return min(total, duration)
    }
    private var hasCoverage: Bool { coveredSeconds > 0.5 }
    private var fullyCovered: Bool { duration > 0 && coveredSeconds >= duration - 1 }

    // Unify progress/active state across the two exporters.
    private var activeExporting: Bool {
        isPreparing || (outputMode == .package ? evidenceExporter.isExporting : isExporting)
    }
    private var activeProgress: Double { outputMode == .package ? evidenceExporter.progress : progress }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 8) {
                Image(systemName: "scissors").foregroundStyle(SentinelTheme.accent)
                Text("Export Clip").font(.title3.weight(.semibold))
                Spacer()
            }

            Text(cameraName).font(.subheadline.weight(.medium)).foregroundStyle(.secondary)

            rangeRow(label: "Start", date: $start)
            rangeRow(label: "End", date: $end)

            coverageSummary

            Divider()

            Picker("Output", selection: $outputMode) {
                ForEach(OutputMode.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .disabled(activeExporting)

            Toggle(isOn: $burnTimestamp) {
                VStack(alignment: .leading, spacing: 1) {
                    Text("Burn in timestamp")
                    Text("Overlays a live per-second clock — re-encodes the video (not lossless).")
                        .font(.caption2).foregroundStyle(.secondary)
                }
            }
            .disabled(activeExporting)

            if outputMode == .package {
                Label(burnTimestamp
                      ? "Package video will be re-encoded (timestamp overlay)."
                      : "Package video is a lossless copy of the exact range.",
                      systemImage: burnTimestamp ? "info.circle" : "checkmark.seal")
                    .font(.caption).foregroundStyle(.secondary)
            }

            if let errorMessage {
                Text(errorMessage).font(.caption).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if activeExporting {
                VStack(alignment: .leading, spacing: 6) {
                    ProgressView(value: activeProgress)
                    Text("Exporting… \(Int(activeProgress * 100))%")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.cancelAction).disabled(activeExporting)
                Button("Export…") { runExport() }
                    .keyboardShortcut(.defaultAction)
                    .buttonStyle(.borderedProminent)
                    .disabled(activeExporting || duration < 0.5 || !hasCoverage)
            }
        }
        .padding(20)
        .frame(width: 440)
    }

    // ── Range row: date+time picker with fine ± second nudges ────────────────
    private func rangeRow(label: String, date: Binding<Date>) -> some View {
        HStack(spacing: 8) {
            Text(label).font(.subheadline).frame(width: 42, alignment: .leading)
            DatePicker("", selection: date, displayedComponents: [.date, .hourAndMinute])
                .labelsHidden().datePickerStyle(.field)
            Stepper("", onIncrement: { date.wrappedValue.addTimeInterval(1) },
                        onDecrement: { date.wrappedValue.addTimeInterval(-1) })
                .labelsHidden().help("Adjust by ±1 second")
            Button("−10s") { date.wrappedValue.addTimeInterval(-10) }.controlSize(.small)
            Button("+10s") { date.wrappedValue.addTimeInterval(10) }.controlSize(.small)
        }
        .disabled(activeExporting)
    }

    private var coverageSummary: some View {
        HStack(spacing: 6) {
            Image(systemName: fullyCovered ? "checkmark.circle.fill"
                  : (hasCoverage ? "exclamationmark.triangle.fill" : "xmark.circle.fill"))
                .foregroundStyle(fullyCovered ? .green : (hasCoverage ? .orange : .red))
            Text(coverageText).font(.caption).foregroundStyle(.secondary)
        }
    }

    private var coverageText: String {
        guard duration >= 0.5 else { return "Select a range longer than half a second." }
        let dur = Self.durationString(duration)
        if !hasCoverage { return "No recordings exist in this range (\(dur))." }
        if fullyCovered { return "Clip length \(dur) — fully recorded." }
        let covered = Self.durationString(coveredSeconds)
        return "Clip length \(dur) — only \(covered) recorded (gaps will be skipped)."
    }

    static func durationString(_ seconds: TimeInterval) -> String {
        let s = Int(seconds.rounded())
        let h = s / 3600, m = (s % 3600) / 60, sec = s % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%d:%02d", m, sec)
    }

    private var overlay: ClipExporter.TimestampOverlay? {
        burnTimestamp ? ClipExporter.TimestampOverlay(cameraName: cameraName) : nil
    }

    private func runExport() {
        guard !isPreparing && !activeExporting else { return }
        errorMessage = nil
        isPreparing = true
        outputMode == .package ? exportPackage() : exportMP4()
    }

    private func exportMP4() {
        let panel = NSSavePanel()
        let stamp = Self.fileStamp(start)
        panel.nameFieldStringValue = "\(Self.safeName(cameraName))_\(stamp).mp4"
        panel.allowedContentTypes = [.mpeg4Movie]
        panel.canCreateDirectories = true
        panel.title = "Export Clip"
        panel.begin { response in
            guard response == .OK, let url = panel.url else { isPreparing = false; return }
            Task { @MainActor in
                isExporting = true; progress = 0
                defer { isExporting = false; isPreparing = false }
                do {
                    _ = try await ClipExporter.exportRange(
                        segments: segments, cameraID: cameraID,
                        from: start, to: end, to: url,
                        overlay: overlay,
                        progress: { p in progress = p }
                    )
                    NSWorkspace.shared.activateFileViewerSelecting([url])
                    dismiss()
                } catch {
                    errorMessage = (error as? ClipExporter.ExportError)?.errorDescription ?? error.localizedDescription
                    SentinelLog.shared.error(error, context: "Clip export failed", category: "export")
                }
            }
        }
    }

    private func exportPackage() {
        let stamp = Self.fileStamp(start)
        let caseID = "EXP-\(stamp)"
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "\(caseID)-\(Self.safeName(cameraName)).sentinelevidence"
        panel.canCreateDirectories = true
        panel.title = "Export Evidence Package"
        panel.message = "Choose where to save the .sentinelevidence bundle."
        panel.begin { response in
            guard response == .OK, let url = panel.url else { isPreparing = false; return }
            Task { @MainActor in
                defer { isPreparing = false }
                await evidenceExporter.exportRange(
                    segments: segments, cameraID: cameraID,
                    from: start, to: end, to: url,
                    caseID: caseID,
                    title: "Range Export \(Self.durationString(duration))",
                    operatorName: operatorName,
                    auditLog: auditLog,
                    cameraName: cameraName,
                    cameraIPAddress: cameraIPAddress,
                    overlay: overlay
                )
                // Handle the terminal result inline (no fragile onChange on a
                // shared, possibly-unchanged lastResult).
                switch evidenceExporter.lastResult {
                case .success(let resultURL):
                    NSWorkspace.shared.activateFileViewerSelecting([resultURL])
                    dismiss()
                case .failure(let message):
                    errorMessage = message
                case .none:
                    break
                }
            }
        }
    }

    private static func safeName(_ s: String) -> String {
        s.replacingOccurrences(of: " ", with: "_").replacingOccurrences(of: "/", with: "-")
    }
    private static func fileStamp(_ date: Date) -> String {
        let f = DateFormatter(); f.dateFormat = "yyyyMMdd-HHmmss"; return f.string(from: date)
    }
}
