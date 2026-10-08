import Foundation
import AVFoundation
import QuartzCore
import SentinelCore

/// Builds a single continuous MP4 covering an arbitrary wall-clock time range by
/// stitching the recording segments that overlap that window and trimming each
/// to the exact in/out point.
///
/// Recordings are stored as ~15-minute fMP4 segments, so a precise incident clip
/// (e.g. 14:58:40 → 15:01:10) usually spans a segment boundary. This composes
/// the overlapping segments end-to-end and exports with
/// `AVAssetExportPresetPassthrough` — a **lossless stream copy** (no transcode),
/// which keeps the export forensically faithful and fast. Trim points land on
/// the nearest sync sample, which is the expected behaviour for passthrough.
enum ClipExporter {
    enum ExportError: LocalizedError {
        case emptyRange
        case noRecordingsInRange
        case noVideoTrack
        case exportSessionUnavailable
        case failed(String)
        case cancelled

        var errorDescription: String? {
            switch self {
            case .emptyRange:
                return "The selected end time must be after the start time."
            case .noRecordingsInRange:
                return "No recordings exist for the selected time range."
            case .noVideoTrack:
                return "The recordings in this range contain no video track."
            case .exportSessionUnavailable:
                return "Could not create the video export session."
            case .failed(let message):
                return "Export failed: \(message)"
            case .cancelled:
                return "Export was cancelled."
            }
        }
    }

    /// When set, the clip is re-encoded with a burned-in wall-clock timestamp
    /// that advances every second (plus an optional camera label). Drawing pixels
    /// requires re-encoding, so an overlay export is NOT a lossless copy.
    struct TimestampOverlay {
        var cameraName: String = ""
    }

    /// The wall-clock span a built clip actually covers, plus the segments used.
    /// `actualStart`/`actualEnd` can differ slightly from the requested range
    /// when recordings don't fully cover it (gaps, or a range past the last
    /// segment) — the caller uses these for accurate custody metadata.
    struct Result {
        let actualStart: Date
        let actualEnd: Date
        let segmentCount: Int
        let duration: TimeInterval
        /// Directory of the recordings the clip was built from (custody metadata).
        let sourcePath: String?
    }

    /// One stitched segment's place on the composition timeline: where it starts
    /// in composition-seconds, the wall-clock time of that point, and its length.
    /// Used to map composition time → true recorded time for the overlay clock so
    /// it stays correct even when gaps between segments are collapsed.
    private struct OverlaySpan {
        let compStart: Double
        let wallStart: Date
        let duration: Double
    }

    /// Compose `[start, end]` from `segments` (for one camera) into a single MP4
    /// at `destination`. `progress` is called on the main actor with 0…1.
    @discardableResult
    static func exportRange(
        segments: [RecordingSegment],
        cameraID: UUID,
        from start: Date,
        to end: Date,
        to destination: URL,
        overlay: TimestampOverlay? = nil,
        progress: @escaping @MainActor (Double) -> Void = { _ in }
    ) async throws -> Result {
        guard end > start else { throw ExportError.emptyRange }

        // Segments for this camera overlapping the window, chronological.
        let overlapping = segments
            .filter { $0.cameraID == cameraID }
            .map { seg -> (segment: RecordingSegment, start: Date, end: Date) in
                let s = min(seg.createdAt, seg.modifiedAt)
                let e = max(seg.createdAt, seg.modifiedAt)
                return (seg, s, e)
            }
            .filter { $0.end > start && $0.start < end }
            .sorted { $0.start < $1.start }

        guard overlapping.isEmpty == false else { throw ExportError.noRecordingsInRange }

        let composition = AVMutableComposition()
        guard let compVideo = composition.addMutableTrack(
            withMediaType: .video,
            preferredTrackID: kCMPersistentTrackID_Invalid
        ) else {
            throw ExportError.noVideoTrack
        }
        var compAudio: AVMutableCompositionTrack?

        var cursor = CMTime.zero
        var firstTransform: CGAffineTransform?
        var actualStart: Date?
        var actualEnd: Date?
        var insertedCount = 0
        var sourceURL: URL?
        var overlayMap: [OverlaySpan] = []

        for item in overlapping {
            let asset = AVURLAsset(url: item.segment.fileURL)

            // Intersect the requested window with this segment's wall-clock span.
            let windowStart = max(start, item.start)
            let windowEnd = min(end, item.end)
            guard windowEnd > windowStart else { continue }

            guard let videoTrack = try? await asset.loadTracks(withMediaType: .video).first else { continue }
            let assetDuration = (try? await asset.load(.duration).seconds) ?? 0
            guard assetDuration > 0 else { continue }

            // Map wall-clock window → asset-relative time, clamped to real media.
            let startOffset = max(0, windowStart.timeIntervalSince(item.start))
            let safeStart = min(startOffset, assetDuration)
            let safeDuration = max(0, min(windowEnd.timeIntervalSince(windowStart), assetDuration - safeStart))
            guard safeDuration > 0.05 else { continue }

            let range = CMTimeRange(
                start: CMTime(seconds: safeStart, preferredTimescale: 600),
                duration: CMTime(seconds: safeDuration, preferredTimescale: 600)
            )

            do {
                try compVideo.insertTimeRange(range, of: videoTrack, at: cursor)
            } catch {
                // Skip an unreadable segment rather than failing the whole export.
                continue
            }

            // Keep audio aligned to its video: pad the audio track with empty time
            // whenever a segment has no audio (or before the track's first use), so
            // audio from a later segment never slides ahead of its picture.
            if let audioTrack = try? await asset.loadTracks(withMediaType: .audio).first {
                if compAudio == nil {
                    compAudio = composition.addMutableTrack(
                        withMediaType: .audio,
                        preferredTrackID: kCMPersistentTrackID_Invalid
                    )
                    if cursor > .zero {
                        compAudio?.insertEmptyTimeRange(CMTimeRange(start: .zero, duration: cursor))
                    }
                }
                do {
                    try compAudio?.insertTimeRange(range, of: audioTrack, at: cursor)
                } catch {
                    compAudio?.insertEmptyTimeRange(CMTimeRange(start: cursor, duration: range.duration))
                }
            } else if let compAudio {
                compAudio.insertEmptyTimeRange(CMTimeRange(start: cursor, duration: range.duration))
            }

            let segWallStart = item.start.addingTimeInterval(safeStart)
            overlayMap.append(OverlaySpan(compStart: cursor.seconds, wallStart: segWallStart, duration: range.duration.seconds))

            if firstTransform == nil {
                firstTransform = try? await videoTrack.load(.preferredTransform)
            }
            if sourceURL == nil { sourceURL = item.segment.fileURL }
            if actualStart == nil { actualStart = segWallStart }
            actualEnd = segWallStart.addingTimeInterval(range.duration.seconds)
            insertedCount += 1
            cursor = cursor + range.duration
        }

        guard let actualStart, let actualEnd, cursor > .zero else {
            throw ExportError.noRecordingsInRange
        }
        compVideo.preferredTransform = firstTransform ?? .identity

        // No overlay → lossless passthrough (stream copy). Overlay → re-encode
        // with a Core Animation timestamp track (passthrough can't draw pixels).
        let videoComposition: AVVideoComposition?
        let presetName: String
        if let overlay {
            videoComposition = try await makeOverlayComposition(
                composition: composition,
                duration: cursor,
                spans: overlayMap,
                overlay: overlay
            )
            presetName = AVAssetExportPresetHighestQuality
        } else {
            videoComposition = nil
            presetName = AVAssetExportPresetPassthrough
        }

        guard let session = AVAssetExportSession(asset: composition, presetName: presetName) else {
            throw ExportError.exportSessionUnavailable
        }
        session.videoComposition = videoComposition

        if FileManager.default.fileExists(atPath: destination.path) {
            try? FileManager.default.removeItem(at: destination)
        }

        try await runExport(session: session, to: destination, progress: progress)

        return Result(
            actualStart: actualStart,
            actualEnd: actualEnd,
            segmentCount: insertedCount,
            duration: cursor.seconds,
            sourcePath: sourceURL?.deletingLastPathComponent().path
        )
    }

    // MARK: - Timestamp overlay (per-frame live clock)

    /// Builds a video composition that re-encodes the stitched clip with a
    /// burned-in wall-clock timestamp. The timestamp is driven by a discrete
    /// keyframe animation over the layer's `string` — one entry per second,
    /// each mapped through `spans` (composition-time → wall-clock breakpoints) so
    /// the clock reads the TRUE recorded time of every frame even when gaps
    /// between segments were collapsed during stitching.
    private static func makeOverlayComposition(
        composition: AVMutableComposition,
        duration: CMTime,
        spans: [OverlaySpan],
        overlay: TimestampOverlay
    ) async throws -> AVMutableVideoComposition {
        let videoComposition = try await AVMutableVideoComposition.videoComposition(withPropertiesOf: composition)
        let renderSize = videoComposition.renderSize

        let parentLayer = CALayer()
        parentLayer.frame = CGRect(origin: .zero, size: renderSize)
        let videoLayer = CALayer()
        videoLayer.frame = parentLayer.frame
        parentLayer.addSublayer(videoLayer)

        let fontSize = max(14, renderSize.height * 0.034)
        let textLayer = CATextLayer()
        textLayer.fontSize = fontSize
        textLayer.font = "Menlo-Bold" as CFString
        textLayer.foregroundColor = CGColor(red: 1, green: 1, blue: 1, alpha: 1)
        textLayer.shadowColor = CGColor(red: 0, green: 0, blue: 0, alpha: 1)
        textLayer.shadowOpacity = 0.9
        textLayer.shadowRadius = 3
        textLayer.shadowOffset = CGSize(width: 0, height: -1)
        textLayer.alignmentMode = .left
        textLayer.contentsScale = 3
        textLayer.isWrapped = false
        let pad = fontSize * 0.7
        // CA origin is bottom-left, so y = pad pins the label to the bottom edge.
        textLayer.frame = CGRect(x: pad, y: pad, width: renderSize.width - pad * 2, height: fontSize * 1.5)

        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd HH:mm:ss"
        let seconds = max(1, Int(ceil(duration.seconds)))
        // One value per composition-second; map each through `spans` to the real
        // recorded wall-clock at that point in the timeline.
        var values: [String] = []
        for i in 0..<seconds {
            let t = Double(i)
            let wall: Date
            if let span = spans.last(where: { $0.compStart <= t + 0.0001 }) {
                let into = min(max(0, t - span.compStart), span.duration)
                wall = span.wallStart.addingTimeInterval(into)
            } else if let first = spans.first {
                wall = first.wallStart
            } else {
                wall = Date(timeIntervalSince1970: 0)
            }
            let stamp = formatter.string(from: wall)
            values.append(overlay.cameraName.isEmpty ? stamp : "\(stamp)   \(overlay.cameraName)")
        }
        // Discrete keyframe animation needs keyTimes.count == values.count + 1,
        // with the final keyTime at 1.0 to close the last interval.
        var keyTimes: [NSNumber] = []
        for i in 0...seconds { keyTimes.append(NSNumber(value: Double(i) / Double(seconds))) }
        textLayer.string = values.first

        let anim = CAKeyframeAnimation(keyPath: "string")
        anim.values = values
        anim.keyTimes = keyTimes
        anim.calculationMode = .discrete
        anim.duration = max(0.1, duration.seconds)
        anim.beginTime = AVCoreAnimationBeginTimeAtZero
        anim.isRemovedOnCompletion = false
        anim.fillMode = .forwards
        textLayer.add(anim, forKey: "timestamp")
        parentLayer.addSublayer(textLayer)

        videoComposition.animationTool = AVVideoCompositionCoreAnimationTool(
            postProcessingAsVideoLayer: videoLayer,
            in: parentLayer
        )
        return videoComposition
    }

    // MARK: - Export plumbing (version-split: async API on 15+, legacy on 13/14)

    private static func runExport(
        session: AVAssetExportSession,
        to destination: URL,
        progress: @escaping @MainActor (Double) -> Void
    ) async throws {
        if #available(macOS 15.0, *) {
            let states = Task {
                for await state in session.states(updateInterval: 0.2) {
                    if case .exporting(let p) = state {
                        await progress(p.fractionCompleted)
                    }
                }
            }
            defer { states.cancel() }
            do {
                try await session.export(to: destination, as: .mp4)
                await progress(1.0)
            } catch {
                throw ExportError.failed(error.localizedDescription)
            }
        } else {
            session.outputURL = destination
            session.outputFileType = .mp4
            session.shouldOptimizeForNetworkUse = true

            let poller = Task {
                while !Task.isCancelled {
                    let p = session.progress
                    await progress(Double(p))
                    if p >= 1.0 { break }
                    try? await Task.sleep(nanoseconds: 200_000_000)
                }
            }
            defer { poller.cancel() }

            await session.export()

            switch session.status {
            case .completed:
                await progress(1.0)
            case .cancelled:
                throw ExportError.cancelled
            default:
                throw ExportError.failed(session.error?.localizedDescription ?? "Unknown error")
            }
        }
    }
}
