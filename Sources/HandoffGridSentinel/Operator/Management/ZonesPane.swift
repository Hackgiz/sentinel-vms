import AppKit
import SwiftUI
import SentinelCore

// Devices > Zones & Privacy (wave 11 stream E): per camera, detection zones
// (motion/object detection only counts inside them), privacy masks (blacked
// out of every snapshot, thumbnail and export) and line-crossing tripwires.
// The backdrop is the camera's latest event picture (masked areas already
// show black there), else a labelled 16:9 canvas. Editing logic lives in
// `SentinelCore/Zones/ZoneEditorModel.swift`; Save is one PUT of the set.

/// Hands the site's `SentinelAPI` from the environment to `ZonesPane`
/// (what the Management window registers for `ManagementCatalog.Leaf.zones`).
struct ZonesPaneHost: View {
    @EnvironmentObject private var runtime: SiteRuntime

    var body: some View { ZonesPane(api: runtime.api) }
}

struct ZonesPane: View {
    let api: SentinelAPI

    @State private var cameras: [V2.Camera] = []
    @State private var cameraID: String?
    @State private var pendingCameraID: String?
    @State private var error: String?
    @State private var loaded = false
    @State private var model = ZoneEditorModel()

    var body: some View {
        PaneFrame(title: "Zones & Privacy",
                  subtitle: "Detection zones limit motion and person/vehicle detection to part of the picture. Privacy masks black out part of the picture in every snapshot, thumbnail and exported clip (exports of a camera with masks are re-encoded, so they take longer). Lines raise a line-crossing event and alarm when a person, vehicle or animal crosses them.",
                  error: error) {
            HSplitView {
                cameraList.frame(minWidth: 180, idealWidth: 210, maxWidth: 280)
                Group {
                    if let camera = cameras.first(where: { $0.id == cameraID }) {
                        ZoneEditorView(api: api, camera: camera, model: $model)
                            .id(camera.id)
                    } else if loaded && cameras.isEmpty {
                        placeholder("No cameras yet. Add a camera first, then draw its zones here.")
                    } else if loaded {
                        placeholder("Pick a camera.")
                    } else if error == nil {
                        ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                }
                .frame(minWidth: 560)
            }
        }
        .task { await loadCameras() }
        .confirmationDialog("Discard unsaved zone changes?", isPresented: Binding(
            get: { pendingCameraID != nil }, set: { if $0 == false { pendingCameraID = nil } })) {
            Button("Discard Changes", role: .destructive) {
                switchTo(pendingCameraID)
                pendingCameraID = nil
            }
        } message: {
            Text("The zones you changed on this camera haven't been saved.")
        }
    }

    private var cameraList: some View {
        List(selection: Binding(get: { cameraID }, set: { pick($0) })) {
            ForEach(cameras) { camera in
                VStack(alignment: .leading, spacing: 1) {
                    Text(camera.name)
                    let masks = camera.masks?.count ?? 0
                    if masks > 0 {
                        Text("\(masks) privacy mask\(masks == 1 ? "" : "s")").font(.caption).foregroundStyle(.secondary)
                    }
                }
                .tag(camera.id)
            }
        }
    }

    private func placeholder(_ text: String) -> some View {
        Text(text).foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func pick(_ id: String?) {
        guard let id, id != cameraID else { return }
        if model.isDirty { pendingCameraID = id } else { switchTo(id) }
    }

    private func switchTo(_ id: String?) {
        model = ZoneEditorModel()
        cameraID = id
    }

    private func loadCameras() async {
        do {
            cameras = try await api.cameras()
            if cameraID == nil || cameras.contains(where: { $0.id == cameraID }) == false { cameraID = cameras.first?.id }
            loaded = true
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Editor

private struct ZoneEditorView: View {
    let api: SentinelAPI
    let camera: V2.Camera
    @Binding var model: ZoneEditorModel

    @State private var backdrop: NSImage?
    @State private var loadError: String?
    @State private var saveError: String?
    @State private var status: String?
    @State private var busy = false
    @State private var zonesLoaded = false

    /// The picture's width / height: the backdrop's, else the stream's, else 16:9.
    private var aspect: Double {
        if let size = backdrop?.size, size.width > 0, size.height > 0 { return Double(size.width / size.height) }
        if let w = camera.status.width, let h = camera.status.height, w > 0, h > 0 { return Double(w) / Double(h) }
        return 16.0 / 9.0
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            toolbar
            if let text = loadError ?? saveError {
                Text(text).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            if let status, model.isDirty == false {
                Text(status).font(.caption).foregroundStyle(.secondary)
            }
            Text(model.hint).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack(alignment: .top, spacing: 10) {
                ZoneCanvas(model: $model, backdrop: backdrop, aspect: aspect, cameraName: camera.name)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                shapeList.frame(width: 270)
            }
        }
        .padding(14)
        .disabled(zonesLoaded == false && loadError == nil)
        .task { await load() }
    }

    private var toolbar: some View {
        HStack(spacing: 8) {
            Text(camera.name).font(.headline).lineLimit(1)
            Spacer()
            if model.drawing != nil {
                Button("Finish") { model.finishDrawing() }
                    .keyboardShortcut(.defaultAction)
                    .disabled((model.drawing?.points.count ?? 0) < (model.drawing?.needed ?? 3))
                Button("Cancel") { model.cancelDrawing() }
            } else {
                Button { model.startDrawing(.detect) } label: { Label("Detection Zone", systemImage: "plus.viewfinder") }
                    .disabled(model.isFull)
                Button { model.startDrawing(.mask) } label: { Label("Privacy Mask", systemImage: "eye.slash") }
                    .disabled(model.isFull)
                Button { model.startDrawing(.line) } label: { Label("Line", systemImage: "line.diagonal") }
                    .disabled(model.isFull)
            }
            Divider().frame(height: 18)
            Button("Discard") { model.discard(); saveError = nil }
                .disabled(model.isDirty == false || busy)
            Button(busy ? "Saving…" : "Save Zones") { Task { await save() } }
                .buttonStyle(.borderedProminent)
                .disabled(model.canSave == false || busy)
        }
        .controlSize(.small)
        .help(model.isFull ? "A camera can have at most \(ZoneGeometry.maxZones) zones." : "")
    }

    private var shapeList: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(model.zones) { zone in
                    ZoneRow(zone: zone, selected: model.selected == zone.key, problem: zone.problem,
                            update: { change in model.update(zone.key, change) },
                            delete: { model.delete(zone.key) })
                        .onTapGesture { model.selected = zone.key }
                }
                if model.zones.isEmpty && model.drawing == nil && zonesLoaded {
                    Text("No zones yet.").font(.caption).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func load() async {
        do {
            model.load(try await api.zones(cameraID: camera.id))
            zonesLoaded = true
            loadError = nil
        } catch {
            loadError = managementErrorText(error)
        }
        await loadBackdrop()
    }

    /// The latest event picture of the camera, if any (no picture: the editor works on a plain canvas).
    private func loadBackdrop() async {
        guard let page = try? await api.events(cameraID: camera.id, limit: 25),
              let event = page.items.first(where: { $0.hasThumbnail }),
              let data = try? await api.eventThumbnail(event.id),
              let image = NSImage(data: data) else { return }
        backdrop = image
    }

    private func save() async {
        busy = true
        defer { busy = false }
        do {
            model.load(try await api.putZones(cameraID: camera.id, model.putInput))
            saveError = nil
            status = "Saved."
        } catch {
            saveError = managementErrorText(error)
        }
    }
}

// MARK: - Shape row

private struct ZoneRow: View {
    let zone: ZoneDraft
    let selected: Bool
    let problem: String?
    let update: ((inout ZoneDraft) -> Void) -> Void
    let delete: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                RoundedRectangle(cornerRadius: 3).fill(ZoneStyle.stroke(zone.kind)).frame(width: 10, height: 10)
                TextField("Name", text: Binding(get: { zone.name }, set: { v in update { $0.name = v } }))
                    .textFieldStyle(.roundedBorder)
            }
            HStack(spacing: 6) {
                if zone.kind == .line {
                    Picker("Direction", selection: Binding(get: { zone.direction }, set: { v in update { $0.direction = v } })) {
                        ForEach(ZoneLines.directions, id: \.value) { Text($0.label).tag($0.value) }
                    }
                    .labelsHidden()
                } else {
                    Picker("Kind", selection: Binding(get: { zone.kind }, set: { v in update { $0.kind = v } })) {
                        Text("Detection zone").tag(V2.ZoneKind.detect)
                        Text("Privacy mask").tag(V2.ZoneKind.mask)
                    }
                    .labelsHidden()
                }
                Toggle("On", isOn: Binding(get: { zone.enabled }, set: { v in update { $0.enabled = v } }))
                    .toggleStyle(.checkbox)
                Button(role: .destructive, action: delete) { Image(systemName: "trash") }
                    .help("Delete this shape")
            }
            if zone.kind == .line {
                Picker("Raises", selection: Binding(get: { zone.severity }, set: { v in update { $0.severity = v } })) {
                    ForEach(ZoneLines.severities, id: \.value) { Text($0.label).tag($0.value) }
                }
            }
            if let problem {
                Text(problem).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
        }
        .controlSize(.small)
        .padding(8)
        .background(selected ? SentinelTheme.accent.opacity(0.14) : SentinelTheme.panelRaised,
                    in: RoundedRectangle(cornerRadius: SentinelMetrics.controlRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.controlRadius)
            .stroke(selected ? SentinelTheme.accent : SentinelTheme.line))
        .contentShape(Rectangle())
    }
}

// MARK: - Canvas

enum ZoneStyle {
    static func stroke(_ kind: V2.ZoneKind) -> Color {
        switch kind {
        case .detect: return SentinelTheme.accent
        case .mask: return Color(white: 0.85)
        case .line: return SentinelTheme.amber
        }
    }

    static func fill(_ kind: V2.ZoneKind) -> Color {
        switch kind {
        case .detect: return SentinelTheme.accent.opacity(0.22)
        case .mask: return Color.black.opacity(0.62)
        case .line: return .clear
        }
    }
}

private struct ZoneCanvas: View {
    @Binding var model: ZoneEditorModel
    let backdrop: NSImage?
    let aspect: Double
    let cameraName: String

    @State private var pressed = false
    @State private var cursorPushed = false
    @FocusState private var focused: Bool

    var body: some View {
        GeometryReader { proxy in
            let size = ZoneGeometry.canvasSize(aspect: aspect, in: proxy.size)
            ZStack {
                if let backdrop {
                    Image(nsImage: backdrop).resizable().interpolation(.medium)
                } else {
                    Rectangle().fill(SentinelTheme.well)
                    VStack(spacing: 4) {
                        Text(cameraName).font(.headline)
                        Text("No recent picture from this camera. You can still draw zones on the canvas.")
                            .font(.caption)
                    }
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding()
                }
                Canvas { ctx, sz in draw(&ctx, sz) }
            }
            .frame(width: size.width, height: size.height)
            .clipShape(Rectangle())
            .overlay(Rectangle().stroke(focused ? SentinelTheme.accent : SentinelTheme.line))
            .contentShape(Rectangle())
            .gesture(DragGesture(minimumDistance: 0, coordinateSpace: .local)
                .onChanged { value in
                    if pressed == false {
                        pressed = true
                        focused = true
                        model.pointerDown(at: value.startLocation, in: size)
                    }
                    model.pointerDragged(to: value.location, in: size)
                }
                .onEnded { value in
                    pressed = false
                    model.pointerUp(at: value.location, in: size, time: ProcessInfo.processInfo.systemUptime,
                                    doubleClickInterval: NSEvent.doubleClickInterval)
                })
            .focusable()
            .focused($focused)
            .onDeleteCommand { model.deleteKey() }
            .onExitCommand { model.cancelDrawing() }
            .onHover { inside in
                if inside, cursorPushed == false { NSCursor.crosshair.push(); cursorPushed = true }
                if inside == false, cursorPushed { NSCursor.pop(); cursorPushed = false }
            }
            .onDisappear { if cursorPushed { NSCursor.pop(); cursorPushed = false } }
            .frame(width: proxy.size.width, height: proxy.size.height)
        }
        .accessibilityLabel("Zones of \(cameraName)")
    }

    private func path(_ pts: [ZonePoint], in size: CGSize, closed: Bool) -> Path {
        var p = Path()
        guard let first = pts.first else { return p }
        p.move(to: ZoneGeometry.position(first, in: size))
        for q in pts.dropFirst() { p.addLine(to: ZoneGeometry.position(q, in: size)) }
        if closed { p.closeSubpath() }
        return p
    }

    private func draw(_ ctx: inout GraphicsContext, _ size: CGSize) {
        for zone in model.zones {
            let selected = model.selected == zone.key
            let bad = zone.problem != nil
            let color = bad ? SentinelTheme.alarm : ZoneStyle.stroke(zone.kind)
            let style = StrokeStyle(lineWidth: selected ? 3 : 2, lineJoin: .round, dash: zone.enabled ? [] : [6, 4])
            if zone.kind == .line {
                guard zone.points.count == 2 else { continue }
                ctx.stroke(path(zone.points, in: size, closed: false), with: .color(color.opacity(zone.enabled ? 1 : 0.6)),
                           style: StrokeStyle(lineWidth: selected ? 4 : 3, lineCap: .round, dash: style.dash))
                if bad == false {
                    let a = Double(size.width / max(size.height, 1))
                    for arrow in ZoneLines.directionArrows(zone.points[0], zone.points[1], zone.direction, length: 0.09, aspect: a) {
                        ctx.stroke(path([arrow.from, arrow.to], in: size, closed: false), with: .color(color), lineWidth: 2)
                        ctx.fill(path(ZoneLines.arrowHead(arrow.from, arrow.to, size: 0.03, aspect: a), in: size, closed: true), with: .color(color))
                    }
                }
            } else {
                let shape = path(zone.points, in: size, closed: true)
                ctx.fill(shape, with: .color(ZoneStyle.fill(zone.kind).opacity(zone.enabled ? 1 : 0.5)))
                ctx.stroke(shape, with: .color(color), style: style)
            }
            if selected {
                for v in zone.points { handle(&ctx, ZoneGeometry.position(v, in: size), radius: 5, color: color) }
            }
        }
        if let d = model.drawing {
            let color = ZoneStyle.stroke(d.kind)
            if d.points.count > 1 {
                ctx.stroke(path(d.points, in: size, closed: false), with: .color(color),
                           style: StrokeStyle(lineWidth: 2, lineJoin: .round, dash: [5, 3]))
            }
            for (i, v) in d.points.enumerated() {
                handle(&ctx, ZoneGeometry.position(v, in: size), radius: i == 0 ? 6 : 4, color: color)
            }
        }
    }

    private func handle(_ ctx: inout GraphicsContext, _ c: CGPoint, radius r: CGFloat, color: Color) {
        let rect = CGRect(x: c.x - r, y: c.y - r, width: 2 * r, height: 2 * r)
        ctx.fill(Path(ellipseIn: rect), with: .color(.white))
        ctx.stroke(Path(ellipseIn: rect), with: .color(color), lineWidth: 2)
    }
}

#Preview("Zones & Privacy") {
    ZonesPane(api: ManagementPreview.api()).frame(width: 1000, height: 640)
}
