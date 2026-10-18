package server

// Site-wide records (WAVE9 Stream D): the audit trail and the servers'
// system logs name cameras across the whole site ("Issued video token",
// "motion alarm camera=…"). Like alarms without a camera (scope.go), only
// users who see every camera may read them; a group-scoped Supervisor gets
// 403 rather than a trail that names cameras outside their groups
// (PLATFORM.md phase 2: a scoped operator can't see other cameras by any
// API). Verifying the chains (a yes/no per chain) stays open to ViewAudit.

import (
	"net/http"
	"strings"
)

const siteWideMessage = "the audit trail and server logs cover every camera; your access is limited to some camera groups"

// siteWide lets h run only for callers who see every camera.
func (s *Server) siteWide(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.siteWideAllowed(w, r) {
			h(w, r)
		}
	}
}

// siteWideAllowed reports whether the caller sees every camera, answering
// 403 when not.
func (s *Server) siteWideAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.cameraScope(r.Context()).all {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/v2/") {
		writeV2Error(w, http.StatusForbidden, codeForbidden, siteWideMessage)
	} else {
		writeError(w, http.StatusForbidden, siteWideMessage)
	}
	return false
}
