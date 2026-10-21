package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFacesAndPeopleStore(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	ev := func(cam string, at int64) *Event {
		e := &Event{CameraID: cam, Kind: "Person", Score: 0.9, CreatedAt: at, Box: "[0.1,0.1,0.5,0.9]", Thumbnail: "t.jpg"}
		if err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	face := func(e *Event, score float64, person string) *Face {
		f := &Face{EventID: e.ID, CameraID: e.CameraID, AtMs: e.CreatedAt * 1000, Box: "[0.2,0.2,0.4,0.5]", Score: score,
			Model: "sha256:m", Vec: []byte{1, 2, 3, 4}, PersonID: person}
		if err := s.AddFace(ctx, f, []byte("jpeg")); err != nil {
			t.Fatal(err)
		}
		return f
	}
	e1, e2, e3 := ev("c1", 100), ev("c2", 200), ev("c1", 300)

	ann := &Person{Name: "  Ann  "}
	if err := s.AddPerson(ctx, ann); err != nil {
		t.Fatal(err)
	}
	if ann.Name != "Ann" || ann.Alert != PersonAlertDefault || ann.CreatedAt == 0 {
		t.Fatalf("person defaults: %+v", ann)
	}
	bob := &Person{Name: "bob", Alert: PersonAlertAlways}
	_ = s.AddPerson(ctx, bob)

	fa := face(e1, 0.9, ann.ID)
	fb := face(e1, 0.95, "")
	fc := face(e2, 0.8, "")
	fd := face(e3, 0.7, ann.ID)
	fd2 := &Face{EventID: e3.ID, CameraID: "c1", AtMs: 1, Model: "sha256:other", Vec: []byte{9, 9, 9, 9}, PersonID: bob.ID}
	_ = s.AddFace(ctx, fd2, nil)

	if list, _ := s.EventFaces(ctx, e1.ID); len(list) != 2 || list[0].ID != fb.ID {
		t.Fatalf("event faces best first: %+v", list)
	}
	if list, _ := s.UnassignedFaces(ctx, 10); len(list) != 2 || list[0].ID != fc.ID {
		t.Fatalf("unassigned newest first: %+v", list)
	}
	if list, _ := s.UnassignedFaces(ctx, 1); len(list) != 1 {
		t.Fatal("limit")
	}
	if n, _ := s.CountUnassignedFaces(ctx); n != 2 {
		t.Fatalf("unassigned count %d", n)
	}
	if list, _ := s.PersonFaces(ctx, ann.ID); len(list) != 2 || list[0].ID != fd.ID {
		t.Fatalf("person faces: %+v", list)
	}
	if g, _ := s.Gallery(ctx, "sha256:m"); len(g) != 2 || g[0].PersonID != ann.ID {
		t.Fatalf("gallery of one model: %+v", g)
	}
	crop, cam, err := s.FaceCrop(ctx, fa.ID)
	if err != nil || string(crop) != "jpeg" || cam != "c1" {
		t.Fatalf("crop %q %q %v", crop, cam, err)
	}
	if _, _, err := s.FaceCrop(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing crop")
	}

	// People by name, with last seen.
	people, _ := s.People(ctx)
	if len(people) != 2 || people[0].Name != "Ann" || people[1].Name != "bob" {
		t.Fatalf("people by name: %+v", people)
	}
	if people[0].LastSeenAt != 300_000 || people[0].LastSeenCameraID != "c1" || people[1].LastSeenAt != 1 {
		t.Fatalf("last seen: %+v", people)
	}

	// Assign: only unassigned faces, only to people who exist.
	if err := s.AssignFace(ctx, fc.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignFace(ctx, fc.ID, ann.ID); !errors.Is(err, ErrFaceAssigned) {
		t.Fatalf("reassign: %v", err)
	}
	if err := s.AssignFace(ctx, "nope", ann.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing face: %v", err)
	}
	if err := s.AssignFace(ctx, fb.ID, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing person: %v", err)
	}

	// Event person, rename.
	if err := s.SetEventPerson(ctx, e1.ID, ann.ID, "Ann"); err != nil {
		t.Fatal(err)
	}
	_ = s.SetEventPerson(ctx, e3.ID, ann.ID, "Ann")
	if n, err := s.RenamePersonEvents(ctx, ann.ID, "Anne"); err != nil || n != 2 {
		t.Fatalf("rename: %d %v", n, err)
	}
	if e, _ := s.Event(ctx, e1.ID); e.PersonID != ann.ID || e.PersonName != "Anne" {
		t.Fatalf("event person: %+v", e)
	}
	ann.Name, ann.Alert = "Anne", PersonAlertNever
	if err := s.UpdatePerson(ctx, ann); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Person(ctx, ann.ID); p.Name != "Anne" || p.Alert != PersonAlertNever {
		t.Fatalf("update: %+v", p)
	}
	if err := s.UpdatePerson(ctx, &Person{ID: "nope", Name: "x", Alert: "default"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("update missing")
	}

	// Purge: unassigned only, older than the cutoff.
	old := face(ev("c1", 10), 0.5, "")
	if n, err := s.PurgeUnassignedFaces(ctx, 50_000); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if _, err := s.Face(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old unassigned face kept")
	}
	if _, err := s.Face(ctx, fb.ID); err != nil {
		t.Fatal("newer unassigned face purged")
	}

	// Deleting a person deletes their faces; events keep the name.
	if err := s.DeletePerson(ctx, ann.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.PersonFaces(ctx, ann.ID); len(list) != 0 {
		t.Fatal("faces left after person delete")
	}
	if e, _ := s.Event(ctx, e1.ID); e.PersonName != "Anne" {
		t.Fatal("event lost the name")
	}
	if err := s.DeletePerson(ctx, ann.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete twice")
	}
	if err := s.DeleteFace(ctx, fb.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFace(ctx, fb.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete face twice")
	}

	// Faces go with their event.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, e2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Face(ctx, fc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("face outlived its event")
	}
	if n, _ := s.CountPeople(ctx); n != 1 {
		t.Fatalf("people %d", n)
	}
}

func TestSetPersonAlarmText(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, _, err := s.RaiseAlarm(ctx, AlarmInput{CameraID: "c1", CameraName: "Front", Kind: "Person", Title: "Person detected",
		Severity: "Critical", Detail: "Front detected a person.", At: time.Now(), Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetPersonAlarmText(ctx, a.ID, "Ann detected", "Ann at Front")
	if err != nil || got.Title != "Ann detected" || got.Detail != "Ann at Front" || got.Severity != "Critical" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.SetPersonAlarmText(ctx, "nope", "x", "y"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing alarm")
	}
	if !ValidPersonAlert("never") || ValidPersonAlert("") || ValidPersonAlert("sometimes") {
		t.Fatal("ValidPersonAlert")
	}
}
