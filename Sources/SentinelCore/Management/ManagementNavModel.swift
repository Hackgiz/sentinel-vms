import Foundation

// The Management window's site-navigation tree (wave 6 stream E). The node
// catalog mirrors stream A's `navModel.ts`; ids are the web hash paths
// ("security/roles"). Pure value logic, so the shell is testable without UI.

/// One node of the tree: a category (has children) or a leaf page.
public struct ManagementNode: Hashable, Identifiable, Sendable {
    public let id: String
    public var title: String
    public var symbol: String
    /// The permission that reveals the node; nil = any signed-in manager.
    /// A category is visible when at least one of its leaves is.
    public var permission: V2.Permission?
    public var children: [ManagementNode]

    public var isLeaf: Bool { children.isEmpty }

    public init(id: String, title: String, symbol: String, permission: V2.Permission? = nil, children: [ManagementNode] = []) {
        self.id = id
        self.title = title
        self.symbol = symbol
        self.permission = permission
        self.children = children
    }
}

/// A flattened, indented row of the visible tree.
public struct ManagementRow: Hashable, Identifiable, Sendable {
    public var node: ManagementNode
    public var depth: Int
    public var isExpanded: Bool
    public var id: String { node.id }
}

public enum ManagementCatalog {
    /// The selection id of the site root (welcome pane).
    public static let rootID = ""
    public static let fallbackSiteName = "Sentinel"

    /// Leaf ids, used by the shell, registry and tests.
    public enum Leaf {
        public static let overview = "basics/overview"
        public static let siteInfo = "basics/site-info"
        public static let cloud = "basics/cloud" // WAVE12(E): Sentinel Cloud
        public static let insights = "basics/insights" // wave 15
        public static let recordingServers = "servers/recording-servers"
        public static let cameras = "devices/cameras"
        public static let cameraGroups = "devices/camera-groups"
        public static let zones = "devices/zones" // wave 11
        public static let floorplans = "client/floorplans"
        public static let rules = "rules/rules"
        public static let timeProfiles = "rules/time-profiles"
        public static let webhooks = "rules/webhooks"
        public static let notifications = "rules/notifications"
        public static let roles = "security/roles"
        public static let users = "security/users"
        public static let people = "security/people" // wave 17
        public static let plates = "security/plates" // wave 17
        public static let directory = "security/directory"
        public static let systemMonitor = "system/system-monitor"
        public static let detection = "system/detection" // wave 17
        public static let recording = "system/recording" // wave 22
        public static let monitorThresholds = "system/monitor-thresholds"
        public static let currentTasks = "system/current-tasks"
        public static let evidenceLocks = "system/evidence-locks"
        public static let configReport = "system/config-report"
        public static let serverLogs = "logs/server-logs"
        public static let audit = "logs/audit"
    }

    /// The full tree (ungated). Gating generalizes today's role rules: a
    /// Supervisor (viewAudit) sees System Dashboard (monitor, tasks) and Logs.
    public static let categories: [ManagementNode] = [
        ManagementNode(id: "basics", title: "Basics", symbol: "info.circle", children: [
            .init(id: Leaf.overview, title: "Overview", symbol: "gauge.with.dots.needle.33percent", permission: .changeSettings),
            .init(id: Leaf.insights, title: "Insights", symbol: "chart.bar.xaxis", permission: .viewLive), // wave 15
            .init(id: Leaf.siteInfo, title: "Site Information", symbol: "building.2", permission: .changeSettings),
            .init(id: Leaf.cloud, title: "Sentinel Cloud", symbol: "cloud", permission: .changeSettings) // WAVE12(E): Sentinel Cloud
        ]),
        ManagementNode(id: "servers", title: "Servers", symbol: "server.rack", children: [
            .init(id: Leaf.recordingServers, title: "Recording Servers", symbol: "server.rack", permission: .changeSettings)
        ]),
        ManagementNode(id: "devices", title: "Devices", symbol: "camera", children: [
            .init(id: Leaf.cameras, title: "Cameras", symbol: "camera.fill", permission: .manageCameras),
            .init(id: Leaf.cameraGroups, title: "Camera Groups", symbol: "square.stack.3d.up", permission: .manageCameras),
            .init(id: Leaf.zones, title: "Zones & Privacy", symbol: "skew", permission: .manageCameras) // wave 11
        ]),
        ManagementNode(id: "client", title: "Client", symbol: "macwindow", children: [
            .init(id: Leaf.floorplans, title: "Floorplans", symbol: "map", permission: .changeSettings)
        ]),
        ManagementNode(id: "rules", title: "Rules and Events", symbol: "checklist", children: [
            .init(id: Leaf.rules, title: "Rules", symbol: "checklist", permission: .changeSettings),
            .init(id: Leaf.timeProfiles, title: "Time Profiles", symbol: "calendar.badge.clock", permission: .changeSettings),
            .init(id: Leaf.webhooks, title: "Webhooks", symbol: "arrow.up.right.square", permission: .changeSettings),
            .init(id: Leaf.notifications, title: "Notifications", symbol: "bell", permission: .changeSettings)
        ]),
        ManagementNode(id: "security", title: "Security", symbol: "lock.shield", children: [
            .init(id: Leaf.roles, title: "Roles", symbol: "person.badge.key", permission: .manageUsers),
            .init(id: Leaf.users, title: "Users", symbol: "person.2.fill", permission: .manageUsers),
            // Wave 17: reads need viewLive, but these panes are for managing the lists.
            .init(id: Leaf.people, title: "People", symbol: "person.crop.square", permission: .changeSettings),
            .init(id: Leaf.plates, title: "Plates", symbol: "car.rear", permission: .changeSettings),
            .init(id: Leaf.directory, title: "Directory", symbol: "person.crop.rectangle.stack", permission: .manageUsers)
        ]),
        ManagementNode(id: "system", title: "System Dashboard", symbol: "waveform.path.ecg", children: [
            .init(id: Leaf.systemMonitor, title: "System Monitor", symbol: "waveform.path.ecg", permission: .viewAudit),
            .init(id: Leaf.detection, title: "Detection", symbol: "viewfinder", permission: .changeSettings), // wave 17
            .init(id: Leaf.recording, title: "Recording", symbol: "record.circle", permission: .changeSettings), // wave 22
            .init(id: Leaf.monitorThresholds, title: "Monitor Thresholds", symbol: "slider.horizontal.3", permission: .changeSettings),
            .init(id: Leaf.currentTasks, title: "Current Tasks", symbol: "list.bullet.clipboard", permission: .viewAudit),
            .init(id: Leaf.evidenceLocks, title: "Evidence Locks", symbol: "lock.doc", permission: .exportEvidence),
            .init(id: Leaf.configReport, title: "Configuration Report", symbol: "doc.text.magnifyingglass", permission: .changeSettings)
        ]),
        ManagementNode(id: "logs", title: "Logs", symbol: "text.alignleft", children: [
            .init(id: Leaf.serverLogs, title: "Server Logs", symbol: "terminal", permission: .viewAudit),
            .init(id: Leaf.audit, title: "Audit", symbol: "list.bullet.rectangle.portrait.fill", permission: .viewAudit)
        ])
    ]

    /// The permissions that make the Management window worth opening.
    /// viewLive (wave 15) opens it for Insights; such a user sees only that leaf.
    public static let managementPermissions: [V2.Permission] = [.manageCameras, .manageUsers, .changeSettings, .viewAudit, .viewLive]

    public static func hasManagementAccess(_ can: (V2.Permission) -> Bool) -> Bool {
        managementPermissions.contains(where: can)
    }

    public static var allLeaves: [ManagementNode] { categories.flatMap(\.children) }
}

/// The tree as one signed-in user sees it, plus filtering, flattening,
/// breadcrumbs and deep links.
public struct ManagementNavModel: Equatable, Sendable {
    public private(set) var categories: [ManagementNode]

    /// Builds the tree for a user: leaves they lack the permission for are
    /// dropped, then categories left empty.
    public init(can: (V2.Permission) -> Bool, catalog: [ManagementNode] = ManagementCatalog.categories) {
        categories = catalog.compactMap { category in
            let leaves = category.children.filter { $0.permission.map(can) ?? true }
            guard leaves.isEmpty == false else { return nil }
            var copy = category
            copy.children = leaves
            return copy
        }
    }

    public var leaves: [ManagementNode] { categories.flatMap(\.children) }

    public func node(_ id: String) -> ManagementNode? {
        for category in categories {
            if category.id == id { return category }
            if let leaf = category.children.first(where: { $0.id == id }) { return leaf }
        }
        return nil
    }

    public func category(containing leafID: String) -> ManagementNode? {
        categories.first { $0.children.contains { $0.id == leafID } }
    }

    // MARK: Filter

    /// Leaves whose title or category title contains every word of `query`
    /// (case-insensitive); categories without a match disappear.
    public func filtered(_ query: String) -> [ManagementNode] {
        let words = query.lowercased().split(whereSeparator: \.isWhitespace).map(String.init)
        guard words.isEmpty == false else { return categories }
        return categories.compactMap { category in
            let leaves = category.children.filter { leaf in
                let hay = (leaf.title + " " + category.title).lowercased()
                return words.allSatisfy { hay.contains($0) }
            }
            guard leaves.isEmpty == false else { return nil }
            var copy = category
            copy.children = leaves
            return copy
        }
    }

    /// The visible rows (categories + leaves below them). A filter expands
    /// everything that matched; otherwise `collapsed` category ids are folded.
    public func rows(filter: String = "", collapsed: Set<String> = []) -> [ManagementRow] {
        let searching = filter.trimmingCharacters(in: .whitespaces).isEmpty == false
        var rows: [ManagementRow] = []
        for category in filtered(filter) {
            let expanded = searching || collapsed.contains(category.id) == false
            rows.append(ManagementRow(node: category, depth: 0, isExpanded: expanded))
            if expanded {
                rows += category.children.map { ManagementRow(node: $0, depth: 1, isExpanded: false) }
            }
        }
        return rows
    }

    // MARK: Breadcrumb & deep links

    /// `[siteName]` for the root, `[Category]` for a category, `[Category, Page]`
    /// for a leaf. Unknown ids fall back to the root.
    public func breadcrumb(for id: String, siteName: String) -> [String] {
        if let leaf = leaves.first(where: { $0.id == id }), let category = category(containing: id) {
            return [category.title, leaf.title]
        }
        if let category = categories.first(where: { $0.id == id }) { return [category.title] }
        return [siteName]
    }

    /// Resolves a deep link (`security/roles`, `#/security/roles`, `/security`)
    /// to a node id this user can see; anything else is the root welcome.
    public func resolve(deepLink: String?) -> String {
        guard var path = deepLink?.trimmingCharacters(in: .whitespaces) else { return ManagementCatalog.rootID }
        if path.hasPrefix("#") { path.removeFirst() }
        while path.hasPrefix("/") { path.removeFirst() }
        while path.hasSuffix("/") { path.removeLast() }
        return node(path) != nil ? path : ManagementCatalog.rootID
    }

    public static func hashPath(for id: String) -> String { id.isEmpty ? "#/" : "#/\(id)" }

    // MARK: Collapse state (AppStorage-friendly)

    public static func decodeCollapsed(_ raw: String) -> Set<String> {
        Set(raw.split(separator: ",").map(String.init))
    }

    public static func encodeCollapsed(_ ids: Set<String>) -> String {
        ids.sorted().joined(separator: ",")
    }
}

/// id -> pane registry, generic over what a pane is (the app instantiates it
/// with a SwiftUI view factory; tests with plain values). Stream F's panes
/// register here, one line each.
public struct ManagementPaneRegistry<Pane> {
    private var panes: [String: Pane] = [:]
    public init() {}

    public mutating func register(_ id: String, _ pane: Pane) { panes[id] = pane }
    public func pane(for id: String) -> Pane? { panes[id] }
    public func isRegistered(_ id: String) -> Bool { panes[id] != nil }
    public var registeredIDs: Set<String> { Set(panes.keys) }

    /// Leaf ids in `model` that still have no pane (rendered as stubs).
    public func unregistered(in model: ManagementNavModel) -> [String] {
        model.leaves.map(\.id).filter { panes[$0] == nil }
    }
}
