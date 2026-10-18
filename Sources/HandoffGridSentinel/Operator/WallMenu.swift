import AppKit
import SentinelCore

/// The menu bar's "Wall" menu (Wave 10, stream A), rebuilt each time it
/// opens from the site's wall layouts.
@MainActor
final class WallMenuController: NSObject, NSMenuDelegate {
    static let shared = WallMenuController()

    func makeMenu() -> NSMenu {
        let menu = NSMenu(title: "Wall")
        menu.delegate = self
        menu.autoenablesItems = false
        return menu
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        let session = OperatorContext.shared.session
        let signedIn = session.isSignedIn && session.runtime != nil
        let windows = OperatorWindows.shared

        let save = item("Save Current Monitors as Layout…", #selector(save))
        save.isEnabled = signedIn
        menu.addItem(save)
        let full = item(windows.allMonitorsFullscreen ? "Exit Fullscreen on All Monitors" : "Fullscreen All Monitors",
                        #selector(fullscreenAll), key: "f")
        full.keyEquivalentModifierMask = [.command, .option]
        full.isEnabled = signedIn && windows.monitorCount > 0
        menu.addItem(full)
        menu.addItem(.separator())

        guard signedIn, let wall = session.runtime?.wall else {
            menu.addItem(disabled("Sign in to use wall layouts"))
            return
        }
        if wall.layouts.isEmpty {
            menu.addItem(disabled(wall.isLoaded ? "No saved wall layouts" : "Loading wall layouts…"))
        } else {
            menu.addItem(disabled("Recall Layout"))
            for (index, layout) in wall.layouts.enumerated() {
                let entry = item(layout.shared ? "\(layout.name)  (Shared)" : layout.name, #selector(recall(_:)),
                                 key: index < 9 ? "\(index + 1)" : "")
                entry.keyEquivalentModifierMask = [.command, .option]
                entry.representedObject = layout.id
                entry.indentationLevel = 1
                menu.addItem(entry)
            }
            let editable = wall.layouts.filter(WallActions.canEdit)
            if editable.isEmpty == false {
                menu.addItem(.separator())
                menu.addItem(submenu("Update with Current Monitors", editable, #selector(update(_:))))
                menu.addItem(submenu("Delete Layout", editable, #selector(delete(_:))))
            }
        }
        menu.addItem(.separator())
        menu.addItem(item("Refresh Layouts", #selector(refresh)))
    }

    /// Opening the menu re-reads the layouts in the background, so the next
    /// open shows anything saved elsewhere. (Not in `menuNeedsUpdate`: AppKit
    /// also calls that while matching key equivalents.)
    func menuWillOpen(_ menu: NSMenu) {
        WallActions.refresh()
    }

    private func item(_ title: String, _ action: Selector, key: String = "") -> NSMenuItem {
        let entry = NSMenuItem(title: title, action: action, keyEquivalent: key)
        entry.target = self
        return entry
    }

    private func disabled(_ title: String) -> NSMenuItem {
        let entry = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        entry.isEnabled = false
        return entry
    }

    private func submenu(_ title: String, _ layouts: [V2.WallLayout], _ action: Selector) -> NSMenuItem {
        let holder = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        let sub = NSMenu(title: title)
        for layout in layouts {
            let entry = item(layout.name, action)
            entry.representedObject = layout.id
            sub.addItem(entry)
        }
        holder.submenu = sub
        return holder
    }

    private func layout(_ sender: NSMenuItem) -> V2.WallLayout? {
        guard let id = sender.representedObject as? String else { return nil }
        return OperatorContext.shared.session.runtime?.wall.layouts.first { $0.id == id }
    }

    @objc private func save() { WallActions.saveCurrentMonitors() }
    @objc private func fullscreenAll() { OperatorWindows.shared.toggleFullscreenAllMonitors() }
    @objc private func refresh() { WallActions.refresh() }

    @objc private func recall(_ sender: NSMenuItem) {
        if let layout = layout(sender) { WallActions.recall(layout) }
    }

    @objc private func update(_ sender: NSMenuItem) {
        if let layout = layout(sender) { WallActions.updateWithCurrentMonitors(layout) }
    }

    @objc private func delete(_ sender: NSMenuItem) {
        if let layout = layout(sender) { WallActions.delete(layout) }
    }
}
