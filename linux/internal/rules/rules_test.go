package rules

import (
	"testing"
	"time"
)

var utc = time.UTC

// at builds a time on a known week: 2026-10-09 is a Friday.
func at(day, hour, min int) time.Time { return time.Date(2026, 10, day, hour, min, 0, 0, utc) }

func TestActiveAtDaytimeWindow(t *testing.T) {
	r := Rule{Schedule: true, FromMinute: 9 * 60, ToMinute: 17*60 + 30}
	cases := map[time.Time]bool{
		at(9, 8, 59):  false,
		at(9, 9, 0):   true,
		at(9, 17, 29): true,
		at(9, 17, 30): false, // end is exclusive
		at(9, 23, 0):  false,
	}
	for tm, want := range cases {
		if got := r.ActiveAt(tm); got != want {
			t.Errorf("%s: got %v want %v", tm.Format("Mon 15:04"), got, want)
		}
	}
}

func TestActiveAtOvernightWindow(t *testing.T) {
	r := Rule{Schedule: true, FromMinute: 22 * 60, ToMinute: 6 * 60}
	cases := map[time.Time]bool{
		at(9, 21, 59): false,
		at(9, 22, 0):  true,
		at(9, 23, 59): true,
		at(10, 0, 0):  true,
		at(10, 5, 59): true,
		at(10, 6, 0):  false,
		at(10, 12, 0): false,
	}
	for tm, want := range cases {
		if got := r.ActiveAt(tm); got != want {
			t.Errorf("%s: got %v want %v", tm.Format("Mon 15:04"), got, want)
		}
	}
}

func TestOvernightWindowBelongsToStartDay(t *testing.T) {
	// Fridays only, 22:00–06:00.
	r := Rule{Schedule: true, FromMinute: 22 * 60, ToMinute: 6 * 60, Days: []int{int(time.Friday)}}
	if !r.ActiveAt(at(9, 23, 0)) {
		t.Error("Friday 23:00 should be active")
	}
	if !r.ActiveAt(at(10, 3, 0)) {
		t.Error("Saturday 03:00 is part of Friday night")
	}
	if r.ActiveAt(at(9, 3, 0)) {
		t.Error("Friday 03:00 belongs to Thursday night")
	}
	if r.ActiveAt(at(10, 23, 0)) {
		t.Error("Saturday 23:00 is not a Friday window")
	}
}

func TestDaysWithoutSchedule(t *testing.T) {
	r := Rule{Days: []int{0, 6}} // weekends, all day
	if r.ActiveAt(at(9, 12, 0)) {
		t.Error("Friday should be inactive")
	}
	if !r.ActiveAt(at(10, 0, 30)) || !r.ActiveAt(at(11, 23, 59)) {
		t.Error("weekend should be active")
	}
}

func TestTimeZoneUsesServerLocation(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	r := Rule{Enabled: true, Schedule: true, FromMinute: 22 * 60, ToMinute: 6 * 60}
	// 03:00 UTC on Oct 10 = 23:00 EDT Oct 9 → active in New York.
	a := Alarm{Kind: "Motion", Severity: "Warning", At: time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)}
	if !r.Matches(a, ny) {
		t.Error("should match at 23:00 New York time")
	}
	// 12:00 UTC = 08:00 EDT → inactive.
	a.At = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if r.Matches(a, ny) {
		t.Error("should not match at 08:00 New York time")
	}
}

func TestMatchesFilters(t *testing.T) {
	r := Rule{Enabled: true, CameraIDs: []string{"cam1"}, Kinds: []string{"Person", "vehicle"}, MinSeverity: "Warning"}
	now := at(9, 12, 0)
	tests := []struct {
		name string
		a    Alarm
		want bool
	}{
		{"camera and kind match", Alarm{CameraID: "cam1", Kind: "Person", Severity: "Critical", At: now}, true},
		{"kind case-insensitive", Alarm{CameraID: "cam1", Kind: "Vehicle", Severity: "Warning", At: now}, true},
		{"other camera", Alarm{CameraID: "cam2", Kind: "Person", Severity: "Critical", At: now}, false},
		{"other kind", Alarm{CameraID: "cam1", Kind: "Motion", Severity: "Critical", At: now}, false},
		{"below min severity", Alarm{CameraID: "cam1", Kind: "Person", Severity: "Info", At: now}, false},
	}
	for _, tc := range tests {
		if got := r.Matches(tc.a, utc); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	// Alarms with no camera (Storage) pass the camera filter, like the Mac.
	r2 := Rule{Enabled: true, CameraIDs: []string{"cam1"}, MinSeverity: "Info"}
	if !r2.Matches(Alarm{Kind: "Storage", Severity: "Critical", At: now}, utc) {
		t.Error("camera-less alarm should pass the camera filter")
	}
	r2.Enabled = false
	if r2.Matches(Alarm{Kind: "Storage", Severity: "Critical", At: now}, utc) {
		t.Error("disabled rule matched")
	}
}

func TestEmptyRuleMatchesEverything(t *testing.T) {
	r := Rule{Enabled: true}
	r.Normalize()
	if !r.Matches(Alarm{CameraID: "x", Kind: "Motion", Severity: "Info", At: at(9, 3, 0)}, utc) {
		t.Error("an unfiltered rule should match any alarm")
	}
}

func TestInstructionsAndPushRules(t *testing.T) {
	rs := []Rule{
		{Name: "Dock", Enabled: true, Kinds: []string{"Person"}, MinSeverity: "Info", Delivery: DeliveryPush, Instructions: "Call the site manager."},
		{Name: "Silent", Enabled: true, MinSeverity: "Info", Delivery: DeliveryDashboard},
		{Name: "Off", Enabled: false, MinSeverity: "Info", Delivery: DeliveryPush, Instructions: "never"},
	}
	a := Alarm{CameraID: "c", Kind: "Person", Severity: "Critical", At: at(9, 12, 0)}
	ins := Instructions(rs, a, utc)
	if len(ins) != 1 || ins[0].Rule != "Dock" || ins[0].Text != "Call the site manager." {
		t.Fatalf("instructions = %+v", ins)
	}
	if p := PushRules(rs, a, utc); len(p) != 1 || p[0] != "Dock" {
		t.Fatalf("push rules = %v", p)
	}
	a.Kind = "Motion"
	if p := PushRules(rs, a, utc); len(p) != 0 {
		t.Fatalf("motion should not push: %v", p)
	}
	if ins := Instructions(nil, a, utc); ins == nil || len(ins) != 0 {
		t.Fatal("no rules must give an empty, non-nil list (JSON [])")
	}
}

func TestValidate(t *testing.T) {
	good := Rule{Name: "x", Schedule: true, FromMinute: 1320, ToMinute: 360, Kinds: []string{"Person"}}
	good.Normalize()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Rule{
		{Name: ""},
		{Name: "x", Schedule: true, FromMinute: 60, ToMinute: 60},
		{Name: "x", Schedule: true, FromMinute: 0, ToMinute: 1440},
		{Name: "x", Kinds: []string{"Dragon"}},
		{Name: "x", Days: []int{7}},
		{Name: "x", Delivery: "email"},
		{Name: "x", MinSeverity: "Severe"},
	}
	for i, r := range bad {
		r.Normalize()
		if r.Validate() == nil {
			t.Errorf("case %d should fail: %+v", i, r)
		}
	}
	all := Rule{Name: "x", Days: []int{0, 1, 2, 3, 4, 5, 6, 3}}
	all.Normalize()
	if len(all.Days) != 0 {
		t.Errorf("all seven days should normalize to every day, got %v", all.Days)
	}
}

func TestInWindow(t *testing.T) {
	if !InWindow(23*60, 22*60, 7*60) || !InWindow(60, 22*60, 7*60) || InWindow(12*60, 22*60, 7*60) {
		t.Error("overnight window")
	}
	if InWindow(5, 5, 5) {
		t.Error("empty window")
	}
}
