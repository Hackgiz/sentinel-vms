import Foundation

// Wave 22 addendum: finding a camera's second stream
// (`POST /api/v2/cameras/{id}/streams/discover` and `…/streams/resize`).
// Decoding is lenient: a field the server leaves out gets an empty value, so
// a newer or older server never breaks the picker.

public extension V2 {
    /// One stream the camera offers. `url` never carries credentials.
    struct DiscoveredStream: Codable, Hashable, Identifiable, Sendable {
        public var url: String
        /// e.g. "1280×720 · H.264 · 15 fps"
        public var label: String
        public var width: Int
        public var height: Int
        public var fps: Double
        /// "H264" | "H265" | "MJPEG" | anything else.
        public var codec: String
        /// "onvif" or "guess" (a URL pattern that answered RTSP DESCRIBE).
        public var source: String
        /// What the camera uses it for now: "main", "sub" or "".
        public var role: String
        /// Present when the stream can be resized over ONVIF.
        public var encoderToken: String?

        public var id: String { url }

        public init(url: String, label: String = "", width: Int = 0, height: Int = 0, fps: Double = 0,
                    codec: String = "", source: String = "onvif", role: String = "", encoderToken: String? = nil) {
            self.url = url; self.label = label; self.width = width; self.height = height; self.fps = fps
            self.codec = codec; self.source = source; self.role = role; self.encoderToken = encoderToken
        }

        enum CodingKeys: String, CodingKey { case url, label, width, height, fps, codec, source, role, encoderToken }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            url = try c.decode(String.self, forKey: .url)
            label = try c.decodeIfPresent(String.self, forKey: .label) ?? ""
            width = try c.decodeIfPresent(Int.self, forKey: .width) ?? 0
            height = try c.decodeIfPresent(Int.self, forKey: .height) ?? 0
            fps = try c.decodeIfPresent(Double.self, forKey: .fps) ?? 0
            codec = try c.decodeIfPresent(String.self, forKey: .codec) ?? ""
            source = try c.decodeIfPresent(String.self, forKey: .source) ?? ""
            role = try c.decodeIfPresent(String.self, forKey: .role) ?? ""
            let token = try c.decodeIfPresent(String.self, forKey: .encoderToken)
            encoderToken = token?.isEmpty == true ? nil : token
        }
    }

    /// The only extra stream is smaller than 640×360 but can be resized.
    struct TooSmallSub: Codable, Hashable, Sendable {
        public var url: String
        public var encoderToken: String
        public var width: Int
        public var height: Int
        public init(url: String, encoderToken: String, width: Int, height: Int) {
            self.url = url; self.encoderToken = encoderToken; self.width = width; self.height = height
        }
    }

    /// The discover answer.
    struct StreamDiscovery: Codable, Hashable, Sendable {
        @NullableArray public var streams: [DiscoveredStream]
        /// URL of the stream the server suggests as the second stream.
        public var recommendedSub: String?
        public var tooSmallSub: TooSmallSub?

        public init(streams: [DiscoveredStream], recommendedSub: String? = nil, tooSmallSub: TooSmallSub? = nil) {
            self.streams = streams; self.recommendedSub = recommendedSub; self.tooSmallSub = tooSmallSub
        }

        enum CodingKeys: String, CodingKey { case streams, recommendedSub, tooSmallSub }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            _streams = try c.decode(NullableArray<DiscoveredStream>.self, forKey: .streams)
            let rec = try c.decodeIfPresent(String.self, forKey: .recommendedSub)
            recommendedSub = rec?.isEmpty == true ? nil : rec
            tooSmallSub = try c.decodeIfPresent(TooSmallSub.self, forKey: .tooSmallSub)
        }
    }

    /// Discover body: both absent = use the camera's saved login.
    struct StreamDiscoverRequest: Codable, Hashable, Sendable {
        public var username: String?
        public var password: String?
        public init(username: String? = nil, password: String? = nil) {
            self.username = username; self.password = password
        }
    }

    struct StreamResizeRequest: Codable, Hashable, Sendable {
        public var encoderToken: String
        public var width: Int
        public var height: Int
        public var fps: Int?
        public init(encoderToken: String, width: Int, height: Int, fps: Int? = nil) {
            self.encoderToken = encoderToken; self.width = width; self.height = height; self.fps = fps
        }
    }
}
