import AppKit
import SwiftUI
import SentinelCore

/// The navigation rail (wave 20), like the iPhone's unfolded sidebar: the
/// site and its connection on top, the fixed workspaces as icon-over-label
/// items, the Management panes the user may open, and Administration
/// pinned at the bottom. It replaces the tab bar's fixed tabs; saved-view
/// tabs stay inside Live.
struct OperatorRail: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    @EnvironmentObject private var navigator: OperatorNavigator

    let tabs: TabsState
    let onSelect: (FixedTab) -> Void
    /// Insights, Rules, People, Plates and Admin open in the main window.
    let onSelectPane: (RailPane) -> Void

    static let width: CGFloat = 84


    var body: some View {
        VStack(spacing: 0) {
            // The window's traffic lights sit over the rail's top.
            Color.clear.frame(height: 28)
            siteBadge
                .padding(.bottom, SentinelTheme.Space.sm)
            ScrollView(showsIndicators: false) {
                VStack(spacing: 1) {
                    ForEach(FixedTab.order, id: \.self) { tab in
                        if tab == .search { eventsItem }
                        item(title: tab.railLabel, symbol: tab.symbol, selected: isSelected(tab),
                             badge: tab == .alarms ? WorkspaceTabs.badgeText(alarms.open.count) : "",
                             help: "\(tab.label) (\(tab.keyLabel))") { onSelect(tab) }
                    }
                    let nav = ManagementNavModel(can: session.can)
                    let extras = RailPane.railOrder.filter { pane in pane.leaf.map { nav.node($0) != nil } ?? false }
                    if extras.isEmpty == false {
                        Rectangle().fill(SentinelTheme.line).frame(height: 1)
                            .padding(.horizontal, SentinelTheme.Space.md)
                            .padding(.vertical, SentinelTheme.Space.xs)
                        ForEach(extras, id: \.self) { pane in
                            item(title: pane.title, symbol: pane.symbol, selected: isSelected(pane), badge: "",
                                 help: pane.title) { onSelectPane(pane) }
                        }
                    }
                }
            }
            Spacer(minLength: SentinelTheme.Space.sm)
            item(title: RailPane.admin.title, symbol: RailPane.admin.symbol,
                 selected: isSelected(.admin) || OperatorSection.admin.contains(navigator.section), badge: "",
                 help: "Administration") { onSelectPane(.admin) }
                .padding(.bottom, SentinelTheme.Space.sm)
        }
        .padding(.horizontal, 6)
        .frame(width: Self.width)
        .frame(maxHeight: .infinity)
        .background(SentinelTheme.chrome)
    }

    private func isSelected(_ tab: FixedTab) -> Bool {
        switch navigator.section {
        case .workspace:
            if tab == .live, tabs.activeViewID != nil { return true }
            return tabs.activeFixed == tab
        case .alarms:
            return tab == .alarms
        default:
            return false
        }
    }

    private func isSelected(_ pane: RailPane) -> Bool {
        navigator.section == .workspace && tabs.activePane == pane
    }

    private var eventsItem: some View {
        item(title: "Events", symbol: "sparkles.rectangle.stack.fill", selected: navigator.section == .events, badge: "",
             help: "Events: detections and analytics (⌘4)") {
            navigator.section = navigator.section == .events ? .workspace : .events
        }
    }

    private var connected: Bool {
        if case .connected = runtime.streamState { return true }
        return false
    }

    private var siteBadge: some View {
        let name = session.site?.name ?? "Sentinel"
        return VStack(spacing: 6) {
            ZStack(alignment: .bottomTrailing) {
                RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous)
                    .fill(SentinelTheme.accent.opacity(0.18))
                    .frame(width: 40, height: 40)
                    .overlay(
                        Image(systemName: "shield.lefthalf.filled")
                            .font(.system(size: 18, weight: .semibold))
                            .foregroundStyle(SentinelTheme.accent)
                    )
                Circle()
                    .fill(connected ? SentinelTheme.recording : SentinelTheme.amber)
                    .frame(width: 10, height: 10)
                    .overlay(Circle().stroke(SentinelTheme.chrome, lineWidth: 2))
                    .offset(x: 3, y: 3)
            }
            Text(name)
                .font(.system(size: 11, weight: .semibold))
                .lineLimit(1)
                .minimumScaleFactor(0.8)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .help("\(name) · \(session.site?.address ?? "") · \(runtime.streamSummary.label)")
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(name), \(connected ? "online" : runtime.streamSummary.label)")
    }

    private func item(title: String, symbol: String, selected: Bool, badge: String, help: String,
                      action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(spacing: 3) {
                Image(systemName: symbol)
                    .font(.system(size: 16, weight: .semibold))
                    .frame(height: 20)
                    .overlay(alignment: .topTrailing) {
                        if badge.isEmpty == false {
                            Text(badge)
                                .font(.system(size: 10, weight: .heavy).monospacedDigit())
                                .foregroundStyle(.white)
                                .padding(.horizontal, 4)
                                .frame(minWidth: 17, minHeight: 17)
                                .background(SentinelTheme.alarm, in: Capsule())
                                .offset(x: 13, y: -7)
                        }
                    }
                Text(title)
                    .font(.system(size: 10.5, weight: .semibold))
                    .lineLimit(1)
                    .minimumScaleFactor(0.8)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 5)
        }
        .buttonStyle(RailItemStyle(selected: selected))
        .help(help)
        .accessibilityLabel(badge.isEmpty ? title : "\(title), \(badge)")
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

private struct RailItemStyle: ButtonStyle {
    let selected: Bool
    @State private var hovered = false

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .foregroundStyle(selected ? SentinelTheme.accent : Color.primary.opacity(hovered ? 0.85 : 0.6))
            .background(
                RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous)
                    .fill(selected ? SentinelTheme.accent.opacity(0.16)
                          : Color.primary.opacity(configuration.isPressed ? 0.10 : (hovered ? 0.05 : 0)))
            )
            .contentShape(Rectangle())
            .onHover { hovered = $0 }
    }
}
