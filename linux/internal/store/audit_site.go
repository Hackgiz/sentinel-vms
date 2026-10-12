package store

// The site's audit log for API v2 (PLATFORM.md, "Audit and logs"). The
// management server keeps one hash chain (Audit). A recording server keeps
// its own chain for the actions it takes and forwards each entry; the
// management server appends it to its chain like any other entry, with
// server_id naming the recording server and server_chain_hash holding that
// server's chain hash, so both chains can be verified here.

import (
	"context"
	"database/sql"
	"errors"
)

// SiteAuditEntry is an audit row with where it came from.
type SiteAuditEntry struct {
	AuditEntry
	Seq             int64
	ServerID        string // "" = the management server's own entry
	ServerChainHash string // the recording server's chain hash ("" for own entries)
}

// AuditPage lists up to limit entries before seq (0 = from the newest),
// newest first. serverID nil = every entry; "" = the management server's
// own; else one recording server's forwarded entries.
func (s *Store) AuditPage(ctx context.Context, serverID *string, beforeSeq int64, limit int) ([]SiteAuditEntry, error) {
	q := `SELECT seq, id, time, user, area, action, detail, previous_hash, chain_hash, server_id, server_chain_hash FROM audit WHERE 1=1`
	var args []any
	if serverID != nil {
		q += ` AND server_id = ?`
		args = append(args, *serverID)
	}
	if beforeSeq > 0 {
		q += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteAuditEntry{}
	for rows.Next() {
		var e SiteAuditEntry
		if err := rows.Scan(&e.Seq, &e.ID, &e.Time, &e.User, &e.Area, &e.Action, &e.Detail, &e.PreviousHash, &e.ChainHash,
			&e.ServerID, &e.ServerChainHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditChainHash is the hash that chains e after e.PreviousHash, the same
// on every server.
func AuditChainHash(e AuditEntry) string { return e.computeHash() }

// AppendForwardedAudit adds an entry a recording server forwarded: e is the
// entry as that server wrote it (its id, time, fields and its own
// ChainHash). It joins the site chain and keeps the server's hash. A
// re-sent entry (same server and id) is ignored.
func (s *Store) AppendForwardedAudit(ctx context.Context, serverID string, e AuditEntry) error {
	if serverID == "" || e.ID == "" || e.ChainHash == "" {
		return errors.New("a forwarded audit entry needs its server, id and chain hash")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit WHERE server_id = ? AND id = ?`, serverID, e.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var prev string
	if err := tx.QueryRowContext(ctx, `SELECT chain_hash FROM audit ORDER BY seq DESC LIMIT 1`).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	serverHash := e.ChainHash
	e.PreviousHash = prev
	e.ChainHash = e.computeHash()
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(id, time, user, area, action, detail, previous_hash, chain_hash, server_id, server_chain_hash) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Time, e.User, e.Area, e.Action, e.Detail, e.PreviousHash, e.ChainHash, serverID, serverHash); err != nil {
		return err
	}
	return tx.Commit()
}

// AuditChain is the result of verifying one chain.
type AuditChain struct {
	ServerID string // "" = the site chain
	Entries  int    // entries that verified before BrokenID
	BrokenID string // the first entry that doesn't verify; "" = intact
}

// VerifyAuditChains checks the site chain (every entry, oldest first) and
// then, for each recording server that forwarded entries, that server's
// own chain: each forwarded entry's server hash must follow from the one
// before it (a recording server's chain starts empty when it enrolls, and
// it forwards every entry it writes).
func (s *Store) VerifyAuditChains(ctx context.Context) (site AuditChain, servers []AuditChain, err error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, time, user, area, action, detail, previous_hash, chain_hash, server_id, server_chain_hash FROM audit ORDER BY seq ASC`)
	if err != nil {
		return site, nil, err
	}
	defer rows.Close()
	prev := ""
	type serverState struct {
		chain AuditChain
		prev  string
	}
	byServer := map[string]*serverState{}
	var order []string
	for rows.Next() {
		var e SiteAuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.User, &e.Area, &e.Action, &e.Detail, &e.PreviousHash, &e.ChainHash,
			&e.ServerID, &e.ServerChainHash); err != nil {
			return site, nil, err
		}
		if site.BrokenID == "" {
			if e.PreviousHash != prev || e.computeHash() != e.ChainHash {
				site.BrokenID = e.ID
			} else {
				prev = e.ChainHash
				site.Entries++
			}
		}
		if e.ServerID == "" {
			continue
		}
		st := byServer[e.ServerID]
		if st == nil {
			st = &serverState{chain: AuditChain{ServerID: e.ServerID}}
			byServer[e.ServerID] = st
			order = append(order, e.ServerID)
		}
		if st.chain.BrokenID != "" {
			continue
		}
		own := e.AuditEntry
		own.PreviousHash = st.prev
		if own.computeHash() != e.ServerChainHash {
			st.chain.BrokenID = e.ID
			continue
		}
		st.prev = e.ServerChainHash
		st.chain.Entries++
	}
	if err := rows.Err(); err != nil {
		return site, nil, err
	}
	servers = make([]AuditChain, 0, len(order))
	for _, id := range order {
		servers = append(servers, byServer[id].chain)
	}
	return site, servers, nil
}
