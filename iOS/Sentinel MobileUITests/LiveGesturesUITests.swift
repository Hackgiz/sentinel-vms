// LiveGesturesUITests.swift
// Drives the camera detail's Live view in Demo Mode the way a finger does:
// pinch to zoom, double-tap to reset, ⤢ for fullscreen (WAVE23 §C Gestures),
// and the PTZ control bar on the demo PTZ camera (§C PTZ).

import XCTest

final class LiveGesturesUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication()
        // Demo Mode straight onto the Live tab (DEBUG launch arguments, RootView).
        app.launchArguments = ["--demo", "--tab", "live"]
        app.launch()
    }

    /// Opens a camera from the Live grid by tapping its tile.
    private func openCamera(_ name: String) {
        let tile = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", name)).firstMatch
        XCTAssertTrue(tile.waitForExistence(timeout: 10), "no tile for \(name)")
        tile.tap()
        XCTAssertTrue(app.navigationBars[name].waitForExistence(timeout: 5), "camera detail did not open")
    }

    private var video: XCUIElement { app.otherElements["liveVideo"].firstMatch }

    private func zoom(of element: XCUIElement) -> String { (element.value as? String) ?? "" }

    func testPinchZoomsAndDoubleTapResets() throws {
        openCamera("Front Entrance")
        XCTAssertTrue(video.waitForExistence(timeout: 5))
        XCTAssertEqual(zoom(of: video), "1.0×")

        video.pinch(withScale: 3, velocity: 2)
        let zoomed = zoom(of: video)
        XCTAssertNotEqual(zoomed, "1.0×", "pinch did not reach the video view")

        video.doubleTap()
        let reset = NSPredicate(format: "value == %@", "1.0×")
        expectation(for: reset, evaluatedWith: video)
        waitForExpectations(timeout: 3)
    }

    func testFullscreenButtonPresentsCover() throws {
        openCamera("Front Entrance")
        let button = app.buttons["Fullscreen"]
        XCTAssertTrue(button.waitForExistence(timeout: 5))
        button.tap()
        let close = app.buttons["Close"]
        XCTAssertTrue(close.waitForExistence(timeout: 5), "fullscreen cover did not appear")
        // The fullscreen player zooms too.
        let fullVideo = app.otherElements["liveVideo"].firstMatch
        XCTAssertTrue(fullVideo.waitForExistence(timeout: 5))
        fullVideo.pinch(withScale: 3, velocity: 2)
        XCTAssertNotEqual(zoom(of: fullVideo), "1.0×", "pinch did not reach the fullscreen video")
        close.tap()
        XCTAssertTrue(close.waitForNonExistence(timeout: 5))
    }

    func testPTZControlsOnDemoPTZCamera() throws {
        openCamera("Parking Lot")
        let ptz = app.buttons["PTZ"]
        XCTAssertTrue(ptz.waitForExistence(timeout: 5), "PTZ button missing on the PTZ camera")
        ptz.tap()
        let up = app.buttons["Tilt up"]
        XCTAssertTrue(up.waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["Pan left"].exists)
        XCTAssertTrue(app.buttons["Zoom in"].exists)
        XCTAssertTrue(app.buttons["Zoom out"].exists)

        // Press and hold moves; letting go stops (the demo controller logs the commands).
        let log = app.staticTexts["ptzLastCommand"]
        up.press(forDuration: 1.0)
        let stopped = NSPredicate(format: "label == %@", "stop")
        expectation(for: stopped, evaluatedWith: log)
        waitForExpectations(timeout: 3)

        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "PTZ bar"
        shot.lifetime = .keepAlways
        add(shot)

        let preset = app.buttons["Gate"]
        XCTAssertTrue(preset.waitForExistence(timeout: 3))
        preset.tap()
        let went = NSPredicate(format: "label == %@", "goto demo-preset-1")
        expectation(for: went, evaluatedWith: log)
        waitForExpectations(timeout: 3)
    }

    func testNoPTZOnOrdinaryCamera() throws {
        openCamera("Front Entrance")
        XCTAssertTrue(video.waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["PTZ"].waitForExistence(timeout: 2))
    }
}

private extension XCUIElement {
    func waitForNonExistence(timeout: TimeInterval) -> Bool {
        let gone = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: self)
        return XCTWaiter().wait(for: [gone], timeout: timeout) == .completed
    }
}
