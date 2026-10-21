package main

import (
	"log/slog"

	"sentinel-linux/internal/faces"
	"sentinel-linux/internal/server"
)

// setupFaces loads the optional face models (WAVE16, stream C) from
// SENTINEL_FACE_DET_MODEL / SENTINEL_FACE_REC_MODEL or <data>/models/
// face_det.onnx and face_rec.onnx, through the ONNX Runtime object
// detection uses. Without them the server runs as before and the People
// page explains why face recognition can't be turned on.
func setupFaces(dataDir string, srv *server.Server) {
	lib, _ := findObjectModel(dataDir)
	e, reason := faces.Open(dataDir, lib)
	srv.SetFaceEngine(e, reason)
	if e != nil {
		slog.Info("face recognition ready", "detect", e.DetectModel(), "embed", e.EmbedModel())
	} else {
		slog.Info("face recognition unavailable", "reason", reason)
	}
}
