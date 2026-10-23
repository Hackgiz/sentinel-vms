import Foundation

// Recording modes (wave 22): the view-free logic behind the Mac camera
// editor's Recording section, the cameras list badge, the Recording defaults
// pane and the timeline's low-res bands. Wording follows WAVE22-RECORDING-MODES.md
// and the web's camera editor, so every client says the same thing.

public enum RecordingModeLogic {
    // MARK: Ranges (the server clamps too)

    public static let preRollRange = 0...30
    public static let postRollRange = 5...120
    public static let lowResRetentionMax = 365
    public static let retentionMax = 3650
    public static let minFreeRange = 5...50

    // MARK: Modes

    /// One row of the mode picker.
    public struct ModeChoice: Hashable, Identifiable, Sendable {
        public var mode: V2.RecordingMode
        public var label: String
        public var detail: String
        public var recommended: Bool
        public var enabled: Bool
        public var id: String { mode.rawValue }
    }

    /// The four concrete modes, in picker order.
    public static let modes: [V2.RecordingMode] = [.continuous, .events, .dual, .off]

    /// The picker label of a mode ("Continuous", "Events only", …).
    public static func label(_ mode: V2.RecordingMode) -> String {
        switch mode {
        case .continuous: return "Continuous"
        case .events: return "Events only"
        case .dual: return "Low-res 24/7 + full-res on events"
        case .off: return "Off (live only)"
        case .default: return "Server default"
        default: return mode.rawValue.capitalized
        }
    }

    /// The one-line explanation under a mode.
    public static func detail(_ mode: V2.RecordingMode) -> String {
        switch mode {
        case .continuous: return "Records the main stream around the clock."
        case .events: return "Keeps full resolution only around motion and detections, with a few seconds before and after."
        case .dual: return "Keeps the sub-stream around the clock, plus full resolution around events. Uses far less disk than Continuous."
        case .off: return "Nothing is recorded. Live view and alarms still work."
        case .default: return "Follows the server's Recording defaults."
        default: return ""
        }
    }

    /// The mode marked "Recommended": dual when the camera has a sub-stream, else continuous.
    public static func recommended(hasSubStream: Bool) -> V2.RecordingMode { hasSubStream ? .dual : .continuous }

    /// The camera editor's picker: "Server default" first, then the four modes.
    /// `dual` is disabled without a sub-stream.
    public static func choices(hasSubStream: Bool, serverDefault: V2.RecordingMode?) -> [ModeChoice] {
        let rec = recommended(hasSubStream: hasSubStream)
        var out = [ModeChoice(mode: .default, label: defaultLabel(serverDefault, hasSubStream: hasSubStream),
                              detail: detail(.default), recommended: false, enabled: true)]
        for mode in modes {
            out.append(ModeChoice(mode: mode, label: label(mode),
                                  detail: mode == .dual && hasSubStream == false ? StreamFinderLogic.dualHint : detail(mode),
                                  recommended: mode == rec, enabled: mode != .dual || hasSubStream))
        }
        return out
    }

    /// The server default's picker choices (no "Server default", dual always allowed;
    /// cameras without a sub-stream fall back to Events only).
    public static func defaultChoices() -> [ModeChoice] {
        modes.map { mode in
            ModeChoice(mode: mode, label: label(mode),
                       detail: mode == .dual ? detail(mode) + " Cameras without a sub-stream use Events only." : detail(mode),
                       recommended: false, enabled: true)
        }
    }

    /// "Server default (Continuous)", or plain "Server default" when it isn't known.
    public static func defaultLabel(_ serverDefault: V2.RecordingMode?, hasSubStream: Bool) -> String {
        guard let serverDefault else { return label(.default) }
        return "\(label(.default)) (\(label(resolve(.default, serverDefault: serverDefault, hasSubStream: hasSubStream))))"
    }

    /// What a camera set to `mode` records: `default` takes the server default,
    /// and `dual` without a sub-stream falls back to `events` (as the recorder does).
    public static func resolve(_ mode: V2.RecordingMode, serverDefault: V2.RecordingMode?, hasSubStream: Bool) -> V2.RecordingMode {
        var m = mode == .default ? (serverDefault ?? .continuous) : mode
        if m == .dual && hasSubStream == false { m = .events }
        return m
    }

    /// True when the server knows recording modes (it sent `recordingMode`).
    public static func supportsModes(_ camera: V2.Camera?) -> Bool { camera?.recordingMode != nil }

    /// What the camera actually does: the server's `effectiveRecordingMode`, else
    /// its own mode when concrete, else the old `recording` flag.
    public static func effective(_ camera: V2.Camera) -> V2.RecordingMode {
        if let e = camera.effectiveRecordingMode, e != .default { return e }
        if let m = camera.recordingMode, m != .default { return m }
        return camera.recording ? .continuous : .off
    }

    /// The short badge: "24/7", "Events", "24/7 low + events", "Live only".
    public static func shortLabel(_ mode: V2.RecordingMode) -> String {
        switch mode {
        case .continuous: return "24/7"
        case .events: return "Events"
        case .dual: return "24/7 low + events"
        case .off: return "Live only"
        default: return mode.rawValue.capitalized
        }
    }

    /// The cameras list's Recording column. Older servers keep "On · 7 d" / "Off".
    public static func listLabel(_ camera: V2.Camera) -> String {
        let days = camera.retentionDays > 0 ? " · \(camera.retentionDays) d" : ""
        guard camera.recordingMode != nil || camera.effectiveRecordingMode != nil else {
            return camera.recording ? "On\(days)" : "Off"
        }
        let mode = effective(camera)
        return shortLabel(mode) + (mode == .off ? "" : days)
    }

    // MARK: Triggers

    public static let triggers: [V2.RecordTrigger] = [.motion, .objects]

    public static func triggerLabel(_ trigger: V2.RecordTrigger) -> String {
        switch trigger {
        case .motion: return "Any motion"
        case .objects: return "People and vehicles"
        case .default: return "Server default"
        default: return trigger.rawValue.capitalized
        }
    }

    public static func triggerDetail(_ trigger: V2.RecordTrigger) -> String {
        switch trigger {
        case .motion: return "Motion, Sentinel detections and the camera's own events start a recording."
        case .objects: return "Only people and vehicles start a recording. Wind, rain and pixel motion don't."
        default: return "Follows the server's Recording defaults."
        }
    }

    /// The warning when an events/dual camera has nothing to start a recording.
    public static func noTriggerWarning(effective mode: V2.RecordingMode, motionLevel: Int, cameraAnalytics: Bool) -> String? {
        guard mode == .events || mode == .dual, motionLevel == 0, cameraAnalytics == false else { return nil }
        return "Nothing will start a recording. Turn on motion detection or camera analytics."
    }

    /// True when the mode keeps windows around events (trigger and pre/post-roll apply).
    public static func usesEvents(_ mode: V2.RecordingMode) -> Bool { mode == .events || mode == .dual }

    // MARK: Pre/post-roll and retention

    /// "Server default (5 s)" at 0, else "12 s".
    public static func secondsLabel(_ seconds: Int, serverDefault: Int?) -> String {
        if seconds <= 0 { return serverDefault.map { "server default (\($0) s)" } ?? "server default" }
        return "\(seconds) s"
    }

    /// "Server default (14 days)" at 0, else "30 days".
    public static func daysLabel(_ days: Int, serverDefault: Int?) -> String {
        if days <= 0 { return serverDefault.map { "server default (\($0) day\($0 == 1 ? "" : "s"))" } ?? "server default" }
        return "\(days) day\(days == 1 ? "" : "s")"
    }

    /// Pre-roll stepper: 0 (default), 1 … 30.
    public static func stepPreRoll(_ v: Int, up: Bool) -> Int {
        up ? min(preRollRange.upperBound, max(0, v) + 1) : max(0, v - 1)
    }

    /// Post-roll stepper: 0 (default), then 5 … 120 in 5 s steps.
    public static func stepPostRoll(_ v: Int, up: Bool) -> Int {
        if up { return v < postRollRange.lowerBound ? postRollRange.lowerBound : min(postRollRange.upperBound, v + 5) }
        return v <= postRollRange.lowerBound ? 0 : v - 5
    }

    /// Free-space deletion: 0 = off, else clamped into 5–50.
    public static func clampMinFree(_ v: Int) -> Int {
        v <= 0 ? 0 : min(minFreeRange.upperBound, max(minFreeRange.lowerBound, v))
    }

    /// "Off", or "Delete the oldest video when free space drops under 10 %".
    public static func minFreeLabel(_ percent: Int) -> String {
        percent <= 0 ? "Off. Old video is deleted only by the retention days."
            : "Delete the oldest video when free space drops under \(percent) %. Low-res goes before full-res; evidence is never touched."
    }

    // MARK: Camera form

    /// The editor's Recording fields of one camera.
    public struct Form: Hashable, Sendable {
        public var mode: V2.RecordingMode
        public var trigger: V2.RecordTrigger
        public var preRollSeconds: Int
        public var postRollSeconds: Int
        public var lowResRetentionDays: Int

        public init(mode: V2.RecordingMode = .default, trigger: V2.RecordTrigger = .default,
                    preRollSeconds: Int = 0, postRollSeconds: Int = 0, lowResRetentionDays: Int = 0) {
            self.mode = mode
            self.trigger = trigger
            self.preRollSeconds = preRollSeconds
            self.postRollSeconds = postRollSeconds
            self.lowResRetentionDays = lowResRetentionDays
        }
    }

    /// A stored camera's form (a new camera follows the server defaults).
    public static func form(of camera: V2.Camera?) -> Form {
        guard let camera else { return Form() }
        return Form(mode: camera.recordingMode ?? (camera.recording ? .default : .off),
                    trigger: camera.recordTrigger ?? .default,
                    preRollSeconds: camera.preRollSeconds ?? 0, postRollSeconds: camera.postRollSeconds ?? 0,
                    lowResRetentionDays: camera.lowResRetentionDays ?? 0)
    }

    /// Copies the changed recording fields onto `patch` (all of them for a new camera).
    public static func merge(_ camera: V2.Camera?, _ form: Form, into patch: inout V2.CameraPatch) {
        let cur = Self.form(of: camera)
        let isNew = camera == nil
        if isNew || form.mode != cur.mode || camera?.recordingMode == nil { patch.recordingMode = form.mode }
        if isNew || form.trigger != cur.trigger { patch.recordTrigger = form.trigger }
        if isNew || form.preRollSeconds != cur.preRollSeconds { patch.preRollSeconds = form.preRollSeconds }
        if isNew || form.postRollSeconds != cur.postRollSeconds { patch.postRollSeconds = form.postRollSeconds }
        if isNew || form.lowResRetentionDays != cur.lowResRetentionDays { patch.lowResRetentionDays = form.lowResRetentionDays }
    }

    /// A new camera's recording fields on its create body.
    public static func apply(_ form: Form, to input: inout V2.CameraInput) {
        input.recordingMode = form.mode
        input.recordTrigger = form.trigger
        input.preRollSeconds = form.preRollSeconds
        input.postRollSeconds = form.postRollSeconds
        input.lowResRetentionDays = form.lowResRetentionDays
    }

    // MARK: Server defaults form

    /// Only the changed defaults, as a PATCH (empty when nothing changed).
    public static func patch(from old: V2.RecordingSettings, to new: V2.RecordingSettings) -> V2.RecordingSettingsPatch {
        var p = V2.RecordingSettingsPatch()
        if new.defaultMode != old.defaultMode { p.defaultMode = new.defaultMode }
        if new.defaultTrigger != old.defaultTrigger { p.defaultTrigger = new.defaultTrigger }
        if new.defaultPreRollSeconds != old.defaultPreRollSeconds { p.defaultPreRollSeconds = new.defaultPreRollSeconds }
        if new.defaultPostRollSeconds != old.defaultPostRollSeconds { p.defaultPostRollSeconds = new.defaultPostRollSeconds }
        if new.defaultRetentionDays != old.defaultRetentionDays { p.defaultRetentionDays = new.defaultRetentionDays }
        if new.defaultLowResRetentionDays != old.defaultLowResRetentionDays { p.defaultLowResRetentionDays = new.defaultLowResRetentionDays }
        if new.minFreePercent != old.minFreePercent { p.minFreePercent = clampMinFree(new.minFreePercent) }
        return p
    }

    /// The pane's error line: a 404 means the server predates wave 22.
    public static func errorText(_ error: Error) -> String {
        if let api = error as? SentinelAPIError, api.isNotFound {
            return "This server doesn't offer recording modes. Update Sentinel on the Management Server to use them."
        }
        return (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }

    // MARK: Timeline

    public static let lowResLegend = "Low-res"
    public static let lowResHelp = "Lighter band: low-res footage (the sub-stream recorded 24/7). Full resolution is kept around events."

    /// The low-res stretches of a timeline, merged.
    public static func lowResRanges(_ spans: [V2.Span]) -> [TimelineGaps.Range] {
        TimelineGaps.merge(spans.filter(\.isLowRes).map { TimelineGaps.Range(start: $0.start, end: $0.end) })
    }

    /// True when any low-res span overlaps [from, to).
    public static func hasLowRes(_ spans: [V2.Span], from: Int64, to: Int64) -> Bool {
        spans.contains { $0.isLowRes && $0.end > from && $0.start < to }
    }
}
