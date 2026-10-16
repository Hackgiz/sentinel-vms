package store

import (
	"context"
	"database/sql"
	"errors"
)

// Shared views and their folders (view_groups). A shared view is visible to
// every user and kept by anyone who may manage views; a private view
// (views.go) belongs to one user. Folders only hold shared views and other
// folders; the handler enforces who may write them.

var (
	// ErrViewGroupParent: the parent folder doesn't exist, moving the
	// folder there would make it its own ancestor, or the tree would get
	// too deep.
	ErrViewGroupParent = errors.New("invalid parent folder")
	// ErrViewGroupNotEmpty: a folder with folders or views in it isn't deleted.
	ErrViewGroupNotEmpty = errors.New("folder is not empty")
	// ErrViewGroupMissing: a view was placed in a folder that doesn't exist.
	ErrViewGroupMissing = errors.New("no such folder")
)

// maxViewGroupDepth bounds folder nesting (the tree stays readable).
const maxViewGroupDepth = 6

type ViewGroup struct {
	ID        string
	ParentID  string // "" = top level of the Shared root
	Name      string
	SortOrder int
}

// ViewGroups lists every folder, by sort order then name.
func (s *Store) ViewGroups(ctx context.Context) ([]ViewGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, parent_id, name, sort_order FROM view_groups ORDER BY sort_order, name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ViewGroup{}
	for rows.Next() {
		var g ViewGroup
		if err := rows.Scan(&g.ID, &g.ParentID, &g.Name, &g.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ViewGroup returns one folder.
func (s *Store) ViewGroup(ctx context.Context, id string) (*ViewGroup, error) {
	var g ViewGroup
	err := s.DB.QueryRowContext(ctx, `SELECT id, parent_id, name, sort_order FROM view_groups WHERE id = ?`, id).Scan(&g.ID, &g.ParentID, &g.Name, &g.SortOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}

// SaveViewGroup creates (id "") or updates a folder. The parent must exist,
// must not be the folder or below it, and the tree stays at most
// maxViewGroupDepth deep (counting only the chain above; folders already
// below an updated one are not re-measured).
func (s *Store) SaveViewGroup(ctx context.Context, id, parentID, name string, sortOrder int) (*ViewGroup, error) {
	all, err := s.ViewGroups(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ViewGroup, len(all))
	for _, g := range all {
		byID[g.ID] = g
	}
	if id != "" {
		if _, ok := byID[id]; !ok {
			return nil, ErrNotFound
		}
	}
	depth := 1 // this folder
	for p := parentID; p != ""; depth++ {
		g, ok := byID[p]
		if !ok || p == id || depth >= maxViewGroupDepth {
			return nil, ErrViewGroupParent
		}
		p = g.ParentID
	}
	if id == "" {
		id = NewID()
		_, err = s.DB.ExecContext(ctx, `INSERT INTO view_groups(id, parent_id, name, sort_order) VALUES(?,?,?,?)`, id, parentID, name, sortOrder)
	} else {
		_, err = s.DB.ExecContext(ctx, `UPDATE view_groups SET parent_id = ?, name = ?, sort_order = ? WHERE id = ?`, parentID, name, sortOrder, id)
	}
	if err != nil {
		return nil, err
	}
	return s.ViewGroup(ctx, id)
}

// DeleteViewGroup removes an empty folder: ErrViewGroupNotEmpty while it
// still holds folders or views.
func (s *Store) DeleteViewGroup(ctx context.Context, id string) error {
	if _, err := s.ViewGroup(ctx, id); err != nil {
		return err
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM view_groups WHERE parent_id = ?) + (SELECT COUNT(*) FROM saved_views WHERE group_id = ?)`, id, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrViewGroupNotEmpty
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM view_groups WHERE id = ?`, id)
	return err
}

// SharedViews lists every shared view, by sort order then name.
func (s *Store) SharedViews(ctx context.Context) ([]SavedView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+savedViewSelect+` FROM saved_views WHERE shared = 1 ORDER BY sort_order, name COLLATE NOCASE, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedView{}
	for rows.Next() {
		v, err := scanSavedView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// PrivateViews lists a user's views that aren't shared, by name.
func (s *Store) PrivateViews(ctx context.Context, userID string) ([]SavedView, error) {
	all, err := s.SavedViews(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, v := range all {
		if !v.Shared {
			out = append(out, v)
		}
	}
	return out, nil
}

// ViewByID returns any view, whoever owns it; the caller decides whether
// the viewer may see or change it.
func (s *Store) ViewByID(ctx context.Context, id string) (*SavedView, error) {
	return scanSavedView(s.DB.QueryRowContext(ctx, `SELECT `+savedViewSelect+` FROM saved_views WHERE id = ?`, id))
}

// ViewWrite is what a view save sets.
type ViewWrite struct {
	Name      string
	Layout    string
	GroupID   string
	Shared    bool
	SortOrder int
}

// SaveViewWrite creates (id "", owned by userID) or updates (any owner)
// a view, including its folder and shared flag.
func (s *Store) SaveViewWrite(ctx context.Context, userID, id string, w ViewWrite) (*SavedView, error) {
	if w.GroupID != "" {
		if _, err := s.ViewGroup(ctx, w.GroupID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, ErrViewGroupMissing
			}
			return nil, err
		}
	}
	shared := 0
	if w.Shared {
		shared = 1
	}
	t := now()
	if id == "" {
		id = NewID()
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO saved_views(`+savedViewCols+`, group_id, shared, sort_order) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, userID, w.Name, w.Layout, t, t, w.GroupID, shared, w.SortOrder); err != nil {
			return nil, err
		}
	} else {
		res, err := s.DB.ExecContext(ctx, `UPDATE saved_views SET name = ?, layout = ?, group_id = ?, shared = ?, sort_order = ?, updated_at = ? WHERE id = ?`,
			w.Name, w.Layout, w.GroupID, shared, w.SortOrder, t, id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.ViewByID(ctx, id)
}

// DeleteViewByID removes any view.
func (s *Store) DeleteViewByID(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM saved_views WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
