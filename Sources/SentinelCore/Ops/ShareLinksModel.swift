import Foundation

// Pure logic for the evidence "Share…" dialog, the Shared links list and
// the "Package…" action (port of web `shareModel.ts`).

public enum SharePreset: String, CaseIterable, Hashable, Sendable, Identifiable {
    case oneHour = "1h"
    case oneDay = "24h"
    case sevenDays = "7d"
    case custom

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .oneHour: return "1 hour"
        case .oneDay: return "24 hours"
        case .sevenDays: return "7 days"
        case .custom: return "Custom…"
        }
    }

    /// How long a preset lasts (nil for custom).
    public var duration: Int64? {
        switch self {
        case .oneHour: return 3_600_000
        case .oneDay: return 24 * 3_600_000
        case .sevenDays: return 7 * 24 * 3_600_000
        case .custom: return nil
        }
    }
}

public enum ShareRules {
    /// Longest custom expiry (the server's limit).
    public static let maxMs: Int64 = 30 * 24 * 3_600_000
    public static let maxUsesLimit = 1000
    public static let noteLimit = 500
    public static let onceWarning = "Copy it now: this link is shown only once."

    /// Why the dialog can't be submitted, or nil when it can.
    public static func blocker(preset: SharePreset, customAt: UnixMillis, maxUses: String,
                               now: UnixMillis = Date().unixMillis) -> String? {
        if preset == .custom {
            if customAt <= 0 { return "Pick an expiry date and time." }
            if customAt <= now { return "The expiry must be in the future." }
            if customAt > now + maxMs { return "A link can last at most 30 days." }
        }
        if parseMaxUses(maxUses) == nil { return "Max uses must be a whole number from 0 to 1000." }
        return nil
    }

    /// Blank = 0 (unlimited); nil when the text isn't a whole number 0…1000.
    public static func parseMaxUses(_ text: String) -> Int? {
        let t = text.trimmingCharacters(in: .whitespaces)
        if t.isEmpty { return 0 }
        guard t.allSatisfy({ $0.isASCII && $0.isNumber }), let n = Int(t), (0...maxUsesLimit).contains(n) else { return nil }
        return n
    }

    /// The POST body for a dialog state.
    public static func request(preset: SharePreset, customAt: UnixMillis, maxUses: String, note: String) -> V2.ShareLinkRequest {
        let trimmed = note.trimmingCharacters(in: .whitespacesAndNewlines)
        return V2.ShareLinkRequest(expiry: preset.rawValue,
                                   expiresAt: preset == .custom ? customAt : nil,
                                   maxUses: parseMaxUses(maxUses) ?? 0,
                                   note: trimmed.isEmpty ? nil : String(trimmed.prefix(noteLimit)))
    }

    /// "3 of 5", or "3" for an unlimited link.
    public static func usesText(_ l: V2.ShareLink) -> String {
        l.maxUses > 0 ? "\(l.uses) of \(l.maxUses)" : "\(l.uses)"
    }

    /// Links that can still be opened come first, then by newest.
    public static func sorted(_ list: [V2.ShareLink]) -> [V2.ShareLink] {
        list.sorted { a, b in
            let aa = a.status == .active, ba = b.status == .active
            if aa != ba { return aa }
            return a.createdAt > b.createdAt
        }
    }

    public static func canRevoke(_ l: V2.ShareLink) -> Bool { l.status == .active }

    /// What anyone holding the link gets, for the dialog's text.
    public static func createdText(caseID: String) -> String {
        "\(onceWarning) Anyone on this network who opens it can watch and download \(caseID) until it expires or you revoke it."
    }

    public static func revokeText(caseID: String) -> String {
        "Anyone holding the link to \(caseID) will get “no longer available” straight away."
    }
}

/// Share URLs made in this run of the app, so the Shared links list can
/// offer "Copy" for them. The server keeps only the token's hash, so a link
/// made elsewhere (or before a relaunch) can't be copied again. In memory
/// only: the URL is a bearer credential and is never written to disk.
public struct ShareLinkURLCache: Equatable, Sendable {
    private var urls: [String: String] = [:]

    public init() {}

    public mutating func remember(id: String, url: String) {
        guard id.isEmpty == false, url.isEmpty == false else { return }
        urls[id] = url
    }

    /// The URL, only while the link can still be opened.
    public func url(for link: V2.ShareLink) -> String? {
        link.status == .active ? urls[link.id] : nil
    }

    public mutating func forget(id: String) { urls[id] = nil }

    /// Drops links that are gone or can no longer be opened.
    public mutating func prune(keeping links: [V2.ShareLink]) {
        let active = Set(links.filter { $0.status == .active }.map(\.id))
        urls = urls.filter { active.contains($0.key) }
    }

    public var count: Int { urls.count }
}

public enum EvidencePackageRules {
    /// The Save panel's default name: "<caseId>-package.zip".
    public static func defaultName(caseID: String) -> String {
        "\(FileNames.safe(caseID.isEmpty ? "evidence" : caseID))-package.zip"
    }

    /// The server's message, with the 409 (file no longer matches) spelled out when it has none.
    public static func failureText(_ error: Error) -> String {
        if let api = error as? SentinelAPIError, case .server(let status, _, let message) = api, status == 409, message.isEmpty {
            return "The vault file no longer matches its recorded SHA-256, so it can't be packaged."
        }
        return ExportRules.failureText(error)
    }
}
