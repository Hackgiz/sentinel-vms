import Foundation

// The view-free logic behind the Management window's Insights pane and the
// Rules pane's draft box and template gallery. It mirrors the web Admin's
// `admin/insights.ts` and `rulesApi.ts` so both clients show the same numbers.

/// Alarm rule vocabulary shared with the server.
public enum RuleCatalog {
    /// The server's `rules.Kinds`, in its order.
    public static let kinds = ["Motion", "Person", "Vehicle", "Line crossing", "Intrusion", "Loitering", "Tamper",
                               "Camera Offline", "Storage", "audio"]

    /// The server's bounds on a draft description, in characters.
    public static let draftMinLength = 3
    public static let draftMaxLength = 500

    /// Whether the "Draft rule" button is enabled for this text.
    public static func canDraft(_ text: String) -> Bool {
        let n = text.trimmingCharacters(in: .whitespacesAndNewlines).count
        return n >= draftMinLength && n <= draftMaxLength
    }

    /// Template categories in display order.
    public static let templateCategories: [(id: String, label: String)] = [
        ("security", "Security"), ("health", "Health"), ("operations", "Operations")
    ]

    public struct TemplateGroup: Equatable, Identifiable, Sendable {
        public var id: String
        public var label: String
        public var items: [V2.RuleTemplate]
    }

    /// Templates by category, in catalog order; a category this client
    /// doesn't know goes last, in first-seen order.
    public static func groupTemplates(_ items: [V2.RuleTemplate]) -> [TemplateGroup] {
        var ids = templateCategories.map(\.id)
        for t in items where ids.contains(t.category) == false { ids.append(t.category) }
        return ids.compactMap { id in
            let group = items.filter { $0.category == id }
            guard group.isEmpty == false else { return nil }
            let label = templateCategories.first { $0.id == id }?.label ?? InsightsLogic.kindLabel(id)
            return TemplateGroup(id: id, label: label, items: group)
        }
    }
}

/// The ranges the Insights pane offers. Seven days is the default, as on the server.
public enum InsightsRange: String, CaseIterable, Identifiable, Sendable {
    case day = "24h"
    case week = "7d"
    case month = "30d"

    public static let `default` = InsightsRange.week

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .day: return "24 h"
        case .week: return "7 days"
        case .month: return "30 days"
        }
    }

    public var milliseconds: UnixMillis {
        let day: UnixMillis = 24 * 3600 * 1000
        switch self {
        case .day: return day
        case .week: return 7 * day
        case .month: return 30 * day
        }
    }

    /// `from` and `to` (Unix ms) for the range ending at `now`.
    public func bounds(now: Date) -> (from: UnixMillis, to: UnixMillis) {
        let to = now.unixMillis
        return (to - milliseconds, to)
    }
}

/// What a recommendation's button does in the Management window.
public enum InsightsActionPlan: Equatable, Sendable {
    /// Change the camera's "Raise alarms for" here (PATCH the camera).
    case setAlertOn(cameraId: String, alertOn: V2.AlertOn)
    /// Open the Rules pane with this template in the editor.
    case openTemplate(templateId: String)
    /// Open the camera's settings in Cameras.
    case openCamera(cameraId: String)
    /// Open System Monitor (storage per recording server).
    case openStorage

    public var label: String {
        switch self {
        case let .setAlertOn(_, alertOn): return "Switch to \(InsightsLogic.alertOnLabel(alertOn).lowercased())"
        case .openTemplate: return "Use template"
        case .openCamera: return "Open camera"
        case .openStorage: return "Check storage"
        }
    }
}

public enum InsightsLogic {
    /// Kinds that get their own colour in the daily bars; the rest are summed as Other.
    public static let topKinds = 4
    public static let other = "Other"

    public struct Segment: Equatable, Sendable {
        public var kind: String
        public var count: Int
    }

    public struct DayBar: Equatable, Identifiable, Sendable {
        /// The server's local date, YYYY-MM-DD.
        public var day: String
        public var events: Int
        /// Height of the bar: the day's alarms.
        public var total: Int
        /// Bottom to top, in `kinds` order; only kinds with alarms that day.
        public var segments: [Segment]
        public var id: String { day }
    }

    // Rule-kind order, so a kind keeps its slot (and colour) when the range changes.
    private static func kindOrder(_ a: String, _ b: String) -> Bool {
        let ra = RuleCatalog.kinds.firstIndex(of: a) ?? RuleCatalog.kinds.count
        let rb = RuleCatalog.kinds.firstIndex(of: b) ?? RuleCatalog.kinds.count
        return ra != rb ? ra < rb : a < b
    }

    /// The daily bars: the range's `top` busiest kinds each get a segment and
    /// everything else is Other. A day's alarms that byKind doesn't account
    /// for count as Other, so a bar is never shorter than the day's alarms.
    public static func stackDaily(_ daily: [V2.Insights.Day], top: Int = topKinds) -> (kinds: [String], bars: [DayBar]) {
        var totals: [String: Int] = [:]
        for d in daily {
            for (k, n) in d.byKind ?? [:] where k.isEmpty == false && n > 0 { totals[k, default: 0] += n }
        }
        let picked = totals.sorted { $0.value != $1.value ? $0.value > $1.value : kindOrder($0.key, $1.key) }
            .prefix(top).map(\.key).sorted(by: kindOrder)
        let counts: [[String: Int]] = daily.map { d in
            var m: [String: Int] = [:]
            var sum = 0
            for (k, n) in d.byKind ?? [:] where n > 0 {
                m[picked.contains(k) ? k : other, default: 0] += n
                sum += n
            }
            if d.alarms > sum { m[other, default: 0] += d.alarms - sum }
            return m
        }
        let kinds = counts.contains { $0[other] != nil } ? picked + [other] : picked
        let bars = zip(daily, counts).map { d, m -> DayBar in
            let segments = kinds.compactMap { k in m[k].map { Segment(kind: k, count: $0) } }
            return DayBar(day: d.day, events: d.events, total: segments.reduce(0) { $0 + $1.count }, segments: segments)
        }
        return (kinds, bars)
    }

    /// A kind's share as a whole percent: "<1%" for a sliver, ">99%" for nearly all.
    public static func formatShare(_ share: Double) -> String {
        guard share > 0 else { return "0%" }
        if share >= 1 { return "100%" }
        if share < 0.01 { return "<1%" }
        if share > 0.99 { return ">99%" }
        return "\(Int((share * 100).rounded()))%"
    }

    /// An alarm kind as shown ("audio" -> "Audio"); an empty kind is Other.
    public static func kindLabel(_ kind: String) -> String {
        guard let first = kind.first else { return other }
        return first.uppercased() + kind.dropFirst()
    }

    public static func alertOnLabel(_ alertOn: V2.AlertOn) -> String {
        switch alertOn {
        case .auto: return "Automatic"
        case .motion: return "Any motion"
        case .peopleAndVehicles: return "People & vehicles"
        case .people: return "People only"
        default: return alertOn.rawValue
        }
    }

    /// "2026-10-14" -> "Wed, Oct 14". The day is already the server's local
    /// date, so it's read and shown in one fixed zone.
    public static func dayLabel(_ day: String, locale: Locale = .current, template: String = "EEEMMMd") -> String {
        let parse = DateFormatter()
        parse.locale = Locale(identifier: "en_US_POSIX")
        parse.timeZone = TimeZone(identifier: "UTC")
        parse.dateFormat = "yyyy-MM-dd"
        guard let date = parse.date(from: day) else { return day }
        let out = DateFormatter()
        out.locale = locale
        out.timeZone = TimeZone(identifier: "UTC")
        out.setLocalizedDateFormatFromTemplate(template)
        return out.string(from: date)
    }

    /// The permission each action needs: the server's guard on what it changes or opens.
    public static func permission(for actionType: String) -> V2.Permission? {
        switch actionType {
        case "setAlertOn": return .manageCameras // PATCH /api/v2/cameras/{cameraId}
        case "openTemplate": return .changeSettings // the editor saves with POST /api/v2/rules
        case "openCamera": return .manageCameras // the camera's settings in Cameras
        case "openStorage": return .viewLive // System Monitor
        default: return nil
        }
    }

    /// The button for a recommendation, or nil when there is none to show: no
    /// action, one this client doesn't know, a missing id, or a user without
    /// the permission it needs.
    public static func action(for rec: V2.Recommendation, can: (V2.Permission) -> Bool) -> InsightsActionPlan? {
        guard let a = rec.action, let permission = permission(for: a.type), can(permission) else { return nil }
        let cameraId = a.cameraId.flatMap { $0.isEmpty ? nil : $0 } ?? rec.cameraId ?? ""
        switch a.type {
        case "setAlertOn":
            guard cameraId.isEmpty == false, let to = a.alertOn else { return nil }
            return .setAlertOn(cameraId: cameraId, alertOn: to)
        case "openTemplate":
            guard let id = a.templateId, id.isEmpty == false else { return nil }
            return .openTemplate(templateId: id)
        case "openCamera":
            return cameraId.isEmpty ? nil : .openCamera(cameraId: cameraId)
        case "openStorage":
            return .openStorage
        default:
            return nil
        }
    }

    /// The message for a failed alert change; a 403 names the missing permission.
    public static func alertChangeError(_ error: Error) -> String {
        if let api = error as? SentinelAPIError, api.isForbidden { return "You need permission to change cameras." }
        return error.localizedDescription
    }
}
