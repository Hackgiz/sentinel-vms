import Foundation

// The arithmetic behind the Reports workspace (parity 3.4.2), kept pure so it
// can be tested without a server: turns the activity response into
// per-camera rows, alarm totals per severity and a time series for the bars.
// Mirrors `linux/web/src/operator/reportModel.ts`.

/// The ranges the Reports workspace offers. Raw values match the web's ids.
public enum ReportRange: String, CaseIterable, Identifiable, Sendable {
    case day = "24h"
    case week = "7d"
    case month = "31d"

    public static let `default` = ReportRange.day

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .day: return "Last 24 hours"
        case .week: return "Last 7 days"
        case .month: return "Last 31 days"
        }
    }

    public var milliseconds: UnixMillis {
        let day: UnixMillis = 24 * 3600 * 1000
        switch self {
        case .day: return day
        case .week: return 7 * day
        case .month: return 31 * day
        }
    }

    /// Hours for a day, days for longer ranges.
    public var bucket: ReportModel.Bucket { self == .day ? .hour : .day }

    /// `from` and `to` (Unix ms) for the range ending at `now`.
    public func bounds(now: Date) -> (from: UnixMillis, to: UnixMillis) {
        let to = now.unixMillis
        return (to - milliseconds, to)
    }
}

public enum ReportModel {
    public enum Bucket: Sendable { case hour, day }

    /// One camera's line in the "By camera" table.
    public struct CameraRow: Equatable, Identifiable, Sendable {
        public var cameraId: String
        public var name: String
        public var events: Int
        public var alarms: Int
        /// The kind detected most often; nil when there were no detections.
        public var topKind: String?
        public var id: String { cameraId }

        public init(cameraId: String, name: String, events: Int, alarms: Int, topKind: String?) {
            self.cameraId = cameraId; self.name = name; self.events = events; self.alarms = alarms; self.topKind = topKind
        }
    }

    public struct SeverityTotal: Equatable, Sendable {
        public var severity: String
        public var count: Int
        public init(severity: String, count: Int) { self.severity = severity; self.count = count }
    }

    public struct Bar: Equatable, Identifiable, Sendable {
        /// The bucket's start, Unix ms.
        public var at: UnixMillis
        public var count: Int
        public var id: UnixMillis { at }
        public init(at: UnixMillis, count: Int) { self.at = at; self.count = count }
    }

    /// One row per camera, busiest first (events plus alarms), then by name.
    /// Names fall back to the camera id.
    public static func cameraRows(_ a: V2.Activity, names: [String: String]) -> [CameraRow] {
        var rows: [String: CameraRow] = [:]
        var kinds: [String: [String: Int]] = [:]
        var order: [String] = []
        func touch(_ id: String) {
            if rows[id] == nil {
                rows[id] = CameraRow(cameraId: id, name: names[id] ?? id, events: 0, alarms: 0, topKind: nil)
                order.append(id)
            }
        }
        for e in a.events {
            touch(e.cameraId)
            rows[e.cameraId]?.events += e.count
            kinds[e.cameraId, default: [:]][e.kind, default: 0] += e.count
        }
        for al in a.alarms {
            touch(al.cameraId)
            rows[al.cameraId]?.alarms += al.count
        }
        return order.compactMap { id -> CameraRow? in
            guard var row = rows[id] else { return nil }
            // The first kind to reach the highest count wins, as on the web.
            var best = 0
            for e in a.events where e.cameraId == id {
                let n = kinds[id]?[e.kind] ?? 0
                if n > best { best = n; row.topKind = e.kind }
            }
            return row
        }
        .sorted { x, y in
            let (tx, ty) = (x.events + x.alarms, y.events + y.alarms)
            return tx != ty ? tx > ty : x.name.localizedCompare(y.name) == .orderedAscending
        }
    }

    /// Alarm counts per severity: Critical, Warning, Info, then anything else
    /// (larger first).
    public static func severityTotals(_ a: V2.Activity) -> [SeverityTotal] {
        let order = ["Critical", "Warning", "Info"]
        func rank(_ s: String) -> Int { order.firstIndex(of: s) ?? order.count }
        var counts: [String: Int] = [:]
        for al in a.alarms { counts[al.severity, default: 0] += al.count }
        return counts.map { SeverityTotal(severity: $0.key, count: $0.value) }
            .sorted { x, y in
                rank(x.severity) != rank(y.severity) ? rank(x.severity) < rank(y.severity)
                    : x.count != y.count ? x.count > y.count : x.severity < y.severity
            }
    }

    /// Event counts per bucket across all cameras, covering the whole range
    /// with zeros for empty buckets: hours, or local days for longer ranges.
    public static func series(_ a: V2.Activity, bucket: Bucket, calendar: Calendar = .current) -> [Bar] {
        let hour: UnixMillis = 3_600_000
        func key(_ t: UnixMillis) -> UnixMillis {
            switch bucket {
            case .hour:
                return Int64((Double(t) / Double(hour)).rounded(.down)) * hour
            case .day:
                return calendar.startOfDay(for: Date(unixMillis: t)).unixMillis
            }
        }
        var counts: [UnixMillis: Int] = [:]
        for e in a.events { counts[key(e.hour), default: 0] += e.count }

        var out: [Bar] = []
        var t = key(a.from)
        while t < a.to {
            out.append(Bar(at: t, count: counts[t] ?? 0))
            switch bucket {
            case .hour:
                t += hour
            case .day:
                // Calendar days, so a daylight-saving change can't shift the buckets.
                guard let next = calendar.date(byAdding: .day, value: 1, to: Date(unixMillis: t)) else { return out }
                t = next.unixMillis
            }
        }
        return out
    }

    /// How a kind reads in the table; the server sends "" for an unlabelled one.
    public static func kindLabel(_ kind: String) -> String {
        kind.isEmpty ? "Other" : kind
    }

    /// Cameras with any detection or alarm in the range.
    public static func activeCameras(_ rows: [CameraRow]) -> Int {
        rows.filter { $0.events + $0.alarms > 0 }.count
    }
}
