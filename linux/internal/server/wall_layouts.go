package server

// API v2: video wall layouts (WAVE10-APPLE.md, Stream A; api/openapi.yaml,
// tag "wall-layouts"). A wall layout is a named arrangement of the Mac
// operator app's monitor windows: which display each sits on and what it
// shows (a saved view, a list of cameras, or a tour cycling saved views).
//
//   - Private to its creator unless shared. Sharing a layout, or changing
//     or deleting one that is shared, needs auth.ManageViews (the
//     permission shared saved views use). Someone else's private layout
//     doesn't exist for the caller (404).
//   - Camera scope: writes may only name cameras the writer sees and views
//     the writer may open (400 otherwise); reads leave out of each monitor
//     the cameras outside the reader's scope and the views the reader
//     can't open, so a shared wall never reveals a camera.
//   - Every write is audited (area "Views").

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const (
	maxWallMonitors      = 16
	maxWallMonitorCams   = 64
	maxWallTourViews     = 32
	minWallTourSeconds   = 5
	maxWallTourSeconds   = 3600
	maxWallScreenHintLen = 200
)

func (s *Server) registerV2WallLayouts(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/wall-layouts", auth.ViewLive, s.handleV2WallLayouts)
	s.v2Route(mux, "POST /api/v2/wall-layouts", auth.ViewLive, s.handleV2CreateWallLayout)
	s.v2Route(mux, "GET /api/v2/wall-layouts/{layoutId}", auth.ViewLive, s.handleV2WallLayout)
	s.v2Route(mux, "PATCH /api/v2/wall-layouts/{layoutId}", auth.ViewLive, s.handleV2UpdateWallLayout)
	s.v2Route(mux, "DELETE /api/v2/wall-layouts/{layoutId}", auth.ViewLive, s.handleV2DeleteWallLayout)
}

type v2WallTour struct {
	ViewIDs []string `json:"viewIds"`
	Seconds int      `json:"seconds"`
}

// v2WallMonitor is one monitor window. Exactly what it shows: viewId, or
// cameraIds, or a tour (a tour may sit alongside a viewId: the view it
// starts on).
type v2WallMonitor struct {
	Slot         int         `json:"slot"`
	ScreenHint   string      `json:"screenHint"`
	ScreenIndex  *int        `json:"screenIndex,omitempty"`
	ViewID       string      `json:"viewId,omitempty"`
	CameraIDs    []string    `json:"cameraIds,omitempty"`
	Tour         *v2WallTour `json:"tour,omitempty"`
	AlarmMonitor bool        `json:"alarmMonitor,omitempty"`
	Fullscreen   bool        `json:"fullscreen,omitempty"`
}

// v2WallBody is what the store keeps as a layout's body.
type v2WallBody struct {
	Monitors []v2WallMonitor `json:"monitors"`
}

type v2WallLayout struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Shared    bool            `json:"shared"`
	Monitors  []v2WallMonitor `json:"monitors"`
	CreatedBy string          `json:"createdBy"`
	CreatedAt int64           `json:"createdAt"`
	UpdatedAt int64           `json:"updatedAt"`
	CanEdit   bool            `json:"canEdit"`
}

// wallViewAccess answers whether the caller may open a saved view.
type wallViewAccess func(id string) bool

func (s *Server) wallViewAccessFor(r *http.Request) wallViewAccess {
	u := currentUser(r)
	cache := map[string]bool{}
	return func(id string) bool {
		if ok, seen := cache[id]; seen {
			return ok
		}
		v, err := s.Store.ViewByID(r.Context(), id)
		ok := err == nil && (v.Shared || v.UserID == u.ID)
		cache[id] = ok
		return ok
	}
}

// wallLayoutView converts a stored layout for the caller, filtered to what
// they may see.
func (s *Server) wallLayoutView(r *http.Request, l *store.WallLayout, views wallViewAccess) v2WallLayout {
	u := currentUser(r)
	var body v2WallBody
	_ = json.Unmarshal([]byte(l.Body), &body)
	scope := s.cameraScope(r.Context())
	monitors := make([]v2WallMonitor, 0, len(body.Monitors))
	for _, m := range body.Monitors {
		if m.ViewID != "" && !views(m.ViewID) {
			m.ViewID = ""
		}
		if m.CameraIDs != nil {
			cams := []string{}
			for _, id := range m.CameraIDs {
				if scope.camera(id) {
					cams = append(cams, id)
				}
			}
			m.CameraIDs = cams
		}
		if m.Tour != nil {
			ids := []string{}
			for _, id := range m.Tour.ViewIDs {
				if views(id) {
					ids = append(ids, id)
				}
			}
			t := *m.Tour
			t.ViewIDs = ids
			m.Tour = &t
		}
		monitors = append(monitors, m)
	}
	canEdit := l.CreatedBy == u.ID
	if l.Shared {
		canEdit = s.can(u.Role, auth.ManageViews)
	}
	return v2WallLayout{ID: l.ID, Name: l.Name, Shared: l.Shared, Monitors: monitors, CreatedBy: l.CreatedBy,
		CreatedAt: secToMs(l.CreatedAt), UpdatedAt: secToMs(l.UpdatedAt), CanEdit: canEdit}
}

// checkWallMonitors validates monitors against the writer's scope.
func (s *Server) checkWallMonitors(r *http.Request, monitors []v2WallMonitor) error {
	if monitors == nil {
		return fmt.Errorf("send monitors (an empty list for an empty wall)")
	}
	if len(monitors) > maxWallMonitors {
		return fmt.Errorf("at most %d monitors", maxWallMonitors)
	}
	scope := s.cameraScope(r.Context())
	views := s.wallViewAccessFor(r)
	slots := map[int]bool{}
	for i := range monitors {
		m := &monitors[i]
		n := i + 1
		if m.Slot < 0 || m.Slot >= maxWallMonitors {
			return fmt.Errorf("monitor %d: slot must be from 0 to %d", n, maxWallMonitors-1)
		}
		if slots[m.Slot] {
			return fmt.Errorf("monitor %d: slot %d is used twice", n, m.Slot)
		}
		slots[m.Slot] = true
		m.ScreenHint = strings.TrimSpace(m.ScreenHint)
		if len([]rune(m.ScreenHint)) > maxWallScreenHintLen {
			return fmt.Errorf("monitor %d: screenHint can be up to %d characters", n, maxWallScreenHintLen)
		}
		if m.ScreenIndex != nil && (*m.ScreenIndex < 0 || *m.ScreenIndex > 63) {
			return fmt.Errorf("monitor %d: screenIndex must be from 0 to 63", n)
		}
		if m.ViewID != "" && m.CameraIDs != nil {
			return fmt.Errorf("monitor %d: show a view or a list of cameras, not both", n)
		}
		if m.ViewID == "" && m.CameraIDs == nil && m.Tour == nil {
			return fmt.Errorf("monitor %d: give it a viewId, cameraIds or a tour", n)
		}
		if m.ViewID != "" && !views(m.ViewID) {
			return fmt.Errorf("monitor %d: no view %q", n, m.ViewID)
		}
		if len(m.CameraIDs) > maxWallMonitorCams {
			return fmt.Errorf("monitor %d: at most %d cameras", n, maxWallMonitorCams)
		}
		for _, id := range m.CameraIDs {
			if _, err := s.Store.Camera(r.Context(), id); err != nil || !scope.camera(id) {
				return fmt.Errorf("monitor %d: no camera %q", n, id)
			}
		}
		if t := m.Tour; t != nil {
			if len(t.ViewIDs) == 0 || len(t.ViewIDs) > maxWallTourViews {
				return fmt.Errorf("monitor %d: a tour lists 1 to %d views", n, maxWallTourViews)
			}
			if t.Seconds < minWallTourSeconds || t.Seconds > maxWallTourSeconds {
				return fmt.Errorf("monitor %d: tour seconds must be from %d to %d", n, minWallTourSeconds, maxWallTourSeconds)
			}
			for _, id := range t.ViewIDs {
				if !views(id) {
					return fmt.Errorf("monitor %d: tour: no view %q", n, id)
				}
			}
		}
	}
	return nil
}

func cleanWallName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", fmt.Errorf("name must be 1 to 100 characters")
	}
	return name, nil
}

func forbidSharedWalls(w http.ResponseWriter) {
	writeV2Error(w, http.StatusForbidden, codeForbidden, "sharing wall layouts, or changing shared ones, needs a role that can manage shared views")
}

func wallDetail(l *store.WallLayout, monitors int) string {
	kind := "private"
	if l.Shared {
		kind = "shared"
	}
	return fmt.Sprintf("%s (%s, %d monitors)", l.Name, kind, monitors)
}

func (s *Server) handleV2WallLayouts(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.WallLayoutsFor(r.Context(), currentUser(r).ID)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	views := s.wallViewAccessFor(r)
	out := make([]v2WallLayout, 0, len(list))
	for i := range list {
		out = append(out, s.wallLayoutView(r, &list[i], views))
	}
	v2Items(w, out)
}

// visibleWallLayout loads the layout in the path if the caller may see it.
func (s *Server) visibleWallLayout(w http.ResponseWriter, r *http.Request) (*store.WallLayout, bool) {
	l, err := s.Store.WallLayoutByID(r.Context(), r.PathValue("layoutId"))
	if err != nil || (!l.Shared && l.CreatedBy != currentUser(r).ID) {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "wall layout not found")
		return nil, false
	}
	return l, true
}

func (s *Server) handleV2WallLayout(w http.ResponseWriter, r *http.Request) {
	l, ok := s.visibleWallLayout(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.wallLayoutView(r, l, s.wallViewAccessFor(r)))
}

func (s *Server) handleV2CreateWallLayout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string          `json:"name"`
		Shared   bool            `json:"shared"`
		Monitors []v2WallMonitor `json:"monitors"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	if in.Shared && !s.can(u.Role, auth.ManageViews) {
		forbidSharedWalls(w)
		return
	}
	name, err := cleanWallName(in.Name)
	if err == nil {
		err = s.checkWallMonitors(r, in.Monitors)
	}
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	body, err := json.Marshal(v2WallBody{Monitors: in.Monitors})
	if err != nil {
		writeV2Err(w, err)
		return
	}
	l := &store.WallLayout{Name: name, Body: string(body), Shared: in.Shared, CreatedBy: u.ID}
	if err := s.Store.AddWallLayout(r.Context(), l); err != nil {
		writeV2Fail(w, err, "wall layout")
		return
	}
	s.audit(r, "Views", "Saved wall layout", wallDetail(l, len(in.Monitors)))
	writeJSON(w, http.StatusCreated, s.wallLayoutView(r, l, s.wallViewAccessFor(r)))
}

func (s *Server) handleV2UpdateWallLayout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string          `json:"name"`
		Shared   *bool            `json:"shared"`
		Monitors *[]v2WallMonitor `json:"monitors"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	l, ok := s.visibleWallLayout(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	manage := s.can(u.Role, auth.ManageViews)
	if (l.Shared || (in.Shared != nil && *in.Shared)) && !manage {
		forbidSharedWalls(w)
		return
	}
	wasShared := l.Shared
	if in.Name != nil {
		name, err := cleanWallName(*in.Name)
		if err != nil {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		l.Name = name
	}
	if in.Shared != nil {
		l.Shared = *in.Shared
	}
	var body v2WallBody
	_ = json.Unmarshal([]byte(l.Body), &body)
	if in.Monitors != nil {
		if err := s.checkWallMonitors(r, *in.Monitors); err != nil {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		body.Monitors = *in.Monitors
		raw, err := json.Marshal(body)
		if err != nil {
			writeV2Err(w, err)
			return
		}
		l.Body = string(raw)
	}
	if err := s.Store.UpdateWallLayout(r.Context(), l); err != nil {
		writeV2Fail(w, err, "wall layout")
		return
	}
	action := "Updated wall layout"
	switch {
	case wasShared && !l.Shared:
		action = "Unshared wall layout"
	case !wasShared && l.Shared:
		action = "Shared wall layout"
	}
	s.audit(r, "Views", action, wallDetail(l, len(body.Monitors)))
	writeJSON(w, http.StatusOK, s.wallLayoutView(r, l, s.wallViewAccessFor(r)))
}

func (s *Server) handleV2DeleteWallLayout(w http.ResponseWriter, r *http.Request) {
	l, ok := s.visibleWallLayout(w, r)
	if !ok {
		return
	}
	if l.Shared && !s.can(currentUser(r).Role, auth.ManageViews) {
		forbidSharedWalls(w)
		return
	}
	if err := s.Store.DeleteWallLayout(r.Context(), l.ID); err != nil {
		writeV2Fail(w, err, "wall layout")
		return
	}
	var body v2WallBody
	_ = json.Unmarshal([]byte(l.Body), &body)
	s.audit(r, "Views", "Deleted wall layout", wallDetail(l, len(body.Monitors)))
	w.WriteHeader(http.StatusNoContent)
}
