package server

// Detector settings API (WAVE16-DETECTION.md, stream A):
//
//	GET /api/v2/detector  ViewLive: saved model and rate, and what runs
//	PUT /api/v2/detector  ChangeSettings, audited
//
// The model is read when the recorder's detector starts (a change shows
// as pending until a restart); the rate reaches the recorders at once
// through the configuration snapshot (cameras.go).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/models"
)

// Detection rate limits (passes per second per camera; 0 = auto).
const (
	detectRateMax      = 5
	detectRateAutoCPU  = 1
	detectRateAutoAccl = 5
)

type v2DetectorStatus struct {
	Model         string  `json:"model"`
	Engine        string  `json:"engine"`
	Device        string  `json:"device,omitempty"`
	EffectiveRate int     `json:"effectiveRate"`
	Pending       bool    `json:"pending"`
	Reason        string  `json:"reason,omitempty"`
	InferMs       float64 `json:"inferMs"`
}

type v2DetectorSettings struct {
	Model  string           `json:"model"`
	Rate   int              `json:"rate"`
	Status v2DetectorStatus `json:"status"`
}

func (s *Server) detectorSaved(ctx context.Context) (string, int) {
	model := s.Store.Setting(ctx, SettingDetectorModel, "")
	rate, _ := strconv.Atoi(s.Store.Setting(ctx, SettingDetectRate, "0"))
	return model, rate
}

// detectorInfo asks the default recording server what its detector runs
// (nil = no detector, or the server is unreachable).
func (s *Server) detectorInfo(ctx context.Context) *detect.DetectorInfo {
	// The default recorder first; on a site with several recording servers
	// and no local one there is no default, so ask each connected one and
	// report the first detector found (they all run the same settings).
	rid, ok := s.defaultRecorderID(ctx)
	ids := []string{rid}
	if !ok {
		ids = ids[:0]
		if list, err := s.Store.RecordingServers(ctx); err == nil {
			for _, r := range list {
				if !r.Revoked() && s.recorderConnected(r.ID) {
					ids = append(ids, r.ID)
				}
			}
		}
	}
	for _, id := range ids {
		if info := s.recorderDetectorInfo(ctx, id); info != nil {
			return info
		}
	}
	return nil
}

// recorderDetectorInfo asks one recording server what its detector runs.
func (s *Server) recorderDetectorInfo(ctx context.Context, rid string) *detect.DetectorInfo {
	var hw link.HWAccelStatus
	if err := s.command(ctx, rid, link.CmdHWAccelStatus, nil, &hw); err != nil || !hw.Available {
		return nil
	}
	if len(hw.Detection) == 0 || string(hw.Detection) == "null" {
		return nil
	}
	var info detect.DetectorInfo
	if json.Unmarshal(hw.Detection, &info) != nil {
		return nil
	}
	return &info
}

// effectiveRate is the rate in use: what the recorder reports, else the
// saved rate, else auto (1/s on the CPU, 5/s with an accelerator).
func effectiveRate(saved int, info *detect.DetectorInfo) int {
	if info != nil && info.Rate > 0 {
		return info.Rate
	}
	if saved > 0 {
		return min(saved, detectRateMax)
	}
	if info != nil && info.Engine != "" && info.Engine != detect.EngineCPU {
		return detectRateAutoAccl
	}
	return detectRateAutoCPU
}

func (s *Server) detectorSettings(ctx context.Context) v2DetectorSettings {
	model, rate := s.detectorSaved(ctx)
	out := v2DetectorSettings{Model: model, Rate: rate}
	want := models.NormDetector(model)
	info := s.detectorInfo(ctx)
	st := &out.Status
	st.EffectiveRate = effectiveRate(rate, info)
	if info == nil {
		st.Pending = want != models.IDYOLOXTiny
		st.Reason = "object detection is not running (no ONNX Runtime or model on the recording server, or it is offline)"
		return out
	}
	st.Engine, st.Device, st.InferMs = info.Engine, info.Device, info.AvgMs
	st.Model = models.NormDetector(info.ModelID)
	if st.Model != want {
		st.Pending = true
		switch state := s.modelState(want); {
		case state == models.StateDownloading:
			st.Reason = "downloading"
		case state != models.StateInstalled:
			if _, _, err := models.Detector(s.modelsDataDir(), want); err != nil {
				st.Reason = err.Error()
			}
		case info.ModelNote != "":
			st.Reason = info.ModelNote // installed here, but the recorder couldn't use it
		}
	}
	return out
}

func (s *Server) handleV2Detector(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.detectorSettings(r.Context()))
}

func (s *Server) handleV2PutDetector(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model *string `json:"model"`
		Rate  *int    `json:"rate"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	model, rate := s.detectorSaved(ctx)
	if in.Rate != nil {
		if *in.Rate < 0 || *in.Rate > detectRateMax {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "rate must be 0 (auto) or 1 to 5")
			return
		}
		rate = *in.Rate
	}
	if in.Model != nil {
		id := *in.Model
		e, ok := models.Lookup(models.NormDetector(id))
		if !ok || e.Purpose != models.PurposeDetector {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "model must be a detector from the catalog")
			return
		}
		if !e.Bundled {
			if s.modelState(e.ID) == models.StateDownloading {
				writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" is still downloading")
				return
			}
			if _, _, err := models.Detector(s.modelsDataDir(), e.ID); err != nil {
				writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
				return
			}
		}
		model = id
	}
	oldModel, oldRate := s.detectorSaved(ctx)
	if err := s.Store.SetSetting(ctx, SettingDetectorModel, model); err != nil {
		writeV2Err(w, err)
		return
	}
	if err := s.Store.SetSetting(ctx, SettingDetectRate, strconv.Itoa(rate)); err != nil {
		writeV2Err(w, err)
		return
	}
	if model != oldModel || rate != oldRate {
		rateText := "auto"
		if rate > 0 {
			rateText = strconv.Itoa(rate) + "/s"
		}
		s.audit(r, "Detection", "Changed detector", models.NormDetector(model)+", "+rateText)
		if err := s.ApplyMedia(ctx); err != nil {
			slog.Error("media apply failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, s.detectorSettings(ctx))
}
