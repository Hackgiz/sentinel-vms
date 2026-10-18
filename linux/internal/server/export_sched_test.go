package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

var utcLoc = time.UTC

// at: 2026-10-09 is a Friday (the rules tests use the same week).
func at(day, hour, min int) time.Time { return time.Date(2026, 10, day, hour, min, 0, 0, utcLoc) }

func sched(days []int, start, end string) *store.ExportSchedule {
	return &store.ExportSchedule{ID: "s", Name: "n", CameraID: "c", Days: days, StartHM: start, EndHM: end, Dest: "evidence", Enabled: true}
}

func TestScheduleWindowsDaytime(t *testing.T) {
	e := sched([]int{5}, "09:00", "17:00") // Fridays
	ws := scheduleWindows(e, at(9, 16, 59), at(9, 17, 0), utcLoc)
	if len(ws) != 1 || !ws[0].From.Equal(at(9, 9, 0)) || !ws[0].To.Equal(at(9, 17, 0)) {
		t.Fatalf("windows %+v", ws)
	}
	// The window fires once: not before its end, not after the range passes it.
	if ws := scheduleWindows(e, at(9, 16, 0), at(9, 16, 59), utcLoc); len(ws) != 0 {
		t.Fatalf("fired before the end: %+v", ws)
	}
	if ws := scheduleWindows(e, at(9, 17, 0), at(9, 17, 30), utcLoc); len(ws) != 0 {
		t.Fatalf("fired again after the end: %+v", ws)
	}
	// Not a scheduled day.
	if ws := scheduleWindows(e, at(10, 16, 59), at(10, 17, 0), utcLoc); len(ws) != 0 {
		t.Fatalf("Saturday fired: %+v", ws)
	}
	// 24:00 ends at the next midnight.
	e = sched([]int{5}, "12:00", "24:00")
	ws = scheduleWindows(e, at(9, 23, 59), at(10, 0, 0), utcLoc)
	if len(ws) != 1 || !ws[0].To.Equal(at(10, 0, 0)) {
		t.Fatalf("24:00 window %+v", ws)
	}
}

func TestScheduleWindowsOvernightBelongToStartDay(t *testing.T) {
	// Fridays only, 22:00–06:00: the window ends Saturday 06:00.
	e := sched([]int{5}, "22:00", "06:00")
	ws := scheduleWindows(e, at(10, 5, 59), at(10, 6, 0), utcLoc)
	if len(ws) != 1 || !ws[0].From.Equal(at(9, 22, 0)) || !ws[0].To.Equal(at(10, 6, 0)) {
		t.Fatalf("Friday night %+v", ws)
	}
	// Nothing fires at Friday 06:00 (that is Thursday night's end) or Sunday 06:00.
	if ws := scheduleWindows(e, at(9, 5, 59), at(9, 6, 0), utcLoc); len(ws) != 0 {
		t.Fatalf("Friday 06:00 fired: %+v", ws)
	}
	if ws := scheduleWindows(e, at(11, 5, 59), at(11, 6, 0), utcLoc); len(ws) != 0 {
		t.Fatalf("Sunday 06:00 fired: %+v", ws)
	}
	// A range spanning the end boundary (a late tick) still finds it.
	if ws := scheduleWindows(e, at(10, 5, 0), at(10, 7, 0), utcLoc); len(ws) != 1 {
		t.Fatalf("late tick %+v", ws)
	}
	// Every day: 8-hour windows, one end per day.
	e = sched([]int{0, 1, 2, 3, 4, 5, 6}, "22:00", "06:00")
	if ws := scheduleWindows(e, at(9, 0, 0), at(11, 7, 0), utcLoc); len(ws) != 2 {
		t.Fatalf("two nights: %+v", ws)
	}
}

func TestScheduleWindowsDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	// 2026-11-01 02:00 → 01:00: the Saturday-night window 22:00–06:00 is 9 real hours.
	e := sched([]int{6}, "22:00", "06:00")
	end := time.Date(2026, 11, 1, 6, 0, 0, 0, ny)
	ws := scheduleWindows(e, end.Add(-time.Minute), end, ny)
	if len(ws) != 1 || !ws[0].From.Equal(time.Date(2026, 10, 31, 22, 0, 0, 0, ny)) || !ws[0].To.Equal(end) {
		t.Fatalf("DST window %+v", ws)
	}
	if d := ws[0].To.Sub(ws[0].From); d != 9*time.Hour {
		t.Fatalf("window is %v", d)
	}
}

// schedFixture is the playback fixture (footage on Friday 2026-10-09
// 09:00:00–09:01:00 and 09:01:30–09:02:30, local time) with an operator.
func schedFixture(t *testing.T) (*Server, *store.Camera, time.Time) {
	s, c, t0 := playbackFixture(t)
	s.exportJobs.pollEvery = 10 * time.Millisecond
	return s, c, t0
}

func addSchedule(t *testing.T, s *Server, camID, name, start, end string) *store.ExportSchedule {
	t.Helper()
	e := &store.ExportSchedule{Name: name, CameraID: camID, Days: []int{5}, StartHM: start, EndHM: end, Enabled: true, CreatedBy: "test"}
	if err := s.Store.SaveExportSchedule(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestFireDueSchedulesEnqueuesVaultExportOnce(t *testing.T) {
	s, c, t0 := schedFixture(t)
	ctx := context.Background()
	e := addSchedule(t, s, c.ID, "Morning", "09:00", "09:03")

	// Before the window ends: nothing.
	if f := s.fireDueSchedules(ctx, t0.Add(time.Minute), t0.Add(2*time.Minute+59*time.Second)); len(f) != 0 {
		t.Fatalf("fired early: %v", f)
	}
	// The tick that crosses the end fires it.
	f := s.fireDueSchedules(ctx, t0.Add(2*time.Minute+50*time.Second), t0.Add(3*time.Minute+10*time.Second))
	if len(f) != 1 || f[0] != e.ID {
		t.Fatalf("fired %v", f)
	}
	waitJobs(t, s)
	// A late retry over the same window does not fire it again.
	if f := s.fireDueSchedules(ctx, t0.Add(2*time.Minute), t0.Add(10*time.Minute)); len(f) != 0 {
		t.Fatalf("fired twice: %v", f)
	}
	// Wait for the status goroutine.
	var got *store.ExportSchedule
	for i := 0; i < 200; i++ {
		got, _ = s.Store.ExportSchedule(ctx, e.ID)
		if got.LastStatus == "ok" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.LastStatus != "ok" || got.LastRunAt != t0.Add(3*time.Minute).Unix() {
		t.Fatalf("schedule state: %+v", got)
	}
	cases, _ := s.Store.EvidenceCases(ctx, store.EvidenceCaseFilter{})
	if len(cases) != 1 || cases[0].CameraID != c.ID || cases[0].LockedBy != "Scheduler" || !strings.HasPrefix(cases[0].Title, "Morning") ||
		cases[0].FromMs != t0.UnixMilli() || cases[0].ToMs != t0.Add(3*time.Minute).UnixMilli() {
		t.Fatalf("evidence %+v", cases)
	}
}

func TestFireDueSchedulesSkipsDisabledAndReportsEmptyWindows(t *testing.T) {
	s, c, t0 := schedFixture(t)
	ctx := context.Background()
	off := addSchedule(t, s, c.ID, "Off", "09:00", "09:03")
	off.Enabled = false
	if err := s.Store.SaveExportSchedule(ctx, off); err != nil {
		t.Fatal(err)
	}
	// A window with no footage (08:00–08:30): fires, then reports why it failed.
	empty := addSchedule(t, s, c.ID, "Empty", "08:00", "08:30")
	f := s.fireDueSchedules(ctx, t0.Add(-31*time.Minute), t0.Add(-29*time.Minute))
	if len(f) != 1 || f[0] != empty.ID {
		t.Fatalf("fired %v", f)
	}
	got, _ := s.Store.ExportSchedule(ctx, empty.ID)
	if !strings.HasPrefix(got.LastStatus, "failed:") || !strings.Contains(got.LastStatus, "nothing was recorded") {
		t.Fatalf("status %q", got.LastStatus)
	}
	if got, _ = s.Store.ExportSchedule(ctx, off.ID); got.LastRunAt != 0 {
		t.Fatal("disabled schedule ran")
	}
}

func TestSchedulerDoesNotBackfillWhileDown(t *testing.T) {
	s, c, t0 := schedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	missed := addSchedule(t, s, c.ID, "Missed", "09:00", "09:03") // ended before the server came up
	later := addSchedule(t, s, c.ID, "Later", "09:00", "09:20")   // ends while it runs

	var mu sync.Mutex
	clock := t0.Add(10 * time.Minute) // server starts at 09:10
	s.exportJobs.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	s.exportJobs.schedEvery = 5 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); s.RunExportSchedules(ctx) }()

	time.Sleep(100 * time.Millisecond) // many ticks at 09:10: nothing is due
	if got, _ := s.Store.ExportSchedule(ctx, missed.ID); got.LastRunAt != 0 {
		t.Fatalf("backfilled a window that ended while down: %+v", got)
	}
	mu.Lock()
	clock = t0.Add(21 * time.Minute)
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, _ := s.Store.ExportSchedule(ctx, later.ID); got.LastRunAt != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("window that ended while running did not fire")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	waitJobs(t, s)
	if got, _ := s.Store.ExportSchedule(context.Background(), missed.ID); got.LastRunAt != 0 {
		t.Fatalf("missed window fired later: %+v", got)
	}
}

func TestV2ExportSchedulesCRUD(t *testing.T) {
	s, c, _ := schedFixture(t)
	h := s.Handler()
	ctx := context.Background()
	op := v2Token(t, s, "op", "Operator")
	viewer := v2Token(t, s, "viewer", "Viewer")
	scoped := v2Token(t, s, "scoped", "Operator")
	other, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Other", RTSPURL: "rtsp://10.0.0.7/s"})
	g, _ := s.Store.SaveCameraGroup(ctx, "", "Other only", []string{other.ID})
	u, _ := s.Store.UserByName(ctx, "scoped")
	if err := s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"Nightly","cameraId":%q,"days":[5,1,1],"start":"22:00","end":"06:00"}`, c.ID)

	for _, call := range [][2]string{{"GET", "/api/v2/export-schedules"}, {"POST", "/api/v2/export-schedules"}, {"PATCH", "/api/v2/export-schedules/x"}, {"DELETE", "/api/v2/export-schedules/x"}} {
		if r := do(t, h, call[0], call[1], viewer, body); r.Code != 403 {
			t.Errorf("viewer %v: %d", call, r.Code)
		}
	}
	if r := do(t, h, "POST", "/api/v2/export-schedules", scoped, body); r.Code != 404 {
		t.Errorf("out-of-scope camera: %d", r.Code)
	}
	code, sc := v2Call(t, h, "POST", "/api/v2/export-schedules", op, body)
	if code != 201 || sc["name"] != "Nightly" || sc["enabled"] != true || sc["dest"] != "evidence" || sc["createdBy"] != "op" ||
		fmt.Sprint(sc["days"]) != "[1 5]" || sc["lastRunAt"] != float64(0) {
		t.Fatalf("create: %d %v", code, sc)
	}
	id := sc["id"].(string)

	// Validation.
	bad := []string{
		fmt.Sprintf(`{"name":"","cameraId":%q,"days":[1],"start":"22:00","end":"06:00"}`, c.ID),
		fmt.Sprintf(`{"name":"x","cameraId":%q,"days":[],"start":"22:00","end":"06:00"}`, c.ID),
		fmt.Sprintf(`{"name":"x","cameraId":%q,"days":[9],"start":"22:00","end":"06:00"}`, c.ID),
		fmt.Sprintf(`{"name":"x","cameraId":%q,"days":[1],"start":"22:00","end":"22:00"}`, c.ID),
		fmt.Sprintf(`{"name":"x","cameraId":%q,"days":[1],"start":"06:00","end":"19:00"}`, c.ID),
		fmt.Sprintf(`{"name":"x","cameraId":%q,"days":[1],"start":"22:00","end":"06:00","dest":"download"}`, c.ID),
		`{"name":"x","cameraId":"nope","days":[1],"start":"22:00","end":"06:00"}`,
	}
	for i, b := range bad {
		if r := do(t, h, "POST", "/api/v2/export-schedules", op, b); r.Code != 400 && r.Code != 404 {
			t.Errorf("bad body %d accepted: %d %s", i, r.Code, r.Body)
		}
	}
	if r := do(t, h, "PATCH", "/api/v2/export-schedules/"+id, op, `{"end":"22:00"}`); r.Code != 400 {
		t.Errorf("patch to empty window: %d", r.Code)
	}

	// Partial update, list, scope.
	code, sc = v2Call(t, h, "PATCH", "/api/v2/export-schedules/"+id, op, `{"enabled":false,"end":"05:00"}`)
	if code != 200 || sc["enabled"] != false || sc["end"] != "05:00" || sc["start"] != "22:00" || sc["name"] != "Nightly" {
		t.Fatalf("patch: %d %v", code, sc)
	}
	_, list := v2Call(t, h, "GET", "/api/v2/export-schedules", op, "")
	if rows := items(t, list); len(rows) != 1 {
		t.Fatalf("list: %v", list)
	}
	_, list = v2Call(t, h, "GET", "/api/v2/export-schedules", scoped, "")
	if rows := items(t, list); len(rows) != 0 {
		t.Fatalf("scoped list: %v", list)
	}
	if r := do(t, h, "PATCH", "/api/v2/export-schedules/"+id, scoped, `{"enabled":true}`); r.Code != 404 {
		t.Errorf("scoped patch: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/export-schedules/"+id, scoped, ""); r.Code != 404 {
		t.Errorf("scoped delete: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/export-schedules/"+id, op, ""); r.Code != 204 {
		t.Errorf("delete: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/export-schedules/"+id, op, ""); r.Code != 404 {
		t.Errorf("second delete: %d", r.Code)
	}
	logs, _ := s.Store.AuditLog(ctx, 50)
	var added, edited, deleted bool
	for _, e := range logs {
		added = added || e.Action == "Added export schedule"
		edited = edited || e.Action == "Edited export schedule"
		deleted = deleted || e.Action == "Deleted export schedule"
	}
	if !added || !edited || !deleted {
		t.Errorf("audit: added %v edited %v deleted %v", added, edited, deleted)
	}
}
