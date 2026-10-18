package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestValidatePolygon(t *testing.T) {
	square := [][2]float64{{0.1, 0.1}, {0.9, 0.1}, {0.9, 0.9}, {0.1, 0.9}}
	many := func(n int) [][2]float64 { // n points on a convex arc (no self-intersection)
		out := make([][2]float64, n)
		for i := range out {
			a := 2 * math.Pi * float64(i) / float64(n)
			out[i] = [2]float64{0.5 + 0.4*math.Cos(a), 0.5 + 0.4*math.Sin(a)}
		}
		return out
	}
	for _, c := range []struct {
		name string
		pts  [][2]float64
		want string // "" = valid, else a substring of the error
	}{
		{"triangle", [][2]float64{{0, 0}, {1, 0}, {0, 1}}, ""},
		{"square", square, ""},
		{"reversed winding", [][2]float64{{0.1, 0.9}, {0.9, 0.9}, {0.9, 0.1}, {0.1, 0.1}}, ""},
		{"concave L", [][2]float64{{0.1, 0.1}, {0.5, 0.1}, {0.5, 0.5}, {0.9, 0.5}, {0.9, 0.9}, {0.1, 0.9}}, ""},
		{"20 points", many(20), ""},
		{"21 points", many(21), "3 to 20"},
		{"two points", [][2]float64{{0, 0}, {1, 1}}, "3 to 20"},
		{"none", nil, "3 to 20"},
		{"x above 1", [][2]float64{{0, 0}, {1.01, 0}, {0, 1}}, "outside the frame"},
		{"negative y", [][2]float64{{0, -0.1}, {1, 0}, {0, 1}}, "outside the frame"},
		{"NaN", [][2]float64{{0, 0}, {math.NaN(), 0}, {0, 1}}, "outside the frame"},
		{"Inf", [][2]float64{{0, 0}, {math.Inf(1), 0}, {0, 1}}, "outside the frame"},
		{"repeated point", [][2]float64{{0, 0}, {0, 0}, {1, 1}, {0, 1}}, "same place"},
		{"collinear", [][2]float64{{0, 0}, {0.5, 0.5}, {1, 1}}, "folds back"},
		{"sliver", [][2]float64{{0, 0}, {1, 0}, {1, 1e-7}}, "no area"},
		{"bowtie", [][2]float64{{0.1, 0.1}, {0.9, 0.9}, {0.9, 0.1}, {0.1, 0.9}}, "crosses itself"},
		{"edge touches a vertex", [][2]float64{{0.1, 0.1}, {0.9, 0.1}, {0.5, 0.1}, {0.5, 0.9}}, ""}, // replaced below
		{"fold back", [][2]float64{{0.1, 0.1}, {0.9, 0.1}, {0.5, 0.1}, {0.5, 0.9}}, "folds back"},
		{"closing edge crosses", [][2]float64{{0.1, 0.1}, {0.9, 0.1}, {0.9, 0.9}, {0.3, 0.9}, {0.3, 0}}, "crosses itself"},
		{"concave arrow", [][2]float64{{0.1, 0.5}, {0.5, 0.1}, {0.9, 0.5}, {0.5, 0.9}, {0.5, 0.3}}, ""},
	} {
		if c.name == "edge touches a vertex" {
			// A T-touch: the last edge's end lands on an earlier edge.
			c.pts, c.want = [][2]float64{{0.1, 0.1}, {0.9, 0.1}, {0.9, 0.9}, {0.5, 0.1}, {0.1, 0.9}}, "crosses itself"
		}
		err := ValidatePolygon(c.pts)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

func TestValidateZones(t *testing.T) {
	tri := [][2]float64{{0, 0}, {1, 0}, {0, 1}}
	if err := ValidateZones([]ZoneInput{{Kind: "detect", Points: tri}, {Kind: "mask", Name: "Window", Points: tri}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateZones([]ZoneInput{{Kind: "blur", Points: tri}}); err == nil {
		t.Fatal("bad kind accepted")
	}
	if err := ValidateZones([]ZoneInput{{Kind: "mask", Name: strings.Repeat("x", 101), Points: tri}}); err == nil {
		t.Fatal("long name accepted")
	}
	if err := ValidateZones([]ZoneInput{{Kind: "mask", Points: tri[:2]}}); err == nil || !strings.Contains(err.Error(), "zone 1") {
		t.Fatalf("short polygon: %v", err)
	}
	if err := ValidateZones(make([]ZoneInput, MaxZones+1)); err == nil {
		t.Fatal("too many zones accepted")
	}
}

func TestCameraZonesRoundTrip(t *testing.T) {
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
	tri := [][2]float64{{0, 0}, {1, 0}, {0, 1}}
	sq := [][2]float64{{0.2, 0.2}, {0.4, 0.2}, {0.4, 0.4}, {0.2, 0.4}}

	if zs, err := s.CameraZones(ctx, cam.ID); err != nil || len(zs) != 0 {
		t.Fatalf("empty: %v %v", zs, err)
	}
	zs, err := s.SetCameraZones(ctx, cam.ID, []ZoneInput{
		{Kind: "detect", Name: " Drive ", Points: tri, Enabled: true},
		{Kind: "mask", Name: "Window", Points: sq, Enabled: false},
	})
	if err != nil || len(zs) != 2 || zs[0].Name != "Drive" || !zs[0].Enabled || zs[1].Enabled || zs[1].Points[2] != [2]float64{0.4, 0.4} {
		t.Fatalf("set: %+v %v", zs, err)
	}
	// Only enabled zones go to recorders.
	if lz := LinkZones(zs); len(lz) != 1 || lz[0].Kind != "detect" {
		t.Fatalf("link zones %+v", lz)
	}
	// Replace: keep the first by id, drop the second, add a new one.
	keep := zs[0]
	zs2, err := s.SetCameraZones(ctx, cam.ID, []ZoneInput{
		{ID: keep.ID, Kind: "detect", Name: "Driveway", Points: tri, Enabled: true},
		{ID: "stranger", Kind: "mask", Points: sq, Enabled: true},
	})
	if err != nil || len(zs2) != 2 || zs2[0].ID != keep.ID || zs2[0].Name != "Driveway" || zs2[0].CreatedAt != keep.CreatedAt {
		t.Fatalf("replace: %+v %v", zs2, err)
	}
	if zs2[1].ID == "stranger" || zs2[1].ID == zs[1].ID {
		t.Fatalf("an unknown id must mint a new zone: %+v", zs2[1])
	}
	all, err := s.AllCameraZones(ctx)
	if err != nil || len(all[cam.ID]) != 2 {
		t.Fatalf("all: %v %v", all, err)
	}
	if _, err := s.SetCameraZones(ctx, "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing camera: %v", err)
	}
	// Clearing works, and deleting the camera takes zones with it.
	if zs, err := s.SetCameraZones(ctx, cam.ID, nil); err != nil || len(zs) != 0 {
		t.Fatalf("clear: %v %v", zs, err)
	}
	if _, err := s.SetCameraZones(ctx, cam.ID, []ZoneInput{{Kind: "mask", Points: sq, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCamera(ctx, cam.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllCameraZones(ctx); len(all[cam.ID]) != 0 {
		t.Fatalf("zones outlived their camera: %v", all)
	}
}
