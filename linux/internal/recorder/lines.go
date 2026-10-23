package recorder

import (
	"encoding/json"
	"fmt"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
)

// Line crossing on the recording server (WAVE8-DETECT-EVIDENCE.md, Stream
// A). The camera's enabled line zones ride in link.CameraConfig.Zones; the
// detector reader tracks objects against them and calls OnLineCross.

// detectLines is the encoded set of the camera's enabled lines, for
// detect.Source.Lines ("" = none).
func detectLines(zones []link.Zone) string {
	var lines []detect.Line
	for _, z := range zones {
		if z.Kind != link.ZoneLine || len(z.Points) != 2 {
			continue
		}
		name := z.Name
		if name == "" {
			name = "Line"
		}
		lines = append(lines, detect.Line{Name: name, P0: z.Points[0], P1: z.Points[1], Direction: z.Direction})
	}
	return detect.EncodeLines(lines)
}

// OnLineCross records one line crossing as a "Line crossing" detection
// event (the same kind camera-reported crossings use), label = the object's
// class, with the analyzed frame outlined as its thumbnail. The detector has
// already debounced it (one per track per line per 10 s).
func (r *Recorder) OnLineCross(c detect.LineCrossing) {
	cam, ok := r.camera(c.CameraID)
	if !ok {
		return
	}
	r.activity(cam.ID, "Line crossing")
	box, _ := json.Marshal(c.Object.Box)
	way := "crossed"
	switch c.Direction {
	case detect.DirLTR:
		way = "crossed left to right"
	case detect.DirRTL:
		way = "crossed right to left"
	}
	d := link.Detection{
		Source: link.SourceLines, Kind: "Line crossing", Label: c.Object.Label, Score: c.Object.Score, Box: string(box),
		Rule: c.Line.Name, Topic: "Sentinel/LineCross/" + c.Direction,
		Summary: fmt.Sprintf("a %s %s the line %q", c.Object.Label, way, c.Line.Name),
		TrackID: trackID(cam.ID, c.TrackID),
	}
	// Crossings within the alarm merge window fold into one alarm.
	ev, ok := r.recordDetection(cam, d, c.At, c.At.Add(-alarmMergeWin))
	if !ok {
		return
	}
	var detected []byte
	if c.Frame != nil {
		detected, _ = detect.FrameJPEG(c.Frame, c.ContentW, c.ContentH, [][4]float64{c.Object.Box}, d.Kind)
	}
	r.background(func() { r.finishDetection(cam, ev, detected) })
}
