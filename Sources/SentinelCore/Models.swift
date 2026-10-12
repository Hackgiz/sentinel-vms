import Foundation
import SwiftUI

// Shared value types the Operator client still uses. The 1.x app models
// (CameraFeed, CameraStore, AlertEvent, …) went with the built-in server.

public enum CameraStatus: String, Codable, Hashable {
    case online
    case motion
    case offline

    public var label: String {
        switch self {
        case .online: return "Online"
        case .motion: return "Motion"
        case .offline: return "Offline"
        }
    }

    public var symbol: String {
        switch self {
        case .online: return "checkmark.circle.fill"
        case .motion: return "figure.walk"
        case .offline: return "xmark.octagon.fill"
        }
    }

    public var tint: Color {
        switch self {
        case .online: return .green
        case .motion: return .orange
        case .offline: return .red
        }
    }
}


public struct SanitizedRTSPInput {
    public let url: String
    public let username: String
    public let password: String

    public var hasEmbeddedCredentials: Bool {
        username.isEmpty == false || password.isEmpty == false
    }
}

public enum RTSPCredentialFormatter {
    public static func sanitize(_ rawURL: String) -> SanitizedRTSPInput {
        let trimmedURL = rawURL.trimmingCharacters(in: .whitespacesAndNewlines)

        guard var components = URLComponents(string: trimmedURL) else {
            return SanitizedRTSPInput(url: trimmedURL, username: "", password: "")
        }

        let username = components.user ?? ""
        let password = components.password ?? ""
        components.user = nil
        components.password = nil

        return SanitizedRTSPInput(
            url: components.string ?? trimmedURL,
            username: username,
            password: password
        )
    }

    public static func url(_ rawURL: String, username: String, password: String) -> String {
        guard rawURL.lowercased().hasPrefix("rtsp://"),
              password.isEmpty == false,
              var components = URLComponents(string: rawURL) else {
            return rawURL
        }

        if components.password?.isEmpty == false {
            return rawURL
        }

        let existingUser = components.user ?? ""
        let resolvedUser = username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? existingUser : username

        guard resolvedUser.isEmpty == false else {
            return rawURL
        }

        components.user = resolvedUser
        components.password = password
        return components.string ?? rawURL
    }

    public static func redacted(_ rawURL: String) -> String {
        guard let rtspRange = rawURL.range(of: "rtsp://", options: [.caseInsensitive]) else {
            return rawURL
        }

        let prefix = String(rawURL[..<rtspRange.lowerBound])
        let urlPart = String(rawURL[rtspRange.lowerBound...])
        guard var components = URLComponents(string: urlPart) else {
            return rawURL
        }

        if components.user != nil || components.password != nil {
            components.password = components.password == nil ? nil : "redacted"
        }

        return prefix + (components.string ?? urlPart)
    }

    public static func normalizedStreamKey(_ rawURL: String) -> String? {
        let sanitized = sanitize(rawURL).url
        guard var components = URLComponents(string: sanitized),
              components.scheme?.lowercased() == "rtsp",
              let host = components.host?.trimmingCharacters(in: .whitespacesAndNewlines),
              host.isEmpty == false else {
            return nil
        }

        if let port = components.port, (1...65535).contains(port) == false {
            return nil
        }

        components.scheme = "rtsp"
        components.host = host.lowercased()
        components.user = nil
        components.password = nil
        return components.string?.lowercased()
    }

    public static func isUsableRTSPURL(_ rawURL: String) -> Bool {
        normalizedStreamKey(rawURL) != nil
    }

    // Hard gate before any string ends up inside a GStreamer pipeline argument
    // (location=..., uri=...). GStreamer treats `!`, whitespace, and newlines
    // as element separators / property delimiters, so an attacker-controlled
    // RTSP URL that contains them could splice in extra elements or override
    // sink locations. Reject anything that doesn't parse as a plain rtsp(s)
    // URL with a host and free of those bytes.
    public static func isSafeForPipeline(_ rawURL: String) -> Bool {
        guard rawURL.isEmpty == false else { return false }
        // Reject whitespace, GStreamer separator `!`, NULs, quotes, backslash.
        let forbidden: Set<Character> = ["!", " ", "\t", "\n", "\r", "\0", "\"", "'", "\\", "`", "$"]
        if rawURL.contains(where: { forbidden.contains($0) }) {
            return false
        }
        guard let components = URLComponents(string: rawURL),
              let scheme = components.scheme?.lowercased(),
              scheme == "rtsp" || scheme == "rtsps",
              let host = components.host, host.isEmpty == false else {
            return false
        }
        if let port = components.port, (1...65535).contains(port) == false {
            return false
        }
        return true
    }
}


public extension JSONEncoder {
    public static var prettySentinel: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        return encoder
    }
}

public enum AlertSeverity: String, Codable, Hashable, Sendable {
    case critical = "Critical"
    case warning = "Warning"
    case info = "Info"

    public var rank: Int {
        switch self {
        case .critical: return 0
        case .warning: return 1
        case .info: return 2
        }
    }

    public var symbol: String {
        switch self {
        case .critical: return "exclamationmark.octagon.fill"
        case .warning: return "exclamationmark.triangle.fill"
        case .info: return "info.circle.fill"
        }
    }

    public var tint: Color {
        switch self {
        case .critical: return .red
        case .warning: return .orange
        case .info: return SentinelTheme.accent
        }
    }
}

public enum AlertState: String, Codable, CaseIterable, Identifiable, Hashable, Sendable {
    case new = "New"
    case acknowledged = "Acknowledged"
    case investigating = "Investigating"
    case snoozed = "Snoozed"
    case resolved = "Resolved"
    case falseAlarm = "False Alarm"

    public var id: String { rawValue }

    public var isOpen: Bool {
        switch self {
        case .new, .acknowledged, .investigating, .snoozed:
            return true
        case .resolved, .falseAlarm:
            return false
        }
    }

    public var tint: Color {
        switch self {
        case .new: return .red
        case .acknowledged: return SentinelTheme.amber
        case .investigating: return SentinelTheme.accent
        case .snoozed: return .secondary
        case .resolved: return .green
        case .falseAlarm: return .secondary
        }
    }

    public static func normalized(_ rawValue: String) -> AlertState {
        switch rawValue {
        case "Closed":
            return .resolved
        case "False alarm", "FalseAlarm":
            return .falseAlarm
        default:
            return AlertState(rawValue: rawValue) ?? .new
        }
    }
}

