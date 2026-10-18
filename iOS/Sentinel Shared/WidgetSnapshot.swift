// WidgetSnapshot.swift
// What the app tells its widgets, through the App Group container. Compiled
// into both the app (which writes it after each refresh / stream event) and
// the "Sentinel Widgets" extension (which only reads it).
//
// The snapshot is deliberately small and contains nothing secret: no tokens,
// no server addresses, no user names — just the counts and the newest alarm
// a Home/Lock Screen widget shows. Sign-in tokens stay in the app's Keychain.

import Foundation

enum SentinelAppGroup {
    static let identifier = "group.com.handoffgrid.sentinel.mobile"

    /// The shared container, or nil when the App Group isn't provisioned
    /// (unsigned simulator builds): writers and readers then do nothing.
    static var containerURL: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: identifier)
    }
}

/// Deep links the widgets and the Live Activity open (`sentinelmobile://…`).
enum SentinelDeepLink: Equatable {
    case home
    case alarms
    case alarm(id: String)

    static let scheme = "sentinelmobile"

    var url: URL {
        var c = URLComponents()
        c.scheme = Self.scheme
        switch self {
        case .home: c.host = "home"
        case .alarms: c.host = "alarms"
        case .alarm(let id):
            c.host = "alarm"
            c.path = "/" + id
        }
        return c.url ?? URL(string: "\(Self.scheme)://home")!
    }

    init?(url: URL) {
        guard url.scheme?.lowercased() == Self.scheme else { return nil }
        switch url.host?.lowercased() {
        case "home": self = .home
        case "alarms": self = .alarms
        case "alarm":
            let id = url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
            self = id.isEmpty ? .alarms : .alarm(id: id)
        default: return nil
        }
    }
}

struct WidgetSnapshot: Codable, Equatable {
    static let currentVersion = 1

    var version: Int = Self.currentVersion
    /// When the app wrote it (seconds since 1970).
    var generatedAt: Date
    /// "Warehouse"; "Demo" in demo mode; empty when no site is chosen.
    var siteName: String
    var signedIn: Bool
    var isDemo: Bool
    var camerasOnline: Int
    var camerasTotal: Int
    var camerasRecording: Int
    var openAlarms: Int
    var newAlarms: Int
    /// The newest open alarm, if any.
    var lastAlarm: AlarmSummary?

    struct AlarmSummary: Codable, Equatable {
        var id: String
        var title: String
        var cameraName: String
        var severity: String   // Critical | Warning | Info
        var state: String      // New | Acknowledged | …
        var raisedAt: Date
        /// A small JPEG of the alarm's event sits next to the snapshot.
        var hasImage: Bool
    }

    /// Recording state for the status widget: all / some / none of the
    /// online cameras are recording.
    enum RecordingState: String { case all, some, none }

    var recordingState: RecordingState {
        if camerasOnline == 0 || camerasRecording == 0 { return .none }
        return camerasRecording >= camerasOnline ? .all : .some
    }

    /// Equal apart from the timestamp (the app skips rewriting and reloading
    /// timelines when nothing a widget shows changed).
    func sameContent(as other: WidgetSnapshot?) -> Bool {
        guard var other else { return false }
        other.generatedAt = generatedAt
        return other == self
    }

    static let signedOut = WidgetSnapshot(generatedAt: Date(timeIntervalSince1970: 0), siteName: "", signedIn: false,
                                          isDemo: false, camerasOnline: 0, camerasTotal: 0, camerasRecording: 0,
                                          openAlarms: 0, newAlarms: 0, lastAlarm: nil)

    /// Sample content for widget galleries and previews.
    static let placeholder = WidgetSnapshot(
        generatedAt: Date(), siteName: "Warehouse", signedIn: true, isDemo: false,
        camerasOnline: 11, camerasTotal: 12, camerasRecording: 11, openAlarms: 2, newAlarms: 1,
        lastAlarm: AlarmSummary(id: "sample", title: "Person detected", cameraName: "Loading Dock",
                                severity: "Critical", state: "New", raisedAt: Date().addingTimeInterval(-240),
                                hasImage: false))
}

/// Reads and writes the snapshot (and the last alarm's thumbnail) in the
/// App Group container. `directory` is injectable for tests.
struct WidgetSnapshotStore {
    static let fileName = "widget-snapshot.json"
    static let imageName = "widget-last-alarm.jpg"
    /// Widgets have a tight memory budget; larger thumbnails are not shared.
    static let maxImageBytes = 64 * 1024

    var directory: URL?

    init(directory: URL? = SentinelAppGroup.containerURL) {
        self.directory = directory
    }

    static let shared = WidgetSnapshotStore()

    private static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .secondsSince1970
        e.outputFormatting = [.sortedKeys]
        return e
    }()

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .secondsSince1970
        return d
    }()

    private var snapshotURL: URL? { directory?.appendingPathComponent(Self.fileName) }
    private var imageURL: URL? { directory?.appendingPathComponent(Self.imageName) }

    func read() -> WidgetSnapshot? {
        guard let url = snapshotURL, let data = try? Data(contentsOf: url) else { return nil }
        guard let snapshot = try? Self.decoder.decode(WidgetSnapshot.self, from: data),
              snapshot.version <= WidgetSnapshot.currentVersion else { return nil }
        return snapshot
    }

    /// Writes atomically. Returns false when there is no shared container.
    @discardableResult
    func write(_ snapshot: WidgetSnapshot) -> Bool {
        guard let url = snapshotURL, let data = try? Self.encoder.encode(snapshot) else { return false }
        return (try? data.write(to: url, options: [.atomic])) != nil
    }

    func readImage() -> Data? {
        guard let url = imageURL else { return nil }
        return try? Data(contentsOf: url)
    }

    /// Stores the thumbnail, or removes it (nil / too large).
    @discardableResult
    func writeImage(_ data: Data?) -> Bool {
        guard let url = imageURL else { return false }
        guard let data, data.isEmpty == false, data.count <= Self.maxImageBytes else {
            try? FileManager.default.removeItem(at: url)
            return false
        }
        return (try? data.write(to: url, options: [.atomic])) != nil
    }

    /// Sign-out / site removal: the widgets go back to "not signed in".
    func clear() {
        if let url = snapshotURL { try? FileManager.default.removeItem(at: url) }
        if let url = imageURL { try? FileManager.default.removeItem(at: url) }
    }
}
