package store

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestValidateLine(t *testing.T) {
	for _, c := range []struct {
		name string
		pts  [][2]float64
		want string // "" = valid, else an error substring
	}{
		{"diagonal", [][2]float64{{0.1, 0.1}, {0.9, 0.8}}, ""},
		{"edge to edge", [][2]float64{{0, 0}, {1, 1}}, ""},
		{"vertical", [][2]float64{{0.5, 0}, {0.5, 1}}, ""},
		{"one point", [][2]float64{{0.5, 0.5}}, "exactly 2"},
		{"none", nil, "exactly 2"},
		{"three points", [][2]float64{{0, 0}, {0.5, 0.5}, {1, 1}}, "exactly 2"},
		{"same place", [][2]float64{{0.5, 0.5}, {0.5, 0.5}}, "same place"},
		{"speck", [][2]float64{{0.5, 0.5}, {0.5, 0.505}}, "too short"},
		{"x above 1", [][2]float64{{0, 0}, {1.2, 1}}, "outside the frame"},
		{"negative", [][2]float64{{-0.1, 0}, {1, 1}}, "outside the frame"},
		{"NaN", [][2]float64{{0, 0}, {math.NaN(), 1}}, "outside the frame"},
		{"Inf", [][2]float64{{0, 0}, {math.Inf(-1), 1}}, "outside the frame"},
	} {
		err := ValidateLine(c.pts)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

func TestValidateZonesLineKindKeepsPolygonRules(t *testing.T) {
	seg := [][2]float64{{0.1, 0.1}, {0.9, 0.9}}
	tri := [][2]float64{{0, 0}, {1, 0}, {0, 1}}
	ok := func(in ...ZoneInput) error { return ValidateZones(in) }

	if err := ok(ZoneInput{Kind: "line", Points: seg}, ZoneInput{Kind: "detect", Points: tri}, ZoneInput{Kind: "mask", Points: tri}); err != nil {
		t.Fatalf("mixed set: %v", err)
	}
	// The 2-point rule belongs to lines alone: polygons still need 3.
	for _, kind := range []string{"detect", "mask"} {
		if err := ok(ZoneInput{Kind: kind, Points: seg}); err == nil || !strings.Contains(err.Error(), "3 to 20") {
			t.Errorf("%s with 2 points: %v", kind, err)
		}
	}
	// ...and lines don't take polygons.
	if err := ok(ZoneInput{Kind: "line", Points: tri}); err == nil || !strings.Contains(err.Error(), "exactly 2") {
		t.Errorf("line with 3 points: %v", err)
	}
	// Polygon checks are untouched (bowtie still rejected).
	if err := ok(ZoneInput{Kind: "detect", Points: [][2]float64{{0.1, 0.1}, {0.9, 0.9}, {0.9, 0.1}, {0.1, 0.9}}}); err == nil {
		t.Error("bowtie accepted")
	}
	if err := ok(ZoneInput{Kind: "blur", Points: tri}); err == nil || !strings.Contains(err.Error(), `"line"`) {
		t.Errorf("unknown kind: %v", err)
	}
	// Meta: bad direction and non-JSON are rejected for lines.
	if err := ok(ZoneInput{Kind: "line", Points: seg, Meta: `{"direction":"up"}`}); err == nil {
		t.Error("bad direction accepted")
	}
	if err := ok(ZoneInput{Kind: "line", Points: seg, Meta: `nope`}); err == nil {
		t.Error("non-JSON meta accepted")
	}
	// Severity: only Critical, Warning and Info, spelled exactly.
	for _, m := range []string{`{"severity":"critical"}`, `{"severity":"High"}`, `{"severity":3}`} {
		if err := ok(ZoneInput{Kind: "line", Points: seg, Meta: m}); err == nil {
			t.Errorf("bad severity %s accepted", m)
		}
	}
	for _, m := range []string{"", "null", "{}", `{"direction":"ltr"}`, `{"direction":"rtl"}`, `{"direction":"both"}`,
		`{"severity":"Critical"}`, `{"severity":"Warning"}`, `{"direction":"rtl","severity":"Info"}`} {
		if err := ok(ZoneInput{Kind: "line", Points: seg, Meta: m}); err != nil {
			t.Errorf("meta %q: %v", m, err)
		}
	}
	// 51 zones is still too many, lines included.
	many := make([]ZoneInput, MaxZones+1)
	for i := range many {
		many[i] = ZoneInput{Kind: "line", Points: seg}
	}
	if err := ValidateZones(many); err == nil {
		t.Error("too many lines accepted")
	}
}

func TestNormalizeZoneMeta(t *testing.T) {
	for _, c := range []struct{ kind, in, want string }{
		{"line", "", `{"direction":"both","severity":"Critical"}`},
		{"line", "{}", `{"direction":"both","severity":"Critical"}`},
		{"line", `{"direction":"ltr","extra":1}`, `{"direction":"ltr","severity":"Critical"}`},
		{"line", ` {"direction":"rtl"} `, `{"direction":"rtl","severity":"Critical"}`},
		{"line", `{"severity":"Warning"}`, `{"direction":"both","severity":"Warning"}`},
		{"line", `{"direction":"ltr","severity":"Info"}`, `{"direction":"ltr","severity":"Info"}`},
		{"detect", `{"direction":"ltr"}`, `{}`},
		{"mask", "garbage", `{}`},
	} {
		got, err := NormalizeZoneMeta(c.kind, c.in)
		if err != nil || got != c.want {
			t.Errorf("%s %q: got %q, %v want %q", c.kind, c.in, got, err, c.want)
		}
	}
}

func TestCameraLineZoneRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	cam, err := s.SaveCamera(ctx, "", CameraInput{Name: "Gate", RTSPURL: "rtsp://10.0.0.1/s"})
	if err != nil {
		t.Fatal(err)
	}
	seg := [][2]float64{{0.2, 0.5}, {0.8, 0.5}}
	tri := [][2]float64{{0, 0}, {1, 0}, {0, 1}}
	zs, err := s.SetCameraZones(ctx, cam.ID, []ZoneInput{
		{Kind: "line", Name: "Front gate", Points: seg, Meta: `{"direction":"ltr"}`, Enabled: true},
		{Kind: "line", Name: "Default dir", Points: seg, Enabled: true},
		{Kind: "line", Name: "Off", Points: seg, Meta: `{"direction":"rtl","severity":"Info"}`, Enabled: false},
		{Kind: "detect", Points: tri, Meta: `{"direction":"ltr"}`, Enabled: true},
	})
	if err != nil || len(zs) != 4 {
		t.Fatalf("set: %+v %v", zs, err)
	}
	if zs[0].Meta != `{"direction":"ltr","severity":"Critical"}` || zs[1].Meta != `{"direction":"both","severity":"Critical"}` || zs[3].Meta != `{}` {
		t.Fatalf("metas %q %q %q", zs[0].Meta, zs[1].Meta, zs[3].Meta)
	}
	if LineSeverity(zs[2]) != "Info" || LineSeverity(zs[1]) != "Critical" || LineSeverity(zs[3]) != "" {
		t.Fatalf("severities %q %q %q", LineSeverity(zs[2]), LineSeverity(zs[1]), LineSeverity(zs[3]))
	}
	// A wave-8 row stored before severity existed reads as Critical.
	if got := LineSeverity(Zone{Kind: "line", Meta: `{"direction":"ltr"}`}); got != "Critical" {
		t.Fatalf("legacy meta severity %q", got)
	}
	if got := LineSeverity(Zone{Kind: "line", Meta: "garbage"}); got != "Critical" {
		t.Fatalf("damaged meta severity %q", got)
	}
	if len(zs[0].Points) != 2 || zs[0].Points[1] != [2]float64{0.8, 0.5} {
		t.Fatalf("line points %v", zs[0].Points)
	}
	// Recorders get enabled lines with name and direction; polygons carry none.
	lz := LinkZones(zs)
	if len(lz) != 3 || lz[0].Kind != "line" || lz[0].Name != "Front gate" || lz[0].Direction != "ltr" || lz[1].Direction != "both" || lz[2].Direction != "" {
		t.Fatalf("link zones %+v", lz)
	}
	all, err := s.AllCameraZones(ctx)
	if err != nil || len(all[cam.ID]) != 4 || all[cam.ID][0].Meta != `{"direction":"ltr","severity":"Critical"}` {
		t.Fatalf("all: %+v %v", all, err)
	}
	// A hand-damaged meta still watches the line, both ways.
	if got := LineDirection(Zone{Kind: "line", Meta: "garbage"}); got != DirBoth {
		t.Fatalf("damaged meta read as %q", got)
	}
	if LineDirection(Zone{Kind: "detect"}) != "" {
		t.Fatal("polygons have no direction")
	}
}
