import Foundation

/// The sidebar's view tree: a Private root (the user's own views) and a
/// Shared root (folders nested by `parentId`, shared views inside them),
/// narrowed by the search field. Pure, so it can be tested. Mirrors the web
/// client's `viewTreeModel.ts`.
public enum ViewTree {
    public indirect enum Node: Equatable, Identifiable, Sendable {
        case folder(id: String, name: String, children: [Node], viewCount: Int)
        case view(V2.SavedView)

        public var id: String {
            switch self {
            case .folder(let id, _, _, _): return "f:\(id)"
            case .view(let view): return "v:\(view.id)"
            }
        }
    }

    public enum RootID: String, Sendable { case `private`, shared }

    public struct Root: Equatable, Identifiable, Sendable {
        public var id: RootID
        public var label: String
        public var nodes: [Node]
        /// Views under the root after the search narrowed it.
        public var viewCount: Int
        /// Users without `manageViews` see the Shared root read-only (lock glyph).
        public var readOnly: Bool
    }

    private static func name(_ lhs: String, _ rhs: String) -> Bool {
        lhs.compare(rhs, options: [.caseInsensitive, .diacriticInsensitive]) == .orderedAscending
    }

    private static func ordered<T>(_ list: [T], order: (T) -> Int, name n: (T) -> String) -> [T] {
        list.enumerated().sorted { a, b in
            let oa = order(a.element), ob = order(b.element)
            if oa != ob { return oa < ob }
            let na = n(a.element), nb = n(b.element)
            if na.compare(nb, options: [.caseInsensitive, .diacriticInsensitive]) != .orderedSame { return name(na, nb) }
            return a.offset < b.offset
        }.map(\.element)
    }

    private static func has(_ text: String, _ needle: String) -> Bool {
        text.lowercased().contains(needle)
    }

    private static func countViews(_ nodes: [Node]) -> Int {
        nodes.reduce(0) { sum, node in
            switch node {
            case .view: return sum + 1
            case .folder(_, _, _, let count): return sum + count
            }
        }
    }

    /// The Shared root's nodes: folders nested by parentId (a missing
    /// parent counts as top level), then views, each in order.
    public static func sharedNodes(groups: [V2.ViewGroup], shared: [V2.SavedView]) -> [Node] {
        let ids = Set(groups.map(\.id))
        func build(_ parent: String, _ seen: Set<String>) -> [Node] {
            let folders = ordered(groups.filter { group in
                let p = ids.contains(group.parentId) && group.parentId != group.id ? group.parentId : ""
                return p == parent && seen.contains(group.id) == false
            }, order: \.sortOrder, name: \.name).map { group -> Node in
                let children = build(group.id, seen.union([group.id]))
                return .folder(id: group.id, name: group.name, children: children, viewCount: countViews(children))
            }
            let views = ordered(shared.filter { view in
                let g = view.groupId ?? ""
                return (ids.contains(g) ? g : "") == parent
            }, order: { $0.sortOrder ?? 0 }, name: \.name).map { Node.view($0) }
            return folders + views
        }
        return build("", [])
    }

    private static func prune(_ nodes: [Node], _ needle: String) -> [Node] {
        var out: [Node] = []
        for node in nodes {
            switch node {
            case .view(let view):
                if has(view.name, needle) { out.append(node) }
            case .folder(let id, let name, let children, _):
                if has(name, needle) {
                    out.append(node) // a folder whose own name matches keeps everything in it
                } else {
                    let kept = prune(children, needle)
                    if kept.isEmpty == false {
                        out.append(.folder(id: id, name: name, children: kept, viewCount: countViews(kept)))
                    }
                }
            }
        }
        return out
    }

    /// Both roots for the sidebar. With a search, only views whose name
    /// matches (and the folders leading to them) remain and empty roots drop
    /// away; without one the Shared root always shows, so managers can start a folder.
    public static func build(views: [V2.SavedView], shared: [V2.SavedView], groups: [V2.ViewGroup],
                             query: String, canManage: Bool) -> [Root] {
        let needle = query.trimmingCharacters(in: .whitespaces).lowercased()
        let priv = ordered(views, order: { $0.sortOrder ?? 0 }, name: \.name).map { Node.view($0) }
        let sharedAll = sharedNodes(groups: groups, shared: shared)
        var roots = [
            Root(id: .private, label: "Private", nodes: needle.isEmpty ? priv : prune(priv, needle), viewCount: 0, readOnly: false),
            Root(id: .shared, label: "Shared", nodes: needle.isEmpty ? sharedAll : prune(sharedAll, needle), viewCount: 0, readOnly: canManage == false),
        ]
        for i in roots.indices { roots[i].viewCount = countViews(roots[i].nodes) }
        return needle.isEmpty ? roots : roots.filter { $0.nodes.isEmpty == false }
    }

    public struct FolderOption: Equatable, Identifiable, Sendable {
        public var id: String
        /// "Parent / Child".
        public var label: String
    }

    /// Every folder with its path, in tree order (for pickers).
    public static func folderOptions(groups: [V2.ViewGroup]) -> [FolderOption] {
        var out: [FolderOption] = []
        func walk(_ nodes: [Node], _ prefix: String) {
            for case .folder(let id, let name, let children, _) in nodes {
                out.append(FolderOption(id: id, label: prefix + name))
                walk(children, "\(prefix)\(name) / ")
            }
        }
        walk(sharedNodes(groups: groups, shared: []), "")
        return out
    }

    /// A view by id, private or shared.
    public static func find(_ id: String, views: [V2.SavedView], shared: [V2.SavedView]) -> V2.SavedView? {
        views.first { $0.id == id } ?? shared.first { $0.id == id }
    }

    /// Folder ids at or below `id` (a folder can't move into itself).
    public static func descendants(of id: String, groups: [V2.ViewGroup]) -> Set<String> {
        var out: Set<String> = [id]
        var grew = true
        while grew {
            grew = false
            for g in groups where out.contains(g.parentId) && out.insert(g.id).inserted { grew = true }
        }
        return out
    }
}
