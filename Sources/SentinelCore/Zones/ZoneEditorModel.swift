import CoreGraphics
import Foundation

// State of the Zones & Privacy editor (wave 11 stream E): the drafts, the
// shape being drawn, the selection, pointer handling (click to place, drag
// to move, double-click to finish) and the PUT payload. Mirrors the editor
// in `linux/web/src/admin/ZonesSection.tsx`.

/// One zone being edited. `key` is stable across edits (the zone id, or `new-N`).
public struct ZoneDraft: Identifiable, Hashable, Sendable {
    public var key: String
    public var zoneID: String?
    public var kind: V2.ZoneKind
    public var name: String
    public var enabled: Bool
    public var points: [ZonePoint]
    /// Lines only: which way crossings count, relative to p0 → p1.
    public var direction: V2.LineDirection
    /// Lines only: the alarm a crossing raises (`info` = event only).
    public var severity: V2.LineSeverity

    public var id: String { key }

    public init(key: String, zoneID: String? = nil, kind: V2.ZoneKind, name: String, enabled: Bool = true,
                points: [ZonePoint], direction: V2.LineDirection = .both, severity: V2.LineSeverity = .critical) {
        self.key = key
        self.zoneID = zoneID
        self.kind = kind
        self.name = name
        self.enabled = enabled
        self.points = points
        self.direction = direction
        self.severity = severity
    }

    public init(zone: V2.CameraZone) {
        self.init(key: zone.id, zoneID: zone.id, kind: zone.kind, name: zone.name, enabled: zone.enabled,
                  points: ZonePoint.list(zone.points), direction: ZoneLines.direction(of: zone.meta),
                  severity: ZoneLines.severity(of: zone.meta))
    }

    /// Why this shape can't be saved, or nil.
    public var problem: String? {
        if name.count > ZoneGeometry.maxNameLength { return "The name is longer than \(ZoneGeometry.maxNameLength) characters." }
        return kind == .line ? ZoneLines.lineProblem(points) : ZoneGeometry.polygonProblem(points)
    }

    /// The PUT form: meta only on lines, name trimmed.
    public var input: V2.CameraZoneInput {
        V2.CameraZoneInput(id: zoneID, kind: kind, name: name.trimmingCharacters(in: .whitespacesAndNewlines),
                           points: points.map(\.pair),
                           meta: kind == .line ? V2.ZoneLineMeta(direction: direction, severity: severity) : nil,
                           enabled: enabled)
    }

    /// What counts for "unsaved changes" (direction/severity only matter on lines).
    fileprivate var signature: [AnyHashable] {
        [zoneID ?? "", kind.rawValue, name, enabled, points,
         kind == .line ? direction.rawValue : "", kind == .line ? severity.rawValue : ""]
    }

    public static func kindName(_ kind: V2.ZoneKind) -> String {
        switch kind {
        case .detect: return "Detection zone"
        case .mask: return "Privacy mask"
        case .line: return "Line"
        }
    }
}

/// A shape being drawn, point by point.
public struct ZoneDrawing: Hashable, Sendable {
    public var kind: V2.ZoneKind
    public var points: [ZonePoint]

    public var needed: Int { kind == .line ? ZoneLines.linePoints : ZoneGeometry.minPoints }
}

public struct ZoneEditorModel: Sendable {
    public private(set) var zones: [ZoneDraft] = []
    public private(set) var saved: [ZoneDraft] = []
    public private(set) var drawing: ZoneDrawing?
    public var selected: String?

    private var nextKey = 1
    private var lastClick: (point: ZonePoint, time: TimeInterval)?
    private var pointer: Pointer?

    private enum Target: Sendable {
        case none
        case vertex(key: String, index: Int)
        case shape(key: String, start: [ZonePoint])
    }

    private struct Pointer: Sendable {
        var start: CGPoint
        var target: Target
        var moved = false
    }

    /// Pointer travel (canvas points) below which a press counts as a click.
    public static let clickSlop: CGFloat = 3
    /// Two clicks this close (normalized) and quick count as a double-click.
    public static let doubleClickDistance = 0.012

    public init(zones: [V2.CameraZone] = []) {
        load(zones)
    }

    // MARK: Loading and saving

    /// Takes the server's set as both the drafts and the saved baseline.
    public mutating func load(_ stored: [V2.CameraZone]) {
        zones = stored.map(ZoneDraft.init(zone:))
        saved = zones
        drawing = nil
        selected = nil
        pointer = nil
        lastClick = nil
    }

    public mutating func discard() {
        zones = saved
        drawing = nil
        selected = nil
        pointer = nil
    }

    public var isDirty: Bool { zones.map(\.signature) != saved.map(\.signature) }
    public var problems: [String: String] {
        var out: [String: String] = [:]
        for z in zones { if let p = z.problem { out[z.key] = p } }
        return out
    }
    public var isFull: Bool { zones.count >= ZoneGeometry.maxZones }
    public var canSave: Bool { isDirty && problems.isEmpty && drawing == nil }

    /// The body of the one PUT that replaces the camera's set.
    public var putInput: [V2.CameraZoneInput] { zones.map(\.input) }

    // MARK: Drawing

    @discardableResult
    public mutating func startDrawing(_ kind: V2.ZoneKind) -> Bool {
        guard drawing == nil, isFull == false else { return false }
        drawing = ZoneDrawing(kind: kind, points: [])
        selected = nil
        lastClick = nil
        return true
    }

    public mutating func cancelDrawing() {
        drawing = nil
        lastClick = nil
    }

    public mutating func removeLastPoint() {
        guard var d = drawing, d.points.isEmpty == false else { return }
        d.points.removeLast()
        drawing = d
        lastClick = nil
    }

    /// Ends the shape being drawn, if it has enough points. Selects the new shape.
    @discardableResult
    public mutating func finishDrawing() -> Bool {
        guard let d = drawing, d.points.count >= d.needed else { return false }
        let n = zones.filter { $0.kind == d.kind }.count + 1
        let z = ZoneDraft(key: "new-\(nextKey)", kind: d.kind, name: "\(ZoneDraft.kindName(d.kind)) \(n)",
                          points: d.points.map(ZoneGeometry.round))
        nextKey += 1
        zones.append(z)
        selected = z.key
        drawing = nil
        lastClick = nil
        return true
    }

    /// A click on the picture while drawing: adds a point, closes the polygon
    /// on its first point, finishes a line at its second point, and finishes a
    /// polygon on a double-click (the double-click's first click placed the point).
    public mutating func click(at p: ZonePoint, time: TimeInterval, doubleClickInterval: TimeInterval = 0.4) {
        guard var d = drawing else { selected = nil; return }
        if let last = lastClick, time - last.time <= doubleClickInterval,
           hypot(p.x - last.point.x, p.y - last.point.y) <= Self.doubleClickDistance {
            lastClick = nil
            finishDrawing()
            return
        }
        lastClick = (p, time)
        if d.kind == .line {
            d.points.append(p)
            drawing = d
            if d.points.count >= ZoneLines.linePoints { finishDrawing() }
            return
        }
        if ZoneGeometry.closesPolygon(d.points, p) {
            finishDrawing()
            return
        }
        guard d.points.count < ZoneGeometry.maxPoints else { return }
        d.points.append(p)
        drawing = d
    }

    // MARK: Editing

    public func zone(_ key: String) -> ZoneDraft? { zones.first { $0.key == key } }

    public mutating func update(_ key: String, _ change: (inout ZoneDraft) -> Void) {
        guard let i = zones.firstIndex(where: { $0.key == key }) else { return }
        var z = zones[i]
        change(&z)
        if z.kind == .line || zones[i].kind == .line { z.kind = zones[i].kind } // a line stays a line, a polygon stays a polygon
        zones[i] = z
    }

    public mutating func moveVertex(_ key: String, index: Int, to p: ZonePoint) {
        update(key) { z in
            guard z.points.indices.contains(index) else { return }
            z.points[index] = ZoneGeometry.round(ZonePoint(ZoneGeometry.clamp01(p.x), ZoneGeometry.clamp01(p.y)))
        }
    }

    public mutating func delete(_ key: String) {
        zones.removeAll { $0.key == key }
        if selected == key { selected = nil }
    }

    /// The Delete key: while drawing it takes back the last point, else removes the selected shape.
    public mutating func deleteKey() {
        if drawing != nil { removeLastPoint(); return }
        if let key = selected { delete(key) }
    }

    // MARK: Pointer (canvas coordinates, origin top-left, canvas = picture)

    /// The shape under `point`: a vertex of the selected shape first, then the topmost shape.
    private func target(at point: CGPoint, in size: CGSize) -> Target {
        if let key = selected, let z = zone(key), let i = ZoneGeometry.vertexHit(point, z.points, in: size) {
            return .vertex(key: key, index: i)
        }
        for z in zones.reversed() where ZoneGeometry.shapeHit(point, kind: z.kind, z.points, in: size) {
            return .shape(key: z.key, start: z.points)
        }
        return .none
    }

    public mutating func pointerDown(at point: CGPoint, in size: CGSize) {
        guard drawing == nil else { pointer = Pointer(start: point, target: .none); return }
        let t = target(at: point, in: size)
        switch t {
        case .vertex(let key, _), .shape(let key, _): selected = key
        case .none: break
        }
        pointer = Pointer(start: point, target: t)
    }

    public mutating func pointerDragged(to point: CGPoint, in size: CGSize) {
        guard var ptr = pointer else { return }
        if ptr.moved == false, hypot(point.x - ptr.start.x, point.y - ptr.start.y) < Self.clickSlop { return }
        ptr.moved = true
        pointer = ptr
        switch ptr.target {
        case .none: break
        case .vertex(let key, let index):
            moveVertex(key, index: index, to: ZoneGeometry.normalize(point, in: size))
        case .shape(let key, let start):
            let dx = Double(point.x - ptr.start.x) / Double(max(size.width, 1))
            let dy = Double(point.y - ptr.start.y) / Double(max(size.height, 1))
            update(key) { $0.points = ZoneGeometry.translate(start, dx: dx, dy: dy) }
        }
    }

    public mutating func pointerUp(at point: CGPoint, in size: CGSize, time: TimeInterval, doubleClickInterval: TimeInterval = 0.4) {
        let ptr = pointer
        pointer = nil
        if let ptr, ptr.moved { return }
        if drawing != nil {
            click(at: ZoneGeometry.normalize(point, in: size), time: time, doubleClickInterval: doubleClickInterval)
            return
        }
        if case .none? = ptr?.target { selected = nil }
    }

    // MARK: Text

    public var hint: String {
        if let d = drawing {
            if d.kind == .line {
                return "Click the picture where the line starts, then where it ends (\(d.points.count)/\(ZoneLines.linePoints)). Esc cancels."
            }
            return "Click the picture to add points (\(d.points.count)/\(ZoneGeometry.maxPoints)). Double-click, or click the first point, to finish; Delete removes the last point; Esc cancels."
        }
        return "Add a zone, then click its corners on the picture. Drag a corner or a shape to move it; Delete removes the selected shape. With no detection zones the whole picture is watched. A line counts crossings from its left side to its right side as you look from its first point to its second."
    }
}
