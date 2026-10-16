import Foundation

// Testable logic behind the wave-6 management panes. No SwiftUI in here.

// MARK: - Roles

/// Editing state of one role: dirty tracking, builtin guards, the permission
/// matrix grouped by catalog group.
public struct RoleEditorModel: Equatable, Sendable {
    public static let nameLimit = 40

    public private(set) var original: V2.RoleRecord?
    public var name: String
    public var description: String
    public private(set) var permissions: Set<String>

    /// `nil` = a new role.
    public init(role: V2.RoleRecord?) {
        original = role
        name = role?.name ?? ""
        description = role?.description ?? ""
        permissions = Set(role?.permissions ?? [])
    }

    public var isNew: Bool { original == nil }
    /// Built-in roles can't be edited or deleted (the server answers 409).
    public var isLocked: Bool { original?.builtin == true }

    public var isDirty: Bool {
        guard let original else {
            return name.isEmpty == false || description.isEmpty == false || permissions.isEmpty == false
        }
        return name != original.name || description != original.description
            || permissions != Set(original.permissions)
    }

    /// Why Save is disabled, nil when it's allowed.
    public var blocker: String? {
        if isLocked { return "Built-in roles can't be changed. Duplicate it to make a custom role." }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty { return "Give the role a name." }
        if trimmed.count > Self.nameLimit { return "Role names are limited to \(Self.nameLimit) characters." }
        if permissions.isEmpty { return "Grant at least one permission." }
        return nil
    }

    public var canSave: Bool { isDirty && blocker == nil }

    public func has(_ id: String) -> Bool { permissions.contains(id) }

    public mutating func set(_ id: String, _ on: Bool) {
        guard isLocked == false else { return }
        if on { permissions.insert(id) } else { permissions.remove(id) }
    }

    /// Group header toggle: all on unless they already are, then all off.
    public mutating func toggleGroup(_ ids: [String]) {
        guard isLocked == false else { return }
        if ids.allSatisfy(permissions.contains) {
            ids.forEach { permissions.remove($0) }
        } else {
            ids.forEach { permissions.insert($0) }
        }
    }

    public func input(catalog: [V2.PermissionInfo] = []) -> V2.RoleInput {
        // Catalog order keeps the saved list stable; unknown ids sort last.
        let order = Dictionary(uniqueKeysWithValues: catalog.enumerated().map { ($1.id, $0) })
        let ids = permissions.sorted { (order[$0] ?? Int.max, $0) < (order[$1] ?? Int.max, $1) }
        return V2.RoleInput(name: name.trimmingCharacters(in: .whitespacesAndNewlines),
                            description: description.trimmingCharacters(in: .whitespacesAndNewlines),
                            permissions: ids)
    }

    /// Catalog entries grouped for the matrix, groups in first-seen order.
    public static func groups(_ catalog: [V2.PermissionInfo]) -> [(name: String, items: [V2.PermissionInfo])] {
        var order: [String] = []
        var buckets: [String: [V2.PermissionInfo]] = [:]
        for p in catalog {
            let g = p.group.isEmpty ? "General" : p.group
            if buckets[g] == nil { order.append(g) }
            buckets[g, default: []].append(p)
        }
        return order.map { ($0, buckets[$0] ?? []) }
    }
}

public enum RoleRules {
    /// Pre-flight text for Delete; nil when the server should be asked.
    public static func deleteBlocker(_ role: V2.RoleRecord) -> String? {
        if role.builtin { return "Built-in roles can't be deleted." }
        if role.userCount > 0 {
            return "\(role.userCount) user\(role.userCount == 1 ? "" : "s") still hold\(role.userCount == 1 ? "s" : "") this role. Move them to another role first."
        }
        return nil
    }
}

// MARK: - Time profiles

/// Weekly grid of half-hour cells (7 days x 48) and its conversion to and
/// from the server's `{days, start, end}` windows. A window whose end is at
/// or before its start runs past midnight into the next day.
public struct WeeklyGridModel: Equatable, Sendable {
    public static let cellsPerDay = 48
    public static let minutesPerCell = 30
    public static let dayNames = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

    /// `cells[day][slot]`, day 0 = Sunday.
    public private(set) var cells: [[Bool]]
    /// True when a loaded window didn't sit on half-hour boundaries and was
    /// widened to fit the grid.
    public private(set) var widenedOnLoad = false

    public init() {
        cells = Array(repeating: Array(repeating: false, count: Self.cellsPerDay), count: 7)
    }

    public init(windows: [V2.TimeWindow]) {
        self.init()
        for w in windows {
            guard let s = Self.minutes(w.start), let e = Self.minutes(w.end) else { continue }
            if s % Self.minutesPerCell != 0 || e % Self.minutesPerCell != 0 { widenedOnLoad = true }
            let first = s / Self.minutesPerCell                                  // floor
            let end = (e + Self.minutesPerCell - 1) / Self.minutesPerCell        // ceil
            for day in Set(w.days) where (0..<7).contains(day) {
                if end > first {
                    fill(day: day, from: first, to: end)
                } else {
                    // Past midnight (or a full 24 hours when end == start).
                    fill(day: day, from: first, to: Self.cellsPerDay)
                    fill(day: (day + 1) % 7, from: 0, to: end)
                }
            }
        }
    }

    private mutating func fill(day: Int, from: Int, to: Int) {
        guard from < to else { return }
        for slot in max(0, from)..<min(Self.cellsPerDay, to) { cells[day][slot] = true }
    }

    public subscript(day: Int, slot: Int) -> Bool {
        get { cells[day][slot] }
        set { cells[day][slot] = newValue }
    }

    public var isEmpty: Bool { cells.allSatisfy { $0.allSatisfy { !$0 } } }

    public mutating func clear() { self = WeeklyGridModel() }

    /// Sets a rectangle of cells (drag selection), days and slots inclusive
    /// in any order.
    public mutating func paint(days: ClosedRange<Int>, slots: ClosedRange<Int>, on: Bool) {
        for d in max(0, days.lowerBound)...min(6, days.upperBound) {
            for s in max(0, slots.lowerBound)...min(Self.cellsPerDay - 1, slots.upperBound) { cells[d][s] = on }
        }
    }

    /// Everything on for the given days (e.g. weekdays 9-17 presets).
    public mutating func setRange(days: [Int], startSlot: Int, endSlot: Int, on: Bool = true) {
        for d in days where (0..<7).contains(d) {
            if endSlot > startSlot {
                paint(days: d...d, slots: startSlot...(endSlot - 1), on: on)
            }
        }
    }

    public mutating func toggle(day: Int, slot: Int) { cells[day][slot].toggle() }

    /// Total scheduled hours per week.
    public var hoursPerWeek: Double {
        Double(cells.reduce(0) { $0 + $1.filter { $0 }.count }) * Double(Self.minutesPerCell) / 60
    }

    /// Server windows. Runs that reach midnight and continue at the start of
    /// the next day are merged into one past-midnight window; days with the
    /// same span share one window.
    public func windows() -> [V2.TimeWindow] {
        // Contiguous runs per day as [start, end) slots.
        var runs: [[(Int, Int)]] = Array(repeating: [], count: 7)
        for d in 0..<7 {
            var slot = 0
            while slot < Self.cellsPerDay {
                guard cells[d][slot] else { slot += 1; continue }
                var end = slot
                while end < Self.cellsPerDay, cells[d][end] { end += 1 }
                runs[d].append((slot, end))
                slot = end
            }
        }
        // Pass 1: which tails continue into the next day's leading run. Only
        // when that run ends before this one starts, so "end <= start" stays
        // the unambiguous marker for a window past midnight.
        var wrapEnd: [Int: [Int: Int]] = [:]       // day -> run start -> next-day end slot
        var consumed = Set<Int>()                  // days whose leading run joined the previous day
        for d in 0..<7 {
            guard let tail = runs[d].last, tail.1 == Self.cellsPerDay, tail.0 > 0 else { continue }
            let next = (d + 1) % 7
            if let lead = runs[next].first, lead.0 == 0, lead.1 < tail.0 {
                wrapEnd[d, default: [:]][tail.0] = lead.1
                consumed.insert(next)
            }
        }
        // Pass 2: spans; `end` in slots, 0 = midnight, 48 = a full-day run.
        var spans: [(day: Int, start: Int, end: Int)] = []
        for d in 0..<7 {
            for (i, run) in runs[d].enumerated() {
                if i == 0, consumed.contains(d) { continue }
                var e = run.1
                if e == Self.cellsPerDay, run.0 > 0 { e = wrapEnd[d]?[run.0] ?? 0 }
                spans.append((d, run.0, e))
            }
        }
        var grouped: [String: (start: Int, end: Int, days: [Int])] = [:]
        var order: [String] = []
        for span in spans {
            let key = "\(span.start)-\(span.end)"
            if grouped[key] == nil { grouped[key] = (span.start, span.end, []); order.append(key) }
            grouped[key]?.days.append(span.day)
        }
        return order.compactMap { key in
            guard let g = grouped[key] else { return nil }
            return V2.TimeWindow(days: g.days.sorted(), start: Self.text(g.start * Self.minutesPerCell),
                                 end: g.end == Self.cellsPerDay ? "23:59" : Self.text(g.end * Self.minutesPerCell))
        }
    }

    // MARK: Time text

    /// "HH:MM" (also "24:00") to minutes after midnight.
    public static func minutes(_ text: String) -> Int? {
        let parts = text.split(separator: ":")
        guard parts.count == 2, let h = Int(parts[0]), let m = Int(parts[1]),
              (0...24).contains(h), (0..<60).contains(m), h < 24 || m == 0 else { return nil }
        return h * 60 + m
    }

    public static func text(_ minutes: Int) -> String {
        let m = ((minutes % 1440) + 1440) % 1440
        return String(format: "%02d:%02d", m / 60, m % 60)
    }

    /// Row label for a slot, e.g. slot 19 -> "09:30".
    public static func slotLabel(_ slot: Int) -> String { text(slot * minutesPerCell) }
}

public enum TimeProfileRules {
    public static func blocker(name: String, grid: WeeklyGridModel) -> String? {
        if name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return "Give the profile a name." }
        if grid.isEmpty { return "Mark at least one time slot." }
        return nil
    }
}

// MARK: - Webhooks

/// Draft of the webhook editor. The secret is write-only: the draft never
/// holds the saved secret, only whether one exists and what (if anything)
/// the user typed to replace or clear it.
public struct WebhookDraft: Equatable, Sendable {
    public var name = ""
    public var url = ""
    public var enabled = true
    public var eventsText = ""
    /// What the user typed into the secret field (empty = untouched).
    public var newSecret = ""
    public var clearSecret = false
    public private(set) var hasSecret: Bool
    public private(set) var id: String?

    public init() { hasSecret = false }

    public init(_ hook: V2.Webhook) {
        id = hook.id
        name = hook.name
        url = hook.url
        enabled = hook.enabled
        eventsText = hook.events.joined(separator: ", ")
        hasSecret = hook.hasSecret
    }

    public var events: [String] {
        var seen = Set<String>()
        return eventsText.split(whereSeparator: { $0 == "," || $0 == "\n" || $0 == " " })
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { $0.isEmpty == false && seen.insert($0).inserted }
    }

    /// Text under the secret field.
    public var secretStatus: String {
        if clearSecret { return "The saved secret will be removed." }
        if newSecret.isEmpty == false { return hasSecret ? "The saved secret will be replaced." : "A secret will be set." }
        return hasSecret ? "A secret is set. Type a new one to replace it." : "No secret set. Deliveries are unsigned."
    }

    public var blocker: String? {
        if name.trimmingCharacters(in: .whitespaces).isEmpty { return "Give the webhook a name." }
        let trimmed = url.trimmingCharacters(in: .whitespaces)
        guard let parsed = URL(string: trimmed), let scheme = parsed.scheme?.lowercased(),
              scheme == "http" || scheme == "https", (parsed.host ?? "").isEmpty == false else {
            return "Enter a full http:// or https:// address."
        }
        return nil
    }

    /// Request body: `secret` is omitted unless the user typed or cleared it.
    public func input() -> V2.WebhookInput {
        let secret: String? = clearSecret ? "" : (newSecret.isEmpty ? nil : newSecret)
        return V2.WebhookInput(name: name.trimmingCharacters(in: .whitespaces),
                               url: url.trimmingCharacters(in: .whitespaces),
                               events: events, enabled: enabled, secret: secret)
    }
}

public enum WebhookStatusDot: Equatable, Sendable {
    case never, ok, failing, disabled

    public static func of(_ hook: V2.Webhook) -> WebhookStatusDot {
        if hook.enabled == false { return .disabled }
        if hook.lastFiredAt == 0 && hook.lastStatus == 0 { return .never }
        return (200..<300).contains(hook.lastStatus) ? .ok : .failing
    }

    public static func caption(_ hook: V2.Webhook, now: Date = Date()) -> String {
        switch of(hook) {
        case .disabled: return "Disabled"
        case .never: return "Never fired"
        case .ok, .failing:
            let status = hook.lastStatus == 0 ? "no answer" : "HTTP \(hook.lastStatus)"
            guard hook.lastFiredAt > 0 else { return status }
            let rel = RelativeDateTimeFormatter()
            rel.unitsStyle = .short
            return "\(status), \(rel.localizedString(for: Date(unixMillis: hook.lastFiredAt), relativeTo: now))"
        }
    }

    public static func testSummary(_ r: V2.WebhookTestResult) -> String {
        if r.ok { return r.status > 0 ? "Delivered (HTTP \(r.status))." : "Delivered." }
        if let e = r.error, e.isEmpty == false { return "Failed: \(e)" }
        return r.status > 0 ? "Failed (HTTP \(r.status))." : "Failed: no answer from the endpoint."
    }
}

// MARK: - Tasks

public enum TaskBoard {
    /// Finished tasks stay listed this long.
    public static let keepFinished: TimeInterval = 15 * 60
    public static let refreshSeconds: UInt64 = 3

    /// Running first (newest first), then finished (newest first); finished
    /// tasks older than 15 minutes are dropped.
    public static func rows(_ tasks: [V2.SystemTask], now: Date = Date()) -> [V2.SystemTask] {
        let cutoff = now.addingTimeInterval(-keepFinished).unixMillis
        let live = tasks.filter { $0.state == .running }.sorted { $0.started > $1.started }
        let done = tasks.filter { $0.state != .running && ($0.finished ?? $0.started) >= cutoff }
            .sorted { ($0.finished ?? $0.started) > ($1.finished ?? $1.started) }
        return live + done
    }

    public static func progressText(_ t: V2.SystemTask) -> String {
        switch t.state {
        case .running: return t.progress.map { "\(Int(min(100, max(0, $0)).rounded()))%" } ?? "Working…"
        case .done: return "Done"
        case .failed: return t.error.flatMap { $0.isEmpty ? nil : $0 } ?? "Failed"
        default: return t.state.rawValue.capitalized
        }
    }

    /// 0...1, nil = indeterminate or not running.
    public static func fraction(_ t: V2.SystemTask) -> Double? {
        guard t.state == .running, let p = t.progress else { return nil }
        return min(100, max(0, p)) / 100
    }

    public static func duration(_ t: V2.SystemTask, now: Date = Date()) -> String {
        let end = t.finished.map { Date(unixMillis: $0) } ?? now
        let secs = max(0, Int(end.timeIntervalSince(Date(unixMillis: t.started))))
        if secs < 60 { return "\(secs)s" }
        if secs < 3600 { return "\(secs / 60)m \(secs % 60)s" }
        return "\(secs / 3600)h \((secs % 3600) / 60)m"
    }
}

// MARK: - Logs

public enum LogLevel: String, CaseIterable, Sendable {
    case debug, info, warn, error

    public static func rank(_ text: String) -> Int {
        switch text.lowercased() {
        case "debug": return 0
        case "info": return 1
        case "warn", "warning": return 2
        case "error", "err": return 3
        default: return 1
        }
    }
}

/// The tail the Server Logs pane shows: bounded, filtered by minimum level.
public struct LogTail: Equatable, Sendable {
    public static let capacity = 2000
    public static let refreshSeconds: UInt64 = 3

    public private(set) var lines: [V2.LogLine] = []
    public var minimumLevel: LogLevel = .info

    public init() {}

    /// Replaces the tail with the server's newest lines (oldest first),
    /// keeping at most `capacity`.
    public mutating func replace(with fetched: [V2.LogLine]) {
        let sorted = fetched.sorted { $0.ts < $1.ts }
        lines = Array(sorted.suffix(Self.capacity))
    }

    public var visible: [V2.LogLine] {
        let floor = LogLevel.rank(minimumLevel.rawValue)
        return lines.filter { LogLevel.rank($0.level) >= floor }
    }

    /// Plain text for the pasteboard / a save.
    public func text(_ formatter: (UnixMillis) -> String = LogTail.defaultTime) -> String {
        visible.map { "\(formatter($0.ts)) \($0.level.uppercased().padding(toLength: 5, withPad: " ", startingAt: 0)) \($0.message)" }
            .joined(separator: "\n")
    }

    public static func defaultTime(_ ms: UnixMillis) -> String {
        Date(unixMillis: ms).formatted(date: .omitted, time: .standard)
    }
}

// MARK: - Thresholds

/// Editing state of the monitor thresholds with clamping: 1...100, amber
/// never above red.
public struct ThresholdEditor: Equatable, Sendable {
    public private(set) var values: V2.MonitorThresholds
    private let original: V2.MonitorThresholds

    public enum Meter: CaseIterable, Sendable { case cpu, mem, disk }

    public init(_ thresholds: V2.MonitorThresholds = .defaults) {
        values = Self.clamped(thresholds)
        original = values
    }

    public static func clamp(_ v: Double) -> Double { min(100, max(1, v.isFinite ? v.rounded() : 80)) }

    public static func clamped(_ t: V2.MonitorThresholds) -> V2.MonitorThresholds {
        func pair(_ p: V2.ThresholdPair) -> V2.ThresholdPair {
            let red = clamp(p.red)
            return V2.ThresholdPair(amber: min(clamp(p.amber), red), red: red)
        }
        return V2.MonitorThresholds(cpu: pair(t.cpu), mem: pair(t.mem), disk: pair(t.disk))
    }

    public func pair(_ m: Meter) -> V2.ThresholdPair {
        switch m {
        case .cpu: return values.cpu
        case .mem: return values.mem
        case .disk: return values.disk
        }
    }

    private mutating func store(_ p: V2.ThresholdPair, _ m: Meter) {
        switch m {
        case .cpu: values.cpu = p
        case .mem: values.mem = p
        case .disk: values.disk = p
        }
    }

    /// Setting amber above red pulls red up with it; setting red below amber
    /// pulls amber down, so the pair is always valid.
    public mutating func setAmber(_ v: Double, _ m: Meter) {
        var p = pair(m)
        p.amber = Self.clamp(v)
        if p.amber > p.red { p.red = p.amber }
        store(p, m)
    }

    public mutating func setRed(_ v: Double, _ m: Meter) {
        var p = pair(m)
        p.red = Self.clamp(v)
        if p.red < p.amber { p.amber = p.red }
        store(p, m)
    }

    public mutating func resetToDefaults() { values = .defaults }

    public var isDirty: Bool { values != original }
}

// MARK: - Configuration report

public enum ReportFormat: String, CaseIterable, Sendable {
    case json, html

    public var fileExtension: String { rawValue }
    public var label: String { self == .json ? "JSON" : "Printable page" }
}

public enum ConfigReportModel {
    /// "sentinel-config-2026-10-16.json" for the save panel.
    public static func suggestedFileName(site: String, format: ReportFormat, date: Date = Date(),
                                         calendar: Calendar = .current) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: date)
        let stamp = String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
        let slug = site.lowercased().map { $0.isLetter || $0.isNumber ? String($0) : "-" }.joined()
            .split(separator: "-").joined(separator: "-")
        return "\(slug.isEmpty ? "sentinel" : slug)-config-\(stamp).\(format.fileExtension)"
    }

    /// Sorted-key pretty JSON for the preview; anything that isn't JSON is
    /// shown as text.
    public static func preview(_ data: Data, limit: Int = 200_000) -> String {
        var text: String
        if let object = try? JSONSerialization.jsonObject(with: data),
           let pretty = try? JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]),
           let s = String(data: pretty, encoding: .utf8) {
            text = s
        } else {
            text = String(decoding: data, as: UTF8.self)
        }
        if text.count > limit { text = String(text.prefix(limit)) + "\n… (preview truncated; the saved file is complete)" }
        return text
    }

    /// Fetches the report and writes it atomically to `url` (the URL an
    /// NSSavePanel returned). Returns the bytes written.
    @discardableResult
    public static func save(api: SentinelAPI, format: ReportFormat, to url: URL) async throws -> Int {
        let data = try await api.configReport(html: format == .html)
        try data.write(to: url, options: .atomic)
        return data.count
    }
}

// MARK: - Push / MQTT form helpers

public enum NotificationRules {
    /// Minutes after midnight -> "22:00".
    public static func clock(_ minutes: Int) -> String { WeeklyGridModel.text(minutes) }

    public static func mqttStateText(_ s: V2.MQTTStatus) -> String {
        switch s.state {
        case "connected": return "Connected"
        case "connecting": return "Connecting…"
        case "error": return s.error.isEmpty ? "Error" : "Error: \(s.error)"
        default: return "Off"
        }
    }

    public static func mqttBlocker(host: String, port: Int, enabled: Bool) -> String? {
        guard enabled else { return nil }
        if host.trimmingCharacters(in: .whitespaces).isEmpty { return "Enter the broker's address." }
        if (1...65535).contains(port) == false { return "The port must be between 1 and 65535." }
        return nil
    }

    public static func pushTestSummary(_ r: V2.PushTestResult) -> String {
        var text = "Test sent to \(r.sent) device\(r.sent == 1 ? "" : "s")."
        if r.failed.isEmpty == false { text += " " + r.failed.joined(separator: " ") }
        return text
    }
}
