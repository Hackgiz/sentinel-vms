package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

// fakeFFmpeg installs the playback tests' fake ffmpeg under a data dir.
func fakeFFmpeg(t *testing.T, dataDir string) {
	t.Helper()
	bin := filepath.Join(dataDir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	script := "#!/bin/sh\nfor a; do out=\"$a\"; done\nprintf 'clip:%s' \"$*\" > \"$out\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func evBody(camID string, from, to time.Time, extra string) string {
	return fmt.Sprintf(`{"cameraId":%q,"fromMs":%d,"toMs":%d,"title":"Lobby incident"%s}`, camID, from.UnixMilli(), to.UnixMilli(), extra)
}

func TestV2EvidencePermissionsScopeAndLimits(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	ctx := context.Background()
	op := v2Token(t, s, "op", "Operator")
	viewer := v2Token(t, s, "viewer", "Viewer")
	boss := v2Token(t, s, "boss", "Admin")
	scoped := v2Token(t, s, "scoped", "Operator")
	other, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Other", RTSPURL: "rtsp://10.0.0.7/s"})
	g, _ := s.Store.SaveCameraGroup(ctx, "", "Other only", []string{other.ID})
	u, _ := s.Store.UserByName(ctx, "scoped")
	if err := s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	body := evBody(c.ID, t0.Add(10*time.Second), t0.Add(40*time.Second), "")

	// Viewers can neither export, list nor verify.
	for _, call := range [][2]string{{"POST", "/api/v2/evidence"}, {"GET", "/api/v2/evidence"}, {"GET", "/api/v2/evidence/x/download"}, {"POST", "/api/v2/evidence/x/verify"}} {
		if r := do(t, h, call[0], call[1], viewer, body); r.Code != 403 {
			t.Errorf("viewer %v: %d", call, r.Code)
		}
	}
	// Releasing is an admin act.
	if r := do(t, h, "DELETE", "/api/v2/evidence/x", op, ""); r.Code != 403 {
		t.Errorf("operator delete: %d", r.Code)
	}
	// Out of scope: the camera doesn't exist for this operator.
	if r := do(t, h, "POST", "/api/v2/evidence", scoped, body); r.Code != 404 {
		t.Errorf("scoped export: %d %s", r.Code, r.Body)
	}
	// Over the longest clip: 400 naming the limit.
	r := do(t, h, "POST", "/api/v2/evidence", op, evBody(c.ID, t0, t0.Add(31*time.Minute), ""))
	if r.Code != 400 || !strings.Contains(r.Body.String(), "30 minutes") {
		t.Errorf("over max: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "POST", "/api/v2/evidence", op, evBody(c.ID, t0.Add(time.Minute), t0, "")); r.Code != 400 {
		t.Errorf("inverted range: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/evidence", op, evBody("nope", t0, t0.Add(time.Minute), "")); r.Code != 404 {
		t.Errorf("unknown camera: %d", r.Code)
	}

	// A good export, listed and audited; the scoped operator doesn't see it.
	code, ev := v2Call(t, h, "POST", "/api/v2/evidence", op, evBody(c.ID, t0.Add(10*time.Second), t0.Add(40*time.Second), `,"note":"batch 1"`))
	if code != 201 || ev["note"] != "batch 1" || ev["cameraId"] != c.ID || ev["lockedBy"] != "op" || ev["fromMs"] == nil {
		t.Fatalf("export: %d %v", code, ev)
	}
	// An alarm-created case appears in the same list.
	a, _, _ := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person", Title: "P", Severity: "Critical", At: t0.Add(20 * time.Second), MergeSince: t0.Add(20 * time.Second), Origin: "test"})
	if code, _ := v2Call(t, h, "POST", "/api/v2/alarms/"+a.ID+"/evidence", op, ""); code != 200 {
		t.Fatalf("alarm lock: %d", code)
	}
	_, list := v2Call(t, h, "GET", "/api/v2/evidence", op, "")
	if rows := items(t, list); len(rows) != 2 {
		t.Fatalf("list: %v", list)
	}
	_, list = v2Call(t, h, "GET", "/api/v2/evidence", scoped, "")
	if rows := items(t, list); len(rows) != 0 {
		t.Fatalf("scoped list: %v", list)
	}
	id := ev["id"].(string)
	if r := do(t, h, "POST", "/api/v2/evidence/"+id+"/verify", scoped, ""); r.Code != 404 {
		t.Errorf("scoped verify: %d", r.Code)
	}
	if r := do(t, h, "GET", "/api/v2/evidence/"+id+"/download", scoped, ""); r.Code != 404 {
		t.Errorf("scoped download: %d", r.Code)
	}

	// Download carries the case id and hash.
	r = do(t, h, "GET", "/api/v2/evidence/"+id+"/download", op, "")
	if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Disposition"), ev["caseId"].(string)+".mp4") || r.Header().Get("X-Content-SHA256") != ev["sha256"] {
		t.Fatalf("download: %d %v", r.Code, r.Header())
	}

	// Verify passes, then catches a tampered file.
	if code, v := v2Call(t, h, "POST", "/api/v2/evidence/"+id+"/verify", op, ""); code != 200 || v["ok"] != true || v["sha256"] != ev["sha256"] {
		t.Fatalf("verify: %d %v", code, v)
	}
	vault := filepath.Join(recOf(s).DataDir, "evidence", ev["caseId"].(string)+".mp4")
	_ = os.Chmod(vault, 0o640)
	if err := os.WriteFile(vault, []byte("tampered"), 0o640); err != nil {
		t.Fatal(err)
	}
	if code, v := v2Call(t, h, "POST", "/api/v2/evidence/"+id+"/verify", op, ""); code != 200 || v["ok"] != false || v["sha256"] == ev["sha256"] {
		t.Fatalf("tampered verify: %d %v", code, v)
	}

	// Delete: admin only, audited, file and row gone.
	if r := do(t, h, "DELETE", "/api/v2/evidence/"+id, boss, ""); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if _, err := os.Stat(vault); err == nil {
		t.Error("evidence file survived delete")
	}
	if r := do(t, h, "DELETE", "/api/v2/evidence/"+id, boss, ""); r.Code != 404 {
		t.Errorf("second delete: %d", r.Code)
	}
	var n int
	_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit WHERE area = 'Evidence' AND action IN ('Locked evidence','Released evidence')`).Scan(&n)
	if n < 3 {
		t.Errorf("audit entries: %d", n)
	}
}

// A camera whose footage is on a second recording server: the clip is cut
// there, the case is filed against it, and download/verify/delete go to it.
func TestV2EvidenceOnRemoteRecorder(t *testing.T) {
	s, rs2, c, t0, _, _ := multiFootage(t)
	fakeFFmpeg(t, rs2.DataDir)
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")
	boss := v2Token(t, s, "boss", "Admin")
	at := t0.Add(2*time.Hour + 5*time.Second)

	code, ev := v2Call(t, h, "POST", "/api/v2/evidence", op, evBody(c.ID, at, at.Add(30*time.Second), ""))
	if code != 201 || ev["recorderId"] != "rs2" {
		t.Fatalf("export: %d %v", code, ev)
	}
	vault := filepath.Join(rs2.DataDir, "evidence", ev["caseId"].(string)+".mp4")
	if _, err := os.Stat(vault); err != nil {
		t.Fatalf("file not on rs2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recOf(s).DataDir, "evidence", ev["caseId"].(string)+".mp4")); err == nil {
		t.Fatal("file landed on the local server")
	}
	id := ev["id"].(string)
	if r := do(t, h, "GET", "/api/v2/evidence/"+id+"/download", op, ""); r.Code != 200 || r.Header().Get("X-Content-SHA256") != ev["sha256"] {
		t.Fatalf("download: %d", r.Code)
	}
	if code, v := v2Call(t, h, "POST", "/api/v2/evidence/"+id+"/verify", op, ""); code != 200 || v["ok"] != true {
		t.Fatalf("verify: %d %v", code, v)
	}
	_ = os.Chmod(vault, 0o640)
	_ = os.WriteFile(vault, []byte("x"), 0o640)
	if code, v := v2Call(t, h, "POST", "/api/v2/evidence/"+id+"/verify", op, ""); code != 200 || v["ok"] != false {
		t.Fatalf("tampered verify: %d %v", code, v)
	}
	if r := do(t, h, "DELETE", "/api/v2/evidence/"+id, boss, ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if _, err := os.Stat(vault); err == nil {
		t.Error("file survived on rs2")
	}
}
