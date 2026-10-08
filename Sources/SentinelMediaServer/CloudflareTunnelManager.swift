import Foundation
import SwiftUI

/// Manages a `cloudflared` quick-tunnel subprocess that exposes the local
/// Sentinel HTTP API (port 8090) at a public `https://xxx.trycloudflare.com`
/// URL — no account, no port forwarding, no DNS setup required.
///
/// The tunnel URL changes on every start unless the user runs a named tunnel
/// with a free Cloudflare account. For the home / small-business use-case,
/// the URL-in-QR-code pairing flow already handles this: generate a new QR
/// after starting the tunnel and the iOS app gets the fresh URL automatically.
@MainActor
public final class CloudflareTunnelManager: ObservableObject {
    @Published public private(set) var isRunning = false
    @Published public private(set) var tunnelURL: String?
    @Published public private(set) var statusMessage = "Not started"
    @Published public private(set) var startError: String?

    public var isAvailable: Bool { binaryPath != nil }

    private var process: Process?
    private var outputPipe: Pipe?
    private let localPort: Int
    private let supportDir: URL

    public init(localPort: Int = 8090) {
        self.localPort = localPort
        self.supportDir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel", isDirectory: true)
    }

    // MARK: - Lifecycle

    /// A stable, account-backed named tunnel configured for this install. When
    /// present, the public URL is a FIXED hostname (e.g. https://link.sentvms.com)
    /// that survives Mac restarts — unlike the ephemeral *.trycloudflare.com quick
    /// tunnel. Set up out-of-band (cloudflared login + tunnel create + route dns)
    /// and stored per-install, so other downloads with no config fall back to the
    /// quick tunnel automatically.
    private struct NamedTunnelConfig: Decodable {
        let hostname: String
        let tunnelID: String
        let credentialsFile: String
    }

    private func loadNamedTunnelConfig() -> NamedTunnelConfig? {
        let url = supportDir.appendingPathComponent("named-tunnel.json")
        guard let data = try? Data(contentsOf: url),
              let cfg = try? JSONDecoder().decode(NamedTunnelConfig.self, from: data),
              cfg.hostname.isEmpty == false, cfg.tunnelID.isEmpty == false else { return nil }
        // The run only works if the credentials file is actually present.
        let creds = supportDir.appendingPathComponent(cfg.credentialsFile)
        guard FileManager.default.fileExists(atPath: creds.path) else { return nil }
        return cfg
    }

    public func start() {
        guard let binary = binaryPath else {
            startError = "cloudflared not found. Place the binary in ~/Library/Application Support/HandoffGridSentinel/ (or install via Homebrew)."
            statusMessage = "Not installed"
            return
        }
        stop()
        startError = nil
        tunnelURL = nil
        statusMessage = "Starting…"

        let named = loadNamedTunnelConfig()

        let pipe = Pipe()
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: binary)
        // --no-autoupdate prevents cloudflared from trying to update itself
        // (which would require write access to /usr/local/bin).
        if let named {
            let creds = supportDir.appendingPathComponent(named.credentialsFile).path
            proc.arguments = ["tunnel", "--no-autoupdate", "--cred-file", creds,
                              "run", "--url", "http://localhost:\(localPort)", named.tunnelID]
        } else {
            proc.arguments = ["tunnel", "--no-autoupdate", "--url", "http://localhost:\(localPort)"]
        }
        proc.standardOutput = pipe
        proc.standardError = pipe
        proc.terminationHandler = { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.isRunning = false
                self.process = nil
                self.outputPipe?.fileHandleForReading.readabilityHandler = nil
                self.outputPipe = nil
                if self.tunnelURL == nil {
                    self.statusMessage = "Stopped (no URL found)"
                } else {
                    self.statusMessage = "Tunnel stopped"
                    self.tunnelURL = nil
                }
            }
        }

        do {
            try proc.run()
        } catch {
            startError = error.localizedDescription
            statusMessage = "Launch failed"
            return
        }

        process = proc
        outputPipe = pipe
        isRunning = true
        // Always drain the pipe (so cloudflared doesn't block on a full buffer).
        // For the quick tunnel this also parses the ephemeral URL; the named
        // tunnel's fixed hostname is set directly below.
        startReadingOutput(pipe: pipe)
        if let named {
            tunnelURL = "https://\(named.hostname)"
            statusMessage = "Active"
        }
    }

    public func stop() {
        outputPipe?.fileHandleForReading.readabilityHandler = nil
        outputPipe = nil
        process?.interrupt()
        // Give cloudflared 2s to exit cleanly before SIGKILL.
        if let pid = process?.processIdentifier, process?.isRunning == true {
            let capturedPID = pid
            Task.detached {
                try? await Task.sleep(nanoseconds: 2_000_000_000)
                kill(capturedPID, SIGKILL)
            }
        }
        process = nil
        isRunning = false
        tunnelURL = nil
        statusMessage = "Not started"
        startError = nil
    }

    // MARK: - Output parsing

    private func startReadingOutput(pipe: Pipe) {
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            if let url = Self.parseTunnelURL(from: text) {
                Task { @MainActor [weak self] in
                    guard let self else { return }
                    self.tunnelURL = url
                    self.statusMessage = "Active"
                    self.startError = nil
                }
            }
        }
    }

    // cloudflared writes lines like:
    //   "Your quick Tunnel has been created! Visit it at ... https://abc-def.trycloudflare.com"
    // or older builds:
    //   "TRY ON YOUR BROWSER: https://abc-def.trycloudflare.com"
    nonisolated static func parseTunnelURL(from text: String) -> String? {
        // Greedy regex match for any trycloudflare.com HTTPS URL on the line.
        guard let range = text.range(of: #"https://[a-zA-Z0-9\-]+\.trycloudflare\.com"#,
                                     options: .regularExpression) else { return nil }
        return String(text[range])
    }

    // MARK: - Binary detection

    private var binaryPath: String? {
        var candidates: [String] = []
        // Bundled with the app (build-app.sh can copy it into Resources/).
        if let bundled = Bundle.main.url(forResource: "cloudflared", withExtension: nil) {
            candidates.append(bundled.path)
        }
        candidates.append(contentsOf: [
            supportDir.appendingPathComponent("cloudflared").path,
            "/opt/homebrew/bin/cloudflared",
            "/usr/local/bin/cloudflared"
        ])
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0) }
    }
}
