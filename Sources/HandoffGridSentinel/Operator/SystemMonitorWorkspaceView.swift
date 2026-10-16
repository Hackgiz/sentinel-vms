import SwiftUI
import SentinelCore

/// System Monitor: a card per recording server (status dot, version, uptime,
/// CPU/Memory/Disk meters) above a table of every camera's health, from
/// `GET /api/v2/system/health`.
///
/// Wiring (coordinator, at merge): `SystemMonitorWorkspaceView(isVisible: tab == .systemMonitor)`.
/// - `isVisible`: the one 10 s refresh timer runs only while this is true
///   (and while the view is on screen); flipping it false cancels the timer.
///   Panes that stay alive in the background must pass their visibility;
///   the default `true` suits a pane that is removed when hidden.
///
/// Offline servers still render, with null metrics and their last-known cameras.
struct SystemMonitorWorkspaceView: View {
    @EnvironmentObject private var runtime: SiteRuntime

    var isVisible: Bool

    init(isVisible: Bool = true) {
        self.isVisible = isVisible
    }

    @State private var servers: [V2.ServerHealth]?
    @State private var error: String?
    @State private var isLoading = false
    @State private var updatedAt: Date?
    @State private var onScreen = false

    private var active: Bool { isVisible && onScreen }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            toolbar
            Divider()
            if let servers {
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 280), spacing: 12, alignment: .top)], spacing: 12) {
                            ForEach(servers.map(HealthModel.card)) { ServerCardView(card: $0) }
                        }
                        cameraTable(HealthModel.rows(servers))
                    }
                    .padding(14)
                }
            } else if let error {
                Text(error).foregroundStyle(.red).padding(14)
                Spacer()
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .onAppear { onScreen = true }
        .onDisappear { onScreen = false }
        // The only timer: restarts when visibility changes, cancelled when it ends.
        .task(id: active) {
            guard active else { return }
            while Task.isCancelled == false {
                await refresh()
                try? await Task.sleep(nanoseconds: HealthThresholds.refreshSeconds * 1_000_000_000)
            }
        }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            Text("System Monitor").font(.headline)
            if let updatedAt {
                Text("Updated \(updatedAt.formatted(date: .omitted, time: .standard))").font(.caption).foregroundStyle(.secondary)
            }
            if let error, servers != nil {
                Label(error, systemImage: "exclamationmark.triangle.fill").font(.caption).foregroundStyle(SentinelTheme.amber).lineLimit(1)
            }
            Spacer()
            if isLoading { ProgressView().controlSize(.small) }
            Button {
                Task { await refresh() }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .disabled(isLoading)
        }
        .padding(.horizontal, 14).padding(.vertical, 8)
    }

    private func cameraTable(_ rows: [CameraHealthRow]) -> some View {
        Table(rows) {
            TableColumn("Server") { Text($0.server) }.width(min: 90, ideal: 130)
            TableColumn("Camera") { r in
                HStack(spacing: 6) {
                    Circle().fill(healthColor(r.state)).frame(width: 8, height: 8)
                    Text(r.name)
                }
            }.width(min: 120, ideal: 180)
            TableColumn("Online") { r in
                Text(r.online ? "Online" : "Offline").foregroundStyle(r.online ? Color.primary : Color.red)
            }.width(min: 60, ideal: 70)
            TableColumn("Recording") { r in
                Text(r.recording ? "Recording" : "Off").foregroundStyle(.secondary)
            }.width(min: 70, ideal: 90)
            TableColumn("Last segment") { r in
                Text(r.lastSegment).foregroundStyle(r.lastSegmentLevel == .off ? Color.secondary : healthColor(r.lastSegmentLevel))
            }.width(min: 80, ideal: 100)
            TableColumn("Rate") { r in Text(r.kbps).monospacedDigit() }.width(min: 70, ideal: 90)
        }
        .frame(minHeight: CGFloat(rows.count) * 26 + 40)
        .overlay {
            if rows.isEmpty { Text("No cameras reported.").foregroundStyle(.secondary) }
        }
    }

    private func refresh() async {
        isLoading = true
        defer { isLoading = false }
        do {
            servers = try await runtime.api.systemHealth()
            error = nil
            updatedAt = Date()
        } catch {
            // Keep the last reading on screen; say what went wrong.
            self.error = error.localizedDescription
        }
    }
}

func healthColor(_ level: HealthLevel) -> Color {
    switch level {
    case .ok: return .green
    case .warn: return SentinelTheme.amber
    case .crit: return .red
    case .off: return .gray
    }
}

private struct ServerCardView: View {
    let card: ServerCard

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Circle().fill(healthColor(card.state)).frame(width: 10, height: 10)
                Text(card.name).font(.headline).lineLimit(1)
                Spacer()
                Text(card.online ? "Online" : "Offline").font(.caption.weight(.semibold))
                    .foregroundStyle(card.online ? Color.secondary : Color.red)
            }
            HStack(spacing: 12) {
                if card.version.isEmpty == false { Text("v\(card.version)") }
                if card.uptime.isEmpty == false { Text("up \(card.uptime)") }
                if card.latency.isEmpty == false { Text(card.latency) }
                Text("\(card.camerasOnline)/\(card.cameras) cameras")
            }
            .font(.caption).foregroundStyle(.secondary)
            MeterView(title: "CPU", meter: card.cpu)
            MeterView(title: "Memory", meter: card.mem)
            MeterView(title: "Disk", meter: card.disk)
            if card.db.isEmpty == false {
                Text("Database \(card.db)").font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
        .opacity(card.online ? 1 : 0.8)
    }
}

private struct MeterView: View {
    let title: String
    let meter: HealthMeter

    var body: some View {
        HStack(spacing: 8) {
            Text(title).font(.caption).frame(width: 52, alignment: .leading)
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.primary.opacity(0.1))
                    Capsule().fill(healthColor(meter.level))
                        .frame(width: geo.size.width * CGFloat((meter.pct ?? 0) / 100))
                }
            }
            .frame(height: 6)
            Text(meter.label).font(.caption.monospacedDigit()).frame(width: 40, alignment: .trailing)
        }
        .help(meter.detail)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(title) \(meter.label)")
    }
}
