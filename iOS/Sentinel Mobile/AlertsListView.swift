// AlertsListView.swift
// The Mac's alarm queue on the phone: triage-ordered cards with the alarm
// rule's operator instructions and the two remote actions the Mac allows —
// Acknowledge, and Lock Evidence (preserve the recording). Shown in the Events
// tab's "Alarms" mode and, for the top alarms, on Home.

import SwiftUI

struct AlarmsListView: View {
    @ObservedObject var store = AppStore.shared

    private var needsAttention: [SentinelAlert] { store.sortedAlarms.filter(\.needsAttention) }
    private var inProgress: [SentinelAlert] { store.sortedAlarms.filter { $0.needsAttention == false } }

    var body: some View {
        LazyVStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            if let error = store.actionError {
                ActionErrorBanner(message: error) { store.actionError = nil }
            }
            if store.alerts.isEmpty {
                allClear.padding(.top, 60)
            } else {
                if needsAttention.isEmpty == false {
                    sectionLabel("Needs attention", count: needsAttention.count, color: SentinelTheme.alarm)
                    ForEach(needsAttention) { AlarmCard(alert: $0) }
                }
                if inProgress.isEmpty == false {
                    sectionLabel("In progress", count: inProgress.count, color: SentinelTheme.accent)
                        .padding(.top, needsAttention.isEmpty ? 0 : SentinelTheme.Space.sm)
                    ForEach(inProgress) { AlarmCard(alert: $0) }
                }
            }
        }
    }

    private func sectionLabel(_ title: String, count: Int, color: Color) -> some View {
        HStack(spacing: 6) {
            Circle().fill(color).frame(width: 7, height: 7)
            Text(title.uppercased())
                .font(.caption.weight(.bold))
                .tracking(0.6)
                .foregroundStyle(.white.opacity(0.6))
            Text("\(count)")
                .font(.caption.weight(.bold))
                .foregroundStyle(color)
        }
    }

    private var allClear: some View {
        VStack(spacing: 10) {
            ZStack {
                Circle().fill(SentinelTheme.recording.opacity(0.14)).frame(width: 84, height: 84)
                Image(systemName: "checkmark.shield.fill")
                    .font(.system(size: 38))
                    .foregroundStyle(SentinelTheme.recording)
            }
            Text("All clear").font(.title3.weight(.bold)).foregroundStyle(.white)
            Text("No open alarms on your Mac.")
                .font(.subheadline)
                .foregroundStyle(.white.opacity(0.55))
        }
        .frame(maxWidth: .infinity)
    }
}

/// One alarm: what happened, where, what to do, and the actions.
struct AlarmCard: View {
    @ObservedObject var store = AppStore.shared
    let alert: SentinelAlert
    /// Home shows a tighter version (clamped text) above the stats.
    var compact = false

    private var color: Color { SentinelTheme.severityColor(alert.severity) }
    private var isBusy: Bool { store.busyAlarmIDs.contains(alert.id) }
    private var camera: SentinelCameraSummary? { alert.cameraID.flatMap { store.camera(id: $0) } }

    var body: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            header

            if alert.detail.isEmpty == false {
                Text(alert.detail)
                    .font(.callout)
                    .foregroundStyle(.white.opacity(0.75))
                    .lineLimit(compact ? 2 : 4)
                    .fixedSize(horizontal: false, vertical: true)
            }

            chips

            if let instructions = alert.instructions, instructions.isEmpty == false {
                instructionsBlock(instructions)
            }

            if let evidence = alert.evidence, evidence.isLocked {
                Label("Evidence \(evidence.caseID) locked · safe from cleanup", systemImage: "lock.shield.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(SentinelTheme.recording)
            }

            actions
        }
        .padding(SentinelTheme.Space.md)
        .padding(.leading, 4)
        .background(SentinelTheme.panel)
        .overlay(alignment: .leading) {
            Rectangle().fill(color).frame(width: 4)
        }
        .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous)
                .stroke(alert.needsAttention ? color.opacity(0.45) : SentinelTheme.line, lineWidth: 1)
        )
        .animation(.easeInOut(duration: 0.2), value: alert)
    }

    private var header: some View {
        HStack(alignment: .top, spacing: SentinelTheme.Space.md) {
            ZStack {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .fill(color.opacity(0.16))
                    .frame(width: 40, height: 40)
                Image(systemName: Self.symbol(for: alert.kind, severity: alert.severity))
                    .font(.callout.weight(.bold))
                    .foregroundStyle(color)
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(alert.title)
                    .font(.headline)
                    .foregroundStyle(.white)
                    .lineLimit(2)
                HStack(spacing: 4) {
                    if let name = store.cameraName(for: alert) {
                        Text(name)
                        Text("·")
                    }
                    Text(alert.lastEventDate, format: .relative(presentation: .named))
                }
                .font(.caption)
                .foregroundStyle(.white.opacity(0.55))
                .lineLimit(1)
            }
            Spacer(minLength: 4)
            SentinelSeverityBadge(severity: alert.severity)
        }
    }

    private var chips: some View {
        HStack(spacing: 6) {
            Chip(text: alert.state, color: SentinelTheme.stateColor(alert.state))
            if let count = alert.eventCount, count > 1 {
                Chip(text: "\(count) events", systemImage: "square.stack.3d.up.fill", color: .gray)
            }
            if let who = alert.assignee {
                Chip(text: who, systemImage: "person.fill", color: SentinelTheme.accent)
                    .lineLimit(1)
            }
        }
    }

    private func instructionsBlock(_ instructions: [SentinelAlarmInstruction]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("What to do", systemImage: "list.clipboard.fill")
                .font(.caption.weight(.bold))
                .foregroundStyle(SentinelTheme.amber)
            ForEach(instructions, id: \.self) { item in
                VStack(alignment: .leading, spacing: 2) {
                    Text(item.text)
                        .font(.callout)
                        .foregroundStyle(.white)
                        .lineLimit(compact ? 3 : nil)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(item.rule)
                        .font(.caption2)
                        .foregroundStyle(.white.opacity(0.45))
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(SentinelTheme.Space.md)
        .background(SentinelTheme.amber.opacity(0.09), in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.amber.opacity(0.25), lineWidth: 1))
    }

    @ViewBuilder
    private var actions: some View {
        let showAck = alert.canAcknowledge
        let showLock = alert.canLockEvidence
        if showAck || showLock || camera != nil {
            HStack(spacing: SentinelTheme.Space.sm) {
                if showAck {
                    AlarmActionButton(title: "Acknowledge", systemImage: "checkmark.circle.fill", prominent: true, isBusy: isBusy) {
                        Task { await store.acknowledge(alert) }
                    }
                }
                if showLock {
                    AlarmActionButton(title: "Lock Evidence", systemImage: "lock.fill", prominent: showAck == false, isBusy: isBusy) {
                        Task { await store.lockEvidence(alert) }
                    }
                }
                if let camera {
                    NavigationLink(value: camera) {
                        Image(systemName: "video.fill")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(.white)
                            .frame(width: 44, height: 40)
                            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                    }
                    .accessibilityLabel("View \(camera.name)")
                }
            }
        }
    }

    /// Mac AlertKind raw values → symbols.
    static func symbol(for kind: String?, severity: String) -> String {
        switch kind {
        case "Person": return "figure.walk"
        case "Motion": return "figure.walk.motion"
        case "Camera Offline": return "video.slash.fill"
        case "Recording": return "record.circle"
        case "Storage": return "internaldrive.fill"
        case "Credential": return "key.fill"
        default:
            return severity == "Critical" ? "exclamationmark.octagon.fill" : "exclamationmark.triangle.fill"
        }
    }
}

struct AlarmActionButton: View {
    let title: String
    let systemImage: String
    var prominent = false
    let isBusy: Bool
    let action: () -> Void

    var body: some View {
        Button {
            Haptics.tap()
            action()
        } label: {
            HStack(spacing: 6) {
                if isBusy {
                    ProgressView().tint(.white).controlSize(.small)
                } else {
                    Image(systemName: systemImage)
                }
                Text(title).lineLimit(1)
            }
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(.white)
            .frame(maxWidth: .infinity)
            .frame(height: 40)
            .background(
                prominent ? SentinelTheme.accent : SentinelTheme.panelRaised,
                in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous)
            )
            .overlay(
                RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous)
                    .stroke(prominent ? .clear : SentinelTheme.line, lineWidth: 1)
            )
        }
        .buttonStyle(.plain)
        .disabled(isBusy)
    }
}

struct ActionErrorBanner: View {
    let message: String
    let dismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(SentinelTheme.alarm)
            Text(message)
                .font(.caption)
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button(action: dismiss) {
                Image(systemName: "xmark")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.white.opacity(0.6))
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Dismiss")
        }
        .padding(12)
        .background(SentinelTheme.alarm.opacity(0.14), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.alarm.opacity(0.35), lineWidth: 1))
    }
}
