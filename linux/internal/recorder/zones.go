package recorder

import (
	"log/slog"
	"os"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

// Zones and privacy masks on the recording server (WAVE7-FEATURES.md,
// Stream A). The management server ships each camera's enabled zones in
// link.CameraConfig.Zones; detection gating and every JPEG this server
// produces use them from here.

// detectZones is the encoded union of the camera's detect polygons, for
// detect.Source.Zones ("" = whole frame).
func detectZones(zones []link.Zone) string {
	var polys []detect.Polygon
	for _, z := range zones {
		if z.Kind == link.ZoneDetect {
			polys = append(polys, detect.Polygon(z.Points))
		}
	}
	return detect.EncodeZones(polys)
}

// maskedJPEG is the recorder's choke point: every thumbnail, playhead
// snapshot and AI frame passes through it. ok=false means the image could
// not be masked and must be dropped, never written or sent unmasked.
func (r *Recorder) maskedJPEG(cameraID string, jpg []byte) ([]byte, bool) {
	cam, found := r.camera(cameraID)
	if !found {
		return jpg, true
	}
	rects := media.MaskRects(cam.Zones)
	if len(rects) == 0 {
		return jpg, true
	}
	out, err := media.MaskJPEG(jpg, rects)
	if err != nil {
		slog.Warn("dropping a snapshot that could not be masked", "camera", cameraID, "err", err)
		return nil, false
	}
	return out, true
}

// maskFile masks a JPEG file in place; on failure it deletes the file.
func (r *Recorder) maskFile(cameraID, path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	out, ok := r.maskedJPEG(cameraID, b)
	if !ok {
		_ = os.Remove(path)
		return false
	}
	if len(out) != len(b) || string(out) != string(b) {
		if err := os.WriteFile(path, out, 0o640); err != nil {
			_ = os.Remove(path)
			return false
		}
	}
	return true
}

// exportMasks are the camera's mask boxes in playback.ExportMasked form.
func (r *Recorder) exportMasks(cameraID string) [][4]float64 {
	cam, ok := r.camera(cameraID)
	if !ok {
		return nil
	}
	var out [][4]float64
	for _, m := range media.MaskRects(cam.Zones) {
		out = append(out, [4]float64{m.X0, m.Y0, m.X1, m.Y1})
	}
	return out
}
