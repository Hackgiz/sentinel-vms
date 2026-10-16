package store

// Incidents (WAVE4-OPERATOR.md, Stream C): a case file that ties alarms,
// events, evidence cases and operator notes together. The DDL lives in the
// base schema (store.go).

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Incident statuses, severities and item kinds.
var (
	IncidentStatuses   = []string{"open", "investigating", "closed"}
	IncidentSeverities = []string{"low", "medium", "high", "critical"}
	IncidentItemKinds  = []string{"alarm", "event", "evidence", "note"}
)

func oneOf(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func ValidIncidentStatus(v string) bool   { return oneOf(IncidentStatuses, v) }
func ValidIncidentSeverity(v string) bool { return oneOf(IncidentSeverities, v) }
func ValidIncidentItemKind(v string) bool { return oneOf(IncidentItemKinds, v) }

// ErrDuplicateItem: the alarm, event or evidence case is already in the incident.
var ErrDuplicateItem = errors.New("that item is already in the incident")

type Incident struct {
	ID        string
	Title     string
	Status    string
	Severity  string
	Summary   string
	CreatedBy string
	CreatedAt int64 // Unix s
	UpdatedAt int64
	ItemCount int
}

type IncidentItem struct {
	ID         string
	IncidentID string
	Kind       string
	RefID      string
	Note       string
	AddedBy    string
	AddedAt    int64
}

const incidentCols = `id, title, status, severity, summary, created_by, created_at, updated_at, (SELECT COUNT(*) FROM incident_items WHERE incident_id = incidents.id)`

func scanIncident(row interface{ Scan(...any) error }) (*Incident, error) {
	i := &Incident{}
	if err := row.Scan(&i.ID, &i.Title, &i.Status, &i.Severity, &i.Summary, &i.CreatedBy, &i.CreatedAt, &i.UpdatedAt, &i.ItemCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return i, nil
}

func (s *Store) CreateIncident(ctx context.Context, title, severity, by string) (*Incident, error) {
	at := now()
	i := &Incident{ID: NewID(), Title: title, Status: "open", Severity: severity, CreatedBy: by, CreatedAt: at, UpdatedAt: at}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO incidents(id, title, status, severity, summary, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		i.ID, i.Title, i.Status, i.Severity, "", i.CreatedBy, i.CreatedAt, i.UpdatedAt)
	return i, err
}

func (s *Store) Incident(ctx context.Context, id string) (*Incident, error) {
	return scanIncident(s.DB.QueryRowContext(ctx, `SELECT `+incidentCols+` FROM incidents WHERE id = ?`, id))
}

// IncidentFilter selects incidents for SearchIncidents.
type IncidentFilter struct {
	Status string
	Text   string // LIKE over title and summary
	Limit  int    // default 100, at most 500
	Before string // keyset cursor: "<createdAt>.<id>"
}

func IncidentCursor(i *Incident) string { return strconv.FormatInt(i.CreatedAt, 10) + "." + i.ID }

// SearchIncidents returns one page, newest first, and the next cursor.
func (s *Store) SearchIncidents(ctx context.Context, f IncidentFilter) ([]Incident, string, error) {
	var where []string
	var args []any
	if f.Status != "" {
		where = append(where, `status = ?`)
		args = append(args, f.Status)
	}
	if t := strings.TrimSpace(f.Text); t != "" {
		pat := "%" + escapeLike(t) + "%"
		where = append(where, `(title LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if f.Before != "" {
		k, ok := parseEventCursor(f.Before)
		if !ok {
			return nil, "", ErrBadCursor
		}
		where = append(where, `(created_at < ? OR (created_at = ? AND id < ?))`)
		args = append(args, k.CreatedAt, k.CreatedAt, k.ID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	limit = min(limit, searchMaxLimit)
	q := `SELECT ` + incidentCols + ` FROM incidents`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *i)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = IncidentCursor(&out[limit-1])
	}
	return out, next, nil
}

// IncidentPatch is a partial update; nil fields stay.
type IncidentPatch struct {
	Title, Status, Severity, Summary *string
}

// UpdateIncident applies the patch and returns the incident.
func (s *Store) UpdateIncident(ctx context.Context, id string, p IncidentPatch) (*Incident, error) {
	var sets []string
	var args []any
	add := func(col string, v *string) {
		if v != nil {
			sets = append(sets, col+` = ?`)
			args = append(args, *v)
		}
	}
	add("title", p.Title)
	add("status", p.Status)
	add("severity", p.Severity)
	add("summary", p.Summary)
	sets = append(sets, `updated_at = ?`)
	args = append(args, now(), id)
	if err := s.updateOne(ctx, `UPDATE incidents SET `+strings.Join(sets, `, `)+` WHERE id = ?`, args...); err != nil {
		return nil, err
	}
	return s.Incident(ctx, id)
}

// AddIncidentItem appends an item. An alarm, event or evidence case can be in
// an incident once (ErrDuplicateItem).
func (s *Store) AddIncidentItem(ctx context.Context, incidentID, kind, refID, note, by string) (*IncidentItem, error) {
	if _, err := s.Incident(ctx, incidentID); err != nil {
		return nil, err
	}
	if kind != "note" {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM incident_items WHERE incident_id = ? AND kind = ? AND ref_id = ?`, incidentID, kind, refID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, ErrDuplicateItem
		}
	}
	it := &IncidentItem{ID: NewID(), IncidentID: incidentID, Kind: kind, RefID: refID, Note: note, AddedBy: by, AddedAt: now()}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO incident_items(id, incident_id, kind, ref_id, note, added_by, added_at) VALUES(?,?,?,?,?,?,?)`,
		it.ID, it.IncidentID, it.Kind, it.RefID, it.Note, it.AddedBy, it.AddedAt); err != nil {
		return nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE incidents SET updated_at = ? WHERE id = ?`, it.AddedAt, incidentID)
	return it, nil
}

func (s *Store) RemoveIncidentItem(ctx context.Context, incidentID, itemID string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM incident_items WHERE id = ? AND incident_id = ?`, itemID, incidentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE incidents SET updated_at = ? WHERE id = ?`, now(), incidentID)
	return nil
}

// IncidentItems lists an incident's items in the order they were added.
func (s *Store) IncidentItems(ctx context.Context, incidentID string) ([]IncidentItem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, incident_id, kind, ref_id, note, added_by, added_at FROM incident_items WHERE incident_id = ? ORDER BY added_at, rowid`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncidentItem{}
	for rows.Next() {
		var it IncidentItem
		if err := rows.Scan(&it.ID, &it.IncidentID, &it.Kind, &it.RefID, &it.Note, &it.AddedBy, &it.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
