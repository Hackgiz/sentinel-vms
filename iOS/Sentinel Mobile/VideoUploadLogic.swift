// VideoUploadLogic.swift
// WAVE23 (stream I): sending a video from the phone into the evidence vault
// (WAVE23-PHONE-CAMERA.md §B, §C "Send a video", addendum: Sentinel Cloud
// only). Everything here is UI-free and unit tested: the part plan, the
// streaming SHA-256, the availability decision, error mapping and the upload
// job's state machine (pause / resume / retry).

import Foundation
import CryptoKit

// MARK: - Part plan

/// How a file of `size` bytes is cut into parts of `partSize` (the server
/// says 8 MiB: Sentinel Cloud relays ≤ 1 MiB frames and Cloudflare caps a
/// request body at ~100 MB).
struct UploadPartPlan: Equatable, Sendable {
    static let defaultPartSize: Int64 = 8 * 1024 * 1024
    /// The server's limit.
    static let maxFileSize: Int64 = 4 * 1024 * 1024 * 1024

    let size: Int64
    let partSize: Int64

    init(size: Int64, partSize: Int64?) {
        self.size = max(0, size)
        let p = partSize ?? Self.defaultPartSize
        self.partSize = p > 0 ? p : Self.defaultPartSize
    }

    var count: Int { size == 0 ? 0 : Int((size + partSize - 1) / partSize) }

    /// Byte range of part `index` (0-based).
    func range(of index: Int) -> Range<Int64> {
        let start = Int64(index) * partSize
        return start..<min(start + partSize, size)
    }

    /// Bytes in parts before `index` (what is already on the server when
    /// part `index` is next).
    func bytes(before index: Int) -> Int64 { min(Int64(index) * partSize, size) }
}

// MARK: - SHA-256 without loading the file

enum StreamingSHA256 {
    static let chunkSize = 1024 * 1024

    /// Lowercase hex SHA-256 of the file, read `chunkSize` bytes at a time.
    /// `progress` gets the bytes hashed so far; cancellation is honoured
    /// between chunks.
    static func hash(fileAt url: URL, chunkSize: Int = StreamingSHA256.chunkSize,
                     progress: ((Int64) -> Void)? = nil) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        var done: Int64 = 0
        while true {
            try Task.checkCancellation()
            let chunk: Data = try autoreleasepool {
                try handle.read(upToCount: chunkSize) ?? Data()
            }
            if chunk.isEmpty { break }
            hasher.update(data: chunk)
            done += Int64(chunk.count)
            progress?(done)
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }
}

// MARK: - File facts

enum UploadFile {
    /// "video/mp4" or "video/quicktime" (the two the server takes), from the extension.
    static func contentType(for url: URL) -> String {
        switch url.pathExtension.lowercased() {
        case "mov", "qt": return "video/quicktime"
        default: return "video/mp4"
        }
    }

    static func size(of url: URL) -> Int64 {
        let values = try? url.resourceValues(forKeys: [.fileSizeKey])
        return Int64(values?.fileSize ?? 0)
    }

    /// Reads one part from disk (≤ 8 MiB in memory at a time).
    static func readPart(_ url: URL, range: Range<Int64>) throws -> Data {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        try handle.seek(toOffset: UInt64(range.lowerBound))
        return try handle.read(upToCount: Int(range.count)) ?? Data()
    }

    /// "Sentinel Mobile/3.6.0 (iPhone; iOS 18.1)": the server records the device from it.
    static func userAgent(version: String?, model: String, system: String) -> String {
        "Sentinel Mobile/\(version ?? "0") (\(model); iOS \(system))"
    }
}

// MARK: - Availability (Sentinel Cloud addendum)

/// Whether a Sentinel Cloud feature (video uploads, the phone camera) can be
/// used on this site, decided before the user starts.
enum CloudFeatureAvailability: Equatable, Sendable {
    case available
    /// Not a Sentinel Cloud subscriber: show the Cloud sheet.
    case cloudRequired
    /// The server has no endpoints for it (older than wave 23).
    case unsupported

    enum Feature: Sendable { case uploads, phoneCamera }

    /// From `GET /api/v2/cloud`'s `uploadsAvailable` / `phoneCamerasAvailable`.
    /// Absent (older server) or unreadable counts as available; the 402 on
    /// the request itself is the backstop.
    static func decide(_ answer: Result<V2.CloudFeatures, Error>, feature: Feature) -> CloudFeatureAvailability {
        switch answer {
        case .success(let cloud):
            let flag = feature == .uploads ? cloud.uploadsAvailable : cloud.phoneCamerasAvailable
            return flag == false ? .cloudRequired : .available
        case .failure:
            return .available
        }
    }

    /// The site's answer for `feature` (never throws).
    static func check(_ feature: Feature, api: SentinelAPI) async -> CloudFeatureAvailability {
        do {
            return decide(.success(try await api.cloudFeatures()), feature: feature)
        } catch {
            return decide(.failure(error), feature: feature)
        }
    }
}

typealias UploadAvailability = CloudFeatureAvailability

/// What a failed upload call means for the job.
enum UploadFailure: Equatable, Sendable {
    case cloudRequired
    /// 404 on `POST /evidence/uploads`: the server predates uploads.
    case unsupported
    /// 404 on a part / complete: the upload expired or was swept; start over.
    case expired
    case signInRequired(String)
    /// Worth trying again (network, 5xx, 429).
    case retryable(String)
    /// The server refused for good (bad file, checksum mismatch, no right).
    case fatal(String)

    enum Step { case start, part, complete }

    static func classify(_ error: Error, step: Step) -> UploadFailure {
        guard let api = error as? SentinelAPIError else {
            return .retryable(error.localizedDescription)
        }
        if api.isCloudRequired { return .cloudRequired }
        switch api {
        case .signInRequired(let message): return .signInRequired(message)
        case .transport(let message): return .retryable(message)
        case .decoding: return .fatal(api.localizedDescription)
        case .server(let status, _, let message):
            if status == 404 || status == 405 { return step == .start ? .unsupported : .expired }
            if status == 408 || status == 429 || status >= 500 { return .retryable(message) }
            return .fatal(message)
        }
    }
}

// MARK: - The job

/// Where the upload attaches (all optional).
struct UploadContext: Equatable, Sendable {
    var cameraID: String?
    var eventID: String?
    var alarmID: String?
    var incidentID: String?

    static let none = UploadContext()
}

/// The server side of an upload, so the job can be tested without a network.
protocol VideoUploadService: Sendable {
    func availability() async -> UploadAvailability
    func start(_ start: V2.UploadStart) async throws -> V2.UploadCreated
    func putPart(uploadID: String, index: Int, data: Data,
                 progress: @escaping @Sendable (Int64) -> Void) async throws
    func complete(uploadID: String) async throws -> V2.UploadedEvidence
    func abort(uploadID: String) async
}

enum VideoUploadState: Equatable {
    case ready
    case hashing(fraction: Double)
    case starting
    case uploading(sent: Int64, total: Int64)
    case paused(sent: Int64, total: Int64)
    /// The server is checking size and checksum.
    case completing
    case done(V2.UploadedEvidence)
    case failed(message: String, canRetry: Bool)
    case cloudRequired
    case unsupported

    var isBusy: Bool {
        switch self {
        case .hashing, .starting, .uploading, .completing: return true
        default: return false
        }
    }

    var fraction: Double {
        switch self {
        case .uploading(let sent, let total), .paused(let sent, let total):
            return total > 0 ? min(1, Double(sent) / Double(total)) : 0
        case .completing, .done: return 1
        default: return 0
        }
    }
}

/// One video going up: hash → start → parts in order → complete. Parts are
/// idempotent on the server, so pause and retry simply continue from the
/// part that hadn't finished.
@MainActor
final class VideoUploadJob: ObservableObject {
    @Published private(set) var state: VideoUploadState = .ready

    let file: URL
    let fileName: String
    var title: String = ""
    var note: String = ""
    var recordedAt: UnixMillis?
    let context: UploadContext

    /// Automatic attempts per part before the job stops and offers Retry.
    static let attemptsPerPart = 4
    nonisolated static func backoff(attempt: Int) -> TimeInterval { min(8, pow(2, Double(attempt - 1))) }

    private let service: VideoUploadService
    private let sleep: @Sendable (TimeInterval) async throws -> Void
    private var task: Task<Void, Never>?

    private(set) var sha256: String?
    private(set) var uploadID: String?
    private(set) var plan: UploadPartPlan?
    /// The first part not yet confirmed by the server.
    private(set) var nextPart = 0
    private(set) var size: Int64

    init(file: URL, fileName: String? = nil, context: UploadContext = .none, service: VideoUploadService,
         sleep: @escaping @Sendable (TimeInterval) async throws -> Void = { try await Task.sleep(nanoseconds: UInt64($0 * 1e9)) }) {
        self.file = file
        self.fileName = fileName ?? file.lastPathComponent
        self.context = context
        self.service = service
        self.sleep = sleep
        self.size = UploadFile.size(of: file)
    }

    /// Starts, resumes after a pause, or retries after a failure.
    func start() {
        guard task == nil else { return }
        switch state {
        case .done, .cloudRequired, .unsupported: return
        default: break
        }
        runGeneration += 1
        let generation = runGeneration
        task = Task { [weak self] in
            await self?.run()
            // A pause + resume may already have started the next run.
            if self?.runGeneration == generation { self?.task = nil }
        }
    }

    private var runGeneration = 0

    func pause() {
        guard let task else { return }
        task.cancel()
        self.task = nil
        let total = plan?.size ?? size
        state = .paused(sent: plan?.bytes(before: nextPart) ?? 0, total: total)
    }

    /// Stops and drops the upload on the server.
    func cancel() async {
        task?.cancel()
        task = nil
        if let uploadID { await service.abort(uploadID: uploadID) }
        uploadID = nil
    }

    /// Waits for the running step (tests).
    func wait() async {
        while let task { await task.value }
    }

    private func run() async {
        do {
            try await runSteps()
        } catch is CancellationError {
            // Paused: `pause()` already set the state.
        } catch {
            state = .failed(message: error.localizedDescription, canRetry: true)
        }
    }

    private func runSteps() async throws {
        guard size > 0 else {
            state = .failed(message: "This video is empty.", canRetry: false)
            return
        }
        guard size <= UploadPartPlan.maxFileSize else {
            state = .failed(message: "This video is larger than 4 GB. Trim it in Photos and try again.", canRetry: false)
            return
        }
        if sha256 == nil {
            state = .hashing(fraction: 0)
            let url = file, total = size
            let hash = try await Task.detached(priority: .userInitiated) { () throws -> String in
                var lastReport: Int64 = 0
                return try StreamingSHA256.hash(fileAt: url) { done in
                    // A progress tick every 32 MiB is plenty.
                    if done - lastReport >= 32 * 1024 * 1024 || done == total {
                        lastReport = done
                        let fraction = Double(done) / Double(max(total, 1))
                        Task { @MainActor [weak self] in
                            if case .hashing = self?.state { self?.state = .hashing(fraction: fraction) }
                        }
                    }
                }
            }.value
            try Task.checkCancellation()
            sha256 = hash
        }
        if uploadID == nil {
            state = .starting
            let body = V2.UploadStart(
                fileName: fileName, size: size, sha256: sha256 ?? "",
                contentType: UploadFile.contentType(for: file),
                title: title.trimmed.nilIfEmpty, note: note.trimmed.nilIfEmpty, recordedAt: recordedAt,
                cameraId: context.cameraID, eventId: context.eventID, alarmId: context.alarmID,
                incidentId: context.incidentID)
            let created: V2.UploadCreated
            do {
                created = try await service.start(body)
            } catch {
                if error is CancellationError { throw error }
                return fail(UploadFailure.classify(error, step: .start))
            }
            uploadID = created.uploadId
            plan = UploadPartPlan(size: size, partSize: created.partSize)
            nextPart = 0
        }
        guard let plan, let uploadID else { return }

        while nextPart < plan.count {
            try Task.checkCancellation()
            let index = nextPart
            let range = plan.range(of: index)
            let before = plan.bytes(before: index)
            state = .uploading(sent: before, total: plan.size)
            let data = try UploadFile.readPart(file, range: range)
            var attempt = 0
            while true {
                attempt += 1
                do {
                    try await service.putPart(uploadID: uploadID, index: index, data: data) { [weak self] sent in
                        Task { @MainActor [weak self] in
                            guard let self, case .uploading = self.state, self.nextPart == index else { return }
                            self.state = .uploading(sent: before + min(sent, Int64(data.count)), total: plan.size)
                        }
                    }
                    break
                } catch {
                    if error is CancellationError || Task.isCancelled { throw CancellationError() }
                    let failure = UploadFailure.classify(error, step: .part)
                    if case .retryable = failure, attempt < Self.attemptsPerPart {
                        try await sleep(Self.backoff(attempt: attempt))
                        continue
                    }
                    if failure == .expired {
                        // Swept or expired: begin a new upload from part 0 next time.
                        self.uploadID = nil
                        self.plan = nil
                        nextPart = 0
                    }
                    return fail(failure)
                }
            }
            nextPart = index + 1
        }

        state = .completing
        do {
            let item = try await service.complete(uploadID: uploadID)
            state = .done(item)
        } catch {
            if error is CancellationError { throw error }
            let failure = UploadFailure.classify(error, step: .complete)
            if failure == .expired { self.uploadID = nil; self.plan = nil; nextPart = 0 }
            if case .fatal = failure {
                // A checksum or size mismatch: the parts on the server are
                // bad. Start a fresh upload on Retry.
                self.uploadID = nil; self.plan = nil; nextPart = 0
            }
            fail(failure)
        }
    }

    private func fail(_ failure: UploadFailure) {
        switch failure {
        case .cloudRequired: state = .cloudRequired
        case .unsupported: state = .unsupported
        case .expired:
            state = .failed(message: "The upload expired on the server. Retry to send it again.", canRetry: true)
        case .signInRequired(let message): state = .failed(message: message, canRetry: false)
        case .retryable(let message):
            state = .failed(message: "\(message) The upload stopped; Retry continues where it left off.", canRetry: true)
        case .fatal(let message): state = .failed(message: message, canRetry: true)
        }
    }
}

private extension String {
    var trimmed: String { trimmingCharacters(in: .whitespacesAndNewlines) }
    var nilIfEmpty: String? { isEmpty ? nil : self }
}

// MARK: - The real service

/// `VideoUploadService` over the signed-in site's API (LAN, remote address
/// or the Sentinel Cloud relay: whichever the API client uses right now).
struct APIVideoUploadService: VideoUploadService {
    let api: SentinelAPI
    let userAgent: String

    func availability() async -> UploadAvailability {
        await CloudFeatureAvailability.check(.uploads, api: api)
    }

    func start(_ start: V2.UploadStart) async throws -> V2.UploadCreated {
        try await api.startUpload(start, userAgent: userAgent)
    }

    func putPart(uploadID: String, index: Int, data: Data, progress: @escaping @Sendable (Int64) -> Void) async throws {
        try await api.uploadPart(uploadID: uploadID, index: index, data: data, userAgent: userAgent,
                                 progress: UploadProgressDelegate(progress))
    }

    func complete(uploadID: String) async throws -> V2.UploadedEvidence {
        try await api.completeUpload(uploadID: uploadID, userAgent: userAgent)
    }

    func abort(uploadID: String) async {
        try? await api.abortUpload(uploadID: uploadID)
    }
}

/// Bytes of the request body sent so far.
final class UploadProgressDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    private let report: @Sendable (Int64) -> Void

    init(_ report: @escaping @Sendable (Int64) -> Void) { self.report = report }

    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64,
                    totalBytesSent: Int64, totalBytesExpectedToSend: Int64) {
        report(totalBytesSent)
    }
}
