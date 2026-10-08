// PairingScannerView.swift
// First-run welcome + pairing flow. Explains what the app needs, then offers
// three paths: scan the QR shown on the Mac, type the pairing info manually,
// or try Demo Mode with sample data so the rest of the app can be previewed
// without a Mac server (e.g. in the iOS Simulator, which has no camera).

import SwiftUI
import AVFoundation

struct PairingScannerView: View {
    @ObservedObject var session = SentinelSession.shared
    @State private var showScanner = false
    @State private var showManualEntry = false
    @State private var lastError: String?

    var body: some View {
        ZStack {
            SentinelTheme.background
                .ignoresSafeArea()

            ScrollView {
                VStack(spacing: 24) {
                    header
                    stepsCard
                    actionsCard
                    if let lastError {
                        HStack(alignment: .top, spacing: 8) {
                            Image(systemName: "exclamationmark.triangle.fill")
                                .foregroundStyle(SentinelTheme.alarm)
                            Text(lastError)
                                .font(.caption)
                                .foregroundStyle(.white)
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(SentinelTheme.alarm.opacity(0.14), in: RoundedRectangle(cornerRadius: 10))
                        .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.alarm.opacity(0.4), lineWidth: 1))
                    }
                    footer
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 32)
            }
        }
        .sheet(isPresented: $showScanner) {
            QRScannerSheet { code in
                showScanner = false
                Task { await attemptPair(qr: code) }
            }
        }
        .sheet(isPresented: $showManualEntry) {
            ManualPairingSheet { url, code in
                showManualEntry = false
                Task { await attemptPair(manualURL: url, code: code) }
            }
        }
    }

    // MARK: - Sections

    private var header: some View {
        VStack(spacing: 14) {
            ZStack {
                Image("AppLogo")
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .frame(width: 80, height: 80)
                    .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .stroke(.white.opacity(0.15), lineWidth: 1)
                    .frame(width: 80, height: 80)
            }
            .shadow(color: SentinelTheme.accent.opacity(0.4), radius: 20, y: 6)

            VStack(spacing: 6) {
                Text("Sentinel")
                    .font(.title.weight(.bold))
                    .foregroundStyle(.white)
                Text("HandoffGrid VMS · Mobile")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(SentinelTheme.accent)
                    .textCase(.uppercase)
                    .tracking(1.2)
            }

            Text("Live cameras, alerts, and recordings from\nyour Sentinel Mac — anywhere.")
                .font(.subheadline)
                .multilineTextAlignment(.center)
                .foregroundStyle(.white.opacity(0.65))
        }
        .padding(.top, 12)
    }

    private var stepsCard: some View {
        SentinelCard(padding: 18) {
            VStack(alignment: .leading, spacing: 18) {
                HStack(spacing: 8) {
                    Image(systemName: "link.circle.fill")
                        .foregroundStyle(SentinelTheme.accent)
                    Text("Connect to your Mac")
                        .font(.headline)
                        .foregroundStyle(.white)
                }

                step(number: 1, title: "Open Sentinel on your Mac", detail: "It must be running and on the same Wi-Fi as this iPhone.")
                step(number: 2, title: "Go to Settings → iOS Companion App", detail: "You'll see a pairing QR code with a fresh code.")
                step(number: 3, title: "Tap Scan QR below", detail: "Point this camera at the Mac's screen.")
            }
        }
    }

    private var actionsCard: some View {
        VStack(spacing: 10) {
            Button {
                lastError = nil
                showScanner = true
            } label: {
                Label("Scan Pairing QR", systemImage: "qrcode.viewfinder")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .tint(SentinelTheme.accent)

            Button {
                lastError = nil
                showManualEntry = true
            } label: {
                Label("Enter Server Info Manually", systemImage: "keyboard")
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
                    .foregroundStyle(.white)
            }
            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1))

            HStack(spacing: 8) {
                Rectangle().fill(SentinelTheme.line).frame(height: 1)
                Text("OR")
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(.white.opacity(0.4))
                Rectangle().fill(SentinelTheme.line).frame(height: 1)
            }
            .padding(.vertical, 4)

            Button {
                lastError = nil
                session.enterDemoMode()
            } label: {
                Label("Try Demo Mode", systemImage: "play.rectangle.fill")
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
                    .foregroundStyle(SentinelTheme.recording)
            }
            .background(SentinelTheme.recording.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.recording.opacity(0.35), lineWidth: 1))

            Text("Loads sample cameras and alerts so you can explore the app without a Mac server.")
                .font(.caption2)
                .foregroundStyle(.white.opacity(0.5))
                .multilineTextAlignment(.center)
                .padding(.horizontal, 8)
                .padding(.top, 2)
        }
    }

    private var footer: some View {
        VStack(spacing: 4) {
            HStack(spacing: 4) {
                Image(systemName: "wifi")
                    .font(.caption2)
                Text("Pair on Wi-Fi · works on cellular too, automatically")
                    .font(.caption2)
            }
            .foregroundStyle(.white.opacity(0.4))
        }
        .padding(.top, 8)
    }

    private func step(number: Int, title: String, detail: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            ZStack {
                Circle()
                    .fill(SentinelTheme.accent.opacity(0.18))
                    .frame(width: 28, height: 28)
                Circle()
                    .stroke(SentinelTheme.accent.opacity(0.5), lineWidth: 1)
                    .frame(width: 28, height: 28)
                Text("\(number)")
                    .font(.footnote.weight(.bold))
                    .foregroundStyle(SentinelTheme.accent)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.white)
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(.white.opacity(0.6))
            }
            Spacer(minLength: 0)
        }
    }

    // MARK: - Pairing actions

    private func attemptPair(qr: String) async {
        do {
            try await session.pair(payloadFromQR: qr, deviceName: UIDevice.current.name)
        } catch {
            lastError = error.localizedDescription
        }
    }

    private func attemptPair(manualURL: String, code: String) async {
        do {
            try await session.pairManually(serverURL: manualURL, code: code, deviceName: UIDevice.current.name)
        } catch {
            lastError = error.localizedDescription
        }
    }
}

// MARK: - QR Scanner sheet

struct QRScannerSheet: View {
    let onCode: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var cameraUnavailable = false

    var body: some View {
        ZStack {
            if cameraUnavailable {
                unavailableView
            } else {
                QRCameraView(
                    onCode: onCode,
                    onCameraUnavailable: { cameraUnavailable = true }
                )
                .ignoresSafeArea()

                VStack {
                    Spacer()
                    Text("Point the camera at the QR code\nshown on your Mac")
                        .multilineTextAlignment(.center)
                        .font(.callout.weight(.semibold))
                        .foregroundStyle(.white)
                        .padding()
                        .background(.black.opacity(0.65), in: RoundedRectangle(cornerRadius: 10))
                        .padding(.bottom, 50)
                }
            }

            VStack {
                HStack {
                    Spacer()
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.title)
                            .foregroundStyle(.white, .black.opacity(0.5))
                    }
                    .padding()
                }
                Spacer()
            }
        }
        .background(.black)
    }

    private var unavailableView: some View {
        VStack(spacing: 16) {
            Image(systemName: "camera.fill.badge.ellipsis")
                .font(.system(size: 56))
                .foregroundStyle(.white.opacity(0.7))
            Text("Camera not available")
                .font(.title3.weight(.semibold))
                .foregroundStyle(.white)
            Text("The Simulator has no camera. Use Enter Server Info Manually,\nor run the app on a real iPhone.")
                .font(.subheadline)
                .multilineTextAlignment(.center)
                .foregroundStyle(.white.opacity(0.7))
                .padding(.horizontal, 30)
            Button("Close") { dismiss() }
                .buttonStyle(.borderedProminent)
                .padding(.top, 8)
        }
    }
}

// MARK: - Manual pairing sheet

struct ManualPairingSheet: View {
    let onSubmit: (String, String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var url: String = "http://"
    @State private var code: String = ""

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    TextField("http://192.168.1.10:8090", text: $url)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                }
                Section("Pairing Code") {
                    TextField("6-digit code", text: $code)
                        .textInputAutocapitalization(.characters)
                        .autocorrectionDisabled()
                }
                Section {
                    Text("Find these in the Mac app under Settings → iOS Companion App. The URL and code are shown above the QR.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .navigationTitle("Manual Pairing")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Connect") { onSubmit(url, code) }
                        .disabled(url.count < 8 || code.isEmpty)
                }
            }
        }
    }
}

// MARK: - QR camera (existing)

struct QRCameraView: UIViewControllerRepresentable {
    let onCode: (String) -> Void
    let onCameraUnavailable: () -> Void

    func makeUIViewController(context: Context) -> QRCameraController {
        let vc = QRCameraController()
        vc.onCode = onCode
        vc.onCameraUnavailable = onCameraUnavailable
        return vc
    }

    func updateUIViewController(_ uiViewController: QRCameraController, context: Context) {}
}

final class QRCameraController: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
    var onCode: ((String) -> Void)?
    var onCameraUnavailable: (() -> Void)?
    private let session = AVCaptureSession()
    private var previewLayer: AVCaptureVideoPreviewLayer?

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .black

        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device) else {
            DispatchQueue.main.async { [weak self] in self?.onCameraUnavailable?() }
            return
        }

        session.beginConfiguration()
        if session.canAddInput(input) { session.addInput(input) }
        let output = AVCaptureMetadataOutput()
        if session.canAddOutput(output) {
            session.addOutput(output)
            output.metadataObjectTypes = [.qr]
            output.setMetadataObjectsDelegate(self, queue: .main)
        }
        session.commitConfiguration()

        let preview = AVCaptureVideoPreviewLayer(session: session)
        preview.videoGravity = .resizeAspectFill
        preview.frame = view.bounds
        view.layer.addSublayer(preview)
        self.previewLayer = preview

        let captureSession = session
        Task.detached { captureSession.startRunning() }
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        previewLayer?.frame = view.bounds
    }

    func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput metadataObjects: [AVMetadataObject], from connection: AVCaptureConnection) {
        guard let metadata = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
              metadata.type == .qr,
              let value = metadata.stringValue else { return }
        onCode?(value)
    }
}
