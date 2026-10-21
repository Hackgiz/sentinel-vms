// DescribeSearch.swift
// "Describe it" on the Events screen (WAVE17): local semantic search over
// event snapshots (`POST /api/v2/events/semantic`). The field shows only
// when `GET /api/v2/semantic` says it's available; results replace the
// detections list until cleared. The rules are in DetectionLogic.swift.

import SwiftUI

@MainActor
final class DescribeSearchModel: ObservableObject {
    @Published private(set) var availability: DetectionLogic.SemanticAvailability = .checking
    @Published var text = ""
    /// The description the results are for; nil while the list shows.
    @Published private(set) var query: String?
    @Published private(set) var hits: [V2.SemanticHit] = []
    @Published private(set) var busy = false
    @Published var error: String?

    var showingResults: Bool { query != nil }

    func loadStatus(api: SentinelAPI?) async {
        guard let api else { return }
        do {
            availability = DetectionLogic.availability(try await api.semanticStatus())
        } catch is CancellationError {
        } catch {
            availability = DetectionLogic.availability(failedWith: error)
        }
    }

    func search(api: SentinelAPI?) async {
        guard let api else { return }
        if let problem = DetectionLogic.textProblem(text) {
            error = problem
            return
        }
        let body = DetectionLogic.semanticRequest(text)
        busy = true
        defer { busy = false }
        do {
            let results = try await api.semanticSearch(body)
            hits = results.items
            query = body.text
            error = nil
        } catch is CancellationError {
        } catch {
            let failure = DetectionLogic.semanticError(error)
            self.error = failure.text
            Haptics.warning()
        }
    }

    func clear() {
        query = nil
        hits = []
        error = nil
    }
}

/// The search field with its inline error and the indexed-count note.
struct DescribeSearchField: View {
    @ObservedObject var model: DescribeSearchModel
    let submit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: SentinelTheme.Space.sm) {
                HStack(spacing: 6) {
                    Image(systemName: "text.magnifyingglass")
                        .foregroundStyle(.white.opacity(0.5))
                    TextField("Describe it: a white van in the driveway", text: $model.text)
                        .textInputAutocapitalization(.never)
                        .submitLabel(.search)
                        .onSubmit(submit)
                        .foregroundStyle(.white)
                        .accessibilityLabel("Describe what the snapshot shows")
                    if model.text.isEmpty == false {
                        Button {
                            model.text = ""
                            model.clear()
                        } label: {
                            Image(systemName: "xmark.circle.fill").foregroundStyle(.white.opacity(0.45))
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Clear")
                    }
                }
                .font(.subheadline)
                .padding(.horizontal, 10).padding(.vertical, 9)
                .background(SentinelTheme.panel, in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm).stroke(SentinelTheme.line, lineWidth: 1))

                Button(action: submit) {
                    if model.busy {
                        ProgressView().tint(.white).controlSize(.small)
                    } else {
                        Text("Describe it")
                    }
                }
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(.white)
                .padding(.horizontal, 12).padding(.vertical, 9)
                .background(SentinelTheme.accent.opacity(canSubmit ? 1 : 0.4),
                            in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
                .disabled(canSubmit == false)
            }
            if let error = model.error {
                ActionErrorBanner(message: error) { model.error = nil }
            } else if case let .available(indexed) = model.availability, model.showingResults == false {
                Text(DetectionLogic.indexedNote(indexed))
                    .font(.caption2).foregroundStyle(.white.opacity(0.45))
            }
        }
    }

    private var canSubmit: Bool {
        model.busy == false && model.text.trimmingCharacters(in: .whitespacesAndNewlines).count >= DetectionLogic.textMin
    }
}

/// The matches for a description, best first, each with its score.
struct DescribeResultsList: View {
    @ObservedObject var model: DescribeSearchModel

    var body: some View {
        let bars = DetectionLogic.scoreBars(model.hits)
        ScrollView {
            LazyVStack(alignment: .leading, spacing: SentinelTheme.Space.md) {
                HStack(alignment: .firstTextBaseline) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Snapshots that look like “\(model.query ?? "")”")
                            .font(.subheadline.weight(.semibold)).foregroundStyle(.white)
                        Text("\(model.hits.count) match\(model.hits.count == 1 ? "" : "es")")
                            .font(.caption).foregroundStyle(.white.opacity(0.55))
                    }
                    Spacer()
                    Button("Clear") { Haptics.tap(); model.clear() }
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(SentinelTheme.accent)
                }
                if model.hits.isEmpty {
                    Text("Nothing looks like that. Older events may not be indexed yet.")
                        .font(.caption).foregroundStyle(.white.opacity(0.55))
                        .padding(.top, SentinelTheme.Space.lg)
                }
                ForEach(Array(model.hits.enumerated()), id: \.element.id) { i, hit in
                    NavigationLink(value: hit.event) {
                        EventRow(event: hit.event, match: .init(score: hit.score, bar: bars[i]))
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(SentinelTheme.Space.lg)
        }
    }
}

/// A semantic match's similarity: a bar relative to the best hit, and the raw score.
struct ScoreBar: View {
    let score: Double
    /// 0...1
    let bar: Double

    var body: some View {
        HStack(spacing: 6) {
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(.white.opacity(0.1))
                    Capsule().fill(SentinelTheme.accent).frame(width: geo.size.width * max(0, min(1, bar)))
                }
            }
            .frame(width: 60, height: 5)
            Text(DetectionLogic.scoreText(score))
                .font(.caption2.monospacedDigit().weight(.semibold))
                .foregroundStyle(.white.opacity(0.7))
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Similarity \(DetectionLogic.scoreText(score))")
    }
}
