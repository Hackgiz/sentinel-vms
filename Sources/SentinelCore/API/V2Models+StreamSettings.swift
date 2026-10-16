import Foundation

// Encoder settings per stream (`/api/v2/cameras/{id}/stream-settings`).

public extension V2 {
    /// A range the camera accepts. `max == 0` means the camera did not
    /// report a limit: treat it as unbounded.
    struct StreamRange: Codable, Hashable, Sendable {
        public var min: Int
        public var max: Int
        public init(min: Int, max: Int) { self.min = min; self.max = max }
    }

    struct StreamCurrent: Codable, Hashable, Sendable {
        public var width: Int
        public var height: Int
        public var fps: Int
        /// kbit/s; 0 = not reported.
        public var bitrate: Int
        /// Keyframe interval in frames.
        public var govLength: Int
        public var quality: Int
        public init(width: Int, height: Int, fps: Int, bitrate: Int, govLength: Int, quality: Int) {
            self.width = width; self.height = height; self.fps = fps
            self.bitrate = bitrate; self.govLength = govLength; self.quality = quality
        }
    }

    struct StreamResolution: Codable, Hashable, Sendable {
        public var width: Int
        public var height: Int
        public init(width: Int, height: Int) { self.width = width; self.height = height }
    }

    struct StreamOptions: Codable, Hashable, Sendable {
        @NullableArray public var resolutions: [StreamResolution]
        public var fps: StreamRange
        public var bitrate: StreamRange
        public var gov: StreamRange
        public var quality: StreamRange
        public init(resolutions: [StreamResolution], fps: StreamRange, bitrate: StreamRange, gov: StreamRange, quality: StreamRange) {
            self.resolutions = resolutions; self.fps = fps; self.bitrate = bitrate
            self.gov = gov; self.quality = quality
        }
    }

    /// One encoder configuration (`Stream` in the contract).
    struct EncoderStream: Codable, Hashable, Identifiable, Sendable {
        public var configToken: String
        public var profileToken: String
        public var name: String
        /// `main`, `sub` or empty.
        public var inUse: String
        /// H264, JPEG, … (read-only).
        public var encoding: String
        public var current: StreamCurrent
        public var options: StreamOptions
        public var id: String { configToken }
        public init(configToken: String, profileToken: String, name: String, inUse: String, encoding: String,
                    current: StreamCurrent, options: StreamOptions) {
            self.configToken = configToken; self.profileToken = profileToken; self.name = name
            self.inUse = inUse; self.encoding = encoding; self.current = current; self.options = options
        }
    }

    struct StreamSettings: Codable, Hashable, Sendable {
        @NullableArray public var streams: [EncoderStream]
        public init(streams: [EncoderStream]) { self.streams = streams }
    }

    /// Body of the PUT: only what changed; zero or absent keeps the camera's value.
    struct StreamSettingsChange: Codable, Hashable, Sendable {
        public var width: Int?
        public var height: Int?
        public var fps: Int?
        public var bitrate: Int?
        public var govLength: Int?
        public var quality: Int?
        public init(width: Int? = nil, height: Int? = nil, fps: Int? = nil, bitrate: Int? = nil,
                    govLength: Int? = nil, quality: Int? = nil) {
            self.width = width; self.height = height; self.fps = fps
            self.bitrate = bitrate; self.govLength = govLength; self.quality = quality
        }

        public var isEmpty: Bool {
            width == nil && height == nil && fps == nil && bitrate == nil && govLength == nil && quality == nil
        }
    }
}
