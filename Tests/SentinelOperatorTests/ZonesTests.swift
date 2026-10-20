import CoreGraphics
import XCTest
@testable import SentinelCore

/// Wave 11 stream E: zone geometry (mirrors zones.test.ts / lines.test.ts),
/// the editor state machine, the zones API and the audio detection form.
final class ZonesTests: XCTestCase {
    private let site = URL(string: "http://nvr.test:8090")!

    override func setUp() {
        super.setUp()
        MockURLProtocol.reset()
    }

    override func tearDown() {
        MockURLProtocol.reset()
        super.tearDown()
    }

    private func makeAPI() -> SentinelAPI {
        let store = MemoryTokenStore()
        store.save(Fixtures.pair(), siteID: "s1")
        return SentinelAPI(baseURL: site, siteID: "s1", tokenStore: store, session: MockURLProtocol.session(), clientLabel: "Test")
    }

    private func pts(_ pairs: [[Double]]) -> [ZonePoint] { ZonePoint.list(pairs) }
    private let square: [[Double]] = [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]

    // MARK: Polygons (mirrors internal/store/zones.go)

    func testAcceptsSimpleShapesOfAnyWinding() {
        XCTAssertNil(ZoneGeometry.polygonProblem(pts(square)))
        XCTAssertNil(ZoneGeometry.polygonProblem(pts(square.reversed())))
        XCTAssertNil(ZoneGeometry.polygonProblem(pts([[0, 0], [1, 0], [0, 1]])))
        XCTAssertNil(ZoneGeometry.polygonProblem(pts([[0.1, 0.1], [0.5, 0.1], [0.5, 0.5], [0.9, 0.5], [0.9, 0.9], [0.1, 0.9]])), "concave L")
    }

    func testLimitsVertexCount() {
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [1, 1]])), "A zone needs 3 to 20 points.")
        func ring(_ n: Int) -> [ZonePoint] {
            (0..<n).map { i in ZonePoint(0.5 + 0.4 * cos(2 * .pi * Double(i) / Double(n)), 0.5 + 0.4 * sin(2 * .pi * Double(i) / Double(n))) }
        }
        XCTAssertNil(ZoneGeometry.polygonProblem(ring(20)))
        XCTAssertEqual(ZoneGeometry.polygonProblem(ring(21)), "A zone needs 3 to 20 points.")
    }

    func testRejectsOutsideAndRepeatedPoints() {
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [1.2, 0], [0, 1]])), "A point is outside the picture.")
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [.nan, 0], [0, 1]])), "A point is outside the picture.")
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [0.5], [0, 1]])), "A point is outside the picture.", "malformed pair")
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [0, 0], [1, 1], [0, 1]])), "Two neighbouring points are in the same place.")
    }

    func testRejectsBowTiesCrossingsTouchesAndFoldBacks() {
        let crosses = "The outline crosses itself."
        let folds = "The outline folds back on itself."
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0.1, 0.1], [0.9, 0.9], [0.9, 0.1], [0.1, 0.9]])), crosses)
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.3, 0.9], [0.3, 0]])), crosses)
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.5, 0.1], [0.1, 0.9]])), crosses)
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0.1, 0.1], [0.9, 0.1], [0.5, 0.1], [0.5, 0.9]])), folds)
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [0.5, 0.5], [1, 1]])), folds)
        XCTAssertTrue(ZoneGeometry.selfIntersects(pts([[0.1, 0.1], [0.9, 0.9], [0.9, 0.1], [0.1, 0.9]])))
        XCTAssertFalse(ZoneGeometry.selfIntersects(pts(square)))
    }

    func testRejectsSliversAndComputesArea() {
        XCTAssertEqual(ZoneGeometry.polygonProblem(pts([[0, 0], [1, 0], [1, 1e-7]])), "The zone has no area.")
        XCTAssertEqual(ZoneGeometry.polygonArea(pts(square)), 0.64, accuracy: 1e-9)
        XCTAssertEqual(ZoneGeometry.polygonArea(pts([[0, 0], [1, 0], [0, 1]])), 0.5, accuracy: 1e-9)
    }

    // MARK: Editor helpers

    func testNormalizesClampsAndRounds() {
        let size = CGSize(width: 400, height: 200)
        XCTAssertEqual(ZoneGeometry.normalize(CGPoint(x: 200, y: 100), in: size), ZonePoint(0.5, 0.5))
        XCTAssertEqual(ZoneGeometry.normalize(CGPoint(x: -100, y: 900), in: size), ZonePoint(0, 1))
        XCTAssertEqual(ZoneGeometry.position(ZonePoint(0.25, 0.5), in: size), CGPoint(x: 100, y: 100))
        XCTAssertEqual(ZoneGeometry.round(ZonePoint(0.123456, 0.987654)), ZonePoint(0.1235, 0.9877))
        XCTAssertEqual(ZoneGeometry.canvasSize(aspect: 16.0 / 9.0, in: CGSize(width: 1600, height: 1600)), CGSize(width: 1600, height: 900))
        XCTAssertEqual(ZoneGeometry.canvasSize(aspect: 2, in: CGSize(width: 1000, height: 300)), CGSize(width: 600, height: 300))
    }

    func testClosesPolygonOnlyNearFirstPointWithThreePlus() {
        XCTAssertFalse(ZoneGeometry.closesPolygon(Array(pts(square).prefix(2)), ZonePoint(0.1, 0.1)))
        XCTAssertTrue(ZoneGeometry.closesPolygon(pts(square), ZonePoint(0.11, 0.11)))
        XCTAssertFalse(ZoneGeometry.closesPolygon(pts(square), ZonePoint(0.5, 0.5)))
    }

    func testTranslateStopsAtTheFrameEdge() {
        let moved = ZoneGeometry.translate(pts([[0.1, 0.1], [0.3, 0.1], [0.3, 0.3]]), dx: -0.5, dy: 0.2)
        XCTAssertEqual(moved, pts([[0, 0.3], [0.2, 0.3], [0.2, 0.5]]))
        let pinned = ZoneGeometry.translate(pts([[0.8, 0.8], [0.9, 0.9]]), dx: 1, dy: 1)
        XCTAssertEqual(pinned, pts([[0.9, 0.9], [1, 1]]))
    }

    func testHitTesting() {
        let size = CGSize(width: 1000, height: 500)
        let poly = pts(square)
        XCTAssertTrue(ZoneGeometry.contains(poly, ZonePoint(0.5, 0.5)))
        XCTAssertFalse(ZoneGeometry.contains(poly, ZonePoint(0.95, 0.5)))
        // Concave L: the notch is outside.
        let l = pts([[0.1, 0.1], [0.5, 0.1], [0.5, 0.5], [0.9, 0.5], [0.9, 0.9], [0.1, 0.9]])
        XCTAssertFalse(ZoneGeometry.contains(l, ZonePoint(0.7, 0.3)))
        XCTAssertTrue(ZoneGeometry.contains(l, ZonePoint(0.3, 0.3)))
        XCTAssertTrue(ZoneGeometry.shapeHit(CGPoint(x: 500, y: 250), kind: .detect, poly, in: size))
        XCTAssertTrue(ZoneGeometry.shapeHit(CGPoint(x: 904, y: 250), kind: .mask, poly, in: size), "on the outline")
        XCTAssertFalse(ZoneGeometry.shapeHit(CGPoint(x: 950, y: 250), kind: .mask, poly, in: size))
        let line = pts([[0.1, 0.5], [0.9, 0.5]])
        XCTAssertTrue(ZoneGeometry.shapeHit(CGPoint(x: 500, y: 254), kind: .line, line, in: size))
        XCTAssertFalse(ZoneGeometry.shapeHit(CGPoint(x: 500, y: 270), kind: .line, line, in: size))
        XCTAssertEqual(ZoneGeometry.vertexHit(CGPoint(x: 903, y: 448), poly, in: size), 2)
        XCTAssertNil(ZoneGeometry.vertexHit(CGPoint(x: 500, y: 250), poly, in: size))
        XCTAssertEqual(ZoneGeometry.distance(from: CGPoint(x: 0, y: 5), toSegment: CGPoint(x: 10, y: 0), CGPoint(x: 20, y: 0)), hypot(10, 5), accuracy: 1e-9)
    }

    // MARK: Lines (mirrors internal/store/zones_lines.go)

    func testLineProblem() {
        XCTAssertNil(ZoneLines.lineProblem(pts([[0.1, 0.1], [0.9, 0.8]])))
        XCTAssertNil(ZoneLines.lineProblem(pts([[0, 0], [1, 1]])))
        XCTAssertEqual(ZoneLines.lineProblem([]), "A line needs exactly 2 points.")
        XCTAssertEqual(ZoneLines.lineProblem(pts([[0.5, 0.5]])), "A line needs exactly 2 points.")
        XCTAssertEqual(ZoneLines.lineProblem(pts([[0, 0], [0.5, 0.5], [1, 1]])), "A line needs exactly 2 points.")
        XCTAssertEqual(ZoneLines.lineProblem(pts([[0, 0], [1.2, 1]])), "A point is outside the picture.")
        XCTAssertEqual(ZoneLines.lineProblem(pts([[0, 0], [.nan, 1]])), "A point is outside the picture.")
        XCTAssertEqual(ZoneLines.lineProblem(pts([[0.5, 0.5], [0.5, 0.5]])), "The two points of a line are in the same place.")
        XCTAssertEqual(ZoneLines.lineProblem(pts([[0.5, 0.5], [0.5, 0.505]])), "The line is too short to cross.")
    }

    func testDirectionAndSeverityDefaultsFromMeta() throws {
        func meta(_ json: String) throws -> V2.ZoneLineMeta? {
            let zone = try JSONDecoder().decode(V2.CameraZone.self, from: Data(#"{"id":"z","kind":"line","name":"L","points":[[0,0],[1,1]],"enabled":true,"createdAt":1,"updatedAt":2\#(json)}"#.utf8))
            return zone.meta
        }
        XCTAssertEqual(ZoneLines.direction(of: try meta(#","meta":{"direction":"ltr"}"#)), .ltr)
        XCTAssertEqual(ZoneLines.direction(of: try meta(#","meta":{"direction":"rtl"}"#)), .rtl)
        XCTAssertEqual(ZoneLines.direction(of: try meta(#","meta":{"direction":"up"}"#)), .both)
        XCTAssertEqual(ZoneLines.direction(of: try meta(#","meta":{}"#)), .both)
        XCTAssertEqual(ZoneLines.direction(of: try meta("")), .both)
        XCTAssertEqual(ZoneLines.direction(of: try meta(#","meta":null"#)), .both)
        XCTAssertEqual(ZoneLines.direction(of: try meta(#","meta":"junk""#)), .both, "damaged meta reads as defaults")
        XCTAssertEqual(ZoneLines.severity(of: try meta(#","meta":{"severity":"Warning"}"#)), .warning)
        XCTAssertEqual(ZoneLines.severity(of: try meta(#","meta":{"direction":"ltr","severity":"Info"}"#)), .info)
        XCTAssertEqual(ZoneLines.severity(of: try meta(#","meta":{"severity":"info"}"#)), .critical)
        XCTAssertEqual(ZoneLines.severity(of: try meta(#","meta":{"direction":"ltr"}"#)), .critical)
        XCTAssertEqual(ZoneLines.severity(of: nil), .critical)
    }

    func testSideOfPutsTheRightHandSideBelowALeftToRightLine() {
        let p0 = ZonePoint(0.2, 0.5), p1 = ZonePoint(0.8, 0.5)
        XCTAssertGreaterThan(ZoneLines.sideOf(p0, p1, ZonePoint(0.5, 0.7)), 0)
        XCTAssertLessThan(ZoneLines.sideOf(p0, p1, ZonePoint(0.5, 0.3)), 0)
        XCTAssertLessThan(ZoneLines.sideOf(p1, p0, ZonePoint(0.5, 0.7)), 0, "reversing the line swaps sides")
    }

    func testDirectionArrows() throws {
        let p0 = ZonePoint(0.2, 0.5), p1 = ZonePoint(0.8, 0.5)
        let ltr = try XCTUnwrap(ZoneLines.directionArrows(p0, p1, .ltr).first)
        XCTAssertGreaterThan(ltr.to.y, ltr.from.y)
        XCTAssertEqual(ltr.to.x, ltr.from.x, accuracy: 1e-6)
        XCTAssertEqual(ltr.from, ZonePoint(0.5, 0.5))
        let rtl = try XCTUnwrap(ZoneLines.directionArrows(p0, p1, .rtl).first)
        XCTAssertLessThan(rtl.to.y, rtl.from.y)
        XCTAssertEqual(ZoneLines.directionArrows(p0, p1, .both).count, 2)
        for (a, b) in [(ZonePoint(0.2, 0.2), ZonePoint(0.8, 0.9)), (ZonePoint(0.9, 0.1), ZonePoint(0.1, 0.6)), (ZonePoint(0.5, 0.9), ZonePoint(0.5, 0.1))] {
            let arrow = try XCTUnwrap(ZoneLines.directionArrows(a, b, .ltr).first)
            XCTAssertGreaterThan(ZoneLines.sideOf(a, b, arrow.to), 0, "arrow on the side the server counts as the destination")
        }
        let aspect = 2.0
        let wide = try XCTUnwrap(ZoneLines.directionArrows(ZonePoint(0.2, 0.2), ZonePoint(0.8, 0.8), .ltr, length: 0.1, aspect: aspect).first)
        let along = ((0.8 - 0.2) * aspect, 0.8 - 0.2)
        let out = ((wide.to.x - wide.from.x) * aspect, wide.to.y - wide.from.y)
        XCTAssertEqual(along.0 * out.0 + along.1 * out.1, 0, accuracy: 1e-9, "perpendicular on screen")
        XCTAssertEqual(ZoneLines.directionArrows(ZonePoint(0.5, 0.5), ZonePoint(0.5, 0.5), .both), [])
        XCTAssertEqual(ZoneLines.arrowHead(ZonePoint(0.5, 0.5), ZonePoint(0.5, 0.5)), [])
        XCTAssertEqual(ZoneLines.arrowHead(ZonePoint(0.5, 0.5), ZonePoint(0.5, 0.6)).count, 3)
    }

    // MARK: Editor model

    private func zone(_ id: String, _ kind: V2.ZoneKind, _ points: [[Double]], meta: V2.ZoneLineMeta? = nil) -> V2.CameraZone {
        V2.CameraZone(id: id, kind: kind, name: id, points: points, meta: meta)
    }

    func testDrawingAPolygonByClosingOnTheFirstPoint() {
        var m = ZoneEditorModel()
        XCTAssertTrue(m.startDrawing(.detect))
        XCTAssertFalse(m.startDrawing(.mask), "one shape at a time")
        m.click(at: ZonePoint(0.1, 0.1), time: 0)
        m.click(at: ZonePoint(0.9, 0.1), time: 1)
        m.click(at: ZonePoint(0.111111, 0.1), time: 2) // near the first point but only 2 points: adds
        XCTAssertEqual(m.drawing?.points.count, 3)
        m.removeLastPoint()
        m.click(at: ZonePoint(0.9, 0.9), time: 3)
        m.click(at: ZonePoint(0.11, 0.11), time: 4) // closes
        XCTAssertNil(m.drawing)
        XCTAssertEqual(m.zones.count, 1)
        XCTAssertEqual(m.zones[0].name, "Detection zone 1")
        XCTAssertEqual(m.selected, m.zones[0].key)
        XCTAssertTrue(m.isDirty)
        XCTAssertTrue(m.canSave)
    }

    func testDoubleClickFinishesAPolygon() {
        var m = ZoneEditorModel()
        m.startDrawing(.mask)
        m.click(at: ZonePoint(0.2, 0.2), time: 0)
        m.click(at: ZonePoint(0.2, 0.2), time: 0.1) // quick double-click with too few points: ignored
        XCTAssertEqual(m.drawing?.points.count, 1)
        m.click(at: ZonePoint(0.8, 0.2), time: 1)
        m.click(at: ZonePoint(0.5, 0.8), time: 2)
        m.click(at: ZonePoint(0.5, 0.801), time: 2.2)
        XCTAssertNil(m.drawing)
        XCTAssertEqual(m.zones.first?.points.count, 3, "the double-click doesn't add a duplicate point")
        XCTAssertEqual(m.zones.first?.kind, .mask)
        XCTAssertEqual(m.zones.first?.name, "Privacy mask 1")
    }

    func testDrawingALineFinishesAtTheSecondPoint() {
        var m = ZoneEditorModel()
        m.startDrawing(.line)
        m.click(at: ZonePoint(0.123456, 0.5), time: 0)
        XCTAssertNotNil(m.drawing)
        m.click(at: ZonePoint(0.9, 0.5), time: 1)
        XCTAssertNil(m.drawing)
        let line = m.zones[0]
        XCTAssertEqual(line.points, [ZonePoint(0.1235, 0.5), ZonePoint(0.9, 0.5)])
        XCTAssertEqual(line.direction, .both)
        XCTAssertEqual(line.severity, .critical)
        XCTAssertEqual(line.input.meta, V2.ZoneLineMeta(direction: .both, severity: .critical))
    }

    func testDirtyTrackingProblemsAndDiscard() {
        var m = ZoneEditorModel(zones: [zone("z1", .detect, square), zone("l1", .line, [[0.1, 0.5], [0.9, 0.5]], meta: .init(direction: .ltr))])
        XCTAssertFalse(m.isDirty)
        XCTAssertFalse(m.canSave)
        XCTAssertEqual(m.zones[1].direction, .ltr)
        m.update("z1") { $0.direction = .rtl }
        XCTAssertFalse(m.isDirty, "direction is ignored on polygons")
        m.update("z1") { $0.kind = .line }
        XCTAssertEqual(m.zone("z1")?.kind, .detect, "a polygon can't become a line")
        m.update("z1") { $0.kind = .mask }
        XCTAssertTrue(m.isDirty)
        m.update("z1") { $0.kind = .detect }
        XCTAssertFalse(m.isDirty)
        m.moveVertex("z1", index: 2, to: ZonePoint(0.1, 0.1)) // folds onto vertex 0 diagonal: crosses
        XCTAssertNotNil(m.problems["z1"])
        XCTAssertFalse(m.canSave)
        m.discard()
        XCTAssertFalse(m.isDirty)
        XCTAssertTrue(m.problems.isEmpty)
        m.update("l1") { $0.name = String(repeating: "x", count: 101) }
        XCTAssertEqual(m.problems["l1"], "The name is longer than 100 characters.")
    }

    func testDeleteKeyRemovesSelectionOrLastPoint() {
        var m = ZoneEditorModel(zones: [zone("z1", .detect, square)])
        m.selected = "z1"
        m.deleteKey()
        XCTAssertTrue(m.zones.isEmpty)
        XCTAssertNil(m.selected)
        m.startDrawing(.detect)
        m.click(at: ZonePoint(0.3, 0.3), time: 0)
        m.deleteKey()
        XCTAssertEqual(m.drawing?.points.count, 0)
        m.cancelDrawing()
        XCTAssertNil(m.drawing)
    }

    func testFullSetBlocksNewShapes() {
        let many = (0..<ZoneGeometry.maxZones).map { zone("z\($0)", .detect, square) }
        var m = ZoneEditorModel(zones: many)
        XCTAssertTrue(m.isFull)
        XCTAssertFalse(m.startDrawing(.detect))
    }

    func testPointerSelectsDragsVerticesAndShapes() {
        let size = CGSize(width: 1000, height: 500)
        var m = ZoneEditorModel(zones: [zone("z1", .detect, [[0.1, 0.1], [0.3, 0.1], [0.3, 0.3], [0.1, 0.3]])])
        // Click inside: selects.
        m.pointerDown(at: CGPoint(x: 200, y: 100), in: size)
        m.pointerUp(at: CGPoint(x: 200, y: 100), in: size, time: 0)
        XCTAssertEqual(m.selected, "z1")
        // Drag a corner of the selected shape.
        m.pointerDown(at: CGPoint(x: 300, y: 150), in: size)
        m.pointerDragged(to: CGPoint(x: 400, y: 200), in: size)
        m.pointerUp(at: CGPoint(x: 400, y: 200), in: size, time: 1)
        XCTAssertEqual(m.zone("z1")?.points[2], ZonePoint(0.4, 0.4))
        // Drag the whole shape.
        m.pointerDown(at: CGPoint(x: 150, y: 100), in: size)
        m.pointerDragged(to: CGPoint(x: 250, y: 150), in: size)
        m.pointerUp(at: CGPoint(x: 250, y: 150), in: size, time: 2)
        XCTAssertEqual(m.zone("z1")?.points[0], ZonePoint(0.2, 0.2))
        XCTAssertEqual(m.selected, "z1")
        // A tiny jitter is still a click; a click on empty picture deselects.
        m.pointerDown(at: CGPoint(x: 900, y: 450), in: size)
        m.pointerDragged(to: CGPoint(x: 901, y: 451), in: size)
        m.pointerUp(at: CGPoint(x: 901, y: 451), in: size, time: 3)
        XCTAssertNil(m.selected)
    }

    func testPointerClicksPlacePointsWhileDrawing() {
        let size = CGSize(width: 1000, height: 500)
        var m = ZoneEditorModel()
        m.startDrawing(.line)
        for (i, x) in [100.0, 800.0].enumerated() {
            m.pointerDown(at: CGPoint(x: x, y: 250), in: size)
            m.pointerUp(at: CGPoint(x: x, y: 250), in: size, time: Double(i))
        }
        XCTAssertNil(m.drawing)
        XCTAssertEqual(m.zones.first?.points, [ZonePoint(0.1, 0.5), ZonePoint(0.8, 0.5)])
    }

    func testPutInputCarriesIDsMetaOnlyOnLinesAndTrimmedNames() {
        var m = ZoneEditorModel(zones: [zone("z1", .mask, square)])
        m.update("z1") { $0.name = "  Window  " }
        m.startDrawing(.line)
        m.click(at: ZonePoint(0.1, 0.5), time: 0)
        m.click(at: ZonePoint(0.9, 0.5), time: 1)
        let key = m.selected!
        m.update(key) { $0.direction = .rtl; $0.severity = .info; $0.enabled = false }
        let input = m.putInput
        XCTAssertEqual(input.count, 2)
        XCTAssertEqual(input[0].id, "z1")
        XCTAssertEqual(input[0].name, "Window")
        XCTAssertNil(input[0].meta)
        XCTAssertNil(input[1].id)
        XCTAssertEqual(input[1].meta, V2.ZoneLineMeta(direction: .rtl, severity: .info))
        XCTAssertFalse(input[1].enabled)
    }

    // MARK: API

    func testZonesAPIGetAndPutFullSet() async throws {
        MockURLProtocol.handler = { req, body in
            switch (req.httpMethod ?? "", req.url?.path ?? "") {
            case ("GET", "/api/v2/cameras/c 1/zones"):
                return .json(200, #"{"items":[{"id":"z1","kind":"mask","name":"Window","points":[[0.1,0.1],[0.9,0.1],[0.9,0.9]],"enabled":true,"createdAt":1,"updatedAt":2},{"id":"l1","kind":"line","name":"Gate","points":[[0.1,0.5],[0.9,0.5]],"meta":{"direction":"ltr","severity":"Warning"},"enabled":false,"createdAt":3,"updatedAt":4}]}"#)
            case ("PUT", "/api/v2/cameras/c 1/zones"):
                return .json(200, #"{"items":[{"id":"z9","kind":"detect","name":"Drive","points":[[0,0],[1,0],[1,1]],"enabled":true,"createdAt":5,"updatedAt":5}]}"#)
            default: return .json(404, "{}")
            }
        }
        let api = makeAPI()
        let zones = try await api.zones(cameraID: "c 1")
        XCTAssertEqual(zones.map(\.id), ["z1", "l1"])
        XCTAssertNil(zones[0].meta)
        XCTAssertEqual(zones[1].meta?.direction, .ltr)
        XCTAssertEqual(zones[1].meta?.severity, .warning)
        XCTAssertFalse(zones[1].enabled)

        let out = try await api.putZones(cameraID: "c 1", [
            V2.CameraZoneInput(kind: .detect, name: "Drive", points: [[0, 0], [1, 0], [1, 1]]),
            V2.CameraZoneInput(id: "l1", kind: .line, name: "Gate", points: [[0.1, 0.5], [0.9, 0.5]],
                               meta: .init(direction: .both, severity: .info), enabled: false),
        ])
        XCTAssertEqual(out.map(\.id), ["z9"])
        let put = try XCTUnwrap(MockURLProtocol.requests.last)
        XCTAssertEqual(put.request.httpMethod, "PUT")
        XCTAssertEqual(put.request.url?.absoluteString, "http://nvr.test:8090/api/v2/cameras/c%201/zones")
        let json = try XCTUnwrap(put.body.flatMap { try JSONSerialization.jsonObject(with: $0) } as? [String: Any])
        let sent = try XCTUnwrap(json["zones"] as? [[String: Any]])
        XCTAssertEqual(sent.count, 2)
        XCTAssertNil(sent[0]["id"], "new zones omit the id")
        XCTAssertNil(sent[0]["meta"], "polygons carry no meta")
        XCTAssertEqual(sent[0]["enabled"] as? Bool, true)
        XCTAssertEqual(sent[1]["id"] as? String, "l1")
        XCTAssertEqual((sent[1]["meta"] as? [String: Any])?["severity"] as? String, "Info")
        XCTAssertEqual((sent[1]["meta"] as? [String: Any])?["direction"] as? String, "both")
    }

    func testZonesAPISurfacesTheServersRejection() async {
        MockURLProtocol.handler = { _, _ in
            .json(400, #"{"error":{"code":"bad_request","message":"zone 1: the outline crosses itself"}}"#)
        }
        do {
            _ = try await makeAPI().putZones(cameraID: "c1", [])
            XCTFail("expected 400")
        } catch let SentinelAPIError.server(status, _, message) {
            XCTAssertEqual(status, 400)
            XCTAssertEqual(message, "zone 1: the outline crosses itself")
        } catch {
            XCTFail("unexpected \(error)")
        }
    }

    // MARK: Audio detection (mirrors audioSettings.test.ts)

    func testAudioThresholdClamps() {
        XCTAssertEqual(AudioDetectionSettings.clamp(25), 25)
        XCTAssertEqual(AudioDetectionSettings.clamp(2), 6)
        XCTAssertEqual(AudioDetectionSettings.clamp(99), 60)
        XCTAssertEqual(AudioDetectionSettings.clamp(30.6), 31)
        XCTAssertEqual(AudioDetectionSettings.clamp(.nan), AudioDetectionSettings.defaultDb)
        XCTAssertEqual(AudioDetectionSettings.range, 6...60)
    }

    func testAudioFormDefaultsAndPatchesOnlyWhatChanged() throws {
        XCTAssertEqual(AudioDetectionSettings.form(of: nil), .init(audioDetect: false, audioThresholdDb: 25))
        var cam = try JSONDecoder().decode(V2.Camera.self, from: Data(Fixtures.camera.utf8))
        XCTAssertEqual(AudioDetectionSettings.form(of: cam), .init(audioDetect: false, audioThresholdDb: 25), "older servers omit the fields")
        XCTAssertEqual(AudioDetectionSettings.patch(cam, .init(audioDetect: false, audioThresholdDb: 25)), V2.CameraPatch())
        cam.audioDetect = false
        cam.audioThresholdDb = 25
        XCTAssertEqual(AudioDetectionSettings.patch(cam, .init(audioDetect: true, audioThresholdDb: 25)).audioDetect, true)
        XCTAssertNil(AudioDetectionSettings.patch(cam, .init(audioDetect: true, audioThresholdDb: 25)).audioThresholdDb)
        let both = AudioDetectionSettings.patch(cam, .init(audioDetect: true, audioThresholdDb: 40))
        XCTAssertEqual(both.audioDetect, true)
        XCTAssertEqual(both.audioThresholdDb, 40)
        var big = V2.CameraPatch()
        big.name = "Front"
        AudioDetectionSettings.merge(both, into: &big)
        XCTAssertEqual(big.name, "Front")
        XCTAssertEqual(big.audioThresholdDb, 40)
        let encoded = try XCTUnwrap(String(data: SentinelAPI.jsonEncoder.encode(AudioDetectionSettings.patch(cam, .init(audioDetect: false, audioThresholdDb: 31))), encoding: .utf8))
        XCTAssertEqual(encoded, #"{"audioThresholdDb":31}"#)
    }
}
