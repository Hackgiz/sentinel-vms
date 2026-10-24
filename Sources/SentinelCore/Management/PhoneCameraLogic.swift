import Foundation

// Wave 23 phone cameras on the Mac (WAVE23-PHONE-CAMERA.md §D): the
// view-free wording and rules behind the camera list, the camera editor, the
// "Use an iPhone" card and the live tiles. An iPhone camera has no RTSP,
// ONVIF or second stream, so dual recording is never offered.

public enum PhoneCameraLogic {
    // MARK: Identity

    public static func isPhone(_ camera: V2.Camera?) -> Bool { camera?.isPhone == true }

    /// The editor hides RTSP/ONVIF/second-stream/Find streams/Stream Settings for phone cameras.
    public static func showsNetworkFields(_ camera: V2.Camera?) -> Bool { isPhone(camera) == false }

    /// SF Symbol for the phone badge (macOS 13).
    public static let badgeSymbol = "iphone"
    public static let badgeHelp = "iPhone camera"

    // MARK: Status

    /// "Phone not streaming": the tile's offline message and the list's offline state.
    public static let notStreaming = "Phone not streaming"

    /// Addendum 2: the server's status for a phone camera whose site has no Sentinel Cloud.
    /// Shown as is everywhere, never relabelled "Phone not streaming".
    public static let needsCloud = "Needs Sentinel Cloud"

    /// True when the server says the phone camera can't stream without Sentinel Cloud.
    public static func needsCloud(_ camera: V2.Camera) -> Bool {
        camera.isPhone && camera.status.message.trimmingCharacters(in: .whitespaces)
            .caseInsensitiveCompare(needsCloud) == .orderedSame
    }

    /// Streaming / Not streaming / Needs Sentinel Cloud.
    public static func streamingLabel(_ camera: V2.Camera) -> String {
        if needsCloud(camera) { return needsCloud }
        return camera.status.online ? "Streaming" : "Not streaming"
    }

    /// "iPhone camera · Streaming · last seen 2 min ago". The last-seen part
    /// appears only when the server reports it and the phone isn't streaming now.
    public static func summary(_ camera: V2.Camera, now: Date = Date()) -> String {
        var parts = ["iPhone camera", streamingLabel(camera)]
        if camera.status.online == false, let seen = camera.lastSeenAt, seen > 0 {
            parts.append("last seen " + lastSeen(seen, now: now))
        }
        return parts.joined(separator: " · ")
    }

    /// "just now", "5 min ago", "3 h ago", "2 days ago".
    public static func lastSeen(_ ms: UnixMillis, now: Date = Date()) -> String {
        let seconds = max(0, Int(now.timeIntervalSince1970) - Int(ms / 1000))
        if seconds < 60 { return "just now" }
        if seconds < 3600 { return "\(seconds / 60) min ago" }
        if seconds < 86_400 { return "\(seconds / 3600) h ago" }
        let days = seconds / 86_400
        return days == 1 ? "1 day ago" : "\(days) days ago"
    }

    /// The tile/list message: the server's ("Streaming from EJ's iPhone"), else
    /// "Phone not streaming" when offline.
    public static func statusMessage(_ camera: V2.Camera) -> String {
        let message = camera.status.message.trimmingCharacters(in: .whitespaces)
        if needsCloud(camera) { return needsCloud } // addendum 2: never relabelled
        if camera.status.online { return message.isEmpty ? "Streaming" : message }
        // An older wording like "No signal" says nothing useful about a phone.
        if message.isEmpty || message == "No signal" || message == "Offline" { return notStreaming }
        return message
    }

    /// The offline overlay text on a live tile, for any camera.
    public static func offlineMessage(_ camera: V2.Camera) -> String {
        if camera.isPhone { return statusMessage(camera) }
        return camera.status.message.isEmpty ? "No signal" : camera.status.message
    }

    // MARK: Editor

    /// Shown in place of the dual option's hint for phone cameras.
    public static let dualUnavailable = "Not available for iPhone cameras"

    /// A phone camera can be saved with just a name; an RTSP camera needs its URL.
    public static func canSave(name: String, rtspUrl: String, isPhone: Bool) -> Bool {
        guard name.trimmingCharacters(in: .whitespaces).isEmpty == false else { return false }
        return isPhone || rtspUrl.trimmingCharacters(in: .whitespaces).isEmpty == false
    }

    /// Phone cameras never send RTSP/ONVIF/login fields in a patch (the server refuses them).
    public static func stripNetworkFields(_ patch: inout V2.CameraPatch) {
        patch.rtspUrl = nil
        patch.subRtspUrl = nil
        patch.username = nil
        patch.password = nil
        patch.onvifUrl = nil
    }

    /// The recording mode a phone camera may be saved with: `dual` falls back to Events only.
    public static func allowedMode(_ mode: V2.RecordingMode) -> V2.RecordingMode {
        mode == .dual ? .events : mode
    }

    // MARK: Push key

    public static let rotateTitle = "Rotate the push key?"
    public static let rotateMessage = "The iPhone stops streaming at once. To stream again, open Sentinel on the iPhone, go to Settings → Use this iPhone as a camera and set it up again."
    public static let revokeTitle = "Revoke the push key?"
    public static let revokeMessage = "The iPhone stops streaming at once and can't stream again until you rotate in a new key. Recordings are kept."
    public static let newKeyNote = "This key is shown only once. The iPhone that streamed before needs to be set up again."

    // MARK: Add camera

    public static let useIPhoneTitle = "Use an iPhone"
    public static let useIPhoneSummary = "Turn an iPhone into a camera. It streams over Wi-Fi or through Sentinel Cloud and records like any other camera."
    /// The numbered steps on the "Use an iPhone" card.
    public static let useIPhoneSteps = [
        "Open Sentinel on the iPhone and sign in to this site with an account that can manage cameras.",
        "Go to Settings → Use this iPhone as a camera.",
        "Pick a name, the lens and the quality, then tap Start.",
    ]
    /// Addendum 2: using an iPhone as a camera is a Sentinel Cloud feature.
    public static let cloudNote = "Needs Sentinel Cloud."
    public static let learnMoreTitle = "Learn more"
    public static let learnMoreURL = URL(string: "https://sentvms.com/cloud")!
    public static let useIPhoneFootnote = "The camera shows up in this list once the iPhone starts streaming. iOS stops the camera while Sentinel is in the background."
}

// MARK: - Evidence uploaded from the iPhone

public extension EvidenceDisplay {
    /// The badge on uploaded items.
    static let uploadedBadge = "Uploaded from iPhone"

    /// "by Alex · EJ's iPhone · recorded 23 Oct 2026 at 14:02:11"; empty for footage cut from recordings.
    static func uploadDetail(_ c: V2.EvidenceCase, formatter: (UnixMillis) -> String = defaultFormat) -> String {
        guard c.isUpload else { return "" }
        var parts: [String] = []
        let by = (c.uploadedBy ?? c.lockedBy).trimmingCharacters(in: .whitespaces)
        if by.isEmpty == false { parts.append("by \(by)") }
        if let device = c.device?.trimmingCharacters(in: .whitespaces), device.isEmpty == false { parts.append(device) }
        if let recorded = c.recordedAt, recorded > 0 { parts.append("recorded \(formatter(recorded))") }
        return parts.joined(separator: " · ")
    }

    /// What the Camera column shows: the camera's name, else "iPhone video" for an upload without one.
    static func sourceName(_ c: V2.EvidenceCase) -> String {
        if c.cameraName.isEmpty == false { return c.cameraName }
        return c.isUpload ? "iPhone video" : c.cameraId
    }

    /// The range column: uploads have no recorded range, so show when it was recorded (or uploaded).
    static func whenText(_ c: V2.EvidenceCase, formatter: (UnixMillis) -> String = defaultFormat) -> String {
        if c.isUpload, c.fromMs == 0 || c.toMs == 0 {
            if let recorded = c.recordedAt, recorded > 0 { return "Recorded \(formatter(recorded))" }
            return c.range.isEmpty ? "Uploaded \(formatter(c.lockedAt))" : c.range
        }
        return rangeText(c, formatter: formatter)
    }

    /// Download name: `<caseId>.mov` for a QuickTime upload, else `.mp4`.
    static func downloadName(_ c: V2.EvidenceCase) -> String {
        "\(c.caseId.isEmpty ? c.id : c.caseId).\(fileExtension(c))"
    }

    /// `mov` or `mp4`, from the content type, else the original file name.
    static func fileExtension(_ c: V2.EvidenceCase) -> String {
        if let type = c.contentType?.lowercased() {
            if type == "video/quicktime" { return "mov" }
            if type == "video/mp4" { return "mp4" }
        }
        if let name = c.fileName?.lowercased(), name.hasSuffix(".mov") { return "mov" }
        return "mp4"
    }
}
