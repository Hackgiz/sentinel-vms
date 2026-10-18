package store

import "context"

// AuditEntriesFor lists, oldest first, up to limit audit entries whose
// detail names any of the given needles (an evidence case id, the clip's
// SHA-256). The evidence package carries them as its chain section. Each
// entry is as the site chain holds it, so AuditChainHash verifies it.
func (s *Store) AuditEntriesFor(ctx context.Context, limit int, needles ...string) ([]AuditEntry, error) {
	q := `SELECT id, time, user, area, action, detail, previous_hash, chain_hash FROM audit WHERE 0`
	var args []any
	for _, n := range needles {
		if n == "" {
			continue
		}
		q += ` OR instr(detail, ?) > 0`
		args = append(args, n)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY seq LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.User, &e.Area, &e.Action, &e.Detail, &e.PreviousHash, &e.ChainHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
