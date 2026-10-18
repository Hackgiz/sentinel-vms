package server

// API v2: camera groups, and the user scopes and camera memberships built
// on them (PLATFORM.md, "Permissions"; scope.go enforces them).

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const maxGroupName = 100

func (s *Server) registerV2Groups(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/camera-groups", auth.ViewLive, s.handleV2Groups)
	s.v2Route(mux, "POST /api/v2/camera-groups", auth.ManageCameras, s.handleV2SaveGroup)
	s.v2Route(mux, "PUT /api/v2/camera-groups/{groupId}", auth.ManageCameras, s.handleV2SaveGroup)
	s.v2Route(mux, "DELETE /api/v2/camera-groups/{groupId}", auth.ManageCameras, s.handleV2DeleteGroup)
}

type v2Group struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	CameraIDs []string `json:"cameraIds"`
	CreatedAt int64    `json:"createdAt"`
}

func toV2Group(g *store.CameraGroup) v2Group {
	ids := g.CameraIDs
	if ids == nil {
		ids = []string{}
	}
	return v2Group{ID: g.ID, Name: g.Name, CameraIDs: ids, CreatedAt: secToMs(g.CreatedAt)}
}

// handleV2Groups lists every group to users who see every camera, and only
// their own groups to users scoped to groups.
func (s *Server) handleV2Groups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.CameraGroups(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	if u := currentUser(r); u.Role != "Admin" {
		sc, err := s.Store.UserScope(r.Context(), u.ID)
		if err != nil {
			writeV2Err(w, err)
			return
		}
		if !sc.AllCameras {
			mine := map[string]bool{}
			for _, id := range sc.GroupIDs {
				mine[id] = true
			}
			groups = keepVisible(groups, func(g *store.CameraGroup) bool { return mine[g.ID] })
		}
	}
	out := make([]v2Group, 0, len(groups))
	for i := range groups {
		out = append(out, toV2Group(&groups[i]))
	}
	v2Items(w, out)
}

// handleV2SaveGroup serves POST /camera-groups (create) and PUT
// /camera-groups/{groupId} (rename and set its cameras).
func (s *Server) handleV2SaveGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string   `json:"name"`
		CameraIDs []string `json:"cameraIds"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > maxGroupName {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "enter a group name of up to 100 characters")
		return
	}
	if in.CameraIDs == nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "cameraIds is required (it may be empty)")
		return
	}
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	known := map[string]bool{}
	for _, c := range cams {
		known[c.ID] = true
	}
	for _, id := range in.CameraIDs {
		if !known[id] {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "no camera "+strconv.Quote(id))
			return
		}
	}
	id := r.PathValue("groupId")
	g, err := s.Store.SaveCameraGroup(r.Context(), id, name, in.CameraIDs)
	switch {
	case errors.Is(err, store.ErrDuplicateName):
		writeV2Error(w, http.StatusConflict, codeConflict, "a camera group named "+strconv.Quote(name)+" already exists")
		return
	case err != nil:
		writeV2Fail(w, err, "camera group")
		return
	}
	detail := g.Name + " (" + strconv.Itoa(len(g.CameraIDs)) + " cameras)"
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
		s.audit(r, "Cameras", "Added camera group", detail)
	} else {
		s.audit(r, "Cameras", "Edited camera group", detail)
	}
	s.scopeChanged(r.Context())
	writeJSON(w, status, toV2Group(g))
}

func (s *Server) handleV2DeleteGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.CameraGroup(r.Context(), r.PathValue("groupId"))
	if err == nil {
		err = s.Store.DeleteCameraGroup(r.Context(), g.ID)
	}
	if err != nil {
		writeV2Fail(w, err, "camera group")
		return
	}
	s.audit(r, "Cameras", "Deleted camera group", g.Name)
	s.scopeChanged(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// MARK: - Camera memberships

// checkGroupIDs validates a camera's groupIds (nil = not sent).
func (s *Server) checkGroupIDs(ctx context.Context, ids *[]string) error {
	if ids == nil {
		return nil
	}
	_, err := s.groupNames(ctx, *ids)
	return err
}

// groupNames maps group ids to names; an unknown id is an inputError.
func (s *Server) groupNames(ctx context.Context, ids []string) ([]string, error) {
	groups, err := s.Store.CameraGroups(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]string{}
	for _, g := range groups {
		byID[g.ID] = g.Name
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		n, ok := byID[id]
		if !ok {
			return nil, inputError{"no camera group " + strconv.Quote(id)}
		}
		names = append(names, n)
	}
	return names, nil
}

// setCameraGroups replaces the groups a camera is in (nil = not sent) and
// audits it.
func (s *Server) setCameraGroups(r *http.Request, cam *store.Camera, ids *[]string) error {
	if ids == nil {
		return nil
	}
	names, err := s.groupNames(r.Context(), *ids)
	if err != nil {
		return err
	}
	if err := s.Store.SetCameraGroups(r.Context(), cam.ID, *ids); err != nil {
		return err
	}
	s.audit(r, "Cameras", "Set camera groups", cam.Name+": "+listOrNone(names))
	s.scopeChanged(r.Context())
	return nil
}

// fillCameraGroups sets groupIds on camera views.
func (s *Server) fillCameraGroups(ctx context.Context, cams []v2Camera) {
	s.fillCameraMasks(ctx, cams) // zones.go
	s.fillCameraTalkback(cams)   // talkback.go (WAVE9(B))
	groups, err := s.Store.CameraGroups(ctx)
	if err != nil {
		return
	}
	of := map[string][]string{}
	for _, g := range groups {
		for _, c := range g.CameraIDs {
			of[c] = append(of[c], g.ID)
		}
	}
	for i := range cams {
		if ids := of[cams[i].ID]; ids != nil {
			cams[i].GroupIDs = ids
		}
	}
}

func (s *Server) withCameraGroups(ctx context.Context, v v2Camera) v2Camera {
	one := []v2Camera{v}
	s.fillCameraGroups(ctx, one)
	return one[0]
}

// MARK: - User scopes

// parseUserScope validates a UserScope for a user with role: every group
// must exist, and an Admin always has every camera.
func (s *Server) parseUserScope(ctx context.Context, role string, in *v2UserScope) (store.UserScope, error) {
	if in == nil || in.AllCameras {
		return store.UserScope{AllCameras: true}, nil
	}
	if role == "Admin" {
		return store.UserScope{}, inputError{"administrators always see every camera"}
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range in.GroupIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if _, err := s.groupNames(ctx, ids); err != nil {
		return store.UserScope{}, err
	}
	return store.UserScope{GroupIDs: ids}, nil
}

// setUserScope stores a user's scope and audits it.
func (s *Server) setUserScope(r *http.Request, u *store.User, sc store.UserScope) error {
	if err := s.Store.SetUserScope(r.Context(), u.ID, sc); err != nil {
		return err
	}
	detail := "all cameras"
	if !sc.AllCameras {
		names, _ := s.groupNames(r.Context(), sc.GroupIDs)
		detail = "groups: " + listOrNone(names)
	}
	s.audit(r, "Users", "Changed user scope", u.Name+": "+detail)
	s.scopeChanged(r.Context())
	return nil
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
