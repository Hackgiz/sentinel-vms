import Foundation

/// Wave 6 management calls (`WAVE6-ADMIN.md`).
public extension SentinelAPI {
    // MARK: Site

    func siteInfo() async throws -> V2.SiteInfo {
        try await send("GET", "/api/v2/site")
    }

    func updateSiteInfo(_ info: V2.SiteInfo) async throws -> V2.SiteInfo {
        try await send("PUT", "/api/v2/site", body: info)
    }

    // MARK: Roles

    func roles() async throws -> [V2.RoleRecord] {
        let list: V2.Items<V2.RoleRecord> = try await send("GET", "/api/v2/roles")
        return list.items
    }

    func permissionCatalog() async throws -> [V2.PermissionInfo] {
        let list: V2.Items<V2.PermissionInfo> = try await send("GET", "/api/v2/permissions")
        return list.items
    }

    func createRole(_ input: V2.RoleInput) async throws -> V2.RoleRecord {
        try await send("POST", "/api/v2/roles", body: input)
    }

    func updateRole(_ id: String, _ input: V2.RoleInput) async throws -> V2.RoleRecord {
        try await send("PUT", "/api/v2/roles/\(Self.escape(id))", body: input)
    }

    /// 409 while users still hold the role (the message carries the count).
    func deleteRole(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/roles/\(Self.escape(id))")
    }

    // MARK: Time profiles

    func timeProfiles() async throws -> [V2.TimeProfile] {
        let list: V2.Items<V2.TimeProfile> = try await send("GET", "/api/v2/time-profiles")
        return list.items
    }

    func createTimeProfile(_ input: V2.TimeProfileInput) async throws -> V2.TimeProfile {
        try await send("POST", "/api/v2/time-profiles", body: input)
    }

    func updateTimeProfile(_ id: String, _ input: V2.TimeProfileInput) async throws -> V2.TimeProfile {
        try await send("PUT", "/api/v2/time-profiles/\(Self.escape(id))", body: input)
    }

    /// 409 while a rule references the profile.
    func deleteTimeProfile(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/time-profiles/\(Self.escape(id))")
    }

    // MARK: Webhooks

    func webhooks() async throws -> [V2.Webhook] {
        let list: V2.Items<V2.Webhook> = try await send("GET", "/api/v2/webhooks")
        return list.items
    }

    func createWebhook(_ input: V2.WebhookInput) async throws -> V2.Webhook {
        try await send("POST", "/api/v2/webhooks", body: input)
    }

    func updateWebhook(_ id: String, _ input: V2.WebhookInput) async throws -> V2.Webhook {
        try await send("PUT", "/api/v2/webhooks/\(Self.escape(id))", body: input)
    }

    func deleteWebhook(_ id: String) async throws {
        try await sendNoContent("DELETE", "/api/v2/webhooks/\(Self.escape(id))")
    }

    func testWebhook(_ id: String) async throws -> V2.WebhookTestResult {
        try await send("POST", "/api/v2/webhooks/\(Self.escape(id))/test")
    }

    // MARK: Thresholds

    func monitorThresholds() async throws -> V2.MonitorThresholds {
        try await send("GET", "/api/v2/system/thresholds")
    }

    func updateMonitorThresholds(_ t: V2.MonitorThresholds) async throws -> V2.MonitorThresholds {
        try await send("PUT", "/api/v2/system/thresholds", body: t)
    }

    // MARK: Tasks and logs

    func systemTasks() async throws -> [V2.SystemTask] {
        let list: V2.Items<V2.SystemTask> = try await send("GET", "/api/v2/system/tasks")
        return list.items
    }

    /// `server` empty = the management server. `level` empty = all levels.
    func serverLogs(server: String = "", level: String = "", limit: Int = 500) async throws -> [V2.LogLine] {
        var query = [URLQueryItem(name: "limit", value: String(limit))]
        if server.isEmpty == false { query.append(URLQueryItem(name: "server", value: server)) }
        if level.isEmpty == false { query.append(URLQueryItem(name: "level", value: level)) }
        let list: V2.Items<V2.LogLine> = try await send("GET", "/api/v2/system/logs", query: query)
        return list.items
    }

    // MARK: Configuration report

    /// The redacted snapshot: JSON by default, the printable page for `html`.
    func configReport(html: Bool = false) async throws -> Data {
        let query = html ? [URLQueryItem(name: "format", value: "html")] : []
        let (data, _) = try await raw("GET", "/api/v2/system/config-report", query: query)
        return data
    }

    // MARK: Notifications (v1, as-is)

    func pushStatus() async throws -> V2.PushStatus {
        try await send("GET", "/api/v1/push")
    }

    func updatePush(_ update: V2.PushUpdate) async throws -> V2.PushStatus {
        try await send("PUT", "/api/v1/push", body: update)
    }

    func testPush() async throws -> V2.PushTestResult {
        try await send("POST", "/api/v1/push/test")
    }

    func mqttSettings() async throws -> V2.MQTTSettings {
        try await send("GET", "/api/v1/mqtt")
    }

    func updateMQTT(_ update: V2.MQTTUpdate) async throws -> V2.MQTTSettings {
        try await send("PUT", "/api/v1/mqtt", body: update)
    }

    func testMQTT(_ update: V2.MQTTUpdate = V2.MQTTUpdate()) async throws -> V2.MQTTTestResult {
        try await send("POST", "/api/v1/mqtt/test", body: update)
    }
}
