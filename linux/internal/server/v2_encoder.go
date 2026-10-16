package server

// API v2: camera stream settings (ONVIF video encoder configuration) for
// Sentinel Admin. The camera is the source of truth: every GET asks it, a
// PUT validates against the ranges it reports and sets it. A camera on this
// server is reached directly; one on a remote recording server over the
// control link (link.CmdEncoderGet / CmdEncoderSet), since the camera is on
// the recorder's network. Routes need auth.ManageCameras and respect camera
// scope (scope.go): out of scope is a 404.
//
// TODO(stream-settings follow-ups): codec switching and Media2 (H.265-only
// cameras), audio encoder / multicast / OSD, bulk apply, Mac app UI.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Encoder(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}/stream-settings", auth.ManageCameras, s.inScope("cameraId", s.handleV2StreamSettings))
	s.v2Route(mux, "PUT /api/v2/cameras/{cameraId}/stream-settings/{configToken}", auth.ManageCameras, s.inScope("cameraId", s.handleV2SetStreamSettings))
}

type v2Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type v2StreamCurrent struct {
	Width     int `json:"width"`
	Height    int `json:"height"`
	FPS       int `json:"fps"`
	Bitrate   int `json:"bitrate"`
	GovLength int `json:"govLength"`
	Quality   int `json:"quality"`
}

type v2StreamOptions struct {
	Resolutions []onvif.Resolution `json:"resolutions"`
	FPS         v2Range            `json:"fps"`
	Bitrate     v2Range            `json:"bitrate"`
	Gov         v2Range            `json:"gov"`
	Quality     v2Range            `json:"quality"`
}

type v2Stream struct {
	ConfigToken  string          `json:"configToken"`
	ProfileToken string          `json:"profileToken"`
	Name         string          `json:"name"`
	InUse        string          `json:"inUse"`
	Encoding     string          `json:"encoding"`
	Current      v2StreamCurrent `json:"current"`
	Options      v2StreamOptions `json:"options"`
}

func toV2Stream(e onvif.EncoderStream) v2Stream {
	o := e.Options
	res := o.Resolutions
	if res == nil {
		res = []onvif.Resolution{}
	}
	return v2Stream{
		ConfigToken: e.ConfigToken, ProfileToken: e.ProfileToken, Name: e.Name, InUse: e.InUse, Encoding: e.Encoding,
		Current: v2StreamCurrent{Width: e.Width, Height: e.Height, FPS: e.FPS, Bitrate: e.Bitrate, GovLength: e.GovLength, Quality: e.Quality},
		Options: v2StreamOptions{
			Resolutions: res,
			FPS:         v2Range{o.FPSMin, o.FPSMax}, Bitrate: v2Range{o.BitrateMin, o.BitrateMax},
			Gov: v2Range{o.GovMin, o.GovMax}, Quality: v2Range{o.QualityMin, o.QualityMax},
		},
	}
}

// encoderTarget is a camera the stream-settings routes can work on.
type encoderTarget struct {
	cam      *store.Camera
	pass     string
	recorder string // the recording server that has the camera
	local    bool
}

// encoderTarget loads the camera and checks it can be configured; it
// writes the error answer and returns nil otherwise.
func (s *Server) encoderTarget(w http.ResponseWriter, r *http.Request) *encoderTarget {
	cam, err := s.Store.Camera(r.Context(), r.PathValue("cameraId"))
	if err != nil {
		writeV2Err(w, err)
		return nil
	}
	if cam.ONVIFURL == "" {
		writeV2Error(w, http.StatusConflict, codeConflict, "this camera was added without ONVIF")
		return nil
	}
	pass, err := s.Store.CameraPassword(cam)
	if err != nil {
		slog.Error("camera password unreadable", "camera", cam.Name, "err", err)
		writeV2Error(w, http.StatusInternalServerError, codeInternal, "the camera's password could not be read")
		return nil
	}
	rec := cam.RecorderID
	if rec == "" {
		rec = store.LocalRecorderID
	}
	return &encoderTarget{cam: cam, pass: pass, recorder: rec, local: rec == store.LocalRecorderID}
}

// encoderErr is a failure with the HTTP answer it deserves.
type encoderErr struct {
	status int
	code   string
	msg    string
}

func (e *encoderErr) Error() string { return e.msg }

func cameraFailure(msg string, authFailed bool) *encoderErr {
	if authFailed {
		msg = "the camera rejected the stored username or password"
	}
	return &encoderErr{http.StatusBadGateway, codeUnavailable, msg}
}

// linkFailure words a control-link failure for an admin. A recording
// server that isn't connected is a bad gateway, not "service unavailable".
func linkFailure(err error) *encoderErr {
	var ce *link.CommandError
	switch {
	case errors.Is(err, link.ErrNoRecorder):
		return &encoderErr{http.StatusBadGateway, codeUnavailable, "the recording server that has this camera isn't connected"}
	case errors.As(err, &ce) && ce.Code == link.CodeUnknownCommand:
		return &encoderErr{http.StatusBadGateway, codeUnavailable, "the recording server is too old to change stream settings — update it"}
	case errors.As(err, &ce) && ce.Code == link.CodeNotFound:
		return &encoderErr{http.StatusBadGateway, codeUnavailable, "the recording server doesn't have this camera yet — try again in a moment"}
	case errors.Is(err, context.DeadlineExceeded):
		return &encoderErr{http.StatusGatewayTimeout, codeTimeout, "the camera took too long to answer"}
	}
	return &encoderErr{http.StatusBadGateway, codeUnavailable, err.Error()}
}

func writeEncoderErr(w http.ResponseWriter, e *encoderErr) { writeV2Error(w, e.status, e.code, e.msg) }

// streams asks the camera for its encoder configurations and ranges.
func (s *Server) encoderStreams(ctx context.Context, t *encoderTarget) ([]onvif.EncoderStream, *encoderErr) {
	ctx, cancel := context.WithTimeout(ctx, probeCmdTimeout)
	defer cancel()
	if t.local {
		streams, err := onvif.GetEncoders(ctx, t.cam.ONVIFURL, t.cam.Username, t.pass, t.cam.RTSPURL, t.cam.SubRTSPURL)
		if err != nil {
			return nil, cameraFailure(err.Error(), errors.Is(err, onvif.ErrAuth))
		}
		return streams, nil
	}
	var res link.EncoderGetResult
	if err := s.command(ctx, t.recorder, link.CmdEncoderGet, link.EncoderGetArgs{CameraID: t.cam.ID}, &res); err != nil {
		return nil, linkFailure(err)
	}
	if res.Error != "" {
		return nil, cameraFailure(res.Error, res.AuthFailed)
	}
	var streams []onvif.EncoderStream
	if err := json.Unmarshal(res.Streams, &streams); err != nil {
		return nil, &encoderErr{http.StatusBadGateway, codeUnavailable, "the recording server sent an unreadable answer"}
	}
	return streams, nil
}

func (s *Server) handleV2StreamSettings(w http.ResponseWriter, r *http.Request) {
	t := s.encoderTarget(w, r)
	if t == nil {
		return
	}
	streams, e := s.encoderStreams(r.Context(), t)
	if e != nil {
		writeEncoderErr(w, e)
		return
	}
	out := make([]v2Stream, 0, len(streams))
	for _, st := range streams {
		out = append(out, toV2Stream(st))
	}
	writeJSON(w, http.StatusOK, map[string]any{"streams": out})
}

type v2StreamSet struct {
	Width     int `json:"width"`
	Height    int `json:"height"`
	FPS       int `json:"fps"`
	Bitrate   int `json:"bitrate"`
	GovLength int `json:"govLength"`
	Quality   int `json:"quality"`
}

// validateStreamSet checks a change against the camera's own ranges
// (a range whose max is 0 was not reported: unbounded).
func validateStreamSet(in v2StreamSet, cur onvif.EncoderStream) error {
	o := cur.Options
	for name, v := range map[string]int{"width": in.Width, "height": in.Height, "fps": in.FPS, "bitrate": in.Bitrate, "govLength": in.GovLength, "quality": in.Quality} {
		if v < 0 {
			return fmt.Errorf("%s can't be negative", name)
		}
	}
	if in == (v2StreamSet{}) {
		return errors.New("no changes requested")
	}
	if (in.Width == 0) != (in.Height == 0) {
		return errors.New("set width and height together")
	}
	if in.Width > 0 && len(o.Resolutions) > 0 {
		ok := false
		for _, r := range o.Resolutions {
			ok = ok || (r.Width == in.Width && r.Height == in.Height)
		}
		if !ok {
			return fmt.Errorf("this camera doesn't offer %dx%d on this stream", in.Width, in.Height)
		}
	}
	rng := func(name string, v, lo, hi int) error {
		if v != 0 && hi > 0 && (v < lo || v > hi) {
			return fmt.Errorf("%s must be between %d and %d for this camera", name, lo, hi)
		}
		return nil
	}
	for _, e := range []error{
		rng("frame rate", in.FPS, o.FPSMin, o.FPSMax),
		rng("bitrate", in.Bitrate, o.BitrateMin, o.BitrateMax),
		rng("keyframe interval", in.GovLength, o.GovMin, o.GovMax),
		rng("quality", in.Quality, o.QualityMin, o.QualityMax),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) handleV2SetStreamSettings(w http.ResponseWriter, r *http.Request) {
	var in v2StreamSet
	if !readV2JSON(w, r, &in) {
		return
	}
	t := s.encoderTarget(w, r)
	if t == nil {
		return
	}
	token := r.PathValue("configToken")
	streams, e := s.encoderStreams(r.Context(), t)
	if e != nil {
		writeEncoderErr(w, e)
		return
	}
	var cur *onvif.EncoderStream
	for i := range streams {
		if streams[i].ConfigToken == token {
			cur = &streams[i]
		}
	}
	if cur == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "this camera has no such stream")
		return
	}
	if err := validateStreamSet(in, *cur); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeCmdTimeout)
	defer cancel()
	if t.local {
		err := onvif.SetEncoder(ctx, t.cam.ONVIFURL, t.cam.Username, t.pass, onvif.EncoderConfig{
			ConfigToken: token, Width: in.Width, Height: in.Height, FPS: in.FPS, Bitrate: in.Bitrate, GovLength: in.GovLength, Quality: in.Quality,
		})
		if err != nil {
			writeEncoderErr(w, cameraFailure(err.Error(), errors.Is(err, onvif.ErrAuth)))
			return
		}
	} else {
		var res link.EncoderSetResult
		args := link.EncoderSetArgs{CameraID: t.cam.ID, ConfigToken: token, Width: in.Width, Height: in.Height, FPS: in.FPS, Bitrate: in.Bitrate, GovLength: in.GovLength, Quality: in.Quality}
		if err := s.command(ctx, t.recorder, link.CmdEncoderSet, args, &res); err != nil {
			writeEncoderErr(w, linkFailure(err))
			return
		}
		if res.Error != "" {
			writeEncoderErr(w, cameraFailure(res.Error, res.AuthFailed))
			return
		}
	}

	s.audit(r, "Cameras", "Changed stream settings", t.cam.Name+" ("+streamLabel(cur)+"): "+describeStreamChange(in, cur))
	if cur.InUse == "main" && in.FPS > 0 && in.FPS != cur.FPS {
		if err := s.Store.SetCameraFPS(r.Context(), t.cam.ID, in.FPS); err != nil {
			slog.Error("camera fps not saved", "camera", t.cam.Name, "err", err)
		}
	}
	// Re-send the recording server its configuration. Ingest reconnects when
	// the camera drops its RTSP session after the change (MediaMTX retries a
	// static source itself); an unchanged config does not restart the path.
	// TODO(stream-settings): verify against real cameras that a mid-stream
	// resolution change never wedges ingest; if it does, add a single-camera
	// restart to the recorder.
	if err := s.ApplyRecorder(r.Context(), t.recorder); err != nil {
		slog.Warn("recorder re-apply after stream settings change failed", "camera", t.cam.Name, "err", err)
	}
	if cams, err := s.Store.Cameras(r.Context()); err == nil {
		s.publishConfigChanged(cams) // admin screens refresh the stored fps
	}
	w.WriteHeader(http.StatusNoContent)
}

func streamLabel(c *onvif.EncoderStream) string {
	switch c.InUse {
	case "main":
		return "main stream"
	case "sub":
		return "sub stream"
	}
	return c.Name
}

// describeStreamChange lists what changed, old → new.
func describeStreamChange(in v2StreamSet, c *onvif.EncoderStream) string {
	var parts []string
	if in.Width > 0 && (in.Width != c.Width || in.Height != c.Height) {
		parts = append(parts, fmt.Sprintf("resolution %dx%d → %dx%d", c.Width, c.Height, in.Width, in.Height))
	}
	add := func(name string, nv, old int, unit string) {
		if nv > 0 && nv != old {
			parts = append(parts, fmt.Sprintf("%s %d → %d%s", name, old, nv, unit))
		}
	}
	add("fps", in.FPS, c.FPS, "")
	add("bitrate", in.Bitrate, c.Bitrate, " kbit/s")
	add("keyframe interval", in.GovLength, c.GovLength, "")
	add("quality", in.Quality, c.Quality, "")
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, ", ")
}
