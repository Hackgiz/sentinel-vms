import Foundation

/// Selection in the Alarm Manager's card list (wave 20), standing in for
/// the table it replaced: a click selects one, ⌘-click toggles, ⇧-click
/// selects the range from the anchor, ↑/↓ step. Pure, over row ids in
/// display order.
public enum AlarmCardSelection {
    public static func click(_ id: String, ids: [String], selection: Set<String>, anchor: String?,
                             command: Bool, shift: Bool) -> Set<String> {
        if shift, let anchor, let a = ids.firstIndex(of: anchor), let b = ids.firstIndex(of: id) {
            return selection.union(ids[min(a, b)...max(a, b)])
        }
        if command {
            var next = selection
            if next.contains(id) { next.remove(id) } else { next.insert(id) }
            return next
        }
        return [id]
    }

    /// The row an arrow key moves to: from the anchor (or the only
    /// selection), one up or down, clamped; the first row when nothing is
    /// selected. nil when there are no rows.
    public static func step(ids: [String], selection: Set<String>, anchor: String?, down: Bool) -> String? {
        guard ids.isEmpty == false else { return nil }
        let current = anchor.flatMap { selection.contains($0) ? ids.firstIndex(of: $0) : nil }
            ?? ids.firstIndex { selection.contains($0) }
        guard let current else { return down ? ids.first : ids.last }
        return ids[min(max(current + (down ? 1 : -1), 0), ids.count - 1)]
    }
}
