package cloud

import "testing"

func TestEntitlementPaid(t *testing.T) {
	for _, tc := range []struct {
		plan, status string
		want         bool
	}{
		{"cloud", "active", true},
		{"business", "active", true},
		{"cloud", "grace", true},
		{"business", "grace", true},
		{"free", "active", false},
		{"free", "inactive", false},
		{"cloud", "inactive", false},
		{"cloud", "canceled", false},
		{"business", "canceled", false},
		{"", "", false},
	} {
		if got := (Entitlement{Plan: tc.plan, Status: tc.status}).Paid(); got != tc.want {
			t.Errorf("%s/%s: Paid() = %v", tc.plan, tc.status, got)
		}
	}
	// A stale or unlinked entitlement is free.
	if (Entitlement{Plan: "cloud", Status: "active"}).free().Paid() {
		t.Error("free() still paid")
	}
}
