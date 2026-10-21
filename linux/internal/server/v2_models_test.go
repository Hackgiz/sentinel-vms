package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/models"
	"sentinel-linux/internal/tasks"
)

// modelTransport answers every model download: blocks until release is
// closed (or the request is canceled), then sends body.
type modelTransport struct {
	release chan struct{}
	body    string
}

func (m *modelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if m.release != nil {
		select {
		case <-m.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(m.body)), Header: http.Header{}, Request: r}, nil
}

func useModelTransport(t *testing.T, tr *modelTransport) {
	old := models.HTTPClient
	models.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(func() { models.HTTPClient = old })
}

func modelByID(m map[string]any, id string) map[string]any {
	items, _ := m["items"].([]any)
	for _, it := range items {
		if x := it.(map[string]any); x["id"] == id {
			return x
		}
	}
	return nil
}

func TestV2ModelsCatalog(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")
	code, m := v2Call(t, h, "GET", "/api/v2/models", op, "")
	if code != 200 || m["dir"] != filepath.Join(s.Store.DataDir(), "models") {
		t.Fatalf("list: %d %v", code, m)
	}
	if items := m["items"].([]any); len(items) != len(models.Catalog()) {
		t.Fatalf("items %d", len(items))
	}
	ys := modelByID(m, "yolox-s")
	if ys["state"] != "missing" || ys["installed"] != false || ys["purpose"] != "detector" || ys["file"] != "yolox_s.onnx" ||
		ys["sizeBytes"].(float64) <= 0 || !strings.HasPrefix(ys["source"].(string), "https://") {
		t.Fatalf("yolox-s %v", ys)
	}
	if tiny := modelByID(m, "yolox-tiny"); tiny["bundled"] != true {
		t.Fatalf("tiny %v", tiny)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/models/yolox-s/download", op, ""); code != 403 {
		t.Fatalf("operator download: %d", code)
	}
}

func TestV2DetectorSettings(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "boss", "Admin")
	op := v2Token(t, s, "op", "Operator")

	code, m := v2Call(t, h, "GET", "/api/v2/detector", op, "")
	st, _ := m["status"].(map[string]any)
	if code != 200 || m["model"] != "" || m["rate"].(float64) != 0 || st == nil || st["effectiveRate"].(float64) != 1 || st["pending"] != false {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/detector", op, `{"rate": 2}`); code != 403 {
		t.Fatalf("operator put: %d", code)
	}

	// A missing model: 400, nothing saved.
	code, m = v2Call(t, h, "PUT", "/api/v2/detector", admin, `{"model": "yolox-s", "rate": 3}`)
	if code != 400 || !strings.Contains(errMessage(m), "not installed") {
		t.Fatalf("missing model: %d %v", code, m)
	}
	if got := s.Store.Setting(ctx, SettingDetectorModel, "unset"); got != "unset" {
		t.Fatalf("setting changed to %q", got)
	}
	for _, body := range []string{`{"model": "reid-osnet"}`, `{"model": "nope"}`, `{"rate": 6}`, `{"rate": -1}`, `{"speed": 1}`} {
		if code, _ := v2Call(t, h, "PUT", "/api/v2/detector", admin, body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}

	// A custom model installed by hand.
	dir := models.Dir(s.Store.DataDir())
	_ = os.MkdirAll(dir, 0o750)
	_ = os.WriteFile(filepath.Join(dir, "custom.onnx"), []byte("onnx"), 0o640)
	_ = os.WriteFile(filepath.Join(dir, "custom.json"), []byte(`{"family": "ultralytics", "input": 640, "labels": {"0": "person"}}`), 0o640)
	code, m = v2Call(t, h, "PUT", "/api/v2/detector", admin, `{"model": "custom", "rate": 3}`)
	st, _ = m["status"].(map[string]any)
	if code != 200 || m["model"] != "custom" || m["rate"].(float64) != 3 || st["effectiveRate"].(float64) != 3 || st["pending"] != true {
		t.Fatalf("custom: %d %v", code, m)
	}
	if got := lastAudit(t, s, "Changed detector"); !strings.HasSuffix(got, "Detection | custom, 3/s") {
		t.Fatalf("audit %q", got)
	}
	if got := s.Store.Setting(ctx, SettingDetectRate, ""); got != "3" {
		t.Fatalf("rate setting %q", got)
	}
	// The snapshot carries both to the recorders.
	if snap := s.snapshot(ctx, "local", nil); snap.DetectorModel != "custom" || snap.DetectRate != 3 {
		t.Fatalf("snapshot %q %d", snap.DetectorModel, snap.DetectRate)
	}
	// The selected detector can't be removed; the bundled one never.
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/custom", admin, ""); code != 409 {
		t.Fatalf("delete selected: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/yolox-tiny", admin, ""); code != 409 {
		t.Fatalf("delete bundled: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/nope", admin, ""); code != 404 {
		t.Fatalf("delete unknown: %d", code)
	}
	// Back to the default; then custom can go.
	if code, m := v2Call(t, h, "PUT", "/api/v2/detector", admin, `{"model": ""}`); code != 200 || m["model"] != "" || m["rate"].(float64) != 3 {
		t.Fatalf("reset: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/custom", admin, ""); code != 204 {
		t.Fatalf("delete custom: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "custom.onnx")); err == nil {
		t.Fatal("custom.onnx left")
	}
	if got := lastAudit(t, s, "Removed model"); !strings.HasSuffix(got, "Custom detector") {
		t.Fatalf("audit %q", got)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/face-yunet", admin, ""); code != 204 {
		t.Fatalf("delete missing: %d", code)
	}
}

func errMessage(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

func TestV2ModelDownload(t *testing.T) {
	s := newCompanionServer(t)
	s.models.reg = tasks.New()
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	tr := &modelTransport{release: make(chan struct{}), body: "not the real model"}
	useModelTransport(t, tr)

	for path, want := range map[string]int{"/api/v2/models/yolox-tiny/download": 409, "/api/v2/models/custom/download": 409, "/api/v2/models/reid-osnet/download": 409, "/api/v2/models/nope/download": 404} {
		if code, _ := v2Call(t, h, "POST", path, admin, ""); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/face-yunet/download", admin, ""); code != 404 {
		t.Fatalf("cancel without a job: %d", code)
	}

	code, m := v2Call(t, h, "POST", "/api/v2/models/face-yunet/download", admin, "")
	if code != 201 || m["modelId"] != "face-yunet" || m["state"] != "running" || m["taskId"] == "" || m["total"].(float64) != 232589 {
		t.Fatalf("start: %d %v", code, m)
	}
	if code, m2 := v2Call(t, h, "POST", "/api/v2/models/face-yunet/download", admin, ""); code != 200 || m2["taskId"] != m["taskId"] {
		t.Fatalf("again: %d %v", code, m2)
	}
	_, list := v2Call(t, h, "GET", "/api/v2/models", admin, "")
	if fy := modelByID(list, "face-yunet"); fy["state"] != "downloading" || fy["download"] == nil {
		t.Fatalf("listed %v", fy)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/models/face-yunet", admin, ""); code != 409 {
		t.Fatalf("delete while downloading: %d", code)
	}
	if l := s.models.reg.List(); len(l) != 1 || l[0].Kind != "model-download" || l[0].Title != "Download YuNet (face detection)" {
		t.Fatalf("tasks %+v", l)
	}
	code, m = v2Call(t, h, "DELETE", "/api/v2/models/face-yunet/download", admin, "")
	if code != 200 || m["state"] != "canceled" {
		t.Fatalf("cancel: %d %v", code, m)
	}
	if got := lastAudit(t, s, "Canceled model download"); got == "" {
		t.Fatal("cancel not audited")
	}

	// A download whose bytes don't match fails and installs nothing.
	close(tr.release)
	code, m = v2Call(t, h, "POST", "/api/v2/models/face-yunet/download", admin, "")
	if code != 201 {
		t.Fatalf("restart: %d %v", code, m)
	}
	s.modelJob("face-yunet").Wait()
	_, list = v2Call(t, h, "GET", "/api/v2/models", admin, "")
	fy := modelByID(list, "face-yunet")
	dl, _ := fy["download"].(map[string]any)
	if fy["state"] != "missing" || dl["state"] != "failed" || !strings.Contains(dl["error"].(string), "SHA-256 mismatch") {
		t.Fatalf("after failure %v", fy)
	}
	deadline := time.Now().Add(2 * time.Second)
	for lastAudit(t, s, "Model download failed") == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := lastAudit(t, s, "Model download failed"); !strings.Contains(got, "mismatch") {
		t.Fatalf("failure audit %q", got)
	}
	if _, err := os.Stat(filepath.Join(models.Dir(s.Store.DataDir()), "face_det.onnx")); err == nil {
		t.Fatal("unverified model installed")
	}
}

func TestEffectiveRate(t *testing.T) {
	cpu := &detect.DetectorInfo{Engine: detect.EngineCPU}
	gpu := &detect.DetectorInfo{Engine: detect.EngineOpenVINO}
	for _, c := range []struct {
		saved int
		info  *detect.DetectorInfo
		want  int
	}{
		{0, nil, 1}, {0, cpu, 1}, {0, gpu, 5}, {3, cpu, 3}, {9, gpu, 5},
		{2, &detect.DetectorInfo{Engine: detect.EngineCoreML, Rate: 4}, 4},
	} {
		if got := effectiveRate(c.saved, c.info); got != c.want {
			t.Errorf("effectiveRate(%d, %+v) = %d, want %d", c.saved, c.info, got, c.want)
		}
	}
}
