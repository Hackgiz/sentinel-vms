package server

import (
	"net/http/httptest"
	"testing"
)

func TestExportPrefer(t *testing.T) {
	for q, want := range map[string]struct{ small, ok bool }{
		"":              {false, true},
		"?prefer=full":  {false, true},
		"?prefer=small": {true, true},
		"?prefer=tiny":  {false, false},
	} {
		small, ok := exportPrefer(httptest.NewRequest("POST", "/x"+q, nil))
		if small != want.small || ok != want.ok {
			t.Errorf("%q: got %v %v", q, small, ok)
		}
	}
	if (exportOpts{preferSmall: true}).prefer() != "small" || (exportOpts{}).prefer() != "" {
		t.Fatal("prefer() wrong")
	}
}
