import XCTest
@testable import HandoffGridSentinel
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

    func testCameraDisplayDraftDefaultsEmptyFields() {
        let camera = CameraFeed(
            name: "Entry",
            location: "",
            status: .offline,
            resolution: "Pending",
            fps: 0,
            bitrate: "0 Mbps",
            ipAddress: "192.168.2.221",
            profile: "",
            isRecording: false,
            rtspURL: "rtsp://192.168.2.221:554/stream1",
            username: "operator"
        )

        var draft = CameraDisplayDraft(camera: camera)
        draft.location = " "
        draft.profile = " "

        XCTAssertEqual(draft.trimmedLocation, "Unassigned")
        XCTAssertEqual(draft.trimmedProfile, "Main Stream")
        XCTAssertTrue(draft.isValid)
    }

    func testCameraDetectsUnattendedCredentials() {
        let camera = CameraFeed(
            name: "Entry",
            location: "Lobby",
            status: .offline,
            resolution: "Pending",
            fps: 0,
            bitrate: "0 Mbps",
            ipAddress: "192.168.2.221",
            profile: "Main Stream",
            isRecording: true,
            rtspURL: "rtsp://operator:secret@192.168.2.221:554/stream1",
            username: "operator"
        )

        XCTAssertTrue(camera.hasEmbeddedRTSPCredentials)
    }
}
