package store

import (
	"context"
	"testing"
	"time"
)

// Event hours are local hours in the given zone: across the end of daylight
// saving time the repeated 01:xx hour is two buckets, and a zone whose
// offset isn't whole hours (India, +5:30) starts its hours at :30 UTC.
func TestEventActivityLocalHours(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	s := open(t)
	ctx := context.Background()
	add := func(at time.Time) {
		t.Helper()
		if err := s.AddEvent(ctx, &Event{CameraID: "a", Kind: "Motion", CreatedAt: at.Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	// 2026-11-01: clocks go back at 02:00 EDT -> 01:00 EST in New York.
	firstOne := time.Date(2026, 11, 1, 5, 20, 0, 0, time.UTC)  // 01:20 EDT
	secondOne := time.Date(2026, 11, 1, 6, 20, 0, 0, time.UTC) // 01:20 EST
	add(firstOne)
	add(firstOne.Add(10 * time.Minute))
	add(secondOne)
	add(time.Date(2026, 11, 1, 7, 5, 0, 0, time.UTC)) // 02:05 EST

	from, to := firstOne.Add(-time.Hour).Unix(), firstOne.Add(4*time.Hour).Unix()
	got, err := s.EventActivity(ctx, from, to, ny)
	if err != nil {
		t.Fatal(err)
	}
	want := []ActivityCount{
		{"a", "Motion", time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC).Unix(), 2}, // 01:00 EDT
		{"a", "Motion", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC).Unix(), 1}, // 01:00 EST
		{"a", "Motion", time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC).Unix(), 1}, // 02:00 EST
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bucket %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Half-hour zone: 06:20 UTC is 11:50 local, in the local hour that began
	// at 06:30 - 1h = 05:30 UTC.
	got, err = s.EventActivity(ctx, from, to, kolkata)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.Hour%3600 != 1800 {
			t.Fatalf("Kolkata hour starts at :30 UTC, got %v", time.Unix(c.Hour, 0).UTC())
		}
	}
	if len(got) != 3 || got[0].Hour != time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC).Unix() || got[0].Count != 1 || got[1].Count != 2 {
		t.Fatalf("Kolkata buckets %+v", got)
	}
}

func TestLocalHourStartSpringForward(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	// 2026-03-08: 02:00 EST jumps to 03:00 EDT (07:00 UTC). 06:59 UTC is
	// 01:59 EST, 07:00 UTC is 03:00 EDT.
	before := time.Date(2026, 3, 8, 6, 59, 0, 0, time.UTC)
	after := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	if got, want := localHourStart(before.Unix(), ny), time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC).Unix(); got != want {
		t.Fatalf("before = %v", time.Unix(got, 0).UTC())
	}
	if got, want := localHourStart(after.Unix(), ny), after.Unix(); got != want {
		t.Fatalf("after = %v", time.Unix(got, 0).UTC())
	}
}
