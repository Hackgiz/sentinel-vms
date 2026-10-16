import Foundation

// Floorplan DTOs (`/api/v2/floorplans*`). Wave 5 stream B.

public extension V2 {
    struct FloorplanPin: Codable, Hashable, Sendable {
        public var cameraId: String
        /// Fraction of the image width, 0...1.
        public var x: Double
        /// Fraction of the image height, 0...1.
        public var y: Double
        /// Degrees clockwise from up (0-359), -1 = none.
        public var heading: Double

        public init(cameraId: String, x: Double, y: Double, heading: Double = -1) {
            self.cameraId = cameraId
            self.x = x
            self.y = y
            self.heading = heading
        }
    }

    struct Floorplan: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var sortOrder: Int
        /// Image size in pixels.
        public var width: Int
        public var height: Int
        public var mime: String
        public var updatedAt: UnixMillis
        @NullableArray public var pins: [FloorplanPin]

        public init(id: String, name: String, sortOrder: Int = 0, width: Int, height: Int,
                    mime: String = "image/png", updatedAt: UnixMillis = 0, pins: [FloorplanPin] = []) {
            self.id = id
            self.name = name
            self.sortOrder = sortOrder
            self.width = width
            self.height = height
            self.mime = mime
            self.updatedAt = updatedAt
            self.pins = pins
        }
    }
}
