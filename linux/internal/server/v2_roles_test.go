package server

import (
	"context"
	"strings"
	"testing"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func TestRolesSeedIdempotentAndBuiltinSets(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	// Tamper with a seeded row; re-seeding must not overwrite it.
	if _, err := s.Store.DB.Exec(`UPDATE roles SET description = 'mine' WHERE id = 'viewer'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Store.SeedRoles(ctx); err != nil {
			t.Fatal(err)
		}
	}
	roles, err := s.Store.Roles(ctx)
	if err != nil || len(roles) != 4 {
		t.Fatalf("roles = %v, %v", roles, err)
	}
	for _, r := range roles {
		if !r.Builtin {
			t.Errorf("%s not builtin", r.Name)
		}
		want := strings.Join(auth.BuiltinPermissions(r.Name), ",")
		if got := strings.Join(r.Permissions, ","); got != want {
			t.Errorf("%s perms = %s, want %s", r.Name, got, want)
		}
		if r.Name == "Viewer" && r.Description != "mine" {
			t.Errorf("seed overwrote existing row")
		}
	}
}

func TestRoleResolverFallbackAndUnknown(t *testing.T) {
	s := newCompanionServer(t)
	if !s.can("Admin", auth.ManageUsers) || s.can("Viewer", auth.ManageUsers) || !s.can("Operator", auth.ControlPTZ) {
		t.Fatal("builtin fallback wrong")
	}
	if s.can("Ghost", auth.ViewLive) {
		t.Fatal("unknown role has permissions")
	}
	// Builtin names resolve by auth.Can even if the row is gone.
	if _, err := s.Store.DB.Exec(`DELETE FROM roles`); err != nil {
		t.Fatal(err)
	}
	if !s.can("Supervisor", auth.ViewAudit) {
		t.Fatal("builtin fallback needs no row")
	}
}

func TestRolesCRUDAndGating(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	viewer := v2Token(t, s, "vw", "Viewer")

	if r := do(t, h, "GET", "/api/v2/roles", viewer, ""); r.Code != 403 {
		t.Fatalf("viewer roles: %d", r.Code)
	}
	code, m := v2Call(t, h, "GET", "/api/v2/permissions", admin, "")
	if code != 200 || len(items(t, m)) != len(auth.Catalog()) {
		t.Fatalf("permissions: %d %v", code, m)
	}
	code, m = v2Call(t, h, "GET", "/api/v2/roles", admin, "")
	if code != 200 || len(items(t, m)) != 4 {
		t.Fatalf("roles: %d %v", code, m)
	}

	// Validation.
	if code, _ := v2Call(t, h, "POST", "/api/v2/roles", admin, `{"name":"X","permissions":["bogus"]}`); code != 400 {
		t.Fatalf("bogus permission: %d", code)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/roles", admin, `{"name":"admin","permissions":[]}`); code != 409 {
		t.Fatalf("duplicate of builtin: %d", code)
	}

	// A custom role that may only manage cameras.
	code, m = v2Call(t, h, "POST", "/api/v2/roles", admin, `{"name":"Installer","description":"cams","permissions":["manageCameras","viewLive"]}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["id"].(string)
	inst := v2Token(t, s, "ian", "Installer")
	if r := do(t, h, "GET", "/api/v2/camera-groups", inst, ""); r.Code != 200 { // ViewLive
		t.Fatalf("installer viewLive: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/camera-groups", inst, `{"name":"g1","cameraIds":[]}`); r.Code != 201 { // ManageCameras
		t.Fatalf("installer manageCameras: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "GET", "/api/v2/roles", inst, ""); r.Code != 403 {
		t.Fatalf("installer manageUsers: %d", r.Code)
	}

	// Editing takes effect immediately (cache invalidated).
	if code, m := v2Call(t, h, "PUT", "/api/v2/roles/"+id, admin, `{"name":"Installer","permissions":["viewLive","manageUsers"]}`); code != 200 {
		t.Fatalf("update: %d %v", code, m)
	}
	if r := do(t, h, "GET", "/api/v2/roles", inst, ""); r.Code != 200 {
		t.Fatalf("installer after grant: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/camera-groups", inst, `{"name":"g2","cameraIds":[]}`); r.Code != 403 {
		t.Fatalf("installer after revoke: %d", r.Code)
	}

	// Delete is refused while a user holds it, with the count.
	r := do(t, h, "DELETE", "/api/v2/roles/"+id, admin, "")
	if r.Code != 409 || !strings.Contains(r.Body.String(), "1 user") {
		t.Fatalf("delete in use: %d %s", r.Code, r.Body)
	}
	// Users may be assigned the custom role through the v2 users routes.
	if code, m := v2Call(t, h, "POST", "/api/v2/users", admin, `{"name":"zed","role":"Installer","password":"correct horse battery"}`); code != 201 {
		t.Fatalf("create user with custom role: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/users", admin, `{"name":"zoe","role":"Nope","password":"correct horse battery"}`); code != 400 {
		t.Fatalf("unknown role: %d", code)
	}
	// Renaming carries users along.
	if code, _ := v2Call(t, h, "PUT", "/api/v2/roles/"+id, admin, `{"name":"Fitter","permissions":["viewLive"]}`); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	if u, err := s.Store.UserByName(context.Background(), "ian"); err != nil || u.Role != "Fitter" {
		t.Fatalf("user role after rename = %v %v", u, err)
	}
	// Free the role, then delete.
	for _, n := range []string{"ian", "zed"} {
		u, _ := s.Store.UserByName(context.Background(), n)
		_ = s.Store.SetUserRole(context.Background(), u.ID, "Viewer")
	}
	if r := do(t, h, "DELETE", "/api/v2/roles/"+id, admin, ""); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if s.can("Fitter", auth.ViewLive) {
		t.Fatal("deleted role still resolves")
	}
}

func TestBuiltinRolesImmutable(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	if code, _ := v2Call(t, h, "PUT", "/api/v2/roles/viewer", admin, `{"name":"Viewer","permissions":["manageUsers"]}`); code != 409 {
		t.Fatalf("edit builtin: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/roles/operator", admin, ""); code != 409 {
		t.Fatalf("delete builtin: %d", code)
	}
	if _, err := s.Store.DeleteRole(context.Background(), "viewer"); err != store.ErrRoleBuiltin {
		t.Fatalf("store delete builtin: %v", err)
	}
	if s.can("Viewer", auth.ManageUsers) {
		t.Fatal("viewer escalated")
	}
}
