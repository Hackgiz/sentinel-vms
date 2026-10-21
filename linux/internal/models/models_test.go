package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/tasks"
)

var hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handInstalled are the non-bundled models with no pinned download: the
// custom detector, and OSNet (no public export takes batch 1).
var handInstalled = map[string]bool{IDCustom: true, IDReID: true}

func TestCatalog(t *testing.T) {
	ids, files := map[string]bool{}, map[string]bool{}
	purposes := map[string]bool{PurposeDetector: true, PurposeReID: true, PurposeAudio: true, PurposeFaceDetect: true,
		PurposeFaceEmbed: true, PurposePlateDet: true, PurposePlateRec: true, PurposeCLIPImage: true, PurposeCLIPText: true}
	for _, e := range Catalog() {
		if e.ID == "" || ids[e.ID] {
			t.Errorf("id %q empty or repeated", e.ID)
		}
		ids[e.ID] = true
		if !purposes[e.Purpose] {
			t.Errorf("%s: purpose %q not in the API enum", e.ID, e.Purpose)
		}
		if e.Title == "" || e.License == "" || e.Note == "" || len(e.Files) == 0 || e.File != e.Files[0].Name {
			t.Errorf("%s: incomplete entry %+v", e.ID, e)
		}
		var size int64
		for _, f := range e.Files {
			if files[f.Name] {
				t.Errorf("%s: file %s used twice", e.ID, f.Name)
			}
			files[f.Name] = true
			size += f.Size
			if e.Bundled || handInstalled[e.ID] {
				continue
			}
			if !strings.HasPrefix(f.URL, "https://") || !hexSHA.MatchString(f.SHA256) || f.Size <= 0 {
				t.Errorf("%s: %s needs an https URL, a SHA-256 and a size: %+v", e.ID, f.Name, f)
			}
		}
		if e.Downloadable() == (e.Bundled || handInstalled[e.ID]) {
			t.Errorf("%s: Downloadable() = %v", e.ID, e.Downloadable())
		}
		if e.SizeBytes != size {
			t.Errorf("%s: SizeBytes %d, files sum to %d", e.ID, e.SizeBytes, size)
		}
		if e.Purpose == PurposeDetector && e.ID != IDCustom && (e.Input == 0 || e.Family == "") {
			t.Errorf("%s: detector without input or family", e.ID)
		}
	}
	// The names other streams and existing code open.
	for id, file := range map[string]string{IDYOLOXTiny: "yolox_tiny.onnx", IDYOLOXS: "yolox_s.onnx", IDReID: "reid.onnx",
		IDAudio: "audio.onnx", IDFaceDet: "face_det.onnx", IDFaceRec: "face_rec.onnx", IDPlateDet: "plate_det.onnx",
		IDPlateRec: "plate_rec.onnx", IDCLIPImage: "clip_image.onnx", IDCLIPText: "clip_text.onnx", IDCustom: "custom.onnx"} {
		if e, ok := Lookup(id); !ok || e.File != file {
			t.Errorf("%s: file %q, want %s", id, e.File, file)
		}
	}
	if !IsDetector("") || !IsDetector(IDYOLOXS) || !IsDetector(IDCustom) || IsDetector(IDReID) || IsDetector("nope") {
		t.Error("IsDetector")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestStateAndRemove(t *testing.T) {
	data := t.TempDir()
	e := Entry{ID: "two", Files: []File{{Name: "a.onnx", SHA256: sum("aaa"), Size: 3}, {Name: "a.txt", SHA256: sum("t"), Size: 1}}}
	if got := entryState(data, e); got != StateMissing {
		t.Fatalf("empty: %s", got)
	}
	write(t, Dir(data), "a.onnx", "aaa")
	if got := entryState(data, e); got != StateMissing {
		t.Fatalf("one of two files: %s", got)
	}
	write(t, Dir(data), "a.txt", "t")
	if got := entryState(data, e); got != StateInstalled {
		t.Fatalf("both: %s", got)
	}
	// Changed content (new size and mtime) is noticed through the cache.
	time.Sleep(10 * time.Millisecond)
	write(t, Dir(data), "a.onnx", "bbbb")
	if got := entryState(data, e); got != StateCorrupt {
		t.Fatalf("changed: %s", got)
	}

	if got := State(data, IDYOLOXS); got != StateMissing {
		t.Fatalf("yolox-s: %s", got)
	}
	write(t, Dir(data), "yolox_s.onnx", "not the model")
	if got := State(data, IDYOLOXS); got != StateCorrupt {
		t.Fatalf("yolox-s wrong bytes: %s", got)
	}
	if _, _, err := Detector(data, IDYOLOXS); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("detector on corrupt: %v", err)
	}
	if err := Remove(data, IDYOLOXS); err != nil {
		t.Fatal(err)
	}
	if got := State(data, IDYOLOXS); got != StateMissing {
		t.Fatalf("after remove: %s", got)
	}
	if _, _, err := Detector(data, IDYOLOXS); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("detector on missing: %v", err)
	}
	if err := Remove(data, IDYOLOXTiny); err == nil {
		t.Fatal("bundled model removed")
	}
	if _, _, err := Detector(data, IDReID); err == nil {
		t.Fatal("reid accepted as a detector")
	}
}

func TestCustom(t *testing.T) {
	data := t.TempDir()
	if got := State(data, IDCustom); got != StateMissing {
		t.Fatalf("custom: %s", got)
	}
	write(t, Dir(data), CustomModelFile, "onnx bytes")
	write(t, Dir(data), CustomSpecFile, `{"family": "yolo"}`)
	if got := State(data, IDCustom); got != StateCorrupt {
		t.Fatalf("bad json: %s", got)
	}
	write(t, Dir(data), CustomSpecFile, `{"family": "ultralytics", "input": 640, "labels": {"0": "person", "1": "bicycle", "2": "Car", "14": "bird", "80": "package"}}`)
	if got := State(data, IDCustom); got != StateInstalled {
		t.Fatalf("good json: %s", got)
	}
	path, spec, err := Detector(data, IDCustom)
	if err != nil || filepath.Base(path) != CustomModelFile {
		t.Fatalf("detector: %q %v", path, err)
	}
	want := map[int]detect.Kind{0: {Kind: "Person", Label: "person"}, 2: {Kind: "Vehicle", Label: "car"},
		14: {Kind: "Animal", Label: "bird"}, 80: {Kind: "Package", Label: "package"}}
	if spec.Family != detect.FamilyUltralytics || spec.Input != 640 || len(spec.Labels) != len(want) {
		t.Fatalf("spec %+v", spec)
	}
	for i, k := range want {
		if spec.Labels[i] != k {
			t.Errorf("label %d = %+v, want %+v", i, spec.Labels[i], k)
		}
	}
	for _, bad := range []string{
		`{"family": "yolox", "input": 100, "labels": {"0": "person"}}`,
		`{"family": "yolox", "input": 640, "labels": {"x": "person"}}`,
		`{"family": "yolox", "input": 640, "labels": {"0": "bicycle"}}`,
		`not json`,
	} {
		if _, err := ParseCustom([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestDownload(t *testing.T) {
	body := strings.Repeat("model bytes ", 50000)
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok", "/dict":
			_, _ = w.Write([]byte(body))
		case "/slow":
			w.Header().Set("Content-Length", "100000000")
			_, _ = w.Write([]byte(body[:1000]))
			w.(http.Flusher).Flush()
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer close(gate)
	reg := tasks.New()
	data := t.TempDir()

	good := Entry{ID: "good", Title: "Good", Files: []File{{Name: "g.onnx", URL: srv.URL + "/ok", SHA256: sum(body), Size: int64(len(body))}}}
	j, err := DownloadEntry(context.Background(), data, entry(good), reg)
	if err != nil {
		t.Fatal(err)
	}
	j.Wait()
	if v := j.View(); v.State != JobDone || v.Done != int64(len(body)) || v.Total != int64(len(body)) || v.TaskID == "" {
		t.Fatalf("done job %+v", v)
	}
	if got := entryState(data, good); got != StateInstalled {
		t.Fatalf("state after download %s", got)
	}
	if l := reg.List(); len(l) != 1 || l[0].Kind != TaskKind || l[0].Title != "Download Good" || l[0].State != tasks.Done {
		t.Fatalf("tasks %+v", l)
	}

	bad := Entry{ID: "bad", Title: "Bad", Files: []File{{Name: "b.onnx", URL: srv.URL + "/ok", SHA256: sum("other"), Size: 5}}}
	j, _ = DownloadEntry(context.Background(), data, entry(bad), reg)
	j.Wait()
	if v := j.View(); v.State != JobFailed || !strings.Contains(v.Error, "mismatch") {
		t.Fatalf("bad job %+v", v)
	}
	if _, err := os.Stat(filepath.Join(Dir(data), "b.onnx")); err == nil {
		t.Fatal("unverified file installed")
	}
	if _, err := os.Stat(filepath.Join(Dir(data), "b.onnx.part")); err == nil {
		t.Fatal("partial file left")
	}

	slow := Entry{ID: "slow", Title: "Slow", Files: []File{{Name: "s.onnx", URL: srv.URL + "/slow", SHA256: sum("x"), Size: 100000000}}}
	j, _ = DownloadEntry(context.Background(), data, entry(slow), reg)
	deadline := time.Now().Add(5 * time.Second)
	for j.View().Done == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !j.Running() {
		t.Fatalf("slow job %+v", j.View())
	}
	j.Cancel(5 * time.Second)
	if v := j.View(); v.State != JobCanceled || v.FinishedAt == 0 {
		t.Fatalf("canceled job %+v", v)
	}
	if _, err := os.Stat(filepath.Join(Dir(data), "s.onnx.part")); err == nil {
		t.Fatal("partial file left after cancel")
	}

	if _, err := Download(context.Background(), data, IDYOLOXTiny, reg); err != ErrNoSource {
		t.Fatalf("bundled: %v", err)
	}
	for _, id := range []string{IDCustom, IDReID} {
		if _, err := Download(context.Background(), data, id, reg); err != ErrNoSource {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
