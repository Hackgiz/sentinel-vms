import Foundation

// Wave 15 (Mac parity with the web Admin of wave 14): rule drafts, rule
// templates and Insights (`POST /api/v2/rules/draft`,
// `GET /api/v2/rule-templates`, `GET /api/v2/insights`).

// MARK: - Rule drafts and templates

public extension V2 {
    /// `POST /api/v2/rules/draft` body: the admin's description, 3–500 characters.
    struct RuleDraftRequest: Codable, Hashable, Sendable {
        public var text: String

        public init(text: String) { self.text = text }
    }

    /// A rule the server drafted from a description. Nothing is saved; the
    /// draft comes back disabled.
    struct RuleDraft: Codable, Hashable, Sendable {
        public var rule: RuleInput
        /// One sentence on what the rule will do.
        public var explanation: String
        /// What the server couldn't resolve, such as an unknown camera name.
        @NullableArray public var warnings: [String]
    }

    /// A ready-made rule to start from.
    struct RuleTemplate: Codable, Hashable, Identifiable, Sendable {
        public var id: String
        /// security, health or operations.
        public var category: String
        public var title: String
        public var summary: String
        public var rule: RuleInput
    }
}

// MARK: - Insights

public extension V2 {
    struct Insights: Codable, Hashable, Sendable {
        public struct Totals: Codable, Hashable, Sendable {
            public var events: Int
            public var alarms: Int
            public var camerasWithAlarms: Int
            /// Cameras with at least one Camera Offline alarm in the range.
            public var offlineCameras: Int
        }

        public struct Day: Codable, Hashable, Sendable {
            /// The server's local date, YYYY-MM-DD.
            public var day: String
            public var events: Int
            public var alarms: Int
            public var byKind: [String: Int]?

            public init(day: String, events: Int = 0, alarms: Int, byKind: [String: Int]? = nil) {
                self.day = day
                self.events = events
                self.alarms = alarms
                self.byKind = byKind
            }
        }

        public struct CameraActivity: Codable, Hashable, Sendable {
            public var cameraId: String
            public var name: String
            public var alarms: Int
            public var topKind: String?
            public var alertOn: AlertOn
        }

        public struct KindShare: Codable, Hashable, Sendable {
            public var kind: String
            public var alarms: Int
            /// 0–1 of the range's alarms.
            public var share: Double
        }

        public var from: UnixMillis
        public var to: UnixMillis
        public var totals: Totals
        /// Every local day in the range, oldest first, zero-filled.
        @NullableArray public var daily: [Day]
        /// Up to 10 cameras, most alarms first.
        @NullableArray public var cameras: [CameraActivity]
        /// Alarm kinds, most alarms first.
        @NullableArray public var mix: [KindShare]
        /// At most 6, most urgent first.
        @NullableArray public var recommendations: [Recommendation]

        /// Nothing to show yet: no alarms and no events in the range.
        public var isEmpty: Bool { totals.alarms == 0 && totals.events == 0 }
    }

    struct Recommendation: Codable, Hashable, Identifiable, Sendable {
        /// Stable per rule and camera, e.g. "motion-only:cam-7".
        public var id: String
        /// attention, suggestion or info.
        public var level: String
        public var title: String
        public var detail: String
        public var cameraId: String?
        public var action: RecommendationAction?

        public init(id: String, level: String = "info", title: String = "", detail: String = "",
                    cameraId: String? = nil, action: RecommendationAction? = nil) {
            self.id = id
            self.level = level
            self.title = title
            self.detail = detail
            self.cameraId = cameraId
            self.action = action
        }
    }

    struct RecommendationAction: Codable, Hashable, Sendable {
        /// setAlertOn, openTemplate, openCamera or openStorage.
        public var type: String
        /// For setAlertOn and openCamera.
        public var cameraId: String?
        public var alertOn: AlertOn?
        /// For openTemplate.
        public var templateId: String?

        public init(type: String, cameraId: String? = nil, alertOn: AlertOn? = nil, templateId: String? = nil) {
            self.type = type
            self.cameraId = cameraId
            self.alertOn = alertOn
            self.templateId = templateId
        }
    }
}
