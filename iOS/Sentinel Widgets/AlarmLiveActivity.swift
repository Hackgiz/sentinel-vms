// AlarmLiveActivity.swift
// The alarm Live Activity: camera, title, severity, state (New /
// Acknowledged / Closed), who owns it and how long it has been open, on the
// Lock Screen and in the Dynamic Island (compact, minimal, expanded). The app
// starts it when the user follows an alarm and updates it from the event
// stream; the Management Server updates it over APNs while the app is away.

import ActivityKit
import SwiftUI
import WidgetKit

struct AlarmLiveActivityWidget: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: AlarmActivityAttributes.self) { context in
            AlarmLockScreenView(attributes: context.attributes, content: context.state)
                .activityBackgroundTint(WidgetPalette.background.opacity(0.92))
                .activitySystemActionForegroundColor(.white)
                .widgetURL(context.attributes.deepLink)
        } dynamicIsland: { context in
            let attributes = context.attributes
            let content = context.state
            let color = WidgetPalette.severity(content.severity)
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Label {
                        Text(content.severity)
                    } icon: {
                        Image(systemName: WidgetPalette.symbol(severity: content.severity))
                    }
                    .font(.caption.weight(.bold))
                    .foregroundStyle(color)
                    .lineLimit(1)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    AlarmPhaseChip(content: content)
                }
                DynamicIslandExpandedRegion(.center) {
                    Text(attributes.title)
                        .font(.headline)
                        .lineLimit(1)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    HStack {
                        Label(attributes.cameraName.isEmpty ? "Whole site" : attributes.cameraName, systemImage: "video.fill")
                            .lineLimit(1)
                        Spacer()
                        if let who = content.assignee {
                            Label(who, systemImage: "person.fill").lineLimit(1)
                        }
                        ElapsedText(attributes: attributes, content: content)
                    }
                    .font(.caption)
                    .foregroundStyle(.white.opacity(0.75))
                }
            } compactLeading: {
                Image(systemName: WidgetPalette.symbol(severity: content.severity))
                    .foregroundStyle(color)
            } compactTrailing: {
                if content.phase == .closed {
                    Image(systemName: "checkmark.circle.fill").foregroundStyle(WidgetPalette.green)
                } else {
                    ElapsedText(attributes: attributes, content: content)
                        .frame(maxWidth: 52)
                        .foregroundStyle(WidgetPalette.phase(content.phase))
                }
            } minimal: {
                Image(systemName: content.phase == .closed ? "checkmark.circle.fill" : WidgetPalette.symbol(severity: content.severity))
                    .foregroundStyle(content.phase == .closed ? WidgetPalette.green : color)
            }
            .widgetURL(attributes.deepLink)
            .keylineTint(color)
        }
    }
}

struct AlarmLockScreenView: View {
    let attributes: AlarmActivityAttributes
    let content: AlarmActivityContent

    var body: some View {
        let color = WidgetPalette.severity(content.severity)
        HStack(alignment: .top, spacing: 12) {
            ZStack {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .fill(color.opacity(0.18))
                    .frame(width: 44, height: 44)
                Image(systemName: WidgetPalette.symbol(severity: content.severity))
                    .font(.title3.weight(.bold))
                    .foregroundStyle(color)
            }
            VStack(alignment: .leading, spacing: 3) {
                HStack {
                    Text(attributes.title)
                        .font(.headline)
                        .foregroundStyle(.white)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    AlarmPhaseChip(content: content)
                }
                Text(attributes.cameraName.isEmpty ? "Whole site" : attributes.cameraName)
                    .font(.subheadline)
                    .foregroundStyle(.white.opacity(0.75))
                    .lineLimit(1)
                HStack(spacing: 8) {
                    Text(content.severity)
                        .foregroundStyle(color)
                    if let who = content.assignee {
                        Label(who, systemImage: "person.fill").lineLimit(1)
                    }
                    if content.eventCount > 1 {
                        Text("\(content.eventCount) events")
                    }
                    Spacer(minLength: 0)
                    ElapsedText(attributes: attributes, content: content)
                }
                .font(.caption)
                .foregroundStyle(.white.opacity(0.6))
            }
        }
        .padding(14)
    }
}

struct AlarmPhaseChip: View {
    let content: AlarmActivityContent

    var body: some View {
        let color = WidgetPalette.phase(content.phase)
        Text(content.phase == .acknowledged ? content.state : content.phase.rawValue)
            .font(.caption2.weight(.bold))
            .foregroundStyle(color)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(color.opacity(0.18), in: Capsule())
            .lineLimit(1)
    }
}

/// Time since the alarm was raised; frozen at the last change once closed.
struct ElapsedText: View {
    let attributes: AlarmActivityAttributes
    let content: AlarmActivityContent

    var body: some View {
        if content.phase == .closed {
            let seconds = max(0, Double(content.updatedAt - attributes.raisedAt) / 1000)
            Text(Duration.seconds(seconds), format: .time(pattern: seconds >= 3600 ? .hourMinute : .minuteSecond))
                .monospacedDigit()
        } else {
            Text(attributes.raisedDate, style: .timer)
                .monospacedDigit()
                .multilineTextAlignment(.trailing)
        }
    }
}
