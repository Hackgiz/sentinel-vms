package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Alarms count on their local day, not their UTC one, across a change of
// the clocks; from is inclusive and to exclusive.
func TestAlarmActivityByDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	s := open(t)
	ctx := context.Background()
	raise := func(camera, kind string, at time.Time) {
		t.Helper()
		a, _, err := s.RaiseAlarm(ctx, AlarmInput{CameraID: camera, Kind: kind, Title: kind, Severity: "Warning", At: at, Origin: "test"})
		if err != nil {
			t.Fatal(err)
		}
		// Resolved, so the next one opens a new alarm instead of folding in.
		if _, err := s.SetAlarmState(ctx, a.ID, "Resolved", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	nyAt := func(month time.Month, day, hour, min int) time.Time {
		return time.Date(2026, month, day, hour, min, 0, 0, ny)
	}
	from, to := nyAt(10, 31, 0, 0), nyAt(11, 2, 12, 0)
	raise("a", "Motion", from.Add(-time.Second))
	raise("a", "Motion", from)
	raise("a", "Motion", nyAt(10, 31, 23, 30))        // Nov 1 in UTC
	raise("a", "Person", nyAt(11, 1, 1, 30))          // the hour that happens twice
	raise("b", "Camera Offline", nyAt(11, 1, 23, 30)) // 04:30 UTC: Nov 1 at the winter offset, Nov 2 at the summer one
	raise("", "Storage", nyAt(11, 2, 9, 0))
	raise("a", "Motion", to)

	got, err := s.AlarmActivityByDay(ctx, from.Unix(), to.Unix(), ny)
	if err != nil {
		t.Fatal(err)
	}
	want := []AlarmDayCount{
		{"2026-10-31", "a", "Motion", 2},
		{"2026-11-01", "a", "Person", 1},
		{"2026-11-01", "b", "Camera Offline", 1},
		{"2026-11-02", "", "Storage", 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}

	local, err := s.AlarmActivityByDay(ctx, from.Unix(), to.Unix(), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if def, _ := s.AlarmActivityByDay(ctx, from.Unix(), to.Unix(), nil); !reflect.DeepEqual(def, local) {
		t.Fatalf("nil location: %+v, want time.Local's %+v", def, local)
	}
}
