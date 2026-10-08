import AVFoundation
import SwiftUI

public enum SentinelSection: String, CaseIterable, Identifiable, Hashable {
    case home = "Home"
    case live = "Live View"
    case personalViews = "Personal Views"
    case search = "Search"
    case alerts = "Alerts"
    case maps = "Maps"
    case evidence = "Evidence"
    case handoff = "Case Handoff"
    case notifications = "Notifications"
    case ai = "AI"
    case cameras = "Cameras"
    case storage = "Storage"
    case users = "Users"
    case audit = "Audit Log"
    case settings = "Settings"
    case health = "System Health"
    case plan = "Plan & Billing"

    public var id: String { rawValue }

    public var symbol: String {
        switch self {
        case .home: return "gauge.medium"
        case .live: return "rectangle.grid.3x2.fill"
        case .personalViews: return "rectangle.grid.2x2.fill"
        case .search: return "magnifyingglass"
        case .alerts: return "exclamationmark.octagon.fill"
        case .maps: return "map.fill"
        case .evidence: return "lock.shield.fill"
        case .handoff: return "arrow.left.arrow.right.circle.fill"
        case .notifications: return "bell.and.waves.left.and.right.fill"
        case .ai: return "sparkles"
        case .cameras: return "camera.fill"
        case .storage: return "internaldrive.fill"
        case .users: return "person.2.fill"
        case .audit: return "list.bullet.rectangle.portrait.fill"
        case .settings: return "gearshape.fill"
        case .health: return "waveform.path.ecg"
        case .plan: return "creditcard.fill"
        }
    }

    public static let operations: [SentinelSection] = [.home, .live, .personalViews, .search, .alerts, .maps, .evidence, .handoff, .notifications]
    public static let administration: [SentinelSection] = [.cameras, .storage, .users, .audit, .settings, .plan, .health]
}

/// Top-level workspace grouping for the Genetec/Milestone-style icon rail.
/// Each workspace owns a small set of `SentinelSection`s shown in the
/// contextual sub-list beside the rail. The section enum stays the source of
/// truth for routing — workspaces are purely a navigation grouping.
public enum SentinelWorkspace: String, CaseIterable, Identifiable, Hashable {
    case monitoring
    case investigate
    case alarms
    case maps
    case ai
    case admin

    public var id: String { rawValue }

    public var title: String {
        switch self {
        case .monitoring: return "Monitoring"
        case .investigate: return "Investigate"
        case .alarms: return "Alarms"
        case .maps: return "Maps"
        case .ai: return "AI"
        case .admin: return "Administration"
        }
    }

    public var symbol: String {
        switch self {
        case .monitoring: return "rectangle.grid.2x2.fill"
        case .investigate: return "magnifyingglass"
        case .alarms: return "exclamationmark.octagon.fill"
        case .maps: return "map.fill"
        case .ai: return "sparkles"
        case .admin: return "gearshape.fill"
        }
    }

    /// Short label for the narrow (64pt) icon rail, where the full `title`
    /// would overflow. The full `title` is still used for the panel header and
    /// the tooltip.
    public var railTitle: String {
        switch self {
        case .admin: return "Admin"
        default: return title
        }
    }

    /// Sections shown in this workspace's contextual sub-list. `.playback` is
    /// intentionally absent — playback is merged into the Live monitoring view.
    public var sections: [SentinelSection] {
        switch self {
        case .monitoring: return [.home, .live, .personalViews]
        case .investigate: return [.search, .evidence, .handoff]
        case .alarms: return [.alerts, .notifications]
        case .maps: return [.maps]
        case .ai: return [.ai]
        case .admin: return [.cameras, .storage, .users, .audit, .health, .settings, .plan]
        }
    }

    public var defaultSection: SentinelSection { sections.first ?? .live }

    /// Reverse lookup: which workspace owns a given section. Used so external
    /// navigation (Cmd+K, URL scheme, requestOpen) highlights the right rail
    /// icon. Falls back to `.monitoring` for any unmapped section (e.g. the
    /// legacy `.playback` case that no longer appears in a sub-list).
    public static func workspace(containing section: SentinelSection) -> SentinelWorkspace {
        allCases.first { $0.sections.contains(section) } ?? .monitoring
    }
}

public enum CameraStatus: String, Codable, Hashable {
    case online
    case motion
    case offline

    public var label: String {
        switch self {
        case .online: return "Online"
        case .motion: return "Motion"
        case .offline: return "Offline"
        }
    }

    public var symbol: String {
        switch self {
        case .online: return "checkmark.circle.fill"
        case .motion: return "figure.walk"
        case .offline: return "xmark.octagon.fill"
        }
    }

    public var tint: Color {
        switch self {
        case .online: return .green
        case .motion: return .orange
        case .offline: return .red
        }
    }
}

public enum CameraRecordingCodec: String, Codable, CaseIterable, Identifiable, Hashable {
    case h264
    case h265

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .h264: return "H.264"
        case .h265: return "H.265 / HEVC"
        }
    }

    public var shortLabel: String {
        switch self {
        case .h264: return "H.264"
        case .h265: return "H.265"
        }
    }
}

public enum CameraRecordingMode: String, Codable, CaseIterable, Identifiable, Hashable {
    case continuous
    case motion
    case dualStream

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .continuous:  return "Continuous"
        case .motion:      return "Motion"
        case .dualStream:  return "Dual Stream"
        }
    }

    public var detail: String {
        switch self {
        case .continuous:  return "Keep every segment"
        case .motion:      return "Keep clips around motion"
        case .dualStream:  return "Low-res always + high-res on motion"
        }
    }
}

public struct CameraFeed: Codable, Identifiable, Hashable {
    public let id: UUID
    public let name: String
    public let location: String
    public let status: CameraStatus
    public let resolution: String
    public let fps: Int
    public let bitrate: String
    public let ipAddress: String
    public let profile: String
    public let isRecording: Bool
    public let recordingMode: CameraRecordingMode
    public let recordingCodec: CameraRecordingCodec
    public let rtspURL: String
    public let subStreamRTSPURL: String
    public let username: String
    public let localDeviceID: String?
    public let onvifServiceURL: String?
    public let onvifProfileToken: String?
    public let recordingSchedule: RecordingScheduleBlock?
    public let retentionDays: Int?
    public let storageQuotaGB: Double?

    public enum CodingKeys: String, CodingKey {
        case id, name, location, status, resolution, fps, bitrate, ipAddress
        case profile, isRecording, recordingMode, recordingCodec, rtspURL, subStreamRTSPURL, username
        case localDeviceID, onvifServiceURL, onvifProfileToken, recordingSchedule, retentionDays
        case storageQuotaGB
    }

    public init(
        id: UUID = UUID(),
        name: String,
        location: String,
        status: CameraStatus,
        resolution: String,
        fps: Int,
        bitrate: String,
        ipAddress: String,
        profile: String,
        isRecording: Bool,
        recordingMode: CameraRecordingMode = .continuous,
        recordingCodec: CameraRecordingCodec = .h265,
        rtspURL: String = "",
        subStreamRTSPURL: String = "",
        username: String = "",
        localDeviceID: String? = nil,
        onvifServiceURL: String? = nil,
        onvifProfileToken: String? = nil,
        recordingSchedule: RecordingScheduleBlock? = nil,
        retentionDays: Int? = nil,
        storageQuotaGB: Double? = nil
    ) {
        self.id = id
        self.name = name
        self.location = location
        self.status = status
        self.resolution = resolution
        self.fps = fps
        self.bitrate = bitrate
        self.ipAddress = ipAddress
        self.profile = profile
        self.isRecording = isRecording
        self.recordingMode = recordingMode
        self.recordingCodec = recordingCodec
        self.rtspURL = rtspURL
        self.subStreamRTSPURL = subStreamRTSPURL
        self.username = username
        self.localDeviceID = localDeviceID
        self.onvifServiceURL = onvifServiceURL
        self.onvifProfileToken = onvifProfileToken
        self.recordingSchedule = recordingSchedule
        self.retentionDays = retentionDays
        self.storageQuotaGB = storageQuotaGB
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)

        id = try c.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? "Unnamed Camera"
        location = try c.decodeIfPresent(String.self, forKey: .location) ?? "Unassigned"
        status = try c.decodeIfPresent(CameraStatus.self, forKey: .status) ?? .offline
        resolution = try c.decodeIfPresent(String.self, forKey: .resolution) ?? "Pending"
        fps = try c.decodeIfPresent(Int.self, forKey: .fps) ?? 0
        bitrate = try c.decodeIfPresent(String.self, forKey: .bitrate) ?? "0 Mbps"
        ipAddress = try c.decodeIfPresent(String.self, forKey: .ipAddress) ?? "Unknown"
        profile = try c.decodeIfPresent(String.self, forKey: .profile) ?? "Main Stream"
        isRecording = try c.decodeIfPresent(Bool.self, forKey: .isRecording) ?? false
        recordingMode = try c.decodeIfPresent(CameraRecordingMode.self, forKey: .recordingMode) ?? .continuous
        recordingCodec = try c.decodeIfPresent(CameraRecordingCodec.self, forKey: .recordingCodec) ?? .h265
        rtspURL = try c.decodeIfPresent(String.self, forKey: .rtspURL) ?? ""
        subStreamRTSPURL = try c.decodeIfPresent(String.self, forKey: .subStreamRTSPURL) ?? ""
        username = try c.decodeIfPresent(String.self, forKey: .username) ?? ""
        localDeviceID = try c.decodeIfPresent(String.self, forKey: .localDeviceID)
        onvifServiceURL = try c.decodeIfPresent(String.self, forKey: .onvifServiceURL)
        onvifProfileToken = try c.decodeIfPresent(String.self, forKey: .onvifProfileToken)
        recordingSchedule = try c.decodeIfPresent(RecordingScheduleBlock.self, forKey: .recordingSchedule)
        retentionDays = try c.decodeIfPresent(Int.self, forKey: .retentionDays)
        storageQuotaGB = try c.decodeIfPresent(Double.self, forKey: .storageQuotaGB)
    }

    public var isLocalCamera: Bool {
        localDeviceID?.isEmpty == false
    }

    public var isDemoCamera: Bool {
        LegacyRuntimeData.isDemoCamera(name: name, location: location, ipAddress: ipAddress) &&
        rtspURL.isEmpty &&
        localDeviceID == nil
    }

    public var hasEmbeddedRTSPCredentials: Bool {
        let sanitized = RTSPCredentialFormatter.sanitize(rtspURL)
        return sanitized.username.isEmpty == false && sanitized.password.isEmpty == false
    }

    public var rtspURLWithStoredCredentials: String {
        if hasEmbeddedRTSPCredentials {
            return rtspURL
        }

        guard let password = try? CameraSecrets.password(for: id),
              password.isEmpty == false else {
            return rtspURL
        }

        return RTSPCredentialFormatter.url(
            rtspURL,
            username: username,
            password: password
        )
    }

    public var requiresSecureCredentialBridge: Bool {
        username.isEmpty == false
    }
}

public extension CameraFeed {
    public var supportsPTZ: Bool { onvifServiceURL != nil && onvifProfileToken != nil }

    public var isInRecordingSchedule: Bool {
        guard let schedule = recordingSchedule else { return true }
        return schedule.isActiveNow
    }

    public func with(
        name: String? = nil,
        location: String? = nil,
        status: CameraStatus? = nil,
        resolution: String? = nil,
        fps: Int? = nil,
        bitrate: String? = nil,
        ipAddress: String? = nil,
        profile: String? = nil,
        isRecording: Bool? = nil,
        recordingMode: CameraRecordingMode? = nil,
        recordingCodec: CameraRecordingCodec? = nil,
        rtspURL: String? = nil,
        subStreamRTSPURL: String? = nil,
        username: String? = nil,
        onvifProfileToken: String?? = nil,
        recordingSchedule: RecordingScheduleBlock?? = nil,
        retentionDays: Int?? = nil,
        storageQuotaGB: Double?? = nil
    ) -> CameraFeed {
        CameraFeed(
            id: id,
            name: name ?? self.name,
            location: location ?? self.location,
            status: status ?? self.status,
            resolution: resolution ?? self.resolution,
            fps: fps ?? self.fps,
            bitrate: bitrate ?? self.bitrate,
            ipAddress: ipAddress ?? self.ipAddress,
            profile: profile ?? self.profile,
            isRecording: isRecording ?? self.isRecording,
            recordingMode: recordingMode ?? self.recordingMode,
            recordingCodec: recordingCodec ?? self.recordingCodec,
            rtspURL: rtspURL ?? self.rtspURL,
            subStreamRTSPURL: subStreamRTSPURL ?? self.subStreamRTSPURL,
            username: username ?? self.username,
            localDeviceID: localDeviceID,
            onvifServiceURL: onvifServiceURL,
            onvifProfileToken: onvifProfileToken == nil ? self.onvifProfileToken : onvifProfileToken!,
            recordingSchedule: recordingSchedule == nil ? self.recordingSchedule : recordingSchedule!,
            retentionDays: retentionDays == nil ? self.retentionDays : retentionDays!,
            storageQuotaGB: storageQuotaGB == nil ? self.storageQuotaGB : storageQuotaGB!
        )
    }
}

public struct RecordingScheduleBlock: Codable, Hashable {
    public var daysOfWeek: Set<Int>
    public var startHour: Int
    public var endHour: Int

    public static let businessHours = RecordingScheduleBlock(daysOfWeek: [2,3,4,5,6], startHour: 8, endHour: 18)
    public static let afterHours = RecordingScheduleBlock(daysOfWeek: [1,2,3,4,5,6,7], startHour: 18, endHour: 8)
    public static let overnight = RecordingScheduleBlock(daysOfWeek: [1,2,3,4,5,6,7], startHour: 22, endHour: 6)

    public var isActiveNow: Bool {
        let cal = Calendar.current
        let now = Date()
        let hour = cal.component(.hour, from: now)
        let weekday = cal.component(.weekday, from: now)
        guard daysOfWeek.contains(weekday) else { return false }
        if startHour <= endHour {
            return hour >= startHour && hour < endHour
        } else {
            return hour >= startHour || hour < endHour
        }
    }

    public var label: String {
        let days = daysOfWeek.sorted().compactMap { weekdayShort($0) }.joined(separator: " ")
        return "\(days) \(startHour):00–\(endHour):00"
    }

    private func weekdayShort(_ day: Int) -> String? {
        ["", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"][safe: day]
    }
}

private extension Array {
    subscript(safe index: Int) -> Element? {
        indices.contains(index) ? self[index] : nil
    }
}

public struct SanitizedRTSPInput {
    public let url: String
    public let username: String
    public let password: String

    public var hasEmbeddedCredentials: Bool {
        username.isEmpty == false || password.isEmpty == false
    }
}

public enum RTSPCredentialFormatter {
    public static func sanitize(_ rawURL: String) -> SanitizedRTSPInput {
        let trimmedURL = rawURL.trimmingCharacters(in: .whitespacesAndNewlines)

        guard var components = URLComponents(string: trimmedURL) else {
            return SanitizedRTSPInput(url: trimmedURL, username: "", password: "")
        }

        let username = components.user ?? ""
        let password = components.password ?? ""
        components.user = nil
        components.password = nil

        return SanitizedRTSPInput(
            url: components.string ?? trimmedURL,
            username: username,
            password: password
        )
    }

    public static func url(_ rawURL: String, username: String, password: String) -> String {
        guard rawURL.lowercased().hasPrefix("rtsp://"),
              password.isEmpty == false,
              var components = URLComponents(string: rawURL) else {
            return rawURL
        }

        if components.password?.isEmpty == false {
            return rawURL
        }

        let existingUser = components.user ?? ""
        let resolvedUser = username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? existingUser : username

        guard resolvedUser.isEmpty == false else {
            return rawURL
        }

        components.user = resolvedUser
        components.password = password
        return components.string ?? rawURL
    }

    public static func redacted(_ rawURL: String) -> String {
        guard let rtspRange = rawURL.range(of: "rtsp://", options: [.caseInsensitive]) else {
            return rawURL
        }

        let prefix = String(rawURL[..<rtspRange.lowerBound])
        let urlPart = String(rawURL[rtspRange.lowerBound...])
        guard var components = URLComponents(string: urlPart) else {
            return rawURL
        }

        if components.user != nil || components.password != nil {
            components.password = components.password == nil ? nil : "redacted"
        }

        return prefix + (components.string ?? urlPart)
    }

    public static func normalizedStreamKey(_ rawURL: String) -> String? {
        let sanitized = sanitize(rawURL).url
        guard var components = URLComponents(string: sanitized),
              components.scheme?.lowercased() == "rtsp",
              let host = components.host?.trimmingCharacters(in: .whitespacesAndNewlines),
              host.isEmpty == false else {
            return nil
        }

        if let port = components.port, (1...65535).contains(port) == false {
            return nil
        }

        components.scheme = "rtsp"
        components.host = host.lowercased()
        components.user = nil
        components.password = nil
        return components.string?.lowercased()
    }

    public static func isUsableRTSPURL(_ rawURL: String) -> Bool {
        normalizedStreamKey(rawURL) != nil
    }

    // Hard gate before any string ends up inside a GStreamer pipeline argument
    // (location=..., uri=...). GStreamer treats `!`, whitespace, and newlines
    // as element separators / property delimiters, so an attacker-controlled
    // RTSP URL that contains them could splice in extra elements or override
    // sink locations. Reject anything that doesn't parse as a plain rtsp(s)
    // URL with a host and free of those bytes.
    public static func isSafeForPipeline(_ rawURL: String) -> Bool {
        guard rawURL.isEmpty == false else { return false }
        // Reject whitespace, GStreamer separator `!`, NULs, quotes, backslash.
        let forbidden: Set<Character> = ["!", " ", "\t", "\n", "\r", "\0", "\"", "'", "\\", "`", "$"]
        if rawURL.contains(where: { forbidden.contains($0) }) {
            return false
        }
        guard let components = URLComponents(string: rawURL),
              let scheme = components.scheme?.lowercased(),
              scheme == "rtsp" || scheme == "rtsps",
              let host = components.host, host.isEmpty == false else {
            return false
        }
        if let port = components.port, (1...65535).contains(port) == false {
            return false
        }
        return true
    }
}

public struct NewCameraDraft {
    public var name = ""
    public var location = ""
    public var rtspURL = ""
    public var username = ""
    public var password = ""
    public var profile = "Main Stream"
    public var recordingEnabled = true
    public var recordingMode = CameraRecordingMode.continuous
    public var recordingCodec = CameraRecordingCodec.h265

    public init() {}

    public var trimmedName: String {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public var trimmedURL: String {
        sanitizedRTSPInput.url
    }

    public var effectiveUsername: String {
        let trimmedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmedUsername.isEmpty ? sanitizedRTSPInput.username : trimmedUsername
    }

    public var effectivePassword: String {
        password.isEmpty ? sanitizedRTSPInput.password : password
    }

    public var didMoveEmbeddedCredentials: Bool {
        sanitizedRTSPInput.hasEmbeddedCredentials
    }

    public var unattendedRTSPURL: String {
        RTSPCredentialFormatter.url(
            trimmedURL,
            username: effectiveUsername,
            password: effectivePassword
        )
    }

    private var sanitizedRTSPInput: SanitizedRTSPInput {
        RTSPCredentialFormatter.sanitize(rtspURL)
    }

    public var isValid: Bool {
        trimmedName.isEmpty == false && RTSPCredentialFormatter.isUsableRTSPURL(trimmedURL)
    }

    public var validationMessage: String? {
        if trimmedName.isEmpty {
            return "Camera name is required."
        }

        guard let components = URLComponents(string: trimmedURL),
              components.scheme?.lowercased() == "rtsp" else {
            return "RTSP URL must start with rtsp://"
        }

        if components.host?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty != false {
            return "RTSP URL must include a camera host or IP address."
        }

        if let port = components.port, (1...65535).contains(port) == false {
            return "RTSP port must be between 1 and 65535."
        }

        return nil
    }

    public func makeCamera(id: UUID = UUID()) -> CameraFeed {
        CameraFeed(
            id: id,
            name: trimmedName,
            location: location.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Unassigned" : location.trimmingCharacters(in: .whitespacesAndNewlines),
            status: .offline,
            resolution: "Pending",
            fps: 0,
            bitrate: "0 Mbps",
            ipAddress: parsedHost,
            profile: profile,
            isRecording: recordingEnabled,
            recordingMode: recordingMode,
            recordingCodec: recordingCodec,
            rtspURL: unattendedRTSPURL,
            username: effectiveUsername,
            localDeviceID: nil
        )
    }

    private var parsedHost: String {
        URL(string: trimmedURL)?.host() ?? "Unknown"
    }
}

public struct CameraDisplayDraft {
    public var name: String
    public var location: String
    public var profile: String
    public var username: String
    public var password = ""
    public var subStreamRTSPURL: String
    public var enableSchedule: Bool
    public var scheduleDays: Set<Int>
    public var scheduleStartHour: Int
    public var scheduleEndHour: Int
    public var retentionDays: Int

    public init(camera: CameraFeed) {
        name = camera.name
        location = camera.location
        profile = camera.profile
        username = camera.username
        subStreamRTSPURL = camera.subStreamRTSPURL
        enableSchedule = camera.recordingSchedule != nil
        scheduleDays = camera.recordingSchedule?.daysOfWeek ?? [2, 3, 4, 5, 6]
        scheduleStartHour = camera.recordingSchedule?.startHour ?? 0
        scheduleEndHour = camera.recordingSchedule?.endHour ?? 24
        retentionDays = camera.retentionDays ?? 0
    }

    public var trimmedName: String {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public var trimmedLocation: String {
        let value = location.trimmingCharacters(in: .whitespacesAndNewlines)
        return value.isEmpty ? "Unassigned" : value
    }

    public var trimmedProfile: String {
        let value = profile.trimmingCharacters(in: .whitespacesAndNewlines)
        return value.isEmpty ? "Main Stream" : value
    }

    public var trimmedUsername: String {
        username.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public var isValid: Bool {
        trimmedName.isEmpty == false
    }
}

@MainActor
public final class CameraStore: ObservableObject {
    @Published public private(set) var cameras: [CameraFeed] = []
    @Published public private(set) var lastError: String?

    /// (action, detail) — wired by the app into the audit log.
    public var auditRecorder: ((String, String) -> Void)?

    private let fileURL: URL

    public var demoCameraCount: Int {
        cameras.filter(\.isDemoCamera).count
    }

    public func canAddCamera(limit: Int) -> Bool {
        cameras.filter { !$0.isDemoCamera }.count < limit
    }

    public func cameraCountTowardLimit() -> Int {
        cameras.filter { !$0.isDemoCamera }.count
    }

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)

        self.fileURL = supportDirectory.appendingPathComponent("cameras.json")
        load()
    }

    @discardableResult
    public func addCamera(from draft: NewCameraDraft, limit: Int = 999) -> Bool {
        guard cameras.filter({ !$0.isDemoCamera }).count < limit else {
            lastError = "Camera limit reached. Upgrade Sentinel to add more cameras."
            return false
        }

        let cameraID = UUID()

        guard let streamKey = RTSPCredentialFormatter.normalizedStreamKey(draft.trimmedURL) else {
            lastError = draft.validationMessage ?? "Enter a valid RTSP URL."
            return false
        }

        guard hasCamera(withStreamKey: streamKey) == false else {
            lastError = "That RTSP stream is already configured."
            return false
        }

        cameras.append(draft.makeCamera(id: cameraID))
        save()
        auditRecorder?("Added camera", cameras.last.map { "\($0.name) · \($0.ipAddress)" } ?? "")
        return lastError == nil
    }

    @discardableResult
    public func addLocalCamera() -> Bool {
        let devices = LocalCameraCatalog.availableDevices()

        guard devices.isEmpty == false else {
            lastError = "No local camera was found on this Mac."
            return false
        }

        guard let device = devices.first(where: { candidate in
            cameras.contains { $0.localDeviceID == candidate.uniqueID } == false
        }) else {
            lastError = "All detected local cameras have already been added."
            return false
        }

        let camera = CameraFeed(
            name: device.localizedName,
            location: "This Mac",
            status: .online,
            resolution: "Local",
            fps: 30,
            bitrate: "Local",
            ipAddress: "localhost",
            profile: "AVFoundation",
            isRecording: false,
            localDeviceID: device.uniqueID
        )

        cameras.append(camera)
        save()
        return lastError == nil
    }

    public func deleteCamera(_ camera: CameraFeed) {
        cameras.removeAll { $0.id == camera.id }
        auditRecorder?("Deleted camera", "\(camera.name) · \(camera.ipAddress)")
        CameraSecrets.deletePassword(for: camera.id)
        save()
    }

    public func deleteDemoCameras() {
        let demoIDs = cameras.filter(\.isDemoCamera).map(\.id)
        cameras.removeAll { $0.isDemoCamera }
        demoIDs.forEach { CameraSecrets.deletePassword(for: $0) }
        save()
    }

    public func deleteAllCameras() {
        auditRecorder?("Deleted all cameras", "\(cameras.count) camera(s)")
        let cameraIDs = cameras.map(\.id)
        cameras.removeAll()
        cameraIDs.forEach { CameraSecrets.deletePassword(for: $0) }
        save()
    }

    public func moveCamera(from source: IndexSet, to destination: Int) {
        cameras.move(fromOffsets: source, toOffset: destination)
        save()
    }

    public func setRetentionDays(_ days: Int?, for cameraID: UUID) {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else { return }
        cameras[index] = cameras[index].with(retentionDays: .some(days))
        save()
    }

    public func setRecordingSchedule(_ schedule: RecordingScheduleBlock?, for cameraID: UUID) {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else { return }
        cameras[index] = cameras[index].with(recordingSchedule: .some(schedule))
        save()
    }

    public func setRecordingEnabled(_ isEnabled: Bool, for cameraID: UUID) {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else { return }
        cameras[index] = cameras[index].with(isRecording: isEnabled)
        save()
    }

    public func setRecordingMode(_ mode: CameraRecordingMode, for cameraID: UUID) {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else { return }
        cameras[index] = cameras[index].with(recordingMode: mode)
        save()
    }

    public func setRecordingCodec(_ codec: CameraRecordingCodec, for cameraID: UUID) {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else { return }
        cameras[index] = cameras[index].with(recordingCodec: codec)
        save()
    }

    @discardableResult
    public func updateCameraDisplay(cameraID: UUID, draft: CameraDisplayDraft) -> Bool {
        guard draft.isValid else {
            lastError = "Camera name is required."
            return false
        }

        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else {
            lastError = "Camera was not found."
            return false
        }

        let camera = cameras[index]
        let sanitizedRTSP = RTSPCredentialFormatter.sanitize(camera.rtspURL)
        let resolvedUsername = draft.trimmedUsername.isEmpty ? sanitizedRTSP.username : draft.trimmedUsername
        let resolvedPassword = draft.password.isEmpty ? sanitizedRTSP.password : draft.password
        let updatedRTSPURL: String

        if camera.isLocalCamera || camera.rtspURL.isEmpty {
            updatedRTSPURL = camera.rtspURL
        } else if resolvedPassword.isEmpty == false {
            updatedRTSPURL = RTSPCredentialFormatter.url(
                sanitizedRTSP.url,
                username: resolvedUsername,
                password: resolvedPassword
            )
        } else {
            updatedRTSPURL = sanitizedRTSP.url
        }

        let newSchedule: RecordingScheduleBlock? = draft.enableSchedule
            ? RecordingScheduleBlock(
                daysOfWeek: draft.scheduleDays,
                startHour: draft.scheduleStartHour,
                endHour: draft.scheduleEndHour
              )
            : nil

        let trimmedSubStream = draft.subStreamRTSPURL.trimmingCharacters(in: .whitespacesAndNewlines)
        cameras[index] = camera.with(
            name: draft.trimmedName,
            location: draft.trimmedLocation,
            profile: draft.trimmedProfile,
            rtspURL: updatedRTSPURL,
            subStreamRTSPURL: trimmedSubStream,
            username: camera.isLocalCamera ? "" : resolvedUsername,
            recordingSchedule: .some(newSchedule),
            retentionDays: .some(draft.retentionDays > 0 ? draft.retentionDays : nil)
        )

        if resolvedPassword.isEmpty == false {
            CameraSecrets.deletePassword(for: camera.id)
        }

        save()
        auditRecorder?("Edited camera settings", draft.trimmedName.isEmpty ? camera.name : draft.trimmedName)
        return lastError == nil
    }

    @discardableResult
    public func saveUnattendedPassword(_ password: String, for cameraID: UUID) -> Bool {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else {
            lastError = "Camera was not found."
            return false
        }

        guard password.isEmpty == false else {
            lastError = "Enter the camera password to enable unattended recording."
            return false
        }

        let camera = cameras[index]
        let sanitizedRTSP = RTSPCredentialFormatter.sanitize(camera.rtspURL)
        let resolvedUsername = camera.username.isEmpty ? sanitizedRTSP.username : camera.username

        guard resolvedUsername.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false else {
            lastError = "Enter a camera username before saving an unattended password."
            return false
        }

        let storedURL = RTSPCredentialFormatter.url(
            sanitizedRTSP.url,
            username: resolvedUsername,
            password: password
        )

        cameras[index] = camera.with(rtspURL: storedURL, username: resolvedUsername)
        CameraSecrets.deletePassword(for: camera.id)
        save()
        return lastError == nil
    }

    @discardableResult
    public func addDiscoveredCamera(
        _ discoveredCamera: ONVIFDiscoveredCamera,
        profile: ONVIFStreamProfile,
        username: String = "",
        password: String = "",
        limit: Int = 999
    ) -> Bool {
        guard cameras.filter({ !$0.isDemoCamera }).count < limit else {
            lastError = "Camera limit reached. Upgrade Sentinel to add more cameras."
            return false
        }

        let cameraID = UUID()
        let sanitizedRTSP = RTSPCredentialFormatter.sanitize(profile.rtspURL)
        guard let streamKey = RTSPCredentialFormatter.normalizedStreamKey(sanitizedRTSP.url) else {
            lastError = "The selected profile did not return a usable RTSP URL."
            return false
        }

        guard hasCamera(withStreamKey: streamKey) == false else {
            lastError = "That ONVIF stream is already configured."
            return false
        }

        let trimmedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        let resolvedUsername = trimmedUsername.isEmpty ? sanitizedRTSP.username : trimmedUsername
        let resolvedPassword = password.isEmpty ? sanitizedRTSP.password : password
        let storedRTSPURL = RTSPCredentialFormatter.url(
            sanitizedRTSP.url,
            username: resolvedUsername,
            password: resolvedPassword
        )

        let camera = CameraFeed(
            id: cameraID,
            name: discoveredCamera.name,
            location: "Discovered",
            status: .offline,
            resolution: profile.resolution,
            fps: profile.fps,
            bitrate: "Pending",
            ipAddress: discoveredCamera.host,
            profile: profile.name,
            isRecording: true,
            recordingMode: .continuous,
            recordingCodec: .h265,
            rtspURL: storedRTSPURL,
            username: resolvedUsername,
            onvifServiceURL: discoveredCamera.serviceURL,
            onvifProfileToken: profile.token.isEmpty ? nil : profile.token
        )

        cameras.append(camera)
        save()
        auditRecorder?("Added camera (discovered)", "\(camera.name) · \(camera.ipAddress)")
        return lastError == nil
    }

    /// Re-point an existing ONVIF camera at a different stream profile, keeping
    /// its identity (and therefore its recordings/credentials) intact. Used by
    /// the one-time migration that moves cameras off an unviewable MJPEG profile
    /// onto their H.264/H.265 profile. Embedded credentials from the existing
    /// URL are preserved when the incoming ONVIF URL omits them.
    @discardableResult
    public func repointStream(
        cameraID: UUID,
        rtspURL: String,
        profileName: String,
        onvifProfileToken: String?,
        resolution: String,
        fps: Int
    ) -> Bool {
        guard let index = cameras.firstIndex(where: { $0.id == cameraID }) else { return false }
        let camera = cameras[index]
        let existing = RTSPCredentialFormatter.sanitize(camera.rtspURL)
        let incoming = RTSPCredentialFormatter.sanitize(rtspURL)
        let user = incoming.username.isEmpty ? existing.username : incoming.username
        let pass = incoming.password.isEmpty ? existing.password : incoming.password
        let storedURL = pass.isEmpty
            ? incoming.url
            : RTSPCredentialFormatter.url(incoming.url, username: user, password: pass)
        cameras[index] = camera.with(
            resolution: resolution,
            fps: fps,
            profile: profileName,
            rtspURL: storedURL,
            onvifProfileToken: .some(onvifProfileToken)
        )
        save()
        return true
    }

    private func hasCamera(withStreamKey streamKey: String) -> Bool {
        cameras.contains { camera in
            RTSPCredentialFormatter.normalizedStreamKey(camera.rtspURL) == streamKey
        }
    }

    public func resetToDemoCameras() {
        cameras = []
        save()
    }

    private func load() {
        do {
            let directory = fileURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)

            guard FileManager.default.fileExists(atPath: fileURL.path) else {
                cameras = []
                save()
                return
            }

            try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: fileURL.path)
            let data = try Data(contentsOf: fileURL)
            cameras = try JSONDecoder().decode([CameraFeed].self, from: data)
            lastError = nil
            cleanupCameraInventory()
        } catch {
            cameras = []
            lastError = error.localizedDescription
        }
    }

    private func cleanupCameraInventory() {
        var didUpdate = false

        let demoIDs = cameras.filter(\.isDemoCamera).map(\.id)
        if demoIDs.isEmpty == false {
            cameras.removeAll { $0.isDemoCamera }
            demoIDs.forEach { CameraSecrets.deletePassword(for: $0) }
            didUpdate = true
        }

        var retainedByStreamURL: [String: CameraFeed] = [:]
        var duplicateIDs: [UUID] = []

        for camera in cameras {
            guard let streamKey = RTSPCredentialFormatter.normalizedStreamKey(camera.rtspURL) else {
                continue
            }

            if let existing = retainedByStreamURL[streamKey] {
                let existingHasCredentials = existing.username.isEmpty == false
                let cameraHasCredentials = camera.username.isEmpty == false

                if cameraHasCredentials && existingHasCredentials == false {
                    duplicateIDs.append(existing.id)
                    retainedByStreamURL[streamKey] = camera
                } else {
                    duplicateIDs.append(camera.id)
                }
            } else {
                retainedByStreamURL[streamKey] = camera
            }
        }

        if duplicateIDs.isEmpty == false {
            let duplicateSet = Set(duplicateIDs)
            cameras.removeAll { duplicateSet.contains($0.id) }
            duplicateIDs.forEach { CameraSecrets.deletePassword(for: $0) }
            didUpdate = true
        }

        if didUpdate {
            save()
        }
    }

    private func save() {
        do {
            let directory = fileURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
            let data = try JSONEncoder.prettySentinel.encode(cameras)
            try data.write(to: fileURL, options: .atomic)
            try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: fileURL.path)
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }
}

public enum LocalCameraCatalog {
    public static func availableDevices() -> [AVCaptureDevice] {
        AVCaptureDevice.DiscoverySession(
            deviceTypes: [.builtInWideAngleCamera, .externalUnknown],
            mediaType: .video,
            position: .unspecified
        ).devices
    }
}

public extension JSONEncoder {
    public static var prettySentinel: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        return encoder
    }
}

public enum AlertSeverity: String, Codable, Hashable {
    case critical = "Critical"
    case warning = "Warning"
    case info = "Info"

    public var rank: Int {
        switch self {
        case .critical: return 0
        case .warning: return 1
        case .info: return 2
        }
    }

    public var symbol: String {
        switch self {
        case .critical: return "exclamationmark.octagon.fill"
        case .warning: return "exclamationmark.triangle.fill"
        case .info: return "info.circle.fill"
        }
    }

    public var tint: Color {
        switch self {
        case .critical: return .red
        case .warning: return .orange
        case .info: return SentinelTheme.accent
        }
    }
}

public enum AlertState: String, Codable, CaseIterable, Identifiable, Hashable {
    case new = "New"
    case acknowledged = "Acknowledged"
    case investigating = "Investigating"
    case snoozed = "Snoozed"
    case resolved = "Resolved"
    case falseAlarm = "False Alarm"

    public var id: String { rawValue }

    public var isOpen: Bool {
        switch self {
        case .new, .acknowledged, .investigating, .snoozed:
            return true
        case .resolved, .falseAlarm:
            return false
        }
    }

    public var tint: Color {
        switch self {
        case .new: return .red
        case .acknowledged: return SentinelTheme.amber
        case .investigating: return SentinelTheme.accent
        case .snoozed: return .secondary
        case .resolved: return .green
        case .falseAlarm: return .secondary
        }
    }

    public static func normalized(_ rawValue: String) -> AlertState {
        switch rawValue {
        case "Closed":
            return .resolved
        case "False alarm", "FalseAlarm":
            return .falseAlarm
        default:
            return AlertState(rawValue: rawValue) ?? .new
        }
    }
}

public enum AlertKind: String, Codable, CaseIterable, Identifiable, Hashable {
    case motion = "Motion"
    case person = "Person"
    case cameraOffline = "Camera Offline"
    case recording = "Recording"
    case storage = "Storage"
    case credential = "Credential"
    case system = "System"

    public var id: String { rawValue }

    public var symbol: String {
        switch self {
        case .motion: return "figure.walk.motion"
        case .person: return "person.crop.rectangle.badge.plus"
        case .cameraOffline: return "video.slash.fill"
        case .recording: return "record.circle"
        case .storage: return "internaldrive.fill"
        case .credential: return "key.fill"
        case .system: return "waveform.path.ecg"
        }
    }
}

public struct AlertEvent: Codable, Identifiable, Hashable {
    public var id: UUID
    public var time: String
    public var source: String
    public var title: String
    public var severity: AlertSeverity
    public var state: AlertState
    public var kind: AlertKind
    public var cameraID: UUID?
    public var cameraName: String?
    public var detail: String
    public var owner: String
    public var createdAt: Date
    public var updatedAt: Date
    public var lastEventAt: Date
    public var eventCount: Int
    public var snoozedUntil: Date?
    public var linkedClipPath: String?
    public var eventID: UUID?
    public var responseLog: [String]

    public var alertState: AlertState { state }

    public var isOpen: Bool {
        state.isOpen
    }

    public var isSnoozedNow: Bool {
        guard state == .snoozed,
              let snoozedUntil else {
            return false
        }

        return snoozedUntil > Date()
    }

    public var lastEventLabel: String {
        RecordingFormatters.timeFormatter.string(from: lastEventAt)
    }

    public init(
        id: UUID = UUID(),
        time: String? = nil,
        source: String,
        title: String,
        severity: AlertSeverity,
        state: AlertState = .new,
        kind: AlertKind = .system,
        cameraID: UUID? = nil,
        cameraName: String? = nil,
        detail: String = "",
        owner: String = "Unassigned",
        createdAt: Date = Date(),
        updatedAt: Date = Date(),
        lastEventAt: Date? = nil,
        eventCount: Int = 1,
        snoozedUntil: Date? = nil,
        linkedClipPath: String? = nil,
        eventID: UUID? = nil,
        responseLog: [String] = []
    ) {
        self.id = id
        self.time = time ?? RecordingFormatters.timeFormatter.string(from: createdAt)
        self.source = source
        self.title = title
        self.severity = severity
        self.state = state
        self.kind = kind
        self.cameraID = cameraID
        self.cameraName = cameraName
        self.detail = detail
        self.owner = owner
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.lastEventAt = lastEventAt ?? createdAt
        self.eventCount = max(eventCount, 1)
        self.snoozedUntil = snoozedUntil
        self.linkedClipPath = linkedClipPath
        self.eventID = eventID
        self.responseLog = responseLog
    }

    public enum CodingKeys: String, CodingKey {
        case id
        case time
        case source
        case title
        case severity
        case state
        case kind
        case cameraID
        case cameraName
        case detail
        case owner
        case createdAt
        case updatedAt
        case lastEventAt
        case eventCount
        case snoozedUntil
        case linkedClipPath
        case eventID
        case responseLog
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let fallbackDate = Date()

        id = try container.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
        time = try container.decodeIfPresent(String.self, forKey: .time) ?? RecordingFormatters.timeFormatter.string(from: fallbackDate)
        source = try container.decodeIfPresent(String.self, forKey: .source) ?? "System"
        title = try container.decodeIfPresent(String.self, forKey: .title) ?? "Alert"
        severity = try container.decodeIfPresent(AlertSeverity.self, forKey: .severity) ?? .info
        let rawState = try container.decodeIfPresent(String.self, forKey: .state) ?? AlertState.new.rawValue
        state = AlertState.normalized(rawState)
        kind = try container.decodeIfPresent(AlertKind.self, forKey: .kind) ?? .system
        cameraID = try container.decodeIfPresent(UUID.self, forKey: .cameraID)
        cameraName = try container.decodeIfPresent(String.self, forKey: .cameraName)
        detail = try container.decodeIfPresent(String.self, forKey: .detail) ?? ""
        owner = try container.decodeIfPresent(String.self, forKey: .owner) ?? "Unassigned"
        createdAt = try container.decodeIfPresent(Date.self, forKey: .createdAt) ?? fallbackDate
        updatedAt = try container.decodeIfPresent(Date.self, forKey: .updatedAt) ?? createdAt
        lastEventAt = try container.decodeIfPresent(Date.self, forKey: .lastEventAt) ?? createdAt
        eventCount = max(try container.decodeIfPresent(Int.self, forKey: .eventCount) ?? 1, 1)
        snoozedUntil = try container.decodeIfPresent(Date.self, forKey: .snoozedUntil)
        linkedClipPath = try container.decodeIfPresent(String.self, forKey: .linkedClipPath)
        eventID = try container.decodeIfPresent(UUID.self, forKey: .eventID)
        responseLog = try container.decodeIfPresent([String].self, forKey: .responseLog) ?? []
    }
}

public struct SearchResult: Identifiable, Hashable {
    public let id = UUID()
    public let time: String
    public let camera: String
    public let match: String
    public let confidence: String
    public let severity: AlertSeverity
}

public struct EvidenceClip: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var caseID: String
    public var title: String
    public var camera: String
    public var range: String
    public var status: String
    public var filePath: String?
    public var sha256Hash: String?
    public var exportedBy: String?
    public var lockedAt: Date?
    /// Result of the last "Verify Integrity" re-hash against `sha256Hash`.
    public var integrityVerifiedAt: Date?
    public var integrityOK: Bool?
    /// The alarm this clip was captured for (e.g. locked from the iPhone).
    public var alertID: UUID? = nil

    public var isLocked: Bool { status == "Locked" || lockedAt != nil }
}

public struct CameraPin: Identifiable, Hashable {
    public let id = UUID()
    public let cameraName: String
    public let x: Double
    public let y: Double
    public let status: CameraStatus
}

public struct StorageVolume: Identifiable, Hashable {
    public let id = UUID()
    public let name: String
    public let capacity: String
    public let usedRatio: Double
    public let retention: String
}

public struct UserAccount: Codable, Identifiable, Hashable {
    public var id = UUID()
    public var name: String
    public var role: String
    public var status: String
    public var lastSeen: String
    public var passwordHash: String? = nil

    public var hasPin: Bool { passwordHash != nil }

    public var roleColor: String {
        switch role {
        case "Admin": return "red"
        case "Supervisor": return "orange"
        case "Operator": return "blue"
        default: return "secondary"
        }
    }
}

public struct HealthMetric: Identifiable, Hashable {
    public let id = UUID()
    public let title: String
    public let value: String
    public let detail: String
    public let tint: Color

    public static func == (lhs: HealthMetric, rhs: HealthMetric) -> Bool {
        lhs.id == rhs.id
    }

    public func hash(into hasher: inout Hasher) {
        hasher.combine(id)
    }
}

public enum LegacyRuntimeData {
    public static let demoCameraNames: Set<String> = [
        "Front Entrance",
        "Parking Lot East",
        "Warehouse Door",
        "Server Room",
        "Back Gate",
        "Reception Desk",
        "Hallway North",
        "Roof Access"
    ]

    public static let demoCameraAddresses: Set<String> = [
        "10.12.1.24",
        "10.12.1.31",
        "10.12.1.42",
        "10.12.1.58",
        "10.12.1.73",
        "10.12.1.86",
        "10.12.1.92",
        "10.12.1.104"
    ]

    public static let demoCaseIDs: Set<String> = ["HG-1042", "HG-1038", "HG-1027"]

    public static let demoPeople: Set<String> = [
        "Elijah Rivera",
        "Marisol Vega",
        "Andre Cole",
        "Security Desk"
    ]

    public static func isDemoCamera(name: String, location: String, ipAddress: String) -> Bool {
        demoCameraNames.contains(name) && demoCameraAddresses.contains(ipAddress)
    }
}
