package insights

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var est = time.FixedZone("EST", -5*3600)

func estAt(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, est) }

// Days are local, zero-filled and oldest first; an hour of events lands on
// its local day; the cameras, totals and mix add up.
func TestBuildDailyCamerasAndMix(t *testing.T) {
	in := Input{
		From: estAt(10, 12), To: estAt(13, 12), Loc: est,
		Cameras: []Camera{
			{ID: "a", Name: "Gate", AlertOn: "auto"},
			{ID: "b", Name: "Dock", AlertOn: "people"},
			{ID: "c", Name: "Attic", AlertOn: "motion"},
		},
		Events: []EventCount{
			{CameraID: "a", Kind: "Motion", Hour: estAt(10, 12), Count: 3},
			// 03:00 UTC on the 12th is 22:00 on the 11th here.
			{CameraID: "a", Kind: "Person", Hour: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC), Count: 2},
			{CameraID: "b", Kind: "Motion", Hour: estAt(13, 11), Count: 1},
		},
		Alarms: []AlarmCount{
			{Day: "2026-10-11", CameraID: "a", Kind: "Motion", Count: 4},
			{Day: "2026-10-11", CameraID: "a", Kind: "Person", Count: 1},
			{Day: "2026-10-12", CameraID: "b", Kind: "Person", Count: 2},
			{Day: "2026-10-12", CameraID: "b", Kind: "Camera Offline", Count: 1},
			{Day: "2026-10-12", CameraID: "gone", Kind: "Motion", Count: 1}, // a deleted camera
			{Day: "2026-10-13", Kind: "Storage", Count: 1},
		},
	}
	rep := Build(in, estAt(20, 0))

	var days []string
	for _, d := range rep.Daily {
		days = append(days, fmt.Sprintf("%s e%d a%d %v", d.Day, d.Events, d.Alarms, d.ByKind))
	}
	want := []string{
		"2026-10-10 e3 a0 map[]",
		"2026-10-11 e2 a5 map[Motion:4 Person:1]",
		"2026-10-12 e0 a4 map[Camera Offline:1 Motion:1 Person:2]",
		"2026-10-13 e1 a1 map[Storage:1]",
	}
	if !reflect.DeepEqual(days, want) {
		t.Fatalf("daily\n got %q\nwant %q", days, want)
	}
	if want := (Totals{Events: 6, Alarms: 10, CamerasWithAlarms: 3, OfflineCameras: 1}); rep.Totals != want {
		t.Fatalf("totals %+v, want %+v", rep.Totals, want)
	}
	wantCams := []CameraStat{
		{CameraID: "a", Name: "Gate", Alarms: 5, TopKind: "Motion", AlertOn: "auto"},
		{CameraID: "b", Name: "Dock", Alarms: 3, TopKind: "Person", AlertOn: "people"},
		{CameraID: "c", Name: "Attic", Alarms: 0, AlertOn: "motion"},
	}
	if !reflect.DeepEqual(rep.Cameras, wantCams) {
		t.Fatalf("cameras %+v", rep.Cameras)
	}
	wantMix := []KindShare{{"Motion", 5, 0.5}, {"Person", 3, 0.3}, {"Camera Offline", 1, 0.1}, {"Storage", 1, 0.1}}
	if !reflect.DeepEqual(rep.Mix, wantMix) {
		t.Fatalf("mix %+v", rep.Mix)
	}
}

// Ten cameras at most: most alarms first, then by name.
func TestBuildTopTenCameras(t *testing.T) {
	in := Input{From: estAt(10, 0), To: estAt(11, 0), Loc: est}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%02d", i)
		in.Cameras = append(in.Cameras, Camera{ID: id, Name: fmt.Sprintf("Cam %c", 'L'-i), AlertOn: "auto"})
		if i < 3 {
			in.Alarms = append(in.Alarms, AlarmCount{Day: "2026-10-10", CameraID: id, Kind: "Vehicle", Count: i + 1})
		}
	}
	rep := Build(in, estAt(20, 0))
	var names []string
	for _, c := range rep.Cameras {
		names = append(names, c.Name)
	}
	want := "Cam J,Cam K,Cam L,Cam A,Cam B,Cam C,Cam D,Cam E,Cam F,Cam G"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("cameras %s, want %s", got, want)
	}
}

// With nothing in the range the lists are empty, not null, and every day is
// still there.
func TestBuildEmptyRange(t *testing.T) {
	rep := Build(Input{From: estAt(10, 0), To: estAt(17, 0), Loc: est}, estAt(20, 0))
	if len(rep.Daily) != 7 || rep.Totals != (Totals{}) {
		t.Fatalf("daily %d, totals %+v", len(rep.Daily), rep.Totals)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"cameras":[]`, `"mix":[]`, `"recommendations":[]`, `"byKind":{}`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("missing %s in %s", s, b)
		}
	}
}

func TestLocalDays(t *testing.T) {
	type tc struct {
		name     string
		from, to time.Time
		loc      *time.Location
		want     string
	}
	cases := []tc{
		{"partial days at both ends", estAt(10, 12), estAt(13, 12), est, "2026-10-10,2026-10-11,2026-10-12,2026-10-13"},
		{"to at midnight is exclusive", estAt(10, 0), estAt(13, 0), est, "2026-10-10,2026-10-11,2026-10-12"},
		{"inside one day", estAt(10, 9), estAt(10, 10), est, "2026-10-10"},
		{"local, not UTC", time.Date(2026, 10, 11, 2, 0, 0, 0, time.UTC), time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC), est, "2026-10-10"},
	}
	if ny, err := time.LoadLocation("America/New_York"); err == nil {
		// Clocks go back on Nov 1: a 25-hour day.
		cases = append(cases, tc{"fall back", time.Date(2026, 10, 31, 0, 0, 0, 0, ny), time.Date(2026, 11, 3, 0, 0, 0, 0, ny), ny,
			"2026-10-31,2026-11-01,2026-11-02"})
	}
	if scl, err := time.LoadLocation("America/Santiago"); err == nil {
		// Clocks go forward at midnight on Sep 6, so that day has no 00:00.
		cases = append(cases, tc{"no midnight", time.Date(2026, 9, 5, 12, 0, 0, 0, scl), time.Date(2026, 9, 8, 12, 0, 0, 0, scl), scl,
			"2026-09-05,2026-09-06,2026-09-07,2026-09-08"})
	}
	for _, c := range cases {
		if got := strings.Join(localDays(c.from, c.to, c.loc), ","); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
