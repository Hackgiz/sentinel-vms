package store

import (
	"context"
	"testing"
	"time"
)

func TestNextCaseIDSkipsImportedAndGaps(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	day := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)
	if got := s.NextCaseID(ctx, day); got != "HG-20261011-00001" {
		t.Fatalf("empty day: %s", got)
	}
	// An imported case, an extra clip of it, and nothing numbered 1–6.
	for i, id := range []string{"HG-20261011-00007", "HG-20261011-00007-2"} {
		e := &Evidence{ID: id, CaseID: id, CameraID: "c", Title: "t", File: id + ".mp4", LockedAt: int64(i)}
		if err := s.AddEvidence(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.NextCaseID(ctx, day); got != "HG-20261011-00008" {
		t.Fatalf("after import: %s", got)
	}
}
