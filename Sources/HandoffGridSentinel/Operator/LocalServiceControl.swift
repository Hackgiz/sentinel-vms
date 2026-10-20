import AppKit
import Foundation
import SentinelCore

/// Finds the Sentinel server(s) this Mac runs under launchd and starts, stops
/// and restarts them for the menu bar status. The system daemon Sentinel
/// Operator installs needs an administrator (one password prompt per action);
/// user LaunchAgents running `sentinel` are controlled directly.
@MainActor
final class LocalServiceControl: ObservableObject {
    static let shared = LocalServiceControl()

    enum Busy: Equatable { case starting, stopping, restarting }

    @Published private(set) var services: [LocalServerService] = []
    @Published private(set) var loaded: Set<String> = []
    /// What the API-serving service answered, nil when it doesn't.
    @Published private(set) var status: LocalServerManager.Status?
    @Published private(set) var busy: Busy?
    @Published private(set) var lastError: String?

    var loadedCount: Int { services.filter { loaded.contains($0.label) }.count }
    var allLoaded: Bool { services.isEmpty == false && loadedCount == services.count }
    var apiService: LocalServerService? { services.first { $0.servesAPI } }
    var dashboardURL: URL? { status == nil ? nil : apiService?.address }
    var logURL: URL? {
        let path = apiService?.logPath ?? services.compactMap(\.logPath).first
        return path.map { URL(fileURLWithPath: $0) }
    }

    private nonisolated static var uid: uid_t { getuid() }

    // MARK: Discover

    func refresh() async {
        let found = await Task.detached { Self.discover() }.value
        let up = await Task.detached { Set(found.filter(Self.isLoaded).map(\.label)) }.value
        var answered: LocalServerManager.Status?
        if let api = found.first(where: { $0.servesAPI }), up.contains(api.label), let url = api.address {
            answered = await LocalServerManager.probe(url)
        }
        services = found
        loaded = up
        status = answered
    }

    nonisolated static func discover() -> [LocalServerService] {
        var out: [LocalServerService] = []
        if let job = NSDictionary(contentsOfFile: LocalServerManager.plistPath) as? [String: Any],
           let s = LocalServerService.from(job: job, plistPath: LocalServerManager.plistPath, domain: .system,
                                           defaultListen: ":\(LocalServerManager.defaultPort)") {
            out.append(LocalServerService(label: s.label, plistPath: s.plistPath, domain: .system, role: s.role,
                                          listen: s.listen, logPath: s.logPath ?? "/Library/Logs/Sentinel/sentinel.log"))
        }
        let agents = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/LaunchAgents")
        let files = (try? FileManager.default.contentsOfDirectory(at: agents, includingPropertiesForKeys: nil)) ?? []
        for file in files.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) where file.pathExtension == "plist" {
            guard let job = NSDictionary(contentsOf: file) as? [String: Any],
                  let s = LocalServerService.from(job: job, plistPath: file.path, domain: .user) else { continue }
            out.append(s)
        }
        return out
    }

    nonisolated static func target(_ s: LocalServerService) -> String {
        s.domain == .system ? "system/\(s.label)" : "gui/\(uid)/\(s.label)"
    }

    nonisolated static func domain(_ s: LocalServerService) -> String {
        s.domain == .system ? "system" : "gui/\(uid)"
    }

    nonisolated static func isLoaded(_ s: LocalServerService) -> Bool {
        launchctl(["print", target(s)]) == 0
    }

    // MARK: Control

    func start() async { await act(.starting) }
    func stop() async { await act(.stopping) }
    func restart() async { await act(.restarting) }

    private func act(_ what: Busy) async {
        guard busy == nil else { return }
        busy = what
        lastError = nil
        await refresh()
        let ordered = LocalServerService.startOrder(services)
        let plan = ordered.map { s -> [String] in
            let isUp = loaded.contains(s.label)
            switch what {
            case .starting: return isUp ? [] : ["bootstrap", Self.domain(s), s.plistPath]
            case .stopping: return isUp ? ["bootout", Self.target(s)] : []
            case .restarting: return isUp ? ["kickstart", "-k", Self.target(s)] : ["bootstrap", Self.domain(s), s.plistPath]
            }
        }
        // Stop recording servers before the management server.
        let steps = Array(zip(ordered, plan)).filter { !$0.1.isEmpty }
        let runOrder = what == .stopping ? steps.reversed() : steps

        let user = runOrder.filter { $0.0.domain == .user }.map(\.1)
        let system = runOrder.filter { $0.0.domain == .system }.map(\.1)
        let failures = await Task.detached { user.filter { Self.launchctl($0) != 0 }.count }.value
        if system.isEmpty == false {
            let command = system.map { (["/bin/launchctl"] + $0).map(LocalServerManager.shellQuote).joined(separator: " ") }
                .joined(separator: " ; ")
            do { try await LocalServerManager.runAsAdmin(command) } catch is CancellationError {
            } catch { lastError = error.localizedDescription }
        }
        if failures > 0 { lastError = "\(failures) service\(failures == 1 ? "" : "s") didn't respond to launchctl." }

        // The management server takes a few seconds to answer after a start.
        for _ in 0..<(what == .stopping ? 1 : 10) {
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            await refresh()
            if what == .stopping || (allLoaded && status != nil) { break }
        }
        busy = nil
    }

    @discardableResult
    nonisolated static func launchctl(_ args: [String]) -> Int32 {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        p.arguments = args
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return -1 }
        p.waitUntilExit()
        return p.terminationStatus
    }
}
