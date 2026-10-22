import SwiftUI

#if os(macOS)
import AppKit
public typealias PlatformColor = NSColor
#else
import UIKit
public typealias PlatformColor = UIColor
#endif

public enum SentinelTheme {
    public static let background  = adapt(
        dark:  PlatformColor(red: 0.039, green: 0.055, blue: 0.078, alpha: 1),
        light: PlatformColor(red: 0.941, green: 0.941, blue: 0.949, alpha: 1)
    )
    public static let chrome      = adapt(
        dark:  PlatformColor(red: 0.067, green: 0.086, blue: 0.125, alpha: 1),
        light: PlatformColor(red: 1.000, green: 1.000, blue: 1.000, alpha: 1)
    )
    public static let panel       = adapt(
        dark:  PlatformColor(red: 0.082, green: 0.102, blue: 0.145, alpha: 1),
        light: PlatformColor(red: 0.965, green: 0.965, blue: 0.973, alpha: 1)
    )
    public static let panelRaised = adapt(
        dark:  PlatformColor(red: 0.098, green: 0.122, blue: 0.169, alpha: 1),
        light: PlatformColor(red: 1.000, green: 1.000, blue: 1.000, alpha: 1)
    )
    public static let accent      = Color(red: 0.102, green: 0.498, blue: 0.831)
    public static let amber       = Color(red: 0.980, green: 0.720, blue: 0.100)
    public static let line        = adapt(
        dark:  PlatformColor.white.withAlphaComponent(0.09),
        light: PlatformColor.black.withAlphaComponent(0.10)
    )
    public static let recording   = Color(red: 0.071, green: 0.831, blue: 0.471)
    public static let alarm       = Color(red: 1.000, green: 0.231, blue: 0.188)
    public static var motion: Color { amber }

    // Timeline tokens shared with the web and Linux Operator (OPERATOR-UX "Shared visual tokens").
    /// Recorded band `#3b82f6` at 55% opacity.
    public static let timelineBand = Color(red: 0x3b / 255, green: 0x82 / 255, blue: 0xf6 / 255).opacity(0.55)
    /// Solid `#3b82f6`, for the hatching on pending footage and the export range.
    public static let timelineBandSolid = Color(red: 0x3b / 255, green: 0x82 / 255, blue: 0xf6 / 255)
    /// Motion tick `#f59e0b`.
    public static let motionTick = Color(red: 0xf5 / 255, green: 0x9e / 255, blue: 0x0b / 255)
    /// Person / vehicle tick `#60a5fa`.
    public static let objectTick = Color(red: 0x60 / 255, green: 0xa5 / 255, blue: 0xfa / 255)
    /// Alarm flag and playhead `#ef4444`.
    public static let playhead = Color(red: 0xef / 255, green: 0x44 / 255, blue: 0x44 / 255)
    /// Bar background: one step darker than the grid background, in light and dark.
    public static var timelineBar: Color { well }

    /// Slightly elevated fill for inset wells (timeline trough, list rows)
    /// that adapts to light/dark instead of hardcoded near-black RGB.
    public static let well = adapt(
        dark:  PlatformColor(red: 0.027, green: 0.043, blue: 0.071, alpha: 1),
        light: PlatformColor(red: 0.918, green: 0.922, blue: 0.937, alpha: 1)
    )

    // MARK: Wave 20: the iPhone's scale and colours, so both apps read alike

    /// Spacing, as on the iPhone (`SentinelTheme.Space` there).
    public enum Space {
        public static let xs: CGFloat = 4
        public static let sm: CGFloat = 8
        public static let md: CGFloat = 12
        public static let lg: CGFloat = 16
        public static let xl: CGFloat = 24
    }

    /// Corner radii, as on the iPhone.
    public enum Radius {
        public static let sm: CGFloat = 10
        public static let md: CGFloat = 14
        public static let lg: CGFloat = 20
    }

    public static func severityColor(_ severity: AlertSeverity) -> Color {
        switch severity {
        case .critical: return alarm
        case .warning: return amber
        case .info: return accent
        }
    }

    /// An alarm's accent: a Watchlist alarm keeps its kind colour whatever
    /// its severity; the rest follow severity.
    public static func alarmColor(kind: String, severity: AlertSeverity) -> Color {
        kind == "Watchlist" ? kindColor("Watchlist") : severityColor(severity)
    }

    public static func stateColor(_ state: AlertState) -> Color {
        switch state {
        case .resolved, .falseAlarm: return recording
        case .acknowledged: return accent
        case .investigating: return amber
        case .snoozed: return .gray
        case .new: return alarm
        }
    }

    /// Detection-kind colours, the same as the iPhone's chips.
    public static func kindColor(_ kind: String) -> Color {
        switch kind {
        case "Person": return accent
        case "Face": return Color(red: 1.0, green: 0.55, blue: 0.10)
        case "Vehicle": return Color(red: 0.20, green: 0.60, blue: 1.0)
        case "License Plate": return amber
        case "Animal": return recording
        case "Loitering": return Color(red: 0.80, green: 0.30, blue: 0.85)
        case "Watchlist": return alarm
        case "Motion": return amber
        default: return accent
        }
    }

    private static func adapt(dark: PlatformColor, light: PlatformColor) -> Color {
        #if os(macOS)
        return Color(NSColor(name: nil) { $0.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? dark : light })
        #else
        return Color(UIColor { trait in trait.userInterfaceStyle == .dark ? dark : light })
        #endif
    }
}

/// Shared spacing / radius tokens so every screen uses the same rhythm.
public enum SentinelMetrics {
    public static let gutter: CGFloat = 14      // outer padding around screens/cards
    public static let tight: CGFloat = 8        // inner gaps
    public static let cardRadius: CGFloat = 10
    public static let controlRadius: CGFloat = 8
}
