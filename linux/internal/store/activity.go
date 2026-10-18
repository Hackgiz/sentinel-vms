package store

// Live Activity follows (WAVE10). A phone that follows an alarm on its Lock
// Screen keeps one ActivityKit activity; its push token and the alarm it
// follows live in paired_devices.activity_token as a small JSON object
// ({"alarmId","token"}), "" when the phone follows nothing. One followed
// alarm per phone: following another replaces it.

import (
	"context"
	"encoding/json"
)

// ActivityFollow is a phone following an alarm with a Live Activity.
type ActivityFollow struct {
	Device  PairedDevice
	AlarmID string
	Token   string // ActivityKit push token (hex)
}

type activityValue struct {
	AlarmID string `json:"alarmId"`
	Token   string `json:"token"`
}

func encodeActivity(alarmID, token string) string {
	if alarmID == "" || token == "" {
		return ""
	}
	b, _ := json.Marshal(activityValue{AlarmID: alarmID, Token: token})
	return string(b)
}

func decodeActivity(v string) (alarmID, token string, ok bool) {
	var a activityValue
	if v == "" || json.Unmarshal([]byte(v), &a) != nil || a.AlarmID == "" || a.Token == "" {
		return "", "", false
	}
	return a.AlarmID, a.Token, true
}

// SetDeviceActivity records (or, with an empty token, clears) the alarm a
// paired phone follows. ErrNotFound when the device is gone.
func (s *Store) SetDeviceActivity(ctx context.Context, deviceID, alarmID, token string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE paired_devices SET activity_token = ?, last_seen_at = ? WHERE id = ?`,
		encodeActivity(alarmID, token), now(), deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetActivityForPushToken is SetDeviceActivity for a v2 sign-in, whose
// phone is known by its APNs token and owner. ErrNotFound when that phone
// has no push registration owned by owner.
func (s *Store) SetActivityForPushToken(ctx context.Context, apnsToken, owner, alarmID, token string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE paired_devices SET activity_token = ?, last_seen_at = ? WHERE apns_token = ? AND paired_by = ?`,
		encodeActivity(alarmID, token), now(), apnsToken, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ActivityFollowers lists the phones following alarmID.
func (s *Store) ActivityFollowers(ctx context.Context, alarmID string) ([]ActivityFollow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+deviceCols+`, activity_token FROM paired_devices WHERE activity_token != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityFollow
	for rows.Next() {
		var d PairedDevice
		var v string
		if err := rows.Scan(&d.ID, &d.Name, &d.PairedBy, &d.PairedAt, &d.LastSeenAt, &d.apnsToken, &v); err != nil {
			return nil, err
		}
		d.HasPush = d.apnsToken != ""
		if id, tok, ok := decodeActivity(v); ok && id == alarmID {
			out = append(out, ActivityFollow{Device: d, AlarmID: id, Token: tok})
		}
	}
	return out, rows.Err()
}

// ClearDeviceActivity stops a phone following an alarm, but only if it
// still follows that activity token (it may have followed another since).
func (s *Store) ClearDeviceActivity(ctx context.Context, deviceID, token string) error {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT activity_token FROM paired_devices WHERE id = ?`, deviceID).Scan(&v); err != nil {
		return nil // gone already
	}
	if _, tok, ok := decodeActivity(v); !ok || tok != token {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE paired_devices SET activity_token = '' WHERE id = ? AND activity_token = ?`, deviceID, v)
	return err
}
