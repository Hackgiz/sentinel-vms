import SwiftUI
import SentinelCore
import SentinelMediaServer

// MARK: - Sheet container

struct AddCameraSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var onvifDiscoveryStore: ONVIFDiscoveryStore
    @State private var mode = AddCameraMode.byAddress

    var body: some View {
        VStack(spacing: 0) {
            // Header
            HStack(spacing: 12) {
                HandoffGridMark(size: 34)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Add Camera")
                        .font(.title3.weight(.semibold))
                    Text("Connect a camera to Sentinel VMS")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
            }
            .padding(.horizontal, 20)
            .padding(.top, 20)
            .padding(.bottom, 14)

            Picker("Mode", selection: $mode) {
                ForEach(AddCameraMode.allCases) { m in
                    Label(m.label, systemImage: m.symbol).tag(m)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(.horizontal, 20)
            .padding(.bottom, 14)

            Divider().overlay(SentinelTheme.line)

            switch mode {
            case .byAddress:
                ByAddressTab { dismiss() }
            case .networkScan:
                NetworkScanTab { dismiss() }
            case .rtspURL:
                RTSPURLTab { dismiss() }
            }
        }
        .frame(width: 580, height: 680)
        .background(SentinelTheme.background)
    }
}

private enum AddCameraMode: String, CaseIterable, Identifiable {
    case byAddress
    case networkScan
    case rtspURL

    var id: String { rawValue }

    var label: String {
        switch self {
        case .byAddress:   return "By Address"
        case .networkScan: return "Network Scan"
        case .rtspURL:     return "RTSP URL"
        }
    }

    var symbol: String {
        switch self {
        case .byAddress:   return "network"
        case .networkScan: return "dot.radiowaves.left.and.right"
        case .rtspURL:     return "link"
        }
    }
}

// MARK: - By Address tab

private enum AddressDetection {
    case idle
    case detecting
    case found(name: String, manufacturer: String, profiles: [ONVIFStreamProfile], serviceURL: String)
    case notFound(String)

    var isDetecting: Bool {
        if case .detecting = self { return true }
        return false
    }
}

private struct ByAddressTab: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var licenseStore: LicenseStore
    let dismiss: () -> Void

    @State private var ipAddress = ""
    @State private var username = ""
    @State private var password = ""
    @State private var detection: AddressDetection = .idle
    @State private var cameraName = ""
    @State private var cameraLocation = ""
    @State private var selectedProfileID: UUID?
    @State private var recordingEnabled = true
    @State private var recordingMode = CameraRecordingMode.continuous
    @State private var recordingCodec = CameraRecordingCodec.h265

    var body: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    connectionFields
                    if case .found(_, let manufacturer, let profiles, _) = detection {
                        detectionResult(manufacturer: manufacturer, profiles: profiles)
                    } else if case .notFound(let message) = detection {
                        notFoundBanner(message: message)
                    }
                    if let error = cameraStore.lastError {
                        Text(error).font(.caption).foregroundStyle(.red)
                    }
                }
                .padding(20)
            }

            Divider().overlay(SentinelTheme.line)

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                if case .found = detection {
                    Button {
                        addCamera()
                    } label: {
                        Label("Add Camera", systemImage: "plus.circle.fill")
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(cameraName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .keyboardShortcut(.defaultAction)
                }
            }
            .padding(20)
        }
        .onAppear {
            // Pre-fill the last-used camera login (most installs share one admin
            // account across all cameras), so adding camera #2..N is one tap.
            if username.isEmpty, password.isEmpty,
               let saved = CameraSecrets.rememberedCredentials() {
                username = saved.username
                password = saved.password
            }
        }
    }

    private var connectionFields: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Camera Address")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)

            TextField("IP address or hostname  —  e.g. 192.168.1.50", text: $ipAddress)
                .textFieldStyle(.roundedBorder)

            HStack(spacing: 8) {
                TextField("Username", text: $username)
                    .textFieldStyle(.roundedBorder)
                SecureField("Password", text: $password)
                    .textFieldStyle(.roundedBorder)
            }

            Button {
                Task { await runDetection() }
            } label: {
                HStack(spacing: 8) {
                    if detection.isDetecting { ProgressView().controlSize(.small) }
                    Text(detection.isDetecting ? "Detecting…" : "Connect & Auto-Detect")
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .buttonStyle(.borderedProminent)
            .disabled(ipAddress.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || detection.isDetecting)
        }
    }

    @ViewBuilder
    private func detectionResult(manufacturer: String, profiles: [ONVIFStreamProfile]) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            Divider().overlay(SentinelTheme.line)

            HStack(spacing: 8) {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                Text("Camera detected via ONVIF")
                    .font(.caption.weight(.semibold)).foregroundStyle(.green)
                Spacer()
                if manufacturer.isEmpty == false {
                    Text(manufacturer).font(.caption).foregroundStyle(.secondary)
                }
            }

            if profiles.isEmpty == false {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Stream Profile")
                        .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    Picker("Profile", selection: Binding(
                        get: { selectedProfileID ?? profiles.preferredForLiveView?.id ?? UUID() },
                        set: { selectedProfileID = $0 }
                    )) {
                        ForEach(profiles) { p in
                            Text("\(p.name) · \(p.resolution)\(p.fps > 0 ? " · \(p.fps) FPS" : "")").tag(p.id)
                        }
                    }
                    .pickerStyle(.menu)
                }
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Camera Name")
                    .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                TextField("Name", text: $cameraName).textFieldStyle(.roundedBorder)
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Location")
                    .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                TextField("e.g. Front Entrance, Parking Lot", text: $cameraLocation)
                    .textFieldStyle(.roundedBorder)
            }

            HStack(spacing: 10) {
                Toggle("Record", isOn: $recordingEnabled).toggleStyle(.checkbox)
                Picker("Mode", selection: $recordingMode) {
                    ForEach(CameraRecordingMode.allCases) { m in Text(m.label).tag(m) }
                }
                .pickerStyle(.segmented).frame(width: 180)
                Picker("Codec", selection: $recordingCodec) {
                    ForEach(CameraRecordingCodec.allCases) { c in Text(c.shortLabel).tag(c) }
                }
                .pickerStyle(.segmented).frame(width: 120)
            }
        }
    }

    @ViewBuilder
    private func notFoundBanner(message: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(SentinelTheme.amber)
                Text("ONVIF not detected").font(.caption.weight(.semibold)).foregroundStyle(SentinelTheme.amber)
            }
            Text(message).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Text("Try the RTSP URL tab to add this camera manually.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .padding(12)
        .background(SentinelTheme.amber.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
    }

    private func runDetection() async {
        detection = .detecting
        selectedProfileID = nil
        // Do NOT wipe cameraName here. A retry (e.g. after fixing a mistyped
        // password) must keep any name the user already typed; we only auto-fill
        // it below when it's still empty.

        let ip = ipAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        let serviceURL = "http://\(ip)/onvif/device_service"
        let credentials = ONVIFCredentials(
            username: username.trimmingCharacters(in: .whitespacesAndNewlines),
            password: password
        )

        // Fingerprint first so we have a vendor preset ready as a fallback.
        let fingerprint = await CameraFingerprinter.fingerprint(host: ip)
        let manufacturer = fingerprint?.manufacturer ?? .generic

        do {
            let details = try await ONVIFSOAPClient.fetchCameraDetails(
                serviceURL: serviceURL, fallbackHost: ip, credentials: credentials)
            if cameraName.isEmpty { cameraName = details.displayName(fallback: "Camera at \(ip)") }
            // Default to a real video profile (H.265/H.264), not whatever ONVIF
            // lists first — many cameras (e.g. Hanwha) put an MJPEG profile1
            // first, which can't be viewed live over HLS.
            selectedProfileID = details.profiles.preferredForLiveView?.id
            detection = .found(
                name: cameraName,
                manufacturer: details.manufacturer ?? manufacturer.displayName,
                profiles: details.profiles,
                serviceURL: serviceURL
            )
        } catch {
            // ONVIF failed — if fingerprinting identified a vendor, hand back
            // a preset RTSP URL so the user can still add the camera.
            if fingerprint != nil {
                let presetURL = RTSPURLPresets.renderedMainStream(for: manufacturer, host: ip)
                let presetSub = RTSPURLPresets.renderedSubStream(for: manufacturer, host: ip)
                var profiles = [
                    ONVIFStreamProfile(name: "\(manufacturer.displayName) Main", resolution: "Pending", fps: 0, rtspURL: presetURL)
                ]
                if let presetSub {
                    profiles.append(
                        ONVIFStreamProfile(name: "\(manufacturer.displayName) Sub", resolution: "Pending", fps: 0, rtspURL: presetSub)
                    )
                }
                if cameraName.isEmpty { cameraName = "\(manufacturer.displayName) at \(ip)" }
                selectedProfileID = profiles.preferredForLiveView?.id
                detection = .found(
                    name: cameraName,
                    manufacturer: manufacturer.displayName,
                    profiles: profiles,
                    serviceURL: serviceURL
                )
            } else {
                detection = .notFound(error.localizedDescription)
            }
        }
    }

    private func addCamera() {
        guard case .found(_, _, let profiles, _) = detection else { return }
        let profile = profiles.first { $0.id == selectedProfileID } ?? profiles.preferredForLiveView
        guard let profile else { return }

        var draft = NewCameraDraft()
        draft.name = cameraName.trimmingCharacters(in: .whitespacesAndNewlines)
        draft.location = cameraLocation.trimmingCharacters(in: .whitespacesAndNewlines)
        draft.rtspURL = profile.rtspURL
        draft.username = username.trimmingCharacters(in: .whitespacesAndNewlines)
        draft.password = password
        draft.profile = profile.name
        draft.recordingEnabled = recordingEnabled
        draft.recordingMode = recordingMode
        draft.recordingCodec = recordingCodec

        if cameraStore.addCamera(from: draft, limit: licenseStore.cameraLimit) {
            // Remember this login so the next camera (often the same admin
            // account) pre-fills instead of being re-typed.
            CameraSecrets.saveRememberedCredentials(
                username: draft.username,
                password: draft.password
            )
            dismiss()
        }
    }
}

// MARK: - Network Scan tab

private struct NetworkScanTab: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var onvifDiscoveryStore: ONVIFDiscoveryStore
    let dismiss: () -> Void

    @State private var username = ""
    @State private var password = ""
    @State private var selectedProfileIDs: [UUID: UUID] = [:]

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    TextField("Username", text: $username).textFieldStyle(.roundedBorder)
                    SecureField("Password", text: $password).textFieldStyle(.roundedBorder)
                    Button {
                        selectedProfileIDs = [:]
                        onvifDiscoveryStore.scanAndAutoFetch(username: username, password: password)
                    } label: {
                        Label(
                            onvifDiscoveryStore.state == .scanning ? "Scanning…" : "Scan",
                            systemImage: "dot.radiowaves.left.and.right"
                        )
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(onvifDiscoveryStore.state == .scanning)
                    Button {
                        onvifDiscoveryStore.deepScan(username: username, password: password)
                    } label: {
                        Label("Deep", systemImage: "magnifyingglass.circle.fill")
                    }
                    .buttonStyle(.bordered)
                    .help("Probe every host on the local subnet — use if Scan finds nothing.")
                    .disabled(onvifDiscoveryStore.state == .scanning)
                }

                DiscoveryPhasesView(phaseStates: onvifDiscoveryStore.phaseStates)

                if case .failed(let msg) = onvifDiscoveryStore.state {
                    Text(msg).font(.caption).foregroundStyle(.red)
                } else if case .found(let count) = onvifDiscoveryStore.state {
                    Text("\(count) camera\(count == 1 ? "" : "s") found — tap a row to add.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 14)

            Divider().overlay(SentinelTheme.line)

            if onvifDiscoveryStore.discoveredCameras.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "dot.radiowaves.left.and.right")
                        .font(.system(size: 32))
                        .foregroundStyle(.secondary)
                    Text("Enter credentials and tap Scan")
                        .font(.callout).foregroundStyle(.secondary)
                }
                Spacer()
            } else {
                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(onvifDiscoveryStore.discoveredCameras) { camera in
                            ScanResultRow(
                                camera: camera,
                                profileState: onvifDiscoveryStore.profileState(for: camera.id),
                                isAlreadyAdded: cameraStore.cameras.contains { $0.ipAddress == camera.host },
                                selectedProfileID: Binding(
                                    get: { selectedProfileIDs[camera.id] ?? camera.primaryProfile?.id ?? UUID() },
                                    set: { selectedProfileIDs[camera.id] = $0 }
                                ),
                                onAdd: {
                                    let profileID = selectedProfileIDs[camera.id]
                                    let profile = camera.profiles.first { $0.id == profileID } ?? camera.primaryProfile
                                    guard let profile else { return }
                                    cameraStore.addDiscoveredCamera(camera, profile: profile,
                                                                    username: username, password: password)
                                    CameraSecrets.saveRememberedCredentials(username: username, password: password)
                                    dismiss()
                                }
                            )
                        }
                    }
                    .padding(.horizontal, 20)
                    .padding(.vertical, 12)
                }
            }

            Divider().overlay(SentinelTheme.line)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            .padding(20)
        }
        .onAppear {
            if username.isEmpty, password.isEmpty,
               let saved = CameraSecrets.rememberedCredentials() {
                username = saved.username
                password = saved.password
            }
        }
    }
}

private struct ScanResultRow: View {
    let camera: ONVIFDiscoveredCamera
    let profileState: ONVIFProfileLoadState
    let isAlreadyAdded: Bool
    @Binding var selectedProfileID: UUID
    let onAdd: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Text(camera.name).font(.caption.weight(.semibold)).lineLimit(1)
                    if isAlreadyAdded {
                        Text("Added").font(.caption2.weight(.semibold)).foregroundStyle(.green)
                            .padding(.horizontal, 6).padding(.vertical, 2)
                            .background(.green.opacity(0.12), in: Capsule())
                    }
                }
                Text("\(camera.host) · \(camera.manufacturer)")
                    .font(.caption2).foregroundStyle(.secondary).lineLimit(1)

                if profileState.isLoaded && camera.profiles.isEmpty == false {
                    Picker("", selection: $selectedProfileID) {
                        ForEach(camera.profiles) { p in
                            Text("\(p.name) · \(p.resolution)").tag(p.id)
                        }
                    }
                    .labelsHidden().controlSize(.small)
                    .onAppear {
                        if let first = camera.primaryProfile { selectedProfileID = first.id }
                    }
                } else if profileState == .loading {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.mini)
                        Text("Fetching profiles…").font(.caption2).foregroundStyle(.secondary)
                    }
                }
            }

            Spacer()

            if isAlreadyAdded {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
            } else if profileState.isLoaded {
                Button {
                    onAdd()
                } label: {
                    Label("Add", systemImage: "plus.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
            } else if profileState.failureMessage != nil {
                Image(systemName: "exclamationmark.circle").foregroundStyle(.red)
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
        .overlay {
            RoundedRectangle(cornerRadius: 8).stroke(SentinelTheme.line, lineWidth: 1)
        }
        .opacity(isAlreadyAdded ? 0.55 : 1)
    }
}

// MARK: - RTSP URL tab

private struct RTSPURLTab: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var licenseStore: LicenseStore
    let dismiss: () -> Void
    @State private var draft = NewCameraDraft()

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section("Camera") {
                    TextField("Name", text: $draft.name)
                    TextField("Location", text: $draft.location)
                }

                Section("Stream") {
                    TextField("RTSP URL  —  rtsp://192.168.1.50:554/stream1", text: $draft.rtspURL)
                    TextField("Username", text: $draft.username)
                    SecureField("Password", text: $draft.password)
                    Picker("Profile", selection: $draft.profile) {
                        Text("Main Stream").tag("Main Stream")
                        Text("Sub Stream").tag("Sub Stream")
                        Text("Low Motion").tag("Low Motion")
                    }
                }

                Section("Recording") {
                    Toggle("Record this camera", isOn: $draft.recordingEnabled)
                        .toggleStyle(.checkbox)
                    Picker("Mode", selection: $draft.recordingMode) {
                        ForEach(CameraRecordingMode.allCases) { m in Text(m.label).tag(m) }
                    }
                    Picker("Codec", selection: $draft.recordingCodec) {
                        ForEach(CameraRecordingCodec.allCases) { c in Text(c.label).tag(c) }
                    }
                    Text(draft.recordingMode.detail).font(.caption).foregroundStyle(.secondary)
                }

                if let message = draft.validationMessage {
                    Text(message).font(.caption).foregroundStyle(.orange)
                }
                if let message = cameraStore.lastError {
                    Text(message).font(.caption).foregroundStyle(.red)
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
            .padding(.horizontal, 12)

            Divider().overlay(SentinelTheme.line)

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button {
                    if cameraStore.addCamera(from: draft, limit: licenseStore.cameraLimit) { dismiss() }
                } label: {
                    Label("Add Camera", systemImage: "plus.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .disabled(draft.isValid == false)
                .keyboardShortcut(.defaultAction)
            }
            .padding(20)
        }
    }
}
