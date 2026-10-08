import Foundation

public struct CameraFingerprint: Hashable {
    public let manufacturer: CameraManufacturer
    public let confidence: Confidence
    public let serverHeader: String?
    public let realm: String?

    public enum Confidence: Int, Comparable, Hashable {
        case low = 1
        case medium = 2
        case high = 3

        public static func < (lhs: Confidence, rhs: Confidence) -> Bool {
            lhs.rawValue < rhs.rawValue
        }
    }

    public init(
        manufacturer: CameraManufacturer,
        confidence: Confidence,
        serverHeader: String? = nil,
        realm: String? = nil
    ) {
        self.manufacturer = manufacturer
        self.confidence = confidence
        self.serverHeader = serverHeader
        self.realm = realm
    }
}

// Identifies a camera by probing HTTP on its admin port and matching
// signatures on the Server response header / WWW-Authenticate realm / body.
// No authentication required — most cameras leak the manufacturer in their
// 401 response.
public enum CameraFingerprinter {
    public static let defaultProbePorts: [UInt16] = [80, 8080, 8000, 8899, 88]

    public static func fingerprint(host: String, timeout: TimeInterval = 1.5) async -> CameraFingerprint? {
        for port in defaultProbePorts {
            if let result = await probe(host: host, port: port, timeout: timeout) {
                return result
            }
        }
        return nil
    }

    public static func probe(host: String, port: UInt16, timeout: TimeInterval) async -> CameraFingerprint? {
        let scheme = port == 443 ? "https" : "http"
        guard let url = URL(string: "\(scheme)://\(host):\(port)/") else { return nil }

        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: timeout)
        request.httpMethod = "GET"
        request.setValue("Sentinel-VMS-Discovery/1.0", forHTTPHeaderField: "User-Agent")

        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = timeout
        config.timeoutIntervalForResource = timeout
        let session = URLSession(configuration: config)
        defer { session.finishTasksAndInvalidate() }

        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else { return nil }

            let server = (http.value(forHTTPHeaderField: "Server")
                ?? http.value(forHTTPHeaderField: "server")
                ?? "")
            let realm = parseRealm(from: http.value(forHTTPHeaderField: "WWW-Authenticate")
                ?? http.value(forHTTPHeaderField: "www-authenticate"))
            let bodyPreview = String(data: data.prefix(2_048), encoding: .utf8) ?? ""

            if let manufacturer = match(server: server, realm: realm, body: bodyPreview) {
                return CameraFingerprint(
                    manufacturer: manufacturer,
                    confidence: confidenceFor(server: server, realm: realm, manufacturer: manufacturer),
                    serverHeader: server.isEmpty ? nil : server,
                    realm: realm
                )
            }

            // HTTP responded but we couldn't classify — still useful to know
            // it's *something* listening; caller can choose to add as generic.
            if http.statusCode >= 200 && http.statusCode < 500 {
                return CameraFingerprint(
                    manufacturer: .generic,
                    confidence: .low,
                    serverHeader: server.isEmpty ? nil : server,
                    realm: realm
                )
            }
            return nil
        } catch {
            return nil
        }
    }

    private static func match(server: String, realm: String?, body: String) -> CameraManufacturer? {
        let s = server.lowercased()
        let r = (realm ?? "").lowercased()
        let b = body.lowercased()

        if s.contains("hikvision") || r.contains("hikvision") || b.contains("hikvision") { return .hikvision }
        if s.contains("dahua") || r.contains("dahua") || b.contains("dahua") { return .dahua }
        if s.contains("amcrest") || r.contains("amcrest") { return .amcrest }
        if s.contains("axis") || r.contains("axis") || b.contains("axis communications") { return .axis }
        if s.contains("reolink") || r.contains("reolink") || b.contains("reolink") { return .reolink }
        if s.contains("foscam") || r.contains("foscam") { return .foscam }
        if s.contains("ubiquiti") || s.contains("unifi") || r.contains("unifi") { return .ubiquiti }
        if s.contains("bosch") || r.contains("bosch") { return .bosch }
        if s.contains("panasonic") || r.contains("panasonic") { return .panasonic }
        if s.contains("sony") || r.contains("sony") { return .sony }
        if s.contains("vivotek") || r.contains("vivotek") { return .vivotek }
        if s.contains("mobotix") || r.contains("mobotix") { return .mobotix }

        // DNVRS-Webs is Hikvision's older web server identifier.
        if s.contains("dnvrs") || s.contains("dvrdvs") { return .hikvision }
        // App-webs is Dahua's lighttpd fork.
        if s.contains("app-webs") { return .dahua }

        return nil
    }

    private static func confidenceFor(server: String, realm: String?, manufacturer: CameraManufacturer) -> CameraFingerprint.Confidence {
        let combined = (server + (realm ?? "")).lowercased()
        if combined.contains(manufacturer.rawValue) { return .high }
        return .medium
    }

    private static func parseRealm(from header: String?) -> String? {
        guard let header else { return nil }
        // Match realm="..." or realm=...
        let pattern = #"realm\s*=\s*"?([^",]+)"?"#
        guard let regex = try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive]) else { return nil }
        let range = NSRange(header.startIndex..<header.endIndex, in: header)
        guard let match = regex.firstMatch(in: header, range: range),
              let valueRange = Range(match.range(at: 1), in: header) else { return nil }
        return String(header[valueRange]).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
