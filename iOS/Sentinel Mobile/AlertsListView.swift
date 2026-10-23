// AlertsListView.swift
// The site's alarm queue on the phone (API v2): triage-ordered cards with the
// alarm rule's operator instructions and quick Acknowledge, plus a detail
// screen with the full response flow (investigate, snooze, resolve, false
// alarm, notes) and evidence lock. Shown in the Events tab's "Alarms" mode
// and, for the top alarm, on Home.

import SwiftUI

struct AlarmsListView: View {
    @ObservedObject var store = AppStore.shared
    @Environment(\.horizontalSizeClass) private var sizeClass

    /// Regular width (WAVE19): one-line cards beside the detail pane.
    private var dense: Bool { LayoutWidth(sizeClass) == .regular }

    private var needsAttention: [V2.Alarm] { store.sortedAlarms.filter(\.needsAttention) }
    private var inProgress: [V2.Alarm] { store.sortedAlarms.filter { $0.needsAttention == false } }

    var body: some View {
        LazyVStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            if let error = store.actionError {
                ActionErrorBanner(message: error) { store.actionError = nil }
            }
            if store.alarms.isEmpty {
                allClear.padding(.top, 60)
            } else {
                if needsAttention.isEmpty == false {
                    sectionLabel("Needs attention", count: needsAttention.count, color: SentinelTheme.alarm)
                    ForEach(needsAttention) { AlarmCard(alarm: $0, dense: dense, inList: true) }
                }
                if inProgress.isEmpty == false {
                    sectionLabel("In progress", count: inProgress.count, color: SentinelTheme.accent)
                        .padding(.top, needsAttention.isEmpty ? 0 : SentinelTheme.Space.sm)
                    ForEach(inProgress) { AlarmCard(alarm: $0, dense: dense, inList: true) }
                }
            }
        }
        .offlineBackupOffer() // WAVE12(D): "Offline — show cloud backup" when the site can't be reached
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
            Text("No open alarms.")
                .font(.subheadline)
                .foregroundStyle(.white.opacity(0.55))
        }
        .frame(maxWidth: .infinity)
    }
}

/// One alarm: what happened, where, what to do, and the quick actions.
struct AlarmCard: View {
    @ObservedObject var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared
    @ObservedObject private var activities = AlarmActivityController.shared
    let alarm: V2.Alarm
    /// Home shows a tighter version (clamped text) above the stats.
    var compact = false
    /// Regular width (WAVE19): title and severity on one line, camera and
    /// time under it, one line of detail; the rest is in the detail pane.
    var dense = false
    /// In the Events list, where regular width shows the action row only on
    /// the selected card.
    var inList = false
    @Environment(\.selectAlarm) private var selectAlarm
    @Environment(\.selectedRowID) private var selectedRowID
    @Environment(\.horizontalSizeClass) private var sizeClass

    private var showsActions: Bool {
        LayoutLogic.alarmActionsVisible(width: LayoutWidth(sizeClass), inList: inList, selected: selectedRowID == alarm.id)
    }

    private var color: Color { SentinelTheme.alarmColor(alarm) }
    private var isBusy: Bool { store.busyAlarmIDs.contains(alarm.id) }
    private var camera: V2.Camera? { alarm.cameraId.flatMap { store.camera(id: $0) } }

    var body: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            SelectOrLink(alarm, select: selectAlarm) {
                if dense { denseSummary } else { summary }
            }

            if showsActions { actions }
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
                .stroke(selectedRowID == alarm.id ? SentinelTheme.accent : (alarm.needsAttention ? color.opacity(0.45) : SentinelTheme.line),
                        lineWidth: selectedRowID == alarm.id ? 2 : 1)
        )
        .animation(.easeInOut(duration: 0.2), value: alarm)
        .alert("Couldn't update the alarm", isPresented: Binding(
            get: { store.actionErrorAlarmID == alarm.id && store.actionError != nil },
            set: { if $0 == false { store.actionErrorAlarmID = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(store.actionError ?? "")
        }
    }

    private var summary: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            header

            if alarm.detail.isEmpty == false {
                Text(alarm.detail)
                    .font(.callout)
                    .foregroundStyle(.white.opacity(0.75))
                    .lineLimit(compact ? 2 : 4)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
            }

            chips

            if alarm.instructions.isEmpty == false {
                instructionsBlock(alarm.instructions)
            }

            if let evidence = store.evidence[alarm.id] {
                Label("Evidence \(evidence.caseId) locked · safe from cleanup", systemImage: "lock.shield.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(SentinelTheme.recording)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }

    private var denseSummary: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
                symbolBadge(size: 36)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(alignment: .firstTextBaseline, spacing: SentinelTheme.Space.sm) {
                        Text(alarm.title)
                            .font(.headline)
                            .foregroundStyle(.white)
                            .lineLimit(1)
                        Spacer(minLength: 4)
                        SentinelSeverityBadge(severity: alarm.severity.rawValue)
                            .fixedSize()
                    }
                    HStack(spacing: 4) {
                        if let name = store.cameraName(for: alarm) {
                            Text(name)
                            Text("·")
                        }
                        Text(alarm.lastEventDate, format: .relative(presentation: .named))
                        Spacer(minLength: 4)
                        Text(alarm.state.rawValue)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(SentinelTheme.stateColor(alarm.state.rawValue))
                    }
                    .font(.caption)
                    .foregroundStyle(.white.opacity(0.55))
                    .lineLimit(1)
                }
            }
            if alarm.detail.isEmpty == false {
                Text(alarm.detail)
                    .font(.callout)
                    .foregroundStyle(.white.opacity(0.75))
                    .lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }

    private func symbolBadge(size: CGFloat) -> some View {
        ZStack {
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .fill(color.opacity(0.16))
                .frame(width: size, height: size)
            Image(systemName: Self.symbol(for: alarm.kind, severity: alarm.severity.rawValue))
                .font(.callout.weight(.bold))
                .foregroundStyle(color)
        }
    }

    private var header: some View {
        HStack(alignment: .top, spacing: SentinelTheme.Space.md) {
            ZStack {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .fill(color.opacity(0.16))
                    .frame(width: 40, height: 40)
                Image(systemName: Self.symbol(for: alarm.kind, severity: alarm.severity.rawValue))
                    .font(.callout.weight(.bold))
                    .foregroundStyle(color)
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(alarm.title)
                    .font(.headline)
                    .foregroundStyle(.white)
                    .multilineTextAlignment(.leading)
                    .lineLimit(2)
                HStack(spacing: 4) {
                    if let name = store.cameraName(for: alarm) {
                        Text(name)
                        Text("·")
                    }
                    Text(alarm.lastEventDate, format: .relative(presentation: .named))
                }
                .font(.caption)
                .foregroundStyle(.white.opacity(0.55))
                .lineLimit(1)
            }
            Spacer(minLength: 4)
            SentinelSeverityBadge(severity: alarm.severity.rawValue)
        }
    }

    private var chips: some View {
        HStack(spacing: 6) {
            Chip(text: alarm.state.rawValue, color: SentinelTheme.stateColor(alarm.state.rawValue))
            if alarm.eventCount > 1 {
                Chip(text: "\(alarm.eventCount) events", systemImage: "square.stack.3d.up.fill", color: .gray)
            }
            if let who = alarm.assignee {
                Chip(text: who, systemImage: "person.fill", color: SentinelTheme.accent)
                    .lineLimit(1)
            }
        }
    }

    private func instructionsBlock(_ instructions: [V2.Instruction]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("What to do", systemImage: "list.clipboard.fill")
                .font(.caption.weight(.bold))
                .foregroundStyle(SentinelTheme.amber)
            ForEach(Array(instructions.enumerated()), id: \.offset) { _, item in
                VStack(alignment: .leading, spacing: 2) {
                    Text(item.text)
                        .font(.callout)
                        .foregroundStyle(.white)
                        .multilineTextAlignment(.leading)
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
        let showAck = alarm.canAcknowledge && session.can(.acknowledgeAlarms)
        let showFollow = activities.isAvailable && alarm.isOpen
        if showAck || camera != nil || showFollow {
            HStack(spacing: SentinelTheme.Space.sm) {
                if showAck {
                    AlarmActionButton(title: "Acknowledge", systemImage: "checkmark.circle.fill", prominent: true, isBusy: isBusy) {
                        Task { await store.acknowledge(alarm) }
                    }
                }
                if showFollow {
                    FollowAlarmButton(alarm: alarm, cameraName: store.cameraName(for: alarm), compact: true)
                }
                if let camera {
                    // A destination link, not NavigationLink(value: camera): it carries
                    // the alarm time into Playback, and works in every stack (Events,
                    // Home) without a route of its own.
                    NavigationLink {
                        CameraDetailView(camera: camera, startAt: session.isDemoMode ? nil : PlaybackModel.alarmStart(alarm.createdAt))
                    } label: {
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

    /// Alarm kinds → symbols: system kinds here, detection kinds from
    /// `SentinelTheme.kindSymbol`, anything else by severity.
    static func symbol(for kind: String?, severity: String) -> String {
        switch kind {
        case "Camera Offline": return "video.slash.fill"
        case "Recording": return "record.circle"
        case "Storage": return "internaldrive.fill"
        case "Credential": return "key.fill"
        case let kind? where SentinelTheme.hasKindSymbol(kind):
            return SentinelTheme.kindSymbol(kind)
        default:
            return severity == "Critical" ? "exclamationmark.octagon.fill" : "exclamationmark.triangle.fill"
        }
    }
}

/// Starts / stops the alarm Live Activity for this alarm.
struct FollowAlarmButton: View {
    @ObservedObject private var activities = AlarmActivityController.shared
    let alarm: V2.Alarm
    let cameraName: String?
    /// Icon-only (alarm cards) or a full-width labelled button (detail).
    var compact = false

    private var following: Bool { activities.isFollowing(alarm) }

    var body: some View {
        Button {
            Haptics.tap()
            if following { activities.unfollow() } else { activities.follow(alarm, cameraName: cameraName) }
        } label: {
            Group {
                if compact {
                    Image(systemName: following ? "pin.fill" : "pin")
                        .frame(width: 44, height: 40)
                } else {
                    Label(following ? "Following on Lock Screen" : "Follow on Lock Screen",
                          systemImage: following ? "pin.fill" : "pin")
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, SentinelTheme.Space.md)
                }
            }
            .font(compact ? .subheadline.weight(.semibold) : .headline)
            .foregroundStyle(following ? SentinelTheme.accent : .white)
            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm)
                .stroke(following ? SentinelTheme.accent.opacity(0.6) : SentinelTheme.line, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(following ? "Stop following this alarm" : "Follow this alarm on the Lock Screen")
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
                Text(title).lineLimit(1).minimumScaleFactor(0.75)
            }
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 8)
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

// MARK: - Alarm detail

/// One alarm with the full response flow. Reads the alarm from the shared
/// store (so live updates show up) and falls back to fetching it.
struct AlarmDetailView: View {
    let alarmID: String
    @ObservedObject private var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared
    @State private var fetched: V2.Alarm?
    @State private var note = ""
    @Environment(\.horizontalSizeClass) private var sizeClass

    private var alarm: V2.Alarm? { store.alarms.first { $0.id == alarmID } ?? fetched }
    /// Regular width (WAVE19): the Events detail pane, laid out for the room.
    private var wide: Bool { LayoutWidth(sizeClass) == .regular }

    var body: some View {
        ScrollView {
            if let alarm {
                if wide {
                    wideContent(alarm)
                        .frame(maxWidth: 760)
                        .frame(maxWidth: .infinity)
                } else {
                    content(alarm)
                }
            } else {
                ProgressView().tint(SentinelTheme.accent).padding(.top, 60)
            }
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        .navigationTitle("Alarm")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .task(id: alarmID) {
            if store.alarms.contains(where: { $0.id == alarmID }) == false, let api = session.api {
                fetched = try? await api.alarm(alarmID)
            }
        }
    }

    @ViewBuilder
    private func content(_ alarm: V2.Alarm) -> some View {
        let color = SentinelTheme.alarmColor(alarm)
        let busy = store.busyAlarmIDs.contains(alarm.id)
        VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
            if let error = store.actionError {
                ActionErrorBanner(message: error) { store.actionError = nil }
            }

            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack {
                    SentinelSeverityBadge(severity: alarm.severity.rawValue)
                    Chip(text: alarm.state.rawValue, color: SentinelTheme.stateColor(alarm.state.rawValue))
                    Spacer()
                }
                Text(alarm.title).font(.title3.weight(.bold)).foregroundStyle(.white)
                if alarm.detail.isEmpty == false {
                    Text(alarm.detail).font(.callout).foregroundStyle(.white.opacity(0.75))
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(SentinelTheme.Space.md)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(color.opacity(0.4), lineWidth: 1))

            if let eventID = alarm.eventId, let event = store.events.first(where: { $0.id == eventID }), event.hasThumbnail {
                EventThumbnail(event: event, contentMode: .fit)
                    .frame(maxWidth: .infinity).frame(height: 220)
                    .background(.black)
                    .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            }

            VStack(alignment: .leading, spacing: 8) {
                row("Camera", store.cameraName(for: alarm) ?? "Whole site")
                row("Raised", alarm.createdAtDate.formatted(date: .abbreviated, time: .standard))
                row("Latest", alarm.lastEventDate.formatted(date: .abbreviated, time: .standard))
                row("Events", "\(alarm.eventCount)")
                if let who = alarm.assignee { row("Handled by", who) }
            }

            if alarm.instructions.isEmpty == false {
                VStack(alignment: .leading, spacing: 8) {
                    Label("What to do", systemImage: "list.clipboard.fill")
                        .font(.caption.weight(.bold)).foregroundStyle(SentinelTheme.amber)
                    ForEach(Array(alarm.instructions.enumerated()), id: \.offset) { _, item in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(item.text).font(.callout).foregroundStyle(.white)
                            Text(item.rule).font(.caption2).foregroundStyle(.white.opacity(0.45))
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(SentinelTheme.Space.md)
                .background(SentinelTheme.amber.opacity(0.09), in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.amber.opacity(0.25), lineWidth: 1))
            }

            if AlarmActivityController.shared.isAvailable, alarm.isOpen {
                FollowAlarmButton(alarm: alarm, cameraName: store.cameraName(for: alarm), compact: false)
            }

            respond(alarm, busy: busy)
            evidenceSection(alarm, busy: busy)

            if alarm.responseLog.isEmpty == false {
                VStack(alignment: .leading, spacing: 6) {
                    Label("Response log", systemImage: "text.bubble")
                        .font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
                    ForEach(Array(alarm.responseLog.enumerated()), id: \.offset) { _, line in
                        Text(line).font(.caption).foregroundStyle(.white.opacity(0.8))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }

            if let camera = alarm.cameraId.flatMap({ store.camera(id: $0) }) {
                NavigationLink {
                    CameraDetailView(camera: camera, startAt: session.isDemoMode ? nil : PlaybackModel.alarmStart(alarm.createdAt))
                } label: {
                    Label("View \(camera.name)", systemImage: "video.fill")
                        .font(.headline)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, SentinelTheme.Space.md)
                        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                        .foregroundStyle(.white)
                }
            }
        }
        .padding(SentinelTheme.Space.lg)
    }

    // MARK: Regular width

    @ViewBuilder
    private func wideContent(_ alarm: V2.Alarm) -> some View {
        let color = SentinelTheme.alarmColor(alarm)
        let busy = store.busyAlarmIDs.contains(alarm.id)
        let camera = alarm.cameraId.flatMap { store.camera(id: $0) }
        VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
            if let error = store.actionError {
                ActionErrorBanner(message: error) { store.actionError = nil }
            }

            media(alarm, camera: camera)

            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack(spacing: SentinelTheme.Space.sm) {
                    SentinelSeverityBadge(severity: alarm.severity.rawValue)
                    Chip(text: alarm.state.rawValue, color: SentinelTheme.stateColor(alarm.state.rawValue))
                    if alarm.eventCount > 1 {
                        Chip(text: "\(alarm.eventCount) events", systemImage: "square.stack.3d.up.fill", color: .gray)
                    }
                    Spacer()
                }
                Text(alarm.title)
                    .font(.title2.weight(.bold))
                    .foregroundStyle(.white)
                HStack(spacing: 6) {
                    Image(systemName: "video.fill").foregroundStyle(color)
                    Text(store.cameraName(for: alarm) ?? "Whole site")
                    Text("·")
                    Text(alarm.lastEventDate, format: .relative(presentation: .named))
                }
                .font(.subheadline)
                .foregroundStyle(.white.opacity(0.6))
                .lineLimit(1)
                if alarm.detail.isEmpty == false {
                    Text(alarm.detail)
                        .font(.body)
                        .foregroundStyle(.white.opacity(0.85))
                        .padding(.top, 2)
                }
                if let who = alarm.assignee {
                    Label("Handled by \(who)", systemImage: "person.fill")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(SentinelTheme.accent)
                }
            }

            respondWide(alarm, busy: busy)

            if alarm.instructions.isEmpty == false {
                VStack(alignment: .leading, spacing: 8) {
                    Label("What to do", systemImage: "list.clipboard.fill")
                        .font(.caption.weight(.bold)).foregroundStyle(SentinelTheme.amber)
                    ForEach(Array(alarm.instructions.enumerated()), id: \.offset) { _, item in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(item.text).font(.callout).foregroundStyle(.white)
                            Text(item.rule).font(.caption2).foregroundStyle(.white.opacity(0.45))
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(SentinelTheme.Space.md)
                .background(SentinelTheme.amber.opacity(0.09), in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.amber.opacity(0.25), lineWidth: 1))
            }

            eventStrip(alarm, camera: camera)

            if let camera {
                NavigationLink {
                    CameraDetailView(camera: camera, startAt: session.isDemoMode ? nil : PlaybackModel.alarmStart(alarm.createdAt))
                } label: {
                    Label("Open camera", systemImage: "video.fill")
                        .font(.headline)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, SentinelTheme.Space.md)
                        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                        .foregroundStyle(.white)
                }
                .accessibilityLabel("Open \(camera.name)")
            }

            if AlarmActivityController.shared.isAvailable, alarm.isOpen {
                FollowAlarmButton(alarm: alarm, cameraName: store.cameraName(for: alarm), compact: false)
            }

            evidenceSection(alarm, busy: busy)

            VStack(alignment: .leading, spacing: 8) {
                row("Raised", alarm.createdAtDate.formatted(date: .abbreviated, time: .standard))
                row("Latest", alarm.lastEventDate.formatted(date: .abbreviated, time: .standard))
                row("Events", "\(alarm.eventCount)")
            }

            if alarm.responseLog.isEmpty == false {
                VStack(alignment: .leading, spacing: 6) {
                    Label("Response log", systemImage: "text.bubble")
                        .font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
                    ForEach(Array(alarm.responseLog.enumerated()), id: \.offset) { _, line in
                        Text(line).font(.caption).foregroundStyle(.white.opacity(0.8))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        }
        .padding(SentinelTheme.Space.xl)
    }

    /// The alarm's snapshot when the server kept one, else the camera live.
    @ViewBuilder
    private func media(_ alarm: V2.Alarm, camera: V2.Camera?) -> some View {
        let snapshot = alarm.eventId.flatMap { id in store.events.first { $0.id == id } }
        if let snapshot, snapshot.hasThumbnail {
            mediaFrame(label: "SNAPSHOT") {
                EventThumbnail(event: snapshot, contentMode: .fit)
            }
        } else if let camera {
            mediaFrame(label: camera.isOnline ? "LIVE" : nil) {
                LiveStreamView(camera: camera)
            }
        }
    }

    private func mediaFrame<Content: View>(label: String?, @ViewBuilder content: () -> Content) -> some View {
        content()
            .aspectRatio(16 / 9, contentMode: .fit)
            .frame(maxWidth: .infinity)
            .background(.black)
            .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
            .overlay(alignment: .topLeading) {
                if let label {
                    Chip(text: label, systemImage: label == "LIVE" ? "dot.radiowaves.left.and.right" : "photo",
                         color: label == "LIVE" ? SentinelTheme.alarm : SentinelTheme.accent, filled: true)
                        .padding(10)
                }
            }
    }

    /// The alarm's events as a strip; an alarm the server raised without
    /// linked events shows the camera's recent detections instead.
    @ViewBuilder
    private func eventStrip(_ alarm: V2.Alarm, camera: V2.Camera?) -> some View {
        let linked = store.events.filter { $0.alarmId == alarm.id || $0.id == alarm.eventId }
        let fallback = alarm.cameraId.map { id in store.events.filter { $0.cameraId == id } } ?? []
        let events = Array((linked.isEmpty ? fallback : linked).prefix(10))
        if events.isEmpty == false {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                Label(linked.isEmpty ? "Recent on \(camera?.name ?? "this camera")" : "Events in this alarm",
                      systemImage: "square.stack.3d.up.fill")
                    .font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: SentinelTheme.Space.md) {
                        ForEach(events) { event in
                            Button {
                                Haptics.tap()
                                store.eventsMode = .detections
                                store.eventsSelection = .event(event)
                                store.selectedTab = .events
                            } label: {
                                EventThumbCard(event: event, size: CGSize(width: 168, height: 100))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
        }
    }

    /// Note, then every response on one row when it fits (stacked if not).
    @ViewBuilder
    private func respondWide(_ alarm: V2.Alarm, busy: Bool) -> some View {
        if session.can(.acknowledgeAlarms) {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                Label("Respond", systemImage: "hand.raised.fill")
                    .font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
                TextField("Note for the log (optional)", text: $note, axis: .vertical)
                    .lineLimit(1...4)
                    .padding(10)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                    .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: SentinelTheme.Space.sm) {
                        respondButtons(alarm, busy: busy)
                    }
                    .frame(minWidth: 600)
                    // Narrow pane: Acknowledge on its own, then pairs.
                    VStack(spacing: SentinelTheme.Space.sm) {
                        stateButton("Acknowledge", .acknowledged, "checkmark.circle.fill", alarm, busy, prominent: true)
                        HStack(spacing: SentinelTheme.Space.sm) {
                            stateButton("Investigate", .investigating, "magnifyingglass", alarm, busy)
                            snoozeMenu(alarm, busy: busy)
                        }
                        HStack(spacing: SentinelTheme.Space.sm) {
                            stateButton("False alarm", .falseAlarm, "xmark.circle", alarm, busy)
                            stateButton("Resolve", .resolved, "checkmark.seal.fill", alarm, busy)
                        }
                    }
                }
                if alarm.isOpen == false {
                    Text("This alarm is closed. It isn't in the open queue any more.")
                        .font(.caption).foregroundStyle(.white.opacity(0.5))
                }
            }
            .padding(SentinelTheme.Space.md)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
        } else {
            Text("Your role can view alarms but not respond to them.")
                .font(.caption).foregroundStyle(.white.opacity(0.55))
        }
    }

    @ViewBuilder
    private func respondButtons(_ alarm: V2.Alarm, busy: Bool) -> some View {
        stateButton("Acknowledge", .acknowledged, "checkmark.circle.fill", alarm, busy, prominent: true)
        stateButton("Investigate", .investigating, "magnifyingglass", alarm, busy)
        snoozeMenu(alarm, busy: busy)
        stateButton("False alarm", .falseAlarm, "xmark.circle", alarm, busy)
        stateButton("Resolve", .resolved, "checkmark.seal.fill", alarm, busy)
    }

    private func snoozeMenu(_ alarm: V2.Alarm, busy: Bool) -> some View {
        Menu {
            ForEach([5, 15, 30, 60, 240], id: \.self) { minutes in
                Button(minutes < 60 ? "\(minutes) minutes" : "\(minutes / 60) hour\(minutes > 60 ? "s" : "")") {
                    send(alarm, .snoozed, snooze: minutes)
                }
            }
        } label: {
            Label("Snooze", systemImage: "moon.zzz.fill")
                .font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                .lineLimit(1)
                .frame(maxWidth: .infinity).frame(height: 40)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
        }
        .disabled(busy)
    }

    // MARK: Compact

    @ViewBuilder
    private func respond(_ alarm: V2.Alarm, busy: Bool) -> some View {
        if session.can(.acknowledgeAlarms) {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                Label("Respond", systemImage: "hand.raised.fill")
                    .font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
                TextField("Note for the response log (optional)", text: $note, axis: .vertical)
                    .lineLimit(1...4)
                    .padding(10)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                    .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                HStack(spacing: SentinelTheme.Space.sm) {
                    stateButton("Acknowledge", .acknowledged, "checkmark.circle.fill", alarm, busy, prominent: true)
                    stateButton("Investigate", .investigating, "magnifyingglass", alarm, busy)
                }
                HStack(spacing: SentinelTheme.Space.sm) {
                    Menu {
                        ForEach([5, 15, 30, 60, 240], id: \.self) { minutes in
                            Button(minutes < 60 ? "\(minutes) minutes" : "\(minutes / 60) hour\(minutes > 60 ? "s" : "")") {
                                send(alarm, .snoozed, snooze: minutes)
                            }
                        }
                    } label: {
                        Label("Snooze", systemImage: "moon.zzz.fill")
                            .font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                            .frame(maxWidth: .infinity).frame(height: 40)
                            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                    }
                    .disabled(busy)
                    stateButton("False alarm", .falseAlarm, "xmark.circle", alarm, busy)
                    stateButton("Resolve", .resolved, "checkmark.seal.fill", alarm, busy)
                }
                if alarm.isOpen == false {
                    Text("This alarm is closed. It isn't in the open queue any more.")
                        .font(.caption).foregroundStyle(.white.opacity(0.5))
                }
            }
        } else {
            Text("Your role can view alarms but not respond to them.")
                .font(.caption).foregroundStyle(.white.opacity(0.55))
        }
    }

    private func stateButton(_ title: String, _ state: V2.AlarmState, _ symbol: String, _ alarm: V2.Alarm,
                             _ busy: Bool, prominent: Bool = false) -> some View {
        AlarmActionButton(title: title, systemImage: symbol, prominent: prominent && alarm.state != state, isBusy: busy) {
            send(alarm, state)
        }
        .disabled(alarm.state == state)
        .opacity(alarm.state == state ? 0.5 : 1)
    }

    private func send(_ alarm: V2.Alarm, _ state: V2.AlarmState, snooze: Int? = nil) {
        let text = note
        Task {
            let updated = await store.setState(alarm, to: state, note: text, snoozeMinutes: snooze)
            if store.actionError == nil { note = "" }
            // The server's answer is the freshest copy, also for a closed alarm
            // that has left the open queue (and the only right one in Demo Mode).
            if let updated {
                fetched = updated
            } else if let api = session.api, session.isDemoMode == false, let again = try? await api.alarm(alarm.id) {
                fetched = again
            }
        }
    }

    @ViewBuilder
    private func evidenceSection(_ alarm: V2.Alarm, busy: Bool) -> some View {
        if session.can(.exportEvidence), alarm.cameraId != nil {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                Label("Evidence", systemImage: "lock.doc.fill")
                    .font(.caption.weight(.bold)).foregroundStyle(.white.opacity(0.6))
                if let evidence = store.evidence[alarm.id] {
                    row("Case", evidence.caseId)
                    row("Range", evidence.range)
                    row("Locked", "\(Date(unixMillis: evidence.lockedAt).formatted(date: .abbreviated, time: .shortened)) by \(evidence.lockedBy)")
                    HStack {
                        Text("SHA-256").font(.caption).foregroundStyle(.white.opacity(0.55))
                        Spacer()
                        Text(evidence.sha256).font(.caption.monospaced()).foregroundStyle(.white)
                            .lineLimit(1).truncationMode(.middle)
                    }
                    Label("Locked · safe from cleanup", systemImage: "lock.shield.fill")
                        .font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.recording)
                } else {
                    Text("Lock the recording behind this alarm so retention never deletes it. The Recording Server keeps the file and its SHA-256.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                    AlarmActionButton(title: "Lock as Evidence", systemImage: "lock.fill", isBusy: busy) {
                        Task { await store.lockEvidence(alarm) }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(SentinelTheme.Space.md)
            .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
        }
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).font(.subheadline).foregroundStyle(.white.opacity(0.55))
            Spacer()
            Text(value).font(.subheadline.weight(.medium)).foregroundStyle(.white)
                .multilineTextAlignment(.trailing)
        }
    }
}

