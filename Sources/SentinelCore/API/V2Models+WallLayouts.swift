import Foundation

// Wave 10 / stream A: video wall layouts (`/api/v2/wall-layouts`, tag
// "wall-layouts"). A layout names the Mac app's monitor windows: which
// display each sits on and what it shows.

public extension V2 {
    /// A monitor that cycles saved views every `seconds`.
    struct WallTour: Codable, Hashable, Sendable {
        public static let secondsRange = 5...3600
        public static let maxViews = 32

        @NullableArray public var viewIds: [String]
        public var seconds: Int

        public init(viewIds: [String], seconds: Int) {
            self.viewIds = viewIds
            self.seconds = seconds
        }
    }

    /// One monitor window of a wall layout. It shows `viewId` or
    /// `cameraIds`, or a `tour` (which may start on `viewId`).
    struct WallMonitor: Codable, Hashable, Sendable {
        public static let maxSlots = 16
        public static let maxCameras = 64

        public var slot: Int
        /// The display's name when the layout was saved (matched first).
        public var screenHint: String
        /// The display's position when saved (0 = main): the fallback.
        public var screenIndex: Int?
        public var viewId: String?
        public var cameraIds: [String]?
        public var tour: WallTour?
        public var alarmMonitor: Bool?
        public var fullscreen: Bool?

        public init(slot: Int, screenHint: String, screenIndex: Int? = nil, viewId: String? = nil,
                    cameraIds: [String]? = nil, tour: WallTour? = nil, alarmMonitor: Bool? = nil, fullscreen: Bool? = nil) {
            self.slot = slot
            self.screenHint = screenHint
            self.screenIndex = screenIndex
            self.viewId = viewId
            self.cameraIds = cameraIds
            self.tour = tour
            self.alarmMonitor = alarmMonitor
            self.fullscreen = fullscreen
        }

        public var isAlarmMonitor: Bool { alarmMonitor ?? false }
        public var isFullscreen: Bool { fullscreen ?? false }
    }

    struct WallLayout: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        public var name: String
        public var shared: Bool
        @NullableArray public var monitors: [WallMonitor]
        public var createdBy: String
        public var createdAt: UnixMillis
        public var updatedAt: UnixMillis
        public var canEdit: Bool

        public init(id: String, name: String, shared: Bool = false, monitors: [WallMonitor] = [], createdBy: String = "",
                    createdAt: UnixMillis = 0, updatedAt: UnixMillis = 0, canEdit: Bool = true) {
            self.id = id
            self.name = name
            self.shared = shared
            self.monitors = monitors
            self.createdBy = createdBy
            self.createdAt = createdAt
            self.updatedAt = updatedAt
            self.canEdit = canEdit
        }
    }

    struct WallLayoutInput: Codable, Hashable, Sendable {
        public var name: String
        public var shared: Bool
        public var monitors: [WallMonitor]

        public init(name: String, shared: Bool = false, monitors: [WallMonitor]) {
            self.name = name
            self.shared = shared
            self.monitors = monitors
        }
    }

    /// `PATCH` body: only the fields that are set change (monitors replace the list).
    struct WallLayoutPatch: Codable, Hashable, Sendable {
        public var name: String?
        public var shared: Bool?
        public var monitors: [WallMonitor]?

        public init(name: String? = nil, shared: Bool? = nil, monitors: [WallMonitor]? = nil) {
            self.name = name
            self.shared = shared
            self.monitors = monitors
        }
    }
}
