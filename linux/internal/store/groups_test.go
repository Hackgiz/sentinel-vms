package store

import (
	"context"
	"testing"
	"time"
)

func TestGroupsScopesAndPlacement(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, _ := st.SaveCamera(ctx, "", CameraInput{Name: "A", RTSPURL: "rtsp://10.0.0.1/s"})
	b, _ := st.SaveCamera(ctx, "", CameraInput{Name: "B", RTSPURL: "rtsp://10.0.0.2/s"})
	if a.RecorderID != LocalRecorderID {
		t.Fatalf("new camera on %q", a.RecorderID)
	}
	g, err := st.SaveCameraGroup(ctx, "", "Warehouse", []string{a.ID})
	if err != nil || len(g.CameraIDs) != 1 {
		t.Fatalf("group: %v %v", g, err)
	}
	if _, err := st.SaveCameraGroup(ctx, "", "warehouse", nil); err != ErrDuplicateName {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := st.SaveCameraGroup(ctx, "", "Bad", []string{"nope"}); err == nil {
		t.Fatal("unknown camera accepted")
	}
	op, _ := st.CreateUser(ctx, "op", "Operator", "x")
	if ids, all, _ := st.VisibleCameraIDs(ctx, op); !all || ids != nil {
		t.Fatal("new users see every camera")
	}
	if err := st.SetUserScope(ctx, op.ID, UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	if ids, all, _ := st.VisibleCameraIDs(ctx, op); all || !ids[a.ID] || ids[b.ID] {
		t.Fatalf("scoped: %v %v", ids, all)
	}
	_ = st.DeleteCameraGroup(ctx, g.ID)
	if ids, all, _ := st.VisibleCameraIDs(ctx, op); all || len(ids) != 0 {
		t.Fatalf("after group delete: %v %v", ids, all)
	}

	// Moving a camera keeps its history.
	if err := st.AddRecordingServer(ctx, &RecordingServer{ID: "rs2", Name: "Back room"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCameraRecorder(ctx, a.ID, "rs2"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.CameraPlacements(ctx, a.ID)
	if len(p) != 2 || p[0].RecorderID != LocalRecorderID || p[0].To == 0 || p[1].RecorderID != "rs2" || p[1].To != 0 {
		t.Fatalf("placements %+v", p)
	}
	if c, _ := st.Camera(ctx, a.ID); c.RecorderID != "rs2" {
		t.Fatalf("camera on %q", c.RecorderID)
	}

	// Enrollment tokens are single-use.
	secret, _, err := st.CreateEnrollmentToken(ctx, "Garage", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if tk, err := st.ConsumeEnrollmentToken(ctx, secret, "rs3"); err != nil || tk.Name != "Garage" {
		t.Fatalf("consume: %v %v", tk, err)
	}
	if _, err := st.ConsumeEnrollmentToken(ctx, secret, "rs4"); err != ErrNotFound {
		t.Fatalf("reuse: %v", err)
	}
	if err := st.RevokeRecordingServer(ctx, LocalRecorderID); err == nil {
		t.Fatal("revoked the local server")
	}
}
