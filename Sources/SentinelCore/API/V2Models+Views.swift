import Foundation

// Wave 5 / stream A: the parts of the saved-view contract the original
// `V2.View` doesn't carry: shared views, the folders of the Shared tree
// (`/api/v2/view-groups`) and the `manageViews` permission.

public extension V2.Permission {
    /// Create, change and delete shared views and their folders.
    static let manageViews = V2.Permission(rawValue: "manageViews")
}

public extension V2 {
    /// A saved view as the server stores it: layout plus where it lives.
    /// `shared` views belong to the Shared tree (optionally in a folder).
    struct SavedView: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis
        public var name: String
        public var columns: Int
        public var rows: Int
        @NullableArray public var tiles: [ViewTile]
        public var shared: Bool?
        public var groupId: String?
        public var sortOrder: Int?

        public init(id: String, name: String, columns: Int = 2, rows: Int = 2, tiles: [ViewTile] = [],
                    shared: Bool? = nil, groupId: String? = nil, sortOrder: Int? = nil,
                    createdAt: UnixMillis = 0, updatedAt: UnixMillis = 0) {
            self.id = id
            self.name = name
            self.columns = columns
            self.rows = rows
            self.tiles = tiles
            self.shared = shared
            self.groupId = groupId
            self.sortOrder = sortOrder
            self.createdAt = createdAt
            self.updatedAt = updatedAt
        }

        public var isShared: Bool { shared ?? false }

        /// The layout-only view the grid code understands.
        public var asView: View {
            View(id: id, createdAt: createdAt, updatedAt: updatedAt, name: name, columns: columns, rows: rows, tiles: tiles)
        }
    }

    /// `ViewInput` with the shared-tree fields. A PUT replaces the whole
    /// view, so updates carry `shared`/`groupId`/`sortOrder` along.
    struct SavedViewInput: Codable, Hashable, Sendable {
        public var name: String
        public var columns: Int
        public var rows: Int
        public var tiles: [ViewTile]
        public var shared: Bool?
        public var groupId: String?
        public var sortOrder: Int?

        public init(name: String, columns: Int, rows: Int, tiles: [ViewTile],
                    shared: Bool? = nil, groupId: String? = nil, sortOrder: Int? = nil) {
            self.name = name
            self.columns = columns
            self.rows = rows
            self.tiles = tiles
            self.shared = shared
            self.groupId = groupId
            self.sortOrder = sortOrder
        }

        public init(_ input: ViewInput, shared: Bool? = nil, groupId: String? = nil, sortOrder: Int? = nil) {
            self.init(name: input.name, columns: input.columns, rows: input.rows, tiles: input.tiles,
                      shared: shared, groupId: groupId, sortOrder: sortOrder)
        }
    }

    /// A folder of the Shared tree. `parentId` is "" at the top level.
    struct ViewGroup: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var parentId: String
        public var name: String
        public var sortOrder: Int

        public init(id: String, parentId: String = "", name: String, sortOrder: Int = 0) {
            self.id = id
            self.parentId = parentId
            self.name = name
            self.sortOrder = sortOrder
        }

        private enum CodingKeys: String, CodingKey { case id, parentId, name, sortOrder }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            parentId = try c.decodeIfPresent(String.self, forKey: .parentId) ?? ""
            name = try c.decode(String.self, forKey: .name)
            sortOrder = try c.decodeIfPresent(Int.self, forKey: .sortOrder) ?? 0
        }
    }

    struct ViewGroupInput: Codable, Hashable, Sendable {
        public var name: String
        public var parentId: String?
        public var sortOrder: Int?

        public init(name: String, parentId: String? = nil, sortOrder: Int? = nil) {
            self.name = name
            self.parentId = parentId
            self.sortOrder = sortOrder
        }
    }

    /// `GET /api/v2/view-groups`: every folder and every shared view.
    struct SharedViewTree: Codable, Hashable, Sendable {
        @NullableArray public var groups: [ViewGroup]
        @NullableArray public var views: [SavedView]

        public init(groups: [ViewGroup] = [], views: [SavedView] = []) {
            self.groups = groups
            self.views = views
        }
    }
}
