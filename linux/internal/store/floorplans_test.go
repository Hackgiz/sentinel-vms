package store

import (
	"context"
	"errors"
	"testing"
)

func TestFloorplanRoundTrip(t *testing.T) {
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
	f, err := s.SaveFloorplan(ctx, Floorplan{Name: " Ground ", Image: []byte("png"), Mime: "image/png", Width: 800, Height: 600})
	if err != nil || f.Name != "Ground" || f.Width != 800 || f.Image != nil {
		t.Fatalf("create: %+v %v", f, err)
	}
	img, err := s.FloorplanImage(ctx, f.ID)
	if err != nil || string(img.Image) != "png" || img.Mime != "image/png" {
		t.Fatalf("image: %+v %v", img, err)
	}
	f, err = s.SaveFloorplan(ctx, Floorplan{ID: f.ID, Name: "Level 1", SortOrder: 2})
	if err != nil || f.Name != "Level 1" || f.SortOrder != 2 || f.Width != 800 {
		t.Fatalf("update: %+v %v", f, err)
	}
	if img, _ = s.FloorplanImage(ctx, f.ID); string(img.Image) != "png" {
		t.Fatal("update lost the image")
	}
	if _, err := s.SaveFloorplan(ctx, Floorplan{ID: "nope", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := s.SetPins(ctx, f.ID, []Pin{{CameraID: cam.ID, X: 0.25, Y: 0.5, Heading: 90}}); err != nil {
		t.Fatal(err)
	}
	pins, _ := s.Pins(ctx, f.ID)
	if len(pins) != 1 || pins[0].X != 0.25 || pins[0].Heading != 90 {
		t.Fatalf("pins %+v", pins)
	}
	if err := s.SetPins(ctx, f.ID, nil); err != nil {
		t.Fatal(err)
	}
	if pins, _ = s.Pins(ctx, f.ID); len(pins) != 0 {
		t.Fatalf("replace-all left %+v", pins)
	}
	if err := s.SetPins(ctx, "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pins on missing plan: %v", err)
	}
	_ = s.SetPins(ctx, f.ID, []Pin{{CameraID: cam.ID, X: 0.1, Y: 0.1, Heading: -1}})
	if err := s.DeleteFloorplan(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Floorplans(ctx); len(list) != 0 {
		t.Fatalf("list %+v", list)
	}
	if err := s.DeleteFloorplan(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM floorplan_pins`).Scan(&n)
	if n != 0 {
		t.Fatalf("pins survived their plan: %d", n)
	}
}
