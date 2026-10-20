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
