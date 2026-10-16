package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEvidenceCasesListFilterDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	add := func(cam string, at int64, alarm string) *EvidenceCase {
		e := &EvidenceCase{Evidence: Evidence{ID: NewID(), CaseID: s.NextCaseID(ctx, time.Unix(at, 0)), AlarmID: alarm, CameraID: cam, CameraName: cam,
			Title: "t", Range: "r", File: "f.mp4", Size: 3, SHA256: "ab", LockedAt: at, LockedBy: "u"}, Note: "n-" + cam, FromMs: 1000, ToMs: 2000}
		if err := s.AddEvidenceCase(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	a := add("camA", 100, "")
	add("camB", 300, "alarm1") // alarm-created rows are listed too
	add("camA", 200, "")

	all, err := s.EvidenceCases(ctx, EvidenceCaseFilter{})
	if err != nil || len(all) != 3 || all[0].CameraID != "camB" || all[2].ID != a.ID {
		t.Fatalf("all: %v %+v", err, all)
	}
	if all[0].RecorderID != LocalRecorderID || all[2].Note != "n-camA" || all[2].FromMs != 1000 || all[2].ToMs != 2000 {
		t.Fatalf("columns: %+v", all[2])
	}
	onlyA, _ := s.EvidenceCases(ctx, EvidenceCaseFilter{CameraIDs: []string{"camA"}})
	if len(onlyA) != 2 || onlyA[0].LockedAt != 200 {
		t.Fatalf("camA: %+v", onlyA)
	}
	if none, _ := s.EvidenceCases(ctx, EvidenceCaseFilter{CameraIDs: []string{}}); len(none) != 0 {
		t.Fatalf("empty scope: %+v", none)
	}
	if lim, _ := s.EvidenceCases(ctx, EvidenceCaseFilter{Limit: 1}); len(lim) != 1 {
		t.Fatalf("limit: %+v", lim)
	}
	got, err := s.EvidenceCaseByID(ctx, a.ID)
	if err != nil || got.Note != "n-camA" {
		t.Fatalf("by id: %v %+v", err, got)
	}
	if err := s.DeleteEvidenceCase(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEvidenceCase(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.EvidenceCaseByID(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
