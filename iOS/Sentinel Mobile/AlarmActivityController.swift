// AlarmActivityController.swift
// The alarm Live Activity from the app's side. "Follow" on an alarm starts
// one (one followed alarm at a time; following another replaces it), and
// Critical alarms can be followed automatically while the app is in the
// foreground. While the app runs it updates the activity from the v2 event
// stream; it also hands the activity's push token to the Management Server
// (PUT /api/v2/push/activity-token) so the server can update or end it over
// APNs (`liveactivity` push type) when the app is suspended.

import Foundation
import SwiftUI
import UIKit
#if canImport(ActivityKit)
import ActivityKit
#endif

@MainActor
final class AlarmActivityController: ObservableObject {
    static let shared = AlarmActivityController()

    /// The alarm the running Live Activity follows.
    @Published private(set) var followedAlarmID: String?
    @Published var autoFollowCritical: Bool {
        didSet { defaults.set(autoFollowCritical, forKey: Self.autoFollowKey) }
    }

    private let defaults: UserDefaults
    private static let autoFollowKey = "sentinel.mobile.autoFollowCritical"
    /// The followed activity's push token (hex), re-sent after push registration.
    private var activityToken: String?
    /// AlarmActivityRunner on iOS 16.1+ (kept untyped for the 16.0 deployment target).
    private var runner: AnyObject?

    /// How long an ended activity stays on the Lock Screen.
    static let endedLinger: TimeInterval = 15 * 60

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        autoFollowCritical = defaults.bool(forKey: Self.autoFollowKey)
    }

    /// Live Activities exist on this OS and the user allows them.
    var isAvailable: Bool {
        #if canImport(ActivityKit)
        if #available(iOS 16.1, *) { return ActivityAuthorizationInfo().areActivitiesEnabled }
        #endif
        return false
    }

    func isFollowing(_ alarm: V2.Alarm) -> Bool { followedAlarmID == alarm.id }

    // MARK: Pure helpers (unit-tested)

    /// The activity's content for an alarm (the same shape the server pushes).
    nonisolated static func content(for alarm: V2.Alarm) -> AlarmActivityContent {
        AlarmActivityContent(state: alarm.state.rawValue, owner: alarm.owner, severity: alarm.severity.rawValue,
                             updatedAt: max(alarm.updatedAt, alarm.lastEventAt), eventCount: alarm.eventCount)
    }

    /// Whether a newly raised alarm should be followed automatically.
    nonisolated static func shouldAutoFollow(_ alarm: V2.Alarm, enabled: Bool, following: String?, isForeground: Bool) -> Bool {
        enabled && isForeground && following == nil && alarm.isOpen && alarm.severity == .critical
    }

    // MARK: Lifecycle

    /// Adopts an activity left running by a previous launch.
    func restore() {
        #if canImport(ActivityKit)
        guard #available(iOS 16.1, *), runner == nil,
              let activity = Activity<AlarmActivityAttributes>.activities.first(where: { $0.activityState == .active }) else { return }
        adopt(activity)
        #endif
    }

    func follow(_ alarm: V2.Alarm, cameraName: String?) {
        #if canImport(ActivityKit)
        guard #available(iOS 16.1, *), alarm.isOpen else { return }
        if followedAlarmID == alarm.id { return }
        endCurrent(immediately: true)
        let attributes = AlarmActivityAttributes(alarmId: alarm.id, title: alarm.title, cameraName: cameraName ?? "",
                                                 kind: alarm.kind, raisedAt: alarm.createdAt)
        do {
            let activity = try AlarmActivityRunner.request(attributes: attributes, content: Self.content(for: alarm))
            adopt(activity)
            Haptics.success()
        } catch {
            AppStore.shared.actionError = "Couldn't start the Live Activity: \(error.localizedDescription)"
        }
        #endif
    }

    func unfollow() {
        endCurrent(immediately: true)
    }

    /// Every alarm change the app sees (event stream, actions, resync).
    func alarmChanged(_ alarm: V2.Alarm) {
        #if canImport(ActivityKit)
        guard #available(iOS 16.1, *), alarm.id == followedAlarmID, let runner = runner as? AlarmActivityRunner else { return }
        let content = Self.content(for: alarm)
        if alarm.isOpen {
            Task { await runner.update(content) }
        } else {
            Task { await runner.end(content, dismissAfter: Self.endedLinger) }
            detach()
        }
        #endif
    }

    /// A newly raised alarm (event stream).
    func alarmRaised(_ alarm: V2.Alarm, cameraName: String?) {
        let foreground = UIApplication.shared.applicationState == .active
        guard Self.shouldAutoFollow(alarm, enabled: autoFollowCritical, following: followedAlarmID,
                                    isForeground: foreground), isAvailable else { return }
        follow(alarm, cameraName: cameraName)
    }

    /// After a full reload: the followed alarm may have closed while the
    /// stream was down. Look it up once and apply what the server says.
    func reconcile(openAlarms: [V2.Alarm], api: SentinelAPI?) {
        guard let id = followedAlarmID else { return }
        if let alarm = openAlarms.first(where: { $0.id == id }) {
            alarmChanged(alarm)
        } else if let api {
            Task { if let alarm = try? await api.alarm(id) { alarmChanged(alarm) } }
        }
    }

    /// Sign-out or leaving the site: the activity belongs to that sign-in.
    func sessionEnded() {
        endCurrent(immediately: true)
    }

    /// Push registration just (re)ran: the server may not know the token yet.
    func resendToken() {
        sendToken()
    }

    // MARK: Internals

    #if canImport(ActivityKit)
    @available(iOS 16.1, *)
    private func adopt(_ activity: Activity<AlarmActivityAttributes>) {
        let alarmID = activity.attributes.alarmId
        followedAlarmID = alarmID
        activityToken = nil
        runner = AlarmActivityRunner(activity: activity, onToken: { [weak self] hex in
            guard let self, self.followedAlarmID == alarmID else { return }
            self.activityToken = hex
            self.sendToken()
        }, onEnded: { [weak self] in
            guard let self, self.followedAlarmID == alarmID else { return }
            self.detach()
        })
    }
    #endif

    private func endCurrent(immediately: Bool) {
        #if canImport(ActivityKit)
        if #available(iOS 16.1, *), let runner = runner as? AlarmActivityRunner {
            Task { await runner.end(nil, dismissAfter: immediately ? 0 : Self.endedLinger) }
        }
        #endif
        detach()
    }

    /// Forgets the activity locally and tells the server to stop pushing.
    private func detach() {
        let hadToken = activityToken != nil
        runner = nil
        followedAlarmID = nil
        activityToken = nil
        if hadToken { clearServerToken() }
    }

    private func sendToken() {
        let session = SentinelSession.shared
        guard let token = activityToken, let alarmID = followedAlarmID, session.isSignedIn,
              let api = session.api, let apns = session.apnsToken else { return }
        Task { try? await api.registerActivityToken(apnsToken: apns, alarmID: alarmID, activityToken: token) }
    }

    private func clearServerToken() {
        let session = SentinelSession.shared
        guard session.isSignedIn, let api = session.api, let apns = session.apnsToken else { return }
        Task { try? await api.unregisterActivityToken(apnsToken: apns) }
    }
}

#if canImport(ActivityKit)
/// Owns one running activity: watches its push token and state, and wraps
/// the ActivityKit calls whose signatures changed in iOS 16.2.
@available(iOS 16.1, *)
@MainActor
final class AlarmActivityRunner {
    let activity: Activity<AlarmActivityAttributes>
    private var tokenTask: Task<Void, Never>?
    private var stateTask: Task<Void, Never>?

    init(activity: Activity<AlarmActivityAttributes>, onToken: @escaping @MainActor (String) -> Void,
         onEnded: @escaping @MainActor () -> Void) {
        self.activity = activity
        tokenTask = Task {
            for await data in activity.pushTokenUpdates {
                let hex = data.map { String(format: "%02x", $0) }.joined()
                onToken(hex)
            }
        }
        stateTask = Task {
            for await state in activity.activityStateUpdates where state == .ended || state == .dismissed {
                onEnded()
                break
            }
        }
    }

    deinit {
        tokenTask?.cancel()
        stateTask?.cancel()
    }

    static func request(attributes: AlarmActivityAttributes, content: AlarmActivityContent) throws -> Activity<AlarmActivityAttributes> {
        if #available(iOS 16.2, *) {
            return try Activity.request(attributes: attributes, content: ActivityContent(state: content, staleDate: nil),
                                        pushType: .token)
        }
        return try Activity.request(attributes: attributes, contentState: content, pushType: .token)
    }

    func update(_ content: AlarmActivityContent) async {
        if #available(iOS 16.2, *) {
            await activity.update(ActivityContent(state: content, staleDate: nil))
        } else {
            await activity.update(using: content)
        }
    }

    /// Ends the activity, leaving it on the Lock Screen for `dismissAfter`
    /// seconds (0 = remove now). nil content keeps the last state.
    func end(_ content: AlarmActivityContent?, dismissAfter: TimeInterval) async {
        let policy: ActivityUIDismissalPolicy = dismissAfter <= 0 ? .immediate : .after(Date().addingTimeInterval(dismissAfter))
        if #available(iOS 16.2, *) {
            await activity.end(content.map { ActivityContent(state: $0, staleDate: nil) }, dismissalPolicy: policy)
        } else {
            await activity.end(using: content, dismissalPolicy: policy)
        }
    }
}
#endif
