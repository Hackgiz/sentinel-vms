package store

import (
	"context"
	"errors"
	"testing"
)

func TestWallLayoutRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	mine := &WallLayout{Name: "Night shift", Body: `{"monitors":[]}`, CreatedBy: "u1"}
	if err := s.AddWallLayout(ctx, mine); err != nil || mine.ID == "" || mine.CreatedAt == 0 || mine.UpdatedAt != mine.CreatedAt {
		t.Fatalf("add: %+v %v", mine, err)
	}
	shared := &WallLayout{Name: "lobby", Body: `{"monitors":[]}`, Shared: true, CreatedBy: "u2"}
	if err := s.AddWallLayout(ctx, shared); err != nil {
		t.Fatal(err)
	}
	other := &WallLayout{Name: "Private 2", Body: `{}`, CreatedBy: "u2"}
	if err := s.AddWallLayout(ctx, other); err != nil {
		t.Fatal(err)
	}

	got, err := s.WallLayoutsFor(ctx, "u1")
	if err != nil || len(got) != 2 || got[0].Name != "lobby" || !got[0].Shared || got[1].ID != mine.ID {
		t.Fatalf("u1 list: %+v %v", got, err)
	}
	if got, _ := s.WallLayoutsFor(ctx, "u2"); len(got) != 2 {
		t.Fatalf("u2 list: %+v", got)
	}

	mine.Name, mine.Body, mine.Shared = "Days", `{"monitors":[{"slot":0}]}`, true
	if err := s.UpdateWallLayout(ctx, mine); err != nil {
		t.Fatal(err)
	}
	back, err := s.WallLayoutByID(ctx, mine.ID)
	if err != nil || back.Name != "Days" || back.Body != mine.Body || !back.Shared || back.CreatedBy != "u1" {
		t.Fatalf("read back: %+v %v", back, err)
	}
	if err := s.DeleteWallLayout(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WallLayoutByID(ctx, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if err := s.DeleteWallLayout(ctx, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.UpdateWallLayout(ctx, mine); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update gone: %v", err)
	}
}
