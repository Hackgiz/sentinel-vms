package server

// The v2 JSON shapes (api/openapi.yaml components.schemas) and converters
// from the store's types. v2 keeps its own shapes so the store (and /api/v1,
// which serializes store types directly) can change without breaking
// clients: camelCase "Id" keys and Unix milliseconds throughout.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

func secToMs(s int64) int64 { return s * 1000 }

type v2User struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Role      string      `json:"role"`
	CreatedAt int64       `json:"createdAt"`
	Source    string      `json:"source"`
	Scope     v2UserScope `json:"scope"`
}

type v2UserScope struct {
	AllCameras bool     `json:"allCameras"`
	GroupIDs   []string `json:"groupIds"`
}

// toV2User converts a user with their sign-in source and camera scope;
// an Admin's scope is always every camera.
func toV2User(u *store.User, source string, sc store.UserScope) v2User {
	v := v2User{ID: u.ID, Name: u.Name, Role: u.Role, CreatedAt: secToMs(u.CreatedAt), Source: source,
		Scope: v2UserScope{AllCameras: sc.AllCameras || u.Role == "Admin", GroupIDs: []string{}}}
	if !v.Scope.AllCameras && sc.GroupIDs != nil {
		v.Scope.GroupIDs = sc.GroupIDs
	}
	return v
}

// v2UserView reads what toV2User needs besides the user.
func (s *Server) v2UserView(ctx context.Context, u *store.User) v2User {
	source, err := s.Store.UserSource(ctx, u.ID)
	if err != nil || source == "" {
		source = "local"
	}
	sc, err := s.Store.UserScope(ctx, u.ID)
	if err != nil {
		slog.Warn("user scope unreadable", "user", u.Name, "err", err)
	}
	return toV2User(u, source, sc)
}

type v2Me struct {
	v2User
	Permissions []string `json:"permissions"`
}

var v2PermissionNames = []struct {
	p    auth.Permission
	name string
}{
	{auth.ManageCameras, "manageCameras"},
	{auth.ManageUsers, "manageUsers"},
	{auth.ChangeSettings, "changeSettings"},
	{auth.ViewAudit, "viewAudit"},
	{auth.AcknowledgeAlarms, "acknowledgeAlarms"},
	{auth.ExportEvidence, "exportEvidence"},
	{auth.ViewLive, "viewLive"},
}

func v2Permissions(role string) []string {
	out := []string{}
	for _, p := range v2PermissionNames {
		if auth.Can(role, p.p) {
			out = append(out, p.name)
		}
	}
	return out
}

// MARK: - Cameras

var alertOnNames = []string{alertAuto: "auto", alertMotion: "motion", alertObjects: "peopleAndVehicles", alertPeople: "people"}

func alertOnName(v int) string {
	if v >= 0 && v < len(alertOnNames) {
		return alertOnNames[v]
	}
	return "auto"
}

// alertOnValue parses an AlertOn name; ok is false for an unknown one.
func alertOnValue(name string) (int, bool) {
	for i, n := range alertOnNames {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

type v2CameraStatus struct {
	Online    bool   `json:"online"`
	Viewers   int    `json:"viewers"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Analytics string `json:"analytics,omitempty"`
	Message   string `json:"message"`
}

type v2Camera struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Location        string         `json:"location"`
	RTSPURL         string         `json:"rtspUrl"`
	SubRTSPURL      string         `json:"subRtspUrl"`
	Username        string         `json:"username"`
	HasPassword     bool           `json:"hasPassword"`
	Recording       bool           `json:"recording"`
	RetentionDays   int            `json:"retentionDays"`
	FPS             int            `json:"fps"`
	MotionLevel     int            `json:"motionLevel"`
	AlertOn         string         `json:"alertOn"`
	ONVIFURL        string         `json:"onvifUrl"`
	CameraAnalytics bool           `json:"cameraAnalytics"`
	SortOrder       int            `json:"sortOrder"`
	CreatedAt       int64          `json:"createdAt"`
	RecorderID      string         `json:"recorderId"`
	GroupIDs        []string       `json:"groupIds"`
	Status          v2CameraStatus `json:"status"`
}

// toV2Camera combines a camera with its recording server's status (rs is
// the camera's own server; connected says whether it is connected).
func toV2Camera(c *store.Camera, rs link.ServerStatus, connected bool) v2Camera {
	st := rs.Cameras[c.ID]
	v := v2Camera{
		ID: c.ID, Name: c.Name, Location: c.Location, RTSPURL: c.RTSPURL, SubRTSPURL: c.SubRTSPURL,
		Username: c.Username, HasPassword: c.HasPassword, Recording: c.Recording, RetentionDays: c.RetentionDays,
		FPS: c.FPS, MotionLevel: c.MotionLevel, AlertOn: alertOnName(c.AlertOn), ONVIFURL: c.ONVIFURL,
		CameraAnalytics: c.CameraAnalytics, SortOrder: c.SortOrder, CreatedAt: secToMs(c.CreatedAt),
		RecorderID: c.RecorderID,
		GroupIDs:   []string{},
		Status:     v2CameraStatus{Online: st.Ready, Viewers: st.Readers, Width: st.Width, Height: st.Height, Analytics: st.Analytics},
	}
	switch {
	case !connected:
		v.Status.Online, v.Status.Message = false, "Recording server offline"
	case !rs.Media.Running:
		v.Status.Online, v.Status.Message = false, "Media engine starting"
	case st.Ready:
		v.Status.Message = "Online"
	default:
		v.Status.Message = "No signal"
	}
	return v
}

// MARK: - Alarms, events, evidence

type v2Alarm struct {
	ID           string              `json:"id"`
	CameraID     string              `json:"cameraId,omitempty"`
	CameraName   string              `json:"cameraName,omitempty"`
	Kind         string              `json:"kind"`
	Title        string              `json:"title"`
	Detail       string              `json:"detail"`
	Severity     string              `json:"severity"`
	State        string              `json:"state"`
	Owner        string              `json:"owner"`
	CreatedAt    int64               `json:"createdAt"`
	UpdatedAt    int64               `json:"updatedAt"`
	LastEventAt  int64               `json:"lastEventAt"`
	EventCount   int                 `json:"eventCount"`
	EventID      string              `json:"eventId,omitempty"`
	Instructions []rules.Instruction `json:"instructions,omitempty"`
	ResponseLog  []string            `json:"responseLog"`
}

// toV2Alarm converts an alarm; instructions may be nil (the event stream
// leaves them out; GET /alarms fills them from the rules).
func toV2Alarm(a *store.Alarm, instructions []rules.Instruction) v2Alarm {
	log := a.ResponseLog
	if log == nil {
		log = []string{}
	}
	return v2Alarm{
		ID: a.ID, CameraID: a.CameraID, CameraName: a.CameraName, Kind: a.Kind, Title: a.Title, Detail: a.Detail,
		Severity: a.Severity, State: a.State, Owner: a.Owner, CreatedAt: secToMs(a.CreatedAt), UpdatedAt: secToMs(a.UpdatedAt),
		LastEventAt: secToMs(a.LastEventAt), EventCount: a.EventCount, EventID: a.EventID, Instructions: instructions, ResponseLog: log,
	}
}

type v2Event struct {
	ID           string    `json:"id"`
	CameraID     string    `json:"cameraId"`
	Kind         string    `json:"kind"`
	Label        string    `json:"label,omitempty"`
	Score        float64   `json:"score"`
	Box          []float64 `json:"box,omitempty"`
	CreatedAt    int64     `json:"createdAt"`
	HasThumbnail bool      `json:"hasThumbnail"`
	AlarmID      string    `json:"alarmId,omitempty"`
	Description  string    `json:"description,omitempty"`
	Threat       string    `json:"threat,omitempty"`
	Anomaly      bool      `json:"anomaly,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Tags         []string  `json:"tags,omitempty"`
}

func toV2Event(e *store.Event) v2Event {
	v := v2Event{
		ID: e.ID, CameraID: e.CameraID, Kind: e.Kind, Label: e.Label, Score: e.Score, CreatedAt: secToMs(e.CreatedAt),
		HasThumbnail: e.HasThumb, AlarmID: e.AlarmID, Description: e.Description, Threat: e.Threat, Anomaly: e.Anomaly, Reason: e.Reason,
	}
	if e.Box != "" {
		var box []float64
		if json.Unmarshal([]byte(e.Box), &box) == nil && len(box) == 4 {
			v.Box = box
		}
	}
	for _, t := range strings.Split(e.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			v.Tags = append(v.Tags, t)
		}
	}
	return v
}

type v2Evidence struct {
	ID         string `json:"id"`
	CaseID     string `json:"caseId"`
	AlarmID    string `json:"alarmId,omitempty"`
	CameraID   string `json:"cameraId"`
	CameraName string `json:"cameraName"`
	Title      string `json:"title"`
	Range      string `json:"range"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	LockedAt   int64  `json:"lockedAt"`
	LockedBy   string `json:"lockedBy"`
}

func toV2Evidence(e *store.Evidence) v2Evidence {
	return v2Evidence{
		ID: e.ID, CaseID: e.CaseID, AlarmID: e.AlarmID, CameraID: e.CameraID, CameraName: e.CameraName, Title: e.Title,
		Range: e.Range, Size: e.Size, SHA256: e.SHA256, LockedAt: secToMs(e.LockedAt), LockedBy: e.LockedBy,
	}
}
