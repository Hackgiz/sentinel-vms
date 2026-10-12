package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
)

const (
	macFrontDoor = "6F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"
	macCase      = "HG-20260301-00001"
)

// macEvidenceFile is an MP4 a bit over one upload chunk, so an upload
// takes several link commands.
func macEvidenceFile() []byte {
	b := playback.SynthSegment(4, 2)
	for len(b) <= link.ImportChunk {
		b = append(b, b...)
	}
	return b[:link.ImportChunk+1234]
}

// macFixture is testdata/mac_import.json with its placeholders filled:
// the Front door camera's Mac recordings folder (one segment in it),
// alice's bcrypt hash and the evidence file's size and hash.
func macFixture(t *testing.T, evidence []byte) (body, recordings string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/mac_import.json")
	if err != nil {
		t.Fatal(err)
	}
	recordings = filepath.Join(t.TempDir(), "Recordings", macFrontDoor)
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(recordings, time.Now().Add(-2*time.Hour).Format("20060102-150405")+"-000000.mp4")
	if err := os.WriteFile(seg, playback.SynthSegment(60, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(-2*time.Hour + time.Minute)
	_ = os.Chtimes(seg, end, end)
	hash, _ := auth.HashPassword("alice's mac password")
	sum := sha256.Sum256(evidence)
	body = strings.NewReplacer(
		"$RECORDINGS", recordings,
		"$ALICE_HASH", hash,
		"$EVIDENCE_SIZE", strconv.Itoa(len(evidence)),
		"$EVIDENCE_SHA", hex.EncodeToString(sum[:]),
	).Replace(string(raw))
	return body, recordings
}

func macImportCall(t *testing.T, h http.Handler, token, body string) (int, map[string]any) {
	t.Helper()
	return v2Call(t, h, "POST", "/api/v2/import/mac", token, body)
}

func putEvidence(t *testing.T, h http.Handler, token, caseID string, body []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/v2/import/mac/evidence/"+caseID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func skippedHas(m map[string]any, sub string) bool {
	list, _ := m["skipped"].([]any)
	for _, v := range list {
		if s, _ := v.(string); strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func verifyAudit(t *testing.T, s *Server) int {
	t.Helper()
	n, broken, err := s.Store.VerifyAudit(context.Background())
	if err != nil || broken != "" {
		t.Fatalf("audit chain broken at %q (%v)", broken, err)
	}
	site, servers, err := s.Store.VerifyAuditChains(context.Background())
	if err != nil || site.BrokenID != "" {
		t.Fatalf("site chain %+v (%v)", site, err)
	}
	for _, c := range servers {
		if c.BrokenID != "" {
			t.Fatalf("server chain %+v", c)
		}
	}
	return n
}

func TestMacImport(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "admin", "Admin")
	evidence := macEvidenceFile()
	body, recordings := macFixture(t, evidence)
	macFiles, _ := os.ReadDir(recordings)

	code, m := macImportCall(t, h, admin, body)
	if code != 200 {
		t.Fatalf("import: %d %v", code, m)
	}
	for k, want := range map[string]float64{"cameras": 2, "users": 3, "rules": 2, "evidence": 2, "audit": 3} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v (%v)", k, m[k], want, m)
		}
	}
	for _, want := range []string{`camera "Broken"`, `rule "Broken camera rule"`, `camera "Garage": its recordings in /nonexistent/sentinel-test/garage aren't readable`, `user "bob": password not imported`,
		`user "carol": password not imported`, `user "admin": this server already has`} {
		if !skippedHas(m, want) {
			t.Errorf("skipped lacks %q: %v", want, m["skipped"])
		}
	}
	ids, _ := m["cameraIds"].(map[string]any)
	if len(ids) != 2 {
		t.Fatalf("cameraIds %v", ids)
	}
	front, _ := ids[macFrontDoor].(string)

	// Cameras: on the local server, credentials out of the URL and sealed.
	c, err := s.Store.Camera(ctx, front)
	if err != nil || c.Name != "Front door" || c.RecorderID != store.LocalRecorderID || c.Username != "admin" ||
		strings.Contains(c.RTSPURL, "secret") || c.RetentionDays != 30 || c.MotionLevel != 3 || !c.Recording {
		t.Fatalf("front door %+v (%v)", c, err)
	}
	if pass, _ := s.Store.CameraPassword(c); pass != "secret" {
		t.Fatalf("password %q", pass)
	}
	garage, _ := s.Store.Camera(ctx, ids["7A2B3C4D-5E6F-4A7B-8C9D-0E1F2A3B4C5D"].(string))
	if garage == nil || garage.Recording || garage.Username != "viewer" {
		t.Fatalf("garage %+v", garage)
	}

	// The Mac's recordings are listed for the camera, and the Mac folder
	// is untouched.
	code, segs := v2Call(t, h, "GET", "/api/v2/cameras/"+front+"/recordings", admin, "")
	if code != 200 || len(items(t, segs)) != 1 || items(t, segs)[0]["name"] != macFiles[0].Name() {
		t.Fatalf("recordings: %d %v", code, segs)
	}
	if after, _ := os.ReadDir(recordings); len(after) != len(macFiles) {
		t.Fatalf("Mac folder changed: %v", after)
	}

	// Users: alice keeps her Mac password; bob and carol must get one.
	v2SignInExisting(t, s, "alice", "alice's mac password")
	if code, _ := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"bob","password":"anything at all","client":"test"}`); code != 401 {
		t.Fatalf("bob signed in without a password: %d", code)
	}
	bob, _ := s.Store.UserByName(ctx, "bob")
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+bob.ID, admin, `{"password":"a new password"}`); code != 200 {
		t.Fatalf("set bob's password: %d %v", code, m)
	}
	v2SignInExisting(t, s, "bob", "a new password")

	// Rules: the Mac camera id became the new one.
	list, _ := s.Store.AlarmRules(ctx)
	var after *string
	for _, r := range list {
		if r.Name == "Front door after hours" {
			after = &r.CameraIDs[0]
			if len(r.CameraIDs) != 1 || r.Delivery != "push" || r.MinSeverity != "Warning" {
				t.Fatalf("rule %+v", r)
			}
		}
	}
	if after == nil || *after != front {
		t.Fatalf("rules %+v", list)
	}

	// Evidence index: filed on the camera's server, file to follow.
	ev, err := s.Store.EvidenceByCaseID(ctx, macCase)
	if err != nil || ev.CameraID != front || ev.Size != int64(len(evidence)) || ev.LockedAt != 1772399642 || ev.LockedBy != "alice" {
		t.Fatalf("evidence %+v (%v)", ev, err)
	}

	// An extra clip of the case keeps its own case id and names its case.
	if extra, err := s.Store.EvidenceByCaseID(ctx, macCase+"-2"); err != nil || !strings.HasSuffix(extra.Title, "(clip 2 of case "+macCase+")") {
		t.Fatalf("extra clip %+v (%v)", extra, err)
	}

	// Audit: the Mac entries joined the chain, which still verifies.
	entries, _ := s.Store.AuditLog(ctx, 100)
	var macEntries, header int
	for _, e := range entries {
		if strings.HasPrefix(e.ID, "MAC-") {
			macEntries++
			if !strings.Contains(e.Detail, "(on the Mac at 2026-03-01 ") {
				t.Errorf("detail %q", e.Detail)
			}
		}
		if e.Action == "Imported audit from Mac" {
			header++
		}
	}
	if macEntries != 3 || header != 1 {
		t.Fatalf("%d Mac entries, %d headers", macEntries, header)
	}
	for _, a := range []string{"Imported camera"} {
		if !auditActions(t, s, "Cameras")[a] {
			t.Errorf("no %q audit entry", a)
		}
	}
	if !auditActions(t, s, "Users")["Imported user"] || !auditActions(t, s, "Alarm Rules")["Imported rule"] ||
		!auditActions(t, s, "Evidence")["Imported evidence"] || !auditActions(t, s, "Import")["Imported from Mac"] {
		t.Fatal("import actions not audited")
	}
	n := verifyAudit(t, s)

	// Re-sending the same import creates nothing.
	cams, _ := s.Store.Cameras(ctx)
	users, _ := s.Store.Users(ctx)
	code, m = macImportCall(t, h, admin, body)
	if code != 200 {
		t.Fatalf("re-import: %d %v", code, m)
	}
	for _, k := range []string{"cameras", "users", "rules", "evidence", "audit"} {
		if m[k] != float64(0) {
			t.Errorf("re-import %s = %v", k, m[k])
		}
	}
	for _, want := range []string{`camera "Front door": already imported`, `user "alice": already imported`,
		`rule "Everything": already imported`, `evidence ` + macCase + `: already imported`, `audit: 3 entries already imported`} {
		if !skippedHas(m, want) {
			t.Errorf("re-import skipped lacks %q: %v", want, m["skipped"])
		}
	}
	if ids2, _ := m["cameraIds"].(map[string]any); ids2[macFrontDoor] != front {
		t.Fatalf("re-import cameraIds %v", ids2)
	}
	cams2, _ := s.Store.Cameras(ctx)
	users2, _ := s.Store.Users(ctx)
	list2, _ := s.Store.AlarmRules(ctx)
	if len(cams2) != len(cams) || len(users2) != len(users) || len(list2) != len(list) {
		t.Fatalf("re-import duplicated: %d/%d cameras, %d/%d users, %d/%d rules", len(cams2), len(cams), len(users2), len(users), len(list2), len(list))
	}
	if n2 := verifyAudit(t, s); n2 != n+1 { // only the "Imported from Mac" summary
		t.Fatalf("audit grew by %d on re-import", n2-n)
	}
}

func TestMacImportEvidenceUpload(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "admin", "Admin")
	evidence := macEvidenceFile()
	body, _ := macFixture(t, evidence)
	if code, m := macImportCall(t, h, admin, body); code != 200 {
		t.Fatalf("import: %d %v", code, m)
	}
	vault := recOf(s).EvidenceDir()

	if code, m := putEvidence(t, h, admin, "HG-NOPE", evidence); code != 404 {
		t.Fatalf("unknown case: %d %v", code, m)
	}
	if code, m := putEvidence(t, h, admin, macCase, evidence[:100]); code != 400 {
		t.Fatalf("short body: %d %v", code, m)
	}
	notMP4 := bytes.Repeat([]byte{'x'}, len(evidence))
	if code, m := putEvidence(t, h, admin, macCase, notMP4); code != 400 || !strings.Contains(m["error"].(map[string]any)["message"].(string), "MP4") {
		t.Fatalf("not an MP4: %d %v", code, m)
	}
	wrong := append([]byte{}, evidence...)
	wrong[len(wrong)-1] ^= 0xff
	if code, m := putEvidence(t, h, admin, macCase, wrong); code != 400 {
		t.Fatalf("wrong content: %d %v", code, m)
	}
	// A cookie session can't send a file body (CSRF rules); a bearer token can.
	webAdmin := signIn(t, s, "webadmin", "Admin")
	req := httptest.NewRequest("PUT", "/api/v2/import/mac/evidence/"+macCase, bytes.NewReader(evidence))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.AddCookie(webAdmin)
	if rec := httptest.NewRecorder(); func() int { h.ServeHTTP(rec, req); return rec.Code }() != 415 {
		t.Fatal("cookie upload accepted")
	}
	if code, m := putEvidence(t, h, admin, macCase, evidence); code != 204 {
		t.Fatalf("upload: %d %v", code, m)
	}
	if code, m := putEvidence(t, h, admin, macCase+"-2", evidence); code != 204 {
		t.Fatalf("extra clip upload: %d %v", code, m)
	}
	got, err := os.ReadFile(filepath.Join(vault, macCase+".mp4"))
	if err != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("vault file: %d bytes (%v)", len(got), err)
	}
	// Served like any evidence file.
	ev, _ := s.Store.EvidenceByCaseID(ctx, macCase)
	web := signIn(t, s, "web", "Admin")
	if rec := doWeb(t, h, web, "GET", "/api/v1/evidence/"+ev.ID+"/file", ""); rec.Code != 200 || rec.Header().Get("X-Content-SHA256") != ev.SHA256 || !bytes.Equal(rec.Body.Bytes(), evidence) {
		t.Fatalf("evidence file: %d", rec.Code)
	}
	// The same file again stores nothing twice.
	if code, m := putEvidence(t, h, admin, macCase, evidence); code != 204 {
		t.Fatalf("re-upload: %d %v", code, m)
	}
	files, _ := os.ReadDir(vault)
	if len(files) != 2 || files[0].Name() != macCase+"-2.mp4" || files[1].Name() != macCase+".mp4" {
		t.Fatalf("vault holds %v", files)
	}
	if !auditActions(t, s, "Evidence")["Imported evidence file"] {
		t.Fatal("upload not audited")
	}
	verifyAudit(t, s)
}

// TestMacImportEvidenceOnItsServer: evidence for a camera that is on
// another recording server goes to that server's vault.
func TestMacImportEvidenceOnItsServer(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "admin", "Admin")
	rs2 := addRecorder(t, s, "rs2", "Warehouse")
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Dock", RTSPURL: "rtsp://10.0.0.9/s", RecorderID: "rs2"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := macEvidenceFile()
	sum := sha256.Sum256(evidence)
	body := `{"sourceVersion":"1.1.2","cameras":[],"users":[],"rules":[],"audit":[],"evidence":[{"caseId":"HG-1","cameraSourceId":"` + cam.ID +
		`","cameraName":"Dock","title":"t","range":"r","size":` + strconv.Itoa(len(evidence)) + `,"sha256":"` + hex.EncodeToString(sum[:]) +
		`","lockedAt":1772399642000,"lockedBy":"x"}]}`
	if code, m := macImportCall(t, h, admin, body); code != 200 || m["evidence"] != float64(1) {
		t.Fatalf("import: %d %v", code, m)
	}
	if code, m := putEvidence(t, h, admin, "HG-1", evidence); code != 204 {
		t.Fatalf("upload: %d %v", code, m)
	}
	if got, err := os.ReadFile(filepath.Join(rs2.EvidenceDir(), "HG-1.mp4")); err != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("rs2 vault: %d bytes (%v)", len(got), err)
	}
	if _, err := os.Stat(filepath.Join(recOf(s).EvidenceDir(), "HG-1.mp4")); err == nil {
		t.Fatal("file also landed on the local server")
	}

	// With rs2 gone the upload can't be stored; it answers so.
	s.ConnectRecorder("rs2", nil)
	_ = s.Store.SetMacImportRef(ctx, store.MacImportUploaded, "HG-1", "")
	if code, m := putEvidence(t, h, admin, "HG-1", evidence); code != 503 {
		t.Fatalf("offline server: %d %v", code, m)
	}
}

func TestMacImportValidation(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	empty := `"cameras":[],"users":[],"rules":[],"evidence":[],"audit":[]`
	for _, bad := range []string{
		`{` + empty + `}`, // no sourceVersion
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[],"evidence":[]}`,                                       // no audit
		`{"sourceVersion":"1",` + empty + `,"groups":[]}`,                                                              // unknown field
		`{"sourceVersion":"1",` + empty + `}{}`,                                                                        // two objects
		`{"sourceVersion":"1","cameras":[{"sourceId":"a","name":"x"}],"users":[],"rules":[],"evidence":[],"audit":[]}`, // no rtspUrl
		`{"sourceVersion":"1","cameras":[{"sourceId":"a","name":"x","rtspUrl":"rtsp://h/s"},{"sourceId":"a","name":"y","rtspUrl":"rtsp://h/t"}],"users":[],"rules":[],"evidence":[],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[{"sourceId":"a","name":"x","rtspUrl":"rtsp://h/s","recordingsPath":"relative/dir"}],"users":[],"rules":[],"evidence":[],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[{"sourceId":"a","name":"x","rtspUrl":"rtsp://h/s","color":"red"}],"users":[],"rules":[],"evidence":[],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[{"name":"x","role":"Root"}],"rules":[],"evidence":[],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[{"name":"x","role":"Viewer"},{"name":"X","role":"Viewer"}],"rules":[],"evidence":[],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[{"name":"r","enabled":true}],"evidence":[],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[],"evidence":[{"caseId":"../etc","cameraName":"c","title":"t","range":"r","size":1,"sha256":"` + strings.Repeat("a", 64) + `","lockedAt":1,"lockedBy":"x"}],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[],"evidence":[{"caseId":"C1","cameraName":"c","title":"t","range":"r","size":1,"sha256":"nothex","lockedAt":1,"lockedBy":"x"}],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[],"evidence":[{"caseId":"C1","cameraName":"c","title":"t","range":"r","size":0,"sha256":"` + strings.Repeat("a", 64) + `","lockedAt":1,"lockedBy":"x"}],"audit":[]}`,
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[],"evidence":[],"audit":[{"at":1,"user":"u","area":"a","action":"x"}]}`,
		`{"sourceVersion":"1","cameras":[],"users":[],"rules":[],"evidence":[],"audit":[{"at":"yesterday","user":"u","area":"a","action":"x","detail":""}]}`,
	} {
		if code, m := macImportCall(t, h, admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}
	if n, _ := s.Store.Cameras(context.Background()); len(n) != 0 {
		t.Fatalf("a refused import created %d cameras", len(n))
	}

	// Larger than the limit: 413 without reading it all.
	big := io.MultiReader(strings.NewReader(`{"sourceVersion":"`), io.LimitReader(repeatReader('a'), maxMacImportBody), strings.NewReader(`"}`))
	req := httptest.NewRequest("POST", "/api/v2/import/mac", big)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("oversize: %d %s", rec.Code, rec.Body)
	}
}

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestMacImportRefused(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	evidence := macEvidenceFile()
	body, _ := macFixture(t, evidence)
	for _, role := range []string{"Supervisor", "Operator", "Viewer"} {
		tok := v2Token(t, s, "u"+role, role)
		if code, m := macImportCall(t, h, tok, body); code != 403 || errCode(m) != "forbidden" {
			t.Fatalf("%s import: %d %v", role, code, m)
		}
		if code, m := putEvidence(t, h, tok, macCase, evidence); code != 403 {
			t.Fatalf("%s upload: %d %v", role, code, m)
		}
	}
	if code, _ := macImportCall(t, h, "", body); code != 401 {
		t.Fatalf("signed out: %d", code)
	}
	if n, _ := s.Store.Cameras(context.Background()); len(n) != 0 {
		t.Fatal("refused import created cameras")
	}

	// Cameras and evidence need the local recording server.
	admin := v2Token(t, s, "admin", "Admin")
	s.ConnectRecorder(store.LocalRecorderID, nil)
	if code, m := macImportCall(t, h, admin, body); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("no local server: %d %v", code, m)
	}
	if code, m := macImportCall(t, h, admin, `{"sourceVersion":"1","cameras":[],"users":[{"name":"z","role":"Viewer"}],"rules":[],"evidence":[],"audit":[]}`); code != 200 || m["users"] != float64(1) {
		t.Fatalf("users only, no local server: %d %v", code, m)
	}
}
