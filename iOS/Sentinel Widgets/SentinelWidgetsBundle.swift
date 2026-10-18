// SentinelWidgetsBundle.swift
// The "Sentinel Widgets" extension: Home and Lock Screen widgets fed by the
// snapshot the app writes to the App Group, and the alarm Live Activity.
// The extension never talks to the Management Server and never sees a
// sign-in token.

import SwiftUI
import WidgetKit

@main
struct SentinelWidgetsBundle: WidgetBundle {
    var body: some Widget {
        SiteStatusWidget()
        LastAlarmWidget()
        AlarmLiveActivityWidget()
    }
}
