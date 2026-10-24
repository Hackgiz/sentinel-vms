package server

// Recording modes (WAVE22-RECORDING-MODES.md §6): camera fields ride the
// camera create/patch bodies; the server-wide defaults are
//
//	GET/PATCH /api/v2/settings/recording
//
// v1 /api/v1/settings defaultRetentionDays is the same value as
// defaultRetentionDays here.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Recording(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/settings/recording", auth.ViewLive, s.handleV2GetRecordingSettings)
	s.v2Route(mux, "PATCH /api/v2/settings/recording", auth.ChangeSettings, s.handleV2PatchRecordingSettings)
}

const dualNeedsSub = "Low-res 24/7 + full-res on events (dual) needs the camera's sub-stream: add a sub-stream URL or choose another recording mode"

// validateRecordingInput checks the recording fields of a camera save.
// existing is the stored camera (nil for a new one): a save that leaves the
// mode alone may not remove the sub-stream of a dual camera either.
func validateRecordingInput(in store.CameraInput, existing *store.Camera) error {
	if in.RecordingMode != nil && !link.ValidRecordingMode(*in.RecordingMode) {
		return inputError{"recordingMode must be default, continuous, events, dual or off"}
	}
	if in.RecordTrigger != nil && !link.ValidRecordTrigger(*in.RecordTrigger) {
		return inputError{"recordTrigger must be default, motion or objects"}
	}
	if v := in.PreRollSeconds.Value; v != nil && (*v < 0 || *v > link.MaxPreRollSeconds) {
		return inputError{fmt.Sprintf("preRollSeconds must be null (server default) or between 0 and %d", link.MaxPreRollSeconds)}
	}
	if v := in.PostRollSeconds.Value; v != nil && (*v < 0 || *v > link.MaxPostRollSeconds) {
		return inputError{fmt.Sprintf("postRollSeconds must be null (server default) or between 0 and %d", link.MaxPostRollSeconds)}
	}
	if in.LowResRetentionDays != nil && (*in.LowResRetentionDays < 0 || *in.LowResRetentionDays > link.MaxLowResRetentionDays) {
		return inputError{fmt.Sprintf("lowResRetentionDays must be between 0 (server default) and %d", link.MaxLowResRetentionDays)}
	}
	mode := ""
	switch {
	case in.RecordingMode != nil:
		mode = *in.RecordingMode
	case existing != nil && in.Recording:
		mode = existing.RecordingMode
	}
	if mode == link.RecordDual && strings.TrimSpace(in.SubRTSPURL) == "" {
		return inputError{dualNeedsSub}
	}
	return nil
}

// recordingChange is the audit line of a camera's recording settings
// change, "" when they didn't change.
func recordingChange(was, now *store.Camera) string {
	if was == nil || (was.RecordingMode == now.RecordingMode && was.RecordTrigger == now.RecordTrigger &&
		sameRoll(was.PreRollSeconds, now.PreRollSeconds) && sameRoll(was.PostRollSeconds, now.PostRollSeconds) &&
		was.LowResRetentionDays == now.LowResRetentionDays) {
		return ""
	}
	return fmt.Sprintf("%s: %s → %s (trigger %s, pre-roll %s, post-roll %s, low-res retention %d days; 0 = default)",
		now.Name, was.RecordingMode, now.RecordingMode, now.RecordTrigger, rollText(now.PreRollSeconds), rollText(now.PostRollSeconds), now.LowResRetentionDays)
}

func sameRoll(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

// rollText renders a pre/post-roll value for the audit log.
func rollText(v *int) string {
	if v == nil {
		return "default"
	}
	return fmt.Sprintf("%d s", *v)
}

// MARK: - Server defaults

// v2RecordingPatch is the PATCH body: nil = not sent.
type v2RecordingPatch struct {
	DefaultMode                *string `json:"defaultMode"`
	DefaultTrigger             *string `json:"defaultTrigger"`
	DefaultPreRollSeconds      *int    `json:"defaultPreRollSeconds"`
	DefaultPostRollSeconds     *int    `json:"defaultPostRollSeconds"`
	DefaultRetentionDays       *int    `json:"defaultRetentionDays"`
	DefaultLowResRetentionDays *int    `json:"defaultLowResRetentionDays"`
	MinFreePercent             *int    `json:"minFreePercent"`
}

func (p v2RecordingPatch) apply(d *link.RecordingDefaults) error {
	if p.DefaultMode != nil {
		if !link.ValidRecordingMode(*p.DefaultMode) || *p.DefaultMode == link.RecordDefault {
			return inputError{"defaultMode must be continuous, events, dual or off"}
		}
		d.DefaultMode = *p.DefaultMode
	}
	if p.DefaultTrigger != nil {
		if *p.DefaultTrigger != link.TriggerMotion && *p.DefaultTrigger != link.TriggerObjects {
			return inputError{"defaultTrigger must be motion or objects"}
		}
		d.DefaultTrigger = *p.DefaultTrigger
	}
	if v := p.DefaultPreRollSeconds; v != nil {
		if *v < 0 || *v > link.MaxPreRollSeconds {
			return inputError{fmt.Sprintf("defaultPreRollSeconds must be between 0 and %d", link.MaxPreRollSeconds)}
		}
		d.DefaultPreRollSeconds = *v
	}
	if v := p.DefaultPostRollSeconds; v != nil {
		if *v < link.MinPostRollSeconds || *v > link.MaxPostRollSeconds {
			return inputError{fmt.Sprintf("defaultPostRollSeconds must be between %d and %d", link.MinPostRollSeconds, link.MaxPostRollSeconds)}
		}
		d.DefaultPostRollSeconds = *v
	}
	if v := p.DefaultRetentionDays; v != nil {
		if *v < 1 || *v > 3650 {
			return inputError{"defaultRetentionDays must be between 1 and 3650"}
		}
		d.DefaultRetentionDays = *v
	}
	if v := p.DefaultLowResRetentionDays; v != nil {
		if *v < 1 || *v > link.MaxLowResRetentionDays {
			return inputError{fmt.Sprintf("defaultLowResRetentionDays must be between 1 and %d", link.MaxLowResRetentionDays)}
		}
		d.DefaultLowResRetentionDays = *v
	}
	if v := p.MinFreePercent; v != nil {
		if *v != 0 && (*v < link.MinFreePercentLow || *v > link.MinFreePercentHigh) {
			return inputError{fmt.Sprintf("minFreePercent must be 0 (off) or between %d and %d", link.MinFreePercentLow, link.MinFreePercentHigh)}
		}
		d.MinFreePercent = *v
	}
	return nil
}

func (s *Server) handleV2GetRecordingSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.RecordingDefaults(r.Context()))
}

func (s *Server) handleV2PatchRecordingSettings(w http.ResponseWriter, r *http.Request) {
	var p v2RecordingPatch
	if !readV2JSON(w, r, &p) {
		return
	}
	ctx := r.Context()
	was := s.Store.RecordingDefaults(ctx)
	d := was
	if err := p.apply(&d); err != nil {
		writeV2Fail(w, err, "recording settings")
		return
	}
	if d != was {
		if err := s.Store.SetRecordingDefaults(ctx, d); err != nil {
			writeV2Err(w, err)
			return
		}
		s.audit(r, "Storage", "Changed recording defaults", describeRecordingDefaults(was)+" → "+describeRecordingDefaults(d))
		s.applyRecordingChange(ctx)
	}
	writeJSON(w, http.StatusOK, s.Store.RecordingDefaults(ctx))
}

func describeRecordingDefaults(d link.RecordingDefaults) string {
	free := "off"
	if d.MinFreePercent > 0 {
		free = fmt.Sprintf("%d%%", d.MinFreePercent)
	}
	return fmt.Sprintf("%s, %s, pre %d s, post %d s, keep %d/%d days, min free %s", d.DefaultMode, d.DefaultTrigger,
		d.DefaultPreRollSeconds, d.DefaultPostRollSeconds, d.DefaultRetentionDays, d.DefaultLowResRetentionDays, free)
}

func (s *Server) applyRecordingChange(ctx context.Context) {
	if err := s.ApplyMedia(ctx); err != nil {
		slog.Error("media apply failed", "err", err)
	}
}
