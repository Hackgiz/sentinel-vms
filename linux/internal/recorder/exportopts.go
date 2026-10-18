package recorder

// WAVE8(D): export options (watermark, pixelated regions) on the recording
// server. The management server composes the watermark text; this side
// validates the regions, adds the camera's privacy masks and hands all of it
// to playback.ExportWith.

import (
	"strings"

	"sentinel-linux/internal/playback"
)

// exportOptions are the playback options for one export of cameraID: the
// camera's privacy masks plus the requested watermark text and pixelated
// regions (validated here too, since the command may come from any peer).
func (r *Recorder) exportOptions(cameraID, watermark string, blur [][4]float64) (playback.ExportOptions, error) {
	regions, err := playback.ValidateRegions(blur)
	if err != nil {
		return playback.ExportOptions{}, err
	}
	o := playback.ExportOptions{Masks: r.exportMasks(cameraID), Blur: regions}
	if strings.TrimSpace(watermark) != "" {
		o.Watermark = playback.WatermarkText(watermark)
	}
	return o, nil
}
