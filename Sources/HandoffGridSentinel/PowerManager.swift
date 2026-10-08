import Foundation
import IOKit.pwr_mgt

/// Holds an IOKit power assertion so macOS won't idle-sleep the system while
/// Sentinel is recording (or, optionally, the whole time the app is open).
///
/// Uses `kIOPMAssertPreventUserIdleSystemSleep`: the *system* stays awake so
/// recording continues, but the *display* is still allowed to sleep — exactly
/// what a headless recorder wants (no wasted backlight, no interrupted capture).
///
/// Caveat this can't fix: closing a laptop lid triggers clamshell sleep
/// regardless of any assertion, unless the Mac is on AC power with an external
/// display attached. For 24/7 recording, run on AC power and either keep the
/// lid open or use an external display.
@MainActor
final class PowerManager: ObservableObject {
    /// True while an assertion is actually held (drives the UI indicator).
    @Published private(set) var isHoldingAssertion = false

    /// User preference: keep the Mac awake the entire time the app runs, not
    /// only while a recording is active. Persisted across launches.
    @Published var keepAwakeAlways: Bool {
        didSet {
            UserDefaults.standard.set(keepAwakeAlways, forKey: Self.alwaysKey)
            reevaluate()
        }
    }

    private var assertionID = IOPMAssertionID(0)
    private var hasActiveRecordings = false
    private static let alwaysKey = "handoffgrid.keepAwakeAlways"

    init() {
        keepAwakeAlways = UserDefaults.standard.bool(forKey: Self.alwaysKey)
    }

    /// Called whenever the set of active recordings changes.
    func updateRecordingState(isRecording: Bool) {
        hasActiveRecordings = isRecording
        reevaluate()
    }

    /// Human-readable status for the Settings panel.
    var statusDescription: String {
        if isHoldingAssertion {
            return keepAwakeAlways
                ? "Mac kept awake (always-on)"
                : "Mac kept awake while recording"
        }
        return "Mac may sleep when idle"
    }

    private func reevaluate() {
        let shouldHold = keepAwakeAlways || hasActiveRecordings
        if shouldHold {
            acquire()
        } else {
            release()
        }
    }

    private func acquire() {
        guard isHoldingAssertion == false else { return }
        var id = IOPMAssertionID(0)
        let reason = "Sentinel VMS is recording cameras" as CFString
        let result = IOPMAssertionCreateWithName(
            kIOPMAssertPreventUserIdleSystemSleep as CFString,
            IOPMAssertionLevel(kIOPMAssertionLevelOn),
            reason,
            &id
        )
        if result == kIOReturnSuccess {
            assertionID = id
            isHoldingAssertion = true
        }
    }

    private func release() {
        guard isHoldingAssertion else { return }
        IOPMAssertionRelease(assertionID)
        assertionID = IOPMAssertionID(0)
        isHoldingAssertion = false
    }
    // No deinit: this manager lives for the whole app lifetime, and macOS
    // auto-releases any held power assertion when the process exits.
}
