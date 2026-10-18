package store

// Wall layouts (WAVE10-APPLE.md, Stream A): a named arrangement of the
// Mac operator app's monitor windows across displays. Body is the JSON the
// API validates (internal/server/wall_layouts.go); the store keeps it as
// text. A layout is private to its creator unless shared.

import (
	"context"
	"database/sql"
	"errors"
)

type WallLayout struct {
	ID        string
	Name      string
	Body      string // JSON: {"monitors":[...]}
	Shared    bool
	CreatedBy string // user ID
	CreatedAt int64  // Unix seconds
	UpdatedAt int64
}

const wallLayoutCols = `id, name, body, shared, created_by, created_at, updated_at`

func scanWallLayout(row interface{ Scan(...any) error }) (*WallLayout, error) {
	l := &WallLayout{}
	var shared int
	err := row.Scan(&l.ID, &l.Name, &l.Body, &shared, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	l.Shared = shared != 0
	return l, err
}

// AddWallLayout stores a new layout; ID and the times are filled in.
func (s *Store) AddWallLayout(ctx context.Context, l *WallLayout) error {
	l.ID = NewID()
	l.CreatedAt = now()
	l.UpdatedAt = l.CreatedAt
	_, err := s.DB.ExecContext(ctx, `INSERT INTO wall_layouts(`+wallLayoutCols+`) VALUES(?,?,?,?,?,?,?)`,
		l.ID, l.Name, l.Body, boolInt(l.Shared), l.CreatedBy, l.CreatedAt, l.UpdatedAt)
	return err
}

// WallLayoutByID reads one layout (ErrNotFound if there is none).
func (s *Store) WallLayoutByID(ctx context.Context, id string) (*WallLayout, error) {
	return scanWallLayout(s.DB.QueryRowContext(ctx, `SELECT `+wallLayoutCols+` FROM wall_layouts WHERE id = ?`, id))
}

// WallLayoutsFor lists the layouts a user may see: their own and every
// shared one, by name.
func (s *Store) WallLayoutsFor(ctx context.Context, userID string) ([]WallLayout, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+wallLayoutCols+` FROM wall_layouts
		WHERE created_by = ? OR shared = 1 ORDER BY name COLLATE NOCASE, created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WallLayout{}
	for rows.Next() {
		l, err := scanWallLayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// UpdateWallLayout writes name, body and shared; UpdatedAt is refreshed.
func (s *Store) UpdateWallLayout(ctx context.Context, l *WallLayout) error {
	l.UpdatedAt = now()
	res, err := s.DB.ExecContext(ctx, `UPDATE wall_layouts SET name = ?, body = ?, shared = ?, updated_at = ? WHERE id = ?`,
		l.Name, l.Body, boolInt(l.Shared), l.UpdatedAt, l.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWallLayout removes a layout (ErrNotFound if there is none).
func (s *Store) DeleteWallLayout(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM wall_layouts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
