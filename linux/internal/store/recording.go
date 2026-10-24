package store

// Recording modes (WAVE22-RECORDING-MODES.md): the per-camera columns and
// the server-wide defaults (settings table).

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"sentinel-linux/internal/link"
)

// Settings keys of the server-wide recording defaults. default_retention_days
// predates wave 22 (v1 /api/v1/settings) and keeps its key.
const (
	SettingRecordingMode        = "recording.default_mode"
	SettingRecordingTrigger     = "recording.default_trigger"
	SettingRecordingPreRoll     = "recording.default_pre_roll_seconds"
	SettingRecordingPostRoll    = "recording.default_post_roll_seconds"
	SettingDefaultRetentionDays = "default_retention_days"
	SettingLowResRetentionDays  = "recording.default_lowres_retention_days"
	SettingMinFreePercent       = "recording.min_free_percent"
)

// RecordingDefaults reads the server-wide recording settings, normalized
// (missing values take the defaults).
func (s *Store) RecordingDefaults(ctx context.Context) link.RecordingDefaults {
	def := link.DefaultRecordingSettings()
	num := func(key string, fallback int) int {
		if n, err := strconv.Atoi(s.Setting(ctx, key, "")); err == nil {
			return n
		}
		return fallback
	}
	d := link.RecordingDefaults{
		DefaultMode:            s.Setting(ctx, SettingRecordingMode, def.DefaultMode),
		DefaultTrigger:         s.Setting(ctx, SettingRecordingTrigger, def.DefaultTrigger),
		DefaultPreRollSeconds:  num(SettingRecordingPreRoll, def.DefaultPreRollSeconds),
		DefaultPostRollSeconds: num(SettingRecordingPostRoll, def.DefaultPostRollSeconds),
		DefaultRetentionDays:   num(SettingDefaultRetentionDays, def.DefaultRetentionDays),
		MinFreePercent:         num(SettingMinFreePercent, 0),
	}
	// Unset low-res retention follows full-res (2×) rather than a fixed 14.
	d.DefaultLowResRetentionDays = num(SettingLowResRetentionDays, 0)
	return d.Normalized()
}

// SetRecordingDefaults stores the server-wide recording settings (the
// caller validated them).
func (s *Store) SetRecordingDefaults(ctx context.Context, d link.RecordingDefaults) error {
	for k, v := range map[string]string{
		SettingRecordingMode:        d.DefaultMode,
		SettingRecordingTrigger:     d.DefaultTrigger,
		SettingRecordingPreRoll:     strconv.Itoa(d.DefaultPreRollSeconds),
		SettingRecordingPostRoll:    strconv.Itoa(d.DefaultPostRollSeconds),
		SettingDefaultRetentionDays: strconv.Itoa(d.DefaultRetentionDays),
		SettingLowResRetentionDays:  strconv.Itoa(d.DefaultLowResRetentionDays),
		SettingMinFreePercent:       strconv.Itoa(d.MinFreePercent),
	} {
		if err := s.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

// migrateRecordingMode gives cameras from before wave 22 a recording mode:
// recording on → "default" (the server default starts as continuous, so
// nothing changes), off → "off". Idempotent: only rows without a mode.
func migrateRecordingMode(db *sql.DB) error {
	_, err := db.Exec(`UPDATE cameras SET recording_mode = CASE recording WHEN 1 THEN 'default' ELSE 'off' END WHERE recording_mode = ''`)
	return err
}

// migrateRollNull moves the pre-WAVE24 pre/post-roll columns (0 = server
// default) to the nullable pre_roll_s/post_roll_s (NULL = server default, a
// number is literal). It runs once, guarded by a settings key, so a camera
// later set back to the default (NULL) is not re-filled from the old column.
func migrateRollNull(db *sql.DB) error {
	const key = "migrated.roll_null"
	var done string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE cameras SET pre_roll_s = NULLIF(pre_roll_seconds, 0), post_roll_s = NULLIF(post_roll_seconds, 0)`); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, '1')`, key); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// legacyRecordingMode is the mode of a row no migration has touched yet.
func legacyRecordingMode(recording bool) string {
	if recording {
		return link.RecordDefault
	}
	return link.RecordOff
}

// resolveRecording fills the read-only effective mode and makes Recording
// mean "the recorder records something" (effective mode != off).
func resolveRecording(c *Camera, d link.RecordingDefaults) {
	c.EffectiveRecordingMode = link.EffectiveRecordingMode(c.RecordingMode, c.Recording, c.SubRTSPURL != "", d.DefaultMode)
	c.Recording = c.EffectiveRecordingMode != link.RecordOff
}

// recordingModeFor is the mode a save stores: an explicit recordingMode
// wins; otherwise the recording switch keeps compatibility (off → "off",
// on → keep the camera's recording mode, or "default" if it was off).
func recordingModeFor(in CameraInput, existing string) string {
	if in.RecordingMode != nil {
		return *in.RecordingMode
	}
	if !in.Recording {
		return link.RecordOff
	}
	if existing == "" || existing == link.RecordOff {
		return link.RecordDefault
	}
	return existing
}

// saveCameraRecording stores the recording-mode columns of a camera after
// SaveCamera wrote the rest; existing is the stored mode ("" for a new
// camera). The recording column follows the mode (on unless "off").
func (s *Store) saveCameraRecording(ctx context.Context, id string, in CameraInput, existing string) error {
	mode := recordingModeFor(in, existing)
	// pre/post roll: *Set flags say whether to touch the column (nil value
	// writes NULL = server default).
	_, err := s.DB.ExecContext(ctx, `UPDATE cameras SET recording_mode = ?, recording = ?,
		record_trigger = COALESCE(?, record_trigger),
		pre_roll_s = CASE WHEN ? THEN ? ELSE pre_roll_s END,
		post_roll_s = CASE WHEN ? THEN ? ELSE post_roll_s END,
		low_res_retention_days = COALESCE(?, low_res_retention_days)
		WHERE id = ?`,
		mode, boolInt(mode != link.RecordOff), in.RecordTrigger,
		in.PreRollSeconds.Set, in.PreRollSeconds.Value, in.PostRollSeconds.Set, in.PostRollSeconds.Value,
		in.LowResRetentionDays, id)
	return err
}
