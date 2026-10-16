package store

import (
	"context"
	"errors"
	"testing"
)

func TestViewGroupsCRUD(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	alice, _ := st.CreateUser(ctx, "alice", "Admin", "x")

	a, err := st.SaveViewGroup(ctx, "", "", "Site A", 2)
	if err != nil || a.Name != "Site A" || a.SortOrder != 2 {
		t.Fatalf("create: %+v %v", a, err)
	}
	b, err := st.SaveViewGroup(ctx, "", a.ID, "Floor 1", 0)
	if err != nil || b.ParentID != a.ID {
		t.Fatalf("nested: %+v %v", b, err)
	}
	if _, err := st.SaveViewGroup(ctx, "", "nope", "X", 0); !errors.Is(err, ErrViewGroupParent) {
		t.Fatalf("missing parent: %v", err)
	}
	// A folder can't move under itself or its own child.
	if _, err := st.SaveViewGroup(ctx, a.ID, b.ID, "Site A", 0); !errors.Is(err, ErrViewGroupParent) {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := st.SaveViewGroup(ctx, a.ID, a.ID, "Site A", 0); !errors.Is(err, ErrViewGroupParent) {
		t.Fatalf("self parent: %v", err)
	}
	if _, err := st.SaveViewGroup(ctx, "missing", "", "X", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	// Depth is bounded.
	parent := ""
	var depthErr error
	for i := 0; i < maxViewGroupDepth+2; i++ {
		var g *ViewGroup
		g, depthErr = st.SaveViewGroup(ctx, "", parent, "deep", 0)
		if depthErr != nil {
			break
		}
		parent = g.ID
	}
	if !errors.Is(depthErr, ErrViewGroupParent) {
		t.Fatalf("depth not bounded: %v", depthErr)
	}

	// Delete is refused until the folder is empty.
	if err := st.DeleteViewGroup(ctx, a.ID); !errors.Is(err, ErrViewGroupNotEmpty) {
		t.Fatalf("delete with child: %v", err)
	}
	v, err := st.SaveViewWrite(ctx, alice.ID, "", ViewWrite{Name: "Lobby", Layout: "{}", GroupID: b.ID, Shared: true, SortOrder: 1})
	if err != nil || !v.Shared || v.GroupID != b.ID || v.SortOrder != 1 {
		t.Fatalf("shared view: %+v %v", v, err)
	}
	if err := st.DeleteViewGroup(ctx, b.ID); !errors.Is(err, ErrViewGroupNotEmpty) {
		t.Fatalf("delete with view: %v", err)
	}
	if _, err := st.SaveViewWrite(ctx, alice.ID, "", ViewWrite{Name: "X", Layout: "{}", GroupID: "nope", Shared: true}); !errors.Is(err, ErrViewGroupMissing) {
		t.Fatalf("view in missing folder: %v", err)
	}
	if err := st.DeleteViewByID(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteViewGroup(ctx, b.ID); err != nil {
		t.Fatalf("delete empty: %v", err)
	}
	if err := st.DeleteViewGroup(ctx, a.ID); err != nil {
		t.Fatalf("delete parent after child: %v", err)
	}
	if err := st.DeleteViewGroup(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestSharedAndPrivateViews(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	alice, _ := st.CreateUser(ctx, "alice", "Admin", "x")
	bob, _ := st.CreateUser(ctx, "bob", "Operator", "x")
	priv, _ := st.SaveView(ctx, bob.ID, "", "Mine", "{}")
	if priv.Shared || priv.GroupID != "" {
		t.Fatalf("a private view: %+v", priv)
	}
	s2, _ := st.SaveViewWrite(ctx, alice.ID, "", ViewWrite{Name: "Wall", Layout: "{}", Shared: true})
	s1, _ := st.SaveViewWrite(ctx, alice.ID, "", ViewWrite{Name: "Zed", Layout: "{}", Shared: true, SortOrder: -1})
	shared, _ := st.SharedViews(ctx)
	if len(shared) != 2 || shared[0].ID != s1.ID || shared[1].ID != s2.ID {
		t.Fatalf("shared order: %+v", shared)
	}
	if p, _ := st.PrivateViews(ctx, alice.ID); len(p) != 0 {
		t.Fatalf("alice's shared views listed as private: %+v", p)
	}
	if p, _ := st.PrivateViews(ctx, bob.ID); len(p) != 1 {
		t.Fatalf("bob's private views: %+v", p)
	}
	// Any owner's view is reachable by id, and updating keeps the owner.
	u, err := st.SaveViewWrite(ctx, bob.ID, s2.ID, ViewWrite{Name: "Wall 2", Layout: "{}", Shared: true})
	if err != nil || u.UserID != alice.ID || u.Name != "Wall 2" {
		t.Fatalf("update by id: %+v %v", u, err)
	}
}
