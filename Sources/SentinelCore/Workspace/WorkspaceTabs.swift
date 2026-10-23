import Foundation

/// The fixed workspaces of the Operator navigation rail. Raw values match
/// the web client's ids (`workspaces.ts`) so both clients speak the same language.
public enum FixedTab: String, CaseIterable, Codable, Sendable {
    case home, live, map, search, exports, alarms, incidents, monitor, reports

    public var label: String {
        switch self {
        case .home: return "Home"
        case .live: return "Live"
        case .map: return "Map"
        case .search: return "Search"
        case .exports: return "Exports"
        case .alarms: return "Alarm Manager"
        case .incidents: return "Incidents"
        case .monitor: return "System Monitor"
        case .reports: return "Reports"
        }
    }

    /// ⌘<key>. Keys 2 and 4 stay Playback and Events inside the Live tab;
    /// Home (wave 20) takes the free 0. The digits are all taken, so Reports
    /// (parity 3.4.2) is ⌘R; the Mac has no Reload to collide with.
    public var key: String {
        switch self {
        case .home: return "0"
        case .live: return "1"
        case .alarms: return "3"
        case .search: return "5"
        case .map: return "6"
        case .exports: return "7"
        case .incidents: return "8"
        case .monitor: return "9"
        case .reports: return "r"
        }
    }

    /// The shortcut as a tooltip shows it: "⌘3", "⌘R".
    public var keyLabel: String { "⌘" + key.uppercased() }

    /// The rail's short label (icon over label, 84 pt wide).
    public var railLabel: String {
        switch self {
        case .alarms: return "Alarms"
        case .monitor: return "Monitor"
        default: return label
        }
    }

    public var symbol: String {
        switch self {
        case .home: return "house.fill"
        case .live: return "video.fill"
        case .map: return "map.fill"
        case .search: return "magnifyingglass"
        case .exports: return "square.and.arrow.up.fill"
        case .alarms: return "bell.badge.fill"
        case .incidents: return "folder.fill"
        case .monitor: return "waveform.path.ecg"
        case .reports: return "chart.bar.doc.horizontal"
        }
    }

    /// The page header's one line under the title, when the shell has
    /// nothing more specific (a count or a state) to say.
    public var subtitle: String {
        switch self {
        case .home: return "Your site at a glance"
        case .live: return "Live and recorded video"
        case .map: return "Cameras on the floor plans"
        case .search: return "Detections across every camera"
        case .exports: return "Clips, evidence and scheduled exports"
        case .alarms: return "Respond, investigate and lock evidence"
        case .incidents: return "Alarms grouped into cases"
        case .monitor: return "Servers, storage and cameras"
        case .reports: return "Detections and alarms over a range"
        }
    }

    /// Rail order, top to bottom.
    public static let order: [FixedTab] = [.home, .live, .map, .search, .exports, .alarms, .incidents, .monitor, .reports]
}

/// Management panes the rail opens inside the main window (wave 20 mac2),
/// to the right of the rail like any workspace. Insights, Rules, People and
/// Plates show one Management pane; Admin shows the Management tree in a
/// column next to the selected pane. Tab id: `pane:<raw value>`.
public enum RailPane: String, CaseIterable, Codable, Sendable {
    case insights, rules, people, plates, admin

    public var title: String {
        switch self {
        case .insights: return "Insights"
        case .rules: return "Rules"
        case .people: return "People"
        case .plates: return "Plates"
        case .admin: return "Admin"
        }
    }

    public var symbol: String {
        switch self {
        case .insights: return "chart.bar.xaxis"
        case .rules: return "checklist"
        case .people: return "person.2.fill"
        case .plates: return "car.rear.fill"
        case .admin: return "gearshape.fill"
        }
    }

    /// The page header's line under the title.
    public var subtitle: String {
        switch self {
        case .insights: return "Trends and recommendations"
        case .rules: return "When to raise alarms, and what to do"
        case .people: return "Who was recognized, and when"
        case .plates: return "Known plates and where they were read"
        case .admin: return "Servers, cameras, rules, people and system health"
        }
    }

    /// The Management leaf the pane shows; nil for Admin (the whole tree).
    public var leaf: String? {
        switch self {
        case .insights: return ManagementCatalog.Leaf.insights
        case .rules: return ManagementCatalog.Leaf.rules
        case .people: return ManagementCatalog.Leaf.people
        case .plates: return ManagementCatalog.Leaf.plates
        case .admin: return nil
        }
    }

    public var tabID: String { "pane:\(rawValue)" }

    public static func of(tab: String) -> RailPane? {
        tab.hasPrefix("pane:") ? RailPane(rawValue: String(tab.dropFirst(5))) : nil
    }

    /// The single-pane items, in rail order (Admin is pinned at the bottom).
    public static let railOrder: [RailPane] = [.insights, .rules, .people, .plates]

    /// Where a Management node opens in the main window: its own rail pane
    /// when it has one, otherwise Admin with that node selected in the tree.
    public static func destination(forNode id: String) -> (pane: RailPane, node: String?) {
        if let pane = railOrder.first(where: { $0.leaf == id }) { return (pane, nil) }
        return (.admin, id)
    }
}

/// What ⌘<digit> (or ⌘R) asks for.
public struct TabShortcut: Equatable, Sendable {
    public enum Mode: Equatable, Sendable { case live, playback }
    public var tab: String
    public var mode: Mode?
    /// Open the Events page (key 4).
    public var events: Bool

    public init(tab: String, mode: Mode? = nil, events: Bool = false) {
        self.tab = tab
        self.mode = mode
        self.events = events
    }
}

/// The tab model: which tab is active and which saved views are open as
/// tabs. A tab id is a `FixedTab` raw value or `view:<viewId>`. Pure.
public struct TabsState: Codable, Equatable, Sendable {
    public var active: String
    /// Open saved-view tabs, left to right (view ids).
    public var views: [String]

    public static let initial = TabsState(active: FixedTab.live.rawValue, views: [])

    /// Signing in lands on Home (wave 20); open view tabs are kept.
    public func afterSignIn() -> TabsState {
        TabsState(active: FixedTab.home.rawValue, views: views)
    }

    public init(active: String = FixedTab.live.rawValue, views: [String] = []) {
        self.active = active
        self.views = views
    }

    public static func viewTabID(_ viewID: String) -> String { "view:\(viewID)" }

    public static func viewID(of tab: String) -> String? {
        tab.hasPrefix("view:") ? String(tab.dropFirst(5)) : nil
    }

    public var activeViewID: String? { Self.viewID(of: active) }
    public var activeFixed: FixedTab? { FixedTab(rawValue: active) }
    /// The Management pane shown as a workspace (Insights, Rules, …, Admin).
    public var activePane: RailPane? { RailPane.of(tab: active) }

    public mutating func activate(_ tab: String) {
        if active != tab { active = tab }
    }

    /// Opens a saved view in its own tab (or switches to it when open).
    public mutating func openView(_ viewID: String) {
        if views.contains(viewID) == false { views.append(viewID) }
        active = Self.viewTabID(viewID)
    }

    /// Closes a view tab; when it was active, the tab to its left (or the
    /// first remaining one, or Live) takes over.
    public mutating func closeView(_ viewID: String) {
        guard let i = views.firstIndex(of: viewID) else { return }
        views.remove(at: i)
        guard active == Self.viewTabID(viewID) else { return }
        if i > 0 {
            active = Self.viewTabID(views[i - 1])
        } else if let first = views.first {
            active = Self.viewTabID(first)
        } else {
            active = FixedTab.live.rawValue
        }
    }

    /// Drops view tabs whose view is gone, and an active tab that no longer exists.
    public mutating func reconcile(viewExists: (String) -> Bool) {
        views = views.filter(viewExists)
        if let id = activeViewID {
            if views.contains(id) == false { active = FixedTab.live.rawValue }
        } else if FixedTab(rawValue: active) == nil, RailPane.of(tab: active) == nil {
            active = FixedTab.live.rawValue
        }
    }

    // MARK: Persistence (per site, in UserDefaults)

    public static func storageKey(siteID: String) -> String { "sentinel.tabs.\(siteID)" }

    /// Reads stored state leniently: junk, duplicates and empty ids are dropped.
    public static func parse(_ data: Data?) -> TabsState {
        guard let data, let raw = try? JSONDecoder().decode(TabsState.self, from: data) else { return .initial }
        var seen = Set<String>()
        let views = raw.views.filter { $0.isEmpty == false && seen.insert($0).inserted }
        return TabsState(active: raw.active.isEmpty ? FixedTab.live.rawValue : raw.active, views: views)
    }

    public static func load(siteID: String, defaults: UserDefaults = .standard) -> TabsState {
        parse(defaults.data(forKey: storageKey(siteID: siteID)))
    }

    public func save(siteID: String, defaults: UserDefaults = .standard) {
        if let data = try? JSONEncoder().encode(self) { defaults.set(data, forKey: Self.storageKey(siteID: siteID)) }
    }
}

public enum WorkspaceTabs {
    /// The alarm-count badge: nothing at zero, "9+" past nine.
    public static func badgeText(_ count: Int) -> String {
        count <= 0 ? "" : (count > 9 ? "9+" : String(count))
    }

    /// What ⌘<key> does. 0 Home, 1 Live, 2 Playback and 4 Events (inside
    /// the Live tab) and 3/5 Alarm Manager/Search keep their old keys; the
    /// other fixed tabs are numbered onward, and R is Reports. nil when
    /// nothing is bound.
    public static func shortcut(for key: String, available: Set<FixedTab> = Set(FixedTab.allCases)) -> TabShortcut? {
        guard key.count == 1 else { return nil }
        let k = key.lowercased()
        switch k {
        case "1": return TabShortcut(tab: FixedTab.live.rawValue, mode: .live)
        case "2": return TabShortcut(tab: FixedTab.live.rawValue, mode: .playback)
        case "4": return TabShortcut(tab: FixedTab.live.rawValue, events: true)
        default:
            guard let tab = FixedTab.allCases.first(where: { $0.key == k }), available.contains(tab) else { return nil }
            return TabShortcut(tab: tab.rawValue)
        }
    }

    /// The views-and-cameras sidebar belongs to Live and saved-view tabs
    /// only; every other workspace and page uses the full width next to the
    /// rail. `onWorkspace` is false on the shell's pages (Events, the Alarm
    /// Queue, Administration pages).
    public static func hasCameraSidebar(_ tabs: TabsState, onWorkspace: Bool) -> Bool {
        onWorkspace && (tabs.activeFixed == .live || tabs.activeViewID != nil)
    }

    /// Whether the sidebar is on screen: where it belongs and not hidden by
    /// the operator's toggle (⌃⌘S), which only shows where it belongs.
    public static func showsCameraSidebar(_ tabs: TabsState, onWorkspace: Bool, userVisible: Bool) -> Bool {
        userVisible && hasCameraSidebar(tabs, onWorkspace: onWorkspace)
    }

    /// Milliseconds until the next whole minute (the wall clock ticks then).
    public static func msToNextMinute(nowMs: Int64) -> Int64 {
        let rest = nowMs % 60_000
        return rest == 0 ? 60_000 : 60_000 - rest
    }
}
