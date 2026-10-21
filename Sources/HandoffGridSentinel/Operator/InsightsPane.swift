import AppKit
import Charts
import SwiftUI
import SentinelCore

// Management › Basics › Insights (wave 15): what happened over a range, with
// recommendations, from GET /api/v2/insights. No AI call. The arithmetic is in
// SentinelCore's InsightsModel.swift; buttons follow the session's permissions.

/// Hand-offs from one Management pane to another: a template for the Rules
/// editor, a camera for the Cameras settings sheet. The receiving pane
/// consumes and clears it when it appears.
@MainActor
final class ManagementLinks: ObservableObject {
    static let shared = ManagementLinks()
    @Published var templateID: String?
    @Published var cameraID: String?
}

struct InsightsPane: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator
    let context: ManagementPaneContext

    @State private var range = InsightsRange.default
    @State private var data: V2.Insights?
    @State private var error: String?
    @State private var loading = false
    @State private var done: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Text("What happened on your cameras, with suggestions. Days follow the management server's clock.")
                        .font(.callout).foregroundStyle(.secondary)
                    Spacer()
                    Picker("Range", selection: $range) {
                        ForEach(InsightsRange.allCases) { Text($0.label).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .frame(width: 240)
                    Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                        .help("Reload")
                }
                if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
                if let done { Text(done).font(.caption.weight(.semibold)).foregroundStyle(.green) }
                if let data {
                    if data.isEmpty {
                        EmptyStateLine(text: "Not enough activity yet. Insights appear once there are alarms or events in the range.")
                    } else {
                        content(data).opacity(loading ? 0.6 : 1)
                    }
                } else if loading {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            .padding(18)
        }
        .task(id: range) {
            done = nil
            await load()
        }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        let bounds = range.bounds(now: Date())
        do {
            data = try await runtime.api.insights(from: bounds.from, to: bounds.to)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    @ViewBuilder
    private func content(_ data: V2.Insights) -> some View {
        let stack = InsightsLogic.stackDaily(data.daily)
        HStack(spacing: 10) {
            tile("Alarms", data.totals.alarms)
            tile("Events", data.totals.events)
            tile("Cameras with alarms", data.totals.camerasWithAlarms)
            tile("Offline cameras", data.totals.offlineCameras, note: "Had a Camera Offline alarm")
        }
        card("Recommendations") {
            if data.recommendations.isEmpty {
                Text("Nothing to suggest for this range.").font(.caption).foregroundStyle(.secondary)
            } else {
                ForEach(data.recommendations) { rec in
                    RecommendationRow(rec: rec, plan: InsightsLogic.action(for: rec, can: session.can), perform: perform)
                }
            }
        }
        card("Alarms per day") { DailyChart(kinds: stack.kinds, bars: stack.bars) }
        HStack(alignment: .top, spacing: 14) {
            card("Alarm mix") {
                if data.mix.isEmpty {
                    Text("No alarms in this range.").font(.caption).foregroundStyle(.secondary)
                } else {
                    ForEach(data.mix, id: \.kind) { m in
                        HStack(spacing: 8) {
                            Circle().fill(Self.color(m.kind, kinds: stack.kinds)).frame(width: 8, height: 8)
                            Text(InsightsLogic.kindLabel(m.kind)).frame(width: 120, alignment: .leading)
                            GeometryReader { geo in
                                Capsule().fill(SentinelTheme.line)
                                    .overlay(alignment: .leading) {
                                        Capsule().fill(Self.color(m.kind, kinds: stack.kinds))
                                            .frame(width: geo.size.width * min(1, max(0, m.share)))
                                    }
                            }
                            .frame(height: 6)
                            Text("\(m.alarms) · \(InsightsLogic.formatShare(m.share))")
                                .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                                .frame(width: 90, alignment: .trailing)
                        }
                        .font(.callout)
                    }
                }
            }
            card("Busiest cameras") {
                if data.cameras.isEmpty {
                    Text("No camera activity in this range.").font(.caption).foregroundStyle(.secondary)
                } else {
                    Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 6) {
                        GridRow {
                            Text("Camera"); Text("Alarms"); Text("Most often"); Text("Raises alarms for")
                        }
                        .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                        ForEach(data.cameras, id: \.cameraId) { c in
                            GridRow {
                                Text(c.name).font(.callout.weight(.semibold))
                                Text("\(c.alarms)").monospacedDigit()
                                Text(c.topKind.map(InsightsLogic.kindLabel) ?? "—")
                                Text(InsightsLogic.alertOnLabel(c.alertOn))
                            }
                            .font(.callout)
                        }
                    }
                }
            }
        }
    }

    private func perform(_ plan: InsightsActionPlan) async -> String? {
        switch plan {
        case let .setAlertOn(cameraId, alertOn):
            var patch = V2.CameraPatch()
            patch.alertOn = alertOn
            do {
                let camera = try await runtime.api.updateCamera(cameraId, patch)
                cameras.replace(camera)
                done = "Saved. The camera now raises alarms for \(InsightsLogic.alertOnLabel(alertOn).lowercased())."
                await load()
                return nil
            } catch {
                return InsightsLogic.alertChangeError(error)
            }
        case let .openTemplate(templateId):
            ManagementLinks.shared.templateID = templateId
            context.open(ManagementCatalog.Leaf.rules)
        case let .openCamera(cameraId):
            ManagementLinks.shared.cameraID = cameraId
            context.open(ManagementCatalog.Leaf.cameras)
        case .openStorage:
            // Stay in this window when the tree has System Monitor (viewAudit);
            // otherwise the main window's System Monitor tab, open to every viewer.
            if ManagementNavModel(can: session.can).node(ManagementCatalog.Leaf.systemMonitor) != nil {
                context.open(ManagementCatalog.Leaf.systemMonitor)
            } else {
                navigator.tabRequest = FixedTab.monitor.rawValue
                (NSApp.delegate as? SentinelOperatorApplication)?.openMainWindow()
            }
        }
        return nil
    }

    /// The chart's colour for a kind: its slot among the top kinds, or Other's grey.
    static func color(_ kind: String, kinds: [String]) -> Color {
        guard kind != InsightsLogic.other, let i = kinds.firstIndex(of: kind), i < palette.count else { return .gray }
        return palette[i]
    }

    static let palette: [Color] = [SentinelTheme.accent, SentinelTheme.amber, .purple, .teal]

    private func tile(_ title: String, _ value: Int, note: String? = nil) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            Text("\(value)").font(.title2.weight(.semibold)).monospacedDigit()
            if let note { Text(note).font(.caption2).foregroundStyle(.secondary) }
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

private struct RecommendationRow: View {
    let rec: V2.Recommendation
    let plan: InsightsActionPlan?
    let perform: (InsightsActionPlan) async -> String?

    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Text(levelLabel).font(.caption2.weight(.bold))
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(levelColor.opacity(0.2), in: Capsule())
                    .foregroundStyle(levelColor)
                Text(rec.title).font(.callout.weight(.semibold))
                Spacer()
                if let plan {
                    Button(busy ? "Working…" : plan.label) {
                        busy = true
                        error = nil
                        Task {
                            error = await perform(plan)
                            busy = false
                        }
                    }
                    .controlSize(.small)
                    .disabled(busy)
                }
            }
            Text(rec.detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let error { Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red) }
        }
        .padding(.vertical, 4)
    }

    private var levelLabel: String {
        switch rec.level {
        case "attention": return "Attention"
        case "suggestion": return "Suggestion"
        default: return "Info"
        }
    }

    private var levelColor: Color {
        switch rec.level {
        case "attention": return SentinelTheme.amber
        case "suggestion": return SentinelTheme.accent
        default: return .secondary
        }
    }
}

/// Alarms per day as stacked bars: one segment per top kind, the rest as Other.
private struct DailyChart: View {
    let kinds: [String]
    let bars: [InsightsLogic.DayBar]

    private struct Mark: Identifiable {
        var day: String
        var kind: String
        var count: Int
        var id: String { day + "|" + kind }
    }

    private var marks: [Mark] {
        bars.flatMap { bar in bar.segments.map { Mark(day: bar.day, kind: $0.kind, count: $0.count) } }
    }

    var body: some View {
        if bars.allSatisfy({ $0.total == 0 }) {
            Text("No alarms in this range.").font(.caption).foregroundStyle(.secondary)
        } else {
            Chart(marks) { m in
                BarMark(x: .value("Day", InsightsLogic.dayLabel(m.day)), y: .value("Alarms", m.count))
                    .foregroundStyle(by: .value("Kind", InsightsLogic.kindLabel(m.kind)))
                    .accessibilityLabel(InsightsLogic.dayLabel(m.day))
                    .accessibilityValue("\(InsightsLogic.kindLabel(m.kind)) \(m.count)")
            }
            .chartXScale(domain: bars.map { InsightsLogic.dayLabel($0.day) })
            .chartForegroundStyleScale(domain: kinds.map(InsightsLogic.kindLabel),
                                       range: kinds.map { InsightsPane.color($0, kinds: kinds) })
            .chartXAxis {
                AxisMarks { _ in
                    AxisValueLabel(collisionResolution: .greedy)
                }
            }
            .frame(height: 180)
        }
    }
}
