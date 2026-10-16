package homekit

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/brutella/hap"
)

// sqlStore keeps the HAP library's state (long-term keypair, controller
// pairings, config number, device id) in the homekit_state table, so pairing
// travels with the same backup and restore as everything else. It
// implements hap.Store. The store has a single SQLite connection, so
// individual statements are already serialized.
type sqlStore struct{ db *sql.DB }

var _ hap.Store = (*sqlStore)(nil)

func newSQLStore(db *sql.DB) *sqlStore { return &sqlStore{db: db} }

func (s *sqlStore) Set(key string, value []byte) error {
	if value == nil {
		value = []byte{} // the column is NOT NULL
	}
	_, err := s.db.Exec(`INSERT INTO homekit_state(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *sqlStore) Get(key string) ([]byte, error) {
	var v []byte
	if err := s.db.QueryRow(`SELECT value FROM homekit_state WHERE key = ?`, key).Scan(&v); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no entry for key %s", key)
		}
		return nil, err
	}
	return v, nil
}

func (s *sqlStore) Delete(key string) error {
	_, err := s.db.Exec(`DELETE FROM homekit_state WHERE key = ?`, key)
	return err
}

func (s *sqlStore) KeysWithSuffix(suffix string) ([]string, error) {
	rows, err := s.db.Query(`SELECT key FROM homekit_state ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		// Filtered here, not with LIKE: the suffix may contain % or _.
		if strings.HasSuffix(k, suffix) {
			keys = append(keys, k)
		}
	}
	return keys, rows.Err()
}
