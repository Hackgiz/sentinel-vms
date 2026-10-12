// PairingScannerView.swift
// QR scanner used to add a site: the QR code shown by the Management Server
// (Sentinel Admin) carries its address. Scanning only fills in the address;
// the user then signs in like on any other site.

import SwiftUI
import AVFoundation

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
                    Text("Point the camera at the QR code\nshown by your Management Server")
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
            Text("The Simulator has no camera. Enter the server address by hand,\nor run the app on a real iPhone.")
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

// MARK: - QR camera

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
