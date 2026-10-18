package server

// API v2: timeline bookmarks (WAVE7-FEATURES.md, Stream B). Anyone who may
// play back a camera's footage (the viewLive permission) can list and add
// bookmarks on it; they follow the camera's scope (404 outside it). Editing
// or deleting someone else's bookmark needs a manager: a role that can
// manage cameras or read the audit trail (Admin, Supervisor, or a custom
// role holding either). Everything that changes a bookmark is audited.

import (
	"errors"
	"net/http"
	"strconv"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Bookmarks(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}/bookmarks", auth.ViewLive, s.inScope("cameraId", s.handleV2Bookmarks))
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/bookmarks", auth.ViewLive, s.inScope("cameraId", s.handleV2AddBookmark))
	s.v2Route(mux, "PATCH /api/v2/bookmarks/{bookmarkId}", auth.ViewLive, s.handleV2UpdateBookmark)
	s.v2Route(mux, "DELETE /api/v2/bookmarks/{bookmarkId}", auth.ViewLive, s.handleV2DeleteBookmark)
}

type v2Bookmark struct {
	ID         string `json:"id"`
	CameraID   string `json:"cameraId"`
	AtMs       int64  `json:"atMs"`
	DurationMs int64  `json:"durationMs"`
	Title      string `json:"title"`
	Note       string `json:"note"`
	Color      string `json:"color"`
	CreatedBy  string `json:"createdBy"`
	CreatedAt  int64  `json:"createdAt"`
	CanEdit    bool   `json:"canEdit"`
}

func (s *Server) isBookmarkManager(u *store.User) bool {
	return s.can(u.Role, auth.ManageCameras) || s.can(u.Role, auth.ViewAudit)
}

func (s *Server) bookmarkView(u *store.User, b *store.Bookmark) v2Bookmark {
	return v2Bookmark{ID: b.ID, CameraID: b.CameraID, AtMs: b.AtMs, DurationMs: b.DurationMs, Title: b.Title, Note: b.Note,
		Color: b.Color, CreatedBy: b.CreatedBy, CreatedAt: secToMs(b.CreatedAt), CanEdit: b.CreatedBy == u.ID || s.isBookmarkManager(u)}
}

// bookmarkFail answers a store error: a bad field is 400.
func bookmarkFail(w http.ResponseWriter, err error) {
	var bad store.BookmarkInvalidError
	if errors.As(err, &bad) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, bad.Msg)
		return
	}
	writeV2Fail(w, err, "bookmark")
}

func (s *Server) handleV2Bookmarks(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := r.URL.Query()
	from, ok := parseMs(w, q.Get("from"), "from")
	if !ok {
		return
	}
	to, ok := parseMs(w, q.Get("to"), "to")
	if !ok {
		return
	}
	list, err := s.Store.Bookmarks(r.Context(), r.PathValue("cameraId"), from, to)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2Bookmark, 0, len(list))
	for i := range list {
		out = append(out, s.bookmarkView(u, &list[i]))
	}
	v2Items(w, out)
}

func (s *Server) handleV2AddBookmark(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var in struct {
		AtMs       int64  `json:"atMs"`
		DurationMs int64  `json:"durationMs"`
		Title      string `json:"title"`
		Note       string `json:"note"`
		Color      string `json:"color"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	b := &store.Bookmark{CameraID: r.PathValue("cameraId"), AtMs: in.AtMs, DurationMs: in.DurationMs, Title: in.Title,
		Note: in.Note, Color: in.Color, CreatedBy: u.ID}
	if err := s.Store.AddBookmark(r.Context(), b); err != nil {
		bookmarkFail(w, err)
		return
	}
	s.audit(r, "Recordings", "Added bookmark", s.bookmarkDetail(r, b))
	writeJSON(w, http.StatusCreated, s.bookmarkView(u, b))
}

// ownBookmark loads the bookmark named in the path for a caller who may edit
// it. A bookmark on a camera outside the caller's scope is a 404; someone
// else's bookmark is a 403 unless the caller is a manager.
func (s *Server) ownBookmark(w http.ResponseWriter, r *http.Request) (*store.Bookmark, bool) {
	b, err := s.Store.BookmarkByID(r.Context(), r.PathValue("bookmarkId"))
	if err != nil {
		bookmarkFail(w, err)
		return nil, false
	}
	if !s.cameraScope(r.Context()).camera(b.CameraID) {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "bookmark not found")
		return nil, false
	}
	if u := currentUser(r); b.CreatedBy != u.ID && !s.isBookmarkManager(u) {
		writeV2Error(w, http.StatusForbidden, codeForbidden, "only the person who added a bookmark, or a manager, can change it")
		return nil, false
	}
	return b, true
}

func (s *Server) bookmarkDetail(r *http.Request, b *store.Bookmark) string {
	name := b.CameraID
	if c, err := s.Store.Camera(r.Context(), b.CameraID); err == nil {
		name = c.Name
	}
	title := b.Title
	if title == "" {
		title = "(untitled)"
	}
	return name + ": " + title + " at " + strconv.FormatInt(b.AtMs, 10)
}

func (s *Server) handleV2UpdateBookmark(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AtMs       *int64  `json:"atMs"`
		DurationMs *int64  `json:"durationMs"`
		Title      *string `json:"title"`
		Note       *string `json:"note"`
		Color      *string `json:"color"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	b, ok := s.ownBookmark(w, r)
	if !ok {
		return
	}
	if in.AtMs != nil {
		b.AtMs = *in.AtMs
	}
	if in.DurationMs != nil {
		b.DurationMs = *in.DurationMs
	}
	if in.Title != nil {
		b.Title = *in.Title
	}
	if in.Note != nil {
		b.Note = *in.Note
	}
	if in.Color != nil {
		b.Color = *in.Color
	}
	upd, err := s.Store.UpdateBookmark(r.Context(), *b)
	if err != nil {
		bookmarkFail(w, err)
		return
	}
	s.audit(r, "Recordings", "Edited bookmark", s.bookmarkDetail(r, upd))
	writeJSON(w, http.StatusOK, s.bookmarkView(currentUser(r), upd))
}

func (s *Server) handleV2DeleteBookmark(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBookmark(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteBookmark(r.Context(), b.ID); err != nil {
		bookmarkFail(w, err)
		return
	}
	s.audit(r, "Recordings", "Deleted bookmark", s.bookmarkDetail(r, b))
	w.WriteHeader(http.StatusNoContent)
}
