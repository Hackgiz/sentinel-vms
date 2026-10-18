package server

import (
	"context"
	"net/http"
	"testing"

	"sentinel-linux/internal/store"
)

// TestScopeSiteWideRecords: the audit trail and the servers' logs name
// cameras site-wide, so a group-scoped Supervisor (who has ViewAudit) gets
// 403 for them, while chain verification stays open and an unscoped
// Supervisor and an Admin read them.
func TestScopeSiteWideRecords(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	sup := v2Token(t, f.s, "scopedsup", "Supervisor")
	allSup := v2Token(t, f.s, "allsup", "Supervisor")
	u, _ := f.s.Store.UserByName(ctx, "scopedsup")
	if err := f.s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{f.frontG.ID}}); err != nil {
		t.Fatal(err)
	}
	f.h = f.s.Handler()
	routes := []string{
		"/api/v2/audit",
		"/api/v2/system/logs",
		"/api/v2/recording-servers/" + store.LocalRecorderID + "/log?lines=10",
	}
	for _, p := range routes {
		if w := do(t, f.h, "GET", p, sup, ""); w.Code != http.StatusForbidden {
			t.Errorf("scoped supervisor %s: %d %s", p, w.Code, w.Body)
		}
		for name, tok := range map[string]string{"unscoped supervisor": allSup, "admin": f.admin} {
			if w := do(t, f.h, "GET", p, tok, ""); w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
				t.Errorf("%s %s: %d %s", name, p, w.Code, w.Body)
			}
		}
	}
	if w := webDo(t, f.h, f.cookieFor(t, sup), "GET", "/api/v1/audit", ""); w.Code != http.StatusForbidden {
		t.Errorf("scoped supervisor v1 audit: %d", w.Code)
	}
	if w := webDo(t, f.h, f.cookieFor(t, allSup), "GET", "/api/v1/audit", ""); w.Code != http.StatusOK {
		t.Errorf("unscoped supervisor v1 audit: %d", w.Code)
	}
	if w := do(t, f.h, "GET", "/api/v2/audit/verify", sup, ""); w.Code != http.StatusOK {
		t.Errorf("scoped supervisor verify: %d %s", w.Code, w.Body)
	}
	// Viewers and Operators never had ViewAudit.
	if w := do(t, f.h, "GET", "/api/v2/audit", f.allOp, ""); w.Code != http.StatusForbidden {
		t.Errorf("operator audit: %d", w.Code)
	}
}
