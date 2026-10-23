import Foundation

// The view-free logic behind the Operator's Home workspace (wave 20): the
// stat tiles, what needs attention, recent activity, the camera stills and
// the dashboard's layout. It mirrors the iPhone's `HomeDashboard` and
// `LayoutLogic` so the unfolded Duo and the Mac show the same numbers, and
// uses only what the Operator already fetches (cameras, the open alarm
// queue, the recent event feed).

public enum HomeModel {
    /// How a tile's note is coloured.
    public enum Tone: String, Equatable, Sendable {
        case good, warning, bad, info
    }

    /// Which stat a tile shows; also what clicking it opens.
    public enum StatKind: String, CaseIterable, Equatable, Sendable {
        case cameras, recording, events, alarms
    }

    public struct Stat: Equatable, Identifiable, Sendable {
        public var kind: StatKind
        public var title: String
        public var symbol: String
        public var value: String
        public var note: String
        public var tone: Tone
        public var id: String { kind.rawValue }
    }

    // MARK: Stat tiles

    public static func onlineCount(_ cameras: [V2.Camera]) -> Int {
        cameras.filter(\.status.online).count
    }

    /// Cameras recording right now: recording on and online.
    public static func recordingCount(_ cameras: [V2.Camera]) -> Int {
        cameras.filter { $0.recording && $0.status.online }.count
    }

    /// Detections since local midnight among the events the feed holds.
    public static func eventsToday(_ events: [V2.Event], now: Date, calendar: Calendar = .current) -> Int {
        let start = calendar.startOfDay(for: now).unixMillis
        return events.filter { $0.createdAt >= start }.count
    }

    /// The four tiles, left to right: cameras online, recording, events
    /// today, open alarms.
    public static func stats(cameras: [V2.Camera], openAlarms: [V2.Alarm], events: [V2.Event],
                             now: Date, calendar: Calendar = .current) -> [Stat] {
        let total = cameras.count
        let online = onlineCount(cameras)
        let offline = total - online
        let recording = recordingCount(cameras)
        let today = eventsToday(events, now: now, calendar: calendar)
        let open = openAlarms.count
        let new = openAlarms.filter { $0.state == .new }.count
        return [
            Stat(kind: .cameras, title: "Cameras online", symbol: "video.fill",
                 value: "\(online)/\(total)",
                 note: offline > 0 ? "\(offline) offline" : (total == 0 ? "None yet" : "All online"),
                 tone: offline > 0 ? .bad : .good),
            Stat(kind: .recording, title: "Recording", symbol: "record.circle",
                 value: "\(recording)",
                 note: total > 0 && recording == online && online > 0 ? "Every online camera" : "of \(total) cameras",
                 tone: .good),
            Stat(kind: .events, title: "Events today", symbol: "sparkles",
                 value: "\(today)", note: "AI detections", tone: .info),
            Stat(kind: .alarms, title: "Open alarms", symbol: "bell.badge.fill",
                 value: "\(open)",
                 note: new > 0 ? "\(new) new" : (open > 0 ? "Being handled" : "Nothing open"),
                 tone: new > 0 ? .bad : (open > 0 ? .warning : .good))
        ]
    }

    // MARK: Needs attention

    /// Home shows at most this many alarm cards.
    public static let attentionLimit = 3

    /// New alarms, most severe first, then the latest event first.
    public static func needsAttention(_ open: [V2.Alarm]) -> [V2.Alarm] {
        open.filter { $0.state == .new }.sorted {
            if $0.severity.rank != $1.severity.rank { return $0.severity.rank < $1.severity.rank }
            if $0.lastEventAt != $1.lastEventAt { return $0.lastEventAt > $1.lastEventAt }
            return $0.id < $1.id
        }
    }

    /// The cards Home shows and how many more wait in the Alarm Manager.
    public static func attention(_ open: [V2.Alarm], limit: Int = attentionLimit) -> (shown: [V2.Alarm], more: Int) {
        let all = needsAttention(open)
        return (Array(all.prefix(limit)), max(0, all.count - limit))
    }

    /// Acknowledge sits on alarms that are new or snoozed, for roles that may.
    public static func canAcknowledge(_ alarm: V2.Alarm, mayAct: Bool) -> Bool {
        mayAct && (alarm.state == .new || alarm.state == .snoozed)
    }

    // MARK: Recent activity

    public static let activityLimit = 6

    /// The newest detections, newest first.
    public static func recentActivity(_ events: [V2.Event], limit: Int = activityLimit) -> [V2.Event] {
        Array(events.sorted { $0.createdAt > $1.createdAt }.prefix(limit))
    }

    /// The newest event on a camera that has a saved frame: the camera
    /// tile's still (a grid of live players would decode every stream).
    public static func latestStill(for cameraID: String, in events: [V2.Event]) -> V2.Event? {
        events.filter { $0.cameraId == cameraID && $0.hasThumbnail }.max { $0.createdAt < $1.createdAt }
    }

    // MARK: Header

    /// "Local network · live", or where the site is and the connection state.
    public static func subtitle(host: String?, streamLabel: String) -> String {
        let place = isLocal(host: host) ? "Local network" : (host?.isEmpty == false ? host! : "Remote")
        return "\(place) · \(streamLabel == "Live" ? "live" : streamLabel.lowercased())"
    }

    /// Loopback, private IPv4 ranges, link-local and `.local` names.
    public static func isLocal(host: String?) -> Bool {
        guard let host = host?.lowercased(), host.isEmpty == false else { return false }
        if host == "localhost" || host.hasSuffix(".local") || host == "::1" { return true }
        let parts = host.split(separator: ".").compactMap { Int($0) }
        guard parts.count == 4, parts.allSatisfy({ (0...255).contains($0) }) else { return false }
        switch (parts[0], parts[1]) {
        case (10, _), (127, _), (192, 168), (169, 254): return true
        case (172, let b) where (16...31).contains(b): return true
        default: return false
        }
    }

    // MARK: Layout

    /// Home's content never stretches past this; it centres.
    public static let maxWidth: Double = 1100

    public static func contentWidth(available: Double, padding: Double) -> Double {
        max(0, min(available - padding * 2, maxWidth))
    }

    /// 55 % "Needs attention" and activity, 45 % cameras and links.
    public static func columns(contentWidth: Double, spacing: Double) -> (left: Double, right: Double) {
        let usable = max(0, contentWidth - spacing)
        let left = (usable * 0.55).rounded(.down)
        return (left, usable - left)
    }

    /// The camera grid is two across.
    public static let cameraColumns = 2

    // MARK: Symbols

    /// Alarm kinds → symbols, as on the iPhone's alarm cards.
    public static func alarmSymbol(kind: String, severity: AlertSeverity) -> String {
        switch kind {
        case "Person": return "figure.walk"
        case "Vehicle": return "car.fill"
        case "Motion": return "figure.walk.motion"
        case "Camera Offline": return "video.slash.fill"
        case "Recording": return "record.circle"
        case "Storage": return "internaldrive.fill"
        case "Line crossing": return "arrow.left.and.right"
        case "Credential": return "key.fill"
        case "Watchlist": return "flag.fill"
        default: return severity == .critical ? "exclamationmark.octagon.fill" : "exclamationmark.triangle.fill"
        }
    }

    /// Detection kinds → symbols, as on the iPhone's chips; every kind in
    /// PARITY-342 §2 has one.
    public static func kindSymbol(_ kind: String) -> String {
        switch kind {
        case "Person": return "figure.walk"
        case "Face": return "person.fill.viewfinder"
        case "Vehicle": return "car.fill"
        case "License Plate", "License plate": return "doc.text.magnifyingglass"
        case "Animal": return "pawprint.fill"
        case "Package": return "shippingbox.fill"
        case "Loitering": return "clock.badge.exclamationmark"
        case "Line crossing": return "arrow.left.and.right"
        case "Intrusion": return "figure.walk.departure"
        case "audio", "Sound": return "waveform"
        case "Watchlist": return "flag.fill"
        case "Motion": return "figure.walk.motion"
        default: return "sparkles"
        }
    }

    /// "3 min ago", "2 h ago", "yesterday", or a date; for card subtitles.
    public static func relative(_ millis: UnixMillis, now: Date) -> String {
        let seconds = max(0, now.timeIntervalSince(Date(unixMillis: millis)))
        if seconds < 60 { return "just now" }
        if seconds < 3600 { return "\(Int(seconds / 60)) min ago" }
        if seconds < 86_400 { return "\(Int(seconds / 3600)) h ago" }
        let days = Int(seconds / 86_400)
        return days == 1 ? "yesterday" : "\(days) days ago"
    }
}
