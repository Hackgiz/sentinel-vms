package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testWebP is a header-only lossless WebP of the given size.
func testWebP(w, h int) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WEBPVP8L\x0a\x00\x00\x00\x2f")
	bits := make([]byte, 4)
	binary.LittleEndian.PutUint32(bits, uint32(w-1)|uint32(h-1)<<14)
	return append(append(b, bits...), 0, 0, 0, 0, 0)
}

func uploadPlan(t *testing.T, h http.Handler, tok, name string, img []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", name)
	fw, _ := mw.CreateFormFile("image", "plan.bin")
	_, _ = fw.Write(img)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/v2/floorplans", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFloorplanUploadValidation(t *testing.T) {
	f := newScopeFixture(t)
	h := f.h
	var jpg bytes.Buffer
	_ = jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil)

	if rec := uploadPlan(t, h, f.op, "Nope", testPNG(t, 10, 10)); rec.Code != 403 {
		t.Fatalf("operator upload: %d", rec.Code)
	}
	for _, c := range []struct {
		name, mime string
		img        []byte
		w, h       int
	}{{"png", "image/png", testPNG(t, 64, 48), 64, 48}, {"jpeg", "image/jpeg", jpg.Bytes(), 40, 30}, {"webp", "image/webp", testWebP(320, 200), 320, 200}} {
		rec := uploadPlan(t, h, f.admin, c.name, c.img)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if rec.Code != 201 || m["mime"] != c.mime || int(m["width"].(float64)) != c.w || int(m["height"].(float64)) != c.h {
			t.Fatalf("%s upload: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	if rec := uploadPlan(t, h, f.admin, "Text", []byte("hello, not an image")); rec.Code != 415 {
		t.Fatalf("text upload: %d %s", rec.Code, rec.Body)
	}
	if rec := uploadPlan(t, h, f.admin, "GIF", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")); rec.Code != 415 {
		t.Fatalf("gif upload: %d", rec.Code)
	}
	if rec := uploadPlan(t, h, f.admin, "  ", testPNG(t, 4, 4)); rec.Code != 400 {
		t.Fatalf("blank name: %d", rec.Code)
	}
	big := append(testPNG(t, 4, 4), make([]byte, maxFloorplanImage)...)
	if rec := uploadPlan(t, h, f.admin, "Big", big); rec.Code != 413 {
		t.Fatalf("oversize upload: %d", rec.Code)
	}
	// Cookie sessions must say it's a deliberate upload (CSRF guard).
	adminCookie := f.cookieFor(t, f.admin)
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("name", "Cookie")
	fw, _ := mw.CreateFormFile("image", "p.png")
	_, _ = fw.Write(testPNG(t, 8, 8))
	_ = mw.Close()
	for header, want := range map[string]int{"": 415, floorplanUploadHeader: 201} {
		req := httptest.NewRequest("POST", "/api/v2/floorplans", bytes.NewReader(form.Bytes()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(adminCookie)
		if header != "" {
			req.Header.Set(header, "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("cookie upload with header %q: %d %s", header, rec.Code, rec.Body)
		}
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/floorplans", f.admin, `{"name":"x"}`); code != 400 {
		t.Fatalf("json upload: %d %v", code, m)
	}
	if got := auditActions(t, f.s, "Cameras"); !got["Added floorplan"] {
		t.Fatalf("audit %v", got)
	}
}

func TestFloorplanPinsScopeAndPermissions(t *testing.T) {
	f := newScopeFixture(t)
	h := f.h
	rec := uploadPlan(t, h, f.admin, "Ground", testPNG(t, 100, 50))
	var plan map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	id := plan["id"].(string)
	pinsURL := "/api/v2/floorplans/" + id + "/pins"
	pins := fmt.Sprintf(`[{"cameraId":%q,"x":0.2,"y":0.3,"heading":90},{"cameraId":%q,"x":0.8,"y":0.9,"heading":-1}]`, f.front.ID, f.back.ID)

	if code, _ := v2Call(t, h, "PUT", pinsURL, f.op, pins); code != 403 {
		t.Fatalf("operator set pins: %d", code)
	}
	for _, bad := range []string{
		fmt.Sprintf(`[{"cameraId":%q,"x":1.5,"y":0,"heading":-1}]`, f.front.ID),
		fmt.Sprintf(`[{"cameraId":%q,"x":0,"y":0,"heading":400}]`, f.front.ID),
		`[{"cameraId":"nope","x":0,"y":0,"heading":-1}]`,
		fmt.Sprintf(`[{"cameraId":%[1]q,"x":0,"y":0,"heading":-1},{"cameraId":%[1]q,"x":1,"y":1,"heading":-1}]`, f.front.ID),
		`{"not":"a list"}`,
	} {
		if code, m := v2Call(t, h, "PUT", pinsURL, f.admin, bad); code != 400 {
			t.Fatalf("bad pins %s: %d %v", bad, code, m)
		}
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/floorplans/nope/pins", f.admin, pins); code != 404 {
		t.Fatalf("pins on missing plan: %d", code)
	}
	if code, m := v2Call(t, h, "PUT", pinsURL, f.admin, pins); code != 200 || len(m["pins"].([]any)) != 2 {
		t.Fatalf("set pins: %d %v", code, m)
	}

	list := func(tok string) []any {
		code, m := v2Call(t, h, "GET", "/api/v2/floorplans", tok, "")
		if code != 200 {
			t.Fatalf("list: %d %v", code, m)
		}
		its := m["items"].([]any)
		if len(its) != 1 {
			t.Fatalf("items %v", its)
		}
		return its[0].(map[string]any)["pins"].([]any)
	}
	if p := list(f.admin); len(p) != 2 {
		t.Fatalf("admin sees %v", p)
	}
	if p := list(f.allOp); len(p) != 2 {
		t.Fatalf("unscoped operator sees %v", p)
	}
	if p := list(f.op); len(p) != 1 || p[0].(map[string]any)["cameraId"] != f.front.ID {
		t.Fatalf("scoped operator sees %v", p)
	}
	// A scoped operator still gets the plan when none of its cameras are visible.
	if code, _ := v2Call(t, h, "PUT", pinsURL, f.admin, fmt.Sprintf(`[{"cameraId":%q,"x":0.5,"y":0.5,"heading":-1}]`, f.back.ID)); code != 200 {
		t.Fatal("repin")
	}
	if p := list(f.op); len(p) != 0 {
		t.Fatalf("scoped operator sees hidden pins %v", p)
	}

	// Image: bytes, ETag, 304, visible to the scoped operator.
	req := httptest.NewRequest("GET", "/api/v2/floorplans/"+id+"/image", nil)
	req.Header.Set("Authorization", "Bearer "+f.op)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	etag := w.Header().Get("ETag")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || etag == "" || !bytes.Equal(w.Body.Bytes(), testPNG(t, 100, 50)) {
		t.Fatalf("image: %d %v", w.Code, w.Header())
	}
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 304 {
		t.Fatalf("etag: %d", w.Code)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/floorplans/nope/image", f.op, ""); code != 404 {
		t.Fatalf("missing image: %d", code)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/floorplans", "", ""); code != 401 {
		t.Fatalf("anonymous list: %d", code)
	}

	// Rename and delete.
	if code, _ := v2Call(t, h, "PUT", "/api/v2/floorplans/"+id, f.op, `{"name":"x"}`); code != 403 {
		t.Fatalf("operator rename: %d", code)
	}
	if code, m := v2Call(t, h, "PUT", "/api/v2/floorplans/"+id, f.admin, `{"name":"Level 1","sortOrder":4}`); code != 200 || m["name"] != "Level 1" || m["sortOrder"] != 4.0 {
		t.Fatalf("rename: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/floorplans/"+id, f.admin, `{"name":" "}`); code != 400 {
		t.Fatalf("blank rename: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/floorplans/"+id, f.op, ""); code != 403 {
		t.Fatalf("operator delete: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/floorplans/"+id, f.admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/floorplans/"+id, f.admin, ""); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}
	got := auditActions(t, f.s, "Cameras")
	for _, a := range []string{"Added floorplan", "Edited floorplan", "Edited floorplan pins", "Deleted floorplan"} {
		if !got[a] {
			t.Errorf("no audit %q in %v", a, got)
		}
	}
}
