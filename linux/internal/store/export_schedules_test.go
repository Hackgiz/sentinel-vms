package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExportScheduleCRUD(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	cam, err := s.SaveCamera(ctx, "", CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.1/s"})
	if err != nil {
		t.Fatal(err)
	}
	e := &ExportSchedule{Name: "  Night  ", CameraID: cam.ID, Days: []int{5, 1, 5, 0}, StartHM: "22:00", EndHM: "06:00", Enabled: true, CreatedBy: "ej"}
	if err := s.SaveExportSchedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.Name != "Night" || e.Dest != ExportDestEvidence || !reflect.DeepEqual(e.Days, []int{0, 1, 5}) || e.CreatedAt == 0 {
		t.Fatalf("created %+v", e)
	}
	got, err := s.ExportSchedule(ctx, e.ID)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("read back %+v %v (want %+v)", got, err, e)
	}

	e.Name, e.Enabled, e.EndHM, e.CreatedBy = "Nightly", false, "05:30", "someone else"
	if err := s.SaveExportSchedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ExportSchedule(ctx, e.ID)
	if got.Name != "Nightly" || got.Enabled || got.EndHM != "05:30" || got.CreatedBy != "ej" {
		t.Fatalf("updated %+v", got)
	}

	if err := s.RecordExportScheduleRun(ctx, e.ID, 1234, "ok"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ExportSchedule(ctx, e.ID)
	if got.LastRunAt != 1234 || got.LastStatus != "ok" {
		t.Fatalf("last run %+v", got)
	}
	// Editing the schedule keeps its run history.
	e.Name = "Renamed"
	_ = s.SaveExportSchedule(ctx, e)
	if got, _ = s.ExportSchedule(ctx, e.ID); got.LastRunAt != 1234 {
		t.Fatal("update reset last_run_at")
	}

	list, _ := s.ExportSchedules(ctx)
	if len(list) != 1 || list[0].ID != e.ID {
		t.Fatalf("list %+v", list)
	}
	if err := s.SaveExportSchedule(ctx, &ExportSchedule{ID: "nope", Name: "x", CameraID: cam.ID, Days: []int{1}, StartHM: "01:00", EndHM: "02:00"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := s.SaveExportSchedule(ctx, &ExportSchedule{Name: "x", CameraID: "no-such-camera", Days: []int{1}, StartHM: "01:00", EndHM: "02:00"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing camera: %v", err)
	}
	if err := s.DeleteExportSchedule(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExportSchedule(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// Deleting the camera takes its schedules along.
	e2 := &ExportSchedule{Name: "x", CameraID: cam.ID, Days: []int{1}, StartHM: "01:00", EndHM: "02:00"}
	if err := s.SaveExportSchedule(ctx, e2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCamera(ctx, cam.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ExportSchedules(ctx); len(list) != 0 {
		t.Fatalf("schedule survived its camera: %+v", list)
	}
}

func TestExportScheduleValidation(t *testing.T) {
	ok := ExportSchedule{Name: "n", CameraID: "c", Days: []int{1}, StartHM: "22:00", EndHM: "06:00"}
	cases := map[string]func(e *ExportSchedule){
		"no name":        func(e *ExportSchedule) { e.Name = "  " },
		"long name":      func(e *ExportSchedule) { e.Name = strings.Repeat("x", 101) },
		"no camera":      func(e *ExportSchedule) { e.CameraID = "" },
		"no days":        func(e *ExportSchedule) { e.Days = nil },
		"bad day":        func(e *ExportSchedule) { e.Days = []int{7} },
		"bad start":      func(e *ExportSchedule) { e.StartHM = "25:00" },
		"start 24:00":    func(e *ExportSchedule) { e.StartHM = "24:00" },
		"bad end":        func(e *ExportSchedule) { e.EndHM = "6am" },
		"empty window":   func(e *ExportSchedule) { e.EndHM = e.StartHM },
		"over 12 h":      func(e *ExportSchedule) { e.StartHM, e.EndHM = "08:00", "20:01" },
		"over 12 h wrap": func(e *ExportSchedule) { e.StartHM, e.EndHM = "20:00", "08:01" },
		"download dest":  func(e *ExportSchedule) { e.Dest = "download" },
	}
	for name, mutate := range cases {
		e := ok
		mutate(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
	}
	for _, w := range [][2]string{{"22:00", "06:00"}, {"08:00", "20:00"}, {"12:00", "24:00"}, {"00:00", "12:00"}, {"20:00", "08:00"}} {
		e := ok
		e.StartHM, e.EndHM = w[0], w[1]
		if err := e.Validate(); err != nil {
			t.Errorf("%v rejected: %v", w, err)
		}
	}
	if _, length, _ := (&ExportSchedule{StartHM: "22:00", EndHM: "06:00"}).WindowMinutes(); length != 8*60 {
		t.Errorf("overnight length %d", length)
	}
}
