package store

// Appearance embeddings (WAVE9-REACH.md, stream A): one vector per event in
// event_embeddings (vec = float32 little-endian bytes, model = the model
// file's SHA-256 prefix). Rows from another model are never compared and are
// purged when the server starts with a new one.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Embedding is one stored appearance vector.
type Embedding struct {
	EventID  string
	Model    string
	Dim      int
	Vec      []byte
	CameraID string
	AtMs     int64
}

// PutEmbedding stores (or replaces) an event's vector.
func (s *Store) PutEmbedding(ctx context.Context, e Embedding) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO event_embeddings(event_id, model, dim, vec, camera_id, at_ms) VALUES(?,?,?,?,?,?)
		ON CONFLICT(event_id) DO UPDATE SET model=excluded.model, dim=excluded.dim, vec=excluded.vec, camera_id=excluded.camera_id, at_ms=excluded.at_ms`,
		e.EventID, e.Model, e.Dim, e.Vec, e.CameraID, e.AtMs)
	return err
}

// EventEmbedding returns an event's vector from model (ErrNotFound when it
// has none, or only one from another model).
func (s *Store) EventEmbedding(ctx context.Context, eventID, model string) ([]byte, error) {
	var vec []byte
	err := s.DB.QueryRowContext(ctx, `SELECT vec FROM event_embeddings WHERE event_id = ? AND model = ?`, eventID, model).Scan(&vec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return vec, err
}

// PurgeEmbeddings deletes every vector not from keepModel and returns how
// many went.
func (s *Store) PurgeEmbeddings(ctx context.Context, keepModel string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM event_embeddings WHERE model != ?`, keepModel)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountEmbeddings counts the vectors from model.
func (s *Store) CountEmbeddings(ctx context.Context, model string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_embeddings WHERE model = ?`, model).Scan(&n)
	return n, err
}

// EmbeddingFilter picks the candidates of a similarity search.
type EmbeddingFilter struct {
	Model     string
	CameraIDs []string // nil = every camera
	FromMs    int64    // inclusive; 0 = open
	ToMs      int64    // exclusive; 0 = open
	Kind      string   // "" = any event kind
	Exclude   string   // an event id left out (the query itself)
	Max       int      // most rows scanned, newest first; 0 = no cap
}

// ScanEmbeddings streams the candidate vectors (newest first) to fn. The
// camera_id + at_ms index pre-filters them.
func (s *Store) ScanEmbeddings(ctx context.Context, f EmbeddingFilter, fn func(eventID string, vec []byte)) error {
	q := `SELECT ee.event_id, ee.vec FROM event_embeddings ee`
	where := []string{"ee.model = ?"}
	args := []any{f.Model}
	if f.Kind != "" {
		q += ` JOIN events e ON e.id = ee.event_id`
		where = append(where, "e.kind = ?")
		args = append(args, f.Kind)
	}
	if f.CameraIDs != nil {
		if len(f.CameraIDs) == 0 {
			return nil
		}
		where = append(where, "ee.camera_id IN (?"+strings.Repeat(",?", len(f.CameraIDs)-1)+")")
		for _, id := range f.CameraIDs {
			args = append(args, id)
		}
	}
	if f.FromMs > 0 {
		where = append(where, "ee.at_ms >= ?")
		args = append(args, f.FromMs)
	}
	if f.ToMs > 0 {
		where = append(where, "ee.at_ms < ?")
		args = append(args, f.ToMs)
	}
	if f.Exclude != "" {
		where = append(where, "ee.event_id != ?")
		args = append(args, f.Exclude)
	}
	q += ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ee.at_ms DESC`
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

// EventsToEmbed lists, newest first, the events among the newest limit that
// have a detection box and a thumbnail but no vector from model yet.
func (s *Store) EventsToEmbed(ctx context.Context, model string, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM (
			SELECT `+eventCols+` FROM events WHERE box != '' AND thumbnail != '' ORDER BY created_at DESC, id DESC LIMIT ?
		) e WHERE NOT EXISTS (SELECT 1 FROM event_embeddings ee WHERE ee.event_id = e.id AND ee.model = ?)
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

// EventsByID loads events by id; missing ids are left out.
func (s *Store) EventsByID(ctx context.Context, ids []string) (map[string]*Event, error) {
	out := make(map[string]*Event, len(ids))
	for len(ids) > 0 {
		n := min(len(ids), 500)
		chunk := ids[:n]
		ids = ids[n:]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE id IN (?`+strings.Repeat(",?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[e.ID] = e
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
