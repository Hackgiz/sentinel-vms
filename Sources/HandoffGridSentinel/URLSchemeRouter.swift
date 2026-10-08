import AppKit
import Foundation
import SentinelCore
import SentinelMediaServer

// Sentinel registers `sentinel://` so Shortcuts, AppleScript, and ⌘-clicked
// links in other apps can drive the UI without a full IPC layer.
//
// Supported URL shapes:
//   sentinel://section/<rawValue>            → switch to a SentinelSection
//   sentinel://camera/<uuid>                 → open Live view focused on a camera
//   sentinel://incident/new                  → create incident from latest alert
//   sentinel://export/<clip-uuid>            → start an evidence export
//   sentinel://billing                       → re-check license after Stripe checkout
//
// Unknown URLs are ignored (silent no-op) so a typo in a Shortcut doesn't crash
// the app or open a confusing error dialog mid-shift.
@MainActor
enum URLSchemeRouter {
    static let scheme = "sentinel"

    static func handle(_ urls: [URL], commandCenter: SentinelCommandCenter, cameraStore: CameraStore, licenseStore: LicenseStore) {
        for url in urls {
            guard url.scheme?.lowercased() == scheme else { continue }
            route(url, commandCenter: commandCenter, cameraStore: cameraStore, licenseStore: licenseStore)
        }
    }

    private static func route(_ url: URL, commandCenter: SentinelCommandCenter, cameraStore: CameraStore, licenseStore: LicenseStore) {
        // For sentinel://host/path style URLs, `host` carries the verb.
        let verb = (url.host ?? "").lowercased()
        let pathComponents = url.pathComponents.filter { $0 != "/" }
        guard let primary = pathComponents.first else {
            applyVerb(verb, argument: nil, commandCenter: commandCenter, cameraStore: cameraStore, licenseStore: licenseStore)
            return
        }
        applyVerb(verb, argument: primary, commandCenter: commandCenter, cameraStore: cameraStore, licenseStore: licenseStore)
    }

    private static func applyVerb(
        _ verb: String,
        argument: String?,
        commandCenter: SentinelCommandCenter,
        cameraStore: CameraStore,
        licenseStore: LicenseStore
    ) {
        switch verb {
        case "billing":
            // Stripe redirects here after checkout/portal; pull the latest entitlement.
            Task { await licenseStore.refreshEntitlement() }

        case "section":
            guard let raw = argument else { return }
            // Back-compat: "Playback" was merged into the Live view.
            if raw.lowercased() == "playback" {
                commandCenter.requestOpen(.live)
                return
            }
            guard let section = SentinelSection(rawValue: raw) ?? SentinelSection.allCases.first(where: { $0.id.lowercased() == raw.lowercased() }) else {
                return
            }
            commandCenter.requestOpen(section)

        case "camera":
            guard let arg = argument, let uuid = UUID(uuidString: arg) else { return }
            guard cameraStore.cameras.contains(where: { $0.id == uuid }) else { return }
            commandCenter.requestOpen(.live)

        case "incident":
            guard argument?.lowercased() == "new" else { return }
            commandCenter.requestCreateIncident()

        default:
            return
        }
    }
}
