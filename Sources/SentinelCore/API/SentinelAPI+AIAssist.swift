import Foundation

// Rule drafts, rule templates and Insights (wave 15). Draft failures map
// through `AIFailure`: 409 AI off, 429 daily limit, 502 model call failed.

public extension SentinelAPI {
    /// Drafts a rule from a plain-English description (ChangeSettings). One AI
    /// request; nothing is saved.
    func draftRule(_ text: String) async throws -> V2.RuleDraft {
        try await send("POST", "/api/v2/rules/draft", body: V2.RuleDraftRequest(text: text))
    }

    /// Ready-made rules to start from (ViewLive).
    func ruleTemplates() async throws -> [V2.RuleTemplate] {
        let list: V2.Items<V2.RuleTemplate> = try await send("GET", "/api/v2/rule-templates")
        return list.items
    }

    /// Trends and recommendations for a range (ViewLive). Nil bounds leave the
    /// server's default, the last 7 days.
    func insights(from: UnixMillis? = nil, to: UnixMillis? = nil) async throws -> V2.Insights {
        try await send("GET", "/api/v2/insights", query: [
            URLQueryItem(name: "from", value: from.map(String.init)),
            URLQueryItem(name: "to", value: to.map(String.init))
        ])
    }
}
