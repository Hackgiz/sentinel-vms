package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

const lineSeg = `[[0.2,0.5],[0.8,0.5]]`

func TestLineZonesAPI(t *testing.T) {
	f := newScopeFixture(t)
	h := f.h
	put := func(zones ...string) (int, map[string]any) {
		return v2Call(t, h, "PUT", zonesURL(f.front.ID), f.admin, zonePut(zones...))
	}

	code, m := put(
		fmt.Sprintf(`{"kind":"line","name":"Gate","points":%s,"meta":{"direction":"ltr"}}`, lineSeg),
		fmt.Sprintf(`{"kind":"line","name":"Default","points":%s}`, lineSeg),
		fmt.Sprintf(`{"kind":"detect","name":"Drive","points":%s}`, zoneSquare))
	items, _ := m["items"].([]any)
	if code != 200 || len(items) != 3 {
		t.Fatalf("PUT: %d %v", code, m)
	}
	meta := func(i int) any {
		z := items[i].(map[string]any)
		mm, _ := z["meta"].(map[string]any)
		return mm["direction"]
	}
	if items[0].(map[string]any)["kind"] != "line" || len(items[0].(map[string]any)["points"].([]any)) != 2 || meta(0) != "ltr" || meta(1) != "both" {
		t.Fatalf("line zones: %v", items)
	}
	if _, has := items[2].(map[string]any)["meta"]; has {
		t.Fatalf("polygons carry no meta: %v", items[2])
	}
	code, m = v2Call(t, h, "GET", zonesURL(f.front.ID), f.op, "")
	if code != 200 || len(m["items"].([]any)) != 3 || m["items"].([]any)[0].(map[string]any)["meta"].(map[string]any)["direction"] != "ltr" {
		t.Fatalf("GET: %d %v", code, m)
	}
	// Lines never become privacy masks on the camera payload.
	code, m = v2Call(t, h, "GET", "/api/v2/cameras/"+f.front.ID, f.admin, "")
	if v, _ := m["masks"].([]any); code != 200 || len(v) != 0 {
		t.Fatalf("masks %v", m["masks"])
	}
	if got := auditDetails(t, f.s, "Set camera zones"); !strings.Contains(got, "2 line") {
		t.Fatalf("audit detail %q", got)
	}

	for name, bad := range map[string]string{
		"three-point line": fmt.Sprintf(`{"kind":"line","points":%s}`, zoneTriangle),
		"one-point line":   `{"kind":"line","points":[[0.5,0.5]]}`,
		"degenerate line":  `{"kind":"line","points":[[0.5,0.5],[0.5,0.5]]}`,
		"line off frame":   `{"kind":"line","points":[[0,0],[1.5,1]]}`,
		"bad direction":    fmt.Sprintf(`{"kind":"line","points":%s,"meta":{"direction":"north"}}`, lineSeg),
		"meta not object":  fmt.Sprintf(`{"kind":"line","points":%s,"meta":"ltr"}`, lineSeg),
		"2-point detect":   fmt.Sprintf(`{"kind":"detect","points":%s}`, lineSeg),
		"2-point mask":     fmt.Sprintf(`{"kind":"mask","points":%s}`, lineSeg),
	} {
		if code, m := put(bad); code != 400 {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	if zs, _ := f.s.Store.CameraZones(context.Background(), f.front.ID); len(zs) != 3 {
		t.Fatalf("rejected PUTs changed the set: %d zones", len(zs))
	}

	// The recorder's configuration carries enabled lines with name+direction.
	cams, _ := f.s.Store.Cameras(context.Background())
	snap := f.s.snapshot(context.Background(), store.LocalRecorderID, cams)
	for _, c := range snap.Cameras {
		if c.ID != f.front.ID {
			continue
		}
		if len(c.Zones) != 3 || c.Zones[0].Kind != "line" || c.Zones[0].Name != "Gate" || c.Zones[0].Direction != "ltr" || c.Zones[1].Direction != "both" {
			t.Fatalf("snapshot zones %+v", c.Zones)
		}
		return
	}
	t.Fatal("front camera missing from the snapshot")
}

func auditDetails(t *testing.T, s *Server, action string) string {
	t.Helper()
	entries, err := s.Store.AuditLog(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action {
			return e.Detail
		}
	}
	return ""
}

func TestLineCrossingEventAlarmAndRules(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	cam, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Yard", RTSPURL: "rtsp://10.0.0.9/s"})
	if _, err := s.Store.SetCameraZones(ctx, cam.ID, []store.ZoneInput{
		{Kind: "line", Name: "Back fence", Points: [][2]float64{{0.2, 0.5}, {0.8, 0.5}}, Meta: `{"direction":"rtl"}`, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	syncCams(t, s)

	at := time.Now()
	obj := detect.Object{Kind: "Person", Label: "person", Score: 0.88, Box: [4]float64{0.47, 0.3, 0.53, 0.5}}
	recOf(s).OnLineCross(detect.LineCrossing{CameraID: cam.ID, Line: detect.Line{Name: "Back fence", Direction: "rtl"}, Direction: detect.DirRTL, Object: obj, At: at})

	events, _ := s.Store.Events(ctx, "", 10)
	if len(events) != 1 || events[0].Kind != "Line crossing" || events[0].Label != "person" || events[0].CameraID != cam.ID {
		t.Fatalf("events %+v", events)
	}
	alarms, _ := s.Store.Alarms(ctx, true, 10)
	if len(alarms) != 1 || alarms[0].Kind != "Line crossing" || alarms[0].Title != "Line crossed" || alarms[0].Severity != "Critical" ||
		!strings.Contains(alarms[0].Detail, `"Back fence"`) || !strings.Contains(alarms[0].Detail, "a person") {
		t.Fatalf("alarms %+v", alarms)
	}
	if events[0].AlarmID != alarms[0].ID {
		t.Fatalf("event not attached to its alarm: %+v", events[0])
	}

	// A second crossing inside the merge window folds into the same alarm.
	recOf(s).OnLineCross(detect.LineCrossing{CameraID: cam.ID, Line: detect.Line{Name: "Back fence"}, Direction: detect.DirRTL, Object: obj, At: at.Add(20 * time.Second)})
	if alarms, _ := s.Store.Alarms(ctx, true, 10); len(alarms) != 1 {
		t.Fatalf("%d alarms for two crossings 20 s apart", len(alarms))
	}

	// Rules filter on the kind, under either spelling.
	save := func(name string, kinds ...string) {
		r := &rules.Rule{Name: name, Enabled: true, Kinds: kinds, MinSeverity: "Info", Instructions: "Check the fence: " + name}
		r.Normalize()
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := s.Store.SaveAlarmRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	save("by-alias", "linecross")
	save("by-name", "Line crossing")
	save("motion-only", "Motion")
	s.invalidateRules()
	alarms, _ = s.Store.Alarms(ctx, true, 10)
	var got []string
	for _, in := range s.alarmInstructions(ctx, &alarms[0]) {
		got = append(got, in.Rule)
	}
	if strings.Join(got, ",") != "by-alias,by-name" && strings.Join(got, ",") != "by-name,by-alias" {
		t.Fatalf("matching rules %v", got)
	}
}

// Wave 9: per-line severity. The API carries meta.severity (default
// Critical, validated); crossings alarm at the line's severity, and an
// "Info" line logs the event without an alarm.
func TestLineSeverity(t *testing.T) {
	f := newScopeFixture(t)
	code, m := v2Call(t, f.h, "PUT", zonesURL(f.front.ID), f.admin, zonePut(
		fmt.Sprintf(`{"kind":"line","name":"Gate","points":%s,"meta":{"direction":"ltr","severity":"Warning"}}`, lineSeg),
		fmt.Sprintf(`{"kind":"line","name":"Default","points":%s}`, lineSeg)))
	items, _ := m["items"].([]any)
	if code != 200 || len(items) != 2 {
		t.Fatalf("PUT: %d %v", code, m)
	}
	sev := func(i int) any { return items[i].(map[string]any)["meta"].(map[string]any)["severity"] }
	if sev(0) != "Warning" || sev(1) != "Critical" {
		t.Fatalf("severities %v %v", sev(0), sev(1))
	}
	for _, bad := range []string{`"critical"`, `"High"`, `2`} {
		if code, m := v2Call(t, f.h, "PUT", zonesURL(f.front.ID), f.admin, zonePut(
			fmt.Sprintf(`{"kind":"line","points":%s,"meta":{"severity":%s}}`, lineSeg, bad))); code != 400 {
			t.Errorf("severity %s: %d %v", bad, code, m)
		}
	}

	s := newCompanionServer(t)
	ctx := context.Background()
	cam, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Yard", RTSPURL: "rtsp://10.0.0.9/s"})
	seg := [][2]float64{{0.2, 0.5}, {0.8, 0.5}}
	if _, err := s.Store.SetCameraZones(ctx, cam.ID, []store.ZoneInput{
		{Kind: "line", Name: "Path", Points: seg, Meta: `{"severity":"Info"}`, Enabled: true},
		{Kind: "line", Name: "Drive", Points: seg, Meta: `{"severity":"Warning"}`, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	syncCams(t, s)
	at := time.Now()
	obj := detect.Object{Kind: "Person", Label: "person", Score: 0.9, Box: [4]float64{0.47, 0.3, 0.53, 0.5}}
	recOf(s).OnLineCross(detect.LineCrossing{CameraID: cam.ID, Line: detect.Line{Name: "Path"}, Direction: detect.DirLTR, Object: obj, At: at})
	events, _ := s.Store.Events(ctx, "", 10)
	if len(events) != 1 || events[0].Kind != "Line crossing" || events[0].AlarmID != "" {
		t.Fatalf("Info line events %+v", events)
	}
	if alarms, _ := s.Store.Alarms(ctx, true, 10); len(alarms) != 0 {
		t.Fatalf("Info line raised an alarm: %+v", alarms)
	}
	recOf(s).OnLineCross(detect.LineCrossing{CameraID: cam.ID, Line: detect.Line{Name: "Drive"}, Direction: detect.DirLTR, Object: obj, At: at.Add(time.Second)})
	alarms, _ := s.Store.Alarms(ctx, true, 10)
	if len(alarms) != 1 || alarms[0].Severity != "Warning" || alarms[0].Title != "Line crossed" {
		t.Fatalf("Warning line alarms %+v", alarms)
	}
}

func TestLineSeverityOf(t *testing.T) {
	line := func(name, meta string) store.Zone { return store.Zone{Kind: "line", Name: name, Meta: meta} }
	zs := []store.Zone{
		line("A", `{"severity":"Info"}`),
		line("B", `{"severity":"Info"}`), line("B", `{"severity":"Warning"}`),
		line("C", `{"direction":"ltr"}`),
		{Kind: "detect", Name: "D"},
	}
	for name, want := range map[string]string{"A": "Info", "B": "Warning", "C": "Critical", "D": "Critical", "gone": "Critical"} {
		if got := lineSeverityOf(zs, name); got != want {
			t.Errorf("%s: %q want %q", name, got, want)
		}
	}
}
