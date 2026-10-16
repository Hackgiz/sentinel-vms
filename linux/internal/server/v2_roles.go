package server

// API v2: custom roles and the permission catalog (wave 6, stream B).
// Role resolution for gating lives here too: a cached role -> permission
// set map loaded from the store, consulted by every permission check.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Roles(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/roles", auth.ManageUsers, s.handleV2Roles)
	s.v2Route(mux, "POST /api/v2/roles", auth.ManageUsers, s.handleV2SaveRole)
	s.v2Route(mux, "PUT /api/v2/roles/{roleId}", auth.ManageUsers, s.handleV2SaveRole)
	s.v2Route(mux, "DELETE /api/v2/roles/{roleId}", auth.ManageUsers, s.handleV2DeleteRole)
	s.v2Route(mux, "GET /api/v2/permissions", auth.ManageUsers, func(w http.ResponseWriter, _ *http.Request) {
		v2Items(w, auth.Catalog())
	})
}

// MARK: - Resolution

type roleCache struct {
	mu     sync.Mutex
	loaded bool
	perms  map[string]map[string]bool // role name -> permission ids
}

// roleCaches holds one cache per store, so the Server struct stays
// untouched.
var roleCaches sync.Map // *store.Store -> *roleCache

func (s *Server) roleCache() *roleCache {
	if c, ok := roleCaches.Load(s.Store); ok {
		return c.(*roleCache)
	}
	c, loaded := roleCaches.LoadOrStore(s.Store, &roleCache{})
	rc := c.(*roleCache)
	if !loaded && s.Bus != nil {
		sub := s.Bus.Subscribe("roles", 0)
		bus.Handle(sub, TopicConfigChanged, func(ConfigChanged) { rc.invalidate() })
	}
	return rc
}

func (c *roleCache) invalidate() {
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}

// invalidateRoles drops the cached role sets; call after any role write.
func (s *Server) invalidateRoles() { s.roleCache().invalidate() }

// can reports whether role holds p. The four built-in names always use
// auth.Can; any other role comes from the store. An unknown role has no
// permissions.
func (s *Server) can(role string, p auth.Permission) bool {
	for _, b := range store.Roles {
		if b == role {
			return auth.Can(role, p)
		}
	}
	c := s.roleCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		roles, err := s.Store.Roles(context.Background())
		if err != nil {
			return false
		}
		c.perms = map[string]map[string]bool{}
		for _, r := range roles {
			set := make(map[string]bool, len(r.Permissions))
			for _, id := range r.Permissions {
				set[id] = true
			}
			c.perms[r.Name] = set
		}
		c.loaded = true
	}
	return c.perms[role][p.ID()]
}

// MARK: - Handlers

type v2RoleInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

func (s *Server) handleV2Roles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.Store.Roles(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2Items(w, roles)
}

func (s *Server) roleFail(w http.ResponseWriter, err error) {
	var bad store.ErrBadRole
	switch {
	case errors.As(err, &bad):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, bad.Msg)
	case errors.Is(err, store.ErrDuplicateName):
		writeV2Error(w, http.StatusConflict, codeConflict, "a role with that name already exists")
	case errors.Is(err, store.ErrRoleBuiltin):
		writeV2Error(w, http.StatusConflict, codeConflict, "built-in roles can't be changed")
	default:
		writeV2Fail(w, err, "role")
	}
}

func (s *Server) handleV2SaveRole(w http.ResponseWriter, r *http.Request) {
	var in v2RoleInput
	if !readV2JSON(w, r, &in) {
		return
	}
	if len(in.Name) > 100 || len(in.Description) > 500 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "name or description is too long")
		return
	}
	var (
		role   *store.Role
		err    error
		action = "Created role"
		status = http.StatusCreated
	)
	if id := r.PathValue("roleId"); id != "" {
		role, err = s.Store.UpdateRole(r.Context(), id, in.Name, in.Description, in.Permissions)
		action, status = "Changed role", http.StatusOK
	} else {
		role, err = s.Store.CreateRole(r.Context(), in.Name, in.Description, in.Permissions)
	}
	if err != nil {
		s.roleFail(w, err)
		return
	}
	s.invalidateRoles()
	s.audit(r, "Security", action, fmt.Sprintf("%s (%d permissions)", role.Name, len(role.Permissions)))
	writeJSON(w, status, role)
}

func (s *Server) handleV2DeleteRole(w http.ResponseWriter, r *http.Request) {
	role, err := s.Store.RoleByID(r.Context(), r.PathValue("roleId"))
	if err != nil {
		s.roleFail(w, err)
		return
	}
	n, err := s.Store.DeleteRole(r.Context(), role.ID)
	if errors.Is(err, store.ErrRoleInUse) {
		writeV2Error(w, http.StatusConflict, codeConflict, fmt.Sprintf("%d user(s) still hold the role %q; reassign them first", n, role.Name))
		return
	}
	if err != nil {
		s.roleFail(w, err)
		return
	}
	s.invalidateRoles()
	s.audit(r, "Security", "Deleted role", role.Name)
	w.WriteHeader(http.StatusNoContent)
}
