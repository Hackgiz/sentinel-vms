package store

import (
	"context"
	"testing"
)

// WAVE24 D1: old 0 meant "server default" and becomes NULL, positive values
// stay literal, and the copy runs once.
func TestMigrateRollNull(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, err := st.SaveCamera(ctx, "", CameraInput{Name: "A", RTSPURL: "rtsp://10.0.0.1/s", Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.SaveCamera(ctx, "", CameraInput{Name: "B", RTSPURL: "rtsp://10.0.0.2/s", Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-WAVE24 database: old columns filled, flag absent.
	st.DB.Exec(`UPDATE cameras SET pre_roll_seconds = 0, post_roll_seconds = 0, pre_roll_s = 7, post_roll_s = 7 WHERE id = ?`, a.ID)
	st.DB.Exec(`UPDATE cameras SET pre_roll_seconds = 10, post_roll_seconds = 30, pre_roll_s = NULL, post_roll_s = NULL WHERE id = ?`, b.ID)
	st.DB.Exec(`DELETE FROM settings WHERE key = 'migrated.roll_null'`)
	if err := migrateRollNull(st.DB); err != nil {
		t.Fatal(err)
	}
	a, _ = st.Camera(ctx, a.ID)
	b, _ = st.Camera(ctx, b.ID)
	if a.PreRollSeconds != nil || a.PostRollSeconds != nil {
		t.Fatalf("0 should become NULL: %v %v", a.PreRollSeconds, a.PostRollSeconds)
	}
	if b.PreRollSeconds == nil || *b.PreRollSeconds != 10 || b.PostRollSeconds == nil || *b.PostRollSeconds != 30 {
		t.Fatalf("values should stay: %v %v", b.PreRollSeconds, b.PostRollSeconds)
	}
	// A camera set literally to 0 is not touched by a second run.
	in := CameraInput{Name: "A", RTSPURL: "rtsp://10.0.0.1/s", Recording: true, PreRollSeconds: IntPtr(0)}
	if _, err := st.SaveCamera(ctx, a.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := migrateRollNull(st.DB); err != nil {
		t.Fatal(err)
	}
	a, _ = st.Camera(ctx, a.ID)
	if a.PreRollSeconds == nil || *a.PreRollSeconds != 0 {
		t.Fatalf("literal 0 lost: %v", a.PreRollSeconds)
	}
	// And null sets it back to the default.
	in.PreRollSeconds = OptInt{Set: true}
	if _, err := st.SaveCamera(ctx, a.ID, in); err != nil {
		t.Fatal(err)
	}
	a, _ = st.Camera(ctx, a.ID)
	if a.PreRollSeconds != nil {
		t.Fatalf("null should reset: %v", a.PreRollSeconds)
	}
}
