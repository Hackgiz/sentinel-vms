import SwiftUI
import SentinelCore

/// Wave 11 (stream A) drawing on the playback ruler, layered over
/// `RulerCanvas` in the same coordinates: recovered-from-camera footage
/// hatched on the selected camera's band, and that camera's bookmarks —
/// a flag at a point, a tinted bar over a range — in their colors. Clicking
/// a glyph seeks to it; everywhere else the ruler's own drag and zoom work
/// as before (only the glyphs take clicks).
struct PlaybackRulerDecorations: View {
    @ObservedObject var controller: PlaybackController
    @ObservedObject var bookmarks: BookmarkStore
    let cameraID: String?

    init(controller: PlaybackController, cameraID: String?) {
        self.controller = controller
        self.bookmarks = BookmarkStore.for(controller)
        self.cameraID = cameraID
    }

    /// The selected camera's band in `RulerCanvas` (y 22, 16 pt tall).
    static let bandY: CGFloat = 22
    static let bandHeight: CGFloat = 16

    var body: some View {
        GeometryReader { proxy in
            let width = Double(proxy.size.width)
            let range = TimelineRuler.visibleRange(center: controller.position, span: controller.visibleSpan)
            let list = bookmarks.book.bookmarks(for: cameraID)
            let glyphs = BookmarkLogic.glyphs(list, from: range.from, to: range.to, width: width)
            let spans = cameraID.flatMap { controller.timelines[$0] }?.spans ?? []
            let edge = EdgeRecoveryLogic.edgeRanges(spans)
            let lowRes = RecordingModeLogic.lowResRanges(spans) // wave 22
            let byID = Dictionary(list.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })

            ZStack(alignment: .topLeading) {
                Canvas { ctx, size in
                    drawEdge(edge, in: &ctx, size: size, range: range)
                    drawLowResLabels(lowRes, in: &ctx, size: size, range: range)
                    for glyph in glyphs {
                        guard let b = byID[glyph.id] else { continue }
                        drawGlyph(glyph, color: Self.color(b.color), in: &ctx, size: size)
                    }
                }
                .allowsHitTesting(false)

                ForEach(glyphs, id: \.id) { glyph in
                    if let b = byID[glyph.id] {
                        hitTarget(glyph, bookmark: b)
                    }
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        }
    }

    static func color(_ c: V2.BookmarkColor) -> Color {
        let hex = c.hex
        return Color(red: Double((hex >> 16) & 0xFF) / 255, green: Double((hex >> 8) & 0xFF) / 255, blue: Double(hex & 0xFF) / 255)
    }

    // MARK: Drawing

    /// Diagonal stripes over the band where the footage came from the camera's SD card.
    private func drawEdge(_ ranges: [TimelineGaps.Range], in ctx: inout GraphicsContext, size: CGSize, range: (from: Int64, to: Int64)) {
        let span = Double(max(range.to - range.from, 1))
        let w = Double(size.width)
        func x(_ t: Int64) -> CGFloat { CGFloat(Double(t - range.from) / span * w) }
        for r in ranges where r.end > range.from && r.start < range.to {
            let x0 = x(max(r.start, range.from)), x1 = x(min(r.end, range.to))
            let rect = CGRect(x: x0, y: Self.bandY, width: max(x1 - x0, 1), height: Self.bandHeight)
            var inner = ctx
            inner.clip(to: Path(roundedRect: rect, cornerRadius: 2))
            var hatch = Path()
            var hx = rect.minX - rect.height
            while hx < rect.maxX {
                hatch.move(to: CGPoint(x: hx, y: rect.maxY))
                hatch.addLine(to: CGPoint(x: hx + rect.height, y: rect.minY))
                hx += 4
            }
            inner.stroke(hatch, with: .color(.white.opacity(0.6)), lineWidth: 1.2)
        }
    }

    /// Wave 22: "Low-res" written inside the lighter band where it fits (the legend
    /// in the bar's top row explains it on hover).
    private func drawLowResLabels(_ ranges: [TimelineGaps.Range], in ctx: inout GraphicsContext, size: CGSize, range: (from: Int64, to: Int64)) {
        let span = Double(max(range.to - range.from, 1))
        let w = Double(size.width)
        func x(_ t: Int64) -> CGFloat { CGFloat(Double(t - range.from) / span * w) }
        for r in ranges where r.end > range.from && r.start < range.to {
            let x0 = x(max(r.start, range.from)), x1 = x(min(r.end, range.to))
            guard x1 - x0 >= 56 else { continue }
            ctx.draw(Text(RecordingModeLogic.lowResLegend).font(.system(size: 9, weight: .semibold)).foregroundColor(.white.opacity(0.85)),
                     at: CGPoint(x: x0 + 5, y: Self.bandY + Self.bandHeight / 2), anchor: .leading)
        }
    }

    private func drawGlyph(_ g: BookmarkLogic.Glyph, color: Color, in ctx: inout GraphicsContext, size: CGSize) {
        if g.isRange {
            let rect = CGRect(x: g.x, y: 2, width: max(g.x1 - g.x, 2), height: 6)
            ctx.fill(Path(roundedRect: rect, cornerRadius: 2), with: .color(color.opacity(0.75)))
            ctx.fill(Path(CGRect(x: g.x, y: 2, width: 1.5, height: Self.bandY + Self.bandHeight - 2)), with: .color(color.opacity(0.6)))
        } else {
            ctx.fill(Path(CGRect(x: g.x - 0.75, y: 1, width: 1.5, height: Self.bandY + Self.bandHeight - 1)), with: .color(color))
            var flag = Path()
            flag.move(to: CGPoint(x: g.x + 0.75, y: 1))
            flag.addLine(to: CGPoint(x: g.x + 8, y: 4.5))
            flag.addLine(to: CGPoint(x: g.x + 0.75, y: 8))
            flag.closeSubpath()
            ctx.fill(flag, with: .color(color))
        }
    }

    // MARK: Clicking

    private func hitTarget(_ g: BookmarkLogic.Glyph, bookmark b: V2.Bookmark) -> some View {
        let width = g.isRange ? max(g.x1 - g.x, 10) : 12
        let tip = BookmarkLogic.label(b) + (b.isRange ? " (\(BookmarkLogic.durationText(b.durationMs)))" : "")
            + (b.note.isEmpty ? "" : "\n\(b.note)") + "\nClick to go to it"
        return Color.clear
            .frame(width: CGFloat(width), height: 12)
            .contentShape(Rectangle())
            .offset(x: CGFloat(g.isRange ? g.x : g.x - 4), y: 0)
            .onTapGesture { controller.seek(to: b.atMs) }
            .help(tip)
            .contextMenu {
                Button("Go to Bookmark") { controller.seek(to: b.atMs) }
                if b.canEdit {
                    Button("Edit Bookmark…") { bookmarks.beginEdit(b) }
                }
            }
    }
}
