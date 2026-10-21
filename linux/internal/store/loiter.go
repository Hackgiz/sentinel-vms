package store

import (
	"context"
	"fmt"
)

// Loitering (WAVE16-DETECTION.md, stream B): cameras.loiter_seconds, the
// time a person track must stay before the recorder raises a Loitering
// event; 0 = off.

const MaxLoiterSeconds = 3600

// ValidLoiterSeconds reports whether n is an allowed loiterSeconds.
func ValidLoiterSeconds(n int) bool { return n >= 0 && n <= MaxLoiterSeconds }

// SetCameraLoiter stores the camera's loitering time.
func (s *Store) SetCameraLoiter(ctx context.Context, id string, seconds int) error {
	if !ValidLoiterSeconds(seconds) {
		return fmt.Errorf("loiterSeconds must be between 0 and %d", MaxLoiterSeconds)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE cameras SET loiter_seconds = ? WHERE id = ?`, seconds, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
