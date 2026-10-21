package rules

import (
	"reflect"
	"strings"
	"testing"
)

// TestTemplatesMatchTheContract checks the catalog against the table in
// WAVE14-AI-ASSIST.md §2, in order.
func TestTemplatesMatchTheContract(t *testing.T) {
	type row struct {
		id, category, kinds string
		schedule            bool
		from, to            int
		severity, delivery  string
	}
	want := []row{
		{"after-hours-people", CategorySecurity, "Person", true, 18 * 60, 7 * 60, "Warning", DeliveryPush},
		{"loitering-entrance", CategorySecurity, "Loitering", false, 0, 0, "Warning", DeliveryPush},
		{"vehicle-night", CategorySecurity, "Vehicle", true, 22 * 60, 6 * 60, "Warning", DeliveryDashboard},
		{"line-crossing-intrusion", CategorySecurity, "Line crossing,Intrusion", false, 0, 0, "Critical", DeliveryPush},
		{"tamper", CategorySecurity, "Tamper", false, 0, 0, "Critical", DeliveryPush},
		{"camera-offline", CategoryHealth, "Camera Offline", false, 0, 0, "Critical", DeliveryPush},
		{"storage-low", CategoryHealth, "Storage", false, 0, 0, "Critical", DeliveryPush},
		{"loud-noise", CategoryOperations, "audio", false, 0, 0, "Warning", DeliveryDashboard},
	}
	list := Templates()
	if len(list) != len(want) {
		t.Fatalf("%d templates, want %d", len(list), len(want))
	}
	for i, tp := range list {
		w, r := want[i], tp.Rule
		got := row{tp.ID, tp.Category, strings.Join(r.Kinds, ","), r.Schedule, r.FromMinute, r.ToMinute, r.MinSeverity, r.Delivery}
		if got != w {
			t.Errorf("template %d:\n got %+v\nwant %+v", i, got, w)
		}
		if tp.Title == "" || tp.Summary == "" || r.Instructions == "" || r.Name != tp.Title || !r.Enabled {
			t.Errorf("%s: title %q summary %q instructions %q name %q enabled %v", tp.ID, tp.Title, tp.Summary, r.Instructions, r.Name, r.Enabled)
		}
		// Every camera until the admin picks some, every day, no time profile;
		// the lists are [] in JSON, not null.
		if r.CameraIDs == nil || len(r.CameraIDs) != 0 || r.Days == nil || len(r.Days) != 0 || r.TimeProfileID != "" || len(r.Actions) != 0 {
			t.Errorf("%s: cameras %v days %v profile %q actions %v", tp.ID, r.CameraIDs, r.Days, r.TimeProfileID, r.Actions)
		}
	}
}

func TestTemplatesAreValidRules(t *testing.T) {
	seen := map[string]bool{}
	for _, tp := range Templates() {
		if seen[tp.ID] {
			t.Errorf("duplicate template id %q", tp.ID)
		}
		seen[tp.ID] = true
		r := tp.Rule
		r.Normalize()
		if !reflect.DeepEqual(r, tp.Rule) {
			t.Errorf("%s changes when normalized:\n%+v\n%+v", tp.ID, tp.Rule, r)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", tp.ID, err)
		}
		if len(r.Kinds) == 0 {
			t.Errorf("%s matches every alarm", tp.ID)
		}
		for _, k := range r.Kinds {
			if !contains(Kinds, k, false) { // spelled exactly as alarms carry it
				t.Errorf("%s: unknown kind %q", tp.ID, k)
			}
		}
	}
}

func TestTemplatesAreFreshCopies(t *testing.T) {
	a := Templates()
	a[0].Rule.Kinds[0], a[0].Rule.Name = "Motion", "edited"
	if b := Templates(); b[0].Rule.Kinds[0] != "Person" || b[0].Rule.Name != "After-hours people" {
		t.Fatalf("a caller's edit leaked into the catalog: %+v", b[0].Rule)
	}
}
