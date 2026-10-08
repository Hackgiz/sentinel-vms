import Foundation
import CryptoKit
import SwiftUI
import SentinelCore
import SentinelMediaServer

@MainActor
final class EvidenceExporter: ObservableObject {
    enum ExportResult: Equatable {
        case success(URL)
        case failure(String)
    }

    @Published var lastResult: ExportResult?
    @Published var isExporting: Bool = false
    @Published var progress: Double = 0.0

    /// Streaming SHA-256 of a file on disk. Reads in 1 MB chunks so the
    /// entire MP4 is never loaded into memory.
    static func sha256(of url: URL) async throws -> String {
        try await Task.detached(priority: .userInitiated) {
            let handle = try FileHandle(forReadingFrom: url)
            defer { try? handle.close() }

            var hasher = SHA256()
            let chunkSize = 1024 * 1024 // 1 MB

            while true {
                let chunk = try handle.read(upToCount: chunkSize) ?? Data()
                if chunk.isEmpty { break }
                hasher.update(data: chunk)
            }

            let digest = hasher.finalize()
            return digest.map { String(format: "%02x", $0) }.joined()
        }.value
    }

    /// Generic metadata used to render the package sidecars, independent of how
    /// `clip.mp4` was produced (verbatim segment copy vs. precise-range stitch).
    private struct PackageInfo {
        let caseID: String
        let clipID: String
        let clipTitle: String
        let cameraName: String
        let cameraIPAddress: String
        let rangeLabel: String
        let status: String
        let createdAt: Date?
        let sourceFilePath: String
        /// Human-readable note on how clip.mp4 was produced (verbatim vs. lossless
        /// stitch vs. re-encoded overlay) — recorded for chain-of-custody.
        let exportMethod: String
    }

    // MARK: - Public entry points

    /// Verbatim package: copies a whole recording segment unchanged (best for a
    /// single segment, since the bytes are bit-for-bit the original).
    func export(
        clip: EvidenceClip,
        segmentURL: URL,
        to destination: URL,
        operatorName: String,
        auditLog: [AuditLogEntry],
        cameraName: String,
        cameraIPAddress: String
    ) async {
        isExporting = true
        progress = 0.0
        lastResult = nil
        defer { isExporting = false }

        guard let clipDestination = prepareBundle(at: destination) else { return }

        do {
            try FileManager.default.copyItem(at: segmentURL, to: clipDestination)
        } catch {
            lastResult = .failure("Could not copy recording file: \(error.localizedDescription)")
            return
        }
        progress = 0.40

        let info = PackageInfo(
            caseID: clip.caseID,
            clipID: clip.id.uuidString,
            clipTitle: clip.title,
            cameraName: cameraName,
            cameraIPAddress: cameraIPAddress,
            rangeLabel: clip.range,
            status: clip.status,
            createdAt: clip.lockedAt,
            sourceFilePath: clip.filePath ?? segmentURL.path,
            exportMethod: "Verbatim copy of the source recording segment (bit-for-bit original)."
        )
        await writePackage(into: destination, clipDestination: clipDestination, info: info,
                           operatorName: operatorName, auditLog: auditLog, baseProgress: 0.40)
    }

    /// Precise-range package: builds `clip.mp4` for an exact `[start, end]` window
    /// by stitching the overlapping segments and trimming to the in/out point via
    /// `ClipExporter` (lossless passthrough unless `overlay` is set), then writes
    /// the same manifest + chain-of-custody sidecars.
    func exportRange(
        segments: [RecordingSegment],
        cameraID: UUID,
        from start: Date,
        to end: Date,
        to destination: URL,
        caseID: String,
        title: String,
        operatorName: String,
        auditLog: [AuditLogEntry],
        cameraName: String,
        cameraIPAddress: String,
        overlay: ClipExporter.TimestampOverlay? = nil
    ) async {
        isExporting = true
        progress = 0.0
        lastResult = nil
        defer { isExporting = false }

        guard let clipDestination = prepareBundle(at: destination) else { return }

        let result: ClipExporter.Result
        do {
            // Clip build drives 0…55% of the bar; sidecars take it to 100%.
            result = try await ClipExporter.exportRange(
                segments: segments,
                cameraID: cameraID,
                from: start,
                to: end,
                to: clipDestination,
                overlay: overlay,
                progress: { [weak self] p in self?.progress = p * 0.55 }
            )
        } catch {
            let message = (error as? ClipExporter.ExportError)?.errorDescription ?? error.localizedDescription
            lastResult = .failure(message)
            return
        }
        progress = 0.55

        let humanFormatter = DateFormatter()
        humanFormatter.dateFormat = "yyyy-MM-dd HH:mm:ss zzz"
        let rangeLabel = "\(humanFormatter.string(from: result.actualStart)) – \(humanFormatter.string(from: result.actualEnd))"
        let method = overlay == nil
            ? "Lossless stream copy (passthrough) of the exact range, stitched from \(result.segmentCount) segment(s)."
            : "Re-encoded from the exact range (\(result.segmentCount) segment(s) stitched) with a burned-in per-second timestamp overlay."

        let info = PackageInfo(
            caseID: caseID,
            clipID: UUID().uuidString,
            clipTitle: title,
            cameraName: cameraName,
            cameraIPAddress: cameraIPAddress,
            rangeLabel: rangeLabel,
            status: "Exported",
            createdAt: result.actualStart,
            sourceFilePath: result.sourcePath ?? "n/a",
            exportMethod: method
        )
        await writePackage(into: destination, clipDestination: clipDestination, info: info,
                           operatorName: operatorName, auditLog: auditLog, baseProgress: 0.55)
    }

    // MARK: - Shared package assembly

    /// Creates (replacing any prior) the bundle directory and returns the
    /// destination URL for `clip.mp4`, or nil after recording a failure.
    private func prepareBundle(at destination: URL) -> URL? {
        let fm = FileManager.default
        do {
            if fm.fileExists(atPath: destination.path) {
                try fm.removeItem(at: destination)
            }
            try fm.createDirectory(at: destination, withIntermediateDirectories: true)
        } catch {
            lastResult = .failure("Could not create export folder: \(error.localizedDescription)")
            return nil
        }
        progress = 0.05
        return destination.appendingPathComponent("clip.mp4")
    }

    /// Hashes clip.mp4, writes README / chain-of-custody / manifest. `baseProgress`
    /// is where clip.mp4 production left the bar; sidecars carry it to 1.0.
    private func writePackage(
        into destination: URL,
        clipDestination: URL,
        info: PackageInfo,
        operatorName: String,
        auditLog: [AuditLogEntry],
        baseProgress: Double
    ) async {
        let fm = FileManager.default

        let clipHash: String
        do {
            clipHash = try await EvidenceExporter.sha256(of: clipDestination)
        } catch {
            lastResult = .failure("Could not hash recording file: \(error.localizedDescription)")
            return
        }
        progress = max(baseProgress, 0.75)

        let now = Date()
        let isoFormatter = ISO8601DateFormatter()
        isoFormatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]

        let scopedAudit = auditLog.filter { entry in
            entry.detail.contains(info.caseID) ||
            entry.detail.contains(info.clipID) ||
            entry.detail.contains(info.clipTitle)
        }

        // README.txt
        let readmePath = destination.appendingPathComponent("README.txt")
        let readme = """
        HandoffGrid Sentinel — Evidence Package
        =======================================

        Case:   \(info.caseID)
        Camera: \(info.cameraName)
        Range:  \(info.rangeLabel)

        Contents
        --------
        clip.mp4               The exported video (H.264 in MP4 container).
        manifest.json          Machine-readable export metadata, including
                               SHA-256 hashes for every file in this bundle.
        chain-of-custody.txt   Human-readable custody record.
        README.txt             This file.

        How clip.mp4 was produced
        --------------------------
        \(info.exportMethod)

        Playback
        --------
        Open clip.mp4 in QuickTime Player (macOS) or VLC (cross-platform).

        Integrity Verification
        ----------------------
        To verify clip.mp4 has not been altered, compute its SHA-256 and
        compare against the "sha256" field for "clip.mp4" inside
        manifest.json. On macOS:

            shasum -a 256 clip.mp4

        Any mismatch indicates the file has been modified or corrupted
        since export.
        """
        do {
            try readme.write(to: readmePath, atomically: true, encoding: .utf8)
        } catch {
            lastResult = .failure("Could not write README.txt: \(error.localizedDescription)")
            return
        }
        progress = 0.80

        // chain-of-custody.txt
        let custodyPath = destination.appendingPathComponent("chain-of-custody.txt")
        let humanFormatter = DateFormatter()
        humanFormatter.dateFormat = "yyyy-MM-dd HH:mm:ss zzz"

        var custody = """
        HandoffGrid Sentinel — Chain of Custody
        =======================================

        Case ID:        \(info.caseID)
        Clip Title:     \(info.clipTitle)
        Camera:         \(info.cameraName)
        Camera Address: \(info.cameraIPAddress.isEmpty ? "n/a" : info.cameraIPAddress)
        Range:          \(info.rangeLabel)
        Status:         \(info.status)

        Created:        \(info.createdAt.map(humanFormatter.string(from:)) ?? "n/a")
        Exported By:    \(operatorName)
        Exported At:    \(humanFormatter.string(from: now))
        Export Method:  \(info.exportMethod)

        Source:         \(info.sourceFilePath)
        Export Bundle:  \(destination.path)

        SHA-256 (clip.mp4):
            \(clipHash)

        Scoped Audit Trail
        ------------------
        """

        if scopedAudit.isEmpty {
            custody += "\n    (no audit entries reference this case)\n"
        } else {
            for entry in scopedAudit {
                let line = "[\(humanFormatter.string(from: entry.time))] \(entry.user) — \(entry.area) — \(entry.action): \(entry.detail)"
                custody += "\n    \(line)"
            }
            custody += "\n"
        }

        do {
            try custody.write(to: custodyPath, atomically: true, encoding: .utf8)
        } catch {
            lastResult = .failure("Could not write chain-of-custody.txt: \(error.localizedDescription)")
            return
        }
        progress = 0.90

        let readmeHash: String
        let custodyHash: String
        do {
            readmeHash = try await EvidenceExporter.sha256(of: readmePath)
            custodyHash = try await EvidenceExporter.sha256(of: custodyPath)
        } catch {
            lastResult = .failure("Could not hash bundle files: \(error.localizedDescription)")
            return
        }

        let clipSize = (try? fm.attributesOfItem(atPath: clipDestination.path)[.size] as? Int64) ?? 0

        let manifest = ExportManifest(
            schemaVersion: "1.1",
            generator: "HandoffGrid Sentinel",
            caseID: info.caseID,
            clipID: info.clipID,
            clipTitle: info.clipTitle,
            camera: ExportManifest.Camera(
                name: info.cameraName,
                ipAddress: info.cameraIPAddress.isEmpty ? nil : info.cameraIPAddress
            ),
            range: info.rangeLabel,
            status: info.status,
            exportMethod: info.exportMethod,
            createdAt: info.createdAt.map { isoFormatter.string(from: $0) },
            exportedBy: operatorName,
            exportedAt: isoFormatter.string(from: now),
            sourceFilePath: info.sourceFilePath,
            files: [
                ExportManifest.FileEntry(name: "clip.mp4", sizeBytes: clipSize, sha256: clipHash),
                ExportManifest.FileEntry(name: "chain-of-custody.txt", sizeBytes: Int64((try? fm.attributesOfItem(atPath: custodyPath.path)[.size] as? Int64) ?? 0), sha256: custodyHash),
                ExportManifest.FileEntry(name: "README.txt", sizeBytes: Int64((try? fm.attributesOfItem(atPath: readmePath.path)[.size] as? Int64) ?? 0), sha256: readmeHash)
            ],
            auditEntries: scopedAudit.map {
                ExportManifest.AuditEntry(
                    timestamp: isoFormatter.string(from: $0.time),
                    user: $0.user,
                    area: $0.area,
                    action: $0.action,
                    detail: $0.detail
                )
            }
        )

        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let manifestPath = destination.appendingPathComponent("manifest.json")
        do {
            let data = try encoder.encode(manifest)
            try data.write(to: manifestPath, options: .atomic)
        } catch {
            lastResult = .failure("Could not write manifest.json: \(error.localizedDescription)")
            return
        }

        progress = 1.0
        lastResult = .success(destination)
    }
}

// MARK: - Manifest Codable shape

private struct ExportManifest: Codable {
    let schemaVersion: String
    let generator: String
    let caseID: String
    let clipID: String
    let clipTitle: String
    let camera: Camera
    let range: String
    let status: String
    let exportMethod: String
    let createdAt: String?
    let exportedBy: String
    let exportedAt: String
    let sourceFilePath: String
    let files: [FileEntry]
    let auditEntries: [AuditEntry]

    struct Camera: Codable {
        let name: String
        let ipAddress: String?
    }

    struct FileEntry: Codable {
        let name: String
        let sizeBytes: Int64
        let sha256: String
    }

    struct AuditEntry: Codable {
        let timestamp: String
        let user: String
        let area: String
        let action: String
        let detail: String
    }
}
