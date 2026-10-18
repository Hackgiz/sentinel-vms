package server

// Expiring share links, public side (WAVE7-FEATURES.md, Stream D).
//
//	GET /share/{token}        the minimal dark page with the clip in a <video>
//	GET /share/{token}/media  the clip itself (Range supported; ?dl=1 downloads)
//
// Served by the LAN web origin only: the tunnel origin (CompanionHandler)
// never mounts these. A token is looked up by its SHA-256 in constant time;
// expired, revoked, used-up and unknown links all answer the same generic
// 410 page. Opening the page counts one use; the page then embeds media URLs
// carrying a short-lived HMAC "view" proof, so the browser's many Range
// requests and the download button don't each count and a link with
// max_uses=1 still plays. Token guesses are rate limited per IP.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/store"
)

const (
	shareGuessLimit  = 10            // failed lookups per IP per window
	shareGuessWindow = time.Minute   // sliding window
	shareViewTTL     = 6 * time.Hour // how long a page view's media URLs work
	shareAuditUser   = "Share link"  // audit "user" for anonymous access
	shareMaxTracked  = 2000          // distinct IPs tracked before pruning
)

// shareGuard holds the per-server state of the public share route: the HMAC
// key for view proofs and the per-IP guess limiter (same sliding-window
// pattern as the WHEP session limiter in webrtc.go).
type shareGuard struct {
	key []byte
	now func() time.Time

	mu     sync.Mutex
	recent map[string][]time.Time // ip → failure times
}

var (
	shareGuardsMu sync.Mutex
	shareGuards   = map[*Server]*shareGuard{}
)

// shareState returns s's guard, created on first use (the Server struct is
// shared with other streams, so this state lives here rather than on it).
func (s *Server) shareState() *shareGuard {
	shareGuardsMu.Lock()
	defer shareGuardsMu.Unlock()
	g := shareGuards[s]
	if g == nil {
		g = &shareGuard{key: make([]byte, 32), now: time.Now, recent: map[string][]time.Time{}}
		_, _ = rand.Read(g.key)
		shareGuards[s] = g
	}
	return g
}

// blocked reports whether ip has used up its failed lookups.
func (g *shareGuard) blocked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.prune(ip)) >= shareGuessLimit
}

// fail records a failed lookup from ip.
func (g *shareGuard) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recent[ip] = append(g.prune(ip), g.now())
	if len(g.recent) > shareMaxTracked { // drop idle addresses
		for k := range g.recent {
			if len(g.prune(k)) == 0 {
				delete(g.recent, k)
			}
		}
	}
}

// prune drops ip's expired failures; the caller holds g.mu.
func (g *shareGuard) prune(ip string) []time.Time {
	now := g.now()
	kept := g.recent[ip][:0]
	for _, t := range g.recent[ip] {
		if now.Sub(t) < shareGuessWindow {
			kept = append(kept, t)
		}
	}
	g.recent[ip] = kept
	return kept
}

// viewProof signs "this page view of link id is good until exp".
func (g *shareGuard) viewProof(linkID string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return e + "." + g.sig(linkID, e)
}

func (g *shareGuard) sig(linkID, exp string) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(linkID + "|" + exp))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func (g *shareGuard) checkProof(linkID, proof string) bool {
	e, sig, ok := strings.Cut(proof, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(e, 10, 64)
	if err != nil || exp <= g.now().Unix() {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(g.sig(linkID, e)))
}

func (s *Server) registerSharePublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /share/{token}", s.handleSharePage)
	mux.HandleFunc("GET /share/{token}/media", s.handleShareMedia)
}

// shareAudit writes an audit entry for anonymous access, with the remote IP.
func (s *Server) shareAudit(r *http.Request, action, detail string) {
	if err := s.Store.Audit(r.Context(), shareAuditUser, "Evidence", action, detail+" (from "+clientIP(r)+")"); err != nil {
		slog.Error("audit write failed", "err", err)
	}
}

func setShareHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
}

// shareGone is the one answer for an expired, revoked, used-up or unknown
// link; it must not reveal which.
func shareGone(w http.ResponseWriter) {
	setShareHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	_, _ = w.Write([]byte(sharePageHead("Link unavailable") +
		`<main><h1>This link is no longer available</h1><p>It may have expired or been withdrawn. Ask the person who sent it for a new one.</p></main></body></html>`))
}

func shareThrottled(w http.ResponseWriter) {
	setShareHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Retry-After", strconv.Itoa(int(shareGuessWindow.Seconds())))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("Too many attempts. Try again in a minute.\n"))
}

// shareEvidence resolves a link's evidence item; ok is false if it's gone.
func (s *Server) shareEvidence(r *http.Request, l *store.ShareLink) (*store.EvidenceCase, bool) {
	ev, err := s.Store.EvidenceCaseByID(r.Context(), l.EvidenceID)
	return ev, err == nil
}

func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	g, ip := s.shareState(), clientIP(r)
	if g.blocked(ip) {
		shareThrottled(w)
		return
	}
	token := r.PathValue("token")
	l, err := s.Store.ShareLinkByToken(r.Context(), token)
	if err != nil {
		g.fail(ip)
		shareGone(w)
		return
	}
	ev, ok := s.shareEvidence(r, l)
	if !ok || !l.Live(time.Now()) || s.Store.ClaimShareUse(r.Context(), l.ID, time.Now()) != nil {
		g.fail(ip)
		s.shareAudit(r, "Share link refused", "link "+l.ID+" ("+l.CaseID+")")
		shareGone(w)
		return
	}
	s.shareAudit(r, "Share link opened", "link "+l.ID+" case "+ev.CaseID)

	exp := time.Now().Add(shareViewTTL)
	if lim := time.Unix(l.ExpiresAt, 0); lim.Before(exp) {
		exp = lim
	}
	media := "/share/" + token + "/media?v=" + g.viewProof(l.ID, exp)
	setShareHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString(sharePageHead(ev.Title))
	b.WriteString(`<main><h1>` + html.EscapeString(ev.Title) + `</h1>`)
	b.WriteString(`<p class="meta">` + html.EscapeString(ev.CameraName) + ` · ` + html.EscapeString(ev.Range) + ` · Case ` + html.EscapeString(ev.CaseID) + `</p>`)
	if l.Note != "" {
		b.WriteString(`<p class="note">` + html.EscapeString(l.Note) + `</p>`)
	}
	b.WriteString(`<video controls preload="metadata" playsinline src="` + html.EscapeString(media) + `"></video>`)
	b.WriteString(`<p><a class="btn" href="` + html.EscapeString(media+"&dl=1") + `">Download</a></p>`)
	b.WriteString(`<p class="meta">SHA-256 ` + html.EscapeString(ev.SHA256) + `</p>`)
	b.WriteString(`<p class="meta">Shared link, expires ` + time.Unix(l.ExpiresAt, 0).UTC().Format("2006-01-02 15:04 UTC") + `.</p>`)
	b.WriteString(`</main></body></html>`)
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleShareMedia(w http.ResponseWriter, r *http.Request) {
	g, ip := s.shareState(), clientIP(r)
	if g.blocked(ip) {
		shareThrottled(w)
		return
	}
	l, err := s.Store.ShareLinkByToken(r.Context(), r.PathValue("token"))
	// The page view already took its use; here the link only has to be
	// unrevoked and unexpired, and the caller must hold a view proof.
	if err != nil || l.RevokedAt != 0 || l.ExpiresAt <= time.Now().Unix() || !g.checkProof(l.ID, r.URL.Query().Get("v")) {
		g.fail(ip)
		shareGone(w)
		return
	}
	ev, ok := s.shareEvidence(r, l)
	if !ok {
		g.fail(ip)
		shareGone(w)
		return
	}
	download := r.URL.Query().Get("dl") == "1"
	rng := r.Header.Get("Range")
	first := rng == "" || strings.HasPrefix(rng, "bytes=0-")
	setShareHeaders(w)
	s.serveVideoPath(w, r, ev.RecorderID, "/rs/evidence/"+filepath.Base(ev.File), func(h http.Header) {
		if first { // one audit entry per playback or download, not per Range chunk
			what := "Share link played"
			if download {
				what = "Share link downloaded"
			}
			s.shareAudit(r, what, fmt.Sprintf("link %s case %s", l.ID, ev.CaseID))
		}
		setContentType(h, "video/mp4")
		if download {
			h.Set("Content-Disposition", `attachment; filename="`+ev.CaseID+`.mp4"`)
		} else {
			h.Set("Content-Disposition", `inline; filename="`+ev.CaseID+`.mp4"`)
		}
		h.Set("X-Content-SHA256", ev.SHA256)
	})
}

// sharePageHead opens a self-contained dark page: inline CSS, no scripts, no
// app bundle.
func sharePageHead(title string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta name="robots" content="noindex,nofollow"><title>` + html.EscapeString(title) + `</title><style>` +
		`:root{color-scheme:dark}body{margin:0;background:#0b0d10;color:#e6e8eb;font:16px/1.5 system-ui,-apple-system,Segoe UI,Roboto,sans-serif}` +
		`main{max-width:960px;margin:0 auto;padding:24px 16px}h1{font-size:1.3rem;margin:0 0 4px}` +
		`.meta{color:#8b929b;font-size:.85rem;margin:4px 0;word-break:break-all}.note{margin:12px 0;padding:10px 12px;background:#151a20;border-radius:8px}` +
		`video{width:100%;max-height:70vh;background:#000;border-radius:8px;margin:12px 0}` +
		`.btn{display:inline-block;padding:9px 18px;border-radius:8px;background:#2b6fe0;color:#fff;text-decoration:none;font-weight:600}` +
		`</style></head><body>`
}
