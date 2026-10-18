import Foundation

/// A display as the wall sees it: its name and position in the screen list
/// (0 = the main display).
public struct WallDisplay: Equatable, Sendable {
    public var name: String
    public var index: Int

    public init(name: String, index: Int) {
        self.name = name
        self.index = index
    }
}

/// Where a recalled monitor goes.
public struct WallPlacement: Equatable, Sendable {
    public enum Match: Equatable, Sendable {
        /// A display with the saved name.
        case name
        /// No display has that name; the one at the saved position.
        case index
        /// Neither: the main display.
        case fallback
    }

    public var slot: Int
    public var displayIndex: Int
    public var matchedBy: Match

    public init(slot: Int, displayIndex: Int, matchedBy: Match) {
        self.slot = slot
        self.displayIndex = displayIndex
        self.matchedBy = matchedBy
    }
}

/// Recalling a layout: monitors go to the display with the saved name,
/// else the display at the saved position, else the main display.
///
/// Identical displays share a name ("DELL U2720Q" twice): among those the
/// one at the saved position wins, and otherwise monitors are spread over
/// them least-used first, so two monitors saved on two identical displays
/// don't stack up on one.
public enum WallDisplayMatcher {
    public static func place(_ monitors: [V2.WallMonitor], on displays: [WallDisplay]) -> [WallPlacement] {
        guard displays.isEmpty == false else { return [] }
        let ordered = monitors.sorted { $0.slot < $1.slot }
        var byName: [String: [WallDisplay]] = [:]
        for display in displays { byName[key(display.name), default: []].append(display) }
        var nameUse: [Int: Int] = [:]
        var placements: [Int: WallPlacement] = [:]

        // Pass 1: by name. Exact-position hits first so the spread pass
        // below doesn't take a display someone else saved on.
        var pending: [V2.WallMonitor] = []
        for monitor in ordered {
            guard let candidates = byName[key(monitor.screenHint)], candidates.isEmpty == false else { continue }
            if candidates.count == 1 {
                placements[monitor.slot] = WallPlacement(slot: monitor.slot, displayIndex: candidates[0].index, matchedBy: .name)
                nameUse[candidates[0].index, default: 0] += 1
            } else if let index = monitor.screenIndex, candidates.contains(where: { $0.index == index }) {
                placements[monitor.slot] = WallPlacement(slot: monitor.slot, displayIndex: index, matchedBy: .name)
                nameUse[index, default: 0] += 1
            } else {
                pending.append(monitor)
            }
        }
        for monitor in pending {
            let candidates = byName[key(monitor.screenHint)] ?? []
            let pick = candidates.min { (nameUse[$0.index] ?? 0, $0.index) < (nameUse[$1.index] ?? 0, $1.index) }!
            placements[monitor.slot] = WallPlacement(slot: monitor.slot, displayIndex: pick.index, matchedBy: .name)
            nameUse[pick.index, default: 0] += 1
        }

        // Pass 2: by position, else the main display.
        let indices = Set(displays.map(\.index))
        let main = displays.min { $0.index < $1.index }!.index
        for monitor in ordered where placements[monitor.slot] == nil {
            if let index = monitor.screenIndex, indices.contains(index) {
                placements[monitor.slot] = WallPlacement(slot: monitor.slot, displayIndex: index, matchedBy: .index)
            } else {
                placements[monitor.slot] = WallPlacement(slot: monitor.slot, displayIndex: main, matchedBy: .fallback)
            }
        }
        return ordered.compactMap { placements[$0.slot] }
    }

    private static func key(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }
}
