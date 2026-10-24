package server

// API v2 video (api/openapi.yaml, "Video"): POST /cameras/{id}/video-token
// and the /v/{token}/… gateway.
//
// A video token (internal/videotoken) is signed with the management
// server's Ed25519 key (store.VideoTokenKey, sealed with the install key)
// and names one camera, one user, its rights and the recording server it's
// for. Clients put it in the path, /v/<token>/live/<camera>/index.m3u8, so
// the relative URLs inside HLS playlists carry it and no player needs
// custom headers.
//
// This server is the gateway (internal/videogw): it verifies the token,
// checks that the path is one the token's camera and rights cover, and
// hands the request to the video plane of the recording server the token
// is for (its aud) with the prefix removed. Absolute URLs coming back
// (playback playlists, the WHEP Location) get the prefix put back so the
// client stays under it. Each recording server does the same with the keys
// from its config snapshot (recorder.VideoTokens), and baseUrl points there
// when it has a public URL.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videogw"
	"sentinel-linux/internal/videotoken"
)

const (
	videoTokenTTL = 5 * time.Minute
	gatewayPrefix = videogw.Prefix
)

func (s *Server) registerV2Video(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/video-token", auth.ViewLive, s.inScope("cameraId", s.handleVideoToken))
	// The gateway is not an /api/v2 route (the token is the credential, in
	// the path), so it's registered directly and stays out of the spec's
	// route list.
	mux.HandleFunc(gatewayPrefix, s.handleVideoGateway)
}

// MARK: - Signing key

// videoSigners caches each store's signer so the sealed key is read once.
// Keyed by store (Server's fields live in server.go); tests open many.
var videoSigners sync.Map // *store.Store → *videotoken.Signer

// videoSigner is the management server's token signer; its key is made on
// first use.
func (s *Server) videoSigner(ctx context.Context) (*videotoken.Signer, error) {
	if v, ok := videoSigners.Load(s.Store); ok {
		return v.(*videotoken.Signer), nil
	}
	key, err := s.Store.VideoTokenKey(ctx)
	if err != nil {
		return nil, err
	}
	v, _ := videoSigners.LoadOrStore(s.Store, videotoken.NewSigner(key))
	return v.(*videotoken.Signer), nil
}

// videoTokenKeys are the public keys recording servers verify tokens with
// (link.Snapshot.VideoTokenKeys). One key until rotation exists.
func (s *Server) videoTokenKeys(ctx context.Context) []string {
	signer, err := s.videoSigner(ctx)
	if err != nil {
		slog.Error("video token key unavailable", "err", err)
		return nil
	}
	return []string{videotoken.EncodePublicKey(signer.PublicKey())}
}

// MARK: - Issuing

func (s *Server) handleVideoToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rights     []videotoken.Right `json:"rights"`
		RecorderID *string            `json:"recorderId"`
	}
	if !readOptionalV2JSON(w, r, &in) {
		return
	}
	rights, err := videoRights(in.Rights)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	u := currentUser(r)
	if hasRight(rights, videotoken.Export) && !s.can(u.Role, auth.ExportEvidence) {
		writeV2Error(w, http.StatusForbidden, codeForbidden, "your role ("+u.Role+") can't export video")
		return
	}
	id := r.PathValue("cameraId")
	if !cameraIDPattern.MatchString(id) {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "camera not found")
		return
	}
	cam, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	aud, msg := s.videoAudience(r.Context(), cam, in.RecorderID, rights)
	if msg != "" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, msg)
		return
	}
	if !s.recorderConnected(aud) {
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the camera's recording server isn't connected")
		return
	}
	signer, err := s.videoSigner(r.Context())
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, codeInternal, "video tokens are unavailable")
		return
	}
	tok, c, err := signer.Sign(videotoken.Claims{Camera: cam.ID, Subject: u.ID, Rights: rights, Audience: aud}, videoTokenTTL)
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	// Live view isn't audited (it never was); access to footage is.
	if hasRight(rights, videotoken.Playback) || hasRight(rights, videotoken.Export) {
		s.audit(r, "Recordings", "Issued video token", cam.Name+" ("+joinRights(rights)+")")
	}
	// Clients go straight to the recording server when it has a public
	// URL; the gateway path works wherever the API does.
	base := gatewayPrefix + tok
	if rs, err := s.Store.RecordingServer(r.Context(), aud); err == nil && rs.PublicURL != "" {
		base = strings.TrimRight(rs.PublicURL, "/") + gatewayPrefix + tok
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"baseUrl":    base,
		"gatewayUrl": gatewayPrefix + tok,
		"expiresAt":  c.ExpiresAt().UnixMilli(),
		"rights":     rights,
		"cameraId":   cam.ID,
		"recorderId": aud,
	})
}

// videoAudience picks the recording server a token is for: the camera's
// current one, or (for footage recorded before a move) one it was on,
// which only playback and export rights may ask for. msg explains a bad
// request.
func (s *Server) videoAudience(ctx context.Context, cam *store.Camera, asked *string, rights []videotoken.Right) (aud, msg string) {
	if asked == nil {
		return cam.RecorderID, ""
	}
	if hasRight(rights, videotoken.Live) {
		return "", "recorderId is for recorded footage: ask for playback or export rights only"
	}
	if *asked == cam.RecorderID {
		return *asked, ""
	}
	ps, err := s.Store.CameraPlacements(ctx, cam.ID)
	if err == nil {
		for _, p := range ps {
			if p.RecorderID == *asked {
				return *asked, ""
			}
		}
	}
	return "", "this camera never recorded on recording server " + strconv.Quote(*asked)
}

// videoRights validates the requested rights: absent = live, duplicates
// dropped, order kept.
func videoRights(in []videotoken.Right) ([]videotoken.Right, error) {
	if in == nil {
		return []videotoken.Right{videotoken.Live}, nil
	}
	if len(in) == 0 {
		return nil, errors.New("ask for at least one right (live, playback, export)")
	}
	out := make([]videotoken.Right, 0, len(in))
	for _, r := range in {
		if !videotoken.ValidRight(r) || r == videotoken.Push { // push: POST …/push-token only

			return nil, errors.New("unknown right " + strconv.Quote(string(r)) + " (live, playback, export)")
		}
		if !hasRight(out, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

func hasRight(rights []videotoken.Right, r videotoken.Right) bool {
	return videotoken.Claims{Rights: rights}.Has(r)
}

func joinRights(rights []videotoken.Right) string {
	s := make([]string, len(rights))
	for i, r := range rights {
		s[i] = string(r)
	}
	return strings.Join(s, ", ")
}

// readOptionalV2JSON is readV2JSON for a body that may be left out.
func readOptionalV2JSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid request: "+err.Error())
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return readV2JSON(w, r, v)
}

// MARK: - Gateway

// handleVideoGateway serves /v/{token}/{video-plane path} (internal/videogw;
// recording servers serve the same gateway with the keys from their
// snapshot).
func (s *Server) handleVideoGateway(w http.ResponseWriter, r *http.Request) {
	if !s.HasRecorder() {
		w.Header().Set("Cache-Control", "no-store")
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "no recording server is connected")
		return
	}
	gw := videogw.Gateway{
		Verify: func(token string) (videotoken.Claims, error) { return s.verifyVideoToken(r.Context(), token) },
		// The token's audience is the recording server it was issued for.
		Plane: func(c videotoken.Claims) http.Handler {
			if !s.recorderConnected(c.Audience) {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the recording server for this video isn't connected")
				})
			}
			return s.recByID(c.Audience).Video()
		},
		AllowSession: s.allowSession, // shared with the cookie route (webrtc.go)
	}
	gw.ServeHTTP(w, r)
}

// verifyVideoToken checks a gateway token.
func (s *Server) verifyVideoToken(ctx context.Context, token string) (videotoken.Claims, error) {
	signer, err := s.videoSigner(ctx)
	if err != nil {
		return videotoken.Claims{}, err
	}
	// The gateway serves every recording server, so the audience may be
	// any enrolled server that isn't revoked; the token is then checked
	// for that audience.
	aud := tokenAudience(token)
	claims, err := videotoken.NewVerifier(aud, signer.PublicKey()).Verify(token)
	if err != nil {
		return videotoken.Claims{}, err
	}
	if rs, e := s.Store.RecordingServer(ctx, aud); e != nil || rs.Revoked() {
		return videotoken.Claims{}, videotoken.ErrWrongAudience
	}
	return claims, nil
}

// tokenAudience reads a token's aud claim unchecked; Verify then checks
// the whole token for that audience.
func tokenAudience(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Aud string `json:"aud"`
	}
	_ = json.Unmarshal(raw, &c)
	return c.Aud
}

// prefixPlaylist puts prefix in front of every absolute-path URI in an HLS
// playlist (videogw.PrefixPlaylist).
func prefixPlaylist(data []byte, prefix string) []byte { return videogw.PrefixPlaylist(data, prefix) }
