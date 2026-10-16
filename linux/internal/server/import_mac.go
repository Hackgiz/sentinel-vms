package server

// API v2: the Mac importer (PLATFORM.md, "Decision 1" and "Upgrade
// path"). Sentinel Operator for macOS sends what a Sentinel for Mac (1.x)
// install had, once, on first launch after the upgrade:
//
//	POST /api/v2/import/mac                  cameras, users, rules, the
//	                                         evidence index, audit history
//	PUT  /api/v2/import/mac/evidence/{caseId} each evidence file
//
// Both are idempotent. The store remembers what every Mac item became
// (store/import_mac.go), so a re-sent import creates nothing twice and
// reports the repeats in skipped. A malformed payload is refused as a
// whole (400); an item that can't be imported (a bad camera URL, a rule
// naming a camera that wasn't imported, a name already taken) is skipped
// with the reason and the rest goes on.
//
// Imported audit entries join the site chain as ordinary entries (time of
// import, the Mac time in the detail) after one "Imported audit from Mac"
// entry, so the chain verifies as before; see Store.ImportMacAudit.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

// macEvidenceUploadPath prefixes the evidence upload route; csrfGuard
// lets its non-JSON body through for bearer-token requests (cookie
// sessions can't upload).
const macEvidenceUploadPath = "/api/v2/import/mac/evidence/"

func (s *Server) registerV2Import(mux *http.ServeMux) {
	// Admin only: an import creates cameras, users and rules.
	s.v2Route(mux, "POST /api/v2/import/mac", auth.ManageUsers, s.handleMacImport)
	s.v2Route(mux, "PUT /api/v2/import/mac/evidence/{caseId}", auth.ManageUsers, s.handleMacImportEvidence)
}

// Import limits. A Mac install has a few dozen cameras and users; the
// audit history is the bulk of the payload.
const (
	maxMacImportBody     = 64 << 20
	maxMacImportCameras  = 1000
	maxMacImportUsers    = 1000
	maxMacImportRules    = 1000
	maxMacImportEvidence = 100_000
	maxMacImportAudit    = 500_000
	maxImportedEvidence  = 32 << 30 // one evidence file
	maxMacImportText     = 200      // names, ids, roles
	maxMacImportURL      = 2048
	maxMacImportDetail   = 16 << 10
)

var macCaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}$`)

// macImport is MacImport. Required fields are pointers so a missing one
// is told apart from an empty one.
type macImport struct {
	SourceVersion *string              `json:"sourceVersion"`
	Cameras       *[]macImportCamera   `json:"cameras"`
	Users         *[]macImportUser     `json:"users"`
	Rules         *[]macImportRule     `json:"rules"`
	Evidence      *[]macImportEvidence `json:"evidence"`
	Audit         *[]macImportAudit    `json:"audit"`
}

type macImportCamera struct {
	SourceID       *string `json:"sourceId"`
	Name           *string `json:"name"`
	Location       string  `json:"location"`
	RTSPURL        *string `json:"rtspUrl"`
	SubRTSPURL     string  `json:"subRtspUrl"`
	Username       string  `json:"username"`
	Password       *string `json:"password"`
	Recording      *bool   `json:"recording"`
	RetentionDays  int     `json:"retentionDays"`
	MotionLevel    *int    `json:"motionLevel"`
	ONVIFURL       *string `json:"onvifUrl"`
	RecordingsPath string  `json:"recordingsPath"`
}

type macImportUser struct {
	Name         *string `json:"name"`
	Role         *string `json:"role"`
	PasswordHash string  `json:"passwordHash"`
}

// macImportRule is RuleInput, every field required.
type macImportRule struct {
	Name         *string   `json:"name"`
	Enabled      *bool     `json:"enabled"`
	CameraIDs    *[]string `json:"cameraIds"`
	Kinds        *[]string `json:"kinds"`
	MinSeverity  *string   `json:"minSeverity"`
	Schedule     *bool     `json:"schedule"`
	FromMinute   *int      `json:"fromMinute"`
	ToMinute     *int      `json:"toMinute"`
	Days         *[]int    `json:"days"`
	Delivery     *string   `json:"delivery"`
	Instructions *string   `json:"instructions"`
}

type macImportEvidence struct {
	CaseID         *string `json:"caseId"`
	CameraSourceID string  `json:"cameraSourceId"`
	CameraName     *string `json:"cameraName"`
	Title          *string `json:"title"`
	Range          *string `json:"range"`
	Size           *int64  `json:"size"`
	SHA256         *string `json:"sha256"`
	LockedAt       *int64  `json:"lockedAt"`
	LockedBy       *string `json:"lockedBy"`
}

type macImportAudit struct {
	At     *int64  `json:"at"`
	User   *string `json:"user"`
	Area   *string `json:"area"`
	Action *string `json:"action"`
	Detail *string `json:"detail"`
}

// macImportResult is MacImportResult: what this request created, and
// what it didn't and why (repeats of an earlier import included).
type macImportResult struct {
	Cameras   int               `json:"cameras"`
	Users     int               `json:"users"`
	Rules     int               `json:"rules"`
	Evidence  int               `json:"evidence"`
	Audit     int               `json:"audit"`
	Skipped   []string          `json:"skipped"`
	CameraIDs map[string]string `json:"cameraIds"`
}

func (res *macImportResult) skip(format string, args ...any) {
	res.Skipped = append(res.Skipped, fmt.Sprintf(format, args...))
}

// MARK: - Validation

// validate checks the payload against the MacImport schema and the
// limits; any problem refuses the whole import.
func (in *macImport) validate() error {
	switch {
	case in.SourceVersion == nil:
		return inputError{"sourceVersion is required"}
	case in.Cameras == nil, in.Users == nil, in.Rules == nil, in.Evidence == nil, in.Audit == nil:
		return inputError{"cameras, users, rules, evidence and audit are required (send [] for none)"}
	case len(*in.SourceVersion) > maxMacImportText:
		return inputError{"sourceVersion is too long"}
	case len(*in.Cameras) > maxMacImportCameras, len(*in.Users) > maxMacImportUsers, len(*in.Rules) > maxMacImportRules,
		len(*in.Evidence) > maxMacImportEvidence, len(*in.Audit) > maxMacImportAudit:
		return inputError{"too many items in one import"}
	}
	long := func(limit int, vals ...string) bool {
		for _, v := range vals {
			if len(v) > limit {
				return true
			}
		}
		return false
	}
	sources := map[string]bool{}
	for i, c := range *in.Cameras {
		at := fmt.Sprintf("cameras[%d]", i)
		switch {
		case c.SourceID == nil || c.Name == nil || c.RTSPURL == nil:
			return inputError{at + ": sourceId, name and rtspUrl are required"}
		case *c.SourceID == "" || long(maxMacImportText, *c.SourceID, *c.Name, c.Location, c.Username):
			return inputError{at + ": sourceId is empty, or a field is too long"}
		case long(maxMacImportURL, *c.RTSPURL, c.SubRTSPURL, deref(c.Password), deref(c.ONVIFURL)):
			return inputError{at + ": a URL or the password is too long"}
		case sources[*c.SourceID]:
			return inputError{at + ": sourceId " + *c.SourceID + " appears twice"}
		case c.RecordingsPath != "" && (len(c.RecordingsPath) > 4096 || !strings.HasPrefix(c.RecordingsPath, "/") ||
			strings.ContainsRune(c.RecordingsPath, 0) || filepath.Clean(c.RecordingsPath) != c.RecordingsPath):
			return inputError{at + ": recordingsPath must be a clean absolute folder path"}
		}
		sources[*c.SourceID] = true
	}
	names := map[string]bool{}
	for i, u := range *in.Users {
		at := fmt.Sprintf("users[%d]", i)
		switch {
		case u.Name == nil || u.Role == nil:
			return inputError{at + ": name and role are required"}
		case strings.TrimSpace(*u.Name) == "" || long(maxMacImportText, *u.Name, u.PasswordHash):
			return inputError{at + ": name is empty or too long"}
		case !store.ValidRole(*u.Role):
			return inputError{at + ": role must be Admin, Supervisor, Operator or Viewer"}
		case names[strings.ToLower(strings.TrimSpace(*u.Name))]:
			return inputError{at + ": user " + *u.Name + " appears twice"}
		}
		names[strings.ToLower(strings.TrimSpace(*u.Name))] = true
	}
	for i, r := range *in.Rules {
		at := fmt.Sprintf("rules[%d]", i)
		if r.Name == nil || r.Enabled == nil || r.CameraIDs == nil || r.Kinds == nil || r.MinSeverity == nil || r.Schedule == nil ||
			r.FromMinute == nil || r.ToMinute == nil || r.Days == nil || r.Delivery == nil || r.Instructions == nil {
			return inputError{at + ": every RuleInput field is required"}
		}
		if long(maxMacImportText, *r.Name) || long(maxMacImportDetail, *r.Instructions) || len(*r.CameraIDs) > maxMacImportCameras || len(*r.Kinds) > 100 || len(*r.Days) > 7 {
			return inputError{at + ": a field is too long"}
		}
	}
	cases := map[string]bool{}
	for i, e := range *in.Evidence {
		at := fmt.Sprintf("evidence[%d]", i)
		switch {
		case e.CaseID == nil || e.CameraName == nil || e.Title == nil || e.Range == nil || e.Size == nil || e.SHA256 == nil || e.LockedAt == nil || e.LockedBy == nil:
			return inputError{at + ": caseId, cameraName, title, range, size, sha256, lockedAt and lockedBy are required"}
		case !macCaseIDPattern.MatchString(*e.CaseID):
			return inputError{at + ": caseId may hold only letters, digits, '.', '_' and '-' (100 at most)"}
		case !isSHA256Hex(*e.SHA256):
			return inputError{at + ": sha256 must be 64 hex digits"}
		case *e.Size <= 0 || *e.Size > maxImportedEvidence:
			return inputError{at + ": size is out of range"}
		case *e.LockedAt < 0:
			return inputError{at + ": lockedAt must be Unix milliseconds"}
		case long(maxMacImportText, e.CameraSourceID, *e.CameraName, *e.LockedBy) || long(maxMacImportDetail, *e.Title, *e.Range):
			return inputError{at + ": a field is too long"}
		case cases[*e.CaseID]:
			return inputError{at + ": case " + *e.CaseID + " appears twice"}
		}
		cases[*e.CaseID] = true
	}
	for i, a := range *in.Audit {
		at := fmt.Sprintf("audit[%d]", i)
		switch {
		case a.At == nil || a.User == nil || a.Area == nil || a.Action == nil || a.Detail == nil:
			return inputError{at + ": at, user, area, action and detail are required"}
		case *a.At < 0:
			return inputError{at + ": at must be Unix milliseconds"}
		case long(maxMacImportText, *a.User, *a.Area, *a.Action) || long(maxMacImportDetail, *a.Detail):
			return inputError{at + ": a field is too long"}
		}
	}
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// MARK: - POST /import/mac

// macImportMu runs one import at a time, so two sends of the same payload
// can't both create a camera.
var macImportMu sync.Mutex

func (s *Server) handleMacImport(w http.ResponseWriter, r *http.Request) {
	if !s.cameraScope(r.Context()).all {
		writeV2Error(w, http.StatusForbidden, codeForbidden, "only an administrator who sees every camera can import")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMacImportBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var in macImport
	err := dec.Decode(&in)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("send one JSON object")
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeV2Error(w, http.StatusRequestEntityTooLarge, codeBadRequest, fmt.Sprintf("the import is larger than %d MB", maxMacImportBody>>20))
			return
		}
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid request: "+err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if (len(*in.Cameras) > 0 || len(*in.Evidence) > 0) && !s.recorderConnected(store.LocalRecorderID) {
		writeV2Error(w, http.StatusConflict, codeConflict, "this server has no local recording server for the Mac's cameras and evidence")
		return
	}

	macImportMu.Lock()
	defer macImportMu.Unlock()
	task := tasks.Default.Start(tasks.KindImport, "Mac import (Sentinel for Mac "+*in.SourceVersion+")")
	defer task.Finish()
	res := &macImportResult{Skipped: []string{}, CameraIDs: map[string]string{}}
	s.importMacCameras(r, *in.Cameras, res)
	task.Progress(20)
	s.importMacUsers(r, *in.Users, res)
	task.Progress(35)
	s.importMacRules(r, *in.Rules, res)
	task.Progress(50)
	s.importMacEvidence(r, *in.Evidence, res)
	task.Progress(85)
	s.importMacAudit(r, *in.SourceVersion, *in.Audit, res)
	s.audit(r, "Import", "Imported from Mac", fmt.Sprintf("Sentinel for Mac %s: %d cameras, %d users, %d rules, %d evidence entries, %d audit entries; %d skipped",
		*in.SourceVersion, res.Cameras, res.Users, res.Rules, res.Evidence, res.Audit, len(res.Skipped)))
	writeJSON(w, http.StatusOK, res)
}

// importedCamera is the camera a Mac camera became in an earlier import,
// if it still exists.
func (s *Server) importedCamera(ctx context.Context, sourceID string) (*store.Camera, bool) {
	id, ok := s.Store.MacImportRef(ctx, store.MacImportCamera, sourceID)
	if !ok {
		return nil, false
	}
	c, err := s.Store.Camera(ctx, id)
	return c, err == nil
}

// importMacCameras adds the Mac's cameras to the local recording server,
// then sends it the new configuration once.
func (s *Server) importMacCameras(r *http.Request, cams []macImportCamera, res *macImportResult) {
	ctx := r.Context()
	apply := false
	for _, c := range cams {
		name := strings.TrimSpace(*c.Name)
		if c.RecordingsPath != "" {
			// The local recording server runs here, as this process's user:
			// say so now if it can't read the Mac's folder. It tries again
			// each time it gets its configuration (and on a re-import).
			if _, err := os.ReadDir(c.RecordingsPath); err != nil {
				res.skip("camera %q: its recordings in %s aren't readable by the server (%v); give the server's account read access, then import again", name, c.RecordingsPath, errors.Unwrap(err))
			}
		}
		if cam, ok := s.importedCamera(ctx, *c.SourceID); ok {
			res.CameraIDs[*c.SourceID] = cam.ID
			res.skip("camera %q: already imported", name)
			apply = apply || c.RecordingsPath != ""
			continue
		}
		in := store.CameraInput{
			RecorderID: store.LocalRecorderID, Name: name, Location: c.Location, RTSPURL: *c.RTSPURL, SubRTSPURL: c.SubRTSPURL,
			Username: c.Username, Password: c.Password, Recording: c.Recording == nil || *c.Recording, RetentionDays: c.RetentionDays,
			MotionLevel: c.MotionLevel, ONVIFURL: c.ONVIFURL,
		}
		if err := normalizeCamera(&in); err != nil {
			res.skip("camera %q: %v", name, err)
			continue
		}
		cam, err := s.Store.SaveCamera(ctx, "", in)
		if err != nil {
			res.skip("camera %q: %v", name, err)
			continue
		}
		if err := s.Store.SetMacImportRef(ctx, store.MacImportCamera, *c.SourceID, cam.ID); err != nil {
			_ = s.Store.DeleteCamera(ctx, cam.ID) // unrecorded, it would be imported again
			res.skip("camera %q: %v", name, err)
			continue
		}
		detail := cam.Name + " from the Mac"
		if c.RecordingsPath != "" {
			if err := s.Store.SetMacImportRef(ctx, store.MacImportRecordings, cam.ID, c.RecordingsPath); err != nil {
				res.skip("camera %q: recordings folder not kept: %v", name, err)
			} else {
				detail += ", with its recordings in " + c.RecordingsPath
			}
		}
		s.audit(r, "Cameras", "Imported camera", detail)
		res.Cameras++
		res.CameraIDs[*c.SourceID] = cam.ID
		apply = true
	}
	if apply {
		if err := s.ApplyMedia(ctx); err != nil {
			slog.Error("media apply failed", "err", err)
		}
	}
}

// importedRecordings is each camera's Mac recordings folder, for the
// snapshot of recording server id: only the local one runs on the Mac
// that had them.
func (s *Server) importedRecordings(ctx context.Context, id string) map[string]string {
	if id != store.LocalRecorderID {
		return nil
	}
	refs, err := s.Store.MacImportRefs(ctx, store.MacImportRecordings)
	if err != nil {
		slog.Error("imported recordings folders unreadable", "err", err)
	}
	return refs
}

// macPasswordHash is the hash an imported user signs in with: the Mac's
// bcrypt hash, or (mustSet) one no password matches, until an Admin sets
// a password.
func macPasswordHash(h string) (hash string, mustSet bool) {
	if strings.HasPrefix(h, "$2") && len(h) == 60 {
		if _, err := bcrypt.Cost([]byte(h)); err == nil {
			return h, false
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "!mac-import:" + hex.EncodeToString(b), true
}

func (s *Server) importMacUsers(r *http.Request, users []macImportUser, res *macImportResult) {
	ctx := r.Context()
	for _, in := range users {
		name := strings.TrimSpace(*in.Name)
		key := strings.ToLower(name)
		if u, err := s.Store.UserByName(ctx, name); err == nil {
			if id, ok := s.Store.MacImportRef(ctx, store.MacImportUser, key); ok && id == u.ID {
				res.skip("user %q: already imported", name)
			} else {
				res.skip("user %q: this server already has a user with that name; kept it", name)
			}
			continue
		}
		hash, mustSet := macPasswordHash(in.PasswordHash)
		u, err := s.Store.CreateUser(ctx, name, *in.Role, hash)
		if err != nil {
			res.skip("user %q: %v", name, err)
			continue
		}
		if err := s.Store.SetMacImportRef(ctx, store.MacImportUser, key, u.ID); err != nil {
			_ = s.Store.DeleteUser(ctx, u.ID)
			res.skip("user %q: %v", name, err)
			continue
		}
		detail := u.Name + " (" + u.Role + ")"
		if mustSet {
			detail += ", needs a password from an Admin"
			res.skip("user %q: password not imported; an Admin must set one", name)
		}
		s.audit(r, "Users", "Imported user", detail)
		res.Users++
	}
}

// importedCameraID maps a camera id from a Mac rule or evidence entry: a
// Mac camera imported now or earlier, or a camera already on this server.
func (s *Server) importedCameraID(ctx context.Context, id string, res *macImportResult) (string, bool) {
	if cid, ok := res.CameraIDs[id]; ok {
		return cid, true
	}
	if c, ok := s.importedCamera(ctx, id); ok {
		return c.ID, true
	}
	if _, err := s.Store.Camera(ctx, id); err == nil {
		return id, true
	}
	return "", false
}

func (s *Server) importMacRules(r *http.Request, list []macImportRule, res *macImportResult) {
	ctx := r.Context()
	existing, err := s.Store.AlarmRules(ctx)
	if err != nil {
		res.skip("rules: %v", err)
		return
	}
	taken := map[string]bool{}
	for _, ru := range existing {
		taken[strings.ToLower(ru.Name)] = true
	}
	added := false
next:
	for _, in := range list {
		name := strings.TrimSpace(*in.Name)
		key := strings.ToLower(name)
		if id, ok := s.Store.MacImportRef(ctx, store.MacImportRule, key); ok {
			if _, err := s.Store.AlarmRule(ctx, id); err == nil {
				res.skip("rule %q: already imported", name)
				continue
			}
		}
		if taken[key] {
			res.skip("rule %q: this server already has a rule with that name; kept it", name)
			continue
		}
		// A rule limited to cameras that weren't imported must not become a
		// rule for every camera, so it is skipped instead.
		cams := make([]string, 0, len(*in.CameraIDs))
		for _, id := range *in.CameraIDs {
			cid, ok := s.importedCameraID(ctx, id, res)
			if !ok {
				res.skip("rule %q: its camera %s wasn't imported", name, id)
				continue next
			}
			cams = append(cams, cid)
		}
		rule := rules.Rule{
			Name: name, Enabled: *in.Enabled, CameraIDs: cams, Kinds: *in.Kinds, MinSeverity: *in.MinSeverity,
			Schedule: *in.Schedule, FromMinute: *in.FromMinute, ToMinute: *in.ToMinute, Days: *in.Days,
			Delivery: *in.Delivery, Instructions: *in.Instructions,
		}
		rule.Normalize()
		if err := rule.Validate(); err != nil {
			res.skip("rule %q: %v", name, err)
			continue
		}
		if err := s.Store.SaveAlarmRule(ctx, &rule); err != nil {
			res.skip("rule %q: %v", name, err)
			continue
		}
		if err := s.Store.SetMacImportRef(ctx, store.MacImportRule, key, rule.ID); err != nil {
			_ = s.Store.DeleteAlarmRule(ctx, rule.ID)
			res.skip("rule %q: %v", name, err)
			continue
		}
		taken[key] = true
		added = true
		s.audit(r, "Alarm Rules", "Imported rule", rule.Name+" — "+describeRule(rule))
		res.Rules++
	}
	if added {
		s.invalidateRules()
	}
}

// importMacEvidence files the Mac's evidence index; each file follows
// with PUT /import/mac/evidence/{caseId} into the vault of the recording
// server that has the camera (the local one; it holds the Mac's
// cameras).
func (s *Server) importMacEvidence(r *http.Request, list []macImportEvidence, res *macImportResult) {
	ctx := r.Context()
	cases := map[string]bool{}
	for _, in := range list {
		cases[*in.CaseID] = true
	}
	for _, in := range list {
		caseID := *in.CaseID
		if ev, err := s.Store.EvidenceByCaseID(ctx, caseID); err == nil {
			if id, ok := s.Store.MacImportRef(ctx, store.MacImportEvidence, caseID); ok && id == ev.ID {
				res.skip("evidence %s: already imported", caseID)
			} else {
				res.skip("evidence %s: this server already has a case with that id", caseID)
			}
			continue
		}
		camID, rid := "", store.LocalRecorderID
		if in.CameraSourceID != "" {
			if cid, ok := s.importedCameraID(ctx, in.CameraSourceID, res); ok {
				camID = cid
				if c, err := s.Store.Camera(ctx, cid); err == nil {
					rid = c.RecorderID
				}
			}
		}
		ev := &store.Evidence{
			ID: store.NewID(), CaseID: caseID, CameraID: camID, CameraName: *in.CameraName, Title: *in.Title, Range: *in.Range,
			File: caseID + ".mp4", Size: *in.Size, SHA256: strings.ToLower(*in.SHA256), LockedAt: *in.LockedAt / 1000, LockedBy: *in.LockedBy,
		}
		// Sentinel for Mac could lock several clips per case; the importer
		// sends the extra ones as <case>-2, <case>-3, … The title says
		// which case they belong to.
		if base, n, ok := extraClip(caseID); ok && (cases[base] || s.caseExists(ctx, base)) {
			ev.Title += " (clip " + n + " of case " + base + ")"
		}
		if err := s.Store.AddEvidenceOn(ctx, ev, rid); err != nil {
			res.skip("evidence %s: %v", caseID, err)
			continue
		}
		if err := s.Store.SetMacImportRef(ctx, store.MacImportEvidence, caseID, ev.ID); err != nil {
			_ = s.Store.DeleteImportedEvidence(ctx, ev.ID)
			res.skip("evidence %s: %v", caseID, err)
			continue
		}
		s.audit(r, "Evidence", "Imported evidence", caseID+" ("+ev.CameraName+"), locked by "+ev.LockedBy+" on the Mac, sha256 "+ev.SHA256[:16]+"…; file to follow")
		res.Evidence++
	}
}

// extraClip splits an extra clip's case id, "<case>-<n>" (n ≥ 2).
func extraClip(caseID string) (base, n string, ok bool) {
	i := strings.LastIndexByte(caseID, '-')
	if i <= 0 {
		return "", "", false
	}
	k, err := strconv.Atoi(caseID[i+1:])
	if err != nil || k < 2 || strconv.Itoa(k) != caseID[i+1:] {
		return "", "", false
	}
	return caseID[:i], caseID[i+1:], true
}

func (s *Server) caseExists(ctx context.Context, caseID string) bool {
	_, err := s.Store.EvidenceByCaseID(ctx, caseID)
	return err == nil
}

func (s *Server) importMacAudit(r *http.Request, version string, list []macImportAudit, res *macImportResult) {
	if len(list) == 0 {
		return
	}
	entries := make([]store.MacAuditEntry, 0, len(list))
	for _, a := range list {
		entries = append(entries, store.MacAuditEntry{At: *a.At, User: *a.User, Area: *a.Area, Action: *a.Action, Detail: *a.Detail})
	}
	user := "System"
	if u := currentUser(r); u != nil {
		user = u.Name
	}
	n, err := s.Store.ImportMacAudit(r.Context(), user, version, entries)
	if err != nil {
		res.skip("audit: %d entries not imported: %v", len(entries), err)
		return
	}
	res.Audit = n
	if n < len(entries) {
		res.skip("audit: %d entries already imported", len(entries)-n)
	}
}

// MARK: - PUT /import/mac/evidence/{caseId}

// macUploads holds the cases being uploaded right now.
var macUploads = struct {
	sync.Mutex
	busy map[string]bool
}{busy: map[string]bool{}}

// handleMacImportEvidence streams one imported evidence file to the
// recording server that holds its case, in link.ImportChunk pieces. The
// body must be exactly the file the import listed (size and SHA-256) and
// look like an MP4; the recording server checks it again before it takes
// the file into its vault. Sending a file that is already there answers
// 204 without storing anything twice.
func (s *Server) handleMacImportEvidence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.cameraScope(ctx).all {
		writeV2Error(w, http.StatusForbidden, codeForbidden, "only an administrator who sees every camera can import")
		return
	}
	caseID := r.PathValue("caseId")
	if !macCaseIDPattern.MatchString(caseID) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "bad case id")
		return
	}
	ev, err := s.Store.EvidenceByCaseID(ctx, caseID)
	if err == nil {
		if id, ok := s.Store.MacImportRef(ctx, store.MacImportEvidence, caseID); !ok || id != ev.ID {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "no imported evidence with case id "+caseID+"; import its entry first")
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != ev.Size {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, fmt.Sprintf("the body must be the whole file, %d bytes", ev.Size))
		return
	}

	macUploads.Lock()
	if macUploads.busy[caseID] {
		macUploads.Unlock()
		writeV2Error(w, http.StatusConflict, codeConflict, "case "+caseID+" is being uploaded already")
		return
	}
	macUploads.busy[caseID] = true
	macUploads.Unlock()
	defer func() {
		macUploads.Lock()
		delete(macUploads.busy, caseID)
		macUploads.Unlock()
	}()

	if sum, ok := s.Store.MacImportRef(ctx, store.MacImportUploaded, caseID); ok && sum == ev.SHA256 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rid, err := s.Store.EvidenceRecorderID(ctx, ev.ID)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	if !s.recorderConnected(rid) {
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the recording server for this evidence isn't connected")
		return
	}

	done, err := s.uploadMacEvidence(ctx, rid, ev, http.MaxBytesReader(w, r.Body, ev.Size))
	if err != nil {
		var tooBig *http.MaxBytesError
		var bad inputError
		switch {
		case errors.As(err, &tooBig):
			writeV2Error(w, http.StatusRequestEntityTooLarge, codeBadRequest, fmt.Sprintf("the body is longer than the file, %d bytes", ev.Size))
		case errors.As(err, &bad):
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		case commandCode(err) == link.CodeExists:
			writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
		default:
			writeV2Err(w, err)
		}
		return
	}
	if err := s.Store.SetMacImportRef(ctx, store.MacImportUploaded, caseID, ev.SHA256); err != nil {
		slog.Error("imported evidence upload not recorded", "case", caseID, "err", err)
	}
	how := "uploaded"
	if done {
		how = "was already in the vault"
	}
	s.audit(r, "Evidence", "Imported evidence file", caseID+" "+how+" on "+s.recorderName(ctx, rid)+", sha256 "+ev.SHA256[:16]+"…")
	w.WriteHeader(http.StatusNoContent)
}

// uploadMacEvidence sends body to recording server rid as ev's vault
// file. already reports that the vault had the very same file. On any
// failure the partial upload is dropped.
func (s *Server) uploadMacEvidence(ctx context.Context, rid string, ev *store.Evidence, body io.Reader) (already bool, err error) {
	args := link.EvidenceImportArgs{CaseID: ev.CaseID, Size: ev.Size, SHA256: ev.SHA256}
	step := func(op string, off int64, data []byte) (link.EvidenceImportResult, error) {
		a := args
		a.Op, a.Offset, a.Data = op, off, data
		var res link.EvidenceImportResult
		err := s.command(ctx, rid, link.CmdEvidenceImport, a, &res)
		return res, err
	}
	started := false
	defer func() {
		if err != nil && started {
			// The request may be gone; drop the part file regardless.
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			a := args
			a.Op = link.ImportAbort
			_ = s.command(actx, rid, link.CmdEvidenceImport, a, nil)
		}
	}()
	h := sha256.New()
	buf := make([]byte, link.ImportChunk)
	var off int64
	for {
		n, rerr := io.ReadFull(body, buf)
		if n > 0 {
			if off == 0 && !looksLikeMP4(buf[:n]) {
				return false, inputError{"the body isn't an MP4 file"}
			}
			h.Write(buf[:n])
			started = true
			res, err := step(link.ImportWrite, off, buf[:n])
			if err != nil {
				return false, err
			}
			if res.Done {
				return true, nil
			}
			off += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			var tooBig *http.MaxBytesError
			if errors.As(rerr, &tooBig) {
				return false, rerr
			}
			return false, inputError{"the upload was interrupted: " + rerr.Error()}
		}
	}
	if off != ev.Size {
		return false, inputError{fmt.Sprintf("got %d bytes; the imported entry says the file has %d", off, ev.Size)}
	}
	if hex.EncodeToString(h.Sum(nil)) != ev.SHA256 {
		return false, inputError{"the file doesn't match the SHA-256 from the import"}
	}
	res, err := step(link.ImportCommit, 0, nil)
	if err != nil {
		return false, err
	}
	if !res.Done {
		return false, errors.New("the recording server didn't take the file")
	}
	return false, nil
}

// looksLikeMP4 checks the start of a file for an ISO base media file's
// first box (ftyp).
func looksLikeMP4(b []byte) bool {
	return len(b) >= 8 && string(b[4:8]) == "ftyp"
}
