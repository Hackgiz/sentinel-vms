package server

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

// Seven whole days: every day listed and zero-filled, the cameras with their
// alert setting, the mix, and the recommendations the data calls for. A
// disabled rule doesn't cover people; once enabled, it does.
func TestV2InsightsReport(t *testing.T) {
	s := newCompanionServer(t)
	s.rules.loc = time.UTC
	ctx := context.Background()
	to := time.Now().UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -7)
	at := func(day, hour int) time.Time { return from.AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour) }
	cam := func(name string, alertOn int) *store.Camera {
		t.Helper()
		c, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: name, RTSPURL: "rtsp://10.0.0.9/" + name, Recording: true, AlertOn: &alertOn})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	dock, gate, lobby := cam("Dock", alertAuto), cam("Gate", alertPeople), cam("Lobby", alertMotion)
	// Lobby was there all week and saw nothing; Dock and Gate are new.
	if _, err := s.Store.DB.ExecContext(ctx, `UPDATE cameras SET created_at = ? WHERE id = ?`, from.Add(-time.Hour).Unix(), lobby.ID); err != nil {
		t.Fatal(err)
	}
	raise := func(c *store.Camera, kind string, when time.Time) {
		t.Helper()
		in := store.AlarmInput{Kind: kind, Title: kind, Severity: "Warning", At: when, Origin: "test"}
		if c != nil {
			in.CameraID, in.CameraName = c.ID, c.Name
		}
		a, _, err := s.Store.RaiseAlarm(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		// Resolved, so the next one opens a new alarm instead of folding in.
		if _, err := s.Store.SetAlarmState(ctx, a.ID, "Resolved", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		raise(dock, "Motion", at(i%2, 8).Add(time.Duration(i)*time.Minute))
	}
	raise(dock, "Person", at(1, 12))
	for _, d := range []int{0, 2, 4} {
		raise(gate, "Camera Offline", at(d, 3))
	}
	for i := 0; i < 5; i++ {
		raise(gate, "Person", at(5, 20).Add(time.Duration(i)*time.Minute))
	}
	raise(nil, "Storage", at(3, 9))
	raise(dock, "Motion", from.Add(-time.Second)) // before the range
	raise(dock, "Motion", to)                     // to is exclusive
	for _, e := range []store.Event{
		{CameraID: dock.ID, Kind: "Motion", CreatedAt: at(0, 8).Unix()},
		{CameraID: dock.ID, Kind: "Motion", CreatedAt: at(0, 9).Unix()},
		{CameraID: gate.ID, Kind: "Person", CreatedAt: at(5, 20).Unix()},
	} {
		if err := s.Store.AddEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	rule := &rules.Rule{Name: "People", Kinds: []string{"Person"}, MinSeverity: "Warning", Delivery: rules.DeliveryPush}
	if err := s.Store.SaveAlarmRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	tok := adminToken(t, s)
	path := fmt.Sprintf("/api/v2/insights?from=%d&to=%d", from.UnixMilli(), to.UnixMilli())
	code, body := v2Call(t, s.Handler(), http.MethodGet, path, tok, "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["from"].(float64) != float64(from.UnixMilli()) || body["to"].(float64) != float64(to.UnixMilli()) {
		t.Fatalf("range %v–%v", body["from"], body["to"])
	}
	want := map[string]any{"events": 3.0, "alarms": 30.0, "camerasWithAlarms": 2.0, "offlineCameras": 1.0}
	if !reflect.DeepEqual(body["totals"], want) {
		t.Fatalf("totals %v", body["totals"])
	}
	var days []string
	for _, x := range body["daily"].([]any) {
		d := x.(map[string]any)
		days = append(days, fmt.Sprintf("%s e%v a%v %v", d["day"], d["events"], d["alarms"], d["byKind"]))
	}
	wantDays := []string{
		at(0, 0).Format(time.DateOnly) + " e2 a11 map[Camera Offline:1 Motion:10]",
		at(1, 0).Format(time.DateOnly) + " e0 a11 map[Motion:10 Person:1]",
		at(2, 0).Format(time.DateOnly) + " e0 a1 map[Camera Offline:1]",
		at(3, 0).Format(time.DateOnly) + " e0 a1 map[Storage:1]",
		at(4, 0).Format(time.DateOnly) + " e0 a1 map[Camera Offline:1]",
		at(5, 0).Format(time.DateOnly) + " e1 a5 map[Person:5]",
		at(6, 0).Format(time.DateOnly) + " e0 a0 map[]",
	}
	if !reflect.DeepEqual(days, wantDays) {
		t.Fatalf("daily\n got %q\nwant %q", days, wantDays)
	}
	var cams []string
	for _, x := range body["cameras"].([]any) {
		c := x.(map[string]any)
		cams = append(cams, fmt.Sprintf("%s %v %v %v", c["name"], c["alarms"], c["topKind"], c["alertOn"]))
	}
	if got := strings.Join(cams, ", "); got != "Dock 21 Motion auto, Gate 8 Person people, Lobby 0 <nil> motion" {
		t.Fatalf("cameras %s", got)
	}
	var mix []string
	for _, x := range body["mix"].([]any) {
		m := x.(map[string]any)
		mix = append(mix, fmt.Sprintf("%s %v %.3f", m["kind"], m["alarms"], m["share"]))
	}
	if got := strings.Join(mix, ", "); got != "Motion 20 0.667, Person 6 0.200, Camera Offline 3 0.100, Storage 1 0.033" {
		t.Fatalf("mix %s", got)
	}

	recs := func(body map[string]any) (ids string, byID map[string]map[string]any) {
		byID = map[string]map[string]any{}
		var list []string
		for _, x := range body["recommendations"].([]any) {
			r := x.(map[string]any)
			list = append(list, r["id"].(string))
			byID[r["id"].(string)] = r
		}
		return strings.Join(list, ","), byID
	}
	ids, byID := recs(body)
	wantIDs := strings.Join([]string{"repeat-offline:" + gate.ID, "storage-alarms", "motion-only:" + dock.ID, "no-people-rule", "quiet-camera:" + lobby.ID}, ",")
	if ids != wantIDs {
		t.Fatalf("recommendations\n got %s\nwant %s", ids, wantIDs)
	}
	wantAction := map[string]any{"type": "setAlertOn", "cameraId": dock.ID, "alertOn": "peopleAndVehicles"}
	if r := byID["motion-only:"+dock.ID]; r["level"] != "suggestion" || r["cameraId"] != dock.ID || !reflect.DeepEqual(r["action"], wantAction) ||
		!strings.HasPrefix(r["detail"].(string), "20 of 21 alarms were plain motion.") {
		t.Fatalf("motion-only %v", r)
	}

	rule.Enabled = true
	if err := s.Store.SaveAlarmRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	_, body = v2Call(t, s.Handler(), http.MethodGet, path, tok, "")
	if _, byID := recs(body); byID["no-people-rule"] != nil {
		t.Fatalf("an enabled rule covers people: %v", body["recommendations"])
	}
}

// A scoped user's insights count only their cameras, and leave out alarms
// without a camera (Storage), as the activity report does.
func TestV2InsightsScope(t *testing.T) {
	f := newScopeFixture(t)
	around := func(t0 time.Time) string {
		return fmt.Sprintf("/api/v2/insights?from=%d&to=%d", t0.Add(-time.Hour).UnixMilli(), t0.Add(time.Hour).UnixMilli())
	}
	totals := func(tok, path string) (map[string]any, string) {
		t.Helper()
		code, body := v2Call(t, f.h, http.MethodGet, path, tok, "")
		if code != http.StatusOK {
			t.Fatalf("status %d: %v", code, body)
		}
		return body["totals"].(map[string]any), fmt.Sprint(body)
	}

	// The fixture's Person alarms and events, one per camera.
	tot, raw := totals(f.op, around(f.t0))
	if tot["alarms"] != 1.0 || tot["events"] != 1.0 || !strings.Contains(raw, f.front.ID) || strings.Contains(raw, f.back.ID) {
		t.Fatalf("scoped: %s", raw)
	}
	for _, tok := range []string{f.allOp, f.admin} {
		if tot, raw := totals(tok, around(f.t0)); tot["alarms"] != 2.0 || !strings.Contains(raw, f.back.ID) {
			t.Fatalf("unscoped: %s", raw)
		}
	}
	// The fixture's Storage alarm, raised just now.
	tot, raw = totals(f.op, around(time.Now()))
	if tot["alarms"] != 0.0 || strings.Contains(raw, "storage-alarms") {
		t.Fatalf("scoped user sees the Storage alarm: %s", raw)
	}
	if tot, raw := totals(f.admin, around(time.Now())); tot["alarms"] != 1.0 || !strings.Contains(raw, "storage-alarms") {
		t.Fatalf("admin misses the Storage alarm: %s", raw)
	}
}

func TestV2InsightsRange(t *testing.T) {
	s := newCompanionServer(t)
	h, tok := s.Handler(), adminToken(t, s)
	code, body := v2Call(t, h, http.MethodGet, "/api/v2/insights", tok, "")
	if code != http.StatusOK || body["to"].(float64)-body["from"].(float64) != float64((7*24*time.Hour).Milliseconds()) {
		t.Fatalf("default range: %d %v–%v", code, body["from"], body["to"])
	}
	now, day := time.Now().UnixMilli(), (24 * time.Hour).Milliseconds()
	if code, body := v2Call(t, h, http.MethodGet, fmt.Sprintf("/api/v2/insights?from=%d&to=%d", now-31*day, now), tok, ""); code != http.StatusOK {
		t.Fatalf("31 days: %d %v", code, body)
	}
	bad := map[string]string{
		"31 days and a millisecond": fmt.Sprintf("from=%d&to=%d", now-31*day-1, now),
		"to equals from":            fmt.Sprintf("from=%d&to=%d", now, now),
		"reversed":                  fmt.Sprintf("from=%d&to=%d", now, now-1000),
		"not a number":              "to=today",
		"centuries":                 "from=1&to=18446744073710", // overflows a Duration
	}
	for name, q := range bad {
		if code, body := v2Call(t, h, http.MethodGet, "/api/v2/insights?"+q, tok, ""); code != http.StatusBadRequest || errCode(body) != codeBadRequest {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	if code, _ := v2Call(t, h, http.MethodGet, "/api/v2/insights", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
}
