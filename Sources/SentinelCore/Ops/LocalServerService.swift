import Foundation

/// One Sentinel server process this Mac runs under launchd: the system daemon
/// Sentinel Operator installs (`com.handoffgrid.sentinel.server`), or a user
/// LaunchAgent whose program is a `sentinel` binary (a hand-built site with a
/// management server and recording servers). Read from the job's plist, so the
/// menu bar status asks the server this Mac actually runs instead of assuming
/// that whatever answers on port 8090 is it.
public struct LocalServerService: Equatable, Sendable {
    public enum Domain: Equatable, Sendable {
        case system // /Library/LaunchDaemons, needs an administrator to control
        case user   // ~/Library/LaunchAgents, controlled as the signed-in user
    }

    public var label: String
    public var plistPath: String
    public var domain: Domain
    /// `-role`: "all" (the default), "management" or "recording".
    public var role: String
    /// `-listen`, e.g. "127.0.0.1:18090" or ":8090".
    public var listen: String?
    public var logPath: String?

    public init(label: String, plistPath: String, domain: Domain, role: String, listen: String?, logPath: String?) {
        self.label = label
        self.plistPath = plistPath
        self.domain = domain
        self.role = role
        self.listen = listen
        self.logPath = logPath
    }

    /// Serves the API and the web dashboard (recording servers don't).
    public var servesAPI: Bool { role == "all" || role == "management" }

    /// Where to reach it from this Mac: the listen port on loopback when it
    /// listens on every interface.
    public var address: URL? {
        guard let listen, let colon = listen.lastIndex(of: ":") else { return nil }
        let port = listen[listen.index(after: colon)...]
        guard let number = Int(port), (1...65535).contains(number) else { return nil }
        var host = String(listen[..<colon])
        if ["", "0.0.0.0", "[::]", "::", "*"].contains(host) { host = "127.0.0.1" }
        return URL(string: "http://\(host):\(number)")
    }

    /// Builds a service from a launchd job dictionary, or nil when the job
    /// doesn't run a `sentinel` binary as a server.
    public static func from(job: [String: Any], plistPath: String, domain: Domain, defaultListen: String? = nil) -> LocalServerService? {
        guard let label = job["Label"] as? String,
              let args = job["ProgramArguments"] as? [String],
              let program = args.first,
              (program as NSString).lastPathComponent == "sentinel" else { return nil }
        // `sentinel service …` and friends are helpers, not a server.
        if let first = args.dropFirst().first, first.hasPrefix("-") == false { return nil }
        let log = (job["StandardErrorPath"] as? String) ?? (job["StandardOutPath"] as? String)
        return LocalServerService(
            label: label,
            plistPath: plistPath,
            domain: domain,
            role: flag("role", in: args) ?? "all",
            listen: flag("listen", in: args) ?? defaultListen,
            logPath: log
        )
    }

    /// The value of `-name value`, `--name value`, `-name=value` or `--name=value`.
    public static func flag(_ name: String, in args: [String]) -> String? {
        for (i, arg) in args.enumerated() {
            for prefix in ["-\(name)", "--\(name)"] {
                if arg == prefix, i + 1 < args.count { return args[i + 1] }
                if arg.hasPrefix(prefix + "=") { return String(arg.dropFirst(prefix.count + 1)) }
            }
        }
        return nil
    }

    /// Orders a site for starting: servers that answer the API first, so
    /// recording servers find their management server already up.
    public static func startOrder(_ services: [LocalServerService]) -> [LocalServerService] {
        services.sorted { a, b in
            if a.servesAPI != b.servesAPI { return a.servesAPI }
            return a.label < b.label
        }
    }
}
