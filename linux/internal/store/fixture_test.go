package store

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testdata/v0 is a data directory written by the 0.x store (before the
// platform split, PRAGMA user_version 0): two users, a session, two cameras
// (one with a sealed password, sub-stream, ONVIF, custom motion/alert
// settings), settings including a sealed MQTT password, a paired iPhone with
// a push token, an acknowledged alarm, two events (one with thumbnail, AI
// description and alarm link), evidence, an alarm rule and an audit chain.

const (
	fixtureCamera = "07ACA52B-E211-4DD7-BCD0-1CC4B1BDD258"
	fixtureEvent  = "9D9DE76C-F4EB-4F5D-B47C-BEFAA494E8E5"
	fixtureAlarm  = "2CBD2288-BD05-41D2-BA56-FC7F9F47B853"
)

// copyFixture copies testdata/<name> into a fresh temp dir.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", name)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(filepath.Join(dst, e.Name()), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		in.Close()
		out.Close()
	}
	return dst
}

// checkFixtureData asserts everything written by the 0.x store still loads.
func checkFixtureData(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	if n, err := st.UserCount(ctx); err != nil || n != 2 {
		t.Fatalf("users = %d, %v", n, err)
	}
	if u, err := st.UserByName(ctx, "ADMIN"); err != nil || u.Role != "Admin" {
		t.Fatalf("admin = %+v, %v", u, err)
	}
	cams, err := st.Cameras(ctx)
	if err != nil || len(cams) != 2 {
		t.Fatalf("cameras = %d, %v", len(cams), err)
	}
	c := cams[0]
	if c.ID != fixtureCamera || c.Name != "Front Door" || c.SubRTSPURL != "rtsp://10.0.0.9:554/stream2" || !c.Recording ||
		c.RetentionDays != 14 || c.FPS != 15 || c.MotionLevel != 3 || c.AlertOn != 2 || c.ONVIFURL == "" || !c.CameraAnalytics || !c.HasPassword {
		t.Fatalf("camera = %+v", c)
	}
	if pw, err := st.CameraPassword(&c); err != nil || pw != "cam-secret" {
		t.Fatalf("camera password = %q, %v", pw, err)
	}
	if cams[1].Name != "Garage" || cams[1].MotionLevel != 2 || cams[1].HasPassword {
		t.Fatalf("second camera = %+v", cams[1])
	}
	if v := st.Setting(ctx, "default_retention_days", ""); v != "30" {
		t.Fatalf("retention = %q", v)
	}
	if pw, err := st.Unseal(st.Setting(ctx, "mqtt_password", "")); err != nil || pw != "hunter2" {
		t.Fatalf("mqtt password = %q, %v", pw, err)
	}
	devs, err := st.PairedDevices(ctx)
	if err != nil || len(devs) != 1 || !devs[0].HasPush {
		t.Fatalf("devices = %+v, %v", devs, err)
	}
	if toks, _ := st.PushTokens(ctx); len(toks) != 1 {
		t.Fatalf("push tokens = %v", toks)
	}
	a, err := st.Alarm(ctx, fixtureAlarm)
	if err != nil || a.State != "Acknowledged" || a.Owner != "admin" || a.EventID != fixtureEvent || len(a.ResponseLog) != 2 {
		t.Fatalf("alarm = %+v, %v", a, err)
	}
	events, err := st.Events(ctx, "", 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	e, err := st.Event(ctx, fixtureEvent)
	if err != nil || !e.HasThumb || e.Thumbnail != fixtureEvent+".jpg" || e.AlarmID != fixtureAlarm || e.Description != "A person at the door." || e.Tags != "person, door" || e.Label != "person" {
		t.Fatalf("event = %+v, %v", e, err)
	}
	if ev, err := st.EvidenceForAlarm(ctx, fixtureAlarm); err != nil || ev.CaseID != "HG-20261010-00001" || ev.File != "HG-20261010-00001.mp4" {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}
	if rs, err := st.AlarmRules(ctx); err != nil || len(rs) != 1 || rs[0].Name != "Dock" {
		t.Fatalf("rules = %+v, %v", rs, err)
	}
	if checked, broken, err := st.VerifyAudit(ctx); err != nil || broken != "" || checked < 2 {
		t.Fatalf("audit chain: checked %d broken %q %v", checked, broken, err)
	}
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A 0.x data directory upgrades in place: backup first, then the schema
// moves, and everything still loads.
func TestOpensV0DataDirWithBackup(t *testing.T) {
	dir := copyFixture(t, "v0")
	if v := userVersion(t, filepath.Join(dir, "sentinel.db")); v != 0 {
		t.Fatalf("fixture is at schema %d", v)
	}
	st, err := OpenWith(dir, Options{Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureData(t, st)
	if v := userVersion(t, filepath.Join(dir, "sentinel.db")); v != SchemaVersion {
		t.Fatalf("schema after open = %d", v)
	}
	ctx := context.Background()
	if seen, err := st.LinkMessageSeen(ctx, "x"); err != nil || seen {
		t.Fatalf("link inbox: %v %v", seen, err)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "sentinel.db.bak-1.2.3-*"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode: %v %v", info, err)
	}
	if v := userVersion(t, backups[0]); v != 0 {
		t.Fatalf("backup is at schema %d, want the original 0", v)
	}
	st.Close()

	// The backup is a complete 0.x data directory on its own.
	restored := t.TempDir()
	for _, f := range [][2]string{{backups[0], "sentinel.db"}, {filepath.Join(dir, "install.key"), "install.key"}} {
		b, err := os.ReadFile(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(restored, f[1]), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st2, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureData(t, st2)
	st2.Close()

	// Opening again: already current, no second backup.
	st3, err := OpenWith(dir, Options{Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureData(t, st3)
	st3.Close()
	if again, _ := filepath.Glob(filepath.Join(dir, "sentinel.db.bak-*")); len(again) != 1 {
		t.Fatalf("backups after reopen = %v", again)
	}
}

func TestFreshDataDirNeedsNoBackup(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if b, _ := filepath.Glob(filepath.Join(dir, "*.bak-*")); len(b) != 0 {
		t.Fatalf("backup of an empty database: %v", b)
	}
	if v := userVersion(t, filepath.Join(dir, "sentinel.db")); v != SchemaVersion {
		t.Fatalf("schema = %d", v)
	}
}

func TestIndexEventAndInbox(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	e := &Event{ID: NewID(), CameraID: "c", Kind: "Person", Label: "person", Score: 0.9, Box: "[0,0,1,1]", CreatedAt: 100}
	if err := st.IndexEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	_ = st.SetEventThumbnail(ctx, e.ID, e.ID+".jpg")
	if err := st.IndexEvent(ctx, &Event{ID: e.ID, CameraID: "c", Kind: "Motion"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Event(ctx, e.ID)
	if err != nil || got.Kind != "Person" || !got.HasThumb {
		t.Fatalf("re-indexing changed the event: %+v %v", got, err)
	}
	_ = st.MarkLinkMessage(ctx, "m1")
	_ = st.MarkLinkMessage(ctx, "m1")
	if seen, _ := st.LinkMessageSeen(ctx, "m1"); !seen {
		t.Fatal("not seen")
	}
	_ = st.PruneLinkInbox(ctx, time.Now().Add(time.Hour))
	if seen, _ := st.LinkMessageSeen(ctx, "m1"); seen {
		t.Fatal("not pruned")
	}
}
