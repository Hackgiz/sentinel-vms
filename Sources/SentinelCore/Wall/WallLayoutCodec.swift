import Foundation

/// What a wall monitor shows, read from a stored `V2.WallMonitor`.
public enum WallMonitorContent: Equatable, Sendable {
    /// A saved view.
    case view(String)
    /// A camera list laid out on a grid of its own (may be empty).
    case cameras([String])
    /// Cycle `plan.viewIDs`, starting on `startViewID` when it's in the plan.
    case tour(WallTourPlan, startViewID: String?)
    /// Nothing the reader may see is left (cameras out of scope, views gone).
    case empty
}

/// A monitor window's tour: the views it cycles and how long each stays up.
public struct WallTourPlan: Codable, Equatable, Hashable, Sendable {
    public var viewIDs: [String]
    public var seconds: Int

    public init(viewIDs: [String], seconds: Int) {
        self.viewIDs = viewIDs
        self.seconds = seconds
    }

    /// Within the server's limits: 5…3600 seconds, at most 32 views, no
    /// duplicates next to each other. nil when no view is left.
    public var normalized: WallTourPlan? {
        var ids: [String] = []
        for id in viewIDs where id.isEmpty == false && ids.last != id { ids.append(id) }
        ids = Array(ids.prefix(V2.WallTour.maxViews))
        guard ids.isEmpty == false else { return nil }
        let range = V2.WallTour.secondsRange
        return WallTourPlan(viewIDs: ids, seconds: min(max(seconds, range.lowerBound), range.upperBound))
    }

    public var wire: V2.WallTour { V2.WallTour(viewIds: viewIDs, seconds: seconds) }
}

/// A monitor window as it is right now, for "Save Current Monitors".
public struct WallMonitorSnapshot: Equatable, Sendable {
    public var displayName: String
    /// The display's position in the screen list (0 = main), nil if off-screen.
    public var displayIndex: Int?
    /// The window's top-left on its display, for a stable left-to-right order.
    public var originX: Double
    public var originY: Double
    /// The saved view the grid shows unchanged, if any.
    public var viewID: String?
    /// The grid's cameras (used when it isn't showing a saved view).
    public var cameraIDs: [String]
    public var tour: WallTourPlan?
    public var alarmMonitor: Bool
    public var fullscreen: Bool

    public init(displayName: String, displayIndex: Int?, originX: Double = 0, originY: Double = 0, viewID: String? = nil,
                cameraIDs: [String] = [], tour: WallTourPlan? = nil, alarmMonitor: Bool = false, fullscreen: Bool = false) {
        self.displayName = displayName
        self.displayIndex = displayIndex
        self.originX = originX
        self.originY = originY
        self.viewID = viewID
        self.cameraIDs = cameraIDs
        self.tour = tour
        self.alarmMonitor = alarmMonitor
        self.fullscreen = fullscreen
    }
}

/// Between monitor windows and the stored layout body.
public enum WallLayoutCodec {
    /// The monitors to store for these windows: ordered by display, then
    /// left to right, top to bottom; slots numbered from 0; trimmed to the
    /// server's limits (16 monitors, 64 cameras each, tour 5…3600 s).
    public static func monitors(from snapshots: [WallMonitorSnapshot]) -> [V2.WallMonitor] {
        let ordered = snapshots.sorted { a, b in
            let ai = a.displayIndex ?? Int.max, bi = b.displayIndex ?? Int.max
            if ai != bi { return ai < bi }
            if a.originY != b.originY { return a.originY < b.originY }
            return a.originX < b.originX
        }
        return ordered.prefix(V2.WallMonitor.maxSlots).enumerated().map { slot, snap in
            let tour = snap.tour?.normalized
            var monitor = V2.WallMonitor(slot: slot, screenHint: String(snap.displayName.prefix(200)),
                                         screenIndex: snap.displayIndex.map { min(max($0, 0), 63) })
            if let viewID = snap.viewID, viewID.isEmpty == false {
                monitor.viewId = viewID
            } else if let tour {
                monitor.viewId = tour.viewIDs.first
            } else {
                monitor.cameraIds = Array(uniqued(snap.cameraIDs).prefix(V2.WallMonitor.maxCameras))
            }
            monitor.tour = tour?.wire
            monitor.alarmMonitor = snap.alarmMonitor ? true : nil
            monitor.fullscreen = snap.fullscreen ? true : nil
            return monitor
        }
    }

    /// What a stored monitor shows.
    public static func content(of monitor: V2.WallMonitor) -> WallMonitorContent {
        if let tour = monitor.tour, let plan = WallTourPlan(viewIDs: tour.viewIds, seconds: tour.seconds).normalized {
            let start = monitor.viewId.flatMap { plan.viewIDs.contains($0) ? $0 : nil }
            return .tour(plan, startViewID: start)
        }
        if let viewID = monitor.viewId, viewID.isEmpty == false { return .view(viewID) }
        if let cameras = monitor.cameraIds { return .cameras(cameras) }
        return .empty
    }

    /// The layout's monitors in slot order.
    public static func ordered(_ layout: V2.WallLayout) -> [V2.WallMonitor] {
        layout.monitors.sorted { $0.slot < $1.slot }
    }

    public static func encode(_ input: V2.WallLayoutInput) throws -> Data {
        try SentinelAPI.jsonEncoder.encode(input)
    }

    public static func decode(_ data: Data) throws -> V2.WallLayout {
        try SentinelAPI.jsonDecoder.decode(V2.WallLayout.self, from: data)
    }

    private static func uniqued(_ ids: [String]) -> [String] {
        var seen = Set<String>()
        return ids.filter { $0.isEmpty == false && seen.insert($0).inserted }
    }
}

/// Saving a wall layout: name and sharing checks the sheet shows verbatim.
public enum WallLayoutSaving {
    public enum Problem: Error, Equatable, LocalizedError, Sendable {
        case emptyName
        case nameTooLong
        case sharedNeedsPermission
        case noMonitors

        public var errorDescription: String? {
            switch self {
            case .emptyName: return "Give the layout a name."
            case .nameTooLong: return "Layout names can be at most 100 characters."
            case .sharedNeedsPermission: return "Your role can't share wall layouts."
            case .noMonitors: return "Open at least one monitor window first (File ▸ New Monitor Window)."
            }
        }
    }

    public static func input(name: String, shared: Bool, canManage: Bool, monitors: [V2.WallMonitor]) throws -> V2.WallLayoutInput {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.isEmpty == false else { throw Problem.emptyName }
        guard trimmed.count <= 100 else { throw Problem.nameTooLong }
        guard shared == false || canManage else { throw Problem.sharedNeedsPermission }
        guard monitors.isEmpty == false else { throw Problem.noMonitors }
        return V2.WallLayoutInput(name: trimmed, shared: shared, monitors: monitors)
    }

    /// Whether the user may change or delete `layout` (the server's `canEdit`,
    /// re-derived for a stale copy: shared layouts need `manageViews`).
    public static func canEdit(_ layout: V2.WallLayout, userID: String?, canManage: Bool) -> Bool {
        layout.shared ? canManage : (layout.canEdit || layout.createdBy == userID)
    }
}
