import Combine
import Foundation

// Appearance search ("Find similar", wave 11 stream C): which events can be
// searched by appearance, how the panel's controls map to
// `POST /events/{id}/similar`, how scores are shown, what the panel says when
// no model is installed, and the index job's one-line progress.
// Mirrors `linux/web/src/operator/similarModel.ts`.

/// The time window the matches are looked for in.
public enum SimilarWindow: String, CaseIterable, Identifiable, Sendable {
    /// ±24 h around the query event.
    case around
    case week
    case month
    case any

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .around: return "±24 h around it"
        case .week: return "Last 7 days"
        case .month: return "Last 30 days"
        case .any: return "Any time"
        }
    }
}

/// The panel's controls.
public struct SimilarOptions: Equatable, Sendable {
    public static let defaultLimit = 48

    public var window: SimilarWindow = .week
    /// Only the query's own camera (otherwise every visible camera).
    public var sameCamera = false
    /// Compare against every kind, not only the query's.
    public var anyKind = false
    /// Lowest similarity shown, 0…1 (0 = no floor, the server's default).
    public var minScore: Double = 0
    public var limit = SimilarOptions.defaultLimit

    public init() {}
}

/// How strong a match looks: the badge color.
public enum SimilarTone: Equatable, Sendable {
    case strong, likely, weak
}

public enum SimilarLogic {
    private static let day: Int64 = 86_400_000

    /// Kinds without a person/vehicle/animal crop.
    private static let noAppearance: Set<String> = ["motion", "audio"]

    /// Whether "Find similar" makes sense: a detection box on a snapshot.
    public static func canFindSimilar(_ event: V2.Event) -> Bool {
        guard event.hasThumbnail, let box = event.box, box.count == 4, box[2] > box[0], box[3] > box[1] else { return false }
        return noAppearance.contains(event.kind.lowercased()) == false
    }

    /// The request body for the query event and the panel's options.
    public static func request(for query: V2.Event, options: SimilarOptions, now: Date) -> V2.SimilarEventsRequest {
        var body = V2.SimilarEventsRequest(limit: max(1, min(100, options.limit)))
        let nowMs = now.unixMillis
        switch options.window {
        case .around:
            body.from = max(0, query.createdAt - day)
            body.to = query.createdAt + day
        case .week:
            body.from = nowMs - 7 * day
        case .month:
            body.from = nowMs - 30 * day
        case .any:
            break
        }
        if options.sameCamera { body.cameras = [query.cameraId] }
        if options.anyKind { body.anyKind = true }
        let floor = min(1, max(0, options.minScore))
        if floor > 0 { body.minScore = (floor * 100).rounded() / 100 }
        return body
    }

    /// Best match first (ties: newest first), each event once, never the query itself.
    public static func ordered(_ hits: [V2.SimilarEventHit], excluding queryID: String) -> [V2.SimilarEventHit] {
        var seen: Set<String> = [queryID]
        let unique = hits.filter { seen.insert($0.event.id).inserted }
        return unique.enumerated().sorted { a, b in
            let sa = a.element.score.isFinite ? a.element.score : -2
            let sb = b.element.score.isFinite ? b.element.score : -2
            if sa != sb { return sa > sb }
            if a.element.event.createdAt != b.element.event.createdAt { return a.element.event.createdAt > b.element.event.createdAt }
            return a.offset < b.offset
        }.map(\.element)
    }

    /// A cosine score as a whole percentage (negative = 0).
    public static func scorePercent(_ score: Double) -> Int {
        guard score.isFinite else { return 0 }
        return Int((max(0, min(1, score)) * 100).rounded())
    }

    /// Strong matches (≥ 80 %) green, likely (≥ 60 %) amber, the rest grey.
    public static func tone(_ score: Double) -> SimilarTone {
        let p = scorePercent(score)
        return p >= 80 ? .strong : p >= 60 ? .likely : .weak
    }

    static let installHint = "Put an OSNet re-identification model (ONNX) at <data>/models/reid.onnx, or point SENTINEL_REID_MODEL at one, and restart the server."

    /// The "unavailable" text, or nil when appearance search works.
    public static func unavailableText(_ status: V2.AppearanceStatus?) -> String? {
        guard let status, status.available == false else { return nil }
        let reason: String
        if let given = status.reason?.trimmingCharacters(in: .whitespacesAndNewlines), given.isEmpty == false {
            reason = given.prefix(1).uppercased() + given.dropFirst()
        } else {
            reason = "No appearance model is installed"
        }
        let sentence = reason.hasSuffix(".") ? reason : reason + "."
        return "Appearance search is unavailable. \(sentence) \(installHint)"
    }

    /// What to tell the operator when a search fails; `banner` = show it as
    /// the unavailable banner (503: no model).
    public static func failure(_ error: Error) -> (text: String, banner: Bool) {
        if let api = error as? SentinelAPIError, api.status == 503 {
            var reason = api.errorDescription ?? ""
            if let range = reason.range(of: "appearance search is unavailable:", options: [.caseInsensitive, .anchored]) {
                reason = String(reason[range.upperBound...]).trimmingCharacters(in: .whitespaces)
            }
            return (unavailableText(V2.AppearanceStatus(available: false, reason: reason)) ?? reason, true)
        }
        return (error.localizedDescription, false)
    }

    private static let number: NumberFormatter = {
        let f = NumberFormatter()
        f.numberStyle = .decimal
        f.locale = Locale(identifier: "en_US")
        return f
    }()

    static func n(_ value: Int) -> String { number.string(from: NSNumber(value: value)) ?? String(value) }

    /// One line about the index job.
    public static func backfillLabel(_ job: V2.AppearanceBackfill?) -> String {
        guard let job else { return "" }
        switch job.state {
        case .running:
            return job.total > 0 ? "Indexing recent events: \(n(job.processed)) of \(n(job.total))" : "Indexing recent events…"
        case .done:
            return "Index built: \(n(job.indexed)) of \(n(job.total)) events" + (job.skipped > 0 ? " (\(n(job.skipped)) skipped)" : "")
        case .canceled:
            return "Indexing canceled after \(n(job.processed)) of \(n(job.total)) events"
        default:
            return "Indexing failed" + (job.error.map { $0.isEmpty ? "" : ": \($0)" } ?? "")
        }
    }

    /// The index job's progress, 0…1, while it runs with a known total.
    public static func backfillProgress(_ job: V2.AppearanceBackfill?) -> Double? {
        guard let job, job.state == .running, job.total > 0 else { return nil }
        return min(1, max(0, Double(job.processed) / Double(job.total)))
    }

    /// "12 matches of 3,400 indexed events".
    public static func summary(matches: Int, scanned: Int) -> String {
        "\(matches) match\(matches == 1 ? "" : "es") of \(n(scanned)) indexed events"
    }
}

/// The "Find similar" panel's state for one query event: options, the
/// appearance status (model, index job) and the matches. View-free so it can
/// be tested; the view only binds to it.
@MainActor
public final class SimilarModel: ObservableObject {
    public struct Backend: Sendable {
        public var status: @Sendable () async throws -> V2.AppearanceStatus
        public var similar: @Sendable (_ eventID: String, _ request: V2.SimilarEventsRequest) async throws -> V2.SimilarEvents
        public var startBackfill: @Sendable () async throws -> V2.AppearanceBackfill
        public var cancelBackfill: @Sendable () async throws -> V2.AppearanceBackfill

        public init(status: @escaping @Sendable () async throws -> V2.AppearanceStatus,
                    similar: @escaping @Sendable (String, V2.SimilarEventsRequest) async throws -> V2.SimilarEvents,
                    startBackfill: @escaping @Sendable () async throws -> V2.AppearanceBackfill,
                    cancelBackfill: @escaping @Sendable () async throws -> V2.AppearanceBackfill) {
            self.status = status
            self.similar = similar
            self.startBackfill = startBackfill
            self.cancelBackfill = cancelBackfill
        }

        public init(api: SentinelAPI) {
            self.init(
                status: { try await api.appearanceStatus() },
                similar: { id, request in try await api.similarEvents(eventID: id, request: request) },
                startBackfill: { try await api.startAppearanceBackfill() },
                cancelBackfill: { try await api.cancelAppearanceBackfill() }
            )
        }
    }

    public let query: V2.Event
    @Published public var options = SimilarOptions()
    @Published public private(set) var status: V2.AppearanceStatus?
    @Published public private(set) var hits: [V2.SimilarEventHit]?
    @Published public private(set) var scanned = 0
    @Published public private(set) var isLoading = false
    @Published public private(set) var error: String?
    /// Set when appearance search is unavailable (from the status or a 503).
    @Published public private(set) var banner: String?
    /// A backfill start/cancel is in flight.
    @Published public private(set) var isBusy = false

    private let backend: Backend
    private var generation = 0

    public init(query: V2.Event, backend: Backend) {
        self.query = query
        self.backend = backend
    }

    /// The request the current options make.
    public func request(now: Date = Date()) -> V2.SimilarEventsRequest {
        SimilarLogic.request(for: query, options: options, now: now)
    }

    public var backfill: V2.AppearanceBackfill? { status?.backfill }
    public var isBackfillRunning: Bool { status?.backfill?.state == .running }

    /// The line under the controls.
    public var summary: String {
        if isLoading { return "Searching…" }
        guard let hits else { return "" }
        return SimilarLogic.summary(matches: hits.count, scanned: scanned)
    }

    /// The index line: the job's state, or how many events are indexed.
    public var indexLine: String {
        if let job = status?.backfill { return SimilarLogic.backfillLabel(job) }
        return "\(SimilarLogic.n(status?.indexed ?? 0)) events indexed"
    }

    /// Runs the search for the current options; a superseded answer is dropped.
    public func search(now: Date = Date()) async {
        generation += 1
        let mine = generation
        let body = request(now: now)
        isLoading = true
        error = nil
        do {
            let answer = try await backend.similar(query.id, body)
            guard mine == generation else { return }
            hits = SimilarLogic.ordered(answer.items, excluding: query.id)
            scanned = answer.scanned
            banner = nil
        } catch {
            guard mine == generation else { return }
            let failure = SimilarLogic.failure(error)
            hits = nil
            if failure.banner { banner = failure.text } else { self.error = failure.text }
        }
        if mine == generation { isLoading = false }
    }

    /// Refreshes the appearance status (and the banner when it says unavailable).
    public func loadStatus() async {
        do {
            let next = try await backend.status()
            status = next
            if let text = SimilarLogic.unavailableText(next) { banner = text }
        } catch {
            // The search itself reports problems.
        }
    }

    /// Starts the index job (or picks up the running one), then refreshes the status.
    public func startBackfill() async {
        await backfillAction(backend.startBackfill)
    }

    public func cancelBackfill() async {
        await backfillAction(backend.cancelBackfill)
    }

    private func backfillAction(_ call: @Sendable () async throws -> V2.AppearanceBackfill) async {
        guard isBusy == false else { return }
        isBusy = true
        error = nil
        do {
            let job = try await call()
            if var current = status {
                current.backfill = job
                status = current
            }
            await loadStatus()
        } catch {
            self.error = SimilarLogic.failure(error).text
        }
        isBusy = false
    }
}
