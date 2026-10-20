import AppKit
import AVFoundation
import SwiftUI
import SentinelCore

/// Export options for the export forms (long export; the evidence lock once
/// the coordinator wires it): a Watermark toggle and "Pixelate Regions…",
/// which opens the region picker on one camera.
struct ExportOptionsFields: View {
    @Binding var options: ExportOptions
    /// The camera to draw regions on; nil when there is not exactly one.
    var camera: V2.Camera?
    var disabled = false

    @State private var picking = false

    var body: some View {
        HStack(spacing: 10) {
            Toggle("Watermark", isOn: $options.watermark)
                .toggleStyle(.checkbox)
                .disabled(disabled)
                .help(ExportRegionRules.watermarkHelp)
            Button(options.regionsButtonTitle) { picking = true }
                .disabled(disabled || camera == nil)
                .help(camera == nil ? "Pick exactly one camera to draw regions on" : "Draw regions to pixelate in the exported video")
        }
        .sheet(isPresented: $picking) {
            if let camera {
                RegionPickerSheet(camera: camera, initial: options.regions) { options.regions = $0 }
            }
        }
        .onChange(of: camera?.id) { _ in
            // Regions are drawn on one camera's picture; another camera starts clean.
            if options.regions.isEmpty == false { options.regions = [] }
        }
    }
}

/// Draw up to eight rectangles over the camera's live picture (stretched to
/// the stream's aspect so the normalized coordinates match the frame). When
/// the camera is offline, the role can't view live, or the player fails, a
/// plain canvas with the camera's name stands in (the web does the same
/// when it has no picture).
struct RegionPickerSheet: View {
    let camera: V2.Camera
    let initial: [V2.ExportRegion]
    let onApply: ([V2.ExportRegion]) -> Void

    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var runtime: SiteRuntime
    @StateObject private var player = LivePlayerStatus()
    @State private var regions: [V2.ExportRegion] = []
    @State private var drag: (from: CGPoint, to: CGPoint)?
    @State private var loaded = false

    private var aspect: Double { ExportRegionRules.aspect(width: camera.status.width, height: camera.status.height) }
    private var showsVideo: Bool { camera.status.online && session.can(.viewLive) }
    private var playerFailed: Bool { if case .failed = player.state { return true } else { return false } }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Pixelate regions on \(camera.name)").font(.headline)
            Text("Drag on the picture to draw each region (up to \(ExportRegionRules.maxRegions)). They are pixelated in the exported video, on top of the camera's privacy masks. The same regions apply for the whole range.")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            canvas
                .aspectRatio(aspect, contentMode: .fit)
                .frame(maxWidth: .infinity, maxHeight: 420)
            if regions.isEmpty == false {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(regions.enumerated()), id: \.offset) { index, region in
                        HStack(spacing: 8) {
                            RoundedRectangle(cornerRadius: 2).fill(Color.black).overlay(RoundedRectangle(cornerRadius: 2).stroke(SentinelTheme.amber))
                                .frame(width: 14, height: 10)
                            Text(ExportRegionRules.label(region, index: index)).font(.callout)
                            Spacer()
                            Button("Remove", role: .destructive) { regions = ExportRegionRules.remove(at: index, from: regions) }
                                .controlSize(.small)
                        }
                    }
                }
            }
            HStack {
                if regions.count >= ExportRegionRules.maxRegions {
                    Text("That's the most regions an export can have.").font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(ExportRegionRules.applyTitle(count: regions.count)) {
                    onApply(regions)
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .buttonStyle(.borderedProminent)
            }
        }
        .padding(20)
        .frame(width: 760)
        .onAppear {
            guard loaded == false else { return }
            loaded = true
            regions = initial
        }
    }

    private var canvas: some View {
        GeometryReader { geo in
            ZStack(alignment: .topLeading) {
                backdrop(size: geo.size)
                ForEach(Array(regions.enumerated()), id: \.offset) { _, region in
                    regionShape(ExportRegionRules.viewRect(region, in: geo.size), drawing: false)
                }
                if let drag {
                    let live = ExportRegionRules.rect(from: ExportRegionRules.normalize(drag.from, in: geo.size),
                                                      to: ExportRegionRules.normalize(drag.to, in: geo.size))
                    regionShape(ExportRegionRules.viewRect(live, in: geo.size), drawing: true)
                }
            }
            .frame(width: geo.size.width, height: geo.size.height)
            .clipShape(RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(SentinelTheme.line))
            .contentShape(Rectangle())
            .gesture(
                DragGesture(minimumDistance: 0)
                    .onChanged { value in drag = (value.startLocation, value.location) }
                    .onEnded { value in
                        let r = ExportRegionRules.rect(from: ExportRegionRules.normalize(value.startLocation, in: geo.size),
                                                       to: ExportRegionRules.normalize(value.location, in: geo.size))
                        regions = ExportRegionRules.add(r, to: regions)
                        drag = nil
                    })
        }
    }

    @ViewBuilder
    private func backdrop(size: CGSize) -> some View {
        ZStack {
            Color.black
            if showsVideo && playerFailed == false {
                LiveVideoView(cameraID: camera.id, broker: runtime.video, gravity: .resize, status: player)
                    .allowsHitTesting(false)
                if player.state == .connecting {
                    Text("Connecting to \(camera.name)…").font(.caption).foregroundStyle(.white.opacity(0.7))
                }
            } else {
                fallback
            }
        }
        .frame(width: size.width, height: size.height)
    }

    /// The plain 16:9 (or stream-aspect) canvas: a grid and the camera's name.
    private var fallback: some View {
        ZStack {
            Color(white: 0.16)
            Canvas { context, size in
                var path = Path()
                for i in 1..<10 {
                    let x = size.width * CGFloat(i) / 10
                    let y = size.height * CGFloat(i) / 10
                    path.move(to: CGPoint(x: x, y: 0)); path.addLine(to: CGPoint(x: x, y: size.height))
                    path.move(to: CGPoint(x: 0, y: y)); path.addLine(to: CGPoint(x: size.width, y: y))
                }
                context.stroke(path, with: .color(.white.opacity(0.08)), lineWidth: 1)
            }
            VStack(spacing: 4) {
                Text(camera.name).font(.title3.weight(.semibold)).foregroundStyle(.white.opacity(0.85))
                Text("No live picture from this camera. You can still draw regions on the frame.")
                    .font(.caption).foregroundStyle(.white.opacity(0.6))
            }
            .allowsHitTesting(false)
        }
    }

    private func regionShape(_ rect: CGRect, drawing: Bool) -> some View {
        Rectangle()
            .fill(Color.black.opacity(drawing ? 0.35 : 0.6))
            .overlay(Rectangle().stroke(SentinelTheme.amber, style: StrokeStyle(lineWidth: 1.5, dash: drawing ? [4, 3] : [])))
            .frame(width: max(rect.width, 1), height: max(rect.height, 1))
            .offset(x: rect.minX, y: rect.minY)
            .allowsHitTesting(false)
    }
}
