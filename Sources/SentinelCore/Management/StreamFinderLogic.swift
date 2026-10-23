import Foundation

// Wave 22 addendum: the view-free logic behind the Mac camera editor's
// "Second stream (low-res)" field and its Find streams picker. Wording follows
// WAVE22-RECORDING-MODES.md (Addendum) so the web and the Mac say the same thing.

public enum StreamFinderLogic {
    // MARK: Wording

    public static let fieldLabel = "Second stream (low-res)"
    public static let fieldHelp = "Used for live view, detection and low-res recording."
    public static let findButton = "Find streams"
    public static let useRecommendedButton = "Use recommended"
    public static let guessedBadge = "Guessed"
    public static let recommendedTag = "Recommended"
    /// The disabled dual option's hint (a button that runs Find streams).
    public static let dualHint = "Needs a second stream. Find streams…"
    public static let olderServerNote = "This server can't look for streams. Update it, or type the address by hand."
    public static let saveFirstNote = "Save the camera first, then use Find streams."

    /// The size the too-small banner offers.
    public static let resizeTarget = (width: 1280, height: 720)
    /// Smallest size that's useful for detection and live view.
    public static let minimumSub = (width: 640, height: 360)

    // MARK: Availability

    /// Find streams works on saved cameras only (the server looks the camera up by id),
    /// and not against a server that answered 404 (older than the addendum).
    public static func canFind(isSaved: Bool, serverSupportsDiscover: Bool) -> Bool {
        isSaved && serverSupportsDiscover
    }

    /// True when a discover/resize failure means "this server doesn't have the endpoint".
    public static func isOlderServer(_ error: Error) -> Bool {
        (error as? SentinelAPIError)?.isNotFound == true
    }

    /// The login to send: only when the user typed a new password in the editor
    /// (then with the username beside it); otherwise nil, nil = the saved login.
    public static func credentials(username: String, typedPassword: String?) -> (username: String?, password: String?) {
        guard let typedPassword, typedPassword.isEmpty == false else { return (nil, nil) }
        let user = username.trimmingCharacters(in: .whitespaces)
        return (user.isEmpty ? nil : user, typedPassword)
    }

    /// The inline error under the picker.
    public static func errorText(_ error: Error) -> String {
        if let api = error as? SentinelAPIError {
            if api.isAuthFailed {
                return "The camera rejected the username or password. Check them above and try again."
            }
            if api.isNotFound { return olderServerNote }
            if api.isForbidden { return "Your role can't change cameras." }
        }
        return (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }

    // MARK: Rows

    public struct Row: Hashable, Identifiable, Sendable {
        public var stream: V2.DiscoveredStream
        public var title: String
        /// "Guessed" for a URL-pattern guess, else nil.
        public var badge: String?
        public var recommended: Bool
        /// "Main stream now" / "Second stream now", else nil.
        public var roleNote: String?
        /// The editor's second stream field already holds this URL.
        public var selected: Bool
        public var id: String { stream.url }
    }

    /// The picker rows, in the server's order.
    public static func rows(_ discovery: V2.StreamDiscovery, mainURL: String, currentSub: String) -> [Row] {
        let rec = recommendedURL(discovery, mainURL: mainURL)
        let current = currentSub.trimmingCharacters(in: .whitespaces)
        return discovery.streams.map { s in
            Row(stream: s, title: title(s), badge: badge(s), recommended: s.url == rec,
                roleNote: roleNote(s), selected: current.isEmpty == false && s.url == current)
        }
    }

    /// The server's label, or one built from the numbers when it sent none.
    public static func title(_ s: V2.DiscoveredStream) -> String {
        let label = s.label.trimmingCharacters(in: .whitespaces)
        if label.isEmpty == false { return label }
        var parts: [String] = []
        if s.width > 0 && s.height > 0 { parts.append("\(s.width)×\(s.height)") }
        if s.codec.isEmpty == false { parts.append(codecName(s.codec)) }
        if s.fps > 0 { parts.append("\(fpsText(s.fps)) fps") }
        return parts.isEmpty ? s.url : parts.joined(separator: " · ")
    }

    public static func badge(_ s: V2.DiscoveredStream) -> String? {
        s.source.lowercased() == "guess" ? guessedBadge : nil
    }

    public static func roleNote(_ s: V2.DiscoveredStream) -> String? {
        switch s.role.lowercased() {
        case "main": return "Main stream now"
        case "sub": return "Second stream now"
        default: return nil
        }
    }

    static func codecName(_ codec: String) -> String {
        switch codec.uppercased() {
        case "H264": return "H.264"
        case "H265", "HEVC": return "H.265"
        case "MJPEG", "JPEG": return "MJPEG"
        default: return codec
        }
    }

    static func fpsText(_ fps: Double) -> String {
        fps.rounded() == fps ? String(Int(fps)) : String(format: "%.1f", fps)
    }

    // MARK: Recommended

    /// The server's `recommendedSub` when it is one of the streams; else the same rule
    /// computed here: the smallest H.264/H.265 stream ≥ 640×360 that isn't the main one.
    public static func recommendedURL(_ discovery: V2.StreamDiscovery, mainURL: String) -> String? {
        if let rec = discovery.recommendedSub, discovery.streams.contains(where: { $0.url == rec }) { return rec }
        return pickRecommended(discovery.streams, mainURL: mainURL)?.url
    }

    public static func recommended(_ discovery: V2.StreamDiscovery, mainURL: String) -> V2.DiscoveredStream? {
        recommendedURL(discovery, mainURL: mainURL).flatMap { url in discovery.streams.first { $0.url == url } }
    }

    public static func pickRecommended(_ streams: [V2.DiscoveredStream], mainURL: String) -> V2.DiscoveredStream? {
        let main = mainURL.trimmingCharacters(in: .whitespaces)
        return streams
            .filter { $0.role.lowercased() != "main" && $0.url != main }
            .filter { ["H264", "H265", "HEVC"].contains($0.codec.uppercased()) }
            .filter { $0.width >= minimumSub.width && $0.height >= minimumSub.height }
            .min { ($0.width * $0.height, $0.url) < ($1.width * $1.height, $1.url) }
    }

    // MARK: Banner

    public enum Banner: Hashable, Sendable {
        /// The only extra stream is too small but resizable: offer to make it 1280×720.
        case tooSmall(text: String, button: String, sub: V2.TooSmallSub)
        /// Nothing usable as a second stream.
        case noneFound(text: String)
        /// The field already holds the recommended stream.
        case alreadyUsing(text: String)
    }

    /// Which banner (if any) to show above the rows.
    public static func banner(_ discovery: V2.StreamDiscovery, mainURL: String, currentSub: String) -> Banner? {
        if let small = discovery.tooSmallSub, small.encoderToken.isEmpty == false {
            return .tooSmall(text: "This camera's second stream is only \(small.width)×\(small.height).",
                             button: "Make it \(resizeTarget.width)×\(resizeTarget.height)", sub: small)
        }
        let main = mainURL.trimmingCharacters(in: .whitespaces)
        let extras = discovery.streams.filter { $0.role.lowercased() != "main" && $0.url != main }
        if extras.isEmpty {
            return .noneFound(text: "No second stream found. The camera may not have one, or it may need turning on in its own settings.")
        }
        let current = currentSub.trimmingCharacters(in: .whitespaces)
        if current.isEmpty == false, current == recommendedURL(discovery, mainURL: mainURL) {
            return .alreadyUsing(text: "This camera already uses the recommended second stream.")
        }
        return nil
    }

    /// "Use recommended" is offered when there is a recommendation the field doesn't hold yet.
    public static func canUseRecommended(_ discovery: V2.StreamDiscovery, mainURL: String, currentSub: String) -> Bool {
        guard let rec = recommendedURL(discovery, mainURL: mainURL) else { return false }
        return rec != currentSub.trimmingCharacters(in: .whitespaces)
    }
}
