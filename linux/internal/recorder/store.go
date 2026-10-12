package recorder

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"sentinel-linux/internal/link"
)

// Store is the recording side's database: the events it recorded (with
// their snapshot file names) and the outbox of messages not yet taken by
// the management server.
//
// In --role all it shares sentinel.db with the management store (NewStore
// on the same *sql.DB): the events table is then one table that both sides
// write through their own code, and the management side's IndexEvent is a
// no-op. A standalone recording server has its own recorder.db (OpenStore).
type Store struct {
	DB *sql.DB
}

// The events table must stay column-compatible with the management store's
// (internal/store): in --role all it is the same table.
const storeSchema = `
CREATE TABLE IF NOT EXISTS events (
	id          TEXT PRIMARY KEY,
	camera_id   TEXT NOT NULL,
	kind        TEXT NOT NULL,
	score       REAL NOT NULL,
	created_at  INTEGER NOT NULL,
	thumbnail   TEXT NOT NULL DEFAULT '',
	alarm_id    TEXT NOT NULL DEFAULT '',
	label       TEXT NOT NULL DEFAULT '',
	box         TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	threat      TEXT NOT NULL DEFAULT '',
	anomaly     INTEGER NOT NULL DEFAULT 0,
	reason      TEXT NOT NULL DEFAULT '',
	tags        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_time ON events(created_at);
CREATE TABLE IF NOT EXISTS outbox (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	body       TEXT NOT NULL
);
`

// NewStore uses an open database (the shared sentinel.db in --role all).
// It only adds tables; it never alters the management side's.
func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(storeSchema + auditSchema); err != nil {
		return nil, fmt.Errorf("recorder store: %w", err)
	}
	return &Store{DB: db}, nil
}

// OpenStore opens (or creates) a standalone recorder database.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	st, err := NewStore(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

// newEventID matches store.NewID's format (upper-case UUID v4), so event
// ids look the same whichever side created them.
func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// event is one recorded event (the columns the recorder writes).
type event struct {
	ID        string
	CameraID  string
	Kind      string
	Label     string
	Score     float64
	Box       string
	CreatedAt int64
}

// addEvent stores the event and queues msg in the outbox, atomically.
func (s *Store) addEvent(ctx context.Context, e *event, msg link.Event) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id, camera_id, kind, score, created_at, label, box) VALUES(?,?,?,?,?,?,?)`,
		e.ID, e.CameraID, e.Kind, e.Score, e.CreatedAt, e.Label, e.Box); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(id, created_at, body) VALUES(?,?,?)`, msg.ID, time.Now().Unix(), string(body)); err != nil {
		return err
	}
	return tx.Commit()
}

// setThumbnail records the event's snapshot and queues msg, atomically.
func (s *Store) setThumbnail(ctx context.Context, eventID, file string, msg link.Event) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE events SET thumbnail=? WHERE id=?`, file, eventID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(id, created_at, body) VALUES(?,?,?)`, msg.ID, time.Now().Unix(), string(body)); err != nil {
		return err
	}
	return tx.Commit()
}

// enqueue queues a message that has no row of its own (storage health).
func (s *Store) enqueue(ctx context.Context, msg link.Event) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO outbox(id, created_at, body) VALUES(?,?,?)`, msg.ID, time.Now().Unix(), string(body))
	return err
}

// thumbnail returns the snapshot file name of an event ("" = none).
func (s *Store) thumbnail(ctx context.Context, eventID string) string {
	var f string
	_ = s.DB.QueryRowContext(ctx, `SELECT thumbnail FROM events WHERE id = ?`, eventID).Scan(&f)
	return f
}

// pending lists queued messages, oldest first.
func (s *Store) pending(ctx context.Context, limit int) ([]link.Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT body FROM outbox ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []link.Event
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var e link.Event
		if json.Unmarshal([]byte(body), &e) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// delivered removes a message from the outbox.
func (s *Store) delivered(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id)
	return err
}

// pendingCount is the outbox length (status, tests).
func (s *Store) pendingCount(ctx context.Context) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n
}

// trimOutbox bounds the outbox while the management server is away: messages
// older than maxAge go, then the oldest beyond maxRows. Audit entries stay
// (a gap would break this server's chain on the management side).
func (s *Store) trimOutbox(ctx context.Context, maxAge time.Duration, maxRows int) (int64, error) {
	const notAudit = ` AND NOT EXISTS (SELECT 1 FROM recorder_audit a WHERE a.id = outbox.id)`
	res, err := s.DB.ExecContext(ctx, `DELETE FROM outbox WHERE created_at < ?`+notAudit, time.Now().Add(-maxAge).Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	res, err = s.DB.ExecContext(ctx, `DELETE FROM outbox WHERE seq NOT IN (SELECT seq FROM outbox ORDER BY seq DESC LIMIT ?)`+notAudit, maxRows)
	if err != nil {
		return n, err
	}
	m, _ := res.RowsAffected()
	return n + m, nil
}

// pruneEvents deletes events older than cutoff and returns how many and
// their snapshot files.
func (s *Store) pruneEvents(ctx context.Context, cutoff time.Time) (int64, []string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT thumbnail FROM events WHERE created_at < ? AND thumbnail != ''`, cutoff.Unix())
	if err != nil {
		return 0, nil, err
	}
	var files []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			files = append(files, f)
		}
	}
	rows.Close()
	res, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, cutoff.Unix())
	if err != nil {
		return 0, files, err
	}
	n, _ := res.RowsAffected()
	return n, files, nil
}
