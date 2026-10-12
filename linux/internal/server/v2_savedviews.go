package server

// API v2: saved views (api/openapi.yaml, tag "views"): each user's named
// camera layouts for Sentinel Operator. A view is a grid of columns × rows
// with tiles placed on it; tiles may only show cameras the user may see.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const (
	maxViewGrid  = 8
	maxViewTiles = 64
)

func (s *Server) registerV2SavedViews(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/views", auth.ViewLive, s.handleV2SavedViews)
	s.v2Route(mux, "POST /api/v2/views", auth.ViewLive, s.handleV2SaveView)
	s.v2Route(mux, "PUT /api/v2/views/{viewId}", auth.ViewLive, s.handleV2SaveView)
	s.v2Route(mux, "DELETE /api/v2/views/{viewId}", auth.ViewLive, s.handleV2DeleteView)
}

type v2ViewTile struct {
	CameraID string `json:"cameraId"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	W        *int   `json:"w,omitempty"` // absent = 1
	H        *int   `json:"h,omitempty"`
}

// v2ViewLayout is ViewInput (and what the store keeps as the layout).
type v2ViewLayout struct {
	Name    string       `json:"name"`
	Columns int          `json:"columns"`
	Rows    int          `json:"rows"`
	Tiles   []v2ViewTile `json:"tiles"`
}

type v2View struct {
	ID string `json:"id"`
	v2ViewLayout
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

func toV2View(v *store.SavedView) v2View {
	out := v2View{ID: v.ID, CreatedAt: secToMs(v.CreatedAt), UpdatedAt: secToMs(v.UpdatedAt)}
	_ = json.Unmarshal([]byte(v.Layout), &out.v2ViewLayout)
	out.Name = v.Name
	if out.Tiles == nil {
		out.Tiles = []v2ViewTile{}
	}
	return out
}

func (s *Server) handleV2SavedViews(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.SavedViews(r.Context(), currentUser(r).ID)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2View, 0, len(list))
	for i := range list {
		out = append(out, toV2View(&list[i]))
	}
	v2Items(w, out)
}

// handleV2SaveView serves POST /views (create) and PUT /views/{viewId}.
func (s *Server) handleV2SaveView(w http.ResponseWriter, r *http.Request) {
	var in v2ViewLayout
	if !readV2JSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	if err := s.checkViewLayout(r, &in); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	layout, err := json.Marshal(in)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	id := r.PathValue("viewId")
	v, err := s.Store.SaveView(r.Context(), u.ID, id, in.Name, string(layout))
	if err != nil {
		writeV2Fail(w, err, "view")
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, toV2View(v))
}

func (s *Server) handleV2DeleteView(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteSavedView(r.Context(), currentUser(r).ID, r.PathValue("viewId")); err != nil {
		writeV2Fail(w, err, "view")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkViewLayout validates a view: a name, a grid of 1…8 × 1…8, at most
// 64 tiles that lie within the grid and show cameras the user may see.
func (s *Server) checkViewLayout(r *http.Request, in *v2ViewLayout) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return fmt.Errorf("name must be 1 to 100 characters")
	}
	if in.Columns < 1 || in.Columns > maxViewGrid || in.Rows < 1 || in.Rows > maxViewGrid {
		return fmt.Errorf("columns and rows must be from 1 to %d", maxViewGrid)
	}
	if in.Tiles == nil {
		return fmt.Errorf("send tiles (an empty list for an empty view)")
	}
	if len(in.Tiles) > maxViewTiles {
		return fmt.Errorf("at most %d tiles", maxViewTiles)
	}
	scope := s.cameraScope(r.Context()) // store.VisibleCameraIDs, per request
	for i := range in.Tiles {
		t := &in.Tiles[i]
		one := 1
		if t.W == nil {
			t.W = &one
		}
		if t.H == nil {
			t.H = &one
		}
		if t.X < 0 || t.Y < 0 || *t.W < 1 || *t.H < 1 || t.X+*t.W > in.Columns || t.Y+*t.H > in.Rows {
			return fmt.Errorf("tile %d doesn't fit the %d × %d grid", i+1, in.Columns, in.Rows)
		}
		if _, err := s.Store.Camera(r.Context(), t.CameraID); err != nil || !scope.camera(t.CameraID) {
			return fmt.Errorf("tile %d: no camera %q", i+1, t.CameraID)
		}
	}
	return nil
}
