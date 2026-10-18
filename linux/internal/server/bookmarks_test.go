package server

import (
	"encoding/json"
	"fmt"
	"testing"
)

func bmJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("bad json %q: %v", body, err)
	}
	return m
}

// TestBookmarksAPI: create/list/patch/delete, scope, and the ownership rule
// (non-managers may change only their own; Admin and Supervisor any).
func TestBookmarksAPI(t *testing.T) {
	f := newScopeFixture(t)
	h := f.h
	front, back := f.front.ID, f.back.ID
	super := v2Token(t, f.s, "sup", "Supervisor")
	viewer := v2Token(t, f.s, "view", "Viewer")
	base := f.t0.UnixMilli()

	// Create: a point by the scoped operator, a range by the other operator.
	rec := do(t, h, "POST", "/api/v2/cameras/"+front+"/bookmarks", f.op, fmt.Sprintf(`{"atMs":%d,"title":"  Door open ","color":"red"}`, base))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	mine := bmJSON(t, rec.Body.Bytes())
	if mine["title"] != "Door open" || mine["color"] == "" || mine["durationMs"].(float64) != 0 || mine["canEdit"] != true {
		t.Fatalf("created: %v", mine)
	}
	rec = do(t, h, "POST", "/api/v2/cameras/"+front+"/bookmarks", f.allOp, fmt.Sprintf(`{"atMs":%d,"durationMs":60000,"note":"n"}`, base+120_000))
	if rec.Code != 201 {
		t.Fatalf("create range: %d %s", rec.Code, rec.Body)
	}
	theirs := bmJSON(t, rec.Body.Bytes())
	if theirs["color"] != "blue" {
		t.Fatalf("default color: %v", theirs)
	}

	// Validation.
	for name, body := range map[string]string{
		"bad color":  fmt.Sprintf(`{"atMs":%d,"color":"#f00"}`, base),
		"neg dur":    fmt.Sprintf(`{"atMs":%d,"durationMs":-1}`, base),
		"huge dur":   fmt.Sprintf(`{"atMs":%d,"durationMs":86400001}`, base),
		"no time":    `{"title":"x"}`,
		"unknown":    fmt.Sprintf(`{"atMs":%d,"cameraId":"x"}`, base),
		"long title": fmt.Sprintf(`{"atMs":%d,"title":"%0130d"}`, base, 0),
	} {
		if rec := do(t, h, "POST", "/api/v2/cameras/"+front+"/bookmarks", f.op, body); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	// Scope: the Front-only operator can't see or create on Back, and a
	// bookmark on Back is invisible (404) to them.
	if rec := do(t, h, "GET", "/api/v2/cameras/"+back+"/bookmarks", f.op, ""); rec.Code != 404 {
		t.Errorf("hidden list: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/v2/cameras/"+back+"/bookmarks", f.op, fmt.Sprintf(`{"atMs":%d}`, base)); rec.Code != 404 {
		t.Errorf("hidden create: %d", rec.Code)
	}
	rec = do(t, h, "POST", "/api/v2/cameras/"+back+"/bookmarks", f.allOp, fmt.Sprintf(`{"atMs":%d,"title":"back"}`, base))
	if rec.Code != 201 {
		t.Fatalf("back create: %d %s", rec.Code, rec.Body)
	}
	backID := bmJSON(t, rec.Body.Bytes())["id"].(string)
	for _, m := range []string{"PATCH", "DELETE"} {
		body := ""
		if m == "PATCH" {
			body = `{"title":"x"}`
		}
		if rec := do(t, h, m, "/api/v2/bookmarks/"+backID, f.op, body); rec.Code != 404 {
			t.Errorf("hidden %s: %d", m, rec.Code)
		}
	}
	if rec := do(t, h, "POST", "/api/v2/cameras/"+front+"/bookmarks", "", `{"atMs":1}`); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}

	// List windows.
	list := func(tok, q string) []map[string]any {
		t.Helper()
		rec := do(t, h, "GET", "/api/v2/cameras/"+front+"/bookmarks"+q, tok, "")
		if rec.Code != 200 {
			t.Fatalf("list %s: %d %s", q, rec.Code, rec.Body)
		}
		return items(t, bmJSON(t, rec.Body.Bytes()))
	}
	if got := list(f.op, ""); len(got) != 2 || got[0]["id"] != mine["id"] {
		t.Fatalf("list all: %v", got)
	}
	if got := list(viewer, fmt.Sprintf("?from=%d&to=%d", base+130_000, base+140_000)); len(got) != 1 || got[0]["id"] != theirs["id"] || got[0]["canEdit"] != false {
		t.Fatalf("list window as viewer: %v", got)
	}
	if rec := do(t, h, "GET", "/api/v2/cameras/"+front+"/bookmarks?from=abc", f.op, ""); rec.Code != 400 {
		t.Errorf("bad from: %d", rec.Code)
	}

	// Ownership. The scoped operator can't touch the other operator's
	// bookmark; a viewer can't either; the owner, a Supervisor and an Admin can.
	mineURL, theirsURL := "/api/v2/bookmarks/"+mine["id"].(string), "/api/v2/bookmarks/"+theirs["id"].(string)
	if rec := do(t, h, "PATCH", theirsURL, f.op, `{"title":"hijack"}`); rec.Code != 403 {
		t.Errorf("patch other's: %d", rec.Code)
	}
	if rec := do(t, h, "DELETE", theirsURL, f.op, ""); rec.Code != 403 {
		t.Errorf("delete other's: %d", rec.Code)
	}
	if rec := do(t, h, "PATCH", mineURL, viewer, `{"title":"hijack"}`); rec.Code != 403 {
		t.Errorf("viewer patch: %d", rec.Code)
	}
	rec = do(t, h, "PATCH", mineURL, f.op, `{"title":"Renamed","durationMs":5000,"color":"green"}`)
	if m := bmJSON(t, rec.Body.Bytes()); rec.Code != 200 || m["title"] != "Renamed" || m["durationMs"].(float64) != 5000 || m["color"] != "green" || m["note"] != "" {
		t.Fatalf("patch own: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "PATCH", mineURL, f.op, `{"color":"mauve"}`); rec.Code != 400 {
		t.Errorf("patch bad color: %d", rec.Code)
	}
	if rec := do(t, h, "PATCH", mineURL, f.op, `{"durationMs":-3}`); rec.Code != 400 {
		t.Errorf("patch bad duration: %d", rec.Code)
	}
	rec = do(t, h, "PATCH", theirsURL, super, `{"title":"by supervisor"}`)
	if m := bmJSON(t, rec.Body.Bytes()); rec.Code != 200 || m["title"] != "by supervisor" || m["canEdit"] != true {
		t.Fatalf("supervisor patch: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "DELETE", mineURL, f.admin, ""); rec.Code != 204 {
		t.Errorf("admin delete: %d", rec.Code)
	}
	if rec := do(t, h, "DELETE", theirsURL, f.allOp, ""); rec.Code != 204 {
		t.Errorf("owner delete: %d", rec.Code)
	}
	if rec := do(t, h, "DELETE", theirsURL, f.allOp, ""); rec.Code != 404 {
		t.Errorf("delete twice: %d", rec.Code)
	}
	if got := list(f.op, ""); len(got) != 0 {
		t.Fatalf("after deletes: %v", got)
	}

	// Every change is audited.
	entries, err := f.s.Store.AuditPage(t.Context(), nil, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, e := range entries {
		if e.Area == "Recordings" {
			n[e.Action]++
		}
	}
	if n["Added bookmark"] != 3 || n["Edited bookmark"] != 2 || n["Deleted bookmark"] != 2 {
		t.Errorf("audit counts: %v", n)
	}
}
