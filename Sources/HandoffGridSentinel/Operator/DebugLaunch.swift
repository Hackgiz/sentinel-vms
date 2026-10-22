#if DEBUG
import Foundation
import SentinelCore

/// Debug builds only: sign in and pick a workspace from launch arguments,
/// for screenshot rigs that must not drive the GUI.
///
///     HandoffGridSentinel --debug-site-url http://127.0.0.1:28290 \
///         --debug-user admin [--debug-password …] [--debug-tab pane:insights]
///
/// The password may come from `SENTINEL_DEBUG_PASSWORD` instead (keeps it
/// out of `ps`). A debug launch keeps its sites in a separate defaults
/// suite and its tokens in memory, so it never touches the real sign-ins,
/// and it skips the one-copy-at-a-time check.
struct DebugLaunch {
    var siteURL: String
    var user: String
    var password: String
    /// A tab id to show after sign-in: a `FixedTab` raw value, `view:<id>` or `pane:<RailPane>`,
    /// or `events` for the Events page.
    var tab: String?

    static let current: DebugLaunch? = parse(CommandLine.arguments, environment: ProcessInfo.processInfo.environment)

    static func parse(_ args: [String], environment: [String: String]) -> DebugLaunch? {
        func value(_ flag: String) -> String? {
            guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
            return args[i + 1]
        }
        guard let url = value("--debug-site-url"), let user = value("--debug-user"),
              let password = value("--debug-password") ?? environment["SENTINEL_DEBUG_PASSWORD"] else { return nil }
        return DebugLaunch(siteURL: url, user: user, password: password, tab: value("--debug-tab"))
    }

    static let defaults = UserDefaults(suiteName: "com.handoffgrid.sentinel.debug-launch") ?? .standard

    @MainActor
    static func makeSession() -> OperatorSession {
        return OperatorSession(sites: SiteDirectory(defaults: defaults), tokenStore: MemoryTokenStore())
    }
}
#endif
