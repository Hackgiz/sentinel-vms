package server

import (
	"context"
	"encoding/json"
	"testing"

	"sentinel-linux/internal/store"
)

func decodeMap(t *testing.T, body string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("bad json %q: %v", body, err)
	}
	return m
}

func TestV2SharedViewsAndFolders(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s"})
	if err != nil {
		t.Fatal(err)
	}
	admin := v2Token(t, s, "boss", "Admin")
	op := v2Token(t, s, "op", "Operator")
	viewer := v2Token(t, s, "viewer", "Viewer")
	h := s.Handler()
	layout := func(extra string) string {
		return `{"name":"Wall","columns":2,"rows":2,"tiles":[{"cameraId":"` + cam.ID + `","x":0,"y":0}]` + extra + `}`
	}

	// Folders: managers only.
	for _, tok := range []string{op, viewer} {
		if r := do(t, h, "POST", "/api/v2/view-groups", tok, `{"name":"Lobby"}`); r.Code != 403 {
			t.Fatalf("non-manager creates a folder: %d %s", r.Code, r.Body)
		}
	}
	r := do(t, h, "POST", "/api/v2/view-groups", admin, `{"name":"Lobby","sortOrder":3}`)
	if r.Code != 201 {
		t.Fatalf("create folder: %d %s", r.Code, r.Body)
	}
	gid := decodeMap(t, r.Body.String())["id"].(string)
	if r := do(t, h, "POST", "/api/v2/view-groups", admin, `{"name":"Floor 1","parentId":"`+gid+`"}`); r.Code != 201 {
		t.Fatalf("nested folder: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "POST", "/api/v2/view-groups", admin, `{"name":"X","parentId":"nope"}`); r.Code != 400 {
		t.Fatalf("bad parent: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "PUT", "/api/v2/view-groups/"+gid, admin, `{"name":"Lobby!"}`); r.Code != 200 {
		t.Fatalf("rename folder: %d %s", r.Code, r.Body)
	}

	// Writing a shared view or placing one in a folder needs ManageViews.
	for _, tok := range []string{op, viewer} {
		if r := do(t, h, "POST", "/api/v2/views", tok, layout(`,"shared":true`)); r.Code != 403 {
			t.Fatalf("non-manager shares a view: %d %s", r.Code, r.Body)
		}
		if r := do(t, h, "POST", "/api/v2/views", tok, layout(`,"shared":true,"groupId":"`+gid+`"`)); r.Code != 403 {
			t.Fatalf("non-manager places a view: %d %s", r.Code, r.Body)
		}
	}
	// A folder without shared is a mistake.
	if r := do(t, h, "POST", "/api/v2/views", admin, layout(`,"groupId":"`+gid+`"`)); r.Code != 400 {
		t.Fatalf("group on a private view: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "POST", "/api/v2/views", admin, layout(`,"shared":true,"groupId":"nope"`)); r.Code != 400 {
		t.Fatalf("missing folder: %d %s", r.Code, r.Body)
	}
	r = do(t, h, "POST", "/api/v2/views", admin, layout(`,"shared":true,"groupId":"`+gid+`","sortOrder":2`))
	if r.Code != 201 {
		t.Fatalf("shared view: %d %s", r.Code, r.Body)
	}
	sv := decodeMap(t, r.Body.String())
	svid := sv["id"].(string)
	if sv["shared"] != true || sv["groupId"] != gid || sv["sortOrder"] != float64(2) {
		t.Fatalf("shared view shape: %v", sv)
	}

	// Everyone sees it under /view-groups, nobody under /views.
	for _, tok := range []string{op, viewer, admin} {
		r := do(t, h, "GET", "/api/v2/view-groups", tok, "")
		m := decodeMap(t, r.Body.String())
		if r.Code != 200 || len(m["groups"].([]any)) != 2 || len(m["views"].([]any)) != 1 {
			t.Fatalf("GET view-groups: %d %s", r.Code, r.Body)
		}
		if r := do(t, h, "GET", "/api/v2/views", tok, ""); !contains(r.Body.String(), `"items":[]`) {
			t.Fatalf("shared view leaked into private list: %s", r.Body)
		}
	}

	// Viewer/Operator can't change or delete it (403).
	for _, tok := range []string{op, viewer} {
		if r := do(t, h, "PUT", "/api/v2/views/"+svid, tok, layout(`,"shared":true`)); r.Code != 403 {
			t.Fatalf("non-manager edits shared: %d %s", r.Code, r.Body)
		}
		if r := do(t, h, "DELETE", "/api/v2/views/"+svid, tok, ""); r.Code != 403 {
			t.Fatalf("non-manager deletes shared: %d %s", r.Code, r.Body)
		}
	}

	// A viewer's private views stay theirs and private.
	r = do(t, h, "POST", "/api/v2/views", viewer, layout(""))
	if r.Code != 201 || decodeMap(t, r.Body.String())["shared"] != false {
		t.Fatalf("viewer private view: %d %s", r.Code, r.Body)
	}
	pid := decodeMap(t, r.Body.String())["id"].(string)
	if r := do(t, h, "PUT", "/api/v2/views/"+pid, op, layout("")); r.Code != 404 {
		t.Fatalf("another user's private view: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "PUT", "/api/v2/views/"+pid, viewer, layout(`,"shared":true`)); r.Code != 403 {
		t.Fatalf("viewer promotes own view: %d %s", r.Code, r.Body)
	}

	// Folder delete: refused while it holds anything.
	if r := do(t, h, "DELETE", "/api/v2/view-groups/"+gid, admin, ""); r.Code != 409 {
		t.Fatalf("delete non-empty folder: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "PUT", "/api/v2/views/"+svid, admin, layout(`,"shared":true,"groupId":"`+gid+`"`)); r.Code != 200 {
		t.Fatalf("manager edits shared: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "DELETE", "/api/v2/views/"+svid, admin, ""); r.Code != 204 {
		t.Fatalf("manager deletes shared: %d %s", r.Code, r.Body)
	}
	// Still holds the nested folder.
	if r := do(t, h, "DELETE", "/api/v2/view-groups/"+gid, admin, ""); r.Code != 409 {
		t.Fatalf("delete folder with subfolder: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "DELETE", "/api/v2/view-groups/"+gid, op, ""); r.Code != 403 {
		t.Fatalf("operator deletes folder: %d %s", r.Code, r.Body)
	}
	m := decodeMap(t, do(t, h, "GET", "/api/v2/view-groups", admin, "").Body.String())
	for _, x := range m["groups"].([]any) {
		g := x.(map[string]any)
		if g["parentId"] != "" {
			if r := do(t, h, "DELETE", "/api/v2/view-groups/"+g["id"].(string), admin, ""); r.Code != 204 {
				t.Fatalf("delete subfolder: %d %s", r.Code, r.Body)
			}
		}
	}
	if r := do(t, h, "DELETE", "/api/v2/view-groups/"+gid, admin, ""); r.Code != 204 {
		t.Fatalf("delete empty folder: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "DELETE", "/api/v2/view-groups/"+gid, admin, ""); r.Code != 404 {
		t.Fatalf("delete gone folder: %d %s", r.Code, r.Body)
	}

	acts := auditActions(t, s, "Views")
	for _, a := range []string{"Created view folder", "Updated view folder", "Deleted view folder", "Saved shared view", "Deleted shared view"} {
		if !acts[a] {
			t.Errorf("audit lacks %q: %v", a, acts)
		}
	}
}
