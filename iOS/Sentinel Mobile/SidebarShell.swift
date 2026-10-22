// SidebarShell.swift
// Regular-width navigation (WAVE19): a persistent icon-and-label rail on the
// left, like the Mac Operator's, instead of the system TabView, which iOS 26+
// floats as a pill on top of the content when the iPhone Duo is unfolded.
// The rail sits beside the content, never over it. Selection is
// `AppStore.selectedTab`, shared with the compact TabView, so a fold keeps
// the screen the user was on.

import SwiftUI

struct SidebarShell: View {
    @ObservedObject private var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared

    var body: some View {
        HStack(spacing: 0) {
            SentinelSidebar()
            Rectangle()
                .fill(SentinelTheme.line)
                .frame(width: 1)
                .ignoresSafeArea()
            content
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .background(SentinelTheme.background.ignoresSafeArea())
    }

    @ViewBuilder
    private var content: some View {
        switch store.selectedTab {
        case .home:
            HomeView()
        case .live:
            LiveGridView()
        case .events:
            EventsView()
        case .people:
            NavigationStack { PeopleView() }
        case .insights:
            NavigationStack { InsightsView() }
        case .settings:
            SettingsView()
        }
    }
}

/// The rail itself: the site on top, the sections, Settings pinned below.
struct SentinelSidebar: View {
    @ObservedObject private var store = AppStore.shared
    @ObservedObject private var session = SentinelSession.shared

    /// People and Insights wait for /api/v2/me, as they did on Home; Demo
    /// Mode shows them so the layout is complete.
    private var showsPeopleAndInsights: Bool {
        session.isDemoMode || session.me?.can(.viewLive) == true
    }

    var body: some View {
        VStack(spacing: 0) {
            siteBadge
                .padding(.top, SentinelTheme.Space.md)
                .padding(.bottom, SentinelTheme.Space.lg)
            ScrollView(showsIndicators: false) {
                VStack(spacing: SentinelTheme.Space.xs) {
                    ForEach(LayoutLogic.sidebarTabs(showsPeopleAndInsights: showsPeopleAndInsights), id: \.self) { tab in
                        item(tab)
                    }
                }
            }
            Spacer(minLength: SentinelTheme.Space.sm)
            item(.settings)
                .padding(.bottom, SentinelTheme.Space.md)
        }
        .padding(.horizontal, 6)
        .frame(width: LayoutLogic.sidebarWidth)
        .frame(maxHeight: .infinity)
        .background(SentinelTheme.chrome.ignoresSafeArea())
    }

    private var siteName: String {
        if let name = session.serverName, name.isEmpty == false { return name }
        return "Sentinel"
    }

    private var siteOnline: Bool {
        session.isDemoMode || store.streamState.isConnected || (store.hasLoaded && store.lastError == nil)
    }

    private var siteBadge: some View {
        VStack(spacing: 6) {
            ZStack(alignment: .bottomTrailing) {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .fill(SentinelTheme.accent.opacity(0.18))
                    .frame(width: 40, height: 40)
                    .overlay(
                        Image(systemName: "shield.lefthalf.filled")
                            .font(.title3.weight(.semibold))
                            .foregroundStyle(SentinelTheme.accent)
                    )
                Circle()
                    .fill(siteOnline ? SentinelTheme.recording : SentinelTheme.amber)
                    .frame(width: 10, height: 10)
                    .overlay(Circle().stroke(SentinelTheme.chrome, lineWidth: 2))
                    .offset(x: 3, y: 3)
            }
            Text(siteName)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.white.opacity(0.85))
                .lineLimit(2)
                .minimumScaleFactor(0.8)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(siteName), \(siteOnline ? "online" : "connecting")")
    }

    private func item(_ tab: AppTab) -> some View {
        let selected = store.selectedTab == tab
        let badge = tab == .events ? store.newAlertCount : 0
        return Button {
            Haptics.tap()
            store.selectedTab = tab
        } label: {
            VStack(spacing: 4) {
                Image(systemName: Self.symbol(tab))
                    .font(.system(size: 19, weight: .semibold))
                    .frame(height: 24)
                    .overlay(alignment: .topTrailing) {
                        if badge > 0 {
                            Text(badge > 99 ? "99+" : "\(badge)")
                                .font(.caption2.weight(.heavy))
                                .foregroundStyle(.white)
                                .padding(.horizontal, 5)
                                .frame(minWidth: 18, minHeight: 18)
                                .background(SentinelTheme.alarm, in: Capsule())
                                .offset(x: 12, y: -8)
                        }
                    }
                Text(Self.title(tab))
                    .font(.caption2.weight(.semibold))
                    .lineLimit(1)
                    .minimumScaleFactor(0.8)
            }
            .foregroundStyle(selected ? SentinelTheme.accent : .white.opacity(0.6))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 9)
            .background(selected ? SentinelTheme.accent.opacity(0.16) : .clear,
                        in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(badge > 0 ? "\(Self.title(tab)), \(badge) new" : Self.title(tab))
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    static func title(_ tab: AppTab) -> String {
        switch tab {
        case .home: return "Home"
        case .live: return "Live"
        case .events: return "Events"
        case .people: return "People"
        case .insights: return "Insights"
        case .settings: return "Settings"
        }
    }

    static func symbol(_ tab: AppTab) -> String {
        switch tab {
        case .home: return "house.fill"
        case .live: return "video.fill"
        case .events: return "sparkles.rectangle.stack.fill"
        case .people: return "person.2.fill"
        case .insights: return "chart.bar.xaxis"
        case .settings: return "gearshape.fill"
        }
    }
}

/// A screen title in the sidebar's content area: big title, one line under
/// it, matching Events and Live.
struct PageHeader<Trailing: View>: View {
    let title: String
    var subtitle: String?
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                if let subtitle {
                    Text(subtitle)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.white.opacity(0.6))
                }
            }
            Spacer(minLength: 8)
            trailing()
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .padding(.top, 4)
        .padding(.bottom, SentinelTheme.Space.sm)
    }
}

extension PageHeader where Trailing == EmptyView {
    init(title: String, subtitle: String? = nil) {
        self.init(title: title, subtitle: subtitle) { EmptyView() }
    }
}
