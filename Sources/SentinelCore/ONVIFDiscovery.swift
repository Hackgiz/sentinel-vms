import Foundation
import Darwin
import SwiftUI

public enum ONVIFDiscoveryState: Equatable {
    case idle
    case scanning
    case found(Int)
    case failed(String)

    public var title: String {
        switch self {
        case .idle: return "Ready"
        case .scanning: return "Scanning"
        case .found(let count): return "\(count) Found"
        case .failed: return "Failed"
        }
    }

    public var tint: Color {
        switch self {
        case .idle: return .secondary
        case .scanning: return SentinelTheme.accent
        case .found: return .green
        case .failed: return .red
        }
    }
}

public enum ONVIFProfileLoadState: Equatable {
    case idle
    case loading
    case loaded(Int)
    case failed(String)

    public var title: String {
        switch self {
        case .idle: return "Profiles Pending"
        case .loading: return "Fetching Profiles"
        case .loaded(let count): return "\(count) Profiles"
        case .failed: return "Profile Fetch Failed"
        }
    }

    public var tint: Color {
        switch self {
        case .idle: return .secondary
        case .loading: return SentinelTheme.accent
        case .loaded: return .green
        case .failed: return .red
        }
    }

    public var failureMessage: String? {
        guard case .failed(let message) = self else {
            return nil
        }

        return message
    }

    public var isLoaded: Bool {
        if case .loaded = self {
            return true
        }

        return false
    }
}

public struct ONVIFCredentials: Hashable {
    public let username: String
    public let password: String

    public init(username: String, password: String) {
        self.username = username
        self.password = password
    }

    public var isEmpty: Bool {
        username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && password.isEmpty
    }
}

public struct ONVIFStreamProfile: Identifiable, Hashable {
    public let id = UUID()
    public let token: String
    public let name: String
    public let resolution: String
    public let fps: Int
    public let rtspURL: String
    /// Video codec from the ONVIF encoder config: "H265", "H264", "JPEG", or nil.
    public let encoding: String?

    public init(token: String = "", name: String, resolution: String, fps: Int, rtspURL: String, encoding: String? = nil) {
        self.token = token
        self.name = name
        self.resolution = resolution
        self.fps = fps
        self.rtspURL = rtspURL
        self.encoding = encoding
    }

    /// Pixel count for the resolution string ("3328x1872" → 6_229_... ), 0 if unknown.
    public var pixelCount: Int {
        let parts = resolution.lowercased().split(separator: "x")
        guard parts.count == 2, let w = Int(parts[0]), let h = Int(parts[1]) else { return 0 }
        return w * h
    }

    /// Rank for auto-selection: real video codecs beat MJPEG/unknown.
    public var codecRank: Int {
        switch encoding?.uppercased() {
        case "H265", "HEVC": return 3
        case "H264": return 2
        case .some(let e) where e.contains("JPEG"): return 0
        default: return 1
        }
    }
}

public extension Array where Element == ONVIFStreamProfile {
    /// Best profile for live viewing: a real video codec (H.265 > H.264 >
    /// unknown > MJPEG) first, then the highest resolution. Centralizing this
    /// keeps every add/discovery path from defaulting to a camera's MJPEG or
    /// metadata profile (e.g. Hanwha `profile1`) — MJPEG can't be muxed to HLS
    /// or decoded as a live H.26x feed, so the tile would hang on
    /// "PREVIEW START FAILED".
    var preferredForLiveView: ONVIFStreamProfile? {
        self.max { a, b in
            a.codecRank != b.codecRank ? a.codecRank < b.codecRank : a.pixelCount < b.pixelCount
        } ?? self.first
    }
}

public struct ONVIFDiscoveredCamera: Identifiable, Hashable {
    public let id: UUID
    public let name: String
    public let manufacturer: String
    public let model: String
    public let host: String
    public let serviceURL: String
    public let profiles: [ONVIFStreamProfile]

    public init(
        id: UUID = UUID(),
        name: String,
        manufacturer: String,
        model: String,
        host: String,
        serviceURL: String,
        profiles: [ONVIFStreamProfile]
    ) {
        self.id = id
        self.name = name
        self.manufacturer = manufacturer
        self.model = model
        self.host = host
        self.serviceURL = serviceURL
        self.profiles = profiles
    }

    public var primaryProfile: ONVIFStreamProfile? {
        profiles.preferredForLiveView
    }
}

public enum DiscoveryPhase: String, Hashable {
    case onvif
    case mdns
    case deepScan

    public var title: String {
        switch self {
        case .onvif: return "ONVIF Multicast"
        case .mdns: return "Bonjour / mDNS"
        case .deepScan: return "Network Sweep"
        }
    }

    public var symbol: String {
        switch self {
        case .onvif: return "dot.radiowaves.left.and.right"
        case .mdns: return "antenna.radiowaves.left.and.right"
        case .deepScan: return "magnifyingglass.circle"
        }
    }
}

public enum DiscoveryPhaseState: Equatable {
    case idle
    case running(progress: String?)
    case found(Int)
    case failed(String)

    public var label: String {
        switch self {
        case .idle: return "Idle"
        case .running(let p): return p ?? "Running"
        case .found(let n): return "\(n) Found"
        case .failed(let m): return "Failed: \(m)"
        }
    }

    public var isRunning: Bool {
        if case .running = self { return true }
        return false
    }
}

@MainActor
public final class ONVIFDiscoveryStore: ObservableObject {
    @Published public private(set) var state: ONVIFDiscoveryState = .idle
    @Published public private(set) var discoveredCameras: [ONVIFDiscoveredCamera] = []
    @Published public private(set) var profileLoadStates: [UUID: ONVIFProfileLoadState] = [:]
    @Published public private(set) var phaseStates: [DiscoveryPhase: DiscoveryPhaseState] = [:]
    private var scanGeneration = UUID()

    public init() {}

    public func scan() {
        scanAndAutoFetch(username: "", password: "")
    }

    public func loadDemoResults() {
        discoveredCameras = ONVIFDiscoverySimulator.sampleCameras
        profileLoadStates = Dictionary(
            uniqueKeysWithValues: discoveredCameras.map { ($0.id, .loaded($0.profiles.count)) }
        )
        state = .found(discoveredCameras.count)
    }

    public func reset() {
        discoveredCameras = []
        profileLoadStates = [:]
        phaseStates = [:]
        state = .idle
    }

    public func profileState(for cameraID: UUID) -> ONVIFProfileLoadState {
        profileLoadStates[cameraID] ?? .idle
    }

    /// Primary entry point. Runs ONVIF WS-Discovery and Bonjour/mDNS in
    /// parallel, merges results by host, then asynchronously fetches ONVIF
    /// profiles for cameras that responded to ONVIF. Cameras found only via
    /// mDNS get a vendor-preset RTSP URL applied (no creds required).
    public func scanAndAutoFetch(username: String, password: String) {
        guard state != .scanning else { return }

        let generation = UUID()
        scanGeneration = generation
        state = .scanning
        discoveredCameras = []
        profileLoadStates = [:]
        phaseStates = [
            .onvif: .running(progress: "Probing multicast…"),
            .mdns:  .running(progress: "Browsing Bonjour…")
        ]

        Task {
            let credentials = ONVIFCredentials(
                username: username.trimmingCharacters(in: .whitespacesAndNewlines),
                password: password
            )

            async let onvifTask: [ONVIFDiscoveryResponse] = {
                do { return try await ONVIFWSDiscoveryClient.discover(timeout: 4) }
                catch { return [] }
            }()
            async let mdnsTask: [MDNSDiscoveredService] = MDNSCameraDiscovery.discover(timeout: 3.5)

            let onvifResponses = await onvifTask
            guard scanGeneration == generation else { return }
            phaseStates[.onvif] = .found(onvifResponses.count)

            let mdnsServices = await mdnsTask
            guard scanGeneration == generation else { return }
            phaseStates[.mdns] = .found(mdnsServices.count)

            let onvifCameras = onvifResponses.map(ONVIFDiscoveredCamera.init(response:))
            var merged = onvifCameras
            let onvifHosts = Set(onvifCameras.map { $0.host })
            for service in mdnsServices where service.host.isEmpty == false && !onvifHosts.contains(service.host) {
                let manufacturer = service.probableManufacturer ?? .generic
                let preset = RTSPURLPresets.preset(for: manufacturer)
                merged.append(ONVIFDiscoveredCamera(
                    name: service.name.isEmpty ? "Bonjour Camera (\(service.host))" : service.name,
                    manufacturer: manufacturer.displayName,
                    model: service.serviceType,
                    host: service.host,
                    serviceURL: "http://\(service.host)/onvif/device_service",
                    profiles: [
                        ONVIFStreamProfile(
                            name: "Bonjour Main",
                            resolution: "Pending",
                            fps: 0,
                            rtspURL: preset.mainStream.replacingOccurrences(of: "{HOST}", with: service.host)
                        )
                    ]
                ))
            }

            discoveredCameras = merged
            state = .found(merged.count)

            // Mark all entries as profile-loading; ONVIF cameras will be
            // refined via SOAP, mDNS-only cameras already have a preset URL
            // so flip them straight to loaded.
            for camera in merged {
                if onvifHosts.contains(camera.host) {
                    profileLoadStates[camera.id] = .loading
                } else {
                    profileLoadStates[camera.id] = .loaded(camera.profiles.count)
                }
            }

            await fetchONVIFProfilesInBackground(
                cameras: onvifCameras,
                credentials: credentials,
                generation: generation
            )
        }
    }

    /// Active TCP sweep of the local /24. Only runs when explicitly invoked
    /// from the UI — generates network traffic so it should not be automatic.
    public func deepScan(username: String, password: String) {
        let generation = UUID()
        scanGeneration = generation
        state = .scanning
        phaseStates[.deepScan] = .running(progress: "Enumerating local subnet…")

        Task {
            let subnets = NetworkInterfaceUtil.activeIPv4Subnets()
            guard let subnet = subnets.first else {
                phaseStates[.deepScan] = .failed("No active IPv4 interface.")
                if discoveredCameras.isEmpty { state = .failed("No active IPv4 interface.") }
                return
            }
            let hostCount = subnet.hostList.count
            phaseStates[.deepScan] = .running(progress: "Probing \(hostCount) hosts on \(subnet.interface)…")

            let hits = await ActiveSubnetSweep.sweep(subnet: subnet) { [weak self] done, total in
                Task { @MainActor [weak self] in
                    guard let self else { return }
                    if self.scanGeneration == generation {
                        self.phaseStates[.deepScan] = .running(progress: "Probed \(done) of \(total) hosts…")
                    }
                }
            }
            guard scanGeneration == generation else { return }

            let existingHosts = Set(discoveredCameras.map { $0.host })
            var added = 0
            await withTaskGroup(of: (SweepHit, CameraFingerprint?).self) { group in
                for hit in hits where existingHosts.contains(hit.host) == false {
                    group.addTask {
                        let fingerprint = await CameraFingerprinter.fingerprint(host: hit.host)
                        return (hit, fingerprint)
                    }
                }
                for await (hit, fingerprint) in group {
                    guard scanGeneration == generation else { continue }
                    let manufacturer = fingerprint?.manufacturer ?? .generic
                    let preset = RTSPURLPresets.preset(for: manufacturer)
                    let camera = ONVIFDiscoveredCamera(
                        name: "\(manufacturer.displayName) (\(hit.host))",
                        manufacturer: manufacturer.displayName,
                        model: fingerprint?.serverHeader ?? "Discovered via sweep",
                        host: hit.host,
                        serviceURL: "http://\(hit.host)/onvif/device_service",
                        profiles: [
                            ONVIFStreamProfile(
                                name: "Vendor Preset",
                                resolution: "Pending",
                                fps: 0,
                                rtspURL: preset.mainStream.replacingOccurrences(of: "{HOST}", with: hit.host)
                            )
                        ]
                    )
                    discoveredCameras.append(camera)
                    profileLoadStates[camera.id] = .loaded(camera.profiles.count)
                    added += 1
                }
            }

            phaseStates[.deepScan] = .found(added)
            state = .found(discoveredCameras.count)
        }
    }

    /// Probe a single IP (entered manually), fingerprint it, return a
    /// discovered-camera entry ready to add. Useful when WS-Discovery
    /// can't reach a remote VLAN.
    @discardableResult
    public func probeIP(_ host: String, username: String, password: String) async -> ONVIFDiscoveredCamera? {
        let credentials = ONVIFCredentials(
            username: username.trimmingCharacters(in: .whitespacesAndNewlines),
            password: password
        )
        let fingerprint = await CameraFingerprinter.fingerprint(host: host)
        let manufacturer = fingerprint?.manufacturer ?? .generic
        let preset = RTSPURLPresets.preset(for: manufacturer)
        let serviceURL = "http://\(host)/onvif/device_service"

        // Try ONVIF for canonical profiles, fall back to preset on failure.
        let profiles: [ONVIFStreamProfile]
        do {
            let details = try await ONVIFSOAPClient.fetchCameraDetails(
                serviceURL: serviceURL,
                fallbackHost: host,
                credentials: credentials
            )
            profiles = details.profiles.isEmpty
                ? [ONVIFStreamProfile(name: "Vendor Preset", resolution: "Pending", fps: 0, rtspURL: preset.mainStream.replacingOccurrences(of: "{HOST}", with: host))]
                : details.profiles
        } catch {
            profiles = [
                ONVIFStreamProfile(
                    name: "Vendor Preset",
                    resolution: "Pending",
                    fps: 0,
                    rtspURL: preset.mainStream.replacingOccurrences(of: "{HOST}", with: host)
                )
            ]
        }

        let camera = ONVIFDiscoveredCamera(
            name: "\(manufacturer.displayName) (\(host))",
            manufacturer: manufacturer.displayName,
            model: fingerprint?.serverHeader ?? "Manual entry",
            host: host,
            serviceURL: serviceURL,
            profiles: profiles
        )
        if discoveredCameras.contains(where: { $0.host == host }) == false {
            discoveredCameras.append(camera)
            profileLoadStates[camera.id] = .loaded(camera.profiles.count)
            state = .found(discoveredCameras.count)
        }
        return camera
    }

    private func fetchONVIFProfilesInBackground(
        cameras: [ONVIFDiscoveredCamera],
        credentials: ONVIFCredentials,
        generation: UUID
    ) async {
        await withTaskGroup(of: (UUID, Result<ONVIFCameraDetails, Error>).self) { group in
            for camera in cameras {
                let id = camera.id
                let url = camera.serviceURL
                let host = camera.host
                group.addTask {
                    do {
                        let details = try await ONVIFSOAPClient.fetchCameraDetails(
                            serviceURL: url, fallbackHost: host, credentials: credentials)
                        return (id, .success(details))
                    } catch {
                        return (id, .failure(error))
                    }
                }
            }

            for await (id, result) in group {
                guard scanGeneration == generation else { continue }
                guard let index = discoveredCameras.firstIndex(where: { $0.id == id }) else { continue }
                let original = discoveredCameras[index]
                switch result {
                case .success(let details):
                    discoveredCameras[index] = ONVIFDiscoveredCamera(
                        id: original.id,
                        name: details.displayName(fallback: original.name),
                        manufacturer: details.manufacturer ?? original.manufacturer,
                        model: details.model ?? original.model,
                        host: original.host,
                        serviceURL: original.serviceURL,
                        profiles: details.profiles
                    )
                    profileLoadStates[id] = .loaded(details.profiles.count)
                case .failure(let error):
                    profileLoadStates[id] = .failed(error.localizedDescription)
                }
            }
        }
    }

    public func fetchProfiles(for camera: ONVIFDiscoveredCamera, username: String, password: String) {
        profileLoadStates[camera.id] = .loading

        Task {
            do {
                let credentials = ONVIFCredentials(
                    username: username.trimmingCharacters(in: .whitespacesAndNewlines),
                    password: password
                )
                let details = try await ONVIFSOAPClient.fetchCameraDetails(
                    serviceURL: camera.serviceURL,
                    fallbackHost: camera.host,
                    credentials: credentials
                )

                guard let index = discoveredCameras.firstIndex(where: { $0.id == camera.id }) else {
                    return
                }

                discoveredCameras[index] = ONVIFDiscoveredCamera(
                    id: camera.id,
                    name: details.displayName(fallback: camera.name),
                    manufacturer: details.manufacturer ?? camera.manufacturer,
                    model: details.model ?? camera.model,
                    host: camera.host,
                    serviceURL: camera.serviceURL,
                    profiles: details.profiles
                )
                profileLoadStates[camera.id] = .loaded(details.profiles.count)
            } catch {
                profileLoadStates[camera.id] = .failed(error.localizedDescription)
            }
        }
    }
}

public struct ONVIFDiscoveryResponse: Hashable {
    public let endpointReference: String
    public let serviceURL: String
    public let scopes: [String]
    public let rawXML: String

    public var host: String {
        URL(string: serviceURL)?.host() ?? "Unknown"
    }

    public var displayName: String {
        scopeValue(prefix: "name") ?? scopeValue(prefix: "hardware") ?? "ONVIF Camera"
    }

    public var manufacturer: String {
        scopeValue(prefix: "manufacturer") ?? "ONVIF"
    }

    public var model: String {
        scopeValue(prefix: "hardware") ?? "Unknown Model"
    }

    private func scopeValue(prefix: String) -> String? {
        scopes
            .compactMap { scope in
                guard let markerRange = scope.range(of: "/\(prefix)/", options: [.caseInsensitive]) else {
                    return nil
                }

                let value = String(scope[markerRange.upperBound...])
                return value.removingPercentEncoding?.replacingOccurrences(of: "_", with: " ")
            }
            .first
    }
}

public extension ONVIFDiscoveredCamera {
    public init(response: ONVIFDiscoveryResponse) {
        self.init(
            name: response.displayName,
            manufacturer: response.manufacturer,
            model: response.model,
            host: response.host,
            serviceURL: response.serviceURL,
            profiles: [
                ONVIFStreamProfile(
                    name: "ONVIF Profile",
                    resolution: "Pending",
                    fps: 0,
                    rtspURL: "rtsp://\(response.host):554/"
                )
            ]
        )
    }
}

public enum ONVIFDiscoveryError: LocalizedError {
    case socketCreationFailed
    case socketConfigurationFailed
    case sendFailed
    case noResponse

    public var errorDescription: String? {
        switch self {
        case .socketCreationFailed:
            return "Could not create UDP socket for ONVIF WS-Discovery."
        case .socketConfigurationFailed:
            return "Could not configure multicast socket for ONVIF discovery."
        case .sendFailed:
            return "Could not send ONVIF WS-Discovery probe."
        case .noResponse:
            return "No ONVIF cameras responded on the local network."
        }
    }
}

public enum ONVIFWSDiscoveryClient {
    public static func discover(timeout: TimeInterval) async throws -> [ONVIFDiscoveryResponse] {
        try await Task.detached(priority: .userInitiated) {
            try discoverSynchronously(timeout: timeout)
        }.value
    }

    private static func discoverSynchronously(timeout: TimeInterval) throws -> [ONVIFDiscoveryResponse] {
        let socketDescriptor = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP)
        guard socketDescriptor >= 0 else {
            throw ONVIFDiscoveryError.socketCreationFailed
        }

        defer {
            close(socketDescriptor)
        }

        var reuse: Int32 = 1
        guard setsockopt(socketDescriptor, SOL_SOCKET, SO_REUSEADDR, &reuse, socklen_t(MemoryLayout<Int32>.size)) == 0 else {
            throw ONVIFDiscoveryError.socketConfigurationFailed
        }

        var timeoutValue = timeval(
            tv_sec: Int(timeout),
            tv_usec: Int32((timeout - floor(timeout)) * 1_000_000)
        )

        guard setsockopt(socketDescriptor, SOL_SOCKET, SO_RCVTIMEO, &timeoutValue, socklen_t(MemoryLayout<timeval>.size)) == 0 else {
            throw ONVIFDiscoveryError.socketConfigurationFailed
        }

        var destination = sockaddr_in()
        destination.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        destination.sin_family = sa_family_t(AF_INET)
        destination.sin_port = UInt16(3702).bigEndian
        inet_pton(AF_INET, "239.255.255.250", &destination.sin_addr)

        let payload = probeEnvelope
        let sentBytes = payload.withCString { pointer in
            withUnsafePointer(to: &destination) { destinationPointer in
                destinationPointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { socketAddress in
                    sendto(
                        socketDescriptor,
                        pointer,
                        strlen(pointer),
                        0,
                        socketAddress,
                        socklen_t(MemoryLayout<sockaddr_in>.size)
                    )
                }
            }
        }

        guard sentBytes > 0 else {
            throw ONVIFDiscoveryError.sendFailed
        }

        var responses: [ONVIFDiscoveryResponse] = []
        var seenServiceURLs = Set<String>()
        let deadline = Date().addingTimeInterval(timeout)

        while Date() < deadline {
            var buffer = [UInt8](repeating: 0, count: 65_535)
            var sender = sockaddr_storage()
            var senderLength = socklen_t(MemoryLayout<sockaddr_storage>.size)

            let byteCount = withUnsafeMutablePointer(to: &sender) { senderPointer in
                senderPointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { socketAddress in
                    recvfrom(socketDescriptor, &buffer, buffer.count, 0, socketAddress, &senderLength)
                }
            }

            if byteCount <= 0 {
                break
            }

            let data = Data(buffer.prefix(byteCount))
            guard let xml = String(data: data, encoding: .utf8),
                  let response = ONVIFDiscoveryParser.parse(xml: xml),
                  seenServiceURLs.insert(response.serviceURL).inserted else {
                continue
            }

            responses.append(response)
        }

        guard responses.isEmpty == false else {
            throw ONVIFDiscoveryError.noResponse
        }

        return responses
    }

    private static var probeEnvelope: String {
        let messageID = UUID().uuidString
        return """
        <?xml version="1.0" encoding="UTF-8"?>
        <e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope"
                    xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing"
                    xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"
                    xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
          <e:Header>
            <w:MessageID>uuid:\(messageID)</w:MessageID>
            <w:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To>
            <w:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action>
          </e:Header>
          <e:Body>
            <d:Probe>
              <d:Types>dn:NetworkVideoTransmitter</d:Types>
            </d:Probe>
          </e:Body>
        </e:Envelope>
        """
    }
}

public enum ONVIFDiscoveryParser {
    public static func parse(xml: String) -> ONVIFDiscoveryResponse? {
        guard let serviceURL = firstTagValue(in: xml, localName: "XAddrs")?.components(separatedBy: .whitespacesAndNewlines).first(where: { $0.isEmpty == false }) else {
            return nil
        }

        let scopes = firstTagValue(in: xml, localName: "Scopes")?
            .components(separatedBy: .whitespacesAndNewlines)
            .filter { $0.isEmpty == false } ?? []

        return ONVIFDiscoveryResponse(
            endpointReference: firstTagValue(in: xml, localName: "Address") ?? serviceURL,
            serviceURL: serviceURL,
            scopes: scopes,
            rawXML: xml
        )
    }

    private static func firstTagValue(in xml: String, localName: String) -> String? {
        let pattern = #"<(?:[A-Za-z0-9_]+:)?\#(localName)\b[^>]*>(.*?)</(?:[A-Za-z0-9_]+:)?\#(localName)>"#

        guard let regex = try? NSRegularExpression(pattern: pattern, options: [.dotMatchesLineSeparators, .caseInsensitive]) else {
            return nil
        }

        let range = NSRange(xml.startIndex..<xml.endIndex, in: xml)
        guard let match = regex.firstMatch(in: xml, range: range),
              let valueRange = Range(match.range(at: 1), in: xml) else {
            return nil
        }

        return String(xml[valueRange])
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

public enum ONVIFDiscoverySimulator {
    public static let sampleCameras: [ONVIFDiscoveredCamera] = [
        ONVIFDiscoveredCamera(
            name: "ONVIF Camera 01",
            manufacturer: "Hikvision-compatible",
            model: "DS-2CD Series",
            host: "192.168.1.50",
            serviceURL: "http://192.168.1.50/onvif/device_service",
            profiles: [
                ONVIFStreamProfile(name: "Main Stream", resolution: "2688x1520", fps: 20, rtspURL: "rtsp://192.168.1.50:554/Streaming/Channels/101"),
                ONVIFStreamProfile(name: "Sub Stream", resolution: "704x480", fps: 15, rtspURL: "rtsp://192.168.1.50:554/Streaming/Channels/102")
            ]
        ),
        ONVIFDiscoveredCamera(
            name: "ONVIF Camera 02",
            manufacturer: "Dahua-compatible",
            model: "IPC-HDW Series",
            host: "192.168.1.64",
            serviceURL: "http://192.168.1.64/onvif/device_service",
            profiles: [
                ONVIFStreamProfile(name: "Main Stream", resolution: "1920x1080", fps: 30, rtspURL: "rtsp://192.168.1.64:554/cam/realmonitor?channel=1&subtype=0"),
                ONVIFStreamProfile(name: "Sub Stream", resolution: "640x360", fps: 15, rtspURL: "rtsp://192.168.1.64:554/cam/realmonitor?channel=1&subtype=1")
            ]
        ),
        ONVIFDiscoveredCamera(
            name: "ONVIF Camera 03",
            manufacturer: "Axis-compatible",
            model: "P-Series",
            host: "192.168.1.80",
            serviceURL: "http://192.168.1.80/onvif/device_service",
            profiles: [
                ONVIFStreamProfile(name: "H.264", resolution: "1920x1080", fps: 25, rtspURL: "rtsp://192.168.1.80:554/axis-media/media.amp"),
                ONVIFStreamProfile(name: "Low Bandwidth", resolution: "800x450", fps: 12, rtspURL: "rtsp://192.168.1.80:554/axis-media/media.amp?resolution=800x450")
            ]
        )
    ]
}
