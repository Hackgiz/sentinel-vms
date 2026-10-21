package store

import (
	"context"
	"time"
)

// The management side's half of the recorder link: the site-wide event
// index fed by link.Sink, and the inbox that makes redelivered events
// harmless (recorders deliver at least once from their outbox).

// IndexEvent adds an event reported by a recording server to the site-wide
// event index. Idempotent: an event already indexed (or, in --role all,
// already written by the recorder into the shared table) is left alone.
func (s *Store) IndexEvent(ctx context.Context, e *Event) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events(id, camera_id, kind, label, score, box, created_at, track_id) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, e.ID, e.CameraID, e.Kind, e.Label, e.Score, e.Box, e.CreatedAt, e.TrackID)
	return err
}

// LinkMessageSeen reports whether a recorder message id was handled already.
func (s *Store) LinkMessageSeen(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_inbox WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// MarkLinkMessage records that a recorder message id was handled.
func (s *Store) MarkLinkMessage(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO link_inbox(id, received_at) VALUES(?, ?) ON CONFLICT(id) DO NOTHING`, id, now())
	return err
}

// PruneLinkInbox forgets handled message ids older than cutoff (recorders
// never replay anything that old).
func (s *Store) PruneLinkInbox(ctx context.Context, cutoff time.Time) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM link_inbox WHERE received_at < ?`, cutoff.Unix())
	return err
}
