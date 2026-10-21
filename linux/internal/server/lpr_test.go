package server

import (
	"context"
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/lpr"
	"sentinel-linux/internal/store"
)

// fakePlateReader "reads" a fixed text and records the crops it was shown.
// It is never a real model.
type fakePlateReader struct {
	mu    sync.Mutex
	text  string
	crops []image.Rectangle
}

func (f *fakePlateReader) Read(img image.Image) (*lpr.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crops = append(f.crops, img.Bounds())
	if f.text == "" {
		return nil, nil
	}
	return &lpr.Result{Text: f.text, Confidence: 0.9, Raw: f.text}, nil
}
func (f *fakePlateReader) DetModel() string { return "sha256:det0" }
func (f *fakePlateReader) RecModel() string { return "sha256:rec0" }

func (f *fakePlateReader) set(text string) {
	f.mu.Lock()
	f.text = text
	f.mu.Unlock()
}

func (f *fakePlateReader) cropCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.crops)
}

// addVehicle stores a Vehicle event with a thumbnail and its open Vehicle
// alarm, as sink.go would have raised it.
func addVehicle(t *testing.T, f *scopeFixture, cam *store.Camera, at time.Time) (*store.Event, *store.Alarm) {
	t.Helper()
	ctx := context.Background()
	e := addPerson(t, f.s, cam.ID, "Vehicle", at, red)
	a, _, err := f.s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: cam.ID, CameraName: cam.Name, Kind: "Vehicle", Title: "Vehicle detected",
		Severity: "Warning", Detail: cam.Name + " detected a car.", EventID: e.ID, At: at, Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.s.Store.SetEventAlarm(ctx, e.ID, a.ID)
	e.AlarmID = a.ID
	return e, a
}

func TestLPRUnavailableDegrades(t *testing.T) {
	f := newScopeFixture(t)
	code, m := v2Call(t, f.h, "GET", "/api/v2/lpr", f.op, "")
	if code != 200 || m["enabled"] != false || m["available"] != false || m["reason"] != "no plate models installed" || m["plates"] != float64(0) {
		t.Fatalf("status: %d %v", code, m)
	}
	f.s.SetPlateReader(nil, "no plate detection model installed (/data/models/plate_det.onnx)")
	code, m = v2Call(t, f.h, "PUT", "/api/v2/lpr", f.admin, `{"enabled":true}`)
	if code != 409 || errCode(m) != "conflict" || !strings.Contains(fmt.Sprint(m["error"]), "plate_det.onnx") {
		t.Fatalf("enable without models: %d %v", code, m)
	}
	if v := f.s.Store.Setting(context.Background(), SettingLPREnabled, ""); v != "" {
		t.Fatalf("setting changed to %q", v)
	}
	if code, m = v2Call(t, f.h, "PUT", "/api/v2/lpr", f.admin, `{"enabled":false}`); code != 200 || m["enabled"] != false {
		t.Fatalf("disable: %d %v", code, m)
	}
	for _, bad := range []string{`{}`, `{"enabled":"yes"}`, `{"junk":1}`} {
		if code, _ := v2Call(t, f.h, "PUT", "/api/v2/lpr", f.admin, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, _ := v2Call(t, f.h, "PUT", "/api/v2/lpr", f.op, `{"enabled":false}`); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	// Vehicle thumbnails arrive as before; nothing is read.
	e, _ := addVehicle(t, f, f.front, f.t0)
	_ = f.s.Sink().Event(context.Background(), link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: f.front.ID,
		Thumbnail: &link.Thumbnail{EventID: e.ID, File: filepath.Base(e.Thumbnail)}})
	if got, _ := f.s.Store.Event(context.Background(), e.ID); got.Plate != "" {
		t.Fatal("read without models")
	}
}

// waitPlateDone installs a done hook and returns a wait for one event id.
func waitPlateDone(t *testing.T, s *Server) func(id string) {
	ch := make(chan string, 64)
	s.lprState().done = func(id string) { ch <- id }
	return func(id string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-ch:
				if got == id {
					return
				}
			case <-deadline:
				t.Fatalf("event %s was never handled by the plate worker", id)
			}
		}
	}
}

func TestPlateReadOnThumbnail(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	rd := &fakePlateReader{text: "ABC123"}
	wait := waitPlateDone(t, f.s)
	f.s.SetPlateReader(rd, "")
	thumb := func(e *store.Event) {
		t.Helper()
		if err := f.s.Sink().Event(ctx, link.Event{ID: link.NewID(), Type: link.EventThumbnail, CameraID: e.CameraID,
			Thumbnail: &link.Thumbnail{EventID: e.ID, File: filepath.Base(e.Thumbnail)}}); err != nil {
			t.Fatal(err)
		}
		wait(e.ID)
	}

	// Off by default: nothing is read.
	off, _ := addVehicle(t, f, f.front, f.t0)
	thumb(off)
	if rd.cropCount() != 0 {
		t.Fatal("read while switched off")
	}
	code, m := v2Call(t, f.h, "PUT", "/api/v2/lpr", f.admin, `{"enabled":true}`)
	if code != 200 || m["enabled"] != true || m["available"] != true || m["detModel"] != "sha256:det0" || m["recModel"] != "sha256:rec0" {
		t.Fatalf("enable: %d %v", code, m)
	}
	if a := lastAudit(t, f.s, "Changed plate reading"); a != "boss | Plates | Plate reading on" {
		t.Fatalf("audit %q", a)
	}

	car, _ := addVehicle(t, f, f.front, f.t0.Add(time.Minute))
	thumb(car)
	got, _ := f.s.Store.Event(ctx, car.ID)
	if got.Plate != "ABC123" {
		t.Fatalf("plate %q", got.Plate)
	}
	rd.mu.Lock()
	crop := rd.crops[0]
	rd.mu.Unlock()
	// Box [0.5,0.2,0.7,0.9] of 200×100 = (100,20)-(140,90), padded 10 %:
	// (96,13)-(144,97).
	if crop != image.Rect(96, 13, 144, 97) {
		t.Fatalf("crop %v, want the padded box", crop)
	}
	// A person event is never read.
	person := addPerson(t, f.s, f.front.ID, "Person", f.t0.Add(2*time.Minute), red)
	thumb(person)
	if rd.cropCount() != 1 {
		t.Fatal("a person event was read")
	}
	// No plate found: the event stays without one.
	rd.set("")
	quiet, _ := addVehicle(t, f, f.front, f.t0.Add(3*time.Minute))
	thumb(quiet)
	if got, _ := f.s.Store.Event(ctx, quiet.ID); got.Plate != "" {
		t.Fatalf("plate %q", got.Plate)
	}

	// The API shows it and searches by it.
	_, ev := v2Call(t, f.h, "GET", "/api/v2/events/"+car.ID, f.admin, "")
	if ev["plate"] != "ABC123" {
		t.Fatalf("event view %v", ev)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/events/search?plate=abc-1", f.admin, "")
	if it := items(t, m); len(it) != 1 || it[0]["id"] != car.ID {
		t.Fatalf("search by plate: %v", m)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/lpr", f.op, "")
	if m["readLast24h"] != float64(0) { // the fixture's events are from 2026-10-09
		t.Fatalf("readLast24h %v", m["readLast24h"])
	}
}

func TestPlateQueueDropsOldest(t *testing.T) {
	f := newScopeFixture(t)
	st := f.s.lprState()
	st.queue = make(chan string, 2) // no worker: just the hook
	f.s.plateOnThumbnail("a")
	f.s.plateOnThumbnail("b")
	f.s.plateOnThumbnail("c")
	if got := <-st.queue + <-st.queue; got != "bc" {
		t.Fatalf("queue kept %q, want the newest two", got)
	}
}

func TestMatchPlate(t *testing.T) {
	list := []store.Plate{{Plate: "ABC123"}, {Plate: "AB12"}, {Plate: "XYZ789"}}
	for read, want := range map[string]string{
		"ABC123":  "ABC123", // exact
		"ABC124":  "ABC123", // one substitution, 6 characters
		"ABC12":   "",       // 5 characters: exact only
		"AB12":    "AB12",
		"AB13":    "",       // short: exact only
		"XYZ7890": "XYZ789", // one insertion
		"XY789":   "",       // too short for fuzzy
		"QQQ999":  "",
	} {
		got := ""
		if p := matchPlate(list, read); p != nil {
			got = p.Plate
		}
		if got != want {
			t.Errorf("matchPlate(%q) = %q, want %q", read, got, want)
		}
	}
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"ABC", "", 3}, {"KITTEN", "SITTING", 3}, {"ABC123", "ABC123", 0}, {"ABC123", "AB123", 1}} {
		if d := levenshtein(c.a, c.b); d != c.d {
			t.Errorf("levenshtein(%q,%q) = %d", c.a, c.b, d)
		}
	}
}

func TestPlateWatchlistAlarmEffects(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	rd := &fakePlateReader{}
	f.s.SetPlateReader(rd, "")
	_ = f.s.Store.SetSetting(ctx, SettingLPREnabled, "1")
	add := func(plate, name, alert string) {
		t.Helper()
		body := fmt.Sprintf(`{"plate":%q,"name":%q,"alert":%q}`, plate, name, alert)
		if code, m := v2Call(t, f.h, "POST", "/api/v2/plates", f.admin, body); code != 201 {
			t.Fatalf("add %s: %d %v", plate, code, m)
		}
	}
	add("abc-123", "Delivery van", "default")
	add("KNOWN1", "", "never")
	add("BAD777", "Stolen car", "always")

	read := func(text string, at time.Time) (*store.Event, *store.Alarm) {
		t.Helper()
		e, _ := addVehicle(t, f, f.front, at)
		rd.set(text)
		if got, err := f.s.readEventPlate(ctx, e.ID); err != nil || got != text {
			t.Fatalf("read %q: %q %v", text, got, err)
		}
		a, _ := f.s.Store.Alarm(ctx, e.AlarmID)
		return e, a
	}

	// default: the alarm keeps its severity; the name goes into its detail.
	_, a := read("ABC123", f.t0)
	if a.Detail != "Delivery van at Front door" || a.Severity != "Warning" || a.State != "New" {
		t.Fatalf("default: %+v", a)
	}
	// One character off on a 6-character plate still matches.
	_, a = read("ABC128", f.t0.Add(5*time.Minute))
	if a.Detail != "Delivery van at Front door" {
		t.Fatalf("fuzzy default: %+v", a)
	}

	// never: the alarm is closed as a known vehicle, audited.
	_, a = read("KNOWN1", f.t0.Add(10*time.Minute))
	if a.State != "Resolved" || !strings.Contains(strings.Join(a.ResponseLog, "|"), "Known vehicle: KNOWN1") {
		t.Fatalf("never: %+v", a)
	}
	if au := lastAudit(t, f.s, "Set alarm resolved"); !strings.Contains(au, "System | Plates | Vehicle detected (Front door): Known vehicle: KNOWN1") {
		t.Fatalf("audit %q", au)
	}

	// always: a Critical Watchlist alarm linked to the event; the vehicle
	// alarm is left alone.
	e, a := read("BAD777", f.t0.Add(20*time.Minute))
	if a.State != "New" || a.Detail != "Front door detected a car." {
		t.Fatalf("always touched the vehicle alarm: %+v", a)
	}
	alarms, _ := f.s.Store.Alarms(ctx, true, 50)
	var wl *store.Alarm
	for i := range alarms {
		if alarms[i].Kind == "Watchlist" {
			wl = &alarms[i]
		}
	}
	if wl == nil || wl.Title != "Watchlist: BAD777" || wl.Severity != "Critical" || wl.EventID != e.ID || wl.CameraID != f.front.ID ||
		!strings.Contains(wl.Detail, "Stolen car (BAD777) at Front door") {
		t.Fatalf("watchlist alarm %+v", wl)
	}

	// No match: nothing changes.
	_, a = read("ZZZ999", f.t0.Add(30*time.Minute))
	if !strings.HasPrefix(a.Detail, "Front door detected a car.") || a.State != "New" {
		t.Fatalf("no match: %+v", a)
	}

	// Switched off: no read at all.
	_ = f.s.Store.SetSetting(ctx, SettingLPREnabled, "0")
	e2, _ := addVehicle(t, f, f.front, f.t0.Add(40*time.Minute))
	if _, err := f.s.readEventPlate(ctx, e2.ID); err != errPlateSkip {
		t.Fatalf("off: %v", err)
	}
}

func TestPlatesAPI(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	code, p := v2Call(t, f.h, "POST", "/api/v2/plates", f.admin, `{"plate":" ab 12-cd ","name":"Van","notes":"blue","alert":"never"}`)
	if code != 201 || p["plate"] != "AB12CD" || p["name"] != "Van" || p["notes"] != "blue" || p["alert"] != "never" || p["id"] == "" || p["createdAt"] == nil {
		t.Fatalf("add: %d %v", code, p)
	}
	id := p["id"].(string)
	if a := lastAudit(t, f.s, "Added plate"); a != "boss | Plates | AB12CD (Van), alert never" {
		t.Fatalf("audit %q", a)
	}
	if code, m := v2Call(t, f.h, "POST", "/api/v2/plates", f.admin, `{"plate":"AB12CD"}`); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("duplicate: %d %v", code, m)
	}
	_, def := v2Call(t, f.h, "POST", "/api/v2/plates", f.admin, `{"plate":"ZZ9"}`)
	if def["alert"] != "default" {
		t.Fatalf("default alert: %v", def)
	}
	for _, bad := range []string{`{}`, `{"plate":"A"}`, `{"plate":"--"}`, `{"plate":"ABCDEFGHIJK"}`, `{"plate":"1234567890123"}`,
		`{"plate":"AB1","alert":"sometimes"}`, `{"plate":"AB1","name":"` + strings.Repeat("n", 81) + `"}`,
		`{"plate":"AB1","notes":"` + strings.Repeat("n", 501) + `"}`, `{"plate":"AB1","junk":1}`} {
		if code, m := v2Call(t, f.h, "POST", "/api/v2/plates", f.admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}

	// Sightings on both cameras; the Front-scoped operator sees only Front.
	front, _ := addVehicle(t, f, f.front, f.t0)
	back, _ := addVehicle(t, f, f.back, f.t0.Add(time.Hour))
	_ = f.s.Store.SetEventPlate(ctx, front.ID, "AB12CD")
	_ = f.s.Store.SetEventPlate(ctx, back.ID, "AB12CD")
	_, m := v2Call(t, f.h, "GET", "/api/v2/plates", f.admin, "")
	list := items(t, m)
	if len(list) != 2 || list[0]["plate"] != "AB12CD" || list[0]["lastSeenCameraId"] != f.back.ID ||
		list[0]["lastSeenAt"] != float64(f.t0.Add(time.Hour).UnixMilli()) || list[0]["sightings"] != nil || list[1]["lastSeenAt"] != nil {
		t.Fatalf("list: %v", list)
	}
	_, m = v2Call(t, f.h, "GET", "/api/v2/plates", f.op, "")
	if list := items(t, m); list[0]["lastSeenCameraId"] != f.front.ID {
		t.Fatalf("scoped list: %v", list)
	}
	_, one := v2Call(t, f.h, "GET", "/api/v2/plates/"+id, f.admin, "")
	if s, _ := one["sightings"].([]any); len(s) != 2 || s[0].(map[string]any)["id"] != back.ID || s[0].(map[string]any)["plate"] != "AB12CD" {
		t.Fatalf("one: %v", one)
	}
	_, one = v2Call(t, f.h, "GET", "/api/v2/plates/"+id, f.op, "")
	if s, _ := one["sightings"].([]any); len(s) != 1 || s[0].(map[string]any)["id"] != front.ID {
		t.Fatalf("scoped one: %v", one)
	}
	_, one = v2Call(t, f.h, "GET", "/api/v2/plates/"+def["id"].(string), f.admin, "")
	if s, ok := one["sightings"].([]any); !ok || len(s) != 0 {
		t.Fatalf("never seen: %v", one)
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/plates/nope", f.admin, ""); code != 404 {
		t.Fatalf("missing: %d", code)
	}

	// Patch: partial; renaming onto another plate is a conflict.
	code, p = v2Call(t, f.h, "PATCH", "/api/v2/plates/"+id, f.admin, `{"alert":"always"}`)
	if code != 200 || p["alert"] != "always" || p["name"] != "Van" || p["plate"] != "AB12CD" {
		t.Fatalf("patch: %d %v", code, p)
	}
	if a := lastAudit(t, f.s, "Changed plate"); a != "boss | Plates | AB12CD (Van), alert always" {
		t.Fatalf("audit %q", a)
	}
	if code, _ := v2Call(t, f.h, "PATCH", "/api/v2/plates/"+id, f.admin, `{"plate":"zz9"}`); code != 409 {
		t.Fatalf("rename clash: %d", code)
	}
	if code, _ := v2Call(t, f.h, "PATCH", "/api/v2/plates/"+id, f.admin, `{"alert":"x"}`); code != 400 {
		t.Fatalf("bad patch: %d", code)
	}
	if code, _ := v2Call(t, f.h, "PATCH", "/api/v2/plates/nope", f.admin, `{"name":"x"}`); code != 404 {
		t.Fatalf("patch missing: %d", code)
	}

	// Roles: viewers read, only ChangeSettings writes.
	viewer := v2Token(t, f.s, "vw", "Viewer")
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/plates", viewer, ""); code != 200 {
		t.Fatalf("viewer list: %d", code)
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/lpr", viewer, ""); code != 200 {
		t.Fatalf("viewer status: %d", code)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v2/plates", `{"plate":"QQ11"}`}, {"PATCH", "/api/v2/plates/" + id, `{"name":"x"}`}, {"DELETE", "/api/v2/plates/" + id, ""},
	} {
		if code, _ := v2Call(t, f.h, c.method, c.path, f.op, c.body); code != 403 {
			t.Errorf("operator %s %s: %d", c.method, c.path, code)
		}
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/plates", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}

	_, st := v2Call(t, f.h, "GET", "/api/v2/lpr", f.admin, "")
	if st["plates"] != float64(2) {
		t.Fatalf("status plates %v", st)
	}
	if code, _ := v2Call(t, f.h, "DELETE", "/api/v2/plates/"+id, f.admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if a := lastAudit(t, f.s, "Removed plate"); a != "boss | Plates | AB12CD (Van)" {
		t.Fatalf("audit %q", a)
	}
	if code, _ := v2Call(t, f.h, "DELETE", "/api/v2/plates/"+id, f.admin, ""); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}
}

func TestEventSearchPlatePersonTrack(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	a, _ := addVehicle(t, f, f.front, f.t0)
	b, _ := addVehicle(t, f, f.back, f.t0.Add(time.Minute))
	_ = f.s.Store.SetEventPlate(ctx, a.ID, "KN232")
	_ = f.s.Store.SetEventPlate(ctx, b.ID, "KN2321")
	_, _ = f.s.Store.DB.ExecContext(ctx, `UPDATE events SET person_id='p1', track_id=? WHERE id=?`, f.front.ID+":4", a.ID)
	ids := func(tok, q string) []string {
		t.Helper()
		code, m := v2Call(t, f.h, "GET", "/api/v2/events/search?"+q, tok, "")
		if code != 200 {
			t.Fatalf("%s: %d %v", q, code, m)
		}
		var out []string
		for _, it := range items(t, m) {
			out = append(out, it["id"].(string))
		}
		return out
	}
	if got := ids(f.admin, "plate=kn-232"); len(got) != 2 || got[0] != b.ID {
		t.Fatalf("prefix: %v", got)
	}
	if got := ids(f.op, "plate=KN232"); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("scoped: %v", got)
	}
	if got := ids(f.admin, "plate=KN2321"); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("longer prefix: %v", got)
	}
	if got := ids(f.admin, "person=p1"); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("person: %v", got)
	}
	if got := ids(f.admin, "track="+f.front.ID+":4&kinds=Vehicle"); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("track: %v", got)
	}
	if got := ids(f.admin, "track="+f.front.ID); len(got) != 0 {
		t.Fatalf("track is exact: %v", got)
	}
	if code, m := v2Call(t, f.h, "GET", "/api/v2/events/search?plate=--", f.admin, ""); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("bad plate: %d %v", code, m)
	}
}
