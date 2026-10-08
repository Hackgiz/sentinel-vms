// SentinelTheme.swift
// Color palette mirrored from the macOS app's Sources/SentinelCore/SentinelTheme.swift
// so both apps share the same visual language: deep navy backgrounds, layered
// panels, Sentinel blue accent, status colors (recording green, motion amber,
// alarm red).

import SwiftUI
import UIKit

enum SentinelTheme {
    static let background  = adapt(
        dark:  UIColor(red: 0.039, green: 0.055, blue: 0.078, alpha: 1),
        light: UIColor(red: 0.941, green: 0.941, blue: 0.949, alpha: 1)
    )
    static let chrome      = adapt(
        dark:  UIColor(red: 0.067, green: 0.086, blue: 0.125, alpha: 1),
        light: UIColor(red: 1.000, green: 1.000, blue: 1.000, alpha: 1)
    )
    static let panel       = adapt(
        dark:  UIColor(red: 0.082, green: 0.102, blue: 0.145, alpha: 1),
        light: UIColor(red: 0.965, green: 0.965, blue: 0.973, alpha: 1)
    )
    static let panelRaised = adapt(
        dark:  UIColor(red: 0.098, green: 0.122, blue: 0.169, alpha: 1),
        light: UIColor(red: 1.000, green: 1.000, blue: 1.000, alpha: 1)
    )
    static let accent      = Color(red: 0.102, green: 0.498, blue: 0.831)
    static let amber       = Color(red: 0.980, green: 0.720, blue: 0.100)
    static let recording   = Color(red: 0.071, green: 0.831, blue: 0.471)
    static let alarm       = Color(red: 1.000, green: 0.231, blue: 0.188)
    static var motion: Color { amber }

    static let line = adapt(
        dark:  UIColor.white.withAlphaComponent(0.09),
        light: UIColor.black.withAlphaComponent(0.10)
    )

    private static func adapt(dark: UIColor, light: UIColor) -> Color {
        Color(UIColor { trait in trait.userInterfaceStyle == .dark ? dark : light })
    }

    static func severityColor(_ severity: String) -> Color {
        switch severity {
        case "Critical": return alarm
        case "Warning":  return amber
        case "Info":     return accent
        default:         return accent
        }
    }

    static func statusColor(_ status: String) -> Color {
        switch status {
        case "online":   return recording
        case "motion":   return motion
        case "offline":  return alarm
        default:         return .gray
        }
    }

    static func stateColor(_ state: String) -> Color {
        switch state {
        case "Resolved", "False Alarm": return recording
        case "Acknowledged":            return accent
        case "Investigating":           return amber
        case "Snoozed":                 return .gray
        case "New":                     return alarm
        default:                        return .gray
        }
    }

    // MARK: - AI detection-kind styling (event feed)

    static func kindColor(_ kind: String) -> Color {
        switch kind {
        case "Person":        return accent
        case "Face":          return Color(red: 1.0, green: 0.55, blue: 0.10)
        case "Vehicle":       return Color(red: 0.20, green: 0.60, blue: 1.0)
        case "License Plate": return amber
        case "Animal":        return recording
        case "Loitering":     return Color(red: 0.80, green: 0.30, blue: 0.85)
        default:              return accent
        }
    }

    static func kindSymbol(_ kind: String) -> String {
        switch kind {
        case "Person":        return "figure.walk"
        case "Face":          return "person.fill.viewfinder"
        case "Vehicle":       return "car.fill"
        case "License Plate": return "doc.text.magnifyingglass"
        case "Animal":        return "pawprint.fill"
        case "Loitering":     return "clock.badge.exclamationmark"
        default:              return "sparkles"
        }
    }

    // MARK: - Design tokens

    enum Space {
        static let xs: CGFloat = 4
        static let sm: CGFloat = 8
        static let md: CGFloat = 12
        static let lg: CGFloat = 16
        static let xl: CGFloat = 24
    }
    enum Radius {
        static let sm: CGFloat = 10
        static let md: CGFloat = 14
        static let lg: CGFloat = 20
    }
}

// MARK: - Haptics

enum Haptics {
    static func tap() {
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
    }
    static func success() {
        UINotificationFeedbackGenerator().notificationOccurred(.success)
    }
    static func warning() {
        UINotificationFeedbackGenerator().notificationOccurred(.warning)
    }
}

// MARK: - Auth-aware async image

/// Loads a signed snapshot/thumbnail URL with a graceful placeholder. Used for
/// camera tiles and event thumbnails. The token rides in the URL query, so a
/// plain URLSession fetch authenticates.
struct AuthImage: View {
    let url: URL?
    var contentMode: ContentMode = .fill

    @State private var image: UIImage?
    @State private var failed = false

    var body: some View {
        GeometryReader { geo in
            ZStack {
                if let image {
                    Image(uiImage: image)
                        .resizable()
                        .aspectRatio(contentMode: contentMode)
                        .frame(width: geo.size.width, height: geo.size.height)
                        .clipped()
                } else {
                    LinearGradient(colors: [Color(white: 0.12), Color(white: 0.04)],
                                   startPoint: .topLeading, endPoint: .bottomTrailing)
                    if failed {
                        Image(systemName: "photo")
                            .font(.title3)
                            .foregroundStyle(.white.opacity(0.25))
                    } else {
                        ProgressView().tint(.white.opacity(0.4))
                    }
                }
            }
        }
        .task(id: url) { await load() }
    }

    private func load() async {
        image = nil; failed = false
        guard let url else { failed = true; return }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            if let img = UIImage(data: data) { image = img } else { failed = true }
        } catch { failed = true }
    }
}

/// An AI-event frame thumbnail. Only fetches over the network when the Mac
/// actually captured a frame (`event.hasThumbnail`); otherwise it renders a
/// kind-tinted placeholder so we don't fire a guaranteed-404 request or spin
/// forever on events that never had a saved frame.
struct EventThumbnail: View {
    let event: SentinelEvent
    var contentMode: ContentMode = .fill

    var body: some View {
        if event.hasThumbnail {
            AuthImage(url: SentinelSession.shared.eventThumbnailURL(eventID: event.id), contentMode: contentMode)
        } else {
            ZStack {
                LinearGradient(colors: [Color(white: 0.12), Color(white: 0.04)],
                               startPoint: .topLeading, endPoint: .bottomTrailing)
                Image(systemName: SentinelTheme.kindSymbol(event.kind))
                    .font(.title2)
                    .foregroundStyle(SentinelTheme.kindColor(event.kind).opacity(0.75))
            }
        }
    }
}

// MARK: - Reusable chrome

/// A small labeled section header used across the redesigned screens.
struct SectionHeader: View {
    let title: String
    var action: (() -> Void)?
    var actionLabel: String?

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title)
                .font(.headline)
                .foregroundStyle(.white)
            Spacer()
            if let action, let actionLabel {
                Button(actionLabel, action: action)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(SentinelTheme.accent)
            }
        }
    }
}

/// A soft chip/pill (e.g. "LIVE", "2 online").
struct Chip: View {
    let text: String
    var systemImage: String?
    var color: Color = SentinelTheme.accent
    var filled = false

    var body: some View {
        HStack(spacing: 4) {
            if let systemImage { Image(systemName: systemImage).font(.caption2.weight(.bold)) }
            Text(text).font(.caption2.weight(.bold))
        }
        .foregroundStyle(filled ? .white : color)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(filled ? color : color.opacity(0.16), in: Capsule())
    }
}

// MARK: - Reusable styled containers

struct SentinelCard<Content: View>: View {
    var padding: CGFloat = 16
    @ViewBuilder var content: () -> Content

    var body: some View {
        content()
            .padding(padding)
            .background(SentinelTheme.panel)
            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                    .stroke(SentinelTheme.line, lineWidth: 1)
            )
    }
}

struct SentinelStatusBadge: View {
    let status: String
    var body: some View {
        let color = SentinelTheme.statusColor(status)
        Text(status.capitalized)
            .font(.caption2.weight(.bold))
            .foregroundStyle(color)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(color.opacity(0.16), in: Capsule())
            .overlay(Capsule().stroke(color.opacity(0.4), lineWidth: 0.5))
    }
}

struct SentinelSeverityBadge: View {
    let severity: String
    var body: some View {
        let color = SentinelTheme.severityColor(severity)
        Text(severity.uppercased())
            .font(.caption2.weight(.heavy))
            .foregroundStyle(color)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(color.opacity(0.16), in: Capsule())
    }
}
