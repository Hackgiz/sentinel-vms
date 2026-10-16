import Foundation

// Pure logic for the stream-settings sheet (port of web `streamSettings.ts`).

/// What an edit form holds for one stream.
public struct StreamForm: Equatable, Sendable {
    public var width: Int
    public var height: Int
    public var fps: Int
    public var bitrate: Int
    public var govLength: Int
    public var quality: Int

    public init(width: Int, height: Int, fps: Int, bitrate: Int, govLength: Int, quality: Int) {
        self.width = width; self.height = height; self.fps = fps
        self.bitrate = bitrate; self.govLength = govLength; self.quality = quality
    }

    public init(_ stream: V2.EncoderStream) {
        self.init(width: stream.current.width, height: stream.current.height, fps: stream.current.fps,
                  bitrate: stream.current.bitrate, govLength: stream.current.govLength, quality: stream.current.quality)
    }
}

public enum StreamSettingsModel {
    public static func resolutionKey(_ w: Int, _ h: Int) -> String { "\(w)x\(h)" }

    public static func resolutionLabel(_ r: V2.StreamResolution) -> String { "\(r.width)×\(r.height)" }

    /// The resolutions to offer: the camera's list, with the current one always present.
    public static func resolutionChoices(_ s: V2.EncoderStream) -> [V2.StreamResolution] {
        var list = s.options.resolutions
        let current = V2.StreamResolution(width: s.current.width, height: s.current.height)
        if list.contains(current) == false { list.insert(current, at: 0) }
        return list
    }

    /// A range the camera did not report (max 0) is free-form input.
    public static func isBounded(_ r: V2.StreamRange) -> Bool { r.max > 0 }

    /// Sliders only where the range is tight enough to be usable.
    public static func usesSlider(_ r: V2.StreamRange) -> Bool { isBounded(r) && r.max - r.min <= 100 }

    /// Clamps into the range; an unbounded range only enforces a minimum of 1.
    public static func clamp(_ v: Int, _ r: V2.StreamRange) -> Int {
        guard isBounded(r) else { return Swift.max(1, v) }
        return Swift.min(Swift.max(v, r.min), r.max)
    }

    /// Is `v` acceptable (an unbounded range accepts any positive whole number)?
    public static func inRange(_ v: Int, _ r: V2.StreamRange) -> Bool {
        guard v > 0 else { return false }
        return isBounded(r) == false || (v >= r.min && v <= r.max)
    }

    /// The PUT body: only what differs from the camera's current values.
    /// Width and height travel together. nil when nothing changed.
    public static func change(from s: V2.EncoderStream, to f: StreamForm) -> V2.StreamSettingsChange? {
        var c = V2.StreamSettingsChange()
        if f.width != s.current.width || f.height != s.current.height {
            c.width = f.width
            c.height = f.height
        }
        if f.fps != s.current.fps { c.fps = f.fps }
        if f.bitrate != s.current.bitrate { c.bitrate = f.bitrate }
        if f.govLength != s.current.govLength { c.govLength = f.govLength }
        if f.quality != s.current.quality { c.quality = f.quality }
        return c.isEmpty ? nil : c
    }

    public static func isDirty(_ s: V2.EncoderStream, _ f: StreamForm) -> Bool { change(from: s, to: f) != nil }

    /// Changing picture size or frame rate makes the camera restart its stream.
    public static func reconnects(_ c: V2.StreamSettingsChange?) -> Bool {
        guard let c else { return false }
        return c.width != nil || c.fps != nil
    }

    /// First problem with the *changed* fields against the camera's ranges, or nil.
    public static func problem(_ s: V2.EncoderStream, _ f: StreamForm) -> String? {
        let o = s.options
        let c = s.current
        if f.fps != c.fps, inRange(f.fps, o.fps) == false { return rangeText("Frame rate", o.fps) }
        if f.bitrate != c.bitrate, inRange(f.bitrate, o.bitrate) == false { return rangeText("Bitrate", o.bitrate) }
        if f.govLength != c.govLength, inRange(f.govLength, o.gov) == false { return rangeText("Keyframe interval", o.gov) }
        if f.quality != c.quality, inRange(f.quality, o.quality) == false { return rangeText("Quality", o.quality) }
        return nil
    }

    private static func rangeText(_ name: String, _ r: V2.StreamRange) -> String {
        isBounded(r) ? "\(name) must be a whole number from \(r.min) to \(r.max)." : "\(name) must be a positive whole number."
    }

    public static func title(_ s: V2.EncoderStream) -> String {
        switch s.inUse {
        case "main": return "Main stream"
        case "sub": return "Sub stream"
        default: return s.name.isEmpty ? s.profileToken : s.name
        }
    }

    /// Every load failure (409 without ONVIF, 502 unreachable, a camera
    /// that answers nothing useful) reads as "unsupported" rather than an
    /// error toast. `detail` is the server's text, for a quiet secondary line.
    public static func unsupportedDetail(_ error: Error) -> String {
        if let api = error as? SentinelAPIError, case .server(_, _, let message) = api { return message }
        return error.localizedDescription
    }

    public static let unsupportedText = "This camera doesn't support remote configuration."
}
