package recorder

// The recording server's own audit chain (PLATFORM.md, "Audit and logs").
// Actions this server takes are appended to a hash chain in its store and
// queued in the outbox in the same transaction, so they reach the
// management server like events: at least once, in chain order (they are
// always delivered by Replay, oldest first), and never trimmed from the
// outbox while the management server is away. The management side keeps
// each entry's chain hash, so this chain verifies there.
//
// In --role all the management side already audits the user action behind
// every command and configuration change, so the local recorder audits only
// what it does on its own (expired events, dropped undelivered events).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"sentinel-linux/internal/link"
)

// The chain is per server id: a server enrolled again under a new id
// starts a new chain, as the management side expects.
const auditSchema = `
CREATE TABLE IF NOT EXISTS recorder_audit (
	seq           INTEGER PRIMARY KEY AUTOINCREMENT,
	server_id     TEXT NOT NULL,
	id            TEXT NOT NULL UNIQUE,
	time          INTEGER NOT NULL,
	user          TEXT NOT NULL,
	area          TEXT NOT NULL,
	action        TEXT NOT NULL,
	detail        TEXT NOT NULL,
	previous_hash TEXT NOT NULL,
	chain_hash    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS recorder_audit_server ON recorder_audit(server_id, seq);
`

// Who an entry names: commands come from the management server (which
// audits the user who asked); the rest this server decided itself.
const (
	auditByManagement = "Management server"
	auditBySystem     = "System"
)

// appendAudit chains an entry after serverID's last one and queues it for
// the management server, atomically.
func (s *Store) appendAudit(ctx context.Context, serverID, user, area, action, detail string) (link.AuditEntry, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return link.AuditEntry{}, err
	}
	defer tx.Rollback()
	var prev string
	if err := tx.QueryRowContext(ctx, `SELECT chain_hash FROM recorder_audit WHERE server_id = ? ORDER BY seq DESC LIMIT 1`, serverID).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return link.AuditEntry{}, err
	}
	e := link.AuditEntry{ID: newEventID(), Time: time.Now().Unix(), User: user, Area: area, Action: action, Detail: detail, PreviousHash: prev}
	e.ChainHash = e.Hash()
	if _, err := tx.ExecContext(ctx, `INSERT INTO recorder_audit(server_id, id, time, user, area, action, detail, previous_hash, chain_hash) VALUES(?,?,?,?,?,?,?,?,?)`,
		serverID, e.ID, e.Time, e.User, e.Area, e.Action, e.Detail, e.PreviousHash, e.ChainHash); err != nil {
		return link.AuditEntry{}, err
	}
	body, err := json.Marshal(link.Event{ID: e.ID, Type: link.EventAudit, ServerID: serverID, Time: e.Time * 1000, Audit: &e})
	if err != nil {
		return link.AuditEntry{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(id, created_at, body) VALUES(?,?,?)`, e.ID, e.Time, string(body)); err != nil {
		return link.AuditEntry{}, err
	}
	return e, tx.Commit()
}

// auditLog lists serverID's chain, oldest first (tests).
func (s *Store) auditLog(ctx context.Context, serverID string) ([]link.AuditEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, time, user, area, action, detail, previous_hash, chain_hash FROM recorder_audit WHERE server_id = ? ORDER BY seq`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []link.AuditEntry
	for rows.Next() {
		var e link.AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.User, &e.Area, &e.Action, &e.Detail, &e.PreviousHash, &e.ChainHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// local: this is the recorder inside an all-in-one install.
func (r *Recorder) local() bool { return r.ServerID == LocalServerID }

// audit appends an action this server took on its own and sends it on.
func (r *Recorder) audit(area, action, detail string) {
	if r.chain(auditBySystem, area, action, detail) {
		r.flushSoon()
	}
}

// auditCommand appends an action the management server asked for; skipped
// in --role all, where the management side audits it already.
func (r *Recorder) auditCommand(area, action, detail string) {
	if r.local() {
		return
	}
	if r.chain(auditByManagement, area, action, detail) {
		r.flushSoon()
	}
}

// chain appends without delivering (Replay picks it up).
func (r *Recorder) chain(user, area, action, detail string) bool {
	if _, err := r.store.appendAudit(r.ctx(), r.ServerID, user, area, action, detail); err != nil {
		slog.Error("audit", "action", action, "err", err)
		return false
	}
	return true
}

// flushSoon delivers the outbox in the background: audit entries go out
// only through Replay, which keeps their order.
func (r *Recorder) flushSoon() {
	if r.currentSink() == nil {
		return
	}
	r.background(func() { r.Replay(r.ctx()) })
}

// background runs f on its own goroutine; Wait waits for it.
func (r *Recorder) background(f func()) {
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		f()
	}()
}

// Wait blocks until background work (event snapshots, audit delivery)
// started so far has finished. Call it before closing the store.
func (r *Recorder) Wait() { r.bg.Wait() }

// cameraName is a camera's name for audit details (its id if unknown).
func (r *Recorder) cameraName(id string) string {
	if c, ok := r.camera(id); ok && c.Name != "" {
		return c.Name
	}
	return id
}

// snapshotDigest identifies a configuration's content: the management
// server numbers every snapshot it sends (each reconnect, each change on
// any server), so the revision alone doesn't say something changed.
func snapshotDigest(s link.Snapshot) string {
	s.Rev = 0
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// auditApplied records a configuration the management server sent that
// differs from the one before (the first one after a start too).
func (r *Recorder) auditApplied(s link.Snapshot, a link.Applied) {
	recording := 0
	for _, c := range s.Cameras {
		if c.Recording {
			recording++
		}
	}
	parts := []string{
		fmt.Sprintf("revision %d", s.Rev),
		fmt.Sprintf("%d cameras (%d recording)", len(s.Cameras), recording),
		fmt.Sprintf("default retention %d days", s.DefaultRetentionDays),
	}
	if len(a.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("%d not configured", len(a.Errors)))
	}
	r.auditCommand("System", "Applied configuration", strings.Join(parts, ", "))
}
