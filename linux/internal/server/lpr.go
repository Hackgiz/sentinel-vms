package server

// Licence plate reading (WAVE16-DETECTION.md, stream D). With the OCR models
// loaded (cmd/sentinel/lpr.go) and plate reading switched on (setting
// lpr.enabled, off by default), every Vehicle event whose thumbnail arrives
// is read: the detection box (padded 10 %) is cropped out of the thumbnail,
// the plate goes on the event, and a watchlist match acts on the event's
// alarm (default: the name goes into its detail; never: the alarm is closed
// as a known vehicle; always: a Critical Watchlist alarm is raised).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"math"
	"sync"
	"time"

	"sentinel-linux/internal/lpr"
	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/store"
)

// SettingLPREnabled switches plate reading ("1" on; anything else off).
const SettingLPREnabled = "lpr.enabled"

const (
	plateQueueLen  = 128     // pending reads; the oldest is dropped when full
	plateThumbMax  = 4 << 20 // biggest thumbnail fetched
	plateFetchWait = 20 * time.Second
	platePad       = 0.10 // the box grows by this share of its size on each side
	plateFuzzyMin  = 6    // reads this long match a watchlist plate one edit away
)

type lprState struct {
	mu      sync.Mutex
	reader  lpr.Reader
	reason  string // why plate reading is unavailable ("" with models)
	queue   chan string
	started bool

	// thumb fetches an event's thumbnail JPEG; nil = s.eventThumbnail
	// (tests replace it). done, when set, is told about each event the
	// worker finished with (tests wait on it).
	thumb func(ctx context.Context, e *store.Event) ([]byte, bool)
	done  func(eventID string)
}

// lprStates holds each Server's plate state (kept outside the Server struct
// so this stream owns all of it).
var lprStates sync.Map // *Server → *lprState

func (s *Server) lprState() *lprState {
	if st, ok := lprStates.Load(s); ok {
		return st.(*lprState)
	}
	st, _ := lprStates.LoadOrStore(s, &lprState{})
	return st.(*lprState)
}

// SetPlateReader installs the plate models (nil = unavailable, with reason)
// and starts the read queue the first time models arrive.
func (s *Server) SetPlateReader(r lpr.Reader, reason string) {
	st := s.lprState()
	st.mu.Lock()
	st.reader, st.reason = r, reason
	if r == nil && st.reason == "" {
		st.reason = "no plate models installed"
	}
	start := r != nil && !st.started
	if start {
		st.started = true
		st.queue = make(chan string, plateQueueLen)
	}
	q := st.queue
	st.mu.Unlock()
	if start {
		s.OnThumbnail(s.plateOnThumbnail)
		go s.runPlateQueue(s.ctxOrBackground(), q)
	}
}

// plateReader returns the reader, or nil and the reason.
func (s *Server) plateReader() (lpr.Reader, string) {
	st := s.lprState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.reader == nil {
		if st.reason == "" {
			return nil, "no plate models installed"
		}
		return nil, st.reason
	}
	return st.reader, ""
}

func (s *Server) lprEnabled(ctx context.Context) bool {
	return s.Store.Setting(ctx, SettingLPREnabled, "0") == "1"
}

// plateOnThumbnail queues an event whose thumbnail just arrived (wave16.go
// hook, on the sink goroutine): never blocks, drops the oldest when full.
func (s *Server) plateOnThumbnail(eventID string) {
	st := s.lprState()
	st.mu.Lock()
	q := st.queue
	st.mu.Unlock()
	if q == nil {
		return
	}
	for {
		select {
		case q <- eventID:
			return
		default:
		}
		select {
		case <-q: // drop the oldest
		default:
		}
	}
}

func (s *Server) runPlateQueue(ctx context.Context, q <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			if _, err := s.readEventPlate(ctx, id); err != nil && !errors.Is(err, errPlateSkip) {
				slog.Debug("plates: event not read", "event", id, "err", err)
			}
			if done := s.lprState().done; done != nil {
				done(id)
			}
		}
	}
}

// errPlateSkip: the event is not one plate reading looks at (switched off,
// not a vehicle, no box, deleted).
var errPlateSkip = errors.New("not read")

// readEventPlate reads one event's plate, stores it and applies the
// watchlist. It returns the plate ("" when none was read).
func (s *Server) readEventPlate(ctx context.Context, eventID string) (string, error) {
	r, _ := s.plateReader()
	if r == nil || !s.lprEnabled(ctx) {
		return "", errPlateSkip
	}
	e, err := s.Store.Event(ctx, eventID)
	if err != nil || e.Kind != "Vehicle" {
		return "", errPlateSkip
	}
	box, err := reid.ParseBox(e.Box)
	if err != nil {
		return "", errPlateSkip
	}
	if e.Thumbnail == "" {
		return "", errNoThumbnail
	}
	fetch := s.lprState().thumb
	if fetch == nil {
		fetch = func(ctx context.Context, e *store.Event) ([]byte, bool) {
			return s.eventThumbnail(ctx, e, plateThumbMax)
		}
	}
	fctx, cancel := context.WithTimeout(ctx, plateFetchWait)
	jpg, ok := fetch(fctx, e)
	cancel()
	if !ok || len(jpg) == 0 || len(jpg) > plateThumbMax {
		return "", errNoThumbnail
	}
	crop, err := plateCrop(jpg, box)
	if err != nil {
		return "", err
	}
	res, err := r.Read(crop)
	if err != nil {
		return "", fmt.Errorf("plate models: %w", err)
	}
	if res == nil {
		return "", nil
	}
	if err := s.Store.SetEventPlate(ctx, e.ID, res.Text); err != nil {
		return "", err
	}
	e.Plate = res.Text
	s.publishEventUpdated(e.ID)
	s.applyPlateWatch(ctx, e)
	return res.Text, nil
}

// plateCrop decodes the thumbnail and returns the detection box grown by
// platePad on each side.
func plateCrop(jpg []byte, box [4]float64) (image.Image, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, fmt.Errorf("thumbnail: %w", err)
	}
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	pw, ph := (box[2]-box[0])*platePad, (box[3]-box[1])*platePad
	r := image.Rect(int(math.Floor((box[0]-pw)*w)), int(math.Floor((box[1]-ph)*h)),
		int(math.Ceil((box[2]+pw)*w)), int(math.Ceil((box[3]+ph)*h))).Add(b.Min).Intersect(b)
	if r.Dx() < reid.MinCropPx || r.Dy() < reid.MinCropPx {
		return nil, reid.ErrTooSmall
	}
	sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, errors.New("thumbnail format can't be cropped")
	}
	return sub.SubImage(r), nil
}

// matchPlate finds the watchlist entry for a read: exact first, then (for
// reads of at least plateFuzzyMin characters) one edit away.
func matchPlate(list []store.Plate, read string) *store.Plate {
	for i := range list {
		if list[i].Plate == read {
			return &list[i]
		}
	}
	if len(read) < plateFuzzyMin {
		return nil
	}
	for i := range list {
		if levenshtein(list[i].Plate, read) <= 1 {
			return &list[i]
		}
	}
	return nil
}

// levenshtein is the edit distance between two ASCII strings.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// applyPlateWatch acts on a watchlist match for event e (whose Plate is set).
func (s *Server) applyPlateWatch(ctx context.Context, e *store.Event) {
	list, err := s.Store.Plates(ctx)
	if err != nil || len(list) == 0 {
		return
	}
	p := matchPlate(list, e.Plate)
	if p == nil {
		return
	}
	camName := "camera " + e.CameraID
	if c, err := s.Store.Camera(ctx, e.CameraID); err == nil {
		camName = c.Name
	}
	who := p.Name
	if who == "" {
		who = p.Plate
	}
	switch p.Alert {
	case store.PlateAlertNever:
		s.closeKnownVehicleAlarm(ctx, e, p)
	case store.PlateAlertAlways:
		at := time.Unix(e.CreatedAt, 0)
		detail := fmt.Sprintf("%s at %s, %s.", who, camName, at.Format("3:04:05 PM"))
		if p.Name != "" && e.Plate != p.Plate {
			detail = fmt.Sprintf("%s (%s, read %s) at %s, %s.", p.Name, p.Plate, e.Plate, camName, at.Format("3:04:05 PM"))
		} else if p.Name != "" {
			detail = fmt.Sprintf("%s (%s) at %s, %s.", p.Name, p.Plate, camName, at.Format("3:04:05 PM"))
		}
		_, isNew, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: e.CameraID, CameraName: camName, Kind: "Watchlist",
			Title: "Watchlist: " + p.Plate, Severity: "Critical", Detail: detail, EventID: e.ID, At: at,
			MergeWindow: 60 * time.Second, Origin: "Raised by the plate watchlist"})
		if err != nil {
			slog.Error("plates: watchlist alarm", "err", err)
		} else if isNew {
			slog.Info("watchlist plate", "camera", camName, "plate", p.Plate)
		}
	default:
		if e.AlarmID == "" {
			return
		}
		a, err := s.Store.Alarm(ctx, e.AlarmID)
		if err != nil || !store.IsOpenState(a.State) {
			return
		}
		if err := s.Store.UpdateAlarmDetail(ctx, a.ID, fmt.Sprintf("%s at %s", who, camName), ""); err != nil {
			return
		}
		if a, err = s.Store.Alarm(ctx, a.ID); err == nil {
			s.publishAlarmState(a)
		}
	}
}

// closeKnownVehicleAlarm resolves the event's open alarm with the note
// "Known vehicle: <plate>", through the same store call, audit and state
// broadcast an operator's close goes through.
func (s *Server) closeKnownVehicleAlarm(ctx context.Context, e *store.Event, p *store.Plate) {
	if e.AlarmID == "" {
		return
	}
	a, err := s.Store.Alarm(ctx, e.AlarmID)
	if err != nil || !store.IsOpenState(a.State) {
		return
	}
	note := "Known vehicle: " + p.Plate
	if a, err = s.Store.SetAlarmState(ctx, a.ID, "Resolved", "", note); err != nil {
		slog.Error("plates: close known vehicle alarm", "err", err)
		return
	}
	s.scheduleSnoozeEnd(a.ID, 0) // as an operator's close: no snooze end pending
	s.auditSystem("", "Plates", "Set alarm resolved", a.Title+" ("+a.CameraName+"): "+note)
	s.publishAlarmState(a)
}
