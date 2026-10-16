import Foundation

// PTZ DTOs (`/api/v2/cameras/{id}/ptz*`). Wave 5 stream B.

public extension V2.Permission {
    /// Needed for the PTZ probe, moves and presets.
    static let controlPtz = V2.Permission(rawValue: "controlPtz")
}

public extension V2 {
    struct PTZPreset: Codable, Hashable, Identifiable, Sendable {
        public var token: String
        public var name: String
        public var id: String { token }

        public init(token: String, name: String) {
            self.token = token
            self.name = name
        }
    }

    /// `GET /cameras/{id}/ptz`: whether the camera has ONVIF PTZ, and its presets.
    struct PTZState: Codable, Hashable, Sendable {
        public var supported: Bool
        @NullableArray public var presets: [PTZPreset]

        public init(supported: Bool, presets: [PTZPreset] = []) {
            self.supported = supported
            self.presets = presets
        }
    }

    /// Continuous-move velocities, each -1...1 (the server clamps too).
    struct PTZMove: Codable, Hashable, Sendable {
        public var pan: Double
        public var tilt: Double
        public var zoom: Double

        public init(pan: Double, tilt: Double, zoom: Double) {
            self.pan = pan
            self.tilt = tilt
            self.zoom = zoom
        }

        /// The same move with every axis clamped to [-1, 1] (NaN becomes 0).
        public var clamped: PTZMove {
            func c(_ v: Double) -> Double { v.isNaN ? 0 : min(1, max(-1, v)) }
            return PTZMove(pan: c(pan), tilt: c(tilt), zoom: c(zoom))
        }

        public var isStill: Bool { pan == 0 && tilt == 0 && zoom == 0 }
    }
}
