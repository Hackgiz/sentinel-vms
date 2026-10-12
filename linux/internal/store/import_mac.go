package store

// Bookkeeping for the Mac importer (POST /api/v2/import/mac). What was
// imported is remembered in settings rows "mac_import/<kind>/<key>" (no
// schema change): which camera a Mac camera became, which users and rules
// came from the Mac, which evidence files arrived, and each imported
// camera's Mac recordings folder. Re-sending an import finds them there
// and creates nothing twice.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const macImportPrefix = "mac_import/"

// Mac import kinds (settings key "mac_import/<kind>/<key>" → value).
const (
	MacImportCamera     = "camera"     // Mac camera id → camera id
	MacImportUser       = "user"       // lowercased user name → user id
	MacImportRule       = "rule"       // lowercased rule name → rule id
	MacImportEvidence   = "evidence"   // case id → evidence id
	MacImportUploaded   = "uploaded"   // case id → sha256 of the stored file
	MacImportRecordings = "recordings" // camera id → Mac recordings folder
)

// MacImportRef is what key of kind became in an earlier import.
func (s *Store) MacImportRef(ctx context.Context, kind, key string) (string, bool) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, macImportPrefix+kind+"/"+key).Scan(&v)
	return v, err == nil
}

// SetMacImportRef remembers what key of kind became.
func (s *Store) SetMacImportRef(ctx context.Context, kind, key, value string) error {
	return s.SetSetting(ctx, macImportPrefix+kind+"/"+key, value)
}

// MacImportRefs is every key → value of kind.
func (s *Store) MacImportRefs(ctx context.Context, kind string) (map[string]string, error) {
	prefix := macImportPrefix + kind + "/"
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM settings WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[strings.TrimPrefix(k, prefix)] = v
	}
	return out, rows.Err()
}

// EvidenceByCaseID is the evidence filed under a case id.
func (s *Store) EvidenceByCaseID(ctx context.Context, caseID string) (*Evidence, error) {
	return scanEvidence(s.DB.QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE case_id = ?`, caseID))
}

// DeleteImportedEvidence removes an evidence entry the importer couldn't
// record (evidence is otherwise never deleted).
func (s *Store) DeleteImportedEvidence(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM evidence WHERE id = ?`, id)
	return err
}

// MacAuditEntry is one entry of the Mac app's audit log.
type MacAuditEntry struct {
	At     int64 // Unix ms, when the Mac wrote it
	User   string
	Area   string
	Action string
	Detail string
}

// macAuditIDPrefix starts the id of every imported audit entry.
const macAuditIDPrefix = "MAC-"

// macAuditID is an imported entry's id, the same every time the same entry
// is imported: a hash of its fields and n, its position among identical
// entries of the same import.
func macAuditID(e MacAuditEntry, n int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{fmt.Sprint(e.At), e.User, e.Area, e.Action, e.Detail, fmt.Sprint(n)}, "\x1f")))
	return macAuditIDPrefix + strings.ToUpper(hex.EncodeToString(sum[:16]))
}

// ImportMacAudit appends the Mac app's audit entries that aren't in the
// log yet to the site chain, oldest first, in one transaction, after a
// header entry by user saying what was imported. Each keeps its user,
// area and action; its time is the import's (so the chain stays in time
// order) and the Mac time goes in the detail. The Mac chain's own hashes
// are not kept: its entries become ordinary links of the site chain, so
// VerifyAudit and VerifyAuditChains cover them like any other entry.
// It returns how many entries were added (the header not counted).
func (s *Store) ImportMacAudit(ctx context.Context, user, sourceVersion string, entries []MacAuditEntry) (int, error) {
	type item struct {
		MacAuditEntry
		id string
	}
	seen := map[string]int{}
	all := make([]item, 0, len(entries))
	for _, e := range entries {
		base := macAuditID(e, 0)
		all = append(all, item{e, macAuditID(e, seen[base])})
		seen[base]++
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At < all[j].At })

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	have := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM audit WHERE substr(id, 1, ?) = ?`, len(macAuditIDPrefix), macAuditIDPrefix)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		have[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var add []item
	for _, it := range all {
		if !have[it.id] {
			add = append(add, it)
		}
	}
	if len(add) == 0 {
		return 0, nil
	}
	var prev string
	if err := tx.QueryRowContext(ctx, `SELECT chain_hash FROM audit ORDER BY seq DESC LIMIT 1`).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	t := now()
	insert := func(e AuditEntry) error {
		e.PreviousHash = prev
		e.ChainHash = e.computeHash()
		prev = e.ChainHash
		_, err := tx.ExecContext(ctx, `INSERT INTO audit(id, time, user, area, action, detail, previous_hash, chain_hash) VALUES(?,?,?,?,?,?,?,?)`,
			e.ID, e.Time, e.User, e.Area, e.Action, e.Detail, e.PreviousHash, e.ChainHash)
		return err
	}
	header := fmt.Sprintf("%d entries from Sentinel for Mac %s, %s to %s", len(add), sourceVersion, macTime(add[0].At), macTime(add[len(add)-1].At))
	if err := insert(AuditEntry{ID: NewID(), Time: t, User: user, Area: "Audit", Action: "Imported audit from Mac", Detail: header}); err != nil {
		return 0, err
	}
	for _, it := range add {
		detail := strings.TrimSpace(it.Detail + " (on the Mac at " + macTime(it.At) + ")")
		if err := insert(AuditEntry{ID: it.id, Time: t, User: it.User, Area: it.Area, Action: it.Action, Detail: detail}); err != nil {
			return 0, err
		}
	}
	return len(add), tx.Commit()
}

// macTime formats a Mac audit time (Unix ms) for a detail.
func macTime(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05 UTC") }
