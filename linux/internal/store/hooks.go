package store

import (
	"context"
	"sync"
)

// alarmHooks are told about every newly opened alarm (not merges into an
// open one), after it is committed. Listeners must return quickly.
type alarmHooks struct {
	mu  sync.RWMutex
	fns []func(Alarm)
}

// OnNewAlarm registers a listener for newly raised alarms.
func (s *Store) OnNewAlarm(fn func(Alarm)) {
	s.hooks.mu.Lock()
	s.hooks.fns = append(s.hooks.fns, fn)
	s.hooks.mu.Unlock()
}

func (h *alarmHooks) newAlarm(a Alarm) {
	h.mu.RLock()
	fns := h.fns
	h.mu.RUnlock()
	for _, fn := range fns {
		fn(a)
	}
}

// PushTokens returns every paired device's APNs token, keyed by device id.
func (s *Store) PushTokens(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, apns_token FROM paired_devices WHERE apns_token != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, tok string
		if err := rows.Scan(&id, &tok); err != nil {
			return nil, err
		}
		out[id] = tok
	}
	return out, rows.Err()
}

// ClearAPNSToken forgets a device token APNs reported as dead. The phone
// re-registers a fresh one the next time the app starts.
func (s *Store) ClearAPNSToken(ctx context.Context, token string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE paired_devices SET apns_token = '' WHERE apns_token = ?`, token)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
