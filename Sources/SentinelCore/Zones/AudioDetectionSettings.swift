import Foundation

// Audio event detection settings of a camera (wave 11 stream E). Mirrors
// `linux/web/src/admin/audioSettings.ts`.

public enum AudioDetectionSettings {
    public static let defaultDb = 25
    public static let minDb = 6
    public static let maxDb = 60
    public static var range: ClosedRange<Int> { minDb...maxDb }

    /// Clamps a typed or slid threshold into the range the server accepts.
    public static func clamp(_ v: Double) -> Int {
        guard v.isFinite else { return defaultDb }
        return min(maxDb, max(minDb, Int(v.rounded())))
    }

    public struct Form: Hashable, Sendable {
        public var audioDetect: Bool
        public var audioThresholdDb: Int

        public init(audioDetect: Bool = false, audioThresholdDb: Int = AudioDetectionSettings.defaultDb) {
            self.audioDetect = audioDetect
            self.audioThresholdDb = audioThresholdDb
        }
    }

    /// The form values of a stored camera (older servers omit the fields).
    public static func form(of camera: V2.Camera?) -> Form {
        Form(audioDetect: camera?.audioDetect ?? false, audioThresholdDb: camera?.audioThresholdDb ?? defaultDb)
    }

    /// Only the audio fields that changed, as a camera PATCH (empty when nothing did).
    public static func patch(_ camera: V2.Camera?, _ form: Form) -> V2.CameraPatch {
        let cur = Self.form(of: camera)
        var p = V2.CameraPatch()
        if form.audioDetect != cur.audioDetect { p.audioDetect = form.audioDetect }
        if form.audioThresholdDb != cur.audioThresholdDb { p.audioThresholdDb = form.audioThresholdDb }
        return p
    }

    /// Copies the audio fields of `audio` onto `patch` (for folding into a larger camera save).
    public static func merge(_ audio: V2.CameraPatch, into patch: inout V2.CameraPatch) {
        if let v = audio.audioDetect { patch.audioDetect = v }
        if let v = audio.audioThresholdDb { patch.audioThresholdDb = v }
    }
}

// Wave 16 (tracking), on the Mac in wave 17: "Loitering after", shown with
// the audio settings. A person track present this long raises one Loitering
// event; 0 = off. Mirrors the loiter helpers in the web's `audioSettings.ts`.
public enum LoiterSettings {
    public static let maxSeconds = 3600

    /// Clamps typed seconds into 0–3600 (anything unreadable is off).
    public static func clamp(_ v: Double) -> Int {
        guard v.isFinite, v > 0 else { return 0 }
        return min(maxSeconds, Int(v.rounded()))
    }

    /// A stored camera's loitering seconds (older servers omit the field).
    public static func seconds(of camera: V2.Camera?) -> Int { camera?.loiterSeconds ?? 0 }

    /// Copies a changed loitering time onto `patch`.
    public static func merge(_ camera: V2.Camera?, _ seconds: Int, into patch: inout V2.CameraPatch) {
        if seconds != Self.seconds(of: camera) { patch.loiterSeconds = seconds }
    }

    /// One line under the field.
    public static func hint(_ seconds: Int) -> String {
        if seconds <= 0 { return "Off. Set a number of seconds to raise a Loitering event when a person stays that long." }
        let m = seconds / 60, s = seconds % 60
        let t = m > 0 ? "\(m) min\(s > 0 ? " \(s) s" : "")" : "\(s) s"
        return "A person seen for \(t) or more raises one Loitering event (a Warning alarm). Needs object detection on this camera."
    }
}
