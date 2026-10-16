package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Floorplans (WAVE3-OPS.md, Stream B): site maps an Admin uploads, with
// cameras pinned on them. The image stays in SQLite (floorplans are few and
// small). Pin x/y are normalized 0..1 of the image; heading is degrees,
// -1 = none.

type Floorplan struct {
	ID        string
	Name      string
	Mime      string
	Width     int
	Height    int
	SortOrder int
	CreatedAt int64
	UpdatedAt int64
	Image     []byte // only from FloorplanImage / SaveFloorplan input
}

type Pin struct {
	CameraID string
	X, Y     float64
	Heading  float64
}

const floorplanCols = `id, name, mime, width, height, sort_order, created_at, updated_at`

func scanFloorplan(row interface{ Scan(...any) error }) (*Floorplan, error) {
	f := &Floorplan{}
	err := row.Scan(&f.ID, &f.Name, &f.Mime, &f.Width, &f.Height, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// Floorplans lists the plans (without image bytes) by sort order, then name.
func (s *Store) Floorplans(ctx context.Context) ([]Floorplan, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+floorplanCols+` FROM floorplans ORDER BY sort_order, name COLLATE NOCASE, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Floorplan{}
	for rows.Next() {
		f, err := scanFloorplan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// Floorplan returns one plan without its image.
func (s *Store) Floorplan(ctx context.Context, id string) (*Floorplan, error) {
	return scanFloorplan(s.DB.QueryRowContext(ctx, `SELECT `+floorplanCols+` FROM floorplans WHERE id = ?`, id))
}

// FloorplanImage returns a plan with its image bytes.
func (s *Store) FloorplanImage(ctx context.Context, id string) (*Floorplan, error) {
	f := &Floorplan{}
	err := s.DB.QueryRowContext(ctx, `SELECT `+floorplanCols+`, image FROM floorplans WHERE id = ?`, id).
		Scan(&f.ID, &f.Name, &f.Mime, &f.Width, &f.Height, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt, &f.Image)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// SaveFloorplan creates a plan (f.ID "": image required, new ID) or updates
// the name and sort order of an existing one (image untouched). It returns
// the stored plan without image bytes.
func (s *Store) SaveFloorplan(ctx context.Context, f Floorplan) (*Floorplan, error) {
	f.Name = strings.TrimSpace(f.Name)
	t := now()
	if f.ID == "" {
		f.ID = NewID()
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO floorplans(id, name, image, mime, width, height, sort_order, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			f.ID, f.Name, f.Image, f.Mime, f.Width, f.Height, f.SortOrder, t, t); err != nil {
			return nil, err
		}
	} else {
		res, err := s.DB.ExecContext(ctx, `UPDATE floorplans SET name = ?, sort_order = ?, updated_at = ? WHERE id = ?`, f.Name, f.SortOrder, t, f.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.Floorplan(ctx, f.ID)
}

// DeleteFloorplan removes a plan; its pins go with it.
func (s *Store) DeleteFloorplan(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM floorplans WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Pins lists a plan's pins.
func (s *Store) Pins(ctx context.Context, planID string) ([]Pin, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT camera_id, x, y, heading FROM floorplan_pins WHERE floorplan_id = ? ORDER BY camera_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pin{}
	for rows.Next() {
		var p Pin
		if err := rows.Scan(&p.CameraID, &p.X, &p.Y, &p.Heading); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPins replaces all of a plan's pins. The caller validates the cameras.
func (s *Store) SetPins(ctx context.Context, planID string, pins []Pin) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM floorplans WHERE id = ?`, planID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM floorplan_pins WHERE floorplan_id = ?`, planID); err != nil {
		return err
	}
	for _, p := range pins {
		if _, err := tx.ExecContext(ctx, `INSERT INTO floorplan_pins(floorplan_id, camera_id, x, y, heading) VALUES(?,?,?,?,?)`, planID, p.CameraID, p.X, p.Y, p.Heading); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE floorplans SET updated_at = ? WHERE id = ?`, now(), planID); err != nil {
		return err
	}
	return tx.Commit()
}
