import XCTest
@testable import SentinelCore

final class CameraCredentialFormattingTests: XCTestCase {
    func testRTSPFormatterBuildsUnattendedURL() {
        let url = RTSPCredentialFormatter.url(
            "rtsp://192.168.2.221:554/stream1",
            username: "operator",
            password: "secret"
        )

        XCTAssertEqual(url, "rtsp://operator:secret@192.168.2.221:554/stream1")
    }

    func testRTSPFormatterRedactsPassword() {
        let redacted = RTSPCredentialFormatter.redacted("rtsp://operator:secret@192.168.2.221:554/stream1")

        XCTAssertEqual(redacted, "rtsp://operator:redacted@192.168.2.221:554/stream1")
    }

    func testNormalizedStreamKeyIgnoresCredentials() {
        let first = RTSPCredentialFormatter.normalizedStreamKey("rtsp://operator:secret@CAMERA.local:554/stream1")
        let second = RTSPCredentialFormatter.normalizedStreamKey("rtsp://camera.local:554/stream1")

        XCTAssertEqual(first, second)
    }
}
