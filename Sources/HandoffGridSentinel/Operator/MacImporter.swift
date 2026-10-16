import Foundation
import SentinelCore

/// Moves a Sentinel for Mac 1.x install to a Sentinel Server: builds the
/// `MacImport` payload from the old files, posts it, then uploads each locked
/// evidence file. Safe to run again (the server is idempotent by source ids
/// and uploaded files are remembered); remembers completion per site.
@MainActor
final class MacImporter: ObservableObject {
    static let shared = MacImporter()

    enum Phase: Equatable {
        case idle
        case running(String, Double)
        case done(String)
        case failed(String)
    }

    struct Record: Codable {
        var siteAddress: String
        var startedAt: Date
        var completedAt: Date?
        var result: V2.MacImportResult?
        var uploaded: [String]
        var skipped: [String]
    }

    struct Preview {
        var cameras: Int
        var users: Int
        var rules: Int
        var evidence: Int
        var audit: Int
        var recordingFolders: [URL]
        var hasVault: Bool
        var userNames: [String]
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var record: Record?
    /// Hidden after "Later", remembered across launches; the Importer section
    /// in the sidebar remains the way back in.
    @Published var postponed = UserDefaults.standard.bool(forKey: MacImporter.postponedKey) {
        didSet { UserDefaults.standard.set(postponed, forKey: Self.postponedKey) }
    }
    private static let postponedKey = "sentinel.operator.macImport.postponed"

    let install: LegacyMacInstall
    private let key = "sentinel.operator.macImport"

    init(install: LegacyMacInstall = LegacyMacInstall()) {
        self.install = install
        if let data = UserDefaults.standard.data(forKey: key) {
            record = try? JSONDecoder().decode(Record.self, from: data)
        }
        if let completed = record?.completedAt, let result = record?.result {
            phase = .done(Self.summary(result, at: completed))
        }
    }

    var hasLegacyData: Bool { install.hasData }
    var isComplete: Bool { record?.completedAt != nil }
    /// Offer the import (first launch of this version, an Admin signed in).
    var shouldOffer: Bool { isComplete == false && postponed == false && hasLegacyData }

    func preview() -> Preview {
        let output = MacImportBuilder(install: install, sourceVersion: Self.sourceVersion, hash: { _ in "" }).build()
        return Preview(
            cameras: output.payload.cameras.count,
            users: output.payload.users.count,
            rules: output.payload.rules.count,
            evidence: output.payload.evidence.count,
            audit: output.payload.audit.count,
            recordingFolders: output.payload.cameras.compactMap { $0.recordingsPath.map { URL(fileURLWithPath: $0, isDirectory: true) } },
            hasVault: SecretVault(fileURL: install.vaultURL).exists,
            userNames: install.users().map(\.name)
        )
    }

    /// The Mac app's version before this one, for the server's records.
    nonisolated static var sourceVersion: String {
        UserDefaults.standard.string(forKey: "handoffgrid.lastVersion") ?? "1.x"
    }

    /// Runs the import against `api`. `operatorName`/`operatorPassword` open
    /// the old app's secret store so camera passwords come along.
    func run(api: SentinelAPI, siteAddress: String, operatorName: String, operatorPassword: String) async {
        let install = self.install
        phase = .running("Reading Sentinel 1.x data…", 0.02)

        // Camera passwords: the old secret store (needs an operator's
        // password), else the login Keychain of versions before it.
        let vault = SecretVault(fileURL: install.vaultURL)
        var vaultOpen = false
        if operatorPassword.isEmpty == false,
           let user = install.users().first(where: { $0.name.caseInsensitiveCompare(operatorName) == .orderedSame }) {
            vaultOpen = vault.unlockExisting(userKey: user.id.uuidString, password: operatorPassword)
        }
        if vault.exists, operatorPassword.isEmpty == false, vaultOpen == false {
            phase = .failed("That name and password didn't open the camera passwords of Sentinel 1.x. Check them, or leave the password empty to import without camera passwords.")
            return
        }
        let service = CameraSecrets.vaultService
        let output = await Task.detached {
            MacImportBuilder(install: install, sourceVersion: Self.sourceVersion, password: { id in
                if vaultOpen, let data = vault.value(service: service, account: id.uuidString) {
                    return String(data: data, encoding: .utf8)
                }
                return CameraSecrets.legacyKeychainPasswordForImport(id)
            }).build()
        }.value

        var record = self.record?.siteAddress == siteAddress ? self.record! :
            Record(siteAddress: siteAddress, startedAt: Date(), uploaded: [], skipped: [])
        phase = .running("Sending cameras, users, rules and history…", 0.1)
        let result: V2.MacImportResult
        do {
            result = try await api.importMac(output.payload)
        } catch {
            let apiError = error as? SentinelAPIError
            if apiError?.isConflict == true {
                phase = .failed("The server has no local Recording Server to take these cameras. Import into the server running on this Mac. (\(error.localizedDescription))")
            } else if apiError?.isForbidden == true {
                phase = .failed("Only an Admin can import. (\(error.localizedDescription))")
            } else {
                phase = .failed(error.localizedDescription)
            }
            return
        }
        record.result = result
        record.skipped = output.skipped
        save(record)

        let pending = output.files.filter { record.uploaded.contains($0.caseId) == false }
        for (index, file) in pending.enumerated() {
            phase = .running("Uploading evidence \(index + 1) of \(pending.count) (\(OperatorFormat.bytes(file.size)))…",
                             0.2 + 0.8 * Double(index) / Double(max(pending.count, 1)))
            do {
                try await api.uploadImportedEvidence(caseID: file.caseId, fileURL: file.url)
                record.uploaded.append(file.caseId)
            } catch let error as SentinelAPIError where error.isConflict {
                // Already stored (an earlier, interrupted run).
                record.uploaded.append(file.caseId)
            } catch {
                record.skipped.append("Evidence \(file.caseId): \(error.localizedDescription)")
            }
            save(record)
        }

        record.completedAt = Date()
        save(record)
        phase = .done(Self.summary(result, at: record.completedAt!))
    }

    /// Clears "done" so the import can run again (e.g. into another site).
    func reset() {
        record = nil
        UserDefaults.standard.removeObject(forKey: key)
        phase = .idle
    }

    private func save(_ record: Record) {
        self.record = record
        if let data = try? JSONEncoder().encode(record) {
            UserDefaults.standard.set(data, forKey: key)
        }
    }

    static func summary(_ result: V2.MacImportResult, at date: Date) -> String {
        "Imported \(result.cameras) camera(s), \(result.users) user(s), \(result.rules) rule(s), \(result.evidence) evidence item(s) and \(result.audit) audit entries on \(OperatorFormat.dateTime.string(from: date))."
    }
}
