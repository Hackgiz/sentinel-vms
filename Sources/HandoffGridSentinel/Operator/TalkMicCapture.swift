import AVFoundation
import SentinelCore

/// Why the microphone couldn't be used; the text is for the operator.
enum MicCaptureError: LocalizedError {
    case denied
    case missing
    case failed(String)

    var errorDescription: String? {
        switch self {
        case .denied: return TalkProtocol.micDenied
        case .missing: return TalkProtocol.micMissing
        case .failed(let detail):
            return detail.isEmpty ? TalkProtocol.micFailed : "\(TalkProtocol.micFailed) (\(detail))"
        }
    }
}

/// The Mac's microphone as 20 ms frames of little-endian 16-bit mono PCM at
/// `TalkProtocol.rate` (16 kHz): an `AVAudioEngine` input tap converted with
/// `AVAudioConverter`. `onFrame` runs on the audio thread with the frame's
/// bytes and its meter level (0…1).
final class MicCapture: @unchecked Sendable {
    typealias FrameHandler = @Sendable (_ frame: Data, _ level: Double) -> Void

    private let engine = AVAudioEngine()
    private let onFrame: FrameHandler
    private var running = false

    init(onFrame: @escaping FrameHandler) {
        self.onFrame = onFrame
    }

    deinit {
        stop()
    }

    /// Asks for microphone access the first time; throws `.denied` when the
    /// operator refused (now or earlier in System Settings).
    static func authorize() async throws {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            break
        case .notDetermined:
            guard await AVCaptureDevice.requestAccess(for: .audio) else { throw MicCaptureError.denied }
        default:
            throw MicCaptureError.denied
        }
        if AVCaptureDevice.default(for: .audio) == nil { throw MicCaptureError.missing }
    }

    func start() throws {
        guard running == false else { return }
        let input = engine.inputNode
        let inFormat = input.outputFormat(forBus: 0)
        guard inFormat.sampleRate > 0, inFormat.channelCount > 0 else { throw MicCaptureError.missing }
        guard let outFormat = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: Double(TalkProtocol.rate),
                                            channels: 1, interleaved: true),
              let converter = AVAudioConverter(from: inFormat, to: outFormat) else {
            throw MicCaptureError.failed("unsupported input format")
        }
        let ratio = outFormat.sampleRate / inFormat.sampleRate
        let onFrame = self.onFrame
        var framer = TalkFramer()

        // The tap runs on one audio thread, so the framer needs no lock.
        input.installTap(onBus: 0, bufferSize: 2048, format: inFormat) { buffer, _ in
            let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 64
            guard buffer.frameLength > 0,
                  let out = AVAudioPCMBuffer(pcmFormat: outFormat, frameCapacity: capacity) else { return }
            var fed = false
            var error: NSError?
            let status = converter.convert(to: out, error: &error) { _, inputStatus in
                if fed {
                    inputStatus.pointee = .noDataNow
                    return nil
                }
                fed = true
                inputStatus.pointee = .haveData
                return buffer
            }
            guard status != .error, out.frameLength > 0, let channel = out.int16ChannelData else { return }
            let samples = UnsafeBufferPointer(start: channel[0], count: Int(out.frameLength))
            for frame in framer.push(samples) {
                onFrame(TalkPCM.encode(frame), TalkPCM.level(frame))
            }
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            throw MicCaptureError.failed(error.localizedDescription)
        }
        running = true
    }

    /// Releases the microphone.
    func stop() {
        guard running else { return }
        running = false
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
    }
}
