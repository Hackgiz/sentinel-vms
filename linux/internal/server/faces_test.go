package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/faces"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/store"
)

// fakeFaces "finds" a face exactly on the appearance tests' person box
// (40×70 px on their 200×100 thumbnail) plus one too small to keep, and
// describes a face by the box's mean color: red and redish are the same
// person, blue someone else. It is never a real model.
type fakeFaces struct {
	mu     sync.Mutex
	finds  int
	embeds int
	minW   int // images narrower than this have no faces (0 = all have)
}

func (f *fakeFaces) DetectModel() string { return "sha256:facedet" }
func (f *fakeFaces) EmbedModel() string  { return "sha256:facerec" }

func (f *fakeFaces) Find(img image.Image) ([]faces.Face, error) {
	f.mu.Lock()
	f.finds++
	minW := f.minW
	f.mu.Unlock()
	if img.Bounds().Dx() < minW {
		return nil, nil
	}
	return []faces.Face{
		{Box: [4]float64{0.5, 0.2, 0.7, 0.9}, Score: 0.93},
		{Box: [4]float64{0.05, 0.05, 0.15, 0.2}, Score: 0.99}, // 20×15 px: below MinFacePx
	}, nil
}

func (f *fakeFaces) Embed(img image.Image, face faces.Face) ([]float32, error) {
	f.mu.Lock()
	f.embeds++
	f.mu.Unlock()
	b := img.Bounds()
	r := image.Rect(int(face.Box[0]*float64(b.Dx()))+3, int(face.Box[1]*float64(b.Dy()))+3,
		int(face.Box[2]*float64(b.Dx()))-3, int(face.Box[3]*float64(b.Dy()))-3)
	var sr, sg, sb, n float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			sr, sg, sb, n = sr+float64(cr>>8), sg+float64(cg>>8), sb+float64(cb>>8), n+1
		}
	}
	return reid.Normalize([]float32{float32(sr / n), float32(sg / n), float32(sb / n)})
}

type facesFixture struct {
	*scopeFixture
	eng  *fakeFaces
	done chan string
}

// newFacesFixture is the scope fixture with the fake face engine loaded
// (faces still off) and a channel that hears each processed event.
func newFacesFixture(t *testing.T, eng faces.Engine, reason string) *facesFixture {
	t.Helper()
	f := &facesFixture{scopeFixture: newScopeFixture(t), done: make(chan string, 64)}
	f.eng, _ = eng.(*fakeFaces)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.s.Ctx = ctx
	f.s.faces.processed = func(id string) {
		select {
		case f.done <- id:
		default:
		}
	}
	f.s.SetFaceEngine(eng, reason)
	return f
}

// personEvent adds a Person event (thumbnail with a box of color c) with
// its Person alarm, as onDetection would.
func (f *facesFixture) personEvent(t *testing.T, cam *store.Camera, at time.Time, c color.RGBA) *store.Event {
	t.Helper()
	ctx := context.Background()
	e := addPerson(t, f.s, cam.ID, "Person", at, c)
	a, _, err := f.s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: cam.ID, CameraName: cam.Name, Kind: "Person", Title: "Person detected",
		Severity: "Critical", Detail: cam.Name + " detected a person.", EventID: e.ID, At: at, MergeWindow: time.Second, Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Store.SetEventAlarm(ctx, e.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	e.AlarmID = a.ID
	return e
}

// thumb delivers an event's thumbnail through the sink and waits until
// the faces worker has handled it.
func (f *facesFixture) thumb(t *testing.T, e *store.Event) {
	t.Helper()
	if err := f.s.Sink().Event(context.Background(), link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: e.CameraID,
		Thumbnail: &link.Thumbnail{EventID: e.ID, File: filepath.Base(e.Thumbnail)}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case id := <-f.done:
			if id == e.ID {
				return
			}
		case <-deadline:
			t.Fatalf("event %s was not processed", e.ID)
		}
	}
}

func (f *facesFixture) event(t *testing.T, id string) *store.Event {
	t.Helper()
	e, err := f.s.Store.Event(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *facesFixture) alarm(t *testing.T, id string) *store.Alarm {
	t.Helper()
	a, err := f.s.Store.Alarm(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func itemsOf(m map[string]any) []map[string]any {
	var out []map[string]any
	items, _ := m["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestFacesUnavailableAndOffByDefault(t *testing.T) {
	f := newFacesFixture(t, nil, "face models not installed (/data/models/face_det.onnx, /data/models/face_rec.onnx)")
	code, m := v2Call(t, f.h, "GET", "/api/v2/faces", f.op, "")
	if code != 200 || m["enabled"] != false || m["available"] != false || m["unassignedDays"] != float64(7) ||
		!strings.Contains(fmt.Sprint(m["reason"]), "face_det.onnx") || m["people"] != float64(0) || m["unassigned"] != float64(0) {
		t.Fatalf("status: %d %v", code, m)
	}
	// Turning it on without the models: 409 naming them; the setting stays off.
	code, m = v2Call(t, f.h, "PUT", "/api/v2/faces", f.admin, `{"enabled":true}`)
	if code != 409 || errCode(m) != "conflict" || !strings.Contains(fmt.Sprint(m["error"]), "face_rec.onnx") {
		t.Fatalf("enable without models: %d %v", code, m)
	}
	if f.s.facesEnabled(context.Background()) {
		t.Fatal("enabled without models")
	}
	for _, bad := range []string{`{"unassignedDays":0}`, `{"unassignedDays":91}`, `{"junk":1}`, `{"enabled":"yes"}`} {
		if code, m := v2Call(t, f.h, "PUT", "/api/v2/faces", f.admin, bad); code != 400 {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}
	if code, m = v2Call(t, f.h, "PUT", "/api/v2/faces", f.admin, `{"enabled":false,"unassignedDays":30}`); code != 200 || m["unassignedDays"] != float64(30) {
		t.Fatalf("days: %d %v", code, m)
	}
	if a := lastAudit(t, f.s, "Changed face recognition"); a != "boss | Faces | Face recognition off, unassigned faces kept 30 days" {
		t.Fatalf("audit %q", a)
	}
	if code, _ := v2Call(t, f.h, "PUT", "/api/v2/faces", f.op, `{"enabled":false}`); code != 403 {
		t.Fatalf("operator PUT: %d", code)
	}
	// A thumbnail arrives: nothing is queued, nothing stored, the event is
	// untouched.
	e := f.personEvent(t, f.front, f.t0, red)
	_ = f.s.Sink().Event(context.Background(), link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: e.CameraID,
		Thumbnail: &link.Thumbnail{EventID: e.ID, File: filepath.Base(e.Thumbnail)}})
	if list, _ := f.s.Store.EventFaces(context.Background(), e.ID); len(list) != 0 {
		t.Fatal("faces stored without models")
	}
	if a := f.alarm(t, e.AlarmID); a.Title != "Person detected" {
		t.Fatalf("alarm changed: %+v", a)
	}
}

func TestFacesOffDoesNothing(t *testing.T) {
	f := newFacesFixture(t, &fakeFaces{}, "")
	e := f.personEvent(t, f.front, f.t0, red)
	f.thumb(t, e)
	if f.eng.finds != 0 {
		t.Fatal("faces ran while off")
	}
	_, m := v2Call(t, f.h, "GET", "/api/v2/faces", f.admin, "")
	if m["available"] != true || m["enabled"] != false || m["detectModel"] != "sha256:facedet" || m["embedModel"] != "sha256:facerec" {
		t.Fatalf("status %v", m)
	}
}

func TestFacesRecognitionFlow(t *testing.T) {
	f := newFacesFixture(t, &fakeFaces{}, "")
	ctx := context.Background()
	if code, m := v2Call(t, f.h, "PUT", "/api/v2/faces", f.admin, `{"enabled":true}`); code != 200 || m["enabled"] != true {
		t.Fatalf("enable: %d %v", code, m)
	}
	if a := lastAudit(t, f.s, "Changed face recognition"); a != "boss | Faces | Face recognition on" {
		t.Fatalf("audit %q", a)
	}

	// 1. Nobody enrolled: the face is kept unassigned; the small one is not.
	e1 := f.personEvent(t, f.front, f.t0, red)
	f.thumb(t, e1)
	code, m := v2Call(t, f.h, "GET", "/api/v2/faces/unassigned", f.admin, "")
	un := itemsOf(m)
	if code != 200 || len(un) != 1 || un[0]["eventId"] != e1.ID || un[0]["cameraId"] != f.front.ID || un[0]["personId"] != nil ||
		un[0]["at"] != float64(e1.CreatedAt*1000) || un[0]["score"] != 0.93 {
		t.Fatalf("unassigned: %d %v", code, m)
	}
	if box := un[0]["box"].([]any); len(box) != 4 || box[0] != 0.5 || box[3] != 0.9 {
		t.Fatalf("box %v", box)
	}
	if e := f.event(t, e1.ID); e.PersonID != "" {
		t.Fatal("matched with an empty gallery")
	}
	face1 := un[0]["id"].(string)
	r := httptest.NewRequest("GET", "/api/v2/faces/"+face1+"/crop.jpg", nil)
	r.Header.Set("Authorization", "Bearer "+f.admin)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Body.Len() < 100 {
		t.Fatalf("crop: %d %q %d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
	// A repeated thumbnail does not store the face twice.
	f.thumb(t, e1)
	if list, _ := f.s.Store.EventFaces(ctx, e1.ID); len(list) != 1 {
		t.Fatalf("faces after repeat: %d", len(list))
	}

	// 2. Enroll it as a new person; the event gets the name.
	code, m = v2Call(t, f.h, "POST", "/api/v2/people", f.admin, `{"name":"Ann","notes":"neighbour","faceId":"`+face1+`"}`)
	if code != 201 || m["name"] != "Ann" || m["alert"] != "default" || len(m["faces"].([]any)) != 1 || m["lastSeenCameraId"] != f.front.ID {
		t.Fatalf("create: %d %v", code, m)
	}
	ann := m["id"].(string)
	if e := f.event(t, e1.ID); e.PersonID != ann || e.PersonName != "Ann" {
		t.Fatalf("enrolled event: %+v", e)
	}
	if a := lastAudit(t, f.s, "Added person"); a != "boss | Faces | Ann, with a face from Front door" {
		t.Fatalf("audit %q", a)
	}

	// 3. A new sighting matches: event named, alarm named (default alert).
	e2 := f.personEvent(t, f.front, f.t0.Add(time.Hour), redish)
	f.thumb(t, e2)
	ev := f.event(t, e2.ID)
	if ev.PersonID != ann || ev.PersonName != "Ann" {
		t.Fatalf("match: %+v", ev)
	}
	if a := f.alarm(t, e2.AlarmID); a.Title != "Ann detected" || a.Detail != "Ann at Front door" || a.Severity != "Critical" || a.State != "New" {
		t.Fatalf("default alert: %+v", a)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/events/"+e2.ID+"/faces", f.admin, "")
	ef := itemsOf(m)
	if len(ef) != 1 || ef[0]["personId"] != ann || ef[0]["similarity"].(float64) < faces.MatchThreshold {
		t.Fatalf("event faces: %v", m)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/events/"+e2.ID, f.admin, "")
	if m["personId"] != ann || m["personName"] != "Ann" {
		t.Fatalf("v2 event: %v", m)
	}

	// 4. Someone else: kept unassigned, nothing named.
	e3 := f.personEvent(t, f.front, f.t0.Add(2*time.Hour), blue)
	f.thumb(t, e3)
	if ev := f.event(t, e3.ID); ev.PersonID != "" {
		t.Fatalf("blue matched Ann: %+v", ev)
	}
	if a := f.alarm(t, e3.AlarmID); a.Title != "Person detected" {
		t.Fatalf("unmatched alarm changed: %+v", a)
	}

	// 5. alert never: the alarm is closed as a known person, audited.
	if code, m := v2Call(t, f.h, "PATCH", "/api/v2/people/"+ann, f.admin, `{"alert":"never"}`); code != 200 || m["alert"] != "never" || m["notes"] != "neighbour" {
		t.Fatalf("patch alert: %d %v", code, m)
	}
	e4 := f.personEvent(t, f.front, f.t0.Add(3*time.Hour), redish)
	f.thumb(t, e4)
	a4 := f.alarm(t, e4.AlarmID)
	if a4.State != "Resolved" || !strings.Contains(strings.Join(a4.ResponseLog, "|"), "Known person: Ann") {
		t.Fatalf("never: %+v", a4)
	}
	if a := lastAudit(t, f.s, "Set alarm resolved"); !strings.Contains(a, "System | Alarms | Person detected (Front door): Known person: Ann") {
		t.Fatalf("close audit %q", a)
	}

	// 6. alert always: a Critical Watchlist alarm linked to the event; the
	// Person alarm is left as it was.
	_, _ = v2Call(t, f.h, "PATCH", "/api/v2/people/"+ann, f.admin, `{"alert":"always"}`)
	e5 := f.personEvent(t, f.back, f.t0.Add(4*time.Hour), red)
	f.thumb(t, e5)
	if a := f.alarm(t, e5.AlarmID); a.Title != "Person detected" || a.State != "New" {
		t.Fatalf("always touched the Person alarm: %+v", a)
	}
	var watch *store.Alarm
	alarms, _ := f.s.Store.Alarms(ctx, true, 100)
	for i := range alarms {
		if alarms[i].Kind == "Watchlist" {
			watch = &alarms[i]
		}
	}
	if watch == nil || watch.Title != "Watchlist: Ann" || watch.Severity != "Critical" || watch.EventID != e5.ID ||
		watch.CameraID != f.back.ID || !strings.HasPrefix(watch.Detail, "Ann at Back gate, ") {
		t.Fatalf("watchlist alarm: %+v", watch)
	}

	// 7. Vehicles are never searched.
	car := addPerson(t, f.s, f.front.ID, "Vehicle", f.t0.Add(5*time.Hour), red)
	before := f.eng.finds
	f.thumb(t, car)
	if f.eng.finds != before {
		t.Fatal("a Vehicle event was searched for faces")
	}

	// 8. Rename: past events follow. People list by name with faces.
	if code, m := v2Call(t, f.h, "PATCH", "/api/v2/people/"+ann, f.admin, `{"name":"Anne"}`); code != 200 || m["name"] != "Anne" {
		t.Fatalf("rename: %d %v", code, m)
	}
	if e := f.event(t, e2.ID); e.PersonName != "Anne" {
		t.Fatal("rename did not reach past events")
	}
	if a := lastAudit(t, f.s, "Changed person"); a != "boss | Faces | Anne: renamed from Ann" {
		t.Fatalf("audit %q", a)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/people", f.op, "")
	people := itemsOf(m)
	if len(people) != 1 || people[0]["name"] != "Anne" {
		t.Fatalf("people: %v", m)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/faces", f.admin, "")
	if m["people"] != float64(1) || m["unassigned"] != float64(1) {
		t.Fatalf("status counts: %v", m)
	}

	// 9. Delete: their faces go, their events keep the name.
	if code, _ := v2Call(t, f.h, "DELETE", "/api/v2/people/"+ann, f.admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if list, _ := f.s.Store.PersonFaces(ctx, ann); len(list) != 0 {
		t.Fatal("faces left")
	}
	if e := f.event(t, e2.ID); e.PersonName != "Anne" {
		t.Fatal("event lost the name")
	}
	if a := lastAudit(t, f.s, "Deleted person"); a != "boss | Faces | Anne and 4 faces" {
		t.Fatalf("audit %q", a)
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/people/"+ann, f.admin, ""); code != 404 {
		t.Fatal("deleted person still there")
	}
}

func TestFacesEnrollAndValidation(t *testing.T) {
	f := newFacesFixture(t, &fakeFaces{}, "")
	ctx := context.Background()
	_ = f.s.Store.SetSetting(ctx, SettingFacesEnabled, "1")
	e1 := f.personEvent(t, f.front, f.t0, red)
	e2 := f.personEvent(t, f.front, f.t0.Add(time.Minute), blue)
	f.thumb(t, e1)
	f.thumb(t, e2)
	fa, _ := f.s.Store.EventFaces(ctx, e1.ID)
	fb, _ := f.s.Store.EventFaces(ctx, e2.ID)
	if len(fa) != 1 || len(fb) != 1 {
		t.Fatal("faces not stored")
	}
	_, m := v2Call(t, f.h, "POST", "/api/v2/people", f.admin, `{"name":"Bob","alert":"always"}`)
	bob := m["id"].(string)
	if m["alert"] != "always" || len(m["faces"].([]any)) != 0 {
		t.Fatalf("create bare: %v", m)
	}
	for _, bad := range []string{`{}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("x", 81) + `"}`, `{"name":"x","alert":"sometimes"}`,
		`{"name":"x","notes":"` + strings.Repeat("n", 501) + `"}`, `{"name":"x","junk":1}`} {
		if code, m := v2Call(t, f.h, "POST", "/api/v2/people", f.admin, bad); code != 400 {
			t.Errorf("create %s: %d %v", bad, code, m)
		}
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/people", f.admin, `{"name":"x","faceId":"nope"}`); code != 404 {
		t.Fatalf("create with missing face: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/people", f.op, `{"name":"x"}`); code != 403 {
		t.Fatalf("operator create: %d", code)
	}

	// Enroll e2's face for Bob; the event is named; again → 409.
	code, m := v2Call(t, f.h, "POST", "/api/v2/people/"+bob+"/faces", f.admin, `{"faceId":"`+fb[0].ID+`"}`)
	if code != 200 || len(m["faces"].([]any)) != 1 {
		t.Fatalf("enroll: %d %v", code, m)
	}
	if e := f.event(t, e2.ID); e.PersonID != bob || e.PersonName != "Bob" {
		t.Fatalf("enrolled event: %+v", e)
	}
	if a := lastAudit(t, f.s, "Enrolled face"); a != "boss | Faces | Bob, from Front door" {
		t.Fatalf("audit %q", a)
	}
	if code, m := v2Call(t, f.h, "POST", "/api/v2/people/"+bob+"/faces", f.admin, `{"faceId":"`+fb[0].ID+`"}`); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("enroll twice: %d %v", code, m)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/people", f.admin, `{"name":"y","faceId":"`+fb[0].ID+`"}`); code != 409 {
		t.Fatalf("create with an assigned face: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/people/nope/faces", f.admin, `{"faceId":"`+fa[0].ID+`"}`); code != 404 {
		t.Fatalf("enroll for missing person: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/people/"+bob+"/faces", f.admin, `{}`); code != 400 {
		t.Fatalf("enroll without faceId: %d", code)
	}
	if code, _ := v2Call(t, f.h, "PATCH", "/api/v2/people/"+bob, f.admin, `{"name":""}`); code != 400 {
		t.Fatalf("patch empty name: %d", code)
	}
	if code, _ := v2Call(t, f.h, "PATCH", "/api/v2/people/nope", f.admin, `{"name":"z"}`); code != 404 {
		t.Fatalf("patch missing: %d", code)
	}

	// Delete one face.
	if code, _ := v2Call(t, f.h, "DELETE", "/api/v2/faces/"+fa[0].ID, f.op, ""); code != 403 {
		t.Fatalf("operator delete: %d", code)
	}
	if code, _ := v2Call(t, f.h, "DELETE", "/api/v2/faces/"+fa[0].ID, f.admin, ""); code != 204 {
		t.Fatalf("delete face: %d", code)
	}
	if a := lastAudit(t, f.s, "Deleted face"); a != "boss | Faces | unassigned face from Front door" {
		t.Fatalf("audit %q", a)
	}
	if code, _ := v2Call(t, f.h, "DELETE", "/api/v2/faces/"+fa[0].ID, f.admin, ""); code != 404 {
		t.Fatal("delete twice")
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/faces/unassigned?limit=0", f.admin, ""); code != 400 {
		t.Fatal("bad limit")
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/faces/unassigned", f.op, ""); code != 403 {
		t.Fatal("operator unassigned")
	}
}

func TestFacesCameraScope(t *testing.T) {
	f := newFacesFixture(t, &fakeFaces{}, "")
	ctx := context.Background()
	_ = f.s.Store.SetSetting(ctx, SettingFacesEnabled, "1")
	front := f.personEvent(t, f.front, f.t0, red)
	back := f.personEvent(t, f.back, f.t0.Add(time.Minute), red)
	f.thumb(t, front)
	f.thumb(t, back)
	ff, _ := f.s.Store.EventFaces(ctx, front.ID)
	bf, _ := f.s.Store.EventFaces(ctx, back.ID)
	_, m := v2Call(t, f.h, "POST", "/api/v2/people", f.admin, `{"name":"Cy","faceId":"`+bf[0].ID+`"}`)
	cy := m["id"].(string)

	crop := func(token, id string) int {
		r := httptest.NewRequest("GET", "/api/v2/faces/"+id+"/crop.jpg", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w.Code
	}
	// f.op sees only the front camera.
	if c := crop(f.op, ff[0].ID); c != 200 {
		t.Fatalf("front crop: %d", c)
	}
	if c := crop(f.op, bf[0].ID); c != http.StatusNotFound {
		t.Fatalf("back crop for a front-only operator: %d", c)
	}
	if c := crop(f.op, "nope"); c != 404 {
		t.Fatal("missing crop")
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/events/"+back.ID+"/faces", f.op, ""); code != 404 {
		t.Fatalf("back event faces: %d", code)
	}
	if code, m := v2Call(t, f.h, "GET", "/api/v2/events/"+front.ID+"/faces", f.op, ""); code != 200 || len(itemsOf(m)) != 1 {
		t.Fatalf("front event faces: %d %v", code, m)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/people/"+cy, f.op, "")
	if len(m["faces"].([]any)) != 0 || m["lastSeenCameraId"] != nil {
		t.Fatalf("person seen through scope: %v", m)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/people/"+cy, f.admin, "")
	if len(m["faces"].([]any)) != 1 || m["lastSeenCameraId"] != f.back.ID {
		t.Fatalf("admin person: %v", m)
	}
}

func TestFacesQueueDropsOldest(t *testing.T) {
	s := &Server{}
	s.faces.engine = &fakeFaces{}
	s.faces.queue = make(chan string, 2)
	for _, id := range []string{"a", "b", "c"} {
		s.facesOnThumbnail(id)
	}
	if got := <-s.faces.queue + <-s.faces.queue; got != "bc" {
		t.Fatalf("queue kept %q, want the newest two", got)
	}
	s.faces.engine = nil
	s.facesOnThumbnail("d")
	if len(s.faces.queue) != 0 {
		t.Fatal("queued without an engine")
	}
}

func TestFacesPurgeUnassigned(t *testing.T) {
	f := newFacesFixture(t, &fakeFaces{}, "")
	ctx := context.Background()
	_ = f.s.Store.SetSetting(ctx, SettingFacesUnassignedDays, "2")
	e := f.personEvent(t, f.front, f.t0, red)
	p := &store.Person{Name: "Dee"}
	_ = f.s.Store.AddPerson(ctx, p)
	add := func(age time.Duration, person string) *store.Face {
		fc := &store.Face{EventID: e.ID, CameraID: e.CameraID, AtMs: time.Now().Add(-age).UnixMilli(), Model: "m", Vec: []byte{0, 0, 0, 0}, PersonID: person}
		if err := f.s.Store.AddFace(ctx, fc, nil); err != nil {
			t.Fatal(err)
		}
		return fc
	}
	old, fresh, enrolled := add(72*time.Hour, ""), add(24*time.Hour, ""), add(72*time.Hour, p.ID)
	f.s.purgeUnassignedFaces(ctx)
	if _, err := f.s.Store.Face(ctx, old.ID); err == nil {
		t.Fatal("old unassigned face kept")
	}
	for _, fc := range []*store.Face{fresh, enrolled} {
		if _, err := f.s.Store.Face(ctx, fc.ID); err != nil {
			t.Fatal("purged a face it should keep")
		}
	}
	if d := f.s.facesUnassignedDays(ctx); d != 2 {
		t.Fatalf("days %d", d)
	}
	_ = f.s.Store.SetSetting(ctx, SettingFacesUnassignedDays, "junk")
	if d := f.s.facesUnassignedDays(ctx); d != 7 {
		t.Fatalf("default days %d", d)
	}
}

// solidJPEG is a w×h JPEG of one color.
func solidJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A face too small in the 200 px thumbnail is found in the full-resolution
// recorded frame around the person, and stored in full-frame coordinates.
func TestFacesFallBackToRecordedFrame(t *testing.T) {
	eng := &fakeFaces{minW: 400} // the 200×100 thumbnail holds no usable face
	f := newFacesFixture(t, eng, "")
	if code, _ := v2Call(t, f.h, "PUT", "/api/v2/faces", f.admin, `{"enabled":true}`); code != 200 {
		t.Fatal("enable")
	}
	var asked []int64
	var mu sync.Mutex
	frame := solidJPEG(t, 2000, 1000, red)
	f.s.faces.recorded = func(ctx context.Context, e *store.Event) ([]byte, bool) {
		mu.Lock()
		asked = append(asked, e.CreatedAt)
		mu.Unlock()
		return frame, true
	}

	e := f.personEvent(t, f.front, f.t0, red)
	f.thumb(t, e)
	mu.Lock()
	if len(asked) != 1 || asked[0] != e.CreatedAt {
		t.Fatalf("recorded frame asked %v", asked)
	}
	mu.Unlock()
	list, err := f.s.Store.EventFaces(context.Background(), e.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("faces %d %v", len(list), err)
	}
	// personBox [0.5,0.2,0.7,0.9] gives the crop x 800–1600, y 95–725 of the
	// 2000×1000 frame; the fake's face [0.5,0.2,0.7,0.9] of that crop is
	// x 0.6–0.68, y 0.221–0.662 of the frame.
	want := "[0.6,0.221,0.68,0.662]"
	found := false
	for _, fc := range list {
		if fc.Box == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("no face at %s: %+v", want, list)
	}

	// Without a recording the event simply has no faces.
	f.s.faces.recorded = func(ctx context.Context, e *store.Event) ([]byte, bool) { return nil, false }
	e2 := f.personEvent(t, f.front, f.t0.Add(time.Minute), red)
	f.thumb(t, e2)
	if list, _ := f.s.Store.EventFaces(context.Background(), e2.ID); len(list) != 0 {
		t.Fatalf("faces without a recording: %d", len(list))
	}
}

func TestFaceRegion(t *testing.T) {
	if r := faceRegion(""); r != [4]float64{0, 0, 1, 1} {
		t.Fatalf("no box: %v", r)
	}
	if r := faceRegion("[0.9,0.0,1.0,0.4]"); math.Abs(r[0]-0.85) > 1e-9 || r[1] != 0 || r[2] != 1 || math.Abs(r[3]-0.3) > 1e-9 {
		t.Fatalf("edge box: %v", r)
	}
}
