package server

// WAVE8(D): export options on the export routes: a burned-in watermark and
// pixelated regions. The watermark text is composed here, from the
// requesting user, the time and the case id, never taken from the client;
// the request only says whether to watermark.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/playback"
)

// exportRegion is a normalized rectangle of the frame (0-1, origin top left).
type exportRegion struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// exportOptsIn are the optional fields export and long-export requests accept.
type exportOptsIn struct {
	Watermark   bool           `json:"watermark"`
	BlurRegions []exportRegion `json:"blurRegions"`
}

// exportOpts are validated export options.
type exportOpts struct {
	watermark bool
	blur      [][4]float64
}

func parseExportOptions(in exportOptsIn) (exportOpts, error) {
	o := exportOpts{watermark: in.Watermark}
	for _, r := range in.BlurRegions {
		o.blur = append(o.blur, [4]float64{r.X0, r.Y0, r.X1, r.Y1})
	}
	blur, err := playback.ValidateRegions(o.blur)
	if err != nil {
		return exportOpts{}, err
	}
	o.blur = blur
	return o, nil
}

func (o exportOpts) any() bool { return o.watermark || len(o.blur) > 0 }

// watermarkText is the strip text: requesting user, export time (UTC) and,
// when the clip goes into the evidence vault, its case id. Empty when the
// export is not watermarked.
func (o exportOpts) watermarkText(who string, at time.Time, caseID string) string {
	if !o.watermark {
		return ""
	}
	who = strings.TrimSpace(who)
	if who == "" {
		who = "unknown"
	}
	t := "Exported by " + who + " | " + at.UTC().Format("2006-01-02 15:04") + " UTC"
	if caseID != "" {
		t += " | Case " + caseID
	}
	return t
}

// auditNote is appended to an export's audit detail ("" without options).
func (o exportOpts) auditNote() string {
	var parts []string
	if o.watermark {
		parts = append(parts, "watermark")
	}
	if n := len(o.blur); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" pixelated region"+map[bool]string{true: "s"}[n != 1])
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, ", ") + "]"
}

// reservedCases keeps case ids chosen before an export finishes (the
// watermark carries the id) from being handed to a concurrent export.
var reservedCases struct {
	mu  sync.Mutex
	ids map[string]bool
}

// reserveCaseID returns the next free case id and holds it until release is
// called.
func (s *Server) reserveCaseID(ctx context.Context) (id string, release func()) {
	reservedCases.mu.Lock()
	defer reservedCases.mu.Unlock()
	if reservedCases.ids == nil {
		reservedCases.ids = map[string]bool{}
	}
	id = s.Store.NextCaseID(ctx, time.Now())
	for reservedCases.ids[id] {
		id = bumpCaseID(id)
	}
	reservedCases.ids[id] = true
	return id, func() {
		reservedCases.mu.Lock()
		delete(reservedCases.ids, id)
		reservedCases.mu.Unlock()
	}
}

// bumpCaseID adds one to the numeric tail of HG-YYYYMMDD-NNNNN.
func bumpCaseID(id string) string {
	i := strings.LastIndex(id, "-")
	if i < 0 {
		return id + "-2"
	}
	n, _ := strconv.Atoi(id[i+1:])
	return fmt.Sprintf("%s-%05d", id[:i], n+1)
}
