import Foundation

/// Where playback opens when the operator jumps from an event or an alarm to
/// its recording (PARITY-342 §3): a few seconds before it, so the lead-up is
/// on screen. The same pre-rolls on every client.
public enum PlaybackJump {
    /// Seconds of footage before a detection event.
    public static let eventPreRoll: TimeInterval = 5
    /// Seconds of footage before an alarm.
    public static let alarmPreRoll: TimeInterval = 10

    public static func eventStart(_ createdAt: UnixMillis) -> Date {
        Date(unixMillis: createdAt).addingTimeInterval(-eventPreRoll)
    }

    public static func alarmStart(_ createdAt: UnixMillis) -> Date {
        Date(unixMillis: createdAt).addingTimeInterval(-alarmPreRoll)
    }

    public static func start(of event: V2.Event) -> Date { eventStart(event.createdAt) }

    public static func start(of alarm: V2.Alarm) -> Date { alarmStart(alarm.createdAt) }
}
