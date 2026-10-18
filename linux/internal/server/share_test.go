package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

// shareFixture locks one evidence case and returns it with tokens.
func shareFixture(t *testing.T) (s *Server, h http.Handler, evID, op, viewer, boss string) {
	t.Helper()
	s, c, t0 := playbackFixture(t)
	h = s.Handler()
	op = v2Token(t, s, "op", "Operator")
	viewer = v2Token(t, s, "viewer", "Viewer")
	boss = v2Token(t, s, "boss", "Admin")
	code, ev := v2Call(t, h, "POST", "/api/v2/evidence", op, evBody(c.ID, t0.Add(10*time.Second), t0.Add(40*time.Second), ""))
	if code != 201 {
		t.Fatalf("evidence: %d %v", code, ev)
	}
	return s, h, ev["id"].(string), op, viewer, boss
}

func mkShare(t *testing.T, h http.Handler, tok, evID, body string) (id, path string) {
	t.Helper()
	code, m := v2Call(t, h, "POST", "/api/v2/evidence/"+evID+"/share", tok, body)
	if code != 201 {
		t.Fatalf("share: %d %v", code, m)
	}
	u := m["url"].(string)
	return m["link"].(map[string]any)["id"].(string), u[strings.Index(u, "/share/"):]
}

func shareGet(h http.Handler, path, ip string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = ip + ":4000"
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var mediaRe = regexp.MustCompile(`<video[^>]*src="([^"]+)"`)

func mediaURL(t *testing.T, page string) string {
	t.Helper()
	m := mediaRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no <video src> in %s", page)
	}
	return strings.ReplaceAll(m[1], "&amp;", "&")
}

func auditText(t *testing.T, s *Server) string {
	t.Helper()
	list, err := s.Store.AuditLog(t.Context(), 500)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range list {
		b.WriteString(e.User + " | " + e.Action + " | " + e.Detail + "\n")
	}
	return b.String()
}

func TestShareCRUDPermissionsAndScope(t *testing.T) {
	s, h, evID, op, viewer, boss := shareFixture(t)
	ctx := t.Context()
	body := `{"expiry":"24h","maxUses":3,"note":"for the police"}`

	if r := do(t, h, "POST", "/api/v2/evidence/"+evID+"/share", viewer, body); r.Code != 403 {
		t.Errorf("viewer create: %d", r.Code)
	}
	for _, m := range [][2]string{{"GET", "/api/v2/share-links"}, {"DELETE", "/api/v2/share-links/x"}} {
		if r := do(t, h, m[0], m[1], viewer, ""); r.Code != 403 {
			t.Errorf("viewer %v: %d", m, r.Code)
		}
		if r := do(t, h, m[0], m[1], "", ""); r.Code != 401 {
			t.Errorf("anon %v: %d", m, r.Code)
		}
	}
	far := strconv.FormatInt(time.Now().Add(60*24*time.Hour).UnixMilli(), 10)
	for _, bad := range []string{`{"expiry":"2y"}`, `{}`, `{"expiry":"custom","expiresAt":1}`,
		`{"expiry":"custom","expiresAt":` + far + `}`, `{"expiry":"1h","maxUses":-1}`, `{"expiry":"1h","maxUses":5000}`} {
		if r := do(t, h, "POST", "/api/v2/evidence/"+evID+"/share", op, bad); r.Code != 400 {
			t.Errorf("bad %s: %d", bad, r.Code)
		}
	}
	soon := strconv.FormatInt(time.Now().Add(2*time.Hour).UnixMilli(), 10)
	if r := do(t, h, "POST", "/api/v2/evidence/"+evID+"/share", op, `{"expiry":"custom","expiresAt":`+soon+`}`); r.Code != 201 {
		t.Errorf("custom expiry: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "POST", "/api/v2/evidence/nope/share", op, body); r.Code != 404 {
		t.Errorf("unknown evidence: %d", r.Code)
	}

	code, m := v2Call(t, h, "POST", "/api/v2/evidence/"+evID+"/share", op, body)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	link := m["link"].(map[string]any)
	if link["maxUses"] != float64(3) || link["note"] != "for the police" || link["status"] != "active" || !isMs(link["expiresAt"]) {
		t.Errorf("link: %v", link)
	}
	tok := m["token"].(string)
	if !strings.HasSuffix(m["url"].(string), "/share/"+tok) {
		t.Errorf("url: %v", m["url"])
	}

	// The list never carries the token and counts uses.
	r := do(t, h, "GET", "/api/v2/share-links", boss, "")
	if r.Code != 200 || strings.Contains(r.Body.String(), tok) || !strings.Contains(r.Body.String(), `"uses":0`) {
		t.Fatalf("list: %d %s", r.Code, r.Body)
	}

	// Scoped operators neither create, see nor revoke links to cameras outside their scope.
	scoped := v2Token(t, s, "scoped", "Operator")
	other, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Other", RTSPURL: "rtsp://10.0.0.7/s"})
	g, _ := s.Store.SaveCameraGroup(ctx, "", "Other only", []string{other.ID})
	u, _ := s.Store.UserByName(ctx, "scoped")
	_ = s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}})
	if r := do(t, h, "POST", "/api/v2/evidence/"+evID+"/share", scoped, body); r.Code != 404 {
		t.Errorf("scoped create: %d", r.Code)
	}
	if r := do(t, h, "GET", "/api/v2/share-links", scoped, ""); r.Code != 200 || strings.Contains(r.Body.String(), link["id"].(string)) {
		t.Errorf("scoped list: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "DELETE", "/api/v2/share-links/"+link["id"].(string), scoped, ""); r.Code != 404 {
		t.Errorf("scoped revoke: %d", r.Code)
	}

	// Revoke, then the public link is gone and everything is audited.
	if r := do(t, h, "DELETE", "/api/v2/share-links/"+link["id"].(string), op, ""); r.Code != 204 {
		t.Fatalf("revoke: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/share-links/nope", op, ""); r.Code != 404 {
		t.Errorf("revoke missing: %d", r.Code)
	}
	if w := shareGet(h, "/share/"+tok, "10.1.1.1"); w.Code != 410 {
		t.Errorf("revoked open: %d", w.Code)
	}
	audit := auditText(t, s)
	for _, want := range []string{"Created share link", "Revoked share link"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit missing %q", want)
		}
	}
}

func TestSharePublicPageRangeAndAudit(t *testing.T) {
	s, h, evID, op, _, _ := shareFixture(t)
	_, path := mkShare(t, h, op, evID, `{"expiry":"1h","maxUses":1,"note":"<b>hi</b>"}`)

	w := shareGet(h, path, "203.0.113.9")
	page := w.Body.String()
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(page, "<video") ||
		!strings.Contains(page, "Download") || strings.Contains(page, "<script") || strings.Contains(page, "<b>hi</b>") ||
		!strings.Contains(page, "&lt;b&gt;hi&lt;/b&gt;") || !strings.Contains(page, "<style>") {
		t.Fatalf("page: %d %s", w.Code, page)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("page must not be cached")
	}
	media := mediaURL(t, page)

	// max_uses=1 is spent by the page view, but the media still streams for that view.
	full := shareGet(h, media, "203.0.113.9")
	if full.Code != 200 || full.Header().Get("Content-Type") != "video/mp4" || !strings.HasPrefix(full.Body.String(), "clip:") ||
		strings.Contains(full.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("media: %d %v %q", full.Code, full.Header(), full.Body)
	}
	size := full.Body.Len()
	part := shareGet(h, media, "203.0.113.9", "Range", "bytes=1-4")
	if part.Code != 206 || part.Body.String() != full.Body.String()[1:5] ||
		part.Header().Get("Content-Range") != "bytes 1-4/"+strconv.Itoa(size) || part.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("range: %d %v %q", part.Code, part.Header(), part.Body)
	}
	if dl := shareGet(h, media+"&dl=1", "203.0.113.9"); dl.Code != 200 || !strings.Contains(dl.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("download: %d %v", dl.Code, dl.Header())
	}

	// A second page view is over the limit; media without or with a forged proof is 410.
	if w := shareGet(h, path, "203.0.113.9"); w.Code != 410 {
		t.Errorf("second view: %d", w.Code)
	}
	if w := shareGet(h, path+"/media", "203.0.113.20"); w.Code != 410 {
		t.Errorf("media without proof: %d", w.Code)
	}
	if w := shareGet(h, path+"/media?v=9999999999.00", "203.0.113.20"); w.Code != 410 {
		t.Errorf("media forged proof: %d", w.Code)
	}

	// Audited with the remote IP; Range chunks beyond the first aren't each logged.
	audit := auditText(t, s)
	if !strings.Contains(audit, "Share link opened") || !strings.Contains(audit, "203.0.113.9") ||
		!strings.Contains(audit, "Share link played") || !strings.Contains(audit, "Share link downloaded") || !strings.Contains(audit, "Share link refused") {
		t.Errorf("audit: %s", audit)
	}
	if strings.Count(audit, "Share link played") != 1 {
		t.Errorf("played logged %d times", strings.Count(audit, "Share link played"))
	}
}

// All the ways a link can be dead answer the same 410 body.
func TestShareGenericGone(t *testing.T) {
	s, h, evID, op, _, _ := shareFixture(t)
	ctx := t.Context()
	id, expiredPath := mkShare(t, h, op, evID, `{"expiry":"1h"}`)
	revokedID, revokedPath := mkShare(t, h, op, evID, `{"expiry":"1h"}`)
	_, usedPath := mkShare(t, h, op, evID, `{"expiry":"1h","maxUses":1}`)
	_, okPath := mkShare(t, h, op, evID, `{"expiry":"1h"}`)

	// Expire one by rewriting its expiry in the store.
	if _, err := s.Store.DB.ExecContext(ctx, `UPDATE share_links SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).Unix(), id); err != nil {
		t.Fatal(err)
	}
	if r := do(t, h, "DELETE", "/api/v2/share-links/"+revokedID, op, ""); r.Code != 204 {
		t.Fatal(r.Code)
	}
	if w := shareGet(h, usedPath, "10.0.0.5"); w.Code != 200 {
		t.Fatalf("first use: %d", w.Code)
	}

	var bodies []string
	for _, p := range []string{expiredPath, revokedPath, usedPath, "/share/" + strings.Repeat("A", 43), "/share/short"} {
		w := shareGet(h, p, "10.0.0.6")
		if w.Code != 410 {
			t.Errorf("%s: %d", p, w.Code)
		}
		bodies = append(bodies, w.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("410 bodies differ:\n%s\n%s", bodies[0], b)
		}
	}
	if !strings.Contains(bodies[0], "no longer available") || strings.Contains(bodies[0], "<video") {
		t.Errorf("gone page: %s", bodies[0])
	}
	// A fresh IP still opens the good link: the limiter is per address.
	if w := shareGet(h, okPath, "10.0.0.7"); w.Code != 200 {
		t.Errorf("good link: %d", w.Code)
	}
	// An expired link also stops its media.
	if w := shareGet(h, expiredPath+"/media", "10.0.0.8"); w.Code != 410 {
		t.Errorf("expired media: %d", w.Code)
	}
}

func TestShareGuessRateLimit(t *testing.T) {
	s, h, evID, op, _, _ := shareFixture(t)
	_, okPath := mkShare(t, h, op, evID, `{"expiry":"1h"}`)
	now := time.Now()
	g := s.shareState()
	g.now = func() time.Time { return now }

	for i := 0; i < shareGuessLimit; i++ {
		if w := shareGet(h, "/share/guess"+strconv.Itoa(i), "198.51.100.1"); w.Code != 410 {
			t.Fatalf("guess %d: %d", i, w.Code)
		}
	}
	// Over the limit even the right token is refused for that IP...
	w := shareGet(h, okPath, "198.51.100.1")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("limited: %d %v", w.Code, w.Header())
	}
	// ...but not for another one...
	if w := shareGet(h, okPath, "198.51.100.2"); w.Code != 200 {
		t.Errorf("other ip: %d", w.Code)
	}
	// ...and the window slides.
	now = now.Add(shareGuessWindow + time.Second)
	if w := shareGet(h, okPath, "198.51.100.1"); w.Code != 200 {
		t.Errorf("after window: %d", w.Code)
	}
}

// The tunnel origin must not serve share links.
func TestShareNotOnTunnelOrigin(t *testing.T) {
	s, h, evID, op, _, _ := shareFixture(t)
	_, path := mkShare(t, h, op, evID, `{"expiry":"1h"}`)
	w := shareGet(s.CompanionHandler(), path, "10.0.0.9")
	if w.Code == 200 || strings.Contains(w.Body.String(), "<video") {
		t.Errorf("tunnel origin served a share: %d", w.Code)
	}
}
