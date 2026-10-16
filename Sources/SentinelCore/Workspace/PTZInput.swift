import Foundation

// The PTZ overlay's view-free logic: which controls are held and what
// velocity that means, the key map, preset names, and the per-session probe
// cache. Mirrors `linux/web/src/operator/ptz.ts`.

public enum PTZDirection: String, CaseIterable, Hashable, Sendable {
    case up, down, left, right, zoomIn, zoomOut
}

public enum PTZInput {
    /// Pan/tilt speed as a fraction of the camera's maximum.
    public static let speed = 0.5
    public static let zoomSpeed = 0.4

    /// The combined velocity of every held control; opposite controls cancel.
    public static func velocity(of held: [PTZDirection]) -> V2.PTZMove {
        func axis(_ pos: PTZDirection, _ neg: PTZDirection, _ speed: Double) -> Double {
            ((held.contains(pos) ? 1 : 0) - (held.contains(neg) ? 1 : 0)) * speed
        }
        return V2.PTZMove(pan: axis(.right, .left, speed), tilt: axis(.up, .down, speed),
                          zoom: axis(.zoomIn, .zoomOut, zoomSpeed)).clamped
    }

    /// The control a key stands for: arrow keys pan/tilt, + and - zoom.
    /// `keyCode` is the macOS virtual key code (arrows, keypad +/-);
    /// `characters` covers the main-row keys.
    public static func direction(keyCode: UInt16, characters: String?) -> PTZDirection? {
        switch keyCode {
        case 126: return .up
        case 125: return .down
        case 123: return .left
        case 124: return .right
        case 69: return .zoomIn      // keypad +
        case 78: return .zoomOut     // keypad -
        default: break
        }
        switch characters {
        case "+", "=": return .zoomIn
        case "-", "_": return .zoomOut
        default: return nil
        }
    }

    /// A trimmed preset name, or nil when empty or longer than the server's 64.
    public static func cleanPresetName(_ name: String) -> String? {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty || trimmed.count > 64 ? nil : trimmed
    }
}

/// What to send to the camera after a press or release.
public enum PTZCommand: Equatable, Sendable {
    case move(V2.PTZMove)
    case stop
}

/// The set of held controls. A press or release answers with the command it
/// implies, or nil when nothing changed (key repeat, release of an unheld control).
public struct PTZHoldState: Sendable {
    public private(set) var held: [PTZDirection] = []
    public init() {}

    public var last: PTZDirection? { held.last }

    public mutating func press(_ d: PTZDirection) -> PTZCommand? {
        guard held.contains(d) == false else { return nil }
        held.append(d)
        return command
    }

    public mutating func release(_ d: PTZDirection) -> PTZCommand? {
        guard let index = held.firstIndex(of: d) else { return nil }
        held.remove(at: index)
        return command
    }

    public mutating func releaseAll() -> PTZCommand? {
        guard held.isEmpty == false else { return nil }
        held.removeAll()
        return .stop
    }

    private var command: PTZCommand {
        let v = PTZInput.velocity(of: held)
        return v.isStill ? .stop : .move(v)
    }
}

/// Probes `GET …/ptz` once per camera and remembers the answer for the
/// session. Unsupported cameras (403, 404, 502 or `supported: false`) are
/// remembered as "no PTZ"; transport failures are not cached, so the next
/// focus retries.
public actor PTZProbeCache {
    public typealias Fetch = @Sendable (String) async throws -> V2.PTZState

    private let fetch: Fetch
    /// A present key is a final answer; a nil value means "no PTZ".
    private var results: [String: V2.PTZState?] = [:]
    private var inflight: [String: Task<V2.PTZState??, Never>] = [:]

    public init(fetch: @escaping Fetch) {
        self.fetch = fetch
    }

    public init(api: SentinelAPI) {
        self.init(fetch: { try await api.ptzState(cameraID: $0) })
    }

    /// The camera's PTZ state, or nil when it doesn't support PTZ (or the probe couldn't run).
    public func state(for cameraID: String) async -> V2.PTZState? {
        if let known = results[cameraID] { return known }
        if let task = inflight[cameraID] {
            return await task.value ?? nil
        }
        let fetch = self.fetch
        let task = Task<V2.PTZState??, Never> {
            do {
                let state = try await fetch(cameraID)
                return .some(state.supported ? state : nil)
            } catch let error as SentinelAPIError {
                if case .server = error { return .some(nil) }
                return nil
            } catch {
                return nil
            }
        }
        inflight[cameraID] = task
        let outcome = await task.value
        inflight[cameraID] = nil
        if let outcome { results[cameraID] = outcome }
        return outcome ?? nil
    }

    /// Records presets changed by the operator (save / delete).
    public func update(presets: [V2.PTZPreset], for cameraID: String) {
        if let known = results[cameraID], var state = known {
            state.presets = presets
            results[cameraID] = .some(state)
        }
    }

    public func isCached(_ cameraID: String) -> Bool { results[cameraID] != nil }
}
