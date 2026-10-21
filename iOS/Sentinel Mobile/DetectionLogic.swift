// DetectionLogic.swift
// Wave 16 detection on the phone without the screens (WAVE17): person and
// plate chips, "Describe it" availability, the request, score formatting and
// error text, and the People list's labels. Mirrors the web's
// `operator/semanticModel.ts` and `admin/peopleModel.ts` wording, kept pure
// so it is tested without a server.

import Foundation

enum DetectionLogic {
    // MARK: Chips

    /// The recognized person's name, or nil when nobody was matched.
    static func personChip(_ event: V2.Event) -> String? {
        let name = event.personName?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return name.isEmpty ? nil : name
    }

    /// The plate read on the event, upper-cased, or nil.
    static func plateChip(_ event: V2.Event) -> String? {
        let plate = event.plate?.trimmingCharacters(in: .whitespacesAndNewlines).uppercased() ?? ""
        return plate.isEmpty ? nil : plate
    }

    /// The event detail's lines: "Recognized: <name>", "Plate: <text>".
    static func detailLines(_ event: V2.Event) -> [String] {
        var lines: [String] = []
        if let name = personChip(event) { lines.append("Recognized: \(name)") }
        if let plate = plateChip(event) { lines.append("Plate: \(plate)") }
        return lines
    }

    // MARK: Semantic search

    static let textMin = 2
    static let textMax = 200
    static let defaultLimit = 30

    /// Whether "Describe it" is offered. The field shows only when available.
    enum SemanticAvailability: Equatable, Sendable {
        case checking
        case available(indexed: Int)
        case unavailable(reason: String)
        /// The server predates semantic search (404) or the user can't read it.
        case unsupported

        var showsField: Bool {
            if case .available = self { return true }
            return false
        }
    }

    static func availability(_ status: V2.SemanticStatus) -> SemanticAvailability {
        if status.available { return .available(indexed: status.indexed ?? 0) }
        let reason = status.reason?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return .unavailable(reason: reason.isEmpty ? "no semantic search model is installed" : reason)
    }

    /// A failed status read: an older server or a 403 hides the field.
    static func availability(failedWith error: Error) -> SemanticAvailability {
        if let e = error as? SentinelAPIError, e.isNotFound || e.isForbidden { return .unsupported }
        return .unavailable(reason: errorMessage(error))
    }

    /// The placeholder under the field, with the indexed count.
    static func indexedNote(_ indexed: Int) -> String {
        let n = indexed.formatted(.number.locale(Locale(identifier: "en_US")))
        return "Searches snapshots by what they show, on this server. \(n) event\(indexed == 1 ? "" : "s") indexed."
    }

    /// What's wrong with the description, or nil.
    static func textProblem(_ text: String) -> String? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.count < textMin { return "Describe what you're looking for in a few words." }
        if t.count > textMax { return "Keep the description under \(textMax) characters." }
        return nil
    }

    static func semanticRequest(_ text: String, limit: Int = defaultLimit) -> V2.SemanticSearch {
        let t = String(text.trimmingCharacters(in: .whitespacesAndNewlines).prefix(textMax))
        return V2.SemanticSearch(text: t, limit: max(1, min(100, limit)))
    }

    /// The raw cosine score, two decimals: image-text scores sit in a narrow band.
    static func scoreText(_ score: Double) -> String {
        score.isFinite ? String(format: "%.2f", score) : "—"
    }

    /// Bar fill (0...1) for each hit, relative to the best one.
    static func scoreBars(_ hits: [V2.SemanticHit]) -> [Double] {
        let scores = hits.map { $0.score.isFinite ? max(0, $0.score) : 0 }
        let top = scores.max() ?? 0
        return scores.map { top > 0 ? $0 / top : 0 }
    }

    /// What a failed search says; `unavailable` (503) means the model is missing.
    static func semanticError(_ error: Error) -> (text: String, unavailable: Bool) {
        if let e = error as? SentinelAPIError, e.isUnavailable {
            let message = errorMessage(e)
            let reason = message.replacingOccurrences(of: "^semantic search is unavailable:\\s*", with: "",
                                                      options: [.regularExpression, .caseInsensitive])
            return ("Semantic search is unavailable: \(reason)", true)
        }
        return (errorMessage(error), false)
    }

    // MARK: People

    static func alertLabel(_ alert: V2.WatchAlert?) -> String {
        switch alert ?? .default {
        case .default: return "Name them in the alarm"
        case .never: return "Don't alarm (known person)"
        case .always: return "Critical watchlist alarm"
        case let other: return other.rawValue
        }
    }

    /// People by name, ignoring case.
    static func sortPeople(_ people: [V2.Person]) -> [V2.Person] {
        people.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    /// "5 min ago", "3 h ago", "2 days ago", or a date past a week.
    static func ago(_ t: UnixMillis?, now: UnixMillis) -> String {
        guard let t, t > 0 else { return "Never" }
        let s = max(0, (now - t) / 1000)
        if s < 60 { return "Just now" }
        if s < 3600 { return "\(s / 60) min ago" }
        if s < 86400 { return "\(s / 3600) h ago" }
        let d = s / 86400
        if d < 7 { return "\(d) day\(d == 1 ? "" : "s") ago" }
        return Date(unixMillis: t).formatted(date: .abbreviated, time: .omitted)
    }

    /// "3 h ago · Front door", or "Never".
    static func lastSeen(_ person: V2.Person, cameraName: (String) -> String?, now: UnixMillis) -> String {
        guard person.lastSeenAt != nil else { return "Never" }
        let when = ago(person.lastSeenAt, now: now)
        guard let id = person.lastSeenCameraId, let cam = cameraName(id) else { return when }
        return "\(when) · \(cam)"
    }

    /// The text for a failed load: the server's own message, or that an older
    /// server has no people gallery.
    static func peopleError(_ error: Error) -> String {
        if let e = error as? SentinelAPIError, e.isNotFound {
            return "This server doesn't have People yet. Update the Sentinel server to see recognized people."
        }
        return errorMessage(error)
    }

    static func errorMessage(_ error: Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }
}
