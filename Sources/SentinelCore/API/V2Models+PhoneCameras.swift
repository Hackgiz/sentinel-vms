import Foundation

// Wave 23 phone cameras (WAVE23-PHONE-CAMERA.md §A). The iPhone creates them;
// the Mac only rotates or revokes the push key.

public extension V2 {
    /// Answer of `POST /api/v2/phone-cameras` and `POST /api/v2/cameras/{id}/push-key`:
    /// the push key is shown once (the server keeps only its hash).
    struct PhonePushKey: Codable, Hashable, Sendable {
        public var pushKey: String
        /// The camera, when the server includes it.
        public var camera: Camera?

        public init(pushKey: String, camera: Camera? = nil) {
            self.pushKey = pushKey
            self.camera = camera
        }
    }

    /// Body of `POST /api/v2/phone-cameras`.
    struct PhoneCameraInput: Codable, Hashable, Sendable {
        public var name: String
        public var recorderId: String?

        public init(name: String, recorderId: String? = nil) {
            self.name = name
            self.recorderId = recorderId
        }
    }
}
