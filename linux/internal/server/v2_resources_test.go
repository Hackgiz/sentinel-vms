package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

// v2Token signs a new user in over v2 and returns their access token.
func v2Token(t *testing.T, s *Server, name, role string) string {
	t.Helper()
	return v2SignIn(t, s, name, role)["accessToken"].(string)
}

func items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("no items in %v", m)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(map[string]any))
	}
	return out
}

// isMs reports whether v looks like a Unix-millisecond time.
func isMs(v any) bool {
	f, ok := v.(float64)
	return ok && f > 1e12
}

func auditActions(t *testing.T, s *Server, area string) map[string]bool {
	t.Helper()
	entries, err := s.Store.AuditLog(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.Area == area {
			got[e.Action] = true
		}
	}
	return got
}

func TestV2CamerasCRUD(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin, op, viewer := v2Token(t, s, "admin", "Admin"), v2Token(t, s, "op", "Operator"), v2Token(t, s, "viewer", "Viewer")
	body := `{"name":" Door ","rtspUrl":"rtsp://bob:secret@10.0.0.5:554/s","alertOn":"people"}`

	if code, m := v2Call(t, h, "POST", "/api/v2/cameras", op, body); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("operator create: %d %v", code, m)
	}
	for _, bad := range []string{
		`{"name":"x","rtspUrl":"http://nope"}`,
		`{"name":"","rtspUrl":"rtsp://10.0.0.5/s"}`,
		`{"name":"x","rtspUrl":"rtsp://10.0.0.5/s","alertOn":"everything"}`,
		`{"name":"x","rtspUrl":"rtsp://10.0.0.5/s","password2":"x"}`,
		`{"name":"x","rtspUrl":"rtsp://10.0.0.5/s","retentionDays":9999}`,
		`{"name":"x","rtspUrl":"rtsp://10.0.0.5/s","sortOrder":3}`,
	} {
		if code, m := v2Call(t, h, "POST", "/api/v2/cameras", admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("create %s: %d %v", bad, code, m)
		}
	}
	rev := s.cfgRev.Load()
	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, body)
	if code != 201 {
		t.Fatalf("create: %d %v", code, cam)
	}
	if cam["name"] != "Door" || cam["rtspUrl"] != "rtsp://10.0.0.5:554/s" || cam["username"] != "bob" || cam["hasPassword"] != true ||
		cam["alertOn"] != "people" || cam["recording"] != true || cam["motionLevel"] != 2.0 || cam["cameraAnalytics"] != true || !isMs(cam["createdAt"]) {
		t.Fatalf("created camera %v", cam)
	}
	for _, k := range []string{"rtspURL", "onvifURL", "subRTSPURL"} {
		if _, ok := cam[k]; ok {
			t.Fatalf("v1 key %s in %v", k, cam)
		}
	}
	if _, ok := cam["recorderId"]; !ok {
		t.Fatalf("no recorderId in %v", cam)
	}
	if st, _ := cam["status"].(map[string]any); st == nil || st["message"] == "" {
		t.Fatalf("status %v", cam["status"])
	}
	if s.cfgRev.Load() == rev {
		t.Fatal("create didn't apply the configuration")
	}
	id := cam["id"].(string)

	code, list := v2Call(t, h, "GET", "/api/v2/cameras", viewer, "")
	if cams := items(t, list); code != 200 || len(cams) != 1 || cams[0]["id"] != id {
		t.Fatalf("list: %d %v", code, list)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+id, viewer, ""); code != 200 || m["id"] != id {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cameras/nope", viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("get unknown: %d %v", code, m)
	}

	// PATCH keeps what isn't sent.
	patch := func(body string) map[string]any {
		t.Helper()
		code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, body)
		if code != 200 {
			t.Fatalf("patch %s: %d %v", body, code, m)
		}
		return m
	}
	rev = s.cfgRev.Load()
	m := patch(`{"location":"Lobby"}`)
	if m["location"] != "Lobby" || m["name"] != "Door" || m["username"] != "bob" || m["hasPassword"] != true || m["alertOn"] != "people" || m["recording"] != true {
		t.Fatalf("partial patch %v", m)
	}
	if s.cfgRev.Load() == rev {
		t.Fatal("patch didn't apply the configuration")
	}
	if c, _ := s.Store.Camera(context.Background(), id); func() string { p, _ := s.Store.CameraPassword(c); return p }() != "secret" {
		t.Fatal("patch lost the password")
	}
	if m := patch(`{"password":""}`); m["hasPassword"] != false || m["username"] != "bob" {
		t.Fatalf("clear password %v", m)
	}
	if m := patch(`{"rtspUrl":"rtsp://carol:pw2@10.0.0.6/s"}`); m["rtspUrl"] != "rtsp://10.0.0.6/s" || m["username"] != "carol" || m["hasPassword"] != true {
		t.Fatalf("credentials in URL %v", m)
	}
	if m := patch(`{"sortOrder":7,"recording":false,"alertOn":"auto","motionLevel":0}`); m["sortOrder"] != 7.0 || m["recording"] != false || m["alertOn"] != "auto" || m["motionLevel"] != 0.0 {
		t.Fatalf("patch fields %v", m)
	}
	for _, bad := range []string{`{"alertOn":"sometimes"}`, `{"motionLevel":4}`, `{"rtspUrl":"nope"}`, `{"camera":"x"}`} {
		if code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	if c, _ := s.Store.Camera(context.Background(), id); c.SortOrder != 7 || c.MotionLevel != 0 {
		t.Fatalf("rejected patch changed the camera: %+v", c)
	}
	if code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/nope", admin, `{"name":"x"}`); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("patch unknown: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, op, `{"name":"x"}`); code != 403 {
		t.Fatalf("operator patch: %d", code)
	}

	if code, _ := v2Call(t, h, "DELETE", "/api/v2/cameras/"+id, op, ""); code != 403 {
		t.Fatalf("operator delete: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/cameras/"+id, admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/cameras/"+id, admin, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("second delete: %d %v", code, m)
	}
	if got := auditActions(t, s, "Cameras"); !got["Added camera"] || !got["Edited camera"] || !got["Deleted camera"] {
		t.Fatalf("audit %v", got)
	}
}

// raiseKinds opens one alarm per kind on cam, the given seconds apart
// (0 = all in the same second).
func raiseKinds(t *testing.T, s *Server, cam *store.Camera, t0 time.Time, step time.Duration, kinds ...string) []*store.Alarm {
	t.Helper()
	var out []*store.Alarm
	for i, k := range kinds {
		a, isNew, err := s.Store.RaiseAlarm(context.Background(), store.AlarmInput{CameraID: cam.ID, CameraName: cam.Name, Kind: k,
			Title: k + " detected", Severity: "Warning", At: t0.Add(time.Duration(i) * step), Origin: "test"})
		if err != nil || !isNew {
			t.Fatalf("raise %s: %v %v", k, isNew, err)
		}
		out = append(out, a)
	}
	return out
}

func TestV2AlarmsPagingAndFilters(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	viewer := v2Token(t, s, "viewer", "Viewer")
	c1 := addCam(t, s)
	c2, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Yard", RTSPURL: "rtsp://10.0.0.8/s"})
	t0 := time.Now().Add(-time.Hour)
	raiseKinds(t, s, c1, t0, time.Second, "A", "B", "C")
	same := raiseKinds(t, s, c1, t0.Add(10*time.Second), 0, "D", "E", "F") // one second: order falls back to id
	other := raiseKinds(t, s, c2, t0.Add(20*time.Second), time.Second, "G")
	if _, err := s.Store.SetAlarmState(ctx, same[0].ID, "Resolved", "x", ""); err != nil {
		t.Fatal(err)
	}
	_ = s.Store.SaveAlarmRule(ctx, &rules.Rule{Name: "Yard", Enabled: true, CameraIDs: []string{c2.ID}, MinSeverity: "Info",
		Delivery: rules.DeliveryDashboard, Instructions: "Check the gate."})
	s.invalidateRules()

	// Page through everything three at a time; a new alarm raised meanwhile
	// sorts before the first page and doesn't shift the rest.
	var seen []map[string]any
	cursor := ""
	for page := 0; ; page++ {
		path := "/api/v2/alarms?state=all&limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, m := v2Call(t, h, "GET", path, viewer, "")
		if code != 200 {
			t.Fatalf("page %d: %d %v", page, code, m)
		}
		seen = append(seen, items(t, m)...)
		if page == 0 {
			raiseKinds(t, s, c1, time.Now(), time.Second, "H")
		}
		next, _ := m["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 7 {
		t.Fatalf("paged %d alarms, want 7", len(seen))
	}
	ids := map[string]bool{}
	for i, a := range seen {
		if ids[a["id"].(string)] {
			t.Fatalf("alarm %v twice", a["id"])
		}
		ids[a["id"].(string)] = true
		if i > 0 && a["createdAt"].(float64) > seen[i-1]["createdAt"].(float64) {
			t.Fatalf("not newest first at %d", i)
		}
		if !isMs(a["createdAt"]) || !isMs(a["lastEventAt"]) || a["cameraId"] == nil || a["cameraID"] != nil || a["responseLog"] == nil {
			t.Fatalf("alarm keys %v", a)
		}
	}
	if seen[0]["id"] != other[0].ID {
		t.Fatalf("newest alarm %v", seen[0])
	}
	if in := seen[0]["instructions"].([]any); len(in) != 1 || in[0].(map[string]any)["text"] != "Check the gate." {
		t.Fatalf("instructions %v", seen[0]["instructions"])
	}

	// Open only by default (7 raised + H − 1 resolved); camera filter.
	if code, m := v2Call(t, h, "GET", "/api/v2/alarms", viewer, ""); code != 200 || len(items(t, m)) != 7 || m["nextCursor"] != nil {
		t.Fatalf("open alarms: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/alarms?state=all&cameraId="+c2.ID, viewer, ""); code != 200 || len(items(t, m)) != 1 {
		t.Fatalf("camera filter: %d %v", code, m)
	}
	for _, q := range []string{"limit=0", "limit=501", "limit=x", "cursor=%21%21", "cursor=bm9wZQ", "state=closed"} {
		if code, m := v2Call(t, h, "GET", "/api/v2/alarms?"+q, viewer, ""); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("%s: %d %v", q, code, m)
		}
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/alarms/"+other[0].ID, viewer, ""); code != 200 || m["cameraName"] != "Yard" || m["instructions"] == nil {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/alarms/nope", viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("get unknown: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/alarms", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestV2AlarmStateNotesAndSnooze(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	op, viewer := v2Token(t, s, "op", "Operator"), v2Token(t, s, "viewer", "Viewer")
	c := addCam(t, s)
	a := raiseKinds(t, s, c, time.Now(), time.Second, "Motion")[0]
	path := "/api/v2/alarms/" + a.ID + "/state"
	defer func(u time.Duration) { snoozeUnit = u }(snoozeUnit)
	snoozeUnit = 20 * time.Millisecond

	var mu sync.Mutex
	var published []string
	sub := s.Bus.Subscribe("test", 0)
	bus.Handle(sub, TopicAlarmState, func(a store.Alarm) { mu.Lock(); published = append(published, a.State); mu.Unlock() })
	defer sub.Close()

	if code, m := v2Call(t, h, "POST", path, viewer, `{"state":"Acknowledged"}`); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("viewer: %d %v", code, m)
	}
	for _, bad := range []string{
		`{"state":"Done"}`,
		`{"state":"Acknowledged","snoozeMinutes":5}`,
		`{"state":"Snoozed","snoozeMinutes":0}`,
		`{"state":"Snoozed","snoozeMinutes":1441}`,
		`{"state":"Acknowledged","note":"` + strings.Repeat("x", 501) + `"}`,
		`{"state":"Acknowledged","by":"me"}`,
	} {
		if code, m := v2Call(t, h, "POST", path, op, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("%.60s: %d %v", bad, code, m)
		}
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/alarms/nope/state", op, `{"state":"Acknowledged"}`); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown alarm: %d %v", code, m)
	}

	lastLog := func(m map[string]any) string {
		log := m["responseLog"].([]any)
		return log[len(log)-1].(string)
	}
	code, m := v2Call(t, h, "POST", path, op, `{"state":"Acknowledged","note":"on my way"}`)
	if code != 200 || m["state"] != "Acknowledged" || m["owner"] != "op" || !strings.HasSuffix(lastLog(m), "Acknowledged by op: on my way") || !isMs(m["updatedAt"]) {
		t.Fatalf("acknowledge: %d %v", code, m)
	}
	if !auditActions(t, s, "Alarms")["Set alarm acknowledged"] {
		t.Fatal("no audit entry")
	}

	// A snooze ends by itself, back to New.
	code, m = v2Call(t, h, "POST", path, op, `{"state":"Snoozed","snoozeMinutes":2}`)
	if code != 200 || m["state"] != "Snoozed" || !strings.HasSuffix(lastLog(m), "Snoozed for 2 min by op") {
		t.Fatalf("snooze: %d %v", code, m)
	}
	waitState := func(want string) *store.Alarm {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			got, _ := s.Store.Alarm(context.Background(), a.ID)
			if got.State == want || time.Now().After(deadline) {
				return got
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := waitState("New"); got.State != "New" || !strings.HasSuffix(got.ResponseLog[len(got.ResponseLog)-1], "Snooze ended") {
		t.Fatalf("snooze didn't end: %+v", got)
	}

	// The default is 15 minutes; acting on the alarm meanwhile cancels the end.
	code, m = v2Call(t, h, "POST", path, op, `{"state":"Snoozed"}`)
	if code != 200 || !strings.HasSuffix(lastLog(m), "Snoozed for 15 min by op") {
		t.Fatalf("default snooze: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "POST", path, op, `{"state":"Investigating"}`); code != 200 {
		t.Fatalf("investigate: %d", code)
	}
	time.Sleep(15*snoozeUnit + 100*time.Millisecond)
	if got, _ := s.Store.Alarm(context.Background(), a.ID); got.State != "Investigating" {
		t.Fatalf("cancelled snooze still ended: %s", got.State)
	}

	sub.Drain()
	mu.Lock()
	defer mu.Unlock()
	if want := "Acknowledged,Snoozed,New,Snoozed,Investigating"; strings.Join(published, ",") != want {
		t.Fatalf("published %v, want %s", published, want)
	}
}

func TestV2LockEvidence(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	op, viewer := v2Token(t, s, "op", "Operator"), v2Token(t, s, "viewer", "Viewer")
	c := addCam(t, s)
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	dir := filepath.Join(recOf(s).RecordingsRoot(), c.ID)
	_ = os.MkdirAll(dir, 0o755)
	seg := filepath.Join(dir, start.Format("20060102-150405")+"-000001.mp4")
	if err := os.WriteFile(seg, []byte("fake mp4 bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(seg, start.Add(time.Minute), start.Add(time.Minute))
	a := raiseKinds(t, s, c, start.Add(20*time.Second), time.Second, "Motion")[0]
	storage, _, _ := s.Store.RaiseAlarm(ctx, store.AlarmInput{Kind: "Storage", Title: "Disk low", Severity: "Warning", At: time.Now(), Origin: "test"})

	path := "/api/v2/alarms/" + a.ID + "/evidence"
	if code, m := v2Call(t, h, "POST", path, viewer, ""); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("viewer: %d %v", code, m)
	}
	code, ev := v2Call(t, h, "POST", path, op, "")
	if code != 200 || !strings.HasPrefix(ev["caseId"].(string), "HG-") || ev["cameraId"] != c.ID || ev["alarmId"] != a.ID ||
		ev["lockedBy"] != "op" || !isMs(ev["lockedAt"]) || len(ev["sha256"].(string)) != 64 || ev["caseID"] != nil {
		t.Fatalf("lock: %d %v", code, ev)
	}
	if code, again := v2Call(t, h, "POST", path, op, ""); code != 200 || again["id"] != ev["id"] {
		t.Fatalf("retry: %d %v", code, again)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/alarms/"+storage.ID+"/evidence", op, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("no recording: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/alarms/nope/evidence", op, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown: %d %v", code, m)
	}
	if !auditActions(t, s, "Evidence")["Locked evidence"] {
		t.Fatal("no audit entry")
	}
}

func TestV2EventsPaging(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	viewer := v2Token(t, s, "viewer", "Viewer")
	c1 := addCam(t, s)
	t0 := time.Now().Add(-time.Hour).Unix()
	for i, at := range []int64{t0, t0 + 1, t0 + 1, t0 + 1, t0 + 2} {
		cam := c1.ID
		if i == 4 {
			cam = "OTHER"
		}
		e := &store.Event{CameraID: cam, Kind: "Person", Score: 0.8, CreatedAt: at, Box: "[0.1,0.2,0.3,0.4]", Tags: "red, coat"}
		if err := s.Store.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var seen []map[string]any
	cursor := ""
	for {
		code, m := v2Call(t, h, "GET", "/api/v2/events?limit=2&cursor="+cursor, viewer, "")
		if code != 200 {
			t.Fatalf("page: %d %v", code, m)
		}
		seen = append(seen, items(t, m)...)
		next, _ := m["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	ids := map[string]bool{}
	for _, e := range seen {
		ids[e["id"].(string)] = true
	}
	if len(seen) != 5 || len(ids) != 5 || seen[0]["cameraId"] != "OTHER" {
		t.Fatalf("paged events %v", seen)
	}
	e := seen[1]
	if !isMs(e["createdAt"]) || e["cameraID"] != nil || e["hasThumbnail"] != false || len(e["box"].([]any)) != 4 || len(e["tags"].([]any)) != 2 {
		t.Fatalf("event keys %v", e)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/events?cameraId="+c1.ID, viewer, ""); code != 200 || len(items(t, m)) != 4 {
		t.Fatalf("camera filter: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/events?limit=600", viewer, ""); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("limit: %d %v", code, m)
	}
	id := e["id"].(string)
	if code, m := v2Call(t, h, "GET", "/api/v2/events/"+id, viewer, ""); code != 200 || m["id"] != id {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/events/nope", viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("get unknown: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/events/"+id+"/thumbnail.jpg", viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("no thumbnail: %d %v", code, m)
	}
}

func TestV2EventThumbnail(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	viewer := v2Token(t, s, "viewer", "Viewer")
	c := addCam(t, s)
	dir := filepath.Join(s.Store.DataDir(), "events")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "snap.jpg"), []byte("\xff\xd8jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &store.Event{CameraID: c.ID, Kind: "Person", CreatedAt: time.Now().Unix(), Thumbnail: filepath.Join(dir, "snap.jpg")}
	_ = s.Store.AddEvent(context.Background(), e)
	rec := do(t, h, "GET", "/api/v2/events/"+e.ID+"/thumbnail.jpg", viewer, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.String() != "\xff\xd8jpeg" {
		t.Fatalf("thumbnail: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
}

func TestV2TimelineAndRecordings(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	viewer := v2Token(t, s, "viewer", "Viewer")
	q := fmt.Sprintf("?from=%d&to=%d", t0.Add(-time.Hour).UnixMilli(), t0.Add(time.Hour).UnixMilli())

	code, tl := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/timeline"+q, viewer, "")
	if code != 200 || tl["from"] != float64(t0.Add(-time.Hour).UnixMilli()) || !isMs(tl["now"]) {
		t.Fatalf("timeline: %d %v", code, tl)
	}
	spans := tl["spans"].([]any)
	if len(spans) != 2 || spans[0].(map[string]any)["start"] != float64(t0.UnixMilli()) || spans[1].(map[string]any)["media"] != 60.0 {
		t.Fatalf("spans %v", spans)
	}
	events, alarms := tl["events"].([]any), tl["alarms"].([]any)
	if len(events) != 1 || len(alarms) != 1 {
		t.Fatalf("markers %v %v", events, alarms)
	}
	if ev := events[0].(map[string]any); ev["cameraId"] != c.ID || ev["createdAt"] != float64(t0.Add(20*time.Second).UnixMilli()) {
		t.Fatalf("event marker %v", ev)
	}
	if al := alarms[0].(map[string]any); al["cameraId"] != c.ID || al["createdAt"] != float64(t0.Add(100*time.Second).UnixMilli()) || al["severity"] != "Critical" {
		t.Fatalf("alarm marker %v", al)
	}
	if tl["pending"] == nil {
		t.Fatal("pending is null")
	}
	for _, bad := range []string{"", "?from=1&to=x", fmt.Sprintf("?from=%d&to=%d", t0.UnixMilli(), t0.Add(48*time.Hour).UnixMilli())} {
		if code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/timeline"+bad, viewer, ""); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("window %q: %d %v", bad, code, m)
		}
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cameras/nope/timeline"+q, viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown camera: %d %v", code, m)
	}

	code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/recordings", viewer, "")
	segs := items(t, m)
	if code != 200 || len(segs) != 2 {
		t.Fatalf("recordings: %d %v", code, m)
	}
	if g := segs[0]; g["start"] != float64(t0.Add(90*time.Second).UnixMilli()) || g["end"] != float64(t0.Add(150*time.Second).UnixMilli()) ||
		g["writing"] != false || g["size"].(float64) <= 0 || !strings.HasSuffix(g["name"].(string), ".mp4") || g["url"] != nil {
		t.Fatalf("segment %v", g)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cameras/nope/recordings", viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown camera: %d %v", code, m)
	}

	// Without a recording server: 503 unavailable.
	s.AttachRecorder(nil)
	if code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/recordings", viewer, ""); code != 503 || errCode(m) != "unavailable" {
		t.Fatalf("no recorder: %d %v", code, m)
	}
}

func TestV2Rules(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin, op := v2Token(t, s, "admin", "Admin"), v2Token(t, s, "op", "Operator")
	rule := `{"name":"After-hours dock","enabled":true,"cameraIds":["CAM1"],"kinds":["Person"],"minSeverity":"Warning","schedule":true,"fromMinute":1320,"toMinute":360,"days":[1,2],"delivery":"push","instructions":"Call the site manager."}`

	if code, m := v2Call(t, h, "POST", "/api/v2/rules", op, rule); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("operator create: %d %v", code, m)
	}
	code, created := v2Call(t, h, "POST", "/api/v2/rules", admin, rule)
	if code != 201 || created["name"] != "After-hours dock" || len(created["cameraIds"].([]any)) != 1 || created["cameraIDs"] != nil ||
		!isMs(created["createdAt"]) || !isMs(created["updatedAt"]) {
		t.Fatalf("create: %d %v", code, created)
	}
	id := created["id"].(string)
	for _, bad := range []string{
		`{"name":"bad","schedule":true,"fromMinute":60,"toMinute":60}`,
		`{"name":"","enabled":true}`,
		`{"name":"x","cameras":[]}`,
		`{"name":"x","kinds":["Ghost"]}`,
	} {
		if code, m := v2Call(t, h, "POST", "/api/v2/rules", admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("%s: %d %v", bad, code, m)
		}
	}
	code, list := v2Call(t, h, "GET", "/api/v2/rules", op, "")
	if rs := items(t, list); code != 200 || len(rs) != 1 || rs[0]["id"] != id {
		t.Fatalf("list: %d %v", code, list)
	}
	disabled := strings.Replace(rule, `"enabled":true`, `"enabled":false`, 1)
	if code, m := v2Call(t, h, "PUT", "/api/v2/rules/"+id, admin, disabled); code != 200 || m["enabled"] != false || m["id"] != id || m["createdAt"] != created["createdAt"] {
		t.Fatalf("update: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "PUT", "/api/v2/rules/nope", admin, rule); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("update unknown: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/rules/"+id, op, ""); code != 403 {
		t.Fatalf("operator delete: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/rules/"+id, admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/rules/"+id, admin, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("second delete: %d %v", code, m)
	}
	if got := auditActions(t, s, "Alarm Rules"); !got["Created rule"] || !got["Disabled rule"] || !got["Deleted rule"] {
		t.Fatalf("audit %v", got)
	}
}

func TestV2Users(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	adminTok := v2SignIn(t, s, "admin", "Admin")
	admin := adminTok["accessToken"].(string)
	adminID := adminTok["user"].(map[string]any)["id"].(string)
	op := v2Token(t, s, "op", "Operator")

	if code, m := v2Call(t, h, "GET", "/api/v2/users", op, ""); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("operator list: %d %v", code, m)
	}
	code, u := v2Call(t, h, "POST", "/api/v2/users", admin, `{"name":"dana","role":"Viewer","password":"long enough pw"}`)
	if code != 201 || u["name"] != "dana" || u["role"] != "Viewer" || !isMs(u["createdAt"]) || u["passwordHash"] != nil {
		t.Fatalf("create: %d %v", code, u)
	}
	danaID := u["id"].(string)
	if code, m := v2Call(t, h, "POST", "/api/v2/users", admin, `{"name":"DANA","role":"Viewer","password":"long enough pw"}`); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("duplicate: %d %v", code, m)
	}
	for _, bad := range []string{
		`{"name":"eve","role":"Viewer","password":"short"}`,
		`{"name":"eve","role":"Boss","password":"long enough pw"}`,
		`{"name":" ","role":"Viewer","password":"long enough pw"}`,
	} {
		if code, m := v2Call(t, h, "POST", "/api/v2/users", admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("%s: %d %v", bad, code, m)
		}
	}
	code, list := v2Call(t, h, "GET", "/api/v2/users", admin, "")
	if us := items(t, list); code != 200 || len(us) != 3 {
		t.Fatalf("list: %d %v", code, list)
	}

	// Changing a role signs the user out everywhere (tokens and sessions).
	opUser, _ := s.Store.UserByName(ctx, "op")
	cookie, _ := s.Store.CreateSession(ctx, opUser.ID, time.Hour)
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+opUser.ID, admin, `{"role":"Supervisor"}`); code != 200 || m["role"] != "Supervisor" {
		t.Fatalf("role: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/me", op, ""); code != 401 {
		t.Fatalf("token still valid after role change: %d", code)
	}
	if _, err := s.Store.SessionUser(ctx, cookie); err == nil {
		t.Fatal("session still valid after role change")
	}

	// Changing a password too; the new one works.
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+danaID, admin, `{"password":"brand new password"}`); code != 200 || m["role"] != "Viewer" {
		t.Fatalf("password: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"dana","password":"brand new password"}`); code != 200 {
		t.Fatalf("sign in with new password: %d", code)
	}
	for _, bad := range []string{`{}`, `{"password":"short"}`, `{"role":"Boss"}`, `{"name":"x"}`} {
		if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+danaID, admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("%s: %d %v", bad, code, m)
		}
	}
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/nope", admin, `{"role":"Viewer"}`); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown: %d %v", code, m)
	}

	// The last Admin can't be demoted, and nobody can delete themselves.
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+adminID, admin, `{"role":"Operator"}`); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("demote last admin: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/users/"+adminID, admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("delete self: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PATCH", "/api/v2/users/"+adminID, admin, `{"role":"Admin"}`); code != 200 {
		t.Fatalf("unchanged role: %d", code)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/me", admin, ""); code != 200 {
		t.Fatalf("unchanged role signed the user out: %d", code)
	}
	admin = v2Token(t, s, "admin2", "Admin")
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+adminID, admin, `{"role":"Operator"}`); code != 200 || m["role"] != "Operator" {
		t.Fatalf("demote with another admin: %d %v", code, m)
	}

	if code, _ := v2Call(t, h, "DELETE", "/api/v2/users/"+danaID, admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/users/"+danaID, admin, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("second delete: %d %v", code, m)
	}
	if got := auditActions(t, s, "Users"); !got["Created user"] || !got["Changed user role"] || !got["Changed user password"] || !got["Deleted user"] {
		t.Fatalf("audit %v", got)
	}
}

func TestV2RecordingServers(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	viewer := v2Token(t, s, "viewer", "Viewer")
	addCam(t, s)
	addCam(t, s)
	code, m := v2Call(t, h, "GET", "/api/v2/recording-servers", viewer, "")
	list := items(t, m)
	if code != 200 || len(list) != 1 {
		t.Fatalf("servers: %d %v", code, m)
	}
	rs := list[0]
	if rs["name"] != "This server" || rs["online"] != true || rs["cameras"] != 2.0 || rs["version"] != "test" || rs["capabilities"] == nil {
		t.Fatalf("server %v", rs)
	}
	s.AttachRecorder(nil)
	if _, m := v2Call(t, h, "GET", "/api/v2/recording-servers", viewer, ""); items(t, m)[0]["online"] != false {
		t.Fatalf("detached server %v", m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/recording-servers", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

// TestV1ResponsesUnchanged: the shared helpers keep v1's statuses for the
// cases v2 answers differently.
func TestV1ResponsesUnchanged(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := signIn(t, s, "admin", "Admin")
	adminUser, _ := s.Store.UserByName(context.Background(), "admin")
	if rec := doWeb(t, h, admin, "DELETE", "/api/v1/users/"+adminUser.ID, ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "yourself") {
		t.Fatalf("v1 delete self: %d %s", rec.Code, rec.Body)
	}
	if rec := doWeb(t, h, admin, "POST", "/api/v1/users", `{"name":"admin","role":"Viewer","password":"long enough pw"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("v1 duplicate user: %d %s", rec.Code, rec.Body)
	}
	if rec := doWeb(t, h, admin, "POST", "/api/v1/cameras", `{"name":"x","rtspURL":"http://nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("v1 bad camera: %d %s", rec.Code, rec.Body)
	}
	if rec := doWeb(t, h, admin, "DELETE", "/api/v1/cameras/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("v1 delete unknown camera: %d", rec.Code)
	}
}
