package main

import (
	"log/slog"

	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/server"
)

// setupAppearance loads the optional re-identification model for appearance
// search (WAVE9(A)) from SENTINEL_REID_MODEL or <data>/models/reid.onnx,
// through the ONNX Runtime object detection uses. Without it the server
// runs as before and the search UI explains why it is unavailable.
func setupAppearance(dataDir string, srv *server.Server) {
	lib, _ := findObjectModel(dataDir)
	e, reason := reid.Open(dataDir, lib)
	srv.SetAppearanceEmbedder(e, reason)
	if e != nil {
		slog.Info("appearance search ready", "model", e.Model())
	} else {
		slog.Info("appearance search unavailable", "reason", reason)
	}
}
