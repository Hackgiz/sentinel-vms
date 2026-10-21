package main

import (
	"log/slog"

	"sentinel-linux/internal/lpr"
	"sentinel-linux/internal/server"
)

// setupLPR loads the optional plate OCR models (WAVE16(D)) from
// SENTINEL_PLATE_DET_MODEL / SENTINEL_PLATE_REC_MODEL / SENTINEL_PLATE_DICT or
// <data>/models/plate_det.onnx, plate_rec.onnx and plate_rec.txt, through the
// ONNX Runtime object detection uses. Without them the server runs as before
// and the plates page explains why reading is unavailable. Reading itself
// stays off until switched on (setting lpr.enabled).
func setupLPR(dataDir string, srv *server.Server) {
	lib, _ := findObjectModel(dataDir)
	r, reason := lpr.Open(dataDir, lib)
	srv.SetPlateReader(r, reason)
	if r != nil {
		slog.Info("plate reading ready", "det", r.DetModel(), "rec", r.RecModel())
	} else {
		slog.Info("plate reading unavailable", "reason", reason)
	}
}
