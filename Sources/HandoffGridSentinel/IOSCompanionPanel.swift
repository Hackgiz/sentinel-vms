import SwiftUI
import CoreImage.CIFilterBuiltins
import AppKit
import SentinelCore
import SentinelMediaServer

struct IOSCompanionPanel: View {
    @EnvironmentObject private var pairingStore: PairingTokenStore
    @EnvironmentObject private var httpServer: SentinelHTTPServer
    @EnvironmentObject private var tunnelManager: CloudflareTunnelManager
    @EnvironmentObject private var pushService: APNsPushService
    @EnvironmentObject private var licenseStore: LicenseStore
    @Environment(\.openURL) private var openURL

    @State private var generatedQR: NSImage?
    @State private var autoStartRemote = RemoteAccessPreference.autoStart

    var body: some View {
        SentinelPanel("iOS Companion App", systemImage: "iphone.gen3") {
            VStack(alignment: .leading, spacing: 14) {
                serverStatus

                Divider().overlay(SentinelTheme.line)

                pairingSection

                if pairingStore.pairedDevices.isEmpty == false {
                    Divider().overlay(SentinelTheme.line)
                    devicesList
                }

                Divider().overlay(SentinelTheme.line)
                remoteAccessSection

                Divider().overlay(SentinelTheme.line)
                pushNotificationsSection
            }
        }
        // When the tunnel URL appears (or rotates), re-bake it into the QR so a
        // freshly scanning phone gets remote access without any manual step.
        .onChange(of: tunnelManager.tunnelURL) { _ in
            if let code = pairingStore.activePairingCode?.code {
                generatedQR = makeQR(for: pairingPayload(code: code))
            }
        }
    }

    private var serverStatus: some View {
        HStack(alignment: .top, spacing: 12) {
            Circle()
                .fill(httpServer.isRunning ? Color.green : .red)
                .frame(width: 8, height: 8)
                .padding(.top, 6)
            VStack(alignment: .leading, spacing: 2) {
                Text(httpServer.isRunning ? "API server running" : "API server stopped")
                    .font(.caption.weight(.semibold))
                ForEach(httpServer.listenURLs, id: \.self) { url in
                    Text(url)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                if let error = httpServer.lastError {
                    Text(error)
                        .font(.caption2)
                        .foregroundStyle(.red)
                }
            }
            Spacer()
        }
    }

    private var pairingSection: some View {
        HStack(alignment: .top, spacing: 16) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Pair a new iOS device")
                    .font(.caption.weight(.semibold))
                Text("Open Sentinel Mobile on your iPhone and scan this QR code. Pair once — it works on Wi-Fi and cellular, and keeps working after restarts.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                if let code = pairingStore.activePairingCode {
                    HStack(spacing: 8) {
                        Text("Code")
                            .font(.caption.weight(.semibold))
                        Text(code.code)
                            .font(.system(.callout, design: .monospaced).weight(.semibold))
                            .foregroundStyle(SentinelTheme.accent)
                            .textSelection(.enabled)
                        Text("expires \(code.expiresAt, format: .relative(presentation: .named))")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                }

                HStack(spacing: 8) {
                    Button {
                        // Make sure remote access is live before showing the QR
                        // so the remote URL gets baked in (the onChange handler
                        // re-bakes it the moment the tunnel URL arrives). Only
                        // when the remote-access add-on is active — otherwise the
                        // phone pairs for LAN-only viewing.
                        if licenseStore.remoteActive, tunnelManager.isAvailable, tunnelManager.isRunning == false {
                            tunnelManager.start()
                        }
                        let code = pairingStore.generatePairingCode()
                        generatedQR = makeQR(for: pairingPayload(code: code.code))
                    } label: {
                        Label(
                            pairingStore.activePairingCode == nil ? "Pair iPhone" : "Generate New Code",
                            systemImage: "qrcode"
                        )
                    }
                    .buttonStyle(.borderedProminent)

                    if pairingStore.activePairingCode != nil {
                        Button("Clear") {
                            pairingStore.clearPairingCode()
                            generatedQR = nil
                        }
                        .buttonStyle(.bordered)
                    }
                }

                // Reflect remote-access state once a code is shown.
                if pairingStore.activePairingCode != nil {
                    if tunnelManager.tunnelURL != nil {
                        Label("Remote access active — works on cellular", systemImage: "checkmark.seal.fill")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.green)
                    } else if tunnelManager.isRunning {
                        Label("Starting remote access… QR updates automatically", systemImage: "clock")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    } else {
                        Label("LAN only — remote access is off", systemImage: "wifi")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            Spacer(minLength: 0)
            qrPreview
        }
    }

    /// True when remote access is coming up but the Cloudflare tunnel URL isn't
    /// ready yet — so any QR baked right now would carry ONLY the LAN address and
    /// a phone scanning it off-network could never connect. Hide the scannable QR
    /// until the remote URL is baked in (the onChange handler re-bakes it the
    /// instant the tunnel URL arrives).
    private var remoteQRPending: Bool {
        pairingStore.activePairingCode != nil
            && tunnelManager.isAvailable
            && tunnelManager.isRunning
            && tunnelManager.tunnelURL == nil
    }

    private var qrPreview: some View {
        Group {
            if remoteQRPending {
                ZStack {
                    RoundedRectangle(cornerRadius: 8)
                        .stroke(SentinelTheme.line, style: StrokeStyle(lineWidth: 1, dash: [4, 4]))
                        .frame(width: 172, height: 172)
                    VStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Preparing remote QR…\nso it works on cellular")
                            .font(.caption2)
                            .multilineTextAlignment(.center)
                            .foregroundStyle(.secondary)
                    }
                }
            } else if let img = generatedQR {
                Image(nsImage: img)
                    .resizable()
                    .interpolation(.none)
                    .frame(width: 160, height: 160)
                    .background(.white)
                    .padding(6)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
            } else {
                ZStack {
                    RoundedRectangle(cornerRadius: 8)
                        .stroke(SentinelTheme.line, style: StrokeStyle(lineWidth: 1, dash: [4, 4]))
                        .frame(width: 172, height: 172)
                    Text("QR shown\nafter generating")
                        .font(.caption2)
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private var devicesList: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Paired devices")
                .font(.caption.weight(.semibold))
            ForEach(pairingStore.pairedDevices) { device in
                HStack {
                    Image(systemName: "iphone")
                        .foregroundStyle(.secondary)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(device.name)
                            .font(.caption.weight(.semibold))
                        Text(lastSeenLabel(device))
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button {
                        pairingStore.revoke(deviceID: device.id)
                    } label: {
                        Image(systemName: "minus.circle")
                    }
                    .buttonStyle(.plain)
                    .help("Revoke access")
                }
                .padding(8)
                .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 6))
            }
        }
    }

    private var remoteAccessSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Remote Access (Cloudflare Tunnel)", systemImage: "globe")
                .font(.caption.weight(.semibold))

            // Remote access is free for everyone now — always show the controls.
            remoteControls
        }
    }

    private var remoteControls: some View {
        VStack(alignment: .leading, spacing: 8) {
            // Auto-start toggle — remote access starts with the app when subscribed.
            Toggle(isOn: $autoStartRemote) {
                Text("Turn on remote access automatically")
                    .font(.caption2)
            }
            .toggleStyle(.switch)
            .controlSize(.mini)
            .disabled(tunnelManager.isAvailable == false)
            .onChange(of: autoStartRemote) { enabled in
                RemoteAccessPreference.autoStart = enabled
                if enabled {
                    if tunnelManager.isAvailable, tunnelManager.isRunning == false {
                        tunnelManager.start()
                    }
                } else {
                    tunnelManager.stop()
                }
            }

            // Status row
            HStack(spacing: 6) {
                Circle()
                    .fill(tunnelStatusColor)
                    .frame(width: 7, height: 7)
                Text(tunnelManager.statusMessage)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            // Live tunnel URL — selectable so the user can copy it
            if let url = tunnelManager.tunnelURL {
                Text(url)
                    .font(.caption2.monospaced())
                    .foregroundStyle(SentinelTheme.accent)
                    .textSelection(.enabled)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }

            if let err = tunnelManager.startError {
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }

            // Controls
            HStack(spacing: 8) {
                if tunnelManager.isRunning {
                    Button("Stop Tunnel") {
                        tunnelManager.stop()
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                } else {
                    Button(tunnelManager.isAvailable ? "Start Remote Access" : "cloudflared not installed") {
                        tunnelManager.start()
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(tunnelManager.isAvailable == false)
                }
            }

            // Hint: paired phones auto-refresh the rotating URL on home Wi-Fi.
            if tunnelManager.isRunning && tunnelManager.tunnelURL != nil {
                Text("Already-paired phones pick up this address automatically next time they're on your Wi-Fi — no need to re-pair.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else if tunnelManager.isAvailable == false {
                Text("Place the cloudflared binary in ~/Library/Application Support/HandoffGridSentinel/")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var pushNotificationsSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Push Notifications (AI Alerts)", systemImage: "bell.badge")
                .font(.caption.weight(.semibold))

            HStack(spacing: 6) {
                Circle()
                    .fill(pushService.isConfigured ? Color.green : SentinelTheme.amber)
                    .frame(width: 7, height: 7)
                Text(pushService.statusMessage)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            let pushReady = pairingStore.pairedDevices.filter { $0.apnsPushToken != nil }.count
            Text("\(pushReady) of \(pairingStore.pairedDevices.count) paired device\(pairingStore.pairedDevices.count == 1 ? "" : "s") registered for push")
                .font(.caption2)
                .foregroundStyle(.secondary)

            if let err = pushService.lastError {
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if pushService.isConfigured == false {
                Text("Drop your Apple Push key (AuthKey_XXXXXXXXXX.p8) into the app-support folder, then click Reload.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack(spacing: 8) {
                Button("Reveal Key Folder") {
                    NSWorkspace.shared.activateFileViewerSelecting([pushService.keyDirectory])
                }
                .buttonStyle(.bordered)
                .controlSize(.small)

                Button("Reload") { pushService.loadConfiguration() }
                    .buttonStyle(.bordered)
                    .controlSize(.small)

                if pushService.isConfigured {
                    Button("Send Test") {
                        let tokens = pairingStore.pairedDevices.compactMap { $0.apnsPushToken }
                        Task {
                            await pushService.sendAlert(
                                title: "Sentinel VMS",
                                body: "Test alert — push notifications are working.",
                                deviceTokens: tokens
                            )
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(pairingStore.pairedDevices.contains { $0.apnsPushToken != nil } == false)
                }
            }

            if let sent = pushService.lastSentAt {
                Text("Last push \(sent, format: .relative(presentation: .named))")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var tunnelStatusColor: Color {
        if tunnelManager.startError != nil { return .red }
        if tunnelManager.tunnelURL != nil { return .green }
        if tunnelManager.isRunning { return SentinelTheme.amber }
        return .secondary
    }

    private func lastSeenLabel(_ device: PairedDevice) -> String {
        if let last = device.lastSeenAt {
            let f = RelativeDateTimeFormatter()
            return "Last seen \(f.localizedString(for: last, relativeTo: Date()))"
        }
        return "Not yet seen"
    }

    private func pairingPayload(code: String) -> String {
        let url = httpServer.listenURLs.first ?? "http://127.0.0.1:8090"
        var payload: [String: Any] = ["url": url, "code": code, "v": 1]
        if let remoteURL = tunnelManager.tunnelURL {
            payload["remoteURL"] = remoteURL
        }
        guard let data = try? JSONSerialization.data(withJSONObject: payload),
              let json = String(data: data, encoding: .utf8) else { return "" }
        return "sentinel-vms://pair?\(json.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? "")"
    }

    private func makeQR(for payload: String) -> NSImage? {
        let context = CIContext()
        let filter = CIFilter.qrCodeGenerator()
        filter.setValue(Data(payload.utf8), forKey: "inputMessage")
        filter.setValue("M", forKey: "inputCorrectionLevel")
        guard let outputImage = filter.outputImage else { return nil }
        let scaled = outputImage.transformed(by: CGAffineTransform(scaleX: 10, y: 10))
        guard let cgImage = context.createCGImage(scaled, from: scaled.extent) else { return nil }
        return NSImage(cgImage: cgImage, size: NSSize(width: 160, height: 160))
    }
}
