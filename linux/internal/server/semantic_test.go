package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/semantic"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

// fakeSemantic describes a snapshot by its mean color and a text by the
// color words in it, so "red" finds red scenes. It is never a real model.
type fakeSemantic struct {
	model string
	mu    sync.Mutex
	seen  []image.Rectangle
	texts []string
	gate  chan struct{} // non-nil: each EmbedImage waits for a token (cancel tests)
}

func (f *fakeSemantic) Model() string { return f.model }

func (f *fakeSemantic) EmbedImage(img image.Image) ([]float32, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	f.seen = append(f.seen, img.Bounds())
	f.mu.Unlock()
	var r, g, b, n float64
	bd := img.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r, g, b, n = r+float64(cr>>8), g+float64(cg>>8), b+float64(cb>>8), n+1
		}
	}
	return reid.Normalize([]float32{float32(r / n), float32(g / n), float32(b / n)})
}

func (f *fakeSemantic) EmbedText(text string) ([]float32, error) {
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	v := []float32{0, 0, 0}
	for i, w := range []string{"red", "green", "blue"} {
		if strings.Contains(text, w) {
			v[i] = 1
		}
	}
	if v[0]+v[1]+v[2] == 0 {
		return nil, errors.New("fake model knows only colors")
	}
	return reid.Normalize(v)
}

func (f *fakeSemantic) imageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// sceneJPEG is a 200×100 frame filled with c.
func sceneJPEG(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	return buf.Bytes()
}

// addScene stores an event (no detection box) whose thumbnail, served by
// the local recording server, is a frame of color c.
func addScene(t *testing.T, s *Server, cam, kind string, at time.Time, c color.RGBA) *store.Event {
	t.Helper()
	dir := filepath.Join(s.Store.DataDir(), "events")
	_ = os.MkdirAll(dir, 0o755)
	e := &store.Event{ID: store.NewID(), CameraID: cam, Kind: kind, Score: 0.9, CreatedAt: at.Unix()}
	e.Thumbnail = filepath.Join(dir, e.ID+".jpg")
	if err := os.WriteFile(e.Thumbnail, sceneJPEG(c), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.AddEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return e
}

var green = color.RGBA{20, 200, 20, 255}

func TestSemanticUnavailableDegrades(t *testing.T) {
	f := newScopeFixture(t)
	q := addScene(t, f.s, f.front.ID, "Motion", f.t0, red)
	code, m := v2Call(t, f.h, "GET", "/api/v2/semantic", f.op, "")
	if code != 200 || m["available"] != false || !strings.Contains(fmt.Sprint(m["reason"]), "no semantic search model") {
		t.Fatalf("status: %d %v", code, m)
	}
	f.s.SetSemanticEngine(nil, "no semantic search image model installed (/data/models/clip_image.onnx)")
	code, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.op, `{"text":"red car"}`)
	if code != 503 || errCode(m) != "unavailable" || !strings.Contains(fmt.Sprint(m["error"]), "clip_image.onnx") {
		t.Fatalf("search: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "POST", "/api/v2/semantic/backfill", f.admin, "")
	if code != 409 || errCode(m) != "conflict" {
		t.Fatalf("backfill: %d %v", code, m)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/semantic", f.op, "")
	if !strings.Contains(fmt.Sprint(m["reason"]), "clip_image.onnx") || m["model"] != nil || m["indexed"] != nil {
		t.Fatalf("status with reason: %v", m)
	}
	// Thumbnails still arrive fine; nothing is indexed.
	_ = f.s.Sink().Event(context.Background(), link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: f.front.ID,
		Thumbnail: &link.Thumbnail{EventID: q.ID, File: filepath.Base(q.Thumbnail)}})
	if n, _ := f.s.Store.CountSemantic(context.Background(), ""); n != 0 {
		t.Fatal("indexed without a model")
	}
}

func TestSemanticOnThumbnailEmbedsWholeSnapshot(t *testing.T) {
	f := newScopeFixture(t)
	eng := &fakeSemantic{model: "sha256:5e5e"}
	f.s.SetSemanticEngine(eng, "")
	f.s.SetSemanticEngine(eng, "") // twice: still one hook, one worker
	ctx := context.Background()
	motion := addScene(t, f.s, f.front.ID, "Motion", f.t0, red) // no box: the scene is what counts
	person := addPerson(t, f.s, f.front.ID, "Person", f.t0, blue)
	for _, e := range []*store.Event{motion, person} {
		if err := f.s.Sink().Event(ctx, link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: f.front.ID,
			Thumbnail: &link.Thumbnail{EventID: e.ID, File: filepath.Base(e.Thumbnail)}}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := f.s.Store.CountSemantic(ctx, "sha256:5e5e"); n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n, _ := f.s.Store.CountSemantic(ctx, "sha256:5e5e"); n != 2 {
		t.Fatalf("indexed %d of 2 after their thumbnails arrived", n)
	}
	time.Sleep(50 * time.Millisecond) // a second hook would embed again
	eng.mu.Lock()
	seen := append([]image.Rectangle(nil), eng.seen...)
	eng.mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("embedded %d times, want 2 (one hook)", len(seen))
	}
	for _, r := range seen {
		if r.Dx() != 200 || r.Dy() != 100 {
			t.Fatalf("embedded %v, want the whole 200×100 snapshot", r)
		}
	}
	// The appearance index is untouched.
	if n, _ := f.s.Store.CountEmbeddings(ctx, "sha256:5e5e"); n != 0 {
		t.Fatal("semantic vectors written to event_embeddings")
	}
}

func semanticIDs(m map[string]any) []string { return similarIDs(m) }

func TestSemanticSearchScopeFiltersAudit(t *testing.T) {
	f := newScopeFixture(t)
	eng := &fakeSemantic{model: "sha256:5e5f"}
	f.s.SetSemanticEngine(eng, "")
	ctx := context.Background()
	redFront := addScene(t, f.s, f.front.ID, "Vehicle", f.t0, red)
	redBack := addScene(t, f.s, f.back.ID, "Vehicle", f.t0.Add(time.Hour), red)
	purpleFront := addScene(t, f.s, f.front.ID, "Person", f.t0.Add(2*time.Hour), color.RGBA{200, 20, 200, 255})
	greenFront := addScene(t, f.s, f.front.ID, "Motion", f.t0.Add(3*time.Hour), green)
	for _, e := range []*store.Event{redFront, redBack, purpleFront, greenFront} {
		if _, err := f.s.embedSemantic(ctx, eng, e); err != nil {
			t.Fatal(err)
		}
	}

	// Admin: red scenes first, purple next; green scores under the default
	// minScore 0.2 and is left out.
	code, m := v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"  a red car  "}`)
	ids := semanticIDs(m)
	if code != 200 || len(ids) != 3 || ids[2] != purpleFront.ID || m["model"] != "sha256:5e5f" {
		t.Fatalf("admin: %d %v", code, m)
	}
	if sc := similarScores(m); sc[0] < 0.95 || sc[2] >= sc[1] {
		t.Fatalf("scores %v", sc)
	}
	if a := lastAudit(t, f.s, "Semantic search"); a != "boss | Search | a red car" {
		t.Fatalf("audit = %q", a)
	}
	if eng.texts[len(eng.texts)-1] != "a red car" {
		t.Fatalf("text sent to the model: %q", eng.texts)
	}
	// minScore 0 lets green in; limit trims.
	if _, m := v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"red","minScore":0}`); len(semanticIDs(m)) != 4 {
		t.Fatalf("minScore 0: %v", semanticIDs(m))
	}
	if _, m := v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"red","limit":1}`); len(semanticIDs(m)) != 1 {
		t.Fatalf("limit 1: %v", semanticIDs(m))
	}
	// Camera and window filters.
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"red","cameras":["`+f.back.ID+`"]}`)
	if ids := semanticIDs(m); len(ids) != 1 || ids[0] != redBack.ID {
		t.Fatalf("camera filter: %v", ids)
	}
	from, to := f.t0.Add(30*time.Minute).UnixMilli(), f.t0.Add(90*time.Minute).UnixMilli()
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, fmt.Sprintf(`{"text":"red","from":%d,"to":%d}`, from, to))
	if ids := semanticIDs(m); len(ids) != 1 || ids[0] != redBack.ID {
		t.Fatalf("window: %v", ids)
	}

	// The operator scoped to Front never sees the back camera's match, even
	// when asking for it.
	code, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.op, `{"text":"red"}`)
	if ids := semanticIDs(m); code != 200 || len(ids) != 2 || strings.Contains(strings.Join(ids, ","), redBack.ID) {
		t.Fatalf("scoped: %d %v", code, ids)
	}
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.op, `{"text":"red","cameras":["`+f.back.ID+`"]}`)
	if ids := semanticIDs(m); len(ids) != 0 {
		t.Fatalf("scoped asking for back: %v", ids)
	}
	viewer := v2Token(t, f.s, "vw", "Viewer")
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/events/semantic", viewer, `{"text":"red"}`); code != 200 {
		t.Fatalf("viewer: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/events/semantic", "", `{"text":"red"}`); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	// Bad input.
	for _, bad := range []string{``, `{}`, `{"text":"r"}`, `{"text":"   r   "}`, `{"text":"` + strings.Repeat("x", 201) + `"}`,
		`{"text":"red","limit":101}`, `{"text":"red","limit":-1}`, `{"text":"red","from":10,"to":5}`,
		`{"text":"red","minScore":-0.1}`, `{"text":"red","minScore":1.5}`, `{"text":"red","junk":1}`} {
		if code, m := v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}
}

func TestSemanticModelSwapInvalidates(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	a := &fakeSemantic{model: "sha256:aaa1"}
	f.s.SetSemanticEngine(a, "")
	for i := 0; i < 2; i++ {
		e := addScene(t, f.s, f.front.ID, "Motion", f.t0.Add(time.Duration(i)*time.Minute), red)
		if _, err := f.s.embedSemantic(ctx, a, e); err != nil {
			t.Fatal(err)
		}
	}
	_, m := v2Call(t, f.h, "GET", "/api/v2/semantic", f.admin, "")
	if m["available"] != true || m["model"] != "sha256:aaa1" || m["indexed"] != float64(2) {
		t.Fatalf("status: %v", m)
	}
	b := &fakeSemantic{model: "sha256:bbb2"}
	f.s.SetSemanticEngine(b, "")
	if n, _ := f.s.Store.CountSemantic(ctx, "sha256:aaa1"); n != 0 {
		t.Fatalf("old vectors left: %d", n)
	}
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"red"}`)
	if ids := semanticIDs(m); len(ids) != 0 || m["model"] != "sha256:bbb2" {
		t.Fatalf("after swap: %v", m)
	}
}

func waitSemanticBackfill(t *testing.T, f *scopeFixture, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, m := v2Call(t, f.h, "GET", "/api/v2/semantic", f.admin, "")
		if bf, ok := m["backfill"].(map[string]any); ok && bf["state"] == state {
			return bf
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("semantic backfill never reached %s", state)
	return nil
}

func TestSemanticBackfill(t *testing.T) {
	f := newScopeFixture(t)
	eng := &fakeSemantic{model: "sha256:ccc3"}
	f.s.SetSemanticEngine(eng, "")
	for i := 0; i < 4; i++ {
		addScene(t, f.s, f.front.ID, "Motion", f.t0.Add(time.Duration(i)*time.Minute), red)
	}
	// The fixture's two events have snapshots that aren't JPEGs: skipped.
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/semantic/backfill", f.op, ""); code != 403 {
		t.Fatalf("operator may not start a backfill: %d", code)
	}
	code, m := v2Call(t, f.h, "POST", "/api/v2/semantic/backfill", f.admin, "")
	if code != 201 || m["taskId"] == nil {
		t.Fatalf("start: %d %v", code, m)
	}
	bf := waitSemanticBackfill(t, f, "done")
	if bf["total"] != float64(6) || bf["indexed"] != float64(4) || bf["skipped"] != float64(2) {
		t.Fatalf("done: %v", bf)
	}
	if tk := taskByID(m["taskId"].(string)); tk == nil || tk.Kind != "semantic" || tk.State != tasks.Done {
		t.Fatalf("task: %+v", tk)
	}
	if a := lastAudit(t, f.s, "Started semantic index"); !strings.Contains(a, "boss | Search | newest 50000 events") {
		t.Fatalf("audit = %q", a)
	}
	if a := lastAudit(t, f.s, "Semantic index built"); !strings.Contains(a, "4 of 6 events indexed, 2 skipped") {
		t.Fatalf("audit = %q", a)
	}
	_, st := v2Call(t, f.h, "GET", "/api/v2/semantic", f.op, "")
	if st["indexed"] != float64(4) {
		t.Fatalf("status: %v", st)
	}
	// A second run finds only the ones it couldn't embed.
	_, m = v2Call(t, f.h, "POST", "/api/v2/semantic/backfill", f.admin, "")
	if bf := waitSemanticBackfill(t, f, "done"); bf["taskId"] != m["taskId"] || bf["total"] != float64(2) {
		t.Fatalf("second run: %v", bf)
	}
	// Appearance's job is separate.
	if _, a := v2Call(t, f.h, "GET", "/api/v2/appearance", f.admin, ""); a["backfill"] != nil {
		t.Fatalf("appearance saw the semantic job: %v", a)
	}
}

func TestSemanticBackfillCancel(t *testing.T) {
	f := newScopeFixture(t)
	eng := &fakeSemantic{model: "sha256:ddd4", gate: make(chan struct{})}
	f.s.SetSemanticEngine(eng, "")
	for i := 0; i < 5; i++ {
		addScene(t, f.s, f.front.ID, "Motion", f.t0.Add(time.Duration(i+1)*time.Hour), red)
	}
	if code, m := v2Call(t, f.h, "DELETE", "/api/v2/semantic/backfill", f.admin, ""); code != 404 {
		t.Fatalf("cancel with no job: %d %v", code, m)
	}
	code, m := v2Call(t, f.h, "POST", "/api/v2/semantic/backfill", f.admin, "")
	if code != 201 {
		t.Fatalf("start: %d %v", code, m)
	}
	eng.gate <- struct{}{} // let exactly one through
	if code, again := v2Call(t, f.h, "POST", "/api/v2/semantic/backfill", f.admin, ""); code != 200 || again["taskId"] != m["taskId"] {
		t.Fatalf("restart while running: %d %v", code, again)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(eng.gate)
	}()
	code, c := v2Call(t, f.h, "DELETE", "/api/v2/semantic/backfill", f.admin, "")
	if code != 200 || c["state"] != "canceled" {
		t.Fatalf("cancel: %d %v", code, c)
	}
	if c["processed"].(float64) >= 7 {
		t.Fatalf("cancel did not stop the job: %v", c)
	}
	if tk := taskByID(m["taskId"].(string)); tk == nil || tk.State != tasks.Failed || tk.Error != "canceled" {
		t.Fatalf("task: %+v", tk)
	}
	if a := lastAudit(t, f.s, "Canceled semantic index"); a == "" {
		t.Fatal("cancel not audited")
	}
	if eng.imageCount() == 0 {
		t.Fatal("nothing embedded before the cancel")
	}
}

// End to end with the real CLIP models: index a cat snapshot and a car
// snapshot through the server, then "a photo of a cat" through the API
// must put the cat first. Needs SENTINEL_ORT_LIB and the files in
// linux/.models-cache (clip_image.onnx, clip_text.onnx, clip_vocab.json,
// clip_merges.txt, test_cat.jpg, test_car.jpg; see
// internal/semantic/engine_test.go for their sources). Skipped otherwise.
func TestSemanticRealModelCatAboveCar(t *testing.T) {
	cache, _ := filepath.Abs("../../.models-cache")
	lib := os.Getenv("SENTINEL_ORT_LIB")
	need := []string{"clip_image.onnx", "clip_text.onnx", "clip_vocab.json", "clip_merges.txt", "test_cat.jpg", "test_car.jpg"}
	for _, n := range need {
		if _, err := os.Stat(filepath.Join(cache, n)); err != nil {
			t.Skipf("real CLIP test skipped: %s not in %s (download Xenova/clip-vit-base-patch32 there)", n, cache)
		}
	}
	if lib == "" {
		t.Skip("real CLIP test skipped: SENTINEL_ORT_LIB is not set")
	}
	t.Setenv(semantic.ImageModelEnv, filepath.Join(cache, "clip_image.onnx"))
	t.Setenv(semantic.TextModelEnv, filepath.Join(cache, "clip_text.onnx"))
	t.Setenv(semantic.VocabEnv, filepath.Join(cache, "clip_vocab.json"))
	t.Setenv(semantic.MergesEnv, filepath.Join(cache, "clip_merges.txt"))
	eng, reason := semantic.Open(t.TempDir(), lib)
	if eng == nil {
		t.Fatal(reason)
	}
	f := newScopeFixture(t)
	f.s.SetSemanticEngine(eng, "")
	ctx := context.Background()
	add := func(name string, at time.Time) *store.Event {
		e := addScene(t, f.s, f.front.ID, "Motion", at, red)
		b, err := os.ReadFile(filepath.Join(cache, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.Thumbnail, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.embedSemantic(ctx, eng, e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	cat := add("test_cat.jpg", f.t0)
	car := add("test_car.jpg", f.t0.Add(time.Minute))
	code, m := v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"a photo of a cat","minScore":0}`)
	ids, sc := semanticIDs(m), similarScores(m)
	if code != 200 || len(ids) != 2 || ids[0] != cat.ID || ids[1] != car.ID {
		t.Fatalf("cat query: %d %v", code, m)
	}
	t.Logf("\"a photo of a cat\": cat %.4f, car %.4f", sc[0], sc[1])
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"a red car","minScore":0}`)
	if ids := semanticIDs(m); len(ids) != 2 || ids[0] != car.ID {
		t.Fatalf("car query: %v", m)
	}
	// With the default floor (0.2) the cat still matches; the car does not.
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/semantic", f.admin, `{"text":"a photo of a cat"}`)
	if ids := semanticIDs(m); len(ids) != 1 || ids[0] != cat.ID {
		t.Fatalf("default minScore: %v", m)
	}
}
