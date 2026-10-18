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
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

// fakeEmbedder describes a crop by its mean color (so a red-shirted
// "person" matches other red ones) and records what it was shown. It is
// never a real model.
type fakeEmbedder struct {
	model string
	mu    sync.Mutex
	crops []image.Rectangle
	gate  chan struct{} // non-nil: each Embed waits for a token (cancel tests)
	err   error
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(crop image.Image) ([]float32, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	f.crops = append(f.crops, crop.Bounds())
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var r, g, b, n float64
	bd := crop.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			cr, cg, cb, _ := crop.At(x, y).RGBA()
			r, g, b, n = r+float64(cr>>8), g+float64(cg>>8), b+float64(cb>>8), n+1
		}
	}
	return reid.Normalize([]float32{float32(r/n) + 1, float32(g/n) + 1, float32(b/n) + 1})
}

func (f *fakeEmbedder) cropCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.crops)
}

// personJPEG is a 200×100 gray frame with a box of color c at
// [0.5,0.2]-[0.7,0.9] (the event's detection box).
func personJPEG(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{128, 128, 128, 255})
			if x >= 100 && x < 140 && y >= 20 && y < 90 {
				img.Set(x, y, c)
			}
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	return buf.Bytes()
}

const personBox = "[0.5,0.2,0.7,0.9]"

var (
	red    = color.RGBA{220, 20, 20, 255}
	redish = color.RGBA{200, 40, 30, 255}
	blue   = color.RGBA{20, 20, 220, 255}
)

// addPerson stores an event whose thumbnail (served by the local recording
// server like a real one) shows a box of color c.
func addPerson(t *testing.T, s *Server, cam, kind string, at time.Time, c color.RGBA) *store.Event {
	t.Helper()
	dir := filepath.Join(s.Store.DataDir(), "events")
	_ = os.MkdirAll(dir, 0o755)
	e := &store.Event{ID: store.NewID(), CameraID: cam, Kind: kind, Score: 0.9, CreatedAt: at.Unix(), Box: personBox}
	e.Thumbnail = filepath.Join(dir, e.ID+".jpg")
	if err := os.WriteFile(e.Thumbnail, personJPEG(c), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.AddEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return e
}

func similarIDs(m map[string]any) []string {
	var ids []string
	items, _ := m["items"].([]any)
	for _, it := range items {
		ids = append(ids, it.(map[string]any)["event"].(map[string]any)["id"].(string))
	}
	return ids
}

func similarScores(m map[string]any) []float64 {
	var out []float64
	items, _ := m["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["score"].(float64))
	}
	return out
}

func lastAudit(t *testing.T, s *Server, action string) string {
	t.Helper()
	logs, _ := s.Store.AuditLog(context.Background(), 50)
	for _, l := range logs {
		if l.Action == action {
			return l.User + " | " + l.Area + " | " + l.Detail
		}
	}
	return ""
}

func TestAppearanceUnavailableDegrades(t *testing.T) {
	f := newScopeFixture(t)
	q := addPerson(t, f.s, f.front.ID, "Person", f.t0, red)
	code, m := v2Call(t, f.h, "GET", "/api/v2/appearance", f.op, "")
	if code != 200 || m["available"] != false || !strings.Contains(fmt.Sprint(m["reason"]), "no appearance model") {
		t.Fatalf("status: %d %v", code, m)
	}
	f.s.SetAppearanceEmbedder(nil, "no appearance model installed (/data/models/reid.onnx)")
	code, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.op, "{}")
	if code != 503 || errCode(m) != "unavailable" || !strings.Contains(fmt.Sprint(m["error"]), "reid.onnx") {
		t.Fatalf("similar: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "POST", "/api/v2/appearance/backfill", f.admin, "")
	if code != 409 || errCode(m) != "conflict" {
		t.Fatalf("backfill: %d %v", code, m)
	}
	// Thumbnails still arrive fine; nothing is indexed.
	_ = f.s.Sink().Event(context.Background(), link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: f.front.ID,
		Thumbnail: &link.Thumbnail{EventID: q.ID, File: filepath.Base(q.Thumbnail)}})
	if n, _ := f.s.Store.CountEmbeddings(context.Background(), ""); n != 0 {
		t.Fatal("indexed without a model")
	}
}

func TestEmbedOnThumbnailCropsTheBox(t *testing.T) {
	f := newScopeFixture(t)
	emb := &fakeEmbedder{model: "sha256:aaaa"}
	f.s.SetAppearanceEmbedder(emb, "")
	ctx := context.Background()
	e := addPerson(t, f.s, f.front.ID, "Person", f.t0, red)
	motion := &store.Event{CameraID: f.front.ID, Kind: "Motion", Score: 0.5, CreatedAt: f.t0.Unix(), Thumbnail: e.Thumbnail}
	_ = f.s.Store.AddEvent(ctx, motion)
	for _, id := range []string{motion.ID, e.ID} {
		if err := f.s.Sink().Event(ctx, link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: f.front.ID,
			Thumbnail: &link.Thumbnail{EventID: id, File: filepath.Base(e.Thumbnail)}}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		if b, err := f.s.Store.EventEmbedding(ctx, e.ID, "sha256:aaaa"); err == nil {
			raw = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if raw == nil {
		t.Fatal("event was not embedded after its thumbnail arrived")
	}
	v, err := reid.Decode(raw)
	if err != nil || len(v) != 3 || v[0] < 0.9 { // red dominates: the box, not the gray frame
		t.Fatalf("vector %v %v", v, err)
	}
	if _, err := f.s.Store.EventEmbedding(ctx, motion.ID, "sha256:aaaa"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a motion event (no box) must not be embedded")
	}
	emb.mu.Lock()
	crop := emb.crops[0]
	emb.mu.Unlock()
	if crop.Dx() != 36 || crop.Dy() != 66 { // 40×70 box minus the 2 px outline inset
		t.Fatalf("crop = %v, want the detection box", crop)
	}
}

func TestSimilarSearchScopeFiltersAudit(t *testing.T) {
	f := newScopeFixture(t)
	emb := &fakeEmbedder{model: "sha256:bbbb"}
	f.s.SetAppearanceEmbedder(emb, "")
	ctx := context.Background()
	q := addPerson(t, f.s, f.front.ID, "Person", f.t0, red)
	sameFront := addPerson(t, f.s, f.front.ID, "Person", f.t0.Add(time.Hour), redish)
	back := addPerson(t, f.s, f.back.ID, "Person", f.t0.Add(2*time.Hour), red)
	other := addPerson(t, f.s, f.front.ID, "Person", f.t0.Add(3*time.Hour), blue)
	car := addPerson(t, f.s, f.front.ID, "Vehicle", f.t0.Add(4*time.Hour), red)
	for _, e := range []*store.Event{sameFront, back, other, car} {
		if _, err := f.s.embedEvent(ctx, emb, e); err != nil {
			t.Fatal(err)
		}
	}

	// Admin: everything of the same kind, best first; the query event itself
	// is computed on demand and never returned.
	code, m := v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, "{}")
	ids := similarIDs(m)
	if code != 200 || len(ids) != 3 || ids[2] != other.ID || m["model"] != "sha256:bbbb" {
		t.Fatalf("admin: %d %v", code, m)
	}
	if sc := similarScores(m); sc[0] < 0.99 || sc[2] > sc[1] {
		t.Fatalf("scores %v", sc)
	}
	if _, err := f.s.Store.EventEmbedding(ctx, q.ID, "sha256:bbbb"); err != nil {
		t.Fatal("query vector should be stored after an on-demand embed")
	}
	if a := lastAudit(t, f.s, "Appearance search"); !strings.Contains(a, "boss | Search | Front door person") || !strings.Contains(a, "3 similar") {
		t.Fatalf("audit = %q", a)
	}
	// anyKind brings the vehicle in; limit trims.
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, `{"anyKind":true}`)
	if ids := similarIDs(m); len(ids) != 4 {
		t.Fatalf("anyKind: %v", ids)
	}
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, `{"limit":1}`)
	if ids := similarIDs(m); len(ids) != 1 || (ids[0] != back.ID && ids[0] != sameFront.ID) {
		t.Fatalf("limit 1: %v", ids)
	}
	// Camera and window filters.
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, `{"cameras":["`+f.back.ID+`"]}`)
	if ids := similarIDs(m); len(ids) != 1 || ids[0] != back.ID {
		t.Fatalf("camera filter: %v", ids)
	}
	from, to := f.t0.Add(30*time.Minute).UnixMilli(), f.t0.Add(90*time.Minute).UnixMilli()
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, fmt.Sprintf(`{"from":%d,"to":%d}`, from, to))
	if ids := similarIDs(m); len(ids) != 1 || ids[0] != sameFront.ID {
		t.Fatalf("window: %v", ids)
	}
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, `{"minScore":0.99}`)
	for _, id := range similarIDs(m) {
		if id == other.ID {
			t.Fatal("minScore let the blue one through")
		}
	}

	// The operator scoped to Front never sees the back camera's match, even
	// when asking for it, and can't search from a back camera event.
	code, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.op, "{}")
	if ids := similarIDs(m); code != 200 || len(ids) != 2 || strings.Contains(strings.Join(ids, ","), back.ID) {
		t.Fatalf("scoped: %d %v", code, ids)
	}
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.op, `{"cameras":["`+f.back.ID+`"]}`)
	if ids := similarIDs(m); len(ids) != 0 {
		t.Fatalf("scoped asking for back: %v", ids)
	}
	if code, m := v2Call(t, f.h, "POST", "/api/v2/events/"+back.ID+"/similar", f.op, "{}"); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("out-of-scope query event: %d %v", code, m)
	}
	// Viewers search events today, so they may search by appearance too.
	viewer := v2Token(t, f.s, "vw", "Viewer")
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", viewer, "{}"); code != 200 {
		t.Fatalf("viewer: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", "", "{}"); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}

	// Bad input and events without a detection.
	for _, bad := range []string{`{"limit":101}`, `{"limit":-1}`, `{"from":10,"to":5}`, `{"minScore":2}`, `{"junk":1}`} {
		if code, m := v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}
	motion := f.event[f.front.ID] // fixture Person event without a box
	if code, m := v2Call(t, f.h, "POST", "/api/v2/events/"+motion.ID+"/similar", f.admin, "{}"); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "no detection box") {
		t.Fatalf("no box: %d %v", code, m)
	}
	noThumb := &store.Event{CameraID: f.front.ID, Kind: "Person", CreatedAt: f.t0.Unix(), Box: personBox}
	_ = f.s.Store.AddEvent(ctx, noThumb)
	if code, m := v2Call(t, f.h, "POST", "/api/v2/events/"+noThumb.ID+"/similar", f.admin, "{}"); code != 409 {
		t.Fatalf("no thumbnail: %d %v", code, m)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/events/nope/similar", f.admin, "{}"); code != 404 {
		t.Fatalf("missing event: %d", code)
	}
}

func TestAppearanceModelSwapInvalidates(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	a := &fakeEmbedder{model: "sha256:1111"}
	f.s.SetAppearanceEmbedder(a, "")
	q := addPerson(t, f.s, f.front.ID, "Person", f.t0, red)
	m2 := addPerson(t, f.s, f.front.ID, "Person", f.t0.Add(time.Minute), red)
	for _, e := range []*store.Event{q, m2} {
		if _, err := f.s.embedEvent(ctx, a, e); err != nil {
			t.Fatal(err)
		}
	}
	_, m := v2Call(t, f.h, "GET", "/api/v2/appearance", f.admin, "")
	if m["available"] != true || m["model"] != "sha256:1111" || m["indexed"] != float64(2) {
		t.Fatalf("status: %v", m)
	}
	// A new model file: old vectors go, nothing compares across models.
	b := &fakeEmbedder{model: "sha256:2222"}
	f.s.SetAppearanceEmbedder(b, "")
	if n, _ := f.s.Store.CountEmbeddings(ctx, "sha256:1111"); n != 0 {
		t.Fatalf("old vectors left: %d", n)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/appearance", f.admin, "")
	if m["model"] != "sha256:2222" || m["indexed"] != float64(0) {
		t.Fatalf("after swap: %v", m)
	}
	_, m = v2Call(t, f.h, "POST", "/api/v2/events/"+q.ID+"/similar", f.admin, "{}")
	if ids := similarIDs(m); len(ids) != 0 || b.cropCount() != 1 {
		t.Fatalf("after swap, before re-index: %v (crops %d)", ids, b.cropCount())
	}
}

func waitBackfill(t *testing.T, f *scopeFixture, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, m := v2Call(t, f.h, "GET", "/api/v2/appearance", f.admin, "")
		if bf, ok := m["backfill"].(map[string]any); ok && bf["state"] == state {
			return bf
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backfill never reached %s", state)
	return nil
}

func taskByID(id string) *tasks.Task {
	for _, tk := range tasks.Default.List() {
		if tk.ID == id {
			return &tk
		}
	}
	return nil
}

func TestAppearanceBackfill(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	emb := &fakeEmbedder{model: "sha256:cccc"}
	f.s.SetAppearanceEmbedder(emb, "")
	for i := 0; i < 4; i++ {
		addPerson(t, f.s, f.front.ID, "Person", f.t0.Add(time.Duration(i)*time.Minute), red)
	}
	tiny := &store.Event{CameraID: f.back.ID, Kind: "Person", CreatedAt: f.t0.Unix(), Box: "[0.1,0.1,0.11,0.12]"}
	tiny.Thumbnail = addPerson(t, f.s, f.back.ID, "Animal", f.t0, red).Thumbnail // reuse a JPEG
	_ = f.s.Store.AddEvent(ctx, tiny)

	if code, _ := v2Call(t, f.h, "POST", "/api/v2/appearance/backfill", f.op, ""); code != 403 {
		t.Fatalf("operator may not start a backfill: %d", code)
	}
	code, m := v2Call(t, f.h, "POST", "/api/v2/appearance/backfill", f.admin, "")
	if code != 201 || m["taskId"] == nil {
		t.Fatalf("start: %d %v", code, m)
	}
	bf := waitBackfill(t, f, "done")
	if bf["total"] != float64(6) || bf["indexed"] != float64(5) || bf["skipped"] != float64(1) {
		t.Fatalf("done: %v", bf)
	}
	if tk := taskByID(m["taskId"].(string)); tk == nil || tk.Kind != "appearance" || tk.State != tasks.Done {
		t.Fatalf("task: %+v", tk)
	}
	if a := lastAudit(t, f.s, "Started appearance index"); !strings.Contains(a, "boss | Search") {
		t.Fatalf("audit = %q", a)
	}
	// A second run finds only the one it couldn't embed.
	_, m = v2Call(t, f.h, "POST", "/api/v2/appearance/backfill", f.admin, "")
	if bf := waitBackfill(t, f, "done"); bf["taskId"] != m["taskId"] || bf["total"] != float64(1) {
		t.Fatalf("second run: %v", bf)
	}
}

func TestAppearanceBackfillCancel(t *testing.T) {
	f := newScopeFixture(t)
	emb := &fakeEmbedder{model: "sha256:dddd", gate: make(chan struct{})}
	f.s.SetAppearanceEmbedder(emb, "")
	for i := 0; i < 5; i++ {
		addPerson(t, f.s, f.front.ID, "Person", f.t0.Add(time.Duration(i)*time.Minute), red)
	}
	if code, m := v2Call(t, f.h, "DELETE", "/api/v2/appearance/backfill", f.admin, ""); code != 404 {
		t.Fatalf("cancel with no job: %d %v", code, m)
	}
	code, m := v2Call(t, f.h, "POST", "/api/v2/appearance/backfill", f.admin, "")
	if code != 201 {
		t.Fatalf("start: %d %v", code, m)
	}
	emb.gate <- struct{}{} // let exactly one through
	// Starting again while it runs returns the running job.
	if code, again := v2Call(t, f.h, "POST", "/api/v2/appearance/backfill", f.admin, ""); code != 200 || again["taskId"] != m["taskId"] {
		t.Fatalf("restart while running: %d %v", code, again)
	}
	go func() { // unblock the embed in flight once cancel is underway
		time.Sleep(50 * time.Millisecond)
		close(emb.gate)
	}()
	code, c := v2Call(t, f.h, "DELETE", "/api/v2/appearance/backfill", f.admin, "")
	if code != 200 || c["state"] != "canceled" {
		t.Fatalf("cancel: %d %v", code, c)
	}
	if c["processed"].(float64) >= 5 {
		t.Fatalf("cancel did not stop the job: %v", c)
	}
	if tk := taskByID(m["taskId"].(string)); tk == nil || tk.State != tasks.Failed || tk.Error != "canceled" {
		t.Fatalf("task: %+v", tk)
	}
	if a := lastAudit(t, f.s, "Canceled appearance index"); a == "" {
		t.Fatal("cancel not audited")
	}
}
