package store

import "context"

// SetCameraFPS records the frame rate a camera now runs its main stream at
// (after Sentinel Admin changed it over ONVIF). 0 clears it to "unknown".
func (s *Store) SetCameraFPS(ctx context.Context, cameraID string, fps int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE cameras SET fps=? WHERE id=?`, fps, cameraID)
	return err
}
