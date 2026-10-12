package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Camera groups and user scopes (PLATFORM.md, "Permissions"): a user's
// role applies to every camera, or only to the cameras in their groups.
// Admins always see every camera.

type CameraGroup struct {
	ID        string
	Name      string
	CameraIDs []string
	CreatedAt int64
}

// ErrDuplicateName: another group already has that name.
var ErrDuplicateName = errors.New("that name is already taken")

func (s *Store) CameraGroups(ctx context.Context) ([]CameraGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT g.id, g.name, g.created_at, COALESCE(m.camera_id, '')
		FROM camera_groups g LEFT JOIN camera_group_members m ON m.group_id = g.id ORDER BY g.name COLLATE NOCASE, g.id, m.camera_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CameraGroup
	for rows.Next() {
		var id, name, cam string
		var created int64
		if err := rows.Scan(&id, &name, &created, &cam); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != id {
			out = append(out, CameraGroup{ID: id, Name: name, CreatedAt: created, CameraIDs: []string{}})
		}
		if cam != "" {
			out[len(out)-1].CameraIDs = append(out[len(out)-1].CameraIDs, cam)
		}
	}
	return out, rows.Err()
}

func (s *Store) CameraGroup(ctx context.Context, id string) (*CameraGroup, error) {
	groups, err := s.CameraGroups(ctx)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i], nil
		}
	}
	return nil, ErrNotFound
}

// SaveCameraGroup creates (id "") or replaces a group and its members.
// Unknown camera ids are an error.
func (s *Store) SaveCameraGroup(ctx context.Context, id, name string, cameraIDs []string) (*CameraGroup, error) {
	name = strings.TrimSpace(name)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if id == "" {
		id = NewID()
		_, err = tx.ExecContext(ctx, `INSERT INTO camera_groups(id, name, created_at) VALUES(?,?,?)`, id, name, now())
	} else {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE camera_groups SET name = ? WHERE id = ?`, name, id)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				return nil, ErrNotFound
			}
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateName
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM camera_group_members WHERE group_id = ?`, id); err != nil {
		return nil, err
	}
	for _, c := range cameraIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO camera_group_members(group_id, camera_id) VALUES(?,?)`, id, c); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return nil, fmt.Errorf("no camera %s", c)
			}
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.CameraGroup(ctx, id)
}

func (s *Store) DeleteCameraGroup(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM camera_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CameraGroupIDs lists the groups a camera is in.
func (s *Store) CameraGroupIDs(ctx context.Context, cameraID string) ([]string, error) {
	return s.strings(ctx, `SELECT group_id FROM camera_group_members WHERE camera_id = ? ORDER BY group_id`, cameraID)
}

// SetCameraGroups replaces the groups a camera is in.
func (s *Store) SetCameraGroups(ctx context.Context, cameraID string, groupIDs []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM camera_group_members WHERE camera_id = ?`, cameraID); err != nil {
		return err
	}
	for _, g := range groupIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO camera_group_members(group_id, camera_id) VALUES(?,?)`, g, cameraID); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return fmt.Errorf("no group %s", g)
			}
			return err
		}
	}
	return tx.Commit()
}

// UserScope is which cameras a user's role applies to.
type UserScope struct {
	AllCameras bool
	GroupIDs   []string
}

func (s *Store) UserScope(ctx context.Context, userID string) (UserScope, error) {
	var all int
	err := s.DB.QueryRowContext(ctx, `SELECT all_cameras FROM users WHERE id = ?`, userID).Scan(&all)
	if errors.Is(err, sql.ErrNoRows) {
		return UserScope{}, ErrNotFound
	}
	if err != nil {
		return UserScope{}, err
	}
	ids, err := s.strings(ctx, `SELECT group_id FROM user_group_scopes WHERE user_id = ? ORDER BY group_id`, userID)
	return UserScope{AllCameras: all == 1, GroupIDs: ids}, err
}

// SetUserScope replaces a user's scope. Unknown group ids are an error.
func (s *Store) SetUserScope(ctx context.Context, userID string, sc UserScope) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET all_cameras = ? WHERE id = ?`, boolInt(sc.AllCameras), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_scopes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, g := range sc.GroupIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_group_scopes(user_id, group_id) VALUES(?,?)`, userID, g); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return fmt.Errorf("no group %s", g)
			}
			return err
		}
	}
	return tx.Commit()
}

// VisibleCameraIDs is the set of cameras a user may see; all is true when
// the user isn't limited (then the set is nil).
func (s *Store) VisibleCameraIDs(ctx context.Context, u *User) (ids map[string]bool, all bool, err error) {
	if u.Role == "Admin" {
		return nil, true, nil
	}
	sc, err := s.UserScope(ctx, u.ID)
	if err != nil || sc.AllCameras {
		return nil, err == nil, err
	}
	list, err := s.strings(ctx, `SELECT DISTINCT m.camera_id FROM user_group_scopes u JOIN camera_group_members m ON m.group_id = u.group_id WHERE u.user_id = ?`, u.ID)
	if err != nil {
		return nil, false, err
	}
	ids = make(map[string]bool, len(list))
	for _, id := range list {
		ids[id] = true
	}
	return ids, false, nil
}

func (s *Store) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
