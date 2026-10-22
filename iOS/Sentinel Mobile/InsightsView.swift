// InsightsView.swift
// Insights (WAVE15): what happened on the site's cameras over a range, with
// the server's recommendations. Read-only, reached from Home. The only change
// it can make is switching a camera to people & vehicles, and only for a user
// with manageCameras. The arithmetic is in InsightsLogic.swift.

import SwiftUI
import Charts

// MARK: - Model

@MainActor
final class InsightsModel: ObservableObject {
    @Published var range: InsightsMath.Range = .default
    @Published private(set) var insights: V2.Insights?
    @Published private(set) var loading = false
    @Published var error: String?
    @Published var notice: String?
    @Published private(set) var busyRecommendationIDs: Set<String> = []

    func reload(api: SentinelAPI?) async {
        guard let api else { return }
        loading = true
        defer { loading = false }
        do {
            let query = InsightsMath.RangeQuery(range, now: Date().unixMillis)
            insights = try await api.insights(query)
            error = nil
        } catch is CancellationError {
        } catch {
            self.error = InsightsMath.errorMessage(error)
        }
    }

    func switchAlerts(_ rec: V2.Recommendation, cameraID: String, alertOn: V2.AlertOn, api: SentinelAPI?) async {
        guard let api else { return }
        busyRecommendationIDs.insert(rec.id)
        defer { busyRecommendationIDs.remove(rec.id) }
        do {
            try await api.setAlertOn(cameraID: cameraID, alertOn)
            Haptics.success()
            error = nil
            notice = "Saved. The camera now raises alarms for \(InsightsMath.alertOnLabel(alertOn).lowercased())."
            await reload(api: api)
        } catch is CancellationError {
        } catch {
            Haptics.warning()
            self.error = InsightsMath.errorMessage(error)
        }
    }
}

// MARK: - Screen

struct InsightsView: View {
    @ObservedObject private var session = SentinelSession.shared
    @StateObject private var model = InsightsModel()
    @Environment(\.horizontalSizeClass) private var sizeClass

    /// Only what /api/v2/me says: unknown permissions count as no here.
    private func can(_ permission: V2.Permission) -> Bool {
        session.me?.can(permission) ?? false
    }

    private var regular: Bool { LayoutLogic.usesSplit(for: LayoutWidth(sizeClass)) }

    var body: some View {
        VStack(spacing: 0) {
            // A sidebar section in regular width (WAVE19): its own title,
            // like Events and People; compact keeps the pushed screen's bar.
            if regular {
                PageHeader(title: "Insights", subtitle: "Trends and recommendations")
            }
            screen
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        .navigationTitle("Insights")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(regular ? .hidden : .visible, for: .navigationBar)
        .task(id: TaskKey(range: model.range, signedIn: session.me != nil)) {
            guard session.me != nil else { return }
            model.notice = nil
            await model.reload(api: session.api)
        }
    }

    private var screen: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            if session.isDemoMode {
                DemoUnavailable(symbol: "chart.bar.xaxis", title: "Insights need a site",
                                text: "Insights summarize your Sentinel server's alarms and detections. Connect to a site to see them.")
            } else if session.me == nil {
                ProgressView().tint(SentinelTheme.accent)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
                        Picker("Range", selection: $model.range) {
                            ForEach(InsightsMath.Range.allCases) { Text($0.label).tag($0) }
                        }
                        .pickerStyle(.segmented)

                        if let error = model.error {
                            ActionErrorBanner(message: error) { model.error = nil }
                        }
                        if let notice = model.notice {
                            noticeBanner(notice)
                        }
                        content
                    }
                    .padding(SentinelTheme.Space.lg)
                    .frame(maxWidth: LayoutLogic.dashboardMaxWidth)
                    .frame(maxWidth: .infinity)
                }
                .refreshable { await model.reload(api: session.api) }
            }
        }
    }

    private struct TaskKey: Equatable {
        var range: InsightsMath.Range
        var signedIn: Bool
    }

    @ViewBuilder
    private var content: some View {
        if let data = model.insights {
            if InsightsMath.isEmpty(data) {
                placeholder(symbol: "chart.bar", title: "Not enough activity yet",
                            text: "Insights appear once there are alarms or events in the range.")
            } else {
                loaded(data)
                    .opacity(model.loading ? 0.6 : 1)
            }
        } else if model.error == nil {
            ProgressView().tint(SentinelTheme.accent).frame(maxWidth: .infinity).padding(.top, 60)
        }
    }

    @ViewBuilder
    private func loaded(_ data: V2.Insights) -> some View {
        let stack = InsightsMath.stackDaily(data.daily)
        Text("Days follow the management server's clock.")
            .font(.caption).foregroundStyle(.white.opacity(0.55))
        // Insights has no list to select from, so regular width (and pushed
        // from Home, where a split can't nest) gets two columns instead:
        // the summary and what to do on the left, the charts on the right.
        if LayoutLogic.usesSplit(for: LayoutWidth(sizeClass)) {
            HStack(alignment: .top, spacing: SentinelTheme.Space.lg) {
                VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
                    totals(data.totals)
                    recommendations(data.recommendations)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
                    DailyBarsCard(stack: stack)
                    mix(data.mix, kinds: stack.kinds)
                    busiestCameras(data.cameras)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            totals(data.totals)
            recommendations(data.recommendations)
            DailyBarsCard(stack: stack)
            mix(data.mix, kinds: stack.kinds)
            busiestCameras(data.cameras)
        }
    }

    // MARK: Totals

    private func totals(_ t: V2.Insights.Totals) -> some View {
        LazyVGrid(columns: [GridItem(.flexible(), spacing: SentinelTheme.Space.md),
                            GridItem(.flexible(), spacing: SentinelTheme.Space.md)],
                  spacing: SentinelTheme.Space.md) {
            tile("Alarms", t.alarms, icon: "bell.fill", color: SentinelTheme.alarm)
            tile("Events", t.events, icon: "sparkles", color: SentinelTheme.accent)
            tile("Cameras with alarms", t.camerasWithAlarms, icon: "video.fill", color: SentinelTheme.amber)
            tile("Offline cameras", t.offlineCameras, icon: "video.slash.fill", color: .gray)
        }
    }

    private func tile(_ title: String, _ value: Int, icon: String, color: Color) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Image(systemName: icon).font(.callout).foregroundStyle(color)
            Text(value.formatted()).font(.title3.weight(.bold)).foregroundStyle(.white)
            Text(title).font(.caption2).foregroundStyle(.white.opacity(0.55)).lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(SentinelTheme.Space.md)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
        .accessibilityElement(children: .combine)
    }

    // MARK: Recommendations

    private func recommendations(_ recs: [V2.Recommendation]) -> some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            SectionHeader(title: "Recommendations")
            if recs.isEmpty {
                SentinelCard {
                    Text("Nothing to suggest for this range.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            } else {
                ForEach(recs) { rec in
                    recommendationCard(rec)
                }
            }
        }
    }

    private func recommendationCard(_ rec: V2.Recommendation) -> some View {
        let style = levelStyle(rec.level)
        return SentinelCard {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack(spacing: 8) {
                    Chip(text: style.label.uppercased(), color: style.color)
                    Spacer(minLength: 0)
                }
                Text(rec.title).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                Text(rec.detail).font(.caption).foregroundStyle(.white.opacity(0.7))
                    .fixedSize(horizontal: false, vertical: true)
                switch InsightsMath.action(for: rec, can: can) {
                case let .setAlertOn(label, cameraID, alertOn):
                    let busy = model.busyRecommendationIDs.contains(rec.id)
                    Button {
                        Haptics.tap()
                        Task { await model.switchAlerts(rec, cameraID: cameraID, alertOn: alertOn, api: session.api) }
                    } label: {
                        HStack(spacing: 6) {
                            if busy { ProgressView().tint(.white).controlSize(.small) }
                            Text(busy ? "Switching…" : label).font(.subheadline.weight(.semibold))
                        }
                        .foregroundStyle(.white)
                        .padding(.horizontal, 14).padding(.vertical, 8)
                        .background(SentinelTheme.accent, in: Capsule())
                    }
                    .buttonStyle(.plain)
                    .disabled(busy)
                case let .hint(text):
                    Label(text, systemImage: "info.circle")
                        .font(.caption2).foregroundStyle(.white.opacity(0.5))
                case nil:
                    EmptyView()
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func levelStyle(_ level: V2.Recommendation.Level) -> (label: String, color: Color) {
        switch level {
        case .attention: return ("Attention", SentinelTheme.amber)
        case .suggestion: return ("Suggestion", SentinelTheme.accent)
        default: return ("Info", .gray)
        }
    }

    // MARK: Alarm mix

    private func mix(_ mix: [V2.Insights.KindShare], kinds: [String]) -> some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            SectionHeader(title: "Alarm mix")
            SentinelCard {
                if mix.isEmpty {
                    Text("No alarms in this range.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    VStack(spacing: 10) {
                        ForEach(mix, id: \.kind) { m in
                            mixRow(m, color: InsightsPalette.color(for: m.kind, kinds: kinds))
                        }
                    }
                }
            }
        }
    }

    private func mixRow(_ m: V2.Insights.KindShare, color: Color) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Circle().fill(color).frame(width: 8, height: 8)
                Text(InsightsMath.kindLabel(m.kind)).font(.caption.weight(.semibold)).foregroundStyle(.white)
                Spacer()
                Text("\(m.alarms.formatted()) · \(InsightsMath.formatShare(m.share))")
                    .font(.caption.monospacedDigit()).foregroundStyle(.white.opacity(0.7))
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.white.opacity(0.08))
                    Capsule().fill(color).frame(width: geo.size.width * min(1, max(0, m.share)))
                }
            }
            .frame(height: 6)
            .accessibilityHidden(true)
        }
        .accessibilityElement(children: .combine)
    }

    // MARK: Busiest cameras

    private func busiestCameras(_ cameras: [V2.Insights.Camera]) -> some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            SectionHeader(title: "Busiest cameras")
            SentinelCard(padding: 0) {
                if cameras.isEmpty {
                    Text("No camera activity in this range.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(16)
                } else {
                    VStack(spacing: 0) {
                        ForEach(Array(cameras.enumerated()), id: \.element.cameraId) { index, camera in
                            if index > 0 { Divider().overlay(SentinelTheme.line) }
                            cameraRow(camera)
                        }
                    }
                }
            }
        }
    }

    private func cameraRow(_ c: V2.Insights.Camera) -> some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 2) {
                Text(c.name).font(.subheadline.weight(.semibold)).foregroundStyle(.white).lineLimit(1)
                Text("Most often \(c.topKind.map(InsightsMath.kindLabel) ?? "—") · alarms for \(InsightsMath.alertOnLabel(c.alertOn).lowercased())")
                    .font(.caption2).foregroundStyle(.white.opacity(0.55))
            }
            Spacer(minLength: 8)
            Text(c.alarms.formatted()).font(.headline.monospacedDigit()).foregroundStyle(.white)
        }
        .padding(.horizontal, 16).padding(.vertical, 12)
        .accessibilityElement(children: .combine)
    }

    // MARK: Chrome

    private func noticeBanner(_ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "checkmark.circle.fill").foregroundStyle(SentinelTheme.recording)
            Text(text).font(.caption).foregroundStyle(.white).frame(maxWidth: .infinity, alignment: .leading)
            Button { model.notice = nil } label: {
                Image(systemName: "xmark").font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Dismiss")
        }
        .padding(12)
        .background(SentinelTheme.recording.opacity(0.14), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.recording.opacity(0.35), lineWidth: 1))
    }

    private func placeholder(symbol: String, title: String, text: String) -> some View {
        VStack(spacing: 10) {
            Image(systemName: symbol).font(.system(size: 40)).foregroundStyle(SentinelTheme.accent)
            Text(title).font(.headline).foregroundStyle(.white)
            Text(text).font(.caption).foregroundStyle(.white.opacity(0.55))
                .multilineTextAlignment(.center).padding(.horizontal, 40)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 60)
    }
}

// MARK: - Daily bars

/// A kind's colour by its slot among the chart's kinds; Other is grey.
enum InsightsPalette {
    static let slots: [Color] = [
        SentinelTheme.accent,
        SentinelTheme.amber,
        SentinelTheme.recording,
        Color(red: 0.80, green: 0.30, blue: 0.85)
    ]

    static func color(for kind: String, kinds: [String]) -> Color {
        guard kind != InsightsMath.other, let i = kinds.firstIndex(of: kind), i < slots.count else { return .gray }
        return slots[i]
    }
}

/// Alarms per day as stacked bars: one segment per top kind, the rest as Other.
struct DailyBarsCard: View {
    let stack: InsightsMath.Stack

    private struct Mark: Identifiable {
        var day: String
        var kind: String
        var count: Int
        var id: String { day + "|" + kind }
    }

    private var marks: [Mark] {
        stack.bars.flatMap { bar in bar.segments.map { Mark(day: bar.day, kind: $0.kind, count: $0.count) } }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            SectionHeader(title: "Alarms per day")
            SentinelCard {
                if stack.peak == 0 {
                    Text("No alarms in this range.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    chart
                }
            }
        }
    }

    @ViewBuilder
    private var chart: some View {
        let peak = stack.peak
        let busiest = stack.bars.first { $0.total == peak }
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            if let busiest {
                Text("Busiest: \(InsightsMath.dayLabel(busiest.day)), \(peak.formatted()) alarm\(peak == 1 ? "" : "s")")
                    .font(.caption).foregroundStyle(.white.opacity(0.6))
            }
            Chart(marks) { m in
                BarMark(x: .value("Day", m.day), y: .value("Alarms", m.count))
                    .foregroundStyle(by: .value("Kind", InsightsMath.kindLabel(m.kind)))
            }
            .chartForegroundStyleScale(domain: stack.kinds.map(InsightsMath.kindLabel),
                                       range: stack.kinds.map { InsightsPalette.color(for: $0, kinds: stack.kinds) })
            .chartXScale(domain: stack.bars.map(\.day))
            .chartXAxis(.hidden)
            .chartLegend(.hidden)
            .frame(height: 160)
            .accessibilityLabel("Alarms per day by kind")
            if let first = stack.bars.first, let last = stack.bars.last {
                HStack {
                    Text(InsightsMath.dayLabel(first.day))
                    Spacer()
                    if stack.bars.count > 1 { Text(InsightsMath.dayLabel(last.day)) }
                }
                .font(.caption2).foregroundStyle(.white.opacity(0.5))
            }
            legend
        }
    }

    private var legend: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 100), alignment: .leading)], alignment: .leading, spacing: 6) {
            ForEach(stack.kinds, id: \.self) { kind in
                HStack(spacing: 6) {
                    RoundedRectangle(cornerRadius: 2).fill(InsightsPalette.color(for: kind, kinds: stack.kinds))
                        .frame(width: 10, height: 10)
                    Text(InsightsMath.kindLabel(kind)).font(.caption2).foregroundStyle(.white.opacity(0.75))
                }
            }
        }
    }
}

/// People and Insights read the site's own data; Demo Mode has none, so the
/// screen says so instead of waiting forever (WAVE19).
struct DemoUnavailable: View {
    let symbol: String
    let title: String
    let text: String

    var body: some View {
        VStack(spacing: SentinelTheme.Space.md) {
            ZStack {
                Circle().fill(SentinelTheme.accent.opacity(0.12)).frame(width: 76, height: 76)
                Image(systemName: symbol)
                    .font(.system(size: 28, weight: .semibold)).foregroundStyle(SentinelTheme.accent)
            }
            Text(title).font(.headline).foregroundStyle(.white)
            Text(text)
                .font(.subheadline).foregroundStyle(.white.opacity(0.6))
                .multilineTextAlignment(.center)
                .frame(maxWidth: 320)
        }
        .padding(SentinelTheme.Space.xl)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
