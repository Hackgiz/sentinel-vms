package store

// Semantic search vectors (WAVE16-DETECTION.md, stream E): one CLIP vector
// per event snapshot in event_semantic (vec = float32 little-endian bytes,
// model = the image model file's SHA-256 prefix). Rows from another model
// are never compared and are purged when the server starts with a new one.
// Modelled on embeddings.go (appearance search), in its own table.

import (
	"context"
	"strings"
)

// SemanticVector is one stored snapshot vector.
type SemanticVector struct {
	EventID  string
	Model    string
	Dim      int
	Vec      []byte
	CameraID string
	AtMs     int64
}

// PutSemantic stores (or replaces) an event's snapshot vector.
func (s *Store) PutSemantic(ctx context.Context, v SemanticVector) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO event_semantic(event_id, model, dim, vec, camera_id, at_ms) VALUES(?,?,?,?,?,?)
		ON CONFLICT(event_id) DO UPDATE SET model=excluded.model, dim=excluded.dim, vec=excluded.vec, camera_id=excluded.camera_id, at_ms=excluded.at_ms`,
		v.EventID, v.Model, v.Dim, v.Vec, v.CameraID, v.AtMs)
	return err
}

// CountSemantic counts the vectors from model.
func (s *Store) CountSemantic(ctx context.Context, model string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_semantic WHERE model = ?`, model).Scan(&n)
	return n, err
}

// PurgeSemantic deletes every vector not from keepModel and returns how
// many went.
func (s *Store) PurgeSemantic(ctx context.Context, keepModel string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM event_semantic WHERE model != ?`, keepModel)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SemanticFilter picks the candidates of a text search.
type SemanticFilter struct {
	Model     string
	CameraIDs []string // nil = every camera; empty = none
	FromMs    int64    // inclusive; 0 = open
	ToMs      int64    // exclusive; 0 = open
	Max       int      // most rows scanned, newest first; 0 = no cap
}

// ScanSemantic streams the candidate vectors (newest first) to fn. The
// camera_id + at_ms index pre-filters them.
func (s *Store) ScanSemantic(ctx context.Context, f SemanticFilter, fn func(eventID string, vec []byte)) error {
	q := `SELECT event_id, vec FROM event_semantic`
	where := []string{"model = ?"}
	args := []any{f.Model}
	if f.CameraIDs != nil {
		if len(f.CameraIDs) == 0 {
			return nil
		}
		where = append(where, "camera_id IN (?"+strings.Repeat(",?", len(f.CameraIDs)-1)+")")
		for _, id := range f.CameraIDs {
			args = append(args, id)
		}
	}
	if f.FromMs > 0 {
		where = append(where, "at_ms >= ?")
		args = append(args, f.FromMs)
	}
	if f.ToMs > 0 {
		where = append(where, "at_ms < ?")
		args = append(args, f.ToMs)
	}
	q += ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY at_ms DESC`
	if f.Max > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Max)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var vec []byte
		if err := rows.Scan(&id, &vec); err != nil {
			return err
		}
		fn(id, vec)
	}
	return rows.Err()
}

// EventsToSemantic lists, newest first, the events among the newest limit
// that have a thumbnail but no vector from model yet.
func (s *Store) EventsToSemantic(ctx context.Context, model string, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM (
			SELECT `+eventCols+` FROM events WHERE thumbnail != '' ORDER BY created_at DESC, id DESC LIMIT ?
		) e WHERE NOT EXISTS (SELECT 1 FROM event_semantic es WHERE es.event_id = e.id AND es.model = ?)
		ORDER BY created_at DESC, id DESC`, limit, model)
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
