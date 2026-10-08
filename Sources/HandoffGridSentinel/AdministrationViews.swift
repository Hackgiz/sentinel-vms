import SwiftUI
import SentinelCore
import SentinelMediaServer

struct CamerasView: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var onvifDiscoveryStore: ONVIFDiscoveryStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var licenseStore: LicenseStore
    @EnvironmentObject private var commandCenter: SentinelCommandCenter
    let addCamera: () -> Void
    @State private var isConfirmingClearDemo = false
    @State private var isConfirmingClearAll = false
    @State private var editingCamera: CameraFeed?

    private var canManageCameras: Bool { operatorSessionStore.can(.addCamera) }

    var body: some View {
        HStack(spacing: 0) {
            ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 10) {
                    Label("Camera Inventory", systemImage: "camera.fill")
                        .font(.headline)

                    // Camera count + free/unlimited badge.
                    Text("\(cameraStore.cameraCountTowardLimit()) camera\(cameraStore.cameraCountTowardLimit() == 1 ? "" : "s")")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(Color.secondary.opacity(0.12), in: Capsule())

                    Label("Unlimited · free", systemImage: "checkmark.circle")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.green)
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(Color.green.opacity(0.12), in: Capsule())
                        .help("Sentinel VMS is free — add as many cameras as you like.")

                    Spacer()

                    Button(action: addCamera) {
                        Label("Add Camera", systemImage: "plus.circle.fill")
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!canManageCameras)
                    .help("Add a camera via RTSP URL")

                    Button {
                        cameraStore.addLocalCamera()
                    } label: {
                        Label("Local Camera", systemImage: "video.fill")
                    }
                    .disabled(!canManageCameras)

                    Menu {
                        Button {
                            onvifDiscoveryStore.scan()
                        } label: {
                            Label("Discover ONVIF", systemImage: "magnifyingglass")
                        }
                        .disabled(onvifDiscoveryStore.state == .scanning || !canManageCameras)

                        if cameraStore.demoCameraCount > 0 {
                            Divider()
                            Button(role: .destructive) {
                                isConfirmingClearDemo = true
                            } label: {
                                Label("Clear Demo Cameras", systemImage: "trash")
                            }
                            .disabled(!canManageCameras)
                        }

                        Divider()
                        Button(role: .destructive) {
                            isConfirmingClearAll = true
                        } label: {
                            Label("Clear All Cameras", systemImage: "trash.slash")
                        }
                        .disabled(cameraStore.cameras.isEmpty || !canManageCameras)
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                    .menuStyle(.borderlessButton)
                    .fixedSize()
                }

                if let message = cameraStore.lastError {
                    Text(message)
                        .font(.caption)
                        .foregroundStyle(.orange)
                }

                CameraAdministrationSummary()

                SentinelPanel("Configured Cameras") {
                    VStack(spacing: 8) {
                        if cameraStore.cameras.isEmpty {
                            EmptyStateLine(text: "No cameras configured. Add a real RTSP camera, discover ONVIF cameras, or add a local camera.")
                        } else {
                            CameraHeaderRow()

                            ForEach(cameraStore.cameras) { camera in
                                CameraInventoryRow(
                                    camera: camera,
                                    edit: {
                                        editingCamera = camera
                                    },
                                    delete: {
                                        deleteCamera(camera)
                                    }
                                )
                            }
                        }
                    }
                }

                CameraSitesPanel()
            }
            .padding(14)
            } // ScrollView

            Divider()
                .overlay(SentinelTheme.line)

            ONVIFDiscoveryPanel()
                .frame(width: 380)
                .background(SentinelTheme.chrome)
        }
        .background(SentinelTheme.background)
        .confirmationDialog("Remove demo cameras?", isPresented: $isConfirmingClearDemo) {
            Button("Remove Demo Cameras", role: .destructive) {
                stopIngest(for: cameraStore.cameras.filter(\.isDemoCamera))
                cameraStore.deleteDemoCameras()
            }
        } message: {
            Text("This removes only the starter cameras. Real RTSP, ONVIF, and local cameras stay configured.")
        }
        .confirmationDialog("Remove all configured cameras?", isPresented: $isConfirmingClearAll) {
            Button("Remove All Cameras", role: .destructive) {
                stopIngest(for: cameraStore.cameras)
                cameraStore.deleteAllCameras()
            }
        } message: {
            Text("This clears every configured camera and removes saved camera credentials.")
        }
        .sheet(item: $editingCamera) { camera in
            EditCameraSheet(camera: camera)
        }
    }

    private func deleteCamera(_ camera: CameraFeed) {
        stopIngest(for: [camera])
        cameraStore.deleteCamera(camera)
    }

    private func stopIngest(for cameras: [CameraFeed]) {
        for camera in cameras {
            mediaIngestStore.stopLiveBridge(for: camera.id)
            mediaIngestStore.stopRecording(for: camera.id)
        }
        mediaEngineStore.stopExternalPreviews()
    }
}

struct ONVIFDiscoveryPanel: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var onvifDiscoveryStore: ONVIFDiscoveryStore
    @State private var username = ""
    @State private var password = ""
    @State private var selectedCameraIDs = Set<UUID>()
    @State private var selectedProfileIDs: [UUID: UUID] = [:]

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("ONVIF Discovery")
                        .font(.headline)
                    Text("Scan your network — profiles load automatically.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Circle()
                    .fill(onvifDiscoveryStore.state.tint)
                    .frame(width: 9, height: 9)
            }

            VStack(spacing: 8) {
                HStack(spacing: 8) {
                    TextField("Username", text: $username)
                        .textFieldStyle(.roundedBorder)
                    SecureField("Password", text: $password)
                        .textFieldStyle(.roundedBorder)
                }

                HStack(spacing: 8) {
                    Button {
                        selectedCameraIDs = []
                        selectedProfileIDs = [:]
                        onvifDiscoveryStore.scanAndAutoFetch(username: username, password: password)
                    } label: {
                        Label(
                            onvifDiscoveryStore.state == .scanning ? "Scanning…" : "Scan Network",
                            systemImage: "dot.radiowaves.left.and.right"
                        )
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(onvifDiscoveryStore.state == .scanning)

                    Button {
                        onvifDiscoveryStore.deepScan(username: username, password: password)
                    } label: {
                        Label("Deep Scan", systemImage: "magnifyingglass.circle.fill")
                    }
                    .buttonStyle(.bordered)
                    .help("Probe every host on your subnet (only when ONVIF/Bonjour find nothing).")
                    .disabled(onvifDiscoveryStore.state == .scanning)

                    if onvifDiscoveryStore.discoveredCameras.isEmpty == false {
                        Button {
                            onvifDiscoveryStore.reset()
                            selectedCameraIDs = []
                            selectedProfileIDs = [:]
                        } label: {
                            Image(systemName: "xmark.circle")
                        }
                        .help("Clear results")
                    }
                }
            }

            DiscoveryPhasesView(phaseStates: onvifDiscoveryStore.phaseStates)

            if case .failed(let message) = onvifDiscoveryStore.state {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if onvifDiscoveryStore.discoveredCameras.isEmpty {
                Divider().overlay(SentinelTheme.line)
                VStack(alignment: .leading, spacing: 8) {
                    EmptyStateLine(text: "Enter credentials and tap Scan Network.")
                    EmptyStateLine(text: "Profiles are fetched automatically for all found cameras.")
                }
            } else {
                Divider().overlay(SentinelTheme.line)

                HStack {
                    Button {
                        let addable = addableCameras
                        if selectedCameraIDs.count == addable.count {
                            selectedCameraIDs = []
                        } else {
                            selectedCameraIDs = Set(addable.map(\.id))
                        }
                    } label: {
                        Text(selectedCameraIDs.count == addableCameras.count && addableCameras.isEmpty == false ? "Deselect All" : "Select All")
                    }
                    .buttonStyle(.plain)
                    .font(.caption)
                    .foregroundStyle(SentinelTheme.accent)
                    .disabled(addableCameras.isEmpty)

                    Spacer()

                    if selectedCameraIDs.isEmpty == false {
                        Button {
                            addSelectedCameras()
                        } label: {
                            Label(
                                "Add \(selectedCameraIDs.count) Camera\(selectedCameraIDs.count == 1 ? "" : "s")",
                                systemImage: "plus.circle.fill"
                            )
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                    }
                }

                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(onvifDiscoveryStore.discoveredCameras) { camera in
                            ONVIFCameraRow(
                                camera: camera,
                                profileState: onvifDiscoveryStore.profileState(for: camera.id),
                                isAlreadyAdded: isAlreadyAdded(camera),
                                isSelected: selectedCameraIDs.contains(camera.id),
                                selectedProfileID: selectedProfileBinding(for: camera),
                                toggle: { toggleSelection(camera) }
                            )
                        }
                    }
                }
            }

            Spacer(minLength: 0)
        }
        .padding(14)
    }

    private var addableCameras: [ONVIFDiscoveredCamera] {
        onvifDiscoveryStore.discoveredCameras.filter {
            !isAlreadyAdded($0) && onvifDiscoveryStore.profileState(for: $0.id).isLoaded
        }
    }

    private func isAlreadyAdded(_ camera: ONVIFDiscoveredCamera) -> Bool {
        cameraStore.cameras.contains { $0.ipAddress == camera.host }
    }

    private func toggleSelection(_ camera: ONVIFDiscoveredCamera) {
        guard !isAlreadyAdded(camera),
              onvifDiscoveryStore.profileState(for: camera.id).isLoaded else { return }
        if selectedCameraIDs.contains(camera.id) {
            selectedCameraIDs.remove(camera.id)
        } else {
            selectedCameraIDs.insert(camera.id)
        }
    }

    private func selectedProfileBinding(for camera: ONVIFDiscoveredCamera) -> Binding<UUID> {
        Binding(
            get: { selectedProfileIDs[camera.id] ?? camera.primaryProfile?.id ?? UUID() },
            set: { selectedProfileIDs[camera.id] = $0 }
        )
    }

    private func addSelectedCameras() {
        for camera in onvifDiscoveryStore.discoveredCameras where selectedCameraIDs.contains(camera.id) {
            let profileID = selectedProfileIDs[camera.id]
            guard let profile = camera.profiles.first(where: { $0.id == profileID }) ?? camera.primaryProfile else { continue }
            cameraStore.addDiscoveredCamera(camera, profile: profile, username: username, password: password)
        }
        selectedCameraIDs = []
    }
}

struct ONVIFCameraRow: View {
    let camera: ONVIFDiscoveredCamera
    let profileState: ONVIFProfileLoadState
    let isAlreadyAdded: Bool
    let isSelected: Bool
    @Binding var selectedProfileID: UUID
    let toggle: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            stateIndicator

            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Text(camera.name)
                        .font(.caption.weight(.semibold))
                        .lineLimit(1)

                    if isAlreadyAdded {
                        Text("Added")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.green)
                            .padding(.horizontal, 6).padding(.vertical, 2)
                            .background(.green.opacity(0.12), in: Capsule())
                    }
                }

                Text("\(camera.host) · \(camera.manufacturer)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)

                if profileState.isLoaded && camera.profiles.isEmpty == false {
                    Picker("Profile", selection: $selectedProfileID) {
                        ForEach(camera.profiles) { profile in
                            Text("\(profile.encoding.map { "\($0) · " } ?? "")\(profile.name) · \(profile.resolution) · \(profile.fps) FPS")
                                .tag(profile.id)
                        }
                    }
                    .labelsHidden()
                    .controlSize(.small)
                    .onAppear {
                        if selectedProfileID == UUID(), let first = camera.primaryProfile {
                            selectedProfileID = first.id
                        }
                    }
                } else if profileState == .loading {
                    Text("Fetching profiles…")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                } else if let error = profileState.failureMessage {
                    Text(error)
                        .font(.caption2)
                        .foregroundStyle(.red)
                        .lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }

            Spacer(minLength: 0)
        }
        .padding(10)
        .background(
            isSelected ? SentinelTheme.accent.opacity(0.10) : SentinelTheme.panelRaised,
            in: RoundedRectangle(cornerRadius: 8)
        )
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(
                    isSelected ? SentinelTheme.accent.opacity(0.5) : SentinelTheme.line,
                    lineWidth: 1
                )
        }
        .opacity(isAlreadyAdded ? 0.55 : 1)
        .contentShape(RoundedRectangle(cornerRadius: 8))
        .onTapGesture { toggle() }
    }

    @ViewBuilder
    private var stateIndicator: some View {
        if isAlreadyAdded {
            Image(systemName: "checkmark.circle.fill")
                .foregroundStyle(.green)
                .frame(width: 22)
        } else if profileState.isLoaded {
            Image(systemName: isSelected ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(isSelected ? SentinelTheme.accent : .secondary)
                .frame(width: 22)
        } else if profileState == .loading {
            ProgressView().controlSize(.small).frame(width: 22)
        } else if profileState.failureMessage != nil {
            Image(systemName: "exclamationmark.circle")
                .foregroundStyle(.red)
                .frame(width: 22)
        } else {
            Image(systemName: "circle")
                .foregroundStyle(.secondary)
                .frame(width: 22)
        }
    }
}

struct CameraSitesPanel: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore

    private var siteSummaries: [CameraSiteSummary] {
        Dictionary(grouping: cameraStore.cameras) { camera in
            camera.location.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Unassigned" : camera.location
        }
        .map { location, cameras in
            CameraSiteSummary(
                id: location,
                location: location,
                cameras: cameras.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending },
                onlineCount: cameras.filter { mediaIngestStore.effectiveStatus(for: $0) != .offline }.count,
                recordingCount: cameras.filter(\.isRecording).count
            )
        }
        .sorted { first, second in
            first.location.localizedCaseInsensitiveCompare(second.location) == .orderedAscending
        }
    }

    var body: some View {
        SentinelPanel("Camera Sites", systemImage: "building.2.fill") {
            if siteSummaries.isEmpty {
                EmptyStateLine(text: "Sites appear here as cameras are assigned locations.")
            } else {
                LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                    ForEach(siteSummaries) { site in
                        VStack(alignment: .leading, spacing: 8) {
                            HStack(alignment: .firstTextBaseline) {
                                Text(site.location)
                                    .font(.headline)
                                    .lineLimit(1)

                                Spacer()

                                Text("\(site.onlineCount)/\(site.cameras.count) online")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(site.onlineCount == site.cameras.count ? Color.green : SentinelTheme.amber)
                            }

                            Text("\(site.recordingCount) armed for recording")
                                .font(.caption)
                                .foregroundStyle(.secondary)

                            Text(site.cameras.map(\.name).joined(separator: ", "))
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                                .lineLimit(2)
                        }
                        .padding(12)
                        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                    }
                }
            }
        }
    }
}

struct CameraSiteSummary: Identifiable {
    let id: String
    let location: String
    let cameras: [CameraFeed]
    let onlineCount: Int
    let recordingCount: Int
}

struct CameraHeaderRow: View {
    var body: some View {
        HStack {
            Text("Camera").frame(maxWidth: .infinity, alignment: .leading)
            Text("Signal").frame(width: 128, alignment: .leading)
            Text("Recording").frame(width: 210, alignment: .leading)
            Text("Health").frame(width: 170, alignment: .leading)
            Text("Actions").frame(width: 74, alignment: .trailing)
        }
        .font(.caption.weight(.semibold))
        .foregroundStyle(.secondary)
        .padding(.horizontal, 12)
    }
}

struct CameraInventoryRow: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var cameraCredentialStore: CameraCredentialStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    let camera: CameraFeed
    let edit: () -> Void
    let delete: () -> Void
    @State private var launchResult: MediaPipelineLaunchResult?

    var body: some View {
        let status = mediaIngestStore.effectiveStatus(for: camera)
        let segments = mediaIngestStore.segments(for: camera.id)
        let isRecording = mediaIngestStore.isRecording(cameraID: camera.id)

        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 5) {
                    HStack(spacing: 8) {
                        Image(systemName: camera.isLocalCamera ? "web.camera.fill" : "camera.fill")
                            .foregroundStyle(status.tint)

                        Text(camera.name)
                            .font(.headline)
                    }

                    Text("\(camera.location) · \(camera.profile)")
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    Text(camera.rtspURL.isEmpty ? camera.ipAddress : RTSPCredentialFormatter.redacted(camera.rtspURL))
                        .font(.caption2.monospaced())
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                VStack(alignment: .leading, spacing: 6) {
                    StatusBadge(status: status)

                    AdminInfoPill(
                        text: camera.isLocalCamera ? "Local" : "RTSP",
                        systemImage: camera.isLocalCamera ? "desktopcomputer" : "network",
                        tint: camera.isLocalCamera ? SentinelTheme.accent : .secondary
                    )
                }
                .frame(width: 128, alignment: .leading)

                VStack(alignment: .leading, spacing: 8) {
                    Toggle(
                        isOn: Binding(
                            get: { camera.isRecording },
                            set: { cameraStore.setRecordingEnabled($0, for: camera.id) }
                        )
                    ) {
                        Text(camera.isRecording ? "Armed" : "Manual")
                            .font(.caption.weight(.semibold))
                    }
                    .toggleStyle(.checkbox)
                    .disabled(camera.isLocalCamera)
                    .help(camera.isLocalCamera ? "Local AVFoundation cameras are preview-only here." : "Marks this camera as recording-enabled in the inventory.")

                    Picker(
                        "Mode",
                        selection: Binding(
                            get: { camera.recordingMode },
                            set: { cameraStore.setRecordingMode($0, for: camera.id) }
                        )
                    ) {
                        ForEach(CameraRecordingMode.allCases) { mode in
                            Text(mode.label).tag(mode)
                        }
                    }
                    .pickerStyle(.segmented)
                    .controlSize(.small)
                    .disabled(camera.isLocalCamera)
                    .help("Continuous keeps all clips. Motion keeps clips around detected activity.")

                    Picker(
                        "Codec",
                        selection: Binding(
                            get: { camera.recordingCodec },
                            set: { cameraStore.setRecordingCodec($0, for: camera.id) }
                        )
                    ) {
                        ForEach(CameraRecordingCodec.allCases) { codec in
                            Text(codec.shortLabel).tag(codec)
                        }
                    }
                    .pickerStyle(.segmented)
                    .controlSize(.small)
                    .disabled(camera.isLocalCamera || isRecording)
                    .help(isRecording ? "Stop recording before changing the recording codec." : "Choose H.264 compatibility or H.265/HEVC storage efficiency.")

                    Button {
                        handleRecordingToggle()
                    } label: {
                        Label(isRecording ? "Stop Recording" : "Start Recording", systemImage: isRecording ? "stop.fill" : "record.circle")
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .disabled(recordingActionDisabled)
                }
                .frame(width: 210, alignment: .leading)

                VStack(alignment: .leading, spacing: 6) {
                    AdminInfoPill(
                        text: "\(segments.count) segment\(segments.count == 1 ? "" : "s")",
                        systemImage: "film.stack.fill",
                        tint: segments.isEmpty ? .secondary : SentinelTheme.accent
                    )

                    AdminInfoPill(
                        text: camera.recordingCodec.shortLabel,
                        systemImage: "video.badge.waveform",
                        tint: camera.recordingCodec == .h265 ? SentinelTheme.motion : SentinelTheme.accent
                    )

                    AdminInfoPill(
                        text: camera.recordingMode.label,
                        systemImage: camera.recordingMode == .motion ? "figure.walk.motion" : "record.circle",
                        tint: camera.recordingMode == .motion ? SentinelTheme.motion : SentinelTheme.accent
                    )

                    AdminInfoPill(
                        text: credentialLabel,
                        systemImage: credentialSymbol,
                        tint: credentialTint
                    )
                }
                .frame(width: 170, alignment: .leading)

                HStack(spacing: 6) {
                    Button(action: edit) {
                        Image(systemName: "pencil")
                    }
                    .buttonStyle(.borderless)
                    .help("Edit camera display name and location")

                    Button(role: .destructive, action: delete) {
                        Image(systemName: "trash")
                    }
                    .buttonStyle(.borderless)
                    .help("Delete camera")
                }
                .frame(width: 74, alignment: .trailing)
            }

            if let launchResult {
                Text(launchResult.title)
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(launchResult.didLaunch ? .green : .red)
                    .lineLimit(1)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background((launchResult.didLaunch ? Color.green : Color.red).opacity(0.10), in: RoundedRectangle(cornerRadius: 8))
                    .help(launchResult.detail)
            }
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(status.tint.opacity(status == .offline ? 0.34 : 0.18), lineWidth: 1)
        }
    }

    private var recordingActionDisabled: Bool {
        camera.isLocalCamera ||
        camera.rtspURL.isEmpty ||
        (mediaIngestStore.isRecording(cameraID: camera.id) == false && mediaEngineStore.snapshot.gstreamerLaunchPath == nil)
    }

    private var credentialLabel: String {
        if camera.isLocalCamera {
            return "No credential"
        }

        if cameraCredentialStore.hasSessionAccess {
            return cameraCredentialStore.accessStatusLabel
        }

        if camera.hasEmbeddedRTSPCredentials {
            return "Unattended"
        }

        return camera.username.isEmpty ? "No credential" : "Credential set"
    }

    private var credentialSymbol: String {
        credentialLabel == "No credential" ? "lock.slash" : "key.fill"
    }

    private var credentialTint: Color {
        credentialLabel == "No credential" ? .secondary : SentinelTheme.amber
    }

    private func handleRecordingToggle() {
        if mediaIngestStore.isRecording(cameraID: camera.id) {
            mediaIngestStore.stopRecording(for: camera.id)
            launchResult = MediaPipelineLaunchResult(
                didLaunch: true,
                title: "Recording Stopped",
                detail: "\(camera.name) is no longer writing local segments.",
                commandPreview: ""
            )
            return
        }

        Task {
            launchResult = await mediaIngestStore.startRecording(
                for: camera,
                mediaEngine: mediaEngineStore,
                credentials: cameraCredentialStore
            )
        }
    }
}

struct EditCameraSheet: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    let camera: CameraFeed
    @State private var draft: CameraDisplayDraft

    init(camera: CameraFeed) {
        self.camera = camera
        _draft = State(initialValue: CameraDisplayDraft(camera: camera))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 12) {
                HandoffGridMark(size: 34)

                VStack(alignment: .leading, spacing: 2) {
                    Text("Edit Camera")
                        .font(.title3.weight(.semibold))

                    Text(camera.name)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }

                Spacer()
            }
            .padding(20)

            Divider()
                .overlay(SentinelTheme.line)

            Form {
                Section("Display") {
                    TextField("Display Name", text: $draft.name)
                    TextField("Location", text: $draft.location)
                    TextField("Profile Label", text: $draft.profile)
                }

                if camera.isLocalCamera == false {
                    Section("Credential") {
                        TextField("Username", text: $draft.username)
                        SecureField(camera.hasEmbeddedRTSPCredentials ? "Replace Password" : "Password", text: $draft.password)

                        HStack {
                            Text("Stored Mode")
                            Spacer()
                            Text(camera.hasEmbeddedRTSPCredentials ? "Unattended" : "Session")
                                .foregroundStyle(camera.hasEmbeddedRTSPCredentials ? Color.green : SentinelTheme.amber)
                        }
                    }
                }

                Section("Stream") {
                    Text(RTSPCredentialFormatter.redacted(camera.rtspURL.isEmpty ? camera.ipAddress : camera.rtspURL))
                        .font(.caption.monospaced())
                        .lineLimit(3)
                        .truncationMode(.middle)

                    if camera.isLocalCamera == false {
                        VStack(alignment: .leading, spacing: 4) {
                            HStack {
                                Text("Low-res sub-stream URL")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                Spacer()
                                if let suggested = RTSPURLPresets.deriveSubStream(fromMain: camera.rtspURL),
                                   draft.subStreamRTSPURL.isEmpty {
                                    Button("Auto-fill") { draft.subStreamRTSPURL = suggested }
                                        .font(.caption2)
                                        .buttonStyle(.borderless)
                                }
                            }
                            TextField("rtsp://", text: $draft.subStreamRTSPURL)
                                .font(.caption.monospaced())
                                .autocorrectionDisabled()
                            Text("When set, live view, AI, and the iOS app use this low-res feed (saves Mac CPU) while recording keeps the high-res main. Only enable for cameras that allow two simultaneous streams — some budget cameras (e.g. certain Tapo models) will drop the recording stream otherwise.")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)

                            if mediaMTXStore.subDisabledCameraIDs.contains(camera.id) {
                                HStack(spacing: 8) {
                                    Label("Low-res live was auto-disabled — it was interrupting this camera's recording.", systemImage: "exclamationmark.triangle.fill")
                                        .font(.caption2)
                                        .foregroundStyle(SentinelTheme.amber)
                                    Spacer()
                                    Button("Try again") { mediaMTXStore.reenableSubStream(for: camera.id) }
                                        .font(.caption2)
                                        .buttonStyle(.borderless)
                                }
                                .padding(.top, 2)
                            }
                        }
                    }
                }

                Section("Recording Schedule") {
                    Toggle("Restrict to schedule", isOn: $draft.enableSchedule)

                    if draft.enableSchedule {
                        VStack(alignment: .leading, spacing: 8) {
                            Text("Active days")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            HStack(spacing: 6) {
                                ForEach([("Su",1),("Mo",2),("Tu",3),("We",4),("Th",5),("Fr",6),("Sa",7)], id: \.1) { label, day in
                                    let active = draft.scheduleDays.contains(day)
                                    Button {
                                        if active { draft.scheduleDays.remove(day) }
                                        else { draft.scheduleDays.insert(day) }
                                    } label: {
                                        Text(label)
                                            .font(.caption.weight(.semibold))
                                            .frame(width: 32, height: 28)
                                            .background(active ? SentinelTheme.accent : Color.secondary.opacity(0.2), in: RoundedRectangle(cornerRadius: 6))
                                            .foregroundStyle(active ? .white : .primary)
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }

                        Picker("Start hour", selection: $draft.scheduleStartHour) {
                            ForEach(0..<24, id: \.self) { h in
                                Text(String(format: "%02d:00", h)).tag(h)
                            }
                        }

                        Picker("End hour", selection: $draft.scheduleEndHour) {
                            ForEach(1...24, id: \.self) { h in
                                Text(h == 24 ? "00:00 (+1d)" : String(format: "%02d:00", h)).tag(h)
                            }
                        }
                    }
                }

                Section("Retention") {
                    Picker("Keep recordings for", selection: $draft.retentionDays) {
                        Text("Global default").tag(0)
                        Text("1 day").tag(1)
                        Text("3 days").tag(3)
                        Text("7 days").tag(7)
                        Text("14 days").tag(14)
                        Text("30 days").tag(30)
                        Text("90 days").tag(90)
                    }
                }

                if let message = cameraStore.lastError {
                    Text(message)
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
            .padding(.horizontal, 12)

            Divider()
                .overlay(SentinelTheme.line)

            HStack {
                Spacer()

                Button("Cancel") {
                    dismiss()
                }
                .keyboardShortcut(.cancelAction)

                Button {
                    if cameraStore.updateCameraDisplay(cameraID: camera.id, draft: draft) {
                        dismiss()
                    }
                } label: {
                    Label("Save Changes", systemImage: "checkmark.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .disabled(draft.isValid == false)
                .keyboardShortcut(.defaultAction)
            }
            .padding(20)
        }
        .frame(width: 520, height: 660)
        .background(SentinelTheme.background)
    }
}

struct CameraAdministrationSummary: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore

    private var onlineCount: Int {
        cameraStore.cameras.filter { mediaIngestStore.effectiveStatus(for: $0) != .offline }.count
    }

    private var armedCount: Int {
        cameraStore.cameras.filter(\.isRecording).count
    }

    private var secureCount: Int {
        cameraStore.cameras.filter { $0.hasEmbeddedRTSPCredentials || $0.isLocalCamera }.count
    }

    var body: some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible()), count: 4), spacing: 12) {
            MetricCard(
                title: "Online Signal",
                value: "\(onlineCount)/\(cameraStore.cameras.count)",
                detail: cameraStore.cameras.isEmpty ? "No cameras configured" : "Inventory health",
                tint: onlineCount == cameraStore.cameras.count && cameraStore.cameras.isEmpty == false ? .green : .orange
            )

            MetricCard(
                title: "Recording Modes",
                value: "\(armedCount) armed",
                detail: "\(mediaIngestStore.activeRecordings.count) actively writing",
                tint: mediaIngestStore.activeRecordings.isEmpty ? SentinelTheme.accent : .red
            )

            MetricCard(
                title: "Credential Coverage",
                value: "\(secureCount)",
                detail: "Unattended or local sources",
                tint: secureCount == cameraStore.cameras.count && cameraStore.cameras.isEmpty == false ? .green : SentinelTheme.amber
            )

            MetricCard(
                title: "Ingest Engine",
                value: mediaEngineStore.snapshot.readiness.rawValue,
                detail: "GStreamer recording path",
                tint: mediaEngineStore.snapshot.readiness.tint
            )
        }
    }
}

struct AdminInfoPill: View {
    let text: String
    let systemImage: String
    let tint: Color

    var body: some View {
        Label(text, systemImage: systemImage)
            .font(.caption2.weight(.semibold))
            .foregroundStyle(tint)
            .lineLimit(1)
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .background(tint.opacity(0.12), in: Capsule())
    }
}

struct StorageView: View {
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @AppStorage("handoffgrid.recordingRetentionDays") private var retentionDays = 7
    @AppStorage("handoffgrid.dualStreamCooldownSeconds") private var dualStreamCooldown: Double = 30
    @State private var retentionMessage: String?
    @State private var approvalRequest: ApprovalRequest?

    /// Applies a retention change, asking for supervisor sign-off first when the
    /// signed-in role isn't allowed to change it alone.
    private func requestRetention(_ days: Int) {
        guard days != retentionDays else { return }
        let old = retentionDays
        let apply = {
            retentionDays = days
            workflowStore.recordAudit(area: "Storage", action: "Changed retention", detail: "\(old) → \(days) days")
        }
        if operatorSessionStore.needsApproval(.changeRetention) {
            approvalRequest = ApprovalRequest(action: .changeRetention, detail: "Keep recordings \(old) → \(days) days") { _, _ in apply() }
        } else {
            apply()
        }
    }

    private func requestApplyRetention() {
        let days = retentionDays
        let run = {
            let removed = mediaIngestStore.pruneRecordings(olderThanDays: days)
            retentionMessage = removed == 0 ? "No old recordings needed cleanup." : "Removed \(removed) old segment\(removed == 1 ? "" : "s")."
            workflowStore.recordAudit(area: "Storage", action: "Applied retention", detail: "Keep \(days) days — removed \(removed) segment\(removed == 1 ? "" : "s")")
        }
        if operatorSessionStore.needsApproval(.applyRetention) {
            approvalRequest = ApprovalRequest(action: .applyRetention, detail: "Delete recordings older than \(days) days") { _, _ in run() }
        } else {
            run()
        }
    }

    var body: some View {
        let summary = mediaIngestStore.storageSummary
        let volumeUsage = StorageVolumeUsage(path: summary.recordingRootURL.path)

        ScrollView {
        VStack(alignment: .leading, spacing: 14) {
            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 12) {
                MetricCard(title: "Local Segments", value: "\(summary.segmentCount)", detail: summary.totalSizeLabel, tint: SentinelTheme.accent)
                MetricCard(title: "Recording Load", value: "\(summary.activeRecordingCount) streams", detail: summary.activeRecordingCount == 0 ? "Idle" : "Writing MP4 segments", tint: summary.activeRecordingCount == 0 ? .secondary : .red)
                MetricCard(title: "Disk Used", value: volumeUsage.usedPercentLabel, detail: "\(volumeUsage.freeLabel) free", tint: volumeUsage.tint)
            }

            StorageExportSummaryPanel(summary: summary)

            SentinelPanel("Local Recording Store", systemImage: "internaldrive.fill") {
                VStack(alignment: .leading, spacing: 10) {
                    HStack {
                        Text(summary.recordingRootURL.path)
                            .font(.caption2.monospaced())
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)

                        Spacer()

                        Button {
                            NSWorkspace.shared.open(summary.recordingRootURL)
                        } label: {
                            Label("Open Folder", systemImage: "folder")
                        }

                        Button {
                            mediaIngestStore.refreshRecordingSegments()
                        } label: {
                            Label("Refresh", systemImage: "arrow.clockwise")
                        }
                    }

                    HStack {
                        Text("Newest segments are shown first. Open any clip directly from the row for export review.")
                            .font(.caption)
                            .foregroundStyle(.secondary)

                        Spacer()

                        Text(summary.totalSizeLabel)
                            .font(.caption.monospacedDigit().weight(.semibold))
                            .foregroundStyle(SentinelTheme.accent)
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text("Volume utilization")
                                .font(.caption.weight(.semibold))

                            Spacer()

                            Text("\(volumeUsage.usedLabel) used · \(volumeUsage.totalLabel) total")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }

                        MeterBar(value: volumeUsage.usedRatio, tint: volumeUsage.tint)
                    }
                    .padding(10)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))

                    if mediaIngestStore.recordingSegments.isEmpty {
                        EmptyStateLine(text: "No local MP4 segments have been indexed yet.")
                    } else {
                        VStack(spacing: 8) {
                            ForEach(mediaIngestStore.recordingSegments.prefix(50)) { segment in
                                RecordingSegmentRow(segment: segment)
                            }
                        }
                    }
                }
            }

            PerCameraStoragePanel()

            StorageForecastPanel()

            RecordingArchivePanel()

            SentinelPanel("Retention Policy", systemImage: "clock.arrow.circlepath") {
                VStack(alignment: .leading, spacing: 12) {
                    Picker("Keep recordings", selection: Binding(get: { retentionDays }, set: requestRetention)) {
                        Text("24 hours").tag(1)
                        Text("7 days").tag(7)
                        Text("30 days").tag(30)
                        Text("90 days").tag(90)
                    }
                    .pickerStyle(.segmented)

                    RetentionImpactStrip(
                        retentionDays: retentionDays,
                        segments: mediaIngestStore.recordingSegments,
                        activeRecordings: Set(mediaIngestStore.activeRecordings.keys)
                    )

                    HStack {
                        Text("The app applies this policy at startup and when you press Apply Retention.")
                            .font(.caption)
                            .foregroundStyle(.secondary)

                        Spacer()

                        Button {
                            requestApplyRetention()
                        } label: {
                            Label("Apply Retention", systemImage: "trash.circle")
                        }
                    }

                    if let retentionMessage {
                        Text(retentionMessage)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(10)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                    }
                }
            }

            SentinelPanel("Dual Stream Settings", systemImage: "square.2.layers.3d") {
                VStack(alignment: .leading, spacing: 10) {
                    Text("When a camera is in Dual Stream mode, low-res recording runs continuously via MediaMTX. High-res GStreamer recording activates on motion and stops after this delay.")
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    Picker("High-res stop delay", selection: $dualStreamCooldown) {
                        Text("10 seconds").tag(10.0)
                        Text("30 seconds").tag(30.0)
                        Text("1 minute").tag(60.0)
                        Text("2 minutes").tag(120.0)
                        Text("5 minutes").tag(300.0)
                    }
                    .pickerStyle(.segmented)
                }
            }

            SentinelPanel("Active Recordings", systemImage: "record.circle") {
                if mediaIngestStore.activeRecordings.isEmpty {
                    EmptyStateLine(text: "Start local recording from a camera inspector in Live View.")
                } else {
                    VStack(spacing: 8) {
                        ForEach(mediaIngestStore.activeRecordings.values.sorted { $0.cameraName < $1.cameraName }) { session in
                            HStack(spacing: 10) {
                                RecordingPill()

                                VStack(alignment: .leading, spacing: 3) {
                                    Text(session.cameraName)
                                        .font(.caption.weight(.semibold))

                                    Text("Started \(RecordingFormatters.timeFormatter.string(from: session.startedAt)) · PID \(session.processID)")
                                        .font(.caption2)
                                        .foregroundStyle(.secondary)

                                    Text(session.directoryURL.lastPathComponent)
                                        .font(.caption2.monospaced())
                                        .foregroundStyle(.secondary)
                                }

                                Spacer()

                                Button {
                                    NSWorkspace.shared.open(session.directoryURL)
                                } label: {
                                    Image(systemName: "folder")
                                }
                                .help("Open recording folder")

                                Button {
                                    NSWorkspace.shared.open(session.logURL)
                                } label: {
                                    Image(systemName: "doc.text.magnifyingglass")
                                }
                                .help("Open recording log")

                                Button {
                                    mediaIngestStore.stopRecording(for: session.cameraID)
                                } label: {
                                    Image(systemName: "stop.fill")
                                }
                                .help("Stop recording")
                            }
                            .padding(10)
                            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                        }
                    }
                }
            }
        }
        .padding(14)
        } // ScrollView
        .background(SentinelTheme.background)
        .approvalSheet($approvalRequest)
    }
}

struct RecordingSegmentRow: View {
    let segment: RecordingSegment

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "film.fill")
                .foregroundStyle(SentinelTheme.accent)

            VStack(alignment: .leading, spacing: 3) {
                Text(segment.fileName)
                    .font(.caption.weight(.semibold))
                    .lineLimit(1)
                    .truncationMode(.middle)

                Text("\(segment.dateLabel) · \(segment.timeLabel)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Text(segment.sizeLabel)
                .font(.caption)
                .foregroundStyle(.secondary)

            Button {
                NSWorkspace.shared.open(segment.fileURL)
            } label: {
                Image(systemName: "play.rectangle")
            }
            .buttonStyle(.borderless)
            .help("Open recording clip")

            Button {
                NSWorkspace.shared.activateFileViewerSelecting([segment.fileURL])
            } label: {
                Image(systemName: "arrow.up.forward.app")
            }
            .buttonStyle(.borderless)
            .help("Reveal clip in Finder")
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
        .help(segment.fileURL.path)
    }
}

struct StorageExportSummaryPanel: View {
    let summary: RecordingStorageSummary

    var body: some View {
        SentinelPanel("Export Visibility", systemImage: "square.and.arrow.up") {
            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                StorageExportFact(
                    title: "Export Source",
                    value: summary.recordingRootURL.lastPathComponent,
                    detail: "Local MP4 segment archive",
                    tint: .green
                )

                StorageExportFact(
                    title: "Indexed Footage",
                    value: "\(summary.segmentCount)",
                    detail: summary.totalSizeLabel,
                    tint: summary.segmentCount == 0 ? .secondary : SentinelTheme.accent
                )

                StorageExportFact(
                    title: "Write Activity",
                    value: summary.activeRecordingCount == 0 ? "Idle" : "Active",
                    detail: "\(summary.activeRecordingCount) recording stream\(summary.activeRecordingCount == 1 ? "" : "s")",
                    tint: summary.activeRecordingCount == 0 ? .secondary : .red
                )
            }
        }
    }
}

struct StorageExportFact: View {
    let title: String
    let value: String
    let detail: String
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Circle()
                    .fill(tint)
                    .frame(width: 7, height: 7)

                Text(title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)

                Spacer()
            }

            Text(value)
                .font(.headline)
                .lineLimit(1)

            Text(detail)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct PerCameraStoragePanel: View {
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore

    private struct CameraUsage: Identifiable {
        let id: UUID
        let name: String
        let bytes: Int64
        let segmentCount: Int
    }

    private var cameraUsages: [CameraUsage] {
        cameraStore.cameras.map { camera in
            let segs = mediaIngestStore.segments(for: camera.id)
            return CameraUsage(
                id: camera.id,
                name: camera.name,
                bytes: segs.reduce(0) { $0 + $1.byteCount },
                segmentCount: segs.count
            )
        }
        .filter { $0.segmentCount > 0 }
        .sorted { $0.bytes > $1.bytes }
    }

    private var totalBytes: Int64 {
        cameraUsages.reduce(0) { $0 + $1.bytes }
    }

    var body: some View {
        if cameraUsages.isEmpty == false {
            SentinelPanel("Storage by Camera", systemImage: "chart.bar.fill") {
                VStack(spacing: 8) {
                    ForEach(cameraUsages) { usage in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack {
                                Text(usage.name)
                                    .font(.caption.weight(.semibold))
                                    .lineLimit(1)
                                Spacer()
                                Text(ByteCountFormatter.string(fromByteCount: usage.bytes, countStyle: .file))
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(.secondary)
                                Text("· \(usage.segmentCount) clips")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            let ratio = totalBytes > 0 ? Double(usage.bytes) / Double(totalBytes) : 0
                            MeterBar(value: ratio, tint: SentinelTheme.accent)
                        }
                    }
                }
            }
        }
    }
}

struct StorageVolumeUsage {
    let totalBytes: Int64
    let freeBytes: Int64

    init(path: String) {
        let attributes = (try? FileManager.default.attributesOfFileSystem(forPath: path)) ?? [:]
        totalBytes = Self.int64Value(attributes[.systemSize])
        freeBytes = Self.int64Value(attributes[.systemFreeSize])
    }

    var usedBytes: Int64 {
        max(totalBytes - freeBytes, 0)
    }

    var usedRatio: Double {
        guard totalBytes > 0 else {
            return 0
        }

        return Double(usedBytes) / Double(totalBytes)
    }

    var usedPercentLabel: String {
        "\(Int((usedRatio * 100).rounded()))%"
    }

    var usedLabel: String {
        ByteCountFormatter.string(fromByteCount: usedBytes, countStyle: .file)
    }

    var freeLabel: String {
        ByteCountFormatter.string(fromByteCount: freeBytes, countStyle: .file)
    }

    var totalLabel: String {
        ByteCountFormatter.string(fromByteCount: totalBytes, countStyle: .file)
    }

    var tint: Color {
        if usedRatio > 0.90 {
            return .red
        }

        if usedRatio > 0.75 {
            return SentinelTheme.amber
        }

        return .green
    }

    private static func int64Value(_ value: Any?) -> Int64 {
        if let number = value as? NSNumber {
            return number.int64Value
        }

        if let intValue = value as? Int {
            return Int64(intValue)
        }

        if let int64Value = value as? Int64 {
            return int64Value
        }

        return 0
    }
}

struct RetentionImpactStrip: View {
    let retentionDays: Int
    let segments: [RecordingSegment]
    let activeRecordings: Set<UUID>

    private var expiredSegments: [RecordingSegment] {
        let cutoff = Date().addingTimeInterval(-Double(retentionDays) * 86_400)
        return segments.filter { segment in
            segment.modifiedAt < cutoff && activeRecordings.contains(segment.cameraID) == false
        }
    }

    private var retainedSegments: Int {
        max(segments.count - expiredSegments.count, 0)
    }

    var body: some View {
        HStack(spacing: 10) {
            AdminInfoPill(
                text: "\(retainedSegments) retained",
                systemImage: "checkmark.shield.fill",
                tint: retainedSegments == 0 ? .secondary : .green
            )

            AdminInfoPill(
                text: "\(expiredSegments.count) eligible",
                systemImage: "trash.circle",
                tint: expiredSegments.isEmpty ? .secondary : SentinelTheme.amber
            )

            Spacer()

            Text("Policy preview excludes cameras currently recording.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }
}

struct UsersView: View {
    @EnvironmentObject private var userDirectoryStore: UserDirectoryStore
    @EnvironmentObject private var workflowStore: WorkflowStore
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @State private var isInvitingUser = false
    @State private var inviteName = ""
    @State private var inviteRole = "Operator"
    @State private var pinEditingUserID: UUID?
    @State private var newPin = ""

    private var isAdmin: Bool {
        operatorSessionStore.currentOperator?.role == "Admin" || operatorSessionStore.guestMode
    }

    var body: some View {
        ScrollView {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Label("Users & Roles", systemImage: "person.2.fill")
                    .font(.headline)

                Spacer()

                if isAdmin == false {
                    Label("Admin role required", systemImage: "lock.fill")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(SentinelTheme.amber)
                }

                Button {
                    isInvitingUser = true
                } label: {
                    Label("Invite", systemImage: "person.badge.plus")
                }
                .buttonStyle(.borderedProminent)
                .disabled(isAdmin == false)
            }

            SentinelPanel("Operator Accounts") {
                VStack(spacing: 8) {
                    if userDirectoryStore.users.isEmpty {
                        EmptyStateLine(text: "No operator accounts configured.")
                    } else {
                        ForEach(userDirectoryStore.users) { user in
                            VStack(alignment: .leading, spacing: 8) {
                                HStack {
                                    Image(systemName: "person.crop.circle.fill")
                                        .font(.title2)
                                        .foregroundStyle(SentinelTheme.accent)

                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(user.name).font(.headline)
                                        Text(user.role).font(.caption).foregroundStyle(.secondary)
                                    }

                                    Spacer()

                                    Text(user.lastSeen)
                                        .font(.caption).foregroundStyle(.secondary)

                                    Label(user.hasPin ? "PIN set" : "No PIN",
                                          systemImage: user.hasPin ? "lock.fill" : "lock.open")
                                        .font(.caption2.weight(.semibold))
                                        .foregroundStyle(user.hasPin ? .green : .secondary)

                                    Button(pinEditingUserID == user.id ? "Done" : "Set PIN") {
                                        if pinEditingUserID == user.id {
                                            userDirectoryStore.setPin(newPin, for: user.id)
                                            pinEditingUserID = nil; newPin = ""
                                        } else {
                                            pinEditingUserID = user.id; newPin = ""
                                        }
                                    }
                                    .buttonStyle(.bordered).controlSize(.small)
                                }

                                if pinEditingUserID == user.id {
                                    HStack(spacing: 8) {
                                        SecureField("New PIN (blank to remove)", text: $newPin)
                                            .textFieldStyle(.roundedBorder)
                                        Text("4–8 digits").font(.caption2).foregroundStyle(.secondary)
                                    }
                                }
                            }
                            .padding(12)
                            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                        }
                    }
                }
            }

            SentinelPanel("Role Profiles", systemImage: "lock.shield.fill") {
                LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                    ForEach(workflowStore.roleProfiles) { role in
                        VStack(alignment: .leading, spacing: 8) {
                            Text(role.name)
                                .font(.headline)

                            Text(role.description)
                                .font(.caption)
                                .foregroundStyle(.secondary)

                            Text(role.permissions.joined(separator: ", "))
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                                .lineLimit(2)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(12)
                        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
                    }
                }
            }
        }
        .padding(14)
        } // ScrollView
        .background(SentinelTheme.background)
        .sheet(isPresented: $isInvitingUser) {
            VStack(alignment: .leading, spacing: 18) {
                Text("Invite User")
                    .font(.title3.weight(.semibold))

                TextField("Name", text: $inviteName)
                    .textFieldStyle(.roundedBorder)

                Picker("Role", selection: $inviteRole) {
                    Text("Admin").tag("Admin")
                    Text("Supervisor").tag("Supervisor")
                    Text("Operator").tag("Operator")
                    Text("Viewer").tag("Viewer")
                }

                HStack {
                    Spacer()

                    Button("Cancel") {
                        isInvitingUser = false
                    }

                    Button {
                        userDirectoryStore.invite(name: inviteName, role: inviteRole)
                        inviteName = ""
                        inviteRole = "Operator"
                        isInvitingUser = false
                    } label: {
                        Label("Invite", systemImage: "person.badge.plus")
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(inviteName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .padding(22)
            .frame(width: 380)
            .background(SentinelTheme.background)
        }
    }
}

struct HealthView: View {
    @Environment(\.colorScheme) private var colorScheme
    @EnvironmentObject private var cameraStore: CameraStore
    @EnvironmentObject private var mediaEngineStore: MediaEngineStore
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    @State private var diskFreeBytes: Int64 = 0
    @State private var diskTotalBytes: Int64 = 0

    private var offlineCount: Int {
        cameraStore.cameras.filter { mediaIngestStore.effectiveStatus(for: $0) == .offline }.count
    }

    private var recordingStorageUsed: Int64 {
        mediaIngestStore.recordingSegments.reduce(0) { $0 + $1.byteCount }
    }

    private var diskFreeLabel: String {
        diskFreeBytes > 0 ? ByteCountFormatter.string(fromByteCount: diskFreeBytes, countStyle: .file) : "—"
    }

    private var diskUsedRatio: Double {
        diskTotalBytes > 0 ? Double(diskTotalBytes - diskFreeBytes) / Double(diskTotalBytes) : 0
    }

    var body: some View {
        ScrollView {
        VStack(alignment: .leading, spacing: 14) {
            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 12) {
                MetricCard(
                    title: "Configured Cameras",
                    value: "\(cameraStore.cameras.count)",
                    detail: "\(cameraStore.cameras.filter { $0.rtspURL.isEmpty == false }.count) RTSP streams",
                    tint: cameraStore.cameras.isEmpty ? .secondary : SentinelTheme.accent
                )

                MetricCard(
                    title: "Live Bridges",
                    value: "\(mediaIngestStore.liveStreams.count)",
                    detail: mediaIngestStore.liveStreams.isEmpty ? "Idle" : "Streaming to Live View",
                    tint: mediaIngestStore.liveStreams.isEmpty ? .secondary : .green
                )

                MetricCard(
                    title: "Media Engine",
                    value: mediaEngineStore.snapshot.readiness.rawValue,
                    detail: SentinelStreamingArchitecture.ingestEngine,
                    tint: mediaEngineStore.snapshot.readiness.tint
                )

                MetricCard(
                    title: "Local Recording",
                    value: "\(mediaIngestStore.activeRecordings.count) active",
                    detail: "\(mediaIngestStore.recordingSegments.count) indexed segments",
                    tint: mediaIngestStore.activeRecordings.isEmpty ? SentinelTheme.accent : .red
                )

                MetricCard(
                    title: "Camera Issues",
                    value: "\(offlineCount)",
                    detail: offlineCount == 0 ? "All reachable" : "Needs admin review",
                    tint: offlineCount == 0 ? .green : .red
                )

                MetricCard(
                    title: "Disk Free",
                    value: diskFreeLabel,
                    detail: String(format: "%.0f%% used · %@ recordings", diskUsedRatio * 100, ByteCountFormatter.string(fromByteCount: recordingStorageUsed, countStyle: .file)),
                    tint: diskUsedRatio > 0.90 ? .red : diskUsedRatio > 0.75 ? SentinelTheme.amber : .green
                )
            }
            .task {
                refreshDiskStats()
            }

            MediaEnginePanel(
                snapshot: mediaEngineStore.snapshot,
                refresh: mediaEngineStore.refresh
            )

            SentinelPanel("Services", systemImage: "waveform.path.ecg") {
                VStack(spacing: 8) {
                    ServiceRow(
                        name: "MediaMTX Proxy",
                        state: mediaMTXStore.isRunning ? "Running — port \(MediaMTXStore.rtspPort)" : (mediaMTXStore.isAvailable ? "Stopped" : "Not installed"),
                        tint: mediaMTXStore.isRunning ? .green : (mediaMTXStore.isAvailable ? SentinelTheme.amber : .secondary)
                    )
                    if mediaMTXStore.isRunning, let ip = MediaMTXStore.localIPAddress() {
                        ServiceRow(
                            name: "iOS HLS Stream",
                            state: "http://\(ip):\(MediaMTXStore.hlsPort)/{camera-id}/index.m3u8",
                            tint: .green
                        )
                    }
                    ServiceRow(name: "GStreamer Detection", state: mediaEngineStore.snapshot.readiness == .missing ? "Waiting" : "Ready", tint: mediaEngineStore.snapshot.readiness.tint)
                    ServiceRow(name: "Motion Bridges", state: mediaIngestStore.liveStreams.isEmpty ? "Idle" : "\(mediaIngestStore.liveStreams.count) Active", tint: mediaIngestStore.liveStreams.isEmpty ? .secondary : .green)
                    ServiceRow(name: "Recording", state: mediaIngestStore.activeRecordings.isEmpty ? "Idle" : "\(mediaIngestStore.activeRecordings.count) Active", tint: mediaIngestStore.activeRecordings.isEmpty ? .secondary : .red)
                    ServiceRow(name: "Playback Indexer", state: "\(mediaIngestStore.recordingSegments.count) Segments", tint: .green)
                }
            }

            SentinelPanel("Camera Connectivity", systemImage: "network") {
                if cameraStore.cameras.isEmpty {
                    EmptyStateLine(text: "Add cameras to monitor connectivity, credentials, recording state, and indexed storage.")
                } else {
                    VStack(spacing: 8) {
                        ForEach(cameraStore.cameras) { camera in
                            CameraHealthRow(camera: camera)
                        }
                    }
                }
            }

            DiagnosticsPanel()
        }
        .padding(14)
        } // ScrollView
        .background(SentinelTheme.background)
    }

    private func refreshDiskStats() {
        let url = mediaIngestStore.recordingRootURL
        let values = try? url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey, .volumeTotalCapacityKey])
        diskFreeBytes = Int64(values?.volumeAvailableCapacityForImportantUsage ?? 0)
        diskTotalBytes = Int64(values?.volumeTotalCapacity ?? 0)
    }
}

struct CameraHealthRow: View {
    @EnvironmentObject private var mediaIngestStore: MediaIngestStore
    @EnvironmentObject private var mediaMTXStore: MediaMTXStore
    let camera: CameraFeed

    var body: some View {
        let health = mediaIngestStore.healthSnapshot(for: camera)
        let liveStream = mediaIngestStore.liveStream(for: camera.id)
        let recordingSession = mediaIngestStore.recordingSession(for: camera.id)

        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 8) {
                        Circle()
                            .fill(health.status.tint)
                            .frame(width: 8, height: 8)

                        Text(camera.name)
                            .font(.caption.weight(.semibold))
                    }

                    Text("\(camera.location) · \(camera.ipAddress)")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                AdminInfoPill(
                    text: liveStream == nil ? "No live bridge" : "Live PID \(liveStream?.processID ?? 0)",
                    systemImage: liveStream == nil ? "video.slash" : "video.fill",
                    tint: liveStream == nil ? .secondary : .green
                )
                .frame(width: 132, alignment: .leading)

                AdminInfoPill(
                    text: recordingSession == nil ? "Not writing" : "REC PID \(recordingSession?.processID ?? 0)",
                    systemImage: recordingSession == nil ? "record.circle" : "record.circle.fill",
                    tint: recordingSession == nil ? .secondary : .red
                )
                .frame(width: 132, alignment: .leading)

                AdminInfoPill(
                    text: health.fpsLabel,
                    systemImage: "speedometer",
                    tint: health.estimatedFPS == 0 ? .secondary : SentinelTheme.accent
                )
                .frame(width: 104, alignment: .leading)

                StatusBadge(status: health.status)
                    .frame(width: 100, alignment: .trailing)
            }

            HStack(spacing: 10) {
                AdminInfoPill(
                    text: "Frame \(health.frameAgeLabel)",
                    systemImage: "clock.fill",
                    tint: health.lastFrameAt == nil ? .secondary : .green
                )

                AdminInfoPill(
                    text: "\(health.segmentCount) clips",
                    systemImage: "film.stack.fill",
                    tint: health.segmentCount == 0 ? .secondary : SentinelTheme.accent
                )

                AdminInfoPill(
                    text: camera.recordingMode.label,
                    systemImage: camera.recordingMode == .motion ? "figure.walk.motion" : "record.circle",
                    tint: camera.recordingMode == .motion ? SentinelTheme.motion : SentinelTheme.accent
                )

                AdminInfoPill(
                    text: "\(health.eventCount) events",
                    systemImage: "figure.walk.motion",
                    tint: health.eventCount == 0 ? .secondary : SentinelTheme.motion
                )

                if mediaMTXStore.isAvailable {
                    let state = mediaMTXStore.streamState(for: camera.id)
                    AdminInfoPill(
                        text: state.label,
                        systemImage: "antenna.radiowaves.left.and.right",
                        tint: state.tint
                    )
                }

                if let lastError = health.lastError {
                    Text(lastError)
                        .font(.caption2)
                        .foregroundStyle(.orange)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }

                Spacer()
            }
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(health.status.tint.opacity(health.status == .offline ? 0.34 : 0.16), lineWidth: 1)
        }
    }
}

struct MediaEnginePanel: View {
    let snapshot: MediaEngineSnapshot
    let refresh: () -> Void

    var body: some View {
        SentinelPanel("Media Engine", systemImage: "cpu.fill") {
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    EngineStatusPill(label: "GStreamer", value: snapshot.readiness.rawValue, tint: snapshot.readiness.tint)
                    EngineStatusPill(label: "Client", value: SentinelStreamingArchitecture.appleClientPlayback, tint: SentinelTheme.accent)
                    EngineStatusPill(label: "Future", value: SentinelStreamingArchitecture.lowLatencyFuture, tint: SentinelTheme.amber)

                    Spacer()

                    Button {
                        refresh()
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                    }
                }

                LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                    EnginePathCard(title: "gst-launch", path: snapshot.gstreamerLaunchPath)
                    EnginePathCard(title: "gst-inspect", path: snapshot.gstreamerInspectPath)
                    EnginePathCard(title: "Framework", path: snapshot.frameworkPath)
                }

                VStack(alignment: .leading, spacing: 6) {
                    ForEach(snapshot.notes, id: \.self) { note in
                        HStack(spacing: 8) {
                            Circle()
                                .fill(snapshot.readiness.tint)
                                .frame(width: 6, height: 6)

                            Text(note)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
    }
}

struct EngineStatusPill: View {
    let label: String
    let value: String
    let tint: Color

    var body: some View {
        HStack(spacing: 6) {
            Text(label)
                .foregroundStyle(.secondary)

            Text(value)
                .foregroundStyle(tint)
        }
        .font(.caption.weight(.semibold))
        .padding(.horizontal, 9)
        .padding(.vertical, 5)
        .background(tint.opacity(0.12), in: Capsule())
    }
}

struct EnginePathCard: View {
    let title: String
    let path: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(.caption.weight(.semibold))

            Text(path ?? "Not found")
                .font(.caption2.monospaced())
                .foregroundStyle(path == nil ? .orange : .secondary)
                .lineLimit(2)
                .truncationMode(.middle)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct ServiceRow: View {
    let name: String
    let state: String
    let tint: Color

    var body: some View {
        HStack {
            Circle()
                .fill(tint)
                .frame(width: 8, height: 8)

            Text(name)
                .font(.headline)

            Spacer()

            Text(state)
                .font(.caption.weight(.semibold))
                .foregroundStyle(tint)
        }
        .padding(12)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct DiscoveryPhasesView: View {
    let phaseStates: [DiscoveryPhase: DiscoveryPhaseState]

    private let order: [DiscoveryPhase] = [.onvif, .mdns, .deepScan]

    var body: some View {
        let active = order.compactMap { phase -> (DiscoveryPhase, DiscoveryPhaseState)? in
            guard let state = phaseStates[phase], state != .idle else { return nil }
            return (phase, state)
        }
        if active.isEmpty {
            EmptyView()
        } else {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(active, id: \.0) { phase, state in
                    HStack(spacing: 8) {
                        Image(systemName: phase.symbol)
                            .font(.caption)
                            .foregroundStyle(tint(for: state))
                            .frame(width: 16)
                        Text(phase.title)
                            .font(.caption.weight(.semibold))
                        Text(state.label)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        Spacer()
                        if state.isRunning {
                            ProgressView().controlSize(.small)
                        }
                    }
                }
            }
            .padding(.vertical, 4)
        }
    }

    private func tint(for state: DiscoveryPhaseState) -> Color {
        switch state {
        case .idle: return .secondary
        case .running: return SentinelTheme.accent
        case .found(let n): return n > 0 ? .green : .secondary
        case .failed: return .red
        }
    }
}
