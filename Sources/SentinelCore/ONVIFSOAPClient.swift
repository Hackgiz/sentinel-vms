import CryptoKit
import Foundation
import Security

public struct ONVIFCameraDetails {
    public let manufacturer: String?
    public let model: String?
    public let firmwareVersion: String?
    public let serialNumber: String?
    public let hardwareID: String?
    public let profiles: [ONVIFStreamProfile]

    public func displayName(fallback: String) -> String {
        if let manufacturer, let model, manufacturer.isEmpty == false, model.isEmpty == false {
            return "\(manufacturer) \(model)"
        }

        return model ?? fallback
    }
}

private struct ONVIFProfileDescriptor {
    public let token: String
    public let name: String
    public let resolution: String
    public let fps: Int
    public let encoding: String?
}

public enum ONVIFSOAPError: LocalizedError {
    case invalidServiceURL(String)
    case invalidResponse
    case httpStatus(Int, String)
    case missingMediaService
    case noProfiles
    case noStreamURIs

    public var errorDescription: String? {
        switch self {
        case .invalidServiceURL(let url):
            return "Invalid ONVIF service URL: \(url)"
        case .invalidResponse:
            return "The camera returned an invalid ONVIF response."
        case .httpStatus(let status, let detail):
            return "ONVIF request failed with HTTP \(status): \(detail)"
        case .missingMediaService:
            return "The camera did not report an ONVIF media service."
        case .noProfiles:
            return "The camera did not return any ONVIF media profiles."
        case .noStreamURIs:
            return "The camera did not return RTSP stream URIs for its profiles."
        }
    }
}

public enum ONVIFSOAPClient {
    public static func fetchCameraDetails(
        serviceURL: String,
        fallbackHost: String,
        credentials: ONVIFCredentials
    ) async throws -> ONVIFCameraDetails {
        guard let deviceURL = URL(string: serviceURL) else {
            throw ONVIFSOAPError.invalidServiceURL(serviceURL)
        }

        // Sync to the camera's clock first (unauthenticated) so the WS-Security
        // token's Created time matches the device's own clock. Best-effort: if
        // it fails we fall back to the Mac's clock (offset 0).
        await syncClock(deviceURL: deviceURL)

        let deviceInfo = try await getDeviceInformation(deviceURL: deviceURL, credentials: credentials)
        let mediaURL = (try? await getMediaServiceURL(deviceURL: deviceURL, credentials: credentials))
            ?? fallbackMediaServiceURL(from: deviceURL)

        let descriptors = try await getProfiles(
            mediaURL: mediaURL,
            deviceURL: deviceURL,
            credentials: credentials
        )

        var profiles: [ONVIFStreamProfile] = []

        for descriptor in descriptors {
            guard let uri = try? await getStreamURI(
                mediaURL: mediaURL,
                deviceURL: deviceURL,
                profileToken: descriptor.token,
                credentials: credentials
            ) else {
                continue
            }

            profiles.append(
                ONVIFStreamProfile(
                    token: descriptor.token,
                    name: descriptor.name,
                    resolution: descriptor.resolution,
                    fps: descriptor.fps,
                    rtspURL: uri,
                    encoding: descriptor.encoding
                )
            )
        }

        guard profiles.isEmpty == false else {
            throw ONVIFSOAPError.noStreamURIs
        }

        return ONVIFCameraDetails(
            manufacturer: deviceInfo.manufacturer,
            model: deviceInfo.model,
            firmwareVersion: deviceInfo.firmwareVersion,
            serialNumber: deviceInfo.serialNumber,
            hardwareID: deviceInfo.hardwareID,
            profiles: profiles
        )
    }

    /// Unauthenticated GetSystemDateAndTime probe → records how far the
    /// camera's UTC clock is from ours, so the WS-Security Created timestamp can
    /// be aligned to the device. Best-effort: silently no-ops on failure.
    private static func syncClock(deviceURL: URL) async {
        let body = "<tds:GetSystemDateAndTime/>"
        guard let xml = try? await postSOAP(
            url: deviceURL,
            action: "http://www.onvif.org/ver10/device/wsdl/GetSystemDateAndTime",
            body: body,
            credentials: ONVIFCredentials(username: "", password: ""),
            includeSecurity: false
        ) else { return }

        guard let utc = ONVIFXML.nodes(in: xml, localName: "UTCDateTime").first,
              let cameraDate = ONVIFXML.parseDateTime(in: utc) else { return }
        ONVIFClockOffsets.shared.set(host: deviceURL.host, offset: cameraDate.timeIntervalSinceNow)
    }

    private static func getDeviceInformation(
        deviceURL: URL,
        credentials: ONVIFCredentials
    ) async throws -> ONVIFCameraDetails {
        let body = """
        <tds:GetDeviceInformation/>
        """

        let xml = try await postSOAP(
            url: deviceURL,
            action: "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation",
            body: body,
            credentials: credentials
        )

        return ONVIFCameraDetails(
            manufacturer: ONVIFXML.firstTagValue(in: xml, localName: "Manufacturer"),
            model: ONVIFXML.firstTagValue(in: xml, localName: "Model"),
            firmwareVersion: ONVIFXML.firstTagValue(in: xml, localName: "FirmwareVersion"),
            serialNumber: ONVIFXML.firstTagValue(in: xml, localName: "SerialNumber"),
            hardwareID: ONVIFXML.firstTagValue(in: xml, localName: "HardwareId"),
            profiles: []
        )
    }

    private static func getMediaServiceURL(
        deviceURL: URL,
        credentials: ONVIFCredentials
    ) async throws -> URL {
        let body = """
        <tds:GetCapabilities>
          <tds:Category>Media</tds:Category>
        </tds:GetCapabilities>
        """

        let xml = try await postSOAP(
            url: deviceURL,
            action: "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
            body: body,
            credentials: credentials
        )

        let mediaNode = ONVIFXML.nodes(in: xml, localName: "Media").first
        guard let value = mediaNode.flatMap({ ONVIFXML.firstTagValue(in: $0, localName: "XAddr") }) ?? ONVIFXML.firstTagValue(in: xml, localName: "XAddr"),
              let url = URL(string: value) else {
            throw ONVIFSOAPError.missingMediaService
        }

        return url
    }

    private static func getProfiles(
        mediaURL: URL,
        deviceURL: URL,
        credentials: ONVIFCredentials
    ) async throws -> [ONVIFProfileDescriptor] {
        do {
            return try await getProfiles(mediaURL: mediaURL, credentials: credentials)
        } catch {
            if mediaURL != deviceURL {
                return try await getProfiles(mediaURL: deviceURL, credentials: credentials)
            }

            throw error
        }
    }

    private static func getProfiles(
        mediaURL: URL,
        credentials: ONVIFCredentials
    ) async throws -> [ONVIFProfileDescriptor] {
        let xml = try await postSOAP(
            url: mediaURL,
            action: "http://www.onvif.org/ver10/media/wsdl/GetProfiles",
            body: "<trt:GetProfiles/>",
            credentials: credentials
        )

        let descriptors = ONVIFXML.nodes(in: xml, localName: "Profiles").compactMap { node -> ONVIFProfileDescriptor? in
            guard let token = ONVIFXML.attributeValue(named: "token", in: node),
                  token.isEmpty == false else {
                return nil
            }

            let name = ONVIFXML.firstTagValue(in: node, localName: "Name") ?? "Profile \(token)"
            let width = Int(ONVIFXML.firstTagValue(in: node, localName: "Width") ?? "")
            let height = Int(ONVIFXML.firstTagValue(in: node, localName: "Height") ?? "")
            let resolution = resolutionLabel(width: width, height: height)
            let fps = fpsValue(from: node)
            // Encoding lives in the VideoEncoderConfiguration (audio configs also
            // carry an <Encoding>, so scope to the video node to avoid picking it).
            let videoConfig = node.descendants(matching: "VideoEncoderConfiguration").first
            let encoding = videoConfig.flatMap { ONVIFXML.firstTagValue(in: $0, localName: "Encoding") }

            return ONVIFProfileDescriptor(
                token: token,
                name: name,
                resolution: resolution,
                fps: fps,
                encoding: encoding
            )
        }

        guard descriptors.isEmpty == false else {
            throw ONVIFSOAPError.noProfiles
        }

        return descriptors
    }

    private static func getStreamURI(
        mediaURL: URL,
        deviceURL: URL,
        profileToken: String,
        credentials: ONVIFCredentials
    ) async throws -> String {
        do {
            return try await getStreamURI(mediaURL: mediaURL, profileToken: profileToken, credentials: credentials)
        } catch {
            if mediaURL != deviceURL {
                return try await getStreamURI(mediaURL: deviceURL, profileToken: profileToken, credentials: credentials)
            }

            throw error
        }
    }

    private static func getStreamURI(
        mediaURL: URL,
        profileToken: String,
        credentials: ONVIFCredentials
    ) async throws -> String {
        let escapedToken = ONVIFXML.escaped(profileToken)
        let body = """
        <trt:GetStreamUri>
          <trt:StreamSetup>
            <tt:Stream>RTP-Unicast</tt:Stream>
            <tt:Transport>
              <tt:Protocol>RTSP</tt:Protocol>
            </tt:Transport>
          </trt:StreamSetup>
          <trt:ProfileToken>\(escapedToken)</trt:ProfileToken>
        </trt:GetStreamUri>
        """

        let xml = try await postSOAP(
            url: mediaURL,
            action: "http://www.onvif.org/ver10/media/wsdl/GetStreamUri",
            body: body,
            credentials: credentials
        )

        guard let uri = ONVIFXML.firstTagValue(in: xml, localName: "Uri"),
              uri.lowercased().hasPrefix("rtsp://") else {
            throw ONVIFSOAPError.noStreamURIs
        }

        return uri
    }

    public static func ptzContinuousMove(
        deviceServiceURL: URL,
        profileToken: String,
        pan: Double,
        tilt: Double,
        zoom: Double,
        credentials: ONVIFCredentials
    ) async throws {
        let ptzURL = (try? await getPTZServiceURL(deviceURL: deviceServiceURL, credentials: credentials))
            ?? fallbackPTZServiceURL(from: deviceServiceURL)
        let token = ONVIFXML.escaped(profileToken)
        let body = """
        <tptz:ContinuousMove>
          <tptz:ProfileToken>\(token)</tptz:ProfileToken>
          <tptz:Velocity>
            <tt:PanTilt x="\(pan)" y="\(tilt)"/>
            <tt:Zoom x="\(zoom)"/>
          </tptz:Velocity>
        </tptz:ContinuousMove>
        """
        _ = try? await postSOAP(url: ptzURL, action: "http://www.onvif.org/ver20/ptz/wsdl/ContinuousMove",
                                body: body, credentials: credentials, ptzNamespace: true)
    }

    public static func ptzGetPresets(
        deviceServiceURL: URL,
        profileToken: String,
        credentials: ONVIFCredentials
    ) async throws -> [(token: String, name: String)] {
        let ptzURL = (try? await getPTZServiceURL(deviceURL: deviceServiceURL, credentials: credentials))
            ?? fallbackPTZServiceURL(from: deviceServiceURL)
        let token = ONVIFXML.escaped(profileToken)
        let body = "<tptz:GetPresets><tptz:ProfileToken>\(token)</tptz:ProfileToken></tptz:GetPresets>"
        let xml = try await postSOAP(url: ptzURL,
                                     action: "http://www.onvif.org/ver20/ptz/wsdl/GetPresets",
                                     body: body, credentials: credentials, ptzNamespace: true)
        return ONVIFXML.nodes(in: xml, localName: "Preset").compactMap { node in
            guard let presetToken = ONVIFXML.attributeValue(named: "token", in: node),
                  presetToken.isEmpty == false else { return nil }
            let name = ONVIFXML.firstTagValue(in: node, localName: "Name") ?? presetToken
            return (token: presetToken, name: name)
        }
    }

    public static func ptzSetPreset(
        deviceServiceURL: URL,
        profileToken: String,
        presetName: String,
        credentials: ONVIFCredentials
    ) async throws -> String {
        let ptzURL = (try? await getPTZServiceURL(deviceURL: deviceServiceURL, credentials: credentials))
            ?? fallbackPTZServiceURL(from: deviceServiceURL)
        let profToken = ONVIFXML.escaped(profileToken)
        let escapedName = ONVIFXML.escaped(presetName)
        let body = """
        <tptz:SetPreset>
          <tptz:ProfileToken>\(profToken)</tptz:ProfileToken>
          <tptz:PresetName>\(escapedName)</tptz:PresetName>
        </tptz:SetPreset>
        """
        let xml = try await postSOAP(url: ptzURL,
                                     action: "http://www.onvif.org/ver20/ptz/wsdl/SetPreset",
                                     body: body, credentials: credentials, ptzNamespace: true)
        return ONVIFXML.firstTagValue(in: xml, localName: "PresetToken") ?? ""
    }

    public static func ptzGotoPreset(
        deviceServiceURL: URL,
        profileToken: String,
        presetToken: String,
        credentials: ONVIFCredentials
    ) async throws {
        let ptzURL = (try? await getPTZServiceURL(deviceURL: deviceServiceURL, credentials: credentials))
            ?? fallbackPTZServiceURL(from: deviceServiceURL)
        let profToken = ONVIFXML.escaped(profileToken)
        let pToken = ONVIFXML.escaped(presetToken)
        let body = """
        <tptz:GotoPreset>
          <tptz:ProfileToken>\(profToken)</tptz:ProfileToken>
          <tptz:PresetToken>\(pToken)</tptz:PresetToken>
        </tptz:GotoPreset>
        """
        _ = try? await postSOAP(url: ptzURL,
                                action: "http://www.onvif.org/ver20/ptz/wsdl/GotoPreset",
                                body: body, credentials: credentials, ptzNamespace: true)
    }

    public static func ptzStop(
        deviceServiceURL: URL,
        profileToken: String,
        credentials: ONVIFCredentials
    ) async {
        let ptzURL = (try? await getPTZServiceURL(deviceURL: deviceServiceURL, credentials: credentials))
            ?? fallbackPTZServiceURL(from: deviceServiceURL)
        let token = ONVIFXML.escaped(profileToken)
        let body = """
        <tptz:Stop>
          <tptz:ProfileToken>\(token)</tptz:ProfileToken>
          <tptz:PanTilt>true</tptz:PanTilt>
          <tptz:Zoom>true</tptz:Zoom>
        </tptz:Stop>
        """
        _ = try? await postSOAP(url: ptzURL, action: "http://www.onvif.org/ver20/ptz/wsdl/Stop",
                                body: body, credentials: credentials, ptzNamespace: true)
    }

    // MARK: - ONVIF PullPoint Event Subscription
    // Cameras can report their own motion, tamper, and alarm events via ONVIF
    // PullPointSubscription — more accurate than local pixel-diff detection.

    /// Subscribe to camera events and return a subscription reference URL.
    public static func createPullPointSubscription(
        deviceServiceURL: URL,
        credentials: ONVIFCredentials,
        terminationTime: String = "PT60S"
    ) async throws -> URL {
        let eventURL = try await getEventServiceURL(deviceURL: deviceServiceURL, credentials: credentials)
        let body = """
        <tev:CreatePullPointSubscription>
          <tev:InitialTerminationTime>\(terminationTime)</tev:InitialTerminationTime>
        </tev:CreatePullPointSubscription>
        """
        let xml = try await postSOAP(
            url: eventURL,
            action: "http://www.onvif.org/ver10/events/wsdl/EventPortType/CreatePullPointSubscriptionRequest",
            body: body,
            credentials: credentials
        )
        guard let addr = ONVIFXML.firstTagValue(in: xml, localName: "Address"),
              let subURL = URL(string: addr) else {
            throw ONVIFSOAPError.invalidResponse
        }
        return subURL
    }

    /// Pull pending motion/alarm messages from an active subscription endpoint.
    /// Returns true if any motion/alarm message was present in the response.
    public static func pullMessages(
        subscriptionURL: URL,
        credentials: ONVIFCredentials,
        messageLimit: Int = 10,
        timeout: String = "PT5S"
    ) async throws -> Bool {
        let body = """
        <tev:PullMessages>
          <tev:Timeout>\(timeout)</tev:Timeout>
          <tev:MessageLimit>\(messageLimit)</tev:MessageLimit>
        </tev:PullMessages>
        """
        let xml = try await postSOAP(
            url: subscriptionURL,
            action: "http://www.onvif.org/ver10/events/wsdl/PullPointSubscription/PullMessagesRequest",
            body: body,
            credentials: credentials
        )
        // Detect motion/alarm topics in the response
        let motionTopics = ["RuleEngine/MotionDetector", "VideoAnalytics/Motion", "Device/Trigger"]
        return motionTopics.contains { xml.range(of: $0, options: .caseInsensitive) != nil }
    }

    /// Renew a PullPoint subscription before it expires.
    public static func renewSubscription(
        subscriptionURL: URL,
        credentials: ONVIFCredentials,
        terminationTime: String = "PT60S"
    ) async {
        let body = """
        <wsnt:Renew>
          <wsnt:TerminationTime>\(terminationTime)</wsnt:TerminationTime>
        </wsnt:Renew>
        """
        _ = try? await postSOAP(
            url: subscriptionURL,
            action: "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/RenewRequest",
            body: body,
            credentials: credentials
        )
    }

    private static func getEventServiceURL(deviceURL: URL, credentials: ONVIFCredentials) async throws -> URL {
        let body = "<tds:GetCapabilities><tds:Category>Events</tds:Category></tds:GetCapabilities>"
        let xml = try await postSOAP(url: deviceURL,
                                     action: "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
                                     body: body, credentials: credentials)
        let eventsNode = ONVIFXML.nodes(in: xml, localName: "Events").first
        if let addr = eventsNode.flatMap({ ONVIFXML.firstTagValue(in: $0, localName: "XAddr") }),
           let url = URL(string: addr) {
            return url
        }
        // Fallback: derive from device URL
        var components = URLComponents(url: deviceURL, resolvingAgainstBaseURL: false)
        components?.path = "/onvif/event_service"
        return components?.url ?? deviceURL
    }

    private static func getPTZServiceURL(deviceURL: URL, credentials: ONVIFCredentials) async throws -> URL {
        let body = "<tds:GetCapabilities><tds:Category>PTZ</tds:Category></tds:GetCapabilities>"
        let xml = try await postSOAP(url: deviceURL,
                                     action: "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
                                     body: body, credentials: credentials)
        let ptzNode = ONVIFXML.nodes(in: xml, localName: "PTZ").first
        if let addr = ptzNode.flatMap({ ONVIFXML.firstTagValue(in: $0, localName: "XAddr") })
            ?? ONVIFXML.firstTagValue(in: xml, localName: "XAddr"),
           let url = URL(string: addr) {
            return url
        }
        return fallbackPTZServiceURL(from: deviceURL)
    }

    private static func fallbackPTZServiceURL(from deviceURL: URL) -> URL {
        var components = URLComponents(url: deviceURL, resolvingAgainstBaseURL: false)
        let path = deviceURL.path
        if path.localizedCaseInsensitiveContains("device_service") {
            components?.path = path.replacingOccurrences(of: "device_service", with: "ptz_service", options: .caseInsensitive)
        } else {
            components?.path = "/onvif/ptz_service"
        }
        return components?.url ?? deviceURL
    }

    private static func postSOAP(
        url: URL,
        action: String,
        body: String,
        credentials: ONVIFCredentials,
        ptzNamespace: Bool = false,
        includeSecurity: Bool = true
    ) async throws -> String {
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.timeoutInterval = 10
        request.setValue("application/soap+xml; charset=utf-8; action=\"\(action)\"", forHTTPHeaderField: "Content-Type")
        request.setValue(action, forHTTPHeaderField: "SOAPAction")
        request.httpBody = Data(soapEnvelope(body: body, credentials: credentials, ptzNamespace: ptzNamespace, host: url.host, includeSecurity: includeSecurity).utf8)

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let httpResponse = response as? HTTPURLResponse else {
            throw ONVIFSOAPError.invalidResponse
        }

        let xml = String(data: data, encoding: .utf8) ?? ""
        guard (200..<300).contains(httpResponse.statusCode) else {
            let detail = ONVIFXML.firstTagValue(in: xml, localName: "Text")
                ?? ONVIFXML.firstTagValue(in: xml, localName: "Reason")
                ?? xml.trimmingCharacters(in: .whitespacesAndNewlines)
            throw ONVIFSOAPError.httpStatus(httpResponse.statusCode, detail.isEmpty ? "No SOAP fault detail." : detail)
        }

        return xml
    }

    private static func soapEnvelope(body: String, credentials: ONVIFCredentials, ptzNamespace: Bool = false, host: String? = nil, includeSecurity: Bool = true) -> String {
        let header = (credentials.isEmpty || includeSecurity == false) ? "<s:Header/>" : """
        <s:Header>
          \(securityHeader(credentials: credentials, host: host))
        </s:Header>
        """
        let ptzNS = ptzNamespace ? "\n                    xmlns:tptz=\"http://www.onvif.org/ver20/ptz/wsdl\"" : ""

        return """
        <?xml version="1.0" encoding="UTF-8"?>
        <s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
                    xmlns:tds="http://www.onvif.org/ver10/device/wsdl"
                    xmlns:trt="http://www.onvif.org/ver10/media/wsdl"
                    xmlns:tt="http://www.onvif.org/ver10/schema"\(ptzNS)>
          \(header)
          <s:Body>
            \(body)
          </s:Body>
        </s:Envelope>
        """
    }

    private static func securityHeader(credentials: ONVIFCredentials, host: String?) -> String {
        // Stamp the token with the CAMERA's clock (our time + the measured
        // offset), not the Mac's. Many cameras — Hanwha/Wisenet especially —
        // reject a UsernameToken whose Created time is outside a tight window of
        // their own (often un-synced) clock with "the security token could not
        // be authenticated". The offset comes from an unauthenticated
        // GetSystemDateAndTime probe done before the first authed call.
        let offset = ONVIFClockOffsets.shared.offset(for: host)
        let created = ONVIFDateFormatter.createdString(from: Date().addingTimeInterval(offset))
        let nonce = ONVIFSecurity.randomNonce()
        let digest = ONVIFSecurity.passwordDigest(nonce: nonce, created: created, password: credentials.password)

        return """
        <wsse:Security s:mustUnderstand="1"
                       xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
                       xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">
          <wsse:UsernameToken>
            <wsse:Username>\(ONVIFXML.escaped(credentials.username))</wsse:Username>
            <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">\(digest)</wsse:Password>
            <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">\(nonce.base64EncodedString())</wsse:Nonce>
            <wsu:Created>\(created)</wsu:Created>
          </wsse:UsernameToken>
        </wsse:Security>
        """
    }

    private static func fallbackMediaServiceURL(from deviceURL: URL) -> URL {
        let path = deviceURL.path
        if path.localizedCaseInsensitiveContains("device_service") {
            var components = URLComponents(url: deviceURL, resolvingAgainstBaseURL: false)
            components?.path = path.replacingOccurrences(of: "device_service", with: "media_service", options: [.caseInsensitive])
            if let url = components?.url {
                return url
            }
        }

        let root = "\(deviceURL.scheme ?? "https")://\(deviceURL.host ?? "")"
        return URL(string: "\(root)/onvif/media_service") ?? deviceURL
    }

    private static func resolutionLabel(width: Int?, height: Int?) -> String {
        guard let width, let height, width > 0, height > 0 else {
            return "Pending"
        }

        return "\(width)x\(height)"
    }

    private static func fpsValue(from node: ONVIFXMLNode) -> Int {
        guard let value = ONVIFXML.firstTagValue(in: node, localName: "FrameRateLimit"),
              let fps = Double(value) else {
            return 0
        }

        return Int(fps.rounded())
    }
}

/// Per-host clock offset (cameraTime − ourTime), measured via the
/// unauthenticated GetSystemDateAndTime probe and applied to the WS-Security
/// Created timestamp so devices with skewed clocks still accept our token.
final class ONVIFClockOffsets: @unchecked Sendable {
    static let shared = ONVIFClockOffsets()
    private let lock = NSLock()
    private var offsets: [String: TimeInterval] = [:]

    func offset(for host: String?) -> TimeInterval {
        guard let host else { return 0 }
        lock.lock(); defer { lock.unlock() }
        return offsets[host] ?? 0
    }

    func set(host: String?, offset: TimeInterval) {
        guard let host else { return }
        lock.lock(); offsets[host] = offset; lock.unlock()
    }
}

public enum ONVIFSecurity {
    public static func randomNonce() -> Data {
        var bytes = [UInt8](repeating: 0, count: 16)
        let status = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)

        guard status == errSecSuccess else {
            fatalError("SecRandomCopyBytes failed — cannot generate secure nonce")
        }

        return Data(bytes)
    }

    public static func passwordDigest(nonce: Data, created: String, password: String) -> String {
        var data = Data()
        data.append(nonce)
        data.append(Data(created.utf8))
        data.append(Data(password.utf8))

        // WS-Security UsernameToken Profile 1.1 mandates SHA1, not SHA256.
        // Reference: OASIS Web Services Security Username Token Profile §3.1.
        let digest = Insecure.SHA1.hash(data: data)
        return Data(digest).base64EncodedString()
    }
}

public enum ONVIFDateFormatter {
    private static let formatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd'T'HH:mm:ss'Z'"
        return formatter
    }()

    public static func createdString(from date: Date) -> String {
        formatter.string(from: date)
    }
}

public final class ONVIFXMLNode {
    public let name: String
    public let attributes: [String: String]
    weak var parent: ONVIFXMLNode?
    public var children: [ONVIFXMLNode] = []
    public var text = ""

    public init(name: String, attributes: [String: String], parent: ONVIFXMLNode?) {
        self.name = name
        self.attributes = attributes
        self.parent = parent
    }

    public var localName: String {
        name.split(separator: ":").last.map(String.init) ?? name
    }

    public var trimmedText: String {
        text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public func descendants(matching localName: String) -> [ONVIFXMLNode] {
        var matches: [ONVIFXMLNode] = []

        for child in children {
            if child.localName.caseInsensitiveCompare(localName) == .orderedSame {
                matches.append(child)
            }

            matches.append(contentsOf: child.descendants(matching: localName))
        }

        return matches
    }
}

private final class ONVIFXMLTreeParser: NSObject, XMLParserDelegate {
    private(set) var root: ONVIFXMLNode?
    private var current: ONVIFXMLNode?

    public func parse(xml: String) -> ONVIFXMLNode? {
        guard let data = xml.data(using: .utf8) else {
            return nil
        }

        let parser = XMLParser(data: data)
        parser.shouldResolveExternalEntities = false
        parser.delegate = self
        parser.shouldProcessNamespaces = false
        parser.shouldReportNamespacePrefixes = true
        return parser.parse() ? root : nil
    }

    public func parser(
        _ parser: XMLParser,
        didStartElement elementName: String,
        namespaceURI: String?,
        qualifiedName qName: String?,
        attributes attributeDict: [String: String] = [:]
    ) {
        let node = ONVIFXMLNode(
            name: qName ?? elementName,
            attributes: attributeDict,
            parent: current
        )

        if let current {
            current.children.append(node)
        } else {
            root = node
        }

        current = node
    }

    public func parser(_ parser: XMLParser, foundCharacters string: String) {
        current?.text += string
    }

    public func parser(_ parser: XMLParser, foundCDATA CDATABlock: Data) {
        if let value = String(data: CDATABlock, encoding: .utf8) {
            current?.text += value
        }
    }

    public func parser(
        _ parser: XMLParser,
        didEndElement elementName: String,
        namespaceURI: String?,
        qualifiedName qName: String?
    ) {
        current = current?.parent
    }
}

public enum ONVIFXML {
    public static func nodes(in xml: String, localName: String) -> [ONVIFXMLNode] {
        guard let root = ONVIFXMLTreeParser().parse(xml: xml) else {
            return []
        }

        var matches = root.descendants(matching: localName)
        if root.localName.caseInsensitiveCompare(localName) == .orderedSame {
            matches.insert(root, at: 0)
        }

        return matches
    }

    public static func firstTagValue(in xml: String, localName: String) -> String? {
        nodes(in: xml, localName: localName).first?.trimmedText
    }

    public static func firstTagValue(in node: ONVIFXMLNode, localName: String) -> String? {
        node.descendants(matching: localName).first?.trimmedText
    }

    /// Parses an ONVIF UTCDateTime node (<tt:Date>/<tt:Time> with Year/Month/Day
    /// /Hour/Minute/Second children) into a UTC Date.
    public static func parseDateTime(in node: ONVIFXMLNode) -> Date? {
        func intVal(_ name: String) -> Int? { firstTagValue(in: node, localName: name).flatMap { Int($0) } }
        guard let year = intVal("Year"), let month = intVal("Month"), let day = intVal("Day"),
              let hour = intVal("Hour"), let minute = intVal("Minute") else { return nil }
        var comps = DateComponents()
        comps.year = year; comps.month = month; comps.day = day
        comps.hour = hour; comps.minute = minute; comps.second = intVal("Second") ?? 0
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(secondsFromGMT: 0) ?? .current
        return cal.date(from: comps)
    }

    public static func blocks(in xml: String, localName: String) -> [(attributes: String, body: String)] {
        nodes(in: xml, localName: localName).map { node in
            let attributes = node.attributes
                .map { "\($0.key)=\"\($0.value)\"" }
                .sorted()
                .joined(separator: " ")
            return (attributes: attributes, body: node.trimmedText)
        }
    }

    public static func attributeValue(named name: String, in node: ONVIFXMLNode) -> String? {
        node.attributes.first { key, _ in
            key.split(separator: ":").last.map(String.init)?.caseInsensitiveCompare(name) == .orderedSame
        }?.value
    }

    public static func attributeValue(named name: String, in attributes: String) -> String? {
        let pattern = #"\#(name)\s*=\s*['"]([^'"]+)['"]"#

        guard let regex = try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive]) else {
            return nil
        }

        let range = NSRange(attributes.startIndex..<attributes.endIndex, in: attributes)
        guard let match = regex.firstMatch(in: attributes, range: range),
              let valueRange = Range(match.range(at: 1), in: attributes) else {
            return nil
        }

        return decoded(String(attributes[valueRange]))
    }

    public static func escaped(_ value: String) -> String {
        value
            .replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
            .replacingOccurrences(of: "\"", with: "&quot;")
            .replacingOccurrences(of: "'", with: "&apos;")
    }

    public static func decoded(_ value: String) -> String {
        value
            .replacingOccurrences(of: "&amp;", with: "&")
            .replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">")
            .replacingOccurrences(of: "&quot;", with: "\"")
            .replacingOccurrences(of: "&apos;", with: "'")
    }
}
