import Foundation

extension V2 {
    /// One message of `GET /api/v2/events/stream` (`StreamMessage`), decoded
    /// by `type`. Unknown types are kept as `.unknown` so a newer server never
    /// breaks the stream.
    enum StreamMessage: Hashable, Sendable {
        struct Hello: Codable, Hashable, Sendable {
            var user: User
            @NullableArray var topics: [String]
            var serverVersion: String
        }
        struct CameraOnline: Codable, Hashable, Sendable {
            var cameraId: String
            var online: Bool
        }
        struct CameraActivity: Codable, Hashable, Sendable {
            var cameraId: String
            var kind: String
        }
        struct ConfigChanged: Codable, Hashable, Sendable {
            @NullableArray var cameraIds: [String]
        }
        struct RecorderOnline: Codable, Hashable, Sendable {
            var recorderId: String
            var online: Bool
        }
        struct Lagged: Codable, Hashable, Sendable {
            var dropped: Int
        }

        case hello(Hello)
        case alarmNew(Alarm)
        case alarmUpdated(Alarm)
        case eventNew(Event)
        case eventUpdated(Event)
        case cameraStatus(CameraOnline)
        case cameraActivity(CameraActivity)
        case configChanged(ConfigChanged)
        case recorderStatus(RecorderOnline)
        case lagged(Lagged)
        case unknown(type: String)

        private struct Envelope: Decodable {
            var type: String
            var at: UnixMillis
        }

        private struct DataBox<T: Decodable>: Decodable {
            var data: T
        }

        /// Decodes one text frame. Throws for malformed JSON or a known type
        /// whose `data` doesn't match the contract.
        static func decode(_ data: Data, using decoder: JSONDecoder = JSONDecoder()) throws -> (message: StreamMessage, at: UnixMillis) {
            let envelope = try decoder.decode(Envelope.self, from: data)
            func body<T: Decodable>(_: T.Type) throws -> T {
                try decoder.decode(DataBox<T>.self, from: data).data
            }
            let message: StreamMessage
            switch envelope.type {
            case "hello": message = .hello(try body(Hello.self))
            case "alarm.new": message = .alarmNew(try body(Alarm.self))
            case "alarm.updated": message = .alarmUpdated(try body(Alarm.self))
            case "event.new": message = .eventNew(try body(Event.self))
            case "event.updated": message = .eventUpdated(try body(Event.self))
            case "camera.status": message = .cameraStatus(try body(CameraOnline.self))
            case "camera.activity": message = .cameraActivity(try body(CameraActivity.self))
            case "config.changed": message = .configChanged(try body(ConfigChanged.self))
            case "recorder.status": message = .recorderStatus(try body(RecorderOnline.self))
            case "lagged": message = .lagged(try body(Lagged.self))
            default: message = .unknown(type: envelope.type)
            }
            return (message, envelope.at)
        }
    }
}
