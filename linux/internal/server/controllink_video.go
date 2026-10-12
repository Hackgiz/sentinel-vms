package server

// Video tokens the management server mints for itself: the control link's
// remote link.Recorder reaches a recording server's video plane at
// {videoUrl}/v/{token}/… (PLATFORM.md, "Control link wire protocol").

import (
	"net/http"
	"time"

	"sentinel-linux/internal/videogw"
	"sentinel-linux/internal/videotoken"
)

// MintRecorderVideoToken signs a short-lived token for recording server
// recorderID covering camera and rights. The subject is the signed-in user
// of r, if any (the recording server rate-limits WebRTC sessions by it),
// else videogw.ManagementSubject.
func (s *Server) MintRecorderVideoToken(r *http.Request, recorderID, camera string, rights []videotoken.Right, ttl time.Duration) (string, error) {
	signer, err := s.videoSigner(r.Context())
	if err != nil {
		return "", err
	}
	sub := videogw.ManagementSubject
	if u := currentUser(r); u != nil && u.ID != "" {
		sub = u.ID
	}
	tok, _, err := signer.Sign(videotoken.Claims{Camera: camera, Subject: sub, Rights: rights, Audience: recorderID}, ttl)
	return tok, err
}
