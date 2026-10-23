import Foundation

/// What a Sentinel for Mac 1.x install left on disk, read without changing
/// anything (the old stores' initialisers write files, so they aren't used).
///
/// Everything lives in `~/Library/Application Support/HandoffGridSentinel/`:
/// `cameras.json`, `users.json`, `workflow-state.json` (alarm rules and the
/// audit log), `casework-state.json` (evidence index), `Evidence/<case>/…`
/// (locked files), `Recordings/<camera UUID>/` and `secrets.vault` (camera
/// passwords, unlocked by an operator's password).
public struct LegacyMacInstall {
    public let directory: URL

    public static var defaultDirectory: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
    }

    public init(directory: URL = LegacyMacInstall.defaultDirectory) {
        self.directory = directory
    }

    public var recordingsRoot: URL { directory.appendingPathComponent("Recordings", isDirectory: true) }
    public var vaultURL: URL { directory.appendingPathComponent("secrets.vault") }

    // MARK: Files

    public struct Camera: Decodable, Hashable {
        public var id: UUID
        public var name: String
        public var location: String
        public var rtspURL: String
        public var subStreamRTSPURL: String
        public var username: String
        public var isRecording: Bool
        public var recordingMode: String
        public var retentionDays: Int?
        public var onvifServiceURL: String?
        public var localDeviceID: String?
        public var ipAddress: String

        enum CodingKeys: String, CodingKey {
            case id, name, location, rtspURL, subStreamRTSPURL, username, isRecording, recordingMode
            case retentionDays, onvifServiceURL, localDeviceID, ipAddress
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(UUID.self, forKey: .id)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? "Unnamed Camera"
            location = try c.decodeIfPresent(String.self, forKey: .location) ?? ""
            rtspURL = try c.decodeIfPresent(String.self, forKey: .rtspURL) ?? ""
            subStreamRTSPURL = try c.decodeIfPresent(String.self, forKey: .subStreamRTSPURL) ?? ""
            username = try c.decodeIfPresent(String.self, forKey: .username) ?? ""
            isRecording = try c.decodeIfPresent(Bool.self, forKey: .isRecording) ?? false
            recordingMode = try c.decodeIfPresent(String.self, forKey: .recordingMode) ?? "continuous"
            retentionDays = try c.decodeIfPresent(Int.self, forKey: .retentionDays)
            onvifServiceURL = try c.decodeIfPresent(String.self, forKey: .onvifServiceURL)
            localDeviceID = try c.decodeIfPresent(String.self, forKey: .localDeviceID)
            ipAddress = try c.decodeIfPresent(String.self, forKey: .ipAddress) ?? ""
        }
    }

    public struct User: Decodable, Hashable {
        public var id: UUID
        public var name: String
        public var role: String
        public var status: String
        public var passwordHash: String?

        enum CodingKeys: String, CodingKey { case id, name, role, status, passwordHash }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
            name = try c.decode(String.self, forKey: .name)
            role = try c.decodeIfPresent(String.self, forKey: .role) ?? "Viewer"
            status = try c.decodeIfPresent(String.self, forKey: .status) ?? ""
            passwordHash = try c.decodeIfPresent(String.self, forKey: .passwordHash)
        }
    }

    public struct Rule: Decodable, Hashable {
        public var name: String
        public var trigger: String
        public var delivery: String
        public var severity: String
        public var isEnabled: Bool
        public var isSnoozed: Bool
        public var cameraIDs: [UUID]?
        public var activeFromHour: Int?
        public var activeToHour: Int?
        public var instructions: String?
    }

    public struct AuditEntry: Decodable, Hashable {
        public var time: Date
        public var user: String
        public var area: String
        public var action: String
        public var detail: String
    }

    public struct EvidenceClip: Decodable, Hashable {
        public var caseID: String
        public var title: String
        public var camera: String
        public var range: String
        public var status: String
        public var filePath: String?
        public var sha256Hash: String?
        public var exportedBy: String?
        public var lockedAt: Date?

        public var isLocked: Bool { status == "Locked" || lockedAt != nil }
    }

    private struct WorkflowState: Decodable {
        var notificationRules: [Rule]?
        var auditLog: [AuditEntry]?
    }

    private struct CaseworkState: Decodable {
        var evidenceClips: [EvidenceClip]?
    }

    private func read<T: Decodable>(_ name: String, as type: T.Type) -> T? {
        guard let data = try? Data(contentsOf: directory.appendingPathComponent(name)) else { return nil }
        return try? JSONDecoder().decode(T.self, from: data)
    }

    public func cameras() -> [Camera] { read("cameras.json", as: [Camera].self) ?? [] }
    public func users() -> [User] { read("users.json", as: [User].self) ?? [] }
    public func rules() -> [Rule] { read("workflow-state.json", as: WorkflowState.self)?.notificationRules ?? [] }
    public func audit() -> [AuditEntry] { read("workflow-state.json", as: WorkflowState.self)?.auditLog ?? [] }
    public func evidence() -> [EvidenceClip] { read("casework-state.json", as: CaseworkState.self)?.evidenceClips ?? [] }

    /// True when there's anything worth importing.
    public var hasData: Bool {
        cameras().contains { $0.localDeviceID == nil } || users().isEmpty == false || evidence().contains(where: \.isLocked)
    }
}

/// Builds the `POST /api/v2/import/mac` payload from a 1.x install.
public struct MacImportBuilder {
    /// One evidence file to upload after the import, by case id.
    public struct EvidenceFile: Hashable, Sendable {
        public var caseId: String
        public var url: URL
        public var size: Int64
    }

    public struct Output {
        public var payload: V2.MacImport
        public var files: [EvidenceFile]
        /// What the client left out and why (shown with the server's own list).
        public var skipped: [String]
    }

    public let install: LegacyMacInstall
    public let sourceVersion: String
    /// The camera password from the old app's secret store, if it can be read.
    public var password: (UUID) -> String?
    /// SHA-256 of a file (evidence without a stored hash).
    public var hash: (URL) -> String?

    public init(install: LegacyMacInstall, sourceVersion: String,
                password: @escaping (UUID) -> String? = { _ in nil },
                hash: @escaping (URL) -> String? = { try? EvidenceVault.sha256(of: $0) }) {
        self.install = install
        self.sourceVersion = sourceVersion
        self.password = password
        self.hash = hash
    }

    public func build() -> Output {
        var skipped: [String] = []
        let fm = FileManager.default

        // Cameras
        var cameras: [V2.MacImport.Camera] = []
        let legacyCameras = install.cameras()
        for camera in legacyCameras {
            if camera.localDeviceID?.isEmpty == false {
                skipped.append("\(camera.name): a camera built into this Mac can't be recorded by the server")
                continue
            }
            let sanitized = RTSPCredentialFormatter.sanitize(camera.rtspURL)
            guard RTSPCredentialFormatter.isUsableRTSPURL(sanitized.url) else {
                skipped.append("\(camera.name): no usable RTSP address")
                continue
            }
            let sub = RTSPCredentialFormatter.sanitize(camera.subStreamRTSPURL).url
            let user = sanitized.username.isEmpty ? camera.username : sanitized.username
            var secret = sanitized.password.isEmpty ? password(camera.id) : sanitized.password
            if secret?.isEmpty == true { secret = nil }
            if secret == nil, user.isEmpty == false {
                skipped.append("\(camera.name): password not available — enter it in Sentinel Admin")
            }
            let folder = install.recordingsRoot.appendingPathComponent(camera.id.uuidString, isDirectory: true)
            var isDirectory: ObjCBool = false
            let hasRecordings = fm.fileExists(atPath: folder.path, isDirectory: &isDirectory) && isDirectory.boolValue
            cameras.append(V2.MacImport.Camera(
                sourceId: camera.id.uuidString,
                name: camera.name,
                location: camera.location.isEmpty || camera.location == "Unassigned" ? nil : camera.location,
                rtspUrl: sanitized.url,
                subRtspUrl: sub.isEmpty ? nil : sub,
                username: user.isEmpty ? nil : user,
                password: secret,
                recording: camera.isRecording,
                retentionDays: camera.retentionDays.flatMap { $0 > 0 ? $0 : nil },
                motionLevel: nil,
                onvifUrl: camera.onvifServiceURL?.isEmpty == false ? camera.onvifServiceURL : nil,
                recordingsPath: hasRecordings ? folder.path : nil,
                recordingMode: camera.isRecording ? Self.recordingMode(legacy: camera.recordingMode, hasSubStream: sub.isEmpty == false) : nil
            ))
        }
        let sourceIDByName = Dictionary(legacyCameras.map { ($0.name, $0.id.uuidString) }, uniquingKeysWith: { first, _ in first })
        let importedIDs = Set(cameras.map(\.sourceId))

        // Users (the Mac app's password hashes are unsalted SHA-256, not bcrypt).
        var users: [V2.MacImport.User] = []
        var seen = Set<String>()
        for user in install.users() {
            let key = user.name.lowercased()
            guard seen.insert(key).inserted else { continue }
            let role = V2.Role.all.first { $0.rawValue.lowercased() == user.role.lowercased() } ?? .viewer
            users.append(V2.MacImport.User(name: user.name, role: role, passwordHash: nil))
        }
        if users.isEmpty == false {
            skipped.append("User passwords can't be carried over: an Admin sets a new password for each imported user")
        }

        // Rules
        let rules = install.rules().map { Self.rule(from: $0, importedCameraIDs: importedIDs) }

        // Evidence
        var evidence: [V2.MacImport.Evidence] = []
        var files: [EvidenceFile] = []
        var caseIDs = Set<String>()
        for clip in install.evidence() where clip.isLocked {
            guard let path = clip.filePath, fm.fileExists(atPath: path) else {
                skipped.append("Evidence \(clip.caseID) (\(clip.title)): the file is missing")
                continue
            }
            let url = URL(fileURLWithPath: path)
            let size = (try? fm.attributesOfItem(atPath: path)[.size] as? NSNumber)?.int64Value ?? 0
            guard let sha = clip.sha256Hash?.lowercased() ?? hash(url) else {
                skipped.append("Evidence \(clip.caseID): the file can't be read")
                continue
            }
            // One file per case id on the server: number further clips of a case.
            var caseID = clip.caseID.isEmpty ? "MAC-\(sha.prefix(8))" : clip.caseID
            var n = 2
            while caseIDs.contains(caseID) {
                caseID = "\(clip.caseID)-\(n)"
                n += 1
            }
            caseIDs.insert(caseID)
            evidence.append(V2.MacImport.Evidence(
                caseId: caseID,
                cameraSourceId: sourceIDByName[clip.camera],
                cameraName: clip.camera,
                title: clip.title,
                range: clip.range,
                size: size,
                sha256: sha,
                lockedAt: (clip.lockedAt ?? Date()).unixMillis,
                lockedBy: clip.exportedBy ?? ""
            ))
            files.append(EvidenceFile(caseId: caseID, url: url, size: size))
        }

        // Audit, oldest first (the old app keeps it newest first).
        let audit = install.audit()
            .sorted { $0.time < $1.time }
            .map { V2.MacImport.AuditEntry(at: $0.time.unixMillis, user: $0.user, area: $0.area, action: $0.action, detail: $0.detail) }

        let payload = V2.MacImport(sourceVersion: sourceVersion, cameras: cameras, users: users, rules: rules,
                                   evidence: evidence, audit: audit)
        return Output(payload: payload, files: files, skipped: skipped)
    }

    /// A 1.x notification rule as a v2 rule. Camera ids are the Mac source ids
    /// (the server maps them with the import's `cameraIds`).
    /// The server's recording mode for the 1.x app's (wave 22): motion
    /// recording becomes events only; dual-stream becomes low-res 24/7 plus
    /// full-res on events, or events only without a sub-stream.
    static func recordingMode(legacy: String, hasSubStream: Bool) -> String {
        switch legacy {
        case "motion": return "events"
        case "dualStream": return hasSubStream ? "dual" : "events"
        default: return "continuous"
        }
    }

    static func rule(from rule: LegacyMacInstall.Rule, importedCameraIDs: Set<String>) -> V2.RuleInput {
        let severities = ["Info", "Warning", "Critical"]
        let trigger = rule.trigger.trimmingCharacters(in: .whitespaces)
        var kinds: [String] = []
        var minSeverity = severities.first { $0.caseInsensitiveCompare(rule.severity) == .orderedSame } ?? "Info"
        if let severity = severities.first(where: { $0.caseInsensitiveCompare(trigger) == .orderedSame }) {
            minSeverity = severity
        } else if trigger.isEmpty == false, trigger.caseInsensitiveCompare("Any alarm") != .orderedSame {
            kinds = [trigger]
        }
        let cameraIDs = (rule.cameraIDs ?? []).map(\.uuidString).filter { importedCameraIDs.contains($0) }
        var input = V2.RuleInput(name: rule.name, enabled: rule.isEnabled && rule.isSnoozed == false,
                                 cameraIds: cameraIDs, kinds: kinds, minSeverity: minSeverity,
                                 delivery: rule.delivery, instructions: rule.instructions ?? "")
        if let from = rule.activeFromHour, let to = rule.activeToHour, from != to {
            input.schedule = true
            input.fromMinute = min(max(from, 0), 23) * 60
            input.toMinute = to >= 24 ? 1439 : max(to, 0) * 60
            input.days = Array(0...6)
        }
        return input
    }
}
