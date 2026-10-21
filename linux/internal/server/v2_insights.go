package server

// GET /api/v2/insights?from=<ms>&to=<ms> (WAVE14-AI-ASSIST.md, "Insights"):
// trends and recommendations for a range, built by internal/insights from
// counts the server already keeps. No AI call. The range and the camera
// scope work as in the activity report, except that a missing range means
// the last 7 days. Days are local to the rules engine's time zone.

import (
	"net/http"
	"time"

	"sentinel-linux/internal/insights"
)

const insightsDefaultRange = 7 * 24 * time.Hour

type v2Insights struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
	insights.Report
}

func (s *Server) handleV2Insights(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseReportRange(w, r, insightsDefaultRange)
	if !ok {
		return
	}
	ctx := r.Context()
	sc := s.cameraScope(ctx)
	loc := s.rulesLoc()
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	// The store works in Unix seconds; the API speaks milliseconds.
	events, err := s.Store.EventActivity(ctx, from/1000, to/1000)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	alarms, err := s.Store.AlarmActivityByDay(ctx, from/1000, to/1000, loc)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	list, err := s.Store.AlarmRules(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}

	in := insights.Input{From: time.UnixMilli(from), To: time.UnixMilli(to), Loc: loc}
	for _, c := range cams {
		if sc.camera(c.ID) {
			in.Cameras = append(in.Cameras, insights.Camera{ID: c.ID, Name: c.Name, AlertOn: alertOnName(c.AlertOn), Created: time.Unix(c.CreatedAt, 0)})
		}
	}
	for _, e := range events {
		if sc.camera(e.CameraID) {
			in.Events = append(in.Events, insights.EventCount{CameraID: e.CameraID, Kind: e.Kind, Hour: time.Unix(e.Hour, 0), Count: e.Count})
		}
	}
	for _, a := range alarms {
		if sc.camera(a.CameraID) { // alarms without a camera only for users who see every camera
			in.Alarms = append(in.Alarms, insights.AlarmCount{Day: a.Day, CameraID: a.CameraID, Kind: a.Kind, Count: a.Count})
		}
	}
	for _, rule := range list {
		if rule.Enabled {
			in.RuleKinds = append(in.RuleKinds, rule.Kinds)
		}
	}
	writeJSON(w, http.StatusOK, v2Insights{From: from, To: to, Report: insights.Build(in, time.Now())})
}
