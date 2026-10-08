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

    /// Slightly elevated fill for inset wells (timeline trough, list rows)
    /// that adapts to light/dark instead of hardcoded near-black RGB.
    public static let well = adapt(
        dark:  PlatformColor(red: 0.027, green: 0.043, blue: 0.071, alpha: 1),
        light: PlatformColor(red: 0.918, green: 0.922, blue: 0.937, alpha: 1)
    )

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
