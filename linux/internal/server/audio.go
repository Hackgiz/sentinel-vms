package server

import (
	"fmt"
	"net/http"

	"sentinel-linux/internal/store"
)

// Audio event detection settings (WAVE8(B)): the camera payload's
// audioDetect / audioThresholdDb ride the camera create/patch bodies, so the
// existing ManageCameras permission and camera scope guard them.

// audioThreshold is the value sent to the recording server: the threshold in
// dB when audio detection is on, 0 when off.
func audioThreshold(c *store.Camera) int {
	if !c.AudioDetect {
		return 0
	}
	return c.AudioThresholdDB
}

func validateAudioInput(in store.CameraInput) error {
	if in.AudioThresholdDB != nil && !store.ValidAudioThreshold(*in.AudioThresholdDB) {
		return inputError{fmt.Sprintf("audioThresholdDb must be between %d and %d", store.AudioMinThresholdDB, store.AudioMaxThresholdDB)}
	}
	return nil
}

// saveCameraAudio persists the audio part of a camera save (if any was sent),
// updates cam to match and audits a change.
func (s *Server) saveCameraAudio(r *http.Request, cam *store.Camera, in store.CameraInput) error {
	if in.AudioDetect == nil && in.AudioThresholdDB == nil {
		return nil
	}
	wasOn, wasDB := cam.AudioDetect, cam.AudioThresholdDB
	on, db, err := s.Store.SetCameraAudio(r.Context(), cam.ID, in.AudioDetect, in.AudioThresholdDB)
	if err != nil {
		return err
	}
	cam.AudioDetect, cam.AudioThresholdDB = on, db
	switch {
	case on && !wasOn:
		s.audit(r, "Cameras", "Turned on audio detection", fmt.Sprintf("%s (%d dB over the noise floor)", cam.Name, db))
	case !on && wasOn:
		s.audit(r, "Cameras", "Turned off audio detection", cam.Name)
	case db != wasDB:
		s.audit(r, "Cameras", "Changed audio detection threshold", fmt.Sprintf("%s (%d dB over the noise floor)", cam.Name, db))
	}
	return nil
}

// audioAlarm is the alarm an audio event raises: serious sounds are
// Critical, a plain loud noise or an alarm tone a Warning.
func audioAlarm(label string) (title, severity string) {
	switch label {
	case "Gunshot", "Glass break", "Scream":
		return label + " heard", "Critical"
	case "Alarm":
		return "Alarm sound heard", "Warning"
	}
	return "Loud noise heard", "Warning"
}
