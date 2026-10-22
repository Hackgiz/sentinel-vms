import Foundation

/// The views-and-cameras panel (the second column in Live and saved-view
/// tabs): expanded at the operator's width, or collapsed to a thin strip
/// that only offers to expand it again. Collapsed is the same state the
/// header's sidebar button and ⌃⌘S toggle.
public enum CameraPanel {
    /// The collapsed strip: a chevron and a vertical "Views & Cameras" label.
    public static let collapsedWidth: CGFloat = 28
    public static let label = "Views & Cameras"

    /// The column's width on screen.
    public static func width(collapsed: Bool, expandedWidth: CGFloat) -> CGFloat {
        collapsed ? collapsedWidth : expandedWidth
    }

    /// Per site and per window, so a monitor window keeps its own.
    public static func storageKey(siteID: String, windowID: String = "main") -> String {
        "sentinel.cameraPanel.collapsed.\(siteID).\(windowID)"
    }

    /// nil when this site has never recorded a choice.
    public static func loadCollapsed(siteID: String, windowID: String = "main", defaults: UserDefaults = .standard) -> Bool? {
        let key = storageKey(siteID: siteID, windowID: windowID)
        guard defaults.object(forKey: key) != nil else { return nil }
        return defaults.bool(forKey: key)
    }

    public static func saveCollapsed(_ collapsed: Bool, siteID: String, windowID: String = "main",
                                     defaults: UserDefaults = .standard) {
        defaults.set(collapsed, forKey: storageKey(siteID: siteID, windowID: windowID))
    }
}

public extension CameraTree {
    /// The "By server" parents: recording servers that are not revoked.
    /// A revoked server is managed (and shown) in Admin → Recording Servers
    /// only; it stays here just while cameras still point at it, so those
    /// cameras keep their server's real name instead of a bare id.
    static func serverParents(_ servers: [V2.RecordingServer], items: [Item]) -> [Parent] {
        let inUse = Set(items.map(\.serverID))
        return servers
            .filter { $0.revoked == false || inUse.contains($0.id) }
            .map { Parent(id: $0.id, name: $0.name, online: $0.online) }
    }
}
