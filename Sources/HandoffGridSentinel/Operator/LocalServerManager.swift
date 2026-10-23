import AppKit
import Foundation
import SentinelCore

/// The Sentinel Server hosted on this Mac: the `sentinel` archive bundled in
/// Sentinel Operator (`Contents/Resources/sentinel-server-macos.zip`, built by
/// `linux/scripts/build.sh` and copied in by `build-app.sh`), installed as a
/// launchd service by the archive's own `install.sh` (`/usr/local/sentinel`,
/// data in `/Library/Application Support/Sentinel`, runs as `_sentinel`).
@MainActor
final class LocalServerManager: ObservableObject {
    static let shared = LocalServerManager()

    nonisolated static let defaultPort = 8090
    nonisolated static let defaultAddress = "http://127.0.0.1:\(defaultPort)"
    nonisolated static let programPath = "/usr/local/sentinel/sentinel"
    nonisolated static let plistPath = "/Library/LaunchDaemons/com.handoffgrid.sentinel.server.plist"
    nonisolated static let serviceAccount = "_sentinel"

    struct Status: Equatable {
        var version: String
        var setupRequired: Bool
    }

    enum Phase: Equatable {
        case idle
        case working(String)
        case failed(String)
    }

    @Published private(set) var status: Status?
    @Published private(set) var phase: Phase = .idle
    @Published private(set) var lastChecked: Date?
    /// Something that is not our service answers on the port (a VM's port
    /// forward, Docker, a dev build). The card says so instead of "Running".
    @Published private(set) var otherServerOnPort = false

    /// True for addresses that point at this Mac.
    nonisolated static func isLocalAddress(_ url: URL) -> Bool {
        guard let host = url.host?.lowercased() else { return false }
        if ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host) { return true }
        let names = [ProcessInfo.processInfo.hostName.lowercased(), (Host.current().localizedName ?? "").lowercased() + ".local"]
        return names.contains(host)
    }

    var bundledArchive: URL? {
        Bundle.main.url(forResource: "sentinel-server-macos", withExtension: "zip")
    }

    var isInstalled: Bool {
        FileManager.default.fileExists(atPath: Self.programPath) && FileManager.default.fileExists(atPath: Self.plistPath)
    }

    var isRunning: Bool { status != nil }

    // MARK: Detect

    /// Asks `http://127.0.0.1:8090` whether a Sentinel Server answers. Only
    /// our installed service counts as running: anything else on the port
    /// (a Lima VM once answered here with version 2.0.0) is reported apart,
    /// so the card never offers to connect to or set up a stranger.
    @discardableResult
    func refresh() async -> Status? {
        let probed = await Self.probe(URL(string: Self.defaultAddress)!)
        if isInstalled {
            status = probed
            otherServerOnPort = false
        } else {
            status = nil
            otherServerOnPort = probed != nil
        }
        lastChecked = Date()
        return status
    }

    /// `GET /api/v1/status` — answers before anyone has signed in, and says
    /// whether the first administrator still has to be created.
    nonisolated static func probe(_ base: URL) async -> Status? {
        var request = URLRequest(url: base.appendingPathComponent("api/v1/status"))
        request.timeoutInterval = 3
        guard let (data, response) = try? await URLSession.shared.data(for: request),
              (response as? HTTPURLResponse)?.statusCode == 200,
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
        return Status(version: json["version"] as? String ?? "", setupRequired: json["setupRequired"] as? Bool ?? false)
    }

    // MARK: Install

    /// Unpacks the bundled archive and runs its `install.sh` as root (macOS
    /// asks for an administrator's password), then waits for the server.
    func install() async {
        guard let archive = bundledArchive else {
            phase = .failed("This copy of Sentinel Operator doesn't include the server. Download the macOS server from sentvms.com and run its install.sh.")
            return
        }
        phase = .working("Unpacking the server…")
        let staging = FileManager.default.temporaryDirectory.appendingPathComponent("sentinel-install-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: staging) }
        do {
            try FileManager.default.createDirectory(at: staging, withIntermediateDirectories: true)
            try await Self.run("/usr/bin/ditto", ["-x", "-k", archive.path, staging.path])
            guard let installer = Self.findInstaller(in: staging) else {
                phase = .failed("The bundled server archive has no install.sh.")
                return
            }
            phase = .working("Installing the service (macOS asks for an administrator's password)…")
            try await Self.runAsAdmin("/bin/bash \(Self.shellQuote(installer.path))")
            phase = .working("Waiting for the server to start…")
            for _ in 0..<60 {
                if await refresh() != nil { phase = .idle; return }
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
            phase = .failed("The service was installed but the server didn't answer on port \(Self.defaultPort). See /Library/Logs/Sentinel/sentinel.log.")
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    /// Removes the service and program (keeps recordings and data).
    func uninstall() async {
        phase = .working("Removing the service…")
        do {
            try await Self.runAsAdmin("/bin/bash /usr/local/sentinel/uninstall.sh")
            status = nil
            phase = .idle
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    /// Creates the first administrator on a new server (`POST /api/v1/setup`,
    /// which only works while the server has no users). API v2 has no
    /// equivalent yet.
    func createFirstAdmin(name: String, password: String) async throws {
        guard isInstalled else { throw SentinelAPIError.transport(message: "The Sentinel service isn't installed on this Mac.") }
        var request = URLRequest(url: URL(string: Self.defaultAddress)!.appendingPathComponent("api/v1/setup"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["name": name, "password": password])
        let (data, response) = try await URLSession.shared.data(for: request)
        let code = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(code) else { throw SentinelAPIError.from(status: code, body: data) }
        await refresh()
    }

    /// Lets the service account read old recordings in place (the importer
    /// points the local Recording Server at them): an inherited read ACL on
    /// the folders, plus "search" on the parents so the path can be reached.
    func grantRecordingsAccess(_ folders: [URL]) async throws {
        guard folders.isEmpty == false else { return }
        let account = Self.serviceAccount
        var commands: [String] = []
        var parents = Set<String>()
        for folder in folders {
            commands.append("/bin/chmod -R +a \(Self.shellQuote("\(account) allow list,search,read,readattr,readextattr,readsecurity,file_inherit,directory_inherit")) \(Self.shellQuote(folder.path))")
            var parent = folder.deletingLastPathComponent()
            while parent.path != "/" && parent.path.isEmpty == false {
                parents.insert(parent.path)
                parent = parent.deletingLastPathComponent()
            }
        }
        for parent in parents.sorted() {
            commands.append("/bin/chmod +a \(Self.shellQuote("\(account) allow search")) \(Self.shellQuote(parent)) || true")
        }
        try await Self.runAsAdmin(commands.joined(separator: " && "))
    }

    // MARK: Process helpers

    private nonisolated static func findInstaller(in folder: URL) -> URL? {
        let fm = FileManager.default
        let direct = folder.appendingPathComponent("install.sh")
        if fm.fileExists(atPath: direct.path) { return direct }
        let children = (try? fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)) ?? []
        // Prefer the universal build, then this Mac's architecture.
        #if arch(arm64)
        let order = ["universal", "arm64"]
        #else
        let order = ["universal", "amd64"]
        #endif
        let sorted = children.sorted { a, b in
            let ia = order.firstIndex { a.lastPathComponent.contains($0) } ?? 9
            let ib = order.firstIndex { b.lastPathComponent.contains($0) } ?? 9
            return ia < ib
        }
        return sorted.map { $0.appendingPathComponent("install.sh") }.first { fm.fileExists(atPath: $0.path) }
    }

    nonisolated static func shellQuote(_ text: String) -> String {
        "'" + text.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    private nonisolated static func run(_ tool: String, _ arguments: [String]) async throws {
        try await Task.detached {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: tool)
            process.arguments = arguments
            let errors = Pipe()
            process.standardError = errors
            process.standardOutput = FileHandle.nullDevice
            try process.run()
            process.waitUntilExit()
            guard process.terminationStatus == 0 else {
                let text = String(decoding: errors.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                throw SentinelAPIError.transport(message: text.isEmpty ? "\(tool) failed (\(process.terminationStatus))." : text)
            }
        }.value
    }

    /// `do shell script … with administrator privileges` (the standard macOS
    /// password prompt). Cancelling throws `CancellationError`.
    nonisolated static func runAsAdmin(_ command: String) async throws {
        let escaped = command.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
        let script = "do shell script \"\(escaped)\" with administrator privileges"
        try await Task.detached {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
            process.arguments = ["-e", script]
            let errors = Pipe()
            process.standardError = errors
            process.standardOutput = FileHandle.nullDevice
            try process.run()
            process.waitUntilExit()
            guard process.terminationStatus == 0 else {
                let text = String(decoding: errors.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                if text.contains("-128") { throw CancellationError() }
                throw SentinelAPIError.transport(message: text.trimmingCharacters(in: .whitespacesAndNewlines))
            }
        }.value
    }
}
