import Foundation

// View-free logic for the Incidents workspace and the AI affordances shared
// with the Alarm Manager.

/// Why an AI call (`alarms/{id}/triage`, `incidents/{id}/report`) failed,
/// with the server's own wording kept for display.
public enum AIFailure: Equatable, Sendable {
    /// 409: AI is switched off on the server.
    case off(String)
    /// 429: the daily AI limit is reached.
    case limitReached(String)
    /// 502/503: the model call failed or the service is unavailable.
    case unavailable(String)
    /// 403 and anything else.
    case other(String)

    public init(_ error: Error) {
        if let api = error as? SentinelAPIError, case let .server(status, _, message) = api {
            switch status {
            case 409: self = .off(message)
            case 429: self = .limitReached(message)
            case 502, 503, 504: self = .unavailable(message)
            default: self = .other(message)
            }
        } else {
            self = .other(error.localizedDescription)
        }
    }

    /// The text to show the operator.
    public var message: String {
        switch self {
        case let .off(m), let .limitReached(m), let .unavailable(m), let .other(m): return m
        }
    }

    /// AI is off or capped for today: the button should stop offering itself
    /// until the next capability check.
    public var disablesAI: Bool {
        switch self {
        case .off, .limitReached: return true
        default: return false
        }
    }
}

/// What an incident item shows as.
public enum IncidentItemResolution: Equatable, Sendable {
    /// Outside the caller's camera scope: no object.
    case restricted
    /// The object has been deleted.
    case missing
    case alarm(V2.Alarm)
    case event(V2.Event)
    case evidence(V2.Evidence)
    case note(String)
    /// Kind known but no object came with it (and no flag): shown as unavailable.
    case unresolved(kind: String)
}

public extension V2.IncidentItem {
    /// Restricted wins over missing; a flagged item never fails the view.
    var resolution: IncidentItemResolution {
        if restricted == true { return .restricted }
        if missing == true { return .missing }
        switch kind {
        case .alarm: if let alarm { return .alarm(alarm) }
        case .event: if let event { return .event(event) }
        case .evidence: if let evidence { return .evidence(evidence) }
        case .note: return .note(note ?? "")
        default: break
        }
        return .unresolved(kind: kind.rawValue)
    }
}

public enum IncidentTimeline {
    /// Chronological by when the thing happened; ties by when it was added, then id.
    public static func ordered(_ items: [V2.IncidentItem]) -> [V2.IncidentItem] {
        items.sorted {
            if $0.at != $1.at { return $0.at < $1.at }
            if $0.addedAt != $1.addedAt { return $0.addedAt < $1.addedAt }
            return $0.id < $1.id
        }
    }
}

/// The incident list's filter bar: status + text.
public struct IncidentFilter: Equatable, Sendable {
    public var status: V2.IncidentStatus?
    public var q: String

    public init(status: V2.IncidentStatus? = nil, q: String = "") {
        self.status = status
        self.q = q
    }

    public func queryItems(limit: Int? = nil, before: String? = nil) -> [URLQueryItem] {
        let trimmed = q.trimmingCharacters(in: .whitespacesAndNewlines)
        return [
            URLQueryItem(name: "status", value: status?.rawValue),
            URLQueryItem(name: "q", value: trimmed.isEmpty ? nil : trimmed),
            URLQueryItem(name: "limit", value: limit.map(String.init)),
            URLQueryItem(name: "before", value: before)
        ].filter { $0.value != nil }
    }
}

public enum IncidentRules {
    /// Statuses the operator can move to from `current` (the server accepts
    /// any transition; the current one is not offered).
    public static func transitions(from current: V2.IncidentStatus) -> [V2.IncidentStatus] {
        V2.IncidentStatus.all.filter { $0 != current }
    }

    /// Generating a report replaces the summary: confirm first when there is
    /// already text, saved or being edited.
    public static func needsOverwriteConfirmation(savedSummary: String, draft: String) -> Bool {
        !savedSummary.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            || !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    public static func isDirty(savedSummary: String, draft: String) -> Bool {
        savedSummary != draft
    }

    /// A title edit worth sending: non-empty and changed.
    public static func titleChange(current: String, edited: String) -> String? {
        let t = edited.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty || t == current ? nil : t
    }
}
