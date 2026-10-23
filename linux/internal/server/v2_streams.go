package server

// API v2 (WAVE22 addendum): find a saved camera's second stream without
// knowing its RTSP address, and make a too-small one 1280×720.
//
// Discovery runs where the camera is: in this process for the local
// recorder, else on the owning recording server (link.CmdStreamsDiscover).
// Resizing reuses the stream-settings path (changeStream, CmdEncoderSet),
// so it is validated against the camera's ranges and audited the same way.
// Responses never carry credentials: stream URLs are stripped of user info.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/store"
)

// Camera failure codes on 422 (beside the v2 error shape, authFailed says
// the same thing as camera_auth for clients that only read the flag).
const (
	codeCameraAuth        = "camera_auth"
	codeCameraUnreachable = "camera_unreachable"
)

func (s *Server) registerV2Streams(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/streams/discover", auth.ManageCameras, s.inScope("cameraId", s.handleV2StreamsDiscover))
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/streams/resize", auth.ManageCameras, s.inScope("cameraId", s.handleV2StreamsResize))
}

// streamsLogin is an optional one-off login for discovery.
type streamsLogin struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
}

// streamsFailure is why discovery found nothing: the camera's own failure
// (422) or the control link's (linkErr).
type streamsFailure struct {
	msg        string
	authFailed bool
	linkErr    error
}

func writeStreamsFailure(w http.ResponseWriter, f *streamsFailure) {
	if f.linkErr != nil {
		var ce *link.CommandError
		if errors.As(f.linkErr, &ce) && ce.Code == link.CodeUnknownCommand {
			writeV2Error(w, http.StatusBadGateway, codeUnavailable, "the recording server is too old to find streams — update it")
			return
		}
		writeEncoderErr(w, linkFailure(f.linkErr))
		return
	}
	code, msg := codeCameraUnreachable, f.msg
	if f.authFailed {
		code, msg = codeCameraAuth, "the camera rejected the username or password"
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":      map[string]string{"code": code, "message": msg},
		"authFailed": f.authFailed,
	})
}

// discoverStreams asks the camera (directly or through its recorder) for
// its streams. login, when set, replaces the saved username/password.
func (s *Server) discoverStreams(ctx context.Context, cam *store.Camera, login *streamsLogin) (*onvif.StreamDiscovery, *streamsFailure) {
	rec := cam.RecorderID
	if rec == "" {
		rec = store.LocalRecorderID
	}
	ctx, cancel := context.WithTimeout(ctx, onvif.StreamsDiscoverTimeout+5*time.Second)
	defer cancel()
	if rec != store.LocalRecorderID {
		args := link.StreamsDiscoverArgs{CameraID: cam.ID}
		if login != nil {
			args.Login = true
			args.Username, args.Password = cam.Username, ""
			if login.Username != nil {
				args.Username = *login.Username
			}
			if login.Password != nil {
				args.Password = *login.Password
			} else if pass, err := s.Store.CameraPassword(cam); err == nil {
				args.Password = pass
			}
		}
		var res link.StreamsDiscoverResult
		if err := s.command(ctx, rec, link.CmdStreamsDiscover, args, &res); err != nil {
			return nil, &streamsFailure{linkErr: err}
		}
		if res.Error != "" {
			return nil, &streamsFailure{msg: res.Error, authFailed: res.AuthFailed}
		}
		var d onvif.StreamDiscovery
		if err := json.Unmarshal(res.Discovery, &d); err != nil {
			return nil, &streamsFailure{linkErr: errors.New("the recording server sent an unreadable answer")}
		}
		if d.Streams == nil {
			d.Streams = []onvif.FoundStream{}
		}
		return &d, nil
	}

	pass, err := s.Store.CameraPassword(cam)
	if err != nil {
		slog.Error("camera password unreadable", "camera", cam.Name, "err", err)
		return nil, &streamsFailure{msg: "the camera's password could not be read"}
	}
	t := onvif.StreamTarget{ONVIFURL: cam.ONVIFURL, MainURL: cam.RTSPURL, SubURL: cam.SubRTSPURL, Username: cam.Username, Password: pass}
	if login != nil {
		if login.Username != nil {
			t.Username = *login.Username
		}
		if login.Password != nil {
			t.Password = *login.Password
		}
	}
	d, err := onvif.DiscoverStreams(ctx, t, s.DescribeStream)
	if err != nil {
		return nil, &streamsFailure{msg: err.Error(), authFailed: errors.Is(err, onvif.ErrAuth)}
	}
	return d, nil
}

func (s *Server) handleV2StreamsDiscover(w http.ResponseWriter, r *http.Request) {
	var in streamsLogin
	if r.ContentLength != 0 && !readV2JSON(w, r, &in) {
		return
	}
	cam, err := s.Store.Camera(r.Context(), r.PathValue("cameraId"))
	if err != nil {
		writeV2Err(w, err)
		return
	}
	var login *streamsLogin
	if in.Username != nil || in.Password != nil {
		login = &in
	}
	d, f := s.discoverStreams(r.Context(), cam, login)
	if f != nil {
		if f.authFailed && (cam.RecorderID == "" || cam.RecorderID == store.LocalRecorderID) {
			s.audit(r, "Cameras", "Camera sign-in failed during stream discovery", cam.Name)
		}
		writeStreamsFailure(w, f)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type v2StreamResize struct {
	EncoderToken string `json:"encoderToken"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FPS          int    `json:"fps"`
}

func (s *Server) handleV2StreamsResize(w http.ResponseWriter, r *http.Request) {
	var in v2StreamResize
	if !readV2JSON(w, r, &in) {
		return
	}
	in.EncoderToken = strings.TrimSpace(in.EncoderToken)
	switch {
	case in.EncoderToken == "":
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "encoderToken is required")
		return
	case in.Width <= 0 || in.Height <= 0:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "width and height are required")
		return
	case in.FPS < 0:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "fps can't be negative")
		return
	}
	t := s.encoderTarget(w, r)
	if t == nil {
		return
	}
	prev, ok := s.changeStream(w, r, t, in.EncoderToken, v2StreamSet{Width: in.Width, Height: in.Height, FPS: in.FPS})
	if !ok {
		return
	}

	// Answer with the stream as the camera now reports it; if asking again
	// fails, what was set is still right.
	if d, f := s.discoverStreams(r.Context(), t.cam, nil); f == nil {
		for _, st := range d.Streams {
			if st.EncoderToken == in.EncoderToken {
				writeJSON(w, http.StatusOK, st)
				return
			}
		}
	}
	fps := prev.FPS
	if in.FPS > 0 {
		fps = in.FPS
	}
	codec := onvif.NormalizeCodec(prev.Encoding)
	writeJSON(w, http.StatusOK, onvif.FoundStream{
		Label: onvif.StreamLabel(in.Width, in.Height, codec, float64(fps)), Width: in.Width, Height: in.Height, FPS: float64(fps),
		Codec: codec, Source: onvif.StreamSourceONVIF, Role: prev.InUse, EncoderToken: in.EncoderToken,
	})
}
