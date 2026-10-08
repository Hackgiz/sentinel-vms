// SentinelMobileApp.swift

import SwiftUI
import UIKit
import UserNotifications

@main
struct SentinelMobileApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    init() {
        configureAppearance()
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .preferredColorScheme(.dark)
                .tint(SentinelTheme.accent)
        }
    }

    private func configureAppearance() {
        let bg = UIColor { trait in
            trait.userInterfaceStyle == .dark
                ? UIColor(red: 0.067, green: 0.086, blue: 0.125, alpha: 1)
                : UIColor.systemBackground
        }

        let navAppearance = UINavigationBarAppearance()
        navAppearance.configureWithOpaqueBackground()
        navAppearance.backgroundColor = bg
        navAppearance.shadowColor = .clear
        navAppearance.titleTextAttributes = [.foregroundColor: UIColor.white]
        navAppearance.largeTitleTextAttributes = [.foregroundColor: UIColor.white]
        UINavigationBar.appearance().standardAppearance = navAppearance
        UINavigationBar.appearance().scrollEdgeAppearance = navAppearance
        UINavigationBar.appearance().compactAppearance = navAppearance

        let tabAppearance = UITabBarAppearance()
        tabAppearance.configureWithOpaqueBackground()
        tabAppearance.backgroundColor = bg
        tabAppearance.shadowColor = UIColor.white.withAlphaComponent(0.08)
        UITabBar.appearance().standardAppearance = tabAppearance
        UITabBar.appearance().scrollEdgeAppearance = tabAppearance
    }
}

/// Handles APNs registration for AI-alert push notifications. On launch we ask
/// for permission and register; the device token is handed to SentinelSession,
/// which forwards it to the paired Mac VMS (the push provider).
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        // Skip the system permission prompt in dev screenshot mode (--demo) so
        // marketing captures aren't covered by the notifications dialog.
        var skipPrompt = false
        #if DEBUG
        skipPrompt = CommandLine.arguments.contains("--demo") || CommandLine.arguments.contains("--pair-url")
        #endif
        if !skipPrompt {
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
                guard granted else { return }
                DispatchQueue.main.async {
                    application.registerForRemoteNotifications()
                }
            }
        }
        return true
    }

    func application(_ application: UIApplication,
                     didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        let hex = deviceToken.map { String(format: "%02x", $0) }.joined()
        Task { @MainActor in
            SentinelSession.shared.updateAPNSToken(hex)
        }
    }

    func application(_ application: UIApplication,
                     didFailToRegisterForRemoteNotificationsWithError error: Error) {
        // Non-fatal: app still works for in-app alerts; just no push.
        print("APNs registration failed: \(error.localizedDescription)")
    }

    // Show banners even when the app is in the foreground.
    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions {
        [.banner, .sound, .list]
    }

    // Tapping an AI-alert push deep-links to the triggering camera's live view
    // when the Mac included a cameraID; otherwise it falls back to the Events tab.
    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                didReceive response: UNNotificationResponse) async {
        let userInfo = response.notification.request.content.userInfo
        let cameraID = (userInfo["cameraID"] as? String).flatMap(UUID.init(uuidString:))
        await MainActor.run {
            if let cameraID {
                AppStore.shared.route(toCameraID: cameraID)
            } else {
                AppStore.shared.openAlarms()
            }
        }
    }
}
