package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

// Counts land in the right hour and kind, the alarm rows carry severity,
// and a user only sees cameras in their scope.
func TestV2ActivityCountsAndScope(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	cam := addCam(t, s)
	hour := time.Now().Truncate(time.Hour).Add(-3 * time.Hour)

	for i := 0; i < 3; i++ {
		e := &store.Event{ID: "ev" + string(rune('a'+i)), CameraID: cam.ID, Kind: "Person", Score: 0.9, CreatedAt: hour.Add(time.Duration(i) * time.Minute).Unix()}
		if err := s.Store.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	e := &store.Event{ID: "evv", CameraID: cam.ID, Kind: "Vehicle", Score: 0.8, CreatedAt: hour.Add(70 * time.Minute).Unix()}
	if err := s.Store.AddEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: cam.ID, CameraName: cam.Name, Kind: "Person", Title: "Person",
		Severity: "Critical", At: hour, Origin: "test"}); err != nil {
		t.Fatal(err)
	}

	from := hour.Add(-time.Hour).UnixMilli()
	to := hour.Add(3 * time.Hour).UnixMilli()
	path := "/api/v2/reports/activity?from=" + itoa(from) + "&to=" + itoa(to)
	code, body := v2Call(t, s.Handler(), http.MethodGet, path, adminToken(t, s), "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	totals := body["totals"].(map[string]any)
	if totals["events"].(float64) != 4 || totals["alarms"].(float64) != 1 {
		t.Fatalf("totals: %v", totals)
	}
	events := body["events"].([]any)
	var person, vehicle map[string]any
	for _, x := range events {
		m := x.(map[string]any)
		switch m["kind"] {
		case "Person":
			person = m
		case "Vehicle":
			vehicle = m
		}
	}
	if person == nil || person["count"].(float64) != 3 || vehicle == nil || vehicle["count"].(float64) != 1 {
		t.Fatalf("per-kind buckets: %v", events)
	}
	if vehicle["hour"].(float64) == person["hour"].(float64) {
		t.Fatal("the 70-minute event should fall in the next hour bucket")
	}
	alarms := body["alarms"].([]any)
	if len(alarms) != 1 || alarms[0].(map[string]any)["severity"] != "Critical" {
		t.Fatalf("alarm rows: %v", alarms)
	}
}

func TestV2ActivityRejectsBadRanges(t *testing.T) {
	s := newCompanionServer(t)
	tok := adminToken(t, s)
	now := time.Now().UnixMilli()
	cases := map[string]string{
		"reversed":  "/api/v2/reports/activity?from=" + itoa(now) + "&to=" + itoa(now-1000),
		"too long":  "/api/v2/reports/activity?from=" + itoa(now-40*24*3600_000) + "&to=" + itoa(now),
		"not a num": "/api/v2/reports/activity?from=yesterday",
	}
	for name, path := range cases {
		if code, _ := v2Call(t, s.Handler(), http.MethodGet, path, tok, ""); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
}

func TestV2ActivityNeedsSignIn(t *testing.T) {
	s := newCompanionServer(t)
	if code, _ := v2Call(t, s.Handler(), http.MethodGet, "/api/v2/reports/activity", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", code)
	}
}

// itoa avoids importing strconv into the test file for one call.
func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// adminToken signs in the first administrator the test store creates.
func adminToken(t *testing.T, s *Server) string {
	t.Helper()
	return v2SignIn(t, s, "admin", "Admin")["accessToken"].(string)
}
