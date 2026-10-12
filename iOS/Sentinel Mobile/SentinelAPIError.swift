import Foundation

/// Everything a `SentinelAPI` call can fail with, mapped from the contract's
/// error shape `{"error": {"code", "message"}}` and the HTTP status.
enum SentinelAPIError: Error, Equatable, LocalizedError, Sendable {
    /// The server answered with an error. `code` is the stable machine code
    /// (bad_request, unauthorized, forbidden, not_found, conflict,
    /// too_many_requests, unavailable, timeout, token_reused, internal);
    /// `message` is safe to show to a person.
    case server(status: Int, code: String, message: String)
    /// The sign-in ended (refresh token expired, revoked or reused). The user
    /// has to sign in again.
    case signInRequired(message: String)
    /// No response: the server is unreachable, the connection dropped, TLS failed.
    case transport(message: String)
    /// The response didn't match the contract.
    case decoding(message: String)

    var errorDescription: String? {
        switch self {
        case let .server(_, _, message): return message
        case let .signInRequired(message): return message
        case let .transport(message): return message
        case let .decoding(message): return "The server's answer wasn't understood (\(message)). Sentinel Mobile may need an update."
        }
    }

    var code: String? {
        if case let .server(_, code, _) = self { return code }
        return nil
    }

    var status: Int? {
        if case let .server(status, _, _) = self { return status }
        return nil
    }

    var isUnauthorized: Bool { status == 401 || code == "unauthorized" }
    var isForbidden: Bool { status == 403 || code == "forbidden" }
    var isNotFound: Bool { status == 404 || code == "not_found" }
    var isConflict: Bool { status == 409 || code == "conflict" }
    var isUnavailable: Bool { status == 503 || code == "unavailable" }

    /// Builds the error for a non-2xx response. Bodies that aren't the v2
    /// error shape (a proxy page, the v1 `{"error": "text"}` shape) still give
    /// a readable message.
    static func from(status: Int, body: Data) -> SentinelAPIError {
        if let envelope = try? JSONDecoder().decode(V2.ErrorEnvelope.self, from: body) {
            return .server(status: status, code: envelope.error.code, message: envelope.error.message)
        }
        if let v1 = try? JSONDecoder().decode([String: String].self, from: body), let message = v1["error"] {
            return .server(status: status, code: defaultCode(for: status), message: message)
        }
        return .server(status: status, code: defaultCode(for: status), message: defaultMessage(for: status))
    }

    static func defaultCode(for status: Int) -> String {
        switch status {
        case 400: return "bad_request"
        case 401: return "unauthorized"
        case 403: return "forbidden"
        case 404: return "not_found"
        case 409: return "conflict"
        case 429: return "too_many_requests"
        case 503: return "unavailable"
        case 504: return "timeout"
        default: return "internal"
        }
    }

    static func defaultMessage(for status: Int) -> String {
        switch status {
        case 401: return "Sign in again."
        case 403: return "Your role can't do this."
        case 404: return "Not found."
        case 429: return "Too many attempts. Wait a moment and try again."
        case 502, 503, 504: return "The server isn't reachable right now."
        default: return "The server answered with an error (HTTP \(status))."
        }
    }
}
