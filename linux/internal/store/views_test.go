package store

import (
	"context"
	"testing"
)

func TestSavedViewsPerUser(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	alice, _ := st.CreateUser(ctx, "alice", "Operator", "x")
	bob, _ := st.CreateUser(ctx, "bob", "Operator", "x")

	v, err := st.SaveView(ctx, alice.ID, "", "Lobby", `{"columns":2}`)
	if err != nil || v.Name != "Lobby" || v.Layout != `{"columns":2}` || v.CreatedAt == 0 || v.UpdatedAt != v.CreatedAt {
		t.Fatalf("create: %+v %v", v, err)
	}
	if _, err := st.SaveView(ctx, bob.ID, v.ID, "Mine", "{}"); err != ErrNotFound {
		t.Fatalf("bob replaces alice's view: %v", err)
	}
	if _, err := st.SavedView(ctx, bob.ID, v.ID); err != ErrNotFound {
		t.Fatalf("bob reads alice's view: %v", err)
	}
	if err := st.DeleteSavedView(ctx, bob.ID, v.ID); err != ErrNotFound {
		t.Fatalf("bob deletes alice's view: %v", err)
	}
	if v2, err := st.SaveView(ctx, alice.ID, v.ID, "Lobby 2", "{}"); err != nil || v2.Name != "Lobby 2" || v2.ID != v.ID {
		t.Fatalf("replace: %+v %v", v2, err)
	}
	_, _ = st.SaveView(ctx, alice.ID, "", "atrium", "{}")
	list, _ := st.SavedViews(ctx, alice.ID)
	if len(list) != 2 || list[0].Name != "atrium" {
		t.Fatalf("list %+v", list)
	}
	if list, _ := st.SavedViews(ctx, bob.ID); len(list) != 0 {
		t.Fatalf("bob's list %+v", list)
	}
	if err := st.DeleteSavedView(ctx, alice.ID, v.ID); err != nil {
		t.Fatal(err)
	}
	// Deleting the user deletes their views.
	if err := st.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM saved_views`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d views left after deleting the user", n)
	}
}

func TestEvidenceRecorder(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ev := &Evidence{ID: NewID(), CaseID: "HG-1", CameraID: "c", CameraName: "C", Title: "t", Range: "r", File: "f.mp4", SHA256: "x", LockedAt: 1, LockedBy: "u"}
	if err := st.AddEvidenceOn(ctx, ev, "rs2"); err != nil {
		t.Fatal(err)
	}
	if id, err := st.EvidenceRecorderID(ctx, ev.ID); err != nil || id != "rs2" {
		t.Fatalf("recorder %q %v", id, err)
	}
	old := &Evidence{ID: NewID(), CaseID: "HG-2", CameraID: "c", CameraName: "C", Title: "t", Range: "r", File: "g.mp4", SHA256: "x", LockedAt: 1, LockedBy: "u"}
	_ = st.AddEvidence(ctx, old) // filed before recording servers had ids
	if id, _ := st.EvidenceRecorderID(ctx, old.ID); id != LocalRecorderID {
		t.Fatalf("legacy evidence on %q", id)
	}
	if _, err := st.EvidenceRecorderID(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
}
