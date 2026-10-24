package server

// API v2: phone cameras (WAVE23-PHONE-CAMERA.md section A and addendum 2).
//
//	POST   /api/v2/phone-cameras            {name, location?, recorderId?, recordingMode?} → 201 {camera, pushKey}
//	                                        (manageCameras; Sentinel Cloud only: 402 cloud_required)
//	POST   /api/v2/cameras/{id}/push-key    rotate → {pushKey, camera} (manageCameras; the old key stops at once)
//	DELETE /api/v2/cameras/{id}/push-key    revoke → 204 (manageCameras)
//	POST   /api/v2/cameras/{id}/push-token  header X-Sentinel-Push-Key, no session → a video token with
//	                                        the push right, like video-token (Sentinel Cloud only; rate-limited)
//
// The push key is 32 random bytes shown once; only its SHA-256 is stored
// and it is compared in constant time (store/phone.go). It is never logged
// or audited. The phone keeps it in its Keychain, so streaming survives
// the end of its user session; the push token it trades the key for names
// one camera and only the push right.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videogw"
	"sentinel-linux/internal/videotoken"
)

const (
	pushKeyHeader = "X-Sentinel-Push-Key"
	pushTokenTTL  = 5 * time.Minute // the phone refreshes at 4 min

	codeCloudRequired = "cloud_required"
	msgUploadsCloud   = "Uploading videos is part of Sentinel Cloud."
	msgPhoneCloud     = "Using an iPhone as a camera is part of Sentinel Cloud."

	phoneNoDual = "an iPhone camera has no second stream, so it can't record in dual mode"
)

// Push-token limits: per camera (a phone refreshes every 4 minutes) and
// per client address (guessing keys across cameras).
var (
	pushTokenPerCamera = &videogw.SessionLimiter{N: 10, Window: time.Minute}
	pushTokenPerClient = &videogw.SessionLimiter{N: 30, Window: time.Minute}
)

func (s *Server) registerV2PhoneCameras(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/phone-cameras", auth.ManageCameras, s.handleV2CreatePhoneCamera)
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/push-key", auth.ManageCameras, s.inScope("cameraId", s.handleV2RotatePushKey))
	s.v2Route(mux, "DELETE /api/v2/cameras/{cameraId}/push-key", auth.ManageCameras, s.inScope("cameraId", s.handleV2RevokePushKey))
	// The push key is the credential: no user session.
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/push-token", publicRoute, s.handleV2PushToken)
}

// MARK: - Sentinel Cloud gate

// cloudPaid reports whether this site's Sentinel Cloud subscription allows
// the paid features (uploads, phone cameras).
func (s *Server) cloudPaid(ctx context.Context) bool {
	return s.cloudOrIdle().Entitlement(ctx).Paid()
}

// requireCloud answers 402 cloud_required (and false) without a paid plan.
func (s *Server) requireCloud(w http.ResponseWriter, r *http.Request, msg string) bool {
	if s.cloudPaid(r.Context()) {
		return true
	}
	writeV2Error(w, http.StatusPaymentRequired, codeCloudRequired, msg)
	return false
}

// MARK: - Cameras

// phoneSource is a camera's source for its recording server's config.
func phoneSource(c *store.Camera) string {
	if c.IsPhone() {
		return link.SourcePhone
	}
	return ""
}

func (s *Server) handleV2CreatePhoneCamera(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string    `json:"name"`
		Location      *string   `json:"location"`
		RecorderID    *string   `json:"recorderId"`
		RecordingMode *string   `json:"recordingMode"`
		GroupIDs      *[]string `json:"groupIds"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if !s.requireCloud(w, r, msgPhoneCloud) {
		return
	}
	noONVIF := false
	ci := store.CameraInput{Name: in.Name, Recording: true, Source: link.SourcePhone, RecordingMode: in.RecordingMode, CameraAnalytics: &noONVIF}
	if in.Location != nil {
		ci.Location = *in.Location
	}
	if in.RecorderID != nil {
		if err := s.v2RecorderChoice(r.Context(), *in.RecorderID); err != nil {
			writeV2Fail(w, err, "recording server")
			return
		}
		ci.RecorderID = *in.RecorderID
	} else {
		id, ok := s.defaultRecorderID(r.Context())
		if !ok {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this site has several recording servers: choose one (recorderId)")
			return
		}
		ci.RecorderID = id
	}
	if err := s.checkGroupIDs(r.Context(), in.GroupIDs); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	key, hash, err := store.NewPushKey()
	if err != nil {
		writeV2Err(w, err)
		return
	}
	cam, err := s.saveCamera(r, "", ci)
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if err := s.Store.SetCameraPushKey(r.Context(), cam.ID, hash); err != nil {
		writeV2Err(w, err)
		return
	}
	if err := s.setCameraGroups(r, cam, in.GroupIDs); err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Cameras", "Added iPhone camera", cam.Name)
	cam, _ = s.Store.Camera(r.Context(), cam.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"camera": s.withCameraGroups(r.Context(), s.v2CameraView(cam)), "pushKey": key})
}

// pushCamera reads the path's camera, answering 404 unless it is a phone
// camera.
func (s *Server) pushCamera(w http.ResponseWriter, r *http.Request) (*store.Camera, bool) {
	c, err := s.Store.Camera(r.Context(), r.PathValue("cameraId"))
	if err != nil {
		writeV2Fail(w, err, "camera")
		return nil, false
	}
	if !c.IsPhone() {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "this camera isn't an iPhone camera")
		return nil, false
	}
	return c, true
}

func (s *Server) handleV2RotatePushKey(w http.ResponseWriter, r *http.Request) {
	c, ok := s.pushCamera(w, r)
	if !ok {
		return
	}
	key, hash, err := store.NewPushKey()
	if err == nil {
		err = s.Store.SetCameraPushKey(r.Context(), c.ID, hash)
	}
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Cameras", "Rotated iPhone camera key", c.Name)
	c, _ = s.Store.Camera(r.Context(), c.ID)
	writeJSON(w, http.StatusOK, map[string]any{"pushKey": key, "camera": s.withCameraGroups(r.Context(), s.v2CameraView(c))})
}

func (s *Server) handleV2RevokePushKey(w http.ResponseWriter, r *http.Request) {
	c, ok := s.pushCamera(w, r)
	if !ok {
		return
	}
	if err := s.Store.SetCameraPushKey(r.Context(), c.ID, ""); err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Cameras", "Revoked iPhone camera key", c.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2PushToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("cameraId")
	if !pushTokenPerClient.Allow(clientIP(r)) || !pushTokenPerCamera.Allow(id) {
		w.Header().Set("Retry-After", "60")
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "too many push-token requests; wait a minute")
		return
	}
	unauthorized := func() {
		w.Header().Set("WWW-Authenticate", `PushKey realm="sentinel"`)
		writeV2Error(w, http.StatusUnauthorized, codeUnauthorized, "this iPhone's camera key isn't valid any more; set the camera up again")
	}
	key := strings.TrimSpace(r.Header.Get(pushKeyHeader))
	if !cameraIDPattern.MatchString(id) || key == "" || len(key) > 256 {
		unauthorized()
		return
	}
	ok, err := s.Store.CheckCameraPushKey(r.Context(), id, key)
	if err != nil && err != store.ErrNotFound {
		writeV2Err(w, err)
		return
	}
	if !ok {
		slog.Warn("push token refused: wrong or revoked key", "camera", id, "client", clientIP(r))
		unauthorized()
		return
	}
	if !s.requireCloud(w, r, msgPhoneCloud) {
		return
	}
	cam, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	if !s.recorderConnected(cam.RecorderID) {
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the camera's recording server isn't connected")
		return
	}
	signer, err := s.videoSigner(r.Context())
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, codeInternal, "video tokens are unavailable")
		return
	}
	rights := []videotoken.Right{videotoken.Push}
	tok, c, err := signer.Sign(videotoken.Claims{Camera: cam.ID, Subject: "phone:" + cam.ID, Rights: rights, Audience: cam.RecorderID}, pushTokenTTL)
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	base := gatewayPrefix + tok
	if rs, err := s.Store.RecordingServer(r.Context(), cam.RecorderID); err == nil && rs.PublicURL != "" {
		base = strings.TrimRight(rs.PublicURL, "/") + gatewayPrefix + tok
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"baseUrl":    base,
		"gatewayUrl": gatewayPrefix + tok,
		"expiresAt":  c.ExpiresAt().UnixMilli(),
		"rights":     rights,
		"cameraId":   cam.ID,
		"recorderId": cam.RecorderID,
	})
}

// fillPhoneStatus: a phone camera that isn't streaming on a site without
// a Sentinel Cloud subscription says why (addendum 2).
func (s *Server) fillPhoneStatus(ctx context.Context, cams []v2Camera) {
	checked, paid := false, false
	for i := range cams {
		c := &cams[i]
		if c.Phone == nil || c.Phone.Streaming || c.Status.Message != "Phone not streaming" {
			continue
		}
		if !checked {
			checked, paid = true, s.cloudPaid(ctx)
		}
		if !paid {
			c.Status.Message = "Needs Sentinel Cloud"
		}
	}
}
