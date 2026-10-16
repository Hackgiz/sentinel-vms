import Foundation

// Alarm Manager calls (`/api/v2/alarms` filters, counts, events, assign,
// severity, triage). The lightweight queue keeps using `SentinelAPI.alarms`.

public extension SentinelAPI {
    /// `GET /api/v2/alarms` with the Alarm Manager's filters and `before` paging.
    func alarmsPage(filter: AlarmFilter, now: Date = Date(), limit: Int = 100, before: String? = nil) async throws -> V2.Page<V2.Alarm> {
        try await send("GET", "/api/v2/alarms", query: filter.listQueryItems(now: now, limit: limit, before: before))
    }

    /// `GET /api/v2/alarms/counts`: per-state counts honoring every filter but the state chips.
    func alarmCounts(filter: AlarmFilter, now: Date = Date()) async throws -> V2.AlarmCounts {
        try await send("GET", "/api/v2/alarms/counts", query: filter.countsQueryItems(now: now))
    }

    /// The detection events behind an alarm, oldest first.
    func alarmEvents(_ id: String) async throws -> [V2.Event] {
        let list: V2.Items<V2.Event> = try await send("GET", "/api/v2/alarms/\(Self.escape(id))/events")
        return list.items
    }

    /// Sets the owner (empty unassigns) and returns the updated alarm.
    func assignAlarm(_ id: String, owner: String) async throws -> V2.Alarm {
        try await send("POST", "/api/v2/alarms/\(Self.escape(id))/assign", body: V2.AlarmAssignment(owner: owner))
    }

    /// How an operator accepts a triage suggestion.
    func setAlarmSeverity(_ id: String, _ severity: V2.Severity) async throws -> V2.Alarm {
        try await send("POST", "/api/v2/alarms/\(Self.escape(id))/severity", body: V2.AlarmSeverityChange(severity: severity))
    }

    /// AI triage: advice only. 409 when AI is off, 429 at the daily limit
    /// (see `AIFailure`).
    func triageAlarm(_ id: String) async throws -> V2.AlarmTriage {
        try await send("POST", "/api/v2/alarms/\(Self.escape(id))/triage")
    }

    /// Whether the server offers AI (`aiEnabled` of `/events/facets`). Any
    /// failure reads as "not available" so the AI buttons stay hidden.
    func aiEnabled() async -> Bool {
        let flag: V2.AIAvailability? = try? await send("GET", "/api/v2/events/facets")
        return flag?.aiEnabled ?? false
    }
}
