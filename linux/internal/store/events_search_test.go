package store

import (
	"context"
	"fmt"
	"testing"
)

func TestSearchEventsFiltersCursorAndFacets(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, _ := st.SaveCamera(ctx, "", CameraInput{Name: "A", RTSPURL: "rtsp://10.0.0.1/s"})
	b, _ := st.SaveCamera(ctx, "", CameraInput{Name: "B", RTSPURL: "rtsp://10.0.0.2/s"})
	add := func(e Event) {
		t.Helper()
		if err := st.AddEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	add(Event{ID: "e1", CameraID: a.ID, Kind: "Person", Label: "person", Score: 0.9, CreatedAt: 1000, Description: "Man in a red jacket", Threat: "low", Tags: "person,package"})
	add(Event{ID: "e2", CameraID: a.ID, Kind: "Vehicle", Label: "car", Score: 0.5, CreatedAt: 2000, Threat: "none"})
	add(Event{ID: "e3", CameraID: b.ID, Kind: "Person", Label: "person", Score: 0.7, CreatedAt: 3000, Threat: "high", Anomaly: true, Description: "100% sure_thing"})
	add(Event{ID: "e4", CameraID: b.ID, Kind: "Motion", Score: 0.1, CreatedAt: 3000})

	ids := func(f EventFilter) string {
		t.Helper()
		ev, _, err := st.SearchEvents(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		s := ""
		for _, e := range ev {
			s += e.ID + ","
		}
		return s
	}
	yes := true
	cases := []struct {
		name string
		f    EventFilter
		want string
	}{
		{"all newest first", EventFilter{}, "e4,e3,e2,e1,"},
		{"camera", EventFilter{CameraIDs: []string{a.ID}}, "e2,e1,"},
		{"kind", EventFilter{Kinds: []string{"Person", "Vehicle"}}, "e3,e2,e1,"},
		{"label", EventFilter{Labels: []string{"car"}}, "e2,"},
		{"range", EventFilter{From: 1500, To: 3000}, "e2,"},
		{"min score", EventFilter{MinScore: 0.6}, "e3,e1,"},
		{"threat", EventFilter{Threats: []string{"high", "low"}}, "e3,e1,"},
		{"anomaly", EventFilter{Anomaly: &yes}, "e3,"},
		{"text description, case-insensitive", EventFilter{Text: "RED jacket"}, "e1,"},
		{"text tags", EventFilter{Text: "package"}, "e1,"},
		{"text label", EventFilter{Text: "car"}, "e2,"},
		{"text escapes wildcards", EventFilter{Text: "%"}, "e3,"},
		{"text underscore literal", EventFilter{Text: "sure_t"}, "e3,"},
		{"combined", EventFilter{CameraIDs: []string{b.ID}, Kinds: []string{"Person"}, MinScore: 0.5}, "e3,"},
	}
	for _, c := range cases {
		if got := ids(c.f); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	// Keyset paging across equal timestamps (e3/e4 share created_at).
	p1, next, err := st.SearchEvents(ctx, EventFilter{Limit: 2})
	if err != nil || len(p1) != 2 || next == "" {
		t.Fatalf("page 1: %v %q %v", p1, next, err)
	}
	p2, next2, err := st.SearchEvents(ctx, EventFilter{Limit: 2, Before: next})
	if err != nil || len(p2) != 2 || next2 != "" {
		t.Fatalf("page 2: %v %q %v", p2, next2, err)
	}
	if got := p1[0].ID + p1[1].ID + p2[0].ID + p2[1].ID; got != "e4e3e2e1" {
		t.Fatalf("paged order %s", got)
	}
	if _, _, err := st.SearchEvents(ctx, EventFilter{Before: "junk"}); err != ErrBadCursor {
		t.Fatalf("bad cursor: %v", err)
	}
	for i := 0; i < 5; i++ {
		add(Event{CameraID: a.ID, Kind: "Motion", CreatedAt: int64(5000 + i)})
	}
	if ev, _, _ := st.SearchEvents(ctx, EventFilter{Limit: 100000}); len(ev) != 9 {
		t.Fatalf("limit clamp: %d", len(ev))
	}
	_ = fmt.Sprint

	f, err := st.EventFacets(ctx, 0, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := func(fc []FacetCount, v string) int {
		for _, c := range fc {
			if c.Value == v {
				return c.Count
			}
		}
		return 0
	}
	if count(f.Kinds, "Person") != 2 || count(f.Kinds, "Motion") != 1 || count(f.Labels, "person") != 2 || count(f.Labels, "") != 0 || count(f.Threats, "high") != 1 {
		t.Fatalf("facets %+v", f)
	}
	fa, _ := st.EventFacets(ctx, 0, 0, []string{a.ID})
	if count(fa.Kinds, "Person") != 1 || count(fa.Kinds, "Vehicle") != 1 {
		t.Fatalf("camera facets %+v", fa)
	}
}
