package server

// The iPhone companion API. Sentinel Mobile (the App Store app) talks to a
// Linux server exactly as it talks to the Mac, so every path, field name and
// status code here mirrors the Mac's SentinelHTTPServer + SentinelHTTPBridge.
// Additive differences only: the Linux server stores token hashes, serves
// recordings with Range support, and never exposes the dashboard to the tunnel.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/store"
)

const (
	pairingCodeLifetime = 5 * time.Minute
	pairingMaxFailures  = 5
	pairingAlphabet     = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no O/0/I/1, same as the Mac
)

type pairingCode struct {
	Code      string
	ExpiresAt time.Time
	URL       string // LAN address baked into the QR
	CreatedBy string
	failures  int
}

type pairingState struct {
	mu     sync.Mutex
	active *pairingCode
}

func newPairingCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = pairingAlphabet[int(b[i])%len(pairingAlphabet)] // 256 % 32 == 0: unbiased
	}
	return string(b), nil
}

// redeem checks a code from a phone: one-shot, expires, and is thrown away
// after five wrong guesses so it can't be brute-forced over the tunnel.
func (p *pairingState) redeem(code string) (ok bool, createdBy, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.active
	switch {
	case a == nil:
		return false, "", "no active pairing code"
	case time.Now().After(a.ExpiresAt):
		p.active = nil
		return false, "", "pairing code expired"
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if subtle.ConstantTimeCompare([]byte(code), []byte(a.Code)) == 1 {
		p.active = nil
		return true, a.CreatedBy, ""
	}
	a.failures++
	if a.failures >= pairingMaxFailures {
		p.active = nil
		return false, "", fmt.Sprintf("pairing code invalidated after %d wrong attempts", pairingMaxFailures)
	}
	return false, "", fmt.Sprintf("wrong pairing code (attempt %d of %d)", a.failures, pairingMaxFailures)
}

// registerCompanion adds the phone API to mux. It's mounted both on the main
// listener (LAN) and on the loopback-only origin the Cloudflare tunnel uses.
func (s *Server) registerCompanion(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "sentinel-vms"})
	})
	mux.HandleFunc("POST /pair", s.handlePhonePair)
	mux.Handle("GET /cameras", s.phoneAuthed(s.handlePhoneCameras))
	mux.Handle("GET /cameras/{id}", s.phoneAuthed(s.handlePhoneCameraDetail))
	mux.Handle("GET /cameras/{id}/snapshot.jpg", s.phoneAuthed(func(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
		writeError(w, http.StatusNotFound, "not found") // the app doesn't use snapshots
	}))
	mux.Handle("GET /cameras/{id}/segments", s.phoneAuthed(s.handlePhoneSegments))
	mux.Handle("GET /cameras/{id}/segments/{name}", s.phoneAuthed(func(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
		s.serveRecording(w, r, r.PathValue("id"), r.PathValue("name"))
	}))
	// Alarms: phones may only do non-destructive actions (acknowledge, lock
	// evidence) — same rule as the Mac. A missing alarm gets a descriptive 404
	// (a bare "not found" would tell the phone to update the server).
	mux.Handle("GET /alerts", s.phoneAuthed(s.handlePhoneAlerts))
	mux.Handle("POST /alerts/{id}/{action}", s.phoneAuthed(s.handlePhoneAlarmAction))
	mux.Handle("GET /events", s.phoneAuthed(s.handlePhoneEvents))
	mux.Handle("GET /events/{id}/thumbnail.jpg", s.phoneAuthed(func(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
		s.serveEventThumbnail(w, r, r.PathValue("id"))
	}))
	mux.Handle("POST /devices/register", s.phoneAuthed(s.handlePhoneRegister))
	mux.Handle("GET /hls/{id}/{file}", s.phoneAuthed(s.handlePhoneHLS))
}

// CompanionHandler serves ONLY the phone API — used as the Cloudflare tunnel's
// origin so the admin dashboard and /api/v1 are never reachable from the internet.
func (s *Server) CompanionHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerCompanion(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return s.securityHeaders(mux)
}

type phoneHandler func(http.ResponseWriter, *http.Request, *store.PairedDevice)

// phoneAuthed accepts the bearer token in the Authorization header or a
// ?token= query (AVPlayer can't set headers). A 401 makes the phone unpair, so
// it's sent only when the token is genuinely unknown — never on a server error.
func (s *Server) phoneAuthed(h phoneHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
			token = strings.TrimSpace(a[7:])
		} else {
			token = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		dev, err := s.Store.DeviceForToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		} else if err != nil {
			writeError(w, http.StatusServiceUnavailable, "server busy, try again")
			return
		}
		// Keep the phone's stored tunnel URL fresh; quick-tunnel URLs rotate.
		if remote := s.remoteURL(); remote != "" {
			w.Header().Set("X-Sentinel-Remote-URL", remote)
		}
		h(w, r, dev)
	})
}

func (s *Server) remoteURL() string {
	if s.Tunnel == nil {
		return ""
	}
	return s.Tunnel.URL()
}

func (s *Server) serverName() string {
	if s.ServerName != "" {
		return s.ServerName
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "Sentinel Linux"
}

func (s *Server) handlePhonePair(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code       string `json:"code"`
		DeviceName string `json:"deviceName"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || json.Unmarshal(body, &in) != nil || in.Code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}
	name := strings.TrimSpace(in.DeviceName)
	if name == "" {
		name = "iOS Device"
	}
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	ok, createdBy, reason := s.pairing.redeem(in.Code)
	if !ok {
		_ = s.Store.Audit(r.Context(), "iPhone pairing", "Devices", "Pairing failed", name+": "+reason+" (from "+clientIP(r)+")")
		writeError(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	dev, token, err := s.Store.CreatePairedDevice(r.Context(), name, createdBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "couldn't save the device")
		return
	}
	_ = s.Store.Audit(r.Context(), createdBy, "Devices", "Paired iPhone", name)
	resp := map[string]any{"deviceID": dev.ID, "token": token, "name": dev.Name, "serverName": s.serverName()}
	if remote := s.remoteURL(); remote != "" {
		resp["remoteURL"] = remote
	}
	writeJSON(w, http.StatusOK, resp)
}

func cameraHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

func (s *Server) phoneCamera(c *store.Camera, running bool) map[string]any {
	st := s.Media.State(c.ID)
	status := "offline"
	if running && st.Ready {
		status = "online"
	}
	res := "Unknown"
	if st.Width > 0 {
		res = fmt.Sprintf("%d×%d", st.Width, st.Height)
	}
	entry := map[string]any{
		"id":          c.ID,
		"name":        c.Name,
		"location":    c.Location,
		"ipAddress":   cameraHost(c.RTSPURL),
		"status":      status,
		"isRecording": c.Recording && status == "online",
		"resolution":  res,
		"fps":         c.FPS,
		"supportsPTZ": false,
	}
	if running {
		entry["hlsURL"] = "/hls/" + c.ID + "/index.m3u8"
	}
	return entry
}

func (s *Server) handlePhoneCameras(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "server busy, try again")
		return
	}
	running, _ := s.Media.Status()
	out := make([]map[string]any, 0, len(cams))
	for i := range cams {
		out = append(out, s.phoneCamera(&cams[i], running))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePhoneCameraDetail(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	c, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	running, _ := s.Media.Status()
	summary := s.phoneCamera(c, running)
	segs, _ := s.listSegments(id)
	out := map[string]any{
		"id":           c.ID,
		"name":         c.Name,
		"location":     c.Location,
		"status":       summary["status"],
		"isRecording":  summary["isRecording"],
		"estimatedFPS": float64(c.FPS),
		"segmentCount": len(segs),
		"eventCount":   s.Store.EventCount(r.Context(), id),
		"supportsPTZ":  false,
	}
	if summary["status"] != "online" {
		if !running {
			out["lastError"] = "Media engine is starting"
		} else {
			out["lastError"] = "No video from the camera"
		}
	}
	if running {
		out["hlsURL"] = summary["hlsURL"]
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePhoneSegments(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	segs, err := s.listSegments(id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "couldn't read recordings")
		return
	}
	from, hasFrom := queryTime(r, "from")
	to, hasTo := queryTime(r, "to")
	out := []map[string]any{}
	for _, sg := range segs {
		if hasFrom && sg.End < from {
			continue
		}
		if hasTo && sg.Start > to {
			continue
		}
		out = append(out, map[string]any{"name": sg.Name, "createdAt": sg.Start, "modifiedAt": sg.End, "sizeBytes": sg.Size})
	}
	writeJSON(w, http.StatusOK, out)
}

// queryTime reads unix seconds or ISO-8601, like the Mac.
func queryTime(r *http.Request, key string) (int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, false
	}
	var secs float64
	if _, err := fmt.Sscanf(raw, "%g", &secs); err == nil && !strings.ContainsAny(raw, "-T:") {
		return int64(secs), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix(), true
	}
	return 0, false
}

func (s *Server) handlePhoneRegister(w http.ResponseWriter, r *http.Request, dev *store.PairedDevice) {
	var in struct {
		APNSToken string `json:"apnsToken"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if json.Unmarshal(body, &in) != nil || in.APNSToken == "" {
		writeError(w, http.StatusBadRequest, "missing apnsToken")
		return
	}
	if !apnsTokenPattern.MatchString(in.APNSToken) {
		writeError(w, http.StatusBadRequest, "bad apnsToken")
		return
	}
	if err := s.Store.SetDeviceAPNSToken(r.Context(), dev.ID, in.APNSToken); err != nil {
		writeError(w, http.StatusServiceUnavailable, "server busy, try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var (
	apnsTokenPattern = regexp.MustCompile(`^[0-9a-fA-F]{32,200}$`)
	uriAttrPattern   = regexp.MustCompile(`URI="([^"]+)"`)
)

// handlePhoneHLS proxies the camera's live HLS and rewrites every playlist so
// segment URLs come back through this server carrying the phone's token — one
// URL (LAN or tunnel) covers both the API and the video.
func (s *Server) handlePhoneHLS(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
	id, file := r.PathValue("id"), r.PathValue("file")
	if !cameraIDPattern.MatchString(id) || !hlsFilePattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad stream path")
		return
	}
	if _, err := s.Store.Camera(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	q := r.URL.Query()
	token := q.Get("token")
	if token == "" {
		if a := r.Header.Get("Authorization"); len(a) > 7 {
			token = strings.TrimSpace(a[7:])
		}
	}
	q.Del("token") // never forward our credential to MediaMTX
	upstream := s.Media.HLSUpstream(id, file)
	if enc := q.Encode(); enc != "" {
		upstream += "?" + enc // keeps MediaMTX's ?session=
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	resp, err := hlsClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "stream not available")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "stream not available")
		return
	}
	if strings.HasSuffix(file, ".m3u8") {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadGateway, "stream not available")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(rewritePlaylist(data, id, token))
		return
	}
	// Buffer the segment and send it with a Content-Length, like the Mac does.
	// MediaMTX answers chunked; AVPlayer on iOS stalls live playback a few
	// seconds in when segments arrive without a length.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHLSSegment+1))
	if err != nil || len(data) > maxHLSSegment {
		writeError(w, http.StatusBadGateway, "stream not available")
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "video/MP2T"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// maxHLSSegment bounds one buffered live segment (a 2 s 4K segment is ~5 MB).
const maxHLSSegment = 32 << 20

// rewritePlaylist points every relative URI (segment lines and URI="…"
// attributes such as EXT-X-MAP) at /hls/<id>/… with the token appended.
func rewritePlaylist(data []byte, cameraID, token string) []byte {
	fix := func(ref string) string {
		if ref == "" || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "/") {
			return ref
		}
		sep := "?"
		if strings.Contains(ref, "?") {
			sep = "&" // MediaMTX lines already carry ?session=
		}
		return "/hls/" + cameraID + "/" + ref + sep + "token=" + url.QueryEscape(token)
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "#"):
			line = uriAttrPattern.ReplaceAllStringFunc(line, func(m string) string {
				return `URI="` + fix(uriAttrPattern.FindStringSubmatch(m)[1]) + `"`
			})
		default:
			line = fix(trimmed)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// MARK: - Dashboard: pairing + devices + remote access

type pairingView struct {
	Active    bool     `json:"active"`
	Code      string   `json:"code,omitempty"`
	ExpiresAt int64    `json:"expiresAt,omitempty"`
	URL       string   `json:"url,omitempty"`
	URLs      []string `json:"urls"`
	RemoteURL string   `json:"remoteURL,omitempty"`
	Payload   string   `json:"payload,omitempty"` // what the QR encodes
}

// lanURLs lists http://<ip>:<port> for this machine, preferring the address
// the admin is browsing from when it's one of ours.
func (s *Server) lanURLs(r *http.Request) []string {
	port := s.ListenPort
	if port == "" {
		port = "8090"
	}
	var urls []string
	browsing := ""
	if h := r.Host; h != "" {
		if host, _, err := splitHostPortLoose(h); err == nil {
			browsing = host
		}
	}
	for _, ip := range s.localIPs() {
		u := "http://" + ip + ":" + port
		if ip == browsing {
			urls = append([]string{u}, urls...)
		} else {
			urls = append(urls, u)
		}
	}
	return urls
}

func (s *Server) pairingSnapshot(r *http.Request) pairingView {
	v := pairingView{URLs: s.lanURLs(r), RemoteURL: s.remoteURL()}
	s.pairing.mu.Lock()
	a := s.pairing.active
	if a != nil && time.Now().After(a.ExpiresAt) {
		s.pairing.active, a = nil, nil
	}
	if a != nil {
		v.Active, v.Code, v.ExpiresAt, v.URL = true, a.Code, a.ExpiresAt.Unix(), a.URL
	}
	s.pairing.mu.Unlock()
	if v.Active {
		v.Payload = pairingPayload(v.URL, v.Code, v.RemoteURL)
	}
	return v
}

// pairingPayload is the Mac's QR format: sentinel-vms://pair?<percent-encoded
// JSON {url, code, v, remoteURL?}>. The phone tries url (LAN) first, then the
// https remoteURL, so a QR scanned on cellular still pairs.
func pairingPayload(lanURL, code, remoteURL string) string {
	m := map[string]any{"url": lanURL, "code": code, "v": 1}
	if strings.HasPrefix(remoteURL, "https://") {
		m["remoteURL"] = remoteURL
	}
	j, _ := json.Marshal(m)
	return "sentinel-vms://pair?" + strings.ReplaceAll(url.QueryEscape(string(j)), "+", "%20")
}

func (s *Server) handleGetPairing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.pairingSnapshot(r))
}

func (s *Server) handleStartPairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	urls := s.lanURLs(r)
	chosen := strings.TrimSpace(in.URL)
	if chosen == "" && len(urls) > 0 {
		chosen = urls[0]
	}
	if u, err := url.Parse(chosen); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "pick the address your iPhone can reach this server on")
		return
	}
	code, err := newPairingCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pairing.mu.Lock()
	s.pairing.active = &pairingCode{Code: code, ExpiresAt: time.Now().Add(pairingCodeLifetime), URL: chosen, CreatedBy: currentUser(r).Name}
	s.pairing.mu.Unlock()
	s.audit(r, "Devices", "Started iPhone pairing", "code valid 5 minutes, LAN address "+chosen)
	writeJSON(w, http.StatusOK, s.pairingSnapshot(r))
}

func (s *Server) handleCancelPairing(w http.ResponseWriter, r *http.Request) {
	s.pairing.mu.Lock()
	s.pairing.active = nil
	s.pairing.mu.Unlock()
	writeJSON(w, http.StatusOK, s.pairingSnapshot(r))
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.Store.PairedDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, devs)
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.Store.PairedDevice(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err := s.Store.RevokeDevice(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "Devices", "Revoked iPhone", d.Name)
	slog.Info("revoked paired device", "name", d.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
