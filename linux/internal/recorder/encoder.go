package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
)

// encoderCmdTimeout bounds one camera round trip (clock sync, capabilities,
// profiles, a stream URI and options per profile).
const encoderCmdTimeout = 30 * time.Second

// encoderCamera finds the camera in the applied configuration and checks it
// can be configured over ONVIF; the failure is a ready command result.
func (r *Recorder) encoderCamera(c link.Command, id string) (link.CameraConfig, *link.Result) {
	if !cameraIDPattern.MatchString(id) {
		res := link.Fail(c, link.CodeBadRequest, "bad camera id")
		return link.CameraConfig{}, &res
	}
	cam, ok := r.camera(id)
	if !ok {
		res := link.Fail(c, link.CodeNotFound, "this recording server doesn't have that camera")
		return cam, &res
	}
	if cam.ONVIFURL == "" {
		res := link.Fail(c, link.CodeBadRequest, "this camera was added without ONVIF")
		return cam, &res
	}
	return cam, nil
}

func (r *Recorder) encoderGet(ctx context.Context, c link.Command, a link.EncoderGetArgs) link.Result {
	cam, fail := r.encoderCamera(c, a.CameraID)
	if fail != nil {
		return *fail
	}
	ctx, cancel := context.WithTimeout(ctx, encoderCmdTimeout)
	defer cancel()
	streams, err := onvif.GetEncoders(ctx, cam.ONVIFURL, cam.Username, cam.Password, cam.RTSPURL, cam.SubRTSPURL)
	if err != nil {
		return link.OK(c, link.EncoderGetResult{Error: err.Error(), AuthFailed: errors.Is(err, onvif.ErrAuth)})
	}
	raw, err := json.Marshal(streams)
	if err != nil {
		return link.Fail(c, link.CodeInternal, err.Error())
	}
	return link.OK(c, link.EncoderGetResult{Streams: raw})
}

// streamsDiscover finds the camera's streams (ONVIF, then guessed
// addresses checked with DESCRIBE). Unlike the encoder commands it works
// without ONVIF.
func (r *Recorder) streamsDiscover(ctx context.Context, c link.Command, a link.StreamsDiscoverArgs) link.Result {
	if !cameraIDPattern.MatchString(a.CameraID) {
		return link.Fail(c, link.CodeBadRequest, "bad camera id")
	}
	cam, ok := r.camera(a.CameraID)
	if !ok {
		return link.Fail(c, link.CodeNotFound, "this recording server doesn't have that camera")
	}
	t := onvif.StreamTarget{ONVIFURL: cam.ONVIFURL, MainURL: cam.RTSPURL, SubURL: cam.SubRTSPURL, Username: cam.Username, Password: cam.Password}
	if a.Login {
		t.Username, t.Password = a.Username, a.Password
	}
	ctx, cancel := context.WithTimeout(ctx, onvif.StreamsDiscoverTimeout)
	defer cancel()
	d, err := onvif.DiscoverStreams(ctx, t, r.DescribeStream)
	if err != nil {
		if errors.Is(err, onvif.ErrAuth) {
			r.auditCommand("Cameras", "Camera sign-in failed during stream discovery", cam.Name)
		}
		return link.OK(c, link.StreamsDiscoverResult{Error: err.Error(), AuthFailed: errors.Is(err, onvif.ErrAuth)})
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return link.Fail(c, link.CodeInternal, err.Error())
	}
	return link.OK(c, link.StreamsDiscoverResult{Discovery: raw})
}

func (r *Recorder) encoderSet(ctx context.Context, c link.Command, a link.EncoderSetArgs) link.Result {
	cam, fail := r.encoderCamera(c, a.CameraID)
	if fail != nil {
		return *fail
	}
	if a.ConfigToken == "" {
		return link.Fail(c, link.CodeBadRequest, "missing encoder configuration token")
	}
	ctx, cancel := context.WithTimeout(ctx, encoderCmdTimeout)
	defer cancel()
	err := onvif.SetEncoder(ctx, cam.ONVIFURL, cam.Username, cam.Password, onvif.EncoderConfig{
		ConfigToken: a.ConfigToken, Width: a.Width, Height: a.Height, FPS: a.FPS,
		Bitrate: a.Bitrate, GovLength: a.GovLength, Quality: a.Quality,
	})
	if err != nil {
		return link.OK(c, link.EncoderSetResult{Error: err.Error(), AuthFailed: errors.Is(err, onvif.ErrAuth)})
	}
	r.auditCommand("Cameras", "Changed stream settings", fmt.Sprintf("%s: %s", cam.Name, a.ConfigToken))
	return link.OK(c, link.EncoderSetResult{})
}
