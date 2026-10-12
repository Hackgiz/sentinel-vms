import Foundation

/// The camera grid of the Operator workspace: `columns × rows` tiles, each
/// empty or holding one camera. A camera sits in at most one tile.
public struct WorkspaceGrid: Codable, Equatable, Sendable {
    public static let maxSide = 8

    public private(set) var columns: Int
    public private(set) var rows: Int
    public private(set) var slots: [String?]

    public init(columns: Int = 3, rows: Int = 3) {
        let c = Self.clamp(columns), r = Self.clamp(rows)
        self.columns = c
        self.rows = r
        self.slots = Array(repeating: nil, count: c * r)
    }

    private static func clamp(_ side: Int) -> Int { min(max(side, 1), maxSide) }

    /// Smallest near-square grid holding `count` tiles.
    public static func dimensions(forCount count: Int) -> (columns: Int, rows: Int) {
        let n = min(max(count, 1), maxSide * maxSide)
        let cols = min(Int(ceil(Double(n).squareRoot())), maxSide)
        return (cols, min(Int(ceil(Double(n) / Double(cols))), maxSide))
    }

    public var cameraIDs: [String] { slots.compactMap { $0 } }
    public var freeCount: Int { slots.filter { $0 == nil }.count }

    public func tileIndex(of cameraID: String) -> Int? { slots.firstIndex { $0 == cameraID } }

    /// Changes the layout, keeping each camera at its row and column when it still fits.
    public mutating func resize(columns newColumns: Int, rows newRows: Int) {
        let c = Self.clamp(newColumns), r = Self.clamp(newRows)
        var next: [String?] = Array(repeating: nil, count: c * r)
        var overflow: [String] = []
        for (i, camera) in slots.enumerated() {
            guard let camera else { continue }
            let col = i % columns, row = i / columns
            if col < c, row < r { next[row * c + col] = camera } else { overflow.append(camera) }
        }
        for camera in overflow {
            if let free = next.firstIndex(where: { $0 == nil }) { next[free] = camera }
        }
        columns = c
        rows = r
        slots = next
    }

    /// Puts `cameraID` in tile `index`. A camera already on the grid moves
    /// (swapping with whatever the target held).
    public mutating func place(_ cameraID: String, at index: Int) {
        guard slots.indices.contains(index) else { return }
        if let from = tileIndex(of: cameraID) {
            guard from != index else { return }
            slots.swapAt(from, index)
        } else {
            slots[index] = cameraID
        }
    }

    /// Puts `cameraID` in the first free tile; returns its index, or nil if
    /// the camera is already on the grid (index returned) / no tile is free.
    @discardableResult
    public mutating func placeInFirstFree(_ cameraID: String) -> Int? {
        if let existing = tileIndex(of: cameraID) { return existing }
        guard let free = slots.firstIndex(where: { $0 == nil }) else { return nil }
        slots[free] = cameraID
        return free
    }

    /// Drops several cameras starting at tile `index`: the first goes there,
    /// the rest into free tiles in order.
    public mutating func place(_ cameraIDs: [String], startingAt index: Int) {
        guard let first = cameraIDs.first else { return }
        place(first, at: index)
        for id in cameraIDs.dropFirst() { placeInFirstFree(id) }
    }

    public mutating func clearTile(_ index: Int) {
        guard slots.indices.contains(index) else { return }
        slots[index] = nil
    }

    public mutating func clearAll() {
        slots = Array(repeating: nil, count: slots.count)
    }

    /// Replaces the layout with the smallest near-square grid holding `cameraIDs`, in order.
    public mutating func fill(with cameraIDs: [String]) {
        var seen = Set<String>()
        let unique = cameraIDs.filter { seen.insert($0).inserted }
        let size = Self.dimensions(forCount: unique.count)
        columns = size.columns
        rows = size.rows
        slots = Array(repeating: nil, count: columns * rows)
        for (i, id) in unique.prefix(slots.count).enumerated() { slots[i] = id }
    }

    /// Keeps only cameras for which `isAvailable` holds (cameras removed or out of scope).
    public mutating func prune(keeping isAvailable: (String) -> Bool) {
        for i in slots.indices {
            if let id = slots[i], isAvailable(id) == false { slots[i] = nil }
        }
    }

    // MARK: Saved views

    public init(view: V2.View) {
        let c = Self.clamp(view.columns), r = Self.clamp(view.rows)
        self.init(columns: c, rows: r)
        for (n, tile) in view.tiles.enumerated() {
            let x = tile.x ?? n % c, y = tile.y ?? n / c
            guard x >= 0, x < c, y >= 0, y < r, slots[y * c + x] == nil, tileIndex(of: tile.cameraId) == nil else { continue }
            slots[y * c + x] = tile.cameraId
        }
    }

    /// The grid as a saved view's body, tiles at their row and column.
    public func viewInput(name: String) -> V2.ViewInput {
        let tiles = slots.enumerated().compactMap { i, id in
            id.map { V2.ViewTile(cameraId: $0, x: i % columns, y: i / columns, w: 1, h: 1) }
        }
        return V2.ViewInput(name: name, columns: columns, rows: rows, tiles: tiles)
    }
}

/// The sidebar's Cameras tree: grouped by recording server or by camera
/// group, filtered by the search text.
public enum CameraTree {
    public enum Grouping: String, Codable, Sendable { case server, group }

    public struct Item: Equatable, Sendable {
        public var id: String
        public var name: String
        public var location: String
        public var serverID: String
        public var groupIDs: [String]
        public var online: Bool

        public init(id: String, name: String, location: String = "", serverID: String, groupIDs: [String] = [], online: Bool) {
            self.id = id
            self.name = name
            self.location = location
            self.serverID = serverID
            self.groupIDs = groupIDs
            self.online = online
        }
    }

    public struct Parent: Equatable, Sendable {
        public var id: String
        public var name: String
        public var online: Bool
        public init(id: String, name: String, online: Bool) {
            self.id = id
            self.name = name
            self.online = online
        }
    }

    public struct Node: Equatable, Identifiable, Sendable {
        public var parent: Parent
        public var cameras: [Item]
        /// Cameras the parent holds before the search filter.
        public var totalCount: Int
        public var id: String { parent.id }
    }

    public static let ungroupedID = "__ungrouped"

    /// `parents` are servers (or groups) in display order. Cameras whose
    /// server isn't listed fall under a server named by its id. While
    /// searching, parents with no matching camera are hidden unless their own
    /// name matches (then all their cameras show).
    public static func build(items: [Item], parents: [Parent], grouping: Grouping, query: String) -> [Node] {
        let needle = query.trimmingCharacters(in: .whitespaces).lowercased()
        var buckets: [String: [Item]] = [:]
        var order = parents
        for item in items {
            let keys: [String]
            switch grouping {
            case .server: keys = [item.serverID]
            case .group: keys = item.groupIDs.isEmpty ? [ungroupedID] : item.groupIDs
            }
            for key in keys {
                buckets[key, default: []].append(item)
                if order.contains(where: { $0.id == key }) == false {
                    let name = key == ungroupedID ? "Ungrouped" : key
                    order.append(Parent(id: key, name: name, online: grouping == .group))
                }
            }
        }
        return order.compactMap { parent in
            let all = buckets[parent.id] ?? []
            if all.isEmpty, grouping == .group, needle.isEmpty == false { return nil }
            guard needle.isEmpty == false else { return Node(parent: parent, cameras: all, totalCount: all.count) }
            if parent.name.lowercased().contains(needle) { return Node(parent: parent, cameras: all, totalCount: all.count) }
            let matching = all.filter { $0.name.lowercased().contains(needle) || $0.location.lowercased().contains(needle) }
            return matching.isEmpty ? nil : Node(parent: parent, cameras: matching, totalCount: all.count)
        }
    }

    /// Views whose name contains the search text.
    public static func filter<T>(_ list: [T], name: (T) -> String, query: String) -> [T] {
        let needle = query.trimmingCharacters(in: .whitespaces).lowercased()
        return needle.isEmpty ? list : list.filter { name($0).lowercased().contains(needle) }
    }
}
