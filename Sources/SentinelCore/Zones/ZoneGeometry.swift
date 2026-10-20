import CoreGraphics
import Foundation

// View-free geometry for the Zones & Privacy editor (wave 11 stream E).
// Polygons and lines are vertex lists normalized 0...1 against the full
// frame. Mirrors `linux/web/src/shared/zones.ts` and `lines.ts`; the server
// validates the same rules (internal/store/zones.go, zones_lines.go), this
// only lets the editor explain a bad shape before saving.

/// A normalized [x, y] point (0...1 of the frame, y grows downward).
public struct ZonePoint: Hashable, Sendable {
    public var x: Double
    public var y: Double

    public init(_ x: Double, _ y: Double) {
        self.x = x
        self.y = y
    }

    /// From the wire form; a malformed pair reads as NaN (so validation calls it outside the picture).
    public init(_ pair: [Double]) {
        if pair.count == 2 { self.init(pair[0], pair[1]) } else { self.init(.nan, .nan) }
    }

    public var pair: [Double] { [x, y] }

    public static func list(_ pairs: [[Double]]) -> [ZonePoint] { pairs.map(ZonePoint.init) }
}

public enum ZoneGeometry {
    public static let minPoints = 3
    public static let maxPoints = 20
    public static let maxZones = 50
    public static let maxNameLength = 100
    /// Default distance (normalized) within which a click closes the polygon on its first point.
    public static let closeTolerance = 0.025

    // MARK: Primitives

    static func cross(_ a: ZonePoint, _ b: ZonePoint, _ c: ZonePoint) -> Double {
        (b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x)
    }

    static func dot(_ a: ZonePoint, _ b: ZonePoint, _ c: ZonePoint) -> Double {
        (b.x - a.x) * (c.x - a.x) + (b.y - a.y) * (c.y - a.y)
    }

    private static func sgn(_ v: Double) -> Int { v > 0 ? 1 : (v < 0 ? -1 : 0) }

    private static func onSegment(_ a: ZonePoint, _ b: ZonePoint, _ c: ZonePoint) -> Bool {
        min(a.x, b.x) <= c.x && c.x <= max(a.x, b.x) && min(a.y, b.y) <= c.y && c.y <= max(a.y, b.y)
    }

    /// Whether segments ab and cd cross or touch (collinear overlaps included).
    public static func segmentsTouch(_ a: ZonePoint, _ b: ZonePoint, _ c: ZonePoint, _ d: ZonePoint) -> Bool {
        let d1 = sgn(cross(a, b, c))
        let d2 = sgn(cross(a, b, d))
        let d3 = sgn(cross(c, d, a))
        let d4 = sgn(cross(c, d, b))
        if d1 * d2 < 0 && d3 * d4 < 0 { return true }
        return (d1 == 0 && onSegment(a, b, c)) || (d2 == 0 && onSegment(a, b, d))
            || (d3 == 0 && onSegment(c, d, a)) || (d4 == 0 && onSegment(c, d, b))
    }

    private static func inFrame(_ p: ZonePoint) -> Bool {
        p.x.isFinite && p.y.isFinite && p.x >= 0 && p.x <= 1 && p.y >= 0 && p.y <= 1
    }

    // MARK: Polygons

    /// Absolute shoelace area (normalized units).
    public static func polygonArea(_ pts: [ZonePoint]) -> Double {
        guard pts.isEmpty == false else { return 0 }
        var s = 0.0
        for i in pts.indices {
            let j = (i + 1) % pts.count
            s += pts[i].x * pts[j].y - pts[j].x * pts[i].y
        }
        return abs(s) / 2
    }

    /// Why a polygon can't be saved, or nil when it is fine.
    public static func polygonProblem(_ pts: [ZonePoint]) -> String? {
        let n = pts.count
        if n < minPoints || n > maxPoints { return "A zone needs \(minPoints) to \(maxPoints) points." }
        for p in pts where inFrame(p) == false { return "A point is outside the picture." }
        for i in 0..<n {
            let q = pts[(i + 1) % n]
            if pts[i].x == q.x && pts[i].y == q.y { return "Two neighbouring points are in the same place." }
        }
        for i in 0..<n {
            let a = pts[i]
            let b = pts[(i + 1) % n]
            for j in (i + 1)..<n {
                let c = pts[j]
                let d = pts[(j + 1) % n]
                if j == i + 1 || (i == 0 && j == n - 1) {
                    // Neighbouring edges meet at one vertex; doubling back along each other is out.
                    let (shared, far1, far2) = (i == 0 && j == n - 1) ? (a, b, c) : (b, a, d)
                    if cross(shared, far1, far2) == 0 && dot(shared, far1, far2) > 0 { return "The outline folds back on itself." }
                    continue
                }
                if segmentsTouch(a, b, c, d) { return "The outline crosses itself." }
            }
        }
        if polygonArea(pts) < 1e-6 { return "The zone has no area." }
        return nil
    }

    /// Whether the polygon's outline crosses or folds back on itself.
    public static func selfIntersects(_ pts: [ZonePoint]) -> Bool {
        guard let problem = polygonProblem(pts) else { return false }
        return problem.contains("crosses") || problem.contains("folds back")
    }

    /// Whether a click at `p` should close the polygon being drawn (near its first vertex).
    public static func closesPolygon(_ pts: [ZonePoint], _ p: ZonePoint, tolerance: Double = closeTolerance) -> Bool {
        guard pts.count >= minPoints else { return false }
        return hypot(p.x - pts[0].x, p.y - pts[0].y) <= tolerance
    }

    /// Even-odd point-in-polygon test (any winding).
    public static func contains(_ pts: [ZonePoint], _ p: ZonePoint) -> Bool {
        guard pts.count >= 3 else { return false }
        var inside = false
        var j = pts.count - 1
        for i in pts.indices {
            let a = pts[i], b = pts[j]
            if (a.y > p.y) != (b.y > p.y), p.x < (b.x - a.x) * (p.y - a.y) / (b.y - a.y) + a.x {
                inside.toggle()
            }
            j = i
        }
        return inside
    }

    // MARK: Normalization

    public static func clamp01(_ v: Double) -> Double { v.isFinite ? min(1, max(0, v)) : 0 }

    /// A position inside a canvas of `size` (origin top-left) as a clamped normalized point.
    public static func normalize(_ point: CGPoint, in size: CGSize) -> ZonePoint {
        ZonePoint(clamp01(Double(point.x) / Double(size.width > 0 ? size.width : 1)),
                  clamp01(Double(point.y) / Double(size.height > 0 ? size.height : 1)))
    }

    /// A normalized point in a canvas of `size`.
    public static func position(_ p: ZonePoint, in size: CGSize) -> CGPoint {
        CGPoint(x: CGFloat(p.x) * size.width, y: CGFloat(p.y) * size.height)
    }

    /// Rounds to 4 decimals so saved zones are stable across edits.
    public static func round(_ p: ZonePoint) -> ZonePoint {
        ZonePoint((p.x * 1e4).rounded() / 1e4, (p.y * 1e4).rounded() / 1e4)
    }

    /// Moves every point by (dx, dy), stopping at the frame's edges so the shape keeps its form.
    public static func translate(_ pts: [ZonePoint], dx: Double, dy: Double) -> [ZonePoint] {
        guard pts.isEmpty == false else { return pts }
        let minX = pts.map(\.x).min()!, maxX = pts.map(\.x).max()!
        let minY = pts.map(\.y).min()!, maxY = pts.map(\.y).max()!
        let cx = min(max(dx, -minX), 1 - maxX)
        let cy = min(max(dy, -minY), 1 - maxY)
        return pts.map { round(ZonePoint($0.x + cx, $0.y + cy)) }
    }

    /// The canvas size for a picture of `aspect` (width / height) fitted into `container`.
    public static func canvasSize(aspect: Double, in container: CGSize) -> CGSize {
        let a = aspect.isFinite && aspect > 0 ? aspect : 16.0 / 9.0
        guard container.width > 0, container.height > 0 else { return .zero }
        if Double(container.width) / Double(container.height) > a {
            return CGSize(width: container.height * CGFloat(a), height: container.height)
        }
        return CGSize(width: container.width, height: container.width / CGFloat(a))
    }

    // MARK: Hit-testing (canvas coordinates)

    /// Distance from `p` to segment ab, all in canvas points.
    public static func distance(from p: CGPoint, toSegment a: CGPoint, _ b: CGPoint) -> CGFloat {
        let dx = b.x - a.x, dy = b.y - a.y
        let len2 = dx * dx + dy * dy
        guard len2 > 0 else { return hypot(p.x - a.x, p.y - a.y) }
        let t = min(1, max(0, ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2))
        return hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy))
    }

    /// The vertex of `pts` nearest `point` within `radius` canvas points.
    public static func vertexHit(_ point: CGPoint, _ pts: [ZonePoint], in size: CGSize, radius: CGFloat = 8) -> Int? {
        var best: (index: Int, distance: CGFloat)?
        for (i, v) in pts.enumerated() {
            let c = position(v, in: size)
            let d = hypot(c.x - point.x, c.y - point.y)
            guard d <= radius else { continue }
            if best == nil || d <= best!.distance { best = (i, d) }
        }
        return best?.index
    }

    /// Whether `point` lands on a shape: inside a polygon (or on its outline), near a line.
    public static func shapeHit(_ point: CGPoint, kind: V2.ZoneKind, _ pts: [ZonePoint], in size: CGSize, tolerance: CGFloat = 6) -> Bool {
        let c = pts.map { position($0, in: size) }
        if kind == .line {
            guard c.count == 2 else { return false }
            return distance(from: point, toSegment: c[0], c[1]) <= tolerance
        }
        if contains(pts, normalize(point, in: size)) { return true }
        guard c.count >= 2 else { return false }
        for i in c.indices where distance(from: point, toSegment: c[i], c[(i + 1) % c.count]) <= tolerance { return true }
        return false
    }
}

// MARK: - Lines

/// Line crossing tripwires: exactly two points p0 → p1. Mirrors `lines.ts`.
public enum ZoneLines {
    public static let linePoints = 2
    /// Shortest line the server accepts (normalized units).
    public static let minLineLength = 0.01

    public static let directions: [(value: V2.LineDirection, label: String)] = [
        (.both, "Either way"), (.ltr, "Left to right"), (.rtl, "Right to left"),
    ]

    public static let severities: [(value: V2.LineSeverity, label: String)] = [
        (.critical, "Critical alarm"), (.warning, "Warning alarm"), (.info, "Event only"),
    ]

    /// The stored direction; missing or damaged meta watches both ways, as the server does.
    public static func direction(of meta: V2.ZoneLineMeta?) -> V2.LineDirection { meta?.direction ?? .both }

    /// The stored severity; missing or damaged meta is Critical, as the server does.
    public static func severity(of meta: V2.ZoneLineMeta?) -> V2.LineSeverity { meta?.severity ?? .critical }

    /// Why a line can't be saved, or nil when it is fine.
    public static func lineProblem(_ pts: [ZonePoint]) -> String? {
        if pts.count != linePoints { return "A line needs exactly \(linePoints) points." }
        for p in pts where !(p.x.isFinite && p.y.isFinite && p.x >= 0 && p.x <= 1 && p.y >= 0 && p.y <= 1) {
            return "A point is outside the picture."
        }
        if pts[0].x == pts[1].x && pts[0].y == pts[1].y { return "The two points of a line are in the same place." }
        if hypot(pts[0].x - pts[1].x, pts[0].y - pts[1].y) < minLineLength { return "The line is too short to cross." }
        return nil
    }

    /// Which side of p0 → p1 a point is on: positive = the right-hand side
    /// ("ltr" crossings end here), negative = the left, as seen standing at p0
    /// looking at p1 on the picture (y grows downward). Mirrors the server.
    public static func sideOf(_ p0: ZonePoint, _ p1: ZonePoint, _ p: ZonePoint) -> Double {
        (p1.x - p0.x) * (p.y - p0.y) - (p1.y - p0.y) * (p.x - p0.x)
    }

    public struct Arrow: Hashable, Sendable {
        public var from: ZonePoint
        public var to: ZonePoint
    }

    /// The arrows showing which way crossings count, drawn from the line's
    /// middle perpendicular to it: "ltr" toward the right-hand side, "rtl"
    /// toward the left, "both" one each way. `aspect` (picture width / height)
    /// keeps the arrow perpendicular on screen rather than in normalized space.
    public static func directionArrows(_ p0: ZonePoint, _ p1: ZonePoint, _ direction: V2.LineDirection,
                                       length: Double = 0.09, aspect: Double = 16.0 / 9.0) -> [Arrow] {
        let dx = (p1.x - p0.x) * aspect
        let dy = p1.y - p0.y
        let len = hypot(dx, dy)
        guard len > 0 else { return [] }
        // Right-hand normal in y-down screen space is (-dy, dx).
        let nx = -dy / len
        let ny = dx / len
        let mid = ZonePoint((p0.x + p1.x) / 2, (p0.y + p1.y) / 2)
        func toward(_ sign: Double) -> Arrow {
            Arrow(from: mid, to: ZonePoint(mid.x + sign * nx * length / aspect, mid.y + sign * ny * length))
        }
        switch direction {
        case .ltr: return [toward(1)]
        case .rtl: return [toward(-1)]
        case .both: return [toward(1), toward(-1)]
        }
    }

    /// The three corners of an arrowhead at `to` for an arrow from `from` (empty when degenerate).
    public static func arrowHead(_ from: ZonePoint, _ to: ZonePoint, size: Double = 0.025, aspect: Double = 16.0 / 9.0) -> [ZonePoint] {
        let dx = (to.x - from.x) * aspect
        let dy = to.y - from.y
        let len = hypot(dx, dy)
        guard len > 0 else { return [] }
        let ux = dx / len, uy = dy / len
        func corner(_ side: Double) -> ZonePoint {
            ZonePoint(to.x + (-ux * size - uy * side * (size / 2)) / aspect, to.y + -uy * size + ux * side * (size / 2))
        }
        return [to, corner(1), corner(-1)]
    }
}
