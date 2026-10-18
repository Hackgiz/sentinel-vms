package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBookmarkRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	cam, err := s.SaveCamera(ctx, "", CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.1/s"})
	if err != nil {
		t.Fatal(err)
	}
	pt := &Bookmark{CameraID: cam.ID, AtMs: 10_000, Title: "  Delivery ", CreatedBy: "u1"}
	if err := s.AddBookmark(ctx, pt); err != nil || pt.ID == "" || pt.Title != "Delivery" || pt.Color != "blue" || pt.CreatedAt == 0 {
		t.Fatalf("add point: %+v %v", pt, err)
	}
	rg := &Bookmark{CameraID: cam.ID, AtMs: 100_000, DurationMs: 60_000, Title: "Incident", Note: "n", Color: "RED", CreatedBy: "u2"}
	if err := s.AddBookmark(ctx, rg); err != nil || rg.Color != "red" {
		t.Fatalf("add range: %+v %v", rg, err)
	}
	if err := s.AddBookmark(ctx, &Bookmark{CameraID: "nope", AtMs: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing camera: %v", err)
	}

	// Window queries: ranges overlapping the window count.
	for _, c := range []struct {
		from, to int64
		want     int
	}{{0, 0, 2}, {5_000, 20_000, 1}, {20_000, 90_000, 0}, {120_000, 130_000, 1}, {161_000, 200_000, 0}, {10_000, 100_000, 2}} {
		got, err := s.Bookmarks(ctx, cam.ID, c.from, c.to)
		if err != nil || len(got) != c.want {
			t.Fatalf("window %d-%d: %d bookmarks (want %d) %v", c.from, c.to, len(got), c.want, err)
		}
	}
	if got, _ := s.Bookmarks(ctx, cam.ID, 0, 0); got[0].ID != pt.ID || got[1].ID != rg.ID {
		t.Fatalf("order %+v", got)
	}
	if got, _ := s.Bookmarks(ctx, "other", 0, 0); len(got) != 0 {
		t.Fatalf("other camera: %+v", got)
	}

	upd := *rg
	upd.Title, upd.DurationMs, upd.Color, upd.CreatedBy = "Renamed", 0, "green", "hacker"
	got, err := s.UpdateBookmark(ctx, upd)
	if err != nil || got.Title != "Renamed" || got.DurationMs != 0 || got.Color != "green" || got.CreatedBy != "u2" || got.CameraID != cam.ID {
		t.Fatalf("update: %+v %v", got, err)
	}
	if _, err := s.UpdateBookmark(ctx, Bookmark{ID: "nope", AtMs: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := s.DeleteBookmark(ctx, pt.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBookmark(ctx, pt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestBookmarkValidation(t *testing.T) {
	for _, c := range []string{"", "red", " Blue ", "gray"} {
		if _, err := NormalizeBookmarkColor(c); err != nil {
			t.Errorf("color %q: %v", c, err)
		}
	}
	if c, _ := NormalizeBookmarkColor("  "); c != BookmarkDefaultColor {
		t.Errorf("blank color -> %q", c)
	}
	for _, c := range []string{"#ff0000", "crimson", "red;"} {
		var bad BookmarkInvalidError
		if _, err := NormalizeBookmarkColor(c); !errors.As(err, &bad) {
			t.Errorf("color %q accepted (%v)", c, err)
		}
	}
	for ms, ok := range map[int64]bool{0: true, 1: true, MaxBookmarkDurationMs: true, -1: false, MaxBookmarkDurationMs + 1: false} {
		if err := ValidateBookmarkDuration(ms); (err == nil) != ok {
			t.Errorf("duration %d: %v", ms, err)
		}
	}
	for name, b := range map[string]Bookmark{
		"no time":    {AtMs: 0},
		"long title": {AtMs: 1, Title: strings.Repeat("x", MaxBookmarkTitle+1)},
		"long note":  {AtMs: 1, Note: strings.Repeat("x", MaxBookmarkNote+1)},
		"bad color":  {AtMs: 1, Color: "mauve"},
		"bad dur":    {AtMs: 1, DurationMs: -5},
	} {
		b := b
		if err := b.clean(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
