// WidgetSnapshotWriter.swift
// Keeps the widgets' snapshot (Sentinel Shared/WidgetSnapshot.swift) in step
// with the app: whenever the cameras, alarms or sign-in change — after a
// refresh or an event-stream message — it rebuilds the compact snapshot,
// writes it to the App Group container and asks WidgetKit to reload. Writes
// are debounced and skipped when nothing a widget shows changed. The newest
// alarm's event thumbnail is shared too, shrunk until it is small enough.
// Nothing secret goes into the shared container.

import Combine
import SwiftUI
import UIKit
import WidgetKit

@MainActor
final class WidgetSnapshotWriter {
    static let shared = WidgetSnapshotWriter()

    private let files: WidgetSnapshotStore
    private var bag = Set<AnyCancellable>()
    private var lastWritten: WidgetSnapshot?
    /// The alarm whose thumbnail is in the container (nil = none).
    private var imageAlarmID: String?
    /// The alarm a thumbnail was last tried for (once per alarm, even on failure).
    private var imageAttemptID: String?
    private var imageTask: Task<Void, Never>?

    init(files: WidgetSnapshotStore = .shared) {
        self.files = files
        lastWritten = files.read()
        if let alarm = lastWritten?.lastAlarm, alarm.hasImage {
            imageAlarmID = alarm.id
            imageAttemptID = alarm.id
        }
    }

    /// Starts following the app's state. Called once at launch.
    func start(store: AppStore = .shared, session: SentinelSession = .shared) {
        guard bag.isEmpty else { return }
        Publishers.CombineLatest4(store.$cameras, store.$alarms, store.$hasLoaded, session.$phase)
            .debounce(for: .seconds(1), scheduler: RunLoop.main)
            .sink { [weak self] _ in self?.publish(store: store, session: session) }
            .store(in: &bag)
    }

    func publish(store: AppStore = .shared, session: SentinelSession = .shared) {
        let signedIn = session.isSignedIn || session.isDemoMode
        // While the first load runs the store is empty; don't flash "0/0".
        if signedIn, store.hasLoaded == false { return }
        let snapshot = Self.make(siteName: session.serverName, signedIn: signedIn, isDemo: session.isDemoMode,
                                 cameras: store.cameras, alarms: store.alarms,
                                 cameraName: { store.cameraName(for: $0) }, imageAlarmID: imageAlarmID)
        write(snapshot)
        if signedIn {
            refreshImage(for: store.alarms.newestOpen, store: store, session: session)
        } else {
            imageTask?.cancel()
            imageAttemptID = nil
            if imageAlarmID != nil { files.writeImage(nil); imageAlarmID = nil }
        }
    }

    private func write(_ snapshot: WidgetSnapshot) {
        guard snapshot.sameContent(as: lastWritten) == false else { return }
        guard files.write(snapshot) else { return }   // no App Group (unsigned build)
        lastWritten = snapshot
        WidgetCenter.shared.reloadAllTimelines()
    }

    /// Fetches, shrinks and shares the newest alarm's event thumbnail once
    /// per alarm. Demo mode and alarms without an event have no image.
    private func refreshImage(for alarm: V2.Alarm?, store: AppStore, session: SentinelSession) {
        guard alarm?.id != imageAttemptID else { return }
        imageAttemptID = alarm?.id
        imageTask?.cancel()
        guard let alarm, let eventID = alarm.eventId, session.isDemoMode == false, let api = session.api else {
            if imageAlarmID != nil { files.writeImage(nil); imageAlarmID = nil }
            return
        }
        imageTask = Task { [weak self] in
            let data = try? await api.eventThumbnail(eventID)
            guard Task.isCancelled == false, let self else { return }
            let small = data.flatMap { Self.shrink($0, maxBytes: WidgetSnapshotStore.maxImageBytes) }
            self.imageAlarmID = self.files.writeImage(small) ? alarm.id : nil
            // Re-publish: the snapshot's hasImage follows imageAlarmID.
            self.publish(store: store, session: session)
        }
    }

    // MARK: Pure helpers (unit-tested)

    static func make(siteName: String?, signedIn: Bool, isDemo: Bool, cameras: [V2.Camera], alarms: [V2.Alarm],
                     cameraName: (V2.Alarm) -> String?, imageAlarmID: String?, now: Date = Date()) -> WidgetSnapshot {
        guard signedIn else {
            var out = WidgetSnapshot.signedOut
            out.generatedAt = now
            out.siteName = siteName ?? ""
            return out
        }
        let online = cameras.filter { $0.status.online }
        let open = alarms.filter(\.isOpen)
        let last = alarms.newestOpen.map { alarm in
            WidgetSnapshot.AlarmSummary(id: alarm.id, title: alarm.title, cameraName: cameraName(alarm) ?? "",
                                        severity: alarm.severity.rawValue, state: alarm.state.rawValue,
                                        raisedAt: Date(unixMillis: alarm.createdAt),
                                        hasImage: imageAlarmID == alarm.id)
        }
        return WidgetSnapshot(generatedAt: now, siteName: siteName ?? "", signedIn: true, isDemo: isDemo,
                              camerasOnline: online.count, camerasTotal: cameras.count,
                              camerasRecording: online.filter(\.recording).count,
                              openAlarms: open.count, newAlarms: open.filter { $0.state == .new }.count,
                              lastAlarm: last)
    }

    /// Downscales a JPEG/PNG to at most `maxSide` points and re-encodes it
    /// until it fits in `maxBytes`; nil when it can't.
    nonisolated static func shrink(_ data: Data, maxSide: CGFloat = 360, maxBytes: Int) -> Data? {
        guard let image = UIImage(data: data), image.size.width > 0, image.size.height > 0 else { return nil }
        var side = maxSide
        for _ in 0..<3 {
            let scale = min(1, side / max(image.size.width, image.size.height))
            let size = CGSize(width: (image.size.width * scale).rounded(), height: (image.size.height * scale).rounded())
            let format = UIGraphicsImageRendererFormat.default()
            format.scale = 1
            let resized = UIGraphicsImageRenderer(size: size, format: format).image { _ in
                image.draw(in: CGRect(origin: .zero, size: size))
            }
            for quality in [0.7, 0.5, 0.35] as [CGFloat] {
                if let jpeg = resized.jpegData(compressionQuality: quality), jpeg.count <= maxBytes { return jpeg }
            }
            side *= 0.7
        }
        return nil
    }
}

extension Array where Element == V2.Alarm {
    /// The most recently raised open alarm.
    var newestOpen: V2.Alarm? {
        filter(\.isOpen).max { $0.createdAt < $1.createdAt }
    }
}
