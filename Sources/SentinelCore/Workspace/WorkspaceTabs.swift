import Foundation

/// The fixed workspaces of the Operator tab bar. Raw values match the web
/// client's ids (`workspaces.ts`) so both clients speak the same language.
public enum FixedTab: String, CaseIterable, Codable, Sendable {
    case live, map, search, exports, alarms, incidents, monitor

    public var label: String {
        switch self {
        case .live: return "Live"
        case .map: return "Map"
        case .search: return "Search"
        case .exports: return "Exports"
        case .alarms: return "Alarm Manager"
        case .incidents: return "Incidents"
        case .monitor: return "System Monitor"
        }
    }

    /// ⌘<key>. Keys 2 and 4 stay Playback and Events inside the Live tab.
    public var key: Int {
        switch self {
        case .live: return 1
        case .alarms: return 3
        case .search: return 5
        case .map: return 6
        case .exports: return 7
        case .incidents: return 8
        case .monitor: return 9
        }
    }

    /// Tab order, left to right.
    public static let order: [FixedTab] = [.live, .map, .search, .exports, .alarms, .incidents, .monitor]
}

/// What ⌘<digit> asks for.
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
        } else if FixedTab(rawValue: active) == nil {
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

    /// What ⌘<digit> does. 1 Live, 2 Playback and 4 Events (inside the Live
    /// tab) and 3/5 Alarm Manager/Search keep their old keys; the other
    /// fixed tabs are numbered onward. nil when nothing is bound.
    public static func shortcut(for key: String, available: Set<FixedTab> = Set(FixedTab.allCases)) -> TabShortcut? {
        guard key.count == 1, let n = Int(key), (1...9).contains(n) else { return nil }
        switch n {
        case 1: return TabShortcut(tab: FixedTab.live.rawValue, mode: .live)
        case 2: return TabShortcut(tab: FixedTab.live.rawValue, mode: .playback)
        case 4: return TabShortcut(tab: FixedTab.live.rawValue, events: true)
        default:
            guard let tab = FixedTab.allCases.first(where: { $0.key == n }), available.contains(tab) else { return nil }
            return TabShortcut(tab: tab.rawValue)
        }
    }

    /// Milliseconds until the next whole minute (the wall clock ticks then).
    public static func msToNextMinute(nowMs: Int64) -> Int64 {
        let rest = nowMs % 60_000
        return rest == 0 ? 60_000 : 60_000 - rest
    }
}
