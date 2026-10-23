package recorder

// Live video: HLS and WebRTC (WHEP, RFC 9725) from the local MediaMTX, whose
// listeners are on 127.0.0.1 only. These handlers are the recorder's half
// of the proxy; the management side has already checked the session,
// permission, origin, camera and rate limit before a request gets here.

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	whepMaxOffer = 64 << 10
	whepMaxPatch = 16 << 10
)

var (
	hlsClient = &http.Client{Timeout: 15 * time.Second}
	// MediaMTX session IDs are UUIDs.
	whepSessionPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// Signaling can wait for an on-demand sub-stream to start (~10 s max).
	whepClient = &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

// handleLive proxies one HLS playlist or segment. The query is passed
// through: MediaMTX >= 1.21 ties HLS requests together with ?session=.
func (r *Recorder) handleLive(w http.ResponseWriter, req *http.Request) {
	id, file := req.PathValue("id"), req.PathValue("file")
	if !cameraIDPattern.MatchString(id) || !hlsFilePattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad stream path")
		return
	}
	upstream := r.Media.HLSUpstream(id, file)
	if req.URL.RawQuery != "" {
		upstream += "?" + req.URL.RawQuery
	}
	up, _ := http.NewRequestWithContext(req.Context(), http.MethodGet, upstream, nil)
	resp, err := hlsClient.Do(up)
	if err != nil {
		writeError(w, http.StatusBadGateway, "stream not available")
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasSuffix(file, ".m3u8") {
		w.Header().Set("Cache-Control", "no-store")
	}
	if file == "index.m3u8" && resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			writeError(w, http.StatusBadGateway, "stream not available")
			return
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, dropOpusAudio(string(body)))
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// dropOpusAudio removes Opus audio from an HLS multivariant playlist. Apple's
// players (iPhone, Mac, Safari) refuse the whole stream with "Cannot Open"
// when a variant lists Opus, which MediaMTX passes through from cameras that
// send it. Video plays on its own; browsers get live audio over WebRTC.
func dropOpusAudio(playlist string) string {
	if !strings.Contains(playlist, "opus") {
		return playlist
	}
	lines := strings.Split(playlist, "\n")
	groups := map[string]bool{}
	for i, l := range lines {
		if !strings.HasPrefix(l, "#EXT-X-STREAM-INF:") || !strings.Contains(l, "opus") {
			continue
		}
		if m := hlsAudioAttr.FindStringSubmatch(l); m != nil {
			groups[m[1]] = true
			l = strings.Replace(l, m[0], "", 1)
		}
		lines[i] = hlsCodecsAttr.ReplaceAllStringFunc(l, func(attr string) string {
			var keep []string
			for _, c := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(attr, `CODECS="`), `"`), ",") {
				if !strings.HasPrefix(c, "opus") {
					keep = append(keep, c)
				}
			}
			return `CODECS="` + strings.Join(keep, ",") + `"`
		})
	}
	out := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(l, "#EXT-X-MEDIA:") && strings.Contains(l, "TYPE=AUDIO") {
			if m := hlsGroupAttr.FindStringSubmatch(l); m != nil && groups[m[1]] {
				continue
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

var (
	hlsAudioAttr  = regexp.MustCompile(`,AUDIO="([^"]*)"`)
	hlsCodecsAttr = regexp.MustCompile(`CODECS="[^"]*"`)
	hlsGroupAttr  = regexp.MustCompile(`GROUP-ID="([^"]*)"`)
)

func (r *Recorder) handleWHEPOffer(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if r.Media == nil || !r.Media.WebRTCEnabled() {
		writeError(w, http.StatusServiceUnavailable, "WebRTC is turned off on this server")
		return
	}
	offer, err := io.ReadAll(http.MaxBytesReader(w, req.Body, whepMaxOffer))
	if err != nil || len(offer) == 0 {
		writeError(w, http.StatusBadRequest, "invalid SDP offer")
		return
	}
	up, _ := http.NewRequestWithContext(req.Context(), http.MethodPost, r.Media.WHEPUpstream(id, ""), strings.NewReader(string(offer)))
	up.Header.Set("Content-Type", "application/sdp")
	resp, err := whepClient.Do(up)
	if err != nil {
		writeError(w, http.StatusBadGateway, "live stream not available")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, whepMaxOffer))
	if err != nil {
		writeError(w, http.StatusBadGateway, "live stream not available")
		return
	}
	if resp.StatusCode != http.StatusCreated {
		// e.g. 400 "codecs not supported" (H.265 to a browser that can't
		// take it) or 404 "no stream": the dashboard falls back to HLS.
		copyHeaders(w, resp, "Content-Type")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	loc, ok := rewriteWHEPLocation(resp.Header.Get("Location"), id)
	if !ok {
		slog.Warn("unexpected WHEP Location from mediamtx", "location", resp.Header.Get("Location"))
		writeError(w, http.StatusBadGateway, "live stream not available")
		return
	}
	answer := addRequestHostCandidates(string(body), req.Host, r.Media.WebRTC.ICEPort)
	copyHeaders(w, resp, "Content-Type", "ETag", "Accept-Patch")
	w.Header().Set("Location", loc)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, answer)
}

func (r *Recorder) handleWHEPSession(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if r.Media == nil || !r.Media.WebRTCEnabled() {
		writeError(w, http.StatusServiceUnavailable, "WebRTC is turned off on this server")
		return
	}
	session := req.PathValue("session")
	if !whepSessionPattern.MatchString(session) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var body io.Reader
	if req.Method == http.MethodPatch {
		frag, err := io.ReadAll(http.MaxBytesReader(w, req.Body, whepMaxPatch))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid ICE fragment")
			return
		}
		body = strings.NewReader(string(frag))
	}
	up, _ := http.NewRequestWithContext(req.Context(), req.Method, r.Media.WHEPUpstream(id, session), body)
	if body != nil {
		up.Header.Set("Content-Type", "application/trickle-ice-sdpfrag")
	}
	if v := req.Header.Get("If-Match"); v != "" {
		up.Header.Set("If-Match", v)
	}
	resp, err := whepClient.Do(up)
	if err != nil {
		writeError(w, http.StatusBadGateway, "live stream not available")
		return
	}
	defer resp.Body.Close()
	copyHeaders(w, resp, "Content-Type", "ETag")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, whepMaxPatch))
}

func copyHeaders(w http.ResponseWriter, resp *http.Response, names ...string) {
	for _, n := range names {
		if v := resp.Header.Get(n); v != "" {
			w.Header().Set(n, v)
		}
	}
}

// rewriteWHEPLocation maps MediaMTX's session URL (/<path>/whep/<id>, maybe
// absolute or relative) onto Sentinel's /webrtc/<camera>/whep/<id>. The
// MediaMTX path (which may be "<camera>-sub") never reaches the browser.
func rewriteWHEPLocation(loc, cameraID string) (string, bool) {
	u, err := url.Parse(loc)
	if err != nil || loc == "" {
		return "", false
	}
	p := strings.TrimRight(u.Path, "/")
	session := p[strings.LastIndex(p, "/")+1:]
	if !whepSessionPattern.MatchString(session) {
		return "", false
	}
	return "/webrtc/" + cameraID + "/whep/" + session, true
}

// addRequestHostCandidates adds host ICE candidates for the address the
// browser used to reach Sentinel, when that is an IP MediaMTX didn't
// already offer. That's what makes WebRTC work out of the box in Docker
// (bridge network: MediaMTX only sees the container's IP) as long as the ICE
// port is published 1:1. Hostnames are left alone (browsers' handling of
// FQDN candidates varies) — use the "additional hosts" setting for those.
func addRequestHostCandidates(sdp, host string, port int) string {
	if port <= 0 {
		return sdp
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return sdp
	}
	addr := ip.String()
	ps := strconv.Itoa(port)
	lines := strings.Split(sdp, "\r\n")
	for _, l := range lines {
		if f := strings.Fields(l); strings.HasPrefix(l, "a=candidate:") && len(f) >= 6 && f[4] == addr {
			return sdp // already offered
		}
	}
	extra := []string{
		"a=candidate:sentinel1 1 udp 2113937151 " + addr + " " + ps + " typ host",
		"a=candidate:sentinel2 1 tcp 1677721855 " + addr + " " + ps + " typ host tcptype passive",
	}
	out := make([]string, 0, len(lines)+4)
	inMedia, added := false, false
	for _, l := range lines {
		if strings.HasPrefix(l, "m=") {
			inMedia, added = true, false
		}
		// Before the section's first candidate (or end-of-candidates).
		if inMedia && !added && (strings.HasPrefix(l, "a=candidate:") || l == "a=end-of-candidates") {
			out = append(out, extra...)
			added = true
		}
		out = append(out, l)
	}
	return strings.Join(out, "\r\n")
}
