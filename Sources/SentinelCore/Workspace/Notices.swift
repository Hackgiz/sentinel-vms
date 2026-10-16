import Foundation

/// A tiny notice bus for the banner strip: the data layer and the
/// workspaces say what the operator should hear about (recording server
/// offline / back, a camera dropping, the AI daily limit, an export
/// finished) and the strip shows one line each until dismissed. Ports the
/// web client's `notices.ts`: keyed replacement, at most four lines,
/// info/success fade after 8 s, the same notice within 30 s is dropped.

public enum NoticeKind: String, Equatable, Sendable {
    case info, success, warn, error
}

public struct NoticeInput: Equatable, Sendable {
    public var kind: NoticeKind
    public var text: String
    /// A notice with the same key replaces the earlier one (offline -> back).
    public var key: String?

    public init(kind: NoticeKind = .info, text: String, key: String? = nil) {
        self.kind = kind
        self.text = text
        self.key = key
    }
}

public struct Notice: Identifiable, Equatable, Sendable {
    public var id: Int
    public var kind: NoticeKind
    public var text: String
    public var key: String?
    public var at: Date

    /// Success and info lines fade on their own; warnings and errors stay.
    public var autoDismisses: Bool { kind == .success || kind == .info }
}

/// The list rules, separate from the bus so they can be tested directly.
public enum NoticeList {
    /// At most this many lines show at once; older ones drop off.
    public static let maxNotices = 4
    public static let autoDismissAfter: TimeInterval = 8
    public static let dedupeWindow: TimeInterval = 30

    /// Adds a notice: a keyed one replaces its predecessor; the list is capped (newest last).
    public static func add(_ list: [Notice], _ notice: Notice) -> [Notice] {
        var next = notice.key.map { key in list.filter { $0.key != key } } ?? list
        next.append(notice)
        return Array(next.suffix(maxNotices))
    }

    public static func dismiss(_ list: [Notice], id: Int) -> [Notice] {
        list.filter { $0.id != id }
    }

    /// Drops auto-dismissing notices that are older than `autoDismissAfter`.
    public static func expire(_ list: [Notice], now: Date) -> [Notice] {
        list.filter { $0.autoDismisses == false || now.timeIntervalSince($0.at) < autoDismissAfter }
    }
}

/// What the live stream's transitions say.
public enum NoticeRules {
    /// A recording server changing state, or nil when nothing changed or the
    /// old state is unknown (the first report on connect).
    public static func recorder(name: String, wasOnline: Bool?, online: Bool, id: String) -> NoticeInput? {
        guard let wasOnline, wasOnline != online else { return nil }
        return online
            ? NoticeInput(kind: .success, text: "Recording server \(name) is back online.", key: "recorder:\(id)")
            : NoticeInput(kind: .warn, text: "Recording server \(name) is offline. Its cameras are unavailable until it reconnects.", key: "recorder:\(id)")
    }

    /// A camera going offline (warn) or coming back (info); nil when unchanged or unknown.
    public static func camera(name: String, wasOnline: Bool?, online: Bool, id: String) -> NoticeInput? {
        guard let wasOnline, wasOnline != online else { return nil }
        return online
            ? NoticeInput(kind: .info, text: "Camera \(name) is back online", key: "camera-online:\(id)")
            : NoticeInput(kind: .warn, text: "Camera \(name) went offline", key: "camera-offline:\(id)")
    }

    /// Setup mode edits saved views; pressed anywhere else, say so (same
    /// copy as the web client's setup hint).
    public static let setupHint = NoticeInput(
        kind: .info,
        text: "Setup mode edits saved views. The Live grid is always editable — drag cameras from the tree, then “Save current as view…”. Open a saved view in its own tab to edit it.",
        key: "setup-hint")
}

/// The bus the banner strip shows. One shared instance, so any workspace
/// (or a stream's view) can post without wiring: `NoticeCenter.shared.post(...)`.
@MainActor
public final class NoticeCenter: ObservableObject {
    public static let shared = NoticeCenter()

    @Published public private(set) var notices: [Notice] = []

    private let now: () -> Date
    private let schedulesExpiry: Bool
    private var nextID = 1
    private var lastSeen: [String: Date] = [:]
    private var timers: [Int: Task<Void, Never>] = [:]

    public init(now: @escaping () -> Date = Date.init, schedulesExpiry: Bool = true) {
        self.now = now
        self.schedulesExpiry = schedulesExpiry
    }

    /// Posts a notice; nil (nothing to say) is ignored. Returns whether it
    /// was delivered (false when the same key+text was seen within 30 s).
    @discardableResult
    public func post(_ input: NoticeInput?) -> Bool {
        guard let input else { return false }
        let time = now()
        let seenKey = "\(input.key ?? "")|\(input.text)"
        if let previous = lastSeen[seenKey], time.timeIntervalSince(previous) < NoticeList.dedupeWindow { return false }
        lastSeen[seenKey] = time
        if lastSeen.count > 200 { lastSeen = lastSeen.filter { time.timeIntervalSince($0.value) < NoticeList.dedupeWindow } }
        let notice = Notice(id: nextID, kind: input.kind, text: input.text, key: input.key, at: time)
        nextID += 1
        let before = Set(notices.map(\.id))
        notices = NoticeList.add(notices, notice)
        for id in before.subtracting(notices.map(\.id)) { cancelTimer(id) }
        if schedulesExpiry, notice.autoDismisses { startTimer(for: notice) }
        return true
    }

    /// One-shot notice with nothing to replace (the text is its own identity).
    @discardableResult
    public func post(_ kind: NoticeKind, _ text: String, key: String? = nil) -> Bool {
        post(NoticeInput(kind: kind, text: text, key: key))
    }

    public func dismiss(id: Int) {
        cancelTimer(id)
        notices = NoticeList.dismiss(notices, id: id)
    }

    /// Drops fading notices older than 8 s (the timers do this; exposed for tests).
    public func expire() {
        let kept = NoticeList.expire(notices, now: now())
        for id in Set(notices.map(\.id)).subtracting(kept.map(\.id)) { cancelTimer(id) }
        notices = kept
    }

    /// Forgets everything (site switch, sign-out).
    public func clear() {
        timers.values.forEach { $0.cancel() }
        timers = [:]
        notices = []
        lastSeen = [:]
    }

    private func startTimer(for notice: Notice) {
        let id = notice.id
        timers[id] = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(NoticeList.autoDismissAfter * 1_000_000_000))
            guard Task.isCancelled == false else { return }
            self?.dismiss(id: id)
        }
    }

    private func cancelTimer(_ id: Int) {
        timers[id]?.cancel()
        timers[id] = nil
    }
}
