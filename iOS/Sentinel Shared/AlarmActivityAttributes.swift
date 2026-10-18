// AlarmActivityAttributes.swift
// The alarm Live Activity's data, shared by the app (which starts, updates
// and ends it) and the widget extension (which draws it).
//
// ContentState is also what the Management Server sends in an APNs
// `liveactivity` push (internal/push/liveactivity.go, "content-state"), so
// its JSON keys are a wire contract: state, owner, severity, updatedAt
// (Unix ms), eventCount. ActivityKit decodes it with a default JSONDecoder,
// which is why there are no Date fields here.

import Foundation
#if canImport(ActivityKit)
import ActivityKit
#endif

/// Plain-Swift view of an alarm state for the Live Activity: the server's
/// six states fold into the three the activity shows.
enum AlarmActivityPhase: String, Codable, Equatable {
    case new = "New"
    case acknowledged = "Acknowledged"
    case closed = "Closed"

    init(serverState: String) {
        switch serverState {
        case "New": self = .new
        case "Resolved", "False Alarm", "Closed": self = .closed
        default: self = .acknowledged   // Acknowledged, Investigating, Snoozed, unknown
        }
    }
}

struct AlarmActivityContent: Codable, Hashable {
    /// The server's alarm state (New, Acknowledged, Investigating, Snoozed,
    /// Resolved, False Alarm).
    var state: String
    /// Who is handling it; "" or "Unassigned" when nobody.
    var owner: String
    var severity: String
    /// Last change, Unix milliseconds.
    var updatedAt: Int64
    var eventCount: Int

    var phase: AlarmActivityPhase { AlarmActivityPhase(serverState: state) }

    var assignee: String? {
        let trimmed = owner.trimmingCharacters(in: .whitespaces)
        guard trimmed.isEmpty == false, trimmed != "Unassigned", trimmed != "Current Operator" else { return nil }
        return trimmed
    }
}

#if canImport(ActivityKit)
@available(iOS 16.1, *)
struct AlarmActivityAttributes: ActivityAttributes {
    typealias ContentState = AlarmActivityContent

    var alarmId: String
    var title: String
    var cameraName: String
    var kind: String
    /// When the alarm was raised, Unix milliseconds (elapsed time counts from here).
    var raisedAt: Int64

    var raisedDate: Date { Date(timeIntervalSince1970: TimeInterval(raisedAt) / 1000) }
    var deepLink: URL { SentinelDeepLink.alarm(id: alarmId).url }
}
#endif
