package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlatesWatchlistAndSightings(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	van := &Plate{Plate: "ABC123", Name: "Delivery van"}
	if err := st.AddPlate(ctx, van); err != nil || van.ID == "" || van.Alert != PlateAlertDefault || van.CreatedAt == 0 {
		t.Fatalf("add: %v %+v", err, van)
	}
	if err := st.AddPlate(ctx, &Plate{Plate: "ABC123"}); !errors.Is(err, ErrDuplicatePlate) {
		t.Fatalf("duplicate: %v", err)
	}
	other := &Plate{Plate: "XYZ9", Alert: PlateAlertAlways}
	_ = st.AddPlate(ctx, other)
	list, _ := st.Plates(ctx)
	if len(list) != 2 || list[0].Plate != "ABC123" || list[1].Alert != PlateAlertAlways {
		t.Fatalf("list %+v", list)
	}
	if n, _ := st.CountPlates(ctx); n != 2 {
		t.Fatalf("count %d", n)
	}
	other.Plate, other.Name, other.Alert = "ABC123", "clash", PlateAlertNever
	if err := st.UpdatePlate(ctx, other); !errors.Is(err, ErrDuplicatePlate) {
		t.Fatalf("rename onto another: %v", err)
	}
	other.Plate = "XYZ99"
	if err := st.UpdatePlate(ctx, other); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.PlateByID(ctx, other.ID); p.Plate != "XYZ99" || p.Alert != PlateAlertNever || p.Name != "clash" {
		t.Fatalf("updated %+v", p)
	}
	if err := st.UpdatePlate(ctx, &Plate{ID: "nope", Plate: "Q1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if !ValidPlateAlert("never") || ValidPlateAlert("sometimes") || ValidPlateAlert("") {
		t.Fatal("ValidPlateAlert")
	}

	// Sightings: events with the plate, newest first, per camera.
	a, _ := st.SaveCamera(ctx, "", CameraInput{Name: "A", RTSPURL: "rtsp://10.0.0.1/s"})
	b, _ := st.SaveCamera(ctx, "", CameraInput{Name: "B", RTSPURL: "rtsp://10.0.0.2/s"})
	for i, e := range []Event{
		{ID: "v1", CameraID: a.ID, Kind: "Vehicle", CreatedAt: 1000},
		{ID: "v2", CameraID: b.ID, Kind: "Vehicle", CreatedAt: 2000},
		{ID: "v3", CameraID: a.ID, Kind: "Vehicle", CreatedAt: 3000},
		{ID: "v4", CameraID: a.ID, Kind: "Vehicle", CreatedAt: 4000, Plate: "ABC124"},
	} {
		if err := st.AddEvent(ctx, &e); err != nil {
			t.Fatal(i, err)
		}
	}
	for _, id := range []string{"v1", "v2"} {
		if err := st.SetEventPlate(ctx, id, "ABC123"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetEventPlate(ctx, "missing", "ABC123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing event: %v", err)
	}
	if e, _ := st.Event(ctx, "v1"); e.Plate != "ABC123" {
		t.Fatalf("event plate %q", e.Plate)
	}
	ev, err := st.PlateSightings(ctx, "ABC123", nil, 20)
	if err != nil || len(ev) != 2 || ev[0].ID != "v2" || ev[1].ID != "v1" {
		t.Fatalf("sightings %v %v", ev, err)
	}
	if ev, _ := st.PlateSightings(ctx, "ABC123", []string{a.ID}, 20); len(ev) != 1 || ev[0].ID != "v1" {
		t.Fatalf("scoped sightings %v", ev)
	}
	if last, err := st.PlateLastSeen(ctx, "ABC123", nil); err != nil || last.ID != "v2" || last.CameraID != b.ID {
		t.Fatalf("last seen %v %v", last, err)
	}
	if _, err := st.PlateLastSeen(ctx, "NEVER1", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("never seen: %v", err)
	}
	if ev, _ := st.PlateSightings(ctx, "", nil, 5); len(ev) != 0 {
		t.Fatal("an empty plate matches nothing")
	}
	if n, _ := st.CountPlateReads(ctx, 1500); n != 2 { // v2, v4
		t.Fatalf("reads since 1500: %d", n)
	}

	// Search: plate prefix, person and track exact.
	_, _ = st.DB.ExecContext(ctx, `UPDATE events SET person_id='p1', track_id='cam:7' WHERE id='v3'`)
	ids := func(f EventFilter) string {
		t.Helper()
		out, _, err := st.SearchEvents(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range out {
			s = append(s, e.ID)
		}
		return strings.Join(s, ",")
	}
	if got := ids(EventFilter{Plate: "ABC12"}); got != "v4,v2,v1" {
		t.Fatalf("plate prefix: %s", got)
	}
	if got := ids(EventFilter{Plate: "ABC123"}); got != "v2,v1" {
		t.Fatalf("plate exact: %s", got)
	}
	if got := ids(EventFilter{Plate: "A%"}); got != "" {
		t.Fatalf("LIKE wildcards must be literal: %s", got)
	}
	if got := ids(EventFilter{PersonID: "p1"}); got != "v3" {
		t.Fatalf("person: %s", got)
	}
	if got := ids(EventFilter{TrackID: "cam:7", CameraIDs: []string{a.ID}}); got != "v3" {
		t.Fatalf("track: %s", got)
	}
	if got := ids(EventFilter{TrackID: "cam:"}); got != "" {
		t.Fatalf("track is exact: %s", got)
	}

	if err := st.DeletePlate(ctx, van.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePlate(ctx, van.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if e, _ := st.Event(ctx, "v1"); e.Plate != "ABC123" {
		t.Fatal("deleting a watchlist entry must keep the reads")
	}
}

// An existing database without the plates table or events.plate gets both.
func TestPlatesMigrateOldDatabase(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _ = st.DB.ExecContext(ctx, `DROP TABLE plates`)
	_, _ = st.DB.ExecContext(ctx, `DROP INDEX IF EXISTS events_plate`)
	if _, err := st.DB.ExecContext(ctx, `ALTER TABLE events DROP COLUMN plate`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AddPlate(ctx, &Plate{Plate: "OLD1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PlateSightings(ctx, "OLD1", nil, 1); err != nil {
		t.Fatal(err)
	}
}
