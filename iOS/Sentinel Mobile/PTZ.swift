// PTZ.swift
// Pan/tilt/zoom for the camera detail's Live view (WAVE23 §C PTZ): the API
// shapes and calls, the view-free logic (which D-pad controls are held and
// what velocity that means, when the controls show) and the controller that
// sends the commands. Mirrors the Mac (SentinelCore/Workspace/PTZInput.swift,
// Operator/PTZOverlayView.swift) and web (operator/PTZOverlay.tsx, ptz.ts).

import Foundation
import SwiftUI

// MARK: - API shapes

extension V2.Permission {
    /// Move PTZ cameras and use their presets (`GET …/ptz` needs it too).
    static let controlPtz = V2.Permission(rawValue: "controlPtz")
}

extension V2 {
    struct PTZPreset: Codable, Hashable, Identifiable, Sendable {
        var token: String
        var name: String
        var id: String { token }
    }

    /// `GET /api/v2/cameras/{cameraId}/ptz`.
    struct PTZState: Codable, Hashable, Sendable {
        var supported: Bool
        @NullableArray var presets: [PTZPreset]

        init(supported: Bool, presets: [PTZPreset] = []) {
            self.supported = supported
            self.presets = presets
        }
    }

    /// `POST …/ptz/move`: velocities -1…1.
    struct PTZMove: Codable, Hashable, Sendable {
        var pan: Double
        var tilt: Double
        var zoom: Double

        /// Each axis clamped to -1…1.
        var clamped: PTZMove {
            func c(_ v: Double) -> Double { v.isFinite ? min(max(v, -1), 1) : 0 }
            return PTZMove(pan: c(pan), tilt: c(tilt), zoom: c(zoom))
        }

        var isStill: Bool { pan == 0 && tilt == 0 && zoom == 0 }
    }
}

extension SentinelAPI {
    /// `GET /api/v2/cameras/{id}/ptz`: 403 without controlPtz, 502 when the
    /// camera's recording server can't be reached.
    func ptzState(cameraID: String) async throws -> V2.PTZState {
        try await send("GET", "/api/v2/cameras/\(Self.escape(cameraID))/ptz")
    }

    /// `POST …/ptz/move`: start a continuous move until `ptzStop`.
    func ptzMove(cameraID: String, _ move: V2.PTZMove) async throws {
        try await sendNoContent("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/move", body: move.clamped)
    }

    /// `POST …/ptz/stop`.
    func ptzStop(cameraID: String) async throws {
        try await sendNoContent("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/stop")
    }

    /// `POST …/ptz/presets/{token}/goto`.
    func ptzGoto(cameraID: String, presetToken: String) async throws {
        try await sendNoContent("POST", "/api/v2/cameras/\(Self.escape(cameraID))/ptz/presets/\(Self.escape(presetToken))/goto")
    }
}

// MARK: - Logic

enum PTZDirection: String, CaseIterable, Hashable, Sendable {
    case up, down, left, right, zoomIn, zoomOut

    /// The VoiceOver / UI test name of the control.
    var label: String {
        switch self {
        case .up: return "Tilt up"
        case .down: return "Tilt down"
        case .left: return "Pan left"
        case .right: return "Pan right"
        case .zoomIn: return "Zoom in"
        case .zoomOut: return "Zoom out"
        }
    }

    var symbol: String {
        switch self {
        case .up: return "chevron.up"
        case .down: return "chevron.down"
        case .left: return "chevron.left"
        case .right: return "chevron.right"
        case .zoomIn: return "plus.magnifyingglass"
        case .zoomOut: return "minus.magnifyingglass"
        }
    }
}

enum PTZInput {
    /// Pan/tilt speed as a fraction of the camera's maximum (same as Mac and web).
    static let speed = 0.5
    static let zoomSpeed = 0.4

    /// The combined velocity of every held control; opposite controls cancel.
    static func velocity(of held: [PTZDirection]) -> V2.PTZMove {
        func axis(_ pos: PTZDirection, _ neg: PTZDirection, _ speed: Double) -> Double {
            Double((held.contains(pos) ? 1 : 0) - (held.contains(neg) ? 1 : 0)) * speed
        }
        return V2.PTZMove(pan: axis(.right, .left, speed), tilt: axis(.up, .down, speed),
                          zoom: axis(.zoomIn, .zoomOut, zoomSpeed)).clamped
    }
}

/// What to send to the camera after a press or release.
enum PTZCommand: Equatable, Sendable {
    case move(V2.PTZMove)
    case stop
    case goto(String)

    /// Short text for the demo bar and logs ("move 0.5 0 0", "stop", "goto <token>").
    var summary: String {
        switch self {
        case .move(let m):
            func f(_ v: Double) -> String { v == 0 ? "0" : String(format: "%g", v) }
            return "move \(f(m.pan)) \(f(m.tilt)) \(f(m.zoom))"
        case .stop: return "stop"
        case .goto(let token): return "goto \(token)"
        }
    }
}

/// The set of held controls. A press or release answers with the command it
/// implies, or nil when nothing changed (a second press of a held control,
/// release of an unheld one).
struct PTZHoldState: Sendable {
    private(set) var held: [PTZDirection] = []
    /// A move was sent and no stop since.
    private(set) var moving = false

    var last: PTZDirection? { held.last }

    mutating func press(_ d: PTZDirection) -> PTZCommand? {
        guard held.contains(d) == false else { return nil }
        held.append(d)
        return command()
    }

    mutating func release(_ d: PTZDirection) -> PTZCommand? {
        guard let index = held.firstIndex(of: d) else { return nil }
        held.remove(at: index)
        return command()
    }

    /// Lets go of everything (bar closed, view gone, app in the background):
    /// a stop whenever anything is held or the camera may still be moving.
    mutating func halt() -> PTZCommand? {
        guard held.isEmpty == false || moving else { return nil }
        held.removeAll()
        moving = false
        return .stop
    }

    private mutating func command() -> PTZCommand {
        let v = PTZInput.velocity(of: held)
        moving = v.isStill == false
        return v.isStill ? .stop : .move(v)
    }
}

/// When the PTZ button shows.
enum PTZAccess {
    /// Whether to ask the server at all: in Demo Mode always (no-op client);
    /// signed in, only if the role may control PTZ or isn't known yet (the
    /// probe's 403 then answers).
    static func shouldProbe(isDemo: Bool, permissions: [V2.Permission]?) -> Bool {
        if isDemo { return true }
        guard let permissions else { return true }
        return permissions.contains(.controlPtz)
    }

    /// The PTZ button shows for an online camera whose probe said `supported`.
    static func isVisible(state: V2.PTZState?, cameraOnline: Bool) -> Bool {
        cameraOnline && state?.supported == true
    }
}

// MARK: - Clients

/// Where PTZ commands go: the server, or nowhere in Demo Mode.
protocol PTZClient: Sendable {
    func state(cameraID: String) async throws -> V2.PTZState
    func send(_ command: PTZCommand, cameraID: String) async throws
}

struct ServerPTZClient: PTZClient {
    let api: SentinelAPI

    func state(cameraID: String) async throws -> V2.PTZState {
        try await api.ptzState(cameraID: cameraID)
    }

    func send(_ command: PTZCommand, cameraID: String) async throws {
        switch command {
        case .move(let move): try await api.ptzMove(cameraID: cameraID, move)
        case .stop: try await api.ptzStop(cameraID: cameraID)
        case .goto(let token): try await api.ptzGoto(cameraID: cameraID, presetToken: token)
        }
    }
}

/// Demo Mode: one PTZ camera with two presets; commands go nowhere.
struct DemoPTZClient: PTZClient {
    static let cameraID = "demo-cam-2"
    static let presets = [V2.PTZPreset(token: "demo-preset-1", name: "Gate"),
                          V2.PTZPreset(token: "demo-preset-2", name: "Entrance")]

    func state(cameraID: String) async throws -> V2.PTZState {
        cameraID == Self.cameraID ? V2.PTZState(supported: true, presets: Self.presets) : V2.PTZState(supported: false)
    }

    func send(_ command: PTZCommand, cameraID: String) async throws {}
}

// MARK: - Controller

/// Sends PTZ commands for one camera, one at a time so a stop can never
/// overtake a move, and tracks which controls are held.
@MainActor
final class PTZController: ObservableObject {
    @Published private(set) var state: V2.PTZState?
    @Published private(set) var lit: PTZDirection?
    /// The last command sent (shown in Demo Mode; UI tests read it).
    @Published private(set) var lastCommand = ""
    @Published var note = ""

    let cameraID: String
    /// Resolved from the session on first use unless a test injects one.
    private var client: PTZClient?
    private var hold = PTZHoldState()
    private var queue: Task<Void, Never>?

    nonisolated init(cameraID: String, client: PTZClient? = nil) {
        self.cameraID = cameraID
        self.client = client
    }

    /// The client for the current session: none when the role can't use PTZ.
    static func client(for session: SentinelSession) -> PTZClient? {
        if session.isDemoMode { return DemoPTZClient() }
        guard PTZAccess.shouldProbe(isDemo: false, permissions: session.me?.permissions),
              let api = session.api else { return nil }
        return ServerPTZClient(api: api)
    }

    var presets: [V2.PTZPreset] { state?.presets ?? [] }

    /// Asks once whether the camera has PTZ. Any failure (403, 404, 502,
    /// offline) means no controls.
    func probe() async {
        guard state == nil else { return }
        if client == nil { client = Self.client(for: SentinelSession.shared) }
        guard let client else {
            state = V2.PTZState(supported: false)
            return
        }
        state = (try? await client.state(cameraID: cameraID)) ?? V2.PTZState(supported: false)
    }

    func press(_ d: PTZDirection) {
        guard let command = hold.press(d) else { return }
        Haptics.tap()
        apply(command)
    }

    func release(_ d: PTZDirection) {
        apply(hold.release(d))
    }

    /// Stops the camera if anything is held or moving (bar closed, view gone, background).
    func halt() {
        apply(hold.halt())
    }

    func goto(_ preset: V2.PTZPreset) {
        Haptics.tap()
        apply(.goto(preset.token))
    }

    private func apply(_ command: PTZCommand?) {
        guard let command else { return }
        lit = hold.last
        guard let client else { return }
        lastCommand = command.summary
        let previous = queue
        let cameraID = cameraID
        queue = Task { [weak self] in
            await previous?.value
            do {
                try await client.send(command, cameraID: cameraID)
            } catch {
                self?.note = error.localizedDescription
            }
        }
    }

    /// Waits for every queued command (tests).
    func drain() async {
        await queue?.value
    }
}
