import SwiftUI
import SentinelCore

// MARK: - UI copy for each detection capability

/// User-facing descriptions for the AI hub. Lives in the app layer (not the
/// core model) because it is purely presentation copy.
extension AIDetectionKind {
    /// One-line plain-language summary of what this detector does.
    var functionDescription: String {
        switch self {
        case .person:
            return "Spots people in frame with on-device Vision ML and draws live tracking boxes. Powers person alerts and recording triggers."
        case .face:
            return "Flags when a face is clearly visible — useful at entryways for confirming who approached the camera."
        case .licensePlate:
            return "Reads license-plate characters (4–8 alphanumeric) from vehicles passing the camera."
        case .vehicle:
            return "Recognizes cars, trucks, vans, and other vehicles entering the scene."
        case .animal:
            return "Detects pets and wildlife, which helps cut down on false person alerts."
        case .loitering:
            return "Raises an alert when a person lingers in view longer than 45 seconds."
        }
    }

    /// Short, one-word descriptor used as the sidebar row subtitle.
    var laneLabel: String { "On-device" }

    /// The detector's overlay color as a SwiftUI `Color`.
    var swiftUIColor: Color {
        let (r, g, b) = overlayColor
        return Color(red: r, green: g, blue: b)
    }
}

// MARK: - AI capability focus (AI-local sub-navigation)

/// Which AI capability the AI workspace is currently showing. This stays a
/// local concept (owned by `SentinelCommandCenter`) instead of becoming a set
/// of `SentinelSection` cases, so it never leaks into the global router,
/// Cmd+K, or the sidebar-badge machinery.
enum AICapabilityFocus: Hashable {
    case overview
    case detector(AIDetectionKind)
    case sceneDescriptions
    case threat
    case search
}

/// One pass over the detection log → all the counts the AI surfaces need.
/// Computed once per render rather than re-filtering the dictionary per row.
struct AIEventCounts {
    var byKind: [AIDetectionKind: Int] = [:]
    var described = 0
    var notable = 0
    var total = 0

    init(_ detections: [UUID: [AIDetectionEvent]]) {
        for events in detections.values {
            for event in events {
                byKind[event.kind, default: 0] += 1
                total += 1
                if event.sceneDescription != nil { described += 1 }
                if event.threat.isNotable { notable += 1 }
            }
        }
    }
}

// MARK: - AI Center (detail pane)

/// The AI workspace detail pane. Switches on `commandCenter.aiFocus`, which the
/// AI capability sidebar drives. Replaces the old single scrolling settings page
/// with a dashboard-first console.
struct AICenterView: View {
    @EnvironmentObject private var commandCenter: SentinelCommandCenter

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                switch commandCenter.aiFocus {
                case .overview:
                    AIOverviewDashboard()
                case .detector(let kind):
                    AIDetectorFocusView(kind: kind)
                case .sceneDescriptions:
                    AISceneDescriptionsFocusView()
                case .threat:
                    AIThreatFocusView()
                case .search:
                    AISearchFocusView()
                }
            }
            .padding(16)
            .frame(maxWidth: 920, alignment: .leading)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

// MARK: - Overview dashboard

private struct AIOverviewDashboard: View {
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var licenseStore: LicenseStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter

    private let threeCol = [GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10)]
    private let twoCol = [GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10)]

    var body: some View {
        let counts = AIEventCounts(aiDetectionStore.detections)

        Text("Every AI feature in one place. On-device detectors run free and locally; cloud AI adds Claude-powered descriptions, threat reads, and search.")
            .font(.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)

        LazyVGrid(columns: threeCol, spacing: 10) {
            MetricCard(title: "Active Cameras",
                       value: "\(aiDetectionStore.activeCameraIDs.count)",
                       detail: "running detectors",
                       tint: SentinelTheme.accent)
            MetricCard(title: "Detection Events",
                       value: "\(counts.total)",
                       detail: "\(counts.notable) notable",
                       tint: SentinelTheme.amber)
            MetricCard(title: "Cloud AI",
                       value: licenseStore.aiActive ? "Active" : "Off",
                       detail: licenseStore.aiActive ? "Claude · your key" : "add API key",
                       tint: licenseStore.aiActive ? SentinelTheme.recording : .secondary)
        }

        SentinelPanel("On-Device Detection", systemImage: "cpu") {
            VStack(alignment: .leading, spacing: 12) {
                Text("Runs free and entirely on this Mac using Apple's Vision framework — no video leaves the machine. Pick a detector to toggle it and see recent activity.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                LazyVGrid(columns: twoCol, spacing: 10) {
                    ForEach(AIDetectionKind.allCases) { kind in
                        AICapabilityTile(kind: kind, count: counts.byKind[kind] ?? 0) {
                            commandCenter.aiFocus = .detector(kind)
                        }
                    }
                }
            }
        }

        SentinelPanel("Cloud AI — Claude", systemImage: "sparkles") {
            VStack(alignment: .leading, spacing: 0) {
                CloudNavRow(systemImage: "text.below.photo.fill",
                            tint: SentinelTheme.accent,
                            title: "Scene Descriptions",
                            subtitle: licenseStore.aiActive
                                ? "Ring-style sentence for each person alert"
                                : "Add your Anthropic API key to enable",
                            badge: counts.described) {
                    commandCenter.aiFocus = .sceneDescriptions
                }
                Divider().overlay(SentinelTheme.line)
                CloudNavRow(systemImage: "exclamationmark.shield.fill",
                            tint: .orange,
                            title: "Threat Classification",
                            subtitle: "\(counts.notable) notable event\(counts.notable == 1 ? "" : "s") flagged",
                            badge: counts.notable) {
                    commandCenter.aiFocus = .threat
                }
                Divider().overlay(SentinelTheme.line)
                CloudNavRow(systemImage: "text.magnifyingglass",
                            tint: SentinelTheme.accent,
                            title: "AI Search",
                            subtitle: "Search every event in plain English",
                            badge: nil) {
                    commandCenter.aiFocus = .search
                }
            }
        }
    }
}

// MARK: - Detector focus

private struct AIDetectorFocusView: View {
    let kind: AIDetectionKind
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var cameraStore: CameraStore
    @AppStorage private var enabled: Bool

    private let twoCol = [GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10)]

    init(kind: AIDetectionKind) {
        self.kind = kind
        _enabled = AppStorage(wrappedValue: true, kind.defaultsKey)
    }

    private var events: [AIDetectionEvent] {
        aiDetectionStore.detections.values.flatMap { $0 }
            .filter { $0.kind == kind }
            .sorted { $0.timestamp > $1.timestamp }
    }

    var body: some View {
        let events = self.events

        // Header: icon + name + live toggle
        HStack(alignment: .center, spacing: 12) {
            Image(systemName: kind.symbol)
                .font(.system(size: 22, weight: .semibold))
                .foregroundStyle(kind.swiftUIColor)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text(kind.rawValue)
                    .font(.title3.weight(.semibold))
                Text(enabled ? "On — watching" : "Off")
                    .font(.caption.weight(.medium))
                    .foregroundStyle(enabled ? SentinelTheme.recording : .secondary)
            }
            Spacer()
            Toggle("", isOn: $enabled)
                .toggleStyle(.switch)
                .labelsHidden()
        }
        .padding(.vertical, 2)

        LazyVGrid(columns: twoCol, spacing: 10) {
            MetricCard(title: "Events",
                       value: "\(events.count)",
                       detail: "this session",
                       tint: kind.swiftUIColor)
            MetricCard(title: "Last Seen",
                       value: events.first?.timeLabel ?? "—",
                       detail: events.first.map { cameraName(for: $0) } ?? "no events yet",
                       tint: SentinelTheme.accent)
        }

        SentinelPanel("What it does", systemImage: kind.symbol) {
            Text(kind.functionDescription)
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }

        SentinelPanel("Recent Activity", systemImage: "clock") {
            if events.isEmpty {
                EmptyStateLine(text: enabled
                    ? "No \(kind.rawValue) events yet — this detector is on and watching."
                    : "\(kind.rawValue) detection is off. Turn it on above to start logging events.")
            } else {
                AIEventList(events: Array(events.prefix(20)), cameraName: cameraName(for:))
            }
        }
    }

    private func cameraName(for event: AIDetectionEvent) -> String {
        cameraStore.cameras.first { $0.id == event.cameraID }?.name ?? "Camera"
    }
}

// MARK: - Scene Descriptions focus

private struct AISceneDescriptionsFocusView: View {
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var licenseStore: LicenseStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var cameraStore: CameraStore

    // NOTE: this @AppStorage + its onChange→describeEnabled sync are the cloud
    // describe path's source of truth — they must stay together.
    @AppStorage(AIDescription.defaultsKey) private var aiDescriptionsEnabled = true

    private var sceneStatusText: String {
        if licenseStore.aiActive {
            return "AI is on — Claude (your Anthropic key) describes each person alert. You pay Anthropic directly."
        }
        if SentinelAISettings.hasAPIKey {
            return "Your Anthropic key is saved. Turn on “Enable AI features” to start describing alerts."
        }
        return "Add your own Anthropic API key to enable Claude scene descriptions — you pay Anthropic directly, typically pennies per alert."
    }

    private var described: [AIDetectionEvent] {
        aiDetectionStore.detections.values.flatMap { $0 }
            .filter { $0.sceneDescription != nil }
            .sorted { $0.timestamp > $1.timestamp }
    }

    var body: some View {
        SentinelPanel("AI Scene Descriptions", systemImage: "text.below.photo.fill") {
            VStack(alignment: .leading, spacing: 8) {
                Toggle(isOn: $aiDescriptionsEnabled) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("AI Scene Descriptions")
                            .font(.callout.weight(.semibold))
                        Text("Claude vision writes a Ring-style sentence for each person alert — clothing, carried items, and what they're doing.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .toggleStyle(.checkbox)
                .onChange(of: aiDescriptionsEnabled) { newValue in
                    aiDetectionStore.describeEnabled = newValue
                }

                Text(sceneStatusText)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                Button {
                    commandCenter.requestUpgrade()
                } label: {
                    Label(licenseStore.aiActive ? "AI Key Settings" : "Set Up AI — Bring Your Own Key",
                          systemImage: licenseStore.aiActive ? "key.fill" : "sparkles")
                }
                .controlSize(.small)
                .padding(.top, 2)
            }
        }

        SentinelPanel("Recently Described", systemImage: "clock") {
            if described.isEmpty {
                EmptyStateLine(text: "No described events yet. When a person alert fires with descriptions on, Claude's summary appears here.")
            } else {
                AIEventList(events: Array(described.prefix(20)),
                            cameraName: cameraName(for:),
                            showsDescription: true)
            }
        }
    }

    private func cameraName(for event: AIDetectionEvent) -> String {
        cameraStore.cameras.first { $0.id == event.cameraID }?.name ?? "Camera"
    }
}

// MARK: - Threat Classification focus

private struct AIThreatFocusView: View {
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var cameraStore: CameraStore

    private var notable: [AIDetectionEvent] {
        aiDetectionStore.detections.values.flatMap { $0 }
            .filter { $0.threat.isNotable }
            .sorted { $0.timestamp > $1.timestamp }
    }

    var body: some View {
        SentinelPanel("Threat Classification", systemImage: "exclamationmark.shield.fill") {
            AICapabilityRow(
                systemImage: "exclamationmark.shield.fill",
                tint: .orange,
                title: "Threat Classification",
                description: "Claude rates each described event none / low / elevated / high and flags anomalies — coloring the timeline and prioritizing which alerts get pushed to your phone.",
                note: "Automatic whenever scene descriptions are on."
            )
        }

        SentinelPanel("Notable Events", systemImage: "flag.fill") {
            if notable.isEmpty {
                EmptyStateLine(text: "Nothing flagged as unusual or suspicious. Elevated and high-threat events will collect here.")
            } else {
                AIEventList(events: Array(notable.prefix(20)),
                            cameraName: cameraName(for:),
                            showsDescription: true)
            }
        }
    }

    private func cameraName(for event: AIDetectionEvent) -> String {
        cameraStore.cameras.first { $0.id == event.cameraID }?.name ?? "Camera"
    }
}

// MARK: - AI Search focus

private struct AISearchFocusView: View {
    @EnvironmentObject private var commandCenter: SentinelCommandCenter

    private let examples = [
        "person in a red jacket last night",
        "any vehicles after midnight",
        "delivery on the porch this week",
        "someone loitering near the entrance",
    ]

    var body: some View {
        SentinelPanel("AI Search", systemImage: "text.magnifyingglass") {
            VStack(alignment: .leading, spacing: 10) {
                AICapabilityRow(
                    systemImage: "text.magnifyingglass",
                    tint: SentinelTheme.accent,
                    title: "AI Search",
                    description: "Ask in plain English — \"person in a red jacket last night\" — and Claude searches across every detection event, description, and tag.",
                    note: nil
                )
                Button {
                    commandCenter.requestAISearch()
                } label: {
                    Label("Open AI Search", systemImage: "magnifyingglass")
                }
                .controlSize(.small)
            }
        }

        SentinelPanel("Try asking", systemImage: "lightbulb") {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(examples, id: \.self) { example in
                    HStack(spacing: 8) {
                        Image(systemName: "quote.opening")
                            .font(.caption2)
                            .foregroundStyle(SentinelTheme.accent)
                        Text(example)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
    }
}

// MARK: - AI capability sidebar (rendered in the workspace panel for .ai)

/// The contextual list shown beside the rail when the AI workspace is active.
/// Mirrors `SidebarRow`'s look (10/7 padding, 7pt-radius accent highlight) but
/// adds a status dot + event count so the column feels alive instead of empty.
struct AICapabilitySidebar: View {
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    @EnvironmentObject private var aiDetectionStore: AIDetectionStore
    @EnvironmentObject private var licenseStore: LicenseStore

    @AppStorage(AIDescription.defaultsKey) private var aiDescriptionsEnabled = true

    private var cloudDotState: AICapabilityDotState {
        if !licenseStore.aiActive { return .locked }
        return aiDescriptionsEnabled ? .on : .off
    }

    var body: some View {
        let counts = AIEventCounts(aiDetectionStore.detections)

        ScrollView(.vertical, showsIndicators: false) {
            VStack(alignment: .leading, spacing: 2) {
                AICapabilityRowView(
                    systemImage: "sparkles.rectangle.stack.fill",
                    title: "Overview",
                    subtitle: nil,
                    count: nil,
                    isSelected: commandCenter.aiFocus == .overview
                ) { EmptyView() } action: {
                    commandCenter.aiFocus = .overview
                }

                groupHeader("ON-DEVICE")
                ForEach(AIDetectionKind.allCases) { kind in
                    AICapabilityRowView(
                        systemImage: kind.symbol,
                        title: kind.rawValue,
                        subtitle: kind.laneLabel,
                        count: counts.byKind[kind] ?? 0,
                        isSelected: commandCenter.aiFocus == .detector(kind)
                    ) {
                        DetectorStatusDot(kind: kind)
                    } action: {
                        commandCenter.aiFocus = .detector(kind)
                    }
                }

                groupHeader("CLOUD · CLAUDE")
                AICapabilityRowView(
                    systemImage: "text.below.photo.fill",
                    title: "Scene Descriptions",
                    subtitle: licenseStore.aiActive ? "On · your key" : "API key needed",
                    count: counts.described,
                    isSelected: commandCenter.aiFocus == .sceneDescriptions
                ) {
                    AICapabilityStatusDot(state: cloudDotState)
                } action: {
                    commandCenter.aiFocus = .sceneDescriptions
                }
                AICapabilityRowView(
                    systemImage: "exclamationmark.shield.fill",
                    title: "Threat Classification",
                    subtitle: "Notable events",
                    count: counts.notable,
                    isSelected: commandCenter.aiFocus == .threat
                ) {
                    AICapabilityStatusDot(state: cloudDotState)
                } action: {
                    commandCenter.aiFocus = .threat
                }
                AICapabilityRowView(
                    systemImage: "text.magnifyingglass",
                    title: "AI Search",
                    subtitle: "Opens Search",
                    count: nil,
                    isSelected: commandCenter.aiFocus == .search
                ) { EmptyView() } action: {
                    commandCenter.aiFocus = .search
                }
            }
            .padding(.horizontal, 8)
            .padding(.bottom, 12)
        }
    }

    private func groupHeader(_ text: String) -> some View {
        Text(text)
            .font(.system(size: 9, weight: .bold))
            .kerning(0.8)
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10)
            .padding(.top, 12)
            .padding(.bottom, 2)
    }
}

// MARK: - Shared sidebar / dashboard pieces

enum AICapabilityDotState {
    case on, locked, off

    var color: Color {
        switch self {
        case .on: return SentinelTheme.recording
        case .locked: return SentinelTheme.amber
        case .off: return Color.secondary.opacity(0.5)
        }
    }
}

struct AICapabilityStatusDot: View {
    let state: AICapabilityDotState
    var body: some View {
        Circle()
            .fill(state.color)
            .frame(width: 7, height: 7)
    }
}

/// Status dot wired to a detector's on/off `@AppStorage` flag so it updates
/// live as the detector is toggled anywhere in the app.
private struct DetectorStatusDot: View {
    @AppStorage private var enabled: Bool
    init(kind: AIDetectionKind) {
        _enabled = AppStorage(wrappedValue: true, kind.defaultsKey)
    }
    var body: some View {
        AICapabilityStatusDot(state: enabled ? .on : .off)
    }
}

/// A sidebar row for the AI capability list: icon + title + subtitle, a trailing
/// status dot, and an optional event-count capsule.
private struct AICapabilityRowView<Trailing: View>: View {
    let systemImage: String
    let title: String
    let subtitle: String?
    let count: Int?
    let isSelected: Bool
    @ViewBuilder let trailing: Trailing
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Image(systemName: systemImage)
                    .frame(width: 18, alignment: .center)
                    .foregroundStyle(isSelected ? SentinelTheme.accent : .primary)
                VStack(alignment: .leading, spacing: 1) {
                    Text(title)
                        .font(.callout)
                        .foregroundStyle(isSelected ? SentinelTheme.accent : .primary)
                        .lineLimit(1)
                    if let subtitle {
                        Text(subtitle)
                            .font(.system(size: 9))
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                Spacer(minLength: 4)
                trailing
                if let count, count > 0 {
                    Text("\(count)")
                        .font(.caption2.weight(.bold).monospacedDigit())
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(.primary.opacity(0.08), in: Capsule())
                        .minimumScaleFactor(0.8)
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                isSelected ? SentinelTheme.accent.opacity(0.15) : .clear,
                in: RoundedRectangle(cornerRadius: 7)
            )
            .contentShape(RoundedRectangle(cornerRadius: 7))
        }
        .buttonStyle(.plain)
    }
}

/// A tappable detector tile for the Overview dashboard grid.
private struct AICapabilityTile: View {
    let kind: AIDetectionKind
    let count: Int
    let action: () -> Void
    @AppStorage private var enabled: Bool

    init(kind: AIDetectionKind, count: Int, action: @escaping () -> Void) {
        self.kind = kind
        self.count = count
        self.action = action
        _enabled = AppStorage(wrappedValue: true, kind.defaultsKey)
    }

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Image(systemName: kind.symbol)
                        .foregroundStyle(kind.swiftUIColor)
                    Spacer()
                    AICapabilityStatusDot(state: enabled ? .on : .off)
                }
                Text(kind.rawValue)
                    .font(.callout.weight(.semibold))
                    .lineLimit(1)
                Text(enabled ? "\(count) event\(count == 1 ? "" : "s")" : "Off")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
            .overlay {
                RoundedRectangle(cornerRadius: 8).stroke(SentinelTheme.line, lineWidth: 1)
            }
            .contentShape(RoundedRectangle(cornerRadius: 8))
        }
        .buttonStyle(.plain)
    }
}

/// A tappable row inside the Overview's Cloud AI panel (drills into a focus).
private struct CloudNavRow: View {
    let systemImage: String
    let tint: Color
    let title: String
    let subtitle: String
    let badge: Int?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: systemImage)
                    .foregroundStyle(tint)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.callout.weight(.semibold))
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer(minLength: 4)
                if let badge, badge > 0 {
                    Text("\(badge)")
                        .font(.caption2.weight(.bold).monospacedDigit())
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(.primary.opacity(0.08), in: Capsule())
                }
                Image(systemName: "chevron.right")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
            .padding(.vertical, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

/// A list of detection events (time + camera + label + optional threat badge and
/// Claude description), with hairline separators.
private struct AIEventList: View {
    let events: [AIDetectionEvent]
    let cameraName: (AIDetectionEvent) -> String
    var showsDescription = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(events.enumerated()), id: \.element.id) { index, event in
                if index > 0 {
                    Divider().overlay(SentinelTheme.line)
                }
                AIEventRow(event: event, cameraName: cameraName(event), showsDescription: showsDescription)
                    .padding(.vertical, 6)
            }
        }
    }
}

private struct AIEventRow: View {
    let event: AIDetectionEvent
    let cameraName: String
    var showsDescription = false

    private var primaryLabel: String {
        if let text = event.detectedText, !text.isEmpty {
            return text
        }
        return "\(event.kind.rawValue) · \(event.confidenceLabel)"
    }

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(event.timeLabel)
                    .font(.caption.weight(.semibold).monospacedDigit())
                Text(cameraName)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            .frame(width: 92, alignment: .leading)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Image(systemName: event.kind.symbol)
                        .font(.caption2)
                        .foregroundStyle(event.kind.swiftUIColor)
                    Text(primaryLabel)
                        .font(.caption.weight(.medium))
                        .lineLimit(1)
                    if event.threat.isNotable {
                        ThreatBadge(threat: event.threat, reason: event.threatReason)
                    }
                }
                if showsDescription, let description = event.sceneDescription, !description.isEmpty {
                    Text(description)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: 0)
        }
    }
}

// MARK: - Shared capability description row (reused by Threat / Search focus)

/// A non-interactive capability description row (icon + title + blurb).
private struct AICapabilityRow: View {
    let systemImage: String
    let tint: Color
    let title: String
    let description: String
    let note: String?

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: systemImage)
                .foregroundStyle(tint)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.callout.weight(.semibold))
                Text(description)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if let note {
                    Text(note)
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }
            Spacer(minLength: 0)
        }
    }
}
