package server

import (
	"net/http"
	"strconv"
	"time"
)

// GET /api/v2/reports/activity?from=<ms>&to=<ms>
//
// Counts of events and alarms per camera, kind and hour (events) or per
// camera, kind and severity (alarms) for a time range. Same permission as
// the alarm and event lists, and the same camera scope: a user never sees a
// count for a camera they can't view. The range is capped at 31 days, and a
// missing range means the last 24 hours.
const maxReportRange = 31 * 24 * time.Hour

type v2ActivityEvent struct {
	CameraID string `json:"cameraId"`
	Kind     string `json:"kind"`
	Hour     int64  `json:"hour"`
	Count    int    `json:"count"`
}

type v2ActivityAlarm struct {
	CameraID string `json:"cameraId"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Count    int    `json:"count"`
}

type v2Activity struct {
	From   int64             `json:"from"`
	To     int64             `json:"to"`
	Totals map[string]int    `json:"totals"`
	Events []v2ActivityEvent `json:"events"`
	Alarms []v2ActivityAlarm `json:"alarms"`
}

func (s *Server) handleV2Activity(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseReportRange(w, r, 24*time.Hour)
	if !ok {
		return
	}

	// The store works in Unix seconds; the API speaks milliseconds.
	sc := s.cameraScope(r.Context())
	events, err := s.Store.EventActivity(r.Context(), from/1000, to/1000)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	alarms, err := s.Store.AlarmActivity(r.Context(), from/1000, to/1000)
	if err != nil {
		writeV2Err(w, err)
		return
	}

	out := v2Activity{From: from, To: to, Totals: map[string]int{"events": 0, "alarms": 0}, Events: []v2ActivityEvent{}, Alarms: []v2ActivityAlarm{}}
	for _, e := range events {
		if !sc.camera(e.CameraID) {
			continue
		}
		out.Events = append(out.Events, v2ActivityEvent{CameraID: e.CameraID, Kind: e.Kind, Hour: secToMs(e.Hour), Count: e.Count})
		out.Totals["events"] += e.Count
	}
	for _, a := range alarms {
		if !sc.camera(a.CameraID) {
			continue
		}
		out.Alarms = append(out.Alarms, v2ActivityAlarm{CameraID: a.CameraID, Kind: a.Kind, Severity: a.Severity, Count: a.Count})
		out.Totals["alarms"] += a.Count
	}
	writeJSON(w, http.StatusOK, out)
}

// parseReportRange reads a report's ?from= and ?to= (Unix ms), answering 400
// itself when either is malformed, to isn't after from, or the range is
// longer than maxReportRange. A missing to is now; a missing from is def
// before now.
func parseReportRange(w http.ResponseWriter, r *http.Request, def time.Duration) (from, to int64, ok bool) {
	q := r.URL.Query()
	now := time.Now()
	to = now.UnixMilli()
	from = now.Add(-def).UnixMilli()
	if v := q.Get("to"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "to must be a Unix time in milliseconds")
			return 0, 0, false
		}
		to = n
	}
	if v := q.Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "from must be a Unix time in milliseconds")
			return 0, 0, false
		}
		from = n
	}
	if to <= from {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "to must be after from")
		return 0, 0, false
	}
	// Compared in milliseconds: converted to a Duration, a range of
	// centuries overflows and can slip through.
	if to-from > maxReportRange.Milliseconds() {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a report covers at most 31 days")
		return 0, 0, false
	}
	return from, to, true
}
