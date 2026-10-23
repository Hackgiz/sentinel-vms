package store

// Recording modes (WAVE22-RECORDING-MODES.md): the per-camera columns and
// the server-wide defaults (settings table).

import (
	"context"
	"database/sql"
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
	_, err := s.DB.ExecContext(ctx, `UPDATE cameras SET recording_mode = ?, recording = ?,
		record_trigger = COALESCE(?, record_trigger), pre_roll_seconds = COALESCE(?, pre_roll_seconds),
		post_roll_seconds = COALESCE(?, post_roll_seconds), low_res_retention_days = COALESCE(?, low_res_retention_days)
		WHERE id = ?`,
		mode, boolInt(mode != link.RecordOff), in.RecordTrigger, in.PreRollSeconds, in.PostRollSeconds, in.LowResRetentionDays, id)
	return err
}
