package store

import (
	"context"
	"strings"
	"testing"
)

func TestSemanticStoreScanPurge(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	add := func(cam, kind string, at int64, thumb string) *Event {
		e := &Event{CameraID: cam, Kind: kind, Score: 0.9, CreatedAt: at, Thumbnail: thumb}
		if err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	a := add("c1", "Person", 100, "a.jpg")
	b := add("c1", "Motion", 200, "b.jpg") // no box needed: the whole snapshot is embedded
	c := add("c2", "Person", 300, "c.jpg")
	v := add("c2", "Vehicle", 400, "v.jpg")
	add("c1", "Person", 600, "") // no thumbnail yet
	put := func(e *Event, model string) {
		if err := s.PutSemantic(ctx, SemanticVector{EventID: e.ID, Model: model, Dim: 2, Vec: []byte{1, 2, 3, 4, 5, 6, 7, 8}, CameraID: e.CameraID, AtMs: e.CreatedAt * 1000}); err != nil {
			t.Fatal(err)
		}
	}

	todo, err := s.EventsToSemantic(ctx, "m1", 50000)
	if err != nil || len(todo) != 4 || todo[0].ID != v.ID || todo[3].ID != a.ID {
		t.Fatalf("to index: %v %v", todo, err)
	}
	put(a, "m1")
	put(b, "m1")
	put(c, "m1")
	put(v, "old")
	if todo, _ := s.EventsToSemantic(ctx, "m1", 50000); len(todo) != 1 || todo[0].ID != v.ID {
		t.Fatalf("after indexing: %v", todo)
	}
	if todo, _ := s.EventsToSemantic(ctx, "m2", 2); len(todo) != 2 || todo[0].ID != v.ID || todo[1].ID != c.ID {
		t.Fatalf("capped: %v", todo)
	}
	// Appearance vectors live elsewhere: semantic rows never show there.
	if n, _ := s.CountEmbeddings(ctx, "m1"); n != 0 {
		t.Fatal("semantic rows leaked into event_embeddings")
	}

	scan := func(f SemanticFilter) string {
		var ids []string
		if err := s.ScanSemantic(ctx, f, func(id string, vec []byte) {
			if len(vec) != 8 {
				t.Fatalf("vec %v", vec)
			}
			ids = append(ids, id)
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Join(ids, ",")
	}
	if got := scan(SemanticFilter{Model: "m1"}); got != c.ID+","+b.ID+","+a.ID {
		t.Fatalf("all: %s", got)
	}
	if got := scan(SemanticFilter{Model: "m1", CameraIDs: []string{"c1"}}); got != b.ID+","+a.ID {
		t.Fatalf("camera: %s", got)
	}
	if got := scan(SemanticFilter{Model: "m1", CameraIDs: []string{}}); got != "" {
		t.Fatalf("no cameras: %s", got)
	}
	if got := scan(SemanticFilter{Model: "m1", FromMs: 150_000, ToMs: 300_000}); got != b.ID {
		t.Fatalf("window: %s", got)
	}
	if got := scan(SemanticFilter{Model: "m1", Max: 1}); got != c.ID {
		t.Fatalf("max: %s", got)
	}

	if n, _ := s.CountSemantic(ctx, "m1"); n != 3 {
		t.Fatalf("count %d", n)
	}
	if n, err := s.PurgeSemantic(ctx, "m1"); err != nil || n != 1 {
		t.Fatalf("purge %d %v", n, err)
	}
	if n, _ := s.CountSemantic(ctx, "old"); n != 0 {
		t.Fatal("old model kept")
	}
	// Deleting an event drops its vector.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountSemantic(ctx, "m1"); n != 2 {
		t.Fatalf("cascade: %d", n)
	}
}
