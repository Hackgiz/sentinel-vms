import AppKit
import SwiftUI
import SentinelCore

/// The bookmarks of one playback session (Wave 11, stream A): the loaded
/// day's bookmarks for the timeline's cameras, the add/edit sheet's draft and
/// the day list's visibility. The rules live in `BookmarkBook` /
/// `BookmarkLogic` (SentinelCore, tested); this only talks to the API.
///
/// One store per `PlaybackController` (`BookmarkStore.for(controller)`), so
/// the shell's **B** key, the ruler glyphs and the day list share it
/// without new stored properties on the controller.
@MainActor
final class BookmarkStore: ObservableObject {
    @Published private(set) var book = BookmarkBook()
    /// The add/edit sheet's content; nil = closed.
    @Published var draft: BookmarkLogic.Draft?
    @Published var showsList = false
    @Published private(set) var isSaving = false
    @Published var sheetError: String?
    private var loadedKey: String?
    private var api: SentinelAPI?

    // MARK: One per controller

    private struct Entry {
        weak var controller: PlaybackController?
        let store: BookmarkStore
    }
    private static var stores: [ObjectIdentifier: Entry] = [:]

    static func `for`(_ controller: PlaybackController) -> BookmarkStore {
        let key = ObjectIdentifier(controller)
        if let entry = stores[key], entry.controller === controller { return entry.store }
        stores = stores.filter { $0.value.controller != nil }
        let store = BookmarkStore()
        stores[key] = Entry(controller: controller, store: store)
        return store
    }

    // MARK: Loading

    /// Loads the bookmarks of `cameraIDs` over `[from, to)`; a repeat of the
    /// same request is skipped unless `force`.
    func load(api: SentinelAPI, cameraIDs: [String], from: UnixMillis, to: UnixMillis, force: Bool = false) async {
        self.api = api
        let key = "\(ObjectIdentifier(api))|\(cameraIDs.joined(separator: ","))|\(from)|\(to)"
        guard force || key != loadedKey else { return }
        loadedKey = key
        var loaded: [String: [V2.Bookmark]] = [:]
        // One camera at a time, like the timelines (at most nine).
        for id in cameraIDs {
            do {
                loaded[id] = try await api.bookmarks(cameraID: id, from: from, to: to - 1)
            } catch {
                loaded[id] = book.bookmarks(for: id)
                SentinelLog.shared.error("bookmarks \(id) failed: \(error)", category: "playback")
            }
        }
        guard loadedKey == key else { return } // a newer load started meanwhile
        book.replace(loaded, from: from, to: to)
    }

    // MARK: Adding and editing

    /// **B**: a new bookmark at the playhead for the focused camera (the
    /// selected tile when it is in playback, else the first). Covers the
    /// marked in/out range when one is set (the sheet can turn that off).
    func beginAdd(cameraID: String?, controller: PlaybackController) {
        guard let camera = Self.focusedCamera(cameraID, in: controller.cameraIDs) else {
            NSSound.beep()
            return
        }
        showsList = false
        sheetError = nil
        draft = BookmarkLogic.Draft(cameraID: camera, at: controller.position, selection: controller.selection.range)
    }

    func beginEdit(_ b: V2.Bookmark) {
        guard b.canEdit else { return }
        showsList = false
        sheetError = nil
        draft = BookmarkLogic.Draft(editing: b)
    }

    static func focusedCamera(_ selected: String?, in ids: [String]) -> String? {
        if let selected, ids.contains(selected) { return selected }
        return ids.first
    }

    /// Saves the draft; true when the sheet can close.
    func save() async -> Bool {
        guard let draft, isSaving == false else { return false }
        if let problem = draft.problem {
            sheetError = problem
            return false
        }
        guard let api else {
            sheetError = "Not connected to the site yet."
            return false
        }
        isSaving = true
        defer { isSaving = false }
        do {
            let saved: V2.Bookmark
            if let id = draft.editingID {
                saved = try await api.patchBookmark(id: id, draft.patch)
            } else {
                saved = try await api.addBookmark(cameraID: draft.cameraID, draft.input)
            }
            book.upsert(saved)
            self.draft = nil
            return true
        } catch {
            sheetError = error.localizedDescription
            return false
        }
    }

    func delete(_ b: V2.Bookmark) async {
        guard let api, b.canEdit else { return }
        do {
            try await api.deleteBookmark(id: b.id)
            book.remove(id: b.id)
        } catch {
            NoticeCenter.shared.post(.error, "Couldn't delete the bookmark: \(error.localizedDescription)", key: "bookmark-delete")
        }
    }
}
