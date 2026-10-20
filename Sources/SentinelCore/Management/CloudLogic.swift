import Foundation

// Pure logic behind the Management window's Basics > Sentinel Cloud pane
// (wave 12 stream E; the Admin page is stream C's `CloudSection.tsx`):
// which phase the pane is in, the link-code countdown, the display rows for
// a linked server, byte and quota formatting, and the state -> label/tone
// decisions. The pane only lays these out. User-facing words follow the
// contract: "Sentinel Cloud", "remote access", "offsite backup".

/// How a value should be coloured; the pane maps it to `SentinelTheme`.
public enum CloudTone: String, Equatable, Sendable {
    case neutral, good, working, warning, bad
}

/// A short state text and its tone.
public struct CloudLabel: Equatable, Sendable {
    public var text: String
    public var tone: CloudTone

    public init(_ text: String, _ tone: CloudTone) {
        self.text = text
        self.tone = tone
    }
}

/// One label/value line of the linked view.
public struct CloudRow: Hashable, Identifiable, Sendable {
    public enum Section: String, Sendable {
        case subscription, remote, push, backup
    }

    /// Stable key ("plan", "remoteAddress", …).
    public var id: String
    public var section: Section
    public var label: String
    public var value: String
    public var tone: CloudTone
    /// What a copy button next to the value puts on the pasteboard; nil = no button.
    public var copyValue: String?

    public init(id: String, section: Section, label: String, value: String, tone: CloudTone = .neutral, copyValue: String? = nil) {
        self.id = id
        self.section = section
        self.label = label
        self.value = value
        self.tone = tone
        self.copyValue = copyValue
    }
}

/// What the pane shows.
public enum CloudPhase: Equatable, Sendable {
    /// Explanation + "Link to Sentinel Cloud".
    case notLinked
    /// A code waits for approval on sentvms.com.
    case linking(V2.CloudLink)
    /// The code ran out (or the server dropped it) before it was approved.
    case expired
    case linked
}

public enum CloudLogic {
    /// Where the subscription is bought and managed (never from the iPhone app).
    public static let manageSubscriptionURL = URL(string: "https://sentvms.com/cloud")!
    /// `GET /api/v2/cloud` cadence while a code is pending (the control plane's `interval`).
    public static let linkPollSeconds: UInt64 = 5
    /// Status refresh while the pane is on screen otherwise.
    public static let statusRefreshSeconds: UInt64 = 60
    /// `grace` keeps features on this long after `periodEnd`.
    public static let graceDays = 7

    // MARK: Copy

    public static let intro = "Sentinel Cloud is an optional subscription for this server. Sentinel itself stays free: every camera, detector and app keeps working on your network without it."
    public static let benefits = [
        "Remote access at a stable address, with nothing to install and no port forwarding.",
        "Push notifications to the Sentinel iPhone app without your own Apple push key.",
        "Offsite backup of alarm snapshots and clips, viewable on your iPhone even when this server is off."
    ]
    public static let linkSteps = "Linking shows a code. Approve it on sentvms.com while signed in to your Sentinel Cloud account; this server links as soon as you do."
    public static let unlinkWarning = "Remote access, cloud push and offsite backup stop right away. Backed-up alarms stay in Sentinel Cloud for 7 days. You can link again at any time."

    // MARK: Phase

    /// `awaitingLink` is the pane's memory that it saw (or started) a pending
    /// code; when the server then answers with no code and not linked, the
    /// code expired.
    public static func phase(_ status: V2.CloudStatus, now: Date, awaitingLink: Bool) -> CloudPhase {
        if status.linked { return .linked }
        if let link = status.link { return isExpired(link, now: now) ? .expired : .linking(link) }
        return awaitingLink ? .expired : .notLinked
    }

    /// Whether the pane should poll `GET /api/v2/cloud` every `linkPollSeconds`.
    public static func shouldPoll(_ phase: CloudPhase) -> Bool {
        if case .linking = phase { return true }
        return false
    }

    // MARK: Link countdown

    /// Whole seconds left on the code (rounded up), 0 once expired; nil when
    /// the server sent no expiry.
    public static func remainingSeconds(until expiresAt: Date?, now: Date) -> Int? {
        guard let expiresAt else { return nil }
        return max(0, Int(expiresAt.timeIntervalSince(now).rounded(.up)))
    }

    /// "9:41"; "" when the expiry is unknown.
    public static func countdown(until expiresAt: Date?, now: Date) -> String {
        guard let left = remainingSeconds(until: expiresAt, now: now) else { return "" }
        return String(format: "%d:%02d", left / 60, left % 60)
    }

    /// "Code expires in 9:41" / "Code expired".
    public static func countdownText(_ link: V2.CloudLink, now: Date) -> String {
        guard let left = remainingSeconds(until: link.expiresAt, now: now) else { return "Waiting for approval" }
        return left == 0 ? "Code expired" : "Code expires in \(countdown(until: link.expiresAt, now: now))"
    }

    /// The page that approves the code: the server's `verifyURL` when it is a
    /// web address (nothing else is handed to the system to open), else
    /// sentvms.com with the code filled in.
    public static func verifyURL(_ link: V2.CloudLink) -> URL {
        if let url = URL(string: link.verifyURL.trimmingCharacters(in: .whitespaces)),
           let scheme = url.scheme?.lowercased(), scheme == "https" || scheme == "http", url.host?.isEmpty == false {
            return url
        }
        var components = URLComponents(url: manageSubscriptionURL, resolvingAgainstBaseURL: false)!
        components.queryItems = [URLQueryItem(name: "code", value: link.userCode)]
        return components.url ?? manageSubscriptionURL
    }

    /// An unknown expiry never counts as expired (the server's word decides).
    public static func isExpired(_ link: V2.CloudLink, now: Date) -> Bool {
        remainingSeconds(until: link.expiresAt, now: now) == 0
    }

    // MARK: Formatting

    /// 1024-based, like the rest of the app ("10 GB" for a 10 GiB quota,
    /// "1.5 MB", "121 KB"); a whole value drops its ".0".
    public static func bytes(_ n: Int64) -> String {
        guard n >= 1024 else { return "\(max(0, n)) B" }
        let units = ["KB", "MB", "GB", "TB", "PB"]
        var v = Double(n) / 1024
        var i = 0
        while v >= 1024, i < units.count - 1 { v /= 1024; i += 1 }
        if v >= 100 { return "\(Int(v.rounded())) \(units[i])" }
        let one = (v * 10).rounded() / 10
        if one >= 100 { return "100 \(units[i])" }
        return one == one.rounded() ? "\(Int(one)) \(units[i])" : String(format: "%.1f %@", one, units[i])
    }

    /// Share of the quota used, 0…1 (0 when there is no quota).
    public static func usageFraction(used: Int64, quota: Int64) -> Double {
        guard quota > 0 else { return 0 }
        return min(1, max(0, Double(used) / Double(quota)))
    }

    /// "1.2 GB of 10 GB · 12 files"; "1.2 GB used · 12 files" without a quota.
    public static func usageText(used: Int64, quota: Int64, objects: Int) -> String {
        let amount = quota > 0 ? "\(bytes(used)) of \(bytes(quota))" : "\(bytes(used)) used"
        return "\(amount) · \(objects) file\(objects == 1 ? "" : "s")"
    }

    /// Full (the server deletes the oldest backups) is bad; 90 % is a warning.
    public static func usageTone(used: Int64, quota: Int64) -> CloudTone {
        guard quota > 0 else { return .neutral }
        let f = Double(used) / Double(quota)
        if f >= 1 { return .bad }
        if f >= 0.9 { return .warning }
        return .good
    }

    /// "30 days"; nil without a retention.
    public static func retentionText(days: Int) -> String? {
        guard days > 0 else { return nil }
        return days == 1 ? "1 day" : "\(days) days"
    }

    /// "Just now", "5 min ago", "3 h ago", else the date.
    public static func lastUploadText(_ date: Date?, now: Date, format: (Date) -> String = defaultDate) -> String {
        guard let date else { return "Nothing uploaded yet" }
        let age = now.timeIntervalSince(date)
        if age < 60 { return "Just now" }
        if age < 3600 { return "\(Int(age / 60)) min ago" }
        if age < 86_400 { return "\(Int(age / 3600)) h ago" }
        return format(date)
    }

    public static func defaultDate(_ date: Date) -> String {
        let f = DateFormatter()
        f.dateStyle = .medium
        f.timeStyle = .none
        return f.string(from: date)
    }

    // MARK: State labels

    public static func planLabel(_ plan: V2.CloudPlan) -> String {
        switch plan {
        case .free: return "Free"
        case .cloud: return "Sentinel Cloud"
        case .business: return "Sentinel Cloud Business"
        default: return plan.rawValue.isEmpty ? "Unknown" : plan.rawValue.capitalized
        }
    }

    public static func statusLabel(_ status: V2.CloudPlanStatus) -> CloudLabel {
        switch status {
        case .active: return CloudLabel("Active", .good)
        case .grace: return CloudLabel("Payment past due", .warning)
        case .canceled: return CloudLabel("Canceled", .bad)
        case .inactive: return CloudLabel("Not active", .bad)
        default: return CloudLabel(status.rawValue.isEmpty ? "Unknown" : status.rawValue.capitalized, .neutral)
        }
    }

    /// The period line, or nil on the free plan (no `periodEnd`).
    public static func periodText(_ s: V2.CloudStatus, format: (Date) -> String = defaultDate) -> String? {
        guard let end = s.periodEnd else { return nil }
        switch s.status {
        case .active: return "Current period ends \(format(end))"
        case .grace:
            let until = end.addingTimeInterval(TimeInterval(graceDays * 86_400))
            return "Features stay on until \(format(until)); update your payment to keep them"
        case .canceled, .inactive: return "Ended \(format(end))"
        default: return "Period ends \(format(end))"
        }
    }

    public static func remoteLabel(_ remote: V2.CloudRemote) -> CloudLabel {
        guard remote.enabled else { return CloudLabel("Off", .neutral) }
        switch remote.state {
        case .off: return CloudLabel("Off", .neutral)
        case .connecting: return CloudLabel("Connecting…", .working)
        case .connected: return CloudLabel("Connected", .good)
        case .retrying: return CloudLabel("Reconnecting…", .warning)
        case .inactive: return CloudLabel("Paused: subscription not active", .bad)
        default: return CloudLabel(remote.state.rawValue.isEmpty ? "Unknown" : remote.state.rawValue.capitalized, .neutral)
        }
    }

    public static func pushLabel(_ state: V2.CloudPushState) -> CloudLabel {
        switch state {
        case .off: return CloudLabel("Off", .neutral)
        case .cloud: return CloudLabel("Through Sentinel Cloud", .good)
        case .direct: return CloudLabel("Direct, with your own APNs key", .good)
        default: return CloudLabel(state.rawValue.isEmpty ? "Unknown" : state.rawValue.capitalized, .neutral)
        }
    }

    /// Why a switched-on feature isn't working, or nil when the plan includes it.
    public static func featureNote(_ s: V2.CloudStatus, _ feature: V2.CloudFeature) -> String? {
        guard s.linked, s.has(feature) == false else { return nil }
        switch s.status {
        case .active, .grace: return "Not included in your plan."
        default: return "Paused until your Sentinel Cloud subscription is active again."
        }
    }

    // MARK: Rows

    /// The linked view's label/value lines, in display order. Controls
    /// (toggles, buttons) are the pane's; these are the facts beside them.
    public static func rows(_ s: V2.CloudStatus, now: Date, format: (Date) -> String = defaultDate) -> [CloudRow] {
        var rows: [CloudRow] = []
        // Subscription
        rows.append(CloudRow(id: "plan", section: .subscription, label: "Plan", value: planLabel(s.plan)))
        let status = statusLabel(s.status)
        rows.append(CloudRow(id: "status", section: .subscription, label: "Status", value: status.text, tone: status.tone))
        if let period = periodText(s, format: format) {
            rows.append(CloudRow(id: "period", section: .subscription, label: "Period", value: period,
                                 tone: s.status == .grace ? .warning : .neutral))
        }
        if s.accountEmailMasked.isEmpty == false {
            rows.append(CloudRow(id: "account", section: .subscription, label: "Account", value: s.accountEmailMasked))
        }
        if let server = serverText(s) {
            rows.append(CloudRow(id: "server", section: .subscription, label: "This server", value: server))
        }
        // Remote access
        let remote = remoteLabel(s.remote)
        rows.append(CloudRow(id: "remoteState", section: .remote, label: "Connection", value: remote.text, tone: remote.tone))
        let url = s.remote.url.trimmingCharacters(in: .whitespaces)
        rows.append(CloudRow(id: "remoteAddress", section: .remote, label: "Address",
                             value: url.isEmpty ? "Not assigned yet" : url, copyValue: url.isEmpty ? nil : url))
        if s.remote.error.isEmpty == false {
            rows.append(CloudRow(id: "remoteError", section: .remote, label: "Problem", value: s.remote.error, tone: .bad))
        }
        // Push
        let push = pushLabel(s.push.state)
        rows.append(CloudRow(id: "push", section: .push, label: "Notifications", value: push.text, tone: push.tone))
        // Offsite backup
        rows.append(CloudRow(id: "backupUsage", section: .backup, label: "Used",
                             value: usageText(used: s.backup.bytesUsed, quota: s.quotas.backupBytes, objects: s.backup.objects),
                             tone: usageTone(used: s.backup.bytesUsed, quota: s.quotas.backupBytes)))
        if let kept = retentionText(days: s.quotas.backupDays) {
            rows.append(CloudRow(id: "backupRetention", section: .backup, label: "Kept for", value: kept))
        }
        rows.append(CloudRow(id: "backupLastUpload", section: .backup, label: "Last upload",
                             value: lastUploadText(s.backup.lastUploadAt, now: now, format: format)))
        if s.backup.error.isEmpty == false {
            rows.append(CloudRow(id: "backupError", section: .backup, label: "Problem", value: s.backup.error, tone: .bad))
        }
        return rows
    }

    /// "Office Mac (k7f3q9x2ab)"; whichever half exists; nil for neither.
    public static func serverText(_ s: V2.CloudStatus) -> String? {
        switch (s.serverName.isEmpty, s.serverId.isEmpty) {
        case (false, false): return "\(s.serverName) (\(s.serverId))"
        case (false, true): return s.serverName
        case (true, false): return s.serverId
        case (true, true): return nil
        }
    }

    // MARK: Errors

    /// The pane's error line. A 404 means the signed-in server has no cloud
    /// routes: a Recording Server, or a Management Server older than wave 12.
    public static func errorText(_ error: Error) -> String {
        if let api = error as? SentinelAPIError, api.isNotFound {
            return "This server doesn't offer Sentinel Cloud. Sentinel Cloud is set up on the Management Server; update Sentinel there if it's older."
        }
        return (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }
}
