import AppKit
import AVFoundation
import CryptoKit
import SwiftUI
import SentinelCore

/// Export a time range of several cameras as MP4 clips plus a manifest with
/// SHA-256 hashes.
///
/// Uses only existing endpoints: the camera's segment list
/// (`GET /api/v2/cameras/{id}/recordings`), an `export` video token per
/// Recording Server holding footage (`recorderId`), and the video plane's
/// `/recordings/{cameraId}/{file}`. Overlapping segments are downloaded and
/// stitched losslessly (passthrough), trimmed to the range.
enum PlaybackExporter {
    struct Clip: Codable, Hashable {
        var cameraId: String
        var cameraName: String
        var file: String
        var from: UnixMillis
        var to: UnixMillis
        var segments: Int
        var sha256: String
        var size: Int64
        var recorderIds: [String]
    }

    struct Manifest: Codable {
        var site: String
        var exportedBy: String
        var exportedAt: UnixMillis
        var requestedFrom: UnixMillis
        var requestedTo: UnixMillis
        var tool: String
        var clips: [Clip]
        var skipped: [String]
    }

    enum ExportError: LocalizedError {
        case nothingRecorded

        var errorDescription: String? {
            "None of the chosen cameras has recordings in that range."
        }
    }

    /// Progress text and fraction (0…1), reported on the main actor.
    typealias Progress = @MainActor (String, Double) -> Void

    static func export(cameras: [V2.Camera], from: UnixMillis, to: UnixMillis, api: SentinelAPI, video: VideoTokenBroker,
                       siteName: String, userName: String, into folder: URL, progress: @escaping Progress) async throws -> Manifest {
        let fm = FileManager.default
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        let scratch = fm.temporaryDirectory.appendingPathComponent("sentinel-export-\(UUID().uuidString)", isDirectory: true)
        try fm.createDirectory(at: scratch, withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: scratch) }

        var clips: [Clip] = []
        var skipped: [String] = []
        for (index, camera) in cameras.enumerated() {
            let base = Double(index) / Double(cameras.count)
            let share = 1 / Double(cameras.count)
            await progress("\(camera.name): finding recordings…", base)
            let segments: [V2.Segment]
            do {
                segments = try await api.recordings(cameraID: camera.id)
                    .filter { $0.end > from && $0.start < to && $0.size > 0 }
                    .sorted { $0.start < $1.start }
            } catch {
                skipped.append("\(camera.name): \(error.localizedDescription)")
                continue
            }
            guard segments.isEmpty == false else {
                skipped.append("\(camera.name): no recordings in the range")
                continue
            }

            var local: [ClipStitcher.Source] = []
            var recorders: Set<String> = []
            for (n, segment) in segments.enumerated() {
                await progress("\(camera.name): downloading \(n + 1) of \(segments.count)…", base + share * 0.8 * Double(n) / Double(segments.count))
                let recorderID = segment.recorderId ?? camera.recorderId
                do {
                    let file = scratch.appendingPathComponent("\(camera.id)-\(n)-\(segment.name)")
                    try await download(segment: segment, cameraID: camera.id, recorderID: recorderID, video: video, to: file)
                    local.append(.init(url: file, start: Date(unixMillis: segment.start), end: Date(unixMillis: segment.end)))
                    recorders.insert(recorderID)
                } catch {
                    skipped.append("\(camera.name) \(segment.name): \(error.localizedDescription)")
                }
            }
            guard local.isEmpty == false else { continue }

            await progress("\(camera.name): building the clip…", base + share * 0.85)
            let fileName = safeFileName("\(camera.name) \(stamp(from))-\(stamp(to)).mp4")
            let destination = folder.appendingPathComponent(fileName)
            do {
                let result = try await ClipStitcher.stitch(sources: local, from: Date(unixMillis: from), to: Date(unixMillis: to), destination: destination)
                let hash = try await Task.detached { try sha256(of: destination) }.value
                let size = (try? fm.attributesOfItem(atPath: destination.path)[.size] as? Int64) ?? 0
                clips.append(Clip(cameraId: camera.id, cameraName: camera.name, file: fileName,
                                  from: result.actualStart.unixMillis, to: result.actualEnd.unixMillis,
                                  segments: result.segmentCount, sha256: hash, size: size,
                                  recorderIds: recorders.sorted()))
            } catch {
                skipped.append("\(camera.name): \(error.localizedDescription)")
            }
        }

        guard clips.isEmpty == false else {
            if skipped.isEmpty { throw ExportError.nothingRecorded }
            throw SentinelAPIError.server(status: 404, code: "not_found", message: skipped.joined(separator: "\n"))
        }
        let manifest = Manifest(site: siteName, exportedBy: userName, exportedAt: Date().unixMillis,
                                requestedFrom: from, requestedTo: to, tool: "Sentinel Operator for macOS",
                                clips: clips, skipped: skipped)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        try encoder.encode(manifest).write(to: folder.appendingPathComponent("manifest.json"), options: .atomic)
        await progress("Done", 1)
        return manifest
    }

    /// Downloads one segment with an `export` token, falling back to the
    /// management server's gateway when the Recording Server isn't reachable directly.
    private static func download(segment: V2.Segment, cameraID: String, recorderID: String, video: VideoTokenBroker, to file: URL) async throws {
        var preferGateway = false
        while true {
            let endpoint = try await video.endpoint(cameraID: cameraID, rights: [.export], recorderID: recorderID, preferGateway: preferGateway)
            do {
                let (temp, response) = try await URLSession.shared.download(from: endpoint.recording(file: segment.name))
                guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
                guard (200..<300).contains(http.statusCode) else {
                    let body = (try? Data(contentsOf: temp)) ?? Data()
                    throw SentinelAPIError.from(status: http.statusCode, body: body)
                }
                try? FileManager.default.removeItem(at: file)
                try FileManager.default.moveItem(at: temp, to: file)
                return
            } catch let error as URLError where endpoint.isGateway == false && preferGateway == false {
                _ = error
                await video.reportDirectFailure(recorderID: recorderID)
                preferGateway = true
            }
        }
    }

    static func sha256(of url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        while let chunk = try handle.read(upToCount: 1 << 20), chunk.isEmpty == false {
            hasher.update(data: chunk)
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }

    private static func stamp(_ millis: UnixMillis) -> String {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd HH.mm.ss"
        return f.string(from: Date(unixMillis: millis))
    }

    static func safeFileName(_ name: String) -> String {
        String(name.map { "/:\\".contains($0) ? "-" : $0 })
    }
}

/// Stitches recorded segment files covering a wall-clock range into one MP4,
/// trimmed to the range, as a lossless stream copy.
enum ClipStitcher {
    struct Source: Hashable {
        var url: URL
        var start: Date
        var end: Date
    }

    struct Result {
        var actualStart: Date
        var actualEnd: Date
        var segmentCount: Int
    }

    enum StitchError: LocalizedError {
        case emptyRange, noVideo, exportUnavailable, failed(String)

        var errorDescription: String? {
            switch self {
            case .emptyRange: return "The range has no recorded video."
            case .noVideo: return "The recordings in this range contain no video."
            case .exportUnavailable: return "Couldn't start the video export."
            case .failed(let message): return "Export failed: \(message)"
            }
        }
    }

    static func stitch(sources: [Source], from start: Date, to end: Date, destination: URL) async throws -> Result {
        let composition = AVMutableComposition()
        guard let compVideo = composition.addMutableTrack(withMediaType: .video, preferredTrackID: kCMPersistentTrackID_Invalid) else {
            throw StitchError.noVideo
        }
        var compAudio: AVMutableCompositionTrack?
        var cursor = CMTime.zero
        var transform: CGAffineTransform?
        var actualStart: Date?
        var actualEnd: Date?
        var count = 0

        for source in sources.sorted(by: { $0.start < $1.start }) {
            let windowStart = max(start, source.start)
            let windowEnd = min(end, source.end)
            guard windowEnd > windowStart else { continue }
            let asset = AVURLAsset(url: source.url)
            guard let videoTrack = try? await asset.loadTracks(withMediaType: .video).first else { continue }
            let duration = (try? await asset.load(.duration).seconds) ?? 0
            guard duration > 0 else { continue }
            let offset = min(max(0, windowStart.timeIntervalSince(source.start)), duration)
            let length = max(0, min(windowEnd.timeIntervalSince(windowStart), duration - offset))
            guard length > 0.05 else { continue }
            let range = CMTimeRange(start: CMTime(seconds: offset, preferredTimescale: 600),
                                    duration: CMTime(seconds: length, preferredTimescale: 600))
            do {
                try compVideo.insertTimeRange(range, of: videoTrack, at: cursor)
            } catch {
                continue
            }
            if let audio = try? await asset.loadTracks(withMediaType: .audio).first {
                if compAudio == nil {
                    compAudio = composition.addMutableTrack(withMediaType: .audio, preferredTrackID: kCMPersistentTrackID_Invalid)
                    if cursor > .zero { compAudio?.insertEmptyTimeRange(CMTimeRange(start: .zero, duration: cursor)) }
                }
                if (try? compAudio?.insertTimeRange(range, of: audio, at: cursor)) == nil {
                    compAudio?.insertEmptyTimeRange(CMTimeRange(start: cursor, duration: range.duration))
                }
            } else {
                compAudio?.insertEmptyTimeRange(CMTimeRange(start: cursor, duration: range.duration))
            }
            if transform == nil { transform = try? await videoTrack.load(.preferredTransform) }
            let wallStart = source.start.addingTimeInterval(offset)
            if actualStart == nil { actualStart = wallStart }
            actualEnd = wallStart.addingTimeInterval(length)
            count += 1
            cursor = cursor + range.duration
        }

        guard let actualStart, let actualEnd, cursor > .zero else { throw StitchError.emptyRange }
        compVideo.preferredTransform = transform ?? .identity
        guard let session = AVAssetExportSession(asset: composition, presetName: AVAssetExportPresetPassthrough) else {
            throw StitchError.exportUnavailable
        }
        try? FileManager.default.removeItem(at: destination)
        session.outputURL = destination
        session.outputFileType = .mp4
        session.shouldOptimizeForNetworkUse = true
        await session.export()
        guard session.status == .completed else {
            throw StitchError.failed(session.error?.localizedDescription ?? "unknown error")
        }
        return Result(actualStart: actualStart, actualEnd: actualEnd, segmentCount: count)
    }
}

/// Choose the range and cameras, then where to save.
struct PlaybackExportSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @ObservedObject var controller: PlaybackController

    @State private var from = Date()
    @State private var to = Date()
    @State private var chosen: Set<String> = []
    @State private var status = ""
    @State private var fraction = 0.0
    @State private var isRunning = false
    @State private var result: PlaybackExporter.Manifest?
    @State private var folder: URL?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Export Clips").font(.headline)
            Grid(alignment: .leading, horizontalSpacing: 10, verticalSpacing: 8) {
                GridRow {
                    Text("From").foregroundStyle(.secondary)
                    DatePicker("", selection: $from, displayedComponents: [.date, .hourAndMinute]).labelsHidden()
                }
                GridRow {
                    Text("To").foregroundStyle(.secondary)
                    DatePicker("", selection: $to, displayedComponents: [.date, .hourAndMinute]).labelsHidden()
                }
            }
            Text("Cameras").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 4) {
                ForEach(controller.cameraIDs, id: \.self) { id in
                    Toggle(cameras.name(of: id), isOn: Binding(get: { chosen.contains(id) },
                                                               set: { if $0 { chosen.insert(id) } else { chosen.remove(id) } }))
                }
            }
            Text("Each camera becomes one MP4 (a lossless copy of the recording), with a manifest.json listing SHA-256 hashes. The export is in the site's audit log.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)

            if isRunning || result != nil {
                ProgressView(value: fraction) { Text(status).font(.caption) }
            }
            if let result {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Exported \(result.clips.count) clip\(result.clips.count == 1 ? "" : "s").").font(.callout.weight(.semibold))
                    ForEach(result.skipped, id: \.self) { Text($0).font(.caption).foregroundStyle(.secondary) }
                }
            }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if let folder, result != nil {
                    Button("Show in Finder") { NSWorkspace.shared.activateFileViewerSelecting([folder]) }
                }
                Spacer()
                Button(result == nil ? "Cancel" : "Done") { dismiss() }.disabled(isRunning)
                if result == nil {
                    Button("Export…") { start() }
                        .buttonStyle(.borderedProminent)
                        .disabled(isRunning || chosen.isEmpty || to <= from)
                }
            }
        }
        .padding(20)
        .frame(width: 440)
        .onAppear {
            let position = Date(unixMillis: controller.position)
            from = controller.selection.markIn.map { Date(unixMillis: $0) } ?? position.addingTimeInterval(-30)
            to = controller.selection.markOut.map { Date(unixMillis: $0) } ?? position.addingTimeInterval(30)
            chosen = Set(controller.cameraIDs)
        }
    }

    private func start() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.prompt = "Export Here"
        panel.message = "Choose a folder for the exported clips."
        guard panel.runModal() == .OK, let base = panel.url else { return }
        let stampFormatter = DateFormatter()
        stampFormatter.dateFormat = "yyyy-MM-dd HH.mm"
        let target = base.appendingPathComponent(PlaybackExporter.safeFileName("Sentinel export \(stampFormatter.string(from: from))"), isDirectory: true)
        folder = target
        isRunning = true
        error = nil
        let selected = controller.cameraIDs.filter { chosen.contains($0) }.compactMap { cameras.camera($0) }
        Task {
            defer { isRunning = false }
            do {
                result = try await PlaybackExporter.export(
                    cameras: selected, from: from.unixMillis, to: to.unixMillis,
                    api: runtime.api, video: runtime.video,
                    siteName: session.site?.name ?? "", userName: session.me?.name ?? "",
                    into: target
                ) { text, value in
                    status = text
                    fraction = value
                }
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
