package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
)

// Role is a named permission set. Users reference a role by name
// (users.role); the four built-ins keep their historical names.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"builtin"`
	CreatedAt   int64    `json:"createdAt"`
	UserCount   int      `json:"userCount"`
}

var (
	// ErrRoleBuiltin: built-in roles can't be edited or deleted.
	ErrRoleBuiltin = errors.New("built-in roles can't be changed")
	// ErrRoleInUse: users still hold the role.
	ErrRoleInUse = errors.New("role is in use")
)

var builtinDescriptions = map[string]string{
	"Admin":      "Full control of the system.",
	"Supervisor": "Operates cameras and reviews the audit trail.",
	"Operator":   "Watches live video, handles alarms and exports evidence.",
	"Viewer":     "Watches live video only.",
}

// SeedRoles inserts the four built-ins if absent (run on open). It is
// idempotent and never overwrites an existing row.
func (s *Store) SeedRoles(ctx context.Context) error {
	for _, name := range Roles {
		perms, _ := json.Marshal(auth.BuiltinPermissions(name))
		_, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO roles (id, name, description, permissions, builtin, created_at)
			VALUES (?, ?, ?, ?, 1, ?)`, strings.ToLower(name), name, builtinDescriptions[name], string(perms), time.Now().Unix())
		if err != nil {
			return err
		}
	}
	return nil
}

func scanRole(sc interface{ Scan(...any) error }) (*Role, error) {
	var r Role
	var perms string
	var builtin int
	if err := sc.Scan(&r.ID, &r.Name, &r.Description, &perms, &builtin, &r.CreatedAt, &r.UserCount); err != nil {
		return nil, err
	}
	r.Builtin = builtin != 0
	if err := json.Unmarshal([]byte(perms), &r.Permissions); err != nil || r.Permissions == nil {
		r.Permissions = []string{}
	}
	return &r, nil
}

const roleSelect = `SELECT r.id, r.name, r.description, r.permissions, r.builtin, r.created_at,
	(SELECT COUNT(*) FROM users u WHERE u.role = r.name) FROM roles r`

func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.DB.QueryContext(ctx, roleSelect+` ORDER BY r.builtin DESC, r.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) RoleByID(ctx context.Context, id string) (*Role, error) {
	r, err := scanRole(s.DB.QueryRowContext(ctx, roleSelect+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RoleExists reports whether a role with that exact name exists. The
// built-in names always count, even if their rows were lost.
func (s *Store) RoleExists(ctx context.Context, name string) bool {
	for _, b := range Roles {
		if b == name {
			return true
		}
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// ErrBadRole is a validation failure in a role's name or permissions.
type ErrBadRole struct{ Msg string }

func (e ErrBadRole) Error() string { return e.Msg }

func normalizePerms(perms []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range perms {
		if _, ok := auth.ParsePermission(p); !ok {
			return nil, ErrBadRole{"unknown permission " + p}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) CreateRole(ctx context.Context, name, description string, perms []string) (*Role, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrBadRole{"enter a role name"}
	}
	perms, err := normalizePerms(perms)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(perms)
	id := NewID()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO roles (id, name, description, permissions, builtin, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		id, name, strings.TrimSpace(description), string(b), time.Now().Unix()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateName
		}
		return nil, err
	}
	return s.RoleByID(ctx, id)
}

// UpdateRole edits a custom role. Renaming moves its users along.
func (s *Store) UpdateRole(ctx context.Context, id, name, description string, perms []string) (*Role, error) {
	cur, err := s.RoleByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Builtin {
		return nil, ErrRoleBuiltin
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrBadRole{"enter a role name"}
	}
	perms, err = normalizePerms(perms)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(perms)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE roles SET name = ?, description = ?, permissions = ? WHERE id = ?`,
		name, strings.TrimSpace(description), string(b), id); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateName
		}
		return nil, err
	}
	if name != cur.Name {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE role = ?`, name, cur.Name); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.RoleByID(ctx, id)
}

// DeleteRole removes a custom role; ErrRoleInUse (with the count) when
// users hold it.
func (s *Store) DeleteRole(ctx context.Context, id string) (userCount int, err error) {
	cur, err := s.RoleByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if cur.Builtin {
		return cur.UserCount, ErrRoleBuiltin
	}
	if cur.UserCount > 0 {
		return cur.UserCount, ErrRoleInUse
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	return 0, err
}
