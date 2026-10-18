// WidgetSupport.swift
// Timeline provider and shared styling for the snapshot widgets.

import SwiftUI
import UIKit
import WidgetKit

struct SnapshotEntry: TimelineEntry {
    let date: Date
    let snapshot: WidgetSnapshot
    /// Last alarm thumbnail (only when small enough to share).
    let image: UIImage?
    /// The app hasn't refreshed in a while; numbers may be out of date.
    var isStale: Bool { date.timeIntervalSince(snapshot.generatedAt) > SnapshotProvider.staleAfter }
}

/// Reads the app-written snapshot. No network: when the app updates the
/// snapshot it asks WidgetCenter to reload, and the timeline re-reads it
/// every 15 minutes so "updated … ago" and staleness stay honest.
struct SnapshotProvider: TimelineProvider {
    static let refresh: TimeInterval = 15 * 60
    static let staleAfter: TimeInterval = 30 * 60

    var store = WidgetSnapshotStore.shared

    func placeholder(in context: Context) -> SnapshotEntry {
        SnapshotEntry(date: Date(), snapshot: .placeholder, image: nil)
    }

    func getSnapshot(in context: Context, completion: @escaping (SnapshotEntry) -> Void) {
        if context.isPreview, store.read() == nil {
            completion(placeholder(in: context))
        } else {
            completion(current())
        }
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<SnapshotEntry>) -> Void) {
        let entry = current()
        completion(Timeline(entries: [entry], policy: .after(entry.date.addingTimeInterval(Self.refresh))))
    }

    private func current() -> SnapshotEntry {
        let snapshot = store.read() ?? .signedOut
        var image: UIImage?
        if snapshot.lastAlarm?.hasImage == true, let data = store.readImage() {
            image = UIImage(data: data)
        }
        return SnapshotEntry(date: Date(), snapshot: snapshot, image: image)
    }
}

enum WidgetPalette {
    static let background = Color(red: 0.067, green: 0.086, blue: 0.125)
    static let panel = Color(red: 0.11, green: 0.13, blue: 0.18)
    static let accent = Color(red: 0.102, green: 0.498, blue: 0.831)
    static let amber = Color(red: 0.980, green: 0.720, blue: 0.100)
    static let green = Color(red: 0.071, green: 0.831, blue: 0.471)
    static let red = Color(red: 1.000, green: 0.231, blue: 0.188)

    static func severity(_ severity: String) -> Color {
        switch severity {
        case "Critical": return red
        case "Warning": return amber
        default: return accent
        }
    }

    static func phase(_ phase: AlarmActivityPhase) -> Color {
        switch phase {
        case .new: return red
        case .acknowledged: return accent
        case .closed: return green
        }
    }

    static func symbol(severity: String) -> String {
        severity == "Critical" ? "exclamationmark.octagon.fill" : "exclamationmark.triangle.fill"
    }
}

extension View {
    /// iOS 17 wants widgets to declare their background; iOS 16 draws it.
    @ViewBuilder
    func sentinelWidgetBackground(_ color: Color = WidgetPalette.background) -> some View {
        if #available(iOSApplicationExtension 17.0, *) {
            containerBackground(for: .widget) { color }
        } else {
            background(color)
        }
    }
}

/// Shown instead of numbers before the app has signed in to a site.
struct SignedOutWidgetView: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Image(systemName: "shield.lefthalf.filled")
                .font(.title3)
                .foregroundStyle(WidgetPalette.accent)
            Text("Sentinel")
                .font(.headline)
                .foregroundStyle(.white)
            Text("Open the app and sign in to a site.")
                .font(.caption)
                .foregroundStyle(.white.opacity(0.6))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

struct StaleBadge: View {
    let generatedAt: Date

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: "clock.arrow.circlepath")
            Text(generatedAt, style: .relative)
        }
        .font(.system(size: 10, weight: .medium))
        .foregroundStyle(WidgetPalette.amber)
        .lineLimit(1)
    }
}
