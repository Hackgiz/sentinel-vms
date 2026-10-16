import Foundation

/// Where "Save As…" puts a view: a name, and Private or Shared in a folder.
public struct ViewSaveTarget: Equatable, Sendable {
    public enum Problem: Error, Equatable, LocalizedError, Sendable {
        case emptyName
        case nameTooLong
        case sharedNeedsPermission
        case unknownFolder

        public var errorDescription: String? {
            switch self {
            case .emptyName: return "Give the view a name."
            case .nameTooLong: return "View names can be at most \(ViewSaveTarget.maxNameLength) characters."
            case .sharedNeedsPermission: return "Your role can't create shared views."
            case .unknownFolder: return "That folder no longer exists."
            }
        }
    }

    public static let maxNameLength = 100

    public var name: String
    public var shared: Bool
    /// "" = top level of the Shared tree. Ignored (cleared) for private views.
    public var groupId: String

    public init(name: String, shared: Bool = false, groupId: String = "") {
        self.name = name
        self.shared = shared
        self.groupId = groupId
    }

    /// The target as it will be sent: trimmed name, folder cleared when
    /// private. Throws a `Problem` the sheet shows verbatim.
    public func validated(canManage: Bool, folderIDs: Set<String>) throws -> ViewSaveTarget {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.isEmpty == false else { throw Problem.emptyName }
        guard trimmed.count <= Self.maxNameLength else { throw Problem.nameTooLong }
        guard shared else { return ViewSaveTarget(name: trimmed) }
        guard canManage else { throw Problem.sharedNeedsPermission }
        guard groupId.isEmpty || folderIDs.contains(groupId) else { throw Problem.unknownFolder }
        return ViewSaveTarget(name: trimmed, shared: true, groupId: groupId)
    }
}

/// Request bodies for saving views without losing what the editor doesn't
/// touch (a PUT replaces the whole view, so a shared view's flag, folder and
/// order travel along or it would turn private).
public enum ViewInputs {
    /// Whether the viewer may change this view (shared ones need `manageViews`).
    public static func canEdit(_ view: V2.SavedView, canManage: Bool) -> Bool {
        view.isShared == false || canManage
    }

    /// `view` stored again, with an optional new name and/or grid.
    public static func update(_ view: V2.SavedView, name: String? = nil, grid: WorkspaceGrid? = nil) -> V2.SavedViewInput {
        var input: V2.SavedViewInput
        if let grid {
            input = V2.SavedViewInput(grid.viewInput(name: name ?? view.name))
        } else {
            input = V2.SavedViewInput(name: name ?? view.name, columns: view.columns, rows: view.rows, tiles: view.tiles)
        }
        if view.isShared {
            input.shared = true
            if let g = view.groupId, g.isEmpty == false { input.groupId = g }
            if let s = view.sortOrder, s != 0 { input.sortOrder = s }
        }
        return input
    }

    /// A new view from a grid, private or shared in a folder.
    public static func new(grid: WorkspaceGrid, target: ViewSaveTarget) -> V2.SavedViewInput {
        var input = V2.SavedViewInput(grid.viewInput(name: target.name))
        if target.shared {
            input.shared = true
            if target.groupId.isEmpty == false { input.groupId = target.groupId }
        }
        return input
    }

    /// A shared view moved into `groupId` ("" = top level).
    public static func move(_ view: V2.SavedView, toGroup groupId: String) -> V2.SavedViewInput {
        var input = update(view)
        input.groupId = groupId
        return input
    }
}
