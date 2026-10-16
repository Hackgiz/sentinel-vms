import AppKit
import SwiftUI
import SentinelCore

/// The floorplan map: pick a plan, see its cameras pinned on it. A pin shows
/// the camera's name on hover, is grey when the camera is offline and pulses
/// red while an alarm is open on it. State comes from the session's existing
/// camera directory and alarm queue (both kept live by the event stream);
/// the plan list is reloaded when the view appears and when the camera list
/// reloads (a configuration change), or with the refresh button.
///
/// Wiring (coordinator, at merge):
/// `MapWorkspaceView(onOpenCamera: { cameraID in /* focus it in Live */ })`.
/// `onOpenCamera` runs on the main actor when a pin is clicked; the host
/// decides what "open" means (the shell shows the camera in the Live
/// workspace). Requires the usual site environment objects
/// (`SiteRuntime`, `CameraDirectory`, `AlarmQueue`).
struct MapWorkspaceView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var alarms: AlarmQueue

    /// Called with the camera id when a pin is clicked.
    var onOpenCamera: (String) -> Void

    init(onOpenCamera: @escaping (String) -> Void = { _ in }) {
        self.onOpenCamera = onOpenCamera
    }

    @State private var plans: [V2.Floorplan] = []
    @State private var selectedID: String?
    @State private var image: NSImage?
    @State private var loadingPlans = true
    @State private var error: String?
    @State private var imageError: String?
    @StateObject private var imageStore = PlanImageStore()

    private var plan: V2.Floorplan? { plans.first { $0.id == selectedID } ?? plans.first }

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider().overlay(SentinelTheme.line)
            content
        }
        .task { await loadPlans() }
        .onChange(of: cameras.cameras.count) { _ in Task { await loadPlans() } }
        .task(id: plan?.id) { await loadImage() }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            if plans.isEmpty == false {
                Picker("Floorplan", selection: Binding(get: { plan?.id ?? "" }, set: { selectedID = $0 })) {
                    ForEach(plans) { Text($0.name).tag($0.id) }
                }
                .frame(maxWidth: 280)
            }
            if let plan {
                Text("\(plan.pins.count) camera\(plan.pins.count == 1 ? "" : "s")").font(.caption).foregroundStyle(.secondary)
                let alarmed = plan.pins.filter { FloorplanPins.alarm(on: $0.cameraId, in: alarms.open) != nil }.count
                if alarmed > 0 {
                    Text("\(alarmed) with an open alarm").font(.caption.weight(.semibold)).foregroundStyle(.red)
                }
            }
            Spacer()
            Button { Task { await loadPlans() } } label: { Image(systemName: "arrow.clockwise") }
                .help("Reload floorplans")
        }
        .controlSize(.small)
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(SentinelTheme.chrome)
    }

    @ViewBuilder
    private var content: some View {
        if let plan {
            ZStack {
                Color.black.opacity(0.9)
                if let image {
                    MapCanvas(plan: plan, image: image, cameras: cameras, alarms: alarms.open, onOpenCamera: onOpenCamera)
                        .padding(12)
                } else if let imageError {
                    Text(imageError).foregroundStyle(.red).font(.callout)
                } else {
                    ProgressView().controlSize(.small)
                }
            }
        } else {
            VStack(spacing: 8) {
                Image(systemName: "map").font(.largeTitle).foregroundStyle(.secondary)
                Text(loadingPlans ? "Loading floorplans…" : "No floorplans yet.").foregroundStyle(.secondary)
                if let error { Text(error).font(.caption).foregroundStyle(.red) }
                else if loadingPlans == false {
                    Text("An administrator uploads plans and places cameras on them in the admin console.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func loadPlans() async {
        do {
            plans = try await runtime.api.floorplans()
            error = nil
            if selectedID == nil || plans.contains(where: { $0.id == selectedID }) == false {
                selectedID = plans.first?.id
            }
        } catch {
            self.error = error.localizedDescription
        }
        loadingPlans = false
    }

    private func loadImage() async {
        guard let plan else { image = nil; return }
        image = nil
        imageError = nil
        do {
            let data = try await imageStore.cache(for: runtime.api).image(for: plan.id)
            guard Task.isCancelled == false else { return }
            if let decoded = NSImage(data: data) { image = decoded } else { imageError = "The floorplan image couldn't be read." }
        } catch {
            if Task.isCancelled == false { imageError = error.localizedDescription }
        }
    }
}

/// Keeps the ETag cache across plan switches for as long as the view lives.
@MainActor
private final class PlanImageStore: ObservableObject {
    private var cache: FloorplanImageCache?
    func cache(for api: SentinelAPI) -> FloorplanImageCache {
        if let cache { return cache }
        let made = FloorplanImageCache(api: api)
        cache = made
        return made
    }
}

private struct MapCanvas: View {
    let plan: V2.Floorplan
    let image: NSImage
    let cameras: CameraDirectory
    let alarms: [V2.Alarm]
    let onOpenCamera: (String) -> Void

    @State private var hovered: String?

    var body: some View {
        GeometryReader { proxy in
            let size = CGSize(width: CGFloat(max(plan.width, 1)), height: CGFloat(max(plan.height, 1)))
            let rect = FloorplanGeometry.fittedRect(imageSize: size, in: proxy.size)
            ZStack(alignment: .topLeading) {
                Image(nsImage: image)
                    .resizable()
                    .frame(width: rect.width, height: rect.height)
                    .position(x: rect.midX, y: rect.midY)
                ForEach(plan.pins, id: \.cameraId) { pin in
                    let point = FloorplanGeometry.position(of: pin, in: rect)
                    let camera = cameras.camera(pin.cameraId)
                    let state = FloorplanPins.state(camera: camera, cameraID: pin.cameraId, alarms: alarms)
                    MapPin(state: state, heading: pin.heading, hovered: hovered == pin.cameraId,
                           name: camera?.name ?? "Camera",
                           note: note(state, pin.cameraId))
                        .position(point)
                        .zIndex(hovered == pin.cameraId ? 2 : 1)
                        .onHover { inside in
                            if inside { hovered = pin.cameraId } else if hovered == pin.cameraId { hovered = nil }
                        }
                        .onTapGesture { onOpenCamera(pin.cameraId) }
                        .accessibilityElement()
                        .accessibilityLabel("\(camera?.name ?? "Camera"), \(state.rawValue)")
                        .accessibilityAddTraits(.isButton)
                }
            }
        }
    }

    private func note(_ state: PinState, _ cameraID: String) -> String {
        switch state {
        case .alarmed: return FloorplanPins.alarm(on: cameraID, in: alarms)?.title ?? "Open alarm"
        case .offline: return "Offline"
        case .normal: return "Click to open"
        }
    }
}

private struct MapPin: View {
    let state: PinState
    let heading: Double
    let hovered: Bool
    let name: String
    let note: String

    @State private var pulse = false

    private var tint: Color {
        switch state {
        case .normal: return SentinelTheme.accent
        case .offline: return .gray
        case .alarmed: return .red
        }
    }

    var body: some View {
        ZStack {
            if state == .alarmed {
                Circle().fill(Color.red.opacity(0.35))
                    .frame(width: 34, height: 34)
                    .scaleEffect(pulse ? 1.25 : 0.7)
                    .opacity(pulse ? 0.15 : 0.8)
                    .onAppear {
                        withAnimation(.easeInOut(duration: 1.0).repeatForever(autoreverses: true)) { pulse = true }
                    }
            }
            if heading >= 0 {
                Image(systemName: "triangle.fill")
                    .font(.system(size: 8))
                    .foregroundStyle(tint)
                    .offset(y: -17)
                    .rotationEffect(.degrees(heading))
            }
            Circle().fill(tint).frame(width: 22, height: 22)
                .overlay(Circle().stroke(.white, lineWidth: 1.5))
                .overlay(Image(systemName: "video.fill").font(.system(size: 10)).foregroundStyle(.white))
                .shadow(radius: 2)
        }
        .frame(width: 40, height: 40)
        .contentShape(Circle())
        .overlay(alignment: .top) {
            if hovered {
                VStack(spacing: 1) {
                    Text(name).font(.caption.weight(.semibold))
                    Text(note).font(.caption2).foregroundStyle(.secondary)
                }
                .padding(.horizontal, 8).padding(.vertical, 4)
                .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 6))
                .fixedSize()
                .offset(y: -38)
            }
        }
    }
}
