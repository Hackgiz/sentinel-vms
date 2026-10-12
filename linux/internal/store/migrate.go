package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SchemaVersion is the PRAGMA user_version this build writes. 0 is every
// 0.x data directory (before the platform split).
//
//	1  platform phase 1: link_inbox (management side's record of recorder
//	   events already handled, so redelivery from the outbox is harmless).
//	   The recording side adds its own outbox table on the same file in
//	   --role all (see package recorder).
//	2  API v2: api_tokens (bearer access + rotating refresh tokens).
//	3  platform phases 2–3: recording server registry and enrollment tokens,
//	   camera → recording server assignment (and its history, so footage
//	   stays findable after a move), evidence location, camera groups and
//	   user scopes, directory users, saved views, forwarded audit entries.
const SchemaVersion = 3

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{1, "platform phase 1", `
CREATE TABLE IF NOT EXISTS link_inbox (
	id          TEXT PRIMARY KEY,
	received_at INTEGER NOT NULL
);`},
	{2, "API v2 tokens", `
CREATE TABLE IF NOT EXISTS api_tokens (
	id                 TEXT PRIMARY KEY,
	family_id          TEXT NOT NULL,
	user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client             TEXT NOT NULL DEFAULT '',
	access_hash        TEXT NOT NULL UNIQUE,
	access_expires_at  INTEGER NOT NULL,
	refresh_hash       TEXT NOT NULL UNIQUE,
	prev_refresh_hash  TEXT NOT NULL DEFAULT '',
	refresh_expires_at INTEGER NOT NULL,
	created_at         INTEGER NOT NULL,
	used_at            INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS api_tokens_user ON api_tokens(user_id);
CREATE INDEX IF NOT EXISTS api_tokens_prev ON api_tokens(prev_refresh_hash);`},
	{3, "platform phases 2-3", `
CREATE TABLE IF NOT EXISTS recording_servers (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL,
	public_url       TEXT NOT NULL DEFAULT '',
	cert_fingerprint TEXT NOT NULL DEFAULT '',
	seal_public_key  TEXT NOT NULL DEFAULT '',
	version          TEXT NOT NULL DEFAULT '',
	enrolled_at      INTEGER NOT NULL,
	last_seen_at     INTEGER NOT NULL DEFAULT 0,
	revoked_at       INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO recording_servers(id, name, enrolled_at) VALUES('local', 'This server', strftime('%s','now'));
CREATE TABLE IF NOT EXISTS enrollment_tokens (
	token_hash TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at    INTEGER NOT NULL DEFAULT 0,
	server_id  TEXT NOT NULL DEFAULT ''
);
ALTER TABLE cameras ADD COLUMN recorder_id TEXT NOT NULL DEFAULT 'local';
CREATE TABLE IF NOT EXISTS camera_recorder_history (
	camera_id   TEXT NOT NULL,
	recorder_id TEXT NOT NULL,
	from_at     INTEGER NOT NULL,
	to_at       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (camera_id, recorder_id, from_at)
);
INSERT OR IGNORE INTO camera_recorder_history(camera_id, recorder_id, from_at) SELECT id, 'local', 0 FROM cameras;
ALTER TABLE evidence ADD COLUMN recorder_id TEXT NOT NULL DEFAULT 'local';
CREATE TABLE IF NOT EXISTS camera_groups (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS camera_group_members (
	group_id  TEXT NOT NULL REFERENCES camera_groups(id) ON DELETE CASCADE,
	camera_id TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
	PRIMARY KEY (group_id, camera_id)
);
ALTER TABLE users ADD COLUMN all_cameras INTEGER NOT NULL DEFAULT 1;
CREATE TABLE IF NOT EXISTS user_group_scopes (
	user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	group_id TEXT NOT NULL REFERENCES camera_groups(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, group_id)
);
ALTER TABLE users ADD COLUMN source TEXT NOT NULL DEFAULT 'local';
ALTER TABLE users ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS saved_views (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	layout     TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
ALTER TABLE audit ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit ADD COLUMN server_chain_hash TEXT NOT NULL DEFAULT '';`},
}

// prepareMigration runs before Open touches the schema. For a database
// that already holds data and is behind SchemaVersion, it first writes a
// consistent copy next to it: sentinel.db.bak-<app version>-<UTC time>.
// It returns the schema version found.
func prepareMigration(db *sql.DB, dbPath, appVersion string) (int, error) {
	var current int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return 0, err
	}
	existed, err := hasTables(db)
	if err != nil {
		return 0, err
	}
	if existed && current < SchemaVersion {
		backup, err := backupDB(db, dbPath, appVersion)
		if err != nil {
			return 0, fmt.Errorf("backup before migrating: %w", err)
		}
		slog.Info("database backed up before schema upgrade", "backup", backup, "from", current, "to", SchemaVersion)
	}
	return current, nil
}

// applyMigrations brings the schema from version current to SchemaVersion,
// one transaction per step.
func applyMigrations(db *sql.DB, current int) error {
	if current > SchemaVersion {
		slog.Warn("database was written by a newer Sentinel; continuing", "schema", current, "supported", SchemaVersion)
		return nil
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("schema %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// backupDB writes a consistent copy of the database (including anything
// still in the WAL) with VACUUM INTO. The copy is 0600 like the original.
func backupDB(db *sql.DB, dbPath, appVersion string) (string, error) {
	v := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, appVersion)
	if v == "" {
		v = "unknown"
	}
	base := dbPath + ".bak-" + v + "-" + time.Now().UTC().Format("20060102-150405")
	path := base
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = fmt.Sprintf("%s-%d", base, i)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, path); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return filepath.Clean(path), nil
}

// hasTables reports whether the database already holds a schema (an
// existing install) before this build touches it.
func hasTables(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&n)
	return n > 0, err
}
