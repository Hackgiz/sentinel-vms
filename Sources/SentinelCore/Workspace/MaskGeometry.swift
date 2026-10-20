import CoreGraphics
import Foundation

/// Where a camera's privacy masks go over its video (Wave 11, stream A;
/// the web's `MaskOverlay.tsx`). Masks are polygons of normalized [x, y]
/// vertices against the full frame; the video sits in its tile either
/// letterboxed (`resizeAspect`, the web's object-fit: contain) or cropped to
/// fill (`resizeAspectFill`). Display-level only: the server burns masks into
/// snapshots and exports itself.
public enum MaskGeometry {
    /// The frame size assumed when the camera hasn't reported one (the web's `defaultFrame`).
    public static let defaultFrame = CGSize(width: 1920, height: 1080)

    public enum Gravity: Equatable, Sendable {
        /// Fit inside, letterboxed.
        case fit
        /// Fill, cropping the overflow.
        case fill
    }

    /// The stream's pixel size from `camera.status`, or 1920×1080.
    public static func frameSize(width: Int?, height: Int?) -> CGSize {
        if let width, let height, width > 0, height > 0 { return CGSize(width: width, height: height) }
        return defaultFrame
    }

    /// The rect the video occupies inside `container`: centered, aspect kept,
    /// fitted (letterboxed) or filled (overflowing the container).
    public static func videoRect(container: CGSize, frame: CGSize, gravity: Gravity) -> CGRect {
        guard container.width > 0, container.height > 0, frame.width > 0, frame.height > 0 else {
            return CGRect(origin: .zero, size: container)
        }
        let sx = container.width / frame.width
        let sy = container.height / frame.height
        let scale = gravity == .fit ? min(sx, sy) : max(sx, sy)
        let size = CGSize(width: frame.width * scale, height: frame.height * scale)
        return CGRect(x: (container.width - size.width) / 2, y: (container.height - size.height) / 2,
                      width: size.width, height: size.height)
    }

    /// The usable polygons of `masks` (at least three [x, y] vertices),
    /// coordinates clamped to 0–1.
    public static func polygons(_ masks: [[[Double]]]?) -> [[CGPoint]] {
        (masks ?? []).compactMap { polygon in
            let points = polygon.compactMap { v -> CGPoint? in
                guard v.count >= 2, v[0].isFinite, v[1].isFinite else { return nil }
                return CGPoint(x: min(max(v[0], 0), 1), y: min(max(v[1], 0), 1))
            }
            return points.count >= 3 ? points : nil
        }
    }

    /// `polygon` (normalized, y down) mapped into `rect` (y down, as SwiftUI draws).
    public static func place(_ polygon: [CGPoint], in rect: CGRect) -> [CGPoint] {
        polygon.map { CGPoint(x: rect.minX + $0.x * rect.width, y: rect.minY + $0.y * rect.height) }
    }

    /// Every mask polygon in view coordinates for a `container`-sized tile.
    public static func layout(masks: [[[Double]]]?, container: CGSize, frame: CGSize, gravity: Gravity) -> [[CGPoint]] {
        let rect = videoRect(container: container, frame: frame, gravity: gravity)
        return polygons(masks).map { place($0, in: rect) }
    }
}
