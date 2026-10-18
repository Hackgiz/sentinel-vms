// SiteStatusWidget.swift
// "Site status": cameras online / total and whether they are recording.
// Home Screen small + medium; Lock Screen circular, rectangular and inline.

import SwiftUI
import WidgetKit

struct SiteStatusWidget: Widget {
    let kind = "SentinelSiteStatus"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: SnapshotProvider()) { entry in
            SiteStatusView(entry: entry)
                .widgetURL(SentinelDeepLink.home.url)
        }
        .configurationDisplayName("Site status")
        .description("Cameras online and recording at your Sentinel site.")
        .supportedFamilies([.systemSmall, .systemMedium, .accessoryCircular, .accessoryRectangular, .accessoryInline])
    }
}

struct SiteStatusView: View {
    @Environment(\.widgetFamily) private var family
    let entry: SnapshotEntry

    private var s: WidgetSnapshot { entry.snapshot }
    private var allOnline: Bool { s.camerasTotal > 0 && s.camerasOnline == s.camerasTotal }
    private var statusColor: Color {
        if s.camerasTotal == 0 { return .gray }
        if s.camerasOnline == 0 { return WidgetPalette.red }
        return allOnline ? WidgetPalette.green : WidgetPalette.amber
    }
    private var recordingText: String {
        switch s.recordingState {
        case .all: return "Recording"
        case .some: return "\(s.camerasRecording) recording"
        case .none: return "Not recording"
        }
    }

    var body: some View {
        switch family {
        case .accessoryCircular: circular
        case .accessoryRectangular: rectangular
        case .accessoryInline: inline
        case .systemMedium: medium.sentinelWidgetBackground()
        default: small.sentinelWidgetBackground()
        }
    }

    // MARK: Home Screen

    private var small: some View {
        Group {
            if s.signedIn == false {
                SignedOutWidgetView()
            } else {
                VStack(alignment: .leading, spacing: 4) {
                    header
                    Spacer(minLength: 0)
                    HStack(alignment: .firstTextBaseline, spacing: 2) {
                        Text("\(s.camerasOnline)")
                            .font(.system(size: 34, weight: .bold, design: .rounded))
                            .foregroundStyle(.white)
                        Text("/\(s.camerasTotal)")
                            .font(.system(size: 18, weight: .semibold, design: .rounded))
                            .foregroundStyle(.white.opacity(0.5))
                    }
                    Text("cameras online")
                        .font(.caption2)
                        .foregroundStyle(.white.opacity(0.6))
                    recordingLabel
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
        }
    }

    private var medium: some View {
        Group {
            if s.signedIn == false {
                SignedOutWidgetView()
            } else {
                VStack(alignment: .leading, spacing: 10) {
                    header
                    HStack(spacing: 10) {
                        tile(value: "\(s.camerasOnline)/\(s.camerasTotal)", label: "Online", color: statusColor)
                        tile(value: "\(s.camerasRecording)", label: "Recording",
                             color: s.recordingState == .none ? .gray : WidgetPalette.green)
                        tile(value: "\(s.openAlarms)", label: "Open alarms",
                             color: s.newAlarms > 0 ? WidgetPalette.red : .gray)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
        }
    }

    private var header: some View {
        HStack(spacing: 5) {
            Circle().fill(statusColor).frame(width: 7, height: 7)
            Text(s.siteName.isEmpty ? "Sentinel" : s.siteName)
                .font(.caption.weight(.bold))
                .foregroundStyle(.white.opacity(0.85))
                .lineLimit(1)
            Spacer(minLength: 0)
            if entry.isStale { StaleBadge(generatedAt: s.generatedAt) }
        }
    }

    private var recordingLabel: some View {
        Label(recordingText, systemImage: s.recordingState == .none ? "record.circle" : "record.circle.fill")
            .font(.caption2.weight(.semibold))
            .foregroundStyle(s.recordingState == .none ? .gray : WidgetPalette.green)
            .lineLimit(1)
    }

    private func tile(value: String, label: String, color: Color) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(value)
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(.white)
                .minimumScaleFactor(0.6)
                .lineLimit(1)
            Text(label)
                .font(.caption2)
                .foregroundStyle(color)
                .lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(8)
        .background(WidgetPalette.panel, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    // MARK: Lock Screen

    @ViewBuilder
    private var circular: some View {
        if s.signedIn, s.camerasTotal > 0 {
            Gauge(value: Double(s.camerasOnline), in: 0...Double(max(s.camerasTotal, 1))) {
                Image(systemName: "video.fill")
            } currentValueLabel: {
                Text("\(s.camerasOnline)")
            }
            .gaugeStyle(.accessoryCircularCapacity)
        } else {
            Image(systemName: "shield.lefthalf.filled").font(.title2)
        }
    }

    @ViewBuilder
    private var rectangular: some View {
        if s.signedIn {
            VStack(alignment: .leading, spacing: 1) {
                Text(s.siteName.isEmpty ? "Sentinel" : s.siteName)
                    .font(.headline)
                    .lineLimit(1)
                Text("\(s.camerasOnline)/\(s.camerasTotal) cameras online")
                    .lineLimit(1)
                Text(s.openAlarms > 0 ? "\(recordingText) · \(s.openAlarms) alarms" : recordingText)
                    .lineLimit(1)
                    .foregroundStyle(.secondary)
            }
            .font(.caption)
        } else {
            Text("Sentinel: not signed in")
        }
    }

    private var inline: some View {
        Group {
            if s.signedIn {
                Label("\(s.camerasOnline)/\(s.camerasTotal) online · \(recordingText)", systemImage: "video.fill")
            } else {
                Label("Sentinel: not signed in", systemImage: "shield.lefthalf.filled")
            }
        }
    }
}
