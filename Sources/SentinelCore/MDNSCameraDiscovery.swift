import Foundation
import Network

public struct MDNSDiscoveredService: Hashable, Sendable {
    public let serviceType: String
    public let name: String
    public let host: String           // Resolved IPv4 if available, else hostname.
    public let port: UInt16

    public init(serviceType: String, name: String, host: String, port: UInt16) {
        self.serviceType = serviceType
        self.name = name
        self.host = host
        self.port = port
    }

    public var probableManufacturer: CameraManufacturer? {
        let lname = name.lowercased()
        let lservice = serviceType.lowercased()
        if lservice.contains("axis") || lname.contains("axis") { return .axis }
        if lname.contains("hikvision") { return .hikvision }
        if lname.contains("dahua") { return .dahua }
        if lname.contains("amcrest") { return .amcrest }
        if lname.contains("reolink") { return .reolink }
        if lname.contains("unifi") || lname.contains("ubnt") { return .ubiquiti }
        if lname.contains("foscam") { return .foscam }
        return nil
    }
}

// Browses the LAN for camera-related Bonjour service types using NWBrowser.
// Returns a deduped list of services after the deadline.
public enum MDNSCameraDiscovery {
    public static let serviceTypes: [String] = [
        "_rtsp._tcp",
        "_rtsp._udp",
        "_axis-video._tcp",
        "_onvif._tcp",
        "_dahua._tcp",
        "_hikvision._tcp"
    ]

    public static func discover(timeout: TimeInterval = 3.5) async -> [MDNSDiscoveredService] {
        var aggregate: [String: MDNSDiscoveredService] = [:]
        await withTaskGroup(of: [MDNSDiscoveredService].self) { group in
            for type in serviceTypes {
                group.addTask {
                    await browse(serviceType: type, timeout: timeout)
                }
            }
            for await batch in group {
                for service in batch {
                    let key = "\(service.host):\(service.port)"
                    if aggregate[key] == nil || service.serviceType.contains("rtsp") {
                        aggregate[key] = service
                    }
                }
            }
        }
        return Array(aggregate.values)
    }

    private static func browse(serviceType: String, timeout: TimeInterval) async -> [MDNSDiscoveredService] {
        await withCheckedContinuation { (continuation: CheckedContinuation<[MDNSDiscoveredService], Never>) in
            let browser = NWBrowser(
                for: .bonjour(type: serviceType, domain: nil),
                using: .init()
            )
            let queue = DispatchQueue(label: "sentinel.mdns.\(serviceType)")
            let captured = ResultsBox()
            let resume: (@Sendable () -> Void) = {
                guard captured.markResumed() else { return }
                browser.cancel()
                let snapshot = captured.snapshot()
                Task {
                    let resolved = await resolveAll(snapshot, serviceType: serviceType)
                    continuation.resume(returning: resolved)
                }
            }

            browser.browseResultsChangedHandler = { results, _ in
                captured.set(Array(results))
            }
            browser.stateUpdateHandler = { state in
                if case .failed = state { resume() }
            }
            browser.start(queue: queue)
            queue.asyncAfter(deadline: .now() + timeout) { resume() }
        }
    }

    private static func resolveAll(_ results: [NWBrowser.Result], serviceType: String) async -> [MDNSDiscoveredService] {
        await withTaskGroup(of: MDNSDiscoveredService?.self) { group in
            for result in results {
                group.addTask {
                    await resolve(result: result, serviceType: serviceType)
                }
            }
            var out: [MDNSDiscoveredService] = []
            for await value in group {
                if let value { out.append(value) }
            }
            return out
        }
    }

    private static func resolve(result: NWBrowser.Result, serviceType: String) async -> MDNSDiscoveredService? {
        let name: String
        switch result.endpoint {
        case .service(let info):
            name = info.name
        default:
            name = ""
        }

        return await withCheckedContinuation { (continuation: CheckedContinuation<MDNSDiscoveredService?, Never>) in
            let connection = NWConnection(to: result.endpoint, using: .tcp)
            let queue = DispatchQueue(label: "sentinel.mdns.resolve")
            let box = ContinuationBox<MDNSDiscoveredService?>(continuation: continuation)

            connection.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    if let endpoint = connection.currentPath?.remoteEndpoint,
                       case .hostPort(let host, let port) = endpoint {
                        let hostString = MDNSHostFormatter.string(from: host)
                        box.resume(with: MDNSDiscoveredService(
                            serviceType: serviceType,
                            name: name,
                            host: hostString,
                            port: port.rawValue
                        ))
                        connection.cancel()
                    } else {
                        box.resume(with: nil)
                        connection.cancel()
                    }
                case .failed, .cancelled:
                    box.resume(with: nil)
                default:
                    break
                }
            }
            connection.start(queue: queue)
            queue.asyncAfter(deadline: .now() + 1.5) {
                box.resume(with: nil)
                connection.cancel()
            }
        }
    }
}

enum MDNSHostFormatter {
    static func string(from host: NWEndpoint.Host) -> String {
        switch host {
        case .ipv4(let addr):
            let bytes = addr.rawValue
            guard bytes.count == 4 else { return "" }
            return "\(bytes[0]).\(bytes[1]).\(bytes[2]).\(bytes[3])"
        case .ipv6(let addr):
            return "\(addr)"
        case .name(let name, _):
            return name
        @unknown default:
            return ""
        }
    }
}

// Thread-safe wrappers for use inside Network framework callbacks, which run
// on a private queue and need to feed an async continuation exactly once.

final class ResultsBox: @unchecked Sendable {
    private let lock = NSLock()
    private var results: [NWBrowser.Result] = []
    private var resumed = false

    func set(_ value: [NWBrowser.Result]) {
        lock.lock(); defer { lock.unlock() }
        results = value
    }

    func snapshot() -> [NWBrowser.Result] {
        lock.lock(); defer { lock.unlock() }
        return results
    }

    func markResumed() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if resumed { return false }
        resumed = true
        return true
    }
}

final class ContinuationBox<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Never>?

    init(continuation: CheckedContinuation<T, Never>) {
        self.continuation = continuation
    }

    func resume(with value: T) {
        lock.lock()
        let c = continuation
        continuation = nil
        lock.unlock()
        c?.resume(returning: value)
    }
}
