package server

import (
	"net/http"

	"sentinel-linux/internal/store"
)

// handleV2AlarmSeverity changes an alarm's severity: how an operator accepts
// a triage suggestion (triage itself never applies anything).
func (s *Server) handleV2AlarmSeverity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Severity string `json:"severity"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if !store.ValidAlarmSeverity(in.Severity) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "severity must be Critical, Warning or Info")
		return
	}
	old, err := s.scopedAlarm(r.Context(), r.PathValue("alarmId"))
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	a, err := s.Store.SetAlarmSeverity(r.Context(), old.ID, in.Severity, "Severity "+old.Severity+" → "+in.Severity+" by "+currentUser(r).Name)
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	s.audit(r, "Alarms", "Changed alarm severity", a.Title+" ("+a.CameraName+") "+old.Severity+" → "+in.Severity)
	s.publishAlarmState(a)
	writeJSON(w, http.StatusOK, s.v2AlarmView(r.Context(), a))
}
