import AVFoundation
import CoreImage
import CoreVideo
import Foundation

/// Pulls `CVPixelBuffer` frames from an HLS or progressive video URL using
/// `AVPlayer` + `AVPlayerItemVideoOutput`. This is the cross-platform
/// replacement for the GStreamer→JPEG→disk pipeline the AI detector used to
/// rely on.
///
/// **Why this design.** AI detection only needs whatever pixel buffer is on
/// screen right now — there's no benefit to subscribing to every frame.
/// `latestPixelBuffer()` is sample-and-return, called on the detector's
/// 1.5s tick. The decode itself is hardware-accelerated by VideoToolbox via
/// AVPlayer, so the cost of the pipeline is essentially the cost of one
/// `CVPixelBufferRef` copy per detection cycle.
///
/// **Cross-platform note.** Lives in SentinelCore so the iOS companion app
/// can run on-device AI detection later against the same MediaMTX HLS feeds
/// it already plays.
///
/// **Threading.** Initialization and `stop()` should be called from the main
/// actor (AVPlayer expects this). `latestPixelBuffer()` is safe to call from
/// any thread — `AVPlayerItemVideoOutput.copyPixelBuffer` is documented as
/// thread-safe.
public final class VideoStreamDecoder: @unchecked Sendable {
    public let sourceURL: URL

    private let player: AVPlayer
    private let videoOutput: AVPlayerItemVideoOutput

    @MainActor
    public init(url: URL) {
        self.sourceURL = url

        let item = AVPlayerItem(url: url)
        // Don't accumulate buffer — the detector only cares about "latest".
        item.preferredForwardBufferDuration = 1.0

        let attributes: [String: Any] = [
            kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA
        ]
        let output = AVPlayerItemVideoOutput(pixelBufferAttributes: attributes)
        item.add(output)
        self.videoOutput = output

        let player = AVPlayer(playerItem: item)
        player.isMuted = true
        // Background pipeline — we don't want it stalling to refill buffer.
        player.automaticallyWaitsToMinimizeStalling = false
        self.player = player
        player.play()
    }

    /// Returns the most recently decoded frame, or nil if no new frame has
    /// arrived since the last call (or playback hasn't reached steady state).
    /// Safe to call from any thread.
    public func latestPixelBuffer() -> CVPixelBuffer? {
        let hostTime = CACurrentMediaTime()
        let itemTime = videoOutput.itemTime(forHostTime: hostTime)
        guard itemTime.isValid, !itemTime.isIndefinite else { return nil }
        guard videoOutput.hasNewPixelBuffer(forItemTime: itemTime) else { return nil }
        return videoOutput.copyPixelBuffer(forItemTime: itemTime, itemTimeForDisplay: nil)
    }

    @MainActor
    public func stop() {
        player.pause()
        player.replaceCurrentItem(with: nil)
    }
}

/// One-shot CVPixelBuffer → CGImage conversion. Hardware-accelerated via
/// Core Image on Apple silicon. Used by the AI detector to bridge the new
/// pixel-buffer pipeline into the existing CGImage-based detectors without
/// rewriting each one.
public enum VideoFrameConverter {
    private static let context = CIContext(options: nil)

    public static func makeCGImage(from pixelBuffer: CVPixelBuffer) -> CGImage? {
        let ci = CIImage(cvPixelBuffer: pixelBuffer)
        return context.createCGImage(ci, from: ci.extent)
    }
}
