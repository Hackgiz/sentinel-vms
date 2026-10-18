package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEmbeddingsStoreScanPurge(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	add := func(cam, kind string, at int64, box, thumb string) *Event {
		e := &Event{CameraID: cam, Kind: kind, Score: 0.9, CreatedAt: at, Box: box, Thumbnail: thumb}
		if err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	box := "[0.1,0.1,0.5,0.9]"
	a := add("c1", "Person", 100, box, "a.jpg")
	b := add("c1", "Person", 200, box, "b.jpg")
	c := add("c2", "Person", 300, box, "c.jpg")
	v := add("c2", "Vehicle", 400, box, "v.jpg")
	add("c1", "Motion", 500, "", "m.jpg") // no box: never embedded
	add("c1", "Person", 600, box, "")     // no thumbnail yet
	put := func(e *Event, model string) {
		if err := s.PutEmbedding(ctx, Embedding{EventID: e.ID, Model: model, Dim: 2, Vec: []byte{1, 2, 3, 4, 5, 6, 7, 8}, CameraID: e.CameraID, AtMs: e.CreatedAt * 1000}); err != nil {
			t.Fatal(err)
		}
	}

	todo, err := s.EventsToEmbed(ctx, "m1", 50000)
	if err != nil || len(todo) != 4 || todo[0].ID != v.ID || todo[3].ID != a.ID {
		t.Fatalf("to embed: %v %v", todo, err)
	}
	put(a, "m1")
	put(b, "m1")
	put(c, "m1")
	put(v, "old") // an older model's vector does not count
	todo, _ = s.EventsToEmbed(ctx, "m1", 50000)
	if len(todo) != 1 || todo[0].ID != v.ID {
		t.Fatalf("after embedding: %v", todo)
	}
	// The cap looks at the newest events only.
	if todo, _ := s.EventsToEmbed(ctx, "m1", 1); len(todo) != 1 || todo[0].ID != v.ID {
		t.Fatalf("capped: %v", todo)
	}
	if todo, _ := s.EventsToEmbed(ctx, "m2", 2); len(todo) != 2 || todo[0].ID != v.ID || todo[1].ID != c.ID {
		t.Fatalf("capped other model: %v", todo)
	}

	if vec, err := s.EventEmbedding(ctx, a.ID, "m1"); err != nil || len(vec) != 8 {
		t.Fatalf("get: %v %v", vec, err)
	}
	if _, err := s.EventEmbedding(ctx, v.ID, "m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other model must be invisible: %v", err)
	}

	scan := func(f EmbeddingFilter) string {
		var ids []string
		if err := s.ScanEmbeddings(ctx, f, func(id string, _ []byte) { ids = append(ids, id) }); err != nil {
			t.Fatal(err)
		}
		return strings.Join(ids, ",")
	}
	if got := scan(EmbeddingFilter{Model: "m1"}); got != c.ID+","+b.ID+","+a.ID {
		t.Fatalf("all: %s", got)
	}
	if got := scan(EmbeddingFilter{Model: "m1", CameraIDs: []string{"c1"}}); got != b.ID+","+a.ID {
		t.Fatalf("camera: %s", got)
	}
	if got := scan(EmbeddingFilter{Model: "m1", CameraIDs: []string{}}); got != "" {
		t.Fatalf("no cameras: %s", got)
	}
	if got := scan(EmbeddingFilter{Model: "m1", FromMs: 150_000, ToMs: 300_000}); got != b.ID {
		t.Fatalf("window: %s", got)
	}
	if got := scan(EmbeddingFilter{Model: "m1", Exclude: b.ID, Max: 1}); got != c.ID {
		t.Fatalf("exclude+max: %s", got)
	}
	if got := scan(EmbeddingFilter{Model: "old", Kind: "Person"}); got != "" {
		t.Fatalf("kind: %s", got)
	}
	if got := scan(EmbeddingFilter{Model: "old", Kind: "Vehicle"}); got != v.ID {
		t.Fatalf("kind vehicle: %s", got)
	}

	// Replacing keeps one row per event.
	put(a, "m1")
	if n, _ := s.CountEmbeddings(ctx, "m1"); n != 3 {
		t.Fatalf("count = %d", n)
	}
	// A model swap purges the rest.
	if n, err := s.PurgeEmbeddings(ctx, "m1"); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if n, _ := s.CountEmbeddings(ctx, "old"); n != 0 {
		t.Fatalf("old left: %d", n)
	}

	got, err := s.EventsByID(ctx, []string{a.ID, "missing", c.ID})
	if err != nil || len(got) != 2 || got[a.ID] == nil || got[c.ID].CameraID != "c2" {
		t.Fatalf("by id: %v %v", got, err)
	}
}
