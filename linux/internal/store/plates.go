package store

// Licence plates (WAVE16-DETECTION.md, stream D): the watchlist in `plates`
// and the plate read on each vehicle event in `events.plate` (normalized,
// A–Z0–9; "" = none read).

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
)

// Watchlist alert settings (API WatchAlert).
const (
	PlateAlertDefault = "default" // the Vehicle alarm keeps its severity; the name or plate goes into its detail
	PlateAlertNever   = "never"   // a known vehicle: its alarm is closed
	PlateAlertAlways  = "always"  // a Critical Watchlist alarm is raised
)

// ValidPlateAlert reports whether a is a WatchAlert value.
func ValidPlateAlert(a string) bool {
	return a == PlateAlertDefault || a == PlateAlertNever || a == PlateAlertAlways
}

// ErrDuplicatePlate: the plate is already on the watchlist.
var ErrDuplicatePlate = errors.New("that plate is already on the watchlist")

// Plate is one watchlist entry.
type Plate struct {
	ID        string
	Plate     string // normalized
	Name      string
	Notes     string
	Alert     string
	CreatedAt int64 // Unix seconds
}

const plateCols = `id, plate, name, notes, alert, created_at`

func scanPlate(row interface{ Scan(...any) error }) (*Plate, error) {
	p := &Plate{}
	if err := row.Scan(&p.ID, &p.Plate, &p.Name, &p.Notes, &p.Alert, &p.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// Plates lists the watchlist by plate.
func (s *Store) Plates(ctx context.Context) ([]Plate, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+plateCols+` FROM plates ORDER BY plate`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plate{}
	for rows.Next() {
		p, err := scanPlate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PlateByID returns one entry (ErrNotFound).
func (s *Store) PlateByID(ctx context.Context, id string) (*Plate, error) {
	return scanPlate(s.DB.QueryRowContext(ctx, `SELECT `+plateCols+` FROM plates WHERE id = ?`, id))
}

// CountPlates is the watchlist's size.
func (s *Store) CountPlates(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM plates`).Scan(&n)
	return n, err
}

func plateErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrDuplicatePlate
	}
	return err
}

// AddPlate stores a new entry (p.Plate already normalized); it sets ID and
// CreatedAt. ErrDuplicatePlate when the plate is listed.
func (s *Store) AddPlate(ctx context.Context, p *Plate) error {
	p.ID, p.CreatedAt = NewID(), now()
	if p.Alert == "" {
		p.Alert = PlateAlertDefault
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO plates(`+plateCols+`) VALUES(?,?,?,?,?,?)`,
		p.ID, p.Plate, p.Name, p.Notes, p.Alert, p.CreatedAt)
	return plateErr(err)
}

// UpdatePlate saves an entry's plate, name, notes and alert.
func (s *Store) UpdatePlate(ctx context.Context, p *Plate) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE plates SET plate=?, name=?, notes=?, alert=? WHERE id=?`, p.Plate, p.Name, p.Notes, p.Alert, p.ID)
	if err != nil {
		return plateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlate removes an entry (ErrNotFound). Events keep the plate read.
func (s *Store) DeletePlate(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM plates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEventPlate records the plate read on an event ("" clears it).
func (s *Store) SetEventPlate(ctx context.Context, eventID, plate string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE events SET plate=? WHERE id=?`, plate, eventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// plateIndexes remembers the databases whose events.plate index exists.
var plateIndexes sync.Map // *sql.DB → struct{}

// ensurePlateIndex makes the events.plate index once per database. It
// can't live in the schema string: that runs before columnAdds gives older
// databases the column.
func (s *Store) ensurePlateIndex(ctx context.Context) {
	if _, ok := plateIndexes.Load(s.DB); ok {
		return
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS events_plate ON events(plate, created_at)`); err == nil {
		plateIndexes.Store(s.DB, struct{}{})
	}
}

// PlateSightings returns the newest events with exactly this plate, on
// cameraIDs (empty = every camera).
func (s *Store) PlateSightings(ctx context.Context, plate string, cameraIDs []string, limit int) ([]Event, error) {
	if plate == "" {
		return []Event{}, nil
	}
	s.ensurePlateIndex(ctx)
	where, args := []string{`plate = ?`}, []any{plate}
	inClause("camera_id", cameraIDs, &where, &args)
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE `+strings.Join(where, ` AND `)+
		` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// PlateLastSeen is the newest event with the plate on cameraIDs (empty =
// every camera); ErrNotFound when it was never read.
func (s *Store) PlateLastSeen(ctx context.Context, plate string, cameraIDs []string) (*Event, error) {
	ev, err := s.PlateSightings(ctx, plate, cameraIDs, 1)
	if err != nil {
		return nil, err
	}
	if len(ev) == 0 {
		return nil, ErrNotFound
	}
	return &ev[0], nil
}

// CountPlateReads counts events with a plate since the Unix time.
func (s *Store) CountPlateReads(ctx context.Context, since int64) (int, error) {
	s.ensurePlateIndex(ctx)
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE plate <> '' AND created_at >= ?`, since).Scan(&n)
	return n, err
}
