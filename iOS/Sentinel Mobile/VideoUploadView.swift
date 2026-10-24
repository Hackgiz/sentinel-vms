// VideoUploadView.swift
// WAVE23 (stream I): "Add video" (event / alarm detail) and Settings →
// "Send a video to Sentinel". Pick from Photos (PHPicker: no library
// permission) or record with the camera, add a title and note, then
// VideoUploadJob (VideoUploadLogic.swift) sends it in 8 MiB parts.
// Uploads and the phone camera are Sentinel Cloud features: the entry
// points stay visible and explain when the site isn't subscribed.

import AVFoundation
import PhotosUI
import SwiftUI
import UIKit
import UniformTypeIdentifiers

// MARK: - Sentinel Cloud sheet

/// Text only: like CloudStatusView, the app never links to a purchase page
/// (App Store rules). The site's administrator turns Sentinel Cloud on.
struct CloudRequiredSheet: View {
    static let title = "Needs Sentinel Cloud"

    enum Feature {
        case uploads, phoneCamera

        var message: String {
            switch self {
            case .uploads:
                return "Uploading videos from your iPhone is part of Sentinel Cloud. The site's administrator can turn it on."
            case .phoneCamera:
                return "Using your iPhone as a camera is part of Sentinel Cloud. The site's administrator can turn it on."
            }
        }
    }

    let feature: Feature
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(spacing: 18) {
            Image(systemName: "cloud.fill")
                .font(.system(size: 40))
                .foregroundStyle(SentinelTheme.accent)
            Text(Self.title)
                .font(.title3.weight(.bold))
                .foregroundStyle(.white)
            Text(feature.message)
                .font(.callout)
                .multilineTextAlignment(.center)
                .foregroundStyle(.white.opacity(0.8))
            Button {
                dismiss()
            } label: {
                Text("OK")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 12)
                    .background(SentinelTheme.accent, in: RoundedRectangle(cornerRadius: 12))
                    .foregroundStyle(.white)
            }
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(SentinelTheme.background.ignoresSafeArea())
        .presentationDetents([.height(340)])
    }
}

// MARK: - Entry points

/// "Add video" for an event or alarm. Checks Sentinel Cloud first.
struct AddVideoButton: View {
    let context: UploadContext
    var title = "Add video"
    @ObservedObject private var session = SentinelSession.shared
    @State private var showUpload = false
    @State private var showCloud = false
    @State private var checking = false

    var body: some View {
        if session.isDemoMode == false, session.can(.acknowledgeAlarms),
           Wave23Support.isUnsupported(.uploads, siteID: session.currentSite?.id) == false {
            Button {
                Haptics.tap()
                Task { await open() }
            } label: {
                HStack {
                    Label(title, systemImage: "video.badge.plus")
                    if checking { Spacer(); ProgressView().tint(.white) }
                }
                .font(.headline)
                .frame(maxWidth: .infinity)
                .padding(.vertical, SentinelTheme.Space.md)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm))
                .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))
                .foregroundStyle(.white)
            }
            .disabled(checking)
            .sheet(isPresented: $showUpload) { VideoUploadSheet(context: context) }
            .sheet(isPresented: $showCloud) { CloudRequiredSheet(feature: .uploads) }
        }
    }

    private func open() async {
        guard let api = session.api else { return }
        checking = true
        let availability = await CloudFeatureAvailability.check(.uploads, api: api)
        checking = false
        if availability == .cloudRequired { showCloud = true } else { showUpload = true }
    }
}

/// Settings card: "Send a video to Sentinel" and "Use this iPhone as a camera".
struct PhoneVideoSettingsCard: View {
    @ObservedObject private var session = SentinelSession.shared
    @State private var showUpload = false
    @State private var showCamera = false
    @State private var cloudFeature: CloudRequiredSheet.Feature?
    @State private var checking = false

    private var siteID: String? { session.currentSite?.id }

    private var showsUpload: Bool {
        session.can(.acknowledgeAlarms) && Wave23Support.isUnsupported(.uploads, siteID: siteID) == false
    }

    private var showsCamera: Bool {
        guard Wave23Support.isUnsupported(.phoneCamera, siteID: siteID) == false else { return false }
        // Admins create it; once created on this phone, anyone signed in here can stream.
        return session.can(.manageCameras) || (siteID.flatMap { KeychainPhoneCameraStore().load(siteID: $0) } != nil)
    }

    var body: some View {
        if session.isSignedIn, showsUpload || showsCamera {
            SentinelCard {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 8) {
                        Image(systemName: "iphone.gen3").foregroundStyle(SentinelTheme.accent)
                        Text("This iPhone").font(.headline).foregroundStyle(.white)
                        Spacer()
                        if checking { ProgressView().tint(.white) }
                    }
                    if showsUpload {
                        row("Send a video to Sentinel", detail: "Add a video from Photos or the camera to the evidence vault.",
                            systemImage: "square.and.arrow.up") { await open(.uploads) }
                    }
                    if showsCamera {
                        row("Use this iPhone as a camera", detail: "Stream this iPhone's camera to Sentinel like any camera.",
                            systemImage: "video.circle") { await open(.phoneCamera) }
                    }
                }
            }
            .sheet(isPresented: $showUpload) { VideoUploadSheet(context: .none) }
            .fullScreenCover(isPresented: $showCamera) { PhoneCameraView() }
            .sheet(item: Binding(get: { cloudFeature.map(FeatureBox.init) }, set: { cloudFeature = $0?.feature })) {
                CloudRequiredSheet(feature: $0.feature)
            }
        }
    }

    private struct FeatureBox: Identifiable {
        let feature: CloudRequiredSheet.Feature
        var id: String { feature == .uploads ? "uploads" : "camera" }
    }

    private func open(_ feature: CloudFeatureAvailability.Feature) async {
        guard let api = session.api else { return }
        checking = true
        let availability = await CloudFeatureAvailability.check(feature, api: api)
        checking = false
        if availability == .cloudRequired {
            cloudFeature = feature == .uploads ? .uploads : .phoneCamera
        } else if feature == .uploads {
            showUpload = true
        } else {
            showCamera = true
        }
    }

    private func row(_ title: String, detail: String, systemImage: String,
                     action: @escaping () async -> Void) -> some View {
        Button {
            Haptics.tap()
            Task { await action() }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: systemImage).foregroundStyle(SentinelTheme.accent).frame(width: 24)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                    Text(detail).font(.caption).foregroundStyle(.white.opacity(0.55))
                }
                Spacer()
                Image(systemName: "chevron.right").foregroundStyle(.white.opacity(0.3))
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(checking)
    }
}

// MARK: - The upload sheet

struct VideoUploadSheet: View {
    let context: UploadContext
    @ObservedObject private var session = SentinelSession.shared
    @Environment(\.dismiss) private var dismiss
    @State private var picked: PickedVideo?
    @State private var title = ""
    @State private var note = ""
    @State private var job: VideoUploadJob?
    @State private var showPhotos = false
    @State private var showRecorder = false
    @State private var loading = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Group {
                if let job {
                    UploadProgressView(job: job, onClose: { dismiss() })
                } else {
                    form
                }
            }
            .navigationTitle("Send a video")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(job == nil ? "Cancel" : "Close") {
                        if let job {
                            if case .done = job.state {} else {
                                // Closing before it finished drops the upload on the server.
                                Task { await job.cancel() }
                            }
                        }
                        picked?.discard()
                        dismiss()
                    }
                }
            }
        }
        .sheet(isPresented: $showPhotos) {
            PhotoVideoPicker { result in
                showPhotos = false
                Task { await adopt(result) }
            }
            .ignoresSafeArea()
        }
        .fullScreenCover(isPresented: $showRecorder) {
            VideoRecorder { url in
                showRecorder = false
                if let url { Task { await adopt(.success(url)) } }
            }
            .ignoresSafeArea()
        }
        .interactiveDismissDisabled(job?.state.isBusy == true)
    }

    private var form: some View {
        Form {
            Section {
                if let picked {
                    LabeledContent("Video", value: picked.url.lastPathComponent)
                    LabeledContent("Size", value: ByteCountFormatter.string(fromByteCount: picked.size, countStyle: .file))
                    if let at = picked.recordedAt {
                        LabeledContent("Recorded", value: Date(unixMillis: at).formatted(date: .abbreviated, time: .shortened))
                    }
                }
                Button { showPhotos = true } label: {
                    Label(picked == nil ? "Choose from Photos" : "Choose another video", systemImage: "photo.on.rectangle")
                }
                if UIImagePickerController.isSourceTypeAvailable(.camera) {
                    Button { showRecorder = true } label: { Label("Record a video", systemImage: "video.fill") }
                }
                if loading { ProgressView("Preparing the video…") }
            } footer: {
                Text(attachmentNote)
            }

            Section("Details") {
                TextField("Title", text: $title)
                TextField("Note (optional)", text: $note, axis: .vertical).lineLimit(2...5)
            }

            if let error {
                Section { Text(error).foregroundStyle(SentinelTheme.alarm) }
            }

            Section {
                Button {
                    send()
                } label: {
                    Label("Send to Sentinel", systemImage: "arrow.up.circle.fill").frame(maxWidth: .infinity)
                }
                .disabled(picked == nil || loading)
            } footer: {
                Text("The video goes to the evidence vault in 8 MB pieces, so it can pause and continue. Keep Sentinel open until it finishes.")
            }
        }
    }

    private var attachmentNote: String {
        if context.alarmID != nil { return "The video is attached to this alarm." }
        if context.eventID != nil { return "The video is attached to this event." }
        if context.incidentID != nil { return "The video is added to this incident." }
        return "The video goes to the evidence vault."
    }

    private func adopt(_ result: Result<URL, Error>?) async {
        guard let result else { return }
        switch result {
        case .failure(let failure):
            error = failure.localizedDescription
        case .success(let url):
            loading = true
            let recordedAt = await PickedVideo.creationDate(of: url)
            loading = false
            picked?.discard()
            picked = PickedVideo(url: url, size: UploadFile.size(of: url), recordedAt: recordedAt)
            if title.isEmpty { title = "Video \(Date().formatted(date: .abbreviated, time: .shortened))" }
            error = nil
        }
    }

    private func send() {
        guard let picked, let api = session.api else { return }
        let service = APIVideoUploadService(api: api, userAgent: UploadFile.userAgent(
            version: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String,
            model: PickedVideo.deviceModel, system: UIDevice.current.systemVersion))
        let job = VideoUploadJob(file: picked.url, fileName: picked.url.lastPathComponent, context: context, service: service)
        job.title = title
        job.note = note
        job.recordedAt = picked.recordedAt
        self.job = job
        job.start()
    }
}

/// Progress, pause/resume, retry and the result.
struct UploadProgressView: View {
    @ObservedObject var job: VideoUploadJob
    let onClose: () -> Void
    @ObservedObject private var session = SentinelSession.shared
    @State private var backgroundTask: UIBackgroundTaskIdentifier = .invalid

    var body: some View {
        Group {
            if case .cloudRequired = job.state {
                CloudRequiredSheet(feature: .uploads)
            } else {
                content
            }
        }
        .onChange(of: job.state) { state in
            keepRunningBriefly(state.isBusy)
            if case .unsupported = state { Wave23Support.markUnsupported(.uploads, siteID: session.currentSite?.id) }
            if case .done = state { Haptics.success() }
        }
        .onDisappear {
            keepRunningBriefly(false)
            if case .done = job.state { try? FileManager.default.removeItem(at: job.file) }
        }
    }

    private var content: some View {
        VStack(spacing: 18) {
            Spacer()
            Image(systemName: icon).font(.system(size: 44)).foregroundStyle(iconColor)
            Text(headline).font(.headline).foregroundStyle(.white).multilineTextAlignment(.center)
            if showsBar {
                ProgressView(value: job.state.fraction).tint(SentinelTheme.accent)
                Text(detail).font(.caption.monospacedDigit()).foregroundStyle(.white.opacity(0.6))
            }
            if case .failed(let message, _) = job.state {
                Text(message).font(.callout).foregroundStyle(.white.opacity(0.75)).multilineTextAlignment(.center)
            }
            HStack(spacing: 12) { buttons }
            Spacer()
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(SentinelTheme.background.ignoresSafeArea())
    }

    @ViewBuilder
    private var buttons: some View {
        switch job.state {
        case .uploading:
            action("Pause", "pause.fill") { job.pause() }
        case .paused:
            action("Resume", "play.fill") { job.start() }
        case .failed(_, let canRetry):
            if canRetry { action("Retry", "arrow.clockwise") { job.start() } }
            action("Close", "xmark") { onClose() }
        case .done, .unsupported:
            action("Done", "checkmark") { onClose() }
        default:
            EmptyView()
        }
    }

    private func action(_ title: String, _ image: String, _ run: @escaping () -> Void) -> some View {
        Button { Haptics.tap(); run() } label: {
            Label(title, systemImage: image)
                .font(.headline)
                .padding(.horizontal, 18).padding(.vertical, 10)
                .background(SentinelTheme.accent, in: Capsule())
                .foregroundStyle(.white)
        }
    }

    private var showsBar: Bool {
        switch job.state {
        case .hashing, .uploading, .paused, .completing: return true
        default: return false
        }
    }

    private var headline: String {
        switch job.state {
        case .ready, .starting: return "Starting…"
        case .hashing: return "Preparing the video…"
        case .uploading: return "Sending to Sentinel…"
        case .paused: return "Paused"
        case .completing: return "Checking the video on the server…"
        case .done(let item): return "Sent. \"\(item.title ?? job.title)\" is in the evidence vault."
        case .failed: return "The video wasn't sent"
        case .cloudRequired: return CloudRequiredSheet.Feature.uploads.message
        case .unsupported: return "This server can't take videos from the iPhone yet. Update Sentinel on the server."
        }
    }

    private var detail: String {
        switch job.state {
        case .hashing(let fraction): return "Checksum \(Int(fraction * 100))%"
        case .uploading(let sent, let total), .paused(let sent, let total):
            return "\(ByteCountFormatter.string(fromByteCount: sent, countStyle: .file)) of \(ByteCountFormatter.string(fromByteCount: total, countStyle: .file))"
        default: return ""
        }
    }

    private var icon: String {
        switch job.state {
        case .done: return "checkmark.circle.fill"
        case .failed, .unsupported: return "exclamationmark.triangle.fill"
        case .paused: return "pause.circle.fill"
        default: return "arrow.up.circle"
        }
    }

    private var iconColor: Color {
        switch job.state {
        case .done: return SentinelTheme.recording
        case .failed, .unsupported: return SentinelTheme.alarm
        default: return SentinelTheme.accent
        }
    }

    /// A little extra time if the user leaves the app mid-upload; a paused
    /// or failed upload resumes from the part that hadn't finished.
    private func keepRunningBriefly(_ busy: Bool) {
        if busy, backgroundTask == .invalid {
            backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "Sentinel video upload") {
                job.pause()
                UIApplication.shared.endBackgroundTask(backgroundTask)
                backgroundTask = .invalid
            }
        } else if busy == false, backgroundTask != .invalid {
            UIApplication.shared.endBackgroundTask(backgroundTask)
            backgroundTask = .invalid
        }
    }
}

// MARK: - Picked file

struct PickedVideo: Equatable {
    var url: URL
    var size: Int64
    var recordedAt: UnixMillis?

    /// Our own copy in tmp (the picker's file disappears after its callback).
    static func copyToUploads(_ source: URL) throws -> URL {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("SentinelUploads", isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let ext = source.pathExtension.isEmpty ? "mov" : source.pathExtension
        let base = source.deletingPathExtension().lastPathComponent
        let target = folder.appendingPathComponent("\(base)-\(UUID().uuidString.prefix(8)).\(ext)")
        try FileManager.default.copyItem(at: source, to: target)
        return target
    }

    func discard() { try? FileManager.default.removeItem(at: url) }

    static func creationDate(of url: URL) async -> UnixMillis? {
        let asset = AVURLAsset(url: url)
        guard let item = try? await asset.load(.creationDate),
              let date = try? await item.load(.dateValue) else { return nil }
        return date.unixMillis
    }

    /// "iPhone17,2".
    static var deviceModel: String {
        var info = utsname()
        uname(&info)
        let model = withUnsafeBytes(of: &info.machine) { raw in
            String(decoding: raw.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
        return model.isEmpty ? UIDevice.current.model : model
    }
}

/// PHPicker for one video; no Photos permission needed.
struct PhotoVideoPicker: UIViewControllerRepresentable {
    let onPick: (Result<URL, Error>?) -> Void

    func makeUIViewController(context: Context) -> PHPickerViewController {
        var config = PHPickerConfiguration()
        config.filter = .videos
        config.selectionLimit = 1
        config.preferredAssetRepresentationMode = .current // no transcoding
        let picker = PHPickerViewController(configuration: config)
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ uiViewController: PHPickerViewController, context: Context) {}
    func makeCoordinator() -> Coordinator { Coordinator(onPick: onPick) }

    final class Coordinator: NSObject, PHPickerViewControllerDelegate {
        let onPick: (Result<URL, Error>?) -> Void
        init(onPick: @escaping (Result<URL, Error>?) -> Void) { self.onPick = onPick }

        func picker(_ picker: PHPickerViewController, didFinishPicking results: [PHPickerResult]) {
            guard let provider = results.first?.itemProvider else { onPick(nil); return }
            let type = provider.registeredTypeIdentifiers.first {
                UTType($0)?.conforms(to: .movie) == true
            } ?? UTType.movie.identifier
            provider.loadFileRepresentation(forTypeIdentifier: type) { url, error in
                let result: Result<URL, Error>
                if let url {
                    result = Result { try PickedVideo.copyToUploads(url) }
                } else {
                    result = .failure(error ?? CocoaError(.fileReadUnknown))
                }
                DispatchQueue.main.async { self.onPick(result) }
            }
        }
    }
}

/// The system camera in video mode.
struct VideoRecorder: UIViewControllerRepresentable {
    let onFinish: (URL?) -> Void

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.mediaTypes = [UTType.movie.identifier]
        picker.cameraCaptureMode = .video
        picker.videoQuality = .typeHigh
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ uiViewController: UIImagePickerController, context: Context) {}
    func makeCoordinator() -> Coordinator { Coordinator(onFinish: onFinish) }

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let onFinish: (URL?) -> Void
        init(onFinish: @escaping (URL?) -> Void) { self.onFinish = onFinish }

        func imagePickerController(_ picker: UIImagePickerController,
                                   didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
            let url = (info[.mediaURL] as? URL).flatMap { try? PickedVideo.copyToUploads($0) }
            onFinish(url)
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) { onFinish(nil) }
    }
}
