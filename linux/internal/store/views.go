package store

import (
	"context"
	"database/sql"
	"errors"
)

// Saved views (api/openapi.yaml, "views"): a user's named camera layouts.
// The layout (grid size and tiles) is stored as the API's JSON; the store
// doesn't look inside it.

type SavedView struct {
	ID        string
	UserID    string
	Name      string
	Layout    string // JSON
	CreatedAt int64
	UpdatedAt int64
}

const savedViewCols = `id, user_id, name, layout, created_at, updated_at`

func scanSavedView(row interface{ Scan(...any) error }) (*SavedView, error) {
	v := &SavedView{}
	err := row.Scan(&v.ID, &v.UserID, &v.Name, &v.Layout, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// SavedViews lists a user's views by name.
func (s *Store) SavedViews(ctx context.Context, userID string) ([]SavedView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+savedViewCols+` FROM saved_views WHERE user_id = ? ORDER BY name COLLATE NOCASE, created_at`, userID)
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

// SavedView returns one of a user's views; another user's is ErrNotFound.
func (s *Store) SavedView(ctx context.Context, userID, id string) (*SavedView, error) {
	return scanSavedView(s.DB.QueryRowContext(ctx, `SELECT `+savedViewCols+` FROM saved_views WHERE id = ? AND user_id = ?`, id, userID))
}

// SaveView creates (id "") or replaces one of a user's views.
func (s *Store) SaveView(ctx context.Context, userID, id, name, layout string) (*SavedView, error) {
	t := now()
	if id == "" {
		id = NewID()
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO saved_views(`+savedViewCols+`) VALUES(?,?,?,?,?,?)`, id, userID, name, layout, t, t); err != nil {
			return nil, err
		}
	} else {
		res, err := s.DB.ExecContext(ctx, `UPDATE saved_views SET name = ?, layout = ?, updated_at = ? WHERE id = ? AND user_id = ?`, name, layout, t, id, userID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.SavedView(ctx, userID, id)
}

// DeleteSavedView removes one of a user's views.
func (s *Store) DeleteSavedView(ctx context.Context, userID, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM saved_views WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
