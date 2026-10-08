import Foundation

// Known RTSP URL templates by manufacturer. The defaults `rtsp://host:554/`
// fall back to vendor-specific paths so a fingerprinted camera is usable
// even before ONVIF GetStreamUri completes. Templates use the tokens
// {HOST}, {PORT}, {USER}, {PASS}, {CHANNEL} and assume sensible defaults.
public enum CameraManufacturer: String, Codable, Hashable, CaseIterable {
    case hikvision
    case dahua
    case axis
    case reolink
    case amcrest
    case foscam
    case ubiquiti
    case bosch
    case panasonic
    case sony
    case vivotek
    case mobotix
    case generic

    public var displayName: String {
        switch self {
        case .hikvision: return "Hikvision"
        case .dahua: return "Dahua"
        case .axis: return "Axis"
        case .reolink: return "Reolink"
        case .amcrest: return "Amcrest"
        case .foscam: return "Foscam"
        case .ubiquiti: return "Ubiquiti"
        case .bosch: return "Bosch"
        case .panasonic: return "Panasonic"
        case .sony: return "Sony"
        case .vivotek: return "Vivotek"
        case .mobotix: return "Mobotix"
        case .generic: return "Generic ONVIF"
        }
    }
}

public struct RTSPURLPreset {
    public let manufacturer: CameraManufacturer
    public let mainStream: String
    public let subStream: String?

    public init(manufacturer: CameraManufacturer, mainStream: String, subStream: String? = nil) {
        self.manufacturer = manufacturer
        self.mainStream = mainStream
        self.subStream = subStream
    }
}

public enum RTSPURLPresets {
    /// Best-effort low-res sub-stream URL derived from a main RTSP path, for
    /// well-known camera URL schemes. Returns nil when no confident mapping
    /// exists (caller then leaves live on the main stream — no regression).
    /// Operates purely on the path string, so embedded credentials are kept.
    public static func deriveSubStream(fromMain main: String) -> String? {
        guard main.isEmpty == false else { return nil }
        // Ordered, high-confidence suffix/param swaps. First match wins.
        let rules: [(String, String)] = [
            ("stream1", "stream2"),                 // Tapo / Wyze / many generic ONVIF
            ("/Streaming/Channels/101", "/Streaming/Channels/102"), // Hikvision
            ("/Streaming/Channels/1", "/Streaming/Channels/2"),
            ("subtype=0", "subtype=1"),             // Dahua / Amcrest
            ("_main", "_sub"),                      // Reolink (h264Preview_01_main)
            ("/live/ch00_0", "/live/ch01_0"),       // some Chinese OEMs
            ("/cam/realmonitor?channel=1&subtype=0", "/cam/realmonitor?channel=1&subtype=1"),
            ("/h264Preview_01_main", "/h264Preview_01_sub"),
            ("/profile1", "/profile2"),
            ("/ch0_0.264", "/ch0_1.264")
        ]
        for (from, to) in rules where main.contains(from) {
            return main.replacingOccurrences(of: from, with: to)
        }
        return nil
    }

    public static func preset(for manufacturer: CameraManufacturer) -> RTSPURLPreset {
        switch manufacturer {
        case .hikvision:
            return RTSPURLPreset(
                manufacturer: .hikvision,
                mainStream: "rtsp://{HOST}:554/Streaming/Channels/101",
                subStream:  "rtsp://{HOST}:554/Streaming/Channels/102"
            )
        case .dahua, .amcrest:
            return RTSPURLPreset(
                manufacturer: manufacturer,
                mainStream: "rtsp://{HOST}:554/cam/realmonitor?channel=1&subtype=0",
                subStream:  "rtsp://{HOST}:554/cam/realmonitor?channel=1&subtype=1"
            )
        case .axis:
            return RTSPURLPreset(
                manufacturer: .axis,
                mainStream: "rtsp://{HOST}:554/axis-media/media.amp",
                subStream:  "rtsp://{HOST}:554/axis-media/media.amp?resolution=640x360"
            )
        case .reolink:
            return RTSPURLPreset(
                manufacturer: .reolink,
                mainStream: "rtsp://{HOST}:554/h264Preview_01_main",
                subStream:  "rtsp://{HOST}:554/h264Preview_01_sub"
            )
        case .foscam:
            return RTSPURLPreset(
                manufacturer: .foscam,
                mainStream: "rtsp://{HOST}:88/videoMain",
                subStream:  "rtsp://{HOST}:88/videoSub"
            )
        case .ubiquiti:
            return RTSPURLPreset(
                manufacturer: .ubiquiti,
                mainStream: "rtsp://{HOST}:7447/live/ch00_0",
                subStream:  "rtsp://{HOST}:7447/live/ch00_1"
            )
        case .bosch:
            return RTSPURLPreset(
                manufacturer: .bosch,
                mainStream: "rtsp://{HOST}:554/?inst=1",
                subStream:  "rtsp://{HOST}:554/?inst=2"
            )
        case .panasonic:
            return RTSPURLPreset(
                manufacturer: .panasonic,
                mainStream: "rtsp://{HOST}:554/MediaInput/h264/stream_1",
                subStream:  "rtsp://{HOST}:554/MediaInput/h264/stream_2"
            )
        case .sony:
            return RTSPURLPreset(
                manufacturer: .sony,
                mainStream: "rtsp://{HOST}:554/media/video1",
                subStream:  "rtsp://{HOST}:554/media/video2"
            )
        case .vivotek:
            return RTSPURLPreset(
                manufacturer: .vivotek,
                mainStream: "rtsp://{HOST}:554/live.sdp",
                subStream:  nil
            )
        case .mobotix:
            return RTSPURLPreset(
                manufacturer: .mobotix,
                mainStream: "rtsp://{HOST}:554/mobotix.h264",
                subStream:  nil
            )
        case .generic:
            return RTSPURLPreset(
                manufacturer: .generic,
                mainStream: "rtsp://{HOST}:554/",
                subStream:  nil
            )
        }
    }

    public static func renderedMainStream(
        for manufacturer: CameraManufacturer,
        host: String
    ) -> String {
        preset(for: manufacturer).mainStream
            .replacingOccurrences(of: "{HOST}", with: host)
    }

    public static func renderedSubStream(
        for manufacturer: CameraManufacturer,
        host: String
    ) -> String? {
        preset(for: manufacturer).subStream?
            .replacingOccurrences(of: "{HOST}", with: host)
    }
}
