import SwiftUI
import SentinelCore

/// The wave-6 management panes as plain `(id, title, factory)` tuples.
///
/// Wiring (coordinator, at merge): append these to stream E's pane registry,
/// one line, mapping each id to the matching tree leaf, e.g.
/// ```
/// for pane in ManagementPaneCatalog.panes {
///     registry.register(id: pane.id, title: pane.title) { api in pane.factory(api) }
/// }
/// ```
/// Each factory takes the site's `SentinelAPI` and returns the pane. Panes
/// with a refresh interval (Tasks, Logs) take `isVisible:` through the
/// individual views if the host keeps hidden panes alive; the catalog's
/// factories assume the host removes a pane when it isn't selected.
enum ManagementPaneCatalog {
    typealias Entry = (id: String, title: String, factory: (SentinelAPI) -> AnyView)

    static let panes: [Entry] = [
        ("site-info", "Site Information", { AnyView(SiteInfoPane(api: $0)) }),
        ("roles", "Roles", { AnyView(RolesPane(api: $0)) }),
        ("time-profiles", "Time Profiles", { AnyView(TimeProfilesPane(api: $0)) }),
        ("webhooks", "Webhooks", { AnyView(WebhooksPane(api: $0)) }),
        ("notifications", "Notifications", { AnyView(NotificationsPane(api: $0)) }),
        ("tasks", "Current Tasks", { AnyView(TasksPane(api: $0)) }),
        ("evidence", "Evidence Locks", { AnyView(EvidenceLocksPane(api: $0)) }),
        ("config-report", "Configuration Report", { AnyView(ConfigReportPane(api: $0)) }),
        ("logs", "Server Logs", { AnyView(ServerLogsPane(api: $0)) }),
        ("thresholds", "Monitor Thresholds", { AnyView(ThresholdsPane(api: $0)) })
    ]
}
