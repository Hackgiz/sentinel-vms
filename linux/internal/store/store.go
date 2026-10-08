// Package store is Sentinel's persistence layer: one SQLite database in the
// data directory plus an install key that encrypts camera passwords at rest.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sql.DB
	dataDir string
	aead    cipher.AEAD
}

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
	role          TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS cameras (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	location       TEXT NOT NULL DEFAULT '',
	rtsp_url       TEXT NOT NULL,
	sub_rtsp_url   TEXT NOT NULL DEFAULT '',
	username       TEXT NOT NULL DEFAULT '',
	password_enc   TEXT NOT NULL DEFAULT '',
	recording      INTEGER NOT NULL DEFAULT 1,
	retention_days INTEGER NOT NULL DEFAULT 0,
	sort_order     INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS paired_devices (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	paired_by    TEXT NOT NULL DEFAULT '',
	paired_at    INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	apns_token   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS alarms (
	id             TEXT PRIMARY KEY,
	camera_id      TEXT NOT NULL DEFAULT '',
	camera_name    TEXT NOT NULL DEFAULT '',
	kind           TEXT NOT NULL,
	title          TEXT NOT NULL,
	detail         TEXT NOT NULL DEFAULT '',
	severity       TEXT NOT NULL,
	state          TEXT NOT NULL,
	owner          TEXT NOT NULL DEFAULT 'Unassigned',
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	last_event_at  INTEGER NOT NULL,
	event_count    INTEGER NOT NULL DEFAULT 1,
	event_id       TEXT NOT NULL DEFAULT '',
	response_log   TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS alarms_open ON alarms(state, last_event_at);
CREATE TABLE IF NOT EXISTS events (
	id          TEXT PRIMARY KEY,
	camera_id   TEXT NOT NULL,
	kind        TEXT NOT NULL,
	score       REAL NOT NULL,
	created_at  INTEGER NOT NULL,
	thumbnail   TEXT NOT NULL DEFAULT '',
	alarm_id    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_time ON events(created_at);
CREATE TABLE IF NOT EXISTS evidence (
	id          TEXT PRIMARY KEY,
	case_id     TEXT NOT NULL UNIQUE,
	alarm_id    TEXT NOT NULL DEFAULT '',
	camera_id   TEXT NOT NULL,
	camera_name TEXT NOT NULL,
	title       TEXT NOT NULL,
	range_label TEXT NOT NULL,
	file        TEXT NOT NULL,
	size        INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	locked_at   INTEGER NOT NULL,
	locked_by   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	seq           INTEGER PRIMARY KEY AUTOINCREMENT,
	id            TEXT NOT NULL,
	time          INTEGER NOT NULL,
	user          TEXT NOT NULL,
	area          TEXT NOT NULL,
	action        TEXT NOT NULL,
	detail        TEXT NOT NULL,
	previous_hash TEXT NOT NULL,
	chain_hash    TEXT NOT NULL
);
`

// Open creates the data directory (0700), opens the database and loads (or
// creates) the install key used to encrypt camera passwords.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "sentinel.db")+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; avoids SQLITE_BUSY under load
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	key, err := loadOrCreateKey(filepath.Join(dataDir, "install.key"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{DB: db, dataDir: dataDir, aead: aead}, nil
}

// columnAdds are additive migrations for databases created by older builds.
var columnAdds = []struct{ table, column, def string }{
	{"cameras", "fps", "INTEGER NOT NULL DEFAULT 0"},          // frame rate the camera reported over ONVIF
	{"cameras", "motion_level", "INTEGER NOT NULL DEFAULT 2"}, // 0 off, 1 low, 2 medium, 3 high
	{"cameras", "alert_on", "INTEGER NOT NULL DEFAULT 0"},     // 0 auto, 1 any motion, 2 people & vehicles, 3 people only
	{"events", "label", "TEXT NOT NULL DEFAULT ''"},
	{"events", "box", "TEXT NOT NULL DEFAULT ''"},
	{"events", "description", "TEXT NOT NULL DEFAULT ''"},
	{"events", "threat", "TEXT NOT NULL DEFAULT ''"},
	{"events", "anomaly", "INTEGER NOT NULL DEFAULT 0"},
	{"events", "reason", "TEXT NOT NULL DEFAULT ''"},
	{"events", "tags", "TEXT NOT NULL DEFAULT ''"},
}

func addColumns(db *sql.DB) error {
	for _, c := range columnAdds {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) DataDir() string { return s.dataDir }

func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		key, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("install key %s is corrupt", path)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// Seal encrypts a secret for storage; Open reverses it. Empty stays empty.
func (s *Store) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) Unseal(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(data) < s.aead.NonceSize() {
		return "", errors.New("sealed secret is corrupt")
	}
	plain, err := s.aead.Open(nil, data[:s.aead.NonceSize()], data[s.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("sealed secret failed to decrypt")
	}
	return string(plain), nil
}

func (s *Store) Setting(ctx context.Context, key, fallback string) string {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func now() int64 { return time.Now().Unix() }
