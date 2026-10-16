package rules

import (
	"testing"
	"time"
)

// 2024-06-02 is a Sunday.
func tpAt(day, h, m int) time.Time { return time.Date(2024, 6, 2+day, h, m, 0, 0, time.UTC) }

func TestTimeProfileWeekdaySpan(t *testing.T) {
	p := TimeProfile{Weekly: []Span{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "17:30"}}}
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"Mon 07:59 before", tpAt(1, 7, 59), false},
		{"Mon 08:00 start is inside", tpAt(1, 8, 0), true},
		{"Fri 17:29 last minute", tpAt(5, 17, 29), true},
		{"Fri 17:30 end is outside", tpAt(5, 17, 30), false},
		{"Sat noon wrong day", tpAt(6, 12, 0), false},
		{"Sun noon wrong day", tpAt(0, 12, 0), false},
	}
	for _, c := range cases {
		if got := p.ActiveAt(c.t); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestTimeProfilePastMidnight(t *testing.T) {
	// Friday 22:00 to 06:00: the early-morning part belongs to Friday's span.
	p := TimeProfile{Weekly: []Span{{Days: []int{5}, Start: "22:00", End: "06:00"}}}
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"Fri 21:59", tpAt(5, 21, 59), false},
		{"Fri 22:00", tpAt(5, 22, 0), true},
		{"Fri 03:00 is before the span began", tpAt(5, 3, 0), false},
		{"Sat 00:00", tpAt(6, 0, 0), true},
		{"Sat 05:59", tpAt(6, 5, 59), true},
		{"Sat 06:00 end", tpAt(6, 6, 0), false},
		{"Sat 22:00 not listed", tpAt(6, 22, 0), false},
		{"Sun 03:00 (Sat span) not listed", tpAt(0, 3, 0), false},
	}
	for _, c := range cases {
		if got := p.ActiveAt(c.t); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestTimeProfileEndOfDayAndMultipleSpans(t *testing.T) {
	p := TimeProfile{Weekly: []Span{
		{Days: []int{0}, Start: "00:00", End: "24:00"},
		{Days: []int{3}, Start: "09:00", End: "10:00"},
	}}
	if !p.ActiveAt(tpAt(0, 23, 59)) || !p.ActiveAt(tpAt(0, 0, 0)) {
		t.Error("all-day Sunday span")
	}
	if p.ActiveAt(tpAt(1, 0, 0)) {
		t.Error("Monday 00:00 is outside")
	}
	if !p.ActiveAt(tpAt(3, 9, 30)) || p.ActiveAt(tpAt(3, 10, 0)) {
		t.Error("second span")
	}
	if (&TimeProfile{}).ActiveAt(tpAt(1, 12, 0)) {
		t.Error("empty profile is never active")
	}
}

func TestTimeProfileValidate(t *testing.T) {
	bad := []Span{
		{Days: []int{1}, Start: "8:00", End: "17:00"},
		{Days: []int{1}, Start: "08:00", End: "25:00"},
		{Days: []int{1}, Start: "24:00", End: "01:00"},
		{Days: []int{1}, Start: "08:60", End: "09:00"},
		{Days: []int{1}, Start: "08:00", End: "08:00"},
		{Days: []int{}, Start: "08:00", End: "09:00"},
		{Days: []int{7}, Start: "08:00", End: "09:00"},
	}
	for _, sp := range bad {
		if (&TimeProfile{Weekly: []Span{sp}}).Validate() == nil {
			t.Errorf("%+v should be invalid", sp)
		}
	}
	if err := (&TimeProfile{Weekly: []Span{{Days: []int{0, 6}, Start: "22:00", End: "24:00"}}}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestRuleGatedByProfile(t *testing.T) {
	r := Rule{Name: "x", Enabled: true, MinSeverity: "Info", Delivery: DeliveryDashboard}
	a := Alarm{Kind: "Person", Severity: "Critical", At: tpAt(1, 12, 0)}
	if !r.Matches(a, time.UTC) {
		t.Fatal("no profile: today's behavior")
	}
	r.SetProfile(&TimeProfile{Weekly: []Span{{Days: []int{1}, Start: "13:00", End: "14:00"}}})
	if r.Matches(a, time.UTC) {
		t.Fatal("outside the profile the rule must not fire")
	}
	a.At = tpAt(1, 13, 30)
	if !r.Matches(a, time.UTC) {
		t.Fatal("inside the profile")
	}
}
