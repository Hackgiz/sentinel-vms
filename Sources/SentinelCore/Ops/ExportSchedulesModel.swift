import Foundation

// Pure logic for the Scheduled exports tab (port of the schedule half of web
// `longExportModel.ts`): days, HH:MM windows with the overnight rule,
// validation and labels.

/// The editor sheet's state.
public struct ScheduleDraft: Hashable, Sendable {
    public var name: String
    public var cameraID: String
    /// 0 = Sunday … 6 = Saturday, sorted, no duplicates.
    public var days: [Int]
    public var start: String
    public var end: String
    public var enabled: Bool

    public init(name: String = "", cameraID: String = "", days: [Int] = Array(0...6),
                start: String = "22:00", end: String = "06:00", enabled: Bool = true) {
        self.name = name
        self.cameraID = cameraID
        self.days = days
        self.start = start
        self.end = end
        self.enabled = enabled
    }

    /// A new schedule: every night 22:00 → 06:00 on `cameraID`.
    public static func blank(cameraID: String = "") -> ScheduleDraft { ScheduleDraft(cameraID: cameraID) }

    public init(_ s: V2.ExportSchedule) {
        self.init(name: s.name, cameraID: s.cameraId, days: s.days, start: s.start, end: s.end, enabled: s.enabled)
    }

    public mutating func toggle(day: Int) {
        guard (0...6).contains(day) else { return }
        if days.contains(day) { days.removeAll { $0 == day } } else { days = (days + [day]).sorted() }
    }

    public var input: V2.ExportScheduleInput {
        V2.ExportScheduleInput(name: name.trimmingCharacters(in: .whitespacesAndNewlines), cameraId: cameraID,
                               days: Array(Set(days)).sorted(), start: start, end: end, dest: .evidence, enabled: enabled)
    }

    /// The full PATCH for an edited schedule (every field the sheet shows).
    public var patch: V2.ExportSchedulePatch {
        let i = input
        return V2.ExportSchedulePatch(name: i.name, cameraId: i.cameraId, days: i.days, start: i.start, end: i.end,
                                      dest: .evidence, enabled: i.enabled)
    }
}

public enum ScheduleRules {
    public static let dayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
    public static let nameLimit = 100
    public static let maxWindowMinutes = Int(LongExportRules.maxRangeMs / 60_000)
    public static let explanation = "When a window ends, the server exports its footage into the evidence vault. Times are the server's local time; a window that ends before it starts runs overnight. Windows that end while the server is down are not backfilled."
    public static let windowHelp = "An end time before the start runs overnight into the next day. Up to 12 hours; exported when the window ends."

    /// "Every day", "Weekdays", "Weekends", "Mon, Wed, Fri".
    public static func daysText(_ days: [Int]) -> String {
        let set = Array(Set(days)).sorted()
        if set.count == 7 { return "Every day" }
        if set == [1, 2, 3, 4, 5] { return "Weekdays" }
        if set == [0, 6] { return "Weekends" }
        return set.map { (0...6).contains($0) ? dayNames[$0] : "?" }.joined(separator: ", ")
    }

    /// Minutes after midnight of "HH:MM" ("24:00" only when `allow24`), or nil.
    public static func clockMinutes(_ value: String, allow24: Bool = false) -> Int? {
        let parts = value.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 2, parts[0].count == 2, parts[1].count == 2,
              parts.allSatisfy({ $0.allSatisfy(\.isASCII) && $0.allSatisfy(\.isNumber) }),
              let h = Int(parts[0]), let m = Int(parts[1]) else { return nil }
        if m > 59 || h > 24 || (h == 24 && (m != 0 || allow24 == false)) { return nil }
        return h * 60 + m
    }

    /// Window length in minutes (an end before the start runs overnight), or nil if invalid.
    public static func windowMinutes(start: String, end: String) -> Int? {
        guard let s = clockMinutes(start), let e = clockMinutes(end, allow24: true), s != e else { return nil }
        return e > s ? e - s : e + 1440 - s
    }

    /// Whether the window runs into the next day.
    public static func isOvernight(start: String, end: String) -> Bool {
        guard let s = clockMinutes(start), let e = clockMinutes(end, allow24: true) else { return false }
        return e <= s
    }

    /// Why a schedule can't be saved, or nil when it can (the server checks again).
    public static func blocker(_ d: ScheduleDraft) -> String? {
        if d.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return "Give the schedule a name." }
        if d.name.trimmingCharacters(in: .whitespacesAndNewlines).count > nameLimit { return "The name can be at most \(nameLimit) characters." }
        if d.cameraID.isEmpty { return "Pick a camera." }
        if d.days.isEmpty { return "Pick at least one day." }
        guard let length = windowMinutes(start: d.start, end: d.end) else { return "Enter a start and an end time that differ." }
        if length > maxWindowMinutes { return "A window can cover at most 12 hours." }
        return nil
    }

    /// "Mon, Wed · 22:00 → 06:00 (next day)".
    public static func windowText(days: [Int], start: String, end: String) -> String {
        "\(daysText(days)) · \(start) → \(end)\(isOvernight(start: start, end: end) ? " (next day)" : "")"
    }

    public static func windowText(_ s: V2.ExportSchedule) -> String { windowText(days: s.days, start: s.start, end: s.end) }

    /// The last run, in words: "Never", "OK — …", "Running — …", "Failed — nothing was recorded".
    public static func lastRunText(_ s: V2.ExportSchedule,
                                   format: (UnixMillis) -> String = EvidenceDisplay.defaultFormat) -> String {
        guard s.lastRunAt > 0 else { return "Never" }
        let when = format(s.lastRunAt)
        if s.lastStatus == "ok" { return "OK — \(when)" }
        if s.lastStatus == "running" { return "Running — \(when)" }
        if s.lastStatus.hasPrefix("failed:") {
            let why = s.lastStatus.dropFirst("failed:".count).trimmingCharacters(in: .whitespaces)
            return "Failed — \(why.isEmpty ? "see the server log" : why)"
        }
        return when
    }

    /// Whether the last run failed (the table tints it).
    public static func lastRunFailed(_ s: V2.ExportSchedule) -> Bool { s.lastRunAt > 0 && s.lastStatus.hasPrefix("failed:") }

    /// "HH:MM" of hour/minute components (for the sheet's time pickers).
    public static func clock(hour: Int, minute: Int) -> String { String(format: "%02d:%02d", hour, minute) }

    /// Hour and minute of "HH:MM" ("24:00" → 24, 0), or nil.
    public static func components(_ value: String) -> (hour: Int, minute: Int)? {
        guard let m = clockMinutes(value, allow24: true) else { return nil }
        return (m / 60, m % 60)
    }

    /// Schedules sorted for the table: by name, then id.
    public static func sorted(_ list: [V2.ExportSchedule]) -> [V2.ExportSchedule] {
        list.sorted { ($0.name.localizedLowercase, $0.id) < ($1.name.localizedLowercase, $1.id) }
    }
}
