package server

import (
	"context"
	"strings"
	"testing"

	"sentinel-linux/internal/store"
)

func TestV2WallLayouts(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	door, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s"})
	if err != nil {
		t.Fatal(err)
	}
	yard, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Yard", RTSPURL: "rtsp://10.0.0.6/s"})
	admin := v2Token(t, s, "boss", "Admin")
	op := v2Token(t, s, "op", "Operator")
	viewer := v2Token(t, s, "viewer", "Viewer")
	scoped := v2Token(t, s, "scoped", "Operator")
	g, _ := s.Store.SaveCameraGroup(ctx, "", "Door only", []string{door.ID})
	su, _ := s.Store.UserByName(ctx, "scoped")
	if err := s.Store.SetUserScope(ctx, su.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// A shared view and an admin's private view to point monitors at.
	r := do(t, h, "POST", "/api/v2/views", admin, `{"name":"Lobby","columns":1,"rows":1,"tiles":[],"shared":true}`)
	if r.Code != 201 {
		t.Fatalf("shared view: %d %s", r.Code, r.Body)
	}
	sharedView := decodeMap(t, r.Body.String())["id"].(string)
	r = do(t, h, "POST", "/api/v2/views", admin, `{"name":"Mine","columns":1,"rows":1,"tiles":[]}`)
	privView := decodeMap(t, r.Body.String())["id"].(string)

	monitors := `[{"slot":0,"screenHint":"DELL U2720Q","screenIndex":1,"cameraIds":["` + door.ID + `","` + yard.ID + `"],"alarmMonitor":true},` +
		`{"slot":1,"screenHint":"Built-in Retina Display","viewId":"` + sharedView + `","tour":{"viewIds":["` + sharedView + `","` + privView + `"],"seconds":15}}]`

	// Validation.
	for _, c := range []struct{ body, want string }{
		{`{"name":"","monitors":[]}`, "name"},
		{`{"name":"W"}`, "monitors"},
		{`{"name":"W","monitors":[{"slot":0},{"slot":0,"viewId":"x"}]}`, "viewId, cameraIds or a tour"},
		{`{"name":"W","monitors":[{"slot":0,"cameraIds":[]},{"slot":0,"cameraIds":[]}]}`, "used twice"},
		{`{"name":"W","monitors":[{"slot":16,"cameraIds":[]}]}`, "slot"},
		{`{"name":"W","monitors":[{"slot":0,"viewId":"` + sharedView + `","cameraIds":[]}]}`, "not both"},
		{`{"name":"W","monitors":[{"slot":0,"cameraIds":["nope"]}]}`, "no camera"},
		{`{"name":"W","monitors":[{"slot":0,"viewId":"nope"}]}`, "no view"},
		{`{"name":"W","monitors":[{"slot":0,"tour":{"viewIds":[],"seconds":10}}]}`, "tour lists"},
		{`{"name":"W","monitors":[{"slot":0,"tour":{"viewIds":["` + sharedView + `"],"seconds":2}}]}`, "seconds"},
		{`{"name":"W","monitors":[{"slot":0,"cameraIds":[]}],"extra":1}`, "invalid request"},
	} {
		if r := do(t, h, "POST", "/api/v2/wall-layouts", op, c.body); r.Code != 400 || !strings.Contains(r.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.body, r.Code, r.Body)
		}
	}
	// A scoped user can't name a camera they don't see; another user's private view doesn't exist.
	if r := do(t, h, "POST", "/api/v2/wall-layouts", scoped, `{"name":"W","monitors":[{"slot":0,"cameraIds":["`+yard.ID+`"]}]}`); r.Code != 400 {
		t.Errorf("scoped writes out-of-scope camera: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "POST", "/api/v2/wall-layouts", op, `{"name":"W","monitors":[{"slot":0,"viewId":"`+privView+`"}]}`); r.Code != 400 {
		t.Errorf("op uses admin's private view: %d %s", r.Code, r.Body)
	}

	// Sharing needs ManageViews.
	for _, tok := range []string{op, viewer} {
		if r := do(t, h, "POST", "/api/v2/wall-layouts", tok, `{"name":"W","shared":true,"monitors":[]}`); r.Code != 403 {
			t.Errorf("non-manager shares: %d %s", r.Code, r.Body)
		}
	}
	r = do(t, h, "POST", "/api/v2/wall-layouts", admin, `{"name":"Control room","shared":true,"monitors":`+monitors+`}`)
	if r.Code != 201 {
		t.Fatalf("admin shared wall: %d %s", r.Code, r.Body)
	}
	wall := decodeMap(t, r.Body.String())
	wallID := wall["id"].(string)
	if wall["shared"] != true || wall["canEdit"] != true || !isMs(wall["createdAt"]) || len(wall["monitors"].([]any)) != 2 {
		t.Fatalf("shape: %v", wall)
	}

	// A viewer's private wall.
	r = do(t, h, "POST", "/api/v2/wall-layouts", viewer, `{"name":"My desk","monitors":[{"slot":0,"cameraIds":["`+door.ID+`"]}]}`)
	if r.Code != 201 {
		t.Fatalf("viewer private wall: %d %s", r.Code, r.Body)
	}
	privWall := decodeMap(t, r.Body.String())["id"].(string)

	// Lists: own + shared.
	names := func(tok string) []string {
		m := decodeMap(t, do(t, h, "GET", "/api/v2/wall-layouts", tok, "").Body.String())
		out := []string{}
		for _, it := range items(t, m) {
			out = append(out, it["name"].(string))
		}
		return out
	}
	if got := names(viewer); len(got) != 2 {
		t.Errorf("viewer list: %v", got)
	}
	if got := names(op); len(got) != 1 || got[0] != "Control room" {
		t.Errorf("op list: %v", got)
	}

	// Scope filters what a monitor shows: the scoped user sees only Door,
	// and neither user sees the admin's private view in the tour.
	m := decodeMap(t, do(t, h, "GET", "/api/v2/wall-layouts/"+wallID, scoped, "").Body.String())
	mons := m["monitors"].([]any)
	cams := mons[0].(map[string]any)["cameraIds"].([]any)
	if len(cams) != 1 || cams[0] != door.ID || m["canEdit"] != false {
		t.Errorf("scoped read: %v", m)
	}
	tour := mons[1].(map[string]any)["tour"].(map[string]any)
	if ids := tour["viewIds"].([]any); len(ids) != 1 || ids[0] != sharedView {
		t.Errorf("tour leaks a private view: %v", tour)
	}
	if mons[1].(map[string]any)["viewId"] != sharedView || mons[0].(map[string]any)["alarmMonitor"] != true {
		t.Errorf("monitor fields lost: %v", mons)
	}
	m = decodeMap(t, do(t, h, "GET", "/api/v2/wall-layouts/"+wallID, admin, "").Body.String())
	if ids := m["monitors"].([]any)[1].(map[string]any)["tour"].(map[string]any)["viewIds"].([]any); len(ids) != 2 {
		t.Errorf("creator's tour: %v", ids)
	}

	// Other users' private walls don't exist; shared ones are read-only without ManageViews.
	if r := do(t, h, "GET", "/api/v2/wall-layouts/"+privWall, op, ""); r.Code != 404 {
		t.Errorf("op reads viewer's wall: %d", r.Code)
	}
	if r := do(t, h, "PATCH", "/api/v2/wall-layouts/"+privWall, admin, `{"name":"x"}`); r.Code != 404 {
		t.Errorf("admin edits viewer's private wall: %d", r.Code)
	}
	if r := do(t, h, "PATCH", "/api/v2/wall-layouts/"+wallID, op, `{"name":"x"}`); r.Code != 403 {
		t.Errorf("op edits shared wall: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "DELETE", "/api/v2/wall-layouts/"+wallID, viewer, ""); r.Code != 403 {
		t.Errorf("viewer deletes shared wall: %d", r.Code)
	}
	if r := do(t, h, "PATCH", "/api/v2/wall-layouts/"+privWall, viewer, `{"shared":true}`); r.Code != 403 {
		t.Errorf("viewer shares own wall: %d", r.Code)
	}

	// Owner edits; PATCH is partial.
	r = do(t, h, "PATCH", "/api/v2/wall-layouts/"+privWall, viewer, `{"name":" Desk 2 "}`)
	if r.Code != 200 {
		t.Fatalf("owner rename: %d %s", r.Code, r.Body)
	}
	if m := decodeMap(t, r.Body.String()); m["name"] != "Desk 2" || len(m["monitors"].([]any)) != 1 {
		t.Errorf("rename result: %v", m)
	}
	if r := do(t, h, "PATCH", "/api/v2/wall-layouts/"+privWall, viewer, `{"monitors":[{"slot":3,"tour":{"viewIds":["`+sharedView+`"],"seconds":30}}]}`); r.Code != 200 {
		t.Errorf("owner replaces monitors: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "PATCH", "/api/v2/wall-layouts/"+privWall, viewer, `{"monitors":[{"slot":0}]}`); r.Code != 400 {
		t.Errorf("bad monitors on patch: %d", r.Code)
	}

	// Manager unshares, then deletes.
	if r := do(t, h, "PATCH", "/api/v2/wall-layouts/"+wallID, admin, `{"shared":false}`); r.Code != 200 {
		t.Errorf("unshare: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "GET", "/api/v2/wall-layouts/"+wallID, op, ""); r.Code != 404 {
		t.Errorf("unshared wall still visible: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/wall-layouts/"+wallID, admin, ""); r.Code != 204 {
		t.Errorf("delete: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/wall-layouts/"+privWall, viewer, ""); r.Code != 204 {
		t.Errorf("owner delete: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/wall-layouts/"+privWall, viewer, ""); r.Code != 404 {
		t.Errorf("delete gone: %d", r.Code)
	}

	acts := auditActions(t, s, "Views")
	for _, a := range []string{"Saved wall layout", "Updated wall layout", "Unshared wall layout", "Deleted wall layout"} {
		if !acts[a] {
			t.Errorf("audit lacks %q: %v", a, acts)
		}
	}
}
