import Foundation

// Activity Reports (`GET /api/v2/reports/activity`, parity 3.4.2).

public extension V2 {
    /// Detections on one camera, of one kind, in one hour.
    struct ActivityEvent: Codable, Hashable, Sendable {
        public var cameraId: String
        public var kind: String
        /// The hour's start, Unix ms.
        public var hour: UnixMillis
        public var count: Int
        public init(cameraId: String, kind: String, hour: UnixMillis, count: Int) {
            self.cameraId = cameraId; self.kind = kind; self.hour = hour; self.count = count
        }
    }

    /// Alarms on one camera, of one kind and severity, over the whole range.
    struct ActivityAlarm: Codable, Hashable, Sendable {
        public var cameraId: String
        public var kind: String
        /// Critical, Warning or Info; kept as text so a new severity still counts.
        public var severity: String
        public var count: Int
        public init(cameraId: String, kind: String, severity: String, count: Int) {
            self.cameraId = cameraId; self.kind = kind; self.severity = severity; self.count = count
        }
    }

    struct ActivityTotals: Codable, Hashable, Sendable {
        public var events: Int
        public var alarms: Int
        public init(events: Int, alarms: Int) { self.events = events; self.alarms = alarms }
    }

    /// Counts for a range, already limited to the cameras the caller can see.
    struct Activity: Codable, Hashable, Sendable {
        public var from: UnixMillis
        public var to: UnixMillis
        public var totals: ActivityTotals
        @NullableArray public var events: [ActivityEvent]
        @NullableArray public var alarms: [ActivityAlarm]
        public init(from: UnixMillis, to: UnixMillis, totals: ActivityTotals, events: [ActivityEvent], alarms: [ActivityAlarm]) {
            self.from = from; self.to = to; self.totals = totals
            self.events = events; self.alarms = alarms
        }
    }
}
