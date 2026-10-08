import Foundation

// MARK: - Bring-your-own-key Claude client
//
// Sentinel's AI features (Ring-style scene descriptions, "what happened today?"
// digests, and natural-language event search) call the Anthropic API DIRECTLY
// from this Mac using the operator's own API key. There is no Sentinel backend
// in the loop and no per-alert metering — the user owns the key and the bill.
//
// The key is stored in the encrypted SecretVault (see `SentinelAISettings`) and
// never leaves this machine. macOS apps are not browsers, so calling
// `api.anthropic.com` directly via URLSession is the supported path (no CORS).
//
// The prompts, models, and output shapes below are ported verbatim from the old
// `sentinel-describe` / `sentinel-search` / `sentinel-digest` edge functions so
// behavior is unchanged — only the transport (direct, key-authenticated) differs.
public struct SentinelAIClient {

    // MARK: Configuration

    static let messagesEndpoint = URL(string: "https://api.anthropic.com/v1/messages")!
    static let modelsEndpoint = URL(string: "https://api.anthropic.com/v1/models")!
    static let anthropicVersion = "2023-06-01"

    /// Haiku: cheapest/fastest vision model — plenty for clothing colors, carried
    /// objects, basic actions, and a coarse routine-vs-suspicious read.
    static let visionModel = "claude-haiku-4-5"
    /// Sonnet: stronger reasoning for digests and event search. Sonnet 5.5 always
    /// thinks (it can't be disabled), so text requests run at `effort: low` with
    /// headroom in max_tokens — thinking tokens count against that cap.
    static let textModel = "claude-sonnet-5-5"
    static let textEffort = "low"
    /// Server-side refusal fallback: a declined request is re-run on Anthropic's
    /// recommended model for that refusal category instead of failing.
    static let fallbackBeta = "server-side-fallback-2026-07-01"

    /// One AI call analyzes at most this many consecutive frames of one event.
    static let maxFrames = 5
    /// Hard caps so a huge event log can't blow up a single request.
    static let maxSearchEvents = 600
    static let maxDigestEvents = 400

    let apiKey: String
    let session: URLSession

    public init(apiKey: String, session: URLSession = .shared) {
        self.apiKey = apiKey
        self.session = session
    }

    public enum AIError: LocalizedError {
        case noKey
        case disabled
        case http(Int, String)
        case malformed
        case refused

        public var errorDescription: String? {
            switch self {
            case .disabled:
                return "AI is turned off. Enable it in Settings → AI to use this feature."
            case .noKey:
                return "No Anthropic API key set. Add your key in Settings → AI."
            case .http(let code, let message):
                switch code {
                case 401: return "Anthropic rejected the API key. Check the key in Settings → AI."
                case 429: return "Anthropic rate limit reached. Try again shortly."
                case 529: return "Anthropic is temporarily overloaded. Try again shortly."
                default:  return message.isEmpty ? "Claude request failed (\(code))." : message
                }
            case .malformed:
                return "Unexpected response from Claude."
            case .refused:
                return "Claude declined this request. Try rephrasing it."
            }
        }
    }

    // MARK: Key validation

    /// Cheap liveness/validity check for an API key: a `GET /v1/models` costs no
    /// tokens. Returns true only on a 2xx. Used by Settings to show a key as good.
    public static func validate(key: String, session: URLSession = .shared) async -> Bool {
        let trimmed = key.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return false }
        var req = URLRequest(url: modelsEndpoint)
        req.setValue(trimmed, forHTTPHeaderField: "x-api-key")
        req.setValue(anthropicVersion, forHTTPHeaderField: "anthropic-version")
        guard let (_, response) = try? await session.data(for: req),
              let http = response as? HTTPURLResponse else { return false }
        return (200..<300).contains(http.statusCode)
    }

    // MARK: Scene analysis (vision)

    private static let describePrompt =
        "You are a security camera assistant. The image(s) are consecutive frames from ONE event, " +
        "oldest first. Describe the person(s) and what happens across the frames for a security alert, " +
        "and judge how suspicious it looks. Reply ONLY with a JSON object and nothing else:\n" +
        "{\"description\": \"<one short sentence, max ~20 words; clothing colors, carried items, action>\", " +
        "\"threat\": \"none|low|elevated|high\", " +
        "\"anomaly\": <true if this looks unusual or worth attention, else false>, " +
        "\"reason\": \"<short reason for the threat level, max ~12 words>\", " +
        "\"tags\": [\"<a few short tags: e.g. person, vehicle, package, night, loitering>\"]}\n" +
        "Routine activity (a resident, a normal delivery) is threat \"none\" or \"low\". Reserve " +
        "\"elevated\"/\"high\" for things like loitering, testing doors/handles, masked faces, or " +
        "forced entry. If no person is clearly visible, set description to 'No person clearly visible.' " +
        "and threat to 'none'."

    /// Sends one or more consecutive JPEG frames to Claude vision and returns the
    /// structured analysis: a Ring-style description plus a routine-vs-suspicious
    /// threat read, anomaly flag, reason, and tags.
    public func analyzeScene(jpegs: [Data], mediaType: String = "image/jpeg") async throws -> SceneAnalysis {
        guard !jpegs.isEmpty else { throw AIError.malformed }
        let frames = Array(jpegs.prefix(Self.maxFrames))

        var content: [[String: Any]] = frames.map { jpeg in
            [
                "type": "image",
                "source": ["type": "base64", "media_type": mediaType, "data": jpeg.base64EncodedString()],
            ]
        }
        content.append(["type": "text", "text": Self.describePrompt])

        let raw = try await postMessage(model: Self.visionModel, maxTokens: 220, system: nil,
                                        userContent: content)

        // Parse the structured JSON; degrade gracefully to a plain description.
        var description = raw
        var threat: ThreatLevel = .none
        var isAnomaly = false
        var reason = ""
        var tags: [String] = []
        if let obj = Self.firstJSONObject(in: raw) {
            if let d = obj["description"] as? String { description = d.trimmingCharacters(in: .whitespacesAndNewlines) }
            if let t = obj["threat"] as? String, let parsed = ThreatLevel(rawValue: t) { threat = parsed }
            isAnomaly = (obj["anomaly"] as? Bool) == true
            if let r = obj["reason"] as? String { reason = r.trimmingCharacters(in: .whitespacesAndNewlines) }
            if let raw = obj["tags"] as? [Any] {
                tags = raw.compactMap { $0 as? String }.prefix(6).map { $0 }
            }
        }
        return SceneAnalysis(description: description, threat: threat,
                             isAnomaly: isAnomaly, reason: reason, tags: tags)
    }

    // MARK: Daily digest

    private static let digestSystem =
        "You are the security analyst for a self-hosted home/business camera system. " +
        "Given a time-ordered list of detection events (each with a time, camera, type, " +
        "and a short description), write a brief situational digest the owner can read in " +
        "ten seconds. Group similar activity, call out anything unusual or worth attention " +
        "(loitering, unfamiliar vehicles, night activity, repeated visits), and stay factual " +
        "— never invent details that aren't in the events. If nothing notable happened, say so plainly."

    /// Summarizes a window of detection events into a short digest ("What
    /// happened today?"). `events` are lightweight dictionaries the app builds
    /// from its event log (time, camera, kind, description).
    public func dailyDigest(label: String, events: [[String: String]]) async throws -> String {
        guard !events.isEmpty else { return "No detection events \(label)." }
        let shown = Array(events.prefix(Self.maxDigestEvents))
        let lines = shown.map { Self.eventLine($0, includeIndex: nil) }
        let omitted = max(0, events.count - Self.maxDigestEvents)
        let userMsg =
            "Detection events for \(label) (\(events.count) total" +
            (omitted > 0 ? ", showing the first \(Self.maxDigestEvents)" : "") + "):\n\n" +
            lines.joined(separator: "\n") +
            "\n\nWrite the digest now. Start with a one-line headline, then 2–5 short bullet points."

        let raw = try await postMessage(model: Self.textModel, maxTokens: 4000,
                                        system: Self.digestSystem, userText: userMsg)
        return raw.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    // MARK: Natural-language event search

    private static let searchSystem =
        "You search a security camera's detection-event log. You are given a user query and a " +
        "numbered list of events (each: index, time, camera, type, description). Return ONLY the " +
        "events that genuinely match the query's intent — match on described attributes (clothing " +
        "colors, carried objects, vehicles, actions), camera, type, and time. Be precise: do not " +
        "return weak matches. Reply with a single JSON object and nothing else: " +
        "{\"answer\": \"<one short sentence answering the query>\", \"matches\": [<event indexes>]}. " +
        "If nothing matches, return an empty matches array and say so in the answer."

    /// Natural-language search over the event log. Returns Claude's one-line
    /// answer plus the ids of the events that matched (for jumping to clips).
    /// Each event dictionary should carry an `id` (UUID string) the answer maps back to.
    public func searchEvents(query: String, events: [[String: String]]) async throws -> (answer: String, matches: [UUID]) {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { throw AIError.malformed }
        guard !events.isEmpty else { return ("No events to search.", []) }

        let corpus = events.prefix(Self.maxSearchEvents).enumerated()
            .map { Self.eventLine($1, includeIndex: $0) }
            .joined(separator: "\n")
        // "Now" lets the model resolve relative times ("yesterday afternoon",
        // "last night") against each event's full date.
        let now = Self.searchDateFormatter.string(from: Date())
        let userMsg = "Current local time: \(now)\n\nQuery: \(trimmed)\n\nEvents:\n\(corpus)"

        let raw = try await postMessage(model: Self.textModel, maxTokens: 6000,
                                        system: Self.searchSystem, userText: userMsg)

        var answer = raw
        var idxs: [Int] = []
        if let obj = Self.firstJSONObject(in: raw) {
            if let a = obj["answer"] as? String { answer = a }
            if let m = obj["matches"] as? [Any] {
                idxs = m.compactMap { value -> Int? in
                    if let i = value as? Int { return i }
                    if let d = value as? Double { return Int(d) }
                    if let s = value as? String { return Int(s) }
                    return nil
                }
            }
        }
        // Map indexes back to the event ids the app can jump to.
        let matches: [UUID] = idxs
            .filter { $0 >= 0 && $0 < events.count }
            .compactMap { events[$0]["id"] }
            .compactMap(UUID.init(uuidString:))
        return (answer.trimmingCharacters(in: .whitespacesAndNewlines), matches)
    }

    // MARK: - Transport

    /// POSTs a message with a plain-text user turn and returns the first text block.
    private func postMessage(model: String, maxTokens: Int, system: String?, userText: String) async throws -> String {
        try await postMessage(model: model, maxTokens: maxTokens, system: system,
                              userContent: userText)
    }

    /// POSTs a message whose user `content` is either a String or an array of
    /// content blocks (for multimodal/vision requests), and returns the first
    /// text block of the response.
    private func postMessage(model: String, maxTokens: Int, system: String?, userContent: Any) async throws -> String {
        let key = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !key.isEmpty else { throw AIError.noKey }

        var payload: [String: Any] = [
            "model": model,
            "max_tokens": maxTokens,
            "messages": [["role": "user", "content": userContent]],
        ]
        if let system { payload["system"] = system }
        let isTextModel = model == Self.textModel
        if isTextModel {
            payload["output_config"] = ["effort": Self.textEffort]
            payload["fallbacks"] = "default"
        }

        var req = URLRequest(url: Self.messagesEndpoint)
        req.httpMethod = "POST"
        req.setValue(key, forHTTPHeaderField: "x-api-key")
        req.setValue(Self.anthropicVersion, forHTTPHeaderField: "anthropic-version")
        req.setValue("application/json", forHTTPHeaderField: "content-type")
        if isTextModel { req.setValue(Self.fallbackBeta, forHTTPHeaderField: "anthropic-beta") }
        req.httpBody = try JSONSerialization.data(withJSONObject: payload)

        let (data, response) = try await session.data(for: req)
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            throw AIError.http(http.statusCode, Self.errorMessage(from: data))
        }
        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let content = obj["content"] as? [[String: Any]] else {
            throw AIError.malformed
        }
        // A declined request returns 200 with stop_reason "refusal" (only after any
        // fallback also declined) — never read its content as an answer.
        if (obj["stop_reason"] as? String) == "refusal" { throw AIError.refused }
        // First text block.
        let text = content.first { ($0["type"] as? String) == "text" }?["text"] as? String
        guard let text else { throw AIError.malformed }
        return text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    // MARK: - Helpers

    /// "Tue Oct 6 2:05 PM" — full local date+time for event lines and "now".
    public static let searchDateFormatter: DateFormatter = {
        let f = DateFormatter()
        f.setLocalizedDateFormatFromTemplate("EEE MMM d yyyy h:mm a")
        return f
    }()

    /// Formats one event log line, matching the edge functions' corpus format.
    /// `[index] time · camera · kind — description`.
    private static func eventLine(_ e: [String: String], includeIndex: Int?) -> String {
        let parts = ["time", "camera", "kind"].compactMap { key -> String? in
            let v = e[key]?.trimmingCharacters(in: .whitespaces)
            return (v?.isEmpty == false) ? v : nil
        }.joined(separator: " · ")
        let desc = e["description"]?.trimmingCharacters(in: .whitespaces)
        var body = parts + ((desc?.isEmpty == false) ? " — \(desc!)" : "")
        if let tags = e["tags"], tags.isEmpty == false { body += " [tags: \(tags)]" }
        if let threat = e["threat"], threat.isEmpty == false { body += " [threat: \(threat)]" }
        if let i = includeIndex { return "[\(i)] \(body)" }
        return body
    }

    /// Extracts the first balanced `{...}` JSON object from arbitrary model text
    /// (tolerates code fences / stray prose around it).
    private static func firstJSONObject(in text: String) -> [String: Any]? {
        guard let start = text.firstIndex(of: "{"),
              let end = text.lastIndex(of: "}"), start < end else { return nil }
        let slice = String(text[start...end])
        guard let data = slice.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
        return obj
    }

    /// Pulls Anthropic's `{ "error": { "message": ... } }` out of a failure body.
    private static func errorMessage(from data: Data) -> String {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return String(data: data, encoding: .utf8) ?? ""
        }
        if let err = obj["error"] as? [String: Any], let msg = err["message"] as? String { return msg }
        if let msg = obj["error"] as? String { return msg }
        return String(data: data, encoding: .utf8) ?? ""
    }
}
