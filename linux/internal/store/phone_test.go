package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// WAVE23: an old data directory gains the phone-camera and upload columns;
// its cameras are RTSP cameras without a push key.
func TestPhoneCameraColumnsMigrate(t *testing.T) {
	dir := copyFixture(t, "v0")
	st, err := OpenWith(dir, Options{Version: "3.6.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	cams, err := st.Cameras(ctx)
	if err != nil || len(cams) == 0 {
		t.Fatalf("cameras %v %v", cams, err)
	}
	for _, c := range cams {
		if c.Source != "rtsp" || c.HasPushKey || c.IsPhone() {
			t.Fatalf("migrated camera %+v", c)
		}
	}
	for _, col := range []string{"source", "uploaded_by", "device", "recorded_at", "event_id"} {
		var n int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('evidence') WHERE name = ?`, col).Scan(&n); err != nil || n != 1 {
			t.Fatalf("evidence.%s: %d %v", col, n, err)
		}
	}
	if _, err := st.ExpiredUploads(ctx, time.Now().Unix()); err != nil {
		t.Fatalf("evidence_uploads: %v", err)
	}
}

func TestPushKeys(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	phone, err := st.SaveCamera(ctx, "", CameraInput{Name: "Kitchen iPhone", Recording: true, Source: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	rtsp, _ := st.SaveCamera(ctx, "", CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Recording: true})
	if !phone.IsPhone() || phone.HasPushKey || rtsp.IsPhone() {
		t.Fatalf("sources %+v %+v", phone, rtsp)
	}
	key, hash, err := NewPushKey()
	if err != nil || len(key) != 43 || len(hash) != 64 || strings.Contains(hash, key) {
		t.Fatalf("key %q hash %q %v", key, hash, err)
	}
	if err := st.SetCameraPushKey(ctx, phone.ID, hash); err != nil {
		t.Fatal(err)
	}
	// Only the hash is stored.
	var stored string
	_ = st.DB.QueryRow(`SELECT push_key_hash FROM cameras WHERE id = ?`, phone.ID).Scan(&stored)
	if stored != hash || stored == key {
		t.Fatalf("stored %q", stored)
	}
	if ok, err := st.CheckCameraPushKey(ctx, phone.ID, key); !ok || err != nil {
		t.Fatalf("right key: %v %v", ok, err)
	}
	for _, bad := range []string{"", key + "x", key[:42]} {
		if ok, _ := st.CheckCameraPushKey(ctx, phone.ID, bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	// An RTSP camera never has a push key, even if one were stored.
	_ = st.SetCameraPushKey(ctx, rtsp.ID, hash)
	if ok, err := st.CheckCameraPushKey(ctx, rtsp.ID, key); ok || err != ErrNotFound {
		t.Fatalf("rtsp camera: %v %v", ok, err)
	}
	// Edits keep the source; revoking clears the key.
	again, _ := st.SaveCamera(ctx, phone.ID, CameraInput{Name: "Kitchen", Recording: true})
	if !again.IsPhone() || !again.HasPushKey {
		t.Fatalf("after edit %+v", again)
	}
	if err := st.SetCameraPushKey(ctx, phone.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.CheckCameraPushKey(ctx, phone.ID, key); ok {
		t.Fatal("revoked key accepted")
	}
	if err := st.SetCameraPushKey(ctx, "nope", hash); err != ErrNotFound {
		t.Fatalf("unknown camera: %v", err)
	}
}
