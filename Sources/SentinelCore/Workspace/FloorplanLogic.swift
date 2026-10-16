import CoreGraphics
import Foundation

// View-free floorplan logic: where the image sits in its container, pin
// normalization and hit-testing, pin state, and the ETag-validated image
// cache. Mirrors `linux/web/src/shared/floorplan.ts`.

public enum FloorplanGeometry {
    /// The aspect-fit rectangle an image of `imageSize` occupies in `container`.
    public static func fittedRect(imageSize: CGSize, in container: CGSize) -> CGRect {
        guard imageSize.width > 0, imageSize.height > 0, container.width > 0, container.height > 0 else { return .zero }
        let scale = min(container.width / imageSize.width, container.height / imageSize.height)
        let size = CGSize(width: imageSize.width * scale, height: imageSize.height * scale)
        return CGRect(x: (container.width - size.width) / 2, y: (container.height - size.height) / 2,
                      width: size.width, height: size.height)
    }

    /// A point as a 0...1 fraction of the image rect (clamped); `inside` is false when it fell outside it.
    public static func normalize(_ point: CGPoint, in rect: CGRect) -> (x: Double, y: Double, inside: Bool) {
        let rx = rect.width > 0 ? (point.x - rect.minX) / rect.width : 0
        let ry = rect.height > 0 ? (point.y - rect.minY) / rect.height : 0
        func clamp(_ v: Double) -> Double { min(1, max(0, v)) }
        return (clamp(rx), clamp(ry), rx >= 0 && rx <= 1 && ry >= 0 && ry <= 1)
    }

    /// Where a pin sits in the container's coordinates.
    public static func position(of pin: V2.FloorplanPin, in rect: CGRect) -> CGPoint {
        CGPoint(x: rect.minX + CGFloat(min(1, max(0, pin.x))) * rect.width,
                y: rect.minY + CGFloat(min(1, max(0, pin.y))) * rect.height)
    }

    /// The pin nearest `point` within `radius`, if any (the later pin wins a tie: it is drawn on top).
    public static func hitTest(_ point: CGPoint, pins: [V2.FloorplanPin], in rect: CGRect, radius: CGFloat = 14) -> V2.FloorplanPin? {
        var best: (pin: V2.FloorplanPin, distance: CGFloat)?
        for pin in pins {
            let p = position(of: pin, in: rect)
            let d = hypot(p.x - point.x, p.y - point.y)
            guard d <= radius else { continue }
            if best == nil || d <= best!.distance { best = (pin, d) }
        }
        return best?.pin
    }
}

public enum PinState: String, Sendable {
    case normal, offline, alarmed
}

public enum FloorplanPins {
    /// States that make a pin pulse (a snoozed alarm is parked on purpose).
    public static func pulses(_ state: AlertState) -> Bool {
        state == .new || state == .acknowledged || state == .investigating
    }

    /// The newest pulsing alarm on a camera. `alarms` is any order; newest wins.
    public static func alarm(on cameraID: String, in alarms: [V2.Alarm]) -> V2.Alarm? {
        alarms.filter { $0.cameraId == cameraID && pulses($0.state) }.max { $0.createdAt < $1.createdAt }
    }

    /// An open alarm beats offline beats normal. An unknown camera counts as offline.
    public static func state(camera: V2.Camera?, cameraID: String, alarms: [V2.Alarm]) -> PinState {
        if alarm(on: cameraID, in: alarms) != nil { return .alarmed }
        guard let camera, camera.status.online else { return .offline }
        return .normal
    }
}

/// One cached plan image and the validator it came with.
public struct FloorplanImageEntry: Sendable, Equatable {
    public var data: Data
    public var etag: String?
}

/// Plan images kept in memory with their ETag: each open revalidates with
/// `If-None-Match` and only re-downloads when the server says it changed.
public actor FloorplanImageCache {
    public typealias Fetch = @Sendable (_ planID: String, _ etag: String?) async throws -> FloorplanImageResponse

    private let fetch: Fetch
    private var entries: [String: FloorplanImageEntry] = [:]

    public init(fetch: @escaping Fetch) { self.fetch = fetch }

    public init(api: SentinelAPI, session: URLSession = .shared) {
        self.init(fetch: { try await api.floorplanImage(id: $0, ifNoneMatch: $1, session: session) })
    }

    /// The image bytes, revalidated. On a transport failure a cached copy is still returned.
    public func image(for planID: String) async throws -> Data {
        let cached = entries[planID]
        do {
            let response = try await fetch(planID, cached?.etag)
            if let data = response.data {
                entries[planID] = FloorplanImageEntry(data: data, etag: response.etag)
                return data
            }
            if let cached { return cached.data }
            throw SentinelAPIError.decoding(message: "empty floorplan image")
        } catch let error as SentinelAPIError {
            if case .transport = error, let cached { return cached.data }
            if error.isNotFound { entries[planID] = nil }
            throw error
        }
    }

    public func cachedEntry(_ planID: String) -> FloorplanImageEntry? { entries[planID] }
}
