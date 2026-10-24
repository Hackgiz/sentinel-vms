package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

// testVideo is an MP4-looking file (ftyp first) of n bytes.
func testVideo(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	copy(b, []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func putPart(t *testing.T, h http.Handler, bearer, id string, n int, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", fmt.Sprintf("/api/v2/evidence/uploads/%s/parts/%d", id, n), bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// uploadAll sends every part of data.
func uploadAll(t *testing.T, h http.Handler, bearer, id string, data []byte) {
	t.Helper()
	for i := 0; i*uploadPartSize < len(data); i++ {
		end := min((i+1)*uploadPartSize, len(data))
		if w := putPart(t, h, bearer, id, i, data[i*uploadPartSize:end]); w.Code != 204 {
			t.Fatalf("part %d: %d %s", i, w.Code, w.Body)
		}
	}
}

func startUpload(t *testing.T, h http.Handler, bearer string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	return v2Call(t, h, "POST", "/api/v2/evidence/uploads", bearer, string(b))
}

func TestUploadsNeedSentinelCloud(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")
	data := testVideo(1000)
	body := map[string]any{"fileName": "a.mp4", "size": len(data), "sha256": sha(data), "contentType": "video/mp4"}
	for _, plan := range [][2]string{{"", ""}, {"free", "inactive"}, {"cloud", "inactive"}, {"cloud", "canceled"}, {"business", "canceled"}} {
		if plan[0] != "" {
			setCloudPlan(t, s, plan[0], plan[1])
		}
		code, m := startUpload(t, h, op, body)
		if code != 402 || errCode(m) != "cloud_required" || errMessage(m) != "Uploading videos is part of Sentinel Cloud." {
			t.Fatalf("%v: %d %v", plan, code, m)
		}
		id := strings.Repeat("a", 32)
		if w := putPart(t, h, op, id, 0, data); w.Code != 402 {
			t.Fatalf("%v part: %d", plan, w.Code)
		}
		if code, _ := v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+id+"/complete", op, ""); code != 402 {
			t.Fatalf("%v complete: %d", plan, code)
		}
		if code, _ := v2Call(t, h, "DELETE", "/api/v2/evidence/uploads/"+id, op, ""); code != 402 {
			t.Fatalf("%v abort: %d", plan, code)
		}
	}
	for _, plan := range [][2]string{{"cloud", "active"}, {"business", "active"}, {"cloud", "grace"}} {
		setCloudPlan(t, s, plan[0], plan[1])
		if code, m := startUpload(t, h, op, body); code != 201 {
			t.Fatalf("%v: %d %v", plan, code, m)
		}
	}
	// Viewers can't add incident items, so they can't upload.
	if code, _ := startUpload(t, h, v2Token(t, s, "vera", "Viewer"), body); code != 403 {
		t.Fatalf("viewer: %d", code)
	}
}

func TestUploadIntoIncident(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	setCloudPlan(t, s, "cloud", "active")
	op := v2Token(t, s, "op", "Operator")
	cam := addCam(t, s)
	inc, err := s.Store.CreateIncident(ctx, "Break-in", "high", "op")
	if err != nil {
		t.Fatal(err)
	}
	data := testVideo(2*uploadPartSize + 1000) // three parts
	base := map[string]any{"fileName": "IMG_0042.MOV", "size": len(data), "sha256": sha(data), "contentType": "video/quicktime"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, bad := range []map[string]any{
		with(map[string]any{"sha256": "abc"}),
		with(map[string]any{"contentType": "video/avi"}),
		with(map[string]any{"size": int64(5) << 30}),
		with(map[string]any{"size": 0}),
		with(map[string]any{"fileName": ""}),
	} {
		if code, m := startUpload(t, h, op, bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, m)
		}
	}
	if code, _ := startUpload(t, h, op, with(map[string]any{"incidentId": "nope"})); code != 404 {
		t.Errorf("unknown incident: %d", code)
	}
	code, m := startUpload(t, h, op, with(map[string]any{"title": "Back door", "note": "Filmed by the guard", "recordedAt": 1792790000000,
		"cameraId": cam.ID, "incidentId": inc.ID, "device": "EJ's iPhone"}))
	if code != 201 || m["partSize"] != float64(8<<20) || m["partCount"] != float64(3) {
		t.Fatalf("start: %d %v", code, m)
	}
	id := m["uploadId"].(string)
	if exp := int64(m["expiresAt"].(float64)); exp < time.Now().Add(23*time.Hour).UnixMilli() {
		t.Errorf("expires %d", exp)
	}
	// Parts: wrong size, out of range, then all (one twice).
	if w := putPart(t, h, op, id, 0, data[:100]); w.Code != 400 {
		t.Errorf("short part: %d", w.Code)
	}
	if w := putPart(t, h, op, id, 3, data[:100]); w.Code != 400 {
		t.Errorf("part out of range: %d", w.Code)
	}
	if w := putPart(t, h, op, id, 2, data[:uploadPartSize]); w.Code != 413 {
		t.Errorf("last part too long: %d", w.Code)
	}
	// Someone else can't touch it.
	other := v2Token(t, s, "olga", "Operator")
	if w := putPart(t, h, other, id, 0, data[:uploadPartSize]); w.Code != 404 {
		t.Errorf("other user's part: %d", w.Code)
	}
	// Missing parts are named.
	if code, m := v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+id+"/complete", op, ""); code != 400 || !strings.Contains(errMessage(m), "0, 1, 2") {
		t.Errorf("complete without parts: %d %v", code, m)
	}
	uploadAll(t, h, op, id, data)
	if w := putPart(t, h, op, id, 1, data[uploadPartSize:2*uploadPartSize]); w.Code != 204 {
		t.Errorf("part again: %d", w.Code)
	}
	// A cookie caller needs the custom header (CSRF).
	cookie := webAs(t, s, "web", "Operator")
	r := httptest.NewRequest("PUT", "/api/v2/evidence/uploads/"+id+"/parts/0", bytes.NewReader(data[:uploadPartSize]))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("cookie part without the header: %d", w.Code)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+id+"/complete", other, ""); code != 404 {
		t.Errorf("other user's complete: %d", code)
	}

	code, m = v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+id+"/complete", op, "")
	if code != 201 {
		t.Fatalf("complete: %d %v", code, m)
	}
	if m["source"] != "upload" || m["uploadedBy"] != "op" || m["device"] != "EJ's iPhone" || m["sha256"] != sha(data) || m["size"] != float64(len(data)) ||
		m["title"] != "Back door" || m["note"] != "Filmed by the guard" || m["recordedAt"] != float64(1792790000000) || m["cameraId"] != cam.ID || m["recorderId"] != "local" {
		t.Fatalf("evidence %v", m)
	}
	evID, caseID := m["id"].(string), m["caseId"].(string)
	// In the vault, byte for byte; the parts are gone.
	got, err := os.ReadFile(filepath.Join(recOf(s).EvidenceDir(), caseID+".mp4"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("vault file: %v (%d bytes)", err, len(got))
	}
	if _, err := os.Stat(s.uploadDir(id)); !os.IsNotExist(err) {
		t.Errorf("parts kept: %v", err)
	}
	// Asking again returns the same item.
	if code, m := v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+id+"/complete", op, ""); code != 201 || m["id"] != evID {
		t.Errorf("complete again: %d %v", code, m)
	}
	// Listed like other evidence, in the incident, audited.
	sup := v2Token(t, s, "sue", "Supervisor")
	_, list := v2Call(t, h, "GET", "/api/v2/evidence", sup, "")
	if items := items(t, list); len(items) != 1 || items[0]["source"] != "upload" {
		t.Errorf("evidence list %v", list)
	}
	its, _ := s.Store.IncidentItems(ctx, inc.ID)
	if len(its) != 1 || its[0].Kind != "evidence" || its[0].RefID != evID {
		t.Errorf("incident items %+v", its)
	}
	entries, _ := s.Store.AuditLog(ctx, 50)
	found := false
	for _, e := range entries {
		found = found || (e.Action == "Uploaded video Back door" && e.User == "op" && strings.Contains(e.Detail, caseID))
	}
	if !found {
		t.Errorf("no audit entry: %+v", entries)
	}
	// After the subscription ends the video stays listed and verifiable.
	setCloudPlan(t, s, "cloud", "canceled")
	if code, m := v2Call(t, h, "POST", "/api/v2/evidence/"+evID+"/verify", sup, ""); code != 200 || m["ok"] != true {
		t.Errorf("verify after the plan ended: %d %v", code, m)
	}
	if w := webDo(t, h, webAs(t, s, "web2", "Supervisor"), "GET", "/api/v2/evidence/"+evID+"/download", ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Errorf("download after the plan ended: %d (%d bytes)", w.Code, w.Body.Len())
	}
}

func TestUploadAttachesToAlarmAndEvent(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	setCloudPlan(t, s, "business", "active")
	op := v2Token(t, s, "op", "Operator")
	cam := addCam(t, s)
	ev := &store.Event{CameraID: cam.ID, Kind: "Person", Score: 0.9, CreatedAt: time.Now().Unix()}
	if err := s.Store.AddEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	a, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: cam.ID, CameraName: cam.Name, Kind: "Person", Title: "Person", Severity: "Warning", At: time.Now(), Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	data := testVideo(5000)
	code, m := startUpload(t, h, op, map[string]any{"fileName": "clip.mp4", "size": len(data), "sha256": sha(data), "contentType": "video/mp4", "eventId": ev.ID, "alarmId": a.ID})
	if code != 201 {
		t.Fatalf("start: %d %v", code, m)
	}
	uploadAll(t, h, op, m["uploadId"].(string), data)
	code, m = v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+m["uploadId"].(string)+"/complete", op, "")
	if code != 201 || m["eventId"] != ev.ID || m["alarmId"] != a.ID || m["cameraId"] != cam.ID || m["title"] != "clip" {
		t.Fatalf("complete: %d %v", code, m)
	}
	// Noted on the alarm, but not taken for the alarm's own evidence lock.
	got, _ := s.Store.Alarm(ctx, a.ID)
	if !strings.Contains(strings.Join(got.ResponseLog, "|"), "Video uploaded by op") {
		t.Errorf("alarm log %v", got.ResponseLog)
	}
	if _, err := s.Store.EvidenceForAlarm(ctx, a.ID); err == nil {
		t.Error("the upload counts as the alarm's lock")
	}
	// An event on a camera outside the caller's reach is not found.
	if code, _ := startUpload(t, h, op, map[string]any{"fileName": "c.mp4", "size": 10, "sha256": sha([]byte("x")), "contentType": "video/mp4", "eventId": "nope"}); code != 404 {
		t.Errorf("unknown event: %d", code)
	}
}

func TestUploadRefusals(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	setCloudPlan(t, s, "cloud", "active")
	op := v2Token(t, s, "op", "Operator")

	// Wrong hash: refused, parts kept; then abort drops them.
	data := testVideo(3000)
	_, m := startUpload(t, h, op, map[string]any{"fileName": "a.mp4", "size": len(data), "sha256": sha([]byte("other")), "contentType": "video/mp4"})
	id := m["uploadId"].(string)
	uploadAll(t, h, op, id, data)
	if code, m := v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+id+"/complete", op, ""); code != 400 || !strings.Contains(errMessage(m), "sha256") {
		t.Fatalf("wrong hash: %d %v", code, m)
	}
	if _, err := os.Stat(s.uploadDir(id)); err != nil {
		t.Fatalf("parts dropped after a mismatch: %v", err)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/evidence/uploads/"+id, op, ""); code != 204 {
		t.Fatalf("abort: %d", code)
	}
	if _, err := os.Stat(s.uploadDir(id)); !os.IsNotExist(err) {
		t.Fatalf("parts kept after abort: %v", err)
	}
	if w := putPart(t, h, op, id, 0, data); w.Code != 404 {
		t.Fatalf("part after abort: %d", w.Code)
	}

	// Not a movie.
	junk := make([]byte, 3000)
	_, m = startUpload(t, h, op, map[string]any{"fileName": "a.mp4", "size": len(junk), "sha256": sha(junk), "contentType": "video/mp4"})
	uploadAll(t, h, op, m["uploadId"].(string), junk)
	if code, m := v2Call(t, h, "POST", "/api/v2/evidence/uploads/"+m["uploadId"].(string)+"/complete", op, ""); code != 400 || !strings.Contains(errMessage(m), "ftyp") {
		t.Fatalf("no ftyp: %d %v", code, m)
	}

	// Unfinished uploads are swept after 24 h.
	_, m = startUpload(t, h, op, map[string]any{"fileName": "b.mp4", "size": len(data), "sha256": sha(data), "contentType": "video/mp4"})
	stale := m["uploadId"].(string)
	putPart(t, h, op, stale, 0, data)
	if n := s.sweepUploads(ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("swept %d too early", n)
	}
	if n := s.sweepUploads(ctx, time.Now().Add(25*time.Hour)); n != 2 { // this one and the junk one
		t.Fatalf("swept %d", n)
	}
	if _, err := os.Stat(s.uploadDir(stale)); !os.IsNotExist(err) {
		t.Fatalf("stale parts kept: %v", err)
	}
	if _, err := s.Store.Upload(ctx, stale); err == nil {
		t.Fatal("stale row kept")
	}
}

// The device an upload came from: the body wins; otherwise the model inside
// the User-Agent's parentheses, not the product name ("Sentinel").
func TestUploadDeviceFromUserAgent(t *testing.T) {
	ua := "Sentinel Mobile/3.6.0 (iPhone17,2; iOS 18.1)"
	if got := uploadDevice(nil, ua); got != "iPhone17,2" {
		t.Fatalf("got %q", got)
	}
	name := "EJ's iPhone"
	if got := uploadDevice(&name, ua); got != name {
		t.Fatalf("got %q", got)
	}
	if got := uploadDevice(nil, "curl/8.0"); got != "curl/8.0" {
		t.Fatalf("got %q", got)
	}
}
