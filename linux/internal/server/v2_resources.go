package server

// API v2: cameras, alarms, events, recordings, rules, users and recording
// servers. Contract: api/openapi.yaml. The handlers reuse the v1 logic
// (saveCamera, setAlarmState, saveRule, createUser, …) so both APIs write
// the same audit entries and bus messages; only the shapes differ.
//
//	v2_resources.go  routes, paging
//	v2_cameras.go    cameras, timeline, recordings
//	v2_alarms.go     alarms, evidence, events
//	v2_admin.go      rules, users
//	v2_servers.go    recording servers, enrollment
//	v2_savedviews.go saved views

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Resources(mux *http.ServeMux) {
	// cameras
	s.v2Route(mux, "GET /api/v2/cameras", auth.ViewLive, s.handleV2Cameras)
	s.v2Route(mux, "POST /api/v2/cameras", auth.ManageCameras, s.handleV2CreateCamera)
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}", auth.ViewLive, s.inScope("cameraId", s.handleV2Camera))
	s.v2Route(mux, "PATCH /api/v2/cameras/{cameraId}", auth.ManageCameras, s.handleV2PatchCamera)
	s.v2Route(mux, "DELETE /api/v2/cameras/{cameraId}", auth.ManageCameras, s.handleV2DeleteCamera)
	// recordings
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}/timeline", auth.ViewLive, s.inScope("cameraId", s.handleV2Timeline))
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}/recordings", auth.ViewLive, s.inScope("cameraId", s.handleV2Recordings))
	// alarms
	s.v2Route(mux, "GET /api/v2/alarms", auth.ViewLive, s.handleV2Alarms)
	s.v2Route(mux, "GET /api/v2/alarms/{alarmId}", auth.ViewLive, s.handleV2Alarm)
	s.v2Route(mux, "POST /api/v2/alarms/{alarmId}/state", auth.AcknowledgeAlarms, s.handleV2AlarmState)
	s.v2Route(mux, "POST /api/v2/alarms/{alarmId}/evidence", auth.ExportEvidence, s.handleV2LockEvidence)
	// events
	s.v2Route(mux, "GET /api/v2/events", auth.ViewLive, s.handleV2Events)
	s.v2Route(mux, "GET /api/v2/reports/activity", auth.ViewLive, s.handleV2Activity)
	s.v2Route(mux, "GET /api/v2/events/{eventId}", auth.ViewLive, s.handleV2Event)
	s.v2Route(mux, "GET /api/v2/events/{eventId}/thumbnail.jpg", auth.ViewLive, s.handleV2EventThumbnail)
	// rules
	s.v2Route(mux, "GET /api/v2/rules", auth.ViewLive, s.handleV2Rules)
	s.v2Route(mux, "POST /api/v2/rules", auth.ChangeSettings, s.handleV2SaveRule)
	s.v2Route(mux, "PUT /api/v2/rules/{ruleId}", auth.ChangeSettings, s.handleV2SaveRule)
	s.v2Route(mux, "DELETE /api/v2/rules/{ruleId}", auth.ChangeSettings, s.handleV2DeleteRule)
	// users
	s.v2Route(mux, "GET /api/v2/users", auth.ManageUsers, s.handleV2Users)
	s.v2Route(mux, "POST /api/v2/users", auth.ManageUsers, s.handleV2CreateUser)
	s.v2Route(mux, "PATCH /api/v2/users/{userId}", auth.ManageUsers, s.handleV2PatchUser)
	s.v2Route(mux, "DELETE /api/v2/users/{userId}", auth.ManageUsers, s.handleV2DeleteUser)
	s.registerV2Servers(mux)    // v2_servers.go
	s.registerV2SavedViews(mux) // v2_savedviews.go
	s.registerV2Health(mux)     // v2_health.go
}

// v2Items answers a list: {"items": [...]}.
func v2Items[T any](w http.ResponseWriter, items []T) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// writeV2Fail answers an error from logic shared with v1: bad input is
// 400, a conflict 409, a missing thing 404 ("<what> not found").
func writeV2Fail(w http.ResponseWriter, err error, what string) {
	var bad inputError
	var conflict conflictError
	switch {
	case errors.As(err, &bad):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
	case errors.As(err, &conflict):
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeV2Error(w, http.StatusNotFound, codeNotFound, what+" not found")
	default:
		writeV2Err(w, err)
	}
}

// MARK: - Paging

const (
	defaultPageLimit = 100
	maxPageLimit     = 500
)

// v2Page is a paged list request: where to start and how many.
type v2Page struct {
	after store.PageKey
	limit int
}

// parseV2Page reads ?cursor= and ?limit= (1…500, default 100), answering
// 400 itself when they're malformed.
func parseV2Page(w http.ResponseWriter, r *http.Request) (v2Page, bool) {
	p := v2Page{limit: defaultPageLimit}
	q := r.URL.Query()
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageLimit {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to 500")
			return p, false
		}
		p.limit = n
	}
	if raw := q.Get("cursor"); raw != "" {
		k, ok := decodeCursor(raw)
		if !ok {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid cursor")
			return p, false
		}
		p.after = k
	}
	return p, true
}

// The cursor is opaque to clients: base64url of "<createdAt>.<id>".
func encodeCursor(k store.PageKey) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(k.CreatedAt, 10) + "." + k.ID))
}

func decodeCursor(c string) (store.PageKey, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return store.PageKey{}, false
	}
	at, id, ok := strings.Cut(string(b), ".")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return store.PageKey{}, false
	}
	return store.PageKey{CreatedAt: n, ID: id}, true
}

// v2PageOf answers one page. The store was asked for limit+1 rows; the
// extra one only says there's more, and the cursor points after the last
// row shown.
func v2PageOf[S, T any](w http.ResponseWriter, rows []S, limit int, key func(*S) store.PageKey, view func(*S) T) {
	resp := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		resp["nextCursor"] = encodeCursor(key(&rows[limit-1]))
	}
	items := make([]T, 0, len(rows))
	for i := range rows {
		items = append(items, view(&rows[i]))
	}
	resp["items"] = items
	writeJSON(w, http.StatusOK, resp)
}
