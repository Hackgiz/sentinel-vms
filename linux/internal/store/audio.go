package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Audio event detection settings (WAVE8(B), reworked in WAVE9 Stream E).
// cameras.audio_detect is the switch (0 off, 1 on) and
// cameras.audio_threshold_db the threshold in dB over the noise floor
// (0 = never set, reads as AudioDefaultThresholdDB).
//
// Wave 8 had one column and packed both into audio_detect as sign-magnitude:
// 0 off at the default, +N on at N dB, -N off remembering N. A valid
// threshold is at least 6, so any |audio_detect| > 1 is such a legacy value;
// migrateAudioThreshold moves it out when the store opens.
const (
	AudioDefaultThresholdDB = 25
	AudioMinThresholdDB     = 6
	AudioMaxThresholdDB     = 60
)

// DecodeAudio turns the two stored columns into the switch and threshold.
func DecodeAudio(detect, thresholdDB int) (enabled bool, db int) {
	if thresholdDB <= 0 {
		thresholdDB = AudioDefaultThresholdDB
	}
	return detect > 0, thresholdDB
}

// ValidAudioThreshold reports whether db is an acceptable threshold.
func ValidAudioThreshold(db int) bool {
	return db >= AudioMinThresholdDB && db <= AudioMaxThresholdDB
}

// migrateAudioThreshold moves wave-8 sign-magnitude thresholds into
// audio_threshold_db, leaving audio_detect as 0/1. One UPDATE; idempotent,
// since afterwards no |audio_detect| > 1 remains.
func migrateAudioThreshold(db *sql.DB) error {
	_, err := db.Exec(`UPDATE cameras SET audio_threshold_db = ABS(audio_detect), audio_detect = CASE WHEN audio_detect > 0 THEN 1 ELSE 0 END WHERE ABS(audio_detect) > 1`)
	return err
}

// SetCameraAudio changes a camera's audio detection switch and/or
// threshold; nil keeps the stored part. It returns the resulting settings.
func (s *Store) SetCameraAudio(ctx context.Context, id string, enabled *bool, thresholdDB *int) (on bool, db int, err error) {
	if thresholdDB != nil && !ValidAudioThreshold(*thresholdDB) {
		return false, 0, fmt.Errorf("audio threshold must be between %d and %d dB", AudioMinThresholdDB, AudioMaxThresholdDB)
	}
	var detect, thr int
	if err := s.DB.QueryRowContext(ctx, `SELECT audio_detect, audio_threshold_db FROM cameras WHERE id = ?`, id).Scan(&detect, &thr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, 0, ErrNotFound
		}
		return false, 0, err
	}
	on, db = DecodeAudio(detect, thr)
	if enabled != nil {
		on = *enabled
	}
	if thresholdDB != nil {
		db = *thresholdDB
	}
	sw := 0
	if on {
		sw = 1
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE cameras SET audio_detect = ?, audio_threshold_db = ? WHERE id = ?`, sw, db, id); err != nil {
		return false, 0, err
	}
	return on, db, nil
}
