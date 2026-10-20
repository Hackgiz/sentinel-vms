import CoreGraphics
import Foundation

// Export options (port of web `exportOptionsModel.ts`): a watermark and up to
// eight pixelated regions. The server validates the same rules and composes
// the watermark text itself, so the client only says "on".

public struct ExportOptions: Hashable, Sendable {
    public var watermark: Bool
    public var regions: [V2.ExportRegion]

    public init(watermark: Bool = false, regions: [V2.ExportRegion] = []) {
        self.watermark = watermark
        self.regions = regions
    }

    public static let none = ExportOptions()

    /// `watermark` for a request body: nil (omitted) when off.
    public var watermarkField: Bool? { watermark ? true : nil }
    /// `blurRegions` for a request body: nil (omitted) when empty.
    public var regionsField: [V2.ExportRegion]? { regions.isEmpty ? nil : regions }

    /// Whether the options make the server re-encode the clip.
    public var reencodes: Bool { watermark || regions.isEmpty == false }

    /// "watermark, 2 pixelated regions" ("" with no options).
    public var summary: String {
        var parts: [String] = []
        if watermark { parts.append("watermark") }
        let n = regions.count
        if n > 0 { parts.append("\(n) pixelated region\(n == 1 ? "" : "s")") }
        return parts.joined(separator: ", ")
    }

    /// The line under the form, or nil with no options.
    public var note: String? {
        reencodes ? "\(summary): the clip is re-encoded (it is no longer the camera's own video), so it takes longer." : nil
    }

    /// The button label: "Pixelate Regions…" or "Pixelate Regions… (2)".
    public var regionsButtonTitle: String {
        regions.isEmpty ? "Pixelate Regions…" : "Pixelate Regions… (\(regions.count))"
    }
}

public enum ExportRegionRules {
    public static let maxRegions = 8
    public static let minSize = 0.01
    public static let watermarkHelp = "Burn who exported the clip, when, and the case id into the video. Re-encodes the clip."

    private static func round4(_ v: Double) -> Double { (v * 10_000).rounded() / 10_000 }

    /// The rectangle spanned by two normalized corner points, in any order.
    public static func rect(from a: CGPoint, to b: CGPoint) -> V2.ExportRegion {
        V2.ExportRegion(x0: round4(min(a.x, b.x)), y0: round4(min(a.y, b.y)),
                        x1: round4(max(a.x, b.x)), y1: round4(max(a.y, b.y)))
    }

    /// A view point as a normalized point of a canvas of `size`, clamped to 0–1.
    public static func normalize(_ p: CGPoint, in size: CGSize) -> CGPoint {
        guard size.width > 0, size.height > 0 else { return .zero }
        return CGPoint(x: min(max(p.x / size.width, 0), 1), y: min(max(p.y / size.height, 0), 1))
    }

    /// A region as a view rectangle of a canvas of `size`.
    public static func viewRect(_ r: V2.ExportRegion, in size: CGSize) -> CGRect {
        CGRect(x: r.x0 * size.width, y: r.y0 * size.height,
               width: (r.x1 - r.x0) * size.width, height: (r.y1 - r.y0) * size.height)
    }

    /// Why a region can't be used, or nil when it is fine.
    public static func problem(_ r: V2.ExportRegion) -> String? {
        for v in [r.x0, r.y0, r.x1, r.y1] where v.isFinite == false || v < 0 || v > 1 {
            return "The region is outside the picture."
        }
        if r.x1 - r.x0 < minSize || r.y1 - r.y0 < minSize { return "The region is too small." }
        return nil
    }

    /// Why a whole set can't be sent, or nil when it can.
    public static func problem(_ list: [V2.ExportRegion]) -> String? {
        if list.count > maxRegions { return "At most \(maxRegions) regions." }
        return list.lazy.compactMap(problem).first
    }

    /// Adds a drawn region; a tiny drag (a click) or a ninth region is ignored.
    public static func add(_ r: V2.ExportRegion, to list: [V2.ExportRegion]) -> [V2.ExportRegion] {
        if list.count >= maxRegions || problem(r) != nil { return list }
        return list + [r]
    }

    public static func remove(at index: Int, from list: [V2.ExportRegion]) -> [V2.ExportRegion] {
        guard list.indices.contains(index) else { return list }
        var copy = list
        copy.remove(at: index)
        return copy
    }

    /// "Region 1: 25% × 10% of the picture".
    public static func label(_ r: V2.ExportRegion, index: Int) -> String {
        "Region \(index + 1): \(Int(((r.x1 - r.x0) * 100).rounded()))% × \(Int(((r.y1 - r.y0) * 100).rounded()))% of the picture"
    }

    /// The apply button: "Use 2 Regions", "Use 1 Region", "Use No Regions".
    public static func applyTitle(count: Int) -> String {
        count == 0 ? "Use No Regions" : "Use \(count) Region\(count == 1 ? "" : "s")"
    }

    /// The canvas's width/height: the camera's stream when known, else 16:9.
    public static func aspect(width: Int?, height: Int?) -> Double {
        guard let width, let height, width > 0, height > 0 else { return 16.0 / 9.0 }
        let a = Double(width) / Double(height)
        return min(max(a, 0.25), 4)
    }
}
