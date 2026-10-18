// LastAlarmWidget.swift
// "Last alarm": the newest open alarm's title, camera and time, with the
// event's thumbnail on the medium size when the app could share one.
// Tapping opens that alarm in the app.

import SwiftUI
import WidgetKit

struct LastAlarmWidget: Widget {
    let kind = "SentinelLastAlarm"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: SnapshotProvider()) { entry in
            LastAlarmView(entry: entry)
                .widgetURL(entry.snapshot.lastAlarm.map { SentinelDeepLink.alarm(id: $0.id).url } ?? SentinelDeepLink.alarms.url)
        }
        .configurationDisplayName("Last alarm")
        .description("The newest open alarm at your Sentinel site.")
        .supportedFamilies([.systemSmall, .systemMedium, .accessoryRectangular, .accessoryInline])
    }
}

struct LastAlarmView: View {
    @Environment(\.widgetFamily) private var family
    let entry: SnapshotEntry

    private var s: WidgetSnapshot { entry.snapshot }

    var body: some View {
        switch family {
        case .accessoryRectangular: rectangular
        case .accessoryInline: inline
        case .systemMedium: home(showImage: true).sentinelWidgetBackground()
        default: home(showImage: false).sentinelWidgetBackground()
        }
    }

    @ViewBuilder
    private func home(showImage: Bool) -> some View {
        if s.signedIn == false {
            SignedOutWidgetView()
        } else if let alarm = s.lastAlarm {
            HStack(spacing: 10) {
                details(alarm)
                if showImage, let image = entry.image {
                    Image(uiImage: image)
                        .resizable()
                        .aspectRatio(contentMode: .fill)
                        .frame(width: 120)
                        .frame(maxHeight: .infinity)
                        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                }
            }
        } else {
            allClear
        }
    }

    private func details(_ alarm: WidgetSnapshot.AlarmSummary) -> some View {
        let color = WidgetPalette.severity(alarm.severity)
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 5) {
                Image(systemName: WidgetPalette.symbol(severity: alarm.severity))
                    .foregroundStyle(color)
                Text(alarm.severity.uppercased())
                    .font(.caption2.weight(.heavy))
                    .foregroundStyle(color)
                Spacer(minLength: 0)
                if s.openAlarms > 1 {
                    Text("+\(s.openAlarms - 1)")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(.white.opacity(0.6))
                }
            }
            .font(.caption)
            Text(alarm.title)
                .font(.headline)
                .foregroundStyle(.white)
                .lineLimit(2)
            Text(alarm.cameraName.isEmpty ? "Whole site" : alarm.cameraName)
                .font(.caption)
                .foregroundStyle(.white.opacity(0.7))
                .lineLimit(1)
            Spacer(minLength: 0)
            HStack(spacing: 4) {
                Text(alarm.raisedAt, style: .relative)
                Text("ago")
            }
            .font(.caption2)
            .foregroundStyle(.white.opacity(0.55))
            .lineLimit(1)
            Text(alarm.state)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(WidgetPalette.phase(AlarmActivityPhase(serverState: alarm.state)))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var allClear: some View {
        VStack(alignment: .leading, spacing: 6) {
            Image(systemName: "checkmark.shield.fill")
                .font(.title2)
                .foregroundStyle(WidgetPalette.green)
            Text("All clear")
                .font(.headline)
                .foregroundStyle(.white)
            Text(s.siteName.isEmpty ? "No open alarms." : "No open alarms at \(s.siteName).")
                .font(.caption)
                .foregroundStyle(.white.opacity(0.6))
            Spacer(minLength: 0)
            if entry.isStale { StaleBadge(generatedAt: s.generatedAt) }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    @ViewBuilder
    private var rectangular: some View {
        if let alarm = s.lastAlarm, s.signedIn {
            VStack(alignment: .leading, spacing: 1) {
                Label(alarm.title, systemImage: WidgetPalette.symbol(severity: alarm.severity))
                    .font(.headline)
                    .lineLimit(1)
                Text(alarm.cameraName.isEmpty ? "Whole site" : alarm.cameraName)
                    .lineLimit(1)
                Text(alarm.raisedAt, style: .relative)
                    .foregroundStyle(.secondary)
            }
            .font(.caption)
        } else if s.signedIn {
            Label("No open alarms", systemImage: "checkmark.shield")
        } else {
            Text("Sentinel: not signed in")
        }
    }

    private var inline: some View {
        Group {
            if let alarm = s.lastAlarm, s.signedIn {
                Label("\(alarm.title) · \(alarm.cameraName)", systemImage: WidgetPalette.symbol(severity: alarm.severity))
            } else {
                Label("No open alarms", systemImage: "checkmark.shield")
            }
        }
    }
}
