package main

import (
	"log/slog"

	"sentinel-linux/internal/semantic"
	"sentinel-linux/internal/server"
)

// setupSemantic loads the optional CLIP models for semantic search
// (wave16-semantic) from SENTINEL_CLIP_IMAGE_MODEL, SENTINEL_CLIP_TEXT_MODEL,
// SENTINEL_CLIP_VOCAB and SENTINEL_CLIP_MERGES, or clip_image.onnx,
// clip_text.onnx, clip_vocab.json and clip_merges.txt under <data>/models,
// through the ONNX Runtime object detection uses. Without them the server
// runs as before and the search UI explains why it is unavailable.
//
// The two towers are ~600 MB, so they load in the background: until then
// the status says they are loading.
func setupSemantic(dataDir string, srv *server.Server) {
	lib, _ := findObjectModel(dataDir)
	if what, _ := semantic.Paths(dataDir).Missing(); what == "" {
		srv.SetSemanticEngine(nil, "semantic search models are loading")
	}
	go func() {
		e, reason := semantic.Open(dataDir, lib)
		srv.SetSemanticEngine(e, reason)
		if e != nil {
			slog.Info("semantic search ready", "model", e.Model())
		} else {
			slog.Info("semantic search unavailable", "reason", reason)
		}
	}()
}
