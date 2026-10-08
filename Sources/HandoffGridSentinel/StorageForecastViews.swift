import SwiftUI
import AppKit
import SentinelCore
import SentinelMediaServer

/// "At this rate the disk fills in N days" — or that retention keeps it level.
struct StorageForecastPanel: View {
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var cameraStore: CameraStore

    private var forecast: StorageForecast {
        let archive = RecordingArchiveSettings.current
        return StorageForecast.compute(
            segments: mediaIngestStore.recordingSegments.filter { $0.isArchived == false },
            retentionDays: Dictionary(uniqueKeysWithValues: cameraStore.cameras.map { ($0.id, ($0.retentionDays ?? 0) > 0 ? $0.retentionDays! : 7) }),
            freeBytes: mediaIngestStore.availableRecordingBytes,
            archiveAfterDays: archive.isEnabled && archive.isReachable ? archive.archiveAfterDays : nil
        )
    }

    var body: some View {
        let forecast = forecast
        SentinelPanel("Storage Forecast", systemImage: "chart.line.uptrend.xyaxis") {
            VStack(alignment: .leading, spacing: 10) {
                headline(forecast)

                if forecast.cameras.isEmpty == false {
                    VStack(spacing: 6) {
                        ForEach(forecast.cameras.sorted { $0.bytesPerDay > $1.bytesPerDay }, id: \.cameraID) { rate in
                            HStack {
                                Text(cameraStore.cameras.first { $0.id == rate.cameraID }?.name ?? "Removed camera")
                                    .font(.caption.weight(.semibold))
                                Spacer()
                                Text("\(Self.bytes(rate.bytesPerDay))/day · keeps \(rate.retentionDays)d ≈ \(Self.bytes(rate.steadyStateBytes))")
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                    .padding(10)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                }

                Text("Based on the last 48 hours of recording. Recording pauses automatically at 5 GB free to protect the disk.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func headline(_ forecast: StorageForecast) -> some View {
        switch forecast.outcome {
        case .insufficientData:
            Label("Not enough recent recording to forecast yet.", systemImage: "hourglass")
                .foregroundStyle(.secondary)
        case .levelsOff(let atBytes, let inDays):
            Label(
                inDays < 1
                    ? "Fits. Storage is already level at about \(Self.bytes(Double(atBytes))) — retention deletes as fast as you record."
                    : "Fits. Storage levels off at about \(Self.bytes(Double(atBytes))) in \(Int(inDays.rounded(.up))) days.",
                systemImage: "checkmark.circle.fill"
            )
            .foregroundStyle(.green)
        case .fills(let inDays):
            Label(
                "At \(Self.bytes(forecast.totalBytesPerDay))/day, recording stops in about \(Self.days(inDays)). Shorten retention, enable archiving, or free space.",
                systemImage: "exclamationmark.triangle.fill"
            )
            .foregroundStyle(inDays < 3 ? .red : SentinelTheme.amber)
        }
    }

    static func bytes(_ value: Double) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(value), countStyle: .file)
    }

    static func days(_ value: Double) -> String {
        value < 1 ? "\(max(1, Int((value * 24).rounded()))) hours" : "\(Int(value.rounded())) day\(Int(value.rounded()) == 1 ? "" : "s")"
    }
}

/// Move older footage to an external drive; it stays on the timeline while
/// the drive is connected.
struct RecordingArchivePanel: View {
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @State private var settings = RecordingArchiveSettings.current
    @State private var isRunning = false
    @State private var lastResult: String?

    private var shortestRetention: Int {
        cameraStore.cameras.map { ($0.retentionDays ?? 0) > 0 ? $0.retentionDays! : 7 }.min() ?? 7
    }

    private var archivedBytes: Int64 {
        mediaIngestStore.recordingSegments.filter(\.isArchived).reduce(0) { $0 + $1.byteCount }
    }

    var body: some View {
        SentinelPanel("Archive to External Drive", systemImage: "externaldrive.fill.badge.timemachine") {
            VStack(alignment: .leading, spacing: 12) {
                Toggle("Move older recordings to an archive drive", isOn: $settings.isEnabled)

                HStack {
                    Image(systemName: settings.isReachable ? "externaldrive.fill.badge.checkmark" : "externaldrive.badge.xmark")
                        .foregroundStyle(settings.folderPath == nil ? Color.secondary : (settings.isReachable ? Color.green : Color.red))
                    Text(settings.folderPath ?? "No archive folder chosen")
                        .font(.caption.monospaced())
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer()
                    Button("Choose Folder…", action: chooseFolder)
                }

                if settings.folderPath != nil, settings.isReachable == false {
                    Text("The archive drive isn't connected. Footage stays on this Mac until it is, and archived clips are hidden from the timeline.")
                        .font(.caption)
                        .foregroundStyle(.red)
                }

                HStack {
                    Picker("Move after", selection: $settings.archiveAfterDays) {
                        ForEach([1, 2, 3, 7, 14], id: \.self) { Text("\($0) day\($0 == 1 ? "" : "s")").tag($0) }
                    }
                    Picker("Keep in archive", selection: $settings.archiveRetentionDays) {
                        Text("Forever").tag(0)
                        ForEach([30, 90, 180, 365], id: \.self) { Text("\($0) days").tag($0) }
                    }
                }

                if settings.isEnabled, settings.archiveAfterDays >= shortestRetention {
                    Text("Some cameras keep only \(shortestRetention) day\(shortestRetention == 1 ? "" : "s") locally, so their footage is deleted before it's old enough to archive. Pick a shorter \"Move after\" or raise those cameras' retention.")
                        .font(.caption)
                        .foregroundStyle(SentinelTheme.amber)
                }

                HStack {
                    Text("Archived on drive: \(ByteCountFormatter.string(fromByteCount: archivedBytes, countStyle: .file))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Spacer()
                    if let lastResult {
                        Text(lastResult)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Button {
                        isRunning = true
                        Task {
                            let result = await mediaIngestStore.runArchivePass()
                            lastResult = result.summary
                            if result.moved > 0 || result.pruned > 0 || result.failed > 0 {
                                workflowStore.recordAudit(area: "Storage", action: "Archive pass (manual)", detail: result.summary)
                            }
                            isRunning = false
                        }
                    } label: {
                        Label(isRunning ? "Archiving…" : "Archive Now", systemImage: "arrow.right.doc.on.clipboard")
                    }
                    .disabled(isRunning || settings.isEnabled == false || settings.isReachable == false)
                }
            }
        }
        .onChange(of: settings) { newValue in
            let previous = RecordingArchiveSettings.current
            RecordingArchiveSettings.current = newValue
            if previous.isEnabled != newValue.isEnabled || previous.folderPath != newValue.folderPath {
                workflowStore.recordAudit(
                    area: "Storage",
                    action: newValue.isEnabled ? "Archive enabled" : "Archive disabled",
                    detail: newValue.folderPath ?? ""
                )
                mediaIngestStore.refreshRecordingSegments()
            }
        }
    }

    private func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.prompt = "Use as Archive"
        panel.message = "Choose a folder on your archive drive. Sentinel stores footage in a \"Sentinel Archive\" folder inside it."
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            settings.folderPath = url.path
        }
    }
}
