import Foundation
import Network
import SwiftUI

public enum StreamProbeState: Equatable {
    case idle
    case checking
    case passed
    case warning
    case failed

    public var title: String {
        switch self {
        case .idle: return "Not Tested"
        case .checking: return "Checking"
        case .passed: return "Reachable"
        case .warning: return "Needs Decoder"
        case .failed: return "Failed"
        }
    }

    public var tint: Color {
        switch self {
        case .idle: return .secondary
        case .checking: return SentinelTheme.accent
        case .passed: return .green
        case .warning: return .orange
        case .failed: return .red
        }
    }
}

public struct StreamProbeResult: Equatable {
    public let state: StreamProbeState
    public let title: String
    public let detail: String

    public init(state: StreamProbeState, title: String, detail: String) {
        self.state = state
        self.title = title
        self.detail = detail
    }

    public static let idle = StreamProbeResult(
        state: .idle,
        title: "Not Tested",
        detail: "Run a connection test to check RTSP host and port reachability."
    )
}

public enum RTSPStreamProbe {
    public static func check(camera: CameraFeed) async -> StreamProbeResult {
        guard camera.rtspURL.isEmpty == false else {
            return StreamProbeResult(
                state: .warning,
                title: "No RTSP URL",
                detail: "This camera has no RTSP URL configured."
            )
        }

        guard let url = URL(string: camera.rtspURL), url.scheme?.lowercased() == "rtsp" else {
            return StreamProbeResult(
                state: .failed,
                title: "Invalid RTSP URL",
                detail: "The stream URL must start with rtsp:// and include a host."
            )
        }

        guard let host = url.host(), host.isEmpty == false else {
            return StreamProbeResult(
                state: .failed,
                title: "Missing Host",
                detail: "The RTSP URL does not include a camera host or IP address."
            )
        }

        let portValue = url.port ?? 554
        guard (1...65535).contains(portValue),
              let port = NWEndpoint.Port(rawValue: UInt16(portValue)) else {
            return StreamProbeResult(
                state: .failed,
                title: "Invalid Port",
                detail: "The RTSP port must be between 1 and 65535."
            )
        }

        return await checkTCP(host: host, port: port, portValue: portValue)
    }

    private static func checkTCP(host: String, port: NWEndpoint.Port, portValue: Int) async -> StreamProbeResult {
        await withCheckedContinuation { continuation in
            let connection = NWConnection(host: NWEndpoint.Host(host), port: port, using: .tcp)
            let queue = DispatchQueue(label: "HandoffGridSentinel.RTSPProbe")
            let completion = StreamProbeCompletion(connection: connection, continuation: continuation)

            connection.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    completion.finish(
                        StreamProbeResult(
                            state: .passed,
                            title: "RTSP Port Reachable",
                            detail: "\(host):\(portValue) accepted a TCP connection. Decoder integration is the next step."
                        )
                    )
                case .failed(let error):
                    completion.finish(
                        StreamProbeResult(
                            state: .failed,
                            title: "Connection Failed",
                            detail: error.localizedDescription
                        )
                    )
                default:
                    break
                }
            }

            queue.asyncAfter(deadline: .now() + 4) {
                completion.finish(
                    StreamProbeResult(
                        state: .failed,
                        title: "Connection Timed Out",
                        detail: "No response from \(host):\(portValue). Check camera power, network, RTSP port, and firewall."
                    )
                )
            }

            connection.start(queue: queue)
        }
    }
}

private final class StreamProbeCompletion: @unchecked Sendable {
    private let lock = NSLock()
    private var didFinish = false
    private let connection: NWConnection
    private let continuation: CheckedContinuation<StreamProbeResult, Never>

    public init(connection: NWConnection, continuation: CheckedContinuation<StreamProbeResult, Never>) {
        self.connection = connection
        self.continuation = continuation
    }

    public func finish(_ result: StreamProbeResult) {
        lock.lock()
        defer { lock.unlock() }

        guard didFinish == false else {
            return
        }

        didFinish = true
        connection.cancel()
        continuation.resume(returning: result)
    }
}
