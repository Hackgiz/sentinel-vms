// PhoneCameraCapture.swift
// WAVE23 (stream I): the phone's camera → fMP4 segments. AVCaptureSession
// delivers sample buffers; AVAssetWriter (profile .mpeg4AppleHLS, 1 s
// segments, key frame every second) encodes them and hands back an init
// segment (ftyp+moov) and media segments (moof+mdat) through its delegate.
// Only runs on a device: the Simulator has no camera (`isAvailable`).

import AVFoundation
import UIKit
import UniformTypeIdentifiers

final class PhoneCameraCapture: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate,
                                AVCaptureAudioDataOutputSampleBufferDelegate, AVAssetWriterDelegate {
    /// What the server is told in `POST …/sessions`.
    struct Format: Equatable { var width: Int; var height: Int; var fps: Int; var audio: Bool }

    let session = AVCaptureSession()
    /// New init segment (a new writer started): a new push session follows.
    var onInit: ((Data, Format) -> Void)?
    var onSegment: ((PushSegment) -> Void)?
    var onError: ((String) -> Void)?

    private let queue = DispatchQueue(label: "sentinel.phone-camera.capture")
    private let videoOutput = AVCaptureVideoDataOutput()
    private let audioOutput = AVCaptureAudioDataOutput()
    private var writer: AVAssetWriter?
    private var videoInput: AVAssetWriterInput?
    private var audioInput: AVAssetWriterInput?
    private var startTime: CMTime?
    private var format = Format(width: 1280, height: 720, fps: 30, audio: false)
    private var bitRate = 2_500_000
    private var portrait = false
    private var configured = false

    static var isAvailable: Bool {
        #if targetEnvironment(simulator)
        return false
        #else
        return AVCaptureDevice.default(for: .video) != nil
        #endif
    }

    /// Asks for camera (and microphone) access. Returns (camera, microphone).
    static func requestAccess(microphone: Bool) async -> (Bool, Bool) {
        let camera = await AVCaptureDevice.requestAccess(for: .video)
        guard microphone else { return (camera, false) }
        let mic = await AVCaptureDevice.requestAccess(for: .audio)
        return (camera, mic)
    }

    /// Sets up inputs/outputs for the settings. Throws a readable message.
    func configure(lens: PhoneCameraLens, quality: PhoneCameraQuality, microphone: Bool,
                   portrait: Bool) throws {
        session.beginConfiguration()
        defer { session.commitConfiguration() }
        session.inputs.forEach { session.removeInput($0) }
        session.outputs.forEach { session.removeOutput($0) }
        session.sessionPreset = quality == .hd1080p30 ? .hd1920x1080 : .hd1280x720

        let position: AVCaptureDevice.Position = lens == .front ? .front : .back
        guard let device = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: position),
              let input = try? AVCaptureDeviceInput(device: device), session.canAddInput(input) else {
            throw PushError.refused("This iPhone's \(lens.label.lowercased()) camera isn't available.")
        }
        session.addInput(input)
        let duration = CMTime(value: 1, timescale: CMTimeScale(quality.fps))
        if (try? device.lockForConfiguration()) != nil {
            device.activeVideoMinFrameDuration = duration
            device.activeVideoMaxFrameDuration = duration
            device.unlockForConfiguration()
        }

        videoOutput.alwaysDiscardsLateVideoFrames = true
        videoOutput.videoSettings = [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange]
        videoOutput.setSampleBufferDelegate(self, queue: queue)
        guard session.canAddOutput(videoOutput) else { throw PushError.refused("The camera couldn't be started.") }
        session.addOutput(videoOutput)
        if let connection = videoOutput.connection(with: .video) {
            if #available(iOS 17.0, *) {
                let angle: CGFloat = portrait ? 90 : 0
                if connection.isVideoRotationAngleSupported(angle) { connection.videoRotationAngle = angle }
            } else if connection.isVideoOrientationSupported {
                connection.videoOrientation = portrait ? .portrait : .landscapeRight
            }
            if connection.isVideoMirroringSupported { connection.isVideoMirrored = lens == .front }
        }

        var audio = false
        if microphone, let mic = AVCaptureDevice.default(for: .audio),
           let micInput = try? AVCaptureDeviceInput(device: mic), session.canAddInput(micInput) {
            session.addInput(micInput)
            audioOutput.setSampleBufferDelegate(self, queue: queue)
            if session.canAddOutput(audioOutput) {
                session.addOutput(audioOutput)
                audio = true
            }
        }

        queue.sync {
            self.portrait = portrait
            self.bitRate = quality.bitRate
            self.format = Format(width: portrait ? quality.height : quality.width,
                                 height: portrait ? quality.width : quality.height,
                                 fps: quality.fps, audio: audio)
            self.resetWriterLocked()
        }
        configured = true
    }

    func start() {
        guard configured else { return }
        queue.async { if self.session.isRunning == false { self.session.startRunning() } }
    }

    func stop() {
        queue.async {
            self.session.stopRunning()
            self.resetWriterLocked()
        }
    }

    /// Starts a fresh writer with the next frame (after an interruption):
    /// a new init segment and a new push session.
    func restartWriter() {
        queue.async { self.resetWriterLocked() }
    }

    private func resetWriterLocked() {
        if let writer, writer.status == .writing {
            videoInput?.markAsFinished()
            audioInput?.markAsFinished()
            writer.finishWriting {}
        }
        writer = nil
        videoInput = nil
        audioInput = nil
        startTime = nil
    }

    private func makeWriter(at time: CMTime) {
        let writer = AVAssetWriter(contentType: UTType(AVFileType.mp4.rawValue) ?? .mpeg4Movie)
        writer.outputFileTypeProfile = .mpeg4AppleHLS
        writer.preferredOutputSegmentInterval = CMTime(seconds: 1, preferredTimescale: 1)
        writer.initialSegmentStartTime = time
        writer.delegate = self

        let video = AVAssetWriterInput(mediaType: .video, outputSettings: [
            AVVideoCodecKey: AVVideoCodecType.h264,
            AVVideoWidthKey: format.width,
            AVVideoHeightKey: format.height,
            AVVideoCompressionPropertiesKey: [
                AVVideoAverageBitRateKey: bitRate,
                AVVideoMaxKeyFrameIntervalDurationKey: 1,
                AVVideoExpectedSourceFrameRateKey: format.fps,
                AVVideoProfileLevelKey: AVVideoProfileLevelH264HighAutoLevel,
                AVVideoAllowFrameReorderingKey: false
            ] as [String: Any]
        ])
        video.expectsMediaDataInRealTime = true
        guard writer.canAdd(video) else { onError?("The video encoder couldn't start."); return }
        writer.add(video)

        var audio: AVAssetWriterInput?
        if format.audio {
            let input = AVAssetWriterInput(mediaType: .audio, outputSettings: [
                AVFormatIDKey: kAudioFormatMPEG4AAC,
                AVSampleRateKey: 44_100,
                AVNumberOfChannelsKey: 1,
                AVEncoderBitRateKey: 64_000
            ])
            input.expectsMediaDataInRealTime = true
            if writer.canAdd(input) { writer.add(input); audio = input }
        }
        // Set before startWriting: the delegate may answer at once.
        self.writer = writer
        self.videoInput = video
        self.audioInput = audio
        self.startTime = time
        guard writer.startWriting() else {
            onError?(writer.error?.localizedDescription ?? "The video encoder couldn't start.")
            self.writer = nil
            return
        }
        writer.startSession(atSourceTime: time)
    }

    // MARK: Sample buffers (on `queue`)

    func captureOutput(_ output: AVCaptureOutput, didOutput sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
        let time = CMSampleBufferGetPresentationTimeStamp(sampleBuffer)
        if output === videoOutput {
            if writer == nil { makeWriter(at: time) }
            guard let writer, writer.status == .writing, let videoInput, videoInput.isReadyForMoreMediaData else {
                if writer?.status == .failed {
                    onError?(writer?.error?.localizedDescription ?? "The video encoder stopped.")
                    resetWriterLocked()
                }
                return
            }
            videoInput.append(sampleBuffer)
        } else if output === audioOutput {
            guard let writer, writer.status == .writing, let audioInput, let startTime,
                  CMTimeCompare(time, startTime) >= 0, audioInput.isReadyForMoreMediaData else { return }
            audioInput.append(sampleBuffer)
        }
    }

    // MARK: AVAssetWriterDelegate

    func assetWriter(_ writer: AVAssetWriter, didOutputSegmentData segmentData: Data,
                     segmentType: AVAssetSegmentType, segmentReport: AVAssetSegmentReport?) {
        // Late output of a writer that was replaced belongs to no session.
        guard writer === self.writer else { return }
        switch segmentType {
        case .initialization:
            onInit?(segmentData, format)
        case .separable:
            let duration = segmentReport?.trackReports
                .first(where: { $0.mediaType == .video })?.duration.seconds ?? 1
            onSegment?(PushSegment(data: segmentData, duration: duration.isFinite && duration > 0 ? duration : 1))
        @unknown default:
            break
        }
    }
}
