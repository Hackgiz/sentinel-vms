package server

import "sync"

// WAVE16 (lead): shared seams for the detection-depth streams, so each stream
// registers its own hook from its own file instead of editing sink.go.

// Settings keys (System → Detection). The recorder gets them through
// link.Snapshot (cameras.go).
const (
	SettingDetectorModel = "detector.model" // models catalog id; "" = yolox-tiny
	SettingDetectRate    = "detector.rate"  // passes per second per camera; "0" = auto
)

var thumbnailHooks struct {
	mu  sync.RWMutex
	fns []func(eventID string)
}

// OnThumbnail registers fn to run (on the sink goroutine, so it must return
// quickly: queue, don't compute) each time an event's thumbnail arrives.
func (s *Server) OnThumbnail(fn func(eventID string)) {
	thumbnailHooks.mu.Lock()
	thumbnailHooks.fns = append(thumbnailHooks.fns, fn)
	thumbnailHooks.mu.Unlock()
}

// thumbnailArrived fans one thumbnail out to appearance search (WAVE9) and
// every registered hook.
func (s *Server) thumbnailArrived(eventID string) {
	s.appearanceOnThumbnail(eventID) // appearance.go (WAVE9(A))
	thumbnailHooks.mu.RLock()
	fns := append([]func(string){}, thumbnailHooks.fns...)
	thumbnailHooks.mu.RUnlock()
	for _, fn := range fns {
		fn(eventID)
	}
}
