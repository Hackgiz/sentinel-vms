// BackupBrowserView.swift
// The site's offsite backup in Sentinel Cloud: backed-up alarms (snapshot,
// camera, time), newest first, and each alarm's clip. Everything is read from
// Sentinel Cloud with the site's `scb_` token, never from the Management
// Server, so it works when the server is off or gone. Opened from the
// Sentinel Cloud screen, and from Alarms when the site can't be reached.

import SwiftUI
import AVKit

// MARK: - Model

@MainActor
final class BackupBrowserModel: ObservableObject {
    enum Phase: Equatable {
        case idle
        case loading
        case loaded
        case failed(String)
    }

    @Published private(set) var entries: [Cloud.BackupAlarmEntry] = []
    @Published private(set) var phase: Phase = .idle
    @Published private(set) var hasMore = false
    @Published private(set) var loadMoreError: String?

    let client: BackupClient
    private var next: String?
    private var loadingMore = false
    private var detailsRequested: Set<String> = []

    init(client: BackupClient) {
        self.client = client
    }

    /// First page (also pull to refresh).
    func reload() async {
        if entries.isEmpty { phase = .loading }
        do {
            let page = try await client.list()
            entries = Cloud.BackupAlarmEntry.merge(page.objects, into: [])
            next = page.next
            hasMore = page.next != nil
            loadMoreError = nil
            detailsRequested = []
            phase = .loaded
        } catch is CancellationError {
            if entries.isEmpty { phase = .idle }
        } catch {
            if entries.isEmpty {
                phase = .failed(BackupClient.message(for: error))
            } else {
                loadMoreError = BackupClient.message(for: error)
            }
        }
    }

    /// The next page, when the list scrolls to its end.
    func loadMore() async {
        guard let cursor = next, loadingMore == false else { return }
        loadingMore = true
        defer { loadingMore = false }
        do {
            let page = try await client.list(after: cursor)
            entries = Cloud.BackupAlarmEntry.merge(page.objects, into: entries)
            next = page.next
            hasMore = page.next != nil
            loadMoreError = nil
        } catch {
            loadMoreError = BackupClient.message(for: error)
        }
    }

    /// Reads `alarm.json` for a row the listing didn't describe well enough.
    func loadDetails(for entry: Cloud.BackupAlarmEntry) async {
        guard entry.summary.isDescriptive == false, let key = entry.alarmJSONKey,
              detailsRequested.contains(entry.id) == false else { return }
        detailsRequested.insert(entry.id)
        guard let record = try? await client.alarmRecord(key: key),
              let index = entries.firstIndex(where: { $0.id == entry.id }) else { return }
        entries[index] = entries[index].applying(record)
    }

    func entry(id: String) -> Cloud.BackupAlarmEntry? { entries.first { $0.id == id } }
}

// MARK: - List

struct BackupBrowserView: View {
    let grant: V2.CloudBackupAccess
    @StateObject private var model: BackupBrowserModel

    init(grant: V2.CloudBackupAccess) {
        self.grant = grant
        _model = StateObject(wrappedValue: BackupBrowserModel(client: BackupClient(grant: grant)))
    }

    var body: some View {
        ZStack {
            SentinelTheme.background.ignoresSafeArea()
            content
        }
        .navigationTitle("Offsite Backup")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .task { if model.phase == .idle { await model.reload() } }
    }

    @ViewBuilder
    private var content: some View {
        switch model.phase {
        case .idle, .loading:
            ProgressView().tint(SentinelTheme.accent)
        case .failed(let message):
            placeholder(symbol: "exclamationmark.icloud", title: "Couldn't open the offsite backup", text: message) {
                Button("Try Again") { Task { await model.reload() } }
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(SentinelTheme.accent)
            }
        case .loaded:
            if model.entries.isEmpty {
                ScrollView {
                    placeholder(symbol: "photo.stack", title: "Nothing backed up yet",
                                text: "New alarms are copied off site with their snapshot and, when there is one, their clip.") { EmptyView() }
                        .padding(.top, 80)
                }
                .refreshable { await model.reload() }
            } else {
                list
            }
        }
    }

    private var list: some View {
        ScrollView {
            LazyVStack(spacing: SentinelTheme.Space.md) {
                ForEach(model.entries) { entry in
                    NavigationLink {
                        BackupAlarmDetailView(entryID: entry.id, model: model)
                    } label: {
                        BackupAlarmRow(entry: entry, client: model.client)
                    }
                    .buttonStyle(.plain)
                    .task { await model.loadDetails(for: entry) }
                    .onAppear {
                        if entry.id == model.entries.last?.id { Task { await model.loadMore() } }
                    }
                }
                if let error = model.loadMoreError {
                    Text(error).font(.caption).foregroundStyle(.white.opacity(0.55))
                        .multilineTextAlignment(.center).frame(maxWidth: .infinity)
                } else if model.hasMore {
                    ProgressView().tint(.white.opacity(0.5)).padding(.vertical, 8)
                }
            }
            .padding(SentinelTheme.Space.lg)
        }
        .refreshable { await model.reload() }
    }

    private func placeholder<Action: View>(symbol: String, title: String, text: String,
                                           @ViewBuilder action: () -> Action) -> some View {
        VStack(spacing: 10) {
            Image(systemName: symbol).font(.system(size: 40)).foregroundStyle(SentinelTheme.accent)
            Text(title).font(.headline).foregroundStyle(.white)
            Text(text).font(.caption).foregroundStyle(.white.opacity(0.55))
                .multilineTextAlignment(.center).padding(.horizontal, 40)
            action()
        }
        .frame(maxWidth: .infinity)
    }
}

struct BackupAlarmRow: View {
    let entry: Cloud.BackupAlarmEntry
    let client: BackupClient

    var body: some View {
        HStack(spacing: SentinelTheme.Space.md) {
            BackupSnapshot(client: client, key: entry.snapshotKey)
                .frame(width: 112, height: 70)
                .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                .overlay(alignment: .bottomTrailing) {
                    if entry.clipKey != nil {
                        Image(systemName: "play.circle.fill")
                            .font(.callout).foregroundStyle(.white)
                            .shadow(radius: 2).padding(4)
                    }
                }
            VStack(alignment: .leading, spacing: 3) {
                Text(entry.displayTitle)
                    .font(.subheadline.weight(.semibold)).foregroundStyle(.white).lineLimit(2)
                Text(entry.summary.cameraName ?? "Camera")
                    .font(.caption).foregroundStyle(.white.opacity(0.7)).lineLimit(1)
                if let date = entry.date {
                    Text(date.formatted(date: .abbreviated, time: .shortened))
                        .font(.caption2).foregroundStyle(.white.opacity(0.5))
                }
            }
            Spacer(minLength: 4)
            if let severity = entry.summary.severity {
                SentinelSeverityBadge(severity: severity)
            }
        }
        .padding(SentinelTheme.Space.sm)
        .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md).stroke(SentinelTheme.line, lineWidth: 1))
        .contentShape(Rectangle())
    }
}

// MARK: - Detail

struct BackupAlarmDetailView: View {
    let entryID: String
    @ObservedObject var model: BackupBrowserModel
    @State private var player: AVPlayer?
    @State private var playError: String?

    private var entry: Cloud.BackupAlarmEntry? { model.entry(id: entryID) }

    var body: some View {
        ScrollView {
            if let entry {
                content(entry)
            }
        }
        .background(SentinelTheme.background.ignoresSafeArea())
        .navigationTitle(entry?.displayTitle ?? "Alarm")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .onDisappear { player?.pause() }
    }

    @ViewBuilder
    private func content(_ entry: Cloud.BackupAlarmEntry) -> some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.lg) {
            ZStack {
                Color.black
                if let player {
                    VideoPlayer(player: player)
                } else {
                    BackupSnapshot(client: model.client, key: entry.snapshotKey, contentMode: .fit)
                    if entry.clipKey != nil {
                        Button { play(entry) } label: {
                            Image(systemName: "play.circle.fill")
                                .font(.system(size: 56)).foregroundStyle(.white.opacity(0.9))
                                .shadow(radius: 4)
                        }
                        .accessibilityLabel("Play clip")
                    }
                }
            }
            .aspectRatio(16 / 9, contentMode: .fit)
            .frame(maxWidth: .infinity)
            .clipShape(RoundedRectangle(cornerRadius: SentinelTheme.Radius.md, style: .continuous))

            if let playError {
                Text(playError).font(.caption).foregroundStyle(SentinelTheme.alarm)
            }

            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack {
                    if let severity = entry.summary.severity { SentinelSeverityBadge(severity: severity) }
                    if let state = entry.summary.state { Chip(text: state, color: SentinelTheme.stateColor(state)) }
                    Spacer()
                }
                Text(entry.displayTitle).font(.title3.weight(.bold)).foregroundStyle(.white)
                if let detail = entry.summary.detail {
                    Text(detail).font(.callout).foregroundStyle(.white.opacity(0.75))
                }
            }

            VStack(alignment: .leading, spacing: 8) {
                row("Camera", entry.summary.cameraName ?? "—")
                if let date = entry.summary.createdAt ?? entry.summary.lastEventAt {
                    row("Raised", date.formatted(date: .abbreviated, time: .standard))
                }
                if let uploaded = entry.uploadedAt {
                    row("Backed up", uploaded.formatted(date: .abbreviated, time: .shortened))
                }
                if let size = entry.clipSize, size > 0 {
                    row("Clip", ByteCountFormatter.string(fromByteCount: size, countStyle: .file))
                } else if entry.clipKey == nil {
                    row("Clip", "Snapshot only")
                }
            }

            Label("Kept in Sentinel Cloud, readable even when the server is off.", systemImage: "externaldrive.badge.icloud")
                .font(.caption).foregroundStyle(.white.opacity(0.5))
        }
        .padding(SentinelTheme.Space.lg)
        .task(id: entry.id) { await model.loadDetails(for: entry) }
    }

    private func play(_ entry: Cloud.BackupAlarmEntry) {
        guard let key = entry.clipKey, let url = model.client.playbackURL(key: key) else {
            playError = "This clip can't be played."
            return
        }
        playError = nil
        let player = AVPlayer(url: url)
        self.player = player
        player.play()
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).font(.subheadline).foregroundStyle(.white.opacity(0.55))
            Spacer()
            Text(value).font(.subheadline.weight(.medium)).foregroundStyle(.white)
                .multilineTextAlignment(.trailing)
        }
    }
}

// MARK: - Snapshot

/// Backed-up snapshots, so scrolling back doesn't fetch them again.
enum BackupImageCache {
    static let images: NSCache<NSString, UIImage> = {
        let cache = NSCache<NSString, UIImage>()
        cache.countLimit = 120
        return cache
    }()
}

/// A backed-up snapshot, fetched from Sentinel Cloud with the backup token.
struct BackupSnapshot: View {
    let client: BackupClient
    let key: String?
    var contentMode: ContentMode = .fill

    @State private var image: UIImage?
    @State private var failed = false

    var body: some View {
        GeometryReader { geo in
            ZStack {
                if let image {
                    Image(uiImage: image)
                        .resizable()
                        .aspectRatio(contentMode: contentMode)
                        .frame(width: geo.size.width, height: geo.size.height)
                        .clipped()
                } else {
                    LinearGradient(colors: [Color(white: 0.12), Color(white: 0.04)],
                                   startPoint: .topLeading, endPoint: .bottomTrailing)
                    if failed || key == nil {
                        Image(systemName: "photo").font(.title3).foregroundStyle(.white.opacity(0.25))
                    } else {
                        ProgressView().tint(.white.opacity(0.4))
                    }
                }
            }
        }
        .task(id: key) { await load() }
    }

    private func load() async {
        guard let key else { return }
        let cacheKey = "\(client.grant.serverId)/\(key)" as NSString
        if let cached = BackupImageCache.images.object(forKey: cacheKey) {
            image = cached
            return
        }
        image = nil
        failed = false
        do {
            let data = try await client.data(key: key)
            if let decoded = UIImage(data: data) {
                BackupImageCache.images.setObject(decoded, forKey: cacheKey)
                image = decoded
            } else {
                failed = true
            }
        } catch {
            failed = true
        }
    }
}

// MARK: - From Alarms when the site is offline

/// When to offer the backup on the Alarms list.
enum OfflineBackupOffer {
    /// The site looked down (the live stream isn't connected or the last load
    /// failed) and a backup token is saved: worth asking the server once.
    static func shouldProbe(hasGrant: Bool, signedIn: Bool, streamConnected: Bool, streamIdle: Bool,
                            loadFailed: Bool) -> Bool {
        hasGrant && signedIn && ((streamConnected == false && streamIdle == false) || loadFailed)
    }

    /// The answer to the probe means "the site can't be reached": no answer at
    /// all, or the Sentinel Cloud relay saying the server isn't connected.
    static func meansUnreachable(_ error: Error) -> Bool {
        guard let api = error as? SentinelAPIError else { return false }
        if case .transport = api { return true }
        if let status = api.status { return (502...504).contains(status) }
        return false
    }
}

/// Adds "Offline — show cloud backup" above the Alarms list when the site
/// can't be reached and an offsite backup token is saved.
struct OfflineBackupModifier: ViewModifier {
    @ObservedObject private var session = SentinelSession.shared
    @ObservedObject private var store = AppStore.shared
    @ObservedObject private var access = BackupAccess.shared
    @State private var unreachable = false
    @State private var showBackup = false

    private var grant: V2.CloudBackupAccess? {
        session.isDemoMode ? nil : access.grant(forSite: session.currentSite?.id)
    }

    private var suspect: Bool {
        let idle: Bool
        if case .idle = store.streamState { idle = true } else { idle = false }
        return OfflineBackupOffer.shouldProbe(hasGrant: grant != nil, signedIn: session.isSignedIn,
                                              streamConnected: store.streamState.isConnected, streamIdle: idle,
                                              loadFailed: store.lastError != nil)
    }

    func body(content: Content) -> some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
            if suspect, unreachable, grant != nil {
                banner
            }
            content
        }
        .task(id: suspect) { await probe() }
        .sheet(isPresented: $showBackup) {
            NavigationStack {
                if let grant {
                    BackupBrowserView(grant: grant)
                        .toolbar {
                            ToolbarItem(placement: .cancellationAction) {
                                Button("Done") { showBackup = false }
                            }
                        }
                }
            }
            .preferredColorScheme(.dark)
        }
    }

    private var banner: some View {
        Button {
            Haptics.tap()
            showBackup = true
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "icloud.slash").foregroundStyle(SentinelTheme.amber)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Offline — show cloud backup").font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                    Text("Can't reach \(session.serverName ?? "the site"). Backed-up alarms are still in Sentinel Cloud.")
                        .font(.caption).foregroundStyle(.white.opacity(0.6))
                        .multilineTextAlignment(.leading)
                }
                Spacer(minLength: 4)
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.white.opacity(0.4))
            }
            .padding(12)
            .background(SentinelTheme.amber.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.amber.opacity(0.35), lineWidth: 1))
        }
        .buttonStyle(.plain)
    }

    /// While the site looks down, ask it every 30 s whether it answers.
    private func probe() async {
        guard suspect, let api = session.api else {
            unreachable = false
            return
        }
        while Task.isCancelled == false {
            do {
                _ = try await api.providers()
                unreachable = false
            } catch is CancellationError {
                return
            } catch {
                unreachable = OfflineBackupOffer.meansUnreachable(error)
            }
            try? await Task.sleep(nanoseconds: 30_000_000_000)
        }
    }
}

extension View {
    /// The Alarms list's offline affordance (WAVE12(D) hook in AlertsListView).
    func offlineBackupOffer() -> some View {
        modifier(OfflineBackupModifier())
    }
}
