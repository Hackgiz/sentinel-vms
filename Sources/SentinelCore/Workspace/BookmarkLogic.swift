import Foundation

/// Pure helpers behind the playback timeline's bookmark glyphs, day list
/// and add/edit sheet (Wave 11, stream A; mirrors the web's `bookmarks.ts`).
public enum BookmarkLogic {
    public static let maxTitle = 120
    public static let maxNote = 2000
    /// Longest range a bookmark may cover (the API's `durationMs` maximum).
    public static let maxDurationMs: Int64 = 86_400_000
    /// A point bookmark's "Export…" covers this much either side of it.
    public static let pointExportPadMs: Int64 = 30_000

    /// The title, or "Bookmark" when it has none.
    public static func label(_ b: V2.Bookmark) -> String {
        let t = b.title.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? "Bookmark" : t
    }

    /// Oldest first; ties by creation then id (stable across reloads).
    public static func sorted(_ list: [V2.Bookmark]) -> [V2.Bookmark] {
        list.sorted { a, b in
            if a.atMs != b.atMs { return a.atMs < b.atMs }
            if a.createdAt != b.createdAt { return a.createdAt < b.createdAt }
            return a.id < b.id
        }
    }

    public static func upsert(_ list: [V2.Bookmark], _ b: V2.Bookmark) -> [V2.Bookmark] {
        sorted(list.filter { $0.id != b.id } + [b])
    }

    public static func remove(_ list: [V2.Bookmark], id: String) -> [V2.Bookmark] {
        list.filter { $0.id != id }
    }

    /// Bookmarks touching `[start, end]`.
    public static func inWindow(_ list: [V2.Bookmark], start: Int64, end: Int64) -> [V2.Bookmark] {
        list.filter { $0.atMs <= end && $0.endMs >= start }
    }

    /// What a new bookmark covers: the marked in/out range when asked and
    /// valid (capped at 24 h), else the playhead as a point.
    public static func newTime(at: Int64, selection: (from: Int64, to: Int64)?, useSelection: Bool) -> (atMs: Int64, durationMs: Int64) {
        if useSelection, let r = selection, r.to > r.from {
            return (max(r.from, 1), min(r.to - r.from, maxDurationMs))
        }
        return (max(at, 1), 0)
    }

    /// "1 min 20 s" for a range's length; "" for a point.
    public static func durationText(_ ms: Int64) -> String {
        guard ms > 0 else { return "" }
        let s = Int((Double(ms) / 1000).rounded())
        let h = s / 3600, m = (s % 3600) / 60, sec = s % 60
        var parts: [String] = []
        if h > 0 { parts.append("\(h) h") }
        if m > 0 { parts.append("\(m) min") }
        if sec > 0 || (h == 0 && m == 0) { parts.append("\(sec) s") }
        return parts.joined(separator: " ")
    }

    /// The export selection "Export…" sets: a range as it is, a point
    /// ±30 s. The end never passes `now`.
    public static func exportRange(_ b: V2.Bookmark, now: Int64) -> (from: Int64, to: Int64) {
        let from = b.isRange ? b.atMs : b.atMs - pointExportPadMs
        let to = b.isRange ? b.endMs : b.atMs + pointExportPadMs
        let end = min(to, now)
        return (max(from, 0), max(end, from + 1))
    }

    // MARK: Ruler glyphs

    /// A bookmark as drawn on the ruler: a flag at `x` (point) or a bar
    /// from `x` to `x1` (range), in points of the ruler's width.
    public struct Glyph: Equatable {
        public var id: String
        public var x: Double
        public var x1: Double
        public var isRange: Bool
    }

    /// The glyphs for `list` on a ruler showing `[from, to]` that is `width`
    /// points wide; bookmarks outside the window are left out, ranges are
    /// clipped to it.
    public static func glyphs(_ list: [V2.Bookmark], from: Int64, to: Int64, width: Double) -> [Glyph] {
        guard to > from, width > 0 else { return [] }
        let scale = width / Double(to - from)
        return inWindow(list, start: from, end: to).map { b in
            let x0 = Double(max(b.atMs, from) - from) * scale
            let x1 = Double(min(b.endMs, to) - from) * scale
            return Glyph(id: b.id, x: b.isRange ? x0 : Double(b.atMs - from) * scale, x1: b.isRange ? x1 : Double(b.atMs - from) * scale, isRange: b.isRange)
        }
    }

    /// The glyph under a click at `x` (within `slop` points); the nearest
    /// point flag wins over a range bar it sits on.
    public static func hit(_ glyphs: [Glyph], x: Double, slop: Double = 5) -> Glyph? {
        let points = glyphs.filter { !$0.isRange && abs($0.x - x) <= slop }
        if let p = points.min(by: { abs($0.x - x) < abs($1.x - x) }) { return p }
        return glyphs.filter { $0.isRange && x >= $0.x - slop && x <= $0.x1 + slop }
            .min { ($0.x1 - $0.x) < ($1.x1 - $1.x) }
    }

    // MARK: Editing

    /// What the add/edit sheet holds; builds the request bodies and says
    /// what is wrong with them.
    public struct Draft: Equatable {
        public var cameraID: String
        public var atMs: Int64
        /// The marked in/out range when one was set as the sheet opened.
        public var selection: Range?
        /// Cover `selection` instead of the playhead.
        public var useSelection: Bool
        public var title: String
        public var note: String
        public var color: V2.BookmarkColor
        /// Set when editing an existing bookmark.
        public var editingID: String?

        public struct Range: Equatable {
            public var from: Int64
            public var to: Int64
            public init(from: Int64, to: Int64) { self.from = from; self.to = to }
        }

        /// A new bookmark at the playhead; "cover the marked range" is on when a range is marked.
        public init(cameraID: String, at: Int64, selection: (from: Int64, to: Int64)?) {
            self.cameraID = cameraID
            self.atMs = at
            self.selection = selection.flatMap { $0.to > $0.from ? Range(from: $0.from, to: $0.to) : nil }
            self.useSelection = self.selection != nil
            self.title = ""
            self.note = ""
            self.color = .default
            self.editingID = nil
        }

        /// Editing `b` (its time stays; title, note and color change).
        public init(editing b: V2.Bookmark) {
            cameraID = b.cameraId
            atMs = b.atMs
            selection = b.isRange ? Range(from: b.atMs, to: b.endMs) : nil
            useSelection = b.isRange
            title = b.title
            note = b.note
            color = b.color.resolved
            editingID = b.id
        }

        public var isEditing: Bool { editingID != nil }

        /// What the bookmark will cover.
        public var time: (atMs: Int64, durationMs: Int64) {
            BookmarkLogic.newTime(at: atMs, selection: selection.map { ($0.from, $0.to) }, useSelection: useSelection)
        }

        /// Why it can't be saved, or nil.
        public var problem: String? {
            if title.count > BookmarkLogic.maxTitle { return "The title can be at most \(BookmarkLogic.maxTitle) characters." }
            if note.count > BookmarkLogic.maxNote { return "The note can be at most \(BookmarkLogic.maxNote) characters." }
            if cameraID.isEmpty { return "Select a camera first." }
            return nil
        }

        public var input: V2.BookmarkInput {
            let t = time
            return V2.BookmarkInput(atMs: t.atMs, durationMs: t.durationMs,
                                    title: title.trimmingCharacters(in: .whitespacesAndNewlines),
                                    note: note.trimmingCharacters(in: .whitespacesAndNewlines),
                                    color: color.resolved.rawValue)
        }

        /// The edit, as the web sends it: title, note and color.
        public var patch: V2.BookmarkPatch {
            V2.BookmarkPatch(title: title.trimmingCharacters(in: .whitespacesAndNewlines),
                             note: note.trimmingCharacters(in: .whitespacesAndNewlines),
                             color: color.resolved.rawValue)
        }
    }
}

/// The bookmarks a playback session shows: the loaded day's, per camera.
/// Value type so the store's rules are tested here.
public struct BookmarkBook: Equatable {
    public private(set) var byCamera: [String: [V2.Bookmark]] = [:]
    /// The window the bookmarks were loaded for.
    public private(set) var from: Int64 = 0
    public private(set) var to: Int64 = 0

    public init() {}

    /// Replaces everything with a fresh load.
    public mutating func replace(_ loaded: [String: [V2.Bookmark]], from: Int64, to: Int64) {
        byCamera = loaded.mapValues(BookmarkLogic.sorted)
        self.from = from
        self.to = to
    }

    public func bookmarks(for cameraID: String?) -> [V2.Bookmark] {
        cameraID.flatMap { byCamera[$0] } ?? []
    }

    /// Every camera's, oldest first (the day list).
    public var all: [V2.Bookmark] { BookmarkLogic.sorted(byCamera.values.flatMap { $0 }) }

    public func bookmark(_ id: String) -> V2.Bookmark? {
        byCamera.values.lazy.flatMap { $0 }.first { $0.id == id }
    }

    /// Adds or replaces (a bookmark moved to another camera leaves the old one).
    public mutating func upsert(_ b: V2.Bookmark) {
        for key in byCamera.keys where key != b.cameraId { byCamera[key] = BookmarkLogic.remove(byCamera[key] ?? [], id: b.id) }
        byCamera[b.cameraId] = BookmarkLogic.upsert(byCamera[b.cameraId] ?? [], b)
    }

    public mutating func remove(id: String) {
        for key in byCamera.keys { byCamera[key] = BookmarkLogic.remove(byCamera[key] ?? [], id: id) }
    }
}
