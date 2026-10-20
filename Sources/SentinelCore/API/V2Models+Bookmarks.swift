import Foundation

// Timeline bookmarks (`/api/v2/cameras/{cameraId}/bookmarks`,
// `/api/v2/bookmarks/{bookmarkId}`): a pinned moment (durationMs 0) or range
// on one camera's timeline. Wave 11, stream A.

public extension V2 {
    /// The server's palette tokens (`BookmarkColor`). An empty color on
    /// write means blue; an unknown one draws blue too.
    struct BookmarkColor: OpenStringEnum {
        public let rawValue: String
        public init(rawValue: String) { self.rawValue = rawValue }

        public static let red = BookmarkColor(rawValue: "red")
        public static let orange = BookmarkColor(rawValue: "orange")
        public static let yellow = BookmarkColor(rawValue: "yellow")
        public static let green = BookmarkColor(rawValue: "green")
        public static let teal = BookmarkColor(rawValue: "teal")
        public static let blue = BookmarkColor(rawValue: "blue")
        public static let purple = BookmarkColor(rawValue: "purple")
        public static let pink = BookmarkColor(rawValue: "pink")
        public static let gray = BookmarkColor(rawValue: "gray")

        public static let all: [BookmarkColor] = [.red, .orange, .yellow, .green, .teal, .blue, .purple, .pink, .gray]
        public static let `default` = BookmarkColor.blue

        /// Known tokens as they are; empty or unknown ones as the default.
        public var resolved: BookmarkColor { Self.all.contains(self) ? self : .default }

        /// `#rrggbb` as the web draws it (`bookmarks.ts`).
        public var hex: UInt32 {
            switch resolved.rawValue {
            case "red": return 0xEF4444
            case "orange": return 0xF97316
            case "yellow": return 0xEAB308
            case "green": return 0x22C55E
            case "teal": return 0x14B8A6
            case "purple": return 0xA855F7
            case "pink": return 0xEC4899
            case "gray": return 0x9CA3AF
            default: return 0x3B82F6
            }
        }

        public var label: String { resolved.rawValue.capitalized }
    }

    struct Bookmark: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var cameraId: String
        /// Start, Unix ms.
        public var atMs: UnixMillis
        /// 0 = a point, > 0 = a range from `atMs`.
        public var durationMs: Int64
        public var title: String
        public var note: String
        public var color: BookmarkColor
        public var createdBy: String
        public var createdAt: UnixMillis
        /// Whether the caller may edit or delete it (author or manager).
        public var canEdit: Bool

        public init(id: String, cameraId: String, atMs: UnixMillis, durationMs: Int64 = 0, title: String = "",
                    note: String = "", color: BookmarkColor = .default, createdBy: String = "",
                    createdAt: UnixMillis = 0, canEdit: Bool = true) {
            self.id = id
            self.cameraId = cameraId
            self.atMs = atMs
            self.durationMs = durationMs
            self.title = title
            self.note = note
            self.color = color
            self.createdBy = createdBy
            self.createdAt = createdAt
            self.canEdit = canEdit
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            cameraId = try c.decode(String.self, forKey: .cameraId)
            atMs = try c.decode(UnixMillis.self, forKey: .atMs)
            durationMs = try c.decodeIfPresent(Int64.self, forKey: .durationMs) ?? 0
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            note = try c.decodeIfPresent(String.self, forKey: .note) ?? ""
            color = try c.decodeIfPresent(BookmarkColor.self, forKey: .color) ?? .default
            createdBy = try c.decodeIfPresent(String.self, forKey: .createdBy) ?? ""
            createdAt = try c.decodeIfPresent(UnixMillis.self, forKey: .createdAt) ?? 0
            canEdit = try c.decodeIfPresent(Bool.self, forKey: .canEdit) ?? false
        }

        public var isRange: Bool { durationMs > 0 }
        public var endMs: UnixMillis { atMs + max(durationMs, 0) }
    }

    /// `POST /api/v2/cameras/{cameraId}/bookmarks`.
    struct BookmarkInput: Codable, Hashable, Sendable {
        public var atMs: UnixMillis
        public var durationMs: Int64?
        public var title: String?
        public var note: String?
        public var color: String?

        public init(atMs: UnixMillis, durationMs: Int64? = nil, title: String? = nil, note: String? = nil, color: String? = nil) {
            self.atMs = atMs
            self.durationMs = durationMs
            self.title = title
            self.note = note
            self.color = color
        }
    }

    /// `PATCH /api/v2/bookmarks/{bookmarkId}`; absent fields stay.
    struct BookmarkPatch: Codable, Hashable, Sendable {
        public var atMs: UnixMillis?
        public var durationMs: Int64?
        public var title: String?
        public var note: String?
        public var color: String?

        public init(atMs: UnixMillis? = nil, durationMs: Int64? = nil, title: String? = nil, note: String? = nil, color: String? = nil) {
            self.atMs = atMs
            self.durationMs = durationMs
            self.title = title
            self.note = note
            self.color = color
        }
    }
}
