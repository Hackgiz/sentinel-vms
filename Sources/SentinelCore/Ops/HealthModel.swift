import Combine
import Foundation

// System Monitor model (port of web `healthModel.ts`): what the server cards
// and the camera table show, with meter levels and formatting. Pure.

public enum HealthLevel: Equatable, Sendable {
    case ok, warn, crit, off
}

public enum HealthThresholds {
    /// Amber from 80 percent, red from 95.
    public static let warnAt = 80.0
    public static let critAt = 95.0
    /// A recording camera whose newest file is older than this is stale.
    public static let staleWarnSec: Int64 = 120
    public static let staleCritSec: Int64 = 300
    /// Auto-refresh period while the pane is visible.
    public static let refreshSeconds: UInt64 = 10
}

public struct HealthMeter: Equatable, Sendable {
    /// 0…100, nil when the server doesn't know it.
    public var pct: Double?
    public var level: HealthLevel
    /// "63%", or "n/a".
    public var label: String
    /// Hover detail, e.g. "412 GB of 1.8 TB".
    public var detail: String
}

public struct ServerCard: Equatable, Identifiable, Sendable {
    public var id: String
    public var name: String
    public var online: Bool
    /// The dot: ok when online, warn when a meter is high, crit when a meter is
    /// critical or the server is offline.
    public var state: HealthLevel
    public var version: String
    public var uptime: String
    public var latency: String
    public var cpu: HealthMeter
    public var mem: HealthMeter
    public var disk: HealthMeter
    public var db: String
    public var cameras: Int
    public var camerasOnline: Int
}

public struct CameraHealthRow: Equatable, Identifiable, Sendable {
    public var id: String
    public var server: String
    public var serverID: String
    public var name: String
    public var online: Bool
    public var recording: Bool
    public var state: HealthLevel
    public var lastSegment: String
    public var lastSegmentLevel: HealthLevel
    public var kbps: String
    /// For sorting by numeric rate.
    public var kbpsValue: Double
    public var lastSegmentSeconds: Int64
}

public enum HealthModel {
    public static func level(of pct: Double?) -> HealthLevel {
        guard let pct, pct.isFinite else { return .off }
        if pct >= HealthThresholds.critAt { return .crit }
        if pct >= HealthThresholds.warnAt { return .warn }
        return .ok
    }

    static func percent(used: Int64, total: Int64) -> Double? {
        total > 0 ? min(100, max(0, Double(used) / Double(total) * 100)) : nil
    }

    static func meter(_ pct: Double?, detail: String = "") -> HealthMeter {
        HealthMeter(pct: pct, level: level(of: pct), label: pct.map { "\(Int($0.rounded()))%" } ?? "n/a", detail: detail)
    }

    public static func card(_ s: V2.ServerHealth) -> ServerCard {
        let cpu = meter(s.online ? s.cpu : nil)
        let mem = meter(s.mem.flatMap { percent(used: $0.used, total: $0.total) },
                        detail: s.mem.map { "\(bytes($0.used)) of \(bytes($0.total))" } ?? "")
        let disk = meter(s.disk.flatMap { percent(used: $0.used, total: $0.total) },
                         detail: s.disk.map { "\(bytes($0.used)) of \(bytes($0.total)) (\(bytes($0.free)) free)" } ?? "")
        let meters = [cpu, mem, disk]
        let worst: HealthLevel = meters.contains { $0.level == .crit } ? .crit : meters.contains { $0.level == .warn } ? .warn : .ok
        return ServerCard(
            id: s.id, name: s.name, online: s.online,
            state: s.online ? worst : .crit,
            version: s.version,
            uptime: s.online ? s.uptimeSec.map(uptime) ?? "" : "",
            latency: s.latencyMs.map { "\($0) ms" } ?? "",
            cpu: cpu, mem: mem, disk: disk,
            db: s.dbBytes.map(bytes) ?? "",
            cameras: s.cameras.count,
            camerasOnline: s.cameras.filter(\.online).count)
    }

    public static func segmentLevel(online: Bool, recording: Bool, ageSec: Int64?) -> HealthLevel {
        guard let age = ageSec else { return recording ? .warn : .off }
        if recording == false { return .off }
        if age >= HealthThresholds.staleCritSec { return .crit }
        if age >= HealthThresholds.staleWarnSec { return .warn }
        return .ok
    }

    /// Camera rows of every server, sorted by server then camera name.
    public static func rows(_ servers: [V2.ServerHealth]) -> [CameraHealthRow] {
        var out: [CameraHealthRow] = []
        for s in servers {
            for c in s.cameras {
                let seg = segmentLevel(online: c.online, recording: c.recording, ageSec: c.lastSegmentAgeSec)
                out.append(CameraHealthRow(
                    id: "\(s.id)/\(c.id)", server: s.name, serverID: s.id, name: c.name,
                    online: c.online, recording: c.recording,
                    state: c.online == false ? .crit : (seg == .crit || seg == .warn ? seg : .ok),
                    lastSegment: c.lastSegmentAgeSec.map(age) ?? "none",
                    lastSegmentLevel: seg,
                    kbps: c.kbps.map(kbps) ?? "",
                    kbpsValue: c.kbps ?? -1,
                    lastSegmentSeconds: c.lastSegmentAgeSec ?? Int64.max))
            }
        }
        return out.sorted {
            let s = $0.server.localizedCaseInsensitiveCompare($1.server)
            return s == .orderedSame ? $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending : s == .orderedAscending
        }
    }

    // MARK: Formatting

    /// Decimal units, like the web monitor ("1.8 TB").
    public static func bytes(_ n: Int64) -> String {
        guard n >= 0 else { return "" }
        let units = ["B", "KB", "MB", "GB", "TB", "PB"]
        var v = Double(n)
        var i = 0
        while v >= 1000, i < units.count - 1 { v /= 1000; i += 1 }
        return v >= 100 || i == 0 ? "\(Int(v.rounded())) \(units[i])" : String(format: "%.1f %@", v, units[i])
    }

    public static func uptime(_ sec: Int64) -> String {
        let s = max(0, sec)
        let d = s / 86_400, h = (s % 86_400) / 3600, m = (s % 3600) / 60
        if d > 0 { return "\(d)d \(h)h" }
        if h > 0 { return "\(h)h \(m)m" }
        return "\(m)m"
    }

    /// "12 s ago", "3 min ago", "2 h ago".
    public static func age(_ sec: Int64) -> String {
        let s = max(0, sec)
        if s < 90 { return "\(s) s ago" }
        if s < 5400 { return "\(Int((Double(s) / 60).rounded())) min ago" }
        return "\(Int((Double(s) / 3600).rounded())) h ago"
    }

    public static func kbps(_ k: Double) -> String {
        k >= 1000 ? String(format: "%.1f Mbps", k / 1000) : "\(Int(k.rounded())) kbps"
    }
}

// MARK: - Tile activity

/// Which glyph a detection kind gets next to a tile's REC badge.
public enum ActivityKind: String, Equatable, Sendable, CaseIterable {
    case person, vehicle, animal, motion, other

    public init(eventKind kind: String) {
        let k = kind.lowercased()
        func has(_ words: String...) -> Bool { words.contains { k.contains($0) } }
        if has("person", "people", "human", "face") { self = .person }
        else if has("vehicle", "car", "truck", "plate") { self = .vehicle }
        else if has("animal", "dog", "cat", "pet") { self = .animal }
        else if has("motion") { self = .motion }
        else { self = .other }
    }

    public var symbol: String {
        switch self {
        case .person: return "figure.walk"
        case .vehicle: return "car.fill"
        case .animal: return "pawprint.fill"
        case .motion: return "dot.radiowaves.left.and.right"
        case .other: return "bolt.fill"
        }
    }

    public var label: String {
        switch self {
        case .person: return "Person"
        case .vehicle: return "Vehicle"
        case .animal: return "Animal"
        case .motion: return "Motion"
        case .other: return "Activity"
        }
    }
}

/// Remembers the latest detection per camera so a tile can show its glyph
/// for ~5 s. Fed from the existing event stream; no polling.
@MainActor
public final class ActivityTracker: ObservableObject {
    public struct Entry: Equatable, Sendable {
        public var kind: ActivityKind
        public var at: Date
    }

    /// How long a glyph stays visible.
    public static let fadeSeconds: TimeInterval = 5

    @Published public private(set) var latest: [String: Entry] = [:]

    public init() {}

    public func record(cameraID: String, kind: String, at: Date = Date()) {
        latest[cameraID] = Entry(kind: ActivityKind(eventKind: kind), at: at)
    }

    /// The glyph to show now, if the last detection is fresh.
    public func active(_ cameraID: String, now: Date = Date()) -> Entry? {
        guard let e = latest[cameraID], now.timeIntervalSince(e.at) < Self.fadeSeconds else { return nil }
        return e
    }

    /// 1 → 0 over the fade window (0 when stale or unknown).
    public func opacity(_ cameraID: String, now: Date = Date()) -> Double {
        guard let e = latest[cameraID] else { return 0 }
        let left = Self.fadeSeconds - now.timeIntervalSince(e.at)
        return min(1, max(0, left / Self.fadeSeconds))
    }

    public func forget(_ cameraID: String) { latest[cameraID] = nil }
}
