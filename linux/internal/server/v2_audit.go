package server

// API v2: the site's audit log and its verification (PLATFORM.md, "Audit
// and logs"). Entries forwarded by recording servers are on the site chain
// too and keep their own server's chain (store/audit_site.go).

import (
	"net/http"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Audit(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/audit", auth.ViewAudit, s.siteWide(s.handleV2Audit)) // scope_sitewide.go
	s.v2Route(mux, "GET /api/v2/audit/verify", auth.ViewAudit, s.handleV2AuditVerify)
}

type v2AuditEntry struct {
	ID       string `json:"id"`
	At       int64  `json:"at"`
	User     string `json:"user"`
	Area     string `json:"area"`
	Action   string `json:"action"`
	Detail   string `json:"detail"`
	ServerID string `json:"serverId"`
}

func toV2AuditEntry(e *store.SiteAuditEntry) v2AuditEntry {
	return v2AuditEntry{ID: e.ID, At: secToMs(e.Time), User: e.User, Area: e.Area, Action: e.Action, Detail: e.Detail, ServerID: e.ServerID}
}

// auditPageKey: the audit log pages by its sequence number (the cursor's
// time part), newest first.
func auditPageKey(e *store.SiteAuditEntry) store.PageKey {
	return store.PageKey{CreatedAt: e.Seq, ID: e.ID}
}

// handleV2Audit lists entries newest first. ?serverId= picks one source:
// present but empty = the management server's own entries.
func (s *Server) handleV2Audit(w http.ResponseWriter, r *http.Request) {
	page, ok := parseV2Page(w, r)
	if !ok {
		return
	}
	var server *string
	if q := r.URL.Query(); q.Has("serverId") {
		v := q.Get("serverId")
		server = &v
	}
	before := int64(0)
	if !page.after.IsZero() {
		if before = page.after.CreatedAt; before <= 0 {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid cursor")
			return
		}
	}
	entries, err := s.Store.AuditPage(r.Context(), server, before, page.limit+1)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2PageOf(w, entries, page.limit, auditPageKey, toV2AuditEntry)
}

type v2AuditChain struct {
	ServerID string `json:"serverId"`
	OK       bool   `json:"ok"`
	Entries  int    `json:"entries"`
}

// handleV2AuditVerify checks the site chain and each recording server's
// forwarded chain; ok only when all of them verify.
func (s *Server) handleV2AuditVerify(w http.ResponseWriter, r *http.Request) {
	site, servers, err := s.Store.VerifyAuditChains(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	ok := site.BrokenID == ""
	out := make([]v2AuditChain, 0, len(servers))
	for _, c := range servers {
		ok = ok && c.BrokenID == ""
		out = append(out, v2AuditChain{ServerID: c.ServerID, OK: c.BrokenID == "", Entries: c.Entries})
	}
	resp := map[string]any{"ok": ok, "entries": site.Entries, "servers": out}
	if site.BrokenID != "" {
		resp["brokenAt"] = site.BrokenID
	}
	writeJSON(w, http.StatusOK, resp)
}
