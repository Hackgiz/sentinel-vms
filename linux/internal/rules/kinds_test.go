package rules

import "testing"

func TestLineCrossAliasInRuleKinds(t *testing.T) {
	for _, in := range []string{"linecross", "LineCross", "line_crossing", "Line crossing", " line-cross "} {
		if got := CanonicalKind(in); got != "Line crossing" {
			t.Errorf("CanonicalKind(%q) = %q", in, got)
		}
	}
	if CanonicalKind("Person") != "Person" {
		t.Error("other kinds must pass through")
	}

	// The rules editor / API may send the contract's spelling; it validates,
	// is stored canonically, and matches both our tracker's alarms and the
	// camera-reported ones (same alarm kind).
	r := Rule{Name: "Gate", Enabled: true, Kinds: []string{"linecross", "Line crossing", "Person"}}
	r.Normalize()
	if len(r.Kinds) != 2 || r.Kinds[0] != "Line crossing" || r.Kinds[1] != "Person" {
		t.Fatalf("normalized kinds %v", r.Kinds)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := Rule{Name: "x", Kinds: []string{"linecross"}, Delivery: DeliveryDashboard, MinSeverity: "Info"}
	if err := raw.Validate(); err != nil { // not normalized yet: alias still validates
		t.Fatalf("alias rejected: %v", err)
	}
	if err := (&Rule{Name: "x", Kinds: []string{"teleport"}, Delivery: DeliveryDashboard, MinSeverity: "Info"}).Validate(); err == nil {
		t.Fatal("unknown kind accepted")
	}

	now := at(9, 12, 0)
	line := Alarm{CameraID: "c1", Kind: "Line crossing", Severity: "Critical", At: now}
	motion := Alarm{CameraID: "c1", Kind: "Motion", Severity: "Critical", At: now}
	if !r.Matches(line, utc) || r.Matches(motion, utc) {
		t.Error("normalized rule filter wrong")
	}
	// A rule saved before normalization still matches.
	unnorm := Rule{Enabled: true, Kinds: []string{"linecross"}, MinSeverity: "Info"}
	if !unnorm.Matches(line, utc) || unnorm.Matches(motion, utc) {
		t.Error("alias rule filter wrong")
	}
	// The editor's kind list offers it.
	found := false
	for _, k := range Kinds {
		found = found || k == "Line crossing"
	}
	if !found {
		t.Error("Kinds lacks Line crossing")
	}
}
