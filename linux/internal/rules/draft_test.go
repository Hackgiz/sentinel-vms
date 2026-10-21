package rules

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

var (
	draftCameras  = []NameRef{{"c1", "Loading dock"}, {"c2", "Front door"}, {"c3", "Yard"}, {"c4", "yard "}}
	draftProfiles = []NameRef{{"p1", "Business hours"}}
)

func resolve(t *testing.T, d Draft) (Rule, []string) {
	t.Helper()
	r, warn, err := ResolveDraft(d, draftCameras, draftProfiles)
	if err != nil {
		t.Fatalf("ResolveDraft(%+v): %v", d, err)
	}
	if r.Enabled {
		t.Fatal("a draft must come back disabled")
	}
	return r, warn
}

// The contract's example: "tell me about people at the loading dock after 6pm".
func TestResolveDraftExample(t *testing.T) {
	r, warn := resolve(t, Draft{Name: " Loading dock after hours ", CameraNames: []string{"loading DOCK"}, Kinds: []string{"person"},
		MinSeverity: "warning", Schedule: true, From: "18:00", To: "07:00", Days: []int{0, 1, 2, 3, 4, 5, 6}, Delivery: "Push",
		Instructions: " Check the live view. "})
	want := Rule{Name: "Loading dock after hours", CameraIDs: []string{"c1"}, Kinds: []string{"Person"}, MinSeverity: "Warning",
		Schedule: true, FromMinute: 18 * 60, ToMinute: 7 * 60, Days: []int{}, Delivery: DeliveryPush, Instructions: "Check the live view."}
	if !reflect.DeepEqual(r, want) || len(warn) != 0 {
		t.Fatalf("got %+v %q\nwant %+v", r, warn, want)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDraftCameras(t *testing.T) {
	// An unknown name is left out with a warning; the rest still resolve.
	r, warn := resolve(t, Draft{CameraNames: []string{"Garage", "front door", " "}, Kinds: []string{"Person"}})
	if strings.Join(r.CameraIDs, ",") != "c2" || strings.Join(warn, "|") != `No camera called "Garage".` {
		t.Fatalf("%v %q", r.CameraIDs, warn)
	}
	// Every camera with that name.
	if r, _ := resolve(t, Draft{CameraNames: []string{"YARD", "Yard"}}); strings.Join(r.CameraIDs, ",") != "c3,c4" {
		t.Fatalf("same-name cameras: %v", r.CameraIDs)
	}
	// Nothing matched, or nothing named: every camera, and the admin is told.
	for _, names := range [][]string{{"Garage"}, nil} {
		r, warn := resolve(t, Draft{CameraNames: names, Kinds: []string{"Person"}})
		if r.CameraIDs == nil || len(r.CameraIDs) != 0 || warn[len(warn)-1] != "The rule applies to every camera." {
			t.Fatalf("%v: %v %q", names, r.CameraIDs, warn)
		}
	}
}

func TestResolveDraftKinds(t *testing.T) {
	r, warn := resolve(t, Draft{CameraNames: []string{"Yard"}, Kinds: []string{"linecross", "AUDIO", "License plate", "Camera offline"}})
	if strings.Join(r.Kinds, ",") != "Line crossing,audio,Camera Offline" || strings.Join(warn, "|") != `No alarm kind called "License plate".` {
		t.Fatalf("%v %q", r.Kinds, warn)
	}
	for _, kinds := range [][]string{nil, {"Face"}} {
		r, warn := resolve(t, Draft{CameraNames: []string{"Yard"}, Kinds: kinds})
		if r.Kinds == nil || len(r.Kinds) != 0 || warn[len(warn)-1] != "The rule matches every kind of alarm." {
			t.Fatalf("%v: %v %q", kinds, r.Kinds, warn)
		}
	}
}

func TestResolveDraftDefaults(t *testing.T) {
	r, _ := resolve(t, Draft{})
	if r.Name != "New rule" || r.MinSeverity != "Warning" || r.Delivery != DeliveryDashboard || r.Schedule || len(r.Days) != 0 || r.TimeProfileID != "" {
		t.Fatalf("%+v", r)
	}
	for in, want := range map[string]string{"critical": "Critical", " Info ": "Info", "extreme": "Warning"} {
		if r, _ := resolve(t, Draft{MinSeverity: in}); r.MinSeverity != want {
			t.Errorf("severity %q -> %q, want %q", in, r.MinSeverity, want)
		}
	}
	for in, want := range map[string]string{"PUSH": DeliveryPush, "dashboard": DeliveryDashboard, "email": DeliveryDashboard} {
		if r, _ := resolve(t, Draft{Delivery: in}); r.Delivery != want {
			t.Errorf("delivery %q -> %q, want %q", in, r.Delivery, want)
		}
	}
}

func TestResolveDraftSchedule(t *testing.T) {
	cases := []struct {
		name     string
		d        Draft
		schedule bool
		from, to int
		warn     string
	}{
		{"overnight", Draft{Schedule: true, From: "22:00", To: "06:00"}, true, 22 * 60, 6 * 60, ""},
		{"daytime", Draft{Schedule: true, From: "09:30", To: "17:00"}, true, 9*60 + 30, 17 * 60, ""},
		{"until midnight", Draft{Schedule: true, From: "18:00", To: "24:00"}, true, 18 * 60, 0, ""},
		{"whole day", Draft{Schedule: true, From: "00:00", To: "24:00"}, false, 0, 0, ""},
		{"not HH:MM", Draft{Schedule: true, From: "6pm", To: "07:00"}, false, 0, 0, "The hours couldn't be read, so the rule runs at all hours."},
		{"no times", Draft{Schedule: true}, false, 0, 0, "The hours couldn't be read, so the rule runs at all hours."},
		{"24:00 start", Draft{Schedule: true, From: "24:00", To: "07:00"}, false, 0, 0, "The hours couldn't be read, so the rule runs at all hours."},
		{"empty window", Draft{Schedule: true, From: "09:00", To: "09:00"}, false, 0, 0, "The hours start and end at the same time, so the rule runs at all hours."},
		{"off", Draft{From: "18:00", To: "07:00"}, false, 0, 0, ""},
	}
	for _, c := range cases {
		c.d.CameraNames, c.d.Kinds = []string{"Yard"}, []string{"Person"}
		r, warn := resolve(t, c.d)
		if r.Schedule != c.schedule || r.FromMinute != c.from || r.ToMinute != c.to || strings.Join(warn, "|") != c.warn {
			t.Errorf("%s: schedule %v %d–%d %q", c.name, r.Schedule, r.FromMinute, r.ToMinute, warn)
		}
	}
}

func TestResolveDraftDays(t *testing.T) {
	cases := []struct {
		in   []int
		want string
		warn string
	}{
		{nil, "[]", ""},
		{[]int{0, 1, 2, 3, 4, 5, 6}, "[]", ""}, // every day is stored as none
		{[]int{5, 1, 3, 1}, "[1 3 5]", ""},
		{[]int{6, 7}, "[0 6]", ""}, // ISO Sunday
		{[]int{2, 9, -1}, "[2]", ""},
		{[]int{9}, "[]", "The days couldn't be read, so the rule runs every day."},
	}
	for _, c := range cases {
		r, warn := resolve(t, Draft{CameraNames: []string{"Yard"}, Kinds: []string{"Person"}, Days: c.in})
		if fmt.Sprint(r.Days) != c.want || r.Days == nil || strings.Join(warn, "|") != c.warn {
			t.Errorf("days %v -> %v %q", c.in, r.Days, warn)
		}
	}
	// Days apply without a window too: weekends, all day.
	if r, _ := resolve(t, Draft{Days: []int{0, 6}}); r.Schedule || fmt.Sprint(r.Days) != "[0 6]" {
		t.Fatalf("weekends: %+v", r)
	}
}

func TestResolveDraftTimeProfile(t *testing.T) {
	r, warn := resolve(t, Draft{CameraNames: []string{"Yard"}, Kinds: []string{"Person"}, TimeProfileName: " business HOURS"})
	if r.TimeProfileID != "p1" || len(warn) != 0 {
		t.Fatalf("%q %q", r.TimeProfileID, warn)
	}
	r, warn = resolve(t, Draft{CameraNames: []string{"Yard"}, Kinds: []string{"Person"}, TimeProfileName: "Night shift"})
	if r.TimeProfileID != "" || strings.Join(warn, "|") != `No time profile called "Night shift".` {
		t.Fatalf("%q %q", r.TimeProfileID, warn)
	}
}

func TestResolveDraftClipsText(t *testing.T) {
	r, _ := resolve(t, Draft{Name: strings.Repeat("a", 59) + " bcd", Instructions: strings.Repeat("é", 600)})
	if r.Name != strings.Repeat("a", 59) || utf8.RuneCountInString(r.Instructions) != 500 {
		t.Fatalf("name %q, instructions %d characters", r.Name, utf8.RuneCountInString(r.Instructions))
	}
	// 60 characters of three bytes each would break Validate's 120-byte name
	// limit, so the name stops at 40.
	r, _ = resolve(t, Draft{Name: strings.Repeat("監", 70)})
	if r.Name != strings.Repeat("監", 40) {
		t.Fatalf("name %q (%d bytes)", r.Name, len(r.Name))
	}
	if r, _ := resolve(t, Draft{Name: "   "}); r.Name != "New rule" {
		t.Fatalf("blank name -> %q", r.Name)
	}
}
