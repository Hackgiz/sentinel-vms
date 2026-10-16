package server

// API v2: shared views and their folders (api/openapi.yaml, tag "views").
// A view is private to its owner unless it is shared; shared views live in
// the Shared tree, optionally inside folders (view_groups), and are written
// only by roles holding auth.ManageViews. Camera scope is not applied to a
// shared view's layout: a viewer without access to one of its cameras sees
// a "no access" tile (the client decides), the server filters nothing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2ViewGroups(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/view-groups", auth.ViewLive, s.handleV2ViewGroups)
	s.v2Route(mux, "POST /api/v2/view-groups", auth.ManageViews, s.handleV2SaveViewGroup)
	s.v2Route(mux, "PUT /api/v2/view-groups/{groupId}", auth.ManageViews, s.handleV2SaveViewGroup)
	s.v2Route(mux, "DELETE /api/v2/view-groups/{groupId}", auth.ManageViews, s.handleV2DeleteViewGroup)
}

type v2ViewGroupInput struct {
	Name      string `json:"name"`
	ParentID  string `json:"parentId"`
	SortOrder int    `json:"sortOrder"`
}

type v2ViewGroup struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentId"`
	Name      string `json:"name"`
	SortOrder int    `json:"sortOrder"`
}

func toV2ViewGroup(g *store.ViewGroup) v2ViewGroup {
	return v2ViewGroup{ID: g.ID, ParentID: g.ParentID, Name: g.Name, SortOrder: g.SortOrder}
}

// v2ViewInput is ViewInput: the layout plus where a shared view sits.
type v2ViewInput struct {
	v2ViewLayout
	Shared    bool   `json:"shared"`
	GroupID   string `json:"groupId"`
	SortOrder int    `json:"sortOrder"`
}

// handleV2ViewGroups serves GET /view-groups: every folder (flat, with
// parentId; the client nests them) and every shared view.
func (s *Server) handleV2ViewGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.ViewGroups(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	shared, err := s.Store.SharedViews(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	gs := make([]v2ViewGroup, 0, len(groups))
	for i := range groups {
		gs = append(gs, toV2ViewGroup(&groups[i]))
	}
	vs := make([]v2View, 0, len(shared))
	for i := range shared {
		vs = append(vs, toV2View(&shared[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": gs, "views": vs})
}

func (s *Server) handleV2SaveViewGroup(w http.ResponseWriter, r *http.Request) {
	var in v2ViewGroupInput
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "name must be 1 to 100 characters")
		return
	}
	id := r.PathValue("groupId")
	g, err := s.Store.SaveViewGroup(r.Context(), id, in.ParentID, in.Name, in.SortOrder)
	if err != nil {
		if errors.Is(err, store.ErrViewGroupParent) {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "that parent folder can't hold this folder")
			return
		}
		writeV2Fail(w, err, "folder")
		return
	}
	status, action := http.StatusOK, "Updated view folder"
	if id == "" {
		status, action = http.StatusCreated, "Created view folder"
	}
	s.audit(r, "Views", action, g.Name)
	writeJSON(w, status, toV2ViewGroup(g))
}

func (s *Server) handleV2DeleteViewGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.ViewGroup(r.Context(), r.PathValue("groupId"))
	if err != nil {
		writeV2Fail(w, err, "folder")
		return
	}
	if err := s.Store.DeleteViewGroup(r.Context(), g.ID); err != nil {
		if errors.Is(err, store.ErrViewGroupNotEmpty) {
			writeV2Error(w, http.StatusConflict, codeConflict, "move or delete what is in the folder first")
			return
		}
		writeV2Fail(w, err, "folder")
		return
	}
	s.audit(r, "Views", "Deleted view folder", g.Name)
	w.WriteHeader(http.StatusNoContent)
}

func forbidSharedViews(w http.ResponseWriter) {
	writeV2Error(w, http.StatusForbidden, codeForbidden, "managing shared views needs a role that can manage cameras")
}

// handleV2SaveView serves POST /views (create) and PUT /views/{viewId}.
// Private views are the owner's to write. Making a view shared, placing it
// in a folder, or changing one that is shared needs auth.ManageViews.
func (s *Server) handleV2SaveView(w http.ResponseWriter, r *http.Request) {
	var in v2ViewInput
	if !readV2JSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	manage := s.can(u.Role, auth.ManageViews)
	id := r.PathValue("viewId")
	var existing *store.SavedView
	if id != "" {
		v, err := s.Store.ViewByID(r.Context(), id)
		if err != nil || (!v.Shared && v.UserID != u.ID) {
			writeV2Error(w, http.StatusNotFound, codeNotFound, "view not found")
			return
		}
		existing = v
	}
	if (in.Shared || in.GroupID != "" || (existing != nil && existing.Shared)) && !manage {
		forbidSharedViews(w)
		return
	}
	if !in.Shared && (in.GroupID != "" || in.SortOrder != 0) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "only shared views can sit in a folder or be ordered")
		return
	}
	if err := s.checkViewLayout(r, &in.v2ViewLayout); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	layout, err := json.Marshal(in.v2ViewLayout)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v, err := s.Store.SaveViewWrite(r.Context(), u.ID, id, store.ViewWrite{Name: in.Name, Layout: string(layout), GroupID: in.GroupID, Shared: in.Shared, SortOrder: in.SortOrder})
	if err != nil {
		if errors.Is(err, store.ErrViewGroupMissing) {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "no such folder")
			return
		}
		writeV2Fail(w, err, "view")
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	if v.Shared || (existing != nil && existing.Shared) {
		action := "Saved shared view"
		if existing != nil && existing.Shared && !v.Shared {
			action = "Unshared view"
		}
		s.audit(r, "Views", action, fmt.Sprintf("%s (%dx%d)", v.Name, in.Columns, in.Rows))
	}
	writeJSON(w, status, toV2View(v))
}

func (s *Server) handleV2DeleteView(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	v, err := s.Store.ViewByID(r.Context(), r.PathValue("viewId"))
	if err != nil || (!v.Shared && v.UserID != u.ID) {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "view not found")
		return
	}
	if v.Shared && !s.can(u.Role, auth.ManageViews) {
		forbidSharedViews(w)
		return
	}
	if err := s.Store.DeleteViewByID(r.Context(), v.ID); err != nil {
		writeV2Fail(w, err, "view")
		return
	}
	if v.Shared {
		s.audit(r, "Views", "Deleted shared view", v.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}
