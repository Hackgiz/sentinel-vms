package server

// API v2: GET /api/v2/system/config-report, a snapshot of the site's
// configuration for archiving or a support case. JSON by default,
// ?format=html for a printable page.
//
// Redaction is by construction: every section is built from an allow-list
// of fields, never by serialising a store type, so nothing that holds a
// credential can leak by being added to one later. Camera passwords and
// usernames, user password hashes, webhook secrets, certificate
// fingerprints and sealing keys are never read into the report; URLs lose
// their userinfo, query and fragment.

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"
)

type reportServer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PublicURL  string `json:"publicUrl"`
	Version    string `json:"version"`
	Online     bool   `json:"online"`
	Revoked    bool   `json:"revoked"`
	EnrolledAt int64  `json:"enrolledAt"`
	LastSeenAt int64  `json:"lastSeenAt"`
}

type reportCamera struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Location       string `json:"location"`
	Server         string `json:"server"`
	MainStream     string `json:"mainStream"`
	SubStream      string `json:"subStream"`
	ONVIF          string `json:"onvif"`
	HasCredentials bool   `json:"hasCredentials"`
	Recording      bool   `json:"recording"`
	RetentionDays  int    `json:"retentionDays"`
	MotionLevel    int    `json:"motionLevel"`
	AlertOn        int    `json:"alertOn"`
}

type reportGroup struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Cameras []string `json:"cameras"`
}

type reportUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"createdAt"`
}

type reportRole struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Builtin     bool            `json:"builtin"`
	Permissions json.RawMessage `json:"permissions"`
}

type reportRule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Cameras  int    `json:"cameras"` // 0 = every camera
	Kinds    string `json:"kinds"`
	Severity string `json:"minSeverity"`
	Delivery string `json:"delivery"`
	Schedule string `json:"schedule"`
}

type reportProfile struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Body json.RawMessage `json:"body"`
}

type reportWebhook struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Endpoint  string          `json:"endpoint"` // scheme and host only
	HasSecret bool            `json:"hasSecret"`
	Events    json.RawMessage `json:"events"`
	Enabled   bool            `json:"enabled"`
}

type configReport struct {
	GeneratedAt  string          `json:"generatedAt"`
	Versions     map[string]any  `json:"versions"`
	Site         v2Site          `json:"site"`
	Thresholds   v2Thresholds    `json:"thresholds"`
	Servers      []reportServer  `json:"servers"`
	Cameras      []reportCamera  `json:"cameras"`
	Groups       []reportGroup   `json:"groups"`
	Users        []reportUser    `json:"users"`
	Roles        []reportRole    `json:"roles"`
	Rules        []reportRule    `json:"rules"`
	TimeProfiles []reportProfile `json:"timeProfiles"`
	Webhooks     []reportWebhook `json:"webhooks"`
}

// safeURL keeps scheme, host and path of a camera/stream URL; the userinfo,
// query and fragment (where credentials and tokens go) are dropped. An
// unparsable value is dropped whole.
func safeURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// safeEndpoint is scheme and host only: webhook paths often carry a token.
func safeEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

func (s *Server) buildConfigReport(ctx context.Context) (*configReport, error) {
	rep := &configReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Versions:    map[string]any{"server": s.Version, "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH},
		Site:        s.loadSite(ctx), Thresholds: s.loadThresholds(ctx),
		Servers: []reportServer{}, Cameras: []reportCamera{}, Groups: []reportGroup{}, Users: []reportUser{},
		Roles: []reportRole{}, Rules: []reportRule{}, TimeProfiles: []reportProfile{}, Webhooks: []reportWebhook{},
	}
	servers, err := s.Store.RecordingServers(ctx)
	if err != nil {
		return nil, err
	}
	for _, rs := range servers {
		rep.Servers = append(rep.Servers, reportServer{ID: rs.ID, Name: rs.Name, PublicURL: safeEndpoint(rs.PublicURL), Version: rs.Version,
			Online: s.recorderConnected(rs.ID), Revoked: rs.Revoked(), EnrolledAt: rs.EnrolledAt, LastSeenAt: rs.LastSeenAt})
	}
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, c := range cams {
		names[c.ID] = c.Name
		rep.Cameras = append(rep.Cameras, reportCamera{ID: c.ID, Name: c.Name, Location: c.Location, Server: c.RecorderID,
			MainStream: safeURL(c.RTSPURL), SubStream: safeURL(c.SubRTSPURL), ONVIF: safeURL(c.ONVIFURL),
			HasCredentials: c.Username != "" || c.HasPassword || strings.Contains(c.RTSPURL, "@"),
			Recording:      c.Recording, RetentionDays: c.RetentionDays, MotionLevel: c.MotionLevel, AlertOn: c.AlertOn})
	}
	groups, err := s.Store.CameraGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		cn := []string{}
		for _, id := range g.CameraIDs {
			if n := names[id]; n != "" {
				cn = append(cn, n)
			}
		}
		sort.Strings(cn)
		rep.Groups = append(rep.Groups, reportGroup{ID: g.ID, Name: g.Name, Cameras: cn})
	}
	users, err := s.Store.Users(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		rep.Users = append(rep.Users, reportUser{ID: u.ID, Name: u.Name, Role: u.Role, CreatedAt: u.CreatedAt})
	}
	rules, err := s.Store.AlarmRules(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		sched := "always"
		if r.Schedule {
			sched = minuteLabel(r.FromMinute) + "-" + minuteLabel(r.ToMinute)
		}
		rep.Rules = append(rep.Rules, reportRule{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Cameras: len(r.CameraIDs),
			Kinds: strings.Join(r.Kinds, ", "), Severity: r.MinSeverity, Delivery: r.Delivery, Schedule: sched})
	}
	// Roles, time profiles and webhooks are read straight from their tables
	// (columns chosen explicitly: the webhook secret is never selected).
	if err := s.reportRows(ctx, `SELECT id, name, description, permissions, builtin FROM roles ORDER BY builtin DESC, name`, func(rows *sql.Rows) error {
		var r reportRole
		var perms string
		var builtin int
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &perms, &builtin); err != nil {
			return err
		}
		r.Builtin, r.Permissions = builtin == 1, jsonOr(perms, "[]")
		rep.Roles = append(rep.Roles, r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.reportRows(ctx, `SELECT id, name, body FROM time_profiles ORDER BY name`, func(rows *sql.Rows) error {
		var p reportProfile
		var body string
		if err := rows.Scan(&p.ID, &p.Name, &body); err != nil {
			return err
		}
		p.Body = jsonOr(body, "{}")
		rep.TimeProfiles = append(rep.TimeProfiles, p)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.reportRows(ctx, `SELECT id, name, url, secret <> '', events, enabled FROM webhooks ORDER BY name`, func(rows *sql.Rows) error {
		var h reportWebhook
		var raw, events string
		var hasSecret, enabled int
		if err := rows.Scan(&h.ID, &h.Name, &raw, &hasSecret, &events, &enabled); err != nil {
			return err
		}
		h.Endpoint, h.HasSecret, h.Enabled, h.Events = safeEndpoint(raw), hasSecret == 1, enabled == 1, jsonOr(events, "[]")
		rep.Webhooks = append(rep.Webhooks, h)
		return nil
	}); err != nil {
		return nil, err
	}
	return rep, nil
}

func (s *Server) reportRows(ctx context.Context, query string, each func(*sql.Rows) error) error {
	rows, err := s.Store.DB.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func jsonOr(raw, fallback string) json.RawMessage {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	return json.RawMessage(fallback)
}

func minuteLabel(m int) string {
	return time.Date(2000, 1, 1, m/60, m%60, 0, 0, time.UTC).Format("15:04")
}

func (s *Server) handleV2ConfigReport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "html" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "format must be json or html")
		return
	}
	rep, err := s.buildConfigReport(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Settings", "Generated configuration report", format)
	if format == "html" {
		var buf strings.Builder
		if err := configReportTemplate.Execute(&buf, rep); err != nil {
			writeV2Err(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		_, _ = w.Write([]byte(buf.String()))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

var configReportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"when": func(ts int64) string {
		if ts == 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04 UTC")
	},
	"yes": func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	},
	"raw": func(m json.RawMessage) string { return string(m) },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Sentinel configuration report</title>
<style>
body{font:14px/1.45 -apple-system,Segoe UI,sans-serif;margin:32px;color:#111}
h1{font-size:22px;margin:0 0 4px}h2{font-size:16px;margin:28px 0 8px;border-bottom:1px solid #ccc;padding-bottom:4px}
table{border-collapse:collapse;width:100%;font-size:13px}th,td{text-align:left;padding:4px 10px 4px 0;vertical-align:top;border-bottom:1px solid #eee}
th{font-weight:600}.mono{font-family:ui-monospace,Menlo,monospace;font-size:12px}.sub{color:#555}
@media print{body{margin:12mm}h2{break-after:avoid}tr{break-inside:avoid}}
</style></head><body>
<h1>{{if .Site.Name}}{{.Site.Name}}{{else}}Sentinel{{end}} configuration report</h1>
<div class="sub">Generated {{.GeneratedAt}} · server {{index .Versions "server"}} · credentials, password hashes and secrets are redacted</div>
{{if or .Site.Description .Site.Contact}}<p>{{.Site.Description}}{{if .Site.Contact}}<br>Contact: {{.Site.Contact}}{{end}}</p>{{end}}
<h2>Monitor thresholds (amber / red %)</h2>
<table><tr><th>CPU</th><th>Memory</th><th>Disk</th></tr>
<tr><td>{{.Thresholds.CPU.Amber}} / {{.Thresholds.CPU.Red}}</td><td>{{.Thresholds.Mem.Amber}} / {{.Thresholds.Mem.Red}}</td><td>{{.Thresholds.Disk.Amber}} / {{.Thresholds.Disk.Red}}</td></tr></table>
<h2>Recording servers ({{len .Servers}})</h2>
<table><tr><th>Name</th><th>Id</th><th>Version</th><th>Video URL</th><th>Online</th><th>Revoked</th><th>Last seen</th></tr>
{{range .Servers}}<tr><td>{{.Name}}</td><td class="mono">{{.ID}}</td><td>{{.Version}}</td><td class="mono">{{.PublicURL}}</td><td>{{yes .Online}}</td><td>{{yes .Revoked}}</td><td>{{when .LastSeenAt}}</td></tr>{{end}}</table>
<h2>Cameras ({{len .Cameras}})</h2>
<table><tr><th>Name</th><th>Location</th><th>Server</th><th>Main stream</th><th>Credentials</th><th>Recording</th><th>Retention</th></tr>
{{range .Cameras}}<tr><td>{{.Name}}</td><td>{{.Location}}</td><td class="mono">{{.Server}}</td><td class="mono">{{.MainStream}}</td><td>{{if .HasCredentials}}set (redacted){{else}}none{{end}}</td><td>{{yes .Recording}}</td><td>{{if .RetentionDays}}{{.RetentionDays}} d{{else}}default{{end}}</td></tr>{{end}}</table>
<h2>Camera groups ({{len .Groups}})</h2>
<table><tr><th>Name</th><th>Cameras</th></tr>{{range .Groups}}<tr><td>{{.Name}}</td><td>{{range $i, $c := .Cameras}}{{if $i}}, {{end}}{{$c}}{{end}}</td></tr>{{end}}</table>
<h2>Users ({{len .Users}})</h2>
<table><tr><th>Name</th><th>Role</th><th>Created</th></tr>{{range .Users}}<tr><td>{{.Name}}</td><td>{{.Role}}</td><td>{{when .CreatedAt}}</td></tr>{{end}}</table>
<h2>Roles ({{len .Roles}})</h2>
<table><tr><th>Name</th><th>Built in</th><th>Description</th><th>Permissions</th></tr>{{range .Roles}}<tr><td>{{.Name}}</td><td>{{yes .Builtin}}</td><td>{{.Description}}</td><td class="mono">{{raw .Permissions}}</td></tr>{{end}}</table>
<h2>Rules ({{len .Rules}})</h2>
<table><tr><th>Name</th><th>On</th><th>Cameras</th><th>Kinds</th><th>Min severity</th><th>Delivery</th><th>Schedule</th></tr>
{{range .Rules}}<tr><td>{{.Name}}</td><td>{{yes .Enabled}}</td><td>{{if .Cameras}}{{.Cameras}}{{else}}all{{end}}</td><td>{{.Kinds}}</td><td>{{.Severity}}</td><td>{{.Delivery}}</td><td>{{.Schedule}}</td></tr>{{end}}</table>
<h2>Time profiles ({{len .TimeProfiles}})</h2>
<table><tr><th>Name</th><th>Definition</th></tr>{{range .TimeProfiles}}<tr><td>{{.Name}}</td><td class="mono">{{raw .Body}}</td></tr>{{end}}</table>
<h2>Webhooks ({{len .Webhooks}})</h2>
<table><tr><th>Name</th><th>Endpoint</th><th>Secret</th><th>Events</th><th>Enabled</th></tr>
{{range .Webhooks}}<tr><td>{{.Name}}</td><td class="mono">{{.Endpoint}}</td><td>{{if .HasSecret}}set (redacted){{else}}none{{end}}</td><td class="mono">{{raw .Events}}</td><td>{{yes .Enabled}}</td></tr>{{end}}</table>
</body></html>`))
