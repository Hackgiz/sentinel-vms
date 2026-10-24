import Charts
import SwiftUI
import SentinelCore

// Reports workspace (parity 3.4.2): how busy the site was over a range.
// Totals, detections as bars, alarms by severity and a row per camera, from
// GET /api/v2/reports/activity (already limited to the cameras this person
// can see). Matches the Linux Operator's ReportsView; the arithmetic is in
// SentinelCore's ReportModel.swift.

struct ReportsWorkspaceView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory

    @State private var range = ReportRange.default
    @State private var report: V2.Activity?
    @State private var error: String?
    @State private var loading = false
    @State private var gate = LatestLoad()

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Text(summary).font(.callout).foregroundStyle(.secondary)
                    Spacer()
                    Picker("Range", selection: $range) {
                        ForEach(ReportRange.allCases) { Text($0.label).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .frame(width: 360)
                    Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                        .help("Reload")
                }
                if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                if let report {
                    content(report).opacity(loading ? 0.6 : 1)
                } else if loading {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Counting…").font(.callout).foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity)
                }
            }
            .padding(18)
        }
        .background(SentinelTheme.background)
        .task(id: range) { await load() }
    }

    private var summary: String {
        if let report {
            return "\(report.totals.events.formatted()) detections over \(range.label.lowercased())"
        }
        return "Activity over \(range.label.lowercased())"
    }

    private func load() async {
        // Only the latest load may publish or clear the flag: a cancelled one
        // (the range changed) finishes late and must change nothing.
        let ticket = gate.begin()
        loading = true
        defer { if gate.finish(ticket) { loading = false } }
        let bounds = range.bounds(now: Date())
        do {
            let result = try await runtime.api.activityReport(from: bounds.from, to: bounds.to)
            guard gate.isLatest(ticket) else { return }
            report = result
            error = nil
        } catch {
            guard gate.isLatest(ticket), (error is CancellationError) == false else { return }
            self.error = error.localizedDescription
        }
    }

    @ViewBuilder
    private func content(_ report: V2.Activity) -> some View {
        let names = Dictionary(cameras.cameras.map { ($0.id, $0.name) }, uniquingKeysWith: { first, _ in first })
        let rows = ReportModel.cameraRows(report, names: names)
        let bars = ReportModel.series(report, bucket: range.bucket)
        let severities = ReportModel.severityTotals(report)

        HStack(spacing: 10) {
            tile("Detections", report.totals.events)
            tile("Alarms", report.totals.alarms)
            tile("Cameras with activity", ReportModel.activeCameras(rows))
        }
        card(range.bucket == .hour ? "Detections per hour" : "Detections per day") {
            if report.totals.events == 0 {
                Text("Nothing was detected in this range.").font(.caption).foregroundStyle(.secondary)
            } else {
                ActivityChart(bars: bars, bucket: range.bucket)
            }
        }
        card("Alarms by severity") {
            if severities.isEmpty {
                Text("No alarms in this range.").font(.caption).foregroundStyle(.secondary)
            } else {
                HStack(spacing: 8) {
                    ForEach(severities, id: \.severity) { s in
                        let color = AlertSeverity(rawValue: s.severity).map(SentinelTheme.severityColor) ?? .secondary
                        HStack(spacing: 6) {
                            Circle().fill(color).frame(width: 8, height: 8)
                            Text(s.severity).font(.callout)
                            Text("\(s.count)").font(.callout.weight(.semibold)).monospacedDigit()
                        }
                        .padding(.horizontal, 10).padding(.vertical, 5)
                        .background(color.opacity(0.12), in: Capsule())
                    }
                }
            }
        }
        card("By camera") {
            if rows.isEmpty {
                Text("No camera activity in this range.").font(.caption).foregroundStyle(.secondary)
            } else {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 6) {
                    GridRow {
                        Text("Camera"); Text("Detections"); Text("Alarms"); Text("Most often")
                    }
                    .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    ForEach(rows) { row in
                        GridRow {
                            Text(row.name).font(.callout.weight(.semibold)).lineLimit(1)
                            Text("\(row.events)").monospacedDigit()
                            Text("\(row.alarms)").monospacedDigit()
                            if let kind = row.topKind {
                                KindChip(kind: ReportModel.kindLabel(kind))
                            } else {
                                Text("–").foregroundStyle(.secondary)
                            }
                        }
                        .font(.callout)
                    }
                }
            }
        }
    }

    private func tile(_ title: String, _ value: Int) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            Text(value.formatted()).font(.title2.weight(.semibold)).monospacedDigit()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }

    private func card<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.headline)
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
    }
}

/// Detections per bucket as bars, in the accent colour (as on the web).
private struct ActivityChart: View {
    let bars: [ReportModel.Bar]
    let bucket: ReportModel.Bucket

    var body: some View {
        Chart(bars) { bar in
            BarMark(x: .value("Time", Date(unixMillis: bar.at), unit: bucket == .hour ? .hour : .day),
                    y: .value("Detections", bar.count))
                .foregroundStyle(SentinelTheme.accent)
                .accessibilityLabel(Date(unixMillis: bar.at).formatted(bucket == .hour ? .dateTime.hour() : .dateTime.month().day()))
                .accessibilityValue("\(bar.count)")
        }
        .chartXAxis {
            AxisMarks { _ in
                AxisGridLine()
                AxisValueLabel(format: bucket == .hour ? .dateTime.hour() : .dateTime.month(.abbreviated).day(),
                               collisionResolution: .greedy)
            }
        }
        .frame(height: 160)
    }
}
