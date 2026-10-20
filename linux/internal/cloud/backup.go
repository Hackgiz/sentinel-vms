package cloud

// Offsite backup: every new alarm's alarm.json and snapshot, and the clip
// once evidence is locked for it, uploaded to Sentinel Cloud storage.
//
// Object keys (relative to this server; the cloud prefixes the account):
//
//	alarms/<alarmId>/snapshot.jpg  the detection snapshot (image/jpeg)
//	alarms/<alarmId>/clip.mp4      the locked evidence clip (video/mp4)
//	alarms/<alarmId>/alarm.json    the alarm as API v2 shows it (V2.Alarm),
//	                               plus "snapshotKey" and "clipKey" naming
//	                               the objects above when they exist, and
//	                               "backedUpAt" (RFC 3339)
//
// alarm.json is written last, so its presence means the objects it names
// are there; when a clip arrives later it is rewritten (delete + put).
// Every object carries X-Sentinel-Meta with alarmId, cameraId, cameraName,
// kind, severity, title and createdAt (Unix ms), so a listing can show the
// alarm without fetching alarm.json.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BackupSource is what the uploader reads from the Management Server.
type BackupSource interface {
	// LatestAlarmID is the newest alarm's id ("" when there is none).
	LatestAlarmID(ctx context.Context) (string, error)
	// AlarmsAfter lists up to limit alarms raised after the alarm afterID
	// ("-" = from the start), oldest first, none raised before since. An
	// unknown afterID counts as "-".
	AlarmsAfter(ctx context.Context, afterID string, since time.Time, limit int) ([]BackupAlarm, error)
	// Alarm returns one alarm.
	Alarm(ctx context.Context, id string) (BackupAlarm, error)
	// Snapshot is the alarm's detection snapshot (JPEG): ErrNoSnapshot when
	// it has none, ErrSnapshotPending while it is still being written.
	Snapshot(ctx context.Context, alarmID string) ([]byte, error)
	// ClipsAfter lists up to limit evidence clips of alarms locked after
	// (lockedAt, afterID), oldest first.
	ClipsAfter(ctx context.Context, lockedAt time.Time, afterID string, limit int) ([]BackupClip, error)
	// OpenClip opens a clip's file (BackupClip.Size bytes).
	OpenClip(ctx context.Context, c BackupClip) (io.ReadCloser, error)
}

// BackupAlarm is one alarm to back up.
type BackupAlarm struct {
	ID        string
	CreatedAt time.Time
	Record    json.RawMessage // the alarm as API v2 shows it (V2.Alarm)
	Meta      map[string]any  // short fields for X-Sentinel-Meta
}

// BackupClip is a locked evidence clip of an alarm.
type BackupClip struct {
	EvidenceID string
	AlarmID    string
	LockedAt   time.Time
	Size       int64
}

var (
	ErrNoSnapshot      = errors.New("the alarm has no snapshot")
	ErrSnapshotPending = errors.New("the snapshot is still being written")
)

const (
	// maxObject: the contract allows 512 MiB per object, but Cloudflare caps
	// a request body at 100 MB on Sentinel Cloud's plan; larger clips are
	// skipped (no chunking yet).
	maxObject       = 100_000_000
	maxMeta         = 2 << 10
	snapshotWait    = 2 * time.Minute // how long a new alarm may wait for its snapshot
	defaultDays     = 30
	backupBatch     = 20
	defaultPoll     = time.Minute
	defaultUsage    = time.Hour
	defaultDebounce = 5 * time.Second
	retryMin        = 5 * time.Second
	retryMax        = 5 * time.Minute
)

// Backup uploads alarms as they are raised. Run it while backup applies.
type Backup struct {
	BaseURL        string // https://cloud.sentvms.com/v1/backup
	Token          string // scs_…
	Store          Store
	Source         BackupSource
	HTTP           *http.Client // nil = default (no overall timeout: clips are large)
	Days           int          // quotas.backupDays: how far back catch-up reaches
	OnUnauthorized func()       // a 401: the caller checks with the control plane
	Now            func() time.Time

	Debounce   time.Duration // after a new alarm, wait for its snapshot (5 s)
	Poll       time.Duration // rescan for evidence locks and retries (1 min)
	UsageEvery time.Duration // GET /usage (1 h)
	RetryMin   time.Duration // after a transient failure (5 s, doubling)
	RetryMax   time.Duration // (5 min)

	once sync.Once
	wake chan struct{}

	mu     sync.Mutex
	status BackupStatus
	note   string // a clip that was skipped, shown until a clip uploads
	full   bool   // a 413: wait for room
	quota  int64
}

// errors that end a pass
var (
	errQuota    = errors.New("offsite backup is full")
	errAuth     = errors.New("Sentinel Cloud did not accept this server's link")
	errInactive = errors.New("offsite backup isn't active for this server")
)

// retryable marks a transient failure: try again with backoff.
type retryable struct{ err error }

func (e retryable) Error() string { return e.err.Error() }
func (e retryable) Unwrap() error { return e.err }

func (b *Backup) init() { b.once.Do(func() { b.wake = make(chan struct{}, 1) }) }

func (b *Backup) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Notify says a new alarm was raised.
func (b *Backup) Notify() {
	b.init()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Status is the uploader's part of CloudStatus.backup.
func (b *Backup) Status() BackupStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.status
	if s.Error == "" {
		s.Error = b.note
	}
	return s
}

// restore carries usage numbers over from a previous uploader.
func (b *Backup) restore(s BackupStatus) {
	b.mu.Lock()
	b.status.BytesUsed, b.status.Objects, b.status.LastUploadAt = s.BytesUsed, s.Objects, s.LastUploadAt
	b.mu.Unlock()
}

func (b *Backup) setError(msg string) {
	b.mu.Lock()
	b.status.Error = msg
	b.mu.Unlock()
}

// Run uploads until ctx ends.
func (b *Backup) Run(ctx context.Context) {
	b.init()
	poll := time.NewTicker(dur(b.Poll, defaultPoll))
	defer poll.Stop()
	usage := time.NewTicker(dur(b.UsageEvery, defaultUsage))
	defer usage.Stop()
	b.refreshUsage(ctx)
	retry := time.Duration(0)
	for ctx.Err() == nil {
		b.mu.Lock()
		full := b.full
		b.mu.Unlock()
		if !full {
			err := b.pass(ctx)
			var rt retryable
			switch {
			case err == nil:
				retry = 0
				b.mu.Lock()
				if !b.full {
					b.status.Error = ""
				}
				b.mu.Unlock()
			case errors.As(err, &rt):
				retry = min(max(retry*2, dur(b.RetryMin, retryMin)), dur(b.RetryMax, retryMax))
				b.setError("Couldn't upload to Sentinel Cloud; retrying: " + err.Error())
				slog.Warn("sentinel cloud backup: retrying", "in", retry, "err", err)
				if !sleep(ctx, retry) {
					return
				}
				continue
			case errors.Is(err, errQuota):
				// setError done by put
			default:
				b.setError(err.Error())
				slog.Warn("sentinel cloud backup stopped until the next check", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-b.wake:
			if !sleep(ctx, dur(b.Debounce, defaultDebounce)) {
				return
			}
		case <-poll.C:
		case <-usage.C:
			b.refreshUsage(ctx)
		}
	}
}

// pass uploads every alarm and clip not yet uploaded.
func (b *Backup) pass(ctx context.Context) error {
	if err := b.alarms(ctx); err != nil {
		return err
	}
	return b.clips(ctx)
}

func (b *Backup) days() int {
	if b.Days > 0 {
		return b.Days
	}
	return defaultDays
}

func (b *Backup) alarms(ctx context.Context) error {
	cursor := b.Store.Setting(ctx, KeyBackupCursor, "")
	if cursor == "" { // first run: start with the next alarm
		id, err := b.Source.LatestAlarmID(ctx)
		if err != nil {
			return retryable{err}
		}
		if id == "" {
			id = "-"
		}
		if err := b.Store.SetSetting(ctx, KeyBackupCursor, id); err != nil {
			return retryable{err}
		}
		cursor = id
	}
	since := b.now().Add(-time.Duration(b.days()) * 24 * time.Hour)
	for {
		list, err := b.Source.AlarmsAfter(ctx, cursor, since, backupBatch)
		if err != nil {
			return retryable{err}
		}
		if len(list) == 0 {
			return nil
		}
		for _, a := range list {
			done, err := b.uploadAlarm(ctx, a, "")
			if err != nil {
				return err
			}
			if !done {
				return nil // its snapshot isn't written yet: next pass
			}
			cursor = a.ID
			if err := b.Store.SetSetting(ctx, KeyBackupCursor, cursor); err != nil {
				return retryable{err}
			}
		}
		if len(list) < backupBatch {
			return nil
		}
	}
}

// AlarmKey is the object key of one of an alarm's files.
func AlarmKey(alarmID, name string) string { return "alarms/" + alarmID + "/" + name }

// uploadAlarm uploads the snapshot (if any) and then alarm.json. done is
// false when a young alarm's snapshot is still being written. clipKey is
// set when the clip is already uploaded; alarm.json is then rewritten.
func (b *Backup) uploadAlarm(ctx context.Context, a BackupAlarm, clipKey string) (done bool, err error) {
	meta := metaHeader(a)
	snapshotKey := ""
	snap, serr := b.Source.Snapshot(ctx, a.ID)
	young := b.now().Sub(a.CreatedAt) < snapshotWait
	switch {
	case serr == nil:
		key := AlarmKey(a.ID, "snapshot.jpg")
		if err := b.put(ctx, key, "image/jpeg", bytes.NewReader(snap), int64(len(snap)), meta); err != nil {
			return false, err
		}
		snapshotKey = key
	case errors.Is(serr, ErrNoSnapshot):
	case clipKey != "":
		// rewriting after a clip: the snapshot may have been uploaded before
		if key := AlarmKey(a.ID, "snapshot.jpg"); b.exists(ctx, key) {
			snapshotKey = key
		}
	case young:
		return false, nil // pending, or the recording server is briefly away
	default:
		slog.Info("sentinel cloud backup: alarm backed up without its snapshot", "alarm", a.ID, "err", serr)
	}
	doc, err := alarmDocument(a, snapshotKey, clipKey, b.now())
	if err != nil {
		return true, nil // unreadable record: skip it
	}
	key := AlarmKey(a.ID, "alarm.json")
	if clipKey != "" {
		if err := b.delete(ctx, key); err != nil {
			return false, err
		}
	}
	if err := b.put(ctx, key, "application/json", bytes.NewReader(doc), int64(len(doc)), meta); err != nil {
		return false, err
	}
	return true, nil
}

// alarmDocument is alarm.json: the v2 record plus the object keys.
func alarmDocument(a BackupAlarm, snapshotKey, clipKey string, now time.Time) ([]byte, error) {
	m := map[string]any{}
	if len(a.Record) > 0 {
		if err := json.Unmarshal(a.Record, &m); err != nil {
			return nil, err
		}
	}
	if m["id"] == nil {
		m["id"] = a.ID
	}
	if snapshotKey != "" {
		m["snapshotKey"] = snapshotKey
	}
	if clipKey != "" {
		m["clipKey"] = clipKey
	}
	m["backedUpAt"] = formatTime(now)
	return json.Marshal(m)
}

// metaHeader is X-Sentinel-Meta (≤ 2 KiB of JSON).
func metaHeader(a BackupAlarm) string {
	m := map[string]any{"alarmId": a.ID, "createdAt": a.CreatedAt.UnixMilli()}
	for k, v := range a.Meta {
		if s, ok := v.(string); ok && len(s) > 200 {
			v = s[:200]
		}
		m[k] = v
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > maxMeta {
		raw, _ = json.Marshal(map[string]any{"alarmId": a.ID, "createdAt": a.CreatedAt.UnixMilli()})
	}
	return string(raw)
}

// clips uploads evidence clips locked since the clip cursor.
func (b *Backup) clips(ctx context.Context) error {
	cur := b.Store.Setting(ctx, KeyBackupClipCursor, "")
	if cur == "" { // first run: clips locked from now on
		cur = clipCursor(b.now(), "")
		if err := b.Store.SetSetting(ctx, KeyBackupClipCursor, cur); err != nil {
			return retryable{err}
		}
	}
	after, afterID := parseClipCursor(cur)
	list, err := b.Source.ClipsAfter(ctx, after, afterID, backupBatch)
	if err != nil {
		return retryable{err}
	}
	for _, c := range list {
		if err := b.uploadClip(ctx, c); err != nil {
			return err
		}
		if err := b.Store.SetSetting(ctx, KeyBackupClipCursor, clipCursor(c.LockedAt, c.EvidenceID)); err != nil {
			return retryable{err}
		}
	}
	return nil
}

func (b *Backup) uploadClip(ctx context.Context, c BackupClip) error {
	if c.Size <= 0 {
		return nil
	}
	if c.Size > maxObject {
		slog.Warn("sentinel cloud backup: clip too large; skipped", "alarm", c.AlarmID, "bytes", c.Size)
		b.mu.Lock()
		b.note = fmt.Sprintf("A locked clip of %d MB was too large for offsite backup (the limit is 100 MB) and was skipped.", c.Size/1_000_000)
		b.mu.Unlock()
		return nil
	}
	a, err := b.Source.Alarm(ctx, c.AlarmID)
	if err != nil {
		return nil // the alarm is gone: nothing to attach the clip to
	}
	rc, err := b.Source.OpenClip(ctx, c)
	if err != nil {
		return retryable{err}
	}
	key := AlarmKey(c.AlarmID, "clip.mp4")
	err = b.put(ctx, key, "video/mp4", rc, c.Size, metaHeader(a))
	rc.Close()
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.note = ""
	b.mu.Unlock()
	if _, err := b.uploadAlarm(ctx, a, key); err != nil {
		return err
	}
	return nil
}

func clipCursor(t time.Time, id string) string { return strconv.FormatInt(t.Unix(), 10) + ":" + id }

func parseClipCursor(s string) (time.Time, string) {
	sec, id, _ := strings.Cut(s, ":")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}, ""
	}
	return time.Unix(n, 0), id
}

// MARK: - HTTP

func (b *Backup) client() *http.Client {
	if b.HTTP != nil {
		return b.HTTP
	}
	return http.DefaultClient
}

func (b *Backup) objectURL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.TrimRight(b.BaseURL, "/") + "/objects/" + strings.Join(parts, "/")
}

// status maps an error answer: errAuth, errInactive, retryable or a plain
// error (the object is skipped).
func (b *Backup) answer(op string, resp *http.Response) error {
	msg := errorText(resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		if b.OnUnauthorized != nil {
			go b.OnUnauthorized()
		}
		return errAuth
	case resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusForbidden:
		return errInactive
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return retryable{fmt.Errorf("%s: Sentinel Cloud answered %s%s", op, resp.Status, msg)}
	}
	return fmt.Errorf("%s: Sentinel Cloud answered %s%s", op, resp.Status, msg)
}

// put uploads one object. An existing key (409) counts as done; a 413
// stops the uploader until /usage shows room.
func (b *Backup) put(ctx context.Context, key, contentType string, body io.Reader, size int64, meta string) error {
	timeout := 2*time.Minute + time.Duration(size/(64<<10))*time.Second // ≥ 64 KB/s
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPut, b.objectURL(key), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Sentinel-Meta", meta)
	resp, err := b.client().Do(req)
	if err != nil {
		return retryable{fmt.Errorf("upload %s: %w", key, err)}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var out struct {
			BytesUsed  int64 `json:"bytesUsed"`
			BytesQuota int64 `json:"bytesQuota"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
		now := formatTime(b.now())
		b.mu.Lock()
		if out.BytesUsed > 0 {
			b.status.BytesUsed = out.BytesUsed
		}
		if out.BytesQuota > 0 {
			b.quota = out.BytesQuota
		}
		b.status.Objects++
		b.status.LastUploadAt = &now
		b.mu.Unlock()
		return nil
	case http.StatusConflict:
		return nil
	case http.StatusRequestEntityTooLarge:
		b.mu.Lock()
		b.full = true
		b.status.Error = "Offsite backup is full. New alarms aren't backed up until older ones expire or the plan has more room."
		b.mu.Unlock()
		slog.Warn("sentinel cloud backup: storage full; paused")
		return errQuota
	}
	err = b.answer("upload "+key, resp)
	var rt retryable
	if !errors.As(err, &rt) && !errors.Is(err, errAuth) && !errors.Is(err, errInactive) {
		slog.Warn("sentinel cloud backup: object skipped", "key", key, "err", err)
		return nil
	}
	return err
}

func (b *Backup) delete(ctx context.Context, key string) error {
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodDelete, b.objectURL(key), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	resp, err := b.client().Do(req)
	if err != nil {
		return retryable{fmt.Errorf("delete %s: %w", key, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	err = b.answer("delete "+key, resp)
	var rt retryable
	if errors.As(err, &rt) || errors.Is(err, errAuth) || errors.Is(err, errInactive) {
		return err
	}
	return nil
}

// exists checks an object with a one-byte ranged GET.
func (b *Backup) exists(ctx context.Context, key string) bool {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, b.objectURL(key), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := b.client().Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent
}

// usage is GET /usage.
type usage struct {
	BytesUsed  int64  `json:"bytesUsed"`
	Objects    int64  `json:"objects"`
	BytesQuota int64  `json:"bytesQuota"`
	Days       int    `json:"days"`
	OldestAt   string `json:"oldestAt"`
}

// refreshUsage updates the numbers and resumes after a 413 once there is
// room again.
func (b *Backup) refreshUsage(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, strings.TrimRight(b.BaseURL, "/")+"/usage", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	resp, err := b.client().Do(req)
	if err != nil {
		slog.Debug("sentinel cloud backup: usage", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = b.answer("usage", resp)
		return
	}
	var u usage
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&u) != nil {
		return
	}
	b.mu.Lock()
	b.status.BytesUsed, b.status.Objects = u.BytesUsed, u.Objects
	if u.BytesQuota > 0 {
		b.quota = u.BytesQuota
	}
	if b.full && b.quota > 0 && u.BytesUsed < b.quota-roomNeeded {
		b.full, b.status.Error = false, ""
	}
	b.mu.Unlock()
}

// roomNeeded: free space before uploads resume after a 413.
const roomNeeded = 64 << 20
