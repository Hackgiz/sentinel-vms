import Foundation

/// Looks for a newer Sentinel for Mac on sentvms.com once a day. The request is
/// a plain GET carrying only a User-Agent with the app version; nothing about
/// cameras, footage or the user is sent. Failures are silent so the app keeps
/// working offline.
@MainActor
public final class UpdateChecker: ObservableObject {
    public static let endpoint = URL(string: "https://dl.sentvms.com/latest")!
    public static let downloadPage = URL(string: "https://sentvms.com/?get=mac")!

    @Published public private(set) var latestVersion: String?
    @Published public private(set) var notes: String = ""
    @Published public private(set) var downloadURL: URL = UpdateChecker.downloadPage
    @Published public private(set) var lastChecked: Date?
    @Published public private(set) var isChecking = false
    @Published public private(set) var lastError: String?
    @Published public var automaticChecks: Bool {
        didSet { defaults.set(automaticChecks, forKey: Keys.automatic) }
    }
    @Published public private(set) var dismissedVersion: String?

    public let currentVersion: String
    private let session: URLSession
    private let defaults: UserDefaults
    private var loop: Task<Void, Never>?

    private enum Keys {
        static let automatic = "sentinel.updates.automatic"
        static let dismissed = "sentinel.updates.dismissedVersion"
    }

    public init(currentVersion: String = DiagnosticsCollector.appVersion,
                session: URLSession = .shared,
                defaults: UserDefaults = .standard) {
        self.currentVersion = currentVersion
        self.session = session
        self.defaults = defaults
        self.automaticChecks = defaults.object(forKey: Keys.automatic) as? Bool ?? true
        self.dismissedVersion = defaults.string(forKey: Keys.dismissed)
    }

    /// True when sentvms.com lists a higher version than this app.
    public var updateAvailable: Bool {
        guard let latestVersion else { return false }
        return SentinelVersion.isNewer(latestVersion, than: currentVersion)
    }

    /// Show the sidebar notice unless the user hid it for this version.
    public var shouldShowNotice: Bool {
        updateAvailable && dismissedVersion != latestVersion
    }

    public func dismissNotice() {
        dismissedVersion = latestVersion
        defaults.set(latestVersion, forKey: Keys.dismissed)
    }

    /// First check a minute after launch, then daily, while automatic checks are on.
    public func startAutomaticChecks() {
        guard loop == nil else { return }
        loop = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 60 * 1_000_000_000)
            while !Task.isCancelled {
                guard let self else { return }
                if self.automaticChecks {
                    await self.checkNow()
                }
                try? await Task.sleep(nanoseconds: 24 * 60 * 60 * 1_000_000_000)
            }
        }
    }

    public func checkNow() async {
        guard !isChecking else { return }
        isChecking = true
        defer { isChecking = false }
        var request = URLRequest(url: Self.endpoint, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 20)
        request.setValue("SentinelMac/\(currentVersion)", forHTTPHeaderField: "User-Agent")
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
                throw URLError(.badServerResponse)
            }
            let feed = try JSONDecoder().decode(LatestFeed.self, from: data)
            let version = feed.mac.version.trimmingCharacters(in: .whitespaces)
            latestVersion = version.isEmpty ? nil : version
            notes = feed.mac.notes ?? ""
            if let raw = feed.mac.url, let url = URL(string: raw), url.scheme == "https" {
                downloadURL = url
            }
            lastError = nil
            if updateAvailable {
                SentinelLog.shared.info("Sentinel VMS \(version) is available (this Mac has \(currentVersion))", category: "updates")
            }
        } catch {
            lastError = "Couldn't reach sentvms.com to check for updates."
        }
        lastChecked = Date()
    }

    private struct LatestFeed: Decodable {
        struct Release: Decodable {
            let version: String
            let notes: String?
            let url: String?
        }
        let mac: Release
    }
}

public enum SentinelVersion {
    /// Compares dotted version numbers ("1.1.10" > "1.1.9"). Anything after a
    /// "-" or "+" is ignored; unparseable versions never count as newer.
    public static func isNewer(_ latest: String, than current: String) -> Bool {
        guard let l = parse(latest), let c = parse(current) else { return false }
        for i in 0..<max(l.count, c.count) {
            let a = i < l.count ? l[i] : 0
            let b = i < c.count ? c[i] : 0
            if a != b { return a > b }
        }
        return false
    }

    static func parse(_ raw: String) -> [Int]? {
        var v = raw.trimmingCharacters(in: .whitespaces)
        if v.hasPrefix("v") { v.removeFirst() }
        if let cut = v.firstIndex(where: { $0 == "-" || $0 == "+" || $0 == " " }) {
            v = String(v[..<cut])
        }
        guard !v.isEmpty else { return nil }
        var parts: [Int] = []
        for piece in v.split(separator: ".", omittingEmptySubsequences: false) {
            guard let n = Int(piece), n >= 0 else { return nil }
            parts.append(n)
        }
        return parts
    }
}
