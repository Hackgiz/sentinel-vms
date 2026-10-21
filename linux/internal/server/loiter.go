package server

import (
	"fmt"
	"net/http"

	"sentinel-linux/internal/store"
)

// Loitering (WAVE16-DETECTION.md, stream B): loiterSeconds rides the camera
// create/patch bodies like audioDetect, so ManageCameras and the camera
// scope guard it.

func validateLoiterInput(in store.CameraInput) error {
	if in.LoiterSeconds != nil && !store.ValidLoiterSeconds(*in.LoiterSeconds) {
		return inputError{fmt.Sprintf("loiterSeconds must be between 0 and %d", store.MaxLoiterSeconds)}
	}
	return nil
}

// saveCameraLoiter persists loiterSeconds when it was sent, updates cam to
// match and audits a change.
func (s *Server) saveCameraLoiter(r *http.Request, cam *store.Camera, in store.CameraInput) error {
	if in.LoiterSeconds == nil || *in.LoiterSeconds == cam.LoiterSeconds {
		return nil
	}
	was := cam.LoiterSeconds
	if err := s.Store.SetCameraLoiter(r.Context(), cam.ID, *in.LoiterSeconds); err != nil {
		return err
	}
	cam.LoiterSeconds = *in.LoiterSeconds
	switch {
	case cam.LoiterSeconds == 0:
		s.audit(r, "Cameras", "Turned off loitering detection", cam.Name)
	case was == 0:
		s.audit(r, "Cameras", "Turned on loitering detection", fmt.Sprintf("%s (%d s)", cam.Name, cam.LoiterSeconds))
	default:
		s.audit(r, "Cameras", "Changed loitering time", fmt.Sprintf("%s (%d s)", cam.Name, cam.LoiterSeconds))
	}
	return nil
}
