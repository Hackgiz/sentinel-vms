import Foundation
import Network

public struct SweepHit: Hashable {
    public let host: String
    public let openPorts: [UInt16]

    public init(host: String, openPorts: [UInt16]) {
        self.host = host
        self.openPorts = openPorts
    }
}

// Active TCP probe of each host on the local /24 (or larger /16 max).
// Designed for the user-triggered "Deep scan" path — generates traffic so
// it should never fire automatically.
public enum ActiveSubnetSweep {
    public static let defaultPorts: [UInt16] = [554, 80, 8000, 8080, 8899, 88, 8554]
    public static let connectTimeout: TimeInterval = 0.6
    public static let maxConcurrent = 96

    public static func sweep(
        subnet: LocalIPv4Subnet,
        ports: [UInt16] = defaultPorts,
        progress: (@Sendable (Int, Int) -> Void)? = nil
    ) async -> [SweepHit] {
        let hosts = subnet.hostList
        guard hosts.isEmpty == false else { return [] }
        let total = hosts.count
        let counter = ProgressCounter(total: total, callback: progress)

        var hits: [SweepHit] = []
        await withTaskGroup(of: SweepHit?.self) { group in
            var index = 0
            // Fill the initial window.
            while index < min(maxConcurrent, hosts.count) {
                let host = hosts[index]
                group.addTask { await probeHost(host, ports: ports) }
                index += 1
            }
            while let result = await group.next() {
                if let result, !result.openPorts.isEmpty {
                    hits.append(result)
                }
                await counter.tick()
                if index < hosts.count {
                    let host = hosts[index]
                    group.addTask { await probeHost(host, ports: ports) }
                    index += 1
                }
            }
        }
        return hits
    }

    private static func probeHost(_ host: String, ports: [UInt16]) async -> SweepHit? {
        var open: [UInt16] = []
        await withTaskGroup(of: UInt16?.self) { group in
            for port in ports {
                group.addTask { await tryConnect(host: host, port: port) ? port : nil }
            }
            for await result in group {
                if let result { open.append(result) }
            }
        }
        return open.isEmpty ? nil : SweepHit(host: host, openPorts: open.sorted())
    }

    private static func tryConnect(host: String, port: UInt16) async -> Bool {
        guard let nwPort = NWEndpoint.Port(rawValue: port) else { return false }
        let endpoint = NWEndpoint.Host(host)
        return await withCheckedContinuation { (continuation: CheckedContinuation<Bool, Never>) in
            let parameters = NWParameters.tcp
            // Disable Bonjour / multipath niceties that slow down a sweep.
            if let tcp = parameters.defaultProtocolStack.transportProtocol as? NWProtocolTCP.Options {
                tcp.connectionTimeout = Int(connectTimeout)
                tcp.noDelay = true
            }
            let connection = NWConnection(host: endpoint, port: nwPort, using: parameters)
            let queue = DispatchQueue(label: "sentinel.sweep.\(host).\(port)")
            var resumed = false
            let finish: (@Sendable (Bool) -> Void) = { value in
                if resumed { return }
                resumed = true
                connection.cancel()
                continuation.resume(returning: value)
            }
            connection.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    finish(true)
                case .failed, .cancelled, .waiting:
                    finish(false)
                default:
                    break
                }
            }
            connection.start(queue: queue)
            queue.asyncAfter(deadline: .now() + connectTimeout) { finish(false) }
        }
    }
}

private actor ProgressCounter {
    private var done = 0
    private let total: Int
    private let callback: (@Sendable (Int, Int) -> Void)?

    init(total: Int, callback: (@Sendable (Int, Int) -> Void)?) {
        self.total = total
        self.callback = callback
    }

    func tick() {
        done += 1
        callback?(done, total)
    }
}
