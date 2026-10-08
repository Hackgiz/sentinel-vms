import Foundation
import CryptoKit

/// Protected storage for locked evidence. Recordings live under the Recordings
/// root, where MediaMTX's `recordDeleteAfter` and Sentinel's own retention /
/// idle-motion cleanup delete them on a schedule. Locking a clip moves a
/// reference to its bytes here, outside every retention path, so the footage
/// survives cleanup for as long as the case needs it.
public enum EvidenceVault {
    public enum VaultError: LocalizedError {
        case sourceMissing(String)

        public var errorDescription: String? {
            switch self {
            case .sourceMissing(let path): return "The source recording is missing: \(path)"
            }
        }
    }

    public static var rootURL: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HandoffGridSentinel/Evidence", isDirectory: true)
    }

    public static func contains(_ path: String) -> Bool {
        URL(fileURLWithPath: path).standardizedFileURL.path.hasPrefix(rootURL.standardizedFileURL.path + "/")
    }

    /// Places `source` into the vault under the case folder and returns the
    /// protected URL. A finished segment is hard-linked (instant, no extra disk
    /// space; retention deleting the original leaves this link intact). A
    /// segment still being written is copied instead, so the locked bytes are a
    /// fixed snapshot rather than a file that keeps growing under its hash.
    public static func preserve(_ source: URL, caseID: String) throws -> URL {
        let fm = FileManager.default
        guard fm.fileExists(atPath: source.path) else {
            throw VaultError.sourceMissing(source.path)
        }

        let caseDir = rootURL.appendingPathComponent(caseID, isDirectory: true)
        try fm.createDirectory(at: caseDir, withIntermediateDirectories: true)
        let destination = caseDir.appendingPathComponent(source.lastPathComponent)
        if fm.fileExists(atPath: destination.path) {
            return destination
        }

        let modified = (try? source.resourceValues(forKeys: [.contentModificationDateKey]))?.contentModificationDate ?? .distantPast
        let stillRecording = Date().timeIntervalSince(modified) < 60
        if stillRecording {
            try fm.copyItem(at: source, to: destination)
        } else {
            do {
                try fm.linkItem(at: source, to: destination)
            } catch {
                // Different volume (external recordings drive) — fall back to a copy.
                try fm.copyItem(at: source, to: destination)
            }
        }
        // Read-only, so an accidental edit can't silently break the hash.
        try? fm.setAttributes([.posixPermissions: 0o444], ofItemAtPath: destination.path)
        return destination
    }

    /// Streaming SHA-256 in 1 MB chunks — evidence segments can be hundreds of
    /// MB, so never load the whole file. Call off the main actor.
    public static func sha256(of url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        while let chunk = try handle.read(upToCount: 1024 * 1024), chunk.isEmpty == false {
            hasher.update(data: chunk)
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }
}
