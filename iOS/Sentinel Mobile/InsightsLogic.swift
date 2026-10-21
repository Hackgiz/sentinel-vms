// InsightsLogic.swift
// The Insights screen without the screen (WAVE15): the API calls, the range
// query, the daily bars stacked by alarm kind, share formatting and what a
// recommendation's action does for the signed-in user. Mirrors the web
// Admin's `linux/web/src/admin/insights.ts`, kept pure so it is tested
// without a server.

import Foundation

// MARK: - API v2 calls

extension SentinelAPI {
    /// `GET /api/v2/insights` (viewLive; camera scope applies). `from` and
    /// `to` are Unix ms; the server allows at most 31 days.
    func insights(from: UnixMillis, to: UnixMillis) async throws -> V2.Insights {
        try await send("GET", "/api/v2/insights", query: [
            URLQueryItem(name: "from", value: String(from)),
            URLQueryItem(name: "to", value: String(to))
        ])
    }

    func insights(_ query: InsightsMath.RangeQuery) async throws -> V2.Insights {
        try await insights(from: query.from, to: query.to)
    }

    /// `PATCH /api/v2/cameras/{cameraId}` with only `alertOn` (manageCameras).
    /// The updated camera in the answer isn't needed: the screen reloads.
    func setAlertOn(cameraID: String, _ alertOn: V2.AlertOn) async throws {
        try await sendNoContent("PATCH", "/api/v2/cameras/\(Self.escape(cameraID))",
                                body: V2.CameraAlertOnPatch(alertOn: alertOn))
    }
}

// MARK: - Logic

enum InsightsMath {
    static let day: UnixMillis = 24 * 3600 * 1000

    /// The ranges the screen offers. Seven days is the default, as on the server.
    enum Range: String, CaseIterable, Identifiable, Sendable {
        case day1 = "24h"
        case days7 = "7d"
        case days30 = "30d"

        static let `default` = Range.days7

        var id: String { rawValue }

        var label: String {
            switch self {
            case .day1: return "24 h"
            case .days7: return "7 days"
            case .days30: return "30 days"
            }
        }

        var millis: UnixMillis {
            switch self {
            case .day1: return InsightsMath.day
            case .days7: return 7 * InsightsMath.day
            case .days30: return 30 * InsightsMath.day
            }
        }
    }

    /// The `from`/`to` query of `GET /api/v2/insights` for a range ending now.
    struct RangeQuery: Hashable, Sendable {
        var from: UnixMillis
        var to: UnixMillis

        init(from: UnixMillis, to: UnixMillis) {
            self.from = from
            self.to = to
        }

        init(_ range: Range, now: UnixMillis) {
            self.init(from: now - range.millis, to: now)
        }
    }

    /// Nothing to show yet: no alarms and no events in the range.
    static func isEmpty(_ insights: V2.Insights) -> Bool {
        insights.totals.alarms == 0 && insights.totals.events == 0
    }

    // MARK: Daily bars

    /// Kinds that get their own colour; the rest are summed as Other.
    static let topKinds = 4
    static let other = "Other"

    /// The server's rule kinds (`rules.Kinds`), so a kind keeps its slot and
    /// colour when the range changes.
    static let ruleKinds = ["Motion", "Person", "Vehicle", "Line crossing", "Intrusion", "Loitering",
                            "Tamper", "Camera Offline", "Storage", "audio"]

    struct Segment: Hashable, Sendable {
        var kind: String
        var count: Int
    }

    struct DayBar: Hashable, Identifiable, Sendable {
        /// The server's local date, YYYY-MM-DD.
        var day: String
        var events: Int
        /// Height of the bar: the day's alarms.
        var total: Int
        /// Bottom to top, in `Stack.kinds` order; only kinds with alarms that day.
        var segments: [Segment]

        var id: String { day }
    }

    struct Stack: Hashable, Sendable {
        var kinds: [String]
        var bars: [DayBar]

        var peak: Int { bars.map(\.total).max() ?? 0 }
    }

    private static func kindRank(_ kind: String) -> Int {
        ruleKinds.firstIndex(of: kind) ?? ruleKinds.count
    }

    private static func kindOrder(_ a: String, _ b: String) -> Bool {
        let ra = kindRank(a), rb = kindRank(b)
        return ra != rb ? ra < rb : a < b
    }

    /// The range's `top` busiest kinds each get a segment and everything else
    /// is Other. A day's alarms that `byKind` doesn't account for count as
    /// Other, so a bar is never shorter than the day's alarm count.
    static func stackDaily(_ daily: [V2.Insights.Day], top: Int = topKinds) -> Stack {
        var totals: [String: Int] = [:]
        for d in daily {
            for (k, n) in d.byKind where k.isEmpty == false && n > 0 { totals[k, default: 0] += n }
        }
        let picked = totals
            .sorted { $0.value != $1.value ? $0.value > $1.value : kindOrder($0.key, $1.key) }
            .prefix(top)
            .map(\.key)
            .sorted(by: kindOrder)
        let counts: [[String: Int]] = daily.map { d in
            var m: [String: Int] = [:]
            var sum = 0
            for (k, n) in d.byKind where n > 0 {
                m[picked.contains(k) ? k : other, default: 0] += n
                sum += n
            }
            if d.alarms > sum { m[other, default: 0] += d.alarms - sum }
            return m
        }
        let kinds = counts.contains { $0[other] != nil } ? picked + [other] : picked
        let bars = zip(daily, counts).map { d, m in
            let segments = kinds.compactMap { k in m[k].map { Segment(kind: k, count: $0) } }
            return DayBar(day: d.day, events: d.events, total: segments.reduce(0) { $0 + $1.count }, segments: segments)
        }
        return Stack(kinds: kinds, bars: bars)
    }

    // MARK: Formatting

    /// A kind's share of the alarms as a whole percent: "<1%" for a sliver,
    /// ">99%" for nearly all.
    static func formatShare(_ share: Double) -> String {
        guard share > 0 else { return "0%" }
        if share >= 1 { return "100%" }
        if share < 0.01 { return "<1%" }
        if share > 0.99 { return ">99%" }
        return "\(Int((share * 100).rounded()))%"
    }

    /// An alarm kind as shown ("audio" -> "Audio"); an empty kind is Other.
    static func kindLabel(_ kind: String) -> String {
        guard let first = kind.first else { return other }
        return first.uppercased() + kind.dropFirst()
    }

    static func alertOnLabel(_ alertOn: V2.AlertOn) -> String {
        switch alertOn {
        case .auto: return "Automatic"
        case .motion: return "Any motion"
        case .peopleAndVehicles: return "People & vehicles"
        case .people: return "People only"
        default: return alertOn.rawValue
        }
    }

    /// "2026-10-14" -> "Wed, Oct 14". The day is already the server's local
    /// date, so it is read and shown in one fixed zone.
    static func dayLabel(_ day: String, locale: Locale = .current) -> String {
        let parts = day.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return day }
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        guard let date = calendar.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2])) else { return day }
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.calendar = calendar
        formatter.timeZone = calendar.timeZone
        formatter.setLocalizedDateFormatFromTemplate("EEEMMMd")
        return formatter.string(from: date)
    }

    // MARK: Recommendations

    /// The permission each action needs: the server's guard on what it changes or opens.
    static func permission(for action: V2.RecommendationAction.ActionType) -> V2.Permission? {
        switch action {
        case .setAlertOn: return .manageCameras   // PATCH /api/v2/cameras/{cameraId}
        case .openTemplate: return .changeSettings // the rule editor saves with POST /api/v2/rules
        case .openCamera: return .manageCameras    // the camera's settings
        case .openStorage: return .viewLive       // storage per recording server
        default: return nil
        }
    }

    /// What the phone offers for a recommendation. Only the alert switch is a
    /// button; the rest live in screens the phone doesn't have, so they are a hint.
    enum Action: Hashable, Sendable {
        case setAlertOn(label: String, cameraID: String, alertOn: V2.AlertOn)
        case hint(String)
    }

    /// The action for a recommendation, or nil when there is none to show: no
    /// action, one this app doesn't know, a missing id, or a user without the
    /// permission it needs.
    static func action(for rec: V2.Recommendation, can: (V2.Permission) -> Bool) -> Action? {
        guard let a = rec.action, let permission = permission(for: a.type), can(permission) else { return nil }
        let cameraID = a.cameraId ?? rec.cameraId ?? ""
        switch a.type {
        case .setAlertOn:
            guard cameraID.isEmpty == false, let alertOn = a.alertOn else { return nil }
            return .setAlertOn(label: "Switch to \(alertOnLabel(alertOn).lowercased())", cameraID: cameraID, alertOn: alertOn)
        case .openTemplate:
            return a.templateId == nil ? nil : .hint("Use the suggested rule template in Sentinel Admin or on the Mac.")
        case .openCamera:
            return cameraID.isEmpty ? nil : .hint("Change this camera's settings in Sentinel Admin or on the Mac.")
        case .openStorage:
            return .hint("Check storage in System Monitor on the Mac or in Sentinel Admin.")
        default:
            return nil
        }
    }

    /// The text for a failed load or change. The server's own message is what
    /// a person should see (403, 409, 429 and 502 all carry one).
    static func errorMessage(_ error: Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }
}
