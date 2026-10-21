import Combine
import Foundation

/// The forensic search workspace's state: the filter bar, the result pages
/// (keyset `before` cursor), facet counts and the natural-language question.
/// View-free so it can be tested against `MockHTTP`; the view only binds to it.
@MainActor
public final class SearchModel: ObservableObject {
    public struct Backend: Sendable {
        public var search: @Sendable (_ filter: SearchFilter, _ before: String?) async throws -> V2.Page<V2.Event>
        public var facets: @Sendable (_ filter: SearchFilter) async throws -> V2.EventFacets
        public var ask: @Sendable (_ question: String) async throws -> V2.EventAnswer
        // Wave 16 semantic search. Defaults answer 404, as a server without it does.
        public var semanticStatus: @Sendable () async throws -> V2.SemanticStatus
        public var semanticSearch: @Sendable (_ request: V2.SemanticRequest) async throws -> V2.SemanticResults
        /// Starts (true) or cancels (false) the semantic index job.
        public var semanticBackfill: @Sendable (_ start: Bool) async throws -> Void

        public init(search: @escaping @Sendable (SearchFilter, String?) async throws -> V2.Page<V2.Event>,
                    facets: @escaping @Sendable (SearchFilter) async throws -> V2.EventFacets,
                    ask: @escaping @Sendable (String) async throws -> V2.EventAnswer,
                    semanticStatus: @escaping @Sendable () async throws -> V2.SemanticStatus = { throw Backend.missing },
                    semanticSearch: @escaping @Sendable (V2.SemanticRequest) async throws -> V2.SemanticResults = { _ in throw Backend.missing },
                    semanticBackfill: @escaping @Sendable (Bool) async throws -> Void = { _ in throw Backend.missing }) {
            self.search = search
            self.facets = facets
            self.ask = ask
            self.semanticStatus = semanticStatus
            self.semanticSearch = semanticSearch
            self.semanticBackfill = semanticBackfill
        }

        public init(api: SentinelAPI, pageSize: Int = SearchFilter.pageSize) {
            self.init(
                search: { filter, before in try await api.searchEvents(filter, limit: pageSize, before: before) },
                facets: { filter in try await api.eventFacets(filter) },
                ask: { question in try await api.askEvents(question) },
                semanticStatus: { try await api.semanticStatus() },
                semanticSearch: { request in try await api.semanticSearch(request) },
                semanticBackfill: { start in
                    if start { _ = try await api.startSemanticBackfill() } else { _ = try await api.cancelSemanticBackfill() }
                }
            )
        }

        public static let missing = SentinelAPIError.server(status: 404, code: "not_found", message: "Not found.")
    }

    @Published public var filter = SearchFilter()
    @Published public private(set) var events: [V2.Event] = []
    @Published public private(set) var nextCursor: String?
    @Published public private(set) var isLoading = false
    @Published public private(set) var hasSearched = false
    @Published public private(set) var error: String?
    @Published public private(set) var facets: V2.EventFacets?

    // Natural language
    @Published public var question = ""
    @Published public private(set) var isAsking = false
    /// The question the filter bar was last filled from ("Understood … as the filters below").
    @Published public private(set) var understood: String?
    @Published public private(set) var askFailure: AskFailure?

    // Semantic search ("Describe it", wave 16)
    @Published public var describeText = ""
    @Published public private(set) var semanticStatus: V2.SemanticStatus?
    /// Non-nil while the grid shows "Describe it" results instead of the filtered events.
    @Published public private(set) var semanticHits: [V2.SemanticHit]?
    /// The description those results are for.
    @Published public private(set) var semanticQuery = ""
    @Published public private(set) var semanticError: String?
    @Published public private(set) var isDescribing = false

    private let backend: Backend
    /// Bumped by every new search so a slow, superseded page is dropped.
    private var generation = 0
    /// The filter whose first page is on screen (so a no-op change doesn't re-search).
    private var shownFilter: SearchFilter?
    private var facetKey: String?

    public init(backend: Backend) {
        self.backend = backend
    }

    // MARK: Derived

    /// The server says AI is on and a question hasn't since shown it off.
    public var askAvailable: Bool {
        guard facets?.aiEnabled == true else { return false }
        if case .aiOff = askFailure { return false }
        return true
    }

    /// Set while the daily limit is reached: the field stays but is disabled.
    public var askLimitMessage: String? {
        if case .limitReached(let message) = askFailure { return message }
        return nil
    }

    public var hasMore: Bool { nextCursor != nil }

    public var semanticAvailability: (enabled: Bool, help: String) { SemanticLogic.availability(semanticStatus) }

    /// Bar fractions for the semantic hits, relative to the best one.
    public var semanticBars: [Double] { SemanticLogic.scoreBars((semanticHits ?? []).map(\.score)) }

    /// The name on the "person:" chip: from a loaded event, else a placeholder.
    public var personLabel: String {
        guard filter.person.isEmpty == false else { return "" }
        let all = events + (semanticHits ?? []).map(\.event)
        return all.first { $0.personId == filter.person }?.personName ?? "this person"
    }

    public var kindChips: [FacetChip] { FacetChips.merge(facets?.kinds ?? [], selected: filter.kinds) }
    public var labelChips: [FacetChip] { FacetChips.merge(facets?.labels ?? [], selected: filter.labels, limit: 12) }
    public var threatTotal: Int { FacetChips.threatTotal(facets?.threats ?? []) }

    // MARK: Searching

    /// Runs the first page for the current filter. Skips when that page is
    /// already shown unless `force`.
    public func search(force: Bool = false, now: Date = Date()) async {
        if force == false, hasSearched, shownFilter == filter, error == nil { return }
        generation += 1
        let mine = generation
        let searched = filter
        isLoading = true
        error = nil
        do {
            let page = try await backend.search(searched, nil)
            guard mine == generation else { return }
            events = page.items
            nextCursor = page.nextCursor
            shownFilter = searched
            hasSearched = true
        } catch {
            guard mine == generation else { return }
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    /// The next page, on the previous page's cursor.
    public func loadMore() async {
        guard let cursor = nextCursor, isLoading == false, let shown = shownFilter else { return }
        let mine = generation
        isLoading = true
        do {
            let page = try await backend.search(shown, cursor)
            guard mine == generation else { return }
            let known = Set(events.map(\.id))
            events += page.items.filter { known.contains($0.id) == false }
            nextCursor = page.nextCursor
        } catch {
            guard mine == generation else { return }
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    /// Fetches facet counts when the cameras or window changed since the last fetch.
    public func loadFacets(force: Bool = false) async {
        let key = filter.facetKey
        if force == false, key == facetKey, facets != nil { return }
        facetKey = key
        do {
            facets = try await backend.facets(filter)
        } catch {
            // The chips just lose their counts.
        }
    }

    public func clearFilters() {
        filter = filter.cleared
        understood = nil
    }

    // MARK: Same object (wave 16)

    /// Every detection of the event's tracked object, at any time.
    public func showSameObject(_ event: V2.Event) {
        guard let track = event.trackId, track.isEmpty == false else { return }
        closeDescribe()
        filter = SearchFilter.sameObject(track: track)
        understood = nil
    }

    // MARK: Semantic search (wave 16)

    /// Whether semantic search is available. A server without it reads as unavailable.
    public func loadSemanticStatus() async {
        do {
            semanticStatus = try await backend.semanticStatus()
        } catch {
            semanticStatus = V2.SemanticStatus(available: false, reason: SemanticLogic.failure(error).unavailable
                ? "this server does not offer semantic search" : error.localizedDescription)
        }
    }

    /// Posts the description, scoped by the filter bar's time range and cameras.
    public func describe(now: Date = Date()) async {
        let text = describeText.trimmingCharacters(in: .whitespacesAndNewlines)
        if let problem = SemanticLogic.textProblem(text) {
            semanticError = problem
            return
        }
        guard semanticAvailability.enabled, isDescribing == false else { return }
        isDescribing = true
        semanticError = nil
        do {
            let res = try await backend.semanticSearch(SemanticLogic.request(text, filter: filter, now: now))
            semanticHits = res.items
            semanticQuery = text
        } catch {
            let f = SemanticLogic.failure(error)
            if f.unavailable {
                let reason = f.text.replacingOccurrences(of: "Semantic search is unavailable: ", with: "")
                semanticStatus = V2.SemanticStatus(available: false, reason: reason)
            }
            semanticError = f.text
        }
        isDescribing = false
    }

    /// Back to the filtered results.
    public func closeDescribe() {
        semanticHits = nil
        semanticQuery = ""
        semanticError = nil
    }

    /// Starts or cancels the semantic index job (changeSettings), then refreshes the status.
    public func indexSemantic(start: Bool) async {
        semanticError = nil
        do {
            try await backend.semanticBackfill(start)
            semanticStatus = try await backend.semanticStatus()
        } catch {
            semanticError = SemanticLogic.failure(error).text
        }
    }

    // MARK: Natural language

    /// Posts the question to `/events/ask` and fills the filter bar with the
    /// filter the server understood; the answer is the first page.
    public func ask() async {
        let text = question.trimmingCharacters(in: .whitespacesAndNewlines)
        guard text.isEmpty == false, isAsking == false, askAvailable, askLimitMessage == nil else { return }
        isAsking = true
        error = nil
        do {
            let answer = try await backend.ask(text)
            generation += 1   // drop any search in flight
            let next = SearchFilter.from(answer.filter)
            filter = next
            shownFilter = next
            events = answer.items
            nextCursor = answer.nextCursor
            hasSearched = true
            isLoading = false
            understood = text
            askFailure = nil
        } catch {
            askFailure = AskFailure(error)
        }
        isAsking = false
    }
}
