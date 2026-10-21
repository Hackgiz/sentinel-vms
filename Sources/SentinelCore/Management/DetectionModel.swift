import Foundation

// The view-free logic behind the Management window's People, Plates and
// Detection panes and the search workspace's "Describe it" row (wave 17).
// It mirrors the web's `admin/peopleModel.ts`, `admin/platesModel.ts`,
// `pages/detectionModel.ts`, `operator/semanticModel.ts` and
// `shared/alarmKind.ts`, so every client words things the same way.

/// One choice of what a match does to the alarm.
public struct WatchAlertChoice: Equatable, Identifiable, Sendable {
    public var value: V2.WatchAlert
    public var label: String
    public var help: String
    public var id: String { value.rawValue }
}

/// Wording and errors shared by the detection panes.
public enum DetectionText {
    /// The error line for a detection pane. A 404 on the status call means
    /// the server predates wave 16; anything else is the server's message.
    public static func errorText(_ error: Error, feature: String) -> String {
        if let api = error as? SentinelAPIError, api.isNotFound {
            return "This server doesn't offer \(feature). Update Sentinel on the Management Server to use it."
        }
        return (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }

    /// "Just now", "5 min ago", "3 h ago", "2 days ago", or a date past a week.
    public static func ago(_ t: UnixMillis?, now: Date) -> String {
        guard let t, t > 0 else { return "Never" }
        let s = max(0, Int64((Double(now.unixMillis - t) / 1000).rounded()))
        if s < 60 { return "Just now" }
        if s < 3600 { return "\(s / 60) min ago" }
        if s < 86_400 { return "\(s / 3600) h ago" }
        let d = s / 86_400
        if d < 7 { return "\(d) day\(d == 1 ? "" : "s") ago" }
        return dateFormatter.string(from: Date(unixMillis: t))
    }

    /// "3 h ago · Front door", or "Never".
    public static func lastSeen(_ at: UnixMillis?, cameraId: String?, cameraName: (String) -> String?, now: Date) -> String {
        guard let at, at > 0 else { return "Never" }
        if let id = cameraId, let cam = cameraName(id), cam.isEmpty == false { return "\(ago(at, now: now)) · \(cam)" }
        return ago(at, now: now)
    }

    private static let dateFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .short
        f.timeStyle = .none
        return f
    }()
}

// MARK: - People (faces)

public enum PeopleLogic {
    public static let privacyNote = "Face recognition stores a numeric face vector and a small face crop for every face it sees, on this server only. Faces that match nobody are deleted after the days below. Deleting a person deletes their faces. Some places regulate biometric data; check what applies to you. Sentinel does not give legal advice."

    public static let alertChoices: [WatchAlertChoice] = [
        .init(value: .default, label: "Name them in the alarm", help: "The Person alarm keeps its severity; the name goes into its detail."),
        .init(value: .never, label: "Don't alarm (known person)", help: "The alarm is closed with a note, so family and staff don't raise alarms."),
        .init(value: .always, label: "Critical watchlist alarm", help: "A Critical Watchlist alarm is raised whenever they are seen.")
    ]

    public static func alertLabel(_ a: V2.WatchAlert?) -> String {
        let v = a ?? .default
        return alertChoices.first { $0.value == v }?.label ?? v.rawValue
    }

    public static let daysDefault = 7
    public static let daysMin = 1
    public static let daysMax = 90

    /// Clamps the unassigned-face retention to what the server accepts.
    public static func clampDays(_ v: Double) -> Int {
        guard v.isFinite else { return daysDefault }
        return min(daysMax, max(daysMin, Int(v.rounded())))
    }

    /// One line for the switch: on/off, counts, or why it can't run.
    public static func statusLine(_ s: V2.FacesStatus) -> String {
        if s.available == false {
            let reason = s.reason?.isEmpty == false ? s.reason! : "the face models are not installed"
            return "Unavailable: \(reason). Download \"Faces (find)\" and \"Faces (recognize)\" under System → Models."
        }
        if s.enabled == false { return "Off. Turn it on to recognize the people you add here." }
        func n(_ v: Int, _ one: String, _ many: String) -> String { "\(v) \(v == 1 ? one : many)" }
        return "On · \(n(s.people, "person", "people")) · \(n(s.unassigned, "unassigned face", "unassigned faces"))"
    }

    /// The switch can be turned on only when the models are there; it can always be turned off.
    public static func canToggle(_ s: V2.FacesStatus) -> Bool { s.enabled || s.available }

    /// People by name, case-insensitive.
    public static func sorted(_ people: [V2.Person]) -> [V2.Person] {
        people.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    public struct Form: Equatable, Sendable {
        public var name: String
        public var notes: String
        public var alert: V2.WatchAlert

        public init(name: String = "", notes: String = "", alert: V2.WatchAlert = .default) {
            self.name = name
            self.notes = notes
            self.alert = alert
        }

        public init(_ p: V2.Person?) {
            self.init(name: p?.name ?? "", notes: p?.notes ?? "", alert: p?.alert ?? .default)
        }
    }

    /// What's wrong with the form, or nil.
    public static func problem(_ f: Form) -> String? {
        let name = f.name.trimmingCharacters(in: .whitespacesAndNewlines)
        if name.isEmpty { return "Enter a name." }
        if name.count > 80 { return "Use 80 characters or fewer for the name." }
        if f.notes.count > 500 { return "Use 500 characters or fewer for notes." }
        return nil
    }

    /// The POST body for a new person, enrolling `faceId` when given.
    public static func input(_ f: Form, faceId: String? = nil) -> V2.PersonInput {
        V2.PersonInput(name: f.name.trimmingCharacters(in: .whitespacesAndNewlines),
                       notes: f.notes.trimmingCharacters(in: .whitespacesAndNewlines), alert: f.alert, faceId: faceId)
    }

    /// Only the fields that changed (PATCH).
    public static func patch(_ p: V2.Person, _ f: Form) -> V2.PersonInput {
        let next = input(f)
        var out = V2.PersonInput()
        if next.name != p.name { out.name = next.name }
        if next.notes != (p.notes ?? "") { out.notes = next.notes }
        if next.alert != p.alert { out.alert = next.alert }
        return out
    }
}

// MARK: - Plates

public enum PlateLogic {
    public static let minLength = 2
    public static let maxLength = 10

    /// Upper-case, A–Z and 0–9 only: what the server stores and matches.
    public static func normalize(_ s: String) -> String {
        String(s.uppercased().unicodeScalars.filter { ("A"..."Z").contains($0) || ("0"..."9").contains($0) }.map(Character.init))
    }

    public static let alertChoices: [WatchAlertChoice] = [
        .init(value: .default, label: "Put the plate in the alarm", help: "The Vehicle alarm keeps its severity; the name or plate goes into its detail."),
        .init(value: .never, label: "Don't alarm (known vehicle)", help: "The alarm is closed with a note, for your own cars and regular deliveries."),
        .init(value: .always, label: "Critical watchlist alarm", help: "A Critical Watchlist alarm is raised whenever the plate is read.")
    ]

    public static func alertLabel(_ a: V2.WatchAlert?) -> String {
        let v = a ?? .default
        return alertChoices.first { $0.value == v }?.label ?? v.rawValue
    }

    /// One line for the switch: on/off, counts, or why it can't run.
    public static func statusLine(_ s: V2.LPRStatus) -> String {
        if s.available == false {
            let reason = s.reason?.isEmpty == false ? s.reason! : "the plate models are not installed"
            return "Unavailable: \(reason). Download \"Plates (find)\" and \"Plates (read)\" under System → Models."
        }
        if s.enabled == false { return "Off. Turn it on to read plates on vehicle events." }
        let read = s.readLast24h ?? 0
        return "On · \(s.plates) on the watchlist · \(read) plate\(read == 1 ? "" : "s") read in the last 24 h"
    }

    public static func canToggle(_ s: V2.LPRStatus) -> Bool { s.enabled || s.available }

    public struct Form: Equatable, Sendable {
        public var plate: String
        public var name: String
        public var notes: String
        public var alert: V2.WatchAlert

        public init(plate: String = "", name: String = "", notes: String = "", alert: V2.WatchAlert = .default) {
            self.plate = plate
            self.name = name
            self.notes = notes
            self.alert = alert
        }

        public init(_ p: V2.Plate?) {
            self.init(plate: p?.plate ?? "", name: p?.name ?? "", notes: p?.notes ?? "", alert: p?.alert ?? .default)
        }
    }

    /// What's wrong with the form, or nil. `existing` catches duplicates before the server's 409.
    public static func problem(_ f: Form, existing: [V2.Plate] = [], editingID: String? = nil) -> String? {
        let n = normalize(f.plate)
        if n.count < minLength { return "Enter the plate: at least 2 letters or digits." }
        if n.count > maxLength { return "A plate has at most 10 letters and digits." }
        if existing.contains(where: { $0.plate == n && $0.id != editingID }) { return "\(n) is already on the watchlist." }
        if f.name.trimmingCharacters(in: .whitespacesAndNewlines).count > 80 { return "Use 80 characters or fewer for the name." }
        if f.notes.count > 500 { return "Use 500 characters or fewer for notes." }
        return nil
    }

    /// The hint under the plate field.
    public static func plateHint(_ f: Form, editing: Bool) -> String {
        if editing { return "To change the plate, remove it and add the new one." }
        let n = normalize(f.plate)
        if n.isEmpty == false, n != f.plate { return "Saved as \(n) (letters and digits only)." }
        return "Letters and digits; spaces and dashes are ignored."
    }

    /// The POST body for a new entry.
    public static func input(_ f: Form) -> V2.PlateInput {
        V2.PlateInput(plate: normalize(f.plate), name: f.name.trimmingCharacters(in: .whitespacesAndNewlines),
                      notes: f.notes.trimmingCharacters(in: .whitespacesAndNewlines), alert: f.alert)
    }

    /// PATCH: name, notes and alert only (the plate itself is fixed once listed).
    public static func patch(_ p: V2.Plate, _ f: Form) -> V2.PlateInput {
        var out = V2.PlateInput()
        let name = f.name.trimmingCharacters(in: .whitespacesAndNewlines)
        let notes = f.notes.trimmingCharacters(in: .whitespacesAndNewlines)
        if name != (p.name ?? "") { out.name = name }
        if notes != (p.notes ?? "") { out.notes = notes }
        if f.alert != p.alert { out.alert = f.alert }
        return out
    }

    public static func sorted(_ list: [V2.Plate]) -> [V2.Plate] { list.sorted { $0.plate < $1.plate } }

    /// Filter by plate (normalized substring) or name.
    public static func filter(_ list: [V2.Plate], _ q: String) -> [V2.Plate] {
        let t = q.trimmingCharacters(in: .whitespacesAndNewlines)
        guard t.isEmpty == false else { return list }
        let n = normalize(t)
        return list.filter { (n.isEmpty == false && $0.plate.contains(n)) || ($0.name ?? "").localizedCaseInsensitiveContains(t) }
    }

    /// "AB12 · Delivery van", or the plate alone.
    public static func title(_ p: V2.Plate) -> String {
        if let name = p.name, name.isEmpty == false { return "\(p.plate) · \(name)" }
        return p.plate
    }
}

// MARK: - Detection (models and detector)

public enum DetectionLogic {
    public static let defaultDetector = "yolox-tiny"

    /// Purposes in catalog order, with their labels.
    public static let purposes: [(purpose: V2.ModelPurpose, label: String)] = [
        (.detector, "Object detection"),
        (.reid, "Appearance search"),
        (.audio, "Audio classes"),
        (.faceDetect, "Faces (find)"),
        (.faceEmbed, "Faces (recognize)"),
        (.plateDet, "Plates (find)"),
        (.plateRec, "Plates (read)"),
        (.clipImage, "Semantic search (images)"),
        (.clipText, "Semantic search (text)")
    ]

    public static func purposeLabel(_ p: V2.ModelPurpose) -> String {
        purposes.first { $0.purpose == p }?.label ?? p.rawValue
    }

    /// The saved model id, "" meaning the bundled default.
    public static func savedModel(_ s: V2.DetectorSettings) -> String {
        s.model.isEmpty ? defaultDetector : s.model
    }

    public struct DetectorChoice: Equatable, Identifiable, Sendable {
        public var id: String
        public var title: String
        public var note: String
        /// False only for the saved model when its file is gone (shown so the picker is honest).
        public var installed: Bool
    }

    /// Installed detector models; the saved one is listed even when it is missing.
    public static func detectorChoices(_ models: [V2.Model], saved: String) -> [DetectorChoice] {
        var out = models
            .filter { $0.purpose == .detector && ($0.installed || $0.id == saved) }
            .map { DetectorChoice(id: $0.id, title: $0.title, note: $0.note ?? "", installed: $0.installed) }
        if out.contains(where: { $0.id == saved }) == false {
            out.insert(DetectorChoice(id: saved, title: saved, note: "", installed: false), at: 0)
        }
        return out
    }

    public static let rateOptions: [(value: Int, label: String)] =
        [(0, "Auto (1/s on the CPU, 5/s with an accelerator)")] + (1...5).map { ($0, "\($0) per second") }

    /// The status line: model, engine, device, effective rate, inference time.
    public static func statusLine(_ s: V2.DetectorSettings, models: [V2.Model]) -> String {
        let st = s.status
        let title = models.first { $0.id == st.model }?.title ?? (st.model.isEmpty ? defaultDetector : st.model)
        var engine = st.engine
        if let device = st.device, device.isEmpty == false { engine += " (\(device))" }
        var parts = [title, engine, "\(st.effectiveRate)/s per camera"]
        if let ms = st.inferMs, ms > 0 { parts.append(String(format: "%.1f ms per frame", ms)) }
        return parts.joined(separator: " · ")
    }

    /// The "restart to apply" note, or why the chosen model is not running.
    public static func pendingNote(_ s: V2.DetectorSettings) -> String? {
        if let reason = s.status.reason, reason.isEmpty == false { return "The chosen model is not running: \(reason)." }
        if s.status.pending { return "Restart Sentinel to apply the model change. The rate has already changed." }
        return nil
    }

    /// Download progress 0–1, or nil when unknown or not downloading.
    public static func downloadFraction(_ m: V2.Model) -> Double? {
        guard m.state == .downloading, let d = m.download, d.state == .running, d.total > 0 else { return nil }
        return min(1, max(0, Double(d.done) / Double(d.total)))
    }

    public enum ModelAction: String, CaseIterable, Sendable {
        case download, cancel, delete

        public var label: String {
            switch self {
            case .download: return "Download"
            case .cancel: return "Cancel"
            case .delete: return "Delete"
            }
        }
    }

    /// What a row can do. Bundled models never; the selected detector can't
    /// be deleted; a missing or corrupt file can be downloaded when the
    /// catalog names a source.
    public static func actions(_ m: V2.Model, selected: String, canChange: Bool) -> [ModelAction] {
        guard canChange, m.bundled == false else { return [] }
        if m.state == .downloading { return [.cancel] }
        var out: [ModelAction] = []
        if m.state == .missing || m.state == .corrupt, (m.source ?? "").isEmpty == false { out.append(.download) }
        if m.state == .installed || m.state == .corrupt, !(m.purpose == .detector && m.id == selected) { out.append(.delete) }
        return out
    }

    public enum Tone: Sendable { case good, busy, neutral, bad }

    public static func tone(_ m: V2.Model) -> Tone {
        switch m.state {
        case .installed: return .good
        case .downloading: return .busy
        case .corrupt: return .bad
        default: return .neutral
        }
    }

    public static func stateLabel(_ m: V2.Model) -> String {
        if m.bundled, m.state == .installed { return "Bundled" }
        if m.state == .missing, m.download?.state == .failed { return "Download failed" }
        switch m.state {
        case .installed: return "Installed"
        case .downloading: return "Downloading"
        case .missing: return "Not installed"
        case .corrupt: return "Damaged"
        default: return m.state.rawValue.capitalized
        }
    }

    /// Any download running: the pane polls every 1.5 s while true.
    public static func anyDownloading(_ models: [V2.Model]) -> Bool { models.contains { $0.state == .downloading } }

    /// How often the pane refreshes: quickly while a download runs, slowly otherwise.
    public static func pollInterval(_ models: [V2.Model]) -> TimeInterval { anyDownloading(models) ? 1.5 : 15 }

    /// Catalog order, grouped by purpose (detector first); unknown purposes last.
    public static func sorted(_ models: [V2.Model]) -> [V2.Model] {
        func rank(_ p: V2.ModelPurpose) -> Int { purposes.firstIndex { $0.purpose == p } ?? purposes.count }
        return models.enumerated()
            .sorted { (rank($0.element.purpose), $0.offset) < (rank($1.element.purpose), $1.offset) }
            .map(\.element)
    }

    public static func formatBytes(_ n: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: n, countStyle: .file)
    }

    /// The line under the Models table, naming the directory for hand installs.
    public static func handInstallNote(dir: String) -> String? {
        guard dir.isEmpty == false else { return nil }
        return "To install by hand, put the model file in \(dir) on the management server. It counts once its SHA-256 matches."
    }
}

// MARK: - Semantic search ("Describe it")

public enum SemanticLogic {
    public static let textMin = 2
    public static let textMax = 200
    public static let defaultLimit = 30

    /// Whether "Describe it" can run, with the help text that says why not.
    public static func availability(_ st: V2.SemanticStatus?) -> (enabled: Bool, help: String) {
        guard let st else { return (false, "Checking whether semantic search is available…") }
        if st.available {
            let n = st.indexed ?? 0
            return (true, "Searches snapshots by what they show, on this server. \(n.formatted()) event\(n == 1 ? "" : "s") indexed.")
        }
        let reason = st.reason?.isEmpty == false ? st.reason! : "no semantic search model is installed"
        return (false, "Semantic search is unavailable: \(reason). Download the \"Semantic search\" models under System → Models.")
    }

    /// What's wrong with the text, or nil.
    public static func textProblem(_ text: String) -> String? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.count < textMin { return "Describe what you're looking for in a few words." }
        if t.count > textMax { return "Keep the description under \(textMax) characters." }
        return nil
    }

    /// The request, scoped like the filter bar: its time range and cameras.
    public static func request(_ text: String, filter: SearchFilter, now: Date, limit: Int = defaultLimit) -> V2.SemanticRequest {
        let (from, to) = filter.window(now: now)
        let t = String(text.trimmingCharacters(in: .whitespacesAndNewlines).prefix(textMax))
        return V2.SemanticRequest(text: t, cameras: filter.cameras.isEmpty ? nil : filter.cameras,
                                  from: from > 0 ? from : nil, to: to > 0 ? to : nil,
                                  limit: max(1, min(100, limit)))
    }

    /// Bar fraction (0–1) for each hit. Image-text cosines sit in a narrow
    /// band (about 0.2–0.35), so bars are relative to the best hit; the
    /// number shown is the raw score.
    public static func scoreBars(_ scores: [Double]) -> [Double] {
        let top = scores.filter(\.isFinite).max() ?? 0
        return scores.map { top > 0 && $0.isFinite ? max(0, $0) / top : 0 }
    }

    public static func scoreText(_ score: Double) -> String {
        score.isFinite ? String(format: "%.2f", score) : "—"
    }

    /// What to tell the operator when the search fails; `unavailable` = the model is missing.
    public static func failure(_ error: Error) -> (text: String, unavailable: Bool) {
        let text = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
        if let api = error as? SentinelAPIError {
            if api.isUnavailable {
                let reason = text.replacingOccurrences(of: #"^semantic search is unavailable:\s*"#, with: "",
                                                       options: [.regularExpression, .caseInsensitive])
                return ("Semantic search is unavailable: \(reason)", true)
            }
            if api.isNotFound {
                return ("This server doesn't offer semantic search. Update Sentinel on the Management Server to use it.", true)
            }
        }
        return (text, false)
    }
}

// MARK: - Event chips and alarm kinds

/// A chip under an event's kind: the recognized person or the plate read.
public struct EventChip: Equatable, Sendable {
    public enum Kind: String, Sendable { case person, plate }
    public var kind: Kind
    public var text: String

    public var symbol: String { kind == .person ? "person.crop.circle" : "car.rear" }
    public var help: String { kind == .person ? "Recognized person" : "Plate read" }

    /// The person's name and the plate, in display order.
    public static func chips(for e: V2.Event) -> [EventChip] {
        var out: [EventChip] = []
        if let n = e.personName?.trimmingCharacters(in: .whitespaces), n.isEmpty == false { out.append(.init(kind: .person, text: n)) }
        if let p = e.plate?.trimmingCharacters(in: .whitespaces), p.isEmpty == false { out.append(.init(kind: .plate, text: p)) }
        return out
    }
}

/// Symbols for alarm kinds that need to stand out in a list (wave 16: the
/// Watchlist alarm). Other kinds show text only, as before.
public enum AlarmKindStyle {
    public static let watchlist = "Watchlist"

    public static func symbol(_ kind: String) -> String? {
        kind == watchlist ? "flag.fill" : nil
    }
}
