package server

// API v2: floorplans (WAVE3-OPS.md, Stream B). Admins upload site maps and
// pin cameras on them; everyone who may view live video reads them. Pins
// follow their camera's scope (scope.go): a pin on a camera the caller may
// not see is left out of the list, but the plan itself is still listed.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/jpeg" // registers the decoders image.DecodeConfig sniffs with
	_ "image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const (
	maxFloorplanImage = 8 << 20
	maxFloorplanName  = 100
	maxFloorplanPins  = 500

	// The upload is multipart, which csrfGuard (JSON only) lets through for
	// bearer calls and for cookie calls carrying this header.
	floorplanUploadPath   = "/api/v2/floorplans"
	floorplanUploadHeader = "X-Sentinel-Upload"
)

func (s *Server) registerV2Floorplans(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/floorplans", auth.ViewLive, s.handleV2Floorplans)
	s.v2Route(mux, "GET /api/v2/floorplans/{planId}/image", auth.ViewLive, s.handleV2FloorplanImage)
	s.v2Route(mux, "POST /api/v2/floorplans", auth.ManageCameras, s.handleV2CreateFloorplan)
	s.v2Route(mux, "PUT /api/v2/floorplans/{planId}", auth.ManageCameras, s.handleV2UpdateFloorplan)
	s.v2Route(mux, "PUT /api/v2/floorplans/{planId}/pins", auth.ManageCameras, s.handleV2SetPins)
	s.v2Route(mux, "DELETE /api/v2/floorplans/{planId}", auth.ManageCameras, s.handleV2DeleteFloorplan)
}

type v2Pin struct {
	CameraID string  `json:"cameraId"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Heading  float64 `json:"heading"`
}

type v2Floorplan struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sortOrder"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Mime      string  `json:"mime"`
	UpdatedAt int64   `json:"updatedAt"`
	Pins      []v2Pin `json:"pins"`
}

// floorplanView is a plan with the pins the caller may see.
func (s *Server) floorplanView(r *http.Request, f *store.Floorplan) (v2Floorplan, error) {
	pins, err := s.Store.Pins(r.Context(), f.ID)
	if err != nil {
		return v2Floorplan{}, err
	}
	sc := s.cameraScope(r.Context())
	out := v2Floorplan{ID: f.ID, Name: f.Name, SortOrder: f.SortOrder, Width: f.Width, Height: f.Height, Mime: f.Mime, UpdatedAt: secToMs(f.UpdatedAt), Pins: []v2Pin{}}
	for _, p := range pins {
		if sc.camera(p.CameraID) {
			out.Pins = append(out.Pins, v2Pin{CameraID: p.CameraID, X: p.X, Y: p.Y, Heading: p.Heading})
		}
	}
	return out, nil
}

func (s *Server) handleV2Floorplans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.Store.Floorplans(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2Floorplan, 0, len(plans))
	for i := range plans {
		v, err := s.floorplanView(r, &plans[i])
		if err != nil {
			writeV2Err(w, err)
			return
		}
		out = append(out, v)
	}
	v2Items(w, out)
}

func (s *Server) handleV2FloorplanImage(w http.ResponseWriter, r *http.Request) {
	f, err := s.Store.FloorplanImage(r.Context(), r.PathValue("planId"))
	if err != nil {
		writeV2Fail(w, err, "floorplan")
		return
	}
	etag := `"` + f.ID + "-" + strconv.FormatInt(f.UpdatedAt, 10) + "-" + strconv.Itoa(len(f.Image)) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Image)))
	_, _ = w.Write(f.Image)
}

// sniffFloorplan identifies a PNG, JPEG or WebP image by content and
// returns its MIME type and pixel size.
func sniffFloorplan(b []byte) (mime string, w, h int, err error) {
	if len(b) >= 16 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		w, h, err = webpSize(b)
		return "image/webp", w, h, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", 0, 0, errors.New("that file is not a PNG, JPEG or WebP image")
	}
	switch format {
	case "png":
		mime = "image/png"
	case "jpeg":
		mime = "image/jpeg"
	default:
		return "", 0, 0, errors.New("that file is not a PNG, JPEG or WebP image")
	}
	return mime, cfg.Width, cfg.Height, nil
}

// webpSize reads the canvas size from a WebP's first chunk (VP8, VP8L or VP8X).
func webpSize(b []byte) (w, h int, err error) {
	bad := errors.New("that WebP image is damaged")
	if len(b) < 30 {
		return 0, 0, bad
	}
	switch string(b[12:16]) {
	case "VP8 ": // lossy: frame tag (3), start code 9d 01 2a, then 14-bit sizes
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, bad
		}
		w = int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h = int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
	case "VP8L": // lossless: signature 0x2f, then 14+14 bit (size-1)
		if b[20] != 0x2f {
			return 0, 0, bad
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		w = int(bits&0x3fff) + 1
		h = int((bits>>14)&0x3fff) + 1
	case "VP8X": // extended: 24-bit (size-1)
		w = int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h = int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
	default:
		return 0, 0, bad
	}
	if w <= 0 || h <= 0 {
		return 0, 0, bad
	}
	return w, h, nil
}

func floorplanName(w http.ResponseWriter, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxFloorplanName {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "enter a floorplan name of up to 100 characters")
		return "", false
	}
	return name, true
}

func (s *Server) handleV2CreateFloorplan(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFloorplanImage+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeV2Error(w, http.StatusRequestEntityTooLarge, codeBadRequest, "the image is larger than 8 MB")
			return
		}
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "send multipart form data with name and image")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	name, ok := floorplanName(w, r.FormValue("name"))
	if !ok {
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "attach the floorplan as the image field")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFloorplanImage+1))
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "couldn't read the image")
		return
	}
	if len(data) > maxFloorplanImage {
		writeV2Error(w, http.StatusRequestEntityTooLarge, codeBadRequest, "the image is larger than 8 MB")
		return
	}
	mime, width, height, err := sniffFloorplan(data)
	if err != nil {
		writeV2Error(w, http.StatusUnsupportedMediaType, codeBadRequest, err.Error())
		return
	}
	if width < 1 || height < 1 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "the image has no size")
		return
	}
	plans, err := s.Store.Floorplans(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	order := 0
	for _, p := range plans {
		if p.SortOrder >= order {
			order = p.SortOrder + 1
		}
	}
	f, err := s.Store.SaveFloorplan(r.Context(), store.Floorplan{Name: name, Image: data, Mime: mime, Width: width, Height: height, SortOrder: order})
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Cameras", "Added floorplan", f.Name)
	v, err := s.floorplanView(r, f)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleV2UpdateFloorplan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      *string `json:"name"`
		SortOrder *int    `json:"sortOrder"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	cur, err := s.Store.Floorplan(r.Context(), r.PathValue("planId"))
	if err != nil {
		writeV2Fail(w, err, "floorplan")
		return
	}
	if in.Name != nil {
		name, ok := floorplanName(w, *in.Name)
		if !ok {
			return
		}
		cur.Name = name
	}
	if in.SortOrder != nil {
		cur.SortOrder = *in.SortOrder
	}
	f, err := s.Store.SaveFloorplan(r.Context(), *cur)
	if err != nil {
		writeV2Fail(w, err, "floorplan")
		return
	}
	s.audit(r, "Cameras", "Edited floorplan", f.Name)
	v, err := s.floorplanView(r, f)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleV2SetPins(w http.ResponseWriter, r *http.Request) {
	var in []v2Pin
	if !readV2JSON(w, r, &in) {
		return
	}
	f, err := s.Store.Floorplan(r.Context(), r.PathValue("planId"))
	if err != nil {
		writeV2Fail(w, err, "floorplan")
		return
	}
	if len(in) > maxFloorplanPins {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a floorplan holds up to "+strconv.Itoa(maxFloorplanPins)+" pins")
		return
	}
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	known := map[string]bool{}
	for _, c := range cams {
		known[c.ID] = true
	}
	seen := map[string]bool{}
	pins := make([]store.Pin, 0, len(in))
	for _, p := range in {
		switch {
		case !known[p.CameraID]:
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "no camera "+strconv.Quote(p.CameraID))
			return
		case seen[p.CameraID]:
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a camera can be pinned once per floorplan")
			return
		case !inUnit(p.X) || !inUnit(p.Y):
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "x and y are fractions of the image, from 0 to 1")
			return
		case p.Heading != -1 && (math.IsNaN(p.Heading) || p.Heading < 0 || p.Heading >= 360):
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "heading is degrees from 0 to 359, or -1 for none")
			return
		}
		seen[p.CameraID] = true
		pins = append(pins, store.Pin{CameraID: p.CameraID, X: p.X, Y: p.Y, Heading: p.Heading})
	}
	if err := s.Store.SetPins(r.Context(), f.ID, pins); err != nil {
		writeV2Fail(w, err, "floorplan")
		return
	}
	s.audit(r, "Cameras", "Edited floorplan pins", f.Name+" ("+strconv.Itoa(len(pins))+" cameras)")
	f, _ = s.Store.Floorplan(r.Context(), f.ID)
	v, err := s.floorplanView(r, f)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func inUnit(v float64) bool { return v >= 0 && v <= 1 } // false for NaN

func (s *Server) handleV2DeleteFloorplan(w http.ResponseWriter, r *http.Request) {
	f, err := s.Store.Floorplan(r.Context(), r.PathValue("planId"))
	if err == nil {
		err = s.Store.DeleteFloorplan(r.Context(), f.ID)
	}
	if err != nil {
		writeV2Fail(w, err, "floorplan")
		return
	}
	s.audit(r, "Cameras", "Deleted floorplan", f.Name)
	w.WriteHeader(http.StatusNoContent)
}
