import Foundation
@testable import SentinelCore

/// A URLProtocol that answers from a handler, for testing `SentinelAPI`
/// without a server. Each test installs its own handler.
final class MockURLProtocol: URLProtocol {
    struct Reply {
        var status: Int
        var body: Data
        var headers: [String: String] = ["Content-Type": "application/json"]

        static func json(_ status: Int, _ text: String) -> Reply {
            Reply(status: status, body: Data(text.utf8))
        }
    }

    nonisolated(unsafe) static var handler: ((URLRequest, Data?) throws -> Reply)?
    private static let lock = NSLock()
    nonisolated(unsafe) private static var log: [(URLRequest, Data?)] = []

    static var requests: [(request: URLRequest, body: Data?)] {
        lock.lock(); defer { lock.unlock() }
        return log.map { ($0.0, $0.1) }
    }

    static func reset() {
        lock.lock(); defer { lock.unlock() }
        log = []
        handler = nil
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = request.httpBody ?? Self.read(request.httpBodyStream)
        Self.lock.lock()
        Self.log.append((request, body))
        let handler = Self.handler
        Self.lock.unlock()
        do {
            guard let handler else { throw URLError(.cannotConnectToHost) }
            let reply = try handler(request, body)
            let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1", headerFields: reply.headers)!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: reply.body)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}

    private static func read(_ stream: InputStream?) -> Data? {
        guard let stream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MockURLProtocol.self]
        return URLSession(configuration: config)
    }
}

enum Fixtures {
    static let user = """
    {"id":"u1","name":"maria","role":"Operator","createdAt":1760000000000,"source":"local","scope":{"allCameras":true,"groupIds":[]}}
    """

    static func tokenResponse(access: String = "access-2", refresh: String = "refresh-2", expiresIn: TimeInterval = 900) -> String {
        let now = Date().unixMillis
        return """
        {"tokenType":"Bearer","accessToken":"\(access)","expiresAt":\(now + Int64(expiresIn * 1000)),
         "refreshToken":"\(refresh)","refreshExpiresAt":\(now + 30 * 86_400_000),"user":\(user)}
        """
    }

    static func pair(access: String = "access-1", refresh: String = "refresh-1", expiresIn: TimeInterval = 900) -> TokenPair {
        let now = Date()
        return TokenPair(accessToken: access, expiresAt: now.addingTimeInterval(expiresIn).unixMillis,
                         refreshToken: refresh, refreshExpiresAt: now.addingTimeInterval(30 * 86_400).unixMillis,
                         userName: "maria")
    }

    static let camera = """
    {"id":"c1","name":"Front Door","location":"Lobby","rtspUrl":"rtsp://10.0.0.5:554/main","subRtspUrl":"",
     "username":"admin","hasPassword":true,"recording":true,"retentionDays":0,"fps":15,"motionLevel":2,
     "alertOn":"auto","onvifUrl":"","cameraAnalytics":true,"sortOrder":0,"createdAt":1760000000000,
     "recorderId":"local","groupIds":null,
     "status":{"online":true,"viewers":1,"width":1920,"height":1080,"analytics":"","message":"Online"}}
    """

    static let alarm = """
    {"id":"a1","cameraId":"c1","cameraName":"Front Door","kind":"Person","title":"Person at Front Door",
     "detail":"Person detected","severity":"Critical","state":"New","owner":"","createdAt":1760000000000,
     "updatedAt":1760000001000,"lastEventAt":1760000001000,"eventCount":2,"eventId":"e1",
     "instructions":[{"rule":"After hours","text":"Call the guard on duty."}],"responseLog":null}
    """

    static let event = """
    {"id":"e1","cameraId":"c1","kind":"Person","label":"person","score":0.91,"box":[0.1,0.2,0.4,0.9],
     "createdAt":1760000000000,"hasThumbnail":true,"alarmId":"a1","threat":"elevated","tags":["night"]}
    """
}
