@preconcurrency import Vision
import CoreGraphics
import Foundation
import ImageIO

public enum AIDetectionKind: String, Codable, CaseIterable, Identifiable, Hashable, Sendable {
    case person = "Person"
    case face = "Face"
    case licensePlate = "License Plate"
    case vehicle = "Vehicle"
    case animal = "Animal"
    case loitering = "Loitering"

    public var id: String { rawValue }

    public var symbol: String {
        switch self {
        case .person: return "person.crop.rectangle.badge.plus"
        case .face: return "person.fill.viewfinder"
        case .licensePlate: return "doc.text.magnifyingglass"
        case .vehicle: return "car.fill"
        case .animal: return "pawprint.fill"
        case .loitering: return "clock.badge.exclamationmark"
        }
    }

    public var overlayColor: (Double, Double, Double) {
        switch self {
        case .person: return (1.0, 0.18, 0.18)
        case .face: return (1.0, 0.55, 0.10)
        case .licensePlate: return (0.98, 0.88, 0.10)
        case .vehicle: return (0.20, 0.60, 1.0)
        case .animal: return (0.20, 0.80, 0.35)
        case .loitering: return (0.80, 0.15, 0.80)
        }
    }

    public var cooldown: TimeInterval {
        switch self {
        case .person: return 5
        case .face: return 8
        case .licensePlate: return 10
        case .vehicle: return 15
        case .animal: return 15
        case .loitering: return 120
        }
    }

    public var defaultsKey: String { "handoffgrid.detection.\(rawValue.lowercased().replacingOccurrences(of: " ", with: "_"))" }
}

/// How routine vs. suspicious Claude judged an event to be. Drives the timeline
/// badge color and whether an alert is pushed with urgency.
public enum ThreatLevel: String, Codable, Sendable, CaseIterable, Comparable {
    case none, low, elevated, high

    private var rank: Int {
        switch self {
        case .none: return 0
        case .low: return 1
        case .elevated: return 2
        case .high: return 3
        }
    }
    public static func < (lhs: ThreatLevel, rhs: ThreatLevel) -> Bool { lhs.rank < rhs.rank }

    /// Only elevated/high are worth flagging in the UI; none/low are routine.
    public var isNotable: Bool { self >= .elevated }
}

public struct AIDetectionEvent: Codable, Identifiable, Hashable, Sendable {
    public let id: UUID
    public let cameraID: UUID
    public let timestamp: Date
    public let kind: AIDetectionKind
    public let confidence: Double
    public let boundingBox: CGRect
    public let imageSize: CGSize?
    public let framePath: String?
    public let detectedText: String?
    /// Ring-style natural-language description from Claude vision, filled in
    /// asynchronously after the event is recorded (nil until it arrives).
    public var sceneDescription: String?
    /// Claude's routine-vs-suspicious read of the event (defaults to `.none`
    /// until/unless the AI analysis returns a higher level).
    public var threat: ThreatLevel = .none
    /// True when Claude flagged the event as unusual / worth attention.
    public var isAnomaly: Bool = false
    /// Short reason behind the threat level (e.g. "loitering near entrance").
    public var threatReason: String?
    /// Short attribute tags Claude returned (e.g. person, vehicle, package, night).
    public var tags: [String] = []

    public init(
        id: UUID = UUID(),
        cameraID: UUID,
        timestamp: Date = Date(),
        kind: AIDetectionKind,
        confidence: Double,
        boundingBox: CGRect,
        imageSize: CGSize? = nil,
        framePath: String?,
        detectedText: String? = nil,
        sceneDescription: String? = nil,
        threat: ThreatLevel = .none,
        isAnomaly: Bool = false,
        threatReason: String? = nil,
        tags: [String] = []
    ) {
        self.id = id
        self.cameraID = cameraID
        self.timestamp = timestamp
        self.kind = kind
        self.confidence = confidence
        self.boundingBox = boundingBox
        self.imageSize = imageSize
        self.framePath = framePath
        self.detectedText = detectedText
        self.sceneDescription = sceneDescription
        self.threat = threat
        self.isAnomaly = isAnomaly
        self.threatReason = threatReason
        self.tags = tags
    }

    public var confidenceLabel: String {
        "\(Int((confidence * 100).rounded()))%"
    }

    public var timeLabel: String {
        RecordingFormatters.timeFormatter.string(from: timestamp)
    }

    public var overlayLabel: String {
        switch kind {
        case .person: return "PERSON \(confidenceLabel)"
        case .face: return "FACE \(confidenceLabel)"
        case .licensePlate: return detectedText ?? "PLATE"
        case .vehicle: return "VEHICLE \(confidenceLabel)"
        case .animal: return detectedText.map { "ANIMAL: \($0)" } ?? "ANIMAL"
        case .loitering: return "LOITERING \(detectedText.map { "\($0)s" } ?? "")"
        }
    }

    public enum CodingKeys: String, CodingKey {
        case id, cameraID, timestamp, kind, confidence, boundingBox, imageSize, framePath, detectedText
        case sceneDescription, threat, isAnomaly, threatReason, tags
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
        cameraID = try c.decode(UUID.self, forKey: .cameraID)
        timestamp = try c.decodeIfPresent(Date.self, forKey: .timestamp) ?? Date()
        kind = try c.decodeIfPresent(AIDetectionKind.self, forKey: .kind) ?? .person
        confidence = try c.decodeIfPresent(Double.self, forKey: .confidence) ?? 0
        boundingBox = try c.decodeIfPresent(CGRect.self, forKey: .boundingBox) ?? .zero
        imageSize = try c.decodeIfPresent(CGSize.self, forKey: .imageSize)
        framePath = try c.decodeIfPresent(String.self, forKey: .framePath)
        detectedText = try c.decodeIfPresent(String.self, forKey: .detectedText)
        sceneDescription = try c.decodeIfPresent(String.self, forKey: .sceneDescription)
        threat = try c.decodeIfPresent(ThreatLevel.self, forKey: .threat) ?? .none
        isAnomaly = try c.decodeIfPresent(Bool.self, forKey: .isAnomaly) ?? false
        threatReason = try c.decodeIfPresent(String.self, forKey: .threatReason)
        tags = try c.decodeIfPresent([String].self, forKey: .tags) ?? []
    }
}

/// Structured result of one Claude vision analysis (`sentinel-describe`).
public struct SceneAnalysis: Sendable {
    public let description: String
    public let threat: ThreatLevel
    public let isAnomaly: Bool
    public let reason: String
    public let tags: [String]
    public init(description: String, threat: ThreatLevel = .none,
                isAnomaly: Bool = false, reason: String = "", tags: [String] = []) {
        self.description = description
        self.threat = threat
        self.isAnomaly = isAnomaly
        self.reason = reason
        self.tags = tags
    }
}

@MainActor
public final class AIDetectionStore: ObservableObject {
    @Published public private(set) var detections: [UUID: [AIDetectionEvent]] = [:]
    @Published public private(set) var activeCameraIDs: Set<UUID> = []
    @Published public private(set) var lastMessage: String?

    /// Per-camera CURRENT-frame person boxes for live tracking overlays. Unlike
    /// `detections` (a throttled event log with a 6s display window), this is
    /// replaced every frame for whichever camera is being actively watched, so
    /// the on-screen box follows the subject smoothly. Only the focused/fullscreen
    /// camera is populated (see setTrackingCamera) to bound CPU on a camera wall.
    @Published public private(set) var liveTracks: [UUID: [AIDetectionEvent]] = [:]

    private var detectorTasks: [UUID: Task<Void, Never>] = [:]
    private var videoDecoders: [UUID: VideoStreamDecoder] = [:]
    private var liveStreamByID: [UUID: LocalLiveStream] = [:]
    private var trackingCameraID: UUID?
    private var trackingTask: Task<Void, Never>?
    /// ~6 fps — fast enough to read as live tracking, light enough for one camera.
    private let trackingIntervalNanoseconds: UInt64 = 160_000_000
    private var onvifPullTasks: [UUID: Task<Void, Never>] = [:]
    private var lastDetectionAt: [String: Date] = [:]
    private var firstPersonSeenAt: [UUID: Date] = [:]
    private var loiteringAlertedAt: [UUID: Date] = [:]
    private var lastDescribeAt: [UUID: Date] = [:]
    private let detectionsURL: URL
    private let scanIntervalNanoseconds: UInt64 = 1_500_000_000
    private let minimumPersonConfidence = 0.65
    private let minimumFaceConfidence = 0.55
    private let minimumVehicleConfidence: Float = 0.75
    private let loiteringThresholdSeconds: TimeInterval = 45

    /// Minimum gap between Claude-vision descriptions per camera, so one
    /// person-visit produces one rich alert instead of one per scanned frame.
    private let describeCooldown: TimeInterval = 20

    /// Injected at the app layer (wraps LicenseStore.describeScene). When set
    /// and enabled, a person detection triggers a one-line scene description.
    /// nil/disabled = feature off; detection still works exactly as before.
    public var sceneDescriber: ((Data) async -> SceneAnalysis?)?
    public var describeEnabled = false

    /// Fired when a fresh Ring-style scene description is attached to a person
    /// sighting — i.e. the moment a push-worthy "AI alert" exists. Wired at the
    /// app layer to APNsPushService. Carries the camera and the description.
    public var onSceneDescription: ((_ cameraID: UUID, _ description: String) -> Void)?

    public init() {
        let supportDirectory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
        detectionsURL = supportDirectory.appendingPathComponent("ai-detections.json")
        loadDetections()
    }

    public var isRunning: Bool { detectorTasks.isEmpty == false }

    public func detections(for cameraID: UUID) -> [AIDetectionEvent] {
        (detections[cameraID] ?? []).sorted { $0.timestamp < $1.timestamp }
    }

    /// All recent detection events across every camera, newest first. Powers
    /// the iOS event feed.
    public func recentEventsAcrossCameras(limit: Int = 200) -> [AIDetectionEvent] {
        detections.values
            .flatMap { $0 }
            .sorted { $0.timestamp > $1.timestamp }
            .prefix(limit)
            .map { $0 }
    }

    /// Looks up a single event by ID (for serving its frame thumbnail).
    public func event(withID id: UUID) -> AIDetectionEvent? {
        for events in detections.values {
            if let match = events.first(where: { $0.id == id }) { return match }
        }
        return nil
    }

    public func recentDetections(for cameraID: UUID, within seconds: TimeInterval = 6) -> [AIDetectionEvent] {
        let cutoff = Date().addingTimeInterval(-seconds)
        return detections(for: cameraID).filter { $0.timestamp >= cutoff }
    }

    public func sync(liveStreams: [LocalLiveStream], cameras: [CameraFeed], mediaIngestStore: MediaIngestProviding) {
        let streamCameraIDs = Set(liveStreams.map(\.cameraID))

        // Keep a lookup so the live-tracking loop can find a camera's frame
        // source (HLS decoder or JPEG dir) when the operator focuses it.
        // uniquingKeysWith (not uniqueKeysWithValues) — the latter TRAPS on a
        // duplicate cameraID, which would crash the app.
        liveStreamByID = Dictionary(liveStreams.map { ($0.cameraID, $0) }, uniquingKeysWith: { first, _ in first })

        for stream in liveStreams {
            guard detectorTasks[stream.cameraID] == nil else { continue }
            startDetectorLoop(for: stream, mediaIngestStore: mediaIngestStore)
        }

        for cameraID in Set(detectorTasks.keys).subtracting(streamCameraIDs) {
            stopDetector(for: cameraID)
        }

        // Start ONVIF PullPoint subscriptions for cameras with ONVIF service URLs
        let onvifCameras = cameras.filter {
            $0.onvifServiceURL != nil && $0.rtspURL.isEmpty == false
        }
        for camera in onvifCameras where onvifPullTasks[camera.id] == nil {
            startONVIFPullLoop(for: camera, mediaIngestStore: mediaIngestStore)
        }
        let onvifIDs = Set(onvifCameras.map(\.id))
        for cameraID in Set(onvifPullTasks.keys).subtracting(onvifIDs) {
            onvifPullTasks[cameraID]?.cancel()
            onvifPullTasks[cameraID] = nil
        }

        activeCameraIDs = Set(detectorTasks.keys).intersection(Set(cameras.map(\.id)))

        // Startup race: setTrackingCamera may have been called before this
        // camera's live stream was registered. Now that liveStreamByID is
        // populated, start the tracking loop if it's still pending.
        if let tc = trackingCameraID, trackingTask == nil, let stream = liveStreamByID[tc] {
            startTrackingLoop(for: stream)
        }
    }

    public func stopAll() {
        for cameraID in Array(detectorTasks.keys) { stopDetector(for: cameraID) }
        for cameraID in Array(onvifPullTasks.keys) {
            onvifPullTasks[cameraID]?.cancel()
            onvifPullTasks[cameraID] = nil
        }
        trackingTask?.cancel()
        trackingTask = nil
        trackingCameraID = nil
        liveTracks = [:]
        activeCameraIDs = []
    }

    // MARK: - Live person tracking (focused tile only)

    /// What the live overlay should draw for a tile: the high-frame-rate person
    /// tracks when the tracker currently has any, otherwise the recent event
    /// log. Crucially, an EMPTY live-track array (a single frame where the fast
    /// loop happened to detect no one) must NOT blank a tile that still has a
    /// recent detection — that was making the focused tile flicker to nothing.
    public func overlayDetections(for cameraID: UUID) -> [AIDetectionEvent] {
        if let tracks = liveTracks[cameraID], tracks.isEmpty == false {
            return tracks
        }
        return recentDetections(for: cameraID)
    }

    /// Point the high-frame-rate person tracker at a single camera (the one the
    /// operator is watching — selected or fullscreen), or nil to stop tracking.
    /// Only one camera tracks at a time so CPU stays flat regardless of how many
    /// tiles are on screen.
    public func setTrackingCamera(_ cameraID: UUID?) {
        guard trackingCameraID != cameraID else { return }
        if let old = trackingCameraID {
            // Drop the key entirely so the old tile falls back to the event log
            // overlay instead of being stuck showing an empty live-track array.
            liveTracks.removeValue(forKey: old)
        }
        trackingTask?.cancel()
        trackingTask = nil
        trackingCameraID = cameraID

        guard let cameraID else { return }
        // If the stream isn't registered yet (startup race), sync() will start
        // the loop once the live stream appears.
        guard let stream = liveStreamByID[cameraID] else { return }
        startTrackingLoop(for: stream)
    }

    private func startTrackingLoop(for stream: LocalLiveStream) {
        let cameraID = stream.cameraID
        // Reuse the detector's HLS decoder if it already exists; otherwise fall
        // back to the live bridge's JPEG dump (always present for live cameras).
        let decoder = videoDecoders[cameraID]
        let frameDir = stream.frameDirectoryURL
        let minConf = Float(minimumPersonConfidence)
        let interval = trackingIntervalNanoseconds

        trackingTask = Task.detached(priority: .userInitiated) { [weak self] in
            var lastFrameURL: URL?
            var previous: [AIDetectionEvent] = []

            while Task.isCancelled == false {
                var cgImage: CGImage?
                if let decoder, let pb = decoder.latestPixelBuffer() {
                    cgImage = VideoFrameConverter.makeCGImage(from: pb)
                }
                // Fall back to the live-bridge JPEG dump whenever the decoder
                // yields no frame (nil decoder OR a decoder that hasn't buffered
                // a frame yet) — otherwise the loop produces nothing and no boxes
                // are ever published.
                if cgImage == nil,
                   let url = Self.latestFrameURL(in: frameDir),
                   url != lastFrameURL,
                   let img = Self.loadCGImage(from: url) {
                    cgImage = img
                    lastFrameURL = url
                }

                if let cgImage {
                    let imageSize = CGSize(width: cgImage.width, height: cgImage.height)
                    let persons = await LocalAIPersonDetector.detect(in: cgImage)
                        .filter { $0.confidence >= minConf }
                    // Carry stable IDs across frames (IoU match) so SwiftUI
                    // animates each box gliding to its new position rather than
                    // popping in/out.
                    let tracks = Self.associatePersons(
                        candidates: persons,
                        cameraID: cameraID,
                        imageSize: imageSize,
                        previous: previous
                    )
                    previous = tracks
                    await MainActor.run { [weak self] in
                        guard let self, self.trackingCameraID == cameraID else { return }
                        self.liveTracks[cameraID] = tracks
                    }
                }

                try? await Task.sleep(nanoseconds: interval)
            }
        }
    }

    /// Match this frame's person boxes to the previous frame's by IoU so a box
    /// keeps its identity (and the overlay animates it) across frames.
    private nonisolated static func associatePersons(
        candidates: [Candidate],
        cameraID: UUID,
        imageSize: CGSize,
        previous: [AIDetectionEvent]
    ) -> [AIDetectionEvent] {
        var result: [AIDetectionEvent] = []
        var usedPrevious = Set<Int>()

        for candidate in candidates {
            var bestIndex: Int?
            var bestIoU: CGFloat = 0.3   // minimum overlap to be "the same person"
            for (i, prior) in previous.enumerated() where usedPrevious.contains(i) == false {
                let overlap = iou(candidate.boundingBox, prior.boundingBox)
                if overlap > bestIoU {
                    bestIoU = overlap
                    bestIndex = i
                }
            }

            let id = bestIndex.map { previous[$0].id } ?? UUID()
            if let bestIndex { usedPrevious.insert(bestIndex) }
            result.append(
                AIDetectionEvent(
                    id: id,
                    cameraID: cameraID,
                    kind: .person,
                    confidence: Double(candidate.confidence),
                    boundingBox: candidate.boundingBox,
                    imageSize: imageSize,
                    framePath: nil
                )
            )
        }
        return result
    }

    private nonisolated static func iou(_ a: CGRect, _ b: CGRect) -> CGFloat {
        let intersection = a.intersection(b)
        guard intersection.isNull == false else { return 0 }
        let interArea = intersection.width * intersection.height
        let unionArea = a.width * a.height + b.width * b.height - interArea
        return unionArea > 0 ? interArea / unionArea : 0
    }

    private func startONVIFPullLoop(for camera: CameraFeed, mediaIngestStore: MediaIngestProviding) {
        guard let urlStr = camera.onvifServiceURL,
              let serviceURL = URL(string: urlStr) else { return }
        let sanitized = RTSPCredentialFormatter.sanitize(camera.rtspURL)
        let credentials = ONVIFCredentials(
            username: camera.username.isEmpty ? sanitized.username : camera.username,
            password: sanitized.password.isEmpty ? ((try? CameraSecrets.password(for: camera.id)) ?? "") : sanitized.password
        )

        onvifPullTasks[camera.id] = Task(priority: .utility) { [weak self, weak mediaIngestStore] in
            var subscriptionURL: URL? = nil
            var subscriptionStarted = Date.distantPast

            while Task.isCancelled == false {
                // Subscribe or re-subscribe every ~50 seconds (before the 60s TTL expires)
                if subscriptionURL == nil || Date().timeIntervalSince(subscriptionStarted) > 50 {
                    subscriptionURL = try? await ONVIFSOAPClient.createPullPointSubscription(
                        deviceServiceURL: serviceURL, credentials: credentials)
                    subscriptionStarted = Date()
                }

                if let subURL = subscriptionURL {
                    let hasMotion = (try? await ONVIFSOAPClient.pullMessages(
                        subscriptionURL: subURL, credentials: credentials)) ?? false
                    if hasMotion {
                        let timestamp = Date()
                        await MainActor.run {
                            mediaIngestStore?.registerMotion(
                                cameraID: camera.id, intensity: 0.8, timestamp: timestamp)
                            self?.lastMessage = "\(camera.name): camera-native motion (ONVIF)"
                        }
                    }
                }

                try? await Task.sleep(nanoseconds: 5_000_000_000)
            }
        }
    }

    private func startDetectorLoop(for stream: LocalLiveStream, mediaIngestStore: MediaIngestProviding) {
        // Detection task: scan interval 1.5s, max ~0.67 detections/sec per camera
        let scanInterval = scanIntervalNanoseconds
        let minPersonConf = minimumPersonConfidence
        let minFaceConf = minimumFaceConfidence
        let minVehicleConf = minimumVehicleConfidence
        let loiteringThreshold = loiteringThresholdSeconds

        // Phase 2: when MediaMTX is proxying, pull frames directly from its
        // HLS feed via VideoToolbox/AVPlayer rather than polling the JPEG
        // dump. This is the same pipeline that will work on iOS.
        let decoder: VideoStreamDecoder? = stream.hlsURL.flatMap { url in
            // VideoStreamDecoder init is @MainActor; we're on it here.
            VideoStreamDecoder(url: url)
        }
        if let decoder { videoDecoders[stream.cameraID] = decoder }

        // Detached so the decode + Vision work runs OFF the main actor. As a
        // main-isolated `Task` it was starved (never scheduled) whenever the
        // main actor was busy, silently stopping AI detection. Mutations already
        // hop back via `await MainActor.run { … }`.
        detectorTasks[stream.cameraID] = Task.detached(priority: .utility) { [weak self, weak mediaIngestStore] in
            var lastFrameURL: URL?

            while Task.isCancelled == false {
                // Prefer pixel-buffer path (no disk I/O, no JPEG encode hop).
                var cgImage: CGImage?
                var framePath: String?

                if let decoder, let pb = decoder.latestPixelBuffer() {
                    cgImage = VideoFrameConverter.makeCGImage(from: pb)
                    framePath = nil   // no on-disk path; detections won't link to a thumbnail file
                } else if decoder == nil,
                          let frameURL = Self.latestFrameURL(in: stream.frameDirectoryURL),
                          frameURL != lastFrameURL,
                          let img = Self.loadCGImage(from: frameURL) {
                    cgImage = img
                    framePath = frameURL.path
                    lastFrameURL = frameURL
                }

                if let cgImage {
                    let imageSize = CGSize(width: cgImage.width, height: cgImage.height)
                    let timestamp = Date()

                    async let personTask = Self.kindEnabled(.person) ? LocalAIPersonDetector.detect(in: cgImage) : []
                    async let faceTask = Self.kindEnabled(.face) ? LocalAIFaceDetector.detect(in: cgImage) : []
                    async let plateTask = Self.kindEnabled(.licensePlate) ? LocalAITextRecognizer.detectPlates(in: cgImage) : []
                    async let vehicleTask = Self.kindEnabled(.vehicle) ? LocalAIVehicleDetector.detect(in: cgImage) : []
                    async let animalTask = Self.kindEnabled(.animal) ? LocalAIAnimalDetector.detect(in: cgImage) : []

                    let (persons, faces, plates, vehicles, animals) =
                        await (personTask, faceTask, plateTask, vehicleTask, animalTask)

                    let personsDetected = persons.filter { $0.confidence >= Float(minPersonConf) }

                    let describeTarget: UUID? = await MainActor.run { () -> UUID? in
                        guard let self else { return nil }
                        var firstPersonID: UUID?
                        for p in personsDetected {
                            let id = self.record(cameraID: stream.cameraID, timestamp: timestamp, kind: .person,
                                         confidence: Double(p.confidence), boundingBox: p.boundingBox,
                                         imageSize: imageSize, framePath: framePath, detectedText: nil)
                            if firstPersonID == nil { firstPersonID = id }
                            mediaIngestStore?.registerPersonDetection(cameraID: stream.cameraID,
                                                                       confidence: Double(p.confidence),
                                                                       timestamp: timestamp)
                        }

                        self.updateLoiteringTracker(
                            cameraID: stream.cameraID,
                            personDetected: personsDetected.isEmpty == false,
                            timestamp: timestamp,
                            imageSize: imageSize,
                            framePath: framePath,
                            threshold: loiteringThreshold
                        )

                        if let f = faces.filter({ $0.confidence >= Float(minFaceConf) })
                            .max(by: { $0.confidence < $1.confidence }) {
                            self.record(cameraID: stream.cameraID, timestamp: timestamp, kind: .face,
                                         confidence: Double(f.confidence), boundingBox: f.boundingBox,
                                         imageSize: imageSize, framePath: framePath, detectedText: nil)
                        }

                        for plate in plates {
                            self.record(cameraID: stream.cameraID, timestamp: timestamp, kind: .licensePlate,
                                         confidence: Double(plate.confidence), boundingBox: plate.boundingBox,
                                         imageSize: imageSize, framePath: framePath, detectedText: plate.detectedText)
                        }

                        if let v = vehicles.filter({ $0.confidence >= minVehicleConf })
                            .max(by: { $0.confidence < $1.confidence }) {
                            self.record(cameraID: stream.cameraID, timestamp: timestamp, kind: .vehicle,
                                         confidence: Double(v.confidence), boundingBox: v.boundingBox,
                                         imageSize: imageSize, framePath: framePath, detectedText: v.detectedText)
                        }

                        if let a = animals.max(by: { $0.confidence < $1.confidence }) {
                            self.record(cameraID: stream.cameraID, timestamp: timestamp, kind: .animal,
                                         confidence: Double(a.confidence), boundingBox: a.boundingBox,
                                         imageSize: imageSize, framePath: framePath, detectedText: a.detectedText)
                        }

                        // Decide whether this person sighting earns a Claude
                        // description (feature on, describer wired, cooldown OK).
                        guard self.describeEnabled, self.sceneDescriber != nil,
                              let personID = firstPersonID else { return nil }
                        if let last = self.lastDescribeAt[stream.cameraID],
                           timestamp.timeIntervalSince(last) < self.describeCooldown {
                            return nil
                        }
                        self.lastDescribeAt[stream.cameraID] = timestamp
                        return personID
                    }

                    // Describe off the main actor: JPEG-encode here, then hop to
                    // main only for the (suspending) network call + attach.
                    if let describeTarget,
                       let jpeg = Self.jpegData(from: cgImage),
                       let analysis = await self?.runSceneDescriber(jpeg),
                       analysis.description.isEmpty == false,
                       analysis.description != "No person clearly visible." {
                        await MainActor.run {
                            self?.attachAnalysis(analysis, toEventID: describeTarget, cameraID: stream.cameraID)
                        }
                    }
                }

                try? await Task.sleep(nanoseconds: scanInterval)
            }
        }
    }

    private func stopDetector(for cameraID: UUID) {
        detectorTasks[cameraID]?.cancel()
        detectorTasks[cameraID] = nil
        videoDecoders[cameraID]?.stop()
        videoDecoders[cameraID] = nil
        firstPersonSeenAt[cameraID] = nil
    }

    private func updateLoiteringTracker(
        cameraID: UUID,
        personDetected: Bool,
        timestamp: Date,
        imageSize: CGSize,
        framePath: String?,
        threshold: TimeInterval
    ) {
        guard Self.kindEnabled(.loitering) else { return }

        if personDetected {
            let firstSeen = firstPersonSeenAt[cameraID] ?? timestamp
            firstPersonSeenAt[cameraID] = firstSeen
            let dwell = timestamp.timeIntervalSince(firstSeen)

            if dwell >= threshold {
                let lastAlert = loiteringAlertedAt[cameraID] ?? .distantPast
                if timestamp.timeIntervalSince(lastAlert) >= AIDetectionKind.loitering.cooldown {
                    loiteringAlertedAt[cameraID] = timestamp
                    record(cameraID: cameraID, timestamp: timestamp, kind: .loitering,
                           confidence: 1.0, boundingBox: .zero, imageSize: imageSize,
                           framePath: framePath, detectedText: "\(Int(dwell))")
                }
            }
        } else {
            firstPersonSeenAt[cameraID] = nil
        }
    }

    @discardableResult
    private func record(
        cameraID: UUID,
        timestamp: Date,
        kind: AIDetectionKind,
        confidence: Double,
        boundingBox: CGRect,
        imageSize: CGSize?,
        framePath: String?,
        detectedText: String?
    ) -> UUID? {
        let key = "\(cameraID.uuidString)-\(kind.rawValue)"
        if let last = lastDetectionAt[key], timestamp.timeIntervalSince(last) < kind.cooldown {
            return nil
        }

        lastDetectionAt[key] = timestamp
        var events = detections[cameraID] ?? []
        let event = AIDetectionEvent(
            cameraID: cameraID,
            timestamp: timestamp,
            kind: kind,
            confidence: confidence,
            boundingBox: boundingBox,
            imageSize: imageSize,
            framePath: framePath,
            detectedText: detectedText
        )
        events.append(event)
        if events.count > 1_000 {
            events.removeFirst(events.count - 1_000)
        }
        detections[cameraID] = events

        let label: String
        switch kind {
        case .licensePlate: label = "License plate detected: \(detectedText ?? "unknown")"
        case .loitering: label = "Loitering alert — \(detectedText ?? "")s in frame"
        default: label = "\(kind.rawValue) detected (\(Int(confidence * 100))%)"
        }
        lastMessage = label
        saveDetections()
        return event.id
    }

    /// Runs the injected scene describer (network call). MainActor-isolated so
    /// the describer closure can safely capture main-actor state; the await
    /// suspends without blocking the UI.
    func runSceneDescriber(_ jpeg: Data) async -> SceneAnalysis? {
        guard let describer = sceneDescriber else { return nil }
        return await describer(jpeg)
    }

    /// Downscales a frame and JPEG-encodes it for the vision request. Small
    /// images keep the API fast and cheap; 768px is ample for clothing/objects.
    nonisolated static func jpegData(from cgImage: CGImage, maxDimension: CGFloat = 768, quality: CGFloat = 0.6) -> Data? {
        let w = CGFloat(cgImage.width), h = CGFloat(cgImage.height)
        guard w > 0, h > 0 else { return nil }
        let scale = min(1, maxDimension / max(w, h))
        let tw = max(1, Int(w * scale)), th = max(1, Int(h * scale))
        guard let ctx = CGContext(
            data: nil, width: tw, height: th, bitsPerComponent: 8, bytesPerRow: 0,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue
        ) else { return nil }
        ctx.interpolationQuality = .medium
        ctx.draw(cgImage, in: CGRect(x: 0, y: 0, width: tw, height: th))
        guard let scaled = ctx.makeImage() else { return nil }
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, "public.jpeg" as CFString, 1, nil) else { return nil }
        CGImageDestinationAddImage(dest, scaled, [kCGImageDestinationLossyCompressionQuality: quality] as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { return nil }
        return out as Data
    }

    /// Attaches a Claude-vision analysis (description + threat read) to a
    /// previously recorded event and republishes so the Alerts UI updates.
    /// No-op if the event aged out.
    public func attachAnalysis(_ analysis: SceneAnalysis, toEventID id: UUID, cameraID: UUID) {
        guard var events = detections[cameraID],
              let idx = events.firstIndex(where: { $0.id == id }) else { return }
        events[idx].sceneDescription = analysis.description
        events[idx].threat = analysis.threat
        events[idx].isAnomaly = analysis.isAnomaly
        events[idx].threatReason = analysis.reason.isEmpty ? nil : analysis.reason
        events[idx].tags = analysis.tags
        detections[cameraID] = events
        lastMessage = analysis.description
        saveDetections()
        onSceneDescription?(cameraID, analysis.description)
    }

    private func loadDetections() {
        guard FileManager.default.fileExists(atPath: detectionsURL.path),
              let data = try? Data(contentsOf: detectionsURL) else { return }
        detections = (try? JSONDecoder().decode([UUID: [AIDetectionEvent]].self, from: data)) ?? [:]
    }

    private var saveTask: Task<Void, Never>?

    /// Coalesced, off-main persistence. Detections fire many times/second across
    /// cameras; encoding the whole dictionary and writing it synchronously on the
    /// main actor per event hitched the UI. Debounce ~2s, then do the (expensive)
    /// encode + atomic write off the main actor. A snapshot is taken on the main
    /// actor so the background write sees a consistent copy.
    private func saveDetections() {
        saveTask?.cancel()
        let url = detectionsURL
        saveTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            guard Task.isCancelled == false, let snapshot = self?.detections else { return }
            await Self.persistDetections(snapshot, to: url)
        }
    }

    private nonisolated static func persistDetections(_ detections: [UUID: [AIDetectionEvent]], to url: URL) async {
        await Task.detached(priority: .utility) {
            guard let data = try? JSONEncoder.prettySentinel.encode(detections) else { return }
            try? createDirectoryIfNeeded(url.deletingLastPathComponent())
            try? data.write(to: url, options: .atomic)
        }.value
    }

    private nonisolated static func kindEnabled(_ kind: AIDetectionKind) -> Bool {
        let defaults = UserDefaults.standard
        guard defaults.object(forKey: kind.defaultsKey) != nil else { return true }
        return defaults.bool(forKey: kind.defaultsKey)
    }

    private nonisolated static func latestFrameURL(in directoryURL: URL) -> URL? {
        guard let files = try? FileManager.default.contentsOfDirectory(
            at: directoryURL,
            includingPropertiesForKeys: [.contentModificationDateKey, .fileSizeKey]
        ) else { return nil }

        return files
            .filter { $0.pathExtension.lowercased() == "jpg" }
            .compactMap { url -> (URL, Date)? in
                let values = try? url.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
                guard let modified = values?.contentModificationDate,
                      (values?.fileSize ?? 0) > 0 else { return nil }
                return (url, modified)
            }
            .max { $0.1 < $1.1 }?.0
    }

    private nonisolated static func loadCGImage(from url: URL) -> CGImage? {
        guard let source = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(source, 0, nil)
    }
}

// MARK: - Detection candidates

private struct Candidate: Sendable {
    public let confidence: Float
    public let boundingBox: CGRect
    public let detectedText: String?

    public init(_ confidence: Float, _ boundingBox: CGRect, _ text: String? = nil) {
        self.confidence = confidence
        self.boundingBox = boundingBox
        self.detectedText = text
    }
}

// MARK: - Vision execution off the cooperative pool

/// Runs blocking Vision work (`VNImageRequestHandler.perform`) on a dedicated
/// GCD queue instead of `Task.detached`, which uses the shared Swift cooperative
/// thread pool. With 5 detectors x N cameras (plus the live tracking loop), the
/// synchronous `perform` calls were saturating that pool — starving the motion
/// monitor's detached task so it never ran (no motion events at all). A separate
/// GCD queue keeps Vision off the cooperative pool; CPU-bound work on a
/// `.userInitiated` concurrent queue stays roughly core-bounded.
private enum VisionRunner {
    static let queue = DispatchQueue(
        label: "com.handoffgrid.sentinel.vision",
        qos: .userInitiated,
        attributes: .concurrent
    )

    // Cap how many Vision `perform` calls run at once. The queue is concurrent
    // (so it never starves the cooperative pool), but without a width limit
    // 5 detectors × N cameras fire simultaneously and thrash the CPU / Neural
    // Engine. Leave one core for the rest of the app.
    static let limiter = DispatchSemaphore(
        value: max(2, ProcessInfo.processInfo.activeProcessorCount - 1)
    )

    static func run<T: Sendable>(_ work: @escaping @Sendable () -> T) async -> T {
        await withCheckedContinuation { (continuation: CheckedContinuation<T, Never>) in
            queue.async {
                limiter.wait()
                defer { limiter.signal() }
                continuation.resume(returning: work())
            }
        }
    }
}

// MARK: - Person

private enum LocalAIPersonDetector {
    public static func detect(in image: CGImage) async -> [Candidate] {
        await VisionRunner.run {
            let request = VNDetectHumanRectanglesRequest()
            request.usesCPUOnly = false  // Allow Neural Engine / GPU
            let handler = VNImageRequestHandler(cgImage: image, options: [:])
            guard (try? handler.perform([request])) != nil else { return [] }
            return (request.results ?? []).map { Candidate($0.confidence, $0.boundingBox) }
        }
    }
}

// MARK: - Face

private enum LocalAIFaceDetector {
    public static func detect(in image: CGImage) async -> [Candidate] {
        await VisionRunner.run {
            let request = VNDetectFaceRectanglesRequest()
            request.usesCPUOnly = false
            let handler = VNImageRequestHandler(cgImage: image, options: [:])
            guard (try? handler.perform([request])) != nil else { return [] }
            return (request.results ?? []).map { Candidate($0.confidence, $0.boundingBox) }
        }
    }
}

// MARK: - License plate OCR

private enum LocalAITextRecognizer {
    private static let plateRegex = try? NSRegularExpression(pattern: "^[A-Z0-9]{4,8}$")

    public static func detectPlates(in image: CGImage) async -> [Candidate] {
        await VisionRunner.run {
            let request = VNRecognizeTextRequest()
            request.recognitionLevel = .fast
            request.usesLanguageCorrection = false
            request.recognitionLanguages = ["en-US"]
            request.usesCPUOnly = false

            let handler = VNImageRequestHandler(cgImage: image, options: [:])
            guard (try? handler.perform([request])) != nil else { return [] }

            return (request.results ?? []).compactMap { obs -> Candidate? in
                guard obs.confidence >= 0.70,
                      let raw = obs.topCandidates(1).first?.string else { return nil }

                let text = raw.trimmingCharacters(in: .whitespaces)
                    .uppercased()
                    .replacingOccurrences(of: " ", with: "")
                    .replacingOccurrences(of: "-", with: "")

                guard let regex = plateRegex,
                      regex.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)) != nil else { return nil }

                let bb = obs.boundingBox
                guard bb.width > bb.height * 1.2 else { return nil }

                return Candidate(obs.confidence, bb, text)
            }
        }
    }
}

// MARK: - Vehicle (scene classification)

private enum LocalAIVehicleDetector {
    private static let vehicleTerms = ["car", "automobile", "truck", "van", "bus",
                                        "motorcycle", "vehicle", "sedan", "pickup",
                                        "minivan", "suv", "jeep"]

    public static func detect(in image: CGImage) async -> [Candidate] {
        await VisionRunner.run {
            let request = VNClassifyImageRequest()
            request.usesCPUOnly = false
            let handler = VNImageRequestHandler(cgImage: image, options: [:])
            guard (try? handler.perform([request])) != nil else { return [] }

            let best = (request.results ?? [])
                .filter { obs in
                    let lower = obs.identifier.lowercased()
                    return vehicleTerms.contains(where: { lower.contains($0) })
                }
                .max(by: { $0.confidence < $1.confidence })

            guard let obs = best, obs.confidence >= 0.60 else { return [] }

            let label = obs.identifier.components(separatedBy: ",").first?
                .trimmingCharacters(in: .whitespaces)
                .capitalized ?? "Vehicle"
            return [Candidate(obs.confidence, CGRect(x: 0.1, y: 0.1, width: 0.8, height: 0.8), label)]
        }
    }
}

// MARK: - Animal

private enum LocalAIAnimalDetector {
    public static func detect(in image: CGImage) async -> [Candidate] {
        await VisionRunner.run {
            if #available(macOS 12.0, *) {
                let request = VNRecognizeAnimalsRequest()
                request.usesCPUOnly = false
                let handler = VNImageRequestHandler(cgImage: image, options: [:])
                guard (try? handler.perform([request])) != nil else { return [] }
                return (request.results ?? []).map { obs in
                    let label = obs.labels.first?.identifier ?? "Animal"
                    return Candidate(obs.confidence, obs.boundingBox, label)
                }
            }
            return []
        }
    }
}

private func createDirectoryIfNeeded(_ url: URL) throws {
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
}
