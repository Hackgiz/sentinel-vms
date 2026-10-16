package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
)

// scopeFixture: cameras front (group Front) and back (group Back), each
// with a recorded segment, an event with a snapshot, an alarm and an
// evidence record; a Storage alarm without a camera; an Operator scoped to
// Front (bearer token, dashboard cookie and a paired iPhone), an unscoped
// Operator and an Admin.
type scopeFixture struct {
	s                *Server
	h                http.Handler
	front, back      *store.Camera
	frontG, backG    *store.CameraGroup
	alarm            map[string]*store.Alarm    // camera ID →
	event            map[string]*store.Event    // camera ID →
	evidence         map[string]*store.Evidence // camera ID →
	storage          *store.Alarm
	segment          string
	t0               time.Time
	op, allOp, admin string // bearer tokens
	opCookie         *http.Cookie
	phone            string
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := &scopeFixture{s: newCompanionServer(t), alarm: map[string]*store.Alarm{}, event: map[string]*store.Event{}, evidence: map[string]*store.Evidence{}}
	s, ctx := f.s, context.Background()
	f.t0 = time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)
	f.segment = f.t0.Format("20060102-150405") + "-000000.mp4"
	snaps := filepath.Join(s.Store.DataDir(), "events")
	_ = os.MkdirAll(snaps, 0o755)
	for i, name := range []string{"Front door", "Back gate"} {
		c, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: name, RTSPURL: fmt.Sprintf("rtsp://10.0.0.%d/s", i+1), Recording: true})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(recOf(s).RecordingsRoot(), c.ID)
		_ = os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, f.segment)
		if err := os.WriteFile(p, playback.SynthSegment(60, 2), 0o644); err != nil {
			t.Fatal(err)
		}
		end := f.t0.Add(time.Minute)
		_ = os.Chtimes(p, end, end)
		snap := filepath.Join(snaps, c.ID+".jpg")
		_ = os.WriteFile(snap, []byte("\xff\xd8jpeg"), 0o644)
		e := &store.Event{CameraID: c.ID, Kind: "Person", Score: 0.9, CreatedAt: f.t0.Add(10 * time.Second).Unix(), Thumbnail: snap}
		if err := s.Store.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		f.event[c.ID] = e
		a, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person", Title: "Person detected",
			Severity: "High", At: f.t0.Add(10 * time.Second), Origin: "test"})
		if err != nil {
			t.Fatal(err)
		}
		f.alarm[c.ID] = a
		ev := &store.Evidence{ID: store.NewID(), CaseID: "CASE-" + c.ID[:4], CameraID: c.ID, CameraName: c.Name, Title: "Clip", Range: "r",
			File: c.ID + ".mp4", SHA256: strings.Repeat("ab", 32), LockedAt: time.Now().Unix(), LockedBy: "boss"}
		if err := s.Store.AddEvidence(ctx, ev); err != nil {
			t.Fatal(err)
		}
		f.evidence[c.ID] = ev
		if i == 0 {
			f.front = c
		} else {
			f.back = c
		}
	}
	var err error
	if f.storage, _, err = s.Store.RaiseAlarm(ctx, store.AlarmInput{Kind: "Storage", Title: "Disk almost full", Severity: "High", At: time.Now(), Origin: "test"}); err != nil {
		t.Fatal(err)
	}
	if f.frontG, err = s.Store.SaveCameraGroup(ctx, "", "Front", []string{f.front.ID}); err != nil {
		t.Fatal(err)
	}
	if f.backG, err = s.Store.SaveCameraGroup(ctx, "", "Back", []string{f.back.ID}); err != nil {
		t.Fatal(err)
	}
	f.op = v2Token(t, s, "op", "Operator")
	f.allOp = v2Token(t, s, "allop", "Operator")
	f.admin = v2Token(t, s, "boss", "Admin")
	op, _ := s.Store.UserByName(ctx, "op")
	if err := s.Store.SetUserScope(ctx, op.ID, store.UserScope{GroupIDs: []string{f.frontG.ID}}); err != nil {
		t.Fatal(err)
	}
	sess, _ := s.Store.CreateSession(ctx, op.ID, time.Hour)
	f.opCookie = &http.Cookie{Name: sessionCookie, Value: sess}
	_, f.phone, _ = s.Store.CreatePairedDevice(ctx, "Op's iPhone", "op")
	f.h = s.Handler()
	return f
}

// call sends a request as the scoped Operator over auth ("cookie",
// "bearer" or "phone"), or with bearer token tok when auth is "token:".
func (f *scopeFixture) call(t *testing.T, auth, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	switch auth {
	case "cookie":
		return webDo(t, f.h, f.opCookie, method, path, body)
	case "phone":
		return do(t, f.h, method, path, f.phone, body)
	case "bearer":
		tok = f.op
	}
	return do(t, f.h, method, path, tok, body)
}

// TestScopeHidesOtherGroupsCameras: an Operator scoped to Front gets 404
// for Back's camera on every camera-scoped route (as if it didn't exist),
// while the same route works for Front's camera, and an Admin and an
// unscoped Operator still reach Back's.
func TestScopeHidesOtherGroupsCameras(t *testing.T) {
	f := newScopeFixture(t)
	q := fmt.Sprintf("from=%d&to=%d", f.t0.Add(-time.Hour).UnixMilli(), f.t0.Add(time.Hour).UnixMilli())
	snapT := f.t0.Add(5 * time.Second).UnixMilli()
	export := fmt.Sprintf(`{"from":%d,"to":%d}`, f.t0.UnixMilli(), f.t0.Add(10*time.Second).UnixMilli())
	routes := []struct {
		auth, method, path, body string
		reach                    bool // the visible camera's request gets past the scope check (not 404)
	}{
		// dashboard /api/v1 and video (cookie)
		{"cookie", "GET", "/api/v1/cameras/{cam}/recordings", "", true},
		{"cookie", "GET", "/api/v1/cameras/{cam}/timeline?" + q, "", true},
		{"cookie", "GET", "/api/v1/cameras/{cam}/playback.m3u8?" + q, "", true},
		{"cookie", "GET", "/api/v1/cameras/{cam}/pieces/x.mp4", "", false},
		{"cookie", "GET", "/api/v1/cameras/{cam}/init/x.mp4", "", false},
		{"cookie", "POST", "/api/v1/cameras/{cam}/export", export, true},
		{"cookie", "GET", fmt.Sprintf("/api/v1/cameras/{cam}/snapshot.jpg?t=%d", snapT), "", true},
		{"cookie", "GET", "/live/{cam}/index.m3u8", "", false},
		{"cookie", "GET", "/recordings/{cam}/{seg}", "", true},
		{"cookie", "POST", "/webrtc/{cam}/whep", "v=0", true},
		{"cookie", "PATCH", "/webrtc/{cam}/whep/" + strings.ToLower(store.NewID()), "", true},
		{"cookie", "DELETE", "/webrtc/{cam}/whep/" + strings.ToLower(store.NewID()), "", true},
		{"cookie", "POST", "/api/v1/alarms/{alarm}/state", `{"state":"Acknowledged"}`, true},
		{"cookie", "POST", "/api/v1/alarms/{alarm}/evidence", "", true},
		{"cookie", "GET", "/api/v1/events/{event}/thumbnail.jpg", "", true},
		{"cookie", "GET", "/api/v1/evidence/{evidence}/file", "", false},
		// API v2 (bearer)
		{"bearer", "GET", "/api/v2/cameras/{cam}", "", true},
		{"bearer", "GET", "/api/v2/cameras/{cam}/timeline?" + q, "", true},
		{"bearer", "GET", "/api/v2/cameras/{cam}/recordings", "", true},
		{"bearer", "POST", "/api/v2/cameras/{cam}/video-token", `{"rights":["live","playback"]}`, true},
		{"bearer", "GET", "/api/v2/alarms/{alarm}", "", true},
		{"bearer", "POST", "/api/v2/alarms/{alarm}/state", `{"state":"Acknowledged"}`, true},
		{"bearer", "POST", "/api/v2/alarms/{alarm}/evidence", "", true},
		{"bearer", "GET", "/api/v2/events/{event}", "", true},
		{"bearer", "GET", "/api/v2/events/{event}/thumbnail.jpg", "", true},
		// iPhone API (the device acts for op)
		{"phone", "GET", "/cameras/{cam}", "", true},
		{"phone", "GET", "/cameras/{cam}/segments", "", true},
		{"phone", "GET", "/cameras/{cam}/segments/{seg}", "", true},
		{"phone", "GET", "/hls/{cam}/index.m3u8", "", true},
		{"phone", "POST", "/alerts/{alarm}/acknowledge", "", true},
		{"phone", "POST", "/alerts/{alarm}/lock-evidence", "", true},
		{"phone", "GET", "/events/{event}/thumbnail.jpg", "", true},
	}
	fill := func(path string, c *store.Camera) string {
		return strings.NewReplacer("{cam}", c.ID, "{seg}", f.segment, "{alarm}", f.alarm[c.ID].ID, "{event}", f.event[c.ID].ID,
			"{evidence}", f.evidence[c.ID].ID).Replace(path)
	}
	for _, rt := range routes {
		t.Run(rt.auth+" "+rt.method+" "+rt.path, func(t *testing.T) {
			if rec := f.call(t, rt.auth, "", rt.method, fill(rt.path, f.back), rt.body); rec.Code != 404 {
				t.Errorf("hidden camera: %d %s", rec.Code, rec.Body)
			}
			if !rt.reach {
				return
			}
			if rec := f.call(t, rt.auth, "", rt.method, fill(rt.path, f.front), rt.body); rec.Code == 404 || rec.Code == 401 || rec.Code == 403 {
				t.Errorf("visible camera: %d %s", rec.Code, rec.Body)
			}
			if rt.auth == "phone" {
				return // phones have no Admin; their unscoped case is covered by the companion tests
			}
			for _, tok := range []string{f.admin, f.allOp} {
				var rec *httptest.ResponseRecorder
				if rt.auth == "cookie" {
					rec = webDo(t, f.h, f.cookieFor(t, tok), rt.method, fill(rt.path, f.back), rt.body)
				} else {
					rec = f.call(t, "token:", tok, rt.method, fill(rt.path, f.back), rt.body)
				}
				if rec.Code == 404 || rec.Code == 401 || rec.Code == 403 {
					t.Errorf("unscoped user: %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
}

// cookieFor makes a dashboard session for the user a bearer token belongs to.
func (f *scopeFixture) cookieFor(t *testing.T, tok string) *http.Cookie {
	t.Helper()
	u, err := f.s.Store.AccessTokenUser(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := f.s.Store.CreateSession(context.Background(), u.ID, time.Hour)
	return &http.Cookie{Name: sessionCookie, Value: sess}
}

// TestScopeFiltersLists: every list leaves out the other group's camera
// and what belongs to it, and camera-less alarms (Storage) are shown only
// to users who see every camera.
func TestScopeFiltersLists(t *testing.T) {
	f := newScopeFixture(t)
	lists := []struct{ auth, path string }{
		{"cookie", "/api/v1/cameras"},
		{"cookie", "/api/v1/alarms?state=all"},
		{"cookie", "/api/v1/events"},
		{"cookie", "/api/v1/evidence"},
		{"bearer", "/api/v2/cameras"},
		{"bearer", "/api/v2/alarms?state=all"},
		{"bearer", "/api/v2/alarms?state=all&limit=1"},
		{"bearer", "/api/v2/events"},
		{"bearer", "/api/v2/events?limit=1"},
		{"phone", "/cameras"},
		{"phone", "/alerts"},
		{"phone", "/events"},
	}
	for _, l := range lists {
		t.Run(l.auth+" "+l.path, func(t *testing.T) {
			rec := f.call(t, l.auth, "", "GET", l.path, "")
			body := rec.Body.String()
			if rec.Code != 200 || !strings.Contains(body, f.front.ID) || strings.Contains(body, f.back.ID) || strings.Contains(body, f.storage.ID) {
				t.Errorf("%d %s", rec.Code, body)
			}
			if l.auth == "phone" {
				return
			}
			var rec2 *httptest.ResponseRecorder
			if l.auth == "cookie" {
				rec2 = webDo(t, f.h, f.cookieFor(t, f.allOp), "GET", l.path, "")
			} else {
				rec2 = f.call(t, "token:", f.allOp, "GET", l.path, "")
			}
			if body := rec2.Body.String(); !strings.Contains(body, f.back.ID) && !strings.Contains(l.path, "limit=1") {
				t.Errorf("unscoped user misses the other camera: %s", body)
			}
		})
	}
	// Paging a scoped list walks past hidden rows: page 1 has front's
	// alarm, then the list ends.
	code, m := v2Call(t, f.h, "GET", "/api/v2/alarms?state=all&limit=1", f.op, "")
	if code != 200 || len(items(t, m)) != 1 || m["nextCursor"] != nil {
		t.Fatalf("scoped page: %d %v", code, m)
	}
	// The unscoped Operator sees the Storage alarm; so does the Admin.
	for _, tok := range []string{f.allOp, f.admin} {
		if _, m := v2Call(t, f.h, "GET", "/api/v2/alarms?state=all", tok, ""); len(items(t, m)) != 3 {
			t.Fatalf("unscoped alarms %v", m)
		}
	}
	// Filtering by a hidden camera finds nothing.
	if _, m := v2Call(t, f.h, "GET", "/api/v2/events?cameraId="+f.back.ID, f.op, ""); len(items(t, m)) != 0 {
		t.Fatalf("hidden camera filter %v", m)
	}
	// Groups: a scoped user sees only their own.
	if _, m := v2Call(t, f.h, "GET", "/api/v2/camera-groups", f.op, ""); len(items(t, m)) != 1 || items(t, m)[0]["id"] != f.frontG.ID {
		t.Fatalf("scoped groups %v", m)
	}
	if _, m := v2Call(t, f.h, "GET", "/api/v2/camera-groups", f.allOp, ""); len(items(t, m)) != 2 {
		t.Fatalf("all groups %v", m)
	}
}

// TestScopeOfPairedPhones: a phone acts for the user who paired it; once
// that user is gone it sees nothing, and alarm pushes follow the scope.
func TestScopeOfPairedPhones(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	d, _ := f.s.Store.DeviceForToken(ctx, f.phone)
	_ = f.s.Store.SetDeviceAPNSToken(ctx, d.ID, strings.Repeat("cd", 32))
	if toks, _ := f.s.pushTokensFor(ctx, f.alarm[f.front.ID]); len(toks) != 1 {
		t.Fatalf("push for a visible alarm: %v", toks)
	}
	for _, a := range []*store.Alarm{f.alarm[f.back.ID], f.storage} {
		if toks, _ := f.s.pushTokensFor(ctx, a); len(toks) != 0 {
			t.Fatalf("push for a hidden alarm %s: %v", a.Kind, toks)
		}
	}
	op, _ := f.s.Store.UserByName(ctx, "op")
	if err := f.s.Store.DeleteUser(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, f.h, "GET", "/cameras", f.phone, ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("orphaned phone: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, f.h, "GET", "/cameras/"+f.front.ID, f.phone, ""); rec.Code != 404 {
		t.Fatalf("orphaned phone detail: %d", rec.Code)
	}
}

// TestScopeFiltersEventStream: a scoped connection hears only about its
// cameras, config.changed lists only those, and a scope change applies
// to a live connection.
func TestScopeFiltersEventStream(t *testing.T) {
	f := newScopeFixture(t)
	s := f.s
	_, wsURL := streamServer(t, s)
	c, resp, err := dialStream(t, wsURL+"?topics=alarm,event,camera,config,activity", bearerHeader(f.op))
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	hello := readStream(t, c)
	if sc, _ := hello.Data["user"].(map[string]any)["scope"].(map[string]any); sc["allCameras"] != false {
		t.Fatalf("hello %+v", hello)
	}
	ctx := context.Background()
	// Everything about back (and the site-wide Storage alarm) is skipped;
	// the marker message about front shows nothing came before it.
	s.publishAlarmState(f.alarm[f.back.ID])
	s.publishAlarmState(f.storage)
	s.publishEventNew(nil, f.event[f.back.ID])
	s.publishEventUpdated(f.event[f.back.ID].ID)
	s.publishCameraOnline(f.back.ID, true)
	s.publishActivity(f.back.ID, "motion")
	s.publishActivity(f.front.ID, "motion")
	if m := readStream(t, c); m.Type != "camera.activity" || m.Data["cameraId"] != f.front.ID {
		t.Fatalf("first message %+v", m)
	}
	s.publishAlarmState(f.alarm[f.front.ID])
	if m := readStream(t, c); m.Type != "alarm.updated" || m.Data["id"] != f.alarm[f.front.ID].ID {
		t.Fatalf("front alarm %+v", m)
	}
	cams, _ := s.Store.Cameras(ctx)
	s.publishConfigChanged(cams)
	if m := readStream(t, c); m.Type != "config.changed" || fmt.Sprint(m.Data["cameraIds"]) != fmt.Sprint([]any{f.front.ID}) {
		t.Fatalf("config %+v", m)
	}
	// Moving back into Front over the API reaches the live connection.
	code, _ := v2Call(t, f.h, "PUT", "/api/v2/camera-groups/"+f.frontG.ID, f.admin,
		`{"name":"Front","cameraIds":["`+f.front.ID+`","`+f.back.ID+`"]}`)
	if code != 200 {
		t.Fatal(code)
	}
	if m := readStream(t, c); m.Type != "config.changed" || len(m.Data["cameraIds"].([]any)) != 2 {
		t.Fatalf("config after regrouping %+v", m)
	}
	s.publishActivity(f.back.ID, "motion")
	if m := readStream(t, c); m.Type != "camera.activity" || m.Data["cameraId"] != f.back.ID {
		t.Fatalf("after regrouping %+v", m)
	}
}

func TestScopeStreamRecheck(t *testing.T) {
	setStreamVar(t, &streamRecheckEvery, 50*time.Millisecond)
	f := newScopeFixture(t)
	_, wsURL := streamServer(t, f.s)
	c, _, err := dialStream(t, wsURL+"?topics=activity", bearerHeader(f.op))
	if err != nil {
		t.Fatal(err)
	}
	readStream(t, c)
	// A scope change made behind the API's back is picked up on recheck.
	op, _ := f.s.Store.UserByName(context.Background(), "op")
	_ = f.s.Store.SetUserScope(context.Background(), op.ID, store.UserScope{AllCameras: true})
	time.Sleep(200 * time.Millisecond)
	f.s.publishActivity(f.back.ID, "motion")
	if m := readStream(t, c); m.Data["cameraId"] != f.back.ID {
		t.Fatalf("%+v", m)
	}
}

func TestV2CameraGroupsCRUD(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	op := v2Token(t, s, "op", "Operator")
	a, b := addCam(t, s), addCam(t, s)

	code, g := v2Call(t, h, "POST", "/api/v2/camera-groups", admin, `{"name":" Warehouse ","cameraIds":["`+a.ID+`"]}`)
	if code != 201 || g["name"] != "Warehouse" || len(g["cameraIds"].([]any)) != 1 || !isMs(g["createdAt"]) {
		t.Fatalf("create: %d %v", code, g)
	}
	gid := g["id"].(string)
	for _, c := range []struct {
		method, path, body string
		code               int
		err                string
	}{
		{"POST", "/api/v2/camera-groups", `{"name":"warehouse","cameraIds":[]}`, 409, "conflict"},
		{"POST", "/api/v2/camera-groups", `{"name":"Office","cameraIds":["nope"]}`, 400, "bad_request"},
		{"POST", "/api/v2/camera-groups", `{"name":"  ","cameraIds":[]}`, 400, "bad_request"},
		{"POST", "/api/v2/camera-groups", `{"name":"Office"}`, 400, "bad_request"},
		{"POST", "/api/v2/camera-groups", `{"name":"Office","cameraIds":[],"extra":1}`, 400, "bad_request"},
		{"PUT", "/api/v2/camera-groups/nope", `{"name":"X","cameraIds":[]}`, 404, "not_found"},
		{"DELETE", "/api/v2/camera-groups/nope", "", 404, "not_found"},
	} {
		if code, m := v2Call(t, h, c.method, c.path, admin, c.body); code != c.code || errCode(m) != c.err {
			t.Errorf("%s %s %s: %d %v", c.method, c.path, c.body, code, m)
		}
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/camera-groups", op, `{"name":"Mine","cameraIds":[]}`); code != 403 {
		t.Fatalf("operator create: %d", code)
	}
	code, g = v2Call(t, h, "PUT", "/api/v2/camera-groups/"+gid, admin, `{"name":"Yard","cameraIds":["`+a.ID+`","`+b.ID+`"]}`)
	if code != 200 || g["name"] != "Yard" || len(g["cameraIds"].([]any)) != 2 {
		t.Fatalf("put: %d %v", code, g)
	}
	// Cameras carry their groups.
	_, cam := v2Call(t, h, "GET", "/api/v2/cameras/"+b.ID, admin, "")
	if fmt.Sprint(cam["groupIds"]) != fmt.Sprint([]any{gid}) {
		t.Fatalf("camera groups %v", cam["groupIds"])
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/camera-groups", op, ""); len(items(t, m)) != 1 {
		t.Fatalf("list %v", m)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/camera-groups/"+gid, admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, cam := v2Call(t, h, "GET", "/api/v2/cameras/"+b.ID, admin, ""); fmt.Sprint(cam["groupIds"]) != "[]" {
		t.Fatalf("after delete %v", cam["groupIds"])
	}
	got := auditActions(t, s, "Cameras")
	for _, a := range []string{"Added camera group", "Edited camera group", "Deleted camera group"} {
		if !got[a] {
			t.Errorf("not audited: %s", a)
		}
	}
}

func TestV2CameraGroupIDsOnCreateAndPatch(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	g, _ := s.Store.SaveCameraGroup(context.Background(), "", "Front", nil)
	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Door","rtspUrl":"rtsp://10.0.0.9/s","groupIds":["`+g.ID+`"]}`)
	if code != 201 || fmt.Sprint(cam["groupIds"]) != fmt.Sprint([]any{g.ID}) {
		t.Fatalf("create: %d %v", code, cam)
	}
	id := cam["id"].(string)
	if code, m := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Gate","rtspUrl":"rtsp://10.0.0.8/s","groupIds":["nope"]}`); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("unknown group: %d %v", code, m)
	}
	if cams, _ := s.Store.Cameras(context.Background()); len(cams) != 1 {
		t.Fatal("a camera with an unknown group was added")
	}
	if code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"groupIds":["nope"]}`); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("patch unknown group: %d %v", code, m)
	}
	if code, cam := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"location":"Hall"}`); code != 200 || fmt.Sprint(cam["groupIds"]) != fmt.Sprint([]any{g.ID}) {
		t.Fatalf("patch keeps groups: %d %v", code, cam)
	}
	if code, cam := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"groupIds":[]}`); code != 200 || fmt.Sprint(cam["groupIds"]) != "[]" {
		t.Fatalf("patch clears groups: %d %v", code, cam)
	}
	if !auditActions(t, s, "Cameras")["Set camera groups"] {
		t.Error("group membership not audited")
	}
}

func TestV2UserScope(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	g, _ := s.Store.SaveCameraGroup(context.Background(), "", "Front", nil)

	code, u := v2Call(t, h, "POST", "/api/v2/users", admin, `{"name":"op","role":"Operator","password":"long enough pw","scope":{"allCameras":false,"groupIds":["`+g.ID+`"]}}`)
	if code != 201 || u["source"] != "local" {
		t.Fatalf("create: %d %v", code, u)
	}
	if sc := u["scope"].(map[string]any); sc["allCameras"] != false || fmt.Sprint(sc["groupIds"]) != fmt.Sprint([]any{g.ID}) {
		t.Fatalf("scope %v", sc)
	}
	id := u["id"].(string)
	for _, c := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", "/api/v2/users", `{"name":"x","role":"Operator","password":"long enough pw","scope":{"allCameras":false,"groupIds":["nope"]}}`, 400},
		{"POST", "/api/v2/users", `{"name":"y","role":"Admin","password":"long enough pw","scope":{"allCameras":false,"groupIds":[]}}`, 400},
		{"PATCH", "/api/v2/users/" + id, `{"scope":{"allCameras":false,"groupIds":["nope"]}}`, 400},
		{"PATCH", "/api/v2/users/" + id, `{"role":"Admin","scope":{"allCameras":false,"groupIds":[]}}`, 400},
	} {
		if code, m := v2Call(t, h, c.method, c.path, admin, c.body); code != c.code {
			t.Errorf("%s %s: %d %v", c.method, c.body, code, m)
		}
	}
	if _, err := s.Store.UserByName(context.Background(), "x"); err == nil {
		t.Fatal("user with a bad scope was created")
	}
	// A scope change takes effect without signing the user out.
	opTok := v2SignInExisting(t, s, "op", "long enough pw")
	code, u = v2Call(t, h, "PATCH", "/api/v2/users/"+id, admin, `{"scope":{"allCameras":true,"groupIds":[]}}`)
	if code != 200 || u["scope"].(map[string]any)["allCameras"] != true {
		t.Fatalf("patch: %d %v", code, u)
	}
	if code, me := v2Call(t, h, "GET", "/api/v2/me", opTok, ""); code != 200 || me["scope"].(map[string]any)["allCameras"] != true {
		t.Fatalf("me: %d %v", code, me)
	}
	if !auditActions(t, s, "Users")["Changed user scope"] {
		t.Error("scope change not audited")
	}
	// Admins always show every camera; v1 users default to every camera.
	_, list := v2Call(t, h, "GET", "/api/v2/users", admin, "")
	for _, u := range items(t, list) {
		if u["role"] == "Admin" && u["scope"].(map[string]any)["allCameras"] != true {
			t.Fatalf("admin scope %v", u)
		}
	}
	c := webAs(t, s, "web-admin", "Admin")
	if rec := webDo(t, h, c, "POST", "/api/v1/users", `{"name":"v1user","role":"Viewer","password":"long enough pw"}`); rec.Code != 201 {
		t.Fatalf("v1 create: %d %s", rec.Code, rec.Body)
	}
	v1u, _ := s.Store.UserByName(context.Background(), "v1user")
	if sc, _ := s.Store.UserScope(context.Background(), v1u.ID); !sc.AllCameras {
		t.Fatal("v1 users see every camera")
	}
}

func v2SignInExisting(t *testing.T, s *Server, name, password string) string {
	t.Helper()
	code, m := v2Call(t, s.Handler(), "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"`+name+`","password":"`+password+`","client":"test"}`)
	if code != 200 {
		t.Fatalf("sign in: %d %v", code, m)
	}
	return m["accessToken"].(string)
}

func TestV2AuditPagingAndVerify(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin") // signing in is audited
	op := v2Token(t, s, "op", "Operator")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = s.Store.Audit(ctx, "boss", "Test", fmt.Sprintf("Action %d", i), "")
	}
	// A recording server's own chain, forwarded.
	rsPrev := ""
	for i := 0; i < 3; i++ {
		e := store.AuditEntry{ID: store.NewID(), Time: time.Now().Unix(), User: "System", Area: "Evidence", Action: fmt.Sprintf("RS action %d", i), PreviousHash: rsPrev}
		e.ChainHash = auditHash(e)
		rsPrev = e.ChainHash
		if err := s.Store.AppendForwardedAudit(ctx, "rs2", e); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			_ = s.Store.AppendForwardedAudit(ctx, "rs2", e) // a re-send is ignored
		}
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/audit", op, ""); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	// Page through everything, newest first.
	var all []map[string]any
	cursor := ""
	for page := 0; page < 20; page++ {
		path := "/api/v2/audit?limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, m := v2Call(t, h, "GET", path, admin, "")
		if code != 200 {
			t.Fatalf("page: %d %v", code, m)
		}
		all = append(all, items(t, m)...)
		next, _ := m["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(all) != 10 || all[0]["action"] != "RS action 2" || all[0]["serverId"] != "rs2" || !isMs(all[0]["at"]) {
		t.Fatalf("entries %d: %v", len(all), all)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/audit?serverId=rs2", admin, ""); code != 200 || len(items(t, m)) != 3 {
		t.Fatalf("rs2 only: %v", m)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/audit?serverId=", admin, ""); len(items(t, m)) != 7 {
		t.Fatalf("own only: %v", m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/audit?cursor=!!", admin, ""); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("bad cursor: %d %v", code, m)
	}

	code, v := v2Call(t, h, "GET", "/api/v2/audit/verify", admin, "")
	servers, _ := v["servers"].([]any)
	if code != 200 || v["ok"] != true || v["entries"] != 10.0 || v["brokenAt"] != nil || len(servers) != 1 {
		t.Fatalf("verify: %d %v", code, v)
	}
	if rs := servers[0].(map[string]any); rs["serverId"] != "rs2" || rs["ok"] != true || rs["entries"] != 3.0 {
		t.Fatalf("rs chain %v", rs)
	}

	// Tamper with a forwarded entry's server hash: only rs2's chain breaks.
	if _, err := s.Store.DB.Exec(`UPDATE audit SET server_chain_hash = 'x' WHERE action = 'RS action 1'`); err != nil {
		t.Fatal(err)
	}
	_, v = v2Call(t, h, "GET", "/api/v2/audit/verify", admin, "")
	if rs := v["servers"].([]any)[0].(map[string]any); v["ok"] != false || v["brokenAt"] != nil || rs["ok"] != false || rs["entries"] != 1.0 {
		t.Fatalf("rs tampered: %v", v)
	}
	// Tamper with an own entry: the site chain breaks there.
	var id string
	_ = s.Store.DB.QueryRow(`SELECT id FROM audit WHERE action = 'Action 2'`).Scan(&id)
	if _, err := s.Store.DB.Exec(`UPDATE audit SET detail = 'edited' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	_, v = v2Call(t, h, "GET", "/api/v2/audit/verify", admin, "")
	if v["ok"] != false || v["brokenAt"] != id {
		t.Fatalf("own tampered: %v", v)
	}
}

// auditHash is the chain hash a recording server's store gives an entry.
func auditHash(e store.AuditEntry) string { return store.AuditChainHash(e) }
