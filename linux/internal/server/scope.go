package server

// Camera scope (PLATFORM.md, "Permissions"): a user's role applies to every
// camera or only to the cameras in their groups (store.VisibleCameraIDs);
// Admins see every camera. Every route that names or lists cameras, their
// footage, events, alarms or evidence checks it:
//
//   - A camera outside the caller's scope doesn't exist for them: routes
//     that name one answer 404 (inScope), lists leave it out.
//   - Alarms without a camera (Storage, recording server health) concern
//     the whole site: only users who see every camera see them.
//   - Evidence follows its camera, events and alarms follow theirs.
//   - Paired iPhones act with the scope of the user who paired them
//     (paired_devices.paired_by is that user's name); if that user is gone
//     the phone sees nothing.
//   - The event stream filters each connection's messages (v2_events.go)
//     and recomputes the scope on config.changed and every recheck.
//   - Video tokens are only issued within scope; a token then stands on
//     its own until it expires (recording servers verify it offline).
//
// The scope is worked out once per request: the auth middlewares put a
// scopeHolder in the context (withScope) and s.cameraScope(ctx) fills it on
// first use. Without one (background work) nothing is visible.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"sentinel-linux/internal/store"
)

// cameraScope is the set of cameras someone may see.
type cameraScope struct {
	all bool
	ids map[string]bool
}

var noCameras = &cameraScope{}

// camera reports whether a camera is in scope.
func (c *cameraScope) camera(id string) bool { return c.all || (id != "" && c.ids[id]) }

// alarm reports whether an alarm is in scope; one without a camera only
// for users who see every camera.
func (c *cameraScope) alarm(a *store.Alarm) bool {
	if a.CameraID == "" {
		return c.all
	}
	return c.camera(a.CameraID)
}

type scopeCtxKey int

const scopeKey scopeCtxKey = 0

// scopeHolder caches one request's scope; subject is whose scope it is
// (nil: nobody, sees nothing).
type scopeHolder struct {
	subject *store.User
	once    sync.Once
	scope   *cameraScope
}

// withScope makes ctx carry u's camera scope, worked out on first use.
func withScope(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, scopeKey, &scopeHolder{subject: u})
}

// cameraScope is the scope of the request ctx belongs to.
func (s *Server) cameraScope(ctx context.Context) *cameraScope {
	h, _ := ctx.Value(scopeKey).(*scopeHolder)
	if h == nil {
		return noCameras
	}
	h.once.Do(func() { h.scope = s.scopeOf(ctx, h.subject) })
	return h.scope
}

// scopeOf reads u's scope from the store; nil u or a store error sees
// nothing.
func (s *Server) scopeOf(ctx context.Context, u *store.User) *cameraScope {
	if u == nil {
		return noCameras
	}
	ids, all, err := s.Store.VisibleCameraIDs(ctx, u)
	if err != nil {
		slog.Error("camera scope unavailable", "user", u.Name, "err", err)
		return noCameras
	}
	return &cameraScope{all: all, ids: ids}
}

// deviceUser is the user a paired iPhone acts for, or nil when they are gone.
func (s *Server) deviceUser(ctx context.Context, dev *store.PairedDevice) *store.User {
	u, err := s.Store.UserByName(ctx, dev.PairedBy)
	if err != nil {
		return nil
	}
	return u
}

// inScope wraps a handler whose path names a camera in param: a camera the
// caller may not see answers 404, as if it didn't exist.
func (s *Server) inScope(param string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cameraScope(r.Context()).camera(r.PathValue(param)) {
			writeScopeNotFound(w, r)
			return
		}
		h(w, r)
	}
}

// phoneInScope is inScope for the iPhone API ({id} names the camera).
func (s *Server) phoneInScope(h phoneHandler) phoneHandler {
	return func(w http.ResponseWriter, r *http.Request, dev *store.PairedDevice) {
		if !s.cameraScope(r.Context()).camera(r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		h(w, r, dev)
	}
}

func writeScopeNotFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/v2/") {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "camera not found")
		return
	}
	writeError(w, http.StatusNotFound, "camera not found")
}

// scopedAlarm reads an alarm the caller may see (store.ErrNotFound otherwise).
func (s *Server) scopedAlarm(ctx context.Context, id string) (*store.Alarm, error) {
	a, err := s.Store.Alarm(ctx, id)
	if err == nil && !s.cameraScope(ctx).alarm(a) {
		return nil, store.ErrNotFound
	}
	return a, err
}

// scopedEvent reads an event the caller may see (store.ErrNotFound otherwise).
func (s *Server) scopedEvent(ctx context.Context, id string) (*store.Event, error) {
	e, err := s.Store.Event(ctx, id)
	if err == nil && !s.cameraScope(ctx).camera(e.CameraID) {
		return nil, store.ErrNotFound
	}
	return e, err
}

// keepVisible filters a list in place to what the scope allows.
func keepVisible[T any](list []T, ok func(*T) bool) []T {
	out := list[:0]
	for i := range list {
		if ok(&list[i]) {
			out = append(out, list[i])
		}
	}
	return out
}

// visibleCameras leaves out the cameras the caller may not see.
func (s *Server) visibleCameras(ctx context.Context, cams []store.Camera) []store.Camera {
	sc := s.cameraScope(ctx)
	return keepVisible(cams, func(c *store.Camera) bool { return sc.camera(c.ID) })
}

// visibleEvents leaves out events on cameras the caller may not see.
func (s *Server) visibleEvents(ctx context.Context, events []store.Event) []store.Event {
	sc := s.cameraScope(ctx)
	return keepVisible(events, func(e *store.Event) bool { return sc.camera(e.CameraID) })
}

// visibleAlarms leaves out alarms the caller may not see.
func (s *Server) visibleAlarms(ctx context.Context, alarms []store.Alarm) []store.Alarm {
	return keepVisible(alarms, s.cameraScope(ctx).alarm)
}

// scopedRows reads up to want rows of a keyset-paged list that pass ok,
// asking the store in batches until it has them or the list ends. With the
// whole site in scope it is a single read.
func scopedRows[T any](sc *cameraScope, after store.PageKey, want int, fetch func(after store.PageKey, n int) ([]T, error),
	key func(*T) store.PageKey, ok func(*T) bool) ([]T, error) {
	if sc.all {
		return fetch(after, want)
	}
	out := []T{}
	if len(sc.ids) == 0 {
		return out, nil
	}
	batch := max(want, 200)
	for {
		rows, err := fetch(after, batch)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if ok(&rows[i]) {
				if out = append(out, rows[i]); len(out) == want {
					return out, nil
				}
			}
		}
		if len(rows) < batch {
			return out, nil
		}
		after = key(&rows[len(rows)-1])
	}
}

// pushTokensFor lists the push tokens of the paired iPhones whose user may
// see the alarm.
func (s *Server) pushTokensFor(ctx context.Context, a *store.Alarm) ([]string, error) {
	devs, err := s.Store.PairedDevices(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range devs {
		if t := devs[i].APNSToken(); t != "" && s.scopeOf(ctx, s.deviceUser(ctx, &devs[i])).alarm(a) {
			out = append(out, t)
		}
	}
	return out, nil
}

// scopeChanged tells clients (and the event stream's per-connection
// filters) that who sees which camera changed: config.changed with every
// camera.
func (s *Server) scopeChanged(ctx context.Context) {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		slog.Warn("announcing a scope change failed", "err", err)
		return
	}
	s.publishConfigChanged(cams)
}
