package insights

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	weekFrom = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	weekTo   = weekFrom.Add(7 * 24 * time.Hour)
	afterAll = weekTo.Add(time.Hour)
)

// weekOf is a past seven-day range with one camera, Gate (a).
func weekOf(alarms ...AlarmCount) Input {
	return Input{From: weekFrom, To: weekTo, Loc: time.UTC, Alarms: alarms,
		Cameras: []Camera{{ID: "a", Name: "Gate", AlertOn: "auto"}}}
}

func count(day, camera, kind string, n int) AlarmCount {
	return AlarmCount{Day: day, CameraID: camera, Kind: kind, Count: n}
}

// find builds the report and returns the recommendation with id, or nil.
func find(in Input, now time.Time, id string) *Recommendation {
	for _, r := range Build(in, now).Recommendations {
		if r.ID == id {
			return &r
		}
	}
	return nil
}

func TestMotionOnly(t *testing.T) {
	cases := []struct {
		name          string
		motion, other int
		alertOn       string
		want          bool
	}{
		{"19 alarms", 19, 0, "auto", false},
		{"20 alarms", 20, 0, "auto", true},
		{"69% motion", 69, 31, "motion", false},
		{"70% motion", 70, 30, "motion", true},
		{"already people & vehicles", 20, 0, "peopleAndVehicles", false},
		{"people only", 20, 0, "people", false},
	}
	for _, c := range cases {
		in := weekOf(count("2026-10-10", "a", "Motion", c.motion), count("2026-10-11", "a", "Vehicle", c.other))
		in.Cameras[0].AlertOn = c.alertOn
		if r := find(in, afterAll, "motion-only:a"); (r != nil) != c.want {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	r := find(weekOf(count("2026-10-10", "a", "Motion", 14), count("2026-10-10", "a", "Vehicle", 6)), afterAll, "motion-only:a")
	want := Recommendation{ID: "motion-only:a", Level: "suggestion", CameraID: "a", Title: "Mostly motion alarms on Gate",
		Detail: "14 of 20 alarms were plain motion. Switching to people & vehicles keeps the people and vehicle alerts.",
		Action: &Action{Type: "setAlertOn", CameraID: "a", AlertOn: "peopleAndVehicles"}}
	if r == nil || !reflect.DeepEqual(*r, want) {
		t.Fatalf("14 of 20: %+v", r)
	}
}

func TestRepeatOffline(t *testing.T) {
	offline := func(days ...string) Input {
		in := weekOf()
		for _, d := range days {
			in.Alarms = append(in.Alarms, count(d, "a", "Camera Offline", 4))
		}
		return in
	}
	if r := find(offline("2026-10-10", "2026-10-11"), afterAll, "repeat-offline:a"); r != nil {
		t.Fatalf("2 days: %+v", r)
	}
	r := find(offline("2026-10-10", "2026-10-11", "2026-10-13"), afterAll, "repeat-offline:a")
	want := Recommendation{ID: "repeat-offline:a", Level: "attention", CameraID: "a", Title: "Gate keeps going offline",
		Detail: "It went offline on 3 different days. Check its power and network connection.",
		Action: &Action{Type: "openCamera", CameraID: "a"}}
	if r == nil || !reflect.DeepEqual(*r, want) {
		t.Fatalf("3 days: %+v", r)
	}
}

func TestNoPeopleRule(t *testing.T) {
	// Person alarms on two cameras, one of them deleted: they count together.
	people := func(n int, ruleKinds ...[]string) Input {
		in := weekOf(count("2026-10-10", "a", "Person", n-n/2), count("2026-10-11", "gone", "Person", n/2))
		in.RuleKinds = ruleKinds
		return in
	}
	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"4 person alarms", people(4), false},
		{"5 person alarms", people(5), true},
		{"a rule for people", people(5, []string{"Vehicle"}, []string{"Person"}), false},
		{"a rule for every kind", people(5, []string{}), false},
		{"kind in lower case", people(5, []string{"person"}), false},
		{"rules for other kinds", people(5, []string{"Vehicle", "Motion"}), true},
	}
	for _, c := range cases {
		if r := find(c.in, afterAll, "no-people-rule"); (r != nil) != c.want {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	r := find(people(5), afterAll, "no-people-rule")
	want := Recommendation{ID: "no-people-rule", Level: "suggestion", Title: "No rule covers people",
		Detail: "5 person alarms came in, and no enabled rule covers them. A rule can push them to phones and tell operators what to do.",
		Action: &Action{Type: "openTemplate", TemplateID: "after-hours-people"}}
	if r == nil || !reflect.DeepEqual(*r, want) {
		t.Fatalf("got %+v", r)
	}
}

func TestStorageAlarms(t *testing.T) {
	if r := find(weekOf(), afterAll, "storage-alarms"); r != nil {
		t.Fatalf("no alarms: %+v", r)
	}
	r := find(weekOf(count("2026-10-12", "", "Storage", 1)), afterAll, "storage-alarms")
	want := Recommendation{ID: "storage-alarms", Level: "attention", Title: "Storage alarms",
		Detail: "1 storage alarm in this range. Check the free space on the recordings disk.",
		Action: &Action{Type: "openStorage"}}
	if r == nil || !reflect.DeepEqual(*r, want) {
		t.Fatalf("one alarm: %+v", r)
	}
}

func TestQuietCamera(t *testing.T) {
	before := weekFrom.Add(-time.Hour)
	quiet := func(to, now, created time.Time, events int) *Recommendation {
		in := Input{From: weekFrom, To: to, Loc: time.UTC, Cameras: []Camera{{ID: "a", Name: "Gate", AlertOn: "auto", Created: created}}}
		if events > 0 {
			in.Events = []EventCount{{CameraID: "a", Kind: "Motion", Hour: weekFrom, Count: events}}
		}
		return find(in, now, "quiet-camera:a")
	}
	cases := []struct {
		name string
		got  *Recommendation
		want bool
	}{
		{"7 days without events", quiet(weekTo, afterAll, before, 0), true},
		{"a millisecond short of 7 days", quiet(weekTo.Add(-time.Millisecond), afterAll, before, 0), false},
		{"one event", quiet(weekTo, afterAll, before, 1), false},
		{"camera added during the range", quiet(weekTo, afterAll, weekFrom.Add(time.Hour), 0), false},
		{"creation time unknown", quiet(weekTo, afterAll, time.Time{}, 0), true},
		{"range ends in the future", quiet(weekTo, weekFrom.Add(3*24*time.Hour), before, 0), false},
	}
	for _, c := range cases {
		if (c.got != nil) != c.want {
			t.Errorf("%s: %+v", c.name, c.got)
		}
	}
	want := Recommendation{ID: "quiet-camera:a", Level: "info", CameraID: "a", Title: "No events from Gate",
		Detail: "Nothing was detected in 7 days. Check that the camera still shows the scene and that motion detection is on."}
	if r := quiet(weekTo, afterAll, before, 0); r == nil || !reflect.DeepEqual(*r, want) {
		t.Fatalf("got %+v", r)
	}
}

// Eight fire; six are kept: attention, then suggestion, then info, each by id.
func TestRecommendationsCapAndOrder(t *testing.T) {
	in := weekOf(count("2026-10-10", "m", "Motion", 20), count("2026-10-10", "", "Storage", 2), count("2026-10-10", "x", "Person", 5))
	in.Cameras = []Camera{{ID: "q2", Name: "Q2"}, {ID: "c", Name: "C"}, {ID: "q1", Name: "Q1"}, {ID: "m", Name: "M", AlertOn: "motion"},
		{ID: "b", Name: "B"}, {ID: "q3", Name: "Q3"}}
	for _, cam := range []string{"c", "b"} {
		for _, d := range []string{"2026-10-11", "2026-10-12", "2026-10-13"} {
			in.Alarms = append(in.Alarms, count(d, cam, "Camera Offline", 1))
		}
	}
	for _, cam := range []string{"m", "b", "c"} {
		in.Events = append(in.Events, EventCount{CameraID: cam, Kind: "Motion", Hour: weekFrom, Count: 1})
	}
	var ids []string
	for _, r := range Build(in, afterAll).Recommendations {
		ids = append(ids, r.ID)
	}
	want := "repeat-offline:b,repeat-offline:c,storage-alarms,motion-only:m,no-people-rule,quiet-camera:q1"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
